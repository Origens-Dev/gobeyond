package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/buildpaths"
)

func TestBuildEdgeMiddleware(t *testing.T) {
	root := workspaceRoot(t)
	if _, err := os.Stat(filepath.Join(root, "node_modules", ".bin", "vite")); err != nil {
		t.Skip("workspace Vite is unavailable")
	}
	website := t.TempDir()
	source := filepath.Join(website, "middleware.ts")
	if err := os.WriteFile(source, []byte(`export default function middleware(request: Request) {
  return new Response(new URL(request.url).pathname, { status: 202 })
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(t.TempDir(), "dist")
	if err := buildEdgeMiddleware(root, website, dist, source, os.Environ(), "production"); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dist, buildpaths.EdgeMiddlewareDir, buildpaths.EdgeMiddlewareEntryName)
	data, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fetch(request)") {
		t.Fatalf("middleware bundle does not expose the module-worker fetch shape:\n%s", data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	check := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", `
import { pathToFileURL } from 'node:url'
const worker = (await import(pathToFileURL(process.argv[1]).href)).default
const response = await worker.fetch(new Request('https://example.test/ready'))
if (typeof worker.fetch !== 'function' || response.status !== 202 || await response.text() !== '/ready') process.exit(1)
`, entry)
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("load middleware bundle: %v\n%s", err, output)
	}
}

func TestEdgeMiddlewareBundle(t *testing.T) {
	build := t.TempDir()
	if entry, found, err := edgeMiddlewareBundle(build); err != nil || found || entry != "" {
		t.Fatalf("missing edge middleware = %q, %v, %v", entry, found, err)
	}
	entry := filepath.Join(build, buildpaths.EdgeMiddlewareDir, buildpaths.EdgeMiddlewareEntryName)
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("export default { fetch() {} }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, found, err := edgeMiddlewareBundle(build); err != nil || !found || got != entry {
		t.Fatalf("edge middleware = %q, %v, %v; want %q, true, nil", got, found, err, entry)
	}
}

func TestEdgeMiddlewareRunnerRedirectsAndPassesThrough(t *testing.T) {
	root := workspaceRoot(t)
	if _, err := os.Stat(filepath.Join(root, "node_modules", ".bin", "vite")); err != nil {
		t.Skip("workspace Vite is unavailable")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/__gobeyond/readyz" {
			writer.WriteHeader(http.StatusOK)
			return
		}
		payload := []byte("origin:" + request.URL.Path)
		if request.URL.Path == "/through" {
			var compressed bytes.Buffer
			zipper := gzip.NewWriter(&compressed)
			_, _ = zipper.Write(payload)
			_ = zipper.Close()
			writer.Header().Set("Content-Encoding", "gzip")
			_, _ = writer.Write(compressed.Bytes())
			return
		}
		_, _ = writer.Write(payload)
	}))
	defer upstream.Close()

	website := t.TempDir()
	source := filepath.Join(website, "middleware.ts")
	if err := os.WriteFile(source, []byte(`export default async function middleware(request: Request) {
  const url = new URL(request.url)
  if (url.pathname === '/old') return Response.redirect(new URL('/new', request.url), 308)
  const response = await fetch(request)
  if (!response.url.endsWith(url.pathname)) return new Response('missing response URL', { status: 502 })
  return response
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(t.TempDir(), "dist")
	if err := buildEdgeMiddleware(root, website, build, source, os.Environ(), "development"); err != nil {
		t.Fatal(err)
	}
	entry, found, err := edgeMiddlewareBundle(build)
	if err != nil || !found {
		t.Fatalf("edge middleware bundle = %q, %v, %v", entry, found, err)
	}
	address, err := availableLoopbackAddress()
	if err != nil {
		t.Fatal(err)
	}
	publicOrigin := "http://" + address
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process, err := startDevProcess("middleware test", edgeMiddlewareCommand(ctx, root, entry, address, upstream.URL, publicOrigin, os.Environ()))
	if err != nil {
		t.Fatal(err)
	}
	defer stopDevProcess(process)
	target, _ := url.Parse(publicOrigin)
	if err := waitForDevProcess(process, target, publicOrigin); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	redirect, err := client.Get(publicOrigin + "/old")
	if err != nil {
		t.Fatal(err)
	}
	defer redirect.Body.Close()
	location, err := url.Parse(redirect.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if redirect.StatusCode != http.StatusPermanentRedirect || location.Path != "/new" {
		t.Fatalf("redirect = %d %q", redirect.StatusCode, redirect.Header.Get("Location"))
	}

	response, err := client.Get(publicOrigin + "/through")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "origin:/through" {
		t.Fatalf("pass-through body = %q", body)
	}
}
