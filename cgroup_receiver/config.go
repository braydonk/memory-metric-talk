// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cgroupreceiver // import "github.com/braydonk/memory-metric-talk/cgroup_receiver"

import (
	"go.opentelemetry.io/collector/scraper/scraperhelper"

	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/filterset"
	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/metadata"
)

// Config relating to the cgroup receiver.
type Config struct {
	// ControllerConfig sets collection_interval, initial_delay and timeout.
	scraperhelper.ControllerConfig `mapstructure:",squash"`
	// MetricsBuilderConfig allows to customize scraped metrics/attributes representation.
	MetricsBuilderConfig metadata.MetricsBuilderConfig `mapstructure:",squash"`
	// Include specifies a filter on the cgroup paths (e.g. `/kubepods.slice/...`, `/` for the
	// root cgroup) to scrape. Exclude specifies a filter on the cgroup paths to skip.
	// If neither is set, every cgroup with the memory controller enabled is scraped, which
	// can be hundreds on a systemd host.
	Include MatchConfig `mapstructure:"include"`
	Exclude MatchConfig `mapstructure:"exclude"`
	// ExtraCgroups specifies a filter on the paths of cgroups the kubelet doesn't know about
	// (e.g. cgroups created by a benchmark harness). They are reported as containers with the
	// full set of container.memory.* metrics, since the kubeletstats receiver can't report them.
	ExtraCgroups MatchConfig `mapstructure:"extra_cgroups"`
}

type MatchConfig struct {
	Config filterset.Config `mapstructure:",squash"`

	Paths []string `mapstructure:"paths"`
}
