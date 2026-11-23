# cgroup receiver

OpenTelemetry Collector receiver that reads cgroup v2 memory files
(`memory.max`, `memory.high`, `memory.swap.max`, `memory.pressure`,
`memory.stat`, `memory.events`) of the node root, pod and container cgroups,
and reports them as `k8s.node.memory.*`, `k8s.pod.memory.*` and
`container.memory.*`. Linux only. It feeds the
[`reclaim_demo` dashboard](../reclaim_demo/dashboard.json).

It's the `cgroup` scraper of the hostmetrics receiver from the
`process_memory_metrics` branch of `rogercoll/opentelemetry-collector-contrib`,
packaged as a standalone receiver so it can be built into any collector. The
metrics are listed in [`documentation.md`](./documentation.md).

## Build

Add it to an [OCB](https://opentelemetry.io/docs/collector/custom-collector/)
manifest:

```yaml
receivers:
  - gomod: github.com/braydonk/memory-metric-talk/cgroup_receiver v0.0.0
replaces:
  - github.com/braydonk/memory-metric-talk/cgroup_receiver => /path/to/memory-metric-talk/cgroup_receiver
```

## Config

```yaml
receivers:
  cgroup:
    collection_interval: 1s
    # Cgroups the kubelet doesn't know about, reported as containers with the
    # full container.memory.* set (here: the reclaim_demo instances).
    extra_cgroups:
      match_type: regexp
      paths: ['/reclaim_demo/[^/]+$']
    # Optional filters on the cgroup path (`/` is the root cgroup).
    # include: {match_type: regexp, paths: ['^/kubepods']}
    # exclude: {match_type: regexp, paths: ['\.scope$']}
```

In a container, mount the host's `/sys` and set `HOST_SYS` to it, e.g.
`HOST_SYS=/hostfs/sys`.

## Develop

`internal/metadata` and `documentation.md` are generated from
[`metadata.yaml`](./metadata.yaml) with `mdatagen` v0.164.0.
mdatagen names the generated test package after the directory (`cgroup_receiver`); rename it to `cgroupreceiver` after regenerating.
`internal/filterset` is a copy of contrib's `internal/filter/filterset`, which
can't be imported from outside contrib.

```sh
go test ./...
```
