package visualizer

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

const mib = 1024.0 * 1024.0

// RunSummary holds precomputed display KPIs for a single run inside the dashboard.
type RunSummary struct {
	Index                        int     `json:"index"`
	Name                         string  `json:"name"`
	Dir                          string  `json:"dir"`
	Label                        string  `json:"label"`
	MemoryLimitHuman             string  `json:"memory_limit_human"`
	MemoryLimitUnlimited         bool    `json:"memory_limit_unlimited"`
	MemoryLimitBytes             uint64  `json:"memory_limit_bytes"`
	SwapMaxRaw                   string  `json:"swap_max_raw"`
	DurationSec                  float64 `json:"duration_sec"`
	Verdict                      string  `json:"verdict"`
	VerdictReason                string  `json:"verdict_reason"`
	OOMKilled                    bool    `json:"oom_killed"`
	BinaryMappedMiB              float64 `json:"binary_mapped_mib"`
	PeakBinResidentMiB           float64 `json:"peak_bin_resident_mib"`
	FinalBinResidentMiB          float64 `json:"final_bin_resident_mib"`
	EverFaultedBinMiB            float64 `json:"ever_faulted_bin_mib"`
	CleanlyEvictedBinMiB         float64 `json:"cleanly_evicted_bin_mib"`
	CleanReclaimPercentOfFaulted float64 `json:"clean_reclaim_percent_of_faulted"`
	EverEvictedPages             uint64  `json:"ever_evicted_pages"`
	EverEvictedBinMiB            float64 `json:"ever_evicted_bin_mib"`
	EverRefaultedBinMiB          float64 `json:"ever_refaulted_bin_mib"`
	ReclaimedRefaultPercent      float64 `json:"reclaimed_refault_percent"`
	ReclaimedCleanPercent        float64 `json:"reclaimed_clean_percent"`
	PeakCGMemoryMiB              float64 `json:"peak_cg_memory_mib"`
	FinalCGMemoryMiB             float64 `json:"final_cg_memory_mib"`
	PeakCGAnonMiB                float64 `json:"peak_cg_anon_mib"`
	PeakCGFileMiB                float64 `json:"peak_cg_file_mib"`
	FinalCGActiveFileMiB         float64 `json:"final_cg_active_file_mib"`
	FinalCGInactiveFileMiB       float64 `json:"final_cg_inactive_file_mib"`
	PeakCGWorkingSetMiB          float64 `json:"peak_cg_working_set_mib"`
	FinalCGWorkingSetMiB         float64 `json:"final_cg_working_set_mib"`
	PeakEstIsolatedWSMiB         float64 `json:"peak_est_isolated_ws_mib"`
	FinalEstIsolatedWSMiB        float64 `json:"final_est_isolated_ws_mib"`
	PeakProcRSSMiB               float64 `json:"peak_proc_rss_mib"`
	FinalProcRSSMiB              float64 `json:"final_proc_rss_mib"`
	MaxUnchargedRSSFileMiB       float64 `json:"max_uncharged_rss_file_mib"`
	TotalColdFaults              uint64  `json:"total_cold_faults"`
	TotalEvictions               uint64  `json:"total_evictions"`
	TotalRefaults01              uint64  `json:"total_refaults_01"`
	TotalRefaultsPFN             uint64  `json:"total_refaults_pfn"`
	TotalRefaults                uint64  `json:"total_refaults"`
	EverRefaultedPages           uint64  `json:"ever_refaulted_pages"`
	ThrashingPages               uint64  `json:"thrashing_pages"`
	TotalCGEventsMax             uint64  `json:"total_cg_events_max"`
	TotalCGPgmajfault            uint64  `json:"total_cg_pgmajfault"`
	TotalCGWorkingsetRefaultFile uint64  `json:"total_cg_workingset_refault_file"`
	TotalPSISomeMs               float64 `json:"total_psi_some_ms"`
	TotalPSIFullMs               float64 `json:"total_psi_full_ms"`
	SampleCount                  int     `json:"sample_count"`
	ReclaimEventSamples          int     `json:"reclaim_event_samples"`
	WorkloadEnabled              bool    `json:"workload_enabled"`
	StartupReadinessMs           int64   `json:"startup_readiness_ms"`
	StartupCompleted             bool    `json:"startup_completed"`
	StartupTimedOut              bool    `json:"startup_timed_out"`
	StartupSampleMs              int64   `json:"startup_sample_ms"`
	StartupColdFaults            uint64  `json:"startup_cold_faults"`
	RuntimeColdFaults            uint64  `json:"runtime_cold_faults"`
	StartupColdFaultPercent      float64 `json:"startup_cold_fault_percent"`
}

// DashboardPayload is embedded as JSON inside dashboard.html for client-side ECharts rendering.
type DashboardPayload struct {
	Title     string             `json:"title"`
	Generated string             `json:"generated"`
	MultiRun  bool               `json:"multi_run"`
	Summaries []RunSummary       `json:"summaries"`
	Runs      []*results.RunData `json:"runs"`
}

// Generate loads one or more result directories and writes a standalone HTML dashboard.
func Generate(inputDirs []string, outputPath string) (string, error) {
	if len(inputDirs) == 0 {
		return "", errors.New("visualizer: at least one result directory is required")
	}

	runs := make([]*results.RunData, 0, len(inputDirs))
	for _, dir := range inputDirs {
		rd, err := results.LoadRunData(dir)
		if err != nil {
			return "", fmt.Errorf("load %q: %w", dir, err)
		}
		runs = append(runs, rd)
	}

	// Sort multi-run sweeps from unconstrained ("max") descending to tightest memory limit.
	if len(runs) > 1 {
		slices.SortStableFunc(runs, compareRunsByLimitDesc)
	}

	summaries := make([]RunSummary, len(runs))
	for i, rd := range runs {
		summaries[i] = buildSummary(i, rd)
	}

	if outputPath == "" {
		if len(inputDirs) == 1 {
			outputPath = filepath.Join(inputDirs[0], "dashboard.html")
		} else {
			outputPath = "comparison_dashboard.html"
		}
	}

	title := fmt.Sprintf("%s (%s) — Memory Eviction Lab", summaries[0].Name, summaries[0].MemoryLimitHuman)
	if len(summaries) > 1 {
		title = fmt.Sprintf("Memory Eviction Lab Comparison (%d runs)", len(summaries))
	}

	payload := DashboardPayload{
		Title:     title,
		Generated: time.Now().UTC().Format(time.RFC3339),
		MultiRun:  len(runs) > 1,
		Summaries: summaries,
		Runs:      runs,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal dashboard payload: %w", err)
	}

	tmpl, err := template.New("dashboard").Parse(dashboardHTMLTemplate)
	if err != nil {
		return "", fmt.Errorf("parse html template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		Title       string
		PayloadJSON template.JS
	}{
		Title:       title,
		PayloadJSON: template.JS(jsonBytes),
	}); err != nil {
		return "", fmt.Errorf("execute html template: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir for %q: %w", outputPath, err)
	}
	if err := os.WriteFile(outputPath, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("write dashboard %q: %w", outputPath, err)
	}
	return outputPath, nil
}

func compareRunsByLimitDesc(a, b *results.RunData) int {
	if a.Meta.MemoryLimitUnlimited != b.Meta.MemoryLimitUnlimited {
		if a.Meta.MemoryLimitUnlimited {
			return -1
		}
		return 1
	}
	if c := cmp.Compare(b.Meta.MemoryLimitBytes, a.Meta.MemoryLimitBytes); c != 0 {
		return c
	}
	return cmp.Compare(a.Meta.Name, b.Meta.Name)
}

func buildSummary(idx int, rd *results.RunData) RunSummary {
	m := rd.Meta
	s := m.Summary
	ps := m.PageSizeBytes
	if ps == 0 {
		ps = 4096
	}
	limitLabel := m.MemoryLimitHuman
	if limitLabel == "" {
		limitLabel = m.MemoryLimitRaw
	}

	var (
		lastLive               results.SampleRow
		hasLastLive            bool
		samplePeakCGMem        uint64
		samplePeakCGAnon       uint64
		samplePeakCGFile       uint64
		samplePeakCGWS         uint64
		samplePeakIsoWS        uint64
		samplePeakProcRSS      uint64
		maxUnchargedRSSFile    uint64
	)
	for i := range rd.Samples {
		row := &rd.Samples[i]
		// Ensure in-memory or legacy SampleRows always carry populated Working Set fields.
		if row.CGWorkingSetBytes == 0 && row.CGMemoryCurrentBytes > 0 {
			row.CGWorkingSetBytes = results.KubeletWorkingSetBytes(row.CGMemoryCurrentBytes, row.CGInactiveFileBytes)
		}
		uncharged := results.UnchargedRSSFileBytes(row.ProcRSSFileBytes, row.CGFileBytes)
		if row.EstIsolatedWorkingSetBytes == 0 && (row.CGWorkingSetBytes > 0 || uncharged > 0) {
			row.EstIsolatedWorkingSetBytes = row.CGWorkingSetBytes + uncharged
		}
		samplePeakCGMem = max(samplePeakCGMem, row.CGMemoryCurrentBytes)
		samplePeakCGAnon = max(samplePeakCGAnon, row.CGAnonBytes)
		samplePeakCGFile = max(samplePeakCGFile, row.CGFileBytes)
		samplePeakCGWS = max(samplePeakCGWS, row.CGWorkingSetBytes)
		samplePeakIsoWS = max(samplePeakIsoWS, row.EstIsolatedWorkingSetBytes)
		samplePeakProcRSS = max(samplePeakProcRSS, row.ProcRSSBytes)
		maxUnchargedRSSFile = max(maxUnchargedRSSFile, uncharged)
		if row.BinTotalPages > 0 {
			lastLive = *row
			hasLastLive = true
		}
	}

	peakCGMemBytes := max(s.PeakCGMemoryBytes, samplePeakCGMem)
	peakCGAnonBytes := max(s.PeakCGAnonBytes, samplePeakCGAnon)
	peakCGFileBytes := max(s.PeakCGFileBytes, samplePeakCGFile)
	peakCGWSBytes := max(s.PeakCGWorkingSetBytes, samplePeakCGWS)
	peakIsoWSBytes := max(s.PeakEstIsolatedWSBytes, samplePeakIsoWS)
	peakProcRSSBytes := max(s.PeakProcRSSBytes, samplePeakProcRSS)

	finalCGMemBytes := s.FinalCGMemoryBytes
	finalCGActiveFileBytes := s.FinalCGActiveFileBytes
	finalCGInactiveFileBytes := s.FinalCGInactiveFileBytes
	finalCGWSBytes := s.FinalCGWorkingSetBytes
	finalIsoWSBytes := s.FinalEstIsolatedWSBytes
	finalProcRSSBytes := s.FinalProcRSSBytes
	if hasLastLive {
		if finalCGMemBytes == 0 {
			finalCGMemBytes = lastLive.CGMemoryCurrentBytes
		}
		if finalCGActiveFileBytes == 0 {
			finalCGActiveFileBytes = lastLive.CGActiveFileBytes
		}
		if finalCGInactiveFileBytes == 0 {
			finalCGInactiveFileBytes = lastLive.CGInactiveFileBytes
		}
		if finalCGWSBytes == 0 {
			finalCGWSBytes = lastLive.CGWorkingSetBytes
		}
		if finalIsoWSBytes == 0 {
			finalIsoWSBytes = lastLive.EstIsolatedWorkingSetBytes
		}
		if finalProcRSSBytes == 0 {
			finalProcRSSBytes = lastLive.ProcRSSBytes
		}
	}

	cleanlyEvictedPages := s.CleanlyEvictedBinBytes / ps
	everRefaultedPages := s.EverRefaultedPages
	everEvictedPages := cleanlyEvictedPages + everRefaultedPages
	if hasLastLive {
		cleanlyEvictedPages = lastLive.BinCleanlyEvictedPages
		everRefaultedPages = lastLive.BinEverRefaultedPages
		everEvictedPages = lastLive.BinEverEvictedPages
	}

	var reclaimedRefaultPct, reclaimedCleanPct float64
	if everEvictedPages > 0 {
		reclaimedRefaultPct = 100.0 * float64(everRefaultedPages) / float64(everEvictedPages)
		reclaimedCleanPct = 100.0 * float64(cleanlyEvictedPages) / float64(everEvictedPages)
	}

	workloadEnabled := m.Workload.WorkloadEnabled
	startupReadyMs := m.Workload.StartupReadinessMs
	startupCompleted := workloadEnabled && startupReadyMs > 0
	startupTimedOut := workloadEnabled && startupReadyMs == 0

	var (
		startupSampleMs     int64
		startupColdFaults   uint64
		runtimeColdFaults   uint64
		startupColdFaultPct float64
	)
	if startupCompleted && hasLastLive {
		startupRow := lastLive
		for _, row := range rd.Samples {
			if row.BinTotalPages > 0 && row.ElapsedMs >= startupReadyMs {
				startupRow = row
				break
			}
		}
		startupSampleMs = startupRow.ElapsedMs
		startupColdFaults = min(startupRow.CumColdFaults, s.TotalColdFaults)
		runtimeColdFaults = s.TotalColdFaults - startupColdFaults
		if s.TotalColdFaults > 0 {
			startupColdFaultPct = 100.0 * float64(startupColdFaults) / float64(s.TotalColdFaults)
		}
	}

	return RunSummary{
		Index:                        idx,
		Name:                         m.Name,
		Dir:                          filepath.Base(rd.Dir),
		Label:                        fmt.Sprintf("%s [%s]", m.Name, limitLabel),
		MemoryLimitHuman:             limitLabel,
		MemoryLimitUnlimited:         m.MemoryLimitUnlimited,
		MemoryLimitBytes:             m.MemoryLimitBytes,
		SwapMaxRaw:                   m.SwapMaxRaw,
		DurationSec:                  float64(m.DurationMs) / 1000.0,
		Verdict:                      s.Verdict,
		VerdictReason:                s.VerdictReason,
		OOMKilled:                    m.OOMKilled,
		BinaryMappedMiB:              float64(m.TotalBinaryMappedBytes) / mib,
		PeakBinResidentMiB:           float64(s.PeakBinResidentBytes) / mib,
		FinalBinResidentMiB:          float64(s.FinalBinResidentBytes) / mib,
		EverFaultedBinMiB:            float64(s.EverFaultedBinBytes) / mib,
		CleanlyEvictedBinMiB:         float64(s.CleanlyEvictedBinBytes) / mib,
		CleanReclaimPercentOfFaulted: s.CleanReclaimPercentOfFaulted,
		EverEvictedPages:             everEvictedPages,
		EverEvictedBinMiB:            float64(everEvictedPages*ps) / mib,
		EverRefaultedBinMiB:          float64(everRefaultedPages*ps) / mib,
		ReclaimedRefaultPercent:      reclaimedRefaultPct,
		ReclaimedCleanPercent:        reclaimedCleanPct,
		PeakCGMemoryMiB:              float64(peakCGMemBytes) / mib,
		FinalCGMemoryMiB:             float64(finalCGMemBytes) / mib,
		PeakCGAnonMiB:                float64(peakCGAnonBytes) / mib,
		PeakCGFileMiB:                float64(peakCGFileBytes) / mib,
		FinalCGActiveFileMiB:         float64(finalCGActiveFileBytes) / mib,
		FinalCGInactiveFileMiB:       float64(finalCGInactiveFileBytes) / mib,
		PeakCGWorkingSetMiB:          float64(peakCGWSBytes) / mib,
		FinalCGWorkingSetMiB:         float64(finalCGWSBytes) / mib,
		PeakEstIsolatedWSMiB:         float64(peakIsoWSBytes) / mib,
		FinalEstIsolatedWSMiB:        float64(finalIsoWSBytes) / mib,
		PeakProcRSSMiB:               float64(peakProcRSSBytes) / mib,
		FinalProcRSSMiB:              float64(finalProcRSSBytes) / mib,
		MaxUnchargedRSSFileMiB:       float64(maxUnchargedRSSFile) / mib,
		TotalColdFaults:              s.TotalColdFaults,
		TotalEvictions:               s.TotalEvictions,
		TotalRefaults01:              s.TotalRefaults01,
		TotalRefaultsPFN:             s.TotalRefaultsPFN,
		TotalRefaults:                s.TotalRefaults,
		EverRefaultedPages:           everRefaultedPages,
		ThrashingPages:               s.ThrashingPages,
		TotalCGEventsMax:             s.TotalCGEventsMax,
		TotalCGPgmajfault:            s.TotalCGPgmajfault,
		TotalCGWorkingsetRefaultFile: s.TotalCGWorkingsetRefaultFile,
		TotalPSISomeMs:               s.TotalPSISomeMs,
		TotalPSIFullMs:               s.TotalPSIFullMs,
		SampleCount:                  m.SampleCount,
		ReclaimEventSamples:          m.ReclaimEventSamples,
		WorkloadEnabled:              workloadEnabled,
		StartupReadinessMs:           startupReadyMs,
		StartupCompleted:             startupCompleted,
		StartupTimedOut:              startupTimedOut,
		StartupSampleMs:              startupSampleMs,
		StartupColdFaults:            startupColdFaults,
		RuntimeColdFaults:            runtimeColdFaults,
		StartupColdFaultPercent:      startupColdFaultPct,
	}
}
