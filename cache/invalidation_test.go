package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInvalidateRemoteStartsPlatformWorkflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/cache/invalidate" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-GoBeyond-Internal-Token") != "token" || r.Header.Get("X-GoBeyond-Environment-ID") != "env-1" {
			t.Fatal("platform auth headers missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted","workflow_id":"invalidate-cache-env-1"}`))
	}))
	defer server.Close()

	result, err := InvalidateRemote(context.Background(), RemoteInvalidationOptions{
		EnvironmentID: "env-1", IdempotencyKey: "delivery-1", APIURL: server.URL, Token: "token",
		Tags: []string{"contentful:entry:1"}, Paths: []string{"/"},
	})
	if err != nil {
		t.Fatalf("InvalidateRemote() error = %v", err)
	}
	if result.Status != "accepted" || result.WorkflowID == "" {
		t.Fatalf("result = %+v", result)
	}
}
