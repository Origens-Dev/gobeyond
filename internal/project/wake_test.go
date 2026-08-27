package project

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildWakeMapIncludesActivityAndSubworkflowQueues(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workflows", "parent", "workflow.go"), `package parent

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context) error {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityParentCharge, "x")
	_ = gbworkflows.ExecuteSubworkflow(ctx, references.WorkflowParentFulfill, "y")
	return nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "shop.Parent", TaskQueue: "orders"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "parent", "activities", "charge", "activity.go"), `package charge

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context, input string) (string, error) { return input, nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "shop.Parent.charge", TaskQueue: "billing"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "parent", "subworkflows", "fulfill", "workflow.go"), `package fulfill

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context, input string) (string, error) {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityParentFulfillShip, input)
	return input, nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "shop.Parent.fulfill", TaskQueue: "fulfillment"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "parent", "subworkflows", "fulfill", "activities", "ship", "activity.go"), `package ship

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context, input string) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "shop.Parent.fulfill.ship", TaskQueue: "shipping"}, Run)
`)

	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	wakeMap, err := BuildWakeMap(root, definitions)
	if err != nil {
		t.Fatal(err)
	}
	got := wakeMap["shop.Parent"]
	want := []string{"billing", "fulfillment", "orders", "shipping"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parent wake = %#v, want %#v", got, want)
	}
	gotFulfill := wakeMap["shop.Parent.fulfill"]
	wantFulfill := []string{"fulfillment", "shipping"}
	if !reflect.DeepEqual(gotFulfill, wantFulfill) {
		t.Fatalf("fulfill wake = %#v, want %#v", gotFulfill, wantFulfill)
	}
}

func TestBuildWakeMapControlPlaneBuildUsesGeneralAndRestricted(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workflows", "build-environment", "workflow.go"), `package buildenvironment

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context) error {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityBuildEnvironmentExecute, nil)
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityDeployEnvironmentExecute, nil)
	return nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "control-plane.BuildEnvironment", TaskQueue: "control-plane-workflows",
}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "build-environment", "activities", "execute", "activity.go"), `package execute

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{
	Name: "control-plane.build.execute", TaskQueue: "control-plane-general",
}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "deploy-environment", "workflow.go"), `package deployenvironment

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context) error {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityDeployEnvironmentExecute, nil)
	return nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "control-plane.DeployEnvironment", TaskQueue: "control-plane-workflows",
}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "deploy-environment", "activities", "execute", "activity.go"), `package execute

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{
	Name: "control-plane.deploy.execute", TaskQueue: "control-plane-restricted",
}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "reconcile-edge-tenants", "workflow.go"), `package reconcileedgetenants

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context) error {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityReconcileEdgeTenantsExecute, nil)
	return nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "control-plane.ReconcileEdgeTenants", TaskQueue: "control-plane-edge",
}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "reconcile-edge-tenants", "activities", "execute", "activity.go"), `package execute

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{
	Name: "control-plane.reconcile-edge-tenants.execute", TaskQueue: "control-plane-edge",
}, Run)
`)

	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	wakeMap, err := BuildWakeMap(root, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := wakeMap["control-plane.BuildEnvironment"], []string{
		"control-plane-general", "control-plane-restricted", "control-plane-workflows",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("build wake = %#v, want %#v", got, want)
	}
	if got, want := wakeMap["control-plane.DeployEnvironment"], []string{
		"control-plane-restricted", "control-plane-workflows",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deploy wake = %#v, want %#v", got, want)
	}
	if got, want := wakeMap["control-plane.ReconcileEdgeTenants"], []string{"control-plane-edge"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("edge wake = %#v, want %#v", got, want)
	}
}

func TestBuildWakeMapPortalDeployIncludesGeneralAndRestricted(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workflows", "deploy-environment", "workflow.go"), `package deployenvironment

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context) error {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityDeployEnvironmentPrepare, nil)
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityDeployEnvironmentApply, nil)
	return nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "DeployEnvironment", TaskQueue: "portal-workflows"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "deploy-environment", "activities", "prepare", "activity.go"), `package prepare

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "portal.deploy.prepare", TaskQueue: "portal-general"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "deploy-environment", "activities", "apply", "activity.go"), `package apply

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "portal.deploy.apply", TaskQueue: "portal-restricted"}, Run)
`)

	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	wakeMap, err := BuildWakeMap(root, definitions)
	if err != nil {
		t.Fatal(err)
	}
	got := wakeMap["DeployEnvironment"]
	want := []string{"portal-general", "portal-restricted", "portal-workflows"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("portal deploy wake = %#v, want %#v", got, want)
	}
}

func TestGeneratedWakeSourceAndManifest(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeSourceTestFile(t, filepath.Join(root, "app", "page.tsx"), "export default function Page() { return null }\n")
	writeSourceTestFile(t, filepath.Join(root, "workflows", "demo", "workflow.go"), `package demo

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"example.com/site/generated/workflows/references"
)

func Run(ctx workflow.Context) error {
	_ = gbworkflows.ExecuteActivityReference(ctx, references.ActivityDemoWork, nil)
	return nil
}
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "demo.echo", TaskQueue: "default"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "demo", "activities", "work", "activity.go"), `package work

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "demo.work", TaskQueue: "workers"}, Run)
`)

	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(root, routes, "b_wake", false); err != nil {
		t.Fatal(err)
	}
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "workflows", "wake", "wake_gen.go"),
		"package wake",
		`case "demo.echo"`,
		`"default"`,
		`"workers"`,
		"func WakeWorkers(name string)",
	)
	bytes, err := MarshalWakeManifest(root, mustDiscoverWorkflows(t, root))
	if err != nil {
		t.Fatal(err)
	}
	text := string(bytes)
	if !strings.Contains(text, `"v": 1`) || !strings.Contains(text, `"demo.echo"`) {
		t.Fatalf("wake.json = %s", text)
	}
}

func mustDiscoverWorkflows(t *testing.T, root string) []WorkflowDefinition {
	t.Helper()
	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	return definitions
}
