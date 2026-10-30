package visualizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

func writeSyntheticRun(t *testing.T, dir string, name string, limitRaw string, unlimited bool, limitBytes uint64, verdict string) {
	t.Helper()
	w, err := results.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter(%q): %v", dir, err)
	}
	if err := w.WriteSample(results.SampleRow{
		TimestampUnixMs:        1700000000000,
		ElapsedMs:              0,
		Trigger:                results.TriggerStart,
		BinTotalPages:          1000,
		BinPresentFilePages:    500,
		BinNeverFaultedPages:   500,
		BinEverFaultedPages:    500,
		CGMemoryCurrentBytes:   40 * 1024 * 1024,
		ProcRSSBytes:           40 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSample(results.SampleRow{
		TimestampUnixMs:        1700000001000,
		ElapsedMs:              1000,
		Trigger:                results.TriggerFinal,
		BinTotalPages:          1000,
		BinPresentFilePages:    120,
		BinCleanlyEvictedPages: 380,
		BinEvictedOutPages:     380,
		BinNeverFaultedPages:   500,
		BinEverFaultedPages:    500,
		BinEverEvictedPages:    380,
		CumColdFaults:          500,
		CumEvictions:           380,
		CGMemoryCurrentBytes:   25 * 1024 * 1024,
		ProcRSSBytes:           25 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSections([]results.SectionRow{
		{SectionName: ".text", VMAPerms: "r-xp", TotalPages: 1000, NeverFaultedPages: 500, PresentFinalPages: 120, CleanlyEvictedPages: 380},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteBlocks([]results.BlockRow{
		{BlockIndex: 0, FileOffsetEndBytes: 262144, PrimarySection: ".text", VMAPerms: "r-xp", TotalPages: 64, PresentFinalPages: 14, CleanlyEvictedPages: 50},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteMeta(results.RunMeta{
		Name:                   name,
		StartTime:              time.Unix(1700000000, 0).UTC(),
		EndTime:                time.Unix(1700000001, 0).UTC(),
		DurationMs:             1000,
		MemoryLimitRaw:         limitRaw,
		MemoryLimitHuman:       limitRaw,
		MemoryLimitUnlimited:   unlimited,
		MemoryLimitBytes:       limitBytes,
		SwapMaxRaw:             "0",
		PageSizeBytes:          4096,
		BlockSizePages:         64,
		TotalBinaryPages:       1000,
		TotalBinaryMappedBytes: 4096000,
		SampleCount:            2,
		Summary: results.FinalSummary{
			Verdict:                verdict,
			VerdictReason:          "synthetic test verdict",
			PeakBinResidentBytes:   500 * 4096,
			FinalBinResidentBytes:  120 * 4096,
			EverFaultedBinBytes:    500 * 4096,
			CleanlyEvictedBinBytes: 380 * 4096,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateSingleAndMultiRun(t *testing.T) {
	base := t.TempDir()
	dir40 := filepath.Join(base, "run-40m")
	dir80 := filepath.Join(base, "run-80m")
	dirMax := filepath.Join(base, "run-max")

	writeSyntheticRun(t, dir40, "sweep", "40MiB", false, 40*1024*1024, results.VerdictThrashing)
	writeSyntheticRun(t, dir80, "sweep", "80MiB", false, 80*1024*1024, results.VerdictCleanReclaim)
	writeSyntheticRun(t, dirMax, "sweep", "max", true, 0, results.VerdictNoReclaim)

	// Single-run generation defaults to <inputDir>/dashboard.html.
	singleOut, err := Generate([]string{dir80}, "")
	if err != nil {
		t.Fatalf("Generate single: %v", err)
	}
	singleHTML, err := os.ReadFile(singleOut)
	if err != nil {
		t.Fatalf("read single html: %v", err)
	}
	if !strings.Contains(string(singleHTML), `"multi_run":false`) {
		t.Fatal("expected multi_run:false in single-run dashboard")
	}

	// Multi-run generation sorts max -> 80MiB -> 40MiB regardless of CLI input order.
	multiOutPath := filepath.Join(base, "comparison.html")
	gotOut, err := Generate([]string{dir40, dirMax, dir80}, multiOutPath)
	if err != nil {
		t.Fatalf("Generate multi: %v", err)
	}
	if gotOut != multiOutPath {
		t.Fatalf("got output path %q; want %q", gotOut, multiOutPath)
	}
	multiHTMLBytes, err := os.ReadFile(multiOutPath)
	if err != nil {
		t.Fatal(err)
	}
	multiHTML := string(multiHTMLBytes)
	idxMax := strings.Index(multiHTML, `"memory_limit_raw":"max"`)
	idx80 := strings.Index(multiHTML, `"memory_limit_raw":"80MiB"`)
	idx40 := strings.Index(multiHTML, `"memory_limit_raw":"40MiB"`)
	if idxMax < 0 || idx80 < 0 || idx40 < 0 || !(idxMax < idx80 && idx80 < idx40) {
		t.Fatalf("expected runs sorted max -> 80MiB -> 40MiB, got indices max=%d 80=%d 40=%d", idxMax, idx80, idx40)
	}

	// Collapsible comparison guide must appear collapsed by default directly above #comparison-table.
	guideTag := `<details id="comparison-guide" class="card guide-details">`
	idxGuide := strings.Index(multiHTML, guideTag)
	idxTable := strings.Index(multiHTML, `<table id="comparison-table">`)
	if idxGuide < 0 {
		t.Fatalf("expected collapsible guide %q in dashboard HTML", guideTag)
	}
	if idxTable < 0 || idxGuide >= idxTable {
		t.Fatalf("expected comparison guide (idx=%d) before comparison table (idx=%d)", idxGuide, idxTable)
	}

	guideSlice := multiHTML[idxGuide:idxTable]
	wantInGuide := []string{
		"How to Read This Table",
		"1. Cold Fault",
		"2. Clean Eviction",
		"3. Binary Refault",
		"4. Page Thrashing / OOM Kill",
		"Run Setup &amp; Verdict (Columns 1&ndash;3)",
		"Kubernetes Pod Eviction Accounting &mdash; Cgroup v2 + procfs (Column 4)",
		"Collector Binary Tracking &mdash; Userspace pagemap (Columns 5&ndash;8)",
		"Kernel Reclaim &amp; Pressure &mdash; Cgroup v2 (Columns 9&ndash;11)",
		"Telemetry Sampling (Column 12)",
		"#1</span><strong>Run</strong>",
		"#2</span><strong>Memory Limit</strong>",
		"#3</span><strong>Outcome</strong>",
		"#4</span><strong>Kubelet Working Set</strong>",
		"#5</span><strong>Peak &rarr; Final Binary</strong>",
		"#6</span><strong>Cleanly Evicted</strong>",
		"#7</span><strong>Reclaimed Refault %</strong>",
		"#8</span><strong>Binary Refaults (0&rarr;1 / PFN)</strong>",
		"#9</span><strong>Kernel Refaults</strong>",
		"#10</span><strong>Major Faults</strong>",
		"#11</span><strong>PSI Full Stall</strong>",
		"#12</span><strong>Samples (Reclaim)</strong>",
		"container_memory_working_set_bytes",
		"badge-oom_killed",
		"badge-process_error",
		"badge-no_reclaim",
		"badge-thrashing",
		"badge-clean_reclaim",
		"workingset_refault_file",
		"pgmajfault",
		"memory.pressure: full total=",
	}
	for _, needle := range wantInGuide {
		if !strings.Contains(guideSlice, needle) {
			t.Errorf("comparison guide missing expected content %q", needle)
		}
	}

	// All 12 comparison table column headers should expose viewport-safe hover/focus tooltips.
	idxTheadEnd := strings.Index(multiHTML[idxTable:], "</thead>")
	if idxTheadEnd < 0 {
		t.Fatal("missing </thead> in #comparison-table")
	}
	theadSlice := multiHTML[idxTable : idxTable+idxTheadEnd]
	if got := strings.Count(theadSlice, `<th tabindex="0" data-tip="`); got != 12 {
		t.Fatalf("got %d tooltip-annotated <th> headers in #comparison-table; want 12", got)
	}
	if !strings.Contains(multiHTML, `<div id="chart-multi-ws" class="chart-box"></div>`) {
		t.Fatal("missing #chart-multi-ws multi-run Working Set chart")
	}
	if !strings.Contains(multiHTML, `<div id="th-tooltip" role="tooltip"></div>`) {
		t.Fatal("missing floating #th-tooltip container")
	}

	// Run deep dive must include the section insight banner and 7-column section analysis table.
	if !strings.Contains(multiHTML, `<div id="section-insight-banner" class="insight-banner"></div>`) {
		t.Fatal("missing #section-insight-banner in dashboard HTML")
	}
	idxSecTable := strings.Index(multiHTML, `<table id="section-analysis-table">`)
	if idxSecTable < 0 {
		t.Fatal("missing #section-analysis-table in dashboard HTML")
	}
	idxSecTheadEnd := strings.Index(multiHTML[idxSecTable:], "</thead>")
	if idxSecTheadEnd < 0 {
		t.Fatal("missing </thead> in #section-analysis-table")
	}
	secTheadSlice := multiHTML[idxSecTable : idxSecTable+idxSecTheadEnd]
	if got := strings.Count(secTheadSlice, `<th tabindex="0" data-tip="`); got != 7 {
		t.Fatalf("got %d tooltip-annotated <th> headers in #section-analysis-table; want 7", got)
	}
}

func TestBuildSummaryReclaimAndStartupMetrics(t *testing.T) {
	rd := &results.RunData{
		Dir: "/tmp/otlp-120MiB",
		Meta: results.RunMeta{
			Name:                   "otlp-minimal",
			MemoryLimitRaw:         "120MiB",
			MemoryLimitHuman:       "120.0 MiB",
			DurationMs:             300000,
			PageSizeBytes:          4096,
			TotalBinaryPages:       1000,
			TotalBinaryMappedBytes: 1000 * 4096,
			Workload: results.WorkloadSummary{
				WorkloadEnabled:    true,
				StartupReadinessMs: 450,
			},
			Summary: results.FinalSummary{
				Verdict:                results.VerdictCleanReclaim,
				CleanlyEvictedBinBytes: 300 * 4096,
				EverFaultedBinBytes:    500 * 4096,
				TotalColdFaults:        500,
				EverRefaultedPages:     100,
			},
		},
		Samples: []results.SampleRow{
			{ElapsedMs: 200, BinTotalPages: 1000, CumColdFaults: 250},
			{ElapsedMs: 500, BinTotalPages: 1000, CumColdFaults: 490},
			{
				ElapsedMs:              300000,
				BinTotalPages:          1000,
				BinEverFaultedPages:    500,
				BinEverEvictedPages:    400,
				BinCleanlyEvictedPages: 300,
				BinEverRefaultedPages:  100,
				CumColdFaults:          500,
			},
		},
	}

	s := buildSummary(0, rd)
	if s.EverEvictedPages != 400 {
		t.Fatalf("EverEvictedPages = %d; want 400", s.EverEvictedPages)
	}
	if s.ReclaimedRefaultPercent != 25.0 {
		t.Fatalf("ReclaimedRefaultPercent = %.2f; want 25.00", s.ReclaimedRefaultPercent)
	}
	if s.ReclaimedCleanPercent != 75.0 {
		t.Fatalf("ReclaimedCleanPercent = %.2f; want 75.00", s.ReclaimedCleanPercent)
	}
	if !s.StartupCompleted || s.StartupTimedOut {
		t.Fatalf("StartupCompleted=%v StartupTimedOut=%v; want true/false", s.StartupCompleted, s.StartupTimedOut)
	}
	if s.StartupSampleMs != 500 || s.StartupColdFaults != 490 || s.RuntimeColdFaults != 10 {
		t.Fatalf("startup split got sample=%dms startup=%d runtime=%d; want 500ms/490/10",
			s.StartupSampleMs, s.StartupColdFaults, s.RuntimeColdFaults)
	}
	if s.StartupColdFaultPercent != 98.0 {
		t.Fatalf("StartupColdFaultPercent = %.2f; want 98.00", s.StartupColdFaultPercent)
	}

	// Startup timeout run (workload enabled, readiness never achieved).
	rdTimeout := *rd
	rdTimeout.Meta.Workload.StartupReadinessMs = 0
	st := buildSummary(1, &rdTimeout)
	if st.StartupCompleted || !st.StartupTimedOut {
		t.Fatalf("timeout run: StartupCompleted=%v StartupTimedOut=%v; want false/true", st.StartupCompleted, st.StartupTimedOut)
	}
}

func TestBuildSummaryWorkingSetLegacyFallbackAndForeignCache(t *testing.T) {
	const mibB = 1024 * 1024
	// Legacy RunMeta whose Working Set summary fields are all 0 must derive peak & final from Samples.
	rd := &results.RunData{
		Dir: "/tmp/legacy-foreign",
		Meta: results.RunMeta{
			Name:          "otlp-minimal",
			PageSizeBytes: 4096,
			Summary: results.FinalSummary{
				Verdict: results.VerdictCleanReclaim,
			},
		},
		Samples: []results.SampleRow{
			{
				ElapsedMs:            500,
				BinTotalPages:        1000,
				CGMemoryCurrentBytes: 60 * mibB,
				CGAnonBytes:          58 * mibB,
				CGKernelBytes:        2 * mibB,
				CGFileBytes:          0,
				CGActiveFileBytes:    0,
				CGInactiveFileBytes:  0,
				ProcRSSBytes:         260 * mibB,
				ProcRSSAnonBytes:     58 * mibB,
				ProcRSSFileBytes:     202 * mibB,
			},
			{
				ElapsedMs:            2000,
				BinTotalPages:        1000,
				CGMemoryCurrentBytes: 120 * mibB,
				CGAnonBytes:          80 * mibB,
				CGKernelBytes:        2 * mibB,
				CGFileBytes:          38 * mibB,
				CGActiveFileBytes:    14 * mibB,
				CGInactiveFileBytes:  24 * mibB,
				ProcRSSBytes:         110 * mibB,
				ProcRSSAnonBytes:     80 * mibB,
				ProcRSSFileBytes:     30 * mibB,
			},
		},
	}

	s := buildSummary(0, rd)
	// Row 0: CG WS = 60 MiB, uncharged RssFile = 202 MiB -> Est Iso WS = 262 MiB.
	// Row 1: CG WS = 120 - 24 = 96 MiB, uncharged = 0 -> Est Iso WS = 96 MiB.
	if s.PeakCGWorkingSetMiB != 96.0 || s.FinalCGWorkingSetMiB != 96.0 {
		t.Fatalf("CG WS peak/final = (%.2f, %.2f); want (96.00, 96.00)", s.PeakCGWorkingSetMiB, s.FinalCGWorkingSetMiB)
	}
	if s.PeakEstIsolatedWSMiB != 262.0 || s.FinalEstIsolatedWSMiB != 96.0 {
		t.Fatalf("Iso WS peak/final = (%.2f, %.2f); want (262.00, 96.00)", s.PeakEstIsolatedWSMiB, s.FinalEstIsolatedWSMiB)
	}
	if s.MaxUnchargedRSSFileMiB != 202.0 {
		t.Fatalf("MaxUnchargedRSSFileMiB = %.2f; want 202.00", s.MaxUnchargedRSSFileMiB)
	}
	if s.FinalCGActiveFileMiB != 14.0 || s.FinalCGInactiveFileMiB != 24.0 || s.FinalProcRSSMiB != 110.0 {
		t.Fatalf("Final active/inactive/rss = (%.2f, %.2f, %.2f); want (14, 24, 110)",
			s.FinalCGActiveFileMiB, s.FinalCGInactiveFileMiB, s.FinalProcRSSMiB)
	}
}
