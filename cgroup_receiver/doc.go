// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate make mdatagen

// Package cgroupreceiver reads cgroup v2 memory interface files (memory.max,
// memory.high, memory.swap.max, memory.pressure) of the node root, pod and container
// cgroups, and reports them as k8s.node.memory.*, k8s.pod.memory.* and container.memory.*. Linux only.
package cgroupreceiver // import "github.com/braydonk/memory-metric-talk/cgroup_receiver"
