package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const parentName = "reclaim_demo"

// setupParent creates <root>/reclaim_demo with the memory controller enabled for its
// children. cgroup v2 refuses to enable controllers for children of a cgroup that has
// processes of its own, which happens when the root we see is our own container's cgroup;
// in that case we first move ourselves into a sibling leaf.
func setupParent(root string) (string, error) {
	if err := enableMemory(root); errors.Is(err, unix.EBUSY) {
		self := filepath.Join(root, parentName+"_manager")
		if err := os.MkdirAll(self, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(self, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			return "", fmt.Errorf("move manager into %s: %w", self, err)
		}
		if err := enableMemory(root); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	parent := filepath.Join(root, parentName)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	return parent, enableMemory(parent)
}

func enableMemory(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return err
	}
	if strings.Contains(string(data), "memory") {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+memory"), 0o644); err != nil {
		return fmt.Errorf("enable memory controller in %s: %w", dir, err)
	}
	return nil
}

type cgroup struct {
	dir string
	fd  *os.File // O_DIRECTORY descriptor for CLONE_INTO_CGROUP
}

func createCgroup(parent, name, memoryMax string) (*cgroup, error) {
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	// Without swap only file pages (binary, page cache) can be reclaimed, which is the point.
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0o644); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: write memory.swap.max: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(memoryMax), 0o644); err != nil {
		return nil, fmt.Errorf("%s: write memory.max=%s: %w", dir, memoryMax, err)
	}
	fd, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	return &cgroup{dir: dir, fd: fd}, nil
}

func (c *cgroup) current() (uint64, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, "memory.current"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// someStall returns the cumulative time at least one task was stalled on memory.
func (c *cgroup) someStall() (time.Duration, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, "memory.pressure"))
	if err != nil {
		return 0, err
	}
	// some avg10=0.00 avg60=0.00 avg300=0.00 total=106340
	for line := range strings.Lines(string(data)) {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		_, total, ok := strings.Cut(line, "total=")
		if !ok {
			break
		}
		us, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
		return time.Duration(us) * time.Microsecond, err
	}
	return 0, fmt.Errorf("%s/memory.pressure: no \"some\" total", c.dir)
}

// reclaim asks the kernel to reclaim n bytes. EAGAIN means it couldn't reclaim all of it
// (e.g. nothing reclaimable is left), which isn't an error for us.
//
// This uses raw syscalls on purpose: cgroup files are pollable, so os.File registers them
// with the Go netpoller and treats EAGAIN as "would block", waiting forever.
func (c *cgroup) reclaim(n uint64) error {
	path := filepath.Join(c.dir, "memory.reclaim")
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)
	_, err = unix.Write(fd, []byte(strconv.FormatUint(n, 10)))
	if err == nil || errors.Is(err, unix.EAGAIN) {
		return nil
	}
	return fmt.Errorf("write %s: %w", path, err)
}

func (c *cgroup) remove() error {
	c.fd.Close()
	// The kernel may still be tearing down the last process for a moment.
	var err error
	for range 50 {
		if err = os.Remove(c.dir); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}
