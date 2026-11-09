//go:build linux

package web

import (
	"cmp"
	"context"
	"debug/elf"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/bytefmt"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/cgroup"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/config"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/elfmap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/manager"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/pagecache"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/pagemap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

//go:embed static/*
var staticEmbed embed.FS

// Run lifecycle states exposed to the UI.
const (
	StatusIdle     = "idle"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusStopping = "stopping"
	StatusFinished = "finished"
)

// ServerOptions configures the interactive localhost web mode.
type ServerOptions struct {
	ManagerOpts   manager.Options
	Port          int
	RunFn         func(ctx context.Context, opts manager.Options, logger *log.Logger) error
	SkipPreflight bool // used by unit tests without root/cgroups
}

// CollectorBinaryOption describes one pre-validated Collector binary selectable in the web UI.
type CollectorBinaryOption struct {
	Path          string             `json:"path"`
	Name          string             `json:"name"`
	Size          uint64             `json:"size"`
	ELFSections   []elfmap.Section   `json:"elf_sections"`
	PreviewLayout *manager.RunLayout `json:"preview_layout,omitempty"`
}

// ConfigSummary presents static configuration defaults and binary section metadata to the UI.
type ConfigSummary struct {
	Name                  string                  `json:"name"`
	ConfigPath            string                  `json:"config_path"`
	CollectorBinary       string                  `json:"collector_binary"`
	CollectorBinarySize   uint64                  `json:"collector_binary_size"`
	CollectorBinaries     []CollectorBinaryOption `json:"collector_binaries"`
	CollectorConfig       string                  `json:"collector_config"`
	DefaultMemoryLimit    string                  `json:"default_memory_limit"`
	DefaultSwapMax        string                  `json:"default_swap_max"`
	DefaultDuration       string                  `json:"default_duration"`
	BaseResultsDir        string                  `json:"base_results_dir,omitempty"`
	WorkloadEnabled       bool                    `json:"workload_enabled"`
	BackendEnabled        bool                    `json:"backend_enabled"`
	CollectorGRPCEndpoint string                  `json:"collector_grpc_endpoint,omitempty"`
	ELFSections           []elfmap.Section        `json:"elf_sections"`
	PreviewLayout         *manager.RunLayout      `json:"preview_layout,omitempty"`
}

// SampleFrame represents one recorded benchmark sample and its packed uint32 visual deltas.
type SampleFrame struct {
	Seq       int               `json:"seq"`
	Row       results.SampleRow `json:"row"`
	DeltasB64 string            `json:"deltas_b64,omitempty"`
}

// TimelineMarker pins an asynchronous event (readiness, live memory.max update, or memory.reclaim) on the timeline.
type TimelineMarker struct {
	Kind                 string `json:"kind"`
	ElapsedMs            int64  `json:"elapsed_ms"`
	Label                string `json:"label"`
	Detail               string `json:"detail,omitempty"`
	MemoryLimitRaw       string `json:"memory_limit_raw,omitempty"`
	MemoryLimitHuman     string `json:"memory_limit_human,omitempty"`
	MemoryLimitUnlimited bool   `json:"memory_limit_unlimited,omitempty"`
	MemoryLimitBytes     uint64 `json:"memory_limit_bytes,omitempty"`
	ReclaimedBytes       uint64 `json:"reclaimed_bytes,omitempty"`
	Partial              bool   `json:"partial,omitempty"`
}

// RunSnapshot holds full state and frame history for the active or most recently completed benchmark run.
type RunSnapshot struct {
	RunID                       uint64               `json:"run_id"`
	Status                      string               `json:"status"`
	StartedAtUnixMs             int64                `json:"started_at_unix_ms"`
	CollectorBinary             string               `json:"collector_binary"`
	CollectorBinarySize         uint64               `json:"collector_binary_size"`
	InitialMemoryLimitHuman     string               `json:"initial_memory_limit_human"`
	InitialMemoryLimitUnlimited bool                 `json:"initial_memory_limit_unlimited"`
	InitialMemoryLimitBytes     uint64               `json:"initial_memory_limit_bytes"`
	MemoryLimitRaw              string               `json:"memory_limit_raw"`
	MemoryLimitHuman            string               `json:"memory_limit_human"`
	MemoryLimitUnlimited        bool                 `json:"memory_limit_unlimited"`
	MemoryLimitBytes            uint64               `json:"memory_limit_bytes"`
	SwapMaxRaw           string               `json:"swap_max_raw"`
	ConfiguredDuration   string               `json:"configured_duration"`
	DurationMsTarget     int64                `json:"duration_ms_target"`
	ResultsSubdir        string               `json:"results_subdir,omitempty"`
	Layout               *manager.RunLayout   `json:"layout,omitempty"`
	StartupReadinessMs   int64                `json:"startup_readiness_ms,omitempty"`
	Frames               []SampleFrame        `json:"frames"`
	Markers              []TimelineMarker     `json:"markers"`
	FinalMeta            *results.RunMeta     `json:"final_meta,omitempty"`
	Sections             []results.SectionRow `json:"sections,omitempty"`
	CollectorLogTail     string               `json:"collector_log_tail,omitempty"`
	RunError             string               `json:"run_error,omitempty"`
}

// StateResponse is returned by GET /api/state on initial page load or SSE reconnect.
type StateResponse struct {
	Config    ConfigSummary `json:"config"`
	ActiveRun *RunSnapshot  `json:"active_run,omitempty"`
}

type sseMessage struct {
	event string
	data  []byte
}

// Server coordinates benchmark runs and serves the interactive localhost UI.
type Server struct {
	baseOpts ServerOptions
	cfgSum   ConfigSummary
	logger   *log.Logger
	rootCtx  context.Context

	mu          sync.Mutex
	nextRunID   uint64
	activeRun   *RunSnapshot
	activeStart time.Time
	ctrl        manager.LiveController
	stopCh      chan struct{}
	stopClosed  bool
	runDoneCh   chan struct{}

	subsMu sync.Mutex
	subs   map[chan sseMessage]struct{}

	symbols symbolCache // GET /api/symbols page -> package map
}

// Serve validates prerequisites, binds strictly to 127.0.0.1:<port>, and runs the HTTP server until ctx is cancelled.
func Serve(ctx context.Context, opts ServerOptions, logger *log.Logger) error {
	if logger == nil {
		logger = log.Default()
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return fmt.Errorf("-web port %d must be between 1 and 65535", opts.Port)
	}

	srv, err := NewServer(ctx, opts, logger)
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("127.0.0.1:%d", opts.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if serr := httpSrv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			errCh <- serr
			return
		}
		errCh <- nil
	}()

	logger.Printf("web interface ready at http://%s (loopback only)", ln.Addr().String())
	if srv.cfgSum.BaseResultsDir != "" {
		logger.Printf("runs will be saved under %s", srv.cfgSum.BaseResultsDir)
	} else {
		logger.Printf("running in live in-memory mode (--results-dir not set)")
	}

	select {
	case serr := <-errCh:
		return serr
	case <-ctx.Done():
		logger.Printf("shutting down web server...")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		srv.WaitActiveRun()
		return nil
	}
}

// NewServer constructs a validated Server instance ready for Handler() dispatch.
func NewServer(ctx context.Context, opts ServerOptions, logger *log.Logger) (*Server, error) {
	if logger == nil {
		logger = log.Default()
	}
	if opts.ManagerOpts.ConfigPath == "" {
		return nil, errors.New("--config is required")
	}
	if opts.ManagerOpts.Duration <= 0 {
		return nil, errors.New("--duration must be > 0")
	}
	if opts.RunFn == nil {
		opts.RunFn = manager.Run
	}

	cfg, err := config.LoadWithBinaryOverrides(
		opts.ManagerOpts.ConfigPath,
		opts.ManagerOpts.MemoryLimitOverride,
		opts.ManagerOpts.SwapMaxOverride,
		opts.ManagerOpts.CollectorBinariesOverride,
	)
	if err != nil {
		return nil, err
	}

	if !opts.SkipPreflight {
		if os.Geteuid() != 0 {
			return nil, errors.New("manager must be run as root (e.g. via sudo) to create cgroups and read /proc/[pid]/pagemap PFNs")
		}
		if err := cgroup.CheckUnifiedMount(opts.ManagerOpts.CgroupRoot); err != nil {
			return nil, err
		}
		if _, err := pagecache.StatFileID(cfg.CollectorConfig); err != nil {
			return nil, err
		}
	}

	binOpts := make([]CollectorBinaryOption, 0, len(cfg.CollectorBinaries))
	for _, binPath := range cfg.CollectorBinaries {
		var (
			sections []elfmap.Section
			binSize  uint64
		)
		if !opts.SkipPreflight {
			elfLayout, err := elfmap.Parse(binPath)
			if err != nil {
				return nil, err
			}
			sections = elfLayout.Sections
			binID, err := pagecache.StatFileID(binPath)
			if err != nil {
				return nil, err
			}
			binSize = binID.Size
		} else {
			if st, serr := os.Stat(binPath); serr == nil {
				binSize = uint64(st.Size())
			}
			if elfLayout, eerr := elfmap.Parse(binPath); eerr == nil {
				sections = elfLayout.Sections
			}
		}
		binOpts = append(binOpts, CollectorBinaryOption{
			Path:          binPath,
			Name:          filepath.Base(binPath),
			Size:          binSize,
			ELFSections:   sections,
			PreviewLayout: buildPreviewLayout(binPath, cfg.BlockSizePages),
		})
	}
	defaultBin := binOpts[0]

	var baseResultsDir string
	if strings.TrimSpace(opts.ManagerOpts.ResultsDir) != "" {
		absRes, err := filepath.Abs(opts.ManagerOpts.ResultsDir)
		if err != nil {
			return nil, fmt.Errorf("resolve --results-dir %q: %w", opts.ManagerOpts.ResultsDir, err)
		}
		baseResultsDir = absRes
	}

	absCfg, _ := filepath.Abs(opts.ManagerOpts.ConfigPath)
	cfgSum := ConfigSummary{
		Name:                  cfg.Name,
		ConfigPath:            absCfg,
		CollectorBinary:       defaultBin.Path,
		CollectorBinarySize:   defaultBin.Size,
		CollectorBinaries:     binOpts,
		CollectorConfig:       cfg.CollectorConfig,
		DefaultMemoryLimit:    cfg.MemoryLimit,
		DefaultSwapMax:        cfg.SwapMax,
		DefaultDuration:       opts.ManagerOpts.Duration.String(),
		BaseResultsDir:        baseResultsDir,
		WorkloadEnabled:       cfg.Workload.Enabled,
		BackendEnabled:        cfg.Backend.Enabled,
		CollectorGRPCEndpoint: cfg.Workload.CollectorGRPCEndpoint,
		ELFSections:           defaultBin.ELFSections,
		PreviewLayout:         defaultBin.PreviewLayout,
	}

	srv := &Server{
		baseOpts: opts,
		cfgSum:   cfgSum,
		logger:   logger,
		rootCtx:  ctx,
		subs:     make(map[chan sseMessage]struct{}),
	}
	srv.refreshSymbols()
	return srv, nil
}

// WaitActiveRun blocks until any currently executing benchmark run finishes tearing down.
func (s *Server) WaitActiveRun() {
	s.mu.Lock()
	doneCh := s.runDoneCh
	s.mu.Unlock()
	if doneCh != nil {
		<-doneCh
	}
}

// Handler returns the HTTP handler serving embedded static assets, SSE events, and control endpoints.
func (s *Server) Handler() http.Handler {
	subFS, err := fs.Sub(staticEmbed, "static")
	if err != nil {
		panic(fmt.Sprintf("embed static fs: %v", err))
	}
	staticServer := http.FileServer(http.FS(subFS))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleGetState)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("POST /api/run/start", s.requireSameOriginJSON(s.handleRunStart))
	mux.HandleFunc("POST /api/run/stop", s.requireSameOriginJSON(s.handleRunStop))
	mux.HandleFunc("POST /api/run/memory-max", s.requireSameOriginJSON(s.handleSetMemoryMax))
	mux.HandleFunc("POST /api/run/reclaim", s.requireSameOriginJSON(s.handleReclaim))
	mux.HandleFunc("GET /api/symbols", s.handleGetSymbols)
	mux.Handle("GET /", staticServer)
	return mux
}

func (s *Server) requireSameOriginJSON(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
			writeJSONError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !isAllowedLocalOrigin(origin, r.Host) {
				writeJSONError(w, http.StatusForbidden, "cross-origin requests are forbidden")
				return
			}
		}
		next(w, r)
	}
}

func isAllowedLocalOrigin(origin, reqHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	if u.Host == reqHost {
		h := u.Hostname()
		return h == "127.0.0.1" || h == "localhost"
	}
	return false
}

func (s *Server) handleGetState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	resp := StateResponse{
		Config: s.cfgSum,
	}
	if s.activeRun != nil {
		cp := cloneRunSnapshot(s.activeRun)
		resp.ActiveRun = &cp
	}
	s.mu.Unlock()

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func cloneRunSnapshot(in *RunSnapshot) RunSnapshot {
	cp := *in
	if in.Layout != nil {
		lcp := *in.Layout
		lcp.Spans = append([]pagemap.SectionSpan(nil), in.Layout.Spans...)
		cp.Layout = &lcp
	}
	cp.Frames = append([]SampleFrame(nil), in.Frames...)
	cp.Markers = append([]TimelineMarker(nil), in.Markers...)
	if in.FinalMeta != nil {
		mcp := *in.FinalMeta
		cp.FinalMeta = &mcp
	}
	cp.Sections = append([]results.SectionRow(nil), in.Sections...)
	return cp
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan sseMessage, 512)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		delete(s.subs, ch)
		s.subsMu.Unlock()
	}()

	// Initial heartbeat comment flushes response headers immediately.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.rootCtx.Done():
			return
		case msg := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.event, msg.data); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) broadcast(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		s.logger.Printf("warning: marshal SSE %s failed: %v", event, err)
		return
	}
	msg := sseMessage{event: event, data: data}

	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- msg:
		default:
			// Slow subscriber falls back to GET /api/state resync on next received frame sequence gap.
		}
	}
}

type startRunRequest struct {
	CollectorBinary string `json:"collector_binary"`
	MemoryLimit     string `json:"memory_limit"`
	SwapMax         string `json:"swap_max"`
	Duration        string `json:"duration"`
}

func (s *Server) resolveBinaryOption(raw string) (CollectorBinaryOption, bool) {
	q := strings.TrimSpace(raw)
	if q == "" && len(s.cfgSum.CollectorBinaries) > 0 {
		return s.cfgSum.CollectorBinaries[0], true
	}
	for _, opt := range s.cfgSum.CollectorBinaries {
		if opt.Path == q {
			return opt, true
		}
	}
	for _, opt := range s.cfgSum.CollectorBinaries {
		if opt.Name == q {
			return opt, true
		}
	}
	return CollectorBinaryOption{}, false
}

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	var req startRunRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	selectedBin, ok := s.resolveBinaryOption(req.CollectorBinary)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unknown collector_binary %q (must match one of the configured binaries)", req.CollectorBinary))
		return
	}

	memRaw := strings.TrimSpace(req.MemoryLimit)
	if memRaw == "" {
		memRaw = s.cfgSum.DefaultMemoryLimit
	}
	memLim, err := bytefmt.ParseLimit(memRaw, false)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid memory_limit: %v", err))
		return
	}
	if !memLim.Unlimited && memLim.Bytes < 4096 {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("memory_limit (%d B) must be at least 4096 B or \"max\"", memLim.Bytes))
		return
	}

	swapRaw := strings.TrimSpace(req.SwapMax)
	if swapRaw == "" {
		swapRaw = s.cfgSum.DefaultSwapMax
	}
	if _, err := bytefmt.ParseLimit(swapRaw, true); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid swap_max: %v", err))
		return
	}

	durRaw := strings.TrimSpace(req.Duration)
	var dur time.Duration
	if durRaw == "" {
		dur = s.baseOpts.ManagerOpts.Duration
	} else {
		dur, err = time.ParseDuration(durRaw)
		if err != nil || dur <= 0 {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid duration %q (expected positive Go duration like 30s or 2m)", durRaw))
			return
		}
	}

	s.mu.Lock()
	if s.isActiveLocked() {
		s.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "a benchmark run is already active")
		return
	}

	s.nextRunID++
	runID := s.nextRunID
	now := time.Now().UTC()
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	var resultsSubdir string
	if s.cfgSum.BaseResultsDir != "" {
		var dirName string
		if len(s.cfgSum.CollectorBinaries) > 1 {
			dirName = fmt.Sprintf("%s-%s-%s-%s-%d",
				sanitizePathToken(s.cfgSum.Name),
				sanitizePathToken(selectedBin.Name),
				sanitizePathToken(memRaw),
				now.Format("20060102-150405"),
				runID,
			)
		} else {
			dirName = fmt.Sprintf("%s-%s-%s-%d",
				sanitizePathToken(s.cfgSum.Name),
				sanitizePathToken(memRaw),
				now.Format("20060102-150405"),
				runID,
			)
		}
		resultsSubdir = filepath.Join(s.cfgSum.BaseResultsDir, dirName)
	}

	snap := &RunSnapshot{
		RunID:                       runID,
		Status:                      StatusStarting,
		StartedAtUnixMs:             now.UnixMilli(),
		CollectorBinary:             selectedBin.Path,
		CollectorBinarySize:         selectedBin.Size,
		InitialMemoryLimitHuman:     memLim.Human(),
		InitialMemoryLimitUnlimited: memLim.Unlimited,
		InitialMemoryLimitBytes:     memLim.Bytes,
		MemoryLimitRaw:              memRaw,
		MemoryLimitHuman:            memLim.Human(),
		MemoryLimitUnlimited:        memLim.Unlimited,
		MemoryLimitBytes:            memLim.Bytes,
		SwapMaxRaw:                  swapRaw,
		ConfiguredDuration:          dur.String(),
		DurationMsTarget:            dur.Milliseconds(),
		ResultsSubdir:               resultsSubdir,
		Frames:                      make([]SampleFrame, 0, 512),
		Markers:                     make([]TimelineMarker, 0, 8),
	}

	s.activeRun = snap
	s.activeStart = now
	s.ctrl = nil
	s.stopCh = stopCh
	s.stopClosed = false
	s.runDoneCh = doneCh

	startedEvent := map[string]any{
		"run_id":                 snap.RunID,
		"status":                 snap.Status,
		"started_at_unix_ms":     snap.StartedAtUnixMs,
		"collector_binary":       snap.CollectorBinary,
		"collector_binary_size":  snap.CollectorBinarySize,
		"memory_limit_raw":       snap.MemoryLimitRaw,
		"memory_limit_human":     snap.MemoryLimitHuman,
		"memory_limit_unlimited": snap.MemoryLimitUnlimited,
		"memory_limit_bytes":     snap.MemoryLimitBytes,
		"swap_max_raw":           snap.SwapMaxRaw,
		"configured_duration":    snap.ConfiguredDuration,
		"duration_ms_target":     snap.DurationMsTarget,
		"results_subdir":         snap.ResultsSubdir,
	}
	s.mu.Unlock()

	s.broadcast("run_started", startedEvent)

	runOpts := s.baseOpts.ManagerOpts
	runOpts.WebMode = true
	runOpts.CollectorBinariesOverride = []string{selectedBin.Path}
	runOpts.Duration = dur
	runOpts.MemoryLimitOverride = memRaw
	runOpts.SwapMaxOverride = swapRaw
	runOpts.ResultsDir = resultsSubdir
	runOpts.StopCh = stopCh
	runOpts.Hooks = s.makeRunHooks(runID)

	// Before manager drops the binary page cache: picks up a rebuilt binary between runs.
	s.refreshSymbols()

	go func() {
		defer close(doneCh)
		rerr := s.baseOpts.RunFn(s.rootCtx, runOpts, s.logger)
		s.finalizeAfterReturn(runID, rerr)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":         runID,
		"status":         StatusStarting,
		"results_subdir": resultsSubdir,
	})
}

func (s *Server) makeRunHooks(runID uint64) *manager.Hooks {
	return &manager.Hooks{
		OnCgroupReady: func(ctrl manager.LiveController) {
			s.mu.Lock()
			if s.activeRun != nil && s.activeRun.RunID == runID {
				s.ctrl = ctrl
				s.activeStart = time.Now().UTC()
			}
			s.mu.Unlock()
		},
		OnLayout: func(layout manager.RunLayout) {
			lcp := layout
			lcp.Spans = append([]pagemap.SectionSpan(nil), layout.Spans...)
			s.mu.Lock()
			if s.activeRun == nil || s.activeRun.RunID != runID {
				s.mu.Unlock()
				return
			}
			if s.activeRun.Status == StatusStarting {
				s.activeRun.Status = StatusRunning
			}
			s.activeRun.Layout = &lcp
			status := s.activeRun.Status
			s.mu.Unlock()

			s.broadcast("run_layout", map[string]any{
				"run_id": runID,
				"status": status,
				"layout": lcp,
			})
		},
		OnReady: func(elapsedMs int64) {
			mk := TimelineMarker{
				Kind:      "ready",
				ElapsedMs: elapsedMs,
				Label:     fmt.Sprintf("Ready (%.2fs)", float64(elapsedMs)/1000.0),
				Detail:    "Collector OTLP endpoint accepting connections; steady-state load started",
			}
			s.mu.Lock()
			if s.activeRun == nil || s.activeRun.RunID != runID {
				s.mu.Unlock()
				return
			}
			s.activeRun.StartupReadinessMs = elapsedMs
			s.activeRun.Markers = append(s.activeRun.Markers, mk)
			s.mu.Unlock()

			s.broadcast("marker", map[string]any{
				"run_id": runID,
				"marker": mk,
			})
		},
		OnSample: func(row results.SampleRow, deltas []uint32) {
			b64 := encodeDeltasBase64(deltas)
			s.mu.Lock()
			if s.activeRun == nil || s.activeRun.RunID != runID {
				s.mu.Unlock()
				return
			}
			if s.activeRun.Status == StatusStarting {
				s.activeRun.Status = StatusRunning
			}
			frame := SampleFrame{
				Seq:       len(s.activeRun.Frames),
				Row:       row,
				DeltasB64: b64,
			}
			s.activeRun.Frames = append(s.activeRun.Frames, frame)
			s.mu.Unlock()

			s.broadcast("sample", map[string]any{
				"run_id": runID,
				"frame":  frame,
			})
		},
		OnDone: func(meta results.RunMeta, sections []results.SectionRow, collectorLogTail string) {
			mcp := meta
			secCp := append([]results.SectionRow(nil), sections...)
			s.mu.Lock()
			if s.activeRun == nil || s.activeRun.RunID != runID {
				s.mu.Unlock()
				return
			}
			s.activeRun.Status = StatusFinished
			s.activeRun.FinalMeta = &mcp
			s.activeRun.Sections = secCp
			s.activeRun.CollectorLogTail = collectorLogTail
			s.ctrl = nil
			s.mu.Unlock()

			s.broadcast("run_done", map[string]any{
				"run_id":             runID,
				"status":             StatusFinished,
				"final_meta":         mcp,
				"sections":           secCp,
				"collector_log_tail": collectorLogTail,
			})
		},
	}
}

func (s *Server) finalizeAfterReturn(runID uint64, runErr error) {
	s.mu.Lock()
	if s.activeRun == nil || s.activeRun.RunID != runID {
		s.mu.Unlock()
		return
	}
	alreadyDone := s.activeRun.Status == StatusFinished && s.activeRun.FinalMeta != nil
	s.activeRun.Status = StatusFinished
	s.ctrl = nil
	var errMsg string
	if runErr != nil {
		errMsg = runErr.Error()
		s.activeRun.RunError = errMsg
	}
	finalMeta := s.activeRun.FinalMeta
	sections := append([]results.SectionRow(nil), s.activeRun.Sections...)
	logTail := s.activeRun.CollectorLogTail
	s.mu.Unlock()

	if !alreadyDone || errMsg != "" {
		s.broadcast("run_done", map[string]any{
			"run_id":             runID,
			"status":             StatusFinished,
			"final_meta":         finalMeta,
			"sections":           sections,
			"collector_log_tail": logTail,
			"run_error":          errMsg,
		})
	}
}

func (s *Server) handleRunStop(w http.ResponseWriter, r *http.Request) {
	// Drain optional JSON body gracefully.
	var dummy map[string]any
	_ = decodeJSONBody(r, &dummy)

	s.mu.Lock()
	if s.activeRun == nil || (s.activeRun.Status != StatusStarting && s.activeRun.Status != StatusRunning) {
		s.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no running benchmark to stop")
		return
	}
	runID := s.activeRun.RunID
	if !s.stopClosed && s.stopCh != nil {
		close(s.stopCh)
		s.stopClosed = true
	}
	s.activeRun.Status = StatusStopping
	s.mu.Unlock()

	s.broadcast("status_change", map[string]any{
		"run_id": runID,
		"status": StatusStopping,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": runID,
		"status": StatusStopping,
	})
}

type setMemoryMaxRequest struct {
	MemoryLimit string `json:"memory_limit"`
}

func (s *Server) handleSetMemoryMax(w http.ResponseWriter, r *http.Request) {
	var req setMemoryMaxRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw := strings.TrimSpace(req.MemoryLimit)
	lim, err := bytefmt.ParseLimit(raw, false)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid memory_limit: %v", err))
		return
	}
	if !lim.Unlimited && lim.Bytes < 4096 {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("memory_limit (%d B) must be at least 4096 B or \"max\"", lim.Bytes))
		return
	}

	s.mu.Lock()
	if s.activeRun == nil || s.ctrl == nil || s.activeRun.Status != StatusRunning {
		s.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no active running cgroup to modify")
		return
	}
	ctrl := s.ctrl
	runID := s.activeRun.RunID
	s.mu.Unlock()

	if err := ctrl.SetMemoryMax(lim); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.mu.Lock()
	elapsedMs := s.currentElapsedLocked()
	mk := TimelineMarker{
		Kind:                 "limit_change",
		ElapsedMs:            elapsedMs,
		Label:                "memory.max → " + lim.Human(),
		Detail:               fmt.Sprintf("Updated live cgroup memory.max to %s", lim.Human()),
		MemoryLimitRaw:       raw,
		MemoryLimitHuman:     lim.Human(),
		MemoryLimitUnlimited: lim.Unlimited,
		MemoryLimitBytes:     lim.Bytes,
	}
	if s.activeRun != nil && s.activeRun.RunID == runID {
		s.activeRun.MemoryLimitRaw = raw
		s.activeRun.MemoryLimitHuman = lim.Human()
		s.activeRun.MemoryLimitUnlimited = lim.Unlimited
		s.activeRun.MemoryLimitBytes = lim.Bytes
		s.activeRun.Markers = append(s.activeRun.Markers, mk)
	}
	s.mu.Unlock()

	s.broadcast("marker", map[string]any{
		"run_id": runID,
		"marker": mk,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":                 runID,
		"memory_limit_raw":       raw,
		"memory_limit_human":     lim.Human(),
		"memory_limit_unlimited": lim.Unlimited,
		"memory_limit_bytes":     lim.Bytes,
	})
}

type reclaimRequest struct {
	Amount string `json:"amount"`
}

func (s *Server) handleReclaim(w http.ResponseWriter, r *http.Request) {
	var req reclaimRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	amtRaw := strings.TrimSpace(req.Amount)
	if amtRaw == "" {
		writeJSONError(w, http.StatusBadRequest, "amount is required (e.g. \"16MiB\", \"64MiB\", or \"all_file\")")
		return
	}

	s.mu.Lock()
	if s.activeRun == nil || s.ctrl == nil || s.activeRun.Status != StatusRunning {
		s.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "no active running cgroup to reclaim from")
		return
	}
	ctrl := s.ctrl
	runID := s.activeRun.RunID
	var (
		hasBeforeRow    bool
		beforeRow       results.SampleRow
		latestFileBytes uint64
		pageSize        uint64 = 4096
	)
	if s.activeRun.Layout != nil && s.activeRun.Layout.PageSizeBytes > 0 {
		pageSize = s.activeRun.Layout.PageSizeBytes
	}
	if n := len(s.activeRun.Frames); n > 0 {
		beforeRow = s.activeRun.Frames[n-1].Row
		hasBeforeRow = true
		latestFileBytes = max(beforeRow.CGFileBytes, beforeRow.BinPresentFilePages*pageSize)
	}
	s.mu.Unlock()

	var targetBytes uint64
	if strings.EqualFold(amtRaw, "all_file") {
		if latestFileBytes >= 4096 {
			targetBytes = latestFileBytes
		} else {
			targetBytes = 64 * 1024 * 1024
		}
	} else {
		lim, err := bytefmt.ParseLimit(amtRaw, false)
		if err != nil || lim.Unlimited || lim.Bytes == 0 {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid reclaim amount %q (expected positive size like 16MiB or \"all_file\")", amtRaw))
			return
		}
		targetBytes = lim.Bytes
	}

	reclaimed, partial, err := ctrl.Reclaim(targetBytes)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.mu.Lock()
	elapsedMs := s.currentElapsedLocked()
	var (
		mappedEvictedBytes     uint64
		unmappedReclaimedBytes uint64
	)
	if hasBeforeRow && s.activeRun != nil && s.activeRun.RunID == runID && len(s.activeRun.Frames) > 0 {
		afterRow := s.activeRun.Frames[len(s.activeRun.Frames)-1].Row
		evPages := afterRow.DeltaEvictions
		if beforeRow.BinPresentFilePages > afterRow.BinPresentFilePages {
			evPages = max(evPages, beforeRow.BinPresentFilePages-afterRow.BinPresentFilePages)
		}
		mappedEvictedBytes = evPages * pageSize

		beforeUnmapped := results.UnmappedFileCacheBytes(beforeRow.CGFileBytes, beforeRow.CGFileMappedBytes)
		afterUnmapped := results.UnmappedFileCacheBytes(afterRow.CGFileBytes, afterRow.CGFileMappedBytes)
		if beforeUnmapped > afterUnmapped {
			unmappedReclaimedBytes = beforeUnmapped - afterUnmapped
		}
	}

	reclaimedHuman := bytefmt.FormatIEC(reclaimed)
	targetHuman := bytefmt.FormatIEC(targetBytes)
	detail := fmt.Sprintf("memory.reclaim(%s) freed %s", targetHuman, reclaimedHuman)
	if mappedEvictedBytes > 0 || unmappedReclaimedBytes > 0 {
		detail += fmt.Sprintf(" (%s mapped binary PTEs, %s unmapped readahead cache)",
			bytefmt.FormatIEC(mappedEvictedBytes),
			bytefmt.FormatIEC(unmappedReclaimedBytes))
	}
	if partial {
		detail += " (reached active/unreclaimable LRU floor)"
	}

	mk := TimelineMarker{
		Kind:           "reclaim",
		ElapsedMs:      elapsedMs,
		Label:          "Reclaim −" + reclaimedHuman,
		Detail:         detail,
		ReclaimedBytes: reclaimed,
		Partial:        partial,
	}

	if s.activeRun != nil && s.activeRun.RunID == runID {
		s.activeRun.Markers = append(s.activeRun.Markers, mk)
	}
	s.mu.Unlock()

	s.broadcast("marker", map[string]any{
		"run_id": runID,
		"marker": mk,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":                   runID,
		"target_bytes":             targetBytes,
		"target_human":             targetHuman,
		"reclaimed_bytes":          reclaimed,
		"reclaimed_human":          reclaimedHuman,
		"mapped_evicted_bytes":     mappedEvictedBytes,
		"unmapped_reclaimed_bytes": unmappedReclaimedBytes,
		"partial":                  partial,
		"detail":                   detail,
	})
}

func (s *Server) isActiveLocked() bool {
	if s.activeRun == nil {
		return false
	}
	switch s.activeRun.Status {
	case StatusStarting, StatusRunning, StatusStopping:
		return true
	default:
		return false
	}
}

func (s *Server) currentElapsedLocked() int64 {
	if s.activeRun != nil && len(s.activeRun.Frames) > 0 {
		return s.activeRun.Frames[len(s.activeRun.Frames)-1].Row.ElapsedMs
	}
	if !s.activeStart.IsZero() {
		return time.Since(s.activeStart).Milliseconds()
	}
	return 0
}

func encodeDeltasBase64(deltas []uint32) string {
	if len(deltas) == 0 {
		return ""
	}
	buf := make([]byte, len(deltas)*4)
	for i, d := range deltas {
		binary.LittleEndian.PutUint32(buf[i*4:i*4+4], d)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func sanitizePathToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "run"
	}
	return out
}

func decodeJSONBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64*1024))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func buildPreviewLayout(binPath string, blockSizePages int) *manager.RunLayout {
	f, err := elf.Open(binPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	pageSize := uint64(os.Getpagesize())
	if pageSize == 0 {
		pageSize = 4096
	}
	if blockSizePages < 1 {
		blockSizePages = config.DefaultBlockSizePages
	}

	type rawSec struct {
		name  string
		perms string
		start uint64
		np    int
	}
	var rawSecs []rawSec
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NULL || s.Type == elf.SHT_NOBITS || s.Size == 0 || s.Name == "" {
			continue
		}
		if s.Flags&elf.SHF_ALLOC == 0 {
			continue
		}
		perms := "r--p"
		if s.Flags&elf.SHF_EXECINSTR != 0 {
			perms = "r-xp"
		} else if s.Flags&elf.SHF_WRITE != 0 {
			perms = "rw-p"
		}
		startOff := (s.Offset / pageSize) * pageSize
		endOff := ((s.Offset + s.Size + pageSize - 1) / pageSize) * pageSize
		np := int((endOff - startOff) / pageSize)
		if np <= 0 {
			continue
		}
		rawSecs = append(rawSecs, rawSec{name: s.Name, perms: perms, start: startOff, np: np})
	}
	slices.SortFunc(rawSecs, func(a, b rawSec) int {
		return cmp.Compare(a.start, b.start)
	})

	spans := make([]pagemap.SectionSpan, 0, len(rawSecs))
	totalPages := 0
	for _, rs := range rawSecs {
		spans = append(spans, pagemap.SectionSpan{
			SectionName:     rs.name,
			VMAPerms:        rs.perms,
			FileOffsetBytes: rs.start,
			SizeBytes:       uint64(rs.np) * pageSize,
			StartPage:       totalPages,
			PageCount:       rs.np,
		})
		totalPages += rs.np
	}
	if totalPages == 0 {
		return nil
	}
	return &manager.RunLayout{
		PageSizeBytes:          pageSize,
		BlockSizePages:         blockSizePages,
		TotalBinaryPages:       uint64(totalPages),
		TotalBinaryMappedBytes: uint64(totalPages) * pageSize,
		Spans:                  spans,
	}
}
