package results

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// LoadRunData reads run_meta.json, samples.csv, sections.csv, and blocks.csv from dir.
func LoadRunData(dir string) (*RunData, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve results dir %q: %w", dir, err)
	}

	metaBytes, err := os.ReadFile(filepath.Join(absDir, RunMetaJSONName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", RunMetaJSONName, err)
	}
	var meta RunMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", RunMetaJSONName, err)
	}

	samples, err := readSamplesCSV(filepath.Join(absDir, SamplesCSVName))
	if err != nil {
		return nil, err
	}

	sections, err := readSectionsCSV(filepath.Join(absDir, SectionsCSVName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	blocks, err := readBlocksCSV(filepath.Join(absDir, BlocksCSVName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	return &RunData{
		Dir:      absDir,
		Meta:     meta,
		Samples:  samples,
		Sections: sections,
		Blocks:   blocks,
	}, nil
}

type csvRowReader struct {
	idx map[string]int
	rec []string
	err error
}

func newCSVIndex(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[h] = i
	}
	return m
}

func (r *csvRowReader) str(col string) string {
	if r.err != nil {
		return ""
	}
	i, ok := r.idx[col]
	if !ok || i >= len(r.rec) {
		r.err = fmt.Errorf("missing column %q", col)
		return ""
	}
	return r.rec[i]
}

func (r *csvRowReader) u64(col string) uint64 {
	s := r.str(col)
	if r.err != nil {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		r.err = fmt.Errorf("column %q: %w", col, err)
	}
	return v
}

func (r *csvRowReader) has(col string) bool {
	i, ok := r.idx[col]
	return ok && i < len(r.rec)
}

func (r *csvRowReader) i64(col string) int64 {
	s := r.str(col)
	if r.err != nil {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		r.err = fmt.Errorf("column %q: %w", col, err)
	}
	return v
}

func (r *csvRowReader) integer(col string) int {
	s := r.str(col)
	if r.err != nil {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		r.err = fmt.Errorf("column %q: %w", col, err)
	}
	return v
}

func readSamplesCSV(path string) ([]SampleRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", SamplesCSVName, err)
	}
	defer f.Close()

	cr := csv.NewReader(f)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read %s header: %w", SamplesCSVName, err)
	}
	rr := csvRowReader{idx: newCSVIndex(header)}

	var rows []SampleRow
	lineNum := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s line %d: %w", SamplesCSVName, lineNum+1, err)
		}
		lineNum++
		rr.rec = rec
		rr.err = nil

		row := SampleRow{
			TimestampUnixMs:          rr.i64("timestamp_unix_ms"),
			ElapsedMs:                rr.i64("elapsed_ms"),
			Trigger:                  rr.str("trigger"),
			BinTotalPages:            rr.u64("bin_total_pages"),
			BinPresentFilePages:      rr.u64("bin_present_file_pages"),
			BinPresentCoWPages:       rr.u64("bin_present_cow_pages"),
			BinSwappedPages:          rr.u64("bin_swapped_pages"),
			BinNeverFaultedPages:     rr.u64("bin_never_faulted_pages"),
			BinEvictedOutPages:       rr.u64("bin_evicted_out_pages"),
			BinCleanlyEvictedPages:   rr.u64("bin_cleanly_evicted_pages"),
			BinEverFaultedPages:      rr.u64("bin_ever_faulted_pages"),
			BinEverEvictedPages:      rr.u64("bin_ever_evicted_pages"),
			BinEverRefaultedPages:    rr.u64("bin_ever_refaulted_pages"),
			BinThrashingPages:        rr.u64("bin_thrashing_pages"),
			DeltaColdFaults:          rr.u64("delta_cold_faults"),
			DeltaEvictions:           rr.u64("delta_evictions"),
			DeltaRefaults01:          rr.u64("delta_refaults_01"),
			DeltaRefaultsPFN:         rr.u64("delta_refaults_pfn"),
			DeltaRefaultsTotal:       rr.u64("delta_refaults_total"),
			CumColdFaults:            rr.u64("cum_cold_faults"),
			CumEvictions:             rr.u64("cum_evictions"),
			CumRefaults01:            rr.u64("cum_refaults_01"),
			CumRefaultsPFN:           rr.u64("cum_refaults_pfn"),
			CumRefaultsTotal:         rr.u64("cum_refaults_total"),
			ROPresentPages:           rr.u64("ro_present_pages"),
			ROEvictedOutPages:        rr.u64("ro_evicted_out_pages"),
			ROCumRefaults:            rr.u64("ro_cum_refaults"),
			RXPresentPages:           rr.u64("rx_present_pages"),
			RXEvictedOutPages:        rr.u64("rx_evicted_out_pages"),
			RXCumRefaults:            rr.u64("rx_cum_refaults"),
			RWPresentFilePages:       rr.u64("rw_present_file_pages"),
			RWPresentCoWPages:        rr.u64("rw_present_cow_pages"),
			RWEvictedOutPages:        rr.u64("rw_evicted_out_pages"),
			RWCumRefaults:            rr.u64("rw_cum_refaults"),
			CGMemoryCurrentBytes:     rr.u64("cg_memory_current_bytes"),
			CGAnonBytes:              rr.u64("cg_anon_bytes"),
			CGFileBytes:              rr.u64("cg_file_bytes"),
			CGActiveFileBytes:        rr.u64("cg_active_file_bytes"),
			CGInactiveFileBytes:      rr.u64("cg_inactive_file_bytes"),
			CGKernelBytes:            rr.u64("cg_kernel_bytes"),
			CGPgfault:                rr.u64("cg_pgfault"),
			CGPgmajfault:             rr.u64("cg_pgmajfault"),
			CGWorkingsetRefaultFile:  rr.u64("cg_workingset_refault_file"),
			CGWorkingsetActivateFile: rr.u64("cg_workingset_activate_file"),
			CGEventsLow:              rr.u64("cg_events_low"),
			CGEventsHigh:             rr.u64("cg_events_high"),
			CGEventsMax:              rr.u64("cg_events_max"),
			CGEventsOOM:              rr.u64("cg_events_oom"),
			CGEventsOOMKill:          rr.u64("cg_events_oom_kill"),
			CGPSISomeTotalUs:         rr.u64("cg_psi_some_total_us"),
			CGPSIFullTotalUs:         rr.u64("cg_psi_full_total_us"),
			ProcRSSBytes:             rr.u64("proc_rss_bytes"),
			ProcRSSAnonBytes:         rr.u64("proc_rss_anon_bytes"),
			ProcRSSFileBytes:         rr.u64("proc_rss_file_bytes"),
			ProcRSSShmemBytes:        rr.u64("proc_rss_shmem_bytes"),
			ProcHWMBytes:             rr.u64("proc_hwm_bytes"),
			ProcSwapBytes:            rr.u64("proc_swap_bytes"),
		}
		hasWS := rr.has("cg_working_set_bytes")
		hasIsoWS := rr.has("est_isolated_working_set_bytes")
		if hasWS {
			row.CGWorkingSetBytes = rr.u64("cg_working_set_bytes")
		} else {
			row.CGWorkingSetBytes = KubeletWorkingSetBytes(row.CGMemoryCurrentBytes, row.CGInactiveFileBytes)
		}
		if hasIsoWS {
			row.EstIsolatedWorkingSetBytes = rr.u64("est_isolated_working_set_bytes")
		} else {
			row.EstIsolatedWorkingSetBytes = row.CGWorkingSetBytes + UnchargedRSSFileBytes(row.ProcRSSFileBytes, row.CGFileBytes)
		}
		if rr.err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", SamplesCSVName, lineNum, rr.err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func readSectionsCSV(path string) ([]SectionRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cr := csv.NewReader(f)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read %s header: %w", SectionsCSVName, err)
	}
	rr := csvRowReader{idx: newCSVIndex(header)}

	var rows []SectionRow
	lineNum := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s line %d: %w", SectionsCSVName, lineNum+1, err)
		}
		lineNum++
		rr.rec = rec
		rr.err = nil

		row := SectionRow{
			SectionName:               rr.str("section_name"),
			VMAPerms:                  rr.str("vma_perms"),
			FileOffsetBytes:           rr.u64("file_offset_bytes"),
			SizeBytes:                 rr.u64("size_bytes"),
			TotalPages:                rr.u64("total_pages"),
			NeverFaultedPages:         rr.u64("never_faulted_pages"),
			PresentFinalPages:         rr.u64("present_final_pages"),
			PresentCoWFinalPages:      rr.u64("present_cow_final_pages"),
			CleanlyEvictedPages:       rr.u64("cleanly_evicted_pages"),
			EvictedAfterRefaultPages:  rr.u64("evicted_after_refault_pages"),
			ResidentAfterRefaultPages: rr.u64("resident_after_refault_pages"),
			ThrashingPages:            rr.u64("thrashing_pages"),
			TotalEvictions:            rr.u64("total_evictions"),
			TotalRefaults01:           rr.u64("total_refaults_01"),
			TotalRefaultsPFN:          rr.u64("total_refaults_pfn"),
			TotalRefaults:             rr.u64("total_refaults"),
		}
		if rr.err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", SectionsCSVName, lineNum, rr.err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func readBlocksCSV(path string) ([]BlockRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cr := csv.NewReader(f)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read %s header: %w", BlocksCSVName, err)
	}
	rr := csvRowReader{idx: newCSVIndex(header)}

	var rows []BlockRow
	lineNum := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s line %d: %w", BlocksCSVName, lineNum+1, err)
		}
		lineNum++
		rr.rec = rec
		rr.err = nil

		row := BlockRow{
			BlockIndex:           rr.integer("block_index"),
			FileOffsetStartBytes: rr.u64("file_offset_start_bytes"),
			FileOffsetEndBytes:   rr.u64("file_offset_end_bytes"),
			PrimarySection:       rr.str("primary_section"),
			VMAPerms:             rr.str("vma_perms"),
			TotalPages:           rr.u64("total_pages"),
			NeverFaultedPages:    rr.u64("never_faulted_pages"),
			PresentFinalPages:    rr.u64("present_final_pages"),
			PresentCoWFinalPages: rr.u64("present_cow_final_pages"),
			CleanlyEvictedPages:  rr.u64("cleanly_evicted_pages"),
			EverRefaultedPages:   rr.u64("ever_refaulted_pages"),
			ThrashingPages:       rr.u64("thrashing_pages"),
			TotalEvictions:       rr.u64("total_evictions"),
			TotalRefaults:        rr.u64("total_refaults"),
		}
		if rr.err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", BlocksCSVName, lineNum, rr.err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
