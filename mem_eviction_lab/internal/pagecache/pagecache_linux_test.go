//go:build linux

package pagecache

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEvictAndVerifyOnDiskFS(t *testing.T) {
	// Deliberately create the test directory in "." (disk-backed workspace) rather than t.TempDir()
	// because /tmp on Linux hosts is commonly tmpfs where FADV_DONTNEED is a no-op.
	if err := ValidateDiskBacked("."); err != nil {
		t.Skipf("current working directory is not disk-backed: %v", err)
	}

	dir, err := os.MkdirTemp(".", "pagecache-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	p := filepath.Join(dir, "sample.bin")
	payload := bytes.Repeat([]byte("evict-me-"), 32*1024) // ~288 KiB (~72 pages)
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Warm the page cache by reading the entire file.
	if _, err := os.ReadFile(p); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	before, after, err := EvictAndVerify(p, false)
	if err != nil {
		t.Fatalf("EvictAndVerify failed: %v", err)
	}
	if before == 0 {
		t.Fatalf("expected warm pages before eviction, got before=%d", before)
	}
	if after != 0 {
		t.Fatalf("expected 0 resident pages after eviction, got after=%d", after)
	}

	id, err := StatFileID(p)
	if err != nil {
		t.Fatalf("StatFileID: %v", err)
	}
	if id.Ino == 0 || id.Size != uint64(len(payload)) {
		t.Fatalf("unexpected FileID: %+v", id)
	}
}

func TestValidateDiskBackedRejectsTmpfs(t *testing.T) {
	tmpDir := t.TempDir()
	var sfs unix.Statfs_t
	if err := unix.Statfs(tmpDir, &sfs); err != nil {
		t.Fatalf("statfs %q: %v", tmpDir, err)
	}
	if uint64(uint32(sfs.Type)) != uint64(unix.TMPFS_MAGIC) {
		t.Skipf("%q is not tmpfs on this host (type=0x%x)", tmpDir, sfs.Type)
	}

	p := filepath.Join(tmpDir, "on-tmpfs.bin")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ValidateDiskBacked(p)
	if err == nil || !strings.Contains(err.Error(), "tmpfs") {
		t.Fatalf("expected tmpfs rejection error, got %v", err)
	}
}
