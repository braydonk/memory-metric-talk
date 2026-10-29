# Reclaimed-Page Refault % & One-Shot Startup Analysis Spec

This document specifies enhancements to `cmd/visualizer` ([`internal/visualizer/visualizer.go`](../internal/visualizer/visualizer.go) and [`internal/visualizer/template.go`](../internal/visualizer/template.go)) to directly answer two central questions from existing run artifacts without changing `cmd/manager` or re-running sweeps:

1. **What percentage of reclaimed Collector binary pages actually refaulted vs. stayed gone permanently?**
2. **How much of each ELF section (`.text`, `.rodata`, `.gopclntab`, `.data`, ...) was cold-faulted at startup and cleanly evicted as one-shot bloat during steady-state runtime?**

---

## 1. Exact Mathematical Identities Over Existing Artifact Fields

All metrics derive deterministically from existing columns in `samples.csv`, `sections.csv`, and `run_meta.json`.

### 1.1 Run-Wide Unique Reclaimed Page Partition
In [`Tracker.Sample`](../internal/pagemap/tracker.go), every unique 4 KiB binary page ever evicted (`bin_ever_evicted_pages`) belongs strictly to one of two disjoint subsets:
- **Cleanly Evicted (`bin_cleanly_evicted_pages`)**: evicted after cold fault, `0` refaults across the entire run.
- **Ever Refaulted (`bin_ever_refaulted_pages`)**: evicted and faulted back in $\ge 1\times$.

$$\text{bin\_ever\_evicted\_pages} \equiv \text{bin\_cleanly\_evicted\_pages} + \text{bin\_ever\_refaulted\_pages}$$

From the last live sample (`bin_total_pages > 0`, falling back to `FinalSummary` fields `cleanly_evicted_pages + ever_refaulted_pages`):
- **`ReclaimedRefaultPercent`** $= \dfrac{\text{bin\_ever\_refaulted\_pages}}{\text{bin\_ever\_evicted\_pages}} \times 100\%$ *(defined when `bin_ever_evicted_pages > 0`; rendered as `—` when `0` pages were reclaimed).*
- **`ReclaimedCleanPercent`** $= \dfrac{\text{bin\_cleanly\_evicted\_pages}}{\text{bin\_ever\_evicted\_pages}} \times 100\% = 100\% - \text{ReclaimedRefaultPercent}$.

### 1.2 Startup vs. Runtime Cold-Fault Split
When `meta.workload.workload_enabled == true`:
- If `meta.workload.startup_readiness_ms > 0` (`StartupCompleted = true`):
  - Select the **first live sample** with `elapsed_ms >= startup_readiness_ms` (falling back to the last live sample if the run terminated immediately at readiness), recording its timestamp `StartupSampleMs` and cumulative cold faults `StartupColdFaults = sample.cum_cold_faults`.
  - `RuntimeColdFaults = TotalColdFaults - StartupColdFaults`.
  - `StartupColdFaultPercent = (StartupColdFaults / TotalColdFaults) * 100%`.
  - Using the first sample $\ge$ readiness ensures all cold faults occurring during `execve`, Go package `init()`, and gRPC listener startup up to `startup_readiness_ms` are captured even when `sample_interval` is `500ms`.
- If `meta.workload.startup_readiness_ms == 0` (`StartupTimedOut = true`, e.g., extreme `init()` thrashing hitting `startup_timeout` or instant startup OOM):
  - Display `"Did not reach readiness"` instead of computing a startup/runtime split.
- If `meta.workload.workload_enabled == false` (self-driving configs like `run-hostmetrics.yaml`):
  - Omit the startup-vs-runtime split card/marker rather than guessing a heuristic boundary.

### 1.3 Per-ELF-Section Reclaim & One-Shot Identities
From each row of [`sections.csv`](../internal/results/types.go) (`SectionRow`):
- **`EverFaultedPages`** $= \text{total\_pages} - \text{never\_faulted\_pages}$
- **`FinalResidentPages`** $= \text{present\_final\_pages} + \text{present\_cow\_final\_pages}$
- **`EverRefaultedPages`** $= \text{evicted\_after\_refault\_pages} + \text{resident\_after\_refault\_pages}$
- **`EverEvictedPages`** $= \text{cleanly\_evicted\_pages} + \text{EverRefaultedPages}$
- **`OneShotCleanPctOfFaulted`** $= \dfrac{\text{cleanly\_evicted\_pages}}{\text{EverFaultedPages}} \times 100\%$ *(lower bound on startup-only bloat within that section: un-evicted resident pages or startup symbols sharing a 4 KiB page with active code are not counted).*
- **`ReclaimedRefaultPct`** $= \dfrac{\text{EverRefaultedPages}}{\text{EverEvictedPages}} \times 100\%$ *(when `EverEvictedPages > 0`, else `—`).*

---

## 2. Dashboard UI Design

### 2.1 Multi-Run Comparison Section (`#multi-run-section`)
1. **New Column `#6`: `Reclaimed Refault %` in `#comparison-table`** (inserted between `Cleanly Evicted` and `Binary Refaults (0→1 / PFN)`, making 11 columns total):
   - Primary value: **`48.3%`** (colored green when `< 5%`, amber when `5%–25%`, red when `> 25%`; rendered as muted `—` if `ever_evicted_pages == 0`).
   - Subtext: `<ever_refaulted_mib> / <ever_evicted_mib> MiB`.
2. **Collapsible Guide (`#comparison-guide`) & Header Tooltips**:
   - Update **Group B (Collector Binary Tracking)** to Columns `4–7`, adding card **`#6 Reclaimed Refault %`** documenting the exact partition `ever_evicted = cleanly_evicted + ever_refaulted`, how to read low vs. high percentages alongside `Cleanly Evicted`, and renumbering downstream cards to `#7`..`#11`.
   - Add `tabindex="0" data-tip="..."` on the new `<th>` header so `#th-tooltip` covers all 11 columns.
3. **Cliff Chart (`#chart-multi-cliff`) Tooltip Enrichment**:
   - Enhance the axis tooltip formatter on `#chart-multi-cliff` to include `Unique Reclaimed Binary`, `Reclaimed Stayed Clean %`, and **`Reclaimed Refault %`** when hovering any memory limit bar.

### 2.2 Run Deep Dive Section (`#deep-dive-section`)
1. **KPI Grid Updates**:
   - Update the **`Cleanly Reclaimed Binary`** KPI card label to **`One-Shot Evicted (Lower Bound)`**, showing `<cleanly_evicted_bin_mib> MiB (<clean_reclaim_pct>% of faulted)` with subtext highlighting `<reclaimed_clean_pct>% stayed clean | <reclaimed_refault_pct>% of <ever_evicted_mib> MiB reclaimed refaulted`.
   - Add a **`Startup vs. Runtime Cold Faults`** phrase inside the `Binary Resident (Peak -> Final)` KPI subtext (when `StartupCompleted` is true: e.g., `161.3 MiB faulted (99.5% by readiness @ 3.7s)` or `Did not reach readiness` if `StartupTimedOut`).
2. **`Startup Ready` Marker Line on Time-Series Charts**:
   - When `StartupCompleted` is true (`startup_readiness_ms > 0`), draw a subtle vertical dashed `markLine` at `x = startup_readiness_ms / 1000` labeled `Ready (<t>s)` on `#chart-bin-lifecycle` and `#chart-cum-transitions` so viewers can visually see cold faults flatten right at startup completion while post-startup reclaim sheds unused pages.
3. **ELF Section Reclaim & One-Shot Bloat Analysis (inside the `#chart-sections` card)**:
   - Immediately below `#chart-sections`, render:
     - **Auto-Generated Insight Callout Banner (`#section-insight-banner`)**: Concise computed takeaway citing the run's actual numbers:
       - Startup cold-fault concentration (when `StartupCompleted`: e.g., *"99.5% of all faulted binary memory (160.4 of 161.3 MiB) paged in by Collector readiness at 3.72s; only 0.87 MiB cold-faulted across the remaining 300.1s of runtime."*).
       - Section reclaim headline comparing `.text` vs. `.rodata` (when evictions occurred: e.g., *".text shed 46.58 MiB (70.1% of faulted code) with 0 refaults (only 27.3% of reclaimed .text pages refaulted), shrinking resident code to 5.18 MiB—whereas 75.6% of reclaimed .rodata pages refaulted due to active Go runtime/GC type metadata."*).
       - Explicit 1-line lower-bound note explaining why `One-Shot Evicted (% of Faulted)` is a conservative lower bound (only pages actually tested by eviction and Sharing no 4 KiB page with active symbols can prove `0` refaults).
     - **Per-Section Analysis Table (`#section-analysis-table`)**:
       Columns (with floating `data-tip` header tooltips wired through `#th-tooltip`):
       1. `ELF Section` (`name` + `vma_perms` pill)
       2. `Mapped` (`MiB` + `% of binary`)
       3. `Ever Faulted` (`MiB` + `% of section`)
       4. `One-Shot Evicted (Lower Bound)` (`cleanly_evicted MiB` + **`% of faulted`**)
       5. `Unique Reclaimed` (`ever_evicted MiB`)
       6. `Reclaimed Refault %` (**`ever_refaulted / ever_evicted %`**, plus `total_refaults` events in subtext)
       7. `Final Resident` (`present file + CoW MiB` + `% of faulted`)
       Sorted by `Ever Faulted` descending so `.text`, `.rodata`, and `.gopclntab` appear right at the top.

---

## 3. Implementation & Verification Plan

1. **Extend [`RunSummary`](../internal/visualizer/visualizer.go)**:
   - Compute and populate `EverEvictedPages`, `EverEvictedBinMiB`, `EverRefaultedBinMiB`, `ReclaimedRefaultPercent`, `ReclaimedCleanPercent`, `WorkloadEnabled`, `StartupReadinessMs`, `StartupCompleted`, `StartupTimedOut`, `StartupSampleMs`, `StartupColdFaults`, `RuntimeColdFaults`, `StartupColdFaultPercent`.
2. **Update [`template.go`](../internal/visualizer/template.go)**:
   - Add column `#6 Reclaimed Refault %` to `#comparison-table`, update `#comparison-guide` cards (`#1`..`#11`), update `#chart-multi-cliff` tooltip formatter, render the `Ready` vertical `markLine` on `#chart-bin-lifecycle` and `#chart-cum-transitions`, and render `#section-insight-banner` + `#section-analysis-table` directly under `#chart-sections` inside `renderSingleRunCharts()`.
   - Extend `initHeaderTooltips()` (using event delegation on `document`) so `th[data-tip]` works seamlessly on both `#comparison-table` and dynamically visible `#section-analysis-table` headers.
3. **Update [`visualizer_test.go`](../internal/visualizer/visualizer_test.go)**:
   - Extend `writeSyntheticRun` with multi-section rows (`.text` and `.rodata`), non-zero refaults, and `WorkloadSummary{WorkloadEnabled: true, StartupReadinessMs: 500}` to unit-test exact `RunSummary` percentages (`ReclaimedRefaultPercent`, `StartupColdFaultPercent`), the 11-column `#comparison-table` + guide, and `#section-analysis-table` presence.
4. **Verify & Regenerate**:
   - Run `make build test` (`go test -v -race ./...`).
   - Regenerate `comparison_dashboard.html` over `results/otlp-*`.
