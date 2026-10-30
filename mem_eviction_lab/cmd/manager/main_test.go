//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestParseCLIResultsDirRequirement(t *testing.T) {
	// Without -web, --results-dir MUST be required.
	_, err := parseCLI([]string{"-config", "configs/run-otlp.yaml"})
	if err == nil || !strings.Contains(err.Error(), "--results-dir") {
		t.Fatalf("expected --results-dir error when -web is omitted, got: %v", err)
	}

	// Without -web, providing --results-dir succeeds.
	opts, err := parseCLI([]string{"-config", "configs/run-otlp.yaml", "-results-dir", "results/run1"})
	if err != nil {
		t.Fatalf("headless parseCLI unexpected error: %v", err)
	}
	if opts.WebMode || opts.ResultsDir != "results/run1" {
		t.Fatalf("unexpected headless opts: %+v", opts)
	}

	// With -web <port>, --results-dir is optional.
	webOpts, err := parseCLI([]string{"-config", "configs/run-otlp.yaml", "-web", "8080"})
	if err != nil {
		t.Fatalf("web parseCLI unexpected error: %v", err)
	}
	if !webOpts.WebMode || webOpts.WebPort != 8080 || webOpts.ResultsDir != "" {
		t.Fatalf("unexpected web opts: %+v", webOpts)
	}

	// Invalid -web port is rejected.
	if _, err := parseCLI([]string{"-config", "configs/run-otlp.yaml", "-web", "70000"}); err == nil {
		t.Fatal("expected error for out-of-range -web port")
	}
}
