package agents_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
)

func TestHostedWebSearchUsesHostReportSocket(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "gb-search-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	socketPath := filepath.Join(tmpDir, "host.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != agents.HostedWebSearchPath || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var request agents.HostedWebSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.NetworkID != "network-1" || request.ToolCallID != "call-1" || request.Query != "current fact" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(agents.HostedWebSearchResponse{
			Answer: "grounded answer", Sources: []string{"https://example.com/fact"}, Searched: true, Provider: "vertex",
		})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	t.Setenv(agents.EnvHostReportSocket, socketPath)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := agents.HostedWebSearch(ctx, agents.HostedWebSearchRequest{
		NetworkID: "network-1", SessionID: "session-1", ToolCallID: "call-1", Query: "current fact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Searched || got.Answer != "grounded answer" || len(got.Sources) != 1 {
		t.Fatalf("response = %+v", got)
	}
}
