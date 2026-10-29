//go:build linux

package cgroup

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Watcher uses epoll (EPOLLPRI) to watch cgroup v2 memory.events for reclaim and OOM counter changes.
type Watcher struct {
	eventsFD int
	epFD     int
	stopFD   int

	ch       chan Events
	done     chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// NewWatcher opens eventsPath and starts an epoll background goroutine watching EPOLLPRI notifications.
//
// In Linux kernfs (fs/kernfs/file.c), regular cgroup attribute files always report EPOLLIN|EPOLLOUT,
// and signal attribute modifications via EPOLLPRI|EPOLLERR until the descriptor is read from offset 0
// (which updates of->event in kernfs_seq_start). We therefore:
//  1. Perform an initial pread(..., 0) to sync of->event to the baseline generation.
//  2. Register EPOLLPRI (never EPOLLIN) on epoll_ctl.
//  3. Immediately pread(..., 0) inside the loop on every wakeup before calling EpollWait again.
func NewWatcher(eventsPath string) (*Watcher, error) {
	evFD, err := unix.Open(eventsPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s for watcher: %w", eventsPath, err)
	}

	var initBuf [1024]byte
	raw, err := preadZero(evFD, initBuf[:])
	if err != nil {
		_ = unix.Close(evFD)
		return nil, fmt.Errorf("initial read %s: %w", eventsPath, err)
	}
	initialEvents, err := ParseEvents(raw)
	if err != nil {
		_ = unix.Close(evFD)
		return nil, fmt.Errorf("initial parse %s: %w", eventsPath, err)
	}

	epFD, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		_ = unix.Close(evFD)
		return nil, fmt.Errorf("epoll_create1: %w", err)
	}

	stopFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		_ = unix.Close(epFD)
		_ = unix.Close(evFD)
		return nil, fmt.Errorf("eventfd: %w", err)
	}

	if err := unix.EpollCtl(epFD, unix.EPOLL_CTL_ADD, stopFD, &unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(stopFD),
	}); err != nil {
		_ = unix.Close(stopFD)
		_ = unix.Close(epFD)
		_ = unix.Close(evFD)
		return nil, fmt.Errorf("epoll_ctl add stopFD: %w", err)
	}

	if err := unix.EpollCtl(epFD, unix.EPOLL_CTL_ADD, evFD, &unix.EpollEvent{
		Events: unix.EPOLLPRI,
		Fd:     int32(evFD),
	}); err != nil {
		_ = unix.Close(stopFD)
		_ = unix.Close(epFD)
		_ = unix.Close(evFD)
		return nil, fmt.Errorf("epoll_ctl add memory.events: %w", err)
	}

	w := &Watcher{
		eventsFD: evFD,
		epFD:     epFD,
		stopFD:   stopFD,
		ch:       make(chan Events, 1),
		done:     make(chan struct{}),
	}
	go w.loop(initialEvents)
	return w, nil
}

// C returns a coalescing channel notified whenever reclaim or OOM counters increment in memory.events.
func (w *Watcher) C() <-chan Events {
	return w.ch
}

func (w *Watcher) loop(prev Events) {
	defer close(w.done)
	defer close(w.ch)

	var epEvents [4]unix.EpollEvent
	var buf [1024]byte

	for {
		n, err := unix.EpollWait(w.epFD, epEvents[:], -1)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		for i := 0; i < n; i++ {
			fd := int(epEvents[i].Fd)
			if fd == w.stopFD {
				return
			}
			if fd == w.eventsFD {
				raw, rerr := preadZero(w.eventsFD, buf[:])
				if rerr != nil {
					return
				}
				cur, perr := ParseEvents(raw)
				if perr != nil {
					continue
				}
				if cur.ReclaimOrOOMIncreased(prev) {
					prev = cur
					// Deliver latest counter snapshot, coalescing if consumer hasn't drained yet.
					select {
					case w.ch <- cur:
					default:
						select {
						case <-w.ch:
						default:
						}
						select {
						case w.ch <- cur:
						default:
						}
					}
				} else {
					prev = cur
				}
			}
		}
	}
}

// Close wakes the epoll goroutine, waits for it to terminate, and closes all file descriptors.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		var one [8]byte
		binary.NativeEndian.PutUint64(one[:], 1)
		_, _ = unix.Write(w.stopFD, one[:])
		<-w.done
		w.closeErr = errors.Join(
			unix.Close(w.eventsFD),
			unix.Close(w.stopFD),
			unix.Close(w.epFD),
		)
	})
	return w.closeErr
}
