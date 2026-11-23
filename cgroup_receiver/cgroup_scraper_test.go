// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cgroupreceiver

import (
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/filterset"
	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/metadata"
)

const (
	qosSlice    = "/kubepods.slice/kubepods-burstable.slice"
	podSlice    = qosSlice + "/kubepods-burstable-pod1b2c3d4e_0000_4f00_8a00_123456789abc.slice"
	containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ctrScope    = podSlice + "/cri-containerd-" + containerID + ".scope"
	// A container outside Kubernetes: not below a pod cgroup, so it's skipped.
	dockerScope = "/system.slice/docker-" + containerID + ".scope"
)

const (
	containerStat = `anon 1000
file 1900
kernel 100
file_mapped 1200
active_file 1400
inactive_file 500
workingset_refault_anon 3
workingset_refault_file 40
pgsteal_kswapd 10
pgsteal_direct 20
pgsteal_proactive 300
pgsteal_khugepaged 0
`
	containerEvents = "low 0\nhigh 0\nmax 42\noom 2\noom_kill 1\noom_group_kill 0\n"
)

// containerStatPoints are the values recorded from containerStat and containerEvents.
var containerStatPoints = map[string]float64{
	"container.memory.file":                           1900,
	"container.memory.file.mapped":                    1200,
	"container.memory.kernel":                         100,
	"container.memory.reclaimed{source=kswapd}":       10,
	"container.memory.reclaimed{source=direct}":       20,
	"container.memory.reclaimed{source=proactive}":    300,
	"container.memory.reclaimed{source=khugepaged}":   0,
	"container.memory.workingset.refaults{type=anon}": 3,
	"container.memory.workingset.refaults{type=file}": 40,
	"container.memory.events{type=high}":              0,
	"container.memory.events{type=max}":               42,
	"container.memory.events{type=oom}":               2,
	"container.memory.events{type=oom_kill}":          1,
}

func withStatPoints(m map[string]float64) map[string]float64 {
	maps.Copy(m, containerStatPoints)
	return m
}

// writeTree builds a fake cgroup v2 mount under <root>/sys/fs/cgroup.
func writeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mount := filepath.Join(root, "sys", "fs", "cgroup")
	write := func(cg, file, content string) {
		dir := filepath.Join(mount, cg)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644))
	}
	// Root cgroup: no limit files.
	write("/", "memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=1500000\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=500000\n")
	// Cgroups that aren't the node, a pod or a container: skipped even with memory files.
	write(qosSlice, "memory.max", "max\n")
	write(qosSlice, "memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=1\n")
	write(dockerScope, "memory.max", "1000\n")
	// Pod slice: memory.max is the sum of the container limits; memory.high is unlimited.
	write(podSlice, "memory.max", "268435456\n")
	write(podSlice, "memory.high", "max\n")
	write(podSlice, "memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=300000\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=150000\n")
	// Container scope.
	write(ctrScope, "memory.max", "125829120\n")
	write(ctrScope, "memory.high", "max\n")
	write(ctrScope, "memory.swap.max", "0\n")
	write(ctrScope, "memory.pressure", "some avg10=1.00 avg60=0.50 avg300=0.10 total=250000\nfull avg10=0.50 avg60=0.20 avg300=0.05 total=100000\n")
	write(ctrScope, "memory.stat", containerStat)
	write(ctrScope, "memory.events", containerEvents)
	return root
}

func scrape(t *testing.T, root string, cfg *Config) pmetric.Metrics {
	t.Helper()
	s, err := newCgroupScraper(receivertest.NewNopSettings(metadata.Type), cfg)
	require.NoError(t, err)
	require.NoError(t, s.start(t.Context(), componenttest.NewNopHost()))
	t.Setenv("HOST_SYS", filepath.Join(root, "sys"))
	md, err := s.scrape(t.Context())
	require.NoError(t, err)
	return md
}

// points flattens one resource's metrics into "name{attr=value,...}" -> value.
func points(rm pmetric.ResourceMetrics) map[string]float64 {
	out := map[string]float64{}
	ms := rm.ScopeMetrics().At(0).Metrics()
	for i := 0; i < ms.Len(); i++ {
		m := ms.At(i)
		dps := m.Sum().DataPoints()
		for j := 0; j < dps.Len(); j++ {
			dp := dps.At(j)
			key := m.Name()
			dp.Attributes().Range(func(k string, v pcommon.Value) bool {
				key += "{" + k + "=" + v.AsString() + "}"
				return true
			})
			if dp.ValueType() == pmetric.NumberDataPointValueTypeDouble {
				out[key] = dp.DoubleValue()
			} else {
				out[key] = float64(dp.IntValue())
			}
		}
	}
	return out
}

func byPath(t *testing.T, md pmetric.Metrics) map[string]pmetric.ResourceMetrics {
	t.Helper()
	out := map[string]pmetric.ResourceMetrics{}
	for i := 0; i < md.ResourceMetrics().Len(); i++ {
		rm := md.ResourceMetrics().At(i)
		p, ok := rm.Resource().Attributes().Get("cgroup.path")
		require.True(t, ok)
		out[p.Str()] = rm
	}
	return out
}

func TestScrape(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup scraper is Linux only")
	}
	res := byPath(t, scrape(t, writeTree(t), createDefaultConfig().(*Config)))
	require.ElementsMatch(t, []string{"/", podSlice, ctrScope}, keys(res), "only node, pod and container cgroups are reported")

	ctrAttrs := res[ctrScope].Resource().Attributes().AsRaw()
	assert.Equal(t, "1b2c3d4e-0000-4f00-8a00-123456789abc", ctrAttrs["k8s.pod.uid"])
	assert.Equal(t, containerID, ctrAttrs["container.id"])
	assert.Equal(t, withStatPoints(map[string]float64{
		"container.memory.limit{type=max}":                125829120,
		"container.memory.limit{type=swap.max}":           0,
		"container.memory.pressure.stall_time{type=some}": 0.25,
		"container.memory.pressure.stall_time{type=full}": 0.1,
	}), points(res[ctrScope]), `"max" (unlimited) memory.high is not reported; usage, working_set and rss come from kubeletstats`)

	podAttrs := res[podSlice].Resource().Attributes().AsRaw()
	assert.Equal(t, "1b2c3d4e-0000-4f00-8a00-123456789abc", podAttrs["k8s.pod.uid"])
	assert.NotContains(t, podAttrs, "container.id")
	assert.Equal(t, map[string]float64{
		"k8s.pod.memory.limit{type=max}":                268435456,
		"k8s.pod.memory.pressure.stall_time{type=some}": 0.3,
		"k8s.pod.memory.pressure.stall_time{type=full}": 0.15,
	}, points(res[podSlice]))

	// The real root has no limit files.
	assert.NotContains(t, res["/"].Resource().Attributes().AsRaw(), "k8s.pod.uid")
	assert.Equal(t, map[string]float64{
		"k8s.node.memory.pressure.stall_time{type=some}": 1.5,
		"k8s.node.memory.pressure.stall_time{type=full}": 0.5,
	}, points(res["/"]))
}

func TestCgroupLevel(t *testing.T) {
	cgroupfsPod := "/kubepods/burstable/pod1b2c3d4e-0000-4f00-8a00-123456789abc"
	for cgPath, want := range map[string]level{
		"/":                             levelNode,
		"/kubepods.slice":               levelOther,
		qosSlice:                        levelOther,
		podSlice:                        levelPod,
		ctrScope:                        levelContainer,
		dockerScope:                     levelOther,
		"/system.slice/a.service":       levelOther,
		cgroupfsPod:                     levelPod,
		cgroupfsPod + "/" + containerID: levelContainer,
	} {
		assert.Equal(t, want, cgroupLevel(cgPath), cgPath)
	}
}

func TestScrapeFilters(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup scraper is Linux only")
	}
	cfg := createDefaultConfig().(*Config)
	cfg.Include = MatchConfig{Config: filterset.Config{MatchType: filterset.Regexp}, Paths: []string{`^/kubepods`}}
	cfg.Exclude = MatchConfig{Config: filterset.Config{MatchType: filterset.Regexp}, Paths: []string{`\.scope$`}}

	res := byPath(t, scrape(t, writeTree(t), cfg))
	assert.ElementsMatch(t, []string{podSlice}, keys(res))
}

func TestScrapeExtraCgroups(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup scraper is Linux only")
	}
	const (
		hostLeaf   = "/mem_eviction_lab/otlp-minimal-123-456"
		nestedLeaf = ctrScope + "/mem_eviction_lab/otlp-minimal-789-012"
	)
	root := writeTree(t)
	mount := filepath.Join(root, "sys", "fs", "cgroup")
	for _, cg := range []string{hostLeaf, nestedLeaf} {
		dir := filepath.Join(mount, cg)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for file, content := range map[string]string{
			"memory.current":  "3000\n",
			"memory.stat":     containerStat,
			"memory.events":   containerEvents,
			"memory.max":      "125829120\n",
			"memory.pressure": "some avg10=0.00 avg60=0.00 avg300=0.00 total=2000000\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1000000\n",
		} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644))
		}
	}

	cfg := createDefaultConfig().(*Config)
	// `pod1b2c3d4e` matches the pod and its container: extras never re-report what the kubelet knows about.
	cfg.ExtraCgroups = MatchConfig{Config: filterset.Config{MatchType: filterset.Regexp}, Paths: []string{`/mem_eviction_lab/[^/]+$`, `pod1b2c3d4e`}}
	res := byPath(t, scrape(t, root, cfg))
	require.ElementsMatch(t, []string{"/", podSlice, ctrScope, hostLeaf, nestedLeaf}, keys(res))

	want := withStatPoints(map[string]float64{
		"container.memory.usage":                          3000,
		"container.memory.working_set":                    2500,
		"container.memory.rss":                            1000,
		"container.memory.limit{type=max}":                125829120,
		"container.memory.pressure.stall_time{type=some}": 2,
		"container.memory.pressure.stall_time{type=full}": 1,
	})
	assert.Equal(t, want, points(res[hostLeaf]))
	assert.Equal(t, want, points(res[nestedLeaf]))
	assert.Empty(t, res[hostLeaf].Resource().Attributes().AsRaw()["k8s.pod.uid"])

	// Nested in a pod: the pod UID is kept so k8sattributes can add the pod name, but the
	// leaf isn't the container, so there's no container.id.
	nested := res[nestedLeaf].Resource().Attributes().AsRaw()
	assert.Equal(t, "1b2c3d4e-0000-4f00-8a00-123456789abc", nested["k8s.pod.uid"])
	assert.NotContains(t, nested, "container.id")

	assert.NotContains(t, points(res[ctrScope]), "container.memory.usage", "kubelet containers keep the limit/pressure-only set")
}

func keys(m map[string]pmetric.ResourceMetrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
