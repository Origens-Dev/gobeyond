package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionExecutablesAreStaticWithImageOptimizer(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.MkdirTemp(root, ".static-executable-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixture) })
	// Import the actual optimizer: its optional purego codec dependency can
	// introduce PT_INTERP even when the Go toolchain has CGO_ENABLED=0.
	if err := os.WriteFile(filepath.Join(fixture, "main.go"), []byte("package main\nimport _ \"github.com/Origens-Dev/gobeyond/imageopt\"\nfunc main(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	environment := withEnvironment(compilerEnvironment(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "GOFLAGS=")
	safeOutputs := map[string]string{}
	for _, name := range []string{"server", "worker"} {
		output := filepath.Join(t.TempDir(), name)
		if err := buildProductionExecutable(root, environment, output, fixture); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := verifyStaticELF(output); err != nil {
			t.Fatal(err)
		}
		safeOutputs[name] = output
	}
	// Prove the guard rejects the original failure mode, rather than merely
	// accepting every CGO-disabled executable.
	dynamic := filepath.Join(t.TempDir(), "unsafe-default")
	command := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", dynamic, fixture)
	command.Dir, command.Env = root, environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build original failure fixture: %v\n%s", err, output)
	}
	if err := verifyStaticELF(dynamic); err == nil || !strings.Contains(err.Error(), "dynamic interpreter") {
		t.Fatalf("unprotected image optimizer executable must be rejected: %v", err)
	}
	dist := t.TempDir()
	copyOutput := func(source, relative string) {
		t.Helper()
		bytes, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dist, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, bytes, 0755); err != nil {
			t.Fatal(err)
		}
	}
	copyOutput(safeOutputs["server"], "server/gobeyond-server")
	copyOutput(dynamic, "workers/restored/gobeyond-worker")
	if err := verifyProductionArtifacts(environment, dist); err == nil {
		t.Fatal("accepted dynamic restored worker in final artifacts")
	}
	copyOutput(safeOutputs["worker"], "workers/restored/gobeyond-worker")
	if err := verifyProductionArtifacts(environment, dist); err != nil {
		t.Fatal(err)
	}
	copyOutput(dynamic, "server/gobeyond-server")
	if err := verifyProductionArtifacts(environment, dist); err == nil {
		t.Fatal("accepted dynamic restored server in final artifacts")
	}
}

func TestProductionTargetOSHandlesEmptyOverride(t *testing.T) {
	for _, environment := range [][]string{nil, {"GOOS="}} {
		if got := productionTargetOS(environment); got != runtime.GOOS {
			t.Fatalf("target OS=%q, want host default %q", got, runtime.GOOS)
		}
	}
	if got := productionTargetOS([]string{"GOOS=linux"}); got != "linux" {
		t.Fatalf("target OS=%q, want linux", got)
	}
}
