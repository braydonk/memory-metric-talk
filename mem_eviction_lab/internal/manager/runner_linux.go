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
	"sync"
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

	WebDefaultSampleInterval = 100 * time.Millisecond
	collectorLogTailMaxBytes = 64 * 1024
)

// RunLayout describes the mapped Collector binary page geometry captured once the process starts.
type RunLayout struct {
	PID                    int                   `json:"pid"`
	PageSizeBytes          uint64                `json:"page_size_bytes"`
	BlockSizePages         int                   `json:"block_size_pages"`
	TotalBinaryPages       uint64                `json:"total_binary_pages"`
	TotalBinaryMappedBytes uint64                `json:"total_binary_mapped_bytes"`
	WarmCachePagesBefore   int                   `json:"warm_cache_pages_before"`
	WarmCachePagesAfter    int                   `json:"warm_cache_pages_after"`
	Spans                  []pagemap.SectionSpan `json:"spans"`
}

// LiveController exposes thread-safe live cgroup controls while a benchmark run is active.
type LiveController interface {
	SetMemoryMax(limit bytefmt.Limit) error
	Reclaim(bytes uint64) (reclaimed uint64, partial bool, err error)
}

type manualSampleReq struct {
	trigger string
	done    chan struct{}
}

type liveController struct {
	inner     LiveController
	triggerCh chan<- manualSampleReq
	loopDone  <-chan struct{}
}

func (c *liveController) SetMemoryMax(limit bytefmt.Limit) error {
	if err := c.inner.SetMemoryMax(limit); err != nil {
		return err
	}
	c.requestSample(results.TriggerLimitChange)
	return nil
}

func (c *liveController) Reclaim(bytes uint64) (reclaimed uint64, partial bool, err error) {
	reclaimed, partial, err = c.inner.Reclaim(bytes)
	if err != nil {
		return reclaimed, partial, err
	}
	c.requestSample(results.TriggerManualReclaim)
	return reclaimed, partial, nil
}

func (c *liveController) requestSample(trigger string) {
	if c.triggerCh == nil {
		return
	}
	req := manualSampleReq{
		trigger: trigger,
		done:    make(chan struct{}),
	}
	select {
	case c.triggerCh <- req:
	case <-c.loopDone:
		return
	}
	select {
	case <-req.done:
	case <-c.loopDone:
	}
}

// Hooks receives real-time streaming callbacks during manager.Run (used by -web mode).
type Hooks struct {
	OnCgroupReady func(ctrl LiveController)
	OnLayout      func(layout RunLayout)
	OnReady       func(elapsedMs int64)
	OnSample      func(row results.SampleRow, deltas []uint32)
	OnDone        func(meta results.RunMeta, sections []results.SectionRow, collectorLogTail string)
}

// Options holds CLI flags and runtime options passed to manager.Run.
type Options struct {
	ConfigPath                string
	CollectorBinariesOverride []string
	Duration                  time.Duration
	ResultsDir                string
	MemoryLimitOverride       string
	SwapMaxOverride           string
	AllowWarmCache            bool
	CgroupRoot                string
	WebPort                   int
	WebMode                   bool
	StopCh                    <-chan struct{}
	Hooks                     *Hooks
}

func validateOptions(opts Options) error {
	if opts.ConfigPath == "" {
		return errors.New("--config is required")
	}
	if opts.Duration <= 0 {
		return errors.New("--duration must be > 0")
	}
	if !opts.WebMode && opts.ResultsDir == "" {
		return errors.New("--results-dir is required unless -web mode is enabled")
	}
	return nil
}

func effectiveSampleInterval(cfgInterval time.Duration, webMode bool) time.Duration {
	if webMode && cfgInterval == config.DefaultSampleInterval {
		return WebDefaultSampleInterval
	}
	return cfgInterval
}

// Run executes a single memory eviction lab run from start to finish and cleans up all kernel resources.
func Run(ctx context.Context, opts Options, logger *log.Logger) (retErr error) {
	if logger == nil {
		logger = log.Default()
	}
	if err := validateOptions(opts); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("manager must be run as root (e.g. via sudo) to create cgroups and read /proc/[pid]/pagemap PFNs")
	}

	cfg, err := config.LoadWithBinaryOverrides(opts.ConfigPath, opts.MemoryLimitOverride, opts.SwapMaxOverride, opts.CollectorBinariesOverride)
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

	// Optionally open results writer & collector log file when ResultsDir is configured;
	// always keep a bounded tail buffer for web UI diagnostics.
	logTail := newTailBuffer(collectorLogTailMaxBytes)
	var colOut io.Writer = logTail
	var resWriter *results.Writer
	if opts.ResultsDir != "" {
		resWriter, err = results.NewWriter(opts.ResultsDir)
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

		colLogFile, ferr := os.OpenFile(filepath.Join(opts.ResultsDir, results.CollectorLogName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if ferr != nil {
			return fmt.Errorf("create %s: %w", results.CollectorLogName, ferr)
		}
		defer colLogFile.Close()
		colOut = io.MultiWriter(colLogFile, logTail)
	}

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
	manualSampleCh := make(chan manualSampleReq, 8)
	loopDone := make(chan struct{})
	var loopDoneOnce sync.Once
	closeLoopDone := func() {
		loopDoneOnce.Do(func() { close(loopDone) })
	}
	defer closeLoopDone()
	if opts.Hooks != nil && opts.Hooks.OnCgroupReady != nil {
		opts.Hooks.OnCgroupReady(&liveController{
			inner:     cg,
			triggerCh: manualSampleCh,
			loopDone:  loopDone,
		})
	}

	eventCh, err := cg.StartWatcher()
	if err != nil {
		return fmt.Errorf("start memory.events watcher: %w", err)
	}

	var backendSrv *workload.Backend
	var backendStopped bool
	stopBackend := func() {
		if backendSrv == nil || backendStopped {
			return
		}
		backendStopped = true
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		backendSrv.Stop(stopCtx)
	}
	if cfg.Backend.Enabled {
		backendSrv = workload.NewBackend(cfg.Backend.GRPCEndpoint)
		if err := backendSrv.Start(); err != nil {
			return err
		}
		defer stopBackend()
		logger.Printf("started OTLP discard backend on %s", backendSrv.Addr())
	}

	cmdArgs := make([]string, 0, 1+len(cfg.CollectorArgs))
	cmdArgs = append(cmdArgs, "--config="+cfg.CollectorConfig)
	cmdArgs = append(cmdArgs, cfg.CollectorArgs...)

	cmd := exec.Command(binID.RealPath, cmdArgs...)
	cmd.Stdout = colOut
	cmd.Stderr = colOut
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
		deltaBuf            []uint32
		wantSampleHook      = opts.Hooks != nil && opts.Hooks.OnSample != nil
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
		row.PopulateWorkingSet()
		summaryAcc.observe(row, pageSize)
		samples = append(samples, row)
		if resWriter != nil {
			_ = resWriter.WriteSample(row)
		}
		if wantSampleHook {
			opts.Hooks.OnSample(row, nil)
		}
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
		if opts.Hooks != nil && opts.Hooks.OnLayout != nil {
			opts.Hooks.OnLayout(RunLayout{
				PID:                    pid,
				PageSizeBytes:          pageSize,
				BlockSizePages:         cfg.BlockSizePages,
				TotalBinaryPages:       tracker.TotalPages(),
				TotalBinaryMappedBytes: tracker.TotalMappedBytes(),
				WarmCachePagesBefore:   warmBefore,
				WarmCachePagesAfter:    warmAfter,
				Spans:                  tracker.LayoutSpans(),
			})
		}
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
		var serr error
		if wantSampleHook {
			deltaBuf, serr = tracker.SampleWithDeltas(&row, deltaBuf)
		} else {
			serr = tracker.Sample(&row)
		}
		if serr != nil {
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
		row.PopulateWorkingSet()
		if trig == results.TriggerReclaimEvent || trig == results.TriggerReclaimDebounced || trig == results.TriggerManualReclaim {
			reclaimEventSamples++
		}
		summaryAcc.observe(row, pageSize)
		samples = append(samples, row)
		if resWriter != nil {
			if err := resWriter.WriteSample(row); err != nil {
				logger.Printf("warning: write sample failed: %v", err)
			}
		}
		if wantSampleHook {
			opts.Hooks.OnSample(row, deltaBuf)
		}
		return true
	}

	var (
		startupErr         error
		startupReadinessMs int64
		stoppedByUser      bool
		gen                *workload.Generator
	)

	if mapsErr != nil {
		closeLoopDone()
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

		sampleInterval := effectiveSampleInterval(cfg.SampleInterval, opts.WebMode)
		ticker := time.NewTicker(sampleInterval)
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
				cancelReady()
				logger.Printf("interrupted (%v); stopping run cleanly", ctx.Err())
				break loop

			case <-opts.StopCh:
				stoppedByUser = true
				cancelReady()
				logger.Printf("stop requested via web UI; stopping run cleanly")
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
				if opts.Hooks != nil && opts.Hooks.OnReady != nil {
					opts.Hooks.OnReady(startupReadinessMs)
				}
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

			case req := <-manualSampleCh:
				takeSample(req.trigger)
				close(req.done)

			case <-ticker.C:
				takeSample(results.TriggerPeriodic)
			}
		}
		closeLoopDone()
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

	var sectionRows []results.SectionRow
	if tracker != nil {
		sectionRows = tracker.SectionRows()
		if resWriter != nil {
			if err := resWriter.WriteSections(sectionRows); err != nil {
				return err
			}
			if err := resWriter.WriteBlocks(tracker.BlockRows()); err != nil {
				return err
			}
			if err := resWriter.WritePages(tracker.PageRows()); err != nil {
				return err
			}
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

	finalSum := summaryAcc.finalize(samples, startupReadinessMs, finalCG, finalCGErr == nil, pageSize, oomKilled, collectorExitedEarly, exitCode, startupErr, stoppedByUser)

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

	if resWriter != nil {
		if err := resWriter.WriteMeta(meta); err != nil {
			return err
		}
	}

	// Clean up backend listener and cgroup synchronously BEFORE firing OnDone so a subsequent
	// back-to-back web run can rebind loopback ports and cgroups immediately.
	stopBackend()
	cgClosed = true
	if err := cg.Close(); err != nil {
		return fmt.Errorf("cleanup cgroup %q: %w", cg.Path(), err)
	}
	logger.Printf("cleaned up cgroup %s", cg.Path())
	if opts.ResultsDir != "" {
		logger.Printf("verdict=%s (%s) — results written to %s", finalSum.Verdict, finalSum.VerdictReason, opts.ResultsDir)
	} else {
		logger.Printf("verdict=%s (%s) — in-memory web run complete", finalSum.Verdict, finalSum.VerdictReason)
	}

	if opts.Hooks != nil && opts.Hooks.OnDone != nil {
		opts.Hooks.OnDone(meta, sectionRows, logTail.String())
	}

	if startupErr != nil && !oomKilled && !stoppedByUser {
		return startupErr
	}
	return nil
}

func fillCgroupFields(row *results.SampleRow, snap cgroup.Snapshot) {
	row.CGMemoryCurrentBytes = snap.CurrentBytes
	row.CGAnonBytes = snap.Stat.AnonBytes
	row.CGFileBytes = snap.Stat.FileBytes
	row.CGFileMappedBytes = snap.Stat.FileMappedBytes
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
	peakBinResidentBytes   uint64
	peakCGMemoryBytes      uint64
	peakCGAnonBytes        uint64
	peakCGFileBytes        uint64
	peakCGWorkingSetBytes  uint64
	peakEstIsolatedWSBytes uint64
	peakProcRSSBytes       uint64
	lastLiveSample         results.SampleRow
	hasLiveSample          bool
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
	ws := r.CGWorkingSetBytes
	if ws == 0 && r.CGMemoryCurrentBytes > 0 {
		ws = results.KubeletWorkingSetBytes(r.CGMemoryCurrentBytes, r.CGInactiveFileBytes)
	}
	if ws > a.peakCGWorkingSetBytes {
		a.peakCGWorkingSetBytes = ws
	}
	isoWS := r.EstIsolatedWorkingSetBytes
	if isoWS == 0 && (ws > 0 || r.ProcRSSFileBytes > 0) {
		isoWS = ws + results.UnchargedRSSFileBytes(r.ProcRSSFileBytes, r.CGFileBytes)
	}
	if isoWS > a.peakEstIsolatedWSBytes {
		a.peakEstIsolatedWSBytes = isoWS
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
	startupReadinessMs int64,
	finalCG cgroup.Snapshot,
	hasFinalCG bool,
	pageSize uint64,
	oomKilled bool,
	exitedEarly bool,
	exitCode int,
	startupErr error,
	stoppedByUser bool,
) results.FinalSummary {
	s := results.FinalSummary{
		PeakBinResidentBytes:   a.peakBinResidentBytes,
		PeakCGMemoryBytes:      a.peakCGMemoryBytes,
		PeakCGAnonBytes:        a.peakCGAnonBytes,
		PeakCGFileBytes:        a.peakCGFileBytes,
		PeakCGWorkingSetBytes:  a.peakCGWorkingSetBytes,
		PeakEstIsolatedWSBytes: a.peakEstIsolatedWSBytes,
		PeakProcRSSBytes:       a.peakProcRSSBytes,
	}

	if a.hasLiveSample {
		last := a.lastLiveSample
		last.PopulateWorkingSet()
		s.FinalBinResidentBytes = (last.BinPresentFilePages + last.BinPresentCoWPages) * pageSize
		s.EverFaultedBinBytes = last.BinEverFaultedPages * pageSize
		s.CleanlyEvictedBinBytes = last.BinCleanlyEvictedPages * pageSize
		if last.BinEverFaultedPages > 0 {
			s.CleanReclaimPercentOfFaulted = 100.0 * float64(last.BinCleanlyEvictedPages) / float64(last.BinEverFaultedPages)
		}
		s.FinalCGMemoryBytes = last.CGMemoryCurrentBytes
		s.FinalCGActiveFileBytes = last.CGActiveFileBytes
		s.FinalCGInactiveFileBytes = last.CGInactiveFileBytes
		s.FinalCGWorkingSetBytes = last.CGWorkingSetBytes
		s.FinalEstIsolatedWSBytes = last.EstIsolatedWorkingSetBytes
		s.FinalProcRSSBytes = last.ProcRSSBytes
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

	churn := analyzeChurn(samples, startupReadinessMs, s.ThrashingPages)
	s.Verdict, s.VerdictReason = classifyVerdict(s, churn, oomKilled, exitedEarly, exitCode, startupErr, stoppedByUser)
	return s
}

const (
	// startupSettleGraceMs grants initial Go init() / pipeline warm-up sweeps time to settle
	// before measuring steady-state churn whenever the run continues for at least minPostSettleWindowMs.
	startupSettleGraceMs  = 2000
	defaultColdStartMs    = 5000
	minPostSettleWindowMs = 5000

	// Active churn requires simultaneous eviction AND refault inside the same time bucket
	// (or sustained refaults paired with cgroup memory.max direct reclaim + PSI full stall),
	// distinguishing continual working-set oscillation from one-shot manual memory.reclaim reloads.
	churn1sMinPages  = 32
	churn1sMinStallP = 16
	churn1sMinPSIMs  = 10.0

	churn5sMinPages  = 64
	churn5sMinStallP = 32
	churn5sMinPSIMs  = 25.0
)

type churnBucket struct {
	evictions uint64
	refaults  uint64
	cgMax     uint64
	psiFullMs float64
}

func (b churnBucket) isChurn1s() bool {
	return (b.evictions >= churn1sMinPages && b.refaults >= churn1sMinPages) ||
		(b.refaults >= churn1sMinStallP && b.cgMax > 0 && b.psiFullMs >= churn1sMinPSIMs)
}

func (b churnBucket) isChurn5s() bool {
	return (b.evictions >= churn5sMinPages && b.refaults >= churn5sMinPages) ||
		(b.refaults >= churn5sMinStallP && b.cgMax > 0 && b.psiFullMs >= churn5sMinPSIMs)
}

type churnMetrics struct {
	PostStartup        bool
	EvalWindowSecs     int
	ChurnSecs1s        int
	TrailingWindowSecs int
	TrailingChurnSecs  int
	EvalWindows5s      int
	ChurnWindows5s     int
	SteadyRefaults     uint64
	SteadyPSIFullMs    float64
	Sustained          bool
}

func analyzeChurn(samples []results.SampleRow, startupReadinessMs int64, activeThrashingPages uint64) churnMetrics {
	if len(samples) == 0 {
		return churnMetrics{}
	}
	endMs := max(int64(0), samples[len(samples)-1].ElapsedMs)

	var evalStartMs int64
	if startupReadinessMs > 0 {
		candidate := startupReadinessMs + startupSettleGraceMs
		if endMs >= candidate+minPostSettleWindowMs {
			evalStartMs = candidate
		}
	} else if endMs >= defaultColdStartMs+minPostSettleWindowMs {
		evalStartMs = defaultColdStartMs
	}

	b1 := make(map[int]*churnBucket)
	b5 := make(map[int]*churnBucket)
	var m churnMetrics
	m.PostStartup = evalStartMs > 0

	for i, r := range samples {
		var dPSIFullUs, dCGMax uint64
		if i > 0 {
			if r.CGPSIFullTotalUs > samples[i-1].CGPSIFullTotalUs {
				dPSIFullUs = r.CGPSIFullTotalUs - samples[i-1].CGPSIFullTotalUs
			}
			if r.CGEventsMax > samples[i-1].CGEventsMax {
				dCGMax = r.CGEventsMax - samples[i-1].CGEventsMax
			}
		}
		ms := max(int64(0), r.ElapsedMs)
		if ms < evalStartMs {
			continue
		}
		dPSIMs := float64(dPSIFullUs) / 1000.0
		m.SteadyRefaults += r.DeltaRefaultsTotal
		m.SteadyPSIFullMs += dPSIMs

		s1 := int(ms / 1000)
		c1 := b1[s1]
		if c1 == nil {
			c1 = &churnBucket{}
			b1[s1] = c1
		}
		c1.evictions += r.DeltaEvictions
		c1.refaults += r.DeltaRefaultsTotal
		c1.cgMax += dCGMax
		c1.psiFullMs += dPSIMs

		s5 := int(ms / 5000)
		c5 := b5[s5]
		if c5 == nil {
			c5 = &churnBucket{}
			b5[s5] = c5
		}
		c5.evictions += r.DeltaEvictions
		c5.refaults += r.DeltaRefaultsTotal
		c5.cgMax += dCGMax
		c5.psiFullMs += dPSIMs
	}

	startSec := int(evalStartMs / 1000)
	endSec := max(startSec, int(endMs/1000))
	m.EvalWindowSecs = endSec - startSec + 1

	trailStart := max(startSec, endSec-9)
	m.TrailingWindowSecs = endSec - trailStart + 1

	for sec := startSec; sec <= endSec; sec++ {
		c := b1[sec]
		if c != nil && c.isChurn1s() {
			m.ChurnSecs1s++
			if sec >= trailStart {
				m.TrailingChurnSecs++
			}
		}
	}

	startW5 := int(evalStartMs / 5000)
	endW5 := max(startW5, int(endMs/5000))
	m.EvalWindows5s = endW5 - startW5 + 1
	for w := startW5; w <= endW5; w++ {
		if c := b5[w]; c != nil && c.isChurn5s() {
			m.ChurnWindows5s++
		}
	}

	switch {
	case m.ChurnSecs1s >= 5 && float64(m.ChurnSecs1s)/float64(m.EvalWindowSecs) >= 0.25:
		m.Sustained = true
	case m.EvalWindows5s >= 4 && m.ChurnWindows5s >= 4 && float64(m.ChurnWindows5s)/float64(m.EvalWindows5s) >= 0.50:
		m.Sustained = true
	case m.TrailingWindowSecs >= 8 && m.TrailingChurnSecs >= 6:
		m.Sustained = true
	case m.EvalWindowSecs >= 2 && m.EvalWindowSecs < 5 && m.ChurnSecs1s == m.EvalWindowSecs && activeThrashingPages >= 32:
		m.Sustained = true
	}
	return m
}

func classifyVerdict(
	s results.FinalSummary,
	churn churnMetrics,
	oomKilled bool,
	exitedEarly bool,
	exitCode int,
	startupErr error,
	stoppedByUser bool,
) (string, string) {
	if oomKilled {
		return results.VerdictOOMKilled, fmt.Sprintf(
			"cgroup OOM killer terminated Collector after %d evictions and %d refaults (peak anon %s)",
			s.TotalEvictions, s.TotalRefaults, bytefmt.FormatIEC(s.PeakCGAnonBytes),
		)
	}
	if stoppedByUser {
		return results.VerdictStopped, fmt.Sprintf(
			"run stopped early by user (%d cold faults, %d evictions, %d refaults)",
			s.TotalColdFaults, s.TotalEvictions, s.TotalRefaults,
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

	windowKind := "runtime"
	if churn.PostStartup {
		windowKind = "post-startup runtime"
	}

	if churn.Sustained {
		return results.VerdictThrashing, fmt.Sprintf(
			"sustained page thrashing across %d/%ds of %s (trailing %d/%ds active): %d refaults across %d binary pages (%d pages actively churning), %.1f ms PSI full stall",
			churn.ChurnSecs1s, churn.EvalWindowSecs, windowKind,
			churn.TrailingChurnSecs, churn.TrailingWindowSecs,
			s.TotalRefaults, s.EverRefaultedPages, s.ThrashingPages, s.TotalPSIFullMs,
		)
	}

	if s.TotalRefaults >= 64 && churn.EvalWindowSecs > 0 {
		return results.VerdictCleanReclaim, fmt.Sprintf(
			"cleanly evicted %s (%.1f%% of faulted binary pages) down to %s resident; %d transient refaults (%.1f ms PSI full stall) stabilized without continuous churn (%d/%ds %s active)",
			bytefmt.FormatIEC(s.CleanlyEvictedBinBytes),
			s.CleanReclaimPercentOfFaulted,
			bytefmt.FormatIEC(s.FinalBinResidentBytes),
			s.TotalRefaults,
			s.TotalPSIFullMs,
			churn.ChurnSecs1s,
			churn.EvalWindowSecs,
			windowKind,
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

type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(maxBytes int) *tailBuffer {
	return &tailBuffer{max: maxBytes}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if t.max <= 0 {
		return n, nil
	}
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if len(t.buf)+len(p) > t.max {
		drop := len(t.buf) + len(p) - t.max
		copy(t.buf, t.buf[drop:])
		t.buf = t.buf[:len(t.buf)-drop]
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
