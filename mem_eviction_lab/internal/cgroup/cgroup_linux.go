//go:build linux

package cgroup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/bytefmt"
)

const (
	DefaultRoot   = "/sys/fs/cgroup"
	ParentDirName = "mem_eviction_lab"
)

// Stat holds parsed fields from memory.stat.
type Stat struct {
	AnonBytes              uint64
	FileBytes              uint64
	KernelBytes            uint64
	ActiveFileBytes        uint64
	InactiveFileBytes      uint64
	Pgfault                uint64
	Pgmajfault             uint64
	WorkingsetRefaultFile  uint64
	WorkingsetActivateFile uint64
}

// Events holds parsed counters from memory.events.
type Events struct {
	Low          uint64
	High         uint64
	Max          uint64
	OOM          uint64
	OOMKill      uint64
	OOMGroupKill uint64
}

// ReclaimOrOOMIncreased reports whether any pressure/OOM counter in next exceeds prev.
func (next Events) ReclaimOrOOMIncreased(prev Events) bool {
	return next.High > prev.High ||
		next.Max > prev.Max ||
		next.OOM > prev.OOM ||
		next.OOMKill > prev.OOMKill ||
		next.OOMGroupKill > prev.OOMGroupKill
}

// Pressure holds monotonic microsecond stall totals from memory.pressure (PSI).
type Pressure struct {
	SomeTotalUs uint64
	FullTotalUs uint64
}

// Snapshot bundles all cgroup v2 metrics read during a single sample.
type Snapshot struct {
	CurrentBytes uint64
	Stat         Stat
	Events       Events
	Pressure     Pressure
}

// Cgroup manages a dedicated cgroup v2 leaf directory for a single Collector run.
type Cgroup struct {
	parentPath string
	leafPath   string

	dirFD      int
	currentFD  int
	statFD     int
	eventsFD   int
	pressureFD int

	mu      sync.Mutex
	readBuf []byte
	watcher *Watcher
	writeFn func(path string, data []byte) error
	closed  bool
}

// CheckUnifiedMount verifies that cgroupRoot is mounted as a unified cgroup v2 filesystem.
func CheckUnifiedMount(cgroupRoot string) error {
	if cgroupRoot == "" {
		cgroupRoot = DefaultRoot
	}
	var sfs unix.Statfs_t
	if err := unix.Statfs(cgroupRoot, &sfs); err != nil {
		return fmt.Errorf("statfs cgroup root %q: %w", cgroupRoot, err)
	}
	if uint64(uint32(sfs.Type)) != uint64(unix.CGROUP2_SUPER_MAGIC) {
		return fmt.Errorf("%q is not a unified cgroup v2 mount (f_type=0x%x); cgroup v2 is required", cgroupRoot, sfs.Type)
	}
	return nil
}

// Create sets up /sys/fs/cgroup/mem_eviction_lab/<leaf>, enables the memory controller,
// configures memory.swap.max and memory.max, and opens descriptors for fast sampling and CLONE_INTO_CGROUP.
func Create(cgroupRoot, runName string, memLimit, swapMax bytefmt.Limit) (cg *Cgroup, retErr error) {
	if cgroupRoot == "" {
		cgroupRoot = DefaultRoot
	}
	if err := CheckUnifiedMount(cgroupRoot); err != nil {
		return nil, err
	}

	parentPath := filepath.Join(cgroupRoot, ParentDirName)
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		return nil, fmt.Errorf("create parent cgroup %q: %w", parentPath, err)
	}

	if err := ensureMemoryController(parentPath); err != nil {
		_ = removeParentIfEmpty(parentPath)
		return nil, err
	}

	leafName := fmt.Sprintf("%s-%d-%d", sanitizeName(runName), os.Getpid(), time.Now().UnixNano())
	leafPath := filepath.Join(parentPath, leafName)
	if err := os.Mkdir(leafPath, 0o755); err != nil {
		_ = removeParentIfEmpty(parentPath)
		return nil, fmt.Errorf("create leaf cgroup %q: %w", leafPath, err)
	}

	c := &Cgroup{
		parentPath: parentPath,
		leafPath:   leafPath,
		dirFD:      -1,
		currentFD:  -1,
		statFD:     -1,
		eventsFD:   -1,
		pressureFD: -1,
		readBuf:    make([]byte, 16*1024),
	}
	defer func() {
		if retErr != nil {
			_ = c.Close()
		}
	}()

	// Configure swap limit before memory limit.
	swapPath := filepath.Join(leafPath, "memory.swap.max")
	if err := writeExistingFile(swapPath, []byte(swapMax.String()+"\n")); err != nil {
		if errors.Is(err, os.ErrNotExist) && !swapMax.Unlimited && swapMax.Bytes == 0 {
			// Kernel compiled with CONFIG_SWAP=n; swap is already strictly zero.
		} else {
			return nil, fmt.Errorf("write %s=%s: %w", swapPath, swapMax.String(), err)
		}
	}

	maxPath := filepath.Join(leafPath, "memory.max")
	if err := writeExistingFile(maxPath, []byte(memLimit.String()+"\n")); err != nil {
		return nil, fmt.Errorf("write %s=%s: %w", maxPath, memLimit.String(), err)
	}

	var err error
	c.dirFD, err = unix.Open(leafPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open cgroup directory fd %q: %w", leafPath, err)
	}
	c.currentFD, err = unix.Open(filepath.Join(leafPath, "memory.current"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open memory.current: %w", err)
	}
	c.statFD, err = unix.Open(filepath.Join(leafPath, "memory.stat"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open memory.stat: %w", err)
	}
	c.eventsFD, err = unix.Open(filepath.Join(leafPath, "memory.events"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open memory.events: %w", err)
	}
	// memory.pressure may be absent or unsupported if PSI is disabled on the kernel.
	pfd, perr := unix.Open(filepath.Join(leafPath, "memory.pressure"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if perr == nil {
		c.pressureFD = pfd
	}

	return c, nil
}

// Path returns the leaf cgroup filesystem path.
func (c *Cgroup) Path() string {
	return c.leafPath
}

// FD returns the O_DIRECTORY file descriptor suitable for syscall.SysProcAttr.CgroupFD (CLONE_INTO_CGROUP).
func (c *Cgroup) FD() int {
	return c.dirFD
}

// StartWatcher opens an epoll watcher on memory.events and begins streaming counter updates.
func (c *Cgroup) StartWatcher() (<-chan Events, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("cgroup is closed")
	}
	if c.watcher != nil {
		return c.watcher.C(), nil
	}
	w, err := NewWatcher(filepath.Join(c.leafPath, "memory.events"))
	if err != nil {
		return nil, err
	}
	c.watcher = w
	return w.C(), nil
}

// ReadSnapshot reads memory.current, memory.stat, memory.events, and memory.pressure using pread(..., 0).
func (c *Cgroup) ReadSnapshot() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Snapshot{}, errors.New("cgroup is closed")
	}

	var snap Snapshot

	b, err := preadZero(c.currentFD, c.readBuf)
	if err != nil {
		return snap, fmt.Errorf("read memory.current: %w", err)
	}
	snap.CurrentBytes, err = ParseUintFile(b)
	if err != nil {
		return snap, fmt.Errorf("parse memory.current: %w", err)
	}

	b, err = preadZero(c.statFD, c.readBuf)
	if err != nil {
		return snap, fmt.Errorf("read memory.stat: %w", err)
	}
	snap.Stat, err = ParseStat(b)
	if err != nil {
		return snap, fmt.Errorf("parse memory.stat: %w", err)
	}

	b, err = preadZero(c.eventsFD, c.readBuf)
	if err != nil {
		return snap, fmt.Errorf("read memory.events: %w", err)
	}
	snap.Events, err = ParseEvents(b)
	if err != nil {
		return snap, fmt.Errorf("parse memory.events: %w", err)
	}

	if c.pressureFD >= 0 {
		b, perr := preadZero(c.pressureFD, c.readBuf)
		if perr == nil {
			if p, parseErr := ParsePressure(b); parseErr == nil {
				snap.Pressure = p
			}
		} else if errors.Is(perr, syscall.EOPNOTSUPP) || errors.Is(perr, syscall.ENOTSUP) {
			_ = unix.Close(c.pressureFD)
			c.pressureFD = -1
		}
	}

	return snap, nil
}

// SetMemoryMax updates memory.max on the active leaf cgroup mid-run.
// Writes run outside c.mu so synchronous kernel direct reclaim does not stall ReadSnapshot.
func (c *Cgroup) SetMemoryMax(memLimit bytefmt.Limit) error {
	c.mu.Lock()
	closed := c.closed
	leafPath := c.leafPath
	wfn := c.writeFn
	c.mu.Unlock()
	if closed {
		return errors.New("cgroup is closed")
	}
	if wfn == nil {
		wfn = writeExistingFile
	}
	maxPath := filepath.Join(leafPath, "memory.max")
	if err := wfn(maxPath, []byte(memLimit.String()+"\n")); err != nil {
		return fmt.Errorf("write %s=%s: %w", maxPath, memLimit.String(), err)
	}
	return nil
}

// Reclaim triggers proactive one-shot kernel LRU reclaim by writing bytes to memory.reclaim (Linux 5.19+).
// On Linux, EAGAIN indicates the kernel swept LRUs down to the reclaimable floor
// (reclaiming fewer bytes than requested); Reclaim reports this via partial=true and err=nil.
func (c *Cgroup) Reclaim(bytes uint64) (reclaimed uint64, partial bool, err error) {
	if bytes == 0 {
		return 0, false, errors.New("reclaim target bytes must be > 0")
	}
	before, berr := c.readCurrentBytes()
	if berr != nil {
		return 0, false, berr
	}

	c.mu.Lock()
	closed := c.closed
	leafPath := c.leafPath
	wfn := c.writeFn
	c.mu.Unlock()
	if closed {
		return 0, false, errors.New("cgroup is closed")
	}
	if wfn == nil {
		wfn = writeExistingFile
	}

	recPath := filepath.Join(leafPath, "memory.reclaim")
	werr := wfn(recPath, []byte(strconv.FormatUint(bytes, 10)+"\n"))
	if werr != nil {
		if errors.Is(werr, syscall.EAGAIN) {
			partial = true
		} else if errors.Is(werr, os.ErrNotExist) {
			return 0, false, fmt.Errorf("%s not present (memory.reclaim requires Linux 5.19+)", recPath)
		} else {
			return 0, false, fmt.Errorf("write %s=%d: %w", recPath, bytes, werr)
		}
	}

	after, aerr := c.readCurrentBytes()
	if aerr == nil && before > after {
		reclaimed = before - after
	}
	return reclaimed, partial, nil
}

func (c *Cgroup) readCurrentBytes() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("cgroup is closed")
	}
	if c.currentFD >= 0 {
		b, err := preadZero(c.currentFD, c.readBuf)
		if err != nil {
			return 0, fmt.Errorf("read memory.current: %w", err)
		}
		return ParseUintFile(b)
	}
	b, err := os.ReadFile(filepath.Join(c.leafPath, "memory.current"))
	if err != nil {
		return 0, fmt.Errorf("read memory.current: %w", err)
	}
	return ParseUintFile(b)
}

// KillAll forcefully terminates all tasks inside the leaf cgroup.
func (c *Cgroup) KillAll() {
	killCgroupTasks(c.leafPath)
}

// Close stops any watcher, closes all cgroup file descriptors, terminates any remaining processes,
// removes the leaf cgroup directory, and cleans up the parent /sys/fs/cgroup/mem_eviction_lab
// directory when empty.
func (c *Cgroup) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	w := c.watcher
	c.watcher = nil
	fds := []*int{&c.pressureFD, &c.eventsFD, &c.statFD, &c.currentFD, &c.dirFD}
	for _, fdPtr := range fds {
		if *fdPtr >= 0 {
			_ = unix.Close(*fdPtr)
			*fdPtr = -1
		}
	}
	c.mu.Unlock()

	var errs []error
	if w != nil {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close cgroup watcher: %w", err))
		}
	}

	if err := removeLeafAndParent(c.leafPath, c.parentPath, 3*time.Second); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func removeLeafAndParent(leafPath, parentPath string, timeout time.Duration) error {
	if leafPath != "" {
		deadline := time.Now().Add(timeout)
		for {
			killCgroupTasks(leafPath)
			err := os.Remove(leafPath)
			if err == nil || errors.Is(err, os.ErrNotExist) {
				break
			}
			if (errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ENOTEMPTY)) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return fmt.Errorf("remove leaf cgroup %q: %w", leafPath, err)
		}
	}
	if parentPath != "" {
		if err := removeParentIfEmpty(parentPath); err != nil {
			return err
		}
	}
	return nil
}

func removeParentIfEmpty(parentPath string) error {
	err := os.Remove(parentPath)
	if err == nil ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.EBUSY) ||
		errors.Is(err, syscall.ENOTEMPTY) {
		return nil
	}
	return fmt.Errorf("remove parent cgroup %q: %w", parentPath, err)
}

func killCgroupTasks(leafPath string) {
	// Linux 5.14+ atomic cgroup kill (never O_CREATE so missing files are never materialized).
	_ = writeExistingFile(filepath.Join(leafPath, "cgroup.kill"), []byte("1\n"))

	// Fallback for stray tasks via cgroup.procs.
	procs, err := os.ReadFile(filepath.Join(leafPath, "cgroup.procs"))
	if err != nil {
		return
	}
	for line := range bytes.Lines(procs) {
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		if pid, err := strconv.Atoi(s); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func writeExistingFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func ensureMemoryController(parentPath string) error {
	ctrlBytes, err := os.ReadFile(filepath.Join(parentPath, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read %s/cgroup.controllers: %w", parentPath, err)
	}
	if !slices.Contains(strings.Fields(string(ctrlBytes)), "memory") {
		return fmt.Errorf("memory controller is not available in %s/cgroup.controllers (%q)", parentPath, strings.TrimSpace(string(ctrlBytes)))
	}

	subPath := filepath.Join(parentPath, "cgroup.subtree_control")
	subBytes, err := os.ReadFile(subPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", subPath, err)
	}
	if slices.Contains(strings.Fields(string(subBytes)), "memory") {
		return nil
	}
	if err := writeExistingFile(subPath, []byte("+memory\n")); err != nil {
		return fmt.Errorf("enable +memory in %s: %w", subPath, err)
	}
	return nil
}

func preadZero(fd int, buf []byte) ([]byte, error) {
	for {
		n, err := unix.Pread(fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return nil, err
		}
		return buf[:n], nil
	}
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		return "run"
	}
	return out
}

// ParseUintFile parses a single-integer cgroup file such as memory.current.
func ParseUintFile(data []byte) (uint64, error) {
	s := strings.TrimSpace(string(data))
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid uint %q: %w", s, err)
	}
	return v, nil
}

// ParseStat parses cgroup v2 memory.stat content.
func ParseStat(data []byte) (Stat, error) {
	var st Stat
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		var dst *uint64
		switch string(fields[0]) {
		case "anon":
			dst = &st.AnonBytes
		case "file":
			dst = &st.FileBytes
		case "kernel":
			dst = &st.KernelBytes
		case "active_file":
			dst = &st.ActiveFileBytes
		case "inactive_file":
			dst = &st.InactiveFileBytes
		case "pgfault":
			dst = &st.Pgfault
		case "pgmajfault":
			dst = &st.Pgmajfault
		case "workingset_refault_file":
			dst = &st.WorkingsetRefaultFile
		case "workingset_activate_file":
			dst = &st.WorkingsetActivateFile
		default:
			continue
		}
		v, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return Stat{}, fmt.Errorf("parse %s %q: %w", fields[0], fields[1], err)
		}
		*dst = v
	}
	return st, nil
}

// ParseEvents parses cgroup v2 memory.events content.
func ParseEvents(data []byte) (Events, error) {
	var ev Events
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		var dst *uint64
		switch string(fields[0]) {
		case "low":
			dst = &ev.Low
		case "high":
			dst = &ev.High
		case "max":
			dst = &ev.Max
		case "oom":
			dst = &ev.OOM
		case "oom_kill":
			dst = &ev.OOMKill
		case "oom_group_kill":
			dst = &ev.OOMGroupKill
		default:
			continue
		}
		v, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return Events{}, fmt.Errorf("parse %s %q: %w", fields[0], fields[1], err)
		}
		*dst = v
	}
	return ev, nil
}

// ParsePressure parses cgroup v2 memory.pressure PSI lines ("some ... total=123", "full ... total=45").
func ParsePressure(data []byte) (Pressure, error) {
	var p Pressure
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kind := string(fields[0])
		if kind != "some" && kind != "full" {
			continue
		}
		for _, tok := range fields[1:] {
			k, vStr, ok := strings.Cut(string(tok), "=")
			if !ok || k != "total" {
				continue
			}
			v, err := strconv.ParseUint(vStr, 10, 64)
			if err != nil {
				return Pressure{}, fmt.Errorf("parse %s total=%q: %w", kind, vStr, err)
			}
			if kind == "some" {
				p.SomeTotalUs = v
			} else {
				p.FullTotalUs = v
			}
		}
	}
	return p, nil
}
