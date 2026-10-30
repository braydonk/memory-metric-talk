# Kubelet `working_set_bytes` Simulation & Visualization Spec

This document specifies how `mem_eviction_lab` simulates, persists, and visualizes Kubernetes container `working_set_bytes` across both the live `-web` interface (`internal/web`) and the static HTML report generator (`cmd/visualizer`).

---

## 1. Kernel & Kubernetes Semantics

### 1.1 Exact Cgroup v2 `working_set_bytes`
In Kubernetes on Linux cgroup v2, both cAdvisor (`setMemoryStats`) and CRI runtimes via `opencontainers/runc/libcontainer/cgroups/fs2` define container `working_set_bytes` as:

```text
cg_working_set_bytes = max(0, memory.current - memory.stat[inactive_file])
```

This is the exact value exported as `container_memory_working_set_bytes`, displayed by `kubectl top pod` / Metrics Server, and evaluated by Kubelet's node-pressure eviction manager against `memory.available`.

Key physical consequences bridging process `VmRSS` (`/proc/<pid>/status`) to Kubelet evictions:
1. **`RssFile` splits across Active and Inactive LRUs**: `VmRSS = RssAnon + RssFile + RssShmem` counts every mapped PTE equally. Inside the cgroup, binary pages on `inactive_file` are subtracted from `working_set_bytes`, whereas binary pages on **`active_file`** ARE counted toward `working_set_bytes`.
2. **Asynchronous Kubelet eviction vs. synchronous `memory.max` reclaim**: Container limit enforcement (`memory.max`) triggers synchronous direct reclaim (demoting/evicting cold binary pages) before invoking the cgroup OOM killer. In contrast, Kubelet node-pressure pod eviction passively polls `working_set_bytes` without reclaiming the pod first — meaning startup binary bloat sitting in `active_file` inflates a pod's eviction rank until pressure or `memory.reclaim` ages those pages out.
3. **Kernel memory & unmapped page cache**: `working_set_bytes` includes cgroup `kernel` memory (slab, kernel stacks, page tables) and active unmapped page cache (e.g., read config files or mmap readahead pages), both of which `VmRSS` omits.

### 1.2 Foreign Page-Cache Ownership & Estimated Isolated-Pod Working Set
Under Linux cgroup v2, page cache folios are charged to the **first cgroup** that reads or faults them into memory.
- **Cold-isolated runs** (`proc_rss_file_bytes <= cg_file_bytes`): The Collector leaf cgroup owns all of its mapped binary pages, so `uncharged_rss_file_bytes == 0`.
- **Shared / foreign page-cache runs** (`proc_rss_file_bytes > cg_file_bytes`): Occurs when a host agent (e.g., an endpoint `fanotify` binary scanner during `execve`), `--allow-warm-cache`, or another pod sharing the same container image layer warms binary pages under a different cgroup before the Collector faults them in.

To show both what Kubelet literally observes on the current host and what an isolated single-pod cgroup would be charged prior to LRU aging/reclaim, the lab computes side-by-side:

```text
uncharged_rss_file_bytes       = max(0, proc_rss_file_bytes - cg_file_bytes)
est_isolated_working_set_bytes = cg_working_set_bytes + uncharged_rss_file_bytes
```

---

## 2. Artifact Schema & Backward Compatibility (`internal/results`, `internal/manager`)

### 2.1 `samples.csv` (`SampleRow`)
Append two columns to `samples.csv`:
- `cg_working_set_bytes`: Exact cgroup v2 `max(0, cg_memory_current_bytes - cg_inactive_file_bytes)`.
- `est_isolated_working_set_bytes`: `cg_working_set_bytes + max(0, proc_rss_file_bytes - cg_file_bytes)`.

When `results.LoadRunData` reads legacy `samples.csv` artifacts lacking these columns, it automatically calls `SampleRow.PopulateWorkingSet()` to derive both fields from the existing cgroup and `/proc/<pid>/status` columns.

### 2.2 `run_meta.json` (`FinalSummary`)
Extend `FinalSummary` with peak and final memory comparison fields:
- `peak_cg_working_set_bytes`, `final_cg_working_set_bytes`
- `peak_est_isolated_ws_bytes`, `final_est_isolated_ws_bytes`
- `final_cg_memory_bytes`, `final_cg_active_file_bytes`, `final_cg_inactive_file_bytes`, `final_proc_rss_bytes`

When `visualizer.Generate` loads legacy `run_meta.json` artifacts whose new summary fields are `0`, `buildSummary` derives peak and final values directly from `RunData.Samples`.

---

## 3. Live `-web` Observatory (`internal/web/static/`)

1. **Cgroup v2 Memory Arena Bar**:
   - Four ordered segments from left to right:
     1. `Anon (Heap/Stack)`
     2. `Kernel (Slab/PT)`
     3. `Active File (In WS)` — solid cyan fill inside Kubelet Working Set
     4. `Inactive File (Excl. from WS)` — muted diagonal-hatched fill representing reclaimable cache excluded by Kubelet
   - Overlaid vertical **Kubelet Working Set marker** (`Kubelet WS <X> MiB`) positioned at exact `cg_working_set_bytes`, plus a secondary dashed **Est. Iso WS** marker whenever `est_isolated_working_set_bytes - cg_working_set_bytes >= 1 MiB`.
2. **KPI Strip**:
   - Expand strip to 7 cells, inserting **Kubelet Working Set** immediately after **Cgroup Memory**, displaying side-by-side `<Cgroup WS> | Iso <Est. Iso WS>` and subtext comparing against `VmRSS` (`Δ` vs `VmRSS`) plus subtracted `Inactive File`.
3. **Timeline Scrubber Sparkline**:
   - Overlay continuous normalized memory traces over the fault/eviction/refault bars: solid `Kubelet WS`, subtle upper `memory.current` trace (highlighting the `inactive_file` cushion), and dashed `Est. Iso WS` when foreign cache is present.

---

## 4. Static Report Visualizer (`cmd/visualizer`)

1. **Multi-Run Sweep Comparison**:
   - Expand `#comparison-table` to 12 columns by inserting **Kubelet Working Set (Peak → Final)** (`#4`), rendering exact `Cgroup WS (Peak → Final)` alongside `Iso WS (Peak → Final)` and final `VmRSS` in every row.
   - Document the formula, `active_file` vs `inactive_file` mechanics, asynchronous Kubelet eviction polling, and `VmRSS` differences inside `#comparison-guide` and viewport-safe `th[data-tip]` headers.
   - Add a multi-run **Kubelet Working Set Over Time (MiB)** time-series chart with rich tooltip breakdown (`Cgroup WS`, `Est. Isolated WS`, `memory.current`, `VmRSS`).
2. **Single-Run Deep Dive**:
   - Add **Kubelet Working Set (Peak → Final)** KPI card showing side-by-side Cgroup & Est. Isolated WS plus final `Active File`, `Inactive File`, and `VmRSS`.
   - Upgrade **Cgroup Memory Composition & Kubelet Working Set vs Limit (MiB)** to stack `Anon + Kernel + Active File (In WS) + Inactive File (Excl. from WS)` underneath overlaid `Kubelet Working Set (Cgroup)`, `Est. Isolated-Pod WS`, `memory.current`, `Process VmRSS`, and `memory.max` lines.
