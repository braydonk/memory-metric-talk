# Interactive Web Interface Spec (`-web`)

This document specifies the interactive localhost web mode for `cmd/manager`, enabling live benchmark control, mid-run cgroup memory pressure manipulation, and real-time 2D visualization of Linux page faults, page cache reclaim, Copy-on-Write splits, and refault thrashing across the Collector ELF binary.

---

## 1. CLI & Process Lifecycle

### 1.1 Flags & Startup Validation ([`cmd/manager/main.go`](../cmd/manager/main.go))
- Add flag `-web <port>` (`flag.IntVar(&opts.WebPort, "web", 0, ...)`), accepting TCP port `1..65535` (supports both `-web 8080` and `--web 8080`).
- **Headless Mode (`-web` omitted / `0`)**: Both `--config` and `--results-dir` are **strictly required** (`cmd/manager` exits with code `2` and `manager.Run` rejects empty `ResultsDir`), because without `-web` disk artifacts are the only way for the user to inspect run output. Runs a single benchmark and exits.
- **Web Mode (`-web > 0`)**:
  - `--config` remains **required** and supplies the base YAML configuration (Collector binary, Collector config, workload settings, and initial UI defaults for `memory_limit`, `swap_max`, and `duration`).
  - `--results-dir` becomes **optional** *(only in `-web` mode)*:
    - **Omitted**: Purely in-memory session (permitted solely because the live browser UI displays the telemetry). No CSV/JSON artifacts are persisted; Collector stdout/stderr is captured in an in-memory tail buffer (last `64 KiB`) for UI diagnostics.
    - **Provided**: Each web-triggered run writes full standard artifacts (`samples.csv`, `sections.csv`, `blocks.csv`, `run_meta.json`, `collector.log`) into a unique subdirectory `<results-dir>/<name>-<limit>-<YYYYMMDD-HHMMSS>/`, chowned back to `SUDO_UID:SUDO_GID`.
  - **Fail-Fast Startup Checks**: Before binding the HTTP socket, `manager` validates `os.Geteuid() == 0`, unified cgroup v2 availability at `--cgroup-root`, base config parsing (`config.Load`), non-`tmpfs` binary/config filesystems, and ELF section parsing (`elfmap.Parse`).
  - **Strict Loopback Binding**: Because `manager` runs as `root`, the HTTP server binds exclusively to `127.0.0.1:<port>` (never `0.0.0.0` or `:port`).
  - **Graceful Shutdown**: On `SIGINT`/`SIGTERM`, cancels any active benchmark run (cleaning up child Collector + leaf/parent cgroups) and shuts down the HTTP server cleanly.

### 1.2 Single-Flight Run Coordinator (`internal/web`)
- Only **one** benchmark may run at a time (protected by a coordinator state machine: `idle` → `starting` → `running` → `stopping` → `finished`).
- Back-to-back runs are supported within the same `-web` process without restarting `manager`. Starting a new run performs a fresh `pagecache.EvictAndVerify` cold-cache flush of the Collector binary and creates a fresh leaf cgroup.

---

## 2. Kernel Cgroup & Tracker Extensions

### 2.1 Live Mid-Run Cgroup Operations ([`internal/cgroup/cgroup_linux.go`](../internal/cgroup/cgroup_linux.go))
Extend `Cgroup` with two thread-safe operations acting on the active leaf cgroup via `writeExistingFile` (`O_WRONLY` without `O_CREATE`):

1. **`SetMemoryMax(limit bytefmt.Limit) error`**:
   - Writes `limit.String() + "\n"` to `<leaf>/memory.max`.
   - Kernel note: lowering `memory.max` below `memory.current` triggers synchronous kernel direct reclaim during `write()` and can trigger a cgroup OOM kill if unreclaimable `anon + kernel` exceeds the new target.
2. **`Reclaim(bytes uint64) (reclaimed uint64, partial bool, err error)`**:
   - Reads pre-reclaim `memory.current`, writes `<bytes>\n` to `<leaf>/memory.reclaim` (Linux 5.19+), and reads post-reclaim `memory.current`.
   - Kernel note: Linux returns `EAGAIN` from `memory.reclaim` whenever it sweeps the LRUs successfully but reclaims fewer bytes than requested (hitting the active/unreclaimable floor). Treat `EAGAIN` as `partial = true, err = nil` and report actual `reclaimed` bytes to the UI; if `memory.reclaim` does not exist (`os.ErrNotExist`, kernel `< 5.19`), return a clear unsupported error.

### 2.2 Runner Hook & Web Sampling Cadence ([`internal/manager/runner_linux.go`](../internal/manager/runner_linux.go))
- Refactor `manager.Run` to support optional execution callbacks and an active run handle without affecting CLI behavior:
  - **Web Steady-State Sampling Cadence**: When running in `-web` mode and the config uses default sampling (`500ms`), tighten `sample_interval` to **`100ms` (10 Hz)** while retaining the **`50ms` (20 Hz)** `memory.events` `epoll` debounce. This ensures the ~3.5s Collector startup window yields ~35+ smooth visual frames even at `memory.max = max` (scanning 102,127 pagemap entries via 3 `pread` syscalls takes `< 1 ms`).
  - **Lifecycle & Sample Callbacks**:
    - `OnLayout(meta RunInitMeta)`: fired right after Collector process starts and `/proc/<pid>/maps` + ELF sections are mapped, delivering the exact binary page layout (`pages[] -> section, file_offset, vma_perms`).
    - `OnReady(elapsedMs int64)`: fired when the Collector OTLP receiver becomes reachable (`startup_readiness_ms`).
    - `OnSample(sample LiveSample)`: fired on each hybrid sample tick with the `results.SampleRow` plus sparse per-page transition deltas.
    - `OnDone(meta results.RunMeta)`: fired when the run terminates (`pass`, `oom_killed`, `process_error`, or user-initiated `stopped`).
  - Support a user-triggered **Stop** cancellation context distinct from parent process shutdown so stopping a run from the UI performs graceful `SIGTERM` → shutdown drain → cgroup teardown and lands cleanly in `finished` (`stopped`) state without exiting `manager`.

### 2.3 Compact Per-Page Delta Encoding ([`internal/pagemap/tracker.go`](../internal/pagemap/tracker.go))
To visualize all ~102,127 individual 4 KiB pages live at 10–20 Hz and retain full run history in browser RAM for scrubbing:
- Expose page descriptor metadata once at run start (`Tracker.PageDescriptors()`), compacted into contiguous section spans `{section, vma_perms, file_offset_bytes, start_page, page_count}` plus `block_size_pages` and `page_size_bytes`.
- During `Tracker.Sample`, optionally append a packed **`uint32`** delta entry for each page whose visual state or transition fired on that tick:
  - **Bits `31..4` (28 bits)**: `pageIndex` (supports binaries up to $2^{28}$ pages = 1 TiB).
  - **Bits `3..0` (4 bits)**: `PageVisualCode`:

| Code | Name | Trigger on Tick $f$ | Flash Animation (Tick $f$) | Resting Visual State (Ticks $> f$) |
| :--- | :--- | :--- | :--- | :--- |
| `0` | `NeverFaulted` | Initial state at `t = 0` | — | Never faulted (cold charcoal) |
| `1` | `ColdFaultFile` | `0 → 1` first fault, `PM_FILE=1` | Bright ice-cyan cold-fault flash | Resident clean file-backed |
| `2` | `ColdFaultCoW` | `0 → 1` first fault, `PM_FILE=0` | Bright warm-gold CoW flash | Resident anonymous CoW |
| `3` | `PromotedCoW` | `1 → 1` clean file page dirtied (`PM_FILE: 1→0`) | Warm-gold CoW flash | Resident anonymous CoW |
| `4` | `EvictedClean` | `1 → 0`, cumulative page `refaults == 0` | Bright mint/seafoam pageout flash | Evicted cleanly (`0` refaults) |
| `5` | `EvictedRefaulted` | `1 → 0`, cumulative page `refaults == 1` | Muted amber pageout flash | Evicted after 1 prior refault |
| `6` | `EvictedThrashing` | `1 → 0`, cumulative page `refaults >= 2` | Coral pageout flash | Evicted (thrashing page, $\ge 2$ refaults) |
| `7` | `RefaultFileOnce` | `0 → 1` (after evict) or `1 → 1` PFN change, `refaults == 1`, file | Bright tangerine refault flash | Resident file-backed (refaulted $1\times$) |
| `8` | `RefaultFileThrash` | `0 → 1` or PFN change, `refaults >= 2`, file | Hot vermilion/red thrash flash | Resident file-backed (thrashing $\ge 2\times$) |
| `9` | `RefaultCoW` | `0 → 1` after eviction, `PM_FILE=0` | Bright tangerine refault flash | Resident CoW (refaulted) |
| `10` | `SwappedOut` | `1 → 0` with `PM_SWAP=1` | Slate swap flash | Swapped out |

- **Wire & Memory Footprint**:
  - Steady-state ticks typically touch `0–50` pages (`0–200 B` raw per tick).
  - A full 35,000-page startup burst totals ~140 KB across all startup frames combined; an extreme 5-minute thrashing run (`700k` transitions) totals ~2.8 MB of deltas across 3,000 frames.

---

## 3. HTTP & Real-Time Streaming API (`internal/web`)

All frontend assets (Vanilla HTML, CSS, Canvas JS) are compiled directly into `bin/manager` via `embed.FS`—zero Node/npm build step or external CDN requirement for core visualization.

| Method & Path | Purpose |
| :--- | :--- |
| `GET /` | Serves the single-page web application (`index.html`, embedded CSS/JS). |
| `GET /api/state` | Returns static config metadata, ELF section pre-scan, initial defaults, current run status, and cached frames of the current/most recent run (allowing browser refresh mid-run without losing timeline history). |
| `GET /api/events` | Server-Sent Events (`text/event-stream`) stream broadcasting `run_layout`, `sample` (with `SampleRow` stats + base64-encoded little-endian `uint32` `deltas_b64`), `ready`, `limit_change`, `reclaim_result`, and `run_done` events. |
| `POST /api/run/start` | JSON `{ "memory_limit": "80MiB", "swap_max": "0", "duration": "60s" }`. Validates inputs and launches a new benchmark if idle/finished (`409 Conflict` if already running). |
| `POST /api/run/stop` | Gracefully stops the currently active benchmark run. |
| `POST /api/run/memory-max` | JSON `{ "memory_limit": "55MiB" }`. Updates live `<leaf>/memory.max` mid-run and broadcasts a `limit_change` marker event. |
| `POST /api/run/reclaim` | JSON `{ "amount": "32MiB" }` (or `"all_file"` to target current `memory.stat.file`). Writes to `<leaf>/memory.reclaim` mid-run and returns/broadcasts bytes reclaimed. |

- Basic CSRF safety on root localhost server: mutating `POST /api/*` routes require `Content-Type: application/json` and reject cross-origin `Origin` headers not matching `http://127.0.0.1:<port>` or `http://localhost:<port>`.

---

## 4. Frontend Design & Visualization Architecture

### 4.1 Aesthetic Direction
- Precision Linux systems-observatory aesthetic using Vanilla CSS custom properties (no purple/violet accents, no glowing neon card borders, no gradient headlines, no grid wallpaper).
- Deep obsidian/slate surface hierarchy (`hsl(216, 20%, 8%)` base, `hsl(215, 18%, 12%)` elevated panels, crisp hairline borders) with strict tabular-numeral (`font-variant-numeric: tabular-nums`) monospace metrics and curated high-contrast semantic page state colors:
  - **Never Faulted**: Deep graphite (`hsl(215, 18%, 15%)`)
  - **Resident File Cache** (clean binary): Cerulean blue (`hsl(204, 78%, 52%)`) + bright ice-cyan cold-fault flash (`hsl(190, 95%, 75%)`)
  - **Resident CoW** (dirty `.data`/`.noptrdata`): Warm amber-gold (`hsl(42, 92%, 58%)`)
  - **Cleanly Evicted** (`0` refaults — safely shed startup bloat): Muted jade (`hsl(162, 42%, 25%)`) + bright seafoam eviction flash (`hsl(156, 85%, 65%)`)
  - **Refaulted ($1\times$)**: Tangerine (`hsl(26, 88%, 56%)` resident / `hsl(26, 45%, 24%)` evicted)
  - **Thrashing ($\ge 2\times$ refaults)**: Vermilion red (`hsl(8, 76%, 50%)` resident / `hsl(8, 45%, 23%)` evicted / `hsl(8, 92%, 66%)` active refault flash)

### 4.2 Layout Hierarchy (Top to Bottom)

1. **Header & Benchmark Control Bar**:
   - Config badge (`name`, Collector binary basename & mapped size, workload summary, save-dir indicator if `--results-dir` is set).
   - Pre-run / Next-run inputs: **`memory.max`** (text input + quick preset chips: `max`, `160MiB`, `120MiB`, `80MiB`, `55MiB`, `40MiB`), **`Duration`** (`30s`, `60s`, `120s`, `300s`, or custom), **`swap.max`**, and primary **Start Benchmark** / **Stop** action button with live run state & verdict badge (`IDLE`, `STARTING`, `RUNNING`, `STOPPED`, `PASS`, `OOM_KILLED`, `PROCESS_ERROR`).

2. **Live Kernel Pressure Controls Strip** *(enabled while Collector cgroup is active)*:
   - **Live `memory.max` Adjuster**: Input + quick step/preset buttons (`-16 MiB`, `-8 MiB`, `+16 MiB`, `max`, `Apply`) with a real-time **OOM Risk Indicator** comparing the typed limit against live irreclaimable `Anon + Kernel` bytes:
     - Safe headroom (`limit > anon + kernel + 12 MiB`): neutral/green indicator showing available file-cache budget.
     - Tight squeeze (`anon + kernel < limit <= anon + kernel + 12 MiB`): amber warning (*"Tight binary cache headroom — expect heavy refaults"*).
     - Below `anon + kernel`: red danger warning (*"Below current Anon+Kernel (X MiB) — applying will likely trigger immediate OOM kill"*).
   - **One-Shot Proactive Reclaim (`memory.reclaim`)**: Buttons for **`Reclaim 16 MiB`**, **`Reclaim 64 MiB`**, and **`Reclaim All File Cache`**, displaying an inline status toast with actual bytes reclaimed (e.g., *"Kernel reclaimed 41.2 MiB via memory.reclaim"*).

3. **Cgroup Memory Arena Bar & Live Telemetry Strip**:
   - **Proportional Arena Capacity Bar**: Single crisp horizontal stacked gauge scaled to `max(memory.max, peak memory.current, mapped_binary_bytes_ref)`:
     - Segment 1: **Anon / Go Heap & Stack** (`memory.stat.anon`) — solid slate-amber
     - Segment 2: **Kernel / Slab & Page Tables** (`memory.stat.kernel`) — muted steel
     - Segment 3: **File Page Cache / Binary** (`memory.stat.file`) — cerulean blue
     - Overlaid vertical **`memory.max` hard wall line**: makes the mechanics immediately visual—as Go `anon + kernel` grows rightward toward the `memory.max` wall, the blue `file` segment gets physically squeezed down, coinciding directly with green eviction flashes and orange/red refault flashes in the page matrix below.
   - **Compact Live KPI Readouts**:
     - `Cgroup Current / Limit` (`MiB`)
     - `Anon + Kernel` vs `File Cache` (`MiB`)
     - `Binary Resident` (`File + CoW MiB`, `% of mapped`)
     - `Cleanly Evicted` (`MiB`, `% of reclaimed staying clean`)
     - `Deltas / Tick` (`+Cold`, `-Evict`, `!Refault`) & cumulative `Refaults`
     - `PSI Memory Stall` (`some` / `full` ms + `memory.events.max` count)

4. **2D ELF Binary Page Canvas (Core Visualization)**:
   - **View Mode Toggle & Legend**:
     - Resolution switch: **`4 KiB Pages (1:1)`** *(default)* vs **`256 KiB Blocks`**.
     - Interactive color legend showing live page counts next to every state swatch.
   - **Adaptive ELF Section Bands (`4 KiB Pages` mode)**:
     - Pages are grouped in file-offset order into labeled horizontal section bands showing section name, VMA permissions (`r-xp`, `r--p`, `rw-p`), mapped size, and live resident/evicted stats.
     - **Adaptive cell scale**:
       - Large sections ($\ge 4,096$ pages, e.g., `.text` `41k`, `.rodata` `25k`, `.gopclntab` `34k`): crisp dense pixel cells (`2×2` or `3×3` CSS px auto-fitted to container width so all ~101k pages fit cleanly on one screen without vertical scrolling marathon).
       - Medium/small critical sections (`32..4,095` pages, e.g., `.noptrdata` `567` pages, `.data` `314` pages, `.typelink` `160` pages, `.itablink` `51` pages): enlarged cells (`5×5` to `8×8` CSS px with 1px gap) so writable CoW splits and hot `.data` thrashing are unmistakably legible.
       - Tiny metadata sections (`< 32` pages, e.g., `.note.go.buildid` `1` page, `.go.buildinfo` `31` pages): rendered with compact visible cells alongside their section label so zero pages are hidden.
     - **Rendering performance**: Precompute `(canvasX, canvasY, cellSize)` lookup typed arrays (`Uint16Array`) per page once on `run_layout` / window resize. Each animation frame writes directly into Canvas `ImageData` (`Uint32Array` ABGR pixel buffer) or batched rects in `< 2 ms` per frame at 60fps, blending active 350ms flash decay smoothly across recently transitioned pages.
   - **Macro Block Mode (`256 KiB Blocks` toggle)**:
     - Aggregates the current frame's `Uint8Array(totalPages)` dynamically into contiguous 256 KiB binary-offset tiles (~1,596 larger tiles), coloring each tile by dominant state / refault heat intensity.
   - **Viewport-Safe Hover Tooltip**:
     - Hovering any cell in either mode pinpoints the exact 4 KiB page (or 256 KiB block) via O(1) band geometry hit-testing and displays: ELF section + permissions, binary file offset (`0x...`), current residency/CoW state, and exact page stats at the viewed frame.

5. **Timeline History Scrubber & Slow-Mo Replay Bar**:
   - Client-side frame store keeps every incoming frame's sparse `Uint32Array` delta plus a full `Uint8Array(totalPages)` checkpoint every `64` frames (~5–8 MB total browser RAM for a 300s 3,000-frame benchmark).
   - **Mini Activity Track above Scrubber Slider**: thin canvas strip plotting per-frame cold faults (cyan), evictions (mint), refaults (red), the vertical `Ready` marker, and any mid-run `memory.max` / `memory.reclaim` action pins so you can immediately spot startup bursts and thrashing spikes and drag straight to them.
   - **Playback Controls**:
     - **`Live` pill / button**: follows newest incoming SSE samples automatically during a run; dragging the slider or clicking **Pause** detaches from live tip while continuing to buffer live frames in the background.
     - **Play / Pause**, **Step `◀` / `▶` (single frame)**, and **Replay Speed selector** (`0.1×` slow-motion for inspecting the 3.5s Go `init()` startup sweep, `0.25×`, `0.5×`, `1×`, `2×`).
   - Post-run diagnostic drawer: collapsible panel showing the ending verdict summary, saved artifact directory path (if `--results-dir` was set), and any Collector log tail on error/OOM.

---

## 5. Implementation & Test Plan

1. **`internal/cgroup`**:
   - Add `SetMemoryMax` and `Reclaim` methods to [`cgroup_linux.go`](../internal/cgroup/cgroup_linux.go).
   - Add unit tests in [`cgroup_linux_test.go`](../internal/cgroup/cgroup_linux_test.go) covering `memory.max` updates, `memory.reclaim` normal writes, `EAGAIN` partial-reclaim handling, and missing pseudo-file rejection (`O_WRONLY` only).
2. **`internal/pagemap`**:
   - Add `PageVisualCode` constants, `SectionSpan` layout export (`Tracker.LayoutSpans()`), and optional `SampleWithDeltas(row *results.SampleRow, deltas []uint32) ([]uint32, error)` in [`tracker.go`](../internal/pagemap/tracker.go) (`Sample(row)` remains backward-compatible).
   - Add unit tests in [`tracker_test.go`](../internal/pagemap/tracker_test.go) verifying packed `uint32` codes across cold file fault, cold CoW fault, `file -> CoW` promotion, clean eviction, `0→1` refault, PFN-change refault, and thrashing ($\ge 2$) transitions.
3. **`internal/manager`**:
   - Add `WebPort int` and optional `Hooks` / live `Controller` registration to `manager.Options` in [`runner_linux.go`](../internal/manager/runner_linux.go) (delivering active `*cgroup.Cgroup` access safely for `SetMemoryMax` / `Reclaim`, supporting UI stop cancellation, and retaining full standalone CLI behavior).
   - Add unit tests in [`runner_linux_test.go`](../internal/manager/runner_linux_test.go) for web sampling interval selection and stop-context handling.
4. **`internal/web` (New Package)**:
   - `server_linux.go`: Single-flight run coordinator, in-memory active run snapshot/replay cache, HTTP mux (`GET /`, `GET /api/state`, `GET /api/events` SSE hub, `POST /api/run/{start,stop,memory-max,reclaim}`), origin/CSRF guard, and loopback listener setup.
   - `static/{index.html, app.css, app.js}` embedded via `//go:embed static/*`.
   - `server_linux_test.go`: Unit/HTTP tests with mock runner verifying loopback validation, single-flight conflict (`409`), SSE frame/delta broadcast, reconnect `/api/state` replay, origin checks, and mid-run control endpoints.
5. **`cmd/manager` & `README.md`**:
   - Wire `-web <port>` in [`cmd/manager/main.go`](../cmd/manager/main.go) and relax `--results-dir` requirement when `-web > 0`.
   - Document `-web` usage, live controls, and visualization guide in [`README.md`](../README.md).
6. **Verification**:
   - Run `go test -v -race ./...` and `make build`, ensuring zero temporary artifacts remain outside `bin/`.
