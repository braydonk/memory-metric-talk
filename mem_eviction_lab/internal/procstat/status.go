package procstat

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Status holds memory residency counters parsed from /proc/[pid]/status (converted to bytes).
type Status struct {
	VmRSSBytes    uint64
	RssAnonBytes  uint64
	RssFileBytes  uint64
	RssShmemBytes uint64
	VmHWMBytes    uint64
	VmSwapBytes   uint64
}

// ReadPID reads and parses /proc/<pid>/status.
func ReadPID(pid int) (Status, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "status")
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(data)
}

// ParseStatus parses /proc/[pid]/status byte content.
func ParseStatus(data []byte) (Status, error) {
	var st Status
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var dst *uint64
		switch string(fields[0]) {
		case "VmHWM:":
			dst = &st.VmHWMBytes
		case "VmRSS:":
			dst = &st.VmRSSBytes
		case "RssAnon:":
			dst = &st.RssAnonBytes
		case "RssFile:":
			dst = &st.RssFileBytes
		case "RssShmem:":
			dst = &st.RssShmemBytes
		case "VmSwap:":
			dst = &st.VmSwapBytes
		default:
			continue
		}
		kb, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return Status{}, fmt.Errorf("parse %s %q: %w", fields[0], fields[1], err)
		}
		*dst = kb * 1024
	}
	return st, nil
}
