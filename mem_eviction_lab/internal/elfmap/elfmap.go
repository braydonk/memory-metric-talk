package elfmap

import (
	"cmp"
	"debug/elf"
	"fmt"
	"slices"
)

// Section describes a file-backed ELF section inside the Collector binary.
type Section struct {
	Name   string
	Offset uint64
	Size   uint64
}

// BinaryLayout maps binary file offsets to ELF section names.
type BinaryLayout struct {
	Sections []Section
}

// Parse opens an ELF binary on disk, validates that it is an executable or PIE/shared object,
// and records all non-empty file-backed sections sorted by file offset.
func Parse(path string) (*BinaryLayout, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ELF binary %q: %w", path, err)
	}
	defer f.Close()

	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return nil, fmt.Errorf("ELF binary %q has unsupported type %v (expected ET_EXEC or ET_DYN)", path, f.Type)
	}

	var sections []Section
	for _, s := range f.Sections {
		// SHT_NOBITS sections (.bss, .noptrbss) take zero bytes in the file; their Offset
		// overlaps subsequent sections in the binary and must not be treated as file-backed ranges.
		if s.Type == elf.SHT_NULL || s.Type == elf.SHT_NOBITS || s.Size == 0 || s.Name == "" {
			continue
		}
		sections = append(sections, Section{
			Name:   s.Name,
			Offset: s.Offset,
			Size:   s.Size,
		})
	}

	slices.SortFunc(sections, func(a, b Section) int {
		if c := cmp.Compare(a.Offset, b.Offset); c != 0 {
			return c
		}
		return cmp.Compare(b.Size, a.Size)
	})

	return &BinaryLayout{Sections: sections}, nil
}

// SectionForPage returns the section holding the largest byte overlap with [fileOffset, fileOffset+pageSize).
func (b *BinaryLayout) SectionForPage(fileOffset uint64, pageSize uint64) Section {
	if b == nil || len(b.Sections) == 0 {
		if fileOffset == 0 {
			return Section{Name: "[elf-headers]", Offset: 0, Size: pageSize}
		}
		return Section{Name: "[unattributed]", Offset: fileOffset, Size: pageSize}
	}

	pageEnd := fileOffset + pageSize

	// Binary search for the first section whose Offset >= pageEnd, then scan backward
	// over candidate sections that might overlap [fileOffset, pageEnd).
	hi, _ := slices.BinarySearchFunc(b.Sections, pageEnd, func(s Section, target uint64) int {
		return cmp.Compare(s.Offset, target)
	})

	var (
		best        Section
		bestOverlap uint64
	)
	for i := hi - 1; i >= 0; i-- {
		s := b.Sections[i]
		sEnd := s.Offset + s.Size
		if sEnd <= fileOffset {
			// Earlier sections can still have a huge size conceptually only if nested,
			// but standard ELF sections do not overlap one another in file offsets.
			// Keep scanning up to a small bound just in case of linker alignment quirks.
			if hi-1-i > 8 {
				break
			}
			continue
		}
		ovStart := max(fileOffset, s.Offset)
		ovEnd := min(pageEnd, sEnd)
		if ovEnd > ovStart {
			ov := ovEnd - ovStart
			if ov > bestOverlap {
				bestOverlap = ov
				best = s
			}
		}
	}

	if bestOverlap > 0 {
		return best
	}
	if fileOffset == 0 {
		return Section{Name: "[elf-headers]", Offset: 0, Size: pageSize}
	}
	return Section{Name: "[padding]", Offset: fileOffset, Size: pageSize}
}
