package visualizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/results"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/symbols"
)

// loadPackages attributes each run's pages.csv to the Go packages of its Collector
// binary, returning one entry per run. Runs without pages.csv, or whose binary is
// missing or changed since the run, get nil and a warning on stderr: the dashboard
// renders without the package table rather than failing.
func loadPackages(runs []*results.RunData) [][]symbols.PackageStats {
	out := make([][]symbols.PackageStats, len(runs))
	maps := make(map[string]*symbols.Map) // sweeps reuse one binary: map it once
	for i, rd := range runs {
		pages, err := results.LoadPages(rd.Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "visualizer: %s: skipping package attribution: %v\n", rd.Dir, err)
			continue
		}
		if pages == nil {
			continue
		}
		pageSize := rd.Meta.PageSizeBytes
		if pageSize == 0 {
			pageSize = 4096
		}
		key := fmt.Sprintf("%s@%s/%d", rd.Meta.CollectorBinary, rd.Meta.CollectorBinarySHA256, pageSize)
		m, ok := maps[key]
		if !ok {
			m, err = openVerified(rd.Meta.CollectorBinary, rd.Meta.CollectorBinarySHA256, pageSize)
			if err != nil {
				fmt.Fprintf(os.Stderr, "visualizer: %s: skipping package attribution: %v\n", rd.Dir, err)
			}
			maps[key] = m // cache failures too, so the warning prints once per binary
		}
		if m != nil {
			out[i] = m.Attribute(pages)
		}
	}
	return out
}

// openVerified maps the binary at path after checking it is the one the run recorded.
func openVerified(path, wantSHA string, pageSize uint64) (*symbols.Map, error) {
	got, err := fileSHA256(path)
	if err != nil {
		return nil, err
	}
	if wantSHA != "" && got != wantSHA {
		return nil, fmt.Errorf("binary %q changed since the run (sha256 %s, run recorded %s)", path, got, wantSHA)
	}
	return symbols.Open(path, pageSize)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
