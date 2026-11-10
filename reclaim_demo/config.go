package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	// CollectorBinary is copied once per instance, so instances don't share page cache.
	CollectorBinary string `yaml:"collector_binary"`
	CollectorConfig string `yaml:"collector_config"`
	// WorkDir holds the per-instance binary copies. It must be disk-backed (not tmpfs),
	// otherwise the binary pages are shmem and can't be reclaimed without swap.
	WorkDir string `yaml:"work_dir"`
	// CgroupRoot is the cgroup v2 mount point; instances live in <root>/reclaim_demo/<name>.
	CgroupRoot string `yaml:"cgroup_root"`
	// BasePort is the OTLP/HTTP port of the first instance; the others use the next ports.
	BasePort int `yaml:"base_port"`
	// TelemetryEndpoint is the OTLP/HTTP metrics URL the collectors push their own
	// telemetry (e.g. accepted data points) to. Empty (default) disables it.
	TelemetryEndpoint string     `yaml:"telemetry_endpoint"`
	Load              LoadConfig `yaml:"load"`
	Instances         []Instance `yaml:"instances"`
}

type LoadConfig struct {
	// Interval between OTLP/HTTP export requests sent to each instance. 0 disables the load.
	Interval   time.Duration `yaml:"interval"`
	DataPoints int           `yaml:"data_points"`
}

type Instance struct {
	Name string `yaml:"name"`
	// MemoryMax is written to memory.max: "max" or a size like "256MiB".
	MemoryMax string `yaml:"memory_max"`
	Reclaim   Policy `yaml:"reclaim"`
}

type Policy struct {
	// Type is "none", "psi" (Senpai-style, driven by memory pressure) or "fixed".
	Type     string        `yaml:"type"`
	Interval time.Duration `yaml:"interval"`
	// psi: reclaim = memory.current * Ratio * max(0, 1 - pressure/PressureTarget),
	// where pressure is the share of the interval tasks were stalled ("some").
	Ratio          float64 `yaml:"ratio"`
	PressureTarget float64 `yaml:"pressure_target"`
	// fixed: Step bytes per interval, whatever the pressure.
	Step string `yaml:"step"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		CgroupRoot: "/sys/fs/cgroup",
		WorkDir:    "/var/tmp/reclaim_demo",
		BasePort:   4318,
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.CollectorBinary == "" || cfg.CollectorConfig == "" {
		return nil, fmt.Errorf("%s: collector_binary and collector_config are required", path)
	}
	if len(cfg.Instances) == 0 {
		return nil, fmt.Errorf("%s: at least one instance is required", path)
	}
	seen := map[string]bool{}
	for i, in := range cfg.Instances {
		if in.Name == "" || strings.ContainsAny(in.Name, "/ ") || seen[in.Name] {
			return nil, fmt.Errorf("instance %d: name %q must be unique and contain no '/' or spaces", i, in.Name)
		}
		seen[in.Name] = true
		if _, err := parseLimit(in.MemoryMax); err != nil {
			return nil, fmt.Errorf("instance %s: memory_max: %w", in.Name, err)
		}
		switch in.Reclaim.Type {
		case "", "none":
		case "psi":
			if in.Reclaim.Interval <= 0 || in.Reclaim.Ratio <= 0 || in.Reclaim.PressureTarget <= 0 {
				return nil, fmt.Errorf("instance %s: psi reclaim needs interval, ratio and pressure_target > 0", in.Name)
			}
		case "fixed":
			if in.Reclaim.Interval <= 0 {
				return nil, fmt.Errorf("instance %s: fixed reclaim needs interval > 0", in.Name)
			}
			if n, err := parseLimit(in.Reclaim.Step); err != nil || n == "max" {
				return nil, fmt.Errorf("instance %s: fixed reclaim needs a step size like \"8MiB\"", in.Name)
			}
		default:
			return nil, fmt.Errorf("instance %s: unknown reclaim type %q (none, psi, fixed)", in.Name, in.Reclaim.Type)
		}
	}
	return cfg, nil
}

// parseLimit turns "max", "1234", "64KiB", "80MiB" or "1GiB" into the string written to a
// cgroup file. Plain numbers are bytes.
func parseLimit(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "max" {
		return "max", nil
	}
	mult := uint64(1)
	for suffix, m := range map[string]uint64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30} {
		if num, ok := strings.CutSuffix(s, suffix); ok {
			s, mult = num, m
			break
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return "", fmt.Errorf("invalid size %q (use max, bytes, KiB, MiB or GiB)", s)
	}
	return strconv.FormatUint(n*mult, 10), nil
}
