package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	RecoveryVersion    int    `json:"recovery_version,omitempty"`
	UncoveredWorkflows int64  `json:"uncovered_workflows,omitempty"`
	WorkflowTasks      int    `json:"workflow_tasks,omitempty"`
	LocalActivities    int    `json:"local_activities,omitempty"`
	ReservedTasks      int    `json:"reserved_tasks,omitempty"`
	SleepFence         string `json:"sleep_fence,omitempty"`
	Quiesced           bool   `json:"quiesced,omitempty"`

	Saturated          bool `json:"saturated"`
	TasksAssigned      int  `json:"tasks_assigned"`
	TasksAvailable     int  `json:"tasks_available"`
	MemoryHeadroomMB   int  `json:"memory_headroom_mb,omitempty"`
	CPUHeadroomPercent int  `json:"cpu_headroom_percent,omitempty"`
}

type hostHealthPayload struct {
	RecoveryVersion    int    `json:"recovery_version,omitempty"`
	UncoveredWorkflows int64  `json:"uncovered_workflows,omitempty"`
	WorkflowTasks      int    `json:"workflow_tasks,omitempty"`
	LocalActivities    int    `json:"local_activities,omitempty"`
	ReservedTasks      int    `json:"reserved_tasks,omitempty"`
	SleepFence         string `json:"sleep_fence,omitempty"`
	Quiesced           bool   `json:"quiesced,omitempty"`

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
	_, err := reportWorkerHealth(ctx, health)
	return err
}

func reportWorkerHealth(ctx context.Context, health WorkerHealth) (string, error) {
	socket := strings.TrimSpace(os.Getenv(envHostReportSocket))
	if socket == "" {
		socket = defaultReportSocket
	}
	envID := strings.TrimSpace(os.Getenv(envEnvironmentID))
	workerID := strings.TrimSpace(os.Getenv(envWorkerID))
	if envID == "" || workerID == "" {
		if health.RecoveryVersion == 1 {
			return "", errors.New("recovery health identity unavailable")
		}
		return "", nil
	}
	body, err := json.Marshal(hostHealthPayload{
		EnvironmentID:   envID,
		RecoveryVersion: health.RecoveryVersion, UncoveredWorkflows: health.UncoveredWorkflows, WorkflowTasks: health.WorkflowTasks, LocalActivities: health.LocalActivities, ReservedTasks: health.ReservedTasks, SleepFence: health.SleepFence, Quiesced: health.Quiesced,
		WorkerID:           workerID,
		DeployKey:          strings.TrimSpace(os.Getenv(envDeployKey)),
		Saturated:          health.Saturated,
		TasksAssigned:      health.TasksAssigned,
		TasksAvailable:     health.TasksAvailable,
		MemoryHeadroomMB:   health.MemoryHeadroomMB,
		CPUHeadroomPercent: health.CPUHeadroomPercent,
	})
	if err != nil {
		return "", err
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
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") ||
			strings.Contains(err.Error(), "connection refused") {
			if health.RecoveryVersion == 1 {
				return "", errors.New("recovery health transport unavailable")
			}
			return "", nil
		}
		return "", fmt.Errorf("report worker health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("report worker health: status %d", resp.StatusCode)
	}
	if health.Quiesced && resp.Header.Get("X-GoBeyond-Quiesce-Accepted") != health.SleepFence {
		return "", errors.New("quiesce acknowledgement missing")
	}
	return resp.Header.Get("X-GoBeyond-Quiesce-Fence"), nil
}

// healthTracker counts in-flight activity tasks for saturation heartbeats.
type healthTracker struct {
	tuner         *recoveryTuner
	uncovered     atomic.Int64
	quiesce       chan string
	recoveryLost  chan error
	maxConcurrent int32
	inFlight      atomic.Int32
}

func (t *healthTracker) snapshot() WorkerHealth {
	if t == nil {
		return WorkerHealth{}
	}
	assigned := int(t.inFlight.Load())
	maximum := int(t.maxConcurrent)
	available := maximum - assigned
	if available < 0 {
		available = 0
	}
	result := WorkerHealth{Saturated: maximum > 0 && assigned >= maximum, TasksAssigned: assigned, TasksAvailable: available}
	if t.tuner != nil {
		result.RecoveryVersion = 1
		result.UncoveredWorkflows = t.uncovered.Load()
		for i, s := range t.tuner.suppliers {
			if s != nil {
				r, e, _ := s.snapshot()
				result.ReservedTasks += r
				switch i {
				case 0:
					result.WorkflowTasks = e
				case 2:
					result.LocalActivities = e
				}
			}
		}
	}
	return result
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
		lastCovered := time.Now()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fence, err := reportWorkerHealth(ctx, tracker.snapshot())
				if err == nil {
					lastCovered = time.Now()
				} else if tracker.tuner != nil && time.Since(lastCovered) >= 30*time.Second {
					select {
					case tracker.recoveryLost <- errors.New("durable worker residency lost"):
					default:
					}
					return
				}
				if err == nil && fence != "" && tracker.quiesce != nil {
					select {
					case tracker.quiesce <- fence:
					default:
					}
				}
			}
		}
	}()
}
