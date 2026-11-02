package symbols

import (
	"debug/elf"
	"os"
	"testing"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

const pageSize = 4096

// openSelf maps the test binary, which is itself a Go binary.
func openSelf(t *testing.T) (*Map, *elf.Section) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m, err := Open(exe, pageSize)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return m, f.Section(".text")
}

func TestOpen(t *testing.T) {
	t.Parallel()
	m, text := openSelf(t)

	var mapped uint64
	names := map[string]bool{}
	for _, p := range m.Packages {
		mapped += p.MappedBytes
		names[p.Name] = true
	}
	if mapped == 0 || mapped > text.Size {
		t.Errorf("mapped bytes = %d, want in (0, %d]", mapped, text.Size)
	}
	for _, want := range []string{"runtime", "github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/symbols"} {
		if !names[want] {
			t.Errorf("package %q not found", want)
		}
	}

	if len(m.Shares)%3 != 0 {
		t.Fatalf("len(Shares) = %d, want a multiple of 3", len(m.Shares))
	}
	perPage := map[uint32]uint64{}
	var shared uint64
	for i := 0; i < len(m.Shares); i += 3 {
		if i > 0 && m.Shares[i] < m.Shares[i-3] {
			t.Fatalf("shares not sorted by page at triple %d", i/3)
		}
		perPage[m.Shares[i]] += uint64(m.Shares[i+2])
		shared += uint64(m.Shares[i+2])
	}
	if shared != mapped {
		t.Errorf("sum of shares = %d, want mapped bytes %d", shared, mapped)
	}
	for page, n := range perPage {
		if n > pageSize {
			t.Errorf("page %d holds %d bytes, want <= %d", page, n, pageSize)
		}
	}
}

func TestAttribute(t *testing.T) {
	t.Parallel()
	m, text := openSelf(t)

	// textPages builds one pages.csv row per .text page with the given fate.
	textPages := func(fate results.BlockRow) []results.BlockRow {
		var rows []results.BlockRow
		for off := text.Offset &^ (pageSize - 1); off < text.Offset+text.Size; off += pageSize {
			r := fate
			r.FileOffsetStartBytes = off
			rows = append(rows, r)
		}
		return rows
	}

	tests := []struct {
		name  string
		pages []results.BlockRow
		want  func(PackageStats) (got, want uint64)
	}{
		{
			name:  "no pages recorded",
			pages: nil,
			want:  func(s PackageStats) (uint64, uint64) { return s.FaultedBytes, 0 },
		},
		{
			name:  "never faulted",
			pages: textPages(results.BlockRow{NeverFaultedPages: 1}),
			want:  func(s PackageStats) (uint64, uint64) { return s.FaultedBytes, 0 },
		},
		{
			name:  "all cleanly evicted",
			pages: textPages(results.BlockRow{CleanlyEvictedPages: 1}),
			want:  func(s PackageStats) (uint64, uint64) { return s.CleanlyEvictedBytes, s.MappedBytes },
		},
		{
			name:  "all refaulted and resident",
			pages: textPages(results.BlockRow{EverRefaultedPages: 1, PresentFinalPages: 1}),
			want: func(s PackageStats) (uint64, uint64) {
				return s.RefaultedBytes + s.FinalResidentBytes, 2 * s.MappedBytes
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stats := m.Attribute(tt.pages)
			if len(stats) != len(m.Packages) {
				t.Fatalf("got %d packages, want %d", len(stats), len(m.Packages))
			}
			for _, s := range stats {
				if got, want := tt.want(s); got != want {
					t.Fatalf("%s: got %d, want %d", s.Name, got, want)
				}
			}
		})
	}
}

func TestOpenNotGoBinary(t *testing.T) {
	t.Parallel()
	if _, err := Open("/proc/self/status", pageSize); err == nil {
		t.Fatal("Open of a non-ELF file succeeded, want error")
	}
}
