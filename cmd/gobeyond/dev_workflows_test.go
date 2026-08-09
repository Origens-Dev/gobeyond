package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Origens-Dev/gobeyond/buildpaths"
)

func TestDiscoverDevWorkflowWorkersUsesPrivateWorkerABI(t *testing.T) {
	buildDirectory := t.TempDir()
	for _, queue := range []string{"orders", "default"} {
		binary := filepath.Join(buildDirectory, buildpaths.WorkersDir, queue, buildpaths.WorkerEntryName)
		if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(buildDirectory, buildpaths.WorkersDir, "ignored.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}

	workers, err := discoverDevWorkflowWorkers(buildDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 || workers[0].queue != "default" || workers[1].queue != "orders" {
		t.Fatalf("workflow workers = %#v", workers)
	}
	if filepath.Base(workers[0].binary) != buildpaths.WorkerEntryName {
		t.Fatalf("worker binary = %q", workers[0].binary)
	}
}

func TestDiscoverDevWorkflowWorkersAllowsNoQueues(t *testing.T) {
	workers, err := discoverDevWorkflowWorkers(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if workers != nil {
		t.Fatalf("workers = %#v", workers)
	}
}
