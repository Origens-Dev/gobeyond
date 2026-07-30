package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppMetadataLikelyPresent(t *testing.T) {
	root := t.TempDir()
	if appMetadataLikelyPresent(root) {
		t.Fatal("empty project should not report metadata")
	}
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "robots.ts"), []byte("export default function robots() { return {} }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !appMetadataLikelyPresent(root) {
		t.Fatal("expected robots.ts to be detected")
	}
}
