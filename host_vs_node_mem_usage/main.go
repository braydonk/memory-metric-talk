package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"time"
)

// hostMemUsage returns MemTotal - MemAvailable from /proc/meminfo, in kB.
func hostMemUsage() (int, error) {
	memData, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}

	var total, available int
	var foundTotal, foundAvailable bool
	for line := range bytes.Lines(memData) {
		// e.g. "MemTotal:       16318496 kB"
		fields := bytes.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var dst *int
		switch string(fields[0]) {
		case "MemTotal:":
			dst, foundTotal = &total, true
		case "MemAvailable:":
			dst, foundAvailable = &available, true
		default:
			continue
		}
		v, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return 0, fmt.Errorf("parsing %s: %w", fields[0], err)
		}
		*dst = v
	}

	if !foundTotal || !foundAvailable {
		return 0, fmt.Errorf("MemTotal or MemAvailable not found in /proc/meminfo")
	}
	return total - available, nil
}

const cgroupRoot = "/sys/fs/cgroup"

// kubeletNodeMemUsage mimics node.memory.usageBytes of the kubelet summary API
// (cgroup v2 only), in kB. The kubelet takes it from cAdvisor's "/" container,
// which reads the cgroup root via opencontainers/cgroups (fs2.statMemory).
func kubeletNodeMemUsage() (int, error) {
	// Inside a cgroup namespace (kind, kubelet in a container) the visible root
	// is a child cgroup and has memory.current.
	current, err := os.ReadFile(cgroupRoot + "/memory.current")
	if err == nil {
		v, err := strconv.Atoi(string(bytes.TrimSpace(current)))
		if err != nil {
			return 0, fmt.Errorf("parsing %s/memory.current: %w", cgroupRoot, err)
		}
		return v / 1024, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}

	// The real root has no memory.current, so it is emulated as anon + file
	// (fs2.rootStatsFromMeminfo). Kernel memory (slab, stacks, ...) is not included.
	statData, err := os.ReadFile(cgroupRoot + "/memory.stat")
	if err != nil {
		return 0, err
	}
	var anon, file int
	var foundAnon, foundFile bool
	for line := range bytes.Lines(statData) {
		// e.g. "anon 1234567"
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		var dst *int
		switch string(fields[0]) {
		case "anon":
			dst, foundAnon = &anon, true
		case "file":
			dst, foundFile = &file, true
		default:
			continue
		}
		v, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return 0, fmt.Errorf("parsing %s in %s/memory.stat: %w", fields[0], cgroupRoot, err)
		}
		*dst = v
	}
	if !foundAnon || !foundFile {
		return 0, fmt.Errorf("anon or file not found in %s/memory.stat (requires cgroup v2 and a kernel exposing root memory.stat)", cgroupRoot)
	}
	return (anon + file) / 1024, nil
}

func main() {
	for {
		usage, err := hostMemUsage()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("[/proc/meminfo] MemTotal - MemAvailable: %d kB\n", usage)

		nodeUsage, err := kubeletNodeMemUsage()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("[kubelet] node.memory.usageBytes: %d kB\n", nodeUsage)
		time.Sleep(2 * time.Second)
	}
}
