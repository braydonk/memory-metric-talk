// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cgroupreceiver // import "github.com/braydonk/memory-metric-talk/cgroup_receiver"

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/scraper/scrapererror"

	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/filterset"
	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/metadata"
)

var (
	// kubepods-burstable-pod<uid>.slice (systemd driver, "_" instead of "-") or pod<uid> (cgroupfs driver).
	podUIDRe = regexp.MustCompile(`pod([0-9a-f]{8}[-_][0-9a-f]{4}[-_][0-9a-f]{4}[-_][0-9a-f]{4}[-_][0-9a-f]{12})`)
	// cri-containerd-<id>.scope, crio-<id>.scope, docker-<id>.scope or a bare <id> (cgroupfs driver).
	containerIDRe = regexp.MustCompile(`(?:^|-)([0-9a-f]{64})(?:\.scope)?$`)
)

// level is the kind of Kubernetes object a cgroup belongs to.
type level int

const (
	levelOther level = iota
	levelNode
	levelPod
	levelContainer
	// levelExtra is a cgroup matching extra_cgroups: reported as a container, but with
	// usage, working_set and rss too, because the kubelet doesn't know about it.
	levelExtra
)

// cgroupLevel classifies a cgroup path the way the kubelet reports on it: the root is the
// node, a pod<uid> directory is a pod, and a container scope below a pod is a container.
func cgroupLevel(cgPath string) level {
	if cgPath == "/" {
		return levelNode
	}
	base := path.Base(cgPath)
	switch {
	case containerIDRe.MatchString(base) && podUIDRe.MatchString(path.Dir(cgPath)):
		return levelContainer
	case podUIDRe.MatchString(base):
		return levelPod
	default:
		return levelOther
	}
}

type cgroupScraper struct {
	settings  receiver.Settings
	config    *Config
	mb        *metadata.MetricsBuilder
	includeFS filterset.FilterSet
	excludeFS filterset.FilterSet
	extraFS   filterset.FilterSet
}

func newCgroupScraper(settings receiver.Settings, cfg *Config) (*cgroupScraper, error) {
	s := &cgroupScraper{settings: settings, config: cfg}
	var err error
	if len(cfg.Include.Paths) > 0 {
		if s.includeFS, err = filterset.CreateFilterSet(cfg.Include.Paths, &cfg.Include.Config); err != nil {
			return nil, fmt.Errorf("error creating cgroup path include filters: %w", err)
		}
	}
	if len(cfg.Exclude.Paths) > 0 {
		if s.excludeFS, err = filterset.CreateFilterSet(cfg.Exclude.Paths, &cfg.Exclude.Config); err != nil {
			return nil, fmt.Errorf("error creating cgroup path exclude filters: %w", err)
		}
	}
	if len(cfg.ExtraCgroups.Paths) > 0 {
		if s.extraFS, err = filterset.CreateFilterSet(cfg.ExtraCgroups.Paths, &cfg.ExtraCgroups.Config); err != nil {
			return nil, fmt.Errorf("error creating extra_cgroups filters: %w", err)
		}
	}
	return s, nil
}

func (s *cgroupScraper) start(context.Context, component.Host) error {
	s.mb = metadata.NewMetricsBuilder(s.config.MetricsBuilderConfig, s.settings)
	return nil
}

func (s *cgroupScraper) scrape(context.Context) (pmetric.Metrics, error) {
	// HOST_SYS points at the host's /sys when the collector runs in a container.
	mount := filepath.Join(cmp.Or(os.Getenv("HOST_SYS"), "/sys"), "fs", "cgroup")
	var errs scrapererror.ScrapeErrors

	err := filepath.WalkDir(mount, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // cgroup removed during the walk
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(mount, dir)
		cgPath := "/" + strings.TrimPrefix(filepath.ToSlash(rel), ".")
		lvl := cgroupLevel(cgPath)
		if lvl == levelOther && s.extraFS != nil && s.extraFS.Matches(cgPath) {
			lvl = levelExtra
		}
		if lvl == levelOther {
			return nil // still walk children: pods and containers are nested in slices
		}
		if (s.includeFS != nil && !s.includeFS.Matches(cgPath)) || (s.excludeFS != nil && s.excludeFS.Matches(cgPath)) {
			return nil
		}
		if err := s.scrapeCgroup(dir, cgPath, lvl); err != nil {
			errs.AddPartial(1, fmt.Errorf("cgroup %s: %w", cgPath, err))
		}
		return nil
	})
	if err != nil {
		errs.Add(fmt.Errorf("walk %s: %w", mount, err))
	}
	return s.mb.Emit(), errs.Combine()
}

// scrapeCgroup records one cgroup's limits and memory pressure as its own resource. Missing
// files are skipped: the real root cgroup has no memory.max, and limits set to "max" are
// unlimited.
func (s *cgroupScraper) scrapeCgroup(dir, cgPath string, lvl level) error {
	recordLimit := map[level]func(pcommon.Timestamp, int64, metadata.AttributeMemoryLimitType){
		levelNode:      s.mb.RecordK8sNodeMemoryLimitDataPoint,
		levelPod:       s.mb.RecordK8sPodMemoryLimitDataPoint,
		levelContainer: s.mb.RecordContainerMemoryLimitDataPoint,
		levelExtra:     s.mb.RecordContainerMemoryLimitDataPoint,
	}[lvl]
	recordPressure := map[level]func(pcommon.Timestamp, float64, metadata.AttributePressureStallType){
		levelNode:      s.mb.RecordK8sNodeMemoryPressureStallTimeDataPoint,
		levelPod:       s.mb.RecordK8sPodMemoryPressureStallTimeDataPoint,
		levelContainer: s.mb.RecordContainerMemoryPressureStallTimeDataPoint,
		levelExtra:     s.mb.RecordContainerMemoryPressureStallTimeDataPoint,
	}[lvl]

	now := pcommon.NewTimestampFromTime(time.Now())
	var errs []error
	keep := func(err error) {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}

	if lvl == levelContainer || lvl == levelExtra {
		keep(s.recordContainerStats(now, dir, lvl == levelExtra))
	}

	for file, typ := range map[string]metadata.AttributeMemoryLimitType{
		"memory.high":     metadata.AttributeMemoryLimitTypeHigh,
		"memory.max":      metadata.AttributeMemoryLimitTypeMax,
		"memory.swap.max": metadata.AttributeMemoryLimitTypeSwapMax,
	} {
		if v, ok, err := readSingleValue(filepath.Join(dir, file)); ok {
			recordLimit(now, v, typ)
		} else {
			keep(err)
		}
	}

	pressure, err := readPressureTotals(filepath.Join(dir, "memory.pressure"))
	keep(err)
	for name, typ := range metadata.MapAttributePressureStallType {
		if us, ok := pressure[name]; ok {
			recordPressure(now, float64(us)/1e6, typ)
		}
	}

	rb := s.mb.NewResourceBuilder()
	rb.SetCgroupPath(cgPath)
	if m := podUIDRe.FindStringSubmatch(cgPath); m != nil {
		rb.SetK8sPodUID(strings.ReplaceAll(m[1], "_", "-"))
	}
	if m := containerIDRe.FindStringSubmatch(path.Base(cgPath)); lvl == levelContainer && m != nil {
		rb.SetContainerID(m[1])
	}
	s.mb.EmitForResource(metadata.WithResource(rb.Emit()))
	return errors.Join(errs...)
}

// recordContainerStats records the memory.stat and memory.events values the kubelet
// doesn't report. withUsage adds usage, working_set and rss (cAdvisor's formulas), for
// cgroups the kubelet doesn't know about.
func (s *cgroupScraper) recordContainerStats(now pcommon.Timestamp, dir string, withUsage bool) error {
	stat, err := readKeyValues(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return err
	}
	record := func(key string, fn func(int64)) {
		if v, ok := stat[key]; ok {
			fn(v)
		}
	}

	var errs []error
	if withUsage {
		usage, ok, err := readSingleValue(filepath.Join(dir, "memory.current"))
		if ok {
			s.mb.RecordContainerMemoryUsageDataPoint(now, usage)
			s.mb.RecordContainerMemoryWorkingSetDataPoint(now, max(0, usage-stat["inactive_file"]))
		} else if err != nil {
			errs = append(errs, err)
		}
		record("anon", func(v int64) { s.mb.RecordContainerMemoryRssDataPoint(now, v) })
	}

	record("file", func(v int64) { s.mb.RecordContainerMemoryFileDataPoint(now, v) })
	record("file_mapped", func(v int64) { s.mb.RecordContainerMemoryFileMappedDataPoint(now, v) })
	record("kernel", func(v int64) { s.mb.RecordContainerMemoryKernelDataPoint(now, v) })
	for src, attr := range metadata.MapAttributeReclaimSource {
		record("pgsteal_"+src, func(v int64) { s.mb.RecordContainerMemoryReclaimedDataPoint(now, v, attr) })
	}
	for typ, attr := range metadata.MapAttributeMemoryBackingType {
		record("workingset_refault_"+typ, func(v int64) { s.mb.RecordContainerMemoryWorkingsetRefaultsDataPoint(now, v, attr) })
	}

	events, err := readKeyValues(filepath.Join(dir, "memory.events"))
	if err != nil {
		errs = append(errs, err)
	}
	for name, typ := range metadata.MapAttributeMemoryEventType {
		if v, ok := events[name]; ok {
			s.mb.RecordContainerMemoryEventsDataPoint(now, v, typ)
		}
	}
	return errors.Join(errs...)
}

// readKeyValues parses "key value" lines (memory.stat).
func readKeyValues(path string) (map[string]int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64)
	for line := range strings.Lines(string(data)) {
		key, val, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		v, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s key %q: %w", path, key, err)
		}
		out[key] = v
	}
	return out, nil
}

// readSingleValue reads a one-value cgroup file. ok is false for "max" (unlimited) and on error.
func readSingleValue(path string) (v int64, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, false, nil
	}
	v, err = strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, true, nil
}

// readPressureTotals parses the cumulative `total=` microseconds of each line of a PSI file:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=106340
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=105885
func readPressureTotals(path string) (map[string]int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64)
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, f := range fields[1:] {
			if total, ok := strings.CutPrefix(f, "total="); ok {
				v, err := strconv.ParseInt(total, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parse %s: %w", path, err)
				}
				out[fields[0]] = v
			}
		}
	}
	return out, nil
}
