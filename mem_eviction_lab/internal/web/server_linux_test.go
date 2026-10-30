//go:build linux

package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/bytefmt"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/manager"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/pagemap"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
)

type fakeLiveController struct {
	mu             sync.Mutex
	lastLimit      bytefmt.Limit
	reclaimTargets []uint64
}

func (f *fakeLiveController) SetMemoryMax(lim bytefmt.Limit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLimit = lim
	return nil
}

func (f *fakeLiveController) Reclaim(bytes uint64) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reclaimTargets = append(f.reclaimTargets, bytes)
	return bytes / 2, true, nil
}

func writeTestConfig(t *testing.T, dir string) string {
	t.Helper()
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	colCfg := filepath.Join(dir, "collector.yaml")
	if err := os.WriteFile(colCfg, []byte("service: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCfg := filepath.Join(dir, "run.yaml")
	yamlBody := fmt.Sprintf(`name: web-unit-test
collector_binary: %q
collector_config: %q
memory_limit: 80MiB
swap_max: "0"
`, selfExe, colCfg)
	if err := os.WriteFile(runCfg, []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return runCfg
}

func TestWebServerLifecycleSSEAndLiveControls(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeTestConfig(t, tmp)
	resBase := filepath.Join(tmp, "saved-runs")

	ctrl := &fakeLiveController{}
	runningReady := make(chan struct{})
	var runningOnce sync.Once

	runFn := func(ctx context.Context, opts manager.Options, _ *log.Logger) error {
		if !opts.WebMode {
			return fmt.Errorf("expected WebMode=true")
		}
		if opts.Hooks.OnCgroupReady != nil {
			opts.Hooks.OnCgroupReady(ctrl)
		}
		if opts.Hooks.OnLayout != nil {
			opts.Hooks.OnLayout(manager.RunLayout{
				PID:                    4242,
				PageSizeBytes:          4096,
				BlockSizePages:         64,
				TotalBinaryPages:       100,
				TotalBinaryMappedBytes: 100 * 4096,
				Spans: []pagemap.SectionSpan{
					{SectionName: ".text", VMAPerms: "r-xp", FileOffsetBytes: 4096, SizeBytes: 60 * 4096, StartPage: 0, PageCount: 60},
					{SectionName: ".data", VMAPerms: "rw-p", FileOffsetBytes: 64 * 4096, SizeBytes: 40 * 4096, StartPage: 60, PageCount: 40},
				},
			})
		}
		if opts.Hooks.OnReady != nil {
			opts.Hooks.OnReady(120)
		}
		if opts.Hooks.OnSample != nil {
			deltas := []uint32{
				pagemap.PackDelta(3, pagemap.CodeColdFaultFile),
				pagemap.PackDelta(62, pagemap.CodeColdFaultCoW),
			}
			opts.Hooks.OnSample(results.SampleRow{
				ElapsedMs:            150,
				Trigger:              results.TriggerPeriodic,
				BinTotalPages:        100,
				BinPresentFilePages:  1,
				BinPresentCoWPages:   1,
				BinEverFaultedPages:  2,
				DeltaColdFaults:      2,
				CumColdFaults:        2,
				CGMemoryCurrentBytes: 52 * 1024 * 1024,
				CGAnonBytes:          30 * 1024 * 1024,
				CGFileBytes:          20 * 1024 * 1024,
				CGKernelBytes:        2 * 1024 * 1024,
			}, deltas)
		}
		runningOnce.Do(func() { close(runningReady) })

		select {
		case <-ctx.Done():
		case <-opts.StopCh:
		}

		if opts.Hooks.OnDone != nil {
			opts.Hooks.OnDone(
				results.RunMeta{
					Name:           "web-unit-test",
					MemoryLimitRaw: opts.MemoryLimitOverride,
					PageSizeBytes:  4096,
					Summary: results.FinalSummary{
						Verdict:       results.VerdictStopped,
						VerdictReason: "stopped in test",
					},
				},
				[]results.SectionRow{
					{SectionName: ".text", VMAPerms: "r-xp", TotalPages: 60, CleanlyEvictedPages: 10},
				},
				"collector started ok\ncollector shutting down\n",
			)
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := log.New(io.Discard, "", 0)
	srv, err := NewServer(ctx, ServerOptions{
		ManagerOpts: manager.Options{
			ConfigPath: cfgPath,
			Duration:   30 * time.Second,
			ResultsDir: resBase,
		},
		SkipPreflight: true,
		RunFn:         runFn,
	}, logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.WaitActiveRun()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Static asset serving check.
	indexRes, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	indexBody, _ := io.ReadAll(indexRes.Body)
	indexRes.Body.Close()
	if indexRes.StatusCode != http.StatusOK || !bytes.Contains(indexBody, []byte("page-map-canvas")) {
		t.Fatalf("unexpected GET / response: status=%d", indexRes.StatusCode)
	}

	// 2. Initial /api/state includes preview binary layout.
	stRes, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state: %v", err)
	}
	var initState StateResponse
	if err := json.NewDecoder(stRes.Body).Decode(&initState); err != nil {
		t.Fatalf("decode /api/state: %v", err)
	}
	stRes.Body.Close()
	if initState.Config.PreviewLayout == nil || initState.Config.PreviewLayout.TotalBinaryPages == 0 {
		t.Fatalf("expected non-empty PreviewLayout in initial /api/state: %+v", initState.Config)
	}

	// 3. Origin & Content-Type guards on mutating endpoints.
	badOriginReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/run/start", strings.NewReader(`{}`))
	badOriginReq.Header.Set("Content-Type", "application/json")
	badOriginReq.Header.Set("Origin", "http://evil.example.com")
	badOriginRes, err := http.DefaultClient.Do(badOriginReq)
	if err != nil {
		t.Fatal(err)
	}
	badOriginRes.Body.Close()
	if badOriginRes.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d; want 403", badOriginRes.StatusCode)
	}

	badCTReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/run/start", strings.NewReader(`{}`))
	badCTReq.Header.Set("Content-Type", "text/plain")
	badCTRes, err := http.DefaultClient.Do(badCTReq)
	if err != nil {
		t.Fatal(err)
	}
	badCTRes.Body.Close()
	if badCTRes.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("plain-text status = %d; want 415", badCTRes.StatusCode)
	}

	// 4. Open SSE stream before starting run.
	sseCtx, sseCancel := context.WithCancel(ctx)
	defer sseCancel()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/api/events", nil)
	sseRes, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer sseRes.Body.Close()

	type parsedSSE struct {
		event string
		data  string
	}
	sseCh := make(chan parsedSSE, 32)
	go func() {
		sc := bufio.NewScanner(sseRes.Body)
		var curEvent string
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: ") {
				curEvent = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				sseCh <- parsedSSE{event: curEvent, data: strings.TrimPrefix(line, "data: ")}
			}
		}
	}()

	postJSON := func(path, body string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, b
	}

	// 5. Start run 1.
	startRes, startBody := postJSON("/api/run/start", `{"memory_limit":"96MiB","duration":"45s","swap_max":"0"}`)
	if startRes.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d (%s); want 202", startRes.StatusCode, startBody)
	}

	select {
	case <-runningReady:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fake runner to enter running state")
	}

	// Concurrent start must fail with 409 Conflict.
	dupRes, _ := postJSON("/api/run/start", `{}`)
	if dupRes.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate start status = %d; want 409", dupRes.StatusCode)
	}

	// 6. Live memory.max and memory.reclaim controls.
	maxRes, maxBody := postJSON("/api/run/memory-max", `{"memory_limit":"55MiB"}`)
	if maxRes.StatusCode != http.StatusOK {
		t.Fatalf("memory-max status = %d (%s); want 200", maxRes.StatusCode, maxBody)
	}
	ctrl.mu.Lock()
	gotLimit := ctrl.lastLimit.Bytes
	ctrl.mu.Unlock()
	if gotLimit != 55*1024*1024 {
		t.Fatalf("ctrl.lastLimit = %d; want 55MiB", gotLimit)
	}

	recRes, recBody := postJSON("/api/run/reclaim", `{"amount":"all_file"}`)
	if recRes.StatusCode != http.StatusOK {
		t.Fatalf("reclaim status = %d (%s); want 200", recRes.StatusCode, recBody)
	}
	ctrl.mu.Lock()
	targets := append([]uint64(nil), ctrl.reclaimTargets...)
	ctrl.mu.Unlock()
	if len(targets) != 1 || targets[0] != 20*1024*1024 {
		t.Fatalf("reclaim targets = %v; want [20MiB from CGFileBytes]", targets)
	}

	// 7. Stop run 1 gracefully.
	stopRes, stopBody := postJSON("/api/run/stop", `{}`)
	if stopRes.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d (%s); want 200", stopRes.StatusCode, stopBody)
	}
	srv.WaitActiveRun()

	// Drain SSE messages up through run_done and verify delta decoding.
	seenEvents := map[string]int{}
	var decodedDeltas []uint32
	deadline := time.After(3 * time.Second)
collectSSE:
	for {
		select {
		case msg := <-sseCh:
			seenEvents[msg.event]++
			if msg.event == "sample" {
				var env struct {
					Frame SampleFrame `json:"frame"`
				}
				if err := json.Unmarshal([]byte(msg.data), &env); err != nil {
					t.Fatalf("unmarshal sample SSE: %v", err)
				}
				raw, err := base64.StdEncoding.DecodeString(env.Frame.DeltasB64)
				if err != nil {
					t.Fatalf("decode deltas_b64: %v", err)
				}
				for i := 0; i+4 <= len(raw); i += 4 {
					decodedDeltas = append(decodedDeltas, binary.LittleEndian.Uint32(raw[i:i+4]))
				}
			}
			if msg.event == "run_done" {
				break collectSSE
			}
		case <-deadline:
			t.Fatalf("timed out waiting for run_done over SSE; seen=%v", seenEvents)
		}
	}

	for _, want := range []string{"run_started", "run_layout", "sample", "marker", "status_change", "run_done"} {
		if seenEvents[want] == 0 {
			t.Fatalf("missing SSE event %q in %v", want, seenEvents)
		}
	}
	if len(decodedDeltas) != 2 {
		t.Fatalf("decodedDeltas = %v; want 2 entries", decodedDeltas)
	}
	p0, c0 := pagemap.UnpackDelta(decodedDeltas[0])
	p1, c1 := pagemap.UnpackDelta(decodedDeltas[1])
	if p0 != 3 || c0 != pagemap.CodeColdFaultFile || p1 != 62 || c1 != pagemap.CodeColdFaultCoW {
		t.Fatalf("unexpected unpacked deltas: (%d,%d), (%d,%d)", p0, c0, p1, c1)
	}

	// 8. Verify reconnect /api/state contains full preserved history & subdirectories.
	stRes2, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var postState StateResponse
	if err := json.NewDecoder(stRes2.Body).Decode(&postState); err != nil {
		t.Fatal(err)
	}
	stRes2.Body.Close()
	if postState.ActiveRun == nil || postState.ActiveRun.Status != StatusFinished || len(postState.ActiveRun.Markers) != 3 {
		t.Fatalf("unexpected post-run /api/state: %+v", postState.ActiveRun)
	}
	if !strings.HasPrefix(postState.ActiveRun.ResultsSubdir, resBase) {
		t.Fatalf("ResultsSubdir = %q; want prefix %q", postState.ActiveRun.ResultsSubdir, resBase)
	}

	// 9. Verify starting a consecutive run works immediately without restarting server.
	startRes2, startBody2 := postJSON("/api/run/start", `{"memory_limit":"max","duration":"10s"}`)
	if startRes2.StatusCode != http.StatusAccepted {
		t.Fatalf("second run start status = %d (%s); want 202", startRes2.StatusCode, startBody2)
	}
	_, _ = postJSON("/api/run/stop", `{}`)
	srv.WaitActiveRun()
}
