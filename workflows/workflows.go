// Package workflows provides the Go-native authoring surface for durable
// workflows and activities. GoBeyond discovers definitions in the authored
// workflows/ tree and generates the Temporal registration plumbing.
package workflows

import (
	"context"
	"fmt"
	"strings"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"
)

// WorkflowConfig is compiler-visible metadata for a workflow definition.
// TaskQueue is a logical queue name such as "orders". Authors never include
// an environment suffix; the generated worker resolves orders to
// orders__local (or the active hosted environment).
type WorkflowConfig struct {
	Name             string
	TaskQueue        string
	ExecutionTimeout time.Duration
}

// ActivityConfig is compiler-visible metadata for an activity definition.
// An empty TaskQueue inherits the nearest owning workflow queue. A top-level
// standalone activity with no TaskQueue uses the default queue.
type ActivityConfig struct {
	Name                   string
	TaskQueue              string
	StartToCloseTimeout    time.Duration
	ScheduleToCloseTimeout time.Duration
	HeartbeatTimeout       time.Duration
}

// WorkflowDefinition associates metadata with an ordinary Temporal workflow
// function. Authors normally expose one as `var Workflow = workflows.Define(...)`.
type WorkflowDefinition struct {
	Config  WorkflowConfig
	Handler any
}

// ActivityDefinition associates metadata with an ordinary Temporal activity
// function. Authors normally expose one as `var Activity = workflows.DefineActivity(...)`.
type ActivityDefinition struct {
	Config  ActivityConfig
	Handler any
}

// WorkflowReference is compiler-generated metadata for a workflow that may be
// invoked without importing its authored package. TaskQueue is always the
// definition's fully resolved logical queue, including inherited defaults.
type WorkflowReference struct {
	Name      string
	TaskQueue string
}

// ActivityReference is compiler-generated metadata for an activity that may
// be invoked without importing its authored package.
type ActivityReference struct {
	Name      string
	TaskQueue string
}

// Define declares a workflow while keeping its handler fully Go-native.
func Define(config WorkflowConfig, handler any) WorkflowDefinition {
	return WorkflowDefinition{Config: config, Handler: handler}
}

// DefineActivity declares a workflow-owned or standalone activity.
func DefineActivity(config ActivityConfig, handler any) ActivityDefinition {
	return ActivityDefinition{Config: config, Handler: handler}
}

// RegisterWorkflow registers a compiler-discovered workflow under its stable
// resolved name.
func RegisterWorkflow(registry worker.Registry, definition WorkflowDefinition, name string) {
	registry.RegisterWorkflowWithOptions(definition.Handler, temporalworkflow.RegisterOptions{Name: name})
}

// RegisterActivity registers a compiler-discovered activity under its stable
// resolved name.
func RegisterActivity(registry worker.Registry, definition ActivityDefinition, name string) {
	registry.RegisterActivityWithOptions(definition.Handler, activity.RegisterOptions{Name: name})
}

// WithActivity applies a definition's timeouts and, when explicitly authored,
// routes it to the corresponding physical queue in the current environment.
// Compiler-generated wrappers use this helper; application workflows may also
// use it when invoking an imported definition.
func WithActivity(ctx temporalworkflow.Context, definition ActivityDefinition) temporalworkflow.Context {
	options := temporalworkflow.GetActivityOptions(ctx)
	if definition.Config.StartToCloseTimeout > 0 {
		options.StartToCloseTimeout = definition.Config.StartToCloseTimeout
	}
	if definition.Config.ScheduleToCloseTimeout > 0 {
		options.ScheduleToCloseTimeout = definition.Config.ScheduleToCloseTimeout
	}
	if definition.Config.HeartbeatTimeout > 0 {
		options.HeartbeatTimeout = definition.Config.HeartbeatTimeout
	}
	if definition.Config.TaskQueue != "" {
		options.TaskQueue = physicalSiblingQueue(temporalworkflow.GetInfo(ctx).TaskQueueName, definition.Config.TaskQueue)
	}
	return temporalworkflow.WithActivityOptions(ctx, options)
}

// ExecuteActivity invokes the definition's handler after applying its
// compiler-visible options.
func ExecuteActivity(ctx temporalworkflow.Context, definition ActivityDefinition, args ...any) temporalworkflow.Future {
	return temporalworkflow.ExecuteActivity(WithActivity(ctx, definition), definition.Handler, args...)
}

// ExecuteActivityReference routes an activity call to its definition-owned
// queue. Generated references make cross-folder calls deterministic.
func ExecuteActivityReference(ctx temporalworkflow.Context, reference ActivityReference, args ...any) temporalworkflow.Future {
	options := temporalworkflow.GetActivityOptions(ctx)
	options.TaskQueue = physicalSiblingQueue(temporalworkflow.GetInfo(ctx).TaskQueueName, reference.TaskQueue)
	ctx = temporalworkflow.WithActivityOptions(ctx, options)
	return temporalworkflow.ExecuteActivity(ctx, reference.Name, args...)
}

// ExecuteSubworkflow invokes a compiler-generated child reference and always
// sets its resolved queue. Leaving the native child option empty would
// incorrectly inherit the parent queue for cross-queue definitions.
func ExecuteSubworkflow(ctx temporalworkflow.Context, reference WorkflowReference, args ...any) temporalworkflow.ChildWorkflowFuture {
	options := temporalworkflow.GetChildWorkflowOptions(ctx)
	options.TaskQueue = physicalSiblingQueue(temporalworkflow.GetInfo(ctx).TaskQueueName, reference.TaskQueue)
	ctx = temporalworkflow.WithChildOptions(ctx, options)
	return temporalworkflow.ExecuteChildWorkflow(ctx, reference.Name, args...)
}

// IsActivityContext reports whether ctx is a Temporal activity context. It is
// useful to build handlers that retain ordinary context.Context signatures.
func IsActivityContext(ctx context.Context) bool {
	return activity.IsActivity(ctx)
}

func physicalSiblingQueue(currentPhysical, logical string) string {
	logical = strings.TrimSpace(strings.ToLower(logical))
	if logical == "" {
		return currentPhysical
	}
	if index := strings.LastIndex(currentPhysical, gb.TaskQueueSeparator); index >= 0 {
		return logical + currentPhysical[index:]
	}
	return logical
}

// ValidateTaskQueue validates an authored logical queue without accepting a
// physical environment suffix.
func ValidateTaskQueue(queue string) (string, error) {
	if strings.Contains(queue, gb.TaskQueueSeparator) {
		return "", fmt.Errorf("task queue %q must be logical; omit the environment suffix", queue)
	}
	return gb.NormalizeTaskQueueID(queue)
}
