# Memory Eviction Lab (`mem_eviction_lab`)

`mem_eviction_lab` is a Linux cgroup v2 experiment harness and static HTML visualizer for studying how the Linux kernel reclaims file-backed pages of an OpenTelemetry Collector ELF binary (`.text`, `.rodata`, `.gopclntab`, `.data`) under memory pressure.

Large Collector distributions like `otelcol-contrib` fault tens or hundreds of MiB of binary pages into RAM during startup (`execve` + Go package `init()` across every compiled-in component). This lab isolates a Collector process inside a cold-cached leaf cgroup, sweeps `memory.max` limits, tracks per-page eviction and refault transitions via `/proc/<pid>/pagemap` alongside kernel cgroup v2 metrics (`memory.stat`, `memory.events`, PSI `memory.pressure`), and pinpoints:
1. How much of the resident Collector binary is **one-shot startup bloat** that can be reclaimed with zero steady-state penalty (**clean reclaim**).
2. Where lowering `memory.max` cuts into the active runtime working set, triggering **page thrashing** or **`oom_kill`**.

---

## Prerequisites & Host Requirements

- **Linux with unified cgroup v2** mounted at `/sys/fs/cgroup` with the `memory` controller available (`cat /sys/fs/cgroup/cgroup.controllers`).
- **Go 1.26+** to build the binaries.
- **`sudo` / root privileges for `manager`**: Required to create/remove leaf cgroups, spawn the Collector via `CLONE_INTO_CGROUP`, and read physical page frame numbers (`PFN`) from `/proc/<pid>/pagemap`. (`visualizer` requires no special privileges.)
- **Disk-backed filesystem (`ext4`, `xfs`, `btrfs`, etc.)**: Both `collector_binary` and `collector_config` must live on a real disk-backed filesystem—**not `/tmp` if `/tmp` is `tmpfs`**. `shmem`-backed files ignore `POSIX_FADV_DONTNEED` and cannot be evicted when cgroup swap is disabled (`memory.swap.max=0`). `manager` checks this automatically with `statfs` and fails fast if violated.
- **Exclusive binary inode during the run**: Before starting the Collector, `manager` drops clean page-cache pages for `collector_binary` (`fsync` + `FADV_DONTNEED`) and verifies `0` resident pages via `mincore` so cgroup v2's first-touch page-cache accounting charges every fault to the test cgroup. Stop any other running processes executing the exact same binary file first (or pass `--allow-warm-cache` to bypass verification).

---

## Quick Start

### 1. Build the lab binaries

```bash
make build
```

This builds two CLIs into `./bin/`:
- `./bin/manager`: Runs a single cgroup-isolated Collector experiment and writes CSV/JSON/log artifacts.
- `./bin/visualizer`: Reads one or more result directories and renders a standalone interactive Apache ECharts HTML dashboard.

### 2. Point a run config at your Collector binary

Edit `collector_binary` in [`configs/run-otlp.yaml`](./configs/run-otlp.yaml) (or [`configs/run-hostmetrics.yaml`](./configs/run-hostmetrics.yaml)) to point to a local Linux Collector ELF binary on disk (relative paths resolve relative to the YAML file's directory).

### 3. Run a single experiment

```bash
sudo ./bin/manager \
  --config configs/run-otlp.yaml \
  --memory-limit 120MiB \
  --duration 60s \
  --results-dir results/otlp-120MiB

# Render a single-run dashboard at results/otlp-120MiB/dashboard.html
./bin/visualizer results/otlp-120MiB
```

### 4. Run a multi-limit sweep & comparison dashboard

Use `--memory-limit` to sweep from an unconstrained control (`max`) down past the thrashing/OOM cliff using the same YAML config:

```bash
sudo bash -c '
  for limit in max 200MiB 150MiB 120MiB 80MiB 55MiB; do
    echo "=== Running memory.max=$limit ==="
    ./bin/manager \
      --config configs/run-otlp.yaml \
      --duration 300s \
      --memory-limit "$limit" \
      --results-dir "results/otlp-$limit" || echo "run $limit exited non-zero (continuing sweep)"
  done
'

./bin/visualizer --output comparison_dashboard.html \
  results/otlp-max \
  results/otlp-200MiB \
  results/otlp-150MiB \
  results/otlp-120MiB \
  results/otlp-80MiB \
  results/otlp-55MiB
```

> **Note**: Using `|| echo ...` inside a scripted sweep prevents an early Collector startup timeout (e.g., severe startup page thrashing under an aggressive limit) from aborting the rest of the loop. Even on OOM kills or startup timeouts, `manager` still writes complete CSV/JSON artifacts and cleans up the cgroup before exiting, and changes result directory ownership back to `SUDO_UID:SUDO_GID`.

Open `comparison_dashboard.html` directly in any browser. Multi-run sweeps automatically sort from `Unlimited (max)` down to the tightest memory limit.

---

## Provided Configurations (`configs/`)

`configs/` includes two ready-made experiment pairs illustrating the two ways to drive a Collector in the lab:

### 1. Driven OTLP Pipeline: `run-otlp.yaml` + `collector-minimal-otlp.yaml`
- **How it works**: [`configs/run-otlp.yaml`](./configs/run-otlp.yaml) enables both the lab's in-process **OTLP gRPC discard backend** (`127.0.0.1:14317`) and its rate-limited **OTLP gRPC load generator** targeting `127.0.0.1:4317` (`1000` log records/s, `500` metric data points/s, and `200` trace spans/s). Both run in the `manager` process outside the Collector's cgroup so they consume zero bytes of the Collector's `memory.max` budget.
- **Collector pipeline**: [`configs/collector-minimal-otlp.yaml`](./configs/collector-minimal-otlp.yaml) configures `otlp -> batch -> otlp_grpc` across logs, metrics, and traces, wiring its endpoints via `${env:OTEL_LAB_COLLECTOR_GRPC_ENDPOINT}` and `${env:OTEL_LAB_BACKEND_ENDPOINT}` (injected automatically by `manager`), with internal Collector metrics disabled (`level: none`) to minimize unrelated noise.
- **When to use it**: Best choice when you want deterministic steady-state throughput verification (sent vs. received item counts) **and** exact startup-vs-runtime cold-fault attribution—because `manager` measures `startup_readiness_ms` (when `:4317` accepts connections) before starting `--duration` of OTLP load, letting the visualizer pinpoint what fraction of binary pages faulted during startup vs. steady-state traffic.

### 2. Self-Driving Collector Pipeline: `run-hostmetrics.yaml` + `collector-hostmetrics-nop.yaml`
- **How it works**: [`configs/run-hostmetrics.yaml`](./configs/run-hostmetrics.yaml) leaves both `backend.enabled` and `workload.enabled` unset (`false`), launching the Collector on its own for `--duration`.
- **Collector pipeline**: [`configs/collector-hostmetrics-nop.yaml`](./configs/collector-hostmetrics-nop.yaml) runs a self-contained metrics pipeline (`hostmetrics` scraping `cpu`, `memory`, `load`, and `filesystem` every `1s` -> `batch` -> `nop` exporter).
- **When to use it**: Ideal for testing pull-based receivers (`hostmetrics`, `prometheus`, `filelog`, `kubeletstats`, etc.), idle Collector memory baselines, or custom Collector configs that talk to your own external endpoints without needing OTLP push load from `manager`.

---

## Creating Your Own Experiments

To create a custom experiment, prepare:
1. A standard OpenTelemetry Collector config YAML (any combination of receivers, processors, exporters, connectors, or extensions supported by your binary).
2. A lab run YAML describing the binary path, memory/swap limits, optional environment variables/flags, and optional OTLP harness settings.

### Full Run YAML Reference

```yaml
# Identifier shown in logs, run_meta.json, and dashboard tables/tabs (required).
name: my-experiment

# Local filesystem paths only (required). Relative paths resolve from this YAML file's directory.
collector_binary: /path/to/otelcol-contrib
collector_config: ./my-collector-config.yaml

# Optional extra CLI flags appended after --config=<collector_config>.
collector_args:
  - "--feature-gates=+some.featureGate"

# Cgroup v2 memory.max enforced from t=0 via CLONE_INTO_CGROUP (required unless --memory-limit is passed).
# Accepts "max" (unlimited baseline), IEC units (KiB, MiB, GiB, K, M, G), SI units (KB, MB, GB), or raw bytes.
memory_limit: "150MiB"

# Cgroup v2 memory.swap.max (default: "0"). Keep at "0" to prevent anonymous heap swapping
# so kernel reclaim exclusively targets clean file-backed pages. Accepts "0", "max", or byte strings.
swap_max: "0"

# Periodic heartbeat interval for /proc/<pid>/pagemap + cgroup snapshots (default: 500ms).
sample_interval: 500ms

# Trailing-edge debounce window coalescing epoll(EPOLLPRI) bursts on cgroup memory.events (default: 50ms).
event_debounce: 50ms

# Max wait for collector_grpc_endpoint to accept TCP connections when workload.enabled is true (default: 15s).
startup_timeout: 15s

# Grace period after SIGTERM before writing "1" to cgroup.kill / SIGKILL on teardown (default: 10s).
shutdown_timeout: 10s

# Spatial granularity for blocks.csv and the Binary Spatial Offset Map chart (default: 64 pages = 256 KiB).
block_size_pages: 64

# Optional environment variables passed to the Collector child process.
env:
  GOMEMLIMIT: "90MiB"
  GOGC: "100"

# Optional built-in OTLP gRPC discard receiver (default: disabled).
# When enabled, injects OTEL_LAB_BACKEND_ENDPOINT=<host:port> into the Collector environment.
backend:
  enabled: true
  grpc_endpoint: "127.0.0.1:14317"

# Optional built-in OTLP gRPC traffic generator (default: disabled).
# When enabled, injects OTEL_LAB_COLLECTOR_GRPC_ENDPOINT=<host:port>, waits up to startup_timeout
# for readiness, then drives load for --duration. Specify any non-empty subset of logs/metrics/traces.
workload:
  enabled: true
  collector_grpc_endpoint: "127.0.0.1:4317"
  signals:
    logs:
      rate: 1000       # log records per second (must be > 0)
      batch_size: 100  # items per Export RPC (default: 100)
    metrics:
      rate: 500
      batch_size: 100
    traces:
      rate: 200
      batch_size: 50
```

> Unknown YAML keys are strictly rejected (`KnownFields(true)`) so typos in limit or workload settings never fail silently.

### Tips for Designing Informative Sweeps

1. **Always start with a `--memory-limit max` baseline**:
   Look at the resulting `Peak Cgroup Anon` (`peak_cg_anon_bytes` in `run_meta.json`) and `Ever Faulted Binary` (`ever_faulted_bin_bytes`). With `swap_max: "0"`, anonymous memory (`anon` + `kernel`) is non-reclaimable—it forms a hard floor below which the Collector **must** eventually OOM.
2. **Bracket the Clean Reclaim Zone before the Thrashing Cliff**:
   If `max` uses ~`90 MiB` `anon` and faults ~`195 MiB` of binary (~`285 MiB` total RSS), test intermediate steps such as `220MiB`, `180MiB`, `150MiB`, `120MiB`, `95MiB`, and `80MiB`. Points inside the clean reclaim zone will show tens of MiB of **Cleanly Evicted** binary with **Reclaimed Refault %** near `0%` and **PSI Full Stall** near `0 ms`.
3. **Combine `GOMEMLIMIT` with tight cgroup limits**:
   By default (`GOGC=100` without `GOMEMLIMIT`), Go may let its anonymous heap expand into headroom freed by evicted binary pages. Setting `env: { GOMEMLIMIT: "70MiB" }` keeps the Go GC reactive so `anon` stays compact while the kernel reclaims unused binary pages above it.
4. **Raise `startup_timeout` when probing near the `anon` floor**:
   When `memory.max` sits only a few MiB above startup `anon + kernel` (e.g., `40MiB`), direct reclaim may keep succeeding by violently thrashing `.rodata`/`.text` pages during Go `init()`, slowing startup past the default `15s` readiness timeout before reaching `main()` or OOM. Increase `startup_timeout` (e.g., `60s`) if you want those extreme runs to play all the way out.
5. **Comparing different Collector binaries (Core vs. Contrib vs. custom OCB builds)**:
   You can create separate run YAMLs pointing at different Collector binaries using identical `collector_config` and `workload` settings, then pass all their result directories to `./bin/visualizer` to compare their mapped sizes, startup bloat, and clean reclaim percentages side-by-side.

---

## CLI & Output Artifact Reference

### `./bin/manager` flags

| Flag | Default | Description |
|---|---|---|
| `--config` | *(required)* | Path to run YAML configuration file |
| `--results-dir` | *(required)* | Output directory for run artifacts |
| `--duration` | `60s` | Observation window (starts *after* readiness when `workload.enabled` is true) |
| `--memory-limit` | `""` | Overrides `memory_limit` from the YAML config (e.g. `max`, `120MiB`) |
| `--allow-warm-cache` | `false` | Continue even if binary pages remain cached after `FADV_DONTNEED` |
| `--cgroup-root` | `/sys/fs/cgroup` | Cgroup v2 mount point |

### `./bin/visualizer` usage

```bash
# Single-run dashboard (writes <results_dir>/dashboard.html by default):
./bin/visualizer [--output path.html] <results_dir>

# Multi-run sweep comparison (writes ./comparison_dashboard.html by default):
./bin/visualizer [--output path.html] <results_dir_1> <results_dir_2> [...]
```

### Generated per-run files (`results/<run>/`)

| File | Contents |
|---|---|
| `samples.csv` | Time-series snapshots (500ms heartbeat + 50ms-debounced `memory.events` wakeups) of binary page residency/evictions/refaults (`0->1` and `PFN`), segment breakdowns (`r--p`, `r-xp`, `rw-p`), cgroup v2 `memory.current`/`stat`/`events`/PSI, and `/proc/<pid>/status` RSS. |
| `sections.csv` | End-of-run per-ELF-section rollup (`.text`, `.rodata`, `.gopclntab`, `.noptrdata`, `.data`, ...) showing mapped, never-faulted, final resident, cleanly evicted, and refaulted page counts. |
| `blocks.csv` | Spatial file-offset histogram across fixed-size contiguous blocks of the Collector binary (`block_size_pages`, default 256 KiB). |
| `run_meta.json` | Run metadata (binary size & SHA-256, cold-cache verification counts, limits, exit/OOM status, workload sent/received totals) plus computed headline summary and automated verdict (`no_reclaim`, `clean_reclaim`, `thrashing`, `oom_killed`, or `process_error`). |
| `collector.log` | Combined `stdout`/`stderr` output from the Collector child process. |
