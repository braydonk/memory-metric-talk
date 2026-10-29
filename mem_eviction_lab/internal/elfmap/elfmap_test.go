package elfmap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSelfExe(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	layout, err := Parse(exe)
	if err != nil {
		t.Fatalf("Parse(%q): %v", exe, err)
	}
	if len(layout.Sections) == 0 {
		t.Fatal("expected non-empty ELF sections")
	}

	var foundText, foundRodata bool
	for _, s := range layout.Sections {
		switch s.Name {
		case ".text":
			foundText = true
			got := layout.SectionForPage(s.Offset, 4096)
			if got.Name != ".text" {
				t.Fatalf("SectionForPage(.text offset %d) = %q; want .text", s.Offset, got.Name)
			}
		case ".rodata":
			foundRodata = true
		case ".bss", ".noptrbss":
			t.Fatalf("SHT_NOBITS section %q must be excluded from file-backed layout", s.Name)
		}
	}
	if !foundText || !foundRodata {
		t.Fatalf("expected both .text (%v) and .rodata (%v) in Go test binary", foundText, foundRodata)
	}
}

func TestParseRejectsNonELF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-elf")
	if err := os.WriteFile(p, []byte("hello world\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(p); err == nil {
		t.Fatal("expected error for non-ELF file, got nil")
	}
}
