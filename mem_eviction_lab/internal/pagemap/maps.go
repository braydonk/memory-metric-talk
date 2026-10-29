package pagemap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SegmentClass categorizes ELF VMA permissions.
type SegmentClass uint8

const (
	ClassRO SegmentClass = iota // r--p (.rodata, .gopclntab, headers)
	ClassRX                     // r-xp (.text)
	ClassRW                     // rw-p (.data, .noptrdata)
)

// VMA represents a contiguous virtual memory area from /proc/[pid]/maps backing the target binary.
type VMA struct {
	StartAddr  uint64
	EndAddr    uint64
	Perms      string
	Class      SegmentClass
	FileOffset uint64
	DevMajor   uint32
	DevMinor   uint32
	Inode      uint64
	Pathname   string
}

// NumPages returns the number of pages spanned by this VMA.
func (v VMA) NumPages(pageSize uint64) uint64 {
	if v.EndAddr <= v.StartAddr || pageSize == 0 {
		return 0
	}
	return (v.EndAddr - v.StartAddr) / pageSize
}

// ReadBinaryVMAs reads /proc/<pid>/maps and returns all VMAs matching (devMajor, devMinor, inode).
func ReadBinaryVMAs(pid int, devMajor, devMinor uint32, inode uint64) ([]VMA, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "maps")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseBinaryVMAs(data, devMajor, devMinor, inode)
}

// ParseBinaryVMAs parses /proc/[pid]/maps content and filters entries matching (devMajor, devMinor, inode).
func ParseBinaryVMAs(data []byte, devMajor, devMinor uint32, inode uint64) ([]VMA, error) {
	var vmas []VMA
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// Column 5 (index 4) is decimal inode. Check inode first as fast-path filter.
		ino, err := strconv.ParseUint(string(fields[4]), 10, 64)
		if err != nil || ino != inode {
			continue
		}

		// Column 4 (index 3) is hex major:minor (e.g. "fe:01").
		majStr, minStr, ok := strings.Cut(string(fields[3]), ":")
		if !ok {
			continue
		}
		maj, err1 := strconv.ParseUint(majStr, 16, 32)
		minr, err2 := strconv.ParseUint(minStr, 16, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		if uint32(maj) != devMajor || uint32(minr) != devMinor {
			continue
		}

		// Column 1 (index 0) is hex start-end.
		startStr, endStr, ok := strings.Cut(string(fields[0]), "-")
		if !ok {
			return nil, fmt.Errorf("malformed address range %q", fields[0])
		}
		startAddr, err := strconv.ParseUint(startStr, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parse start addr %q: %w", startStr, err)
		}
		endAddr, err := strconv.ParseUint(endStr, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parse end addr %q: %w", endStr, err)
		}
		if endAddr <= startAddr {
			continue
		}

		// Column 2 (index 1) is perms (e.g. "r-xp").
		perms := string(fields[1])

		// Column 3 (index 2) is hex file offset.
		fileOff, err := strconv.ParseUint(string(fields[2]), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parse file offset %q: %w", fields[2], err)
		}

		var pathname string
		if len(fields) >= 6 {
			pathname = string(fields[5])
		}

		vmas = append(vmas, VMA{
			StartAddr:  startAddr,
			EndAddr:    endAddr,
			Perms:      perms,
			Class:      classifyPerms(perms),
			FileOffset: fileOff,
			DevMajor:   uint32(maj),
			DevMinor:   uint32(minr),
			Inode:      ino,
			Pathname:   pathname,
		})
	}
	return vmas, nil
}

func classifyPerms(perms string) SegmentClass {
	if len(perms) >= 3 && perms[2] == 'x' {
		return ClassRX
	}
	if len(perms) >= 2 && perms[1] == 'w' {
		return ClassRW
	}
	return ClassRO
}
