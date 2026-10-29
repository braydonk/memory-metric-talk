package workload

import (
	"context"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/config"
)

// WaitTCPReady polls endpoint via TCP dial until it accepts a connection or ctx expires.
func WaitTCPReady(ctx context.Context, endpoint string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		conn, err := dialer.DialContext(waitCtx, "tcp", endpoint)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timed out waiting for Collector endpoint %q: %w (last dial err: %v)", endpoint, waitCtx.Err(), err)
		case <-ticker.C:
		}
	}
}

// Generator pushes steady OTLP gRPC telemetry across configured signals to exercise active pipeline code pages.
type Generator struct {
	cfg config.WorkloadConfig

	conn   *grpc.ClientConn
	cancel context.CancelFunc
	wg     sync.WaitGroup

	batchesSent atomic.Uint64
	itemsSent   atomic.Uint64
	errorsTotal atomic.Uint64
}

// NewGenerator constructs a workload Generator from cfg.
func NewGenerator(cfg config.WorkloadConfig) *Generator {
	return &Generator{cfg: cfg}
}

// Start connects to CollectorGRPCEndpoint and starts rate-limited signal workers.
func (g *Generator) Start(parentCtx context.Context) error {
	if !g.cfg.Enabled || len(g.cfg.Signals) == 0 {
		return nil
	}
	conn, err := grpc.NewClient(
		g.cfg.CollectorGRPCEndpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(math.MaxInt32)),
	)
	if err != nil {
		return fmt.Errorf("dial collector grpc %q: %w", g.cfg.CollectorGRPCEndpoint, err)
	}
	g.conn = conn

	ctx, cancel := context.WithCancel(parentCtx)
	g.cancel = cancel

	for sig, sc := range g.cfg.Signals {
		if sc == nil {
			continue
		}
		g.startSignalWorker(ctx, sig, *sc)
	}
	return nil
}

func (g *Generator) startSignalWorker(ctx context.Context, sig config.SignalType, sc config.SignalConfig) {
	batchSize := max(sc.BatchSize, 1)
	batchesPerSec := sc.Rate / float64(batchSize)
	if batchesPerSec <= 0 {
		batchesPerSec = 1
	}
	limiter := rate.NewLimiter(rate.Limit(batchesPerSec), 1)

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		var (
			logClient    plogotlp.GRPCClient
			metricClient pmetricotlp.GRPCClient
			traceClient  ptraceotlp.GRPCClient
		)
		switch sig {
		case config.SignalLogs:
			logClient = plogotlp.NewGRPCClient(g.conn)
		case config.SignalMetrics:
			metricClient = pmetricotlp.NewGRPCClient(g.conn)
		case config.SignalTraces:
			traceClient = ptraceotlp.NewGRPCClient(g.conn)
		default:
			return
		}

		var seq uint64
		for {
			if err := limiter.Wait(ctx); err != nil {
				return
			}
			seq++
			now := pcommon.NewTimestampFromTime(time.Now())
			rpcCtx, rpcCancel := context.WithTimeout(ctx, 5*time.Second)

			var err error
			switch sig {
			case config.SignalLogs:
				req := plogotlp.NewExportRequestFromLogs(buildLogsBatch(batchSize, now, seq))
				_, err = logClient.Export(rpcCtx, req)
			case config.SignalMetrics:
				req := pmetricotlp.NewExportRequestFromMetrics(buildMetricsBatch(batchSize, now, seq))
				_, err = metricClient.Export(rpcCtx, req)
			case config.SignalTraces:
				req := ptraceotlp.NewExportRequestFromTraces(buildTracesBatch(batchSize, now, seq))
				_, err = traceClient.Export(rpcCtx, req)
			}
			rpcCancel()

			if err != nil {
				if ctx.Err() != nil {
					return
				}
				g.errorsTotal.Add(1)
				continue
			}
			g.batchesSent.Add(1)
			g.itemsSent.Add(uint64(batchSize))
		}
	}()
}

// Totals returns cumulative (batchesSent, itemsSent, errorsTotal).
func (g *Generator) Totals() (uint64, uint64, uint64) {
	return g.batchesSent.Load(), g.itemsSent.Load(), g.errorsTotal.Load()
}

// Stop stops all workers and closes the gRPC client connection.
func (g *Generator) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	if g.conn != nil {
		_ = g.conn.Close()
		g.conn = nil
	}
}

func buildLogsBatch(n int, ts pcommon.Timestamp, seq uint64) plog.Logs {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "mem-eviction-lab")
	sl := rl.ScopeLogs().AppendEmpty()
	slrs := sl.LogRecords()
	slrs.EnsureCapacity(n)
	for i := 0; i < n; i++ {
		lr := slrs.AppendEmpty()
		lr.SetTimestamp(ts)
		lr.SetSeverityNumber(plog.SeverityNumberInfo)
		lr.SetSeverityText("INFO")
		lr.Body().SetStr("mem_eviction_lab synthetic log record payload")
		lr.Attributes().PutInt("batch.seq", int64(seq))
		lr.Attributes().PutInt("item.idx", int64(i))
	}
	return ld
}

func buildMetricsBatch(n int, ts pcommon.Timestamp, seq uint64) pmetric.Metrics {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "mem-eviction-lab")
	sm := rm.ScopeMetrics().AppendEmpty()
	m := sm.Metrics().AppendEmpty()
	m.SetName("mem_eviction_lab.synthetic.gauge")
	dps := m.SetEmptyGauge().DataPoints()
	dps.EnsureCapacity(n)
	for i := 0; i < n; i++ {
		dp := dps.AppendEmpty()
		dp.SetTimestamp(ts)
		dp.SetDoubleValue(float64((seq + uint64(i)) % 1000))
		dp.Attributes().PutInt("series.id", int64(i%16))
	}
	return md
}

func buildTracesBatch(n int, ts pcommon.Timestamp, seq uint64) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "mem-eviction-lab")
	ss := rs.ScopeSpans().AppendEmpty()
	spans := ss.Spans()
	spans.EnsureCapacity(n)
	endTs := pcommon.Timestamp(uint64(ts) + uint64(time.Millisecond))
	for i := 0; i < n; i++ {
		sp := spans.AppendEmpty()
		var tid [16]byte
		tid[0] = byte(seq >> 8)
		tid[1] = byte(seq)
		tid[15] = byte(i + 1)
		var sid [8]byte
		sid[0] = byte(seq)
		sid[7] = byte(i + 1)
		sp.SetTraceID(tid)
		sp.SetSpanID(sid)
		sp.SetName("mem_eviction_lab.op")
		sp.SetStartTimestamp(ts)
		sp.SetEndTimestamp(endTs)
		sp.Attributes().PutInt("item.idx", int64(i))
	}
	return td
}
