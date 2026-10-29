# Memory Eviction Lab — Implementation Plan

This plan implements [1-refined-spec.md](./1-refined-spec.md) under `mem_eviction_lab/`.

## 1. Directory & Package Layout

```text
mem_eviction_lab/
├── go.mod
├── Makefile
├── cmd/
│   ├── manager/
│   │   └── main.go          # Root lab runner CLI (--config, --duration, --results-dir, --memory-limit, --allow-warm-cache)
│   └── visualizer/
│       └── main.go          # Static HTML dashboard generator CLI (--output, <result_dir...>)
├── configs/
│   ├── collector-hostmetrics-nop.yaml  # Self-driving Collector config (hostmetrics -> nop)
│   ├── collector-minimal-otlp.yaml     # Minimal OTLP pipeline (otlp -> batch -> otlp_grpc via OTEL_LAB_* env vars)
│   ├── run-hostmetrics.yaml            # Sample run YAML without external workload
│   └── run-otlp.yaml                   # Sample run YAML exercising built-in OTLP backend + generator
├── internal/
│   ├── bytefmt/
│   │   ├── bytefmt.go       # Parse/format byte limits ("max", "60MiB", "80MB", raw ints)
│   │   └── bytefmt_test.go
│   ├── config/
│   │   ├── config.go        # YAML loading, relative path resolution, defaults, validation
│   │   └── config_test.go
│   ├── pagecache/
│   │   ├── pagecache_linux.go      # statfs tmpfs/ramfs rejection, fsync + FADV_DONTNEED, mincore verification
│   │   └── pagecache_linux_test.go
│   ├── cgroup/
│   │   ├── cgroup_linux.go         # cgroup v2 creation/teardown, memory.max/swap.max, CLONE_INTO_CGROUP FD, stat/PSI readers
│   │   ├── watcher_linux.go        # epoll (EPOLLPRI|EPOLLERR) watcher on memory.events with clean eventfd shutdown
│   │   └── cgroup_linux_test.go    # Parser unit tests for memory.stat, memory.events, memory.pressure
│   ├── elfmap/
│   │   ├── elfmap.go        # debug/elf section parser & file-offset -> ELF section lookup
│   │   └── elfmap_test.go
│   ├── pagemap/
│   │   ├── maps.go          # /proc/[pid]/maps parser matching target binary (major, minor, inode)
│   │   ├── tracker.go       # Pre-opened /proc/[pid]/pagemap scanner, per-page state machine, section/block aggregators
│   │   └── tracker_test.go  # Synthetic maps + pagemap frame sequences testing cold faults, evictions, 0->1 & PFN refaults
│   ├── procstat/
│   │   ├── status.go        # /proc/[pid]/status reader for VmRSS, RssAnon, RssFile, RssShmem, VmHWM, VmSwap
│   │   └── status_test.go
│   ├── workload/
│   │   ├── backend.go       # Minimal in-process OTLP gRPC discard server (logs, metrics, traces)
│   │   ├── generator.go     # Rate-limited OTLP gRPC traffic generator + TCP readiness waiter
│   │   └── workload_test.go # End-to-end local loopback test (generator -> backend)
│   ├── results/
│   │   ├── types.go         # Strongly typed schemas for SampleRow, SectionRow, BlockRow, RunMeta
│   │   ├── writer.go        # Streaming samples.csv writer + final sections.csv, blocks.csv, run_meta.json + SUDO_UID chown
│   │   ├── reader.go        # Result directory loader for visualizer
│   │   └── results_test.go  # Round-trip write/read tests
│   ├── manager/
│   │   └── runner_linux.go  # Orchestrates cold-flush, cgroup, backend, Collector start, hybrid sampling loop, shutdown
│   └── visualizer/
│       ├── visualizer.go    # Prepares multi-run & per-run view models, sorts sweeps, embeds JSON into template
│       ├── template.go      # Self-contained Apache ECharts HTML/CSS/JS dashboard template
│       └── visualizer_test.go
└── specs/
    ├── 0-initial-spec.md
    ├── 1-refined-spec.md
    └── 2-implementation-plan.md
```

## 2. Implementation Phases

### Phase 1: Module Init, Config, Byte Parsing & Result Schemas
1. Initialize `go.mod` (`module github.com/braydonk/memory-metric-talk/mem_eviction_lab`, Go 1.26) with direct dependencies:
   - `golang.org/x/sys` (Linux `unix.Fadvise`, `Mincore`, `Statfs`, `Epoll*`, `Eventfd`, `CLONE_INTO_CGROUP` support)
   - `golang.org/x/time` (token-bucket pacing for OTLP workload)
   - `go.yaml.in/yaml/v3`
   - `go.opentelemetry.io/collector/pdata` & `google.golang.org/grpc`
2. Implement `internal/bytefmt` supporting `"max"`, `"0"` (for swap), SI (`KB`, `MB`, `GB`) and IEC (`KiB`, `MiB`, `GiB`, `K`, `M`, `G`) units, plus exact integer bytes.
3. Implement `internal/config` with validation (non-empty `name`, local file existence for `collector_binary` and `collector_config`, `--memory-limit` override mechanics, defaults for intervals/timeouts/endpoints).
4. Implement `internal/results` (`writer.go`, `reader.go`, and `SUDO_UID`/`SUDO_GID` recursive ownership restoration).

### Phase 2: ELF Mapping, Cold Page Cache, `procfs` & `pagemap` Engine
1. **`internal/elfmap`**: Open binary with `debug/elf`, validate ELF magic/class, extract allocatable/file-backed sections (`Offset`, `Size`, `Name`) sorted by file offset, and provide binary search lookup `SectionAtFileOffset(off uint64) string`.
2. **`internal/pagecache`**:
   - Check `unix.Statfs` against `TMPFS_MAGIC` (`0x01021994`) and `RAMFS_MAGIC` (`0x858458f6`).
   - Run `fsync` + `unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)`.
   - Verify coldness with read-only `mmap` + `unix.Mincore` + `munmap`, returning exact resident page count prior to and after flush.
3. **`internal/pagemap`**:
   - Parse `/proc/[pid]/maps` to extract VMAs matching target `(devMajor, devMinor, inode)` and classify VMA permissions (`r--p` -> `ro`, `r-xp` -> `rx`, `rw-p` -> `rw`).
   - Abstract `PagemapReader` behind a small interface (`ReadRange(vaddrStart uint64, dst []uint64) error`) so `Tracker` state machine logic (`0->1` cold fault vs `1->0` eviction vs `0->1` refault vs `1->1` `PFN`-change refault, CoW detection via cleared `PM_FILE` bit, per-section and per-block rollup) is 100% unit-testable without requiring root in unit tests.
4. **`internal/procstat`**: Zero-noise parser for `/proc/[pid]/status` extracting `VmRSS`, `RssAnon`, `RssFile`, `RssShmem`, `VmHWM`, and `VmSwap` in bytes.

### Phase 3: Cgroup v2 Controller & `memory.events` `epoll` Watcher
1. **`internal/cgroup/cgroup_linux.go`**:
   - Verify `/sys/fs/cgroup` is cgroup2 (`CGROUP2_SUPER_MAGIC = 0x63677270`).
   - Ensure `/sys/fs/cgroup/mem_eviction_lab` exists and `+memory` is enabled in `cgroup.subtree_control`.
   - Create unique leaf directory, apply `memory.swap.max` and `memory.max`, open directory FD with `O_DIRECTORY|O_CLOEXEC` for `SysProcAttr.CgroupFD`.
   - Pre-open `memory.current`, `memory.stat`, `memory.events`, and `memory.pressure` FDs and read via `unix.Pread(..., 0)` into reusable byte buffers on every sample.
   - Implement resilient `Close()` invoked via `defer` on all exit paths (normal finish, startup/runtime error, OOM kill, `SIGINT`/`SIGTERM`): terminate any remaining processes (`cgroup.kill` with `SIGKILL` fallback), poll until `cgroup.procs` is empty (`cgroup.events` `populated 0`), close all cgroup FDs (`CgroupFD` + metric pseudo-files), `os.Remove` the leaf cgroup directory, and clean up the parent `/sys/fs/cgroup/mem_eviction_lab` directory if empty (`os.Remove`, tolerating `EBUSY` only when another concurrent leaf exists).
2. **`internal/cgroup/watcher_linux.go`**:
   - Perform initial `Pread` at offset `0` on `memory.events`.
   - Register `memory.events` FD on `epoll` with `EPOLLPRI | EPOLLERR` alongside an `eventfd` registered with `EPOLLIN` for deterministic cancellation.
   - On wakeup, `Pread(..., 0)` `memory.events`, parse counters, and push onto a size-1 coalescing channel whenever `high`, `max`, `oom`, or `oom_kill` increases.

### Phase 4: OTLP Workload & Manager Orchestration Loop
1. **`internal/workload`**:
   - `backend.go`: Plaintext gRPC server implementing `LogsService`, `MetricsService`, and `TracesService` (`pdata` `plogotlp`, `pmetricotlp`, `ptraceotlp`) tracking atomic received request/item counters.
   - `generator.go`: TCP readiness poller (`net.DialTimeout`, cancellable on early Collector exit) + per-signal goroutines emitting compact `pdata` batches at configured `rate` and `batch_size`.
2. **`internal/manager/runner_linux.go`** & **`cmd/manager/main.go`**:
   - Pre-flight root/cgroup/binary/ELF checks + SHA-256 checksum + `pagecache.EvictAndVerify`.
   - Create leaf cgroup + start `memory.events` watcher + start mock `backend` (if enabled).
   - Start Collector (`cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: cg.FD()}`, combined stdout/stderr -> `collector.log`, passing `OTEL_LAB_BACKEND_ENDPOINT` and `OTEL_LAB_COLLECTOR_GRPC_ENDPOINT`).
   - Discover binary VMAs from `/proc/[pid]/maps`, initialize `pagemap.Tracker`, record `start` sample immediately.
   - Run concurrent collector `cmd.Wait()` watcher channel so sudden OOM kills or config crashes are handled cleanly at any phase.
   - If `workload.enabled`, wait for Collector readiness (while already running the hybrid sampling loop so startup reclaim/OOM is captured in real time!), then run generator for `--duration`; otherwise run sampling loop for `--duration`.
   - Hybrid sampling loop handles `eventCh` with leading-edge immediate sample + trailing-edge debounce timer (`event_debounce`) alongside `sample_interval` periodic ticker.
   - Stop workload -> record `final` live sample (if Collector still alive) -> write `sections.csv` & `blocks.csv` -> gracefully stop Collector (`SIGTERM` -> `shutdown_timeout` -> `cgroup.kill`) -> collect final `memory.events` OOM counters -> write `run_meta.json` -> `chown` results if under `sudo`.

### Phase 5: Visualizer, Sample Configs, Makefile & Verification
1. **`internal/visualizer`** & **`cmd/visualizer/main.go`**:
   - Load 1..N result directories via `results.ReadDir`.
   - Compute run verdicts (`OOM Killed`, `Thrashing`, `Clean Reclaim`, `No Reclaim Triggered`) and summary metrics.
   - Sort multi-run comparisons from unconstrained (`"max"`) descending by `memory_limit_bytes`.
   - Build self-contained dark/light readable HTML dashboard using Apache ECharts covering:
     - Multi-run comparison summary table, Thrashing Cliff chart, and aligned time-series overlays (when `N > 1`).
     - Per-run selector with KPI cards, Cgroup Memory Stack vs Limit, Binary Page Lifecycle stacked area, Eviction & Refault Rates/Totals (`0->1` vs `PFN` vs `workingset_refault_file`), Segment Breakdown (`ro`/`rx`/`rw`), PSI Stall Rate & Major Faults, ELF Section Horizontal Stacked Bar chart, and Binary File-Offset Spatial Heatmap (`blocks.csv`).
2. **`configs/` & `Makefile`**:
   - Add `collector-hostmetrics-nop.yaml`, `collector-minimal-otlp.yaml`, `run-hostmetrics.yaml`, and `run-otlp.yaml`.
   - Add `Makefile` targets (`build`, `test`, `clean`).
3. **Verification**:
   - Comprehensive unit tests across `bytefmt`, `config`, `pagecache` (using real temp files on disk vs `/tmp` `tmpfs` rejection!), `elfmap` (against compiled Go test binary / `/proc/self/exe`), `pagemap` (state machine transitions including sub-poll PFN changes and CoW pages), `cgroup` parsers, `procstat` (including `/proc/self/status`), `workload`, `results`, and `visualizer` (single-run + multi-run sweep HTML generation).
   - Clean up any temporary verification binaries after testing.
