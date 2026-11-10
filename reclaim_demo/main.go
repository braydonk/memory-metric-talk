// Command reclaim_demo runs several long-lived OpenTelemetry Collectors with the same
// configuration, each in its own cgroup v2 leaf (<root>/reclaim_demo/<name>) with its own
// memory limit and proactive reclaim policy. Linux only, needs root.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

func main() {
	configPath := flag.String("config", "demo.yaml", "demo configuration file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		log.Fatal(err)
	}
}

func run(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	collectorConfig, err := filepath.Abs(cfg.CollectorConfig)
	if err != nil {
		return err
	}
	parent, err := setupParent(cfg.CgroupRoot)
	if err != nil {
		return fmt.Errorf("set up %s/%s (run as root on cgroup v2): %w", cfg.CgroupRoot, parentName, err)
	}

	// work_dir may be a shared directory like /var/tmp, so only remove what we create.
	_, err = os.Stat(cfg.WorkDir)
	createdWorkDir := errors.Is(err, fs.ErrNotExist)
	var (
		cgroups []*cgroup
		dirs    []string
	)
	defer func() {
		for _, cg := range cgroups {
			if err := cg.remove(); err != nil {
				log.Printf("remove %s: %v", cg.dir, err)
			}
		}
		_ = os.Remove(parent)
		for _, dir := range dirs {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("remove %s: %v", dir, err)
			}
		}
		if createdWorkDir {
			_ = os.Remove(cfg.WorkDir)
		}
	}()

	runners := make([]*runner, 0, len(cfg.Instances))
	for i, in := range cfg.Instances {
		memoryMax, _ := parseLimit(in.MemoryMax) // validated in loadConfig
		cg, err := createCgroup(parent, in.Name, memoryMax)
		if err != nil {
			return err
		}
		cgroups = append(cgroups, cg)
		dir := filepath.Join(cfg.WorkDir, in.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		dirs = append(dirs, dir)
		binary := filepath.Join(dir, "otelcol")
		if err := copyCold(cfg.CollectorBinary, binary); err != nil {
			return fmt.Errorf("copy collector binary for %s: %w", in.Name, err)
		}
		runners = append(runners, &runner{
			cfg:    in,
			cg:     cg,
			binary: binary,
			port:   cfg.BasePort + i,
			logger: log.New(os.Stderr, fmt.Sprintf("[%s] ", in.Name), log.LstdFlags),
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Go(func() { r.run(ctx, collectorConfig, cfg.TelemetryEndpoint) })
		wg.Go(func() { r.control(ctx) })
		wg.Go(func() { r.load(ctx, cfg.Load) })
	}
	log.Printf("running %d collectors under %s, Ctrl-C to stop", len(runners), parent)
	wg.Wait()
	return nil
}
