package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/imageopt"
)

func TestTargetPlanDefersNodeAndSeparatesWorkflowImplementation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/plan\n\ngo 1.24\n")
	write("app/page.tsx", "export default function Page(){ return <p>Hello</p> }\n")
	write("server/cmd/app/main.go", "package main\nfunc main(){}\n")
	workflow := `package demo
import (
 gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
 "go.temporal.io/sdk/workflow"
)
func Run(ctx workflow.Context) error { return nil }
var Workflow=gbworkflows.Define(gbworkflows.WorkflowConfig{Name:"demo.run",TaskQueue:"one"},Run)
`
	write("workflows/demo/workflow.go", workflow)
	bin := filepath.Join(root, ".gobeyond-test-bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "npm", "pnpm"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	plan := func() buildCheckpoint {
		t.Helper()
		if err := prepareTargetPlan(root); err != nil {
			t.Fatal(err)
		}
		c, err := readBuildCheckpoint(root)
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != 2 || c.Compiled != nil || len(c.Workers) != 1 {
			t.Fatalf("not an early target plan: %+v", c)
		}
		if c.Fingerprints["web"] == "" {
			t.Fatal("web fingerprint unavailable")
		}
		return c
	}
	first := plan()
	if !first.NeedsPortablePreparation {
		t.Fatal("unresolved worker graph did not request full preparation")
	}
	write("workflows/demo/workflow.go", workflow+"\n// implementation-only change\n")
	second := plan()
	if first.Manifest.BuildID != second.Manifest.BuildID {
		t.Fatal("unrelated workflow implementation invalidated web")
	}
	write("workflows/demo/workflow.go", strings.ReplaceAll(workflow, "demo.run", "demo.changed"))
	if plan().Manifest.BuildID == first.Manifest.BuildID {
		t.Fatal("workflow declaration failed to invalidate web metadata")
	}
	write("workflows/demo/workflow.go", workflow)
	write("app/page.tsx", "export default function Page(){ return <p>Changed</p> }\n")
	if plan().Manifest.BuildID == first.Manifest.BuildID {
		t.Fatal("web edit did not invalidate web")
	}
}

func TestPlannedWebGraphDoesNotHideUnresolvedDependencies(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/site\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c := &buildCheckpoint{ProjectRoot: root}
	graph := fingerprintGraph{"server": {Dir: root, Incomplete: true, Imports: []string{"example.test/missing"}}, "example.test/missing": {Error: &struct{ Err string }{"missing"}}}
	if _, err := plannedWebGraph(c, graph).digest(root, nil, nil); err == nil {
		t.Fatal("missing non-contract dependency allowed reuse")
	}
}

func TestTargetPlanPreservesAuthoredImageConfig(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/image-plan\n\ngo 1.24\n")
	write("app/page.tsx", "export default function Page(){ return <p>Hello</p> }\n")
	write("server/cmd/app/main.go", "package main\nfunc main(){}\n")
	config := `{"remoteDomains":["images.ctfassets.net"],"cacheSeconds":60}`
	write(imageopt.DeploymentConfigPath, config)
	if err := os.Chmod(filepath.Join(root, imageopt.DeploymentConfigPath), 0o600); err != nil {
		t.Fatal(err)
	}
	write(".gobeyond/client-boundaries.json", "stale generated state")
	write("generated/stale.go", "package stale\n")
	write("dist/stale", "stale build output")

	if err := prepareTargetPlan(root); err != nil {
		t.Fatal(err)
	}
	got, ok, err := imageopt.LoadDeploymentConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(got.RemoteDomains) != 1 || got.RemoteDomains[0] != "images.ctfassets.net" || got.CacheSeconds != 60 {
		t.Fatalf("authored deployment config after target plan = %+v, present=%v", got, ok)
	}
	raw, err := os.ReadFile(filepath.Join(root, imageopt.DeploymentConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != config {
		t.Fatalf("image config bytes changed: %q", raw)
	}
	info, err := os.Stat(filepath.Join(root, imageopt.DeploymentConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("image config mode changed: %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, ".gobeyond", "client-boundaries.json")); !os.IsNotExist(err) {
		t.Fatalf("stale generated projection survived reset: err=%v", err)
	}
	first, err := readBuildCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	write(imageopt.DeploymentConfigPath, `{"remoteDomains":["images.ctfassets.net"],"cacheSeconds":120}`)
	if err := prepareTargetPlan(root); err != nil {
		t.Fatal(err)
	}
	second, err := readBuildCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.BuildID == second.Manifest.BuildID {
		t.Fatal("image deployment config edit did not change web build identity")
	}
	for _, stale := range []string{"generated/stale.go", "dist/stale"} {
		if _, err := os.Stat(filepath.Join(root, stale)); !os.IsNotExist(err) {
			t.Fatalf("stale output %s survived reset: err=%v", stale, err)
		}
	}
}
