(() => {
  "use strict";

  // PageVisualCode constants synchronized with internal/pagemap/tracker.go
  const CODE_NEVER_FAULTED = 0;
  const CODE_COLD_FAULT_FILE = 1;
  const CODE_COLD_FAULT_COW = 2;
  const CODE_PROMOTED_COW = 3;
  const CODE_EVICTED_CLEAN = 4;
  const CODE_EVICTED_REFAULTED = 5;
  const CODE_EVICTED_THRASHING = 6;
  const CODE_REFAULT_FILE_ONCE = 7;
  const CODE_REFAULT_FILE_THRASH = 8;
  const CODE_REFAULT_COW = 9;
  const CODE_SWAPPED_OUT = 10;

  const CHECKPOINT_STRIDE = 64;
  const LIVE_FLASH_MS = 380;

  // Detect endianness for fast Uint32Array ImageData pixel writes.
  const IS_LITTLE_ENDIAN = (() => {
    const b = new ArrayBuffer(4);
    new Uint32Array(b)[0] = 0x0a0b0c0d;
    return new Uint8Array(b)[0] === 0x0d;
  })();

  function packColor(r, g, b, a = 255) {
    if (IS_LITTLE_ENDIAN) {
      return ((a << 24) | (b << 16) | (g << 8) | r) >>> 0;
    }
    return ((r << 24) | (g << 16) | (b << 8) | a) >>> 0;
  }

  function lerpColor(rgbA, rgbB, t) {
    const r = Math.round(rgbA[0] + (rgbB[0] - rgbA[0]) * t);
    const g = Math.round(rgbA[1] + (rgbB[1] - rgbA[1]) * t);
    const b = Math.round(rgbA[2] + (rgbB[2] - rgbA[2]) * t);
    return packColor(r, g, b, 255);
  }

  // Curated RGB tuples matching app.css (zero purple)
  const BG_STAGE_RGB = [11, 14, 18];
  const BG_BAND_EVEN_RGB = [16, 20, 26];
  const BG_BAND_ODD_RGB = [13, 17, 22];

  const RESTING_RGB = [
    [31, 37, 45],    // 0 NeverFaulted
    [41, 151, 232],  // 1 ColdFaultFile -> Resident clean file
    [242, 184, 48],  // 2 ColdFaultCoW  -> Resident dirty CoW
    [242, 184, 48],  // 3 PromotedCoW   -> Resident dirty CoW
    [36, 98, 80],    // 4 EvictedClean  -> Safely reclaimed (0 refaults)
    [96, 58, 34],    // 5 EvictedRefaulted
    [98, 40, 34],    // 6 EvictedThrashing
    [232, 122, 36],  // 7 RefaultFileOnce -> Resident after settled refault
    [214, 62, 42],   // 8 RefaultFileThrash -> Resident actively thrashing (>=4 rapid refaults)
    [224, 134, 44],  // 9 RefaultCoW
    [102, 114, 62],  // 10 SwappedOut
  ];

  const FLASH_RGB = [
    [31, 37, 45],    // 0 NeverFaulted
    [135, 242, 255], // 1 ColdFaultFile flash (ice cyan)
    [255, 234, 138], // 2 ColdFaultCoW flash (bright gold)
    [255, 226, 118], // 3 PromotedCoW flash
    [92, 242, 186],  // 4 EvictedClean flash (bright seafoam)
    [245, 172, 108], // 5 EvictedRefaulted flash
    [255, 128, 106], // 6 EvictedThrashing flash
    [255, 166, 76],  // 7 RefaultFileOnce flash (bright tangerine)
    [255, 82, 58],   // 8 RefaultFileThrash flash (hot vermilion)
    [255, 176, 84],  // 9 RefaultCoW flash
    [186, 206, 112], // 10 SwappedOut flash
  ];

  const RESTING_U32 = new Uint32Array(RESTING_RGB.map((c) => packColor(c[0], c[1], c[2])));
  const BG_STAGE_U32 = packColor(BG_STAGE_RGB[0], BG_STAGE_RGB[1], BG_STAGE_RGB[2]);
  const BG_BAND_EVEN_U32 = packColor(BG_BAND_EVEN_RGB[0], BG_BAND_EVEN_RGB[1], BG_BAND_EVEN_RGB[2]);
  const BG_BAND_ODD_U32 = packColor(BG_BAND_ODD_RGB[0], BG_BAND_ODD_RGB[1], BG_BAND_ODD_RGB[2]);

  const STATE_LABELS = [
    "Never faulted (cold on disk)",
    "Resident clean file cache (never evicted)",
    "Resident CoW dirty (anonymous page)",
    "Resident CoW dirty (promoted from file page)",
    "Cleanly evicted (0 refaults — shed bloat)",
    "Evicted out (after settled prior refault)",
    "Evicted out (actively thrashing, ≥4 rapid refaults)",
    "Resident file cache (reloaded & settled)",
    "Resident file cache (actively thrashing, ≥4 rapid refaults)",
    "Resident CoW (refaulted)",
    "Swapped out",
  ];

  // Application State
  const state = {
    config: null,
    runId: 0,
    status: "idle", // idle, starting, running, stopping, finished
    memoryLimitRaw: "",
    memoryLimitHuman: "—",
    memoryLimitUnlimited: false,
    memoryLimitBytes: 0,
    initialLimitHuman: "—",
    initialLimitUnlimited: false,
    initialLimitBytes: 0,
    swapMaxRaw: "0",
    configuredDuration: "",
    durationMsTarget: 0,
    resultsSubdir: "",
    startupReadinessMs: 0,
    layout: null, // Active run layout or config.preview_layout
    isPreviewLayout: true,
    markers: [],
    finalMeta: null,
    sections: [],
    collectorLogTail: "",
    runError: "",

    // Frame store & checkpoint scrubber
    totalPages: 0,
    frames: [],       // { seq, row, deltas: Uint32Array, recvPerfMs }
    checkpoints: [],  // Uint8Array[] every CHECKPOINT_STRIDE frames
    livePages: new Uint8Array(0),
    scrubPages: new Uint8Array(0),
    scrubComputedIdx: -1,

    // Playback state
    followLive: true,
    viewIdx: -1,
    isReplaying: false,
    replaySpeed: 0.5,
    replayAccumElapsedMs: 0,
    lastRafPerfMs: 0,

    // View mode & precomputed Canvas geometry
    viewMode: "pages", // "pages" | "blocks"
    geomDirty: true,
    canvasDirty: true,
    timelineDirty: true,
    geom: null,
    imgData: null,
    imgU32: null,
    bgU32: null,

    // Arena peak tracker for smooth bar scaling
    peakArenaBytes: 0,
    resyncInFlight: false,
  };

  // DOM References
  const dom = {
    statusBadge: document.getElementById("run-status-badge"),
    statusText: document.getElementById("run-status-text"),
    metaCfgName: document.getElementById("meta-cfg-name"),
    metaBinName: document.getElementById("meta-bin-name"),
    metaSaveMode: document.getElementById("meta-save-mode"),

    launchForm: document.getElementById("run-launch-form"),
    inputLimit: document.getElementById("input-memory-limit"),
    inputDuration: document.getElementById("input-duration"),
    inputSwap: document.getElementById("input-swap-max"),
    btnStart: document.getElementById("btn-start-run"),
    btnStop: document.getElementById("btn-stop-run"),
    limitChips: Array.from(document.querySelectorAll("#preset-limit-chips .chip-btn")),
    durChips: Array.from(document.querySelectorAll("#preset-duration-chips .chip-btn")),

    livePanel: document.getElementById("live-controls-panel"),
    inputLiveLimit: document.getElementById("input-live-limit"),
    btnApplyLiveLimit: document.getElementById("btn-apply-live-limit"),
    liveStepBtns: Array.from(document.querySelectorAll(".live-step-btn")),
    oomIndicator: document.getElementById("oom-risk-indicator"),
    reclaimBtns: Array.from(document.querySelectorAll(".reclaim-btn")),
    liveToast: document.getElementById("live-action-toast"),

    arenaScaleLabel: document.getElementById("arena-scale-label"),
    legArenaAnon: document.getElementById("leg-arena-anon"),
    legArenaKernel: document.getElementById("leg-arena-kernel"),
    legArenaActiveFile: document.getElementById("leg-arena-active-file"),
    legArenaInactiveFile: document.getElementById("leg-arena-inactive-file"),
    legArenaWs: document.getElementById("leg-arena-ws"),
    legArenaLimit: document.getElementById("leg-arena-limit"),
    segAnon: document.getElementById("arena-seg-anon"),
    segKernel: document.getElementById("arena-seg-kernel"),
    segActiveFile: document.getElementById("arena-seg-active-file"),
    segInactiveFile: document.getElementById("arena-seg-inactive-file"),
    wsLine: document.getElementById("arena-ws-line"),
    wsTag: document.getElementById("arena-ws-tag"),
    isoWsLine: document.getElementById("arena-iso-ws-line"),
    isoWsTag: document.getElementById("arena-iso-ws-tag"),
    limitLine: document.getElementById("arena-limit-line"),
    limitTag: document.getElementById("arena-limit-tag"),

    kpiCgCur: document.getElementById("kpi-cg-cur"),
    kpiCgSub: document.getElementById("kpi-cg-sub"),
    kpiCgWs: document.getElementById("kpi-cg-ws"),
    kpiCgWsSub: document.getElementById("kpi-cg-ws-sub"),
    kpiBinRes: document.getElementById("kpi-bin-res"),
    kpiBinSub: document.getElementById("kpi-bin-sub"),
    kpiCleanEvict: document.getElementById("kpi-clean-evict"),
    kpiCleanSub: document.getElementById("kpi-clean-sub"),
    kpiRefaultPct: document.getElementById("kpi-refault-pct"),
    kpiRefaultSub: document.getElementById("kpi-refault-sub"),
    kpiTickDeltas: document.getElementById("kpi-tick-deltas"),
    kpiCumSub: document.getElementById("kpi-cum-sub"),
    kpiPsi: document.getElementById("kpi-psi"),
    kpiEventsSub: document.getElementById("kpi-events-sub"),

    vizSummary: document.getElementById("viz-summary-caption"),
    btnModePages: document.getElementById("btn-mode-pages"),
    btnModeBlocks: document.getElementById("btn-mode-blocks"),
    cntNever: document.getElementById("cnt-never"),
    cntFlashCold: document.getElementById("cnt-flash-cold"),
    cntResFile: document.getElementById("cnt-res-file"),
    cntResCow: document.getElementById("cnt-res-cow"),
    cntFlashEvict: document.getElementById("cnt-flash-evict"),
    cntEvictClean: document.getElementById("cnt-evict-clean"),
    cntRefault1: document.getElementById("cnt-refault-1"),
    cntThrash: document.getElementById("cnt-thrash"),

    canvasStage: document.getElementById("canvas-stage"),
    canvas: document.getElementById("page-map-canvas"),
    tooltip: document.getElementById("map-hover-tooltip"),

    btnFollowLive: document.getElementById("btn-follow-live"),
    btnPlayPause: document.getElementById("btn-play-pause"),
    btnStepPrev: document.getElementById("btn-step-prev"),
    btnStepNext: document.getElementById("btn-step-next"),
    selectSpeed: document.getElementById("select-replay-speed"),
    sparkKeyIso: document.getElementById("spark-key-iso"),
    frameLabel: document.getElementById("timeline-frame-label"),
    sparkCanvas: document.getElementById("timeline-spark-canvas"),
    slider: document.getElementById("timeline-slider"),

    diagDrawer: document.getElementById("run-diagnostics-drawer"),
    diagHint: document.getElementById("diag-summary-hint"),
    diagVerdict: document.getElementById("diag-verdict-banner"),
    diagSecWrap: document.getElementById("diag-sections-wrap"),
    diagSecTbody: document.getElementById("diag-sections-tbody"),
    diagLog: document.getElementById("diag-collector-log"),
  };

  // Formatting utilities
  function fmtMiB(bytes) {
    if (!bytes || bytes <= 0) return "0.0 MiB";
    const mib = bytes / (1024 * 1024);
    if (mib >= 1024) return (mib / 1024).toFixed(2) + " GiB";
    if (mib < 1) return (bytes / 1024).toFixed(0) + " KiB";
    return mib.toFixed(1) + " MiB";
  }

  function fmtInt(n) {
    return (n || 0).toLocaleString("en-US");
  }

  function fmtHex(n) {
    return "0x" + (n >>> 0 ? n.toString(16) : Math.floor(n).toString(16)).padStart(8, "0");
  }

  function basename(p) {
    if (!p) return "—";
    const idx = p.lastIndexOf("/");
    return idx >= 0 ? p.slice(idx + 1) : p;
  }

  // Exact Kubernetes cgroup v2 container working_set_bytes = max(0, memory.current - inactive_file).
  function calcCgroupWS(row) {
    if (!row) return 0;
    const cur = row.cg_memory_current_bytes || 0;
    const inact = row.cg_inactive_file_bytes || 0;
    return cur > inact ? cur - inact : 0;
  }

  function calcUnchargedRSSFile(row) {
    if (!row) return 0;
    const rf = row.proc_rss_file_bytes || 0;
    const cf = row.cg_file_bytes || 0;
    return rf > cf ? rf - cf : 0;
  }

  function calcEstIsolatedWS(row) {
    if (!row) return 0;
    return calcCgroupWS(row) + calcUnchargedRSSFile(row);
  }

  // Parse human byte limit client-side to power instant OOM headroom indicator.
  function parseClientLimit(raw) {
    if (!raw) return null;
    const s = raw.trim();
    if (!s) return null;
    if (s.toLowerCase() === "max") {
      return { unlimited: true, bytes: 0 };
    }
    const m = /^([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)$/.exec(s);
    if (!m) return null;
    const val = parseFloat(m[1]);
    if (!Number.isFinite(val) || val <= 0) return null;
    const u = m[2].toLowerCase();
    const mults = {
      "": 1,
      b: 1,
      k: 1024, kb: 1000, kib: 1024,
      m: 1024 ** 2, mb: 1000 ** 2, mib: 1024 ** 2,
      g: 1024 ** 3, gb: 1000 ** 3, gib: 1024 ** 3,
    };
    if (!(u in mults)) return null;
    return { unlimited: false, bytes: Math.round(val * mults[u]) };
  }

  function decodeDeltasBase64(b64) {
    if (!b64) return new Uint32Array(0);
    const bin = atob(b64);
    const len = bin.length;
    const count = Math.floor(len / 4);
    const bytes = new Uint8Array(count * 4);
    for (let i = 0; i < count * 4; i++) {
      bytes[i] = bin.charCodeAt(i);
    }
    const dv = new DataView(bytes.buffer);
    const out = new Uint32Array(count);
    for (let i = 0; i < count; i++) {
      out[i] = dv.getUint32(i * 4, true);
    }
    return out;
  }

  function showToast(msg, kind = "info") {
    dom.liveToast.textContent = msg;
    dom.liveToast.dataset.kind = kind;
  }

  // Reset per-run history arrays around a layout.
  function initPageStore(layout, isPreview) {
    state.layout = layout;
    state.isPreviewLayout = isPreview;
    const total = layout ? Number(layout.total_binary_pages) || 0 : 0;
    state.totalPages = total;
    state.frames = [];
    state.livePages = new Uint8Array(total);
    state.scrubPages = new Uint8Array(total);
    state.checkpoints = [new Uint8Array(total)];
    state.scrubComputedIdx = -1;
    state.viewIdx = -1;
    state.geomDirty = true;
    state.canvasDirty = true;
    state.timelineDirty = true;
  }

  function appendSampleFrame(framePayload, recvPerfMs) {
    if (framePayload.seq !== state.frames.length) {
      // Gap detected (e.g. background tab stalled); perform full clean resync.
      syncFullState();
      return;
    }
    const deltas = decodeDeltasBase64(framePayload.deltas_b64);
    const total = state.totalPages;
    for (let i = 0; i < deltas.length; i++) {
      const d = deltas[i];
      const idx = d >>> 4;
      if (idx < total) {
        state.livePages[idx] = d & 0x0f;
      }
    }
    state.frames.push({
      seq: framePayload.seq,
      row: framePayload.row,
      deltas,
      recvPerfMs,
    });
    if (state.frames.length % CHECKPOINT_STRIDE === 0) {
      state.checkpoints.push(new Uint8Array(state.livePages));
    }

    const row = framePayload.row;
    const curBytes = Math.max(row.cg_memory_current_bytes || 0, calcEstIsolatedWS(row));
    if (curBytes > state.peakArenaBytes) {
      state.peakArenaBytes = curBytes;
    }

    if (state.followLive) {
      state.viewIdx = state.frames.length - 1;
      state.canvasDirty = true;
    }
    state.timelineDirty = true;
  }

  function getPagesAtFrame(idx) {
    if (idx < 0 || state.frames.length === 0 || state.totalPages === 0) {
      return state.livePages;
    }
    if (idx >= state.frames.length - 1) {
      return state.livePages;
    }
    if (idx === state.scrubComputedIdx) {
      return state.scrubPages;
    }
    if (idx === state.scrubComputedIdx + 1) {
      const ds = state.frames[idx].deltas;
      for (let i = 0; i < ds.length; i++) {
        const d = ds[i];
        const p = d >>> 4;
        if (p < state.totalPages) state.scrubPages[p] = d & 0x0f;
      }
      state.scrubComputedIdx = idx;
      return state.scrubPages;
    }

    let ck = Math.floor((idx + 1) / CHECKPOINT_STRIDE);
    if (ck >= state.checkpoints.length) ck = state.checkpoints.length - 1;
    state.scrubPages.set(state.checkpoints[ck]);
    const startF = ck * CHECKPOINT_STRIDE;
    for (let f = startF; f <= idx; f++) {
      const ds = state.frames[f].deltas;
      for (let i = 0; i < ds.length; i++) {
        const d = ds[i];
        const p = d >>> 4;
        if (p < state.totalPages) state.scrubPages[p] = d & 0x0f;
      }
    }
    state.scrubComputedIdx = idx;
    return state.scrubPages;
  }

  // Synchronize complete server state on page load or reconnect.
  async function syncFullState() {
    if (state.resyncInFlight) return;
    state.resyncInFlight = true;
    try {
      const res = await fetch("/api/state", { cache: "no-store" });
      if (!res.ok) return;
      const payload = await res.json();
      applyFullState(payload);
    } catch (_) {
      // SSE reconnect will retry cleanly.
    } finally {
      state.resyncInFlight = false;
    }
  }

  function applyFullState(payload) {
    const firstLoad = !state.config;
    state.config = payload.config;

    dom.metaCfgName.textContent = payload.config.name || "—";
    dom.metaBinName.textContent =
      basename(payload.config.collector_binary) +
      (payload.config.collector_binary_size ? ` (${fmtMiB(payload.config.collector_binary_size)})` : "");
    dom.metaSaveMode.textContent = payload.config.base_results_dir
      ? payload.config.base_results_dir
      : "In-memory live only";

    if (firstLoad) {
      dom.inputLimit.value = payload.config.default_memory_limit || "80MiB";
      dom.inputDuration.value = payload.config.default_duration || "60s";
      dom.inputSwap.value = payload.config.default_swap_max || "0";
      dom.inputLiveLimit.value = payload.config.default_memory_limit || "80MiB";
      syncChipHighlights();
    }

    const run = payload.active_run;
    if (!run) {
      state.status = "idle";
      initPageStore(payload.config.preview_layout || null, true);
      updateControlsUI();
      updateTelemetryUI();
      return;
    }

    state.runId = run.run_id;
    state.status = run.status;
    state.memoryLimitRaw = run.memory_limit_raw;
    state.memoryLimitHuman = run.memory_limit_human;
    state.memoryLimitUnlimited = !!run.memory_limit_unlimited;
    state.memoryLimitBytes = run.memory_limit_bytes || 0;
    state.initialLimitHuman = run.initial_memory_limit_human || run.memory_limit_human;
    state.initialLimitUnlimited = run.initial_memory_limit_unlimited !== undefined
      ? !!run.initial_memory_limit_unlimited
      : !!run.memory_limit_unlimited;
    state.initialLimitBytes = run.initial_memory_limit_bytes !== undefined
      ? run.initial_memory_limit_bytes || 0
      : run.memory_limit_bytes || 0;
    state.swapMaxRaw = run.swap_max_raw || "0";
    state.configuredDuration = run.configured_duration || "";
    state.durationMsTarget = run.duration_ms_target || 0;
    state.resultsSubdir = run.results_subdir || "";
    state.startupReadinessMs = run.startup_readiness_ms || 0;
    state.markers = Array.isArray(run.markers) ? run.markers.slice() : [];
    state.finalMeta = run.final_meta || null;
    state.sections = Array.isArray(run.sections) ? run.sections.slice() : [];
    state.collectorLogTail = run.collector_log_tail || "";
    state.runError = run.run_error || "";
    state.peakArenaBytes = 0;

    if (run.layout) {
      initPageStore(run.layout, false);
    } else {
      initPageStore(payload.config.preview_layout || null, true);
    }

    const rawFrames = Array.isArray(run.frames) ? run.frames : [];
    const nowPerf = performance.now() - LIVE_FLASH_MS * 2;
    state.followLive = true;
    state.isReplaying = false;
    for (let i = 0; i < rawFrames.length; i++) {
      appendSampleFrame(rawFrames[i], nowPerf);
    }
    if (run.memory_limit_raw) {
      dom.inputLiveLimit.value = run.memory_limit_raw;
    }

    updateControlsUI();
    updateTelemetryUI();
    updateDiagnosticsUI();
  }

  // Connect EventSource for real-time benchmark stream.
  function connectSSE() {
    const es = new EventSource("/api/events");

    es.addEventListener("run_started", (ev) => {
      const d = JSON.parse(ev.data);
      state.runId = d.run_id;
      state.status = d.status;
      state.memoryLimitRaw = d.memory_limit_raw;
      state.memoryLimitHuman = d.memory_limit_human;
      state.memoryLimitUnlimited = !!d.memory_limit_unlimited;
      state.memoryLimitBytes = d.memory_limit_bytes || 0;
      state.initialLimitHuman = d.memory_limit_human;
      state.initialLimitUnlimited = !!d.memory_limit_unlimited;
      state.initialLimitBytes = d.memory_limit_bytes || 0;
      state.swapMaxRaw = d.swap_max_raw || "0";
      state.configuredDuration = d.configured_duration || "";
      state.durationMsTarget = d.duration_ms_target || 0;
      state.resultsSubdir = d.results_subdir || "";
      state.startupReadinessMs = 0;
      state.markers = [];
      state.finalMeta = null;
      state.sections = [];
      state.collectorLogTail = "";
      state.runError = "";
      state.peakArenaBytes = 0;
      state.followLive = true;
      state.isReplaying = false;

      initPageStore(state.config ? state.config.preview_layout : null, true);
      dom.inputLiveLimit.value = d.memory_limit_raw || "";
      showToast("Evicting warm page cache & spawning Collector cgroup…", "info");
      updateControlsUI();
      updateTelemetryUI();
      updateDiagnosticsUI();
    });

    es.addEventListener("run_layout", (ev) => {
      const d = JSON.parse(ev.data);
      if (d.run_id !== state.runId) return;
      state.status = d.status || "running";
      // Preserve any early frames if sample raced ahead, otherwise reset to exact VMA layout.
      const prevFrames = state.frames.slice();
      initPageStore(d.layout, false);
      for (let i = 0; i < prevFrames.length; i++) {
        const f = prevFrames[i];
        const total = state.totalPages;
        for (let j = 0; j < f.deltas.length; j++) {
          const val = f.deltas[j];
          const idx = val >>> 4;
          if (idx < total) state.livePages[idx] = val & 0x0f;
        }
        state.frames.push(f);
        if (state.frames.length % CHECKPOINT_STRIDE === 0) {
          state.checkpoints.push(new Uint8Array(state.livePages));
        }
      }
      if (state.frames.length > 0) {
        state.viewIdx = state.frames.length - 1;
      }
      showToast(`Tracking ${fmtInt(state.totalPages)} binary 4 KiB pages (${fmtMiB(d.layout.total_binary_mapped_bytes)})`, "ok");
      updateControlsUI();
      updateTelemetryUI();
    });

    es.addEventListener("sample", (ev) => {
      const d = JSON.parse(ev.data);
      if (d.run_id !== state.runId) return;
      if (state.status === "starting") {
        state.status = "running";
        updateControlsUI();
      }
      appendSampleFrame(d.frame, performance.now());
      updateTelemetryUI();
    });

    es.addEventListener("marker", (ev) => {
      const d = JSON.parse(ev.data);
      if (d.run_id !== state.runId) return;
      const mk = d.marker;
      state.markers.push(mk);
      if (mk.kind === "ready") {
        state.startupReadinessMs = mk.elapsed_ms;
        showToast(mk.label + " — steady-state OTLP workload active", "ok");
      } else if (mk.kind === "limit_change") {
        state.memoryLimitRaw = mk.memory_limit_raw;
        state.memoryLimitHuman = mk.memory_limit_human;
        state.memoryLimitUnlimited = !!mk.memory_limit_unlimited;
        state.memoryLimitBytes = mk.memory_limit_bytes || 0;
        dom.inputLiveLimit.value = mk.memory_limit_raw;
        showToast(mk.detail || mk.label, "ok");
      } else if (mk.kind === "reclaim") {
        showToast(mk.detail || mk.label, "ok");
      }
      state.timelineDirty = true;
      updateTelemetryUI();
    });

    es.addEventListener("status_change", (ev) => {
      const d = JSON.parse(ev.data);
      if (d.run_id !== state.runId) return;
      state.status = d.status;
      updateControlsUI();
    });

    es.addEventListener("run_done", (ev) => {
      const d = JSON.parse(ev.data);
      if (d.run_id !== state.runId) return;
      state.status = "finished";
      if (d.final_meta) state.finalMeta = d.final_meta;
      if (Array.isArray(d.sections)) state.sections = d.sections;
      if (typeof d.collector_log_tail === "string") state.collectorLogTail = d.collector_log_tail;
      if (d.run_error) state.runError = d.run_error;

      if (d.run_error && !state.finalMeta) {
        showToast("Run error: " + d.run_error, "error");
      } else if (state.finalMeta && state.finalMeta.summary) {
        showToast(`Completed: ${state.finalMeta.summary.verdict} — use timeline scrubber below to replay`, "ok");
      }
      state.canvasDirty = true;
      state.timelineDirty = true;
      updateControlsUI();
      updateTelemetryUI();
      updateDiagnosticsUI();
    });

    es.onerror = () => {
      // Browser auto-reconnects EventSource; pull snapshot once back up.
      setTimeout(syncFullState, 1200);
    };
  }

  // UI Status & Control Enablement
  function isRunActive() {
    return state.status === "starting" || state.status === "running" || state.status === "stopping";
  }

  function updateControlsUI() {
    const active = isRunActive();
    const canModifyLive = state.status === "running";

    dom.btnStart.disabled = active;
    dom.inputLimit.disabled = active;
    dom.inputDuration.disabled = active;
    dom.inputSwap.disabled = active;
    dom.limitChips.forEach((b) => (b.disabled = active));
    dom.durChips.forEach((b) => (b.disabled = active));

    dom.btnStop.disabled = !(state.status === "starting" || state.status === "running");

    dom.livePanel.dataset.disabled = canModifyLive ? "false" : "true";
    dom.inputLiveLimit.disabled = !canModifyLive;
    dom.btnApplyLiveLimit.disabled = !canModifyLive;
    dom.liveStepBtns.forEach((b) => (b.disabled = !canModifyLive));
    dom.reclaimBtns.forEach((b) => (b.disabled = !canModifyLive));

    // Status badge styling
    let badgeText = state.status.toUpperCase();
    let tone = "idle";
    if (state.status === "starting" || state.status === "running") {
      tone = "running";
    } else if (state.status === "stopping") {
      tone = "warn";
    } else if (state.status === "finished") {
      const verdict = state.finalMeta && state.finalMeta.summary ? state.finalMeta.summary.verdict : "";
      if (state.runError && !verdict) {
        badgeText = "ERROR";
        tone = "danger";
      } else if (verdict === "oom_killed" || verdict === "process_error") {
        badgeText = verdict.toUpperCase();
        tone = "danger";
      } else if (verdict === "thrashing" || verdict === "stopped") {
        badgeText = verdict.toUpperCase();
        tone = "warn";
      } else if (verdict) {
        badgeText = verdict.toUpperCase();
        tone = "ok";
      } else {
        tone = "ok";
      }
    }
    dom.statusBadge.dataset.tone = tone;
    dom.statusText.textContent = badgeText;

    // Scrubber controls
    const hasFrames = state.frames.length > 0;
    dom.slider.disabled = !hasFrames;
    dom.slider.max = hasFrames ? String(state.frames.length - 1) : "0";
    if (state.viewIdx >= 0) {
      dom.slider.value = String(state.viewIdx);
    }
    dom.btnPlayPause.disabled = state.frames.length < 2;
    dom.btnPlayPause.textContent = state.isReplaying ? "Pause ❚❚" : "Replay ▶";
    dom.btnStepPrev.disabled = !hasFrames || state.viewIdx <= 0;
    dom.btnStepNext.disabled = !hasFrames || state.viewIdx >= state.frames.length - 1;

    dom.btnFollowLive.classList.toggle("is-live", state.followLive);
    dom.btnFollowLive.textContent = state.followLive ? "● LIVE" : "○ Return to Live";

    updateOOMRiskIndicator();
  }

  function updateOOMRiskIndicator() {
    if (state.status !== "running") {
      dom.oomIndicator.dataset.risk = "idle";
      dom.oomIndicator.textContent = "Live cgroup controls unlock while benchmark is running";
      return;
    }
    const parsed = parseClientLimit(dom.inputLiveLimit.value);
    if (!parsed) {
      dom.oomIndicator.dataset.risk = "tight";
      dom.oomIndicator.textContent = "Enter bytes with unit (e.g. 64MiB) or max";
      return;
    }
    if (parsed.unlimited) {
      dom.oomIndicator.dataset.risk = "safe";
      dom.oomIndicator.textContent = "Unlimited ceiling — zero kernel memory.max reclaim pressure";
      return;
    }
    const latest = state.frames.length > 0 ? state.frames[state.frames.length - 1].row : null;
    const irreclaimable = latest ? (latest.cg_anon_bytes || 0) + (latest.cg_kernel_bytes || 0) : 0;
    const headroom = parsed.bytes - irreclaimable;
    if (irreclaimable > 0 && headroom <= 0) {
      dom.oomIndicator.dataset.risk = "danger";
      dom.oomIndicator.textContent = `Below live Anon+Kernel (${fmtMiB(irreclaimable)}) — likely immediate OOM kill`;
    } else if (irreclaimable > 0 && headroom <= 12 * 1024 * 1024) {
      dom.oomIndicator.dataset.risk = "tight";
      dom.oomIndicator.textContent = `Tight binary cache budget (+${fmtMiB(headroom)} over Anon+Kernel) — expect refaults`;
    } else {
      dom.oomIndicator.dataset.risk = "safe";
      dom.oomIndicator.textContent =
        irreclaimable > 0
          ? `Headroom: +${fmtMiB(headroom)} above live Anon+Kernel (${fmtMiB(irreclaimable)})`
          : `Target: ${fmtMiB(parsed.bytes)}`;
    }
  }

  // Determine effective memory limit at viewed frame index (respecting historical mid-run changes).
  function limitAtElapsed(elapsedMs) {
    let lim = {
      human: state.initialLimitHuman || state.memoryLimitHuman || "—",
      unlimited: state.initialLimitUnlimited,
      bytes: state.initialLimitBytes || 0,
    };
    for (let i = 0; i < state.markers.length; i++) {
      const m = state.markers[i];
      if (m.kind === "limit_change" && m.elapsed_ms <= elapsedMs + 25) {
        lim = {
          human: m.memory_limit_human,
          unlimited: !!m.memory_limit_unlimited,
          bytes: m.memory_limit_bytes || 0,
        };
      }
    }
    return lim;
  }

  // Update Cgroup Arena Bar, KPI Strip, and Legend Counts for state.viewIdx.
  function updateTelemetryUI() {
    const hasFrame = state.viewIdx >= 0 && state.viewIdx < state.frames.length;
    const frame = hasFrame ? state.frames[state.viewIdx] : null;
    const row = frame ? frame.row : null;
    const pageSize = (state.layout && state.layout.page_size_bytes) || 4096;

    const lim = row ? limitAtElapsed(row.elapsed_ms) : {
      human: state.memoryLimitHuman || (state.config ? state.config.default_memory_limit : "—"),
      unlimited: state.memoryLimitUnlimited,
      bytes: state.memoryLimitBytes || 0,
    };

    const anonB = row ? row.cg_anon_bytes || 0 : 0;
    const kernB = row ? row.cg_kernel_bytes || 0 : 0;
    const fileB = row ? row.cg_file_bytes || 0 : 0;
    const actFileB = row ? row.cg_active_file_bytes || 0 : 0;
    const inactFileB = row ? row.cg_inactive_file_bytes || 0 : 0;
    const curB = row ? row.cg_memory_current_bytes || 0 : 0;
    const rssB = row ? row.proc_rss_bytes || 0 : 0;
    const cgWsB = row ? calcCgroupWS(row) : 0;
    const unchargedB = row ? calcUnchargedRSSFile(row) : 0;
    const isoWsB = row ? calcEstIsolatedWS(row) : 0;

    // Smooth Arena scale denominator
    const mappedBytes = state.layout ? state.layout.total_binary_mapped_bytes || 0 : 0;
    let scaleMax = Math.max(curB, isoWsB, anonB + kernB + fileB, state.peakArenaBytes, 64 * 1024 * 1024);
    if (!lim.unlimited && lim.bytes > 0) {
      scaleMax = Math.max(scaleMax, Math.round(lim.bytes * 1.12));
    } else if (mappedBytes > 0) {
      scaleMax = Math.max(scaleMax, Math.round(mappedBytes * 0.55));
    }

    dom.arenaScaleLabel.textContent = `(bar full scale: ${fmtMiB(scaleMax)})`;
    dom.legArenaAnon.textContent = fmtMiB(anonB);
    dom.legArenaKernel.textContent = fmtMiB(kernB);
    dom.legArenaActiveFile.textContent = fmtMiB(actFileB);
    dom.legArenaInactiveFile.textContent = fmtMiB(inactFileB);
    dom.legArenaWs.textContent =
      unchargedB >= 1024 * 1024 ? `${fmtMiB(cgWsB)} (Iso ${fmtMiB(isoWsB)})` : fmtMiB(cgWsB);
    dom.legArenaLimit.textContent = lim.unlimited ? "max (unlimited)" : lim.human;

    // Order Anon + Kernel + Active File contiguously inside Kubelet Working Set, followed by Inactive File up to memory.current.
    const totalCharged = curB > 0 ? curB : anonB + kernB + fileB;
    const inactDraw = Math.min(totalCharged, inactFileB);
    const wsSpan = Math.max(0, totalCharged - inactDraw);
    const anonDraw = Math.min(anonB, wsSpan);
    const kernDraw = Math.min(kernB, Math.max(0, wsSpan - anonDraw));
    const actFileDraw = Math.max(0, wsSpan - anonDraw - kernDraw);

    const pct = (b) => Math.max(0, Math.min(100, (b / scaleMax) * 100));
    dom.segAnon.style.width = pct(anonDraw).toFixed(2) + "%";
    dom.segKernel.style.width = pct(kernDraw).toFixed(2) + "%";
    dom.segActiveFile.style.width = pct(actFileDraw).toFixed(2) + "%";
    dom.segInactiveFile.style.width = pct(inactDraw).toFixed(2) + "%";

    if (row && totalCharged > 0) {
      const wp = Math.max(0.5, Math.min(99.5, (wsSpan / scaleMax) * 100));
      dom.wsLine.style.display = "block";
      dom.wsLine.style.left = wp.toFixed(2) + "%";
      dom.wsTag.textContent = `Kubelet WS ${fmtMiB(cgWsB)}`;
      dom.wsTag.style.transform = wp < 18 ? "translateX(6px)" : "translateX(-104%)";
    } else {
      dom.wsLine.style.display = "none";
    }

    if (row && unchargedB >= 1024 * 1024) {
      const ip = Math.max(0.5, Math.min(99.5, (isoWsB / scaleMax) * 100));
      dom.isoWsLine.style.display = "block";
      dom.isoWsLine.style.left = ip.toFixed(2) + "%";
      dom.isoWsTag.textContent = `Est. Iso WS ${fmtMiB(isoWsB)}`;
      dom.isoWsTag.style.transform = ip > 82 ? "translateX(-104%)" : "translateX(6px)";
    } else {
      dom.isoWsLine.style.display = "none";
    }

    if (!lim.unlimited && lim.bytes > 0) {
      const lp = Math.max(1, Math.min(99.5, (lim.bytes / scaleMax) * 100));
      dom.limitLine.style.display = "block";
      dom.limitLine.style.left = lp.toFixed(2) + "%";
      dom.limitTag.textContent = `memory.max ${lim.human}`;
      dom.limitTag.style.transform = lp < 16 ? "translateX(6px)" : "translateX(-104%)";
    } else {
      dom.limitLine.style.display = "none";
    }

    if (!row) {
      dom.kpiCgCur.textContent = "—";
      dom.kpiCgSub.textContent = `limit: ${lim.human || "—"}`;
      dom.kpiCgWs.textContent = "—";
      dom.kpiCgWsSub.textContent = "VmRSS: — · Inact: —";
      dom.kpiBinRes.textContent = "—";
      dom.kpiBinSub.textContent = state.totalPages ? `${fmtInt(state.totalPages)} pages mapped` : "—";
      dom.kpiCleanEvict.textContent = "—";
      dom.kpiCleanSub.textContent = "0 pages evicted";
      dom.kpiRefaultPct.textContent = "—";
      dom.kpiRefaultSub.textContent = "0 refaulted / 0 evicted";
      dom.kpiTickDeltas.textContent = "—";
      dom.kpiCumSub.textContent = "cum: +0 cold, −0 evict, !0 refault";
      dom.kpiPsi.textContent = "—";
      dom.kpiEventsSub.textContent = "memory.events max: 0";
      dom.cntNever.textContent = fmtInt(state.totalPages);
      dom.cntFlashCold.textContent = "0";
      dom.cntResFile.textContent = "0";
      dom.cntResCow.textContent = "0";
      dom.cntFlashEvict.textContent = "0";
      dom.cntEvictClean.textContent = "0";
      dom.cntRefault1.textContent = "0";
      dom.cntThrash.textContent = "0";
      dom.frameLabel.textContent = "Frame 0 / 0 · t = 0.00s";
      dom.vizSummary.textContent = state.layout
        ? `${fmtInt(state.totalPages)} mapped 4 KiB pages (${fmtMiB(mappedBytes)}) across ${state.layout.spans.length} ELF section spans — click Start Benchmark`
        : "Waiting for binary layout…";
      return;
    }

    const binResPages = (row.bin_present_file_pages || 0) + (row.bin_present_cow_pages || 0);
    const binTotal = row.bin_total_pages || state.totalPages || 1;
    const everEvicted = row.bin_ever_evicted_pages || 0;
    const everRefaulted = row.bin_ever_refaulted_pages || 0;
    const cleanlyEvicted = row.bin_cleanly_evicted_pages || 0;
    const everFaulted = row.bin_ever_faulted_pages || 0;

    dom.kpiCgCur.textContent = fmtMiB(curB);
    dom.kpiCgSub.textContent = `Anon+Kern ${fmtMiB(anonB + kernB)} · limit ${lim.unlimited ? "max" : lim.human}`;

    dom.kpiCgWs.textContent = `${fmtMiB(cgWsB)} | Iso ${fmtMiB(isoWsB)}`;
    const wsRssDiff = cgWsB - rssB;
    const wsDiffStr = (wsRssDiff >= 0 ? "+" : "−") + fmtMiB(Math.abs(wsRssDiff));
    dom.kpiCgWsSub.textContent =
      unchargedB >= 1024 * 1024
        ? `VmRSS ${fmtMiB(rssB)} (+${fmtMiB(unchargedB)} uncharged RssFile)`
        : `VmRSS ${fmtMiB(rssB)} (Δ ${wsDiffStr}) · Inact −${fmtMiB(inactFileB)}`;

    dom.kpiBinRes.textContent = fmtMiB(binResPages * pageSize);
    dom.kpiBinSub.textContent = `${((binResPages / binTotal) * 100).toFixed(1)}% of ELF · CoW ${fmtMiB((row.bin_present_cow_pages || 0) * pageSize)}`;

    dom.kpiCleanEvict.textContent = fmtMiB(cleanlyEvicted * pageSize);
    dom.kpiCleanSub.textContent = everFaulted > 0
      ? `${((cleanlyEvicted / everFaulted) * 100).toFixed(1)}% of faulted stayed clean`
      : "0 pages faulted yet";

    if (everEvicted > 0) {
      const refPct = (everRefaulted / everEvicted) * 100;
      dom.kpiRefaultPct.textContent = refPct.toFixed(1) + "%";
      dom.kpiRefaultPct.style.color =
        refPct > 25 ? "hsl(8, 86%, 68%)" : refPct >= 5 ? "hsl(38, 92%, 68%)" : "hsl(156, 70%, 66%)";
    } else {
      dom.kpiRefaultPct.textContent = "—";
      dom.kpiRefaultPct.style.color = "";
    }
    dom.kpiRefaultSub.textContent = `${fmtMiB(everRefaulted * pageSize)} / ${fmtMiB(everEvicted * pageSize)} evicted`;

    dom.kpiTickDeltas.textContent = `+${fmtInt(row.delta_cold_faults)}  −${fmtInt(row.delta_evictions)}  !${fmtInt(row.delta_refaults_total)}`;
    dom.kpiCumSub.textContent = `cum: +${fmtInt(row.cum_cold_faults)} −${fmtInt(row.cum_evictions)} !${fmtInt(row.cum_refaults_total)}`;

    const psiSomeMs = (row.cg_psi_some_total_us || 0) / 1000;
    const psiFullMs = (row.cg_psi_full_total_us || 0) / 1000;
    dom.kpiPsi.textContent = `${psiFullMs.toFixed(1)} ms full`;
    dom.kpiEventsSub.textContent = `some ${psiSomeMs.toFixed(1)} ms · max events ${fmtInt(row.cg_events_max)}`;

    // Legend live counts
    dom.cntNever.textContent = fmtInt(row.bin_never_faulted_pages);
    dom.cntFlashCold.textContent = fmtInt(row.delta_cold_faults);
    dom.cntResFile.textContent = fmtInt(row.bin_present_file_pages);
    dom.cntResCow.textContent = fmtInt(row.bin_present_cow_pages);
    dom.cntFlashEvict.textContent = fmtInt(row.delta_evictions);
    dom.cntEvictClean.textContent = fmtInt(cleanlyEvicted);
    const refOncePages = Math.max(0, everRefaulted - (row.bin_thrashing_pages || 0));
    dom.cntRefault1.textContent = fmtInt(refOncePages);
    dom.cntThrash.textContent = fmtInt(row.bin_thrashing_pages);

    const tSec = ((row.elapsed_ms || 0) / 1000).toFixed(2);
    dom.frameLabel.textContent = `Frame ${state.viewIdx + 1} / ${state.frames.length} · t = ${tSec}s · ${row.trigger}`;
    dom.vizSummary.textContent =
      `${fmtInt(binTotal)} pages (${fmtMiB(binTotal * pageSize)}) · ` +
      `Ever faulted: ${fmtMiB(everFaulted * pageSize)} · ` +
      `Currently evicted out: ${fmtMiB((row.bin_evicted_out_pages || 0) * pageSize)}`;

    dom.slider.max = String(state.frames.length - 1);
    dom.slider.value = String(state.viewIdx);
    dom.btnStepPrev.disabled = state.viewIdx <= 0;
    dom.btnStepNext.disabled = state.viewIdx >= state.frames.length - 1;
    dom.btnPlayPause.disabled = state.frames.length < 2;

    if (state.status === "running") {
      updateOOMRiskIndicator();
    }
  }

  function updateDiagnosticsUI() {
    dom.diagLog.textContent = state.collectorLogTail || "(no Collector output captured yet)";

    if (state.runError && !state.finalMeta) {
      dom.diagHint.textContent = "Run error";
      dom.diagVerdict.textContent = "Error: " + state.runError;
      dom.diagSecWrap.style.display = "none";
      dom.diagDrawer.open = true;
      return;
    }

    if (!state.finalMeta || !state.finalMeta.summary) {
      dom.diagHint.textContent = isRunActive() ? "Run in progress…" : "No completed run yet";
      if (isRunActive()) {
        dom.diagVerdict.textContent = state.resultsSubdir
          ? `Streaming live artifacts to ${state.resultsSubdir}`
          : "Streaming live in memory…";
      }
      dom.diagSecWrap.style.display = "none";
      return;
    }

    const s = state.finalMeta.summary;
    const savedMsg = state.resultsSubdir ? ` · Saved to ${state.resultsSubdir}` : " · (In-memory run)";
    let wsSummaryMsg = "";
    if (s.peak_cg_working_set_bytes > 0 || s.final_cg_working_set_bytes > 0) {
      wsSummaryMsg =
        ` · Kubelet WS ${fmtMiB(s.peak_cg_working_set_bytes)} → ${fmtMiB(s.final_cg_working_set_bytes)}` +
        ` (Est. Iso ${fmtMiB(s.peak_est_isolated_ws_bytes)} → ${fmtMiB(s.final_est_isolated_ws_bytes)})`;
    }
    dom.diagHint.textContent = `Verdict: ${s.verdict}`;
    dom.diagVerdict.textContent = `[${s.verdict.toUpperCase()}] ${s.verdict_reason}${wsSummaryMsg}${savedMsg}`;

    if (state.sections && state.sections.length > 0) {
      const pageSize = state.finalMeta.page_size_bytes || 4096;
      dom.diagSecTbody.innerHTML = "";
      const sorted = state.sections.slice().sort((a, b) => {
        const fa = (a.total_pages || 0) - (a.never_faulted_pages || 0);
        const fb = (b.total_pages || 0) - (b.never_faulted_pages || 0);
        return fb - fa;
      });
      for (const sec of sorted) {
        const everFaulted = (sec.total_pages || 0) - (sec.never_faulted_pages || 0);
        const everRef = (sec.evicted_after_refault_pages || 0) + (sec.resident_after_refault_pages || 0);
        const everEv = (sec.cleanly_evicted_pages || 0) + everRef;
        const finalRes = (sec.present_final_pages || 0) + (sec.present_cow_final_pages || 0);
        const oneShotPct = everFaulted > 0 ? (((sec.cleanly_evicted_pages || 0) / everFaulted) * 100).toFixed(1) + "%" : "—";
        const refPct = everEv > 0 ? ((everRef / everEv) * 100).toFixed(1) + "%" : "—";

        const tr = document.createElement("tr");
        const cells = [
          sec.section_name,
          sec.vma_perms,
          fmtMiB((sec.total_pages || 0) * pageSize),
          `${fmtMiB(everFaulted * pageSize)}`,
          `${fmtMiB((sec.cleanly_evicted_pages || 0) * pageSize)} (${oneShotPct})`,
          refPct,
          fmtMiB(finalRes * pageSize),
          fmtInt(sec.total_refaults || 0),
        ];
        cells.forEach((txt, idx) => {
          const td = document.createElement("td");
          if (idx <= 1) td.className = "mono";
          td.textContent = txt;
          tr.appendChild(td);
        });
        dom.diagSecTbody.appendChild(tr);
      }
      dom.diagSecWrap.style.display = "block";
    } else {
      dom.diagSecWrap.style.display = "none";
    }

    if (s.verdict === "oom_killed" || s.verdict === "process_error") {
      dom.diagDrawer.open = true;
    }
  }

  // Geometry Construction for 4 KiB Pages Mode & 256 KiB Blocks Mode
  function rebuildGeometry() {
    state.geomDirty = false;
    state.canvasDirty = true;

    const layout = state.layout;
    if (!layout || !layout.spans || layout.spans.length === 0) {
      state.geom = null;
      return;
    }

    const cssW = Math.max(320, Math.floor(dom.canvasStage.clientWidth || 960));
    const dpr = Math.min(2, window.devicePixelRatio || 1);

    if (state.viewMode === "blocks") {
      state.geom = buildBlocksGeometry(layout, cssW, dpr);
    } else {
      state.geom = buildPagesGeometry(layout, cssW, dpr);
    }

    dom.canvas.width = state.geom.devW;
    dom.canvas.height = state.geom.devH;
    dom.canvas.style.height = state.geom.cssH + "px";

    const ctx = dom.canvas.getContext("2d", { alpha: false });
    state.imgData = ctx.createImageData(state.geom.devW, state.geom.devH);
    state.imgU32 = new Uint32Array(state.imgData.data.buffer);
    state.bgU32 = new Uint32Array(state.geom.devW * state.geom.devH);
    state.bgU32.fill(BG_STAGE_U32);

    // Pre-paint alternating band backgrounds into bgU32 for pages mode.
    if (state.geom.mode === "pages") {
      const devW = state.geom.devW;
      state.geom.bands.forEach((b, bi) => {
        const bgCol = bi % 2 === 0 ? BG_BAND_EVEN_U32 : BG_BAND_ODD_U32;
        const y0 = Math.max(0, b.bandDevY);
        const y1 = Math.min(state.geom.devH, b.bandDevY + b.bandDevH);
        for (let y = y0; y < y1; y++) {
          state.bgU32.fill(bgCol, y * devW, (y + 1) * devW);
        }
      });
    }
  }

  function buildPagesGeometry(layout, cssW, dpr) {
    const gutterCSS = cssW < 640 ? 124 : Math.min(206, Math.max(152, Math.round(cssW * 0.16)));
    const padX = 10;
    const gridXCSS = gutterCSS + padX;
    const gridWCSS = Math.max(120, cssW - gridXCSS - padX);
    const devW = Math.max(1, Math.round(cssW * dpr));
    const gridXDev = Math.round(gridXCSS * dpr);
    const gridWDev = Math.max(32, devW - gridXDev - Math.round(padX * dpr));

    const totalPages = Number(layout.total_binary_pages) || 0;
    const pageDevX = new Uint16Array(totalPages);
    const pageDevY = new Uint16Array(totalPages);
    const pageDevS = new Uint8Array(totalPages);

    const bands = [];
    let curYDev = Math.round(6 * dpr);
    const bandGapDev = Math.max(2, Math.round(4 * dpr));
    const bandPadYDev = Math.max(3, Math.round(5 * dpr));
    const minBandHDev = Math.round(32 * dpr);

    for (let si = 0; si < layout.spans.length; si++) {
      const sp = layout.spans[si];
      const count = sp.page_count;
      if (count <= 0) continue;

      let cellCSS, gapCSS;
      if (count >= 4096) {
        cellCSS = gridWCSS >= 1220 ? 3 : 2;
        gapCSS = 0;
      } else if (count >= 128) {
        cellCSS = 5;
        gapCSS = 1;
      } else {
        cellCSS = 7;
        gapCSS = 1;
      }

      const cellDev = Math.max(1, Math.round(cellCSS * dpr));
      const gapDev = gapCSS > 0 ? Math.max(1, Math.round(gapCSS * dpr)) : 0;
      const pitchDev = cellDev + gapDev;
      const cols = Math.max(1, Math.floor(gridWDev / pitchDev));
      const rows = Math.ceil(count / cols);
      const gridHDev = rows * pitchDev - gapDev;
      const bandDevH = Math.max(minBandHDev, gridHDev + bandPadYDev * 2);
      const gridYDev = curYDev + Math.floor((bandDevH - gridHDev) / 2);

      for (let i = 0; i < count; i++) {
        const pIdx = sp.start_page + i;
        if (pIdx >= totalPages) break;
        const r = Math.floor(i / cols);
        const c = i - r * cols;
        pageDevX[pIdx] = gridXDev + c * pitchDev;
        pageDevY[pIdx] = gridYDev + r * pitchDev;
        pageDevS[pIdx] = cellDev;
      }

      bands.push({
        span: sp,
        bandDevY: curYDev,
        bandDevH,
        gridXDev,
        gridYDev,
        gridWDev,
        gridHDev,
        cellDev,
        gapDev,
        pitchDev,
        cols,
        rows,
      });

      curYDev += bandDevH + bandGapDev;
    }

    const devH = Math.max(Math.round(120 * dpr), curYDev + Math.round(4 * dpr));
    return {
      mode: "pages",
      dpr,
      cssW,
      cssH: Math.round(devH / dpr),
      devW,
      devH,
      gutterCSS,
      bands,
      pageDevX,
      pageDevY,
      pageDevS,
    };
  }

  function buildBlocksGeometry(layout, cssW, dpr) {
    const pageSize = Number(layout.page_size_bytes) || 4096;
    const blockPages = Number(layout.block_size_pages) || 64;
    const blockBytes = pageSize * blockPages;

    const blockMap = new Map();
    for (const sp of layout.spans) {
      for (let i = 0; i < sp.page_count; i++) {
        const pIdx = sp.start_page + i;
        const foff = sp.file_offset_bytes + i * pageSize;
        const bIdx = Math.floor(foff / blockBytes);
        let blk = blockMap.get(bIdx);
        if (!blk) {
          blk = {
            blockIndex: bIdx,
            offsetStart: bIdx * blockBytes,
            offsetEnd: (bIdx + 1) * blockBytes,
            section: sp.section_name,
            perms: sp.vma_perms,
            pages: [],
          };
          blockMap.set(bIdx, blk);
        }
        blk.pages.push(pIdx);
      }
    }

    const blocks = Array.from(blockMap.values()).sort((a, b) => a.blockIndex - b.blockIndex);
    const padCSS = 14;
    const tileCSS = cssW >= 1100 ? 12 : 10;
    const gapCSS = 2;
    const devW = Math.max(1, Math.round(cssW * dpr));
    const padDev = Math.round(padCSS * dpr);
    const topDev = Math.round(28 * dpr);
    const tileDev = Math.max(4, Math.round(tileCSS * dpr));
    const gapDev = Math.max(1, Math.round(gapCSS * dpr));
    const pitchDev = tileDev + gapDev;
    const cols = Math.max(1, Math.floor((devW - padDev * 2) / pitchDev));
    const rows = Math.max(1, Math.ceil(blocks.length / cols));

    for (let i = 0; i < blocks.length; i++) {
      const r = Math.floor(i / cols);
      const c = i - r * cols;
      blocks[i].devX = padDev + c * pitchDev;
      blocks[i].devY = topDev + r * pitchDev;
      blocks[i].pagesU32 = Uint32Array.from(blocks[i].pages);
      delete blocks[i].pages;
    }

    const devH = topDev + rows * pitchDev + padDev;
    return {
      mode: "blocks",
      dpr,
      cssW,
      cssH: Math.round(devH / dpr),
      devW,
      devH,
      padDev,
      topDev,
      tileDev,
      gapDev,
      pitchDev,
      cols,
      blocks,
      blockBytes,
    };
  }

  function fillRectU32(buf, devW, x, y, w, h, col) {
    for (let ry = y; ry < y + h; ry++) {
      const rowOff = ry * devW + x;
      buf.fill(col, rowOff, rowOff + w);
    }
  }

  // Main Canvas Paint Routine
  function renderCanvas(nowPerf) {
    if (state.geomDirty) rebuildGeometry();
    const g = state.geom;
    if (!g || !state.imgU32) return;

    const ctx = dom.canvas.getContext("2d", { alpha: false });
    const buf = state.imgU32;
    buf.set(state.bgU32);

    const pages = getPagesAtFrame(state.viewIdx);
    let anyActiveFlash = false;

    if (g.mode === "pages") {
      const { devW, pageDevX, pageDevY, pageDevS } = g;
      const total = Math.min(pages.length, pageDevX.length);

      // 1. Base resting state pass over all binary pages.
      for (let i = 0; i < total; i++) {
        const col = RESTING_U32[pages[i]];
        const x = pageDevX[i];
        const y = pageDevY[i];
        const s = pageDevS[i];
        if (s === 2) {
          const r0 = y * devW + x;
          const r1 = r0 + devW;
          buf[r0] = col;
          buf[r0 + 1] = col;
          buf[r1] = col;
          buf[r1 + 1] = col;
        } else if (s === 3) {
          const r0 = y * devW + x;
          const r1 = r0 + devW;
          const r2 = r1 + devW;
          buf[r0] = buf[r0 + 1] = buf[r0 + 2] = col;
          buf[r1] = buf[r1 + 1] = buf[r1 + 2] = col;
          buf[r2] = buf[r2 + 1] = buf[r2 + 2] = col;
        } else {
          fillRectU32(buf, devW, x, y, s, s, col);
        }
      }

      // 2. Sparse transient flash pass over trailing 3 frames around viewIdx.
      if (state.viewIdx >= 0 && state.viewIdx < state.frames.length) {
        const startTrail = Math.max(0, state.viewIdx - 2);
        for (let f = startTrail; f <= state.viewIdx; f++) {
          const fr = state.frames[f];
          let intensity;
          if (state.followLive && isRunActive() && state.viewIdx === state.frames.length - 1) {
            const age = Math.max(0, nowPerf - fr.recvPerfMs);
            if (age >= LIVE_FLASH_MS) continue;
            intensity = 1 - age / LIVE_FLASH_MS;
            anyActiveFlash = true;
          } else {
            // Deterministic trailing highlight during replay/scrubbing so pulses stay visible while paused!
            const dist = state.viewIdx - f;
            intensity = dist === 0 ? 1.0 : dist === 1 ? 0.55 : 0.28;
          }

          const ds = fr.deltas;
          for (let j = 0; j < ds.length; j++) {
            const d = ds[j];
            const pIdx = d >>> 4;
            if (pIdx >= total) continue;
            const code = d & 0x0f;
            const flashCol = lerpColor(RESTING_RGB[pages[pIdx]], FLASH_RGB[code], intensity);
            fillRectU32(buf, devW, pageDevX[pIdx], pageDevY[pIdx], pageDevS[pIdx], pageDevS[pIdx], flashCol);
          }
        }
      }

      ctx.putImageData(state.imgData, 0, 0);

      // 3. Draw crisp vector section labels and live residency counts in left gutter.
      const dpr = g.dpr;
      ctx.save();
      ctx.textBaseline = "middle";
      const pageSize = (state.layout && state.layout.page_size_bytes) || 4096;

      for (const b of g.bands) {
        const sp = b.span;
        let resCount = 0;
        let evictCount = 0;
        const endP = Math.min(pages.length, sp.start_page + sp.page_count);
        for (let p = sp.start_page; p < endP; p++) {
          const c = pages[p];
          if (c === CODE_COLD_FAULT_FILE || c === CODE_COLD_FAULT_COW || c === CODE_PROMOTED_COW ||
              c === CODE_REFAULT_FILE_ONCE || c === CODE_REFAULT_FILE_THRASH || c === CODE_REFAULT_COW) {
            resCount++;
          } else if (c !== CODE_NEVER_FAULTED) {
            evictCount++;
          }
        }

        const xLeft = Math.round(10 * dpr);
        const midY = b.bandDevY + Math.min(Math.round(16 * dpr), Math.floor(b.bandDevH / 2));

        ctx.font = `600 ${Math.round(11.5 * dpr)}px "JetBrains Mono", ui-monospace, monospace`;
        ctx.fillStyle = "hsl(210, 22%, 92%)";
        const label = sp.section_name.length > 16 ? sp.section_name.slice(0, 15) + "…" : sp.section_name;
        ctx.fillText(label, xLeft, midY);

        ctx.font = `500 ${Math.round(9.5 * dpr)}px "JetBrains Mono", ui-monospace, monospace`;
        ctx.fillStyle = "hsl(212, 13%, 58%)";
        if (b.bandDevH >= Math.round(42 * dpr)) {
          ctx.fillText(`${sp.vma_perms} · ${fmtMiB(sp.page_count * pageSize)}`, xLeft, midY + Math.round(13 * dpr));
          ctx.fillStyle = resCount > 0 ? "hsl(202, 82%, 68%)" : "hsl(212, 13%, 50%)";
          ctx.fillText(
            `res ${fmtMiB(resCount * pageSize)} · ev ${fmtMiB(evictCount * pageSize)}`,
            xLeft,
            midY + Math.round(25 * dpr)
          );
        } else {
          const tagX = xLeft + Math.round(92 * dpr);
          ctx.fillText(`${sp.vma_perms} ${resCount}/${sp.page_count}`, tagX, midY);
        }
      }
      ctx.restore();
    } else {
      // 256 KiB Blocks Mode
      const { devW, tileDev, blocks } = g;
      for (let i = 0; i < blocks.length; i++) {
        const blk = blocks[i];
        const ps = blk.pagesU32;
        let resFile = 0, resCow = 0, evClean = 0, ref1 = 0, thrash = 0;
        for (let k = 0; k < ps.length; k++) {
          const c = pages[ps[k]];
          if (c === CODE_REFAULT_FILE_THRASH || c === CODE_EVICTED_THRASHING) thrash++;
          else if (c === CODE_REFAULT_FILE_ONCE || c === CODE_REFAULT_COW || c === CODE_EVICTED_REFAULTED) ref1++;
          else if (c === CODE_COLD_FAULT_COW || c === CODE_PROMOTED_COW) resCow++;
          else if (c === CODE_COLD_FAULT_FILE) resFile++;
          else if (c === CODE_EVICTED_CLEAN) evClean++;
        }

        let colCode = CODE_NEVER_FAULTED;
        if (thrash > 0) colCode = CODE_REFAULT_FILE_THRASH;
        else if (ref1 > 0) colCode = CODE_REFAULT_FILE_ONCE;
        else if (resCow > 0 && resCow >= resFile) colCode = CODE_COLD_FAULT_COW;
        else if (resFile > 0) colCode = CODE_COLD_FAULT_FILE;
        else if (evClean > 0) colCode = CODE_EVICTED_CLEAN;

        fillRectU32(buf, devW, blk.devX, blk.devY, tileDev, tileDev, RESTING_U32[colCode]);
      }
      ctx.putImageData(state.imgData, 0, 0);

      ctx.save();
      ctx.font = `600 ${Math.round(11 * g.dpr)}px "JetBrains Mono", ui-monospace, monospace`;
      ctx.fillStyle = "hsl(212, 15%, 66%)";
      ctx.fillText(
        `${blocks.length} contiguous ${fmtMiB(g.blockBytes)} binary file-offset blocks (hover any tile for composition)`,
        g.padDev,
        Math.round(17 * g.dpr)
      );
      ctx.restore();
    }

    state.canvasDirty = anyActiveFlash;
  }

  // Timeline Activity Sparkline Renderer
  function renderTimelineSpark() {
    state.timelineDirty = false;
    const c = dom.sparkCanvas;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    const cssW = Math.max(200, Math.floor(c.clientWidth || 600));
    const cssH = 58;
    const devW = Math.round(cssW * dpr);
    const devH = Math.round(cssH * dpr);
    if (c.width !== devW || c.height !== devH) {
      c.width = devW;
      c.height = devH;
    }

    const ctx = c.getContext("2d");
    ctx.clearRect(0, 0, devW, devH);

    const n = state.frames.length;
    if (n === 0) {
      if (dom.sparkKeyIso) dom.sparkKeyIso.style.display = "none";
      ctx.font = `${Math.round(11 * dpr)}px "JetBrains Mono", ui-monospace, monospace`;
      ctx.fillStyle = "hsl(213, 12%, 46%)";
      ctx.fillText("Timeline frame history & Kubelet Working Set trace will stream here during benchmark execution", Math.round(6 * dpr), Math.round(32 * dpr));
      return;
    }

    let maxDelta = 8;
    let maxMem = 16 * 1024 * 1024;
    let anyIsoDivergence = false;
    for (let i = 0; i < n; i++) {
      const r = state.frames[i].row;
      const tot = (r.delta_cold_faults || 0) + (r.delta_evictions || 0) + (r.delta_refaults_total || 0);
      if (tot > maxDelta) maxDelta = tot;
      const cur = r.cg_memory_current_bytes || 0;
      const iso = calcEstIsolatedWS(r);
      if (cur > maxMem) maxMem = cur;
      if (iso > maxMem) maxMem = iso;
      if (calcUnchargedRSSFile(r) >= 1024 * 1024) anyIsoDivergence = true;
    }
    if (dom.sparkKeyIso) {
      dom.sparkKeyIso.style.display = anyIsoDivergence ? "inline-flex" : "none";
    }

    // Use sqrt compression so medium runtime bursts stay visible next to the massive startup cold-fault spike.
    const scaleY = (v) => (Math.sqrt(v) / Math.sqrt(maxDelta)) * (devH - Math.round(8 * dpr));
    const barW = Math.max(1, devW / n);

    for (let i = 0; i < n; i++) {
      const r = state.frames[i].row;
      const x = n === 1 ? 0 : (i / (n - 1)) * (devW - barW);
      let yBottom = devH;

      const cold = r.delta_cold_faults || 0;
      const evict = r.delta_evictions || 0;
      const refault = r.delta_refaults_total || 0;

      if (cold > 0) {
        const h = Math.max(1, scaleY(cold));
        ctx.fillStyle = "hsla(192, 92%, 68%, 0.45)";
        ctx.fillRect(x, yBottom - h, Math.max(1.5, barW), h);
        yBottom -= h;
      }
      if (evict > 0) {
        const h = Math.max(1, scaleY(evict));
        ctx.fillStyle = "hsla(156, 82%, 60%, 0.45)";
        ctx.fillRect(x, Math.max(0, yBottom - h), Math.max(1.5, barW), h);
      }
      if (refault > 0) {
        const h = Math.max(2, scaleY(refault));
        ctx.fillStyle = "hsla(8, 90%, 62%, 0.72)";
        ctx.fillRect(x, 0, Math.max(2, barW), h);
      }
    }

    // Overlay memory polylines: memory.current, optional Est. Iso WS, and Kubelet Working Set.
    const padTop = Math.round(4 * dpr);
    const padBot = Math.round(3 * dpr);
    const memY = (bytes) =>
      devH - padBot - Math.max(0, Math.min(1, bytes / maxMem)) * (devH - padTop - padBot);
    const strokeMemTrace = (valFn, strokeStyle, widthPx, dash) => {
      ctx.save();
      ctx.strokeStyle = strokeStyle;
      ctx.lineWidth = Math.max(1, widthPx);
      if (dash) ctx.setLineDash(dash);
      ctx.beginPath();
      for (let i = 0; i < n; i++) {
        const x = n === 1 ? devW / 2 : (i / (n - 1)) * devW;
        const y = memY(valFn(state.frames[i].row));
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      }
      if (n === 1) {
        const y = memY(valFn(state.frames[0].row));
        ctx.moveTo(0, y);
        ctx.lineTo(devW, y);
      }
      ctx.stroke();
      ctx.restore();
    };

    strokeMemTrace((r) => r.cg_memory_current_bytes || 0, "hsla(210, 22%, 82%, 0.58)", Math.round(1.25 * dpr), null);
    if (anyIsoDivergence) {
      strokeMemTrace(calcEstIsolatedWS, "hsla(188, 92%, 70%, 0.88)", Math.round(1.5 * dpr), [Math.round(4 * dpr), Math.round(3 * dpr)]);
    }
    strokeMemTrace(calcCgroupWS, "hsl(160, 82%, 56%)", Math.round(2 * dpr), null);

    // Draw Markers (Ready, limit_change, reclaim)
    for (const mk of state.markers) {
      let bestIdx = 0;
      for (let i = 0; i < n; i++) {
        if ((state.frames[i].row.elapsed_ms || 0) >= mk.elapsed_ms) {
          bestIdx = i;
          break;
        }
        bestIdx = i;
      }
      const mx = n <= 1 ? 0 : (bestIdx / (n - 1)) * devW;
      ctx.save();
      ctx.strokeStyle =
        mk.kind === "ready"
          ? "hsla(202, 90%, 72%, 0.9)"
          : mk.kind === "reclaim"
            ? "hsla(156, 85%, 68%, 0.9)"
            : "hsla(38, 95%, 66%, 0.9)";
      ctx.lineWidth = Math.max(1, Math.round(1.5 * dpr));
      ctx.setLineDash([Math.round(3 * dpr), Math.round(2 * dpr)]);
      ctx.beginPath();
      ctx.moveTo(mx, 0);
      ctx.lineTo(mx, devH);
      ctx.stroke();
      ctx.restore();
    }

    // Playhead cursor line
    if (state.viewIdx >= 0 && n > 1) {
      const px = (state.viewIdx / (n - 1)) * devW;
      ctx.fillStyle = "#ffffff";
      ctx.fillRect(Math.max(0, px - dpr), 0, Math.round(2 * dpr), devH);
    }
  }

  // Hover Hit-Testing & Viewport-Safe Tooltip
  function positionTooltip(clientX, clientY) {
    const tt = dom.tooltip;
    const rect = tt.getBoundingClientRect();
    const pad = 12;
    let left = clientX + 14;
    let top = clientY + 14;
    if (left + rect.width + pad > window.innerWidth) {
      left = Math.max(pad, clientX - rect.width - 14);
    }
    if (top + rect.height + pad > window.innerHeight) {
      top = Math.max(pad, clientY - rect.height - 14);
    }
    tt.style.left = left + "px";
    tt.style.top = top + "px";
  }

  function buildTooltipRow(label, val) {
    return `<div class="tt-row"><span>${label}</span><span class="tt-val">${val}</span></div>`;
  }

  function handleCanvasMove(ev) {
    const g = state.geom;
    if (!g || !state.layout) return;

    const rect = dom.canvas.getBoundingClientRect();
    const devX = (ev.clientX - rect.left) * g.dpr;
    const devY = (ev.clientY - rect.top) * g.dpr;
    const pages = getPagesAtFrame(state.viewIdx);
    const pageSize = Number(state.layout.page_size_bytes) || 4096;

    if (g.mode === "pages") {
      let hitBand = null;
      for (let i = 0; i < g.bands.length; i++) {
        const b = g.bands[i];
        if (devY >= b.gridYDev && devY < b.gridYDev + b.gridHDev && devX >= b.gridXDev) {
          hitBand = b;
          break;
        }
      }
      if (!hitBand) {
        dom.tooltip.classList.remove("visible");
        return;
      }
      const col = Math.floor((devX - hitBand.gridXDev) / hitBand.pitchDev);
      const row = Math.floor((devY - hitBand.gridYDev) / hitBand.pitchDev);
      if (col < 0 || col >= hitBand.cols || row < 0 || row >= hitBand.rows) {
        dom.tooltip.classList.remove("visible");
        return;
      }
      const inBand = row * hitBand.cols + col;
      if (inBand >= hitBand.span.page_count) {
        dom.tooltip.classList.remove("visible");
        return;
      }
      const pIdx = hitBand.span.start_page + inBand;
      const foff = hitBand.span.file_offset_bytes + inBand * pageSize;
      const code = pIdx < pages.length ? pages[pIdx] : 0;

      dom.tooltip.innerHTML =
        `<div class="tt-title">${hitBand.span.section_name} (${hitBand.span.vma_perms})</div>` +
        buildTooltipRow("Virtual Page Index", `#${fmtInt(pIdx)}`) +
        buildTooltipRow("ELF File Offset", fmtHex(foff)) +
        buildTooltipRow("Page Size", fmtMiB(pageSize)) +
        buildTooltipRow("Status", STATE_LABELS[code] || "Unknown");
      dom.tooltip.classList.add("visible");
      positionTooltip(ev.clientX, ev.clientY);
    } else {
      const col = Math.floor((devX - g.padDev) / g.pitchDev);
      const row = Math.floor((devY - g.topDev) / g.pitchDev);
      if (col < 0 || col >= g.cols || row < 0) {
        dom.tooltip.classList.remove("visible");
        return;
      }
      const bIdx = row * g.cols + col;
      if (bIdx < 0 || bIdx >= g.blocks.length) {
        dom.tooltip.classList.remove("visible");
        return;
      }
      const blk = g.blocks[bIdx];
      const ps = blk.pagesU32;
      let never = 0, resFile = 0, resCow = 0, evClean = 0, ref = 0, thrash = 0;
      for (let k = 0; k < ps.length; k++) {
        const c = pages[ps[k]];
        if (c === CODE_NEVER_FAULTED) never++;
        else if (c === CODE_COLD_FAULT_FILE) resFile++;
        else if (c === CODE_COLD_FAULT_COW || c === CODE_PROMOTED_COW) resCow++;
        else if (c === CODE_EVICTED_CLEAN) evClean++;
        else if (c === CODE_REFAULT_FILE_THRASH || c === CODE_EVICTED_THRASHING) thrash++;
        else ref++;
      }

      dom.tooltip.innerHTML =
        `<div class="tt-title">Block #${blk.blockIndex} · ${blk.section} (${blk.perms})</div>` +
        buildTooltipRow("File Offset Range", `${fmtHex(blk.offsetStart)} – ${fmtHex(blk.offsetEnd)}`) +
        buildTooltipRow("Mapped Pages", fmtInt(ps.length)) +
        buildTooltipRow("Resident (File / CoW)", `${resFile} / ${resCow}`) +
        buildTooltipRow("Cleanly Evicted", fmtInt(evClean)) +
        buildTooltipRow("Refaulted / Thrashing", `${ref} / ${thrash}`) +
        buildTooltipRow("Never Faulted", fmtInt(never));
      dom.tooltip.classList.add("visible");
      positionTooltip(ev.clientX, ev.clientY);
    }
  }

  // API Action Handlers
  async function postJSON(url, body) {
    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      throw new Error(data.error || `HTTP ${res.status}`);
    }
    return data;
  }

  function syncChipHighlights() {
    const limVal = dom.inputLimit.value.trim().toLowerCase();
    dom.limitChips.forEach((btn) => {
      btn.classList.toggle("active", btn.dataset.limit.toLowerCase() === limVal);
    });
    const durVal = dom.inputDuration.value.trim().toLowerCase();
    dom.durChips.forEach((btn) => {
      btn.classList.toggle("active", btn.dataset.dur.toLowerCase() === durVal);
    });
  }

  function bindEvents() {
    dom.limitChips.forEach((btn) => {
      btn.addEventListener("click", () => {
        dom.inputLimit.value = btn.dataset.limit;
        syncChipHighlights();
      });
    });
    dom.durChips.forEach((btn) => {
      btn.addEventListener("click", () => {
        dom.inputDuration.value = btn.dataset.dur;
        syncChipHighlights();
      });
    });
    dom.inputLimit.addEventListener("input", syncChipHighlights);
    dom.inputDuration.addEventListener("input", syncChipHighlights);

    dom.launchForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      if (isRunActive()) return;
      dom.btnStart.disabled = true;
      try {
        await postJSON("/api/run/start", {
          memory_limit: dom.inputLimit.value.trim(),
          duration: dom.inputDuration.value.trim(),
          swap_max: dom.inputSwap.value.trim(),
        });
      } catch (err) {
        showToast(err.message, "error");
        dom.btnStart.disabled = false;
      }
    });

    dom.btnStop.addEventListener("click", async () => {
      dom.btnStop.disabled = true;
      try {
        await postJSON("/api/run/stop", {});
      } catch (err) {
        showToast(err.message, "error");
      }
    });

    dom.inputLiveLimit.addEventListener("input", updateOOMRiskIndicator);
    dom.inputLiveLimit.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        dom.btnApplyLiveLimit.click();
      }
    });

    dom.btnApplyLiveLimit.addEventListener("click", async () => {
      try {
        await postJSON("/api/run/memory-max", { memory_limit: dom.inputLiveLimit.value.trim() });
      } catch (err) {
        showToast(err.message, "error");
      }
    });

    dom.liveStepBtns.forEach((btn) => {
      btn.addEventListener("click", async () => {
        let targetRaw = btn.dataset.setLimit || "";
        if (!targetRaw) {
          const deltaMiB = parseFloat(btn.dataset.deltaMib || "0");
          const curParsed = parseClientLimit(dom.inputLiveLimit.value);
          const latest = state.frames.length > 0 ? state.frames[state.frames.length - 1].row : null;
          let baseBytes =
            curParsed && !curParsed.unlimited && curParsed.bytes > 0
              ? curParsed.bytes
              : latest && latest.cg_memory_current_bytes
                ? latest.cg_memory_current_bytes
                : 80 * 1024 * 1024;
          const nextMiB = Math.max(4, Math.round((baseBytes + deltaMiB * 1024 * 1024) / (1024 * 1024)));
          targetRaw = `${nextMiB}MiB`;
        }
        dom.inputLiveLimit.value = targetRaw;
        updateOOMRiskIndicator();
        try {
          await postJSON("/api/run/memory-max", { memory_limit: targetRaw });
        } catch (err) {
          showToast(err.message, "error");
        }
      });
    });

    dom.reclaimBtns.forEach((btn) => {
      btn.addEventListener("click", async () => {
        try {
          await postJSON("/api/run/reclaim", { amount: btn.dataset.reclaim });
        } catch (err) {
          showToast(err.message, "error");
        }
      });
    });

    // Resolution Toggle
    const setMode = (mode) => {
      if (state.viewMode === mode) return;
      state.viewMode = mode;
      dom.btnModePages.classList.toggle("active", mode === "pages");
      dom.btnModeBlocks.classList.toggle("active", mode === "blocks");
      dom.tooltip.classList.remove("visible");
      state.geomDirty = true;
      state.canvasDirty = true;
    };
    dom.btnModePages.addEventListener("click", () => setMode("pages"));
    dom.btnModeBlocks.addEventListener("click", () => setMode("blocks"));

    // Canvas hover
    dom.canvas.addEventListener("mousemove", handleCanvasMove);
    dom.canvas.addEventListener("mouseleave", () => dom.tooltip.classList.remove("visible"));

    // Scrubber & Slow-Mo Replay Controls
    dom.slider.addEventListener("input", () => {
      const idx = parseInt(dom.slider.value, 10);
      if (!Number.isFinite(idx)) return;
      state.followLive = false;
      state.isReplaying = false;
      state.viewIdx = Math.max(0, Math.min(state.frames.length - 1, idx));
      state.canvasDirty = true;
      state.timelineDirty = true;
      updateControlsUI();
      updateTelemetryUI();
    });

    dom.btnFollowLive.addEventListener("click", () => {
      state.followLive = true;
      state.isReplaying = false;
      if (state.frames.length > 0) {
        state.viewIdx = state.frames.length - 1;
      }
      state.canvasDirty = true;
      state.timelineDirty = true;
      updateControlsUI();
      updateTelemetryUI();
    });

    dom.btnPlayPause.addEventListener("click", () => {
      if (state.frames.length < 2) return;
      if (state.isReplaying) {
        state.isReplaying = false;
      } else {
        state.followLive = false;
        if (state.viewIdx >= state.frames.length - 1 || state.viewIdx < 0) {
          state.viewIdx = 0;
        }
        state.replayAccumElapsedMs = state.frames[state.viewIdx].row.elapsed_ms || 0;
        state.lastRafPerfMs = performance.now();
        state.isReplaying = true;
        state.canvasDirty = true;
        state.timelineDirty = true;
      }
      updateControlsUI();
      updateTelemetryUI();
    });

    dom.btnStepPrev.addEventListener("click", () => {
      if (state.viewIdx > 0) {
        state.followLive = false;
        state.isReplaying = false;
        state.viewIdx--;
        state.canvasDirty = true;
        state.timelineDirty = true;
        updateControlsUI();
        updateTelemetryUI();
      }
    });

    dom.btnStepNext.addEventListener("click", () => {
      if (state.viewIdx < state.frames.length - 1) {
        state.followLive = false;
        state.isReplaying = false;
        state.viewIdx++;
        state.canvasDirty = true;
        state.timelineDirty = true;
        updateControlsUI();
        updateTelemetryUI();
      }
    });

    dom.selectSpeed.addEventListener("change", () => {
      const sp = parseFloat(dom.selectSpeed.value);
      if (Number.isFinite(sp) && sp > 0) {
        state.replaySpeed = sp;
      }
    });

    let resizeTimer = null;
    window.addEventListener("resize", () => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        state.geomDirty = true;
        state.canvasDirty = true;
        state.timelineDirty = true;
      }, 90);
    });
  }

  // Animation & Slow-Motion Playback Loop
  function rafLoop(nowPerf) {
    if (state.isReplaying && state.frames.length >= 2) {
      const dtWall = Math.min(100, Math.max(0, nowPerf - state.lastRafPerfMs));
      state.lastRafPerfMs = nowPerf;
      state.replayAccumElapsedMs += dtWall * state.replaySpeed;

      let advanced = false;
      while (
        state.viewIdx < state.frames.length - 1 &&
        (state.frames[state.viewIdx + 1].row.elapsed_ms || 0) <= state.replayAccumElapsedMs
      ) {
        state.viewIdx++;
        advanced = true;
      }

      // Guard against zero/duplicate elapsed_ms intervals stalling replay.
      if (!advanced && state.viewIdx < state.frames.length - 1) {
        const nextElapsed = state.frames[state.viewIdx + 1].row.elapsed_ms || 0;
        const curElapsed = state.frames[state.viewIdx].row.elapsed_ms || 0;
        if (nextElapsed < curElapsed) {
          state.viewIdx++;
          state.replayAccumElapsedMs = state.frames[state.viewIdx].row.elapsed_ms || 0;
          advanced = true;
        }
      }

      if (advanced) {
        state.canvasDirty = true;
        state.timelineDirty = true;
        updateTelemetryUI();
      }
      if (state.viewIdx >= state.frames.length - 1 && !isRunActive()) {
        state.isReplaying = false;
        updateControlsUI();
      }
    } else {
      state.lastRafPerfMs = nowPerf;
    }

    if (state.geomDirty || state.canvasDirty) {
      renderCanvas(nowPerf);
    }
    if (state.timelineDirty) {
      renderTimelineSpark();
    }

    requestAnimationFrame(rafLoop);
  }

  // Boot
  bindEvents();
  syncFullState().then(() => {
    connectSSE();
    requestAnimationFrame(rafLoop);
  });
})();
