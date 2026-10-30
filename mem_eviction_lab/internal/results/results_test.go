package results

import (
	"reflect"
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
		ProcRSSBytes:             12 * 1024 * 1024,
		ProcRSSAnonBytes:         4 * 1024 * 1024,
		ProcRSSFileBytes:         8 * 1024 * 1024,
	}
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
