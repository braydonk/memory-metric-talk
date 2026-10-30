package results

import "time"

const (
	SamplesCSVName   = "samples.csv"
	SectionsCSVName  = "sections.csv"
	BlocksCSVName    = "blocks.csv"
	RunMetaJSONName  = "run_meta.json"
	CollectorLogName = "collector.log"
)

// Trigger constants describing why a sample snapshot was captured.
const (
	TriggerStart            = "start"
	TriggerReclaimEvent     = "reclaim_event"
	TriggerReclaimDebounced = "reclaim_debounced"
	TriggerPeriodic         = "periodic"
	TriggerFinal            = "final"
)

// Verdict constants classifying the overall outcome of a lab run.
const (
	VerdictCleanReclaim = "clean_reclaim"
	VerdictThrashing    = "thrashing"
	VerdictOOMKilled    = "oom_killed"
	VerdictNoReclaim    = "no_reclaim"
	VerdictProcessError = "process_error"
	VerdictStopped      = "stopped"
)

// SampleRow represents one row in samples.csv.
type SampleRow struct {
	TimestampUnixMs int64  `json:"timestamp_unix_ms"`
	ElapsedMs       int64  `json:"elapsed_ms"`
	Trigger         string `json:"trigger"`

	// Aggregate Collector binary page residency & history (in pages).
	BinTotalPages          uint64 `json:"bin_total_pages"`
	BinPresentFilePages    uint64 `json:"bin_present_file_pages"`
	BinPresentCoWPages     uint64 `json:"bin_present_cow_pages"`
	BinSwappedPages        uint64 `json:"bin_swapped_pages"`
	BinNeverFaultedPages   uint64 `json:"bin_never_faulted_pages"`
	BinEvictedOutPages     uint64 `json:"bin_evicted_out_pages"`
	BinCleanlyEvictedPages uint64 `json:"bin_cleanly_evicted_pages"`
	BinEverFaultedPages    uint64 `json:"bin_ever_faulted_pages"`
	BinEverEvictedPages    uint64 `json:"bin_ever_evicted_pages"`
	BinEverRefaultedPages  uint64 `json:"bin_ever_refaulted_pages"`
	BinThrashingPages      uint64 `json:"bin_thrashing_pages"`

	// Interval deltas & cumulative binary page transitions (in pages).
	DeltaColdFaults    uint64 `json:"delta_cold_faults"`
	DeltaEvictions     uint64 `json:"delta_evictions"`
	DeltaRefaults01    uint64 `json:"delta_refaults_01"`
	DeltaRefaultsPFN   uint64 `json:"delta_refaults_pfn"`
	DeltaRefaultsTotal uint64 `json:"delta_refaults_total"`
	CumColdFaults      uint64 `json:"cum_cold_faults"`
	CumEvictions       uint64 `json:"cum_evictions"`
	CumRefaults01      uint64 `json:"cum_refaults_01"`
	CumRefaultsPFN     uint64 `json:"cum_refaults_pfn"`
	CumRefaultsTotal   uint64 `json:"cum_refaults_total"`

	// Per-VMA permission class breakdown (ro = r--p, rx = r-xp, rw = rw-p).
	ROPresentPages     uint64 `json:"ro_present_pages"`
	ROEvictedOutPages  uint64 `json:"ro_evicted_out_pages"`
	ROCumRefaults      uint64 `json:"ro_cum_refaults"`
	RXPresentPages     uint64 `json:"rx_present_pages"`
	RXEvictedOutPages  uint64 `json:"rx_evicted_out_pages"`
	RXCumRefaults      uint64 `json:"rx_cum_refaults"`
	RWPresentFilePages uint64 `json:"rw_present_file_pages"`
	RWPresentCoWPages  uint64 `json:"rw_present_cow_pages"`
	RWEvictedOutPages  uint64 `json:"rw_evicted_out_pages"`
	RWCumRefaults      uint64 `json:"rw_cum_refaults"`

	// Cgroup v2 memory telemetry.
	CGMemoryCurrentBytes     uint64 `json:"cg_memory_current_bytes"`
	CGAnonBytes              uint64 `json:"cg_anon_bytes"`
	CGFileBytes              uint64 `json:"cg_file_bytes"`
	CGActiveFileBytes        uint64 `json:"cg_active_file_bytes"`
	CGInactiveFileBytes      uint64 `json:"cg_inactive_file_bytes"`
	CGKernelBytes            uint64 `json:"cg_kernel_bytes"`
	CGPgfault                uint64 `json:"cg_pgfault"`
	CGPgmajfault             uint64 `json:"cg_pgmajfault"`
	CGWorkingsetRefaultFile  uint64 `json:"cg_workingset_refault_file"`
	CGWorkingsetActivateFile uint64 `json:"cg_workingset_activate_file"`
	CGEventsLow              uint64 `json:"cg_events_low"`
	CGEventsHigh             uint64 `json:"cg_events_high"`
	CGEventsMax              uint64 `json:"cg_events_max"`
	CGEventsOOM              uint64 `json:"cg_events_oom"`
	CGEventsOOMKill          uint64 `json:"cg_events_oom_kill"`
	CGPSISomeTotalUs         uint64 `json:"cg_psi_some_total_us"`
	CGPSIFullTotalUs         uint64 `json:"cg_psi_full_total_us"`

	// Process /proc/[pid]/status memory telemetry.
	ProcRSSBytes      uint64 `json:"proc_rss_bytes"`
	ProcRSSAnonBytes  uint64 `json:"proc_rss_anon_bytes"`
	ProcRSSFileBytes  uint64 `json:"proc_rss_file_bytes"`
	ProcRSSShmemBytes uint64 `json:"proc_rss_shmem_bytes"`
	ProcHWMBytes      uint64 `json:"proc_hwm_bytes"`
	ProcSwapBytes     uint64 `json:"proc_swap_bytes"`

	// Simulated Kubelet cgroup v2 working_set_bytes telemetry.
	CGWorkingSetBytes          uint64 `json:"cg_working_set_bytes"`
	EstIsolatedWorkingSetBytes uint64 `json:"est_isolated_working_set_bytes"`
}

// KubeletWorkingSetBytes computes Kubernetes container working_set_bytes on Linux cgroup v2
// (matching cAdvisor setMemoryStats and runc/libcontainer/cgroups/fs2):
//
//	max(0, memory.current - memory.stat[inactive_file])
func KubeletWorkingSetBytes(currentBytes, inactiveFileBytes uint64) uint64 {
	if currentBytes <= inactiveFileBytes {
		return 0
	}
	return currentBytes - inactiveFileBytes
}

// UnchargedRSSFileBytes returns mapped file-backed RSS from /proc/[pid]/status that exceeds
// the leaf cgroup's memory.stat[file] charge due to foreign/shared first-touch page cache ownership.
func UnchargedRSSFileBytes(procRSSFileBytes, cgFileBytes uint64) uint64 {
	if procRSSFileBytes <= cgFileBytes {
		return 0
	}
	return procRSSFileBytes - cgFileBytes
}

// PopulateWorkingSet populates CGWorkingSetBytes and EstIsolatedWorkingSetBytes from raw cgroup and proc fields.
func (r *SampleRow) PopulateWorkingSet() {
	r.CGWorkingSetBytes = KubeletWorkingSetBytes(r.CGMemoryCurrentBytes, r.CGInactiveFileBytes)
	r.EstIsolatedWorkingSetBytes = r.CGWorkingSetBytes + UnchargedRSSFileBytes(r.ProcRSSFileBytes, r.CGFileBytes)
}

// SectionRow represents one row in sections.csv summarizing end-of-run outcomes per mapped ELF section.
type SectionRow struct {
	SectionName               string `json:"section_name"`
	VMAPerms                  string `json:"vma_perms"`
	FileOffsetBytes           uint64 `json:"file_offset_bytes"`
	SizeBytes                 uint64 `json:"size_bytes"`
	TotalPages                uint64 `json:"total_pages"`
	NeverFaultedPages         uint64 `json:"never_faulted_pages"`
	PresentFinalPages         uint64 `json:"present_final_pages"`
	PresentCoWFinalPages      uint64 `json:"present_cow_final_pages"`
	CleanlyEvictedPages       uint64 `json:"cleanly_evicted_pages"`
	EvictedAfterRefaultPages  uint64 `json:"evicted_after_refault_pages"`
	ResidentAfterRefaultPages uint64 `json:"resident_after_refault_pages"`
	ThrashingPages            uint64 `json:"thrashing_pages"`
	TotalEvictions            uint64 `json:"total_evictions"`
	TotalRefaults01           uint64 `json:"total_refaults_01"`
	TotalRefaultsPFN          uint64 `json:"total_refaults_pfn"`
	TotalRefaults             uint64 `json:"total_refaults"`
}

// BlockRow represents one row in blocks.csv summarizing contiguous file-offset buckets across the Collector binary.
type BlockRow struct {
	BlockIndex           int    `json:"block_index"`
	FileOffsetStartBytes uint64 `json:"file_offset_start_bytes"`
	FileOffsetEndBytes   uint64 `json:"file_offset_end_bytes"`
	PrimarySection       string `json:"primary_section"`
	VMAPerms             string `json:"vma_perms"`
	TotalPages           uint64 `json:"total_pages"`
	NeverFaultedPages    uint64 `json:"never_faulted_pages"`
	PresentFinalPages    uint64 `json:"present_final_pages"`
	PresentCoWFinalPages uint64 `json:"present_cow_final_pages"`
	CleanlyEvictedPages  uint64 `json:"cleanly_evicted_pages"`
	EverRefaultedPages   uint64 `json:"ever_refaulted_pages"`
	ThrashingPages       uint64 `json:"thrashing_pages"`
	TotalEvictions       uint64 `json:"total_evictions"`
	TotalRefaults        uint64 `json:"total_refaults"`
}

// WorkloadSummary captures total traffic sent and received when workload/backend are enabled.
type WorkloadSummary struct {
	BackendEnabled       bool   `json:"backend_enabled"`
	WorkloadEnabled      bool   `json:"workload_enabled"`
	StartupReadinessMs   int64  `json:"startup_readiness_ms,omitempty"`
	GeneratorItemsSent   uint64 `json:"generator_items_sent,omitempty"`
	GeneratorBatchesSent uint64 `json:"generator_batches_sent,omitempty"`
	GeneratorErrors      uint64 `json:"generator_errors,omitempty"`
	BackendItemsReceived uint64 `json:"backend_items_received,omitempty"`
	BackendRequests      uint64 `json:"backend_requests,omitempty"`
}

// FinalSummary holds headline aggregate KPIs written into run_meta.json.
type FinalSummary struct {
	Verdict                      string  `json:"verdict"`
	VerdictReason                string  `json:"verdict_reason"`
	PeakBinResidentBytes         uint64  `json:"peak_bin_resident_bytes"`
	FinalBinResidentBytes        uint64  `json:"final_bin_resident_bytes"`
	EverFaultedBinBytes          uint64  `json:"ever_faulted_bin_bytes"`
	CleanlyEvictedBinBytes       uint64  `json:"cleanly_evicted_bin_bytes"`
	CleanReclaimPercentOfFaulted float64 `json:"clean_reclaim_percent_of_faulted"`
	PeakCGMemoryBytes            uint64  `json:"peak_cg_memory_bytes"`
	FinalCGMemoryBytes           uint64  `json:"final_cg_memory_bytes"`
	PeakCGAnonBytes              uint64  `json:"peak_cg_anon_bytes"`
	PeakCGFileBytes              uint64  `json:"peak_cg_file_bytes"`
	FinalCGActiveFileBytes       uint64  `json:"final_cg_active_file_bytes"`
	FinalCGInactiveFileBytes     uint64  `json:"final_cg_inactive_file_bytes"`
	PeakCGWorkingSetBytes        uint64  `json:"peak_cg_working_set_bytes"`
	FinalCGWorkingSetBytes       uint64  `json:"final_cg_working_set_bytes"`
	PeakEstIsolatedWSBytes       uint64  `json:"peak_est_isolated_ws_bytes"`
	FinalEstIsolatedWSBytes      uint64  `json:"final_est_isolated_ws_bytes"`
	PeakProcRSSBytes             uint64  `json:"peak_proc_rss_bytes"`
	FinalProcRSSBytes            uint64  `json:"final_proc_rss_bytes"`
	TotalColdFaults              uint64  `json:"total_cold_faults"`
	TotalEvictions               uint64  `json:"total_evictions"`
	TotalRefaults01              uint64  `json:"total_refaults_01"`
	TotalRefaultsPFN             uint64  `json:"total_refaults_pfn"`
	TotalRefaults                uint64  `json:"total_refaults"`
	EverRefaultedPages           uint64  `json:"ever_refaulted_pages"`
	ThrashingPages               uint64  `json:"thrashing_pages"`
	TotalCGEventsMax             uint64  `json:"total_cg_events_max"`
	TotalCGPgmajfault            uint64  `json:"total_cg_pgmajfault"`
	TotalCGWorkingsetRefaultFile uint64  `json:"total_cg_workingset_refault_file"`
	TotalPSISomeMs               float64 `json:"total_psi_some_ms"`
	TotalPSIFullMs               float64 `json:"total_psi_full_ms"`
}

// RunMeta represents run_meta.json written at run completion.
type RunMeta struct {
	Name                     string          `json:"name"`
	StartTime                time.Time       `json:"start_time"`
	EndTime                  time.Time       `json:"end_time"`
	DurationMs               int64           `json:"duration_ms"`
	ConfiguredDuration       string          `json:"configured_duration"`
	CollectorBinary          string          `json:"collector_binary"`
	CollectorBinarySizeBytes uint64          `json:"collector_binary_size_bytes"`
	CollectorBinarySHA256    string          `json:"collector_binary_sha256"`
	CollectorConfig          string          `json:"collector_config"`
	MemoryLimitRaw           string          `json:"memory_limit_raw"`
	MemoryLimitHuman         string          `json:"memory_limit_human"`
	MemoryLimitUnlimited     bool            `json:"memory_limit_unlimited"`
	MemoryLimitBytes         uint64          `json:"memory_limit_bytes,omitempty"`
	SwapMaxRaw               string          `json:"swap_max_raw"`
	PageSizeBytes            uint64          `json:"page_size_bytes"`
	BlockSizePages           int             `json:"block_size_pages"`
	TotalBinaryPages         uint64          `json:"total_binary_pages"`
	TotalBinaryMappedBytes   uint64          `json:"total_binary_mapped_bytes"`
	SampleCount              int             `json:"sample_count"`
	ReclaimEventSamples      int             `json:"reclaim_event_samples"`
	WarmCachePagesBefore     int             `json:"warm_cache_pages_before"`
	WarmCachePagesAfter      int             `json:"warm_cache_pages_after"`
	ExitCode                 int             `json:"exit_code"`
	ExitSignal               string          `json:"exit_signal,omitempty"`
	OOMKilled                bool            `json:"oom_killed"`
	Workload                 WorkloadSummary `json:"workload"`
	Summary                  FinalSummary    `json:"summary"`
}

// RunData bundles all parsed files from a single result directory for the visualizer.
type RunData struct {
	Dir      string       `json:"dir"`
	Meta     RunMeta      `json:"meta"`
	Samples  []SampleRow  `json:"samples"`
	Sections []SectionRow `json:"sections"`
	Blocks   []BlockRow   `json:"blocks"`
}
