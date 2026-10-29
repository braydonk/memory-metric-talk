//go:build linux

package manager

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/bytefmt"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/cgroup"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/config"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/elfmap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/pagecache"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/pagemap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/procstat"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/workload"
)

const (
	EnvBackendEndpoint       = "OTEL_LAB_BACKEND_ENDPOINT"
	EnvCollectorGRPCEndpoint = "OTEL_LAB_COLLECTOR_GRPC_ENDPOINT"
)

// Options holds CLI flags passed to cmd/manager.
type Options struct {
	ConfigPath          string
	Duration            time.Duration
	ResultsDir          string
	MemoryLimitOverride string
	AllowWarmCache      bool
	CgroupRoot          string
}

// Run executes a single memory eviction lab run from start to finish and cleans up all kernel resources.
func Run(ctx context.Context, opts Options, logger *log.Logger) (retErr error) {
	if logger == nil {
		logger = log.Default()
	}
	if os.Geteuid() != 0 {
		return errors.New("manager must be run as root (e.g. via sudo) to create cgroups and read /proc/[pid]/pagemap PFNs")
	}
	if opts.Duration <= 0 {
		return errors.New("--duration must be > 0")
	}
	if opts.ResultsDir == "" {
		return errors.New("--results-dir is required")
	}

	cfg, err := config.Load(opts.ConfigPath, opts.MemoryLimitOverride)
	if err != nil {
		return err
	}

	// Pre-flight validation: ELF binary structure, disk-backed filesystems, SHA-256, and cold page cache.
	elfLayout, err := elfmap.Parse(cfg.CollectorBinary)
	if err != nil {
		return err
	}
	binID, err := pagecache.StatFileID(cfg.CollectorBinary)
	if err != nil {
		return err
	}
	binSHA256, err := fileSHA256(binID.RealPath)
	if err != nil {
		return fmt.Errorf("sha256 %q: %w", binID.RealPath, err)
	}

	warmBefore, warmAfter, err := pagecache.EvictAndVerify(binID.RealPath, opts.AllowWarmCache)
	if err != nil {
		return err
	}
	if _, _, err := pagecache.EvictAndVerify(cfg.CollectorConfig, true); err != nil {
		return err
	}
	logger.Printf("prepared collector binary %s (%s, evicted %d -> %d warm page-cache pages)",
		binID.RealPath, bytefmt.FormatIEC(binID.Size), warmBefore, warmAfter)

	// Open results writer & collector log file; ensure ownership fixup runs on all exit paths.
	resWriter, err := results.NewWriter(opts.ResultsDir)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := resWriter.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
		if _, chownErr := results.ChownToSudoUser(opts.ResultsDir); chownErr != nil {
			logger.Printf("warning: failed to chown results dir to SUDO_UID:SUDO_GID: %v", chownErr)
		}
	}()

	colLogFile, err := os.OpenFile(filepath.Join(opts.ResultsDir, results.CollectorLogName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", results.CollectorLogName, err)
	}
	defer colLogFile.Close()

	// Create leaf cgroup with memory.swap.max and memory.max; guarantee full cgroup removal on exit.
	cg, err := cgroup.Create(opts.CgroupRoot, cfg.Name, cfg.ParsedMemoryLimit(), cfg.ParsedSwapMax())
	if err != nil {
		return err
	}
	var cgClosed bool
	defer func() {
		if !cgClosed {
			if cerr := cg.Close(); cerr != nil {
				logger.Printf("warning: cgroup cleanup error: %v", cerr)
				if retErr == nil {
					retErr = cerr
				}
			}
		}
	}()
	logger.Printf("created cgroup %s (memory.max=%s, memory.swap.max=%s)",
		cg.Path(), cfg.ParsedMemoryLimit().Human(), cfg.ParsedSwapMax().String())

	eventCh, err := cg.StartWatcher()
	if err != nil {
		return fmt.Errorf("start memory.events watcher: %w", err)
	}

	var backendSrv *workload.Backend
	if cfg.Backend.Enabled {
		backendSrv = workload.NewBackend(cfg.Backend.GRPCEndpoint)
		if err := backendSrv.Start(); err != nil {
			return err
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			backendSrv.Stop(stopCtx)
		}()
		logger.Printf("started OTLP discard backend on %s", backendSrv.Addr())
	}

	cmdArgs := make([]string, 0, 1+len(cfg.CollectorArgs))
	cmdArgs = append(cmdArgs, "--config="+cfg.CollectorConfig)
	cmdArgs = append(cmdArgs, cfg.CollectorArgs...)

	cmd := exec.Command(binID.RealPath, cmdArgs...)
	cmd.Stdout = colLogFile
	cmd.Stderr = colLogFile
	cmd.Env = buildCollectorEnv(cfg, backendSrv)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		UseCgroupFD: true,
		CgroupFD:    cg.FD(),
	}

	startTime := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start collector: %w", err)
	}
	pid := cmd.Process.Pid
	logger.Printf("started collector pid=%d inside cgroup", pid)

	exitedCh := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exitedCh)
	}()

	pageSize := uint64(os.Getpagesize())
	var (
		samples             []results.SampleRow
		reclaimEventSamples int
		summaryAcc          summaryAccumulator
	)

	recordCgroupOnlySample := func(trig string) {
		now := time.Now().UTC()
		row := results.SampleRow{
			TimestampUnixMs: now.UnixMilli(),
			ElapsedMs:       now.Sub(startTime).Milliseconds(),
			Trigger:         trig,
		}
		if snap, serr := cg.ReadSnapshot(); serr == nil {
			fillCgroupFields(&row, snap)
		}
		summaryAcc.observe(row, pageSize)
		samples = append(samples, row)
		_ = resWriter.WriteSample(row)
	}

	// Discover binary VMAs from /proc/[pid]/maps and open /proc/[pid]/pagemap.
	// If memory.max is tiny, the Collector may have already OOM-killed inside execve/runtime init.
	var tracker *pagemap.Tracker
	vmas, mapsErr := pagemap.ReadBinaryVMAs(pid, binID.DevMajor, binID.DevMinor, binID.Ino)
	if mapsErr == nil {
		pmSrc, pmErr := pagemap.OpenProcPagemap(pid, pageSize)
		if pmErr == nil {
			tracker, mapsErr = pagemap.NewTracker(vmas, elfLayout, pageSize, cfg.BlockSizePages, pmSrc)
			if mapsErr != nil {
				_ = pmSrc.Close()
			}
		} else {
			mapsErr = pmErr
		}
	}
	if tracker != nil {
		defer tracker.Close()
	}

	takeSample := func(trig string) bool {
		if tracker == nil {
			return false
		}
		now := time.Now().UTC()
		row := results.SampleRow{
			TimestampUnixMs: now.UnixMilli(),
			ElapsedMs:       now.Sub(startTime).Milliseconds(),
			Trigger:         trig,
		}
		if err := tracker.Sample(&row); err != nil {
			// Process likely exited during the scan; preserve previous live snapshot state.
			return false
		}
		if snap, err := cg.ReadSnapshot(); err == nil {
			fillCgroupFields(&row, snap)
		}
		if pst, err := procstat.ReadPID(pid); err == nil {
			row.ProcRSSBytes = pst.VmRSSBytes
			row.ProcRSSAnonBytes = pst.RssAnonBytes
			row.ProcRSSFileBytes = pst.RssFileBytes
			row.ProcRSSShmemBytes = pst.RssShmemBytes
			row.ProcHWMBytes = pst.VmHWMBytes
			row.ProcSwapBytes = pst.VmSwapBytes
		}
		if trig == results.TriggerReclaimEvent || trig == results.TriggerReclaimDebounced {
			reclaimEventSamples++
		}
		summaryAcc.observe(row, pageSize)
		samples = append(samples, row)
		if err := resWriter.WriteSample(row); err != nil {
			logger.Printf("warning: write sample failed: %v", err)
		}
		return true
	}

	var (
		startupErr         error
		startupReadinessMs int64
		gen                *workload.Generator
	)

	if mapsErr != nil {
		// Check whether the child already died (e.g. instant OOM during startup).
		select {
		case <-exitedCh:
			recordCgroupOnlySample(results.TriggerStart)
		default:
			_ = cmd.Process.Kill()
			<-exitedCh
			return fmt.Errorf("initialize binary pagemap tracker for pid %d: %w", pid, mapsErr)
		}
	} else {
		takeSample(results.TriggerStart)

		readyCtx, cancelReady := context.WithCancel(ctx)
		defer cancelReady()

		var readyCh chan error
		var durationTimer *time.Timer
		var durationCh <-chan time.Time

		if cfg.Workload.Enabled {
			readyCh = make(chan error, 1)
			go func() {
				readyCh <- workload.WaitTCPReady(readyCtx, cfg.Workload.CollectorGRPCEndpoint, cfg.StartupTimeout)
			}()
		} else {
			durationTimer = time.NewTimer(opts.Duration)
			durationCh = durationTimer.C
		}
		defer func() {
			if durationTimer != nil {
				durationTimer.Stop()
			}
		}()

		ticker := time.NewTicker(cfg.SampleInterval)
		defer ticker.Stop()

		var (
			lastEventSample time.Time
			debounceTimer   *time.Timer
			debounceCh      <-chan time.Time
			debounceArmed   bool
		)
		defer func() {
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
		}()

	loop:
		for {
			select {
			case <-ctx.Done():
				logger.Printf("interrupted (%v); stopping run cleanly", ctx.Err())
				break loop

			case <-exitedCh:
				cancelReady()
				logger.Printf("collector exited before duration elapsed")
				break loop

			case rerr := <-readyCh:
				readyCh = nil
				if rerr != nil {
					startupErr = rerr
					logger.Printf("collector readiness failed: %v", rerr)
					break loop
				}
				startupReadinessMs = time.Since(startTime).Milliseconds()
				gen = workload.NewGenerator(cfg.Workload)
				if gerr := gen.Start(ctx); gerr != nil {
					startupErr = gerr
					break loop
				}
				logger.Printf("collector ready in %dms; started OTLP workload for %s", startupReadinessMs, opts.Duration)
				durationTimer = time.NewTimer(opts.Duration)
				durationCh = durationTimer.C

			case <-durationCh:
				break loop

			case <-eventCh:
				now := time.Now()
				since := now.Sub(lastEventSample)
				if cfg.EventDebounce <= 0 || since >= cfg.EventDebounce {
					if debounceTimer != nil {
						debounceTimer.Stop()
						debounceCh = nil
						debounceArmed = false
					}
					lastEventSample = now
					takeSample(results.TriggerReclaimEvent)
				} else if !debounceArmed {
					wait := cfg.EventDebounce - since
					if debounceTimer == nil {
						debounceTimer = time.NewTimer(wait)
					} else {
						debounceTimer.Reset(wait)
					}
					debounceCh = debounceTimer.C
					debounceArmed = true
				}

			case <-debounceCh:
				debounceCh = nil
				debounceArmed = false
				lastEventSample = time.Now()
				takeSample(results.TriggerReclaimDebounced)

			case <-ticker.C:
				takeSample(results.TriggerPeriodic)
			}
		}
	}

	if gen != nil {
		gen.Stop()
	}

	// Capture final live snapshot if Collector is still running.
	collectorExitedEarly := false
	select {
	case <-exitedCh:
		collectorExitedEarly = true
	default:
		takeSample(results.TriggerFinal)
	}

	if tracker != nil {
		if err := resWriter.WriteSections(tracker.SectionRows()); err != nil {
			return err
		}
		if err := resWriter.WriteBlocks(tracker.BlockRows()); err != nil {
			return err
		}
	}

	// Stop the Collector cleanly if it is still running.
	if !collectorExitedEarly {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		shutTimer := time.NewTimer(cfg.ShutdownTimeout)
		select {
		case <-exitedCh:
			shutTimer.Stop()
		case <-shutTimer.C:
			logger.Printf("collector did not exit within %s; killing cgroup", cfg.ShutdownTimeout)
			cg.KillAll()
			<-exitedCh
		}
	}

	endTime := time.Now().UTC()

	// Read final cgroup snapshot BEFORE closing the cgroup so any terminal OOM counter increments are recorded.
	finalCG, finalCGErr := cg.ReadSnapshot()
	if collectorExitedEarly && finalCGErr == nil {
		// Record a closing snapshot carrying final OOM event counts if the process died mid-run.
		if tracker == nil || len(samples) == 0 || samples[len(samples)-1].CGEventsOOMKill < finalCG.Events.OOMKill {
			recordCgroupOnlySample(results.TriggerFinal)
		}
	}

	exitCode, exitSignal := decodeWaitStatus(cmd.ProcessState, waitErr)
	oomKilled := finalCGErr == nil && (finalCG.Events.OOMKill > 0 || finalCG.Events.OOMGroupKill > 0)
	if !oomKilled && len(samples) > 0 && samples[len(samples)-1].CGEventsOOMKill > 0 {
		oomKilled = true
	}

	var wlSum results.WorkloadSummary
	wlSum.BackendEnabled = cfg.Backend.Enabled
	wlSum.WorkloadEnabled = cfg.Workload.Enabled
	wlSum.StartupReadinessMs = startupReadinessMs
	if gen != nil {
		wlSum.GeneratorBatchesSent, wlSum.GeneratorItemsSent, wlSum.GeneratorErrors = gen.Totals()
	}
	if backendSrv != nil {
		wlSum.BackendRequests, wlSum.BackendItemsReceived = backendSrv.Totals()
	}

	finalSum := summaryAcc.finalize(samples, finalCG, finalCGErr == nil, pageSize, oomKilled, collectorExitedEarly, exitCode, startupErr)

	memLim := cfg.ParsedMemoryLimit()
	meta := results.RunMeta{
		Name:                     cfg.Name,
		StartTime:                startTime,
		EndTime:                  endTime,
		DurationMs:               endTime.Sub(startTime).Milliseconds(),
		ConfiguredDuration:       opts.Duration.String(),
		CollectorBinary:          binID.RealPath,
		CollectorBinarySizeBytes: binID.Size,
		CollectorBinarySHA256:    binSHA256,
		CollectorConfig:          cfg.CollectorConfig,
		MemoryLimitRaw:           cfg.MemoryLimit,
		MemoryLimitHuman:         memLim.Human(),
		MemoryLimitUnlimited:     memLim.Unlimited,
		MemoryLimitBytes:         memLim.Bytes,
		SwapMaxRaw:               cfg.SwapMax,
		PageSizeBytes:            pageSize,
		BlockSizePages:           cfg.BlockSizePages,
		SampleCount:              len(samples),
		ReclaimEventSamples:      reclaimEventSamples,
		WarmCachePagesBefore:     warmBefore,
		WarmCachePagesAfter:      warmAfter,
		ExitCode:                 exitCode,
		ExitSignal:               exitSignal,
		OOMKilled:                oomKilled,
		Workload:                 wlSum,
		Summary:                  finalSum,
	}
	if tracker != nil {
		meta.TotalBinaryPages = tracker.TotalPages()
		meta.TotalBinaryMappedBytes = tracker.TotalMappedBytes()
	}

	if err := resWriter.WriteMeta(meta); err != nil {
		return err
	}

	// Clean up cgroup synchronously before returning and log completion.
	cgClosed = true
	if err := cg.Close(); err != nil {
		return fmt.Errorf("cleanup cgroup %q: %w", cg.Path(), err)
	}
	logger.Printf("cleaned up cgroup %s", cg.Path())
	logger.Printf("verdict=%s (%s) — results written to %s", finalSum.Verdict, finalSum.VerdictReason, opts.ResultsDir)

	if startupErr != nil && !oomKilled {
		return startupErr
	}
	return nil
}

func fillCgroupFields(row *results.SampleRow, snap cgroup.Snapshot) {
	row.CGMemoryCurrentBytes = snap.CurrentBytes
	row.CGAnonBytes = snap.Stat.AnonBytes
	row.CGFileBytes = snap.Stat.FileBytes
	row.CGActiveFileBytes = snap.Stat.ActiveFileBytes
	row.CGInactiveFileBytes = snap.Stat.InactiveFileBytes
	row.CGKernelBytes = snap.Stat.KernelBytes
	row.CGPgfault = snap.Stat.Pgfault
	row.CGPgmajfault = snap.Stat.Pgmajfault
	row.CGWorkingsetRefaultFile = snap.Stat.WorkingsetRefaultFile
	row.CGWorkingsetActivateFile = snap.Stat.WorkingsetActivateFile
	row.CGEventsLow = snap.Events.Low
	row.CGEventsHigh = snap.Events.High
	row.CGEventsMax = snap.Events.Max
	row.CGEventsOOM = snap.Events.OOM
	row.CGEventsOOMKill = snap.Events.OOMKill
	row.CGPSISomeTotalUs = snap.Pressure.SomeTotalUs
	row.CGPSIFullTotalUs = snap.Pressure.FullTotalUs
}

type summaryAccumulator struct {
	peakBinResidentBytes uint64
	peakCGMemoryBytes    uint64
	peakCGAnonBytes      uint64
	peakCGFileBytes      uint64
	peakProcRSSBytes     uint64
	lastLiveSample       results.SampleRow
	hasLiveSample        bool
}

func (a *summaryAccumulator) observe(r results.SampleRow, pageSize uint64) {
	binRes := (r.BinPresentFilePages + r.BinPresentCoWPages) * pageSize
	if binRes > a.peakBinResidentBytes {
		a.peakBinResidentBytes = binRes
	}
	if r.CGMemoryCurrentBytes > a.peakCGMemoryBytes {
		a.peakCGMemoryBytes = r.CGMemoryCurrentBytes
	}
	if r.CGAnonBytes > a.peakCGAnonBytes {
		a.peakCGAnonBytes = r.CGAnonBytes
	}
	if r.CGFileBytes > a.peakCGFileBytes {
		a.peakCGFileBytes = r.CGFileBytes
	}
	if r.ProcRSSBytes > a.peakProcRSSBytes {
		a.peakProcRSSBytes = r.ProcRSSBytes
	}
	if r.BinTotalPages > 0 {
		a.lastLiveSample = r
		a.hasLiveSample = true
	}
}

func (a *summaryAccumulator) finalize(
	samples []results.SampleRow,
	finalCG cgroup.Snapshot,
	hasFinalCG bool,
	pageSize uint64,
	oomKilled bool,
	exitedEarly bool,
	exitCode int,
	startupErr error,
) results.FinalSummary {
	s := results.FinalSummary{
		PeakBinResidentBytes: a.peakBinResidentBytes,
		PeakCGMemoryBytes:    a.peakCGMemoryBytes,
		PeakCGAnonBytes:      a.peakCGAnonBytes,
		PeakCGFileBytes:      a.peakCGFileBytes,
		PeakProcRSSBytes:     a.peakProcRSSBytes,
	}

	if a.hasLiveSample {
		last := a.lastLiveSample
		s.FinalBinResidentBytes = (last.BinPresentFilePages + last.BinPresentCoWPages) * pageSize
		s.EverFaultedBinBytes = last.BinEverFaultedPages * pageSize
		s.CleanlyEvictedBinBytes = last.BinCleanlyEvictedPages * pageSize
		if last.BinEverFaultedPages > 0 {
			s.CleanReclaimPercentOfFaulted = 100.0 * float64(last.BinCleanlyEvictedPages) / float64(last.BinEverFaultedPages)
		}
		s.TotalColdFaults = last.CumColdFaults
		s.TotalEvictions = last.CumEvictions
		s.TotalRefaults01 = last.CumRefaults01
		s.TotalRefaultsPFN = last.CumRefaultsPFN
		s.TotalRefaults = last.CumRefaultsTotal
		s.EverRefaultedPages = last.BinEverRefaultedPages
		s.ThrashingPages = last.BinThrashingPages
		s.TotalCGEventsMax = last.CGEventsMax
		s.TotalCGPgmajfault = last.CGPgmajfault
		s.TotalCGWorkingsetRefaultFile = last.CGWorkingsetRefaultFile
		s.TotalPSISomeMs = float64(last.CGPSISomeTotalUs) / 1000.0
		s.TotalPSIFullMs = float64(last.CGPSIFullTotalUs) / 1000.0
	}

	if hasFinalCG {
		s.TotalCGEventsMax = max(s.TotalCGEventsMax, finalCG.Events.Max)
		s.TotalCGPgmajfault = max(s.TotalCGPgmajfault, finalCG.Stat.Pgmajfault)
		s.TotalCGWorkingsetRefaultFile = max(s.TotalCGWorkingsetRefaultFile, finalCG.Stat.WorkingsetRefaultFile)
		s.TotalPSISomeMs = max(s.TotalPSISomeMs, float64(finalCG.Pressure.SomeTotalUs)/1000.0)
		s.TotalPSIFullMs = max(s.TotalPSIFullMs, float64(finalCG.Pressure.FullTotalUs)/1000.0)
	}
	_ = samples

	s.Verdict, s.VerdictReason = classifyVerdict(s, a.lastLiveSample.BinEverFaultedPages, oomKilled, exitedEarly, exitCode, startupErr)
	return s
}

func classifyVerdict(
	s results.FinalSummary,
	everFaultedPages uint64,
	oomKilled bool,
	exitedEarly bool,
	exitCode int,
	startupErr error,
) (string, string) {
	if oomKilled {
		return results.VerdictOOMKilled, fmt.Sprintf(
			"cgroup OOM killer terminated Collector after %d evictions and %d refaults (peak anon %s)",
			s.TotalEvictions, s.TotalRefaults, bytefmt.FormatIEC(s.PeakCGAnonBytes),
		)
	}
	if startupErr != nil {
		return results.VerdictProcessError, startupErr.Error()
	}
	if exitedEarly && exitCode != 0 {
		return results.VerdictProcessError, fmt.Sprintf("Collector exited early with status %d", exitCode)
	}
	if s.TotalEvictions == 0 && s.TotalCGEventsMax == 0 {
		return results.VerdictNoReclaim, fmt.Sprintf(
			"memory stayed under limit (peak cgroup usage %s); no binary pages were evicted",
			bytefmt.FormatIEC(s.PeakCGMemoryBytes),
		)
	}

	// Detect sustained page thrashing when active code/rodata pages repeatedly fault back in
	// or cause meaningful PSI full memory stalls.
	var refaultRatio float64
	if everFaultedPages > 0 {
		refaultRatio = float64(s.TotalRefaults) / float64(everFaultedPages)
	}
	if (s.ThrashingPages > 0 && (s.TotalRefaults >= 64 || refaultRatio >= 0.05)) || s.TotalPSIFullMs >= 100.0 {
		return results.VerdictThrashing, fmt.Sprintf(
			"page thrashing detected: %d refaults across %d binary pages (%d pages refaulted >=2x), %.1f ms PSI full stall",
			s.TotalRefaults, s.EverRefaultedPages, s.ThrashingPages, s.TotalPSIFullMs,
		)
	}

	return results.VerdictCleanReclaim, fmt.Sprintf(
		"cleanly evicted %s (%.1f%% of faulted binary pages) down to %s resident with %d refaults and %.1f ms PSI full stall",
		bytefmt.FormatIEC(s.CleanlyEvictedBinBytes),
		s.CleanReclaimPercentOfFaulted,
		bytefmt.FormatIEC(s.FinalBinResidentBytes),
		s.TotalRefaults,
		s.TotalPSIFullMs,
	)
}

func buildCollectorEnv(cfg *config.Config, b *workload.Backend) []string {
	env := os.Environ()
	if b != nil {
		env = append(env, EnvBackendEndpoint+"="+b.Addr())
	}
	if cfg.Workload.Enabled {
		env = append(env, EnvCollectorGRPCEndpoint+"="+cfg.Workload.CollectorGRPCEndpoint)
	}
	keys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, cmp.Compare)
	for _, k := range keys {
		env = append(env, k+"="+cfg.Env[k])
	}
	return env
}

func decodeWaitStatus(ps *os.ProcessState, waitErr error) (int, string) {
	if ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return -1, ws.Signal().String()
			}
			return ws.ExitStatus(), ""
		}
		return ps.ExitCode(), ""
	}
	if waitErr != nil {
		return -1, waitErr.Error()
	}
	return 0, ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
