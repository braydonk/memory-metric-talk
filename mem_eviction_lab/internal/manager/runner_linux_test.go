//go:build linux

package manager

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/cgroup"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/config"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

func TestClassifyVerdict(t *testing.T) {
	tests := []struct {
		name          string
		sum           results.FinalSummary
		churn         churnMetrics
		oomKilled     bool
		exitedEarly   bool
		exitCode      int
		startupErr    error
		stoppedByUser bool
		wantVerdict   string
	}{
		{
			name:          "oom killed takes precedence",
			sum:           results.FinalSummary{TotalEvictions: 500, TotalRefaults: 120, ThrashingPages: 40},
			churn:         churnMetrics{Sustained: true, EvalWindowSecs: 30, ChurnSecs1s: 28},
			oomKilled:     true,
			exitedEarly:   true,
			exitCode:      -1,
			startupErr:    errors.New("dial timeout"),
			stoppedByUser: true,
			wantVerdict:   results.VerdictOOMKilled,
		},
		{
			name:          "stopped by user without oom",
			sum:           results.FinalSummary{TotalColdFaults: 900, TotalEvictions: 200},
			stoppedByUser: true,
			wantVerdict:   results.VerdictStopped,
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
			wantVerdict: results.VerdictNoReclaim,
		},
		{
			name: "clean reclaim after high one-shot forced reclaim burst that stabilized",
			sum: results.FinalSummary{
				TotalEvictions:         42000,
				CleanlyEvictedBinBytes: 34000 * 4096,
				FinalBinResidentBytes:  6000 * 4096,
				TotalRefaults:          6000,
				EverRefaultedPages:     6000,
				ThrashingPages:         0,
				TotalPSIFullMs:         280.0,
			},
			churn: churnMetrics{
				PostStartup:        true,
				EvalWindowSecs:     55,
				ChurnSecs1s:        1,
				TrailingWindowSecs: 10,
				TrailingChurnSecs:  0,
				Sustained:          false,
			},
			wantVerdict: results.VerdictCleanReclaim,
		},
		{
			name: "sustained continuous churn reports thrashing",
			sum: results.FinalSummary{
				TotalEvictions:     120000,
				TotalRefaults:      95000,
				EverRefaultedPages: 8000,
				ThrashingPages:     3200,
				TotalPSIFullMs:     4500.0,
			},
			churn: churnMetrics{
				PostStartup:        true,
				EvalWindowSecs:     50,
				ChurnSecs1s:        42,
				TrailingWindowSecs: 10,
				TrailingChurnSecs:  10,
				Sustained:          true,
			},
			wantVerdict: results.VerdictThrashing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := classifyVerdict(tc.sum, tc.churn, tc.oomKilled, tc.exitedEarly, tc.exitCode, tc.startupErr, tc.stoppedByUser)
			if got != tc.wantVerdict {
				t.Fatalf("classifyVerdict() = (%q, %q); want %q", got, reason, tc.wantVerdict)
			}
			if reason == "" {
				t.Fatal("expected non-empty verdict reason")
			}
		})
	}
}

func TestAnalyzeChurnScenarios(t *testing.T) {
	// Helper constructing realistic 500ms-cadence time series with monotonically advancing cumulative counters.
	buildTrace := func(durSec int, perSec func(sec int) (ev, rf, dMax, dPSIUs uint64)) []results.SampleRow {
		rows := make([]results.SampleRow, 0, durSec*2)
		var cumEv, cumRf, cgMax, psiUs uint64
		for sec := 0; sec < durSec; sec++ {
			ev, rf, dm, dp := perSec(sec)
			// Split across two 500ms samples in the second.
			ev1, ev2 := ev/2, ev-ev/2
			rf1, rf2 := rf/2, rf-rf/2
			dm1, dm2 := dm/2, dm-dm/2
			dp1, dp2 := dp/2, dp-dp/2

			cumEv += ev1
			cumRf += rf1
			cgMax += dm1
			psiUs += dp1
			rows = append(rows, results.SampleRow{
				ElapsedMs:          int64(sec*1000 + 200),
				BinTotalPages:      100000,
				BinEverFaultedPages: 40000,
				DeltaEvictions:     ev1,
				DeltaRefaultsTotal: rf1,
				CumEvictions:       cumEv,
				CumRefaultsTotal:   cumRf,
				CGEventsMax:        cgMax,
				CGPSIFullTotalUs:   psiUs,
			})

			cumEv += ev2
			cumRf += rf2
			cgMax += dm2
			psiUs += dp2
			rows = append(rows, results.SampleRow{
				ElapsedMs:          int64(sec*1000 + 700),
				BinTotalPages:      100000,
				BinEverFaultedPages: 40000,
				DeltaEvictions:     ev2,
				DeltaRefaultsTotal: rf2,
				CumEvictions:       cumEv,
				CumRefaultsTotal:   cumRf,
				CGEventsMax:        cgMax,
				CGPSIFullTotalUs:   psiUs,
			})
		}
		return rows
	}

	t.Run("forced memory.reclaim bursts settle cleanly without thrashing", func(t *testing.T) {
		// 60s run (ready at 3.5s): user triggers massive Reclaim All File Cache at t=15s and t=35s.
		// Each bursts thousands of evictions + working-set reload refaults + 160ms PSI stall, then settles.
		samples := buildTrace(60, func(sec int) (ev, rf, dMax, dPSIUs uint64) {
			switch sec {
			case 15:
				return 38000, 5200, 0, 160_000
			case 16:
				return 0, 600, 0, 25_000 // trailing working-set reload with zero new evictions
			case 35:
				return 6000, 5400, 0, 150_000
			default:
				return 0, 0, 0, 0
			}
		})
		ch := analyzeChurn(samples, 3500, 0)
		if ch.Sustained {
			t.Fatalf("expected forced reclaim bursts to settle cleanly, got Sustained=true (%+v)", ch)
		}
		if ch.ChurnSecs1s != 2 {
			t.Fatalf("ChurnSecs1s = %d; want 2", ch.ChurnSecs1s)
		}
	})

	t.Run("startup init surge settles cleanly during steady state", func(t *testing.T) {
		// Modeled after otlp-120MiB: intense eviction/refault during Go init() (t=0..4s),
		// ready at 3.7s, then virtually silent across t=5..60s.
		samples := buildTrace(60, func(sec int) (ev, rf, dMax, dPSIUs uint64) {
			if sec <= 4 {
				return 12000, 6000, 400, 45_000
			}
			if sec == 25 {
				return 120, 10, 4, 0 // minor GC ripple below churn threshold
			}
			return 0, 0, 0, 0
		})
		ch := analyzeChurn(samples, 3700, 0)
		if ch.Sustained {
			t.Fatalf("expected settled post-startup runtime, got Sustained=true (%+v)", ch)
		}
		if !ch.PostStartup || ch.ChurnSecs1s != 0 {
			t.Fatalf("unexpected post-startup metrics: %+v", ch)
		}
	})

	t.Run("continuous steady-state churn flagged as thrashing", func(t *testing.T) {
		samples := buildTrace(60, func(sec int) (ev, rf, dMax, dPSIUs uint64) {
			if sec >= 10 {
				return 1800, 1750, 2500, 85_000
			}
			return 0, 0, 0, 0
		})
		ch := analyzeChurn(samples, 3500, 2500)
		if !ch.Sustained {
			t.Fatalf("expected Sustained=true on continuous churn (%+v)", ch)
		}
	})

	t.Run("periodic 5s scrape/GC thrashing flagged via 5s epochs", func(t *testing.T) {
		// Churn occurs once every 5s (20% of 1s buckets, below 25% 1s rule, but 100% of 5s epochs).
		samples := buildTrace(60, func(sec int) (ev, rf, dMax, dPSIUs uint64) {
			if sec >= 8 && sec%5 == 0 {
				return 900, 850, 120, 30_000
			}
			return 0, 0, 0, 0
		})
		ch := analyzeChurn(samples, 3500, 400)
		if !ch.Sustained {
			t.Fatalf("expected periodic 5s thrashing to set Sustained=true (%+v)", ch)
		}
	})

	t.Run("late-run limit reduction into continuous churn flagged via trailing window", func(t *testing.T) {
		// 180s run: completely calm until t=168s, then lowered memory.max causes nonstop thrashing to t=180s.
		samples := buildTrace(180, func(sec int) (ev, rf, dMax, dPSIUs uint64) {
			if sec >= 168 {
				return 2200, 2100, 3000, 95_000
			}
			return 0, 0, 0, 0
		})
		ch := analyzeChurn(samples, 4000, 1800)
		if !ch.Sustained || ch.TrailingChurnSecs != 10 {
			t.Fatalf("expected trailing-window thrashing detection, got %+v", ch)
		}
	})
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

	sum := acc.finalize(nil, 0, cgroup.Snapshot{Events: cgroup.Events{Max: 7}}, true, 4096, false, false, 0, nil, false)
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

func TestValidateOptionsAndWebSampleInterval(t *testing.T) {
	// Headless mode strictly requires --results-dir.
	err := validateOptions(Options{
		ConfigPath: "configs/run-otlp.yaml",
		Duration:   30 * time.Second,
		ResultsDir: "",
		WebMode:    false,
	})
	if err == nil || !strings.Contains(err.Error(), "--results-dir") {
		t.Fatalf("expected --results-dir error in headless mode, got: %v", err)
	}

	// Web mode allows omitting --results-dir.
	if err := validateOptions(Options{
		ConfigPath: "configs/run-otlp.yaml",
		Duration:   30 * time.Second,
		ResultsDir: "",
		WebMode:    true,
	}); err != nil {
		t.Fatalf("expected web mode without --results-dir to succeed, got: %v", err)
	}

	// Default 500ms tightens to 100ms in web mode, while explicit custom intervals stay unchanged.
	if got := effectiveSampleInterval(config.DefaultSampleInterval, true); got != WebDefaultSampleInterval {
		t.Fatalf("effectiveSampleInterval(default, web=true) = %s; want %s", got, WebDefaultSampleInterval)
	}
	if got := effectiveSampleInterval(250*time.Millisecond, true); got != 250*time.Millisecond {
		t.Fatalf("effectiveSampleInterval(250ms, web=true) = %s; want 250ms", got)
	}
	if got := effectiveSampleInterval(config.DefaultSampleInterval, false); got != config.DefaultSampleInterval {
		t.Fatalf("effectiveSampleInterval(default, web=false) = %s; want %s", got, config.DefaultSampleInterval)
	}

	tb := newTailBuffer(8)
	_, _ = tb.Write([]byte("hello "))
	_, _ = tb.Write([]byte("world!"))
	if got := tb.String(); got != "o world!" {
		t.Fatalf("tailBuffer = %q; want %q", got, "o world!")
	}
}

func TestSummaryAccumulatorWorkingSet(t *testing.T) {
	const mibB = 1024 * 1024
	var acc summaryAccumulator

	s1 := results.SampleRow{
		ElapsedMs:            500,
		BinTotalPages:        1000,
		BinPresentFilePages:  600,
		BinEverFaultedPages:  600,
		CGMemoryCurrentBytes: 120 * mibB,
		CGAnonBytes:          20 * mibB,
		CGKernelBytes:        2 * mibB,
		CGFileBytes:          98 * mibB,
		CGActiveFileBytes:    90 * mibB,
		CGInactiveFileBytes:  8 * mibB,
		ProcRSSBytes:         115 * mibB,
		ProcRSSAnonBytes:     20 * mibB,
		ProcRSSFileBytes:     95 * mibB,
	}
	s1.PopulateWorkingSet()

	s2 := results.SampleRow{
		ElapsedMs:              5000,
		BinTotalPages:          1000,
		BinPresentFilePages:    150,
		BinCleanlyEvictedPages: 450,
		BinEverFaultedPages:    600,
		CumEvictions:           450,
		CGMemoryCurrentBytes:   105 * mibB,
		CGAnonBytes:            70 * mibB,
		CGKernelBytes:          3 * mibB,
		CGFileBytes:            32 * mibB,
		CGActiveFileBytes:      12 * mibB,
		CGInactiveFileBytes:    20 * mibB,
		ProcRSSBytes:           130 * mibB,
		ProcRSSAnonBytes:       70 * mibB,
		ProcRSSFileBytes:       60 * mibB, // 28 MiB uncharged foreign RssFile
	}
	s2.PopulateWorkingSet()

	samples := []results.SampleRow{s1, s2}
	for _, r := range samples {
		acc.observe(r, 4096)
	}
	final := acc.finalize(samples, 0, cgroup.Snapshot{}, false, 4096, false, false, 0, nil, false)

	// s1: CG WS = 112 MiB, Iso WS = 112 MiB
	// s2: CG WS = 85 MiB,  Iso WS = 85 + 28 = 113 MiB
	if final.PeakCGWorkingSetBytes != 112*mibB || final.FinalCGWorkingSetBytes != 85*mibB {
		t.Fatalf("CG WS peak/final = (%d, %d); want (%d, %d)",
			final.PeakCGWorkingSetBytes, final.FinalCGWorkingSetBytes, 112*mibB, 85*mibB)
	}
	if final.PeakEstIsolatedWSBytes != 113*mibB || final.FinalEstIsolatedWSBytes != 113*mibB {
		t.Fatalf("Iso WS peak/final = (%d, %d); want (%d, %d)",
			final.PeakEstIsolatedWSBytes, final.FinalEstIsolatedWSBytes, 113*mibB, 113*mibB)
	}
	if final.FinalCGActiveFileBytes != 12*mibB || final.FinalCGInactiveFileBytes != 20*mibB || final.FinalProcRSSBytes != 130*mibB {
		t.Fatalf("final active/inactive/rss = (%d, %d, %d); want (%d, %d, %d)",
			final.FinalCGActiveFileBytes, final.FinalCGInactiveFileBytes, final.FinalProcRSSBytes,
			12*mibB, 20*mibB, 130*mibB)
	}
}
