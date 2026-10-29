//go:build linux

package manager

import (
	"errors"
	"slices"
	"testing"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/cgroup"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/config"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

func TestClassifyVerdict(t *testing.T) {
	tests := []struct {
		name            string
		sum             results.FinalSummary
		everFaulted     uint64
		oomKilled       bool
		exitedEarly     bool
		exitCode        int
		startupErr      error
		wantVerdict     string
	}{
		{
			name:        "oom killed takes precedence",
			sum:         results.FinalSummary{TotalEvictions: 500, TotalRefaults: 120, ThrashingPages: 40},
			everFaulted: 1000,
			oomKilled:   true,
			exitedEarly: true,
			exitCode:    -1,
			startupErr:  errors.New("dial timeout"),
			wantVerdict: results.VerdictOOMKilled,
		},
		{
			name:        "startup error without oom",
			startupErr:  errors.New("dial timeout"),
			wantVerdict: results.VerdictProcessError,
		},
		{
			name:        "early non-zero exit",
			exitedEarly: true,
			exitCode:    1,
			wantVerdict: results.VerdictProcessError,
		},
		{
			name:        "no reclaim triggered",
			sum:         results.FinalSummary{PeakCGMemoryBytes: 50 * 1024 * 1024},
			everFaulted: 10000,
			wantVerdict: results.VerdictNoReclaim,
		},
		{
			name: "clean reclaim with minor incidental refaults",
			sum: results.FinalSummary{
				TotalEvictions:         8000,
				CleanlyEvictedBinBytes: 7990 * 4096,
				FinalBinResidentBytes:  2000 * 4096,
				TotalRefaults:          10,
				ThrashingPages:         1,
				TotalPSIFullMs:         2.5,
			},
			everFaulted: 10000,
			wantVerdict: results.VerdictCleanReclaim,
		},
		{
			name: "thrashing by repeated refaults",
			sum: results.FinalSummary{
				TotalEvictions:     12000,
				TotalRefaults:      250,
				EverRefaultedPages: 80,
				ThrashingPages:     30,
				TotalPSIFullMs:     12.0,
			},
			everFaulted: 3000,
			wantVerdict: results.VerdictThrashing,
		},
		{
			name: "thrashing by high psi full stall",
			sum: results.FinalSummary{
				TotalEvictions: 5000,
				TotalRefaults:  20,
				TotalPSIFullMs: 250.0,
			},
			everFaulted: 5000,
			wantVerdict: results.VerdictThrashing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := classifyVerdict(tc.sum, tc.everFaulted, tc.oomKilled, tc.exitedEarly, tc.exitCode, tc.startupErr)
			if got != tc.wantVerdict {
				t.Fatalf("classifyVerdict() = (%q, %q); want %q", got, reason, tc.wantVerdict)
			}
			if reason == "" {
				t.Fatal("expected non-empty verdict reason")
			}
		})
	}
}

func TestSummaryAccumulatorAndEnv(t *testing.T) {
	var acc summaryAccumulator
	acc.observe(results.SampleRow{
		BinTotalPages:          1000,
		BinPresentFilePages:    600,
		BinEverFaultedPages:    600,
		CGMemoryCurrentBytes:   50 * 1024 * 1024,
		ProcRSSBytes:           50 * 1024 * 1024,
	}, 4096)
	acc.observe(results.SampleRow{
		BinTotalPages:          1000,
		BinPresentFilePages:    150,
		BinPresentCoWPages:     10,
		BinCleanlyEvictedPages: 440,
		BinEverFaultedPages:    600,
		CumColdFaults:          600,
		CumEvictions:           440,
		CGEventsMax:            5,
		CGMemoryCurrentBytes:   30 * 1024 * 1024,
		ProcRSSBytes:           30 * 1024 * 1024,
	}, 4096)

	sum := acc.finalize(nil, cgroup.Snapshot{Events: cgroup.Events{Max: 7}}, true, 4096, false, false, 0, nil)
	if sum.Verdict != results.VerdictCleanReclaim {
		t.Fatalf("Verdict = %q; want %q", sum.Verdict, results.VerdictCleanReclaim)
	}
	if sum.TotalCGEventsMax != 7 {
		t.Fatalf("TotalCGEventsMax = %d; want 7", sum.TotalCGEventsMax)
	}

	cfg := &config.Config{
		Env: map[string]string{"GOMEMLIMIT": "45MiB"},
		Workload: config.WorkloadConfig{
			Enabled:               true,
			CollectorGRPCEndpoint: "127.0.0.1:4317",
		},
	}
	env := buildCollectorEnv(cfg, nil)
	if !slices.Contains(env, "GOMEMLIMIT=45MiB") || !slices.Contains(env, EnvCollectorGRPCEndpoint+"=127.0.0.1:4317") {
		t.Fatalf("missing expected collector env entries in %v", env)
	}
}
