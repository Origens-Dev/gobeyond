package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
)

func TestDiscoverWorkersRejectsRootDurables(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workers", "durables.go"), "package durables\n")

	_, err := DiscoverWorkers(root)
	if err == nil {
		t.Fatal("expected root workers/durables.go to be rejected")
	}
	if !strings.Contains(err.Error(), "workers/durables.go is not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "workers/default/durables.go") {
		t.Fatalf("error should point at workers/default/durables.go: %v", err)
	}
}

func TestDiscoverWorkersAcceptsDefaultFolder(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workers", "default", "durables.go"), "package durables\n")

	workers, err := DiscoverWorkers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %#v", workers)
	}
	if workers[0].ID != gb.DefaultWorkerID {
		t.Fatalf("id = %q, want %q", workers[0].ID, gb.DefaultWorkerID)
	}
	if workers[0].DurablesFile != "workers/default/durables.go" {
		t.Fatalf("DurablesFile = %q", workers[0].DurablesFile)
	}
	if workers[0].Key != WorkerKey(gb.DefaultWorkerID) {
		t.Fatalf("Key = %q, want %q", workers[0].Key, WorkerKey(gb.DefaultWorkerID))
	}
}

func TestDiscoverWorkersAcceptsNamedFolder(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workers", "demo", "durables.go"), "package demo\n")

	workers, err := DiscoverWorkers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].ID != "demo" {
		t.Fatalf("workers = %#v", workers)
	}
	if workers[0].DurablesFile != "workers/demo/durables.go" {
		t.Fatalf("DurablesFile = %q", workers[0].DurablesFile)
	}
}

func TestDiscoverWorkersMissingWorkersDir(t *testing.T) {
	root := t.TempDir()
	workers, err := DiscoverWorkers(root)
	if err != nil {
		t.Fatal(err)
	}
	if workers != nil {
		t.Fatalf("expected nil workers, got %#v", workers)
	}
	if _, err := os.Stat(filepath.Join(root, "workers")); !os.IsNotExist(err) {
		t.Fatalf("workers dir should be absent: %v", err)
	}
}
