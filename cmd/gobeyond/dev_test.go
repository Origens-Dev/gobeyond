package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDevOptions(t *testing.T) {
	defaults, err := parseDevOptions(nil)
	if err != nil || defaults.port != 3000 {
		t.Fatalf("defaults = %#v, %v", defaults, err)
	}
	long, err := parseDevOptions([]string{"--port", "4310"})
	if err != nil || long.port != 4310 {
		t.Fatalf("--port = %#v, %v", long, err)
	}
	short, err := parseDevOptions([]string{"-p", "4311"})
	if err != nil || short.port != 4311 {
		t.Fatalf("-p = %#v, %v", short, err)
	}
	for _, args := range [][]string{{"--port", "0"}, {"--port", "65536"}, {"3001"}} {
		if _, err := parseDevOptions(args); err == nil {
			t.Fatalf("expected %v to fail", args)
		}
	}
}

func TestClassifyDevRebuildUsesGoOnlyFastPathConservatively(t *testing.T) {
	root := t.TempDir()
	website := filepath.Join(root, "examples", "seo-site")
	serverPage := "examples/seo-site/server/pages/products_slug/page.go"
	coLocatedPage := "examples/seo-site/app/products/[slug]/page.go"
	coLocatedActions := "examples/seo-site/app/products/[slug]/actions.go"
	coLocatedAPI := "examples/seo-site/app/api/time/route.go"
	sharedInternal := "examples/seo-site/internal/site/site.go"
	page := "examples/seo-site/app/products/[slug]/page.tsx"
	previous := map[string]string{
		serverPage:       "go-v1",
		coLocatedPage:    "page-go-v1",
		coLocatedActions: "actions-go-v1",
		coLocatedAPI:     "api-go-v1",
		sharedInternal:   "internal-go-v1",
		page:             "tsx-v1",
	}

	unchanged := cloneDevSnapshot(previous)
	if got := classifyDevRebuild(previous, unchanged, root, website); got != devRebuildNone {
		t.Fatalf("unchanged snapshot mode = %d", got)
	}

	goEdit := cloneDevSnapshot(previous)
	goEdit[serverPage] = "go-v2"
	if got := classifyDevRebuild(previous, goEdit, root, website); got != devRebuildGoOnly {
		t.Fatalf("existing server Go edit mode = %d", got)
	}
	for _, file := range []string{coLocatedPage, coLocatedActions, coLocatedAPI, sharedInternal} {
		goEdit = cloneDevSnapshot(previous)
		goEdit[file] = "go-v2"
		if got := classifyDevRebuild(previous, goEdit, root, website); got != devRebuildGoOnly {
			t.Fatalf("existing co-located Go edit %s mode = %d", file, got)
		}
	}

	tsxEdit := cloneDevSnapshot(previous)
	tsxEdit[page] = "tsx-v2"
	if got := classifyDevRebuild(previous, tsxEdit, root, website); got != devRebuildFull {
		t.Fatalf("TSX edit mode = %d", got)
	}

	frameworkEdit := cloneDevSnapshot(previous)
	frameworkEdit["renderer/render.go"] = "go-v2"
	previousWithFramework := cloneDevSnapshot(previous)
	previousWithFramework["renderer/render.go"] = "go-v1"
	if got := classifyDevRebuild(previousWithFramework, frameworkEdit, root, website); got != devRebuildFull {
		t.Fatalf("framework Go edit mode = %d", got)
	}

	addedGoFile := cloneDevSnapshot(previous)
	addedGoFile["examples/seo-site/app/api/new/route.go"] = "new"
	if got := classifyDevRebuild(previous, addedGoFile, root, website); got != devRebuildFull {
		t.Fatalf("added Go file mode = %d", got)
	}

	deletedGoFile := cloneDevSnapshot(previous)
	delete(deletedGoFile, coLocatedPage)
	if got := classifyDevRebuild(previous, deletedGoFile, root, website); got != devRebuildFull {
		t.Fatalf("deleted Go file mode = %d", got)
	}

	failedAddition := cloneDevSnapshot(previous)
	failedAddition["examples/seo-site/app/new/page.go"] = "invalid-v1"
	correctedAddition := cloneDevSnapshot(failedAddition)
	correctedAddition["examples/seo-site/app/new/page.go"] = "valid-v2"
	if got := classifyDevRebuild(previous, correctedAddition, root, website); got != devRebuildFull {
		t.Fatalf("edited file added after the last successful build mode = %d", got)
	}
}

func TestDevSnapshotsEqual(t *testing.T) {
	left := map[string]string{"a": "1", "b": "2"}
	if !devSnapshotsEqual(left, cloneDevSnapshot(left)) {
		t.Fatal("identical snapshots must compare equal")
	}
	if devSnapshotsEqual(left, map[string]string{"a": "1", "b": "changed"}) {
		t.Fatal("digest changes must compare unequal")
	}
	if devSnapshotsEqual(left, map[string]string{"a": "1"}) {
		t.Fatal("file removal must compare unequal")
	}
}

func TestDevCompilerInputsChanged(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "packages", "compiler", "tsconfig.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := map[string]string{
		"packages/compiler/src/cli.ts": "compiler-v1",
		"app/page.tsx":                 "page-v1",
	}
	pageEdit := cloneDevSnapshot(previous)
	pageEdit["app/page.tsx"] = "page-v2"
	if devCompilerInputsChanged(previous, pageEdit, root) {
		t.Fatal("website edits must reuse the prepared compiler")
	}
	compilerEdit := cloneDevSnapshot(previous)
	compilerEdit["packages/compiler/src/cli.ts"] = "compiler-v2"
	if !devCompilerInputsChanged(previous, compilerEdit, root) {
		t.Fatal("compiler source edits must invalidate the prepared compiler")
	}
	compilerAddition := cloneDevSnapshot(previous)
	compilerAddition["packages/compiler/src/new.ts"] = "new"
	if !devCompilerInputsChanged(previous, compilerAddition, root) {
		t.Fatal("new compiler source must invalidate the prepared compiler")
	}
}

func cloneDevSnapshot(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for path, digest := range source {
		result[path] = digest
	}
	return result
}

func TestDevGatewayAtomicallySwitchesBackendsAndInjectsReloadClient(t *testing.T) {
	first := devTestBackend(t, "first")
	defer first.Close()
	second := devTestBackend(t, "second")
	defer second.Close()

	gateway := newDevGateway()
	public := httptest.NewServer(gateway)
	defer public.Close()

	assertGatewayResponse := func(expected string) {
		t.Helper()
		response, err := http.Get(public.URL + "/products/example")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "<h1>"+expected+"</h1>") {
			t.Fatalf("response did not come from %s backend: %s", expected, body)
		}
		if strings.Count(string(body), "/_gobeyond/dev/client.js") != 1 {
			t.Fatalf("development client was not injected once: %s", body)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("cache-control = %q", response.Header.Get("Cache-Control"))
		}
	}

	gateway.switchTarget(mustURL(t, first.URL))
	assertGatewayResponse("first")
	gateway.switchTarget(mustURL(t, second.URL))
	assertGatewayResponse("second")

	response, err := http.Get(public.URL + "/_gobeyond/dev/client.js")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	client, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(client), "EventSource") {
		t.Fatalf("development client status=%d body=%s", response.StatusCode, client)
	}
}

func TestDevGatewayBroadcastsReloadEvents(t *testing.T) {
	gateway := newDevGateway()
	public := httptest.NewServer(gateway)
	defer public.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+"/_gobeyond/dev/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for line := ""; line != "\n"; {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
	}

	gateway.broadcast(devEvent{name: "reload"})
	event, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	data, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if event != "event: reload\n" || data != "data: \"\"\n" {
		t.Fatalf("unexpected event stream: %q %q", event, data)
	}
}

func devTestBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("ETag", "backend-etag")
		_, _ = io.WriteString(writer, "<!doctype html><body><h1>"+name+"</h1></body>")
	}))
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
