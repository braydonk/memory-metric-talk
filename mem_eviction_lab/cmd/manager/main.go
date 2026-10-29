//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/cgroup"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/manager"
)

func main() {
	var opts manager.Options
	flag.StringVar(&opts.ConfigPath, "config", "", "Path to lab run YAML configuration file (required)")
	flag.DurationVar(&opts.Duration, "duration", 60*time.Second, "Active observation / workload duration")
	flag.StringVar(&opts.ResultsDir, "results-dir", "", "Output directory for CSV, JSON, and log artifacts (required)")
	flag.StringVar(&opts.MemoryLimitOverride, "memory-limit", "", "Optional override for memory_limit in YAML (e.g. \"max\", \"60MiB\")")
	flag.BoolVar(&opts.AllowWarmCache, "allow-warm-cache", false, "Continue even if binary pages remain pinned in host page cache after FADV_DONTNEED")
	flag.StringVar(&opts.CgroupRoot, "cgroup-root", cgroup.DefaultRoot, "Cgroup v2 unified hierarchy mount point")
	flag.Parse()

	if opts.ConfigPath == "" || opts.ResultsDir == "" {
		flag.Usage()
		os.Exit(2)
	}

	logger := log.New(os.Stderr, "[manager] ", log.LstdFlags|log.Lmsgprefix)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := manager.Run(ctx, opts, logger)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
