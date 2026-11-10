package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type runner struct {
	cfg    Instance
	cg     *cgroup
	binary string
	port   int
	logger *log.Logger
}

// copyCold copies the collector binary and evicts the copy from the page cache, so the
// collector's own faults charge its pages to its cgroup ("first toucher pays"). Without
// this the copy's pages would stay charged to the manager, which wrote them.
func copyCold(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	// Dirty pages can't be dropped, so write them back first.
	if err := out.Sync(); err != nil {
		return err
	}
	return unix.Fadvise(int(out.Fd()), 0, 0, unix.FADV_DONTNEED)
}

// telemetryConfig is merged on top of the collector config when telemetry_endpoint is set.
// It turns the collector's internal metrics on (otelcol_receiver_accepted_metric_points is
// the throughput) and pushes them over OTLP, with k8s.container.name set so a dashboard
// can join them with the cgroup metrics.
const telemetryConfig = `yaml:service::telemetry: {
  resource: {k8s.container.name: "${env:INSTANCE_NAME}"},
  metrics: {level: normal, readers: [{periodic: {interval: 2000, exporter: {otlp: {
    protocol: http/protobuf, endpoint: "${env:TELEMETRY_OTLP_ENDPOINT}"}}}}]}}`

// run starts the collector in its cgroup and restarts it when it exits (e.g. OOM kill),
// until ctx is canceled.
func (r *runner) run(ctx context.Context, collectorConfig, telemetryEndpoint string) {
	for {
		args := []string{"--config=" + collectorConfig}
		env := append(os.Environ(),
			"OTLP_HTTP_ENDPOINT=127.0.0.1:"+strconv.Itoa(r.port),
			"INSTANCE_NAME="+r.cfg.Name,
		)
		if telemetryEndpoint != "" {
			args = append(args, "--config="+telemetryConfig)
			env = append(env, "TELEMETRY_OTLP_ENDPOINT="+telemetryEndpoint)
		}
		cmd := exec.Command(r.binary, args...)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(r.cg.fd.Fd())}
		logPath := filepath.Join(filepath.Dir(r.binary), "collector.log")
		logFile, err := os.Create(logPath)
		if err != nil {
			r.logger.Printf("create %s, collector output is discarded: %v", logPath, err)
		} else {
			cmd.Stdout, cmd.Stderr = logFile, logFile
		}
		if err := cmd.Start(); err != nil {
			r.logger.Printf("start collector: %v", err)
		} else {
			r.logger.Printf("collector started (pid %d, memory.max=%s, logs in %s)", cmd.Process.Pid, r.cfg.MemoryMax, logPath)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				r.logger.Printf("collector exited: %v", err)
			case <-ctx.Done():
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill()
					<-done
				}
			}
		}
		if logFile != nil {
			logFile.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// control applies the instance's reclaim policy every interval.
func (r *runner) control(ctx context.Context) {
	p := r.cfg.Reclaim
	if p.Type == "" || p.Type == "none" {
		return
	}
	var step uint64
	if p.Type == "fixed" {
		s, _ := parseLimit(p.Step) // validated in loadConfig
		step, _ = strconv.ParseUint(s, 10, 64)
	}
	prevStall, _ := r.cg.someStall()
	prevAt := time.Now()
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur, err := r.cg.current()
		if err != nil {
			r.logger.Printf("read memory.current: %v", err)
			continue
		}
		stall, err := r.cg.someStall()
		if err != nil {
			r.logger.Printf("read memory.pressure: %v", err)
			continue
		}
		now := time.Now()
		pressure := float64(stall-prevStall) / float64(now.Sub(prevAt))
		prevStall, prevAt = stall, now

		want := step
		if p.Type == "psi" {
			// Senpai (Meta TMO): reclaim harder the further pressure is below the target,
			// and stop once the workload starts stalling.
			want = uint64(float64(cur) * p.Ratio * max(0, 1-pressure/p.PressureTarget))
		}
		if want < 4096 {
			r.logger.Printf("current=%s pressure=%.3f%% reclaim=0", fmtBytes(cur), pressure*100)
			continue
		}
		if err := r.cg.reclaim(want); err != nil {
			r.logger.Printf("write memory.reclaim: %v", err)
			continue
		}
		r.logger.Printf("current=%s pressure=%.3f%% reclaim=%s", fmtBytes(cur), pressure*100, fmtBytes(want))
	}
}

// load sends the same synthetic OTLP/HTTP metrics request to the instance every interval,
// so every collector does the same work.
func (r *runner) load(ctx context.Context, cfg LoadConfig) {
	if cfg.Interval <= 0 {
		return
	}
	body := otlpMetricsJSON(cfg.DataPoints)
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/metrics", r.port)
	client := &http.Client{Timeout: 2 * time.Second}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// Errors are expected while the collector (re)starts.
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

func otlpMetricsJSON(n int) []byte {
	var points bytes.Buffer
	now := time.Now().UnixNano()
	for i := range max(n, 1) {
		if i > 0 {
			points.WriteByte(',')
		}
		fmt.Fprintf(&points, `{"timeUnixNano":"%d","asDouble":%d,"attributes":[{"key":"i","value":{"intValue":"%d"}}]}`, now, i, i)
	}
	return fmt.Appendf(nil, `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"reclaim-demo-load"}}]},"scopeMetrics":[{"metrics":[{"name":"reclaim_demo.load","gauge":{"dataPoints":[%s]}}]}]}]}`, points.String())
}

func fmtBytes(n uint64) string {
	return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
}
