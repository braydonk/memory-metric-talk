# reclaim_demo

Runs several long-lived OpenTelemetry Collectors with the same configuration
(OTLP/HTTP in, `nop` out, same synthetic load), each in its own cgroup v2 leaf
`/sys/fs/cgroup/reclaim_demo/<name>` with its own `memory.max` and proactive
reclaim policy. Linux only, needs root.

The point: show on a dashboard how much of a collector's memory is reclaimable,
and what happens when you reclaim too little, just enough, or too much.

| Policy | What it does |
|---|---|
| `none` | Nothing. Usage stays at "binary + startup cruft" unless the cgroup hits `memory.max`. |
| `psi` | Senpai-style (Meta TMO): every `interval`, write `memory.current * ratio * max(0, 1 - pressure/pressure_target)` bytes to `memory.reclaim`, where `pressure` is the share of the interval tasks were stalled on memory (`memory.pressure` `some`). Reclaims until the workload starts to stall. |
| `fixed` | Writes `step` bytes to `memory.reclaim` every `interval`, ignoring pressure. Shows refaults/thrashing. |

`memory.swap.max` is set to `0`, so only file pages (the binary, page cache) can
be reclaimed.

Each instance runs its own copy of the collector binary, written and evicted
from the page cache before start. Otherwise the instances share the binary's
page cache: the first one to fault a page pays for it, and reclaim in one
cgroup evicts pages the others use.

## Run

On a host (needs root, cgroup v2):

```sh
go build -o reclaim_demo .
sudo ./reclaim_demo --config demo.yaml   # edit collector_binary first
```

In kind (the OTel `cgroup` scraper picks the instances up as extra cgroups):

```sh
tilt up
```

## Config

See [`demo.yaml`](./demo.yaml): `control` (none), `soft-reclaim` (psi), `aggressive-reclaim`
(fixed) with the same generous limit, `limited` with a tight limit and no
proactive reclaim (the kernel reclaims on its own at the limit: thrashing, no
OOM), and `oom` with a heavier per-instance load whose heap outgrows the limit
(real cgroup OOM kills, restarted every ~30s).

Manager logs show one line per controller tick:

```
[otelcol-soft-reclaim] current=251.0MiB pressure=0.000% reclaim=2.5MiB
```

Collector logs are in `<work_dir>/<name>/collector.log`.

## Metrics to watch

From `memory.stat` of each instance (and the OTel cgroup scraper):

- usage (`memory.current`), working set (`usage - inactive_file`), rss (`anon`)
- `memory.pressure`: stall time, what the `psi` policy reacts to
- `pgsteal_proactive`: pages reclaimed through `memory.reclaim`
- `workingset_refault_file`: reclaimed pages that were needed again, the
  "too aggressive" signal

Throughput comes from the collectors themselves. If `telemetry_endpoint` is set,
each one pushes its internal telemetry (`otelcol_receiver_accepted_metric_points`,
heap, ...) over OTLP to it, with `k8s.container.name` set to the instance name.
It's off by default.
