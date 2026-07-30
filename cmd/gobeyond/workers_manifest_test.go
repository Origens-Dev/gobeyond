package main

import (
	"testing"

	"github.com/Origens-Dev/gobeyond/buildpaths"
)

func TestWorkersDeployManifestStampsTemporalAdapter(t *testing.T) {
	entries := []map[string]any{
		{
			"id":        "default",
			"key":       "default",
			"entry":     "workers/default/gobeyond-worker",
			"taskQueue": "default__local",
		},
	}
	got := workersDeployManifest(entries)
	if got["v"] != 1 {
		t.Fatalf("v = %v, want 1", got["v"])
	}
	if got["adapter"] != buildpaths.DurablesAdapterTemporal {
		t.Fatalf("adapter = %v, want %q", got["adapter"], buildpaths.DurablesAdapterTemporal)
	}
	workers, ok := got["workers"].([]map[string]any)
	if !ok || len(workers) != 1 || workers[0]["id"] != "default" {
		t.Fatalf("workers = %#v", got["workers"])
	}
}
