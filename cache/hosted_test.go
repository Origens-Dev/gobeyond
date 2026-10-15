package cache

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func TestHostedInvalidationBoundSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "host.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var body string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/cache/invalidate" {
			t.Errorf("unexpected request")
		}
		allowedHeaders := map[string]bool{
			"Content-Type":    true,
			"Content-Length":  true,
			"Accept-Encoding": true,
			"User-Agent":      true,
		}
		for name := range r.Header {
			if !allowedHeaders[name] {
				t.Errorf("unexpected outgoing header %q", name)
			}
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(202)
		io.WriteString(w, `{"status":"existing"}`)
	})}
	go server.Serve(ln)
	defer server.Close()
	t.Setenv("GOBEYOND_HOST_REPORT_SOCKET", socket)
	_, err = InvalidateHosted(context.Background(), HostedInvalidationOptions{IdempotencyKey: "event-1"})
	if err != nil {
		t.Fatal(err)
	}
	if body != `{"idempotency_key":"event-1"}` {
		t.Fatal(body)
	}
}
func TestHostedInvalidationFailsClosed(t *testing.T) {
	t.Setenv("GOBEYOND_HOST_REPORT_SOCKET", "")
	if _, err := InvalidateHosted(context.Background(), HostedInvalidationOptions{IdempotencyKey: "event"}); err == nil {
		t.Fatal("missing capability accepted")
	}
}
