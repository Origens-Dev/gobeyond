package runtime

import (
	"net/http"
	"testing"
)

func TestRequestDeploymentRevisionPrefersNewName(t *testing.T) {
	header := http.Header{}
	header.Set(DeploymentRevisionHeader, "new")
	header.Set("X-GoBeyond-Deployment", "old")
	if got := requestDeploymentRevision(header); got != "new" {
		t.Fatalf("got %q, want new", got)
	}
}

func TestRequestDeploymentRevisionFallsBackToLegacyName(t *testing.T) {
	header := http.Header{}
	header.Set("X-GoBeyond-Deployment", "old")
	if got := requestDeploymentRevision(header); got != "old" {
		t.Fatalf("got %q, want old", got)
	}
}
