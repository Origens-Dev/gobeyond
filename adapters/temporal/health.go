package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	envHostReportSocket = "GOBEYOND_HOST_REPORT_SOCKET"
	envWorkerID         = "GOBEYOND_WORKER_ID"
	envDeployKey        = "GOBEYOND_DEPLOY_KEY"
	envEnvironmentID    = "GOBEYOND_ENVIRONMENT_ID"
	hostReportPath      = "/v1/worker-health"
	defaultReportSocket = "/run/gobeyond/host-report.sock"
)

// WorkerHealth is saturation / slot pressure reported to gbhost
// (ReportWorkerHealth → schedule heartbeat).
type WorkerHealth struct {
	Saturated          bool `json:"saturated"`
	TasksAssigned      int  `json:"tasks_assigned"`
	TasksAvailable     int  `json:"tasks_available"`
	MemoryHeadroomMB   int  `json:"memory_headroom_mb,omitempty"`
	CPUHeadroomPercent int  `json:"cpu_headroom_percent,omitempty"`
}

type hostHealthPayload struct {
	EnvironmentID      string `json:"environment_id"`
	WorkerID           string `json:"worker_id"`
	DeployKey          string `json:"deploy_key,omitempty"`
	Saturated          bool   `json:"saturated"`
	TasksAssigned      int    `json:"tasks_assigned"`
	TasksAvailable     int    `json:"tasks_available"`
	MemoryHeadroomMB   int    `json:"memory_headroom_mb,omitempty"`
	CPUHeadroomPercent int    `json:"cpu_headroom_percent,omitempty"`
}

// ReportWorkerHealth POSTs saturation/tasks to gbhost's host-report UDS.
// Best-effort: missing socket (local Docker) is a no-op success.
func ReportWorkerHealth(ctx context.Context, health WorkerHealth) error {
	socket := strings.TrimSpace(os.Getenv(envHostReportSocket))
	if socket == "" {
		socket = defaultReportSocket
	}
	envID := strings.TrimSpace(os.Getenv(envEnvironmentID))
	workerID := strings.TrimSpace(os.Getenv(envWorkerID))
	if envID == "" || workerID == "" {
		return nil
	}
	body, err := json.Marshal(hostHealthPayload{
		EnvironmentID:      envID,
		WorkerID:           workerID,
		DeployKey:          strings.TrimSpace(os.Getenv(envDeployKey)),
		Saturated:          health.Saturated,
		TasksAssigned:      health.TasksAssigned,
		TasksAvailable:     health.TasksAvailable,
		MemoryHeadroomMB:   health.MemoryHeadroomMB,
		CPUHeadroomPercent: health.CPUHeadroomPercent,
	})
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://host"+hostReportPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") ||
			strings.Contains(err.Error(), "connection refused") {
			return nil
		}
		return fmt.Errorf("report worker health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("report worker health: status %d", resp.StatusCode)
	}
	return nil
}

// healthTracker counts in-flight activity tasks for saturation heartbeats.
type healthTracker struct {
	maxConcurrent int32
	inFlight      atomic.Int32
}

func (t *healthTracker) snapshot() WorkerHealth {
	if t == nil {
		return WorkerHealth{}
	}
	assigned := int(t.inFlight.Load())
	max := int(t.maxConcurrent)
	available := max - assigned
	if available < 0 {
		available = 0
	}
	return WorkerHealth{
		Saturated:      max > 0 && assigned >= max,
		TasksAssigned:  assigned,
		TasksAvailable: available,
	}
}

func (t *healthTracker) begin() {
	if t != nil {
		t.inFlight.Add(1)
	}
}

func (t *healthTracker) end() {
	if t != nil {
		t.inFlight.Add(-1)
	}
}

func startHealthReporter(ctx context.Context, tracker *healthTracker) {
	if strings.TrimSpace(os.Getenv(envEnvironmentID)) == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = ReportWorkerHealth(ctx, tracker.snapshot())
			}
		}
	}()
}
