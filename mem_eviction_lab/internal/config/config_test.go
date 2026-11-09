package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidAndOverrides(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "otelcol")
	colCfgPath := filepath.Join(dir, "col.yaml")
	if err := os.WriteFile(binPath, []byte("fakebin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(colCfgPath, []byte("service: {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	runYAML := `
name: otlp-test
collector_binary: ./otelcol
collector_config: ./col.yaml
memory_limit: "60MiB"
backend:
  enabled: true
workload:
  enabled: true
  signals:
    logs:
      rate: 1000
`
	cfgFile := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(cfgFile, []byte(runYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile, "")
	if err != nil {
		t.Fatalf("Load unexpected error: %v", err)
	}
	if cfg.CollectorBinary != binPath {
		t.Fatalf("CollectorBinary = %q; want %q", cfg.CollectorBinary, binPath)
	}
	if cfg.CollectorConfig != colCfgPath {
		t.Fatalf("CollectorConfig = %q; want %q", cfg.CollectorConfig, colCfgPath)
	}
	if cfg.ParsedMemoryLimit().Bytes != 60*1024*1024 {
		t.Fatalf("ParsedMemoryLimit = %+v; want 60 MiB", cfg.ParsedMemoryLimit())
	}
	if cfg.ParsedSwapMax().Bytes != 0 || cfg.ParsedSwapMax().Unlimited {
		t.Fatalf("ParsedSwapMax = %+v; want 0", cfg.ParsedSwapMax())
	}
	if cfg.SampleInterval != DefaultSampleInterval || cfg.EventDebounce != DefaultEventDebounce {
		t.Fatalf("unexpected default intervals: sample=%v debounce=%v", cfg.SampleInterval, cfg.EventDebounce)
	}
	if cfg.Backend.GRPCEndpoint != DefaultBackendGRPCEndpoint {
		t.Fatalf("Backend.GRPCEndpoint = %q; want %q", cfg.Backend.GRPCEndpoint, DefaultBackendGRPCEndpoint)
	}
	if cfg.Workload.Signals[SignalLogs].BatchSize != DefaultBatchSize {
		t.Fatalf("BatchSize = %d; want %d", cfg.Workload.Signals[SignalLogs].BatchSize, DefaultBatchSize)
	}

	// Test --memory-limit CLI override with "max"
	cfgMax, err := Load(cfgFile, "max")
	if err != nil {
		t.Fatalf("Load with max override failed: %v", err)
	}
	if !cfgMax.ParsedMemoryLimit().Unlimited {
		t.Fatalf("expected Unlimited=true for max override, got %+v", cfgMax.ParsedMemoryLimit())
	}

	// Test collector_binaries YAML list and LoadWithBinaryOverrides CLI override
	binPath2 := filepath.Join(dir, "otelcol-contrib")
	if err := os.WriteFile(binPath2, []byte("fakebin2"), 0o755); err != nil {
		t.Fatal(err)
	}
	multiYAML := `
name: otlp-multi
collector_binaries:
  - ./otelcol
  - ./otelcol-contrib
collector_config: ./col.yaml
memory_limit: "60MiB"
`
	multiCfgFile := filepath.Join(dir, "run-multi.yaml")
	if err := os.WriteFile(multiCfgFile, []byte(multiYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgMulti, err := Load(multiCfgFile, "")
	if err != nil {
		t.Fatalf("Load multi binaries unexpected error: %v", err)
	}
	if len(cfgMulti.CollectorBinaries) != 2 || cfgMulti.CollectorBinaries[0] != binPath || cfgMulti.CollectorBinaries[1] != binPath2 {
		t.Fatalf("CollectorBinaries = %v; want [%s, %s]", cfgMulti.CollectorBinaries, binPath, binPath2)
	}
	if cfgMulti.CollectorBinary != binPath {
		t.Fatalf("CollectorBinary = %q; want first binary %q", cfgMulti.CollectorBinary, binPath)
	}

	// CLI override replaces YAML list
	cfgOverride, err := LoadWithBinaryOverrides(multiCfgFile, "", "", []string{binPath2})
	if err != nil {
		t.Fatalf("LoadWithBinaryOverrides unexpected error: %v", err)
	}
	if len(cfgOverride.CollectorBinaries) != 1 || cfgOverride.CollectorBinary != binPath2 {
		t.Fatalf("CLI override got CollectorBinary=%q, CollectorBinaries=%v; want [%s]",
			cfgOverride.CollectorBinary, cfgOverride.CollectorBinaries, binPath2)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "otelcol")
	colCfgPath := filepath.Join(dir, "col.yaml")
	if err := os.WriteFile(binPath, []byte("fakebin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(colCfgPath, []byte("service: {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{
			name: "reject url binary",
			yaml: `
name: t
collector_binary: https://example.com/otelcol
collector_config: ./col.yaml
memory_limit: 50MiB
`,
			wantSub: "only local file paths are supported",
		},
		{
			name: "reject unknown field",
			yaml: `
name: t
collector_binary: ./otelcol
collector_config: ./col.yaml
memory_limits: ["50MiB"]
`,
			wantSub: "field memory_limits not found",
		},
		{
			name: "reject workload without signals",
			yaml: `
name: t
collector_binary: ./otelcol
collector_config: ./col.yaml
memory_limit: 50MiB
workload:
  enabled: true
`,
			wantSub: "workload.signals",
		},
		{
			name: "reject tiny memory limit",
			yaml: `
name: t
collector_binary: ./otelcol
collector_config: ./col.yaml
memory_limit: 512B
`,
			wantSub: "at least 4096 B",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".yaml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p, "")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q did not contain %q", err.Error(), tc.wantSub)
			}
		})
	}

	_ = time.Second
}
