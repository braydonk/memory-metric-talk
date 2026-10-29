package pagemap

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/elfmap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

// Linux /proc/[pid]/pagemap 64-bit entry flags (Documentation/admin-guide/mm/pagemap.rst).
const (
	BitPresent  = uint64(1) << 63
	BitSwapped  = uint64(1) << 62
	BitFilePage = uint64(1) << 61
	PFNMask     = (uint64(1) << 55) - 1
)

// Source reads contiguous 64-bit /proc/[pid]/pagemap entries beginning at vaddr.
type Source interface {
	ReadEntries(vaddr uint64, dst []uint64) error
	Close() error
}

// ProcPagemapSource reads /proc/<pid>/pagemap via pread using a reusable scratch buffer.
type ProcPagemapSource struct {
	f        *os.File
	pageSize uint64
	buf      []byte
}

// OpenProcPagemap opens /proc/<pid>/pagemap for repeated low-overhead scanning.
func OpenProcPagemap(pid int, pageSize uint64) (*ProcPagemapSource, error) {
	p := filepath.Join("/proc", strconv.Itoa(pid), "pagemap")
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	return &ProcPagemapSource{
		f:        f,
		pageSize: pageSize,
	}, nil
}

// ReadEntries populates dst with 64-bit pagemap entries starting at virtual address vaddr.
func (s *ProcPagemapSource) ReadEntries(vaddr uint64, dst []uint64) error {
	if len(dst) == 0 {
		return nil
	}
	needBytes := len(dst) * 8
	if cap(s.buf) < needBytes {
		s.buf = make([]byte, needBytes)
	}
	buf := s.buf[:needBytes]

	fileOffset := int64((vaddr / s.pageSize) * 8)
	fd := int(s.f.Fd())
	read := 0
	for read < needBytes {
		n, err := syscall.Pread(fd, buf[read:], fileOffset+int64(read))
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		read += n
	}

	for i := range dst {
		dst[i] = binary.LittleEndian.Uint64(buf[i*8 : i*8+8])
	}
	return nil
}

// Close closes the underlying /proc/[pid]/pagemap file descriptor.
func (s *ProcPagemapSource) Close() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

type pageState struct {
	fileOffset uint64
	section    elfmap.Section
	vmaPerms   string
	segClass   SegmentClass

	everPresent    bool
	everEvicted    bool
	prevPresent    bool
	prevSwapped    bool
	prevFileBacked bool
	prevPFN        uint64

	evictCount      uint32
	refault01Count  uint32
	refaultPFNCount uint32
}

type vmaSpan struct {
	vma       VMA
	pageStart int
	pageCount int
}

// Tracker tracks per-page residency, eviction, and refault history across a Collector process lifetime.
type Tracker struct {
	src            Source
	pageSize       uint64
	blockSizePages int
	spans          []vmaSpan
	pages          []pageState
	entryBuf       []uint64

	cumColdFaults  uint64
	cumEvictions   uint64
	cumRefaults01  uint64
	cumRefaultsPFN uint64
}

// NewTracker constructs a Tracker over the Collector binary's VMAs.
func NewTracker(vmas []VMA, layout *elfmap.BinaryLayout, pageSize uint64, blockSizePages int, src Source) (*Tracker, error) {
	if len(vmas) == 0 {
		return nil, errors.New("pagemap: no binary VMAs provided")
	}
	if pageSize == 0 {
		return nil, errors.New("pagemap: pageSize must be > 0")
	}
	if blockSizePages < 1 {
		blockSizePages = 64
	}

	var totalPages uint64
	var maxSpan uint64
	for _, v := range vmas {
		np := v.NumPages(pageSize)
		totalPages += np
		if np > maxSpan {
			maxSpan = np
		}
	}
	if totalPages == 0 {
		return nil, errors.New("pagemap: binary VMAs contain 0 pages")
	}

	pages := make([]pageState, 0, totalPages)
	spans := make([]vmaSpan, 0, len(vmas))
	for _, v := range vmas {
		np := int(v.NumPages(pageSize))
		if np == 0 {
			continue
		}
		startIdx := len(pages)
		for i := 0; i < np; i++ {
			foff := v.FileOffset + uint64(i)*pageSize
			sec := layout.SectionForPage(foff, pageSize)
			pages = append(pages, pageState{
				fileOffset: foff,
				section:    sec,
				vmaPerms:   v.Perms,
				segClass:   v.Class,
			})
		}
		spans = append(spans, vmaSpan{
			vma:       v,
			pageStart: startIdx,
			pageCount: np,
		})
	}

	return &Tracker{
		src:            src,
		pageSize:       pageSize,
		blockSizePages: blockSizePages,
		spans:          spans,
		pages:          pages,
		entryBuf:       make([]uint64, totalPages),
	}, nil
}

// TotalPages returns the total number of mapped binary virtual pages tracked.
func (t *Tracker) TotalPages() uint64 {
	return uint64(len(t.pages))
}

// TotalMappedBytes returns TotalPages * pageSize.
func (t *Tracker) TotalMappedBytes() uint64 {
	return uint64(len(t.pages)) * t.pageSize
}

// Close closes the underlying pagemap Source.
func (t *Tracker) Close() error {
	if t.src == nil {
		return nil
	}
	return t.src.Close()
}

// Sample scans all tracked binary VMAs via Source, updates per-page transition state,
// and populates the binary page columns of row.
//
// If Source.ReadEntries fails partway through (e.g. process exited mid-scan via ESRCH/EOF),
// Sample does NOT commit partial state mutations to Tracker.pages so the last complete
// live snapshot remains intact for SectionRows and BlockRows.
func (t *Tracker) Sample(row *results.SampleRow) error {
	// First fetch all entries before mutating state so process exit mid-read cannot corrupt
	// the preserved final snapshot.
	allEntries := t.entryBuf
	total := len(t.pages)
	if cap(allEntries) < total {
		allEntries = make([]uint64, total)
		t.entryBuf = allEntries
	} else {
		allEntries = allEntries[:total]
	}

	for _, sp := range t.spans {
		dst := allEntries[sp.pageStart : sp.pageStart+sp.pageCount]
		if err := t.src.ReadEntries(sp.vma.StartAddr, dst); err != nil {
			return err
		}
	}

	var (
		deltaColdFaults  uint64
		deltaEvictions   uint64
		deltaRefaults01  uint64
		deltaRefaultsPFN uint64
	)

	row.BinTotalPages = uint64(total)

	for i := range t.pages {
		p := &t.pages[i]
		entry := allEntries[i]

		present := entry&BitPresent != 0
		swapped := entry&BitSwapped != 0
		fileBacked := entry&BitFilePage != 0
		var pfn uint64
		if present {
			pfn = entry & PFNMask
		}

		switch {
		case !p.prevPresent && present:
			if !p.everPresent {
				p.everPresent = true
				deltaColdFaults++
			} else {
				deltaRefaults01++
				p.refault01Count++
			}
		case p.prevPresent && !present:
			p.everEvicted = true
			p.evictCount++
			deltaEvictions++
		case p.prevPresent && present && fileBacked && p.prevFileBacked && pfn != 0 && p.prevPFN != 0 && pfn != p.prevPFN:
			p.everEvicted = true
			p.evictCount++
			p.refaultPFNCount++
			deltaEvictions++
			deltaRefaultsPFN++
		}

		p.prevPresent = present
		p.prevSwapped = swapped
		p.prevFileBacked = fileBacked
		p.prevPFN = pfn

		refaults := uint64(p.refault01Count) + uint64(p.refaultPFNCount)

		if present {
			if fileBacked {
				row.BinPresentFilePages++
			} else {
				row.BinPresentCoWPages++
			}
		} else if p.everPresent {
			row.BinEvictedOutPages++
			if refaults == 0 {
				row.BinCleanlyEvictedPages++
			}
		}
		if swapped {
			row.BinSwappedPages++
		}
		if !p.everPresent {
			row.BinNeverFaultedPages++
		} else {
			row.BinEverFaultedPages++
		}
		if p.everEvicted {
			row.BinEverEvictedPages++
		}
		if refaults >= 1 {
			row.BinEverRefaultedPages++
		}
		if refaults >= 2 {
			row.BinThrashingPages++
		}

		switch p.segClass {
		case ClassRO:
			if present {
				row.ROPresentPages++
			} else if p.everPresent {
				row.ROEvictedOutPages++
			}
			row.ROCumRefaults += refaults
		case ClassRX:
			if present {
				row.RXPresentPages++
			} else if p.everPresent {
				row.RXEvictedOutPages++
			}
			row.RXCumRefaults += refaults
		case ClassRW:
			if present {
				if fileBacked {
					row.RWPresentFilePages++
				} else {
					row.RWPresentCoWPages++
				}
			} else if p.everPresent {
				row.RWEvictedOutPages++
			}
			row.RWCumRefaults += refaults
		}
	}

	t.cumColdFaults += deltaColdFaults
	t.cumEvictions += deltaEvictions
	t.cumRefaults01 += deltaRefaults01
	t.cumRefaultsPFN += deltaRefaultsPFN

	row.DeltaColdFaults = deltaColdFaults
	row.DeltaEvictions = deltaEvictions
	row.DeltaRefaults01 = deltaRefaults01
	row.DeltaRefaultsPFN = deltaRefaultsPFN
	row.DeltaRefaultsTotal = deltaRefaults01 + deltaRefaultsPFN

	row.CumColdFaults = t.cumColdFaults
	row.CumEvictions = t.cumEvictions
	row.CumRefaults01 = t.cumRefaults01
	row.CumRefaultsPFN = t.cumRefaultsPFN
	row.CumRefaultsTotal = t.cumRefaults01 + t.cumRefaultsPFN

	return nil
}

// SectionRows aggregates final page histories grouped by (ELF section name, VMA perms).
func (t *Tracker) SectionRows() []results.SectionRow {
	type secKey struct {
		name  string
		perms string
	}
	m := make(map[secKey]*results.SectionRow)
	for i := range t.pages {
		p := &t.pages[i]
		k := secKey{name: p.section.Name, perms: p.vmaPerms}
		r, ok := m[k]
		if !ok {
			r = &results.SectionRow{
				SectionName:     p.section.Name,
				VMAPerms:        p.vmaPerms,
				FileOffsetBytes: p.section.Offset,
				SizeBytes:       p.section.Size,
			}
			m[k] = r
		}
		if p.section.Name == "[padding]" && p.fileOffset < r.FileOffsetBytes {
			r.FileOffsetBytes = p.fileOffset
		}

		r.TotalPages++
		refaults := uint64(p.refault01Count) + uint64(p.refaultPFNCount)
		if !p.everPresent {
			r.NeverFaultedPages++
		} else if p.prevPresent {
			if p.prevFileBacked {
				r.PresentFinalPages++
			} else {
				r.PresentCoWFinalPages++
			}
			if refaults > 0 {
				r.ResidentAfterRefaultPages++
			}
		} else {
			if refaults == 0 {
				r.CleanlyEvictedPages++
			} else {
				r.EvictedAfterRefaultPages++
			}
		}
		if refaults >= 2 {
			r.ThrashingPages++
		}
		r.TotalEvictions += uint64(p.evictCount)
		r.TotalRefaults01 += uint64(p.refault01Count)
		r.TotalRefaultsPFN += uint64(p.refaultPFNCount)
		r.TotalRefaults += refaults
	}

	rows := make([]results.SectionRow, 0, len(m))
	for _, r := range m {
		if r.SectionName == "[padding]" || r.SectionName == "[unattributed]" {
			r.SizeBytes = r.TotalPages * t.pageSize
		}
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b results.SectionRow) int {
		if c := cmp.Compare(a.FileOffsetBytes, b.FileOffsetBytes); c != 0 {
			return c
		}
		return cmp.Compare(a.SectionName, b.SectionName)
	})
	return rows
}

// BlockRows aggregates final page histories into contiguous binary file-offset buckets.
func (t *Tracker) BlockRows() []results.BlockRow {
	blockBytes := uint64(t.blockSizePages) * t.pageSize
	type blockAgg struct {
		row        results.BlockRow
		secCounts  map[string]uint64
		permCounts map[string]uint64
	}
	m := make(map[int]*blockAgg)
	for i := range t.pages {
		p := &t.pages[i]
		idx := int(p.fileOffset / blockBytes)
		b, ok := m[idx]
		if !ok {
			start := uint64(idx) * blockBytes
			b = &blockAgg{
				row: results.BlockRow{
					BlockIndex:           idx,
					FileOffsetStartBytes: start,
					FileOffsetEndBytes:   start + blockBytes,
				},
				secCounts:  make(map[string]uint64),
				permCounts: make(map[string]uint64),
			}
			m[idx] = b
		}
		b.secCounts[p.section.Name]++
		b.permCounts[p.vmaPerms]++

		r := &b.row
		r.TotalPages++
		refaults := uint64(p.refault01Count) + uint64(p.refaultPFNCount)
		if !p.everPresent {
			r.NeverFaultedPages++
		} else if p.prevPresent {
			if p.prevFileBacked {
				r.PresentFinalPages++
			} else {
				r.PresentCoWFinalPages++
			}
		} else if refaults == 0 {
			r.CleanlyEvictedPages++
		}
		if refaults >= 1 {
			r.EverRefaultedPages++
		}
		if refaults >= 2 {
			r.ThrashingPages++
		}
		r.TotalEvictions += uint64(p.evictCount)
		r.TotalRefaults += refaults
	}

	rows := make([]results.BlockRow, 0, len(m))
	for _, b := range m {
		b.row.PrimarySection = topKey(b.secCounts)
		b.row.VMAPerms = topKey(b.permCounts)
		rows = append(rows, b.row)
	}
	slices.SortFunc(rows, func(a, b results.BlockRow) int {
		return cmp.Compare(a.BlockIndex, b.BlockIndex)
	})
	return rows
}

func topKey(m map[string]uint64) string {
	var best string
	var bestN uint64
	for k, n := range m {
		if n > bestN || (n == bestN && k < best) {
			best = k
			bestN = n
		}
	}
	return best
}
