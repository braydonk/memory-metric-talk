package pagemap

import (
	"testing"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/elfmap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

func TestPageRowsPerPageFates(t *testing.T) {
	vmas := []VMA{{StartAddr: 0x20000, EndAddr: 0x24000, Perms: "r-xp", Class: ClassRX, FileOffset: 0x2000}}
	layout := &elfmap.BinaryLayout{Sections: []elfmap.Section{{Name: ".text", Offset: 0x2000, Size: 0x4000}}}
	src := &fakeSource{mem: map[uint64]uint64{}}
	tr, err := NewTracker(vmas, layout, 4096, 64, src)
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	sample := func(mem map[uint64]uint64) {
		t.Helper()
		src.mem = mem
		var row results.SampleRow
		if err := tr.Sample(&row); err != nil {
			t.Fatalf("Sample: %v", err)
		}
	}

	// Page 0 never faults; page 1 stays resident; page 2 is evicted cleanly; page 3 is evicted and refaults.
	sample(map[uint64]uint64{0x21000: fileEntry(1), 0x22000: fileEntry(2), 0x23000: fileEntry(3)})
	sample(map[uint64]uint64{0x21000: fileEntry(1)})
	sample(map[uint64]uint64{0x21000: fileEntry(1), 0x23000: fileEntry(4)})

	rows := tr.PageRows()
	if len(rows) != 4 {
		t.Fatalf("len(PageRows) = %d; want 4", len(rows))
	}
	type fate struct{ never, resident, clean, refaulted uint64 }
	want := []fate{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 1, 0, 1}}
	for i, r := range rows {
		if r.BlockIndex != 2+i || r.FileOffsetStartBytes != uint64(0x2000+i*4096) || r.PrimarySection != ".text" {
			t.Errorf("page %d: unexpected position %+v", i, r)
		}
		got := fate{r.NeverFaultedPages, r.PresentFinalPages, r.CleanlyEvictedPages, r.EverRefaultedPages}
		if got != want[i] {
			t.Errorf("page %d: fate %+v; want %+v", i, got, want[i])
		}
	}
}
