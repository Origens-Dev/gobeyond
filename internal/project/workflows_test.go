package project

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverWorkflowDefinitionsResolvesNestedQueues(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workflows", "process-order", "workflow.go"), `package processorder

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx workflow.Context, input string) (string, error) { return input, nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{TaskQueue: "orders"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "process-order", "activities", "charge", "activity.go"), `package charge

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context, input string) (string, error) { return input, nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "process-order", "subworkflows", "fulfill", "workflow.go"), `package fulfill

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx workflow.Context, input string) (string, error) { return input, nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{TaskQueue: "fulfillment"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "process-order", "subworkflows", "fulfill", "activities", "ship", "activity.go"), `package ship

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx context.Context, input string) error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "process-order", "subworkflows", "audit", "workflow.go"), `package audit

import (
	"go.temporal.io/sdk/workflow"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(ctx workflow.Context) error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{}, Run)
`)

	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 5 {
		t.Fatalf("definitions = %#v", definitions)
	}
	byID := make(map[string]WorkflowDefinition)
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	for id, queue := range map[string]string{
		"process-order":              "orders",
		"process-order.charge":       "orders",
		"process-order.audit":        "orders",
		"process-order.fulfill":      "fulfillment",
		"process-order.fulfill.ship": "fulfillment",
	} {
		if got := byID[id].TaskQueue; got != queue {
			t.Errorf("%s queue = %q, want %q", id, got, queue)
		}
	}
	if !byID["process-order"].Public || byID["process-order.audit"].Public {
		t.Fatalf("public boundary was not preserved: %#v", byID)
	}
	if byID["process-order.fulfill.ship"].ParentID != "process-order.fulfill" {
		t.Fatalf("activity should belong only to nearest subworkflow: %#v", byID["process-order.fulfill.ship"])
	}
}

func TestDiscoverWorkflowDefinitionsStandaloneActivity(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workflows", "send-receipt", "activity.go"), `package receipt

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

type Input struct { OrderID string }
type Output struct { Sent bool }
func Send(ctx context.Context, input Input) (Output, error) { return Output{Sent: true}, nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "receipt.send"}, Send)
`)

	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	definition := definitions[0]
	if !definition.Standalone || !definition.Public || definition.TaskQueue != "default" {
		t.Fatalf("standalone definition = %#v", definition)
	}
	if !definition.HandlerHasInput || definition.InputType != "Input" || !definition.HandlerHasOutput || definition.OutputType != "Output" {
		t.Fatalf("standalone signature = %#v", definition)
	}
}

func TestGroupWorkerQueuesIncludesOnlyDurableAgents(t *testing.T) {
	queues := GroupWorkerQueues(
		[]WorkflowDefinition{{ID: "orders", TaskQueue: "shared"}},
		[]AgentDefinition{
			{ID: "durable-shared", Durable: true, TaskQueue: "shared"},
			{ID: "durable-only", Durable: true, TaskQueue: "agents"},
			{ID: "direct", TaskQueue: "agents"},
		},
	)
	if len(queues) != 2 || queues[0].ID != "agents" || queues[1].ID != "shared" {
		t.Fatalf("worker queues = %#v", queues)
	}
	if len(queues[0].Definitions) != 0 || len(queues[0].Agents) != 1 || queues[0].Agents[0].ID != "durable-only" {
		t.Fatalf("agent-only queue = %#v", queues[0])
	}
	if len(queues[1].Definitions) != 1 || len(queues[1].Agents) != 1 || queues[1].Agents[0].ID != "durable-shared" {
		t.Fatalf("shared queue = %#v", queues[1])
	}
}

func TestDiscoverWorkflowDefinitionsRejectsLegacyWorkers(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workers", "default", "durables.go"), "package durables\n")

	_, err := DiscoverWorkflowDefinitions(root)
	if err == nil || !strings.Contains(err.Error(), "legacy workers/") || !strings.Contains(err.Error(), "workflows/<name>/workflow.go") {
		t.Fatalf("expected precise migration error, got %v", err)
	}
}

func TestDiscoverWorkflowDefinitionsRejectsPhysicalQueue(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "workflows", "orders", "workflow.go"), `package orders

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

func Run() error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{TaskQueue: "orders__local"}, Run)
`)

	_, err := DiscoverWorkflowDefinitions(root)
	if err == nil || !strings.Contains(err.Error(), "must be logical") {
		t.Fatalf("expected physical queue rejection, got %v", err)
	}
}

func TestDiscoverWorkflowDefinitionsRejectsAmbiguousOwnedActivityAndMissingHandler(t *testing.T) {
	t.Run("owned activity has both entry files", func(t *testing.T) {
		root := t.TempDir()
		writeSourceTestFile(t, filepath.Join(root, "workflows", "orders", "workflow.go"), `package orders

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

func Run() error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{}, Run)
`)
		writeSourceTestFile(t, filepath.Join(root, "workflows", "orders", "activities", "charge", "activity.go"), `package charge

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

func Run() error { return nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{}, Run)
`)
		writeSourceTestFile(t, filepath.Join(root, "workflows", "orders", "activities", "charge", "workflow.go"), "package charge\n")
		_, err := DiscoverWorkflowDefinitions(root)
		if err == nil || !strings.Contains(err.Error(), "cannot contain both activity.go and workflow.go") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("workflow handler must exist", func(t *testing.T) {
		root := t.TempDir()
		writeSourceTestFile(t, filepath.Join(root, "workflows", "orders", "workflow.go"), `package orders

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{}, Missing)
`)
		_, err := DiscoverWorkflowDefinitions(root)
		if err == nil || !strings.Contains(err.Error(), "handler function Missing was not found") {
			t.Fatalf("error = %v", err)
		}
	})
}
