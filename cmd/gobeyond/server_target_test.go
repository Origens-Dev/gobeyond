package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServerBuildTargetPrefersCustomComposition(t *testing.T) {
	root := t.TempDir()
	generated := filepath.Join(root, "generated", "cmd", "site")
	custom := filepath.Join(root, "server", "cmd", "app")
	for _, dir := range []string{generated, custom} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := serverBuildTarget(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Fatalf("server target = %q, want custom composition %q", got, custom)
	}
}

func TestServerBuildTargetFallsBackToGeneratedSite(t *testing.T) {
	root := t.TempDir()
	generated := filepath.Join(root, "generated", "cmd", "site")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := serverBuildTarget(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != generated {
		t.Fatalf("server target = %q, want generated site %q", got, generated)
	}
}
