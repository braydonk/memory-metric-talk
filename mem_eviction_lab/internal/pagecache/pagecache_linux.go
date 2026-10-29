//go:build linux

package pagecache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FileID uniquely identifies a file inode on Linux for matching /proc/[pid]/maps entries.
type FileID struct {
	RealPath string
	DevMajor uint32
	DevMinor uint32
	Ino      uint64
	Size     uint64
}

// StatFileID resolves symlinks on path and returns its device major/minor, inode, and byte size.
func StatFileID(path string) (FileID, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return FileID{}, fmt.Errorf("eval symlinks %q: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Stat(realPath, &st); err != nil {
		return FileID{}, fmt.Errorf("stat %q: %w", realPath, err)
	}
	return FileID{
		RealPath: realPath,
		DevMajor: unix.Major(uint64(st.Dev)),
		DevMinor: unix.Minor(uint64(st.Dev)),
		Ino:      st.Ino,
		Size:     uint64(st.Size),
	}, nil
}

// ValidateDiskBacked verifies that path does not reside on tmpfs or ramfs.
// Files on tmpfs/ramfs are backed by shmem; FADV_DONTNEED is a no-op on them and the kernel
// cannot evict their pages when cgroup swap is disabled (memory.swap.max=0).
func ValidateDiskBacked(path string) error {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("eval symlinks %q: %w", path, err)
	}
	var sfs unix.Statfs_t
	if err := unix.Statfs(realPath, &sfs); err != nil {
		return fmt.Errorf("statfs %q: %w", realPath, err)
	}
	switch uint64(uint32(sfs.Type)) {
	case uint64(unix.TMPFS_MAGIC):
		return fmt.Errorf("%q is on tmpfs (shmem); move it to a disk-backed filesystem so file pages can be evicted without swap", realPath)
	case uint64(unix.RAMFS_MAGIC):
		return fmt.Errorf("%q is on ramfs; move it to a disk-backed filesystem so file pages can be evicted", realPath)
	}
	return nil
}

// CountResidentPages returns how many OS pages of the open file are currently resident in page cache.
func CountResidentPages(fd int, size int64) (int, error) {
	if size <= 0 {
		return 0, nil
	}
	data, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return 0, fmt.Errorf("mmap for mincore: %w", err)
	}
	defer func() {
		_ = unix.Munmap(data)
	}()

	pageSize := int64(os.Getpagesize())
	numPages := (size + pageSize - 1) / pageSize
	vec := make([]byte, numPages)
	if _, _, errno := unix.Syscall(
		unix.SYS_MINCORE,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&vec[0])),
	); errno != 0 {
		return 0, fmt.Errorf("mincore: %w", errno)
	}

	resident := 0
	for _, b := range vec {
		if b&1 != 0 {
			resident++
		}
	}
	return resident, nil
}

// EvictAndVerify ensures path is disk-backed, syncs any dirty writeback pages, drops its clean
// page cache pages via POSIX_FADV_DONTNEED, and checks residency via mincore.
func EvictAndVerify(path string, allowWarm bool) (beforePages int, afterPages int, err error) {
	if err := ValidateDiskBacked(path); err != nil {
		return 0, 0, err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return 0, 0, fmt.Errorf("eval symlinks %q: %w", path, err)
	}
	f, err := os.Open(realPath)
	if err != nil {
		return 0, 0, fmt.Errorf("open %q: %w", realPath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat %q: %w", realPath, err)
	}
	size := info.Size()
	if size == 0 {
		return 0, 0, nil
	}

	fd := int(f.Fd())
	beforePages, err = CountResidentPages(fd, size)
	if err != nil {
		return 0, 0, fmt.Errorf("count initial resident pages for %q: %w", realPath, err)
	}

	// Best-effort fsync so newly written dirty pages become clean and eligible for FADV_DONTNEED.
	if syncErr := unix.Fsync(fd); syncErr != nil &&
		!errors.Is(syncErr, syscall.EBADF) &&
		!errors.Is(syncErr, syscall.EINVAL) &&
		!errors.Is(syncErr, syscall.EROFS) {
		return beforePages, beforePages, fmt.Errorf("fsync %q: %w", realPath, syncErr)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED); err != nil {
			return beforePages, beforePages, fmt.Errorf("fadvise(FADV_DONTNEED) %q: %w", realPath, err)
		}
		afterPages, err = CountResidentPages(fd, size)
		if err != nil {
			return beforePages, 0, fmt.Errorf("count post-evict resident pages for %q: %w", realPath, err)
		}
		if afterPages == 0 {
			return beforePages, 0, nil
		}
	}

	if !allowWarm {
		return beforePages, afterPages, fmt.Errorf(
			"%q still has %d resident page(s) in host page cache after FADV_DONTNEED (likely mapped by another running process); stop other instances or pass --allow-warm-cache",
			realPath, afterPages,
		)
	}
	return beforePages, afterPages, nil
}
