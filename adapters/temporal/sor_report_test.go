package temporal

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPostSorIngestNoopWithoutURL(t *testing.T) {
	t.Setenv(envAPIURL, "")
	t.Setenv(envHostReportSocket, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(envEnvironmentID, "env-1")
	t.Setenv(envWorkerID, "default")
	if err := postSorIngest(context.Background(), ReportSorEventInput{
		WorkflowID: "wf", RunID: "run", DedupeKey: "d", Type: "workflow.completed", Kind: "event",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostSorIngestHTTPS(t *testing.T) {
	var got ReportSorEventInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != sorIngestPath {
			t.Fatalf("path %s", r.URL.Path)
		}
		if r.Header.Get("x-gobeyond-internal-token") != "secret" {
			t.Fatalf("token %q", r.Header.Get("x-gobeyond-internal-token"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envAPIURL, srv.URL)
	t.Setenv(envInternalAPIToken, "secret")
	t.Setenv(envHostReportSocket, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(envEnvironmentID, "env-1")
	t.Setenv(envWorkerID, "billing")
	t.Setenv(envOrganizationID, "org-1")
	t.Setenv(envProjectID, "proj-1")
	if err := ReportSorEvent(context.Background(), ReportSorEventInput{
		WorkflowID: "wf-1", RunID: "run-1", DedupeKey: "hist-1",
		Type: "workflow.completed", Kind: "event",
	}); err != nil {
		t.Fatal(err)
	}
	if got.EnvironmentID != "env-1" || got.WorkerID != "billing" || got.Type != "workflow.completed" {
		t.Fatalf("got %+v", got)
	}
	if got.OrganizationID != "org-1" || got.ProjectID != "proj-1" {
		t.Fatalf("identity %+v", got)
	}
}

func TestPostSorIngestHostReport(t *testing.T) {
	// macOS sockaddr_un path limit — keep the socket path short.
	sock := "/tmp/gb-sor-" + t.Name()[len(t.Name())-6:] + ".sock"
	_ = os.Remove(sock)
	t.Cleanup(func() { _ = os.Remove(sock) })
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	mux := http.NewServeMux()
	done := make(chan ReportSorEventInput, 1)
	mux.HandleFunc("POST /v1/sor-ingest", func(w http.ResponseWriter, r *http.Request) {
		var in ReportSorEventInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		done <- in
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	t.Setenv(envHostReportSocket, sock)
	t.Setenv(envAPIURL, "") // force UDS path
	t.Setenv(envEnvironmentID, "env-1")
	t.Setenv(envWorkerID, "default")
	if err := postSorIngest(context.Background(), ReportSorEventInput{
		WorkflowID: "wf", RunID: "run", DedupeKey: "d", Type: "workflow.completed", Kind: "event",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.Type != "workflow.completed" || got.WorkflowID != "wf" {
			t.Fatalf("%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestTruncateErr(t *testing.T) {
	if truncateErr(nil) != "" {
		t.Fatal("nil")
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	if got := truncateErr(errString(string(long))); len(got) != 256 {
		t.Fatalf("len=%d", len(got))
	}
}

type errString string

func (e errString) Error() string { return string(e) }
