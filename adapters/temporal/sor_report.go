package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	envAPIURL           = "GOBEYOND_API_URL"
	envInternalAPIToken = "GOBEYOND_INTERNAL_API_TOKEN"
	envOrganizationID   = "GOBEYOND_ORGANIZATION_ID"
	envProjectID        = "GOBEYOND_PROJECT_ID"
	sorIngestPath       = "/internal/workflows/sor/ingest"
	// ReportSorEventName is the local-activity name for SoR timeline reporting
	// (ADR 010). Must stay stable — workflow histories pin activity names.
	ReportSorEventName = "ReportSorEvent"
)

// ReportSorEventInput is the local-activity argument for SoR timeline /
// activity reporting from worker interceptors (ADR 010).
type ReportSorEventInput struct {
	EnvironmentID  string            `json:"environment_id"`
	OrganizationID string            `json:"organization_id,omitempty"`
	ProjectID      string            `json:"project_id,omitempty"`
	WorkerID       string            `json:"worker_id"`
	WorkflowID     string            `json:"workflow_id"`
	RunID          string            `json:"run_id"`
	DedupeKey      string            `json:"dedupe_key"`
	Type           string            `json:"type"`
	Payload        map[string]string `json:"payload,omitempty"`
	Kind           string            `json:"kind"` // "event" | "activity"
	ActivityID     string            `json:"activity_id,omitempty"`
	ActivityType   string            `json:"activity_type,omitempty"`
	Status         string            `json:"status,omitempty"`
	Attempt        int32             `json:"attempt,omitempty"`
}

// ReportSorEvent is the registered local activity. Best-effort: missing API
// URL (local Docker) is a no-op success so SoR never fails the workflow task.
func ReportSorEvent(ctx context.Context, in ReportSorEventInput) error {
	return postSorIngest(ctx, in)
}

func postSorIngest(ctx context.Context, in ReportSorEventInput) error {
	in = fillSorIdentity(in)
	if strings.TrimSpace(in.EnvironmentID) == "" || strings.TrimSpace(in.WorkerID) == "" {
		return nil
	}
	if strings.TrimSpace(in.WorkflowID) == "" || strings.TrimSpace(in.RunID) == "" {
		return nil
	}
	if strings.TrimSpace(in.Kind) == "" {
		in.Kind = "event"
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	// Prefer host-report UDS when present (gbhost can forward); fall back to
	// public API URL sealed into the tip.
	if err := postSorViaHostReport(ctx, body); err == nil {
		return nil
	} else if !isMissingSocket(err) {
		// Non-socket errors still try HTTPS below.
	}

	base := strings.TrimRight(strings.TrimSpace(os.Getenv(envAPIURL)), "/")
	if base == "" {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+sorIngestPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := strings.TrimSpace(os.Getenv(envInternalAPIToken)); tok != "" {
		req.Header.Set("x-gobeyond-internal-token", tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sor ingest: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sor ingest: status %d", resp.StatusCode)
	}
	return nil
}

func postSorViaHostReport(ctx context.Context, body []byte) error {
	socket := strings.TrimSpace(os.Getenv(envHostReportSocket))
	if socket == "" {
		socket = defaultReportSocket
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://host/v1/sor-ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("sor ingest uds: not found")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sor ingest uds: status %d", resp.StatusCode)
	}
	return nil
}

func isMissingSocket(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return os.IsNotExist(err) ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "not found")
}

func fillSorIdentity(in ReportSorEventInput) ReportSorEventInput {
	if strings.TrimSpace(in.EnvironmentID) == "" {
		in.EnvironmentID = strings.TrimSpace(os.Getenv(envEnvironmentID))
	}
	if strings.TrimSpace(in.WorkerID) == "" {
		in.WorkerID = strings.TrimSpace(os.Getenv(envWorkerID))
	}
	if strings.TrimSpace(in.OrganizationID) == "" {
		in.OrganizationID = strings.TrimSpace(os.Getenv(envOrganizationID))
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		in.ProjectID = strings.TrimSpace(os.Getenv(envProjectID))
	}
	return in
}
