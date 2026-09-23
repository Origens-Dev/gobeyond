package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Origens-Dev/gobeyond/internal/project"
)

func TestCheckpointPreservesRawBytesAndEmptyJSONCollections(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".gobeyond"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := buildCheckpoint{Version: 1, Root: root, Dist: filepath.Join(root, "dist"), ProjectRoot: websiteRoot(root), Manifest: project.Manifest{BuildID: "test"}, Compiled: &compilerProjectOutput{
		Contracts: json.RawMessage("{\n  \"a\": 1\n}"), Plans: []json.RawMessage{json.RawMessage("{ \"plan\": true }")},
		StaticBuild: compilerStaticBuild{Routes: []compilerStaticRoute{{LayoutFiles: []string{}, Entries: []compilerStaticEntry{{Params: map[string]any{}, Props: json.RawMessage(`{}`)}}}}},
	}}
	before, _ := json.Marshal(c.Compiled.StaticBuild)
	if err := writeBuildCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	got, err := readBuildCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(got.Compiled.StaticBuild)
	if !bytes.Equal(before, after) || !bytes.Equal(c.Compiled.Contracts, got.Compiled.Contracts) || !bytes.Equal(c.Compiled.Plans[0], got.Compiled.Plans[0]) {
		t.Fatal("checkpoint changed compiled content")
	}
}

func TestWorkerCheckpointCompilesWithoutNodeOrRegeneration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOS", "linux")
	t.Setenv("GOARCH", "arm64")
	pkg := filepath.Join(root, "generated", "cmd", "workflows", "one")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module checkpoint.test\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := []byte("package main\nfunc main() {}\n")
	file := filepath.Join(pkg, "main.go")
	if err := os.WriteFile(file, source, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".gobeyond"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := buildCheckpoint{Version: 1, Root: root, Dist: filepath.Join(root, "dist"), ProjectRoot: websiteRoot(root), Manifest: project.Manifest{BuildID: "test-build"}, Compiled: &compilerProjectOutput{}, Workers: []workerBuildTargetInfo{{ID: "one", PackageDir: pkg}}}
	if err := writeBuildCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(checkpointPath(root))
	if err != nil {
		t.Fatal(err)
	}
	// There is deliberately no package.json, node_modules, or authored route.
	if err := resumeWorkerCheckpoint(root, "one"); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(filepath.Join(root, "dist", "workers", "one", "gobeyond-worker"))
	if err != nil {
		t.Fatal(err)
	}
	if len(binary) < 4 || !bytes.Equal(binary[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		t.Fatal("worker did not produce a Linux binary")
	}
	after, _ := os.ReadFile(checkpointPath(root))
	authored, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) || !bytes.Equal(source, authored) {
		t.Fatal("worker compilation changed prepared source")
	}
	if err := resumeWorkerCheckpoint(root, "missing"); err == nil {
		t.Fatal("undeclared worker accepted")
	}
	c.Root = "/different/root"
	if err := writeJSONFile(checkpointPath(root), c); err != nil {
		t.Fatal(err)
	}
	if _, err := readBuildCheckpoint(root); err == nil {
		t.Fatal("relocated checkpoint accepted")
	}
}
