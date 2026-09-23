package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
