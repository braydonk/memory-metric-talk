package workload

import (
	"context"
	"fmt"
	"math"
	"net"
	"sync/atomic"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // Support gzip-compressed OTLP exports from Collector.
)

// Backend is a lightweight in-process OTLP gRPC discard receiver used as an exporter sink.
type Backend struct {
	addr       string
	lis        net.Listener
	grpcServer *grpc.Server

	requests atomic.Uint64
	items    atomic.Uint64
}

// NewBackend constructs a discard OTLP gRPC server configured to bind addr (e.g. "127.0.0.1:14317" or "127.0.0.1:0").
func NewBackend(addr string) *Backend {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	return &Backend{addr: addr}
}

// Start opens the TCP listener and begins serving OTLP Logs, Metrics, and Traces RPCs in the background.
func (b *Backend) Start() error {
	lis, err := net.Listen("tcp", b.addr)
	if err != nil {
		return fmt.Errorf("listen backend grpc %q: %w", b.addr, err)
	}
	b.lis = lis
	b.grpcServer = grpc.NewServer(grpc.MaxRecvMsgSize(math.MaxInt32))
	plogotlp.RegisterGRPCServer(b.grpcServer, &discardLogsServer{b: b})
	pmetricotlp.RegisterGRPCServer(b.grpcServer, &discardMetricsServer{b: b})
	ptraceotlp.RegisterGRPCServer(b.grpcServer, &discardTracesServer{b: b})

	go func() {
		_ = b.grpcServer.Serve(lis)
	}()
	return nil
}

// Addr returns the bound host:port address.
func (b *Backend) Addr() string {
	if b.lis == nil {
		return ""
	}
	return b.lis.Addr().String()
}

// Totals returns cumulative (requestsReceived, itemsReceived) across all signals.
func (b *Backend) Totals() (uint64, uint64) {
	return b.requests.Load(), b.items.Load()
}

// Stop gracefully shuts down the gRPC server until ctx expires, then forces stop.
func (b *Backend) Stop(ctx context.Context) {
	if b.grpcServer == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		b.grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		b.grpcServer.Stop()
	}
}

type discardLogsServer struct {
	plogotlp.UnimplementedGRPCServer
	b *Backend
}

func (s *discardLogsServer) Export(_ context.Context, req plogotlp.ExportRequest) (plogotlp.ExportResponse, error) {
	s.b.requests.Add(1)
	s.b.items.Add(uint64(req.Logs().LogRecordCount()))
	return plogotlp.NewExportResponse(), nil
}

type discardMetricsServer struct {
	pmetricotlp.UnimplementedGRPCServer
	b *Backend
}

func (s *discardMetricsServer) Export(_ context.Context, req pmetricotlp.ExportRequest) (pmetricotlp.ExportResponse, error) {
	s.b.requests.Add(1)
	s.b.items.Add(uint64(req.Metrics().DataPointCount()))
	return pmetricotlp.NewExportResponse(), nil
}

type discardTracesServer struct {
	ptraceotlp.UnimplementedGRPCServer
	b *Backend
}

func (s *discardTracesServer) Export(_ context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	s.b.requests.Add(1)
	s.b.items.Add(uint64(req.Traces().SpanCount()))
	return ptraceotlp.NewExportResponse(), nil
}
