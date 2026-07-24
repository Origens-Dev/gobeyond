package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFinalizedBuildIDIncludesBuildTimeOutputs(t *testing.T) {
	first := &compilerProjectOutput{
		APIVersion: "gobeyond.compiler-project/v1alpha1",
		Plans:      []json.RawMessage{json.RawMessage(`{"routeId":"root","root":{"kind":"text","value":{"kind":"literal","value":"page"}}}`)},
		Contracts:  json.RawMessage(`{"apiVersion":"gobeyond.contract/v1alpha1","routes":[],"actions":[]}`),
		StaticBuild: compilerStaticBuild{APIVersion: "gobeyond.static-build/v1alpha1", Routes: []compilerStaticRoute{{
			RouteID: "root", Entries: []compilerStaticEntry{{Props: json.RawMessage(`{"headline":"first"}`)}},
		}}},
	}
	firstID, err := finalizedBuildID("source", first)
	if err != nil {
		t.Fatal(err)
	}
	first.StaticBuild.Routes[0].Entries[0].Props = json.RawMessage(`{"headline":"changed CMS output"}`)
	secondID, err := finalizedBuildID("source", first)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("build-time props must contribute to the final build ID")
	}
}

func TestFinalizedBuildIDIncludesClientBoundaries(t *testing.T) {
	compiled := &compilerProjectOutput{
		APIVersion: "gobeyond.compiler-project/v1alpha1",
		Plans:      []json.RawMessage{json.RawMessage(`{"routeId":"root","root":{"kind":"clientOnly"}}`)},
		Contracts:  json.RawMessage(`{"apiVersion":"gobeyond.contract/v1alpha1","routes":[],"actions":[]}`),
		StaticBuild: compilerStaticBuild{
			APIVersion: "gobeyond.static-build/v1alpha1",
			Routes:     []compilerStaticRoute{},
		},
		ClientBoundaries: compilerClientBoundaryManifest{
			APIVersion: "gobeyond.client-boundaries/v1alpha1",
			Boundaries: []compilerClientBoundaryRecord{{
				ID: "cb_first", RouteID: "root", Source: "app/page.tsx",
				Component: "Widget", Boundary: "app/page.tsx", Reason: "window is browser-only",
				Target: "callSite", Start: 12, End: 21, Line: 2, Column: 3,
			}},
		},
	}
	firstID, err := finalizedBuildID("source", compiled)
	if err != nil {
		t.Fatal(err)
	}
	compiled.ClientBoundaries.Boundaries[0].Reason = "document is browser-only"
	secondID, err := finalizedBuildID("source", compiled)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("client-boundary changes must contribute to the final build ID")
	}
}

func TestCollectBrowserAssetsUsesExactEmittedPaths(t *testing.T) {
	root := t.TempDir()
	assetRoot := filepath.Join(root, "_gobeyond", "builds", "build-1", "assets")
	writeAssetTestFile(t, filepath.Join(assetRoot, "app.js"), "export {}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "assets", "site-z9.css"), "body{}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "assets", "theme-a1.css"), ":root{}")

	assets, err := collectBrowserAssets(root, "build-1", browserBuildInput{})
	if err != nil {
		t.Fatal(err)
	}
	if assets.ClientScript != "/_gobeyond/builds/build-1/assets/app.js" {
		t.Fatalf("client script = %q", assets.ClientScript)
	}
	want := []string{
		"/_gobeyond/builds/build-1/assets/assets/site-z9.css",
		"/_gobeyond/builds/build-1/assets/assets/theme-a1.css",
	}
	if !reflect.DeepEqual(assets.Styles, want) {
		t.Fatalf("styles = %#v, want %#v", assets.Styles, want)
	}
}

func TestCollectBrowserAssetsProjectsRouteAwareViteManifest(t *testing.T) {
	root := t.TempDir()
	assetRoot := filepath.Join(root, "_gobeyond", "builds", "build-1", "assets")
	entry := filepath.Join(t.TempDir(), ".gobeyond", "client-entry.tsx")
	route := filepath.Join(filepath.Dir(entry), "routes", "home.tsx")
	writeAssetTestFile(t, filepath.Join(assetRoot, "app.js"), "export {}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "chunks", "home-a1.js"), "export default function(){}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "chunks", "shared-b1.js"), "export {}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "assets", "base.css"), "body{}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "assets", "home.css"), "main{}")
	writeAssetTestFile(t, filepath.Join(assetRoot, ".vite", "manifest.json"), `{
  ".gobeyond/client-entry.tsx": {"file":"app.js","src":".gobeyond/client-entry.tsx","isEntry":true,"css":["assets/base.css"]},
  ".gobeyond/routes/home.tsx": {"file":"chunks/home-a1.js","src":".gobeyond/routes/home.tsx","isDynamicEntry":true,"imports":["shared"],"css":["assets/home.css"]},
  "shared": {"file":"chunks/shared-b1.js","src":"src/shared.ts"}
}`)

	assets, err := collectBrowserAssets(root, "build-1", browserBuildInput{
		EntryFile:    entry,
		RouteEntries: map[string]string{"home": route},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(assetRoot, ".vite", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("internal Vite manifest should not be deployed: %v", err)
	}
	if assets.APIVersion != "gobeyond.browser-assets/v1alpha1" || assets.BuildID != "build-1" {
		t.Fatalf("manifest identity = %q %q", assets.APIVersion, assets.BuildID)
	}
	routeAssets, err := assets.ForRoute("home")
	if err != nil {
		t.Fatal(err)
	}
	if routeAssets.Bootstrap != "/_gobeyond/builds/build-1/assets/app.js" {
		t.Fatalf("bootstrap = %q", routeAssets.Bootstrap)
	}
	wantPreloads := []string{
		"/_gobeyond/builds/build-1/assets/chunks/home-a1.js",
		"/_gobeyond/builds/build-1/assets/chunks/shared-b1.js",
	}
	if !reflect.DeepEqual(routeAssets.ModulePreloads, wantPreloads) {
		t.Fatalf("preloads = %#v, want %#v", routeAssets.ModulePreloads, wantPreloads)
	}
	wantStyles := []string{
		"/_gobeyond/builds/build-1/assets/assets/base.css",
		"/_gobeyond/builds/build-1/assets/assets/home.css",
	}
	if !reflect.DeepEqual(routeAssets.Styles, wantStyles) {
		t.Fatalf("styles = %#v, want %#v", routeAssets.Styles, wantStyles)
	}
}

func TestViteBuildUsesVersionedAssetBase(t *testing.T) {
	got := viteBuildArguments("/site/vite.config.ts", "build-1", "production")
	want := []string{
		"build",
		"--config",
		"/site/vite.config.ts",
		"--manifest",
		"--mode",
		"production",
		"--base",
		"/_gobeyond/builds/build-1/assets/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected Vite arguments:\nwant %#v\n got %#v", want, got)
	}
}

func TestBrowserNodeEnvironmentMatchesBuildMode(t *testing.T) {
	if got := browserNodeEnvironment("development"); got != "development" {
		t.Fatalf("development NODE_ENV = %q", got)
	}
	if got := browserNodeEnvironment("production"); got != "production" {
		t.Fatalf("production NODE_ENV = %q", got)
	}
}

func TestDiscoverPublicAssetsProducesCloudFrontPaths(t *testing.T) {
	root := t.TempDir()
	writeAssetTestFile(t, filepath.Join(root, "images", "product.svg"), "<svg/>")
	writeAssetTestFile(t, filepath.Join(root, "robots.txt"), "User-agent: *")

	assets, err := discoverPublicAssets(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/images/product.svg", "/robots.txt"}
	if !reflect.DeepEqual(assets, want) {
		t.Fatalf("assets = %#v, want %#v", assets, want)
	}
}

func writeAssetTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
