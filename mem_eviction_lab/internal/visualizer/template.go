package visualizer

const dashboardHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>{{.Title}}</title>
  <script src="https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js"></script>
  <style>
    :root {
      --bg: #0b0f19;
      --card: #131b2e;
      --card-hover: #19243d;
      --border: #1e293b;
      --text: #e2e8f0;
      --muted: #94a3b8;
      --accent: #38bdf8;
      --good: #4ade80;
      --warn: #fbbf24;
      --bad: #f87171;
      --purple: #c084fc;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      padding: 24px;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Inter, sans-serif;
      background: var(--bg);
      color: var(--text);
    }
    header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      flex-wrap: wrap;
      gap: 12px;
      margin-bottom: 24px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--border);
    }
    h1 { margin: 0; font-size: 1.45rem; font-weight: 600; }
    h2 { margin: 0 0 12px 0; font-size: 1.15rem; font-weight: 600; }
    h3 { margin: 0 0 8px 0; font-size: 0.95rem; font-weight: 600; color: var(--text); }
    .subtle { color: var(--muted); font-size: 0.85rem; }
    .section-block { margin-bottom: 32px; }
    .card {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 16px;
    }
    .table-wrap { overflow-x: auto; margin-bottom: 20px; }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.85rem;
    }
    th, td {
      padding: 10px 12px;
      text-align: left;
      border-bottom: 1px solid var(--border);
      white-space: nowrap;
    }
    th { color: var(--muted); font-weight: 600; font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.03em; }
    tbody tr { cursor: pointer; transition: background 0.15s; }
    tbody tr:hover { background: var(--card-hover); }
    tbody tr.active-row { background: rgba(56, 189, 248, 0.12); }
    .badge {
      display: inline-block;
      padding: 3px 8px;
      border-radius: 999px;
      font-size: 0.72rem;
      font-weight: 700;
      letter-spacing: 0.03em;
      text-transform: uppercase;
    }
    .badge-clean_reclaim { background: rgba(74, 222, 128, 0.16); color: var(--good); border: 1px solid rgba(74, 222, 128, 0.4); }
    .badge-thrashing     { background: rgba(251, 191, 36, 0.16); color: var(--warn); border: 1px solid rgba(251, 191, 36, 0.4); }
    .badge-oom_killed    { background: rgba(248, 113, 113, 0.18); color: var(--bad);  border: 1px solid rgba(248, 113, 113, 0.45); }
    .badge-no_reclaim    { background: rgba(56, 189, 248, 0.15); color: var(--accent); border: 1px solid rgba(56, 189, 248, 0.4); }
    .badge-process_error { background: rgba(248, 113, 113, 0.18); color: var(--bad);  border: 1px solid rgba(248, 113, 113, 0.45); }

    .run-tabs {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-bottom: 16px;
    }
    .run-tab {
      background: var(--card);
      color: var(--text);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 8px 14px;
      font-size: 0.85rem;
      font-weight: 500;
      cursor: pointer;
    }
    .run-tab:hover { background: var(--card-hover); }
    .run-tab.active {
      border-color: var(--accent);
      background: rgba(56, 189, 248, 0.14);
      color: var(--accent);
    }

    .verdict-banner {
      display: flex;
      align-items: center;
      gap: 12px;
      flex-wrap: wrap;
      margin-bottom: 16px;
      padding: 14px 16px;
      border-radius: 10px;
      background: var(--card);
      border: 1px solid var(--border);
    }
    .kpi-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
      gap: 14px;
      margin-bottom: 20px;
    }
    .kpi-card {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 14px;
    }
    .kpi-card .label { display: block; color: var(--muted); font-size: 0.75rem; margin-bottom: 4px; }
    .kpi-card .val { display: block; font-size: 1.2rem; font-weight: 600; }
    .kpi-card .sub { display: block; color: var(--muted); font-size: 0.76rem; margin-top: 4px; }

    .chart-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(540px, 1fr));
      gap: 18px;
      margin-bottom: 18px;
    }
    .chart-full {
      margin-bottom: 18px;
    }
    .chart-box { width: 100%; height: 340px; }
    .chart-box-tall { width: 100%; height: 400px; }

    .guide-details {
      padding: 0;
      margin-bottom: 16px;
      overflow: hidden;
    }
    .guide-summary {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      padding: 12px 16px;
      cursor: pointer;
      list-style: none;
      user-select: none;
      font-size: 0.9rem;
      font-weight: 600;
      transition: background 0.15s;
    }
    .guide-summary::-webkit-details-marker { display: none; }
    .guide-summary:hover { background: var(--card-hover); }
    .guide-summary-left {
      display: flex;
      align-items: center;
      gap: 10px;
    }
    .guide-chevron {
      display: inline-block;
      color: var(--accent);
      font-size: 0.78rem;
      transition: transform 0.18s ease;
    }
    .guide-details[open] .guide-chevron { transform: rotate(90deg); }
    .guide-hint-open { display: inline; }
    .guide-hint-close { display: none; }
    .guide-details[open] .guide-hint-open { display: none; }
    .guide-details[open] .guide-hint-close { display: inline; }
    .guide-body {
      padding: 16px;
      border-top: 1px solid var(--border);
    }
    .lifecycle-title {
      font-size: 0.78rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--muted);
      margin: 0 0 8px 0;
    }
    .lifecycle-strip {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: 10px;
      margin-bottom: 18px;
    }
    .lifecycle-step {
      background: rgba(15, 23, 42, 0.65);
      border: 1px solid var(--border);
      border-left: 3px solid var(--accent);
      border-radius: 8px;
      padding: 10px 12px;
      font-size: 0.8rem;
      line-height: 1.45;
    }
    .lifecycle-step strong { display: block; margin-bottom: 3px; font-size: 0.82rem; }
    .lifecycle-step.step-cold    { border-left-color: var(--accent); }
    .lifecycle-step.step-evict   { border-left-color: var(--good); }
    .lifecycle-step.step-refault { border-left-color: var(--warn); }
    .lifecycle-step.step-thrash  { border-left-color: var(--bad); }

    .guide-group { margin-top: 16px; }
    .guide-group-title {
      font-size: 0.78rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--accent);
      margin: 0 0 10px 0;
      padding-bottom: 6px;
      border-bottom: 1px solid var(--border);
    }
    .guide-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(310px, 1fr));
      gap: 12px;
    }
    .guide-item {
      background: rgba(15, 23, 42, 0.55);
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 12px 14px;
      font-size: 0.82rem;
      line-height: 1.5;
    }
    .guide-item-wide { grid-column: 1 / -1; }
    .guide-item-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      flex-wrap: wrap;
      gap: 6px;
      margin-bottom: 6px;
    }
    .guide-col-title {
      display: flex;
      align-items: center;
      gap: 7px;
      font-size: 0.86rem;
    }
    .col-num {
      display: inline-block;
      padding: 1px 6px;
      border-radius: 4px;
      font-size: 0.7rem;
      font-weight: 700;
      color: var(--accent);
      background: rgba(56, 189, 248, 0.14);
      border: 1px solid rgba(56, 189, 248, 0.3);
    }
    .source-tag {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 0.72rem;
      color: #cbd5e1;
      background: rgba(51, 65, 85, 0.55);
      padding: 2px 6px;
      border-radius: 4px;
    }
    .guide-item code {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 0.76rem;
      color: #cbd5e1;
      background: rgba(51, 65, 85, 0.45);
      padding: 1px 4px;
      border-radius: 3px;
    }
    .guide-tip {
      color: var(--muted);
      font-size: 0.78rem;
      margin-top: 6px;
    }
    .outcome-list {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
      gap: 8px 14px;
      margin-top: 8px;
      font-size: 0.79rem;
    }
    .outcome-entry {
      display: flex;
      align-items: flex-start;
      gap: 8px;
    }
    .outcome-entry .badge { flex-shrink: 0; margin-top: 2px; }

    .pct-good { color: var(--good); font-weight: 600; }
    .pct-warn { color: var(--warn); font-weight: 600; }
    .pct-bad  { color: var(--bad);  font-weight: 600; }
    .perm-pill {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 0.72rem;
      color: var(--muted);
      background: rgba(51, 65, 85, 0.5);
      padding: 1px 6px;
      border-radius: 4px;
      margin-left: 6px;
    }
    .insight-banner {
      margin: 14px 0 16px 0;
      padding: 12px 16px;
      border-radius: 8px;
      background: rgba(56, 189, 248, 0.07);
      border: 1px solid rgba(56, 189, 248, 0.28);
      border-left: 3px solid var(--accent);
      font-size: 0.83rem;
      line-height: 1.55;
    }
    .insight-banner ul { margin: 0; padding-left: 18px; }
    .insight-banner li { margin-bottom: 5px; }
    .insight-banner li:last-child { margin-bottom: 0; }
    .insight-banner .caveat {
      margin-top: 8px;
      padding-top: 8px;
      border-top: 1px solid rgba(148, 163, 184, 0.16);
      color: var(--muted);
      font-size: 0.77rem;
    }
    #section-analysis-table tbody tr { cursor: default; }
    #package-analysis-table tbody tr { cursor: default; }
    #package-analysis-table td:first-child { word-break: break-all; font-family: ui-monospace, monospace; font-size: 0.8rem; }

    th[data-tip] {
      cursor: help;
      text-decoration: underline dotted rgba(148, 163, 184, 0.65);
      text-underline-offset: 4px;
      outline: none;
    }
    th[data-tip]:hover,
    th[data-tip]:focus-visible {
      color: var(--text);
      text-decoration-color: var(--accent);
    }
    #th-tooltip {
      position: fixed;
      z-index: 9999;
      display: none;
      max-width: 320px;
      padding: 8px 11px;
      border-radius: 6px;
      background: rgba(15, 23, 42, 0.96);
      border: 1px solid #334155;
      box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
      color: var(--text);
      font-size: 0.78rem;
      line-height: 1.45;
      pointer-events: none;
      white-space: normal;
    }
  </style>
</head>
<body>
  <header>
    <div>
      <h1>{{.Title}}</h1>
      <div class="subtle" id="subtitle"></div>
    </div>
    <div class="subtle" id="generated-at"></div>
  </header>

  <div id="multi-run-section" class="section-block" style="display:none;">
    <h2>Memory Limit Sweep Comparison</h2>
    <details id="comparison-guide" class="card guide-details">
      <summary class="guide-summary">
        <span class="guide-summary-left">
          <span class="guide-chevron">&#9654;</span>
          <span>How to Read This Table &mdash; Metric &amp; Kernel Source Guide</span>
        </span>
        <span class="subtle">
          <span class="guide-hint-open">Click to expand</span>
          <span class="guide-hint-close">Click to collapse</span>
        </span>
      </summary>
      <div class="guide-body">
        <div class="lifecycle-title">Collector Binary Page Lifecycle Under Cgroup v2 Pressure</div>
        <div class="lifecycle-strip">
          <div class="lifecycle-step step-cold">
            <strong>1. Cold Fault (0 &rarr; 1)</strong>
            First-ever access loads a 4 KiB page of Collector <code>.text</code>, <code>.rodata</code>, or <code>.data</code> from disk into page cache during <code>execve</code> / Go <code>init()</code>.
          </div>
          <div class="lifecycle-step step-evict">
            <strong>2. Clean Eviction (1 &rarr; 0, stays 0)</strong>
            When <code>memory.max</code> is tight, the kernel drops clean file-backed binary pages used only once at startup. Zero ongoing CPU or I/O cost.
          </div>
          <div class="lifecycle-step step-refault">
            <strong>3. Binary Refault (1 &rarr; 0 &rarr; 1)</strong>
            An evicted binary page belonged to the active working set; touching that instruction or read-only metadata again stalls execution to fault it back in.
          </div>
          <div class="lifecycle-step step-thrash">
            <strong>4. Page Thrashing / OOM Kill</strong>
            Non-reclaimable <code>anon + kernel</code> squeezes file cache below the active binary working set, causing continuous refaults, high PSI stalls, or <code>oom_kill</code>.
          </div>
        </div>

        <div class="guide-group">
          <div class="guide-group-title">Run Setup &amp; Verdict (Columns 1&ndash;3)</div>
          <div class="guide-grid">
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#1</span><strong>Run</strong></span>
                <span class="source-tag">run_meta.json: name, dir</span>
              </div>
              <div>Experiment configuration name and result directory basename for each sweep point.</div>
              <div class="guide-tip">Sweeps sort automatically from <code>Unlimited (max)</code> down to the tightest limit. Click any row to load that run into the <strong>Run Deep Dive</strong> below.</div>
            </div>
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#2</span><strong>Memory Limit</strong></span>
                <span class="source-tag">cgroup v2: memory.max, memory.swap.max</span>
              </div>
              <div>Hard cgroup v2 byte ceiling enforced from <code>t=0</code> via <code>CLONE_INTO_CGROUP</code> before <code>execve</code> faults any pages.</div>
              <div class="guide-tip">With <code>memory.swap.max=0</code> (default), anonymous Go heap/stack pages cannot swap out, forcing kernel reclaim to target clean file-backed binary pages.</div>
            </div>
            <div class="guide-item guide-item-wide">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#3</span><strong>Outcome</strong></span>
                <span class="source-tag">manager: classifyVerdict()</span>
              </div>
              <div>Automated verdict summarizing how the Collector tolerated this <code>memory.max</code> limit (evaluated top-to-bottom):</div>
              <div class="outcome-list">
                <div class="outcome-entry">
                  <span class="badge badge-oom_killed">oom killed</span>
                  <span>Kernel cgroup OOM killer terminated the Collector (<code>memory.events:oom_kill &gt; 0</code>) because non-reclaimable <code>anon + kernel</code> exhausted the limit.</span>
                </div>
                <div class="outcome-entry">
                  <span class="badge badge-process_error">process error</span>
                  <span>Collector failed startup readiness within <code>startup_timeout</code> or exited early non-zero without OOM. <em>Check refaults/PSI: heavy Go <code>init()</code> thrashing can time out readiness before OOM fires.</em></span>
                </div>
                <div class="outcome-entry">
                  <span class="badge badge-no_reclaim">no reclaim</span>
                  <span>Zero binary evictions and <code>memory.events:max == 0</code>; cgroup memory remained comfortably under the limit.</span>
                </div>
                <div class="outcome-entry">
                  <span class="badge badge-thrashing">thrashing</span>
                  <span>Continuous post-startup page churn where binary pages are simultaneously evicted and refaulted across sustained steady-state windows (<code>&ge;25%</code> of 1s windows, <code>&ge;50%</code> of 5s epochs, or <code>&ge;6/10</code> trailing seconds active).</span>
                </div>
                <div class="outcome-entry">
                  <span class="badge badge-clean_reclaim">clean reclaim</span>
                  <span>Kernel reclaimed unused binary pages cleanly, or stabilized after bounded startup / manual-reclaim corrections without continuous steady-state churn.</span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div class="guide-group">
          <div class="guide-group-title">Kubernetes Pod Eviction Accounting &mdash; Cgroup v2 + procfs (Column 4)</div>
          <div class="guide-grid">
            <div class="guide-item guide-item-wide">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#4</span><strong>Kubelet Working Set</strong></span>
                <span class="source-tag">memory.current &minus; inactive_file | proc_rss_file</span>
              </div>
              <div>Simulates exact Kubernetes <code>container_memory_working_set_bytes = max(0, memory.current &minus; memory.stat[inactive_file])</code> (<strong>Peak &rarr; Final</strong>) side by side with <strong>Est. Isolated-Pod WS</strong> (<code>+ max(0, proc_rss_file &minus; cg_file)</code>) and final <strong>Process VmRSS</strong>.</div>
              <div class="guide-tip">Why it matters for pod evictions: inside <code>VmRSS = RssAnon + RssFile + RssShmem</code>, binary pages on <code>inactive_file</code> are excluded by Kubelet, whereas binary pages on <strong><code>active_file</code></strong> (plus cgroup <code>kernel</code> slab/page-tables omitted from <code>VmRSS</code>) count directly toward <code>kubectl top pod</code> and Kubelet <code>memory.available</code> eviction ranking. Unlike <code>memory.max</code>, Kubelet polls asynchronously without reclaiming the pod first &mdash; so cold startup binary bloat left in <code>active_file</code> keeps eviction risk elevated until pressure ages those pages out. When host/shared page cache prevents first-touch cgroup charging (<code>proc_rss_file &gt; cg_file</code>), <strong>Iso</strong> shows the upper-bound Working Set an isolated single-pod cgroup would carry prior to LRU aging.</div>
            </div>
          </div>
        </div>

        <div class="guide-group">
          <div class="guide-group-title">Collector Binary Tracking &mdash; Userspace pagemap (Columns 5&ndash;8)</div>
          <div class="guide-grid">
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#5</span><strong>Peak &rarr; Final Binary</strong></span>
                <span class="source-tag">/proc/&lt;pid&gt;/maps + pagemap (PM_PRESENT)</span>
              </div>
              <div>Highest sampled resident Collector ELF footprint (<strong>Peak</strong>) vs. resident binary footprint on the final live sample (<strong>Final</strong>), covering clean file pages (<code>.text</code>, <code>.rodata</code>) and private CoW <code>.data</code> pages.</div>
              <div class="guide-tip">A large drop from <code>Peak</code> (or from the <code>max</code> control run) to <code>Final</code> shows how aggressively reclaim shrank the resident binary to make room for the Go heap.</div>
            </div>
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#6</span><strong>Cleanly Evicted</strong></span>
                <span class="source-tag">/proc/&lt;pid&gt;/pagemap state machine</span>
              </div>
              <div>Binary memory faulted in at least once, subsequently evicted, and <strong>never faulted back in</strong> (<code>0</code> refaults) through the end of the run. Parentheses show <code>cleanly_evicted / ever_faulted_binary</code>.</div>
              <div class="guide-tip">Conservative lower bound on one-shot startup/<code>init()</code> bloat dropped from RSS with zero penalty to steady-state execution.</div>
            </div>
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#7</span><strong>Reclaimed Refault %</strong></span>
                <span class="source-tag">ever_refaulted / ever_evicted pages</span>
              </div>
              <div>Percentage of unique evicted binary pages (<code>ever_evicted = cleanly_evicted + ever_refaulted</code>) that had to fault back into RAM at least once. Subtext shows <code>refaulted MiB / unique reclaimed MiB</code>.</div>
              <div class="guide-tip">Near <code>0%</code> proves reclaim trimmed purely dead startup bloat; rising percentages show <code>memory.max</code> forcing eviction into the active runtime working set.</div>
            </div>
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#8</span><strong>Binary Refaults (0&rarr;1 / PFN)</strong></span>
                <span class="source-tag">/proc/&lt;pid&gt;/pagemap (PM_PRESENT, PM_FILE, PFN)</span>
              </div>
              <div>Sampled count of times an evicted 4 KiB Collector binary page had to be paged back into RAM (<code>total = 0&rarr;1 + PFN</code>): <strong>0&rarr;1</strong> caught the page absent (<code>0</code>) then resident (<code>1</code>) on a later poll; <strong>PFN</strong> saw a clean file page stay resident (<code>1</code>) across consecutive polls at a new physical Page Frame Number (sub-poll evict + refault).</div>
              <div class="guide-tip">Caveats: lower bound during heavy thrashing (multiple cycles inside one <code>50 ms</code> poll register once). Small <code>PFN</code> counts on unconstrained runs can occur from kernel page migration/compaction without pressure (verify via <code>Major Faults == 0</code> and <code>PSI == 0 ms</code>).</div>
            </div>
          </div>
        </div>

        <div class="guide-group">
          <div class="guide-group-title">Kernel Reclaim &amp; Pressure &mdash; Cgroup v2 (Columns 9&ndash;11)</div>
          <div class="guide-grid">
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#9</span><strong>Kernel Refaults</strong></span>
                <span class="source-tag">memory.stat: workingset_refault_file</span>
              </div>
              <div>Exact kernel shadow-entry count (<code>mm/workingset.c</code>) of evicted file-backed pages in this cgroup that refaulted within the active/inactive LRU distance window.</div>
              <div class="guide-tip">Never misses rapid sub-poll refault bursts, so it exceeds sampled <code>Binary Refaults</code> under intense thrashing; covers all file pages in the cgroup without ELF section attribution.</div>
            </div>
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#10</span><strong>Major Faults</strong></span>
                <span class="source-tag">memory.stat: pgmajfault</span>
              </div>
              <div>Total cgroup major page faults (<code>VM_FAULT_MAJOR</code>) requiring synchronous filesystem/disk I/O because the target folio was missing from page cache.</div>
              <div class="guide-tip">Sequential startup faults are amortized by kernel mmap readahead (one major fault prefetches adjacent pages resolved via minor faults), whereas random-access thrashing spikes <code>pgmajfault</code> steeply.</div>
            </div>
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#11</span><strong>PSI Full Stall</strong></span>
                <span class="source-tag">memory.pressure: full total=</span>
              </div>
              <div>Cumulative wall-clock milliseconds during which <strong>all</strong> non-idle Collector threads were simultaneously blocked on memory reclaim or disk refault I/O.</div>
              <div class="guide-tip">Direct measure of pipeline impact: near <code>0 ms</code> means reclaim was transparent to Collector latency; hundreds or thousands of ms indicates severe thrashing stalls.</div>
            </div>
          </div>
        </div>

        <div class="guide-group">
          <div class="guide-group-title">Telemetry Sampling (Column 12)</div>
          <div class="guide-grid">
            <div class="guide-item">
              <div class="guide-item-head">
                <span class="guide-col-title"><span class="col-num">#12</span><strong>Samples (Reclaim)</strong></span>
                <span class="source-tag">epoll(memory.events) + 500ms ticker</span>
              </div>
              <div>Total <code>pagemap</code> + cgroup snapshots collected, with parentheses showing how many were woken directly by cgroup reclaim (<code>memory.events</code> <code>EPOLLPRI</code> bursts coalesced at <code>50 ms</code> debounce) vs. the <code>500 ms</code> heartbeat.</div>
              <div class="guide-tip"><code>(0)</code> reclaim samples means <code>memory.max</code> was never reached; hundreds of reclaim samples reveal sustained direct-reclaim activity.</div>
            </div>
          </div>
        </div>
      </div>
    </details>

    <div class="card table-wrap">
      <table id="comparison-table">
        <thead>
          <tr>
            <th tabindex="0" data-tip="Experiment name and directory (run_meta.json). Sorted from Unlimited (max) down to tightest limit; click any row to inspect it below.">Run</th>
            <th tabindex="0" data-tip="Hard cgroup v2 memory.max cap applied before execve (with memory.swap.max=0 by default so reclaim targets file-backed binary pages).">Memory Limit</th>
            <th tabindex="0" data-tip="Automated classifyVerdict() result across OOM events, exit code, binary page refaults, and PSI full memory stall.">Outcome</th>
            <th tabindex="0" data-tip="Simulated Kubelet cgroup v2 working_set_bytes = max(0, memory.current &minus; inactive_file) (Peak &rarr; Final), shown alongside Est. Isolated-Pod WS (+ uncharged RssFile when host page cache owned binary pages) and final Process VmRSS.">Kubelet Working Set</th>
            <th tabindex="0" data-tip="Highest sampled vs. final resident Collector binary memory from /proc/&lt;pid&gt;/pagemap (PM_PRESENT clean file + CoW pages).">Peak &rarr; Final Binary</th>
            <th tabindex="0" data-tip="Collector binary memory faulted in once, evicted by reclaim, and never needed again (0 refaults). Parentheses: % of ever-faulted binary pages (conservative lower bound on startup-only bloat).">Cleanly Evicted</th>
            <th tabindex="0" data-tip="Percentage of unique evicted binary pages that had to fault back in at least once (ever_refaulted / ever_evicted). Near 0% means only dead startup bloat was evicted.">Reclaimed Refault %</th>
            <th tabindex="0" data-tip="Sampled /proc/&lt;pid&gt;/pagemap count of evicted Collector binary pages faulted back in (observed 0&rarr;1 polls plus sub-poll physical PFN changes).">Binary Refaults (0&rarr;1 / PFN)</th>
            <th tabindex="0" data-tip="Exact kernel cgroup counter (memory.stat: workingset_refault_file) of evicted file pages faulted back in within LRU shadow distance.">Kernel Refaults</th>
            <th tabindex="0" data-tip="Total synchronous disk/filesystem major page faults in the cgroup (memory.stat: pgmajfault).">Major Faults</th>
            <th tabindex="0" data-tip="Cumulative wall-clock time all runnable Collector threads stalled on memory reclaim or refault I/O (memory.pressure: full total).">PSI Full Stall</th>
            <th tabindex="0" data-tip="Total telemetry snapshots taken; parentheses show snapshots triggered by epoll(EPOLLPRI) on cgroup memory.events vs. 500ms heartbeat.">Samples (Reclaim)</th>
          </tr>
        </thead>
        <tbody></tbody>
      </table>
    </div>
    <div class="card chart-full">
      <h3>Clean Eviction vs Page-Thrashing Cliff Across Memory Limits</h3>
      <div id="chart-multi-cliff" class="chart-box"></div>
    </div>
    <div class="card chart-full">
      <h3>Simulated Kubelet Working Set Over Time Across Memory Limits (MiB)</h3>
      <div id="chart-multi-ws" class="chart-box"></div>
    </div>
    <div class="chart-grid">
      <div class="card">
        <h3>Cgroup Memory Usage (MiB)</h3>
        <div id="chart-multi-cgmem" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Resident Collector Binary Memory (MiB)</h3>
        <div id="chart-multi-binres" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Cumulative Binary Page Refaults (Pages)</h3>
        <div id="chart-multi-refaults" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Cumulative PSI Full Memory Stall (ms)</h3>
        <div id="chart-multi-psi" class="chart-box"></div>
      </div>
    </div>
  </div>

  <div class="section-block" id="deep-dive-section">
    <h2>Run Deep Dive</h2>
    <div id="run-tabs" class="run-tabs"></div>
    <div id="verdict-banner" class="verdict-banner"></div>
    <div id="kpi-grid" class="kpi-grid"></div>

    <div class="chart-grid">
      <div class="card">
        <h3>Cgroup Memory Composition &amp; Kubelet Working Set vs Limit (MiB)</h3>
        <div id="chart-cg-stack" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Collector Binary Page Lifecycle Over Time (MiB)</h3>
        <div id="chart-bin-lifecycle" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Cumulative Faults, Evictions &amp; Refaults</h3>
        <div id="chart-cum-transitions" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Per-Sample Eviction &amp; Refault Bursts (Pages)</h3>
        <div id="chart-delta-transitions" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Resident Binary Segments (.text / .rodata / .data) &amp; Segment Refaults</h3>
        <div id="chart-segments" class="chart-box"></div>
      </div>
      <div class="card">
        <h3>Memory Pressure Stall (PSI %) &amp; Major Fault Rate</h3>
        <div id="chart-psi-faults" class="chart-box"></div>
      </div>
    </div>

    <div class="card chart-full">
      <h3>ELF Section Final Page Fate &amp; One-Shot Startup Reclaim Analysis</h3>
      <div id="chart-sections" class="chart-box-tall"></div>
      <div id="section-insight-banner" class="insight-banner"></div>
      <div class="table-wrap" style="margin-bottom:0;">
        <table id="section-analysis-table">
          <thead>
            <tr>
              <th tabindex="0" data-tip="ELF section name from debug/elf headers paired with its /proc/&lt;pid&gt;/maps VMA permission flags.">ELF Section</th>
              <th tabindex="0" data-tip="Total virtual memory mapped for this ELF section (and % of total mapped Collector binary).">Mapped</th>
              <th tabindex="0" data-tip="Memory in this section faulted into RAM at least once during startup or runtime (total_pages - never_faulted_pages).">Ever Faulted</th>
              <th tabindex="0" data-tip="Faulted memory evicted by cgroup reclaim that saw 0 refaults for the rest of the run (% of Ever Faulted). Conservative lower bound on one-shot startup bloat.">One-Shot Evicted (Lower Bound)</th>
              <th tabindex="0" data-tip="Distinct binary memory in this section evicted at least once (cleanly_evicted + ever_refaulted).">Unique Reclaimed</th>
              <th tabindex="0" data-tip="Percentage of Unique Reclaimed pages in this section that had to fault back into RAM at least once (ever_refaulted / ever_evicted).">Reclaimed Refault %</th>
              <th tabindex="0" data-tip="Memory remaining resident on the final live snapshot (clean file + private CoW pages, and % of Ever Faulted).">Final Resident</th>
            </tr>
          </thead>
          <tbody></tbody>
        </table>
      </div>
    </div>

    <div class="card chart-full" id="package-analysis-card">
      <h3>Go Package Attribution of <code>.text</code> (Which Code Was Evicted, Refaulted or Kept)</h3>
      <div id="package-insight-banner" class="insight-banner"></div>
      <label class="subtle" style="display:block; margin-bottom:8px;">Sort by
        <select id="package-sort">
          <option value="cleanly_evicted_bytes">One-Shot Evicted</option>
          <option value="faulted_bytes">Ever Faulted</option>
          <option value="refaulted_bytes">Refaulted</option>
          <option value="final_resident_bytes">Final Resident</option>
        </select>
        (top 30)
      </label>
      <div class="table-wrap" style="margin-bottom:0;">
        <table id="package-analysis-table">
          <thead>
            <tr>
              <th tabindex="0" data-tip="Go package of the functions in .text, from .gopclntab function names (debug/gosym). A page shared by several functions splits its bytes between their packages.">Go Package</th>
              <th tabindex="0" data-tip="Number of functions of this package in .text.">Functions</th>
              <th tabindex="0" data-tip="Bytes of .text occupied by this package's functions.">Mapped</th>
              <th tabindex="0" data-tip="Bytes of this package's code on pages faulted into RAM at least once (% of Mapped). A page shared by several functions splits its bytes between them.">Ever Faulted</th>
              <th tabindex="0" data-tip="Bytes on pages evicted and never faulted back (% of Ever Faulted): code that ran (usually init()) and was not needed again.">One-Shot Evicted</th>
              <th tabindex="0" data-tip="Bytes on pages that had to fault back in at least once after eviction: code that was still in use.">Refaulted</th>
              <th tabindex="0" data-tip="Bytes on pages resident on the final live snapshot (% of Ever Faulted).">Final Resident</th>
            </tr>
          </thead>
          <tbody></tbody>
        </table>
      </div>
    </div>

    <div class="card chart-full">
      <h3>Collector Binary Spatial File-Offset Map (Residency, Eviction &amp; Thrashing by Block)</h3>
      <div id="chart-blocks" class="chart-box-tall"></div>
    </div>
  </div>
  <div id="th-tooltip" role="tooltip"></div>

  <script>
    const PAYLOAD = {{.PayloadJSON}};
    const MIB = 1024 * 1024;
    const charts = {};

    function getChart(id) {
      if (!charts[id]) {
        charts[id] = echarts.init(document.getElementById(id), null, { renderer: 'canvas' });
      }
      return charts[id];
    }

    window.addEventListener('resize', () => {
      Object.values(charts).forEach(c => c.resize());
    });

    function fmtMiB(v) { return v.toFixed(2) + ' MiB'; }
    function fmtInt(v) { return Math.round(v).toLocaleString(); }
    function badgeHTML(verdict) {
      const label = (verdict || 'unknown').replace(/_/g, ' ');
      return '<span class="badge badge-' + verdict + '">' + label + '</span>';
    }

    function baseAxisStyle() {
      return {
        axisLine: { lineStyle: { color: '#334155' } },
        axisLabel: { color: '#94a3b8' },
        splitLine: { lineStyle: { color: '#1e293b' } }
      };
    }

    function baseTooltip(trigger = 'axis') {
      return {
        trigger,
        backgroundColor: 'rgba(15, 23, 42, 0.95)',
        borderColor: '#334155',
        textStyle: { color: '#e2e8f0', fontSize: 12 }
      };
    }

    function baseLegend() {
      return {
        top: 0,
        textStyle: { color: '#cbd5e1', fontSize: 11 }
      };
    }

    let selectedIdx = 0;

    function fmtReclaimedRefaultHTML(everEvictedPages, pct) {
      if (!everEvictedPages) {
        return '<span class="subtle">&mdash;</span>';
      }
      const cls = pct < 5 ? 'pct-good' : (pct <= 25 ? 'pct-warn' : 'pct-bad');
      return '<span class="' + cls + '">' + pct.toFixed(1) + '%</span>';
    }

    function initHeaderTooltips() {
      const tip = document.getElementById('th-tooltip');
      if (!tip) return;

      const hideTip = () => { tip.style.display = 'none'; };
      const positionTip = (el) => {
        const text = el.getAttribute('data-tip');
        if (!text) return;
        tip.textContent = text;
        tip.style.display = 'block';
        tip.style.left = '0px';
        tip.style.top = '0px';

        const rect = el.getBoundingClientRect();
        const tipRect = tip.getBoundingClientRect();
        const pad = 12;

        let left = rect.left + (rect.width / 2) - (tipRect.width / 2);
        left = Math.max(pad, Math.min(left, window.innerWidth - tipRect.width - pad));

        let top = rect.top - tipRect.height - 8;
        if (top < pad) {
          top = rect.bottom + 8;
        }
        tip.style.left = Math.round(left) + 'px';
        tip.style.top = Math.round(top) + 'px';
      };

      document.querySelectorAll('th[data-tip]').forEach((th) => {
        th.addEventListener('mouseenter', () => positionTip(th));
        th.addEventListener('mouseleave', hideTip);
        th.addEventListener('focus', () => positionTip(th));
        th.addEventListener('blur', hideTip);
      });
      window.addEventListener('scroll', hideTip, { passive: true });
      window.addEventListener('resize', hideTip);
      document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') hideTip();
      });
    }

    function initDashboard() {
      document.getElementById('generated-at').textContent = 'Generated ' + PAYLOAD.generated;
      document.getElementById('subtitle').textContent = PAYLOAD.multi_run
        ? 'Comparing ' + PAYLOAD.runs.length + ' Collector memory eviction runs'
        : 'Collector binary: ' + PAYLOAD.runs[0].meta.collector_binary;

      initHeaderTooltips();
      if (PAYLOAD.multi_run) {
        document.getElementById('multi-run-section').style.display = 'block';
        renderMultiRunComparison();
      }
      renderRunTabs();
      selectRun(0);
    }

    function renderMultiRunComparison() {
      const tbody = document.querySelector('#comparison-table tbody');
      tbody.innerHTML = '';
      PAYLOAD.summaries.forEach((s, i) => {
        const tr = document.createElement('tr');
        tr.dataset.idx = i;
        const reclaimedCell = s.ever_evicted_pages > 0
          ? fmtReclaimedRefaultHTML(s.ever_evicted_pages, s.reclaimed_refault_percent) +
            '<div class="subtle">' + fmtMiB(s.ever_refaulted_bin_mib) + ' / ' + fmtMiB(s.ever_evicted_bin_mib) + '</div>'
          : '<span class="subtle">&mdash; (0 evicted)</span>';
        const isoDiverged = s.max_uncharged_rss_file_mib >= 1.0;
        const isoLine = 'Iso: ' + fmtMiB(s.peak_est_isolated_ws_mib) + ' &rarr; ' + fmtMiB(s.final_est_isolated_ws_mib);
        const wsCell =
          fmtMiB(s.peak_cg_working_set_mib) + ' &rarr; <strong>' + fmtMiB(s.final_cg_working_set_mib) + '</strong>' +
          '<div class="subtle">' +
          (isoDiverged ? '<span class="pct-warn">' + isoLine + '</span>' : isoLine) +
          ' &middot; VmRSS ' + fmtMiB(s.final_proc_rss_mib) + '</div>';
        tr.innerHTML =
          '<td><strong>' + s.name + '</strong><div class="subtle">' + s.dir + '</div></td>' +
          '<td>' + s.memory_limit_human + '</td>' +
          '<td>' + badgeHTML(s.verdict) + '</td>' +
          '<td>' + wsCell + '</td>' +
          '<td>' + fmtMiB(s.peak_bin_resident_mib) + ' &rarr; <strong>' + fmtMiB(s.final_bin_resident_mib) + '</strong></td>' +
          '<td>' + fmtMiB(s.cleanly_evicted_bin_mib) + ' <span class="subtle">(' + s.clean_reclaim_percent_of_faulted.toFixed(1) + '%)</span></td>' +
          '<td>' + reclaimedCell + '</td>' +
          '<td><strong>' + fmtInt(s.total_refaults) + '</strong> <span class="subtle">(' + fmtInt(s.total_refaults_01) + ' / ' + fmtInt(s.total_refaults_pfn) + ')</span></td>' +
          '<td>' + fmtInt(s.total_cg_workingset_refault_file) + '</td>' +
          '<td>' + fmtInt(s.total_cg_pgmajfault) + '</td>' +
          '<td>' + s.total_psi_full_ms.toFixed(1) + ' ms</td>' +
          '<td>' + fmtInt(s.sample_count) + ' (' + fmtInt(s.reclaim_event_samples) + ')</td>';
        tr.addEventListener('click', () => {
          selectRun(i);
          document.getElementById('deep-dive-section').scrollIntoView({ behavior: 'smooth' });
        });
        tbody.appendChild(tr);
      });

      const labels = PAYLOAD.summaries.map(s => s.memory_limit_human + (s.oom_killed ? ' (OOM)' : ''));
      getChart('chart-multi-cliff').setOption({
        tooltip: {
          ...baseTooltip('axis'),
          formatter: (params) => {
            if (!params || !params.length) return '';
            const idx = params[0].dataIndex;
            const s = PAYLOAD.summaries[idx];
            let html = '<strong>' + s.label + '</strong> &nbsp;' + badgeHTML(s.verdict) + '<br/>';
            params.forEach(p => {
              html += p.marker + p.seriesName + ': <strong>' + p.value + '</strong><br/>';
            });
            if (s.ever_evicted_pages > 0) {
              html += 'Unique reclaimed: <strong>' + fmtMiB(s.ever_evicted_bin_mib) + '</strong> &rarr; ' +
                '<span style="color:#4ade80">' + s.reclaimed_clean_percent.toFixed(1) + '% stayed clean</span> / ' +
                '<span style="color:#f87171">' + s.reclaimed_refault_percent.toFixed(1) + '% refaulted</span>';
            } else {
              html += '<span class="subtle">0 binary pages evicted</span>';
            }
            return html;
          }
        },
        legend: baseLegend(),
        grid: { left: 56, right: 64, top: 44, bottom: 36 },
        xAxis: { type: 'category', data: labels, ...baseAxisStyle() },
        yAxis: [
          { type: 'value', name: 'MiB', nameTextStyle: { color: '#94a3b8' }, ...baseAxisStyle() },
          { type: 'value', name: 'Refaults / Stall ms', nameTextStyle: { color: '#94a3b8' }, ...baseAxisStyle(), splitLine: { show: false } }
        ],
        series: [
          {
            name: 'Final Binary Resident (MiB)',
            type: 'bar',
            stack: 'bin',
            data: PAYLOAD.summaries.map(s => +s.final_bin_resident_mib.toFixed(2)),
            itemStyle: { color: '#38bdf8' }
          },
          {
            name: 'Cleanly Evicted Binary (MiB)',
            type: 'bar',
            stack: 'bin',
            data: PAYLOAD.summaries.map(s => +s.cleanly_evicted_bin_mib.toFixed(2)),
            itemStyle: { color: '#4ade80' }
          },
          {
            name: 'Total Binary Refaults (pages)',
            type: 'line',
            yAxisIndex: 1,
            symbolSize: 8,
            lineStyle: { width: 2.5, color: '#f87171' },
            itemStyle: { color: '#f87171' },
            data: PAYLOAD.summaries.map(s => s.total_refaults)
          },
          {
            name: 'PSI Full Stall (ms)',
            type: 'line',
            yAxisIndex: 1,
            symbolSize: 7,
            lineStyle: { width: 2, type: 'dashed', color: '#fbbf24' },
            itemStyle: { color: '#fbbf24' },
            data: PAYLOAD.summaries.map(s => +s.total_psi_full_ms.toFixed(2))
          }
        ]
      });

      const makeOverlay = (chartId, yName, extractFn) => {
        getChart(chartId).setOption({
          tooltip: baseTooltip('axis'),
          legend: baseLegend(),
          grid: { left: 56, right: 24, top: 48, bottom: 36 },
          xAxis: { type: 'value', name: 's', nameTextStyle: { color: '#94a3b8' }, ...baseAxisStyle() },
          yAxis: { type: 'value', name: yName, nameTextStyle: { color: '#94a3b8' }, ...baseAxisStyle() },
          series: PAYLOAD.runs.map((r, idx) => {
            const ps = r.meta.page_size_bytes || 4096;
            return {
              name: PAYLOAD.summaries[idx].label,
              type: 'line',
              showSymbol: false,
              lineStyle: { width: 2 },
              data: r.samples.map(s => [+(s.elapsed_ms / 1000).toFixed(3), extractFn(s, ps)])
            };
          })
        });
      };

      const sampleCGWS = (s) => Math.max(0, (s.cg_memory_current_bytes || 0) - (s.cg_inactive_file_bytes || 0));
      const sampleIsoWS = (s) => sampleCGWS(s) + Math.max(0, (s.proc_rss_file_bytes || 0) - (s.cg_file_bytes || 0));

      const wsSeries = [];
      PAYLOAD.runs.forEach((r, idx) => {
        const sum = PAYLOAD.summaries[idx];
        wsSeries.push({
          name: sum.label + ' (Cgroup WS)',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 2.2 },
          data: r.samples.map(s => [+(s.elapsed_ms / 1000).toFixed(3), +(sampleCGWS(s) / MIB).toFixed(2)])
        });
        if (sum.max_uncharged_rss_file_mib >= 1.0) {
          wsSeries.push({
            name: sum.label + ' (Est. Isolated WS)',
            type: 'line',
            showSymbol: false,
            lineStyle: { width: 1.8, type: 'dashed' },
            data: r.samples.map(s => [+(s.elapsed_ms / 1000).toFixed(3), +(sampleIsoWS(s) / MIB).toFixed(2)])
          });
        }
      });
      getChart('chart-multi-ws').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 24, top: 48, bottom: 36 },
        xAxis: { type: 'value', name: 's', nameTextStyle: { color: '#94a3b8' }, ...baseAxisStyle() },
        yAxis: { type: 'value', name: 'MiB', nameTextStyle: { color: '#94a3b8' }, ...baseAxisStyle() },
        series: wsSeries
      });

      makeOverlay('chart-multi-cgmem', 'MiB', (s) => +(s.cg_memory_current_bytes / MIB).toFixed(2));
      makeOverlay('chart-multi-binres', 'MiB', (s, ps) => +(((s.bin_present_file_pages + s.bin_present_cow_pages) * ps) / MIB).toFixed(2));
      makeOverlay('chart-multi-refaults', 'Pages', (s) => s.cum_refaults_total);
      makeOverlay('chart-multi-psi', 'ms', (s) => +(s.cg_psi_full_total_us / 1000).toFixed(2));
    }

    function renderRunTabs() {
      const container = document.getElementById('run-tabs');
      if (!PAYLOAD.multi_run) {
        container.style.display = 'none';
        return;
      }
      container.innerHTML = '';
      PAYLOAD.summaries.forEach((s, i) => {
        const btn = document.createElement('button');
        btn.className = 'run-tab';
        btn.innerHTML = s.label + ' &nbsp;' + badgeHTML(s.verdict);
        btn.addEventListener('click', () => selectRun(i));
        container.appendChild(btn);
      });
    }

    function selectRun(idx) {
      selectedIdx = idx;
      document.querySelectorAll('.run-tab').forEach((el, i) => {
        el.classList.toggle('active', i === idx);
      });
      document.querySelectorAll('#comparison-table tbody tr').forEach((el, i) => {
        el.classList.toggle('active-row', i === idx);
      });

      const run = PAYLOAD.runs[idx];
      const sum = PAYLOAD.summaries[idx];
      const meta = run.meta;
      const ps = meta.page_size_bytes || 4096;

      document.getElementById('verdict-banner').innerHTML =
        badgeHTML(sum.verdict) +
        '<div><strong>' + sum.label + '</strong> &mdash; ' + (sum.verdict_reason || '') + '</div>';

      const kpis = [
        {
          label: 'Cgroup Memory Limit',
          val: sum.memory_limit_human,
          sub: 'swap.max=' + meta.swap_max_raw + ' | peak -> final cgroup ' + fmtMiB(sum.peak_cg_memory_mib) + ' -> ' + fmtMiB(sum.final_cg_memory_mib)
        },
        {
          label: 'Kubelet Working Set (Peak -> Final)',
          val: fmtMiB(sum.peak_cg_working_set_mib) + ' -> ' + fmtMiB(sum.final_cg_working_set_mib),
          sub: 'Iso WS: ' + fmtMiB(sum.peak_est_isolated_ws_mib) + ' -> ' + fmtMiB(sum.final_est_isolated_ws_mib) +
            ' | final VmRSS ' + fmtMiB(sum.final_proc_rss_mib) + ' (Inact -' + fmtMiB(sum.final_cg_inactive_file_mib) + ')'
        },
        {
          label: 'Mapped Binary Size',
          val: fmtMiB(sum.binary_mapped_mib),
          sub: fmtInt(meta.total_binary_pages) + ' pages | warm cache ' + meta.warm_cache_pages_before + ' -> ' + meta.warm_cache_pages_after
        },
        {
          label: 'Binary Resident (Peak -> Final)',
          val: fmtMiB(sum.peak_bin_resident_mib) + ' -> ' + fmtMiB(sum.final_bin_resident_mib),
          sub: sum.startup_completed
            ? (fmtMiB(sum.ever_faulted_bin_mib) + ' faulted (' + sum.startup_cold_fault_percent.toFixed(1) + '% by ready @ ' + (sum.startup_readiness_ms / 1000).toFixed(1) + 's)')
            : (sum.startup_timed_out
              ? (fmtMiB(sum.ever_faulted_bin_mib) + ' faulted | did not reach readiness')
              : (fmtMiB(sum.ever_faulted_bin_mib) + ' ever faulted during run'))
        },
        {
          label: 'One-Shot Evicted (Lower Bound)',
          val: fmtMiB(sum.cleanly_evicted_bin_mib) + ' (' + sum.clean_reclaim_percent_of_faulted.toFixed(1) + '%)',
          sub: sum.ever_evicted_pages > 0
            ? (sum.reclaimed_clean_percent.toFixed(1) + '% stayed clean | ' + sum.reclaimed_refault_percent.toFixed(1) + '% of ' + fmtMiB(sum.ever_evicted_bin_mib) + ' reclaimed refaulted')
            : '0 binary pages evicted during run'
        },
        {
          label: 'Binary Page Refaults',
          val: fmtInt(sum.total_refaults) + ' pages',
          sub: '0->1: ' + fmtInt(sum.total_refaults_01) + ' | PFN: ' + fmtInt(sum.total_refaults_pfn) + ' | active thrash: ' + fmtInt(sum.thrashing_pages)
        },
        {
          label: 'Kernel Reclaim & Refaults',
          val: fmtInt(sum.total_cg_events_max) + ' max events',
          sub: 'workingset_refault_file: ' + fmtInt(sum.total_cg_workingset_refault_file) + ' | pgmajfault: ' + fmtInt(sum.total_cg_pgmajfault)
        },
        {
          label: 'PSI Memory Stall',
          val: sum.total_psi_full_ms.toFixed(1) + ' ms full',
          sub: sum.total_psi_some_ms.toFixed(1) + ' ms some | ' + sum.duration_sec.toFixed(1) + 's total run'
        },
        {
          label: 'Process Status & Workload',
          val: meta.oom_killed ? 'OOM Killed' : ('Exit ' + meta.exit_code),
          sub: meta.workload.workload_enabled
            ? ('sent ' + fmtInt(meta.workload.generator_items_sent) + ' / recv ' + fmtInt(meta.workload.backend_items_received))
            : ('samples: ' + fmtInt(meta.sample_count) + ' (' + fmtInt(meta.reclaim_event_samples) + ' reclaim)')
        }
      ];
      document.getElementById('kpi-grid').innerHTML = kpis.map(k =>
        '<div class="kpi-card"><span class="label">' + k.label + '</span><span class="val">' + k.val + '</span><span class="sub">' + k.sub + '</span></div>'
      ).join('');

      renderSingleRunCharts(run, sum, ps);
    }

    function renderSingleRunCharts(run, sum, ps) {
      const samples = run.samples || [];
      const meta = run.meta;
      const t = samples.map(s => +(s.elapsed_ms / 1000).toFixed(3));
      const zipT = (arr) => t.map((x, i) => [x, arr[i]]);
      const pagesToMiB = (p) => +((p * ps) / MIB).toFixed(3);

      // 1. Cgroup Memory Composition & Kubelet Working Set vs Limit
      const cgWSof = (s) => Math.max(0, (s.cg_memory_current_bytes || 0) - (s.cg_inactive_file_bytes || 0));
      const isoWSof = (s) => cgWSof(s) + Math.max(0, (s.proc_rss_file_bytes || 0) - (s.cg_file_bytes || 0));
      const actFileOf = (s) => {
        const act = s.cg_active_file_bytes || 0;
        const inact = s.cg_inactive_file_bytes || 0;
        const f = s.cg_file_bytes || 0;
        // Fallback when synthetic samples specify cg_file_bytes without active/inactive split.
        if (act === 0 && inact === 0 && f > 0) return f;
        return act;
      };
      const limitLine = meta.memory_limit_unlimited ? [] : [{
        yAxis: +(meta.memory_limit_bytes / MIB).toFixed(2),
        name: 'memory.max',
        lineStyle: { color: '#f87171', width: 2, type: 'dashed' },
        label: { formatter: 'memory.max ({c} MiB)', color: '#f87171' }
      }];
      getChart('chart-cg-stack').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 24, top: 52, bottom: 36 },
        xAxis: { type: 'value', name: 's', ...baseAxisStyle() },
        yAxis: { type: 'value', name: 'MiB', ...baseAxisStyle() },
        series: [
          {
            name: 'Anon (Heap/Stacks/CoW)',
            type: 'line', stack: 'cg', areaStyle: { opacity: 0.35 }, showSymbol: false,
            itemStyle: { color: '#c084fc' },
            data: zipT(samples.map(s => +(s.cg_anon_bytes / MIB).toFixed(2)))
          },
          {
            name: 'Kernel',
            type: 'line', stack: 'cg', areaStyle: { opacity: 0.35 }, showSymbol: false,
            itemStyle: { color: '#64748b' },
            data: zipT(samples.map(s => +(s.cg_kernel_bytes / MIB).toFixed(2)))
          },
          {
            name: 'Active File (In Kubelet WS)',
            type: 'line', stack: 'cg', areaStyle: { opacity: 0.42 }, showSymbol: false,
            itemStyle: { color: '#38bdf8' },
            data: zipT(samples.map(s => +(actFileOf(s) / MIB).toFixed(2)))
          },
          {
            name: 'Inactive File (Excl. from WS)',
            type: 'line', stack: 'cg', areaStyle: { opacity: 0.18 }, showSymbol: false,
            itemStyle: { color: '#475569' },
            data: zipT(samples.map(s => +((s.cg_inactive_file_bytes || 0) / MIB).toFixed(2)))
          },
          {
            name: 'Kubelet Working Set (Cgroup)',
            type: 'line', showSymbol: false, lineStyle: { width: 2.5, color: '#34d399' }, itemStyle: { color: '#34d399' },
            data: zipT(samples.map(s => +(cgWSof(s) / MIB).toFixed(2)))
          },
          {
            name: 'Est. Isolated-Pod WS',
            type: 'line', showSymbol: false, lineStyle: { width: 2, type: 'dashed', color: '#22d3ee' }, itemStyle: { color: '#22d3ee' },
            data: zipT(samples.map(s => +(isoWSof(s) / MIB).toFixed(2)))
          },
          {
            name: 'memory.current',
            type: 'line', showSymbol: false, lineStyle: { width: 1.5, color: '#f8fafc' }, itemStyle: { color: '#f8fafc' },
            data: zipT(samples.map(s => +(s.cg_memory_current_bytes / MIB).toFixed(2))),
            markLine: limitLine.length ? { symbol: 'none', data: limitLine } : undefined
          },
          {
            name: 'Process VmRSS',
            type: 'line', showSymbol: false, lineStyle: { width: 1.5, type: 'dotted', color: '#fbbf24' }, itemStyle: { color: '#fbbf24' },
            data: zipT(samples.map(s => +(s.proc_rss_bytes / MIB).toFixed(2)))
          }
        ]
      }, true);

      const readyMarkLine = sum.startup_completed ? {
        symbol: 'none',
        silent: true,
        data: [{
          xAxis: +(sum.startup_readiness_ms / 1000).toFixed(3),
          name: 'Ready',
          lineStyle: { color: '#4ade80', width: 1.5, type: 'dashed' },
          label: { formatter: 'Ready (' + (sum.startup_readiness_ms / 1000).toFixed(1) + 's)', color: '#4ade80', fontSize: 11 }
        }]
      } : undefined;

      // 2. Collector Binary Page Lifecycle Over Time
      getChart('chart-bin-lifecycle').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 24, top: 44, bottom: 36 },
        xAxis: { type: 'value', name: 's', ...baseAxisStyle() },
        yAxis: { type: 'value', name: 'MiB', ...baseAxisStyle() },
        series: [
          {
            name: 'Resident (Clean File)',
            type: 'line', stack: 'life', areaStyle: { opacity: 0.55 }, showSymbol: false,
            itemStyle: { color: '#38bdf8' },
            data: zipT(samples.map(s => pagesToMiB(s.bin_present_file_pages))),
            markLine: readyMarkLine
          },
          {
            name: 'Resident (CoW .data)',
            type: 'line', stack: 'life', areaStyle: { opacity: 0.55 }, showSymbol: false,
            itemStyle: { color: '#c084fc' },
            data: zipT(samples.map(s => pagesToMiB(s.bin_present_cow_pages)))
          },
          {
            name: 'Evicted After Refault',
            type: 'line', stack: 'life', areaStyle: { opacity: 0.55 }, showSymbol: false,
            itemStyle: { color: '#fbbf24' },
            data: zipT(samples.map(s => pagesToMiB(Math.max(0, s.bin_evicted_out_pages - s.bin_cleanly_evicted_pages))))
          },
          {
            name: 'Cleanly Evicted (0 Refaults)',
            type: 'line', stack: 'life', areaStyle: { opacity: 0.55 }, showSymbol: false,
            itemStyle: { color: '#4ade80' },
            data: zipT(samples.map(s => pagesToMiB(s.bin_cleanly_evicted_pages)))
          },
          {
            name: 'Never Faulted',
            type: 'line', stack: 'life', areaStyle: { opacity: 0.2 }, showSymbol: false,
            itemStyle: { color: '#475569' },
            data: zipT(samples.map(s => pagesToMiB(s.bin_never_faulted_pages)))
          }
        ]
      }, true);

      // 3. Cumulative Transitions
      getChart('chart-cum-transitions').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 56, top: 44, bottom: 36 },
        xAxis: { type: 'value', name: 's', ...baseAxisStyle() },
        yAxis: [
          { type: 'value', name: 'Pages', ...baseAxisStyle() },
          { type: 'value', name: 'memory.events:max', ...baseAxisStyle(), splitLine: { show: false } }
        ],
        series: [
          { name: 'Cold Faults', type: 'line', showSymbol: false, itemStyle: { color: '#38bdf8' }, data: zipT(samples.map(s => s.cum_cold_faults)), markLine: readyMarkLine },
          { name: 'Evictions', type: 'line', showSymbol: false, itemStyle: { color: '#4ade80' }, data: zipT(samples.map(s => s.cum_evictions)) },
          { name: 'Refaults (0->1)', type: 'line', showSymbol: false, itemStyle: { color: '#fbbf24' }, data: zipT(samples.map(s => s.cum_refaults_01)) },
          { name: 'Refaults (PFN)', type: 'line', showSymbol: false, itemStyle: { color: '#fb923c' }, data: zipT(samples.map(s => s.cum_refaults_pfn)) },
          { name: 'Refaults (Total)', type: 'line', showSymbol: false, lineStyle: { width: 2.5 }, itemStyle: { color: '#f87171' }, data: zipT(samples.map(s => s.cum_refaults_total)) },
          { name: 'workingset_refault_file', type: 'line', showSymbol: false, lineStyle: { type: 'dashed' }, itemStyle: { color: '#e879f9' }, data: zipT(samples.map(s => s.cg_workingset_refault_file)) },
          { name: 'memory.events:max', type: 'line', yAxisIndex: 1, showSymbol: false, lineStyle: { type: 'dotted' }, itemStyle: { color: '#94a3b8' }, data: zipT(samples.map(s => s.cg_events_max)) }
        ]
      }, true);

      // 4. Interval Delta Transitions
      getChart('chart-delta-transitions').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 24, top: 44, bottom: 36 },
        xAxis: { type: 'value', name: 's', ...baseAxisStyle() },
        yAxis: { type: 'value', name: 'Pages / Sample', ...baseAxisStyle() },
        series: [
          { name: 'Delta Cold Faults', type: 'line', showSymbol: false, itemStyle: { color: '#38bdf8' }, data: zipT(samples.map(s => s.delta_cold_faults)) },
          { name: 'Delta Evictions', type: 'line', showSymbol: false, itemStyle: { color: '#4ade80' }, data: zipT(samples.map(s => s.delta_evictions)) },
          { name: 'Delta Refaults (0->1)', type: 'line', showSymbol: false, itemStyle: { color: '#fbbf24' }, data: zipT(samples.map(s => s.delta_refaults_01)) },
          { name: 'Delta Refaults (PFN)', type: 'line', showSymbol: false, itemStyle: { color: '#f87171' }, data: zipT(samples.map(s => s.delta_refaults_pfn)) }
        ]
      }, true);

      // 5. Segments (.text / .rodata / .data)
      getChart('chart-segments').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 56, top: 44, bottom: 36 },
        xAxis: { type: 'value', name: 's', ...baseAxisStyle() },
        yAxis: [
          { type: 'value', name: 'Resident MiB', ...baseAxisStyle() },
          { type: 'value', name: 'Refaults (Pages)', ...baseAxisStyle(), splitLine: { show: false } }
        ],
        series: [
          { name: 'r-xp (.text) Resident', type: 'line', stack: 'seg', areaStyle: { opacity: 0.35 }, showSymbol: false, itemStyle: { color: '#38bdf8' }, data: zipT(samples.map(s => pagesToMiB(s.rx_present_pages))) },
          { name: 'r--p (.rodata) Resident', type: 'line', stack: 'seg', areaStyle: { opacity: 0.35 }, showSymbol: false, itemStyle: { color: '#2dd4bf' }, data: zipT(samples.map(s => pagesToMiB(s.ro_present_pages))) },
          { name: 'rw-p File Resident', type: 'line', stack: 'seg', areaStyle: { opacity: 0.35 }, showSymbol: false, itemStyle: { color: '#a78bfa' }, data: zipT(samples.map(s => pagesToMiB(s.rw_present_file_pages))) },
          { name: 'rw-p CoW Resident', type: 'line', stack: 'seg', areaStyle: { opacity: 0.35 }, showSymbol: false, itemStyle: { color: '#f472b6' }, data: zipT(samples.map(s => pagesToMiB(s.rw_present_cow_pages))) },
          { name: 'r-xp Refaults', type: 'line', yAxisIndex: 1, showSymbol: false, lineStyle: { type: 'dashed' }, itemStyle: { color: '#f87171' }, data: zipT(samples.map(s => s.rx_cum_refaults)) },
          { name: 'r--p Refaults', type: 'line', yAxisIndex: 1, showSymbol: false, lineStyle: { type: 'dashed' }, itemStyle: { color: '#fbbf24' }, data: zipT(samples.map(s => s.ro_cum_refaults)) }
        ]
      }, true);

      // 6. PSI Stall % & Major Page Fault Rate
      const psiSomePct = [0];
      const psiFullPct = [0];
      const majFaultRate = [0];
      for (let i = 1; i < samples.length; i++) {
        const dtMs = Math.max(1, samples[i].elapsed_ms - samples[i - 1].elapsed_ms);
        const dtUs = dtMs * 1000;
        const dSome = Math.max(0, samples[i].cg_psi_some_total_us - samples[i - 1].cg_psi_some_total_us);
        const dFull = Math.max(0, samples[i].cg_psi_full_total_us - samples[i - 1].cg_psi_full_total_us);
        const dMaj = Math.max(0, samples[i].cg_pgmajfault - samples[i - 1].cg_pgmajfault);
        psiSomePct.push(Math.min(100, +(100 * dSome / dtUs).toFixed(2)));
        psiFullPct.push(Math.min(100, +(100 * dFull / dtUs).toFixed(2)));
        majFaultRate.push(+(1000 * dMaj / dtMs).toFixed(2));
      }
      getChart('chart-psi-faults').setOption({
        tooltip: baseTooltip('axis'),
        legend: baseLegend(),
        grid: { left: 56, right: 56, top: 44, bottom: 36 },
        xAxis: { type: 'value', name: 's', ...baseAxisStyle() },
        yAxis: [
          { type: 'value', name: 'PSI Stall %', max: (v) => Math.max(5, Math.ceil(v.max)), ...baseAxisStyle() },
          { type: 'value', name: 'pgmajfault / s', ...baseAxisStyle(), splitLine: { show: false } }
        ],
        series: [
          { name: 'PSI Some Stall %', type: 'line', showSymbol: false, areaStyle: { opacity: 0.2 }, itemStyle: { color: '#fbbf24' }, data: zipT(psiSomePct) },
          { name: 'PSI Full Stall %', type: 'line', showSymbol: false, areaStyle: { opacity: 0.3 }, itemStyle: { color: '#f87171' }, data: zipT(psiFullPct) },
          { name: 'Major Faults / s', type: 'line', yAxisIndex: 1, showSymbol: false, lineStyle: { type: 'dashed' }, itemStyle: { color: '#38bdf8' }, data: zipT(majFaultRate) }
        ]
      }, true);

      // 7. ELF Sections Stacked Bar + Section Reclaim Analysis
      const secs = (run.sections || []).slice().reverse();
      getChart('chart-sections').setOption({
        tooltip: {
          ...baseTooltip('axis'),
          axisPointer: { type: 'shadow' },
          formatter: (params) => {
            if (!params || !params.length) return '';
            const s = secs[params[0].dataIndex];
            const faultedPg = Math.max(0, s.total_pages - s.never_faulted_pages);
            const everRefPg = s.evicted_after_refault_pages + s.resident_after_refault_pages;
            const everEvPg = s.cleanly_evicted_pages + everRefPg;
            const oneShotPct = faultedPg > 0 ? (100 * s.cleanly_evicted_pages / faultedPg).toFixed(1) : '0.0';
            const recRefPct = everEvPg > 0 ? (100 * everRefPg / everEvPg).toFixed(1) : '0.0';
            let html = '<strong>' + s.section_name + ' (' + s.vma_perms + ')</strong><br/>';
            html += 'Total mapped: ' + pagesToMiB(s.total_pages).toFixed(2) + ' MiB | Ever faulted: <strong>' + pagesToMiB(faultedPg).toFixed(2) + ' MiB</strong><br/>';
            params.forEach(p => {
              html += p.marker + p.seriesName + ': <strong>' + p.value + ' MiB</strong><br/>';
            });
            html += 'One-shot cleanly evicted: <strong>' + oneShotPct + '%</strong> of faulted | ';
            html += everEvPg > 0
              ? ('Reclaimed refault rate: <strong>' + recRefPct + '%</strong> (' + fmtInt(s.total_refaults) + ' events)')
              : '0 pages evicted';
            return html;
          }
        },
        legend: baseLegend(),
        grid: { left: 160, right: 32, top: 44, bottom: 36 },
        xAxis: { type: 'value', name: 'MiB', ...baseAxisStyle() },
        yAxis: { type: 'category', data: secs.map(s => s.section_name + ' (' + s.vma_perms + ')'), ...baseAxisStyle() },
        series: [
          { name: 'Present (File)', type: 'bar', stack: 'sec', itemStyle: { color: '#38bdf8' }, data: secs.map(s => pagesToMiB(s.present_final_pages)) },
          { name: 'Present (CoW)', type: 'bar', stack: 'sec', itemStyle: { color: '#c084fc' }, data: secs.map(s => pagesToMiB(s.present_cow_final_pages)) },
          { name: 'Evicted After Refault', type: 'bar', stack: 'sec', itemStyle: { color: '#fbbf24' }, data: secs.map(s => pagesToMiB(s.evicted_after_refault_pages)) },
          { name: 'Cleanly Evicted (0 Refaults)', type: 'bar', stack: 'sec', itemStyle: { color: '#4ade80' }, data: secs.map(s => pagesToMiB(s.cleanly_evicted_pages)) },
          { name: 'Never Faulted', type: 'bar', stack: 'sec', itemStyle: { color: '#334155' }, data: secs.map(s => pagesToMiB(s.never_faulted_pages)) }
        ]
      }, true);
      renderSectionAnalysis(run, sum, ps);
      renderPackageAnalysis(sum.index);

      // 8. Spatial Binary File-Offset Map
      const blks = run.blocks || [];
      getChart('chart-blocks').setOption({
        tooltip: {
          ...baseTooltip('axis'),
          axisPointer: { type: 'shadow' },
          formatter: (params) => {
            if (!params || !params.length) return '';
            const b = blks[params[0].dataIndex];
            const startM = (b.file_offset_start_bytes / MIB).toFixed(2);
            const endM = (b.file_offset_end_bytes / MIB).toFixed(2);
            let html = '<strong>Block #' + b.block_index + ' [' + startM + ' - ' + endM + ' MiB]</strong><br/>';
            html += 'Section: <strong>' + b.primary_section + '</strong> (' + b.vma_perms + ')<br/>';
            html += 'Resident file: ' + b.present_final_pages + ' | CoW: ' + b.present_cow_final_pages +
              ' | Cleanly evicted: ' + b.cleanly_evicted_pages + ' | Never faulted: ' + b.never_faulted_pages + '<br/>';
            html += 'Refaulted pages: <strong>' + b.ever_refaulted_pages + '</strong> (active thrash: ' + b.thrashing_pages +
              ', total refaults: ' + b.total_refaults + ')';
            return html;
          }
        },
        legend: baseLegend(),
        grid: { left: 56, right: 56, top: 44, bottom: 56 },
        dataZoom: [{ type: 'inside' }, { type: 'slider', bottom: 8, height: 20 }],
        xAxis: {
          type: 'category',
          name: 'Offset (MiB)',
          data: blks.map(b => (b.file_offset_start_bytes / MIB).toFixed(1)),
          ...baseAxisStyle()
        },
        yAxis: [
          { type: 'value', name: 'Pages / Block', ...baseAxisStyle() },
          { type: 'value', name: 'Refaults', ...baseAxisStyle(), splitLine: { show: false } }
        ],
        series: [
          { name: 'Present (File)', type: 'bar', stack: 'blk', barWidth: '100%', itemStyle: { color: '#38bdf8' }, data: blks.map(b => b.present_final_pages) },
          { name: 'Present (CoW)', type: 'bar', stack: 'blk', barWidth: '100%', itemStyle: { color: '#c084fc' }, data: blks.map(b => b.present_cow_final_pages) },
          {
            name: 'Evicted After Refault',
            type: 'bar', stack: 'blk', barWidth: '100%', itemStyle: { color: '#fbbf24' },
            data: blks.map(b => Math.max(0, b.total_pages - b.never_faulted_pages - b.present_final_pages - b.present_cow_final_pages - b.cleanly_evicted_pages))
          },
          { name: 'Cleanly Evicted', type: 'bar', stack: 'blk', barWidth: '100%', itemStyle: { color: '#4ade80' }, data: blks.map(b => b.cleanly_evicted_pages) },
          { name: 'Never Faulted', type: 'bar', stack: 'blk', barWidth: '100%', itemStyle: { color: '#1e293b' }, data: blks.map(b => b.never_faulted_pages) },
          { name: 'Block Refaults', type: 'line', yAxisIndex: 1, showSymbol: false, lineStyle: { width: 1.5, color: '#f87171' }, itemStyle: { color: '#f87171' }, data: blks.map(b => b.total_refaults) }
        ]
      }, true);
    }

    function renderSectionAnalysis(run, sum, ps) {
      const meta = run.meta;
      const rawSecs = run.sections || [];
      const rows = rawSecs.map(s => {
        const faultedPages = Math.max(0, s.total_pages - s.never_faulted_pages);
        const finalResPages = s.present_final_pages + s.present_cow_final_pages;
        const everRefaultedPages = s.evicted_after_refault_pages + s.resident_after_refault_pages;
        const everEvictedPages = s.cleanly_evicted_pages + everRefaultedPages;
        return {
          name: s.section_name,
          perms: s.vma_perms,
          totalPages: s.total_pages,
          faultedPages,
          cleanlyEvictedPages: s.cleanly_evicted_pages,
          everRefaultedPages,
          everEvictedPages,
          finalResPages,
          totalRefaults: s.total_refaults,
          mappedPctOfBin: meta.total_binary_pages > 0 ? (100 * s.total_pages / meta.total_binary_pages) : 0,
          faultedPctOfSec: s.total_pages > 0 ? (100 * faultedPages / s.total_pages) : 0,
          oneShotPctOfFaulted: faultedPages > 0 ? (100 * s.cleanly_evicted_pages / faultedPages) : 0,
          reclaimedRefaultPct: everEvictedPages > 0 ? (100 * everRefaultedPages / everEvictedPages) : 0,
          finalPctOfFaulted: faultedPages > 0 ? (100 * finalResPages / faultedPages) : 0
        };
      });

      const sorted = rows.slice().sort((a, b) => (b.faultedPages - a.faultedPages) || (b.totalPages - a.totalPages));
      const toMiBStr = (pages) => fmtMiB((pages * ps) / MIB);

      const tbody = document.querySelector('#section-analysis-table tbody');
      tbody.innerHTML = sorted.map(r => {
        const oneShotCls = r.oneShotPctOfFaulted >= 50 ? 'pct-good' : '';
        const recCell = r.everEvictedPages > 0
          ? fmtReclaimedRefaultHTML(r.everEvictedPages, r.reclaimedRefaultPct) +
            ' <span class="subtle">(' + fmtInt(r.totalRefaults) + ' events)</span>'
          : '<span class="subtle">&mdash;</span>';
        return '<tr>' +
          '<td><strong>' + r.name + '</strong><span class="perm-pill">' + r.perms + '</span></td>' +
          '<td>' + toMiBStr(r.totalPages) + ' <span class="subtle">(' + r.mappedPctOfBin.toFixed(1) + '%)</span></td>' +
          '<td>' + toMiBStr(r.faultedPages) + ' <span class="subtle">(' + r.faultedPctOfSec.toFixed(1) + '%)</span></td>' +
          '<td><strong class="' + oneShotCls + '">' + toMiBStr(r.cleanlyEvictedPages) + '</strong> <span class="subtle">(' + r.oneShotPctOfFaulted.toFixed(1) + '%)</span></td>' +
          '<td>' + toMiBStr(r.everEvictedPages) + '</td>' +
          '<td>' + recCell + '</td>' +
          '<td><strong>' + toMiBStr(r.finalResPages) + '</strong> <span class="subtle">(' + r.finalPctOfFaulted.toFixed(1) + '%)</span></td>' +
          '</tr>';
      }).join('');

      const bullets = [];
      if (sum.startup_completed) {
        const startupMiB = (sum.startup_cold_faults * ps) / MIB;
        const runtimeMiB = (sum.runtime_cold_faults * ps) / MIB;
        const runtimeSec = Math.max(0, sum.duration_sec - (sum.startup_readiness_ms / 1000));
        bullets.push(
          '<li><strong>Startup cold-fault spike:</strong> <strong>' + sum.startup_cold_fault_percent.toFixed(1) + '%</strong> of all faulted binary memory (' +
          fmtMiB(startupMiB) + ' of ' + fmtMiB(sum.ever_faulted_bin_mib) + ') paged in by Collector readiness at <strong>' +
          (sum.startup_readiness_ms / 1000).toFixed(2) + 's</strong> (first ready sample @ ' + (sum.startup_sample_ms / 1000).toFixed(2) +
          's); only <strong>' + fmtMiB(runtimeMiB) + '</strong> cold-faulted across the remaining ' + runtimeSec.toFixed(1) + 's of runtime.</li>'
        );
      } else if (sum.startup_timed_out) {
        bullets.push(
          '<li><strong>Startup status:</strong> Collector did not reach readiness before termination (' +
          fmtMiB(sum.ever_faulted_bin_mib) + ' of binary memory faulted during startup attempt).</li>'
        );
      }

      if (sum.ever_evicted_pages > 0) {
        bullets.push(
          '<li><strong>Unique reclaimed binary:</strong> Kernel evicted <strong>' + fmtMiB(sum.ever_evicted_bin_mib) +
          '</strong> of unique binary memory &mdash; <strong class="pct-good">' + sum.reclaimed_clean_percent.toFixed(1) + '% (' +
          fmtMiB(sum.cleanly_evicted_bin_mib) + ')</strong> stayed evicted with 0 refaults, while <strong>' +
          sum.reclaimed_refault_percent.toFixed(1) + '% (' + fmtMiB(sum.ever_refaulted_bin_mib) + ')</strong> had to refault at least once.</li>'
        );
      } else {
        bullets.push(
          '<li><strong>No binary reclaim:</strong> Memory remained within limit, so 0 Collector binary pages were evicted during this run.</li>'
        );
      }

      const textRow = rows.find(r => r.name === '.text');
      const roRow = rows.find(r => r.name === '.rodata');
      if (textRow && textRow.everEvictedPages > 0) {
        let secLine = '<li><strong>Code vs. read-only metadata:</strong> <code>.text</code> shed <strong>' +
          toMiBStr(textRow.cleanlyEvictedPages) + ' (' + textRow.oneShotPctOfFaulted.toFixed(1) +
          '% of faulted code)</strong> with 0 refaults (' + textRow.reclaimedRefaultPct.toFixed(1) +
          '% of reclaimed <code>.text</code> pages refaulted), shrinking resident code down to <strong>' +
          toMiBStr(textRow.finalResPages) + '</strong>';
        if (roRow && roRow.everEvictedPages > 0) {
          secLine += ' &mdash; whereas <code>.rodata</code> evicted <strong>' + roRow.oneShotPctOfFaulted.toFixed(1) +
            '%</strong> of faulted pages cleanly and saw <strong>' + roRow.reclaimedRefaultPct.toFixed(1) +
            '%</strong> of its reclaimed pages refault (Go runtime/GC pointer bitmaps and type descriptors keep <code>.rodata</code> hotter than one-shot package <code>init()</code> code).';
        } else {
          secLine += '.';
        }
        secLine += '</li>';
        bullets.push(secLine);
      }

      document.getElementById('section-insight-banner').innerHTML =
        '<ul>' + bullets.join('') + '</ul>' +
        '<div class="caveat">Note: <em>One-Shot Evicted (% of Faulted)</em> is a conservative lower bound on startup-only bloat &mdash; startup symbols sharing a 4 KiB page with active steady-state code/data, or pages the LRU never needed to evict, cannot prove 0 refaults.</div>';
    }

    function renderPackageAnalysis(runIdx) {
      const card = document.getElementById('package-analysis-card');
      const pkgs = (PAYLOAD.packages || [])[runIdx]; // .text rows only (see packages.go)
      if (!pkgs || !pkgs.length) {
        card.style.display = 'none';
        return;
      }
      card.style.display = '';
      const mb = (v) => fmtMiB(v / MIB);
      const pct = (n, d) => d > 0 ? (100 * n / d).toFixed(1) + '%' : '&mdash;';
      const esc = (s) => s.replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
      const sum = (k) => pkgs.reduce((s, p) => s + p[k], 0);
      const sortSel = document.getElementById('package-sort');
      sortSel.onchange = () => renderPackageAnalysis(runIdx);

      const key = sortSel.value;
      const sorted = pkgs.slice().sort((a, b) => (b[key] - a[key]) || (b.faulted_bytes - a.faulted_bytes));
      document.querySelector('#package-analysis-table tbody').innerHTML = sorted.slice(0, 30).map(p =>
        '<tr>' +
        '<td>' + esc(p.package) + '</td>' +
        '<td>' + fmtInt(p.functions) + '</td>' +
        '<td>' + mb(p.mapped_bytes) + '</td>' +
        '<td>' + mb(p.faulted_bytes) + ' <span class="subtle">(' + pct(p.faulted_bytes, p.mapped_bytes) + ')</span></td>' +
        '<td><strong class="' + (p.cleanly_evicted_bytes > 0 ? 'pct-good' : '') + '">' + mb(p.cleanly_evicted_bytes) + '</strong> <span class="subtle">(' + pct(p.cleanly_evicted_bytes, p.faulted_bytes) + ')</span></td>' +
        '<td>' + (p.refaulted_bytes > 0 ? '<span class="pct-warn">' + mb(p.refaulted_bytes) + '</span>' : '<span class="subtle">&mdash;</span>') + '</td>' +
        '<td><strong>' + mb(p.final_resident_bytes) + '</strong> <span class="subtle">(' + pct(p.final_resident_bytes, p.faulted_bytes) + ')</span></td>' +
        '</tr>'
      ).join('');

      const faulted = sum('faulted_bytes');
      const clean = sum('cleanly_evicted_bytes');
      const touched = pkgs.filter(p => p.faulted_bytes > 0).length;
      document.getElementById('package-insight-banner').innerHTML =
        '<ul>' +
        '<li><strong>' + fmtInt(touched) + '</strong> of ' + fmtInt(pkgs.length) + ' Go packages had code loaded into RAM (' + mb(faulted) + ' of ' + mb(sum('mapped_bytes')) + ' of <code>.text</code>).</li>' +
        '<li><strong class="pct-good">' + mb(clean) + '</strong> (' + pct(clean, faulted) + ' of loaded code) was evicted and never needed again; ' +
        '<strong>' + mb(sum('refaulted_bytes')) + '</strong> had to refault.</li>' +
        '</ul>' +
        '<div class="caveat">Bytes are split by overlap when several functions share a 4 KiB page, so one hot function keeps its page neighbours resident. "Loaded" includes neighbouring pages the kernel maps around each fault, not only code that ran.</div>';
    }

    initDashboard();
  </script>
</body>
</html>
`
