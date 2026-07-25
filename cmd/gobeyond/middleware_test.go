package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMiddlewareBuildTarget(t *testing.T) {
	website := t.TempDir()

	// No server/cmd/middleware: no middleware artifact, no error.
	target, err := middlewareBuildTarget(website)
	if err != nil || target != "" {
		t.Fatalf("middlewareBuildTarget without middleware = %q, %v", target, err)
	}

	middlewareDir := filepath.Join(website, "server", "cmd", "middleware")
	if err := os.MkdirAll(middlewareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target, err = middlewareBuildTarget(website)
	if err != nil {
		t.Fatal(err)
	}
	if target != middlewareDir {
		t.Fatalf("middlewareBuildTarget = %q, want %q", target, middlewareDir)
	}
}

func TestMiddlewareBuildTargetRejectsFile(t *testing.T) {
	website := t.TempDir()
	if err := os.MkdirAll(filepath.Join(website, "server", "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(website, "server", "cmd", "middleware"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := middlewareBuildTarget(website); err == nil {
		t.Fatal("expected error for non-directory server/cmd/middleware")
	}
}
