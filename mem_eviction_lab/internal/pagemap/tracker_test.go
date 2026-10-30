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
	// - 0x21000 experiences a second PFN change (simple 2-cycle correction: NOT yet thrashing!)
	src.mem[0x30000] = fileEntry(450)
	src.mem[0x21000] = fileEntry(1000)

	s3 := results.SampleRow{ElapsedMs: 1000}
	if err := tr.Sample(&s3); err != nil {
		t.Fatalf("Sample 3: %v", err)
	}
	if s3.DeltaRefaults01 != 1 || s3.DeltaRefaultsPFN != 1 || s3.BinThrashingPages != 0 || s3.BinCleanlyEvictedPages != 1 {
		t.Fatalf("unexpected sample 3 (expected 0 thrashing pages on 2 refaults): %+v", s3)
	}

	// Samples 4 & 5: 0x21000 keeps churning continuously (3rd and 4th rapid PFN refaults) -> enters active thrashing!
	src.mem[0x21000] = fileEntry(1001)
	s4 := results.SampleRow{ElapsedMs: 1500}
	if err := tr.Sample(&s4); err != nil {
		t.Fatalf("Sample 4: %v", err)
	}
	if s4.BinThrashingPages != 0 {
		t.Fatalf("expected 0 thrashing pages at streak 3, got %d", s4.BinThrashingPages)
	}

	src.mem[0x21000] = fileEntry(1002)
	s5 := results.SampleRow{ElapsedMs: 2000}
	if err := tr.Sample(&s5); err != nil {
		t.Fatalf("Sample 5: %v", err)
	}
	if s5.BinThrashingPages != 1 {
		t.Fatalf("expected 1 actively thrashing page at streak %d, got %d", MinThrashingStreak, s5.BinThrashingPages)
	}

	// Simulate process dying before Sample 6: verify Tracker preserves Sample 5 state for rollups.
	src.err = errors.New("ESRCH")
	s6 := results.SampleRow{ElapsedMs: 2500}
	if err := tr.Sample(&s6); err == nil {
		t.Fatal("expected error on Sample 6")
	}

	secs := tr.SectionRows()
	if len(secs) != 3 {
		t.Fatalf("len(secs) = %d; want 3", len(secs))
	}
	// .rodata: 1 never faulted (0x11000), 1 cleanly evicted (0x10000)
	if secs[0].SectionName != ".rodata" || secs[0].NeverFaultedPages != 1 || secs[0].CleanlyEvictedPages != 1 {
		t.Fatalf("unexpected .rodata row: %+v", secs[0])
	}
	// .text: 2 present final, 1 resident after refault, 1 actively thrashing, 4 PFN refaults
	if secs[1].SectionName != ".text" || secs[1].PresentFinalPages != 2 || secs[1].ThrashingPages != 1 || secs[1].TotalRefaultsPFN != 4 {
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

func TestSampleWithDeltasAndLayoutSpans(t *testing.T) {
	vmas := []VMA{
		{StartAddr: 0x10000, EndAddr: 0x12000, Perms: "r--p", Class: ClassRO, FileOffset: 0x0000}, // pages 0, 1 (.rodata)
		{StartAddr: 0x20000, EndAddr: 0x22000, Perms: "r-xp", Class: ClassRX, FileOffset: 0x2000}, // pages 2, 3 (.text)
		{StartAddr: 0x30000, EndAddr: 0x32000, Perms: "rw-p", Class: ClassRW, FileOffset: 0x4000}, // pages 4, 5 (.data)
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

	spans := tr.LayoutSpans()
	if len(spans) != 3 {
		t.Fatalf("len(spans) = %d; want 3 (%+v)", len(spans), spans)
	}
	if spans[0].SectionName != ".rodata" || spans[0].StartPage != 0 || spans[0].PageCount != 2 ||
		spans[1].SectionName != ".text" || spans[1].StartPage != 2 || spans[1].PageCount != 2 ||
		spans[2].SectionName != ".data" || spans[2].StartPage != 4 || spans[2].PageCount != 2 {
		t.Fatalf("unexpected LayoutSpans: %+v", spans)
	}

	decode := func(ds []uint32) map[uint32]PageVisualCode {
		m := make(map[uint32]PageVisualCode, len(ds))
		for _, d := range ds {
			idx, code := UnpackDelta(d)
			m[idx] = code
		}
		return m
	}

	// Tick 1: cold file faults on pages 0, 2, 3, 4; cold CoW fault on page 5.
	src.mem[0x10000] = fileEntry(10)
	src.mem[0x20000] = fileEntry(20)
	src.mem[0x21000] = fileEntry(21)
	src.mem[0x30000] = fileEntry(30)
	src.mem[0x31000] = cowEntry(31)

	var r1 results.SampleRow
	deltas, err := tr.SampleWithDeltas(&r1, nil)
	if err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	m1 := decode(deltas)
	if len(m1) != 5 ||
		m1[0] != CodeColdFaultFile ||
		m1[2] != CodeColdFaultFile ||
		m1[3] != CodeColdFaultFile ||
		m1[4] != CodeColdFaultFile ||
		m1[5] != CodeColdFaultCoW {
		t.Fatalf("unexpected Tick 1 deltas: %+v", m1)
	}

	// Tick 2: no changes -> 0 deltas.
	var r2 results.SampleRow
	deltas, err = tr.SampleWithDeltas(&r2, deltas)
	if err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("expected 0 deltas on idle Tick 2, got %d", len(deltas))
	}

	// Tick 3: page 0 cleanly evicted; page 3 PFN-refaulted once; page 4 promoted file->CoW; page 5 swapped out.
	delete(src.mem, 0x10000)
	src.mem[0x21000] = fileEntry(222)
	src.mem[0x30000] = cowEntry(300)
	src.mem[0x31000] = BitSwapped

	var r3 results.SampleRow
	deltas, err = tr.SampleWithDeltas(&r3, deltas)
	if err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	m3 := decode(deltas)
	if len(m3) != 4 ||
		m3[0] != CodeEvictedClean ||
		m3[3] != CodeRefaultFileOnce ||
		m3[4] != CodePromotedCoW ||
		m3[5] != CodeSwappedOut {
		t.Fatalf("unexpected Tick 3 deltas: %+v", m3)
	}

	// Tick 4: page 0 refaults (1st time); page 3 PFN-refaults (2nd time: still settled refault code); page 5 faults back as CoW.
	src.mem[0x10000] = fileEntry(11)
	src.mem[0x21000] = fileEntry(333)
	src.mem[0x31000] = cowEntry(310)

	var r4 results.SampleRow
	deltas, err = tr.SampleWithDeltas(&r4, deltas)
	if err != nil {
		t.Fatalf("Tick 4: %v", err)
	}
	m4 := decode(deltas)
	if len(m4) != 3 ||
		m4[0] != CodeRefaultFileOnce ||
		m4[3] != CodeRefaultFileOnce ||
		m4[5] != CodeRefaultCoW {
		t.Fatalf("unexpected Tick 4 deltas: %+v", m4)
	}

	// Ticks 5 & 6: page 3 churns rapidly through 3rd and 4th PFN refault -> transitions to CodeRefaultFileThrash on 4th.
	src.mem[0x21000] = fileEntry(444)
	var r5a results.SampleRow
	deltas, err = tr.SampleWithDeltas(&r5a, deltas)
	if err != nil {
		t.Fatalf("Tick 5a: %v", err)
	}
	if m5a := decode(deltas); len(m5a) != 1 || m5a[3] != CodeRefaultFileOnce {
		t.Fatalf("unexpected Tick 5a deltas: %+v", m5a)
	}

	src.mem[0x21000] = fileEntry(555)
	var r5b results.SampleRow
	deltas, err = tr.SampleWithDeltas(&r5b, deltas)
	if err != nil {
		t.Fatalf("Tick 5b: %v", err)
	}
	if m5b := decode(deltas); len(m5b) != 1 || m5b[3] != CodeRefaultFileThrash {
		t.Fatalf("unexpected Tick 5b deltas: %+v", m5b)
	}

	// Tick 7: evict page 0 (evicted after settled refault) and page 3 (evicted while actively thrashing).
	delete(src.mem, 0x10000)
	delete(src.mem, 0x21000)

	var r5 results.SampleRow
	deltas, err = tr.SampleWithDeltas(&r5, deltas)
	if err != nil {
		t.Fatalf("Tick 7: %v", err)
	}
	m5 := decode(deltas)
	if len(m5) != 2 || m5[0] != CodeEvictedRefaulted || m5[3] != CodeEvictedThrashing {
		t.Fatalf("unexpected Tick 7 deltas: %+v", m5)
	}
}

func TestTrackerChurnCooldownVsContinuousChurn(t *testing.T) {
	vmas := []VMA{
		{StartAddr: 0x20000, EndAddr: 0x21000, Perms: "r-xp", Class: ClassRX, FileOffset: 0x0000},
	}
	layout := &elfmap.BinaryLayout{
		Sections: []elfmap.Section{{Name: ".text", Offset: 0x0000, Size: 0x1000}},
	}
	src := &fakeSource{mem: map[uint64]uint64{0x20000: fileEntry(10)}}
	tr, err := NewTracker(vmas, layout, 4096, 1, src)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}

	var deltas []uint32
	step := func(ms int64) results.SampleRow {
		t.Helper()
		r := results.SampleRow{ElapsedMs: ms}
		var serr error
		deltas, serr = tr.SampleWithDeltas(&r, deltas)
		if serr != nil {
			t.Fatalf("Sample at %dms: %v", ms, serr)
		}
		return r
	}

	// Initial cold fault.
	step(0)

	// 1. Five isolated manual memory.reclaim corrections spaced 20s apart (> ThrashQuietWindowMs).
	// Even with 5 lifetime refaults, the page stabilizes between reclaims and must NEVER be marked thrashing.
	for cycle := 0; cycle < 5; cycle++ {
		tEvict := int64(10_000 + cycle*20_000)
		delete(src.mem, 0x20000)
		rEv := step(tEvict)
		if rEv.BinThrashingPages != 0 {
			t.Fatalf("cycle %d evict: BinThrashingPages = %d; want 0", cycle, rEv.BinThrashingPages)
		}

		src.mem[0x20000] = fileEntry(uint64(100 + cycle))
		rRf := step(tEvict + 200)
		if rRf.BinThrashingPages != 0 {
			t.Fatalf("cycle %d refault: BinThrashingPages = %d; want 0", cycle, rRf.BinThrashingPages)
		}
		if len(deltas) != 1 {
			t.Fatalf("cycle %d expected 1 delta, got %d", cycle, len(deltas))
		}
		if _, code := UnpackDelta(deltas[0]); code != CodeRefaultFileOnce {
			t.Fatalf("cycle %d code = %v; want CodeRefaultFileOnce", cycle, code)
		}
	}

	// 2. Now drive continuous rapid churn (4 cycles spaced 1s apart after quiet reset).
	baseMs := int64(130_000)
	for cycle := 1; cycle <= MinThrashingStreak; cycle++ {
		delete(src.mem, 0x20000)
		step(baseMs + int64(cycle*1000))
		src.mem[0x20000] = fileEntry(uint64(500 + cycle))
		r := step(baseMs + int64(cycle*1000+200))
		wantThrash := uint64(0)
		wantCode := CodeRefaultFileOnce
		if cycle >= MinThrashingStreak {
			wantThrash = 1
			wantCode = CodeRefaultFileThrash
		}
		if r.BinThrashingPages != wantThrash {
			t.Fatalf("rapid cycle %d: BinThrashingPages = %d; want %d", cycle, r.BinThrashingPages, wantThrash)
		}
		if _, code := UnpackDelta(deltas[0]); code != wantCode {
			t.Fatalf("rapid cycle %d: code = %v; want %v", cycle, code, wantCode)
		}
	}

	// 3. Still inside quiet window (+10s): remains marked actively thrashing with 0 spurious deltas.
	lastChurnMs := baseMs + int64(MinThrashingStreak*1000+200)
	rMid := step(lastChurnMs + 10_000)
	if rMid.BinThrashingPages != 1 || len(deltas) != 0 {
		t.Fatalf("expected active thrashing inside quiet window, got thrash=%d deltas=%d", rMid.BinThrashingPages, len(deltas))
	}

	// 4. Past quiet window (+16s with zero evictions/refaults): cools down to settled Refaulted state!
	rCool := step(lastChurnMs + ThrashQuietWindowMs + 1000)
	if rCool.BinThrashingPages != 0 {
		t.Fatalf("expected BinThrashingPages=0 after cooldown, got %d", rCool.BinThrashingPages)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected 1 cooldown visual delta, got %d", len(deltas))
	}
	if idx, code := UnpackDelta(deltas[0]); idx != 0 || code != CodeRefaultFileOnce {
		t.Fatalf("unexpected cooldown delta idx=%d code=%v; want 0 / CodeRefaultFileOnce", idx, code)
	}
}
