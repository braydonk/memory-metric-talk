package results

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

var samplesCSVHeader = []string{
	"timestamp_unix_ms", "elapsed_ms", "trigger",
	"bin_total_pages", "bin_present_file_pages", "bin_present_cow_pages", "bin_swapped_pages",
	"bin_never_faulted_pages", "bin_evicted_out_pages", "bin_cleanly_evicted_pages",
	"bin_ever_faulted_pages", "bin_ever_evicted_pages", "bin_ever_refaulted_pages", "bin_thrashing_pages",
	"delta_cold_faults", "delta_evictions", "delta_refaults_01", "delta_refaults_pfn", "delta_refaults_total",
	"cum_cold_faults", "cum_evictions", "cum_refaults_01", "cum_refaults_pfn", "cum_refaults_total",
	"ro_present_pages", "ro_evicted_out_pages", "ro_cum_refaults",
	"rx_present_pages", "rx_evicted_out_pages", "rx_cum_refaults",
	"rw_present_file_pages", "rw_present_cow_pages", "rw_evicted_out_pages", "rw_cum_refaults",
	"cg_memory_current_bytes", "cg_anon_bytes", "cg_file_bytes", "cg_active_file_bytes", "cg_inactive_file_bytes", "cg_kernel_bytes",
	"cg_pgfault", "cg_pgmajfault", "cg_workingset_refault_file", "cg_workingset_activate_file",
	"cg_events_low", "cg_events_high", "cg_events_max", "cg_events_oom", "cg_events_oom_kill",
	"cg_psi_some_total_us", "cg_psi_full_total_us",
	"proc_rss_bytes", "proc_rss_anon_bytes", "proc_rss_file_bytes", "proc_rss_shmem_bytes", "proc_hwm_bytes", "proc_swap_bytes",
	"cg_working_set_bytes", "est_isolated_working_set_bytes", "cg_file_mapped_bytes",
}

var sectionsCSVHeader = []string{
	"section_name", "vma_perms", "file_offset_bytes", "size_bytes", "total_pages",
	"never_faulted_pages", "present_final_pages", "present_cow_final_pages",
	"cleanly_evicted_pages", "evicted_after_refault_pages", "resident_after_refault_pages", "thrashing_pages",
	"total_evictions", "total_refaults_01", "total_refaults_pfn", "total_refaults",
}

var blocksCSVHeader = []string{
	"block_index", "file_offset_start_bytes", "file_offset_end_bytes", "primary_section", "vma_perms",
	"total_pages", "never_faulted_pages", "present_final_pages", "present_cow_final_pages",
	"cleanly_evicted_pages", "ever_refaulted_pages", "thrashing_pages", "total_evictions", "total_refaults",
}

// Writer streams samples.csv and writes end-of-run sections.csv, blocks.csv, and run_meta.json.
type Writer struct {
	mu         sync.Mutex
	dir        string
	samplesF   *os.File
	samplesCSV *csv.Writer
}

// NewWriter creates resultsDir if needed and opens samples.csv with its header row.
func NewWriter(resultsDir string) (*Writer, error) {
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create results dir %q: %w", resultsDir, err)
	}
	f, err := os.OpenFile(filepath.Join(resultsDir, SamplesCSVName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", SamplesCSVName, err)
	}
	cw := csv.NewWriter(f)
	if err := cw.Write(samplesCSVHeader); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write %s header: %w", SamplesCSVName, err)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flush %s header: %w", SamplesCSVName, err)
	}
	return &Writer{
		dir:        resultsDir,
		samplesF:   f,
		samplesCSV: cw,
	}, nil
}

// Dir returns the target results directory path.
func (w *Writer) Dir() string {
	return w.dir
}

// WriteSample appends one sample snapshot row to samples.csv and flushes it so partial runs remain readable.
func (w *Writer) WriteSample(r SampleRow) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	rec := []string{
		strconv.FormatInt(r.TimestampUnixMs, 10),
		strconv.FormatInt(r.ElapsedMs, 10),
		r.Trigger,
		u(r.BinTotalPages), u(r.BinPresentFilePages), u(r.BinPresentCoWPages), u(r.BinSwappedPages),
		u(r.BinNeverFaultedPages), u(r.BinEvictedOutPages), u(r.BinCleanlyEvictedPages),
		u(r.BinEverFaultedPages), u(r.BinEverEvictedPages), u(r.BinEverRefaultedPages), u(r.BinThrashingPages),
		u(r.DeltaColdFaults), u(r.DeltaEvictions), u(r.DeltaRefaults01), u(r.DeltaRefaultsPFN), u(r.DeltaRefaultsTotal),
		u(r.CumColdFaults), u(r.CumEvictions), u(r.CumRefaults01), u(r.CumRefaultsPFN), u(r.CumRefaultsTotal),
		u(r.ROPresentPages), u(r.ROEvictedOutPages), u(r.ROCumRefaults),
		u(r.RXPresentPages), u(r.RXEvictedOutPages), u(r.RXCumRefaults),
		u(r.RWPresentFilePages), u(r.RWPresentCoWPages), u(r.RWEvictedOutPages), u(r.RWCumRefaults),
		u(r.CGMemoryCurrentBytes), u(r.CGAnonBytes), u(r.CGFileBytes), u(r.CGActiveFileBytes), u(r.CGInactiveFileBytes), u(r.CGKernelBytes),
		u(r.CGPgfault), u(r.CGPgmajfault), u(r.CGWorkingsetRefaultFile), u(r.CGWorkingsetActivateFile),
		u(r.CGEventsLow), u(r.CGEventsHigh), u(r.CGEventsMax), u(r.CGEventsOOM), u(r.CGEventsOOMKill),
		u(r.CGPSISomeTotalUs), u(r.CGPSIFullTotalUs),
		u(r.ProcRSSBytes), u(r.ProcRSSAnonBytes), u(r.ProcRSSFileBytes), u(r.ProcRSSShmemBytes), u(r.ProcHWMBytes), u(r.ProcSwapBytes),
		u(r.CGWorkingSetBytes), u(r.EstIsolatedWorkingSetBytes), u(r.CGFileMappedBytes),
	}
	if err := w.samplesCSV.Write(rec); err != nil {
		return err
	}
	w.samplesCSV.Flush()
	return w.samplesCSV.Error()
}

// WriteSections writes sections.csv from the finalized ELF section rollup.
func (w *Writer) WriteSections(rows []SectionRow) error {
	f, err := os.OpenFile(filepath.Join(w.dir, SectionsCSVName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", SectionsCSVName, err)
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	if err := cw.Write(sectionsCSVHeader); err != nil {
		return err
	}
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	for _, r := range rows {
		rec := []string{
			r.SectionName, r.VMAPerms, u(r.FileOffsetBytes), u(r.SizeBytes), u(r.TotalPages),
			u(r.NeverFaultedPages), u(r.PresentFinalPages), u(r.PresentCoWFinalPages),
			u(r.CleanlyEvictedPages), u(r.EvictedAfterRefaultPages), u(r.ResidentAfterRefaultPages), u(r.ThrashingPages),
			u(r.TotalEvictions), u(r.TotalRefaults01), u(r.TotalRefaultsPFN), u(r.TotalRefaults),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteBlocks writes blocks.csv from the finalized binary offset block rollup.
func (w *Writer) WriteBlocks(rows []BlockRow) error {
	return w.writeBlockRows(BlocksCSVName, rows)
}

// WritePages writes pages.csv: blocks.csv columns at single-page granularity.
func (w *Writer) WritePages(rows []BlockRow) error {
	return w.writeBlockRows(PagesCSVName, rows)
}

func (w *Writer) writeBlockRows(name string, rows []BlockRow) error {
	f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	if err := cw.Write(blocksCSVHeader); err != nil {
		return err
	}
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	for _, r := range rows {
		rec := []string{
			strconv.Itoa(r.BlockIndex), u(r.FileOffsetStartBytes), u(r.FileOffsetEndBytes), r.PrimarySection, r.VMAPerms,
			u(r.TotalPages), u(r.NeverFaultedPages), u(r.PresentFinalPages), u(r.PresentCoWFinalPages),
			u(r.CleanlyEvictedPages), u(r.EverRefaultedPages), u(r.ThrashingPages), u(r.TotalEvictions), u(r.TotalRefaults),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteMeta writes run_meta.json formatted with indentation.
func (w *Writer) WriteMeta(meta RunMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", RunMetaJSONName, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(w.dir, RunMetaJSONName), data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", RunMetaJSONName, err)
	}
	return nil
}

// Close flushes and closes samples.csv.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.samplesF == nil {
		return nil
	}
	w.samplesCSV.Flush()
	flushErr := w.samplesCSV.Error()
	closeErr := w.samplesF.Close()
	w.samplesF = nil
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

// ChownToSudoUser restores file ownership on resultsDir and all of its contents to SUDO_UID:SUDO_GID
// when running as root under sudo. Returns (false, nil) if not running under sudo.
func ChownToSudoUser(resultsDir string) (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	uidStr := os.Getenv("SUDO_UID")
	gidStr := os.Getenv("SUDO_GID")
	if uidStr == "" || gidStr == "" {
		return false, nil
	}
	uid, err := strconv.Atoi(uidStr)
	if err != nil {
		return false, fmt.Errorf("parse SUDO_UID %q: %w", uidStr, err)
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil {
		return false, fmt.Errorf("parse SUDO_GID %q: %w", gidStr, err)
	}
	return true, chownTree(resultsDir, uid, gid)
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}
