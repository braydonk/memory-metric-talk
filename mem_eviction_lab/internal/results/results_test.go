package results

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResultsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	s1 := SampleRow{
		TimestampUnixMs:          1700000000000,
		ElapsedMs:                0,
		Trigger:                  TriggerStart,
		BinTotalPages:            1000,
		BinPresentFilePages:      200,
		BinNeverFaultedPages:     800,
		BinEverFaultedPages:      200,
		DeltaColdFaults:          200,
		CumColdFaults:            200,
		ROPresentPages:           80,
		RXPresentPages:           100,
		RWPresentFilePages:       20,
		CGMemoryCurrentBytes:     12 * 1024 * 1024,
		CGAnonBytes:              4 * 1024 * 1024,
		CGFileBytes:              8 * 1024 * 1024,
		CGActiveFileBytes:        6 * 1024 * 1024,
		CGInactiveFileBytes:      2 * 1024 * 1024,
		ProcRSSBytes:             12 * 1024 * 1024,
		ProcRSSAnonBytes:         4 * 1024 * 1024,
		ProcRSSFileBytes:         8 * 1024 * 1024,
	}
	s1.PopulateWorkingSet()
	s2 := SampleRow{
		TimestampUnixMs:          1700000000500,
		ElapsedMs:                500,
		Trigger:                  TriggerReclaimEvent,
		BinTotalPages:            1000,
		BinPresentFilePages:      90,
		BinPresentCoWPages:       5,
		BinNeverFaultedPages:     600,
		BinEvictedOutPages:       305,
		BinCleanlyEvictedPages:   300,
		BinEverFaultedPages:      400,
		BinEverEvictedPages:      310,
		BinEverRefaultedPages:    10,
		BinThrashingPages:        2,
		DeltaColdFaults:          200,
		DeltaEvictions:           310,
		DeltaRefaults01:          7,
		DeltaRefaultsPFN:         3,
		DeltaRefaultsTotal:       10,
		CumColdFaults:            400,
		CumEvictions:             310,
		CumRefaults01:            7,
		CumRefaultsPFN:           3,
		CumRefaultsTotal:         10,
		CGEventsMax:              4,
		CGWorkingsetRefaultFile:  9,
		CGPSISomeTotalUs:         1250,
	}
	s2.PopulateWorkingSet()

	if err := w.WriteSample(s1); err != nil {
		t.Fatalf("WriteSample(s1): %v", err)
	}
	if err := w.WriteSample(s2); err != nil {
		t.Fatalf("WriteSample(s2): %v", err)
	}

	sections := []SectionRow{
		{
			SectionName:               ".text",
			VMAPerms:                  "r-xp",
			FileOffsetBytes:           4096,
			SizeBytes:                 2048000,
			TotalPages:                500,
			NeverFaultedPages:         300,
			PresentFinalPages:         50,
			CleanlyEvictedPages:       145,
			ResidentAfterRefaultPages: 5,
			ThrashingPages:            1,
			TotalEvictions:            150,
			TotalRefaults01:           4,
			TotalRefaultsPFN:          2,
			TotalRefaults:             6,
		},
	}
	if err := w.WriteSections(sections); err != nil {
		t.Fatalf("WriteSections: %v", err)
	}

	blocks := []BlockRow{
		{
			BlockIndex:           0,
			FileOffsetStartBytes: 0,
			FileOffsetEndBytes:   262144,
			PrimarySection:       ".rodata",
			VMAPerms:             "r--p",
			TotalPages:           64,
			NeverFaultedPages:    10,
			PresentFinalPages:    14,
			CleanlyEvictedPages:  40,
			TotalEvictions:       40,
		},
	}
	if err := w.WriteBlocks(blocks); err != nil {
		t.Fatalf("WriteBlocks: %v", err)
	}

	meta := RunMeta{
		Name:                   "roundtrip-test",
		StartTime:              time.Unix(1700000000, 0).UTC(),
		EndTime:                time.Unix(1700000060, 0).UTC(),
		DurationMs:             60000,
		ConfiguredDuration:     "1m0s",
		CollectorBinary:        "/path/to/otelcol-contrib",
		CollectorBinarySHA256:  "deadbeef",
		CollectorConfig:        "/path/to/cfg.yaml",
		MemoryLimitRaw:         "60MiB",
		MemoryLimitHuman:       "60.0 MiB",
		MemoryLimitBytes:       60 * 1024 * 1024,
		SwapMaxRaw:             "0",
		PageSizeBytes:          4096,
		BlockSizePages:         64,
		TotalBinaryPages:       1000,
		TotalBinaryMappedBytes: 4096000,
		SampleCount:            2,
		ReclaimEventSamples:    1,
		Summary: FinalSummary{
			Verdict:       VerdictCleanReclaim,
			VerdictReason: "cleanly evicted unused binary pages without sustained thrashing",
		},
	}
	if err := w.WriteMeta(meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := LoadRunData(dir)
	if err != nil {
		t.Fatalf("LoadRunData: %v", err)
	}
	if !reflect.DeepEqual(got.Meta, meta) {
		t.Fatalf("Meta mismatch:\ngot:  %+v\nwant: %+v", got.Meta, meta)
	}
	if !reflect.DeepEqual(got.Samples, []SampleRow{s1, s2}) {
		t.Fatalf("Samples mismatch:\ngot:  %+v\nwant: %+v", got.Samples, []SampleRow{s1, s2})
	}
	if !reflect.DeepEqual(got.Sections, sections) {
		t.Fatalf("Sections mismatch:\ngot:  %+v\nwant: %+v", got.Sections, sections)
	}
	if !reflect.DeepEqual(got.Blocks, blocks) {
		t.Fatalf("Blocks mismatch:\ngot:  %+v\nwant: %+v", got.Blocks, blocks)
	}
}

func TestWorkingSetHelpersAndLegacyCSVBackfill(t *testing.T) {
	// 1. Underflow guard when inactive_file transiently exceeds memory.current on a non-atomic read.
	if got := KubeletWorkingSetBytes(10*1024*1024, 11*1024*1024); got != 0 {
		t.Fatalf("KubeletWorkingSetBytes underflow = %d; want 0", got)
	}

	// 2. Normal cold-cache isolated run (cg_file >= proc_rss_file because of readahead/config cache).
	cold := SampleRow{
		CGMemoryCurrentBytes: 120 * 1024 * 1024,
		CGAnonBytes:          80 * 1024 * 1024,
		CGKernelBytes:        2 * 1024 * 1024,
		CGFileBytes:          38 * 1024 * 1024,
		CGActiveFileBytes:    15 * 1024 * 1024,
		CGInactiveFileBytes:  23 * 1024 * 1024,
		ProcRSSBytes:         110 * 1024 * 1024,
		ProcRSSAnonBytes:     80 * 1024 * 1024,
		ProcRSSFileBytes:     30 * 1024 * 1024,
	}
	cold.PopulateWorkingSet()
	if want := uint64(97 * 1024 * 1024); cold.CGWorkingSetBytes != want || cold.EstIsolatedWorkingSetBytes != want {
		t.Fatalf("cold sample WS=(%d, %d); want both %d", cold.CGWorkingSetBytes, cold.EstIsolatedWorkingSetBytes, want)
	}

	// 3. Shared/foreign page-cache run (like otlp-max where execve scanner warmed RssFile outside the cgroup).
	foreign := SampleRow{
		CGMemoryCurrentBytes: 62 * 1024 * 1024,
		CGAnonBytes:          56 * 1024 * 1024,
		CGKernelBytes:        3 * 1024 * 1024,
		CGFileBytes:          12288,
		CGActiveFileBytes:    4096,
		CGInactiveFileBytes:  8192,
		ProcRSSBytes:         256 * 1024 * 1024,
		ProcRSSAnonBytes:     56 * 1024 * 1024,
		ProcRSSFileBytes:     200 * 1024 * 1024,
	}
	foreign.PopulateWorkingSet()
	wantCGWS := uint64(62*1024*1024 - 8192)
	wantIsoWS := wantCGWS + (200*1024*1024 - 12288)
	if foreign.CGWorkingSetBytes != wantCGWS || foreign.EstIsolatedWorkingSetBytes != wantIsoWS {
		t.Fatalf("foreign sample WS=(%d, %d); want (%d, %d)",
			foreign.CGWorkingSetBytes, foreign.EstIsolatedWorkingSetBytes, wantCGWS, wantIsoWS)
	}

	// 4. Legacy 57-column samples.csv (written before working_set columns existed) backfills on LoadRunData.
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSample(cold); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSample(foreign); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteMeta(RunMeta{Name: "legacy-csv", SampleCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	csvPath := filepath.Join(dir, SamplesCSVName)
	rawCSV, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawCSV)), "\n")
	for i, line := range lines {
		cols := strings.Split(line, ",")
		if len(cols) != len(samplesCSVHeader) {
			t.Fatalf("line %d has %d cols; want %d", i, len(cols), len(samplesCSVHeader))
		}
		// Strip the trailing 3 columns (working-set + file_mapped) to mimic older run_results/ CSVs.
		lines[i] = strings.Join(cols[:len(cols)-3], ",")
	}
	if err := os.WriteFile(csvPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadRunData(dir)
	if err != nil {
		t.Fatalf("LoadRunData on legacy CSV: %v", err)
	}
	if len(loaded.Samples) != 2 {
		t.Fatalf("got %d samples; want 2", len(loaded.Samples))
	}
	if loaded.Samples[0].CGWorkingSetBytes != cold.CGWorkingSetBytes ||
		loaded.Samples[0].EstIsolatedWorkingSetBytes != cold.EstIsolatedWorkingSetBytes {
		t.Fatalf("row 0 backfill got (%d, %d); want (%d, %d)",
			loaded.Samples[0].CGWorkingSetBytes, loaded.Samples[0].EstIsolatedWorkingSetBytes,
			cold.CGWorkingSetBytes, cold.EstIsolatedWorkingSetBytes)
	}
	if loaded.Samples[1].CGWorkingSetBytes != foreign.CGWorkingSetBytes ||
		loaded.Samples[1].EstIsolatedWorkingSetBytes != foreign.EstIsolatedWorkingSetBytes {
		t.Fatalf("row 1 backfill got (%d, %d); want (%d, %d)",
			loaded.Samples[1].CGWorkingSetBytes, loaded.Samples[1].EstIsolatedWorkingSetBytes,
			foreign.CGWorkingSetBytes, foreign.EstIsolatedWorkingSetBytes)
	}
}
