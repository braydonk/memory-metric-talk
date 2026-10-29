# Memory Eviction Lab — Refined Specification

This document refines [0-initial-spec.md](./0-initial-spec.md) with concrete Linux kernel mechanics, CLI/YAML contracts, data schemas, and visualization design agreed upon for `mem_eviction_lab`.

## 1. Goal & Hypothesis

`otelcol-contrib` packages hundreds of receivers, processors, exporters, connectors, and extensions into a single static/PIE Go ELF binary. During process startup (`execve` and Go package `init()` execution), the kernel faults in a large volume of file-backed pages from `.text`, `.rodata`, `.gopclntab`, and `.data`/`.noptrdata`, inflating startup RSS.

Under a normal Collector configuration that exercises only a small subset of components:
1. Lowering the cgroup v2 `memory.max` limit forces the kernel to reclaim clean file-backed pages mapped from the Collector binary.
2. Most reclaimed pages belong to unused components or one-shot `init()` paths and stay evicted for the rest of the process lifetime without triggering refaults or PSI memory stalls (**clean reclaim**).
3. Only when `memory.max` is pushed below `anon` (Go heap/stacks/runtime + CoW `.data`) plus the *active* code/rodata working set does the process suffer continual refaults (**page thrashing**) or `oom_kill`.

## 2. Scope & Architecture

`mem_eviction_lab` is a standalone Go module containing two CLIs sharing internal packages:
- `cmd/manager`: Requires root (`euid == 0`) on Linux with unified cgroup v2 (`/sys/fs/cgroup`). Cold-flushes the Collector binary from the host page cache, creates a dedicated leaf cgroup with configured `memory.max` and `memory.swap.max`, optionally runs an in-process OTLP gRPC discard backend and OTLP gRPC load generator, spawns the Collector directly inside the cgroup (`CLONE_INTO_CGROUP`), monitors per-page binary residency/eviction/refaults via `/proc/[pid]/maps` + `/proc/[pid]/pagemap` alongside cgroup v2 telemetry, and emits CSV + JSON + log outputs.
- `cmd/visualizer`: Unprivileged CLI that reads one or more result directories and renders a standalone, self-contained `dashboard.html` using Apache ECharts for single-run deep dives and multi-run memory-limit sweep comparisons.

## 3. Linux Kernel Isolation & Measurement Mechanics

### 3.1 Cold Page Cache Guarantee (`FADV_DONTNEED` + `mincore` + `statfs`)
- **Local Disk-Backed Binary Only**: `collector_binary` accepts only a local filesystem path (downloading archives is intentionally out of scope). Both `collector_binary` and `collector_config` are checked with `statfs` to reject `TMPFS_MAGIC` and `RAMFS_MAGIC` mounts (such as `/tmp` on `tmpfs`), because `shmem`-backed files sit on the anonymous/swap LRU, ignore `POSIX_FADV_DONTNEED`, and cannot be evicted when swap is disabled.
- **Preventing First-Touch Charge Bypass**: In cgroup v2, page cache folios remain charged to whichever cgroup allocated them first. Before spawning the Collector, `manager` opens the binary and config file, runs `fsync`, calls `unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)`, and verifies via `mmap` + `mincore` + `munmap` that `0` pages of the Collector binary remain resident in the host page cache. If any pages remain resident (e.g., pinned by another running Collector process), `manager` fails fast unless `--allow-warm-cache` is explicitly set.

### 3.2 Cgroup v2 Hierarchy, Swap Isolation, and `CLONE_INTO_CGROUP`
- `manager` manages `/sys/fs/cgroup/mem_eviction_lab/<sanitized-name>-<pid>-<ts>` directly on cgroupfs, ensuring `+memory` is enabled in `/sys/fs/cgroup/mem_eviction_lab/cgroup.subtree_control`.
- Limits are written *before* child spawn:
  - `memory.swap.max` defaults to `"0"` (configurable via `swap_max`), ensuring reclaim targets clean file-backed pages rather than swapping anonymous Go heap pages to host swap devices.
  - `memory.max` is set to the configured byte limit (or `"max"` for an unconstrained control run) from `t = 0`.
- The Collector is spawned with `syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: cgFD}` (`clone3` with `CLONE_INTO_CGROUP`) so every page faulted from `execve` onward is charged to the test cgroup.
- **OOM Survival**: If `memory.max` is too tight and the kernel triggers `oom_kill`, `manager` detects process exit plus `memory.events:oom_kill > 0`, stops `pagemap` polling while preserving the last live page histogram, captures final cgroup counters, and writes complete CSV and `run_meta.json` artifacts recording `oom_killed: true`.
- **Teardown, Cgroup Cleanup & Ownership**: On normal completion, errors, OOM kills, or `SIGINT`/`SIGTERM`, `manager` stops the workload generator, sends `SIGTERM` to the Collector, waits up to `shutdown_timeout`, writes `"1"` to `cgroup.kill` (falling back to `SIGKILL` on remaining PIDs in `cgroup.procs`) if any processes remain, waits until `cgroup.procs` is empty (`cgroup.events` `populated 0`), closes all open cgroup file descriptors, removes the leaf cgroup directory (`rmdir`), and removes the parent `/sys/fs/cgroup/mem_eviction_lab` directory as well (ignoring `EBUSY`/`ENOTEMPTY` only if another concurrent `manager` run is using a sibling leaf). If invoked under `sudo` (`SUDO_UID`/`SUDO_GID` set), it recursively `chown`s `results_dir` and all generated files back to the invoking user.

### 3.3 Binary Page Tracking (`/proc/[pid]/maps` + `/proc/[pid]/pagemap` + `debug/elf`)
- Because an evicted file page receives a newly allocated physical page frame (PFN) when faulted back in from disk, page identity across eviction and refault is keyed by **virtual page address** within the Collector binary's VMAs (mapped back to binary file offsets and ELF sections), not by PFN.
- Right after `cmd.Start()` returns (at which point `execve` has completed and installed ELF `PT_LOAD` mappings), `manager` reads `/proc/[pid]/maps` and selects all VMAs matching the Collector binary's `(dev_major, dev_minor, inode)` (typically `r--p`, `r-xp`, and `rw-p`).
- Using Go's stdlib `debug/elf`, `manager` parses the binary's ELF section headers up front (`.text`, `.rodata`, `.gopclntab`, `.noptrdata`, `.data`, etc.) so every tracked virtual page is tagged with its VMA permission class (`ro`, `rx`, `rw`), binary file offset, and ELF section name.
- On each sample, `manager` issues one contiguous `pread` per binary VMA against a pre-opened `/proc/[pid]/pagemap` file descriptor and decodes each 64-bit entry:
  - Bit 63 (`PM_PRESENT`): page resident in RAM
  - Bit 62 (`PM_SWAP`): page swapped out
  - Bit 61 (`PM_FILE`): clean file-backed page (`1`) vs CoW anonymous page (`0`, e.g., written `.data` pages in `rw-p`)
  - Bits 0–54 (`PFN`): physical frame number when present (visible because `manager` runs with `CAP_SYS_ADMIN`)
- Note: `/proc/kpageflags` is not required, keeping sampling fast (~3 `pread` syscalls, ~0.5 MiB total per scan) and non-intrusive. `/proc/[pid]/clear_refs` is strictly avoided so observation never perturbs PTE Accessed bits or kernel LRU reclaim order.

### 3.4 Per-Page Transition State Machine
For each virtual page `p` across consecutive samples `t-1 -> t`:
- **Initial cold fault**: `!prevPresent && present && !everPresent` -> marks `everPresent = true`, increments `cold_faults`.
- **Eviction**: `prevPresent && !present` -> marks `everEvicted = true`, increments `evictions` and `p.evictCount`.
- **Observed `0 -> 1` refault**: `!prevPresent && present && everPresent` -> increments `refaults_01` and `p.refault01Count`.
- **Sub-sample `PFN`-change refault**: `prevPresent && present && fileBacked && prevFileBacked && pfn != 0 && prevPFN != 0 && pfn != prevPFN` -> detects a page evicted and immediately faulted back into a new physical frame between two samples; marks `everEvicted = true` and increments `evictions`, `refaults_pfn`, `p.evictCount`, and `p.refaultPFNCount`.

Recording `refaults_01` and `refaults_pfn` separately (alongside `refaults_total = refaults_01 + refaults_pfn` and kernel `workingset_refault_file`) preserves complete diagnostic clarity.

### 3.5 Hybrid Sampling Trigger (`epoll` + Debounce + Periodic Ticker)
- `manager` monitors `/sys/fs/cgroup/.../memory.events` using `epoll` registered with `EPOLLPRI | EPOLLERR` (never `EPOLLIN`, which is permanently ready on `kernfs` files), re-reading `memory.events` from offset `0` on each notification to re-arm `kernfs` poll state.
- **Event debounce (`event_debounce`, default `50ms`)**: Reclaim bursts can fire thousands of updates per second during heavy thrashing. If an event arrives within `event_debounce` of the prior event-driven sample, `manager` coalesces the burst and triggers a trailing-edge sample (`trigger = reclaim_debounced`) when the window elapses; otherwise it samples immediately (`trigger = reclaim_event`).
- **Periodic heartbeat (`sample_interval`, default `500ms`)**: Ensures continuous time-series coverage and catches quiet post-reclaim refaults occurring while memory remains below `memory.max` (`trigger = periodic`), bookended by `start` and `final` snapshots.

## 4. Configuration & CLI Interface

### 4.1 `manager` CLI
```bash
sudo ./manager \
  --config configs/run-otlp.yaml \
  --duration 60s \
  --results-dir results/otlp-60mib \
  [--memory-limit 60MiB] \
  [--allow-warm-cache]
```
`--memory-limit` optionally overrides `memory_limit` in the YAML config so users can sweep limits (`max`, `120MiB`, `80MiB`, `60MiB`, `45MiB`, ...) from a simple shell loop using a single YAML definition.

### 4.2 Run YAML Schema
Each YAML file describes a single run:
```yaml
name: otlp-minimal
collector_binary: ./otelcol-contrib
collector_config: ./configs/collector-minimal-otlp.yaml
collector_args: []

# Limit written to memory.max. Accepts "max" (unconstrained baseline) or byte strings (e.g. "60MiB", "80MB").
memory_limit: "60MiB"
# Limit written to memory.swap.max. Defaults to "0" (disable swap). Accepts "0", "max", or byte strings.
swap_max: "0"

sample_interval: 500ms
event_debounce: 50ms
startup_timeout: 15s
shutdown_timeout: 10s
block_size_pages: 64 # 256 KiB spatial blocks for blocks.csv

env: {}
  # GOMEMLIMIT: "45MiB"

# Optional in-process OTLP gRPC discard receiver (sets OTEL_LAB_BACKEND_ENDPOINT).
backend:
  enabled: true
  grpc_endpoint: "127.0.0.1:14317"

# Optional in-process OTLP gRPC load generator (sets OTEL_LAB_COLLECTOR_GRPC_ENDPOINT).
workload:
  enabled: true
  collector_grpc_endpoint: "127.0.0.1:4317"
  signals:
    logs:
      rate: 1000
      batch_size: 100
    metrics:
      rate: 500
      batch_size: 100
    traces:
      rate: 200
      batch_size: 50
```

When `workload.enabled` is `false`, `manager` simply launches the Collector and observes it for `--duration` (ideal for self-driving configs such as `hostmetrics -> nop`). When `workload.enabled` is `true`, `manager` starts `backend` (if enabled), starts the Collector, waits up to `startup_timeout` for TCP readiness on `collector_grpc_endpoint` (aborting immediately if the Collector exits/OOMs during startup), and then runs the configured signal generators for `--duration`.

### 4.3 `visualizer` CLI
```bash
./visualizer --output dashboard.html results/otlp-max results/otlp-80mib results/otlp-55mib results/otlp-40mib
```

## 5. Result Directory Artifacts

Each run writes five files into `--results-dir`:

1. **`samples.csv`** — Time-series samples (1 row per snapshot):
   - Timing: `timestamp_unix_ms`, `elapsed_ms`, `trigger` (`start`, `reclaim_event`, `reclaim_debounced`, `periodic`, `final`)
   - Binary page state (pages): `bin_total_pages`, `bin_present_file_pages`, `bin_present_cow_pages`, `bin_swapped_pages`, `bin_never_faulted_pages`, `bin_evicted_out_pages`, `bin_cleanly_evicted_pages`, `bin_ever_faulted_pages`, `bin_ever_evicted_pages`, `bin_ever_refaulted_pages`, `bin_thrashing_pages`
   - Transitions (pages): `delta_cold_faults`, `delta_evictions`, `delta_refaults_01`, `delta_refaults_pfn`, `delta_refaults_total`, `cum_cold_faults`, `cum_evictions`, `cum_refaults_01`, `cum_refaults_pfn`, `cum_refaults_total`
   - Per-segment breakdown (`ro` = `r--p`, `rx` = `r-xp`, `rw` = `rw-p`): resident, evicted-out, CoW (`rw`), and cumulative refault page counts
   - Cgroup v2 stats: `cg_memory_current_bytes`, `cg_anon_bytes`, `cg_file_bytes`, `cg_active_file_bytes`, `cg_inactive_file_bytes`, `cg_kernel_bytes`, `cg_pgfault`, `cg_pgmajfault`, `cg_workingset_refault_file`, `cg_workingset_activate_file`, `cg_events_low`, `cg_events_high`, `cg_events_max`, `cg_events_oom`, `cg_events_oom_kill`, `cg_psi_some_total_us`, `cg_psi_full_total_us`
   - Process `/proc/[pid]/status` stats: `proc_rss_bytes`, `proc_rss_anon_bytes`, `proc_rss_file_bytes`, `proc_rss_shmem_bytes`, `proc_hwm_bytes`, `proc_swap_bytes`
2. **`sections.csv`** — End-of-run binary summary grouped by mapped ELF section (`.text`, `.rodata`, `.gopclntab`, `.noptrdata`, `.data`, etc.): section offset/size, VMA permissions, total pages, never-faulted, final resident (file vs CoW), cleanly evicted (faulted once, evicted, 0 refaults), refaulted once, thrashing (`>= 2` refaults), and total eviction/refault counts.
3. **`blocks.csv`** — Contiguous spatial buckets across mapped binary file offsets (`block_size_pages`, default 256 KiB per block): file offset range, dominant ELF section, VMA perms, and per-fate page counts + refault totals for spatial heatmap visualization.
4. **`run_meta.json`** — Run config metadata, resolved binary SHA-256 and byte size, page size, `memory_limit_bytes`, startup readiness latency, workload item/error totals, process exit code/signal, `oom_killed`, and headline summary metrics.
5. **`collector.log`** — Combined Collector stdout/stderr.

## 6. Visualizer Dashboard Design

`dashboard.html` is a standalone HTML page embedding parsed JSON run data and Apache ECharts:
- **Multi-Run Comparison View** (shown when multiple result dirs are passed, sorted descending from `"max"` down to tightest limit):
  - **Eviction vs Thrashing Cliff Chart**: Memory limit on X-axis vs Cleanly Reclaimed Binary MiB, Final Binary Resident MiB, Cumulative Refaults, and Total PSI Memory Stall (ms), highlighting OOM-killed runs.
  - Side-by-side summary table comparing Peak -> Final Binary Resident MiB, Cleanly Reclaimed %, Refaults (`0->1` / `PFN` / `workingset_refault_file`), `pgmajfault`, PSI stall time, and outcome badge.
  - Synchronized multi-run overlay charts for Cgroup Memory Usage, Binary Resident Pages, Cumulative Refaults, and PSI Stall Rate.
- **Single-Run Deep Dive Selector / Tabs**:
  - **KPI Cards**: Memory limit, outcome badge (`Clean Reclaim` / `Thrashing` / `OOM Killed` / `No Reclaim`), Peak vs Final Binary Resident MiB, Cleanly Reclaimed MiB & % of faulted binary pages, Cumulative Binary Refaults, Kernel `workingset_refault_file`, Total PSI Stall ms.
  - **Cgroup Memory Composition vs Limit**: Stacked `anon` + `file` + `kernel` vs `memory.current`, `VmRSS`, and `memory.max`.
  - **Collector Binary Page Lifecycle**: Stacked area over time of `Present (Clean File)`, `Present (CoW)`, `Cleanly Evicted`, `Evicted (Previously Refaulted)`, and `Never Faulted`.
  - **Eviction & Thrashing Activity**: Interval rates and cumulative totals for `Cold Faults`, `Evictions`, `Refaults (0->1)`, `Refaults (PFN)`, `workingset_refault_file`, and `memory.events:max`.
  - **Segment Residency & Refaults**: `.text` (`r-xp`) vs `.rodata` (`r--p`) vs `.data` (`rw-p`).
  - **Memory Pressure (PSI) & Major Faults**: PSI `some`/`full` % stall rate and `pgmajfault/s`.
  - **ELF Section Breakdown**: Stacked horizontal bar chart from `sections.csv`.
  - **Binary Spatial Residency & Thrash Map**: Block-level heatmap across binary file offsets from `blocks.csv` showing which contiguous regions of the Collector binary stayed cold, evicted cleanly, stayed hot in RAM, or thrashed.
