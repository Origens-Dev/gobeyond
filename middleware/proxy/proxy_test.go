package proxy_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/middleware/proxy"
)

// shortSocketPath returns a socket path under /tmp: macOS caps sun_path
// at 104 bytes and t.TempDir() paths exceed it.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "gb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "app.sock")
}

// upstreamRecorder serves the app side of the hop over a unix socket and
// records the last request it saw.
type upstreamRecorder struct {
	socket string
	last   *http.Request
	body   string
	server *http.Server
}

func startUpstream(t *testing.T) *upstreamRecorder {
	t.Helper()
	recorder := &upstreamRecorder{socket: shortSocketPath(t)}
	listener, err := net.Listen("unix", recorder.socket)
	if err != nil {
		t.Fatal(err)
	}
	recorder.server = &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		recorder.last = request.Clone(context.Background())
		recorder.body = string(body)
		writer.Header().Set("X-Upstream", "yes")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte("upstream body"))
	})}
	go func() { _ = recorder.server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = recorder.server.Shutdown(ctx)
	})
	return recorder
}

func TestForwardPreservesContractHeaders(t *testing.T) {
	upstream := startUpstream(t)
	tampering := func(request *http.Request) (*proxy.Response, error) {
		// Middleware must not be able to alter the preserved class.
		request.Header.Set("X-Gobeyond-Viewer-Host", "evil.example")
		request.Header.Del("X-Origens-Oidc-Token")
		request.Header.Set("X-Forwarded-For", "6.6.6.6")
		// Mutable class: path rewrite + custom header.
		request.URL.Path = "/rewritten"
		request.Header.Set("X-Custom", "added")
		return nil, nil
	}
	handler, err := proxy.New("unix://"+upstream.socket, tampering)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "http://tenant/original?q=1", strings.NewReader("payload"))
	request.Header.Set("X-Gobeyond-Viewer-Host", "docs.example.com")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Origens-Oidc-Token", "token-123")
	// Viewer-supplied auth context must be stripped (middleware-only).
	request.Header.Set("X-Gobeyond-Auth-Context", "c3Bvb2Y")
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, request)

	if writer.Code != http.StatusAccepted || writer.Header().Get("X-Upstream") != "yes" {
		t.Fatalf("response = %d %q", writer.Code, writer.Header().Get("X-Upstream"))
	}
	seen := upstream.last
	if seen == nil {
		t.Fatal("upstream saw no request")
	}
	if seen.URL.Path != "/rewritten" {
		t.Errorf("upstream path = %q, want /rewritten", seen.URL.Path)
	}
	if got := seen.Header.Get("X-Gobeyond-Viewer-Host"); got != "docs.example.com" {
		t.Errorf("viewer-host = %q, want preserved verbatim", got)
	}
	if got := seen.Header.Get("X-Origens-Oidc-Token"); got != "token-123" {
		t.Errorf("oidc token = %q, want preserved verbatim", got)
	}
	if got := seen.Header.Get("X-Forwarded-For"); got == "6.6.6.6" {
		t.Error("middleware-injected X-Forwarded-For leaked upstream")
	}
	if got := seen.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("x-forwarded-proto = %q, want https", got)
	}
	if got := seen.Header.Get("X-Gobeyond-Auth-Context"); got != "" {
		t.Errorf("viewer auth context leaked upstream: %q", got)
	}
	if got := seen.Header.Get("X-Custom"); got != "added" {
		t.Errorf("mutable header X-Custom = %q, want added", got)
	}
	if upstream.body != "payload" {
		t.Errorf("upstream body = %q, want payload", upstream.body)
	}
}

func TestSyntheticResponseSkipsUpstream(t *testing.T) {
	upstream := startUpstream(t)
	redirect := func(*http.Request) (*proxy.Response, error) {
		return &proxy.Response{
			Status: http.StatusMovedPermanently,
			Header: http.Header{"Location": {"/new-home"}},
		}, nil
	}
	handler, err := proxy.New("unix://"+upstream.socket, redirect)
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://tenant/old-home", nil))
	if writer.Code != http.StatusMovedPermanently || writer.Header().Get("Location") != "/new-home" {
		t.Fatalf("synthetic response = %d %q", writer.Code, writer.Header().Get("Location"))
	}
	if upstream.last != nil {
		t.Fatal("upstream must not be contacted for synthetic responses")
	}
}

func TestMiddlewareSetsAuthContext(t *testing.T) {
	upstream := startUpstream(t)
	withAuth := func(request *http.Request) (*proxy.Response, error) {
		return nil, proxy.SetAuthContext(request.Header, map[string]any{"sub": "user-1"})
	}
	handler, err := proxy.New("unix://"+upstream.socket, withAuth)
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://tenant/", nil))
	if writer.Code != http.StatusAccepted {
		t.Fatalf("status = %d", writer.Code)
	}
	value := upstream.last.Header.Get("X-Gobeyond-Auth-Context")
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("auth context is not base64url: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(decoded, &payload); err != nil || payload["sub"] != "user-1" {
		t.Fatalf("auth context payload = %q (%v)", decoded, err)
	}
}

func TestPassThroughWithoutMiddleware(t *testing.T) {
	upstream := startUpstream(t)
	handler, err := proxy.New("unix://"+upstream.socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://tenant/page", nil))
	if writer.Code != http.StatusAccepted || upstream.last == nil {
		t.Fatalf("pass-through failed: %d", writer.Code)
	}
}

func TestValidateAuthContext(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString([]byte(`{"ok":true}`))
	if err := proxy.ValidateAuthContext(valid); err != nil {
		t.Fatalf("valid auth context rejected: %v", err)
	}
	if err := proxy.ValidateAuthContext("!!not-base64!!"); err == nil {
		t.Fatal("non-base64url accepted")
	}
	notJSON := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	if err := proxy.ValidateAuthContext(notJSON); err == nil {
		t.Fatal("non-JSON accepted")
	}
	huge := base64.RawURLEncoding.EncodeToString([]byte(`"` + strings.Repeat("a", proxy.MaxAuthContextBytes) + `"`))
	if err := proxy.ValidateAuthContext(huge); err == nil {
		t.Fatal("oversized auth context accepted")
	}
	header := http.Header{}
	if err := proxy.SetAuthContext(header, strings.Repeat("a", proxy.MaxAuthContextBytes)); err == nil {
		t.Fatal("SetAuthContext accepted an oversized payload")
	}
}

func TestNewRejectsBadUpstream(t *testing.T) {
	if _, err := proxy.New("", nil); err == nil {
		t.Fatal("empty upstream accepted")
	}
	if _, err := proxy.New("ftp://nope", nil); err == nil {
		t.Fatal("unsupported scheme accepted")
	}
}
