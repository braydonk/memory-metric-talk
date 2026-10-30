package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/bytefmt"
)

type SignalType string

const (
	SignalLogs    SignalType = "logs"
	SignalMetrics SignalType = "metrics"
	SignalTraces  SignalType = "traces"
)

const (
	DefaultSampleInterval        = 500 * time.Millisecond
	DefaultEventDebounce         = 50 * time.Millisecond
	DefaultStartupTimeout        = 15 * time.Second
	DefaultShutdownTimeout       = 10 * time.Second
	DefaultBlockSizePages        = 64 // 256 KiB at 4 KiB page size
	DefaultBackendGRPCEndpoint   = "127.0.0.1:14317"
	DefaultCollectorGRPCEndpoint = "127.0.0.1:4317"
	DefaultBatchSize             = 100
)

// Config represents the run YAML configuration for mem_eviction_lab.
type Config struct {
	Name            string            `yaml:"name" json:"name"`
	CollectorBinary string            `yaml:"collector_binary" json:"collector_binary"`
	CollectorConfig string            `yaml:"collector_config" json:"collector_config"`
	CollectorArgs   []string          `yaml:"collector_args,omitempty" json:"collector_args,omitempty"`
	MemoryLimit     string            `yaml:"memory_limit" json:"memory_limit"`
	SwapMax         string            `yaml:"swap_max" json:"swap_max"`
	SampleInterval  time.Duration     `yaml:"sample_interval" json:"sample_interval"`
	EventDebounce   time.Duration     `yaml:"event_debounce" json:"event_debounce"`
	StartupTimeout  time.Duration     `yaml:"startup_timeout" json:"startup_timeout"`
	ShutdownTimeout time.Duration     `yaml:"shutdown_timeout" json:"shutdown_timeout"`
	BlockSizePages  int               `yaml:"block_size_pages" json:"block_size_pages"`
	Env             map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Backend         BackendConfig     `yaml:"backend" json:"backend"`
	Workload        WorkloadConfig    `yaml:"workload" json:"workload"`

	parsedMemLimit bytefmt.Limit
	parsedSwapMax  bytefmt.Limit
}

// BackendConfig configures the optional in-process OTLP gRPC discard receiver.
type BackendConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	GRPCEndpoint string `yaml:"grpc_endpoint" json:"grpc_endpoint"`
}

// WorkloadConfig configures the optional in-process OTLP gRPC load generator.
type WorkloadConfig struct {
	Enabled               bool                         `yaml:"enabled" json:"enabled"`
	CollectorGRPCEndpoint string                       `yaml:"collector_grpc_endpoint" json:"collector_grpc_endpoint"`
	Signals               map[SignalType]*SignalConfig `yaml:"signals,omitempty" json:"signals,omitempty"`
}

// SignalConfig configures load generation for one telemetry signal.
type SignalConfig struct {
	Rate      float64 `yaml:"rate" json:"rate"`
	BatchSize int     `yaml:"batch_size" json:"batch_size"`
}

// ParsedMemoryLimit returns the validated memory.max value.
func (c *Config) ParsedMemoryLimit() bytefmt.Limit {
	return c.parsedMemLimit
}

// ParsedSwapMax returns the validated memory.swap.max value.
func (c *Config) ParsedSwapMax() bytefmt.Limit {
	return c.parsedSwapMax
}

// Load reads, applies defaults, resolves paths, and validates a run YAML file.
// If memoryLimitOverride is non-empty, it replaces memory_limit from the YAML file.
func Load(path string, memoryLimitOverride string) (*Config, error) {
	return LoadWithOverrides(path, memoryLimitOverride, "")
}

// LoadWithOverrides reads and validates a run YAML file with optional overrides for memory_limit and swap_max.
func LoadWithOverrides(path, memoryLimitOverride, swapMaxOverride string) (*Config, error) {
	absCfgPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %q: %w", path, err)
	}
	raw, err := os.ReadFile(absCfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", absCfgPath, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", absCfgPath, err)
	}

	if strings.TrimSpace(memoryLimitOverride) != "" {
		cfg.MemoryLimit = strings.TrimSpace(memoryLimitOverride)
	}
	if strings.TrimSpace(swapMaxOverride) != "" {
		cfg.SwapMax = strings.TrimSpace(swapMaxOverride)
	}

	baseDir := filepath.Dir(absCfgPath)
	if err := cfg.applyDefaultsAndValidate(baseDir); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaultsAndValidate(baseDir string) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("config: name is required")
	}

	var err error
	c.CollectorBinary, err = resolveLocalFile("collector_binary", c.CollectorBinary, baseDir)
	if err != nil {
		return err
	}
	c.CollectorConfig, err = resolveLocalFile("collector_config", c.CollectorConfig, baseDir)
	if err != nil {
		return err
	}

	if strings.TrimSpace(c.MemoryLimit) == "" {
		return errors.New("config: memory_limit is required (or pass --memory-limit)")
	}
	c.parsedMemLimit, err = bytefmt.ParseLimit(c.MemoryLimit, false)
	if err != nil {
		return fmt.Errorf("config: invalid memory_limit: %w", err)
	}
	if !c.parsedMemLimit.Unlimited && c.parsedMemLimit.Bytes < 4096 {
		return fmt.Errorf("config: memory_limit (%d B) must be at least 4096 B or \"max\"", c.parsedMemLimit.Bytes)
	}

	if strings.TrimSpace(c.SwapMax) == "" {
		c.SwapMax = "0"
	}
	c.parsedSwapMax, err = bytefmt.ParseLimit(c.SwapMax, true)
	if err != nil {
		return fmt.Errorf("config: invalid swap_max: %w", err)
	}

	if c.SampleInterval == 0 {
		c.SampleInterval = DefaultSampleInterval
	}
	if c.SampleInterval < 0 {
		return errors.New("config: sample_interval must be > 0")
	}

	if c.EventDebounce == 0 {
		c.EventDebounce = DefaultEventDebounce
	}
	if c.EventDebounce < 0 {
		return errors.New("config: event_debounce must be >= 0")
	}

	if c.StartupTimeout == 0 {
		c.StartupTimeout = DefaultStartupTimeout
	}
	if c.StartupTimeout < 0 {
		return errors.New("config: startup_timeout must be > 0")
	}

	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	if c.ShutdownTimeout < 0 {
		return errors.New("config: shutdown_timeout must be > 0")
	}

	if c.BlockSizePages == 0 {
		c.BlockSizePages = DefaultBlockSizePages
	}
	if c.BlockSizePages < 1 {
		return errors.New("config: block_size_pages must be >= 1")
	}

	if c.Backend.Enabled && strings.TrimSpace(c.Backend.GRPCEndpoint) == "" {
		c.Backend.GRPCEndpoint = DefaultBackendGRPCEndpoint
	}

	if c.Workload.Enabled {
		if strings.TrimSpace(c.Workload.CollectorGRPCEndpoint) == "" {
			c.Workload.CollectorGRPCEndpoint = DefaultCollectorGRPCEndpoint
		}
		if len(c.Workload.Signals) == 0 {
			return errors.New("config: workload.signals must contain at least one signal when workload.enabled is true")
		}
		activeSignals := 0
		for sig, sc := range c.Workload.Signals {
			switch sig {
			case SignalLogs, SignalMetrics, SignalTraces:
			default:
				return fmt.Errorf("config: unknown workload signal %q (expected logs, metrics, or traces)", sig)
			}
			if sc == nil {
				continue
			}
			if sc.Rate <= 0 {
				return fmt.Errorf("config: workload signal %q rate must be > 0", sig)
			}
			if sc.BatchSize == 0 {
				sc.BatchSize = DefaultBatchSize
			}
			if sc.BatchSize < 0 {
				return fmt.Errorf("config: workload signal %q batch_size must be > 0", sig)
			}
			activeSignals++
		}
		if activeSignals == 0 {
			return errors.New("config: workload.signals must contain at least one non-empty signal configuration")
		}
	}

	return nil
}

func resolveLocalFile(field, rawPath, baseDir string) (string, error) {
	p := strings.TrimSpace(rawPath)
	if p == "" {
		return "", fmt.Errorf("config: %s is required", field)
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return "", fmt.Errorf("config: %s %q looks like a URL; only local file paths are supported", field, p)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	p = filepath.Clean(p)
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("config: %s %q: %w", field, p, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("config: %s %q is a directory, expected a file", field, p)
	}
	return p, nil
}
