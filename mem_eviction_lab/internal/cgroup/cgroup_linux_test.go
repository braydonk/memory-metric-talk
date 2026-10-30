//go:build linux

package cgroup

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/bytefmt"
)

func TestParseStatEventsAndPressure(t *testing.T) {
	statRaw := []byte(`anon 41943040
file 12582912
kernel 1048576
active_file 8388608
inactive_file 4194304
pgfault 12345
pgmajfault 67
workingset_refault_file 89
workingset_activate_file 34
`)
	st, err := ParseStat(statRaw)
	if err != nil {
		t.Fatalf("ParseStat: %v", err)
	}
	if st.AnonBytes != 41943040 || st.FileBytes != 12582912 || st.KernelBytes != 1048576 ||
		st.ActiveFileBytes != 8388608 || st.InactiveFileBytes != 4194304 ||
		st.Pgfault != 12345 || st.Pgmajfault != 67 ||
		st.WorkingsetRefaultFile != 89 || st.WorkingsetActivateFile != 34 {
		t.Fatalf("unexpected Stat: %+v", st)
	}

	evRaw := []byte("low 0\nhigh 2\nmax 19\noom 1\noom_kill 1\noom_group_kill 0\n")
	ev, err := ParseEvents(evRaw)
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	if ev.High != 2 || ev.Max != 19 || ev.OOM != 1 || ev.OOMKill != 1 {
		t.Fatalf("unexpected Events: %+v", ev)
	}
	if !ev.ReclaimOrOOMIncreased(Events{High: 2, Max: 18}) {
		t.Fatal("expected ReclaimOrOOMIncreased to be true when Max increased")
	}

	psiRaw := []byte("some avg10=1.20 avg60=0.40 avg300=0.10 total=45678\nfull avg10=0.50 avg60=0.10 avg300=0.01 total=9012\n")
	psi, err := ParsePressure(psiRaw)
	if err != nil {
		t.Fatalf("ParsePressure: %v", err)
	}
	if psi.SomeTotalUs != 45678 || psi.FullTotalUs != 9012 {
		t.Fatalf("unexpected Pressure: %+v", psi)
	}

	cur, err := ParseUintFile([]byte("62914560\n"))
	if err != nil || cur != 62914560 {
		t.Fatalf("ParseUintFile = (%d, %v); want (62914560, nil)", cur, err)
	}
}

func TestRemoveLeafAndParentCleanup(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, ParentDirName)
	leaf1 := filepath.Join(parent, "run-1")
	leaf2 := filepath.Join(parent, "run-2")
	if err := os.MkdirAll(leaf1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(leaf2, 0o755); err != nil {
		t.Fatal(err)
	}

	// Removing leaf1 while sibling leaf2 exists removes leaf1 and keeps parent without failing.
	if err := removeLeafAndParent(leaf1, parent, 200*time.Millisecond); err != nil {
		t.Fatalf("removeLeafAndParent(leaf1): %v", err)
	}
	if _, err := os.Stat(leaf1); !os.IsNotExist(err) {
		t.Fatalf("leaf1 should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("parent should still exist while leaf2 is active: %v", err)
	}

	// Removing leaf2 leaves parent empty and deletes parent too.
	if err := removeLeafAndParent(leaf2, parent, 200*time.Millisecond); err != nil {
		t.Fatalf("removeLeafAndParent(leaf2): %v", err)
	}
	if _, err := os.Stat(leaf2); !os.IsNotExist(err) {
		t.Fatalf("leaf2 should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("parent should be deleted once last leaf is removed, stat err=%v", err)
	}
}

func TestSetMemoryMaxAndReclaim(t *testing.T) {
	leaf := t.TempDir()
	curPath := filepath.Join(leaf, "memory.current")
	maxPath := filepath.Join(leaf, "memory.max")
	recPath := filepath.Join(leaf, "memory.reclaim")

	if err := os.WriteFile(curPath, []byte("83886080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(maxPath, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cg := &Cgroup{
		leafPath:   leaf,
		dirFD:      -1,
		currentFD:  -1,
		statFD:     -1,
		eventsFD:   -1,
		pressureFD: -1,
	}

	lim, err := bytefmt.ParseLimit("55MiB", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cg.SetMemoryMax(lim); err != nil {
		t.Fatalf("SetMemoryMax: %v", err)
	}
	gotMax, err := os.ReadFile(maxPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(gotMax)) != "57671680" {
		t.Fatalf("memory.max = %q; want 57671680", gotMax)
	}

	// Missing memory.reclaim must return error without O_CREATE materializing the file.
	if _, _, err := cg.Reclaim(16 * 1024 * 1024); err == nil {
		t.Fatal("expected error when memory.reclaim is absent")
	}
	if _, err := os.Stat(recPath); !os.IsNotExist(err) {
		t.Fatalf("memory.reclaim must not be created by O_WRONLY open; stat err=%v", err)
	}

	// Simulate full reclaim succeeding.
	if err := os.WriteFile(recPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	cg.writeFn = func(path string, data []byte) error {
		if err := writeExistingFile(path, data); err != nil {
			return err
		}
		return os.WriteFile(curPath, []byte("67108864\n"), 0o644)
	}
	reclaimed, partial, err := cg.Reclaim(16 * 1024 * 1024)
	if err != nil {
		t.Fatalf("Reclaim full: %v", err)
	}
	if partial || reclaimed != 16*1024*1024 {
		t.Fatalf("Reclaim full = (%d, %v); want (16MiB, false)", reclaimed, partial)
	}

	// Simulate kernel returning EAGAIN after partial LRU reclaim down to floor.
	cg.writeFn = func(_ string, _ []byte) error {
		_ = os.WriteFile(curPath, []byte("50331648\n"), 0o644)
		return syscall.EAGAIN
	}
	reclaimed, partial, err = cg.Reclaim(64 * 1024 * 1024)
	if err != nil {
		t.Fatalf("Reclaim partial (EAGAIN) returned unexpected error: %v", err)
	}
	if !partial || reclaimed != 16*1024*1024 {
		t.Fatalf("Reclaim partial = (%d, %v); want (16MiB, true)", reclaimed, partial)
	}
}
