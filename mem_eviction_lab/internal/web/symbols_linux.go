//go:build linux

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/symbols"
)

// symbolCache holds the GET /api/symbols response for the configured Collector binary.
//
// The binary is only read by refresh, which runs at server start and right before each
// run starts (before manager drops the binary's page cache). Reading it mid-run would
// charge its page cache to the manager's cgroup and break cold-cache accounting.
type symbolCache struct {
	mu   sync.Mutex
	key  string // path, size and mtime of the mapped binary
	body []byte // marshaled response; nil when err is set
	err  error
}

// refresh re-maps binPath when its size or mtime changed since the last call.
func (c *symbolCache) refresh(binPath string, pageSize uint64) error {
	st, err := os.Stat(binPath)
	if err != nil {
		c.set("", nil, err)
		return err
	}
	key := fmt.Sprintf("%s:%d:%d", binPath, st.Size(), st.ModTime().UnixNano())

	c.mu.Lock()
	unchanged := key == c.key
	c.mu.Unlock()
	if unchanged {
		return nil
	}

	m, err := symbols.Open(binPath, pageSize)
	if err != nil {
		c.set(key, nil, err)
		return err
	}
	body, err := json.Marshal(struct {
		Key string       `json:"key"`
		Map *symbols.Map `json:"map"`
	}{key, m})
	c.set(key, body, err)
	return err
}

func (c *symbolCache) set(key string, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key, c.body, c.err = key, body, err
}

func (c *symbolCache) serve(w http.ResponseWriter) {
	c.mu.Lock()
	body, err := c.body, c.err
	c.mu.Unlock()
	if body == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("package attribution unavailable: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// refreshSymbols logs instead of failing: the UI works without the package panel.
func (s *Server) refreshSymbols() {
	if err := s.symbols.refresh(s.cfgSum.CollectorBinary, uint64(os.Getpagesize())); err != nil {
		s.logger.Printf("web: package attribution unavailable: %v", err)
	}
}

func (s *Server) handleGetSymbols(w http.ResponseWriter, _ *http.Request) {
	s.symbols.serve(w)
}
