//go:build linux

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/manager"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/symbols"
)

func TestSymbolsEndpointRefreshesOnRebuild(t *testing.T) {
	tmp := t.TempDir()
	// Copy the (Go) test binary so the test can touch it like a rebuild.
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(selfExe)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "collector")
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	colCfg := filepath.Join(tmp, "collector.yaml")
	if err := os.WriteFile(colCfg, []byte("service: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCfg := filepath.Join(tmp, "run.yaml")
	if err := os.WriteFile(runCfg, []byte(fmt.Sprintf("name: sym\ncollector_binary: %q\ncollector_config: %q\nmemory_limit: 80MiB\n", bin, colCfg)), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(context.Background(), ServerOptions{
		ManagerOpts:   manager.Options{ConfigPath: runCfg, Duration: time.Second},
		SkipPreflight: true,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	type resp struct {
		Key string      `json:"key"`
		Map symbols.Map `json:"map"`
	}
	get := func() (int, resp) {
		t.Helper()
		res, err := http.Get(ts.URL + "/api/symbols")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var r resp
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode, r
	}

	code, first := get()
	if code != http.StatusOK {
		t.Fatalf("GET /api/symbols = %d; want 200", code)
	}
	if first.Key == "" || len(first.Map.Packages) == 0 || len(first.Map.Shares) == 0 || len(first.Map.Shares)%3 != 0 {
		t.Fatalf("unexpected map: key=%q packages=%d shares=%d", first.Key, len(first.Map.Packages), len(first.Map.Shares))
	}
	if first.Map.PageSize != uint64(os.Getpagesize()) {
		t.Errorf("page_size = %d; want %d", first.Map.PageSize, os.Getpagesize())
	}

	// Same binary: refresh is a no-op.
	srv.refreshSymbols()
	if _, again := get(); again.Key != first.Key {
		t.Errorf("key changed without a rebuild: %q -> %q", first.Key, again.Key)
	}

	// "Rebuild" (new mtime): the next refresh, as run start does, re-symbolizes.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	srv.refreshSymbols()
	if _, rebuilt := get(); rebuilt.Key == first.Key {
		t.Errorf("key unchanged after rebuild: %q", rebuilt.Key)
	}

	// Missing binary: 404 so the UI hides the panel.
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	srv.refreshSymbols()
	if code, _ := get(); code != http.StatusNotFound {
		t.Errorf("GET /api/symbols with missing binary = %d; want 404", code)
	}
}
