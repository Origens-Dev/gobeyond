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
	assetRoot := filepath.Join(root, "_gobeyond", "assets", "build-1")
	writeAssetTestFile(t, filepath.Join(assetRoot, "app.js"), "export {}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "assets", "site-z9.css"), "body{}")
	writeAssetTestFile(t, filepath.Join(assetRoot, "assets", "theme-a1.css"), ":root{}")

	assets, err := collectBrowserAssets(root, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if assets.ClientScript != "/_gobeyond/assets/build-1/app.js" {
		t.Fatalf("client script = %q", assets.ClientScript)
	}
	want := []string{
		"/_gobeyond/assets/build-1/assets/site-z9.css",
		"/_gobeyond/assets/build-1/assets/theme-a1.css",
	}
	if !reflect.DeepEqual(assets.Styles, want) {
		t.Fatalf("styles = %#v, want %#v", assets.Styles, want)
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
