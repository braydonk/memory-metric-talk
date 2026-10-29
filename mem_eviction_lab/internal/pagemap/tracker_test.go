package pagemap

import (
	"errors"
	"testing"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/elfmap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

type fakeSource struct {
	mem map[uint64]uint64
	err error
}

func (f *fakeSource) ReadEntries(vaddr uint64, dst []uint64) error {
	if f.err != nil {
		return f.err
	}
	for i := range dst {
		dst[i] = f.mem[vaddr+uint64(i)*4096]
	}
	return nil
}

func (f *fakeSource) Close() error { return nil }

func fileEntry(pfn uint64) uint64 {
	return BitPresent | BitFilePage | (pfn & PFNMask)
}

func cowEntry(pfn uint64) uint64 {
	return BitPresent | (pfn & PFNMask)
}

func TestParseBinaryVMAsHexDevAndInode(t *testing.T) {
	mapsText := []byte(`55a000000000-55a000002000 r--p 00000000 fe:01 998877 /opt/otelcol-contrib
55a000002000-55a000005000 r-xp 00002000 fe:01 998877 /opt/otelcol-contrib
55a000005000-55a000006000 rw-p 00005000 fe:01 998877 /opt/otelcol-contrib
55a000006000-55a000010000 rw-p 00000000 00:00 0 
7f1122000000-7f1122010000 r-xp 00000000 fe:01 112233 /lib/x86_64-linux-gnu/libc.so.6
`)
	// 0xfe == 254, 0x01 == 1
	vmas, err := ParseBinaryVMAs(mapsText, 254, 1, 998877)
	if err != nil {
		t.Fatalf("ParseBinaryVMAs: %v", err)
	}
	if len(vmas) != 3 {
		t.Fatalf("len(vmas) = %d; want 3", len(vmas))
	}
	if vmas[0].Class != ClassRO || vmas[1].Class != ClassRX || vmas[2].Class != ClassRW {
		t.Fatalf("unexpected segment classes: %+v", vmas)
	}
	if vmas[1].FileOffset != 0x2000 || vmas[1].NumPages(4096) != 3 {
		t.Fatalf("unexpected r-xp VMA: %+v", vmas[1])
	}
}

func TestTrackerTransitionsAndRollups(t *testing.T) {
	vmas := []VMA{
		{StartAddr: 0x10000, EndAddr: 0x12000, Perms: "r--p", Class: ClassRO, FileOffset: 0x0000}, // 2 pages: 0x10000, 0x11000
		{StartAddr: 0x20000, EndAddr: 0x22000, Perms: "r-xp", Class: ClassRX, FileOffset: 0x2000}, // 2 pages: 0x20000, 0x21000
		{StartAddr: 0x30000, EndAddr: 0x32000, Perms: "rw-p", Class: ClassRW, FileOffset: 0x4000}, // 2 pages: 0x30000, 0x31000
	}
	layout := &elfmap.BinaryLayout{
		Sections: []elfmap.Section{
			{Name: ".rodata", Offset: 0x0000, Size: 0x2000},
			{Name: ".text", Offset: 0x2000, Size: 0x2000},
			{Name: ".data", Offset: 0x4000, Size: 0x2000},
		},
	}

	src := &fakeSource{mem: map[uint64]uint64{}}
	tr, err := NewTracker(vmas, layout, 4096, 2, src)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}

	// Sample 1 (start): fault in 0x10000 (ro file), 0x20000 (rx file), 0x21000 (rx file),
	// 0x30000 (rw file), and 0x31000 (rw CoW anon). Leave 0x11000 never faulted.
	src.mem[0x10000] = fileEntry(100)
	src.mem[0x20000] = fileEntry(200)
	src.mem[0x21000] = fileEntry(201)
	src.mem[0x30000] = fileEntry(300)
	src.mem[0x31000] = cowEntry(301)

	var s1 results.SampleRow
	if err := tr.Sample(&s1); err != nil {
		t.Fatalf("Sample 1: %v", err)
	}
	if s1.DeltaColdFaults != 5 || s1.BinPresentFilePages != 4 || s1.BinPresentCoWPages != 1 || s1.BinNeverFaultedPages != 1 {
		t.Fatalf("unexpected sample 1: %+v", s1)
	}

	// Sample 2 (reclaim):
	// - 0x10000 evicted (1->0)
	// - 0x20000 stays hot at same PFN 200 (no transition)
	// - 0x21000 evicted AND immediately refaulted at new PFN 999 (sub-poll PFN refault!)
	// - 0x30000 evicted (1->0)
	delete(src.mem, 0x10000)
	src.mem[0x21000] = fileEntry(999)
	delete(src.mem, 0x30000)

	var s2 results.SampleRow
	if err := tr.Sample(&s2); err != nil {
		t.Fatalf("Sample 2: %v", err)
	}
	// Evictions: 0x10000 + 0x30000 + implicit 0x21000 = 3; PFN refaults = 1
	if s2.DeltaEvictions != 3 || s2.DeltaRefaultsPFN != 1 || s2.DeltaRefaults01 != 0 {
		t.Fatalf("unexpected sample 2 deltas: %+v", s2)
	}
	if s2.BinCleanlyEvictedPages != 2 { // 0x10000 and 0x30000 evicted with 0 refaults
		t.Fatalf("BinCleanlyEvictedPages = %d; want 2", s2.BinCleanlyEvictedPages)
	}

	// Sample 3:
	// - 0x30000 faults back in (0->1 refault!)
	// - 0x21000 experiences a second PFN change -> becomes a thrashing page (refaults >= 2)!
	src.mem[0x30000] = fileEntry(450)
	src.mem[0x21000] = fileEntry(1000)

	var s3 results.SampleRow
	if err := tr.Sample(&s3); err != nil {
		t.Fatalf("Sample 3: %v", err)
	}
	if s3.DeltaRefaults01 != 1 || s3.DeltaRefaultsPFN != 1 || s3.BinThrashingPages != 1 || s3.BinCleanlyEvictedPages != 1 {
		t.Fatalf("unexpected sample 3: %+v", s3)
	}

	// Simulate process dying before Sample 4: verify Tracker preserves Sample 3 state for rollups.
	src.err = errors.New("ESRCH")
	var s4 results.SampleRow
	if err := tr.Sample(&s4); err == nil {
		t.Fatal("expected error on Sample 4")
	}

	secs := tr.SectionRows()
	if len(secs) != 3 {
		t.Fatalf("len(secs) = %d; want 3", len(secs))
	}
	// .rodata: 1 never faulted (0x11000), 1 cleanly evicted (0x10000)
	if secs[0].SectionName != ".rodata" || secs[0].NeverFaultedPages != 1 || secs[0].CleanlyEvictedPages != 1 {
		t.Fatalf("unexpected .rodata row: %+v", secs[0])
	}
	// .text: 2 present final, 1 resident after refault, 1 thrashing, 2 PFN refaults
	if secs[1].SectionName != ".text" || secs[1].PresentFinalPages != 2 || secs[1].ThrashingPages != 1 || secs[1].TotalRefaultsPFN != 2 {
		t.Fatalf("unexpected .text row: %+v", secs[1])
	}
	// .data: 1 present file (refaulted once), 1 present CoW
	if secs[2].SectionName != ".data" || secs[2].PresentFinalPages != 1 || secs[2].PresentCoWFinalPages != 1 || secs[2].TotalRefaults01 != 1 {
		t.Fatalf("unexpected .data row: %+v", secs[2])
	}

	blks := tr.BlockRows()
	if len(blks) != 3 {
		t.Fatalf("len(blks) = %d; want 3", len(blks))
	}
}
