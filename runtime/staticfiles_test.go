package runtime

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/buildpaths"
)

func TestStaticFilesServesArtifactsAndPublicFiles(t *testing.T) {
	root := t.TempDir()
	assetRel := filepath.Join("_gobeyond", "builds", "b1", "assets", "app.js")
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(assetRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, assetRel), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "logo.svg"), []byte("<svg></svg>"), 0o644); err != nil {
		t.Fatal(err)
	}

	nextCalled := false
	handler := StaticFiles(root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))

	assetURL := buildpaths.AssetURL("b1", "app.js")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, assetURL, nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "console.log") {
		t.Fatalf("asset: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != immutableCacheControl {
		t.Fatalf("asset Cache-Control = %q", got)
	}
	if nextCalled {
		t.Fatal("next called for static artifact")
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/logo.svg", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<svg></svg>" {
		t.Fatalf("public: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("public file must not be immutable, got %q", got)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("fallback status = %d", recorder.Code)
	}
}

func TestStaticFilesGzipsCompressibleTypes(t *testing.T) {
	root := t.TempDir()
	assetRel := filepath.Join("_gobeyond", "builds", "b1", "assets", "app.js")
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(assetRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("var x = 1;\n"), 200)
	if err := os.WriteFile(filepath.Join(root, assetRel), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	handler := StaticFiles(root, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, buildpaths.AssetURL("b1", "app.js"), nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded body mismatch (%d vs %d bytes)", len(decoded), len(payload))
	}
}

func TestStaticFilesEmptyDirectoryPassesThrough(t *testing.T) {
	called := false
	handler := StaticFiles("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !called || recorder.Code != http.StatusOK {
		t.Fatalf("passthrough failed: called=%v status=%d", called, recorder.Code)
	}
}

func TestStaticFilesPreviewOwnsRobotsPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "robots.txt"), []byte("User-agent: *\nAllow: /"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDeploymentKind, "preview")
	handler := StaticFiles(root, http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "http://example.test/robots.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("preview robots = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("preview robots cache-control = %q", got)
	}
}

func TestStaticFilesProductionRobotsDefaultAndPreviewHeader(t *testing.T) {
	root := t.TempDir()
	handler := StaticFiles(root, http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "http://example.test/robots.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "User-agent: *\nAllow: /\n" {
		t.Fatalf("production default robots = %q", got)
	}

	t.Setenv(EnvDeploymentKind, "preview")
	req = httptest.NewRequest(http.MethodGet, "http://example.test/missing", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("preview x-robots-tag = %q", got)
	}
}
