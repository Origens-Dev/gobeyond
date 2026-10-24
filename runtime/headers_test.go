package runtime

import (
	"net/http"
	"testing"
)

func requireDualHeader(t *testing.T, header http.Header, name, legacyName, want string) {
	t.Helper()
	if header.Get(name) != want || header.Get(legacyName) != want {
		t.Fatalf("want %s and %s = %q, got %v", name, legacyName, want, header)
	}
}

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

func TestDualWriteEmitsCurrentAndLegacyNames(t *testing.T) {
	header := http.Header{}
	setBuildIDHeader(header, "build-1")
	setBuildErrorHeader(header, "build_mismatch")
	setDeploymentRevisionHeader(header, "rev-1")
	requireDualHeader(t, header, BuildIDHeader, "X-GoBeyond-Build", "build-1")
	requireDualHeader(t, header, BuildErrorHeader, "X-GoBeyond-Error", "build_mismatch")
	requireDualHeader(t, header, DeploymentRevisionHeader, "X-GoBeyond-Deployment", "rev-1")
}
