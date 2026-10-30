//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/cgroup"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/manager"
	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/web"
)

func parseCLI(args []string) (manager.Options, error) {
	var opts manager.Options
	fs := flag.NewFlagSet("manager", flag.ContinueOnError)
	fs.StringVar(&opts.ConfigPath, "config", "", "Path to lab run YAML configuration file (required)")
	fs.DurationVar(&opts.Duration, "duration", 60*time.Second, "Active observation / workload duration")
	fs.StringVar(&opts.ResultsDir, "results-dir", "", "Output directory for CSV, JSON, and log artifacts (required unless -web is set)")
	fs.StringVar(&opts.MemoryLimitOverride, "memory-limit", "", "Optional override for memory_limit in YAML (e.g. \"max\", \"60MiB\")")
	fs.BoolVar(&opts.AllowWarmCache, "allow-warm-cache", false, "Continue even if binary pages remain pinned in host page cache after FADV_DONTNEED")
	fs.StringVar(&opts.CgroupRoot, "cgroup-root", cgroup.DefaultRoot, "Cgroup v2 unified hierarchy mount point")
	fs.IntVar(&opts.WebPort, "web", 0, "Start interactive localhost web interface on 127.0.0.1:<port> instead of running immediately")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.ConfigPath == "" {
		fs.Usage()
		return opts, errors.New("--config is required")
	}
	if opts.WebPort < 0 || opts.WebPort > 65535 {
		fs.Usage()
		return opts, fmt.Errorf("-web port %d must be between 1 and 65535", opts.WebPort)
	}
	if opts.WebPort == 0 && opts.ResultsDir == "" {
		fs.Usage()
		return opts, errors.New("--results-dir is required when -web is omitted")
	}
	opts.WebMode = opts.WebPort > 0
	return opts, nil
}

func main() {
	opts, err := parseCLI(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	logger := log.New(os.Stderr, "[manager] ", log.LstdFlags|log.Lmsgprefix)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if opts.WebMode {
		err = web.Serve(ctx, web.ServerOptions{
			ManagerOpts: opts,
			Port:        opts.WebPort,
		}, logger)
	} else {
		err = manager.Run(ctx, opts, logger)
	}
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
