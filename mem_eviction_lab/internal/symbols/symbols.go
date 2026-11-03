// Package symbols maps the .text pages of a Go binary to the Go packages whose
// functions they hold. It reads .gopclntab, which Go keeps even in stripped binaries.
package symbols

import (
	"cmp"
	"debug/elf"
	"debug/gosym"
	"errors"
	"fmt"
	"slices"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

// UnknownPackage groups functions without a Go package (linker-generated code).
const UnknownPackage = "[no package]"

// Package is one Go package's code in .text.
type Package struct {
	Name        string `json:"package"`
	Functions   int    `json:"functions"`
	MappedBytes uint64 `json:"mapped_bytes"`
}

// Map is a run-independent map from the file pages of a binary's .text to the
// packages whose code they hold.
type Map struct {
	PageSize uint64    `json:"page_size"`
	Packages []Package `json:"packages"`
	// Shares holds flat (filePage, packageIndex, bytes) triples sorted by file page.
	// A page holding code of several packages has one triple per package.
	Shares []uint32 `json:"shares"`
}

// Open builds the Map of the Go binary at path for pages of pageSize bytes.
func Open(path string, pageSize uint64) (*Map, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	text, pclntab := f.Section(".text"), f.Section(".gopclntab")
	if text == nil || pclntab == nil {
		return nil, errors.New("no .text or .gopclntab section, not a Go binary")
	}
	data, err := pclntab.Data()
	if err != nil {
		return nil, fmt.Errorf("read .gopclntab: %w", err)
	}
	tab, err := gosym.NewTable(nil, gosym.NewLineTable(data, text.Addr))
	if err != nil {
		return nil, fmt.Errorf("parse .gopclntab: %w", err)
	}

	m := &Map{PageSize: pageSize}
	index := make(map[string]uint32)
	textEnd := text.Addr + text.Size
	// Functions are sorted by address and do not overlap, so pages come in order.
	for _, fn := range tab.Funcs {
		start, end := fn.Entry, min(fn.End, textEnd)
		if start >= end {
			continue
		}
		name := cmp.Or(fn.PackageName(), UnknownPackage)
		pkg, ok := index[name]
		if !ok {
			pkg = uint32(len(m.Packages))
			index[name] = pkg
			m.Packages = append(m.Packages, Package{Name: name})
		}
		m.Packages[pkg].Functions++
		m.Packages[pkg].MappedBytes += end - start

		// File offset = .text.Offset + (address - .text.Addr).
		startOff, endOff := text.Offset+(start-text.Addr), text.Offset+(end-text.Addr)
		for page := startOff / pageSize; page*pageSize < endOff; page++ {
			n := min(endOff, (page+1)*pageSize) - max(startOff, page*pageSize)
			m.addShare(uint32(page), pkg, uint32(n))
		}
	}
	return m, nil
}

// addShare adds n bytes of package pkg to page, merging with an existing triple of
// the same page and package.
func (m *Map) addShare(page, pkg, n uint32) {
	for i := len(m.Shares) - 3; i >= 0 && m.Shares[i] == page; i -= 3 {
		if m.Shares[i+1] == pkg {
			m.Shares[i+2] += n
			return
		}
	}
	m.Shares = append(m.Shares, page, pkg, n)
}

// PackageStats is one package's code bytes by the fate of the pages holding them.
type PackageStats struct {
	Package
	FaultedBytes        uint64 `json:"faulted_bytes"`
	CleanlyEvictedBytes uint64 `json:"cleanly_evicted_bytes"`
	RefaultedBytes      uint64 `json:"refaulted_bytes"`
	FinalResidentBytes  uint64 `json:"final_resident_bytes"`
}

// Attribute rolls per-page fates (pages.csv rows) up into packages, sorted by
// cleanly evicted bytes, then faulted bytes, descending.
func (m *Map) Attribute(pages []results.BlockRow) []PackageStats {
	byPage := make(map[uint32]*results.BlockRow, len(pages))
	for i := range pages {
		byPage[uint32(pages[i].FileOffsetStartBytes/m.PageSize)] = &pages[i]
	}

	stats := make([]PackageStats, len(m.Packages))
	for i, p := range m.Packages {
		stats[i].Package = p
	}
	for i := 0; i < len(m.Shares); i += 3 {
		page, ok := byPage[m.Shares[i]]
		if !ok || page.NeverFaultedPages > 0 {
			continue
		}
		s, n := &stats[m.Shares[i+1]], uint64(m.Shares[i+2])
		s.FaultedBytes += n
		if page.CleanlyEvictedPages > 0 {
			s.CleanlyEvictedBytes += n
		}
		if page.EverRefaultedPages > 0 {
			s.RefaultedBytes += n
		}
		if page.PresentFinalPages+page.PresentCoWFinalPages > 0 {
			s.FinalResidentBytes += n
		}
	}

	slices.SortFunc(stats, func(a, b PackageStats) int {
		return cmp.Or(
			cmp.Compare(b.CleanlyEvictedBytes, a.CleanlyEvictedBytes),
			cmp.Compare(b.FaultedBytes, a.FaultedBytes),
			cmp.Compare(a.Name, b.Name),
		)
	})
	return stats
}
