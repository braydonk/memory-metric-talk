// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cgroupreceiver // import "github.com/braydonk/memory-metric-talk/cgroup_receiver"

import (
	"context"
	"errors"
	"runtime"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/scraper"
	"go.opentelemetry.io/collector/scraper/scraperhelper"

	"github.com/braydonk/memory-metric-talk/cgroup_receiver/internal/metadata"
)

// NewFactory for the cgroup receiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(metadata.Type, createDefaultConfig, receiver.WithMetrics(createMetricsReceiver, metadata.MetricsStability))
}

// createDefaultConfig creates the default configuration for the receiver.
func createDefaultConfig() component.Config {
	return &Config{
		ControllerConfig:     scraperhelper.NewDefaultControllerConfig(),
		MetricsBuilderConfig: metadata.NewDefaultMetricsBuilderConfig(),
	}
}

// createMetricsReceiver creates a receiver that runs the cgroup scraper every collection_interval.
func createMetricsReceiver(
	_ context.Context,
	settings receiver.Settings,
	cfg component.Config,
	next consumer.Metrics,
) (receiver.Metrics, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("cgroup receiver is only supported on Linux")
	}
	rCfg := cfg.(*Config)
	s, err := newCgroupScraper(settings, rCfg)
	if err != nil {
		return nil, err
	}
	sc, err := scraper.NewMetrics(s.scrape, scraper.WithStart(s.start))
	if err != nil {
		return nil, err
	}
	return scraperhelper.NewMetricsController(&rCfg.ControllerConfig, settings, next, scraperhelper.AddMetricsScraper(metadata.Type, sc))
}
