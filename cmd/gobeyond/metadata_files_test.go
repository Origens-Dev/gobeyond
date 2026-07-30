package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppMetadataPresent(t *testing.T) {
	root := t.TempDir()
	present, err := appMetadataPresent(root)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("empty project should not report metadata")
	}
	if err := os.MkdirAll(filepath.Join(root, "app", "blog"), 0o755); err != nil {
		t.Fatal(err)
	}
	present, err = appMetadataPresent(root)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("app without metadata files should not report metadata")
	}
	if err := os.WriteFile(filepath.Join(root, "app", "blog", "sitemap.ts"), []byte("export default function sitemap() { return [] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	present, err = appMetadataPresent(root)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("expected nested sitemap.ts to be detected")
	}
}

func TestMaterializeAppMetadataFilesNoopWithoutFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := materializeAppMetadataFiles(root, t.TempDir(), "/nonexistent/cli.js", os.Environ())
	if err != nil {
		t.Fatalf("expected no-op without metadata files, got %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v", paths)
	}
}
