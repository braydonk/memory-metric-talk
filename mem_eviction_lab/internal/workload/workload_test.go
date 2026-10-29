package workload

import (
	"context"
	"testing"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/config"
)

func TestBackendAndGeneratorLoopback(t *testing.T) {
	b := NewBackend("127.0.0.1:0")
	if err := b.Start(); err != nil {
		t.Fatalf("Backend.Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		b.Stop(ctx)
	}()

	ctx := context.Background()
	if err := WaitTCPReady(ctx, b.Addr(), 2*time.Second); err != nil {
		t.Fatalf("WaitTCPReady: %v", err)
	}

	gen := NewGenerator(config.WorkloadConfig{
		Enabled:               true,
		CollectorGRPCEndpoint: b.Addr(),
		Signals: map[config.SignalType]*config.SignalConfig{
			config.SignalLogs:    {Rate: 200, BatchSize: 10},
			config.SignalMetrics: {Rate: 200, BatchSize: 10},
			config.SignalTraces:  {Rate: 200, BatchSize: 10},
		},
	})
	if err := gen.Start(ctx); err != nil {
		t.Fatalf("Generator.Start: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, items := b.Totals()
		if items >= 30 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	gen.Stop()

	reqs, itemsRecv := b.Totals()
	batchesSent, itemsSent, errs := gen.Totals()
	if errs != 0 {
		t.Fatalf("expected 0 generator errors, got %d", errs)
	}
	if reqs == 0 || itemsRecv == 0 || batchesSent == 0 || itemsSent == 0 {
		t.Fatalf("expected non-zero loopback traffic: reqs=%d itemsRecv=%d batchesSent=%d itemsSent=%d",
			reqs, itemsRecv, batchesSent, itemsSent)
	}
}
