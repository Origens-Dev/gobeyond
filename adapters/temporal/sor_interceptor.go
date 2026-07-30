package temporal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// sorWorkerInterceptor installs workflow + activity inbound interceptors that
// emit SoR timeline / activity rows via ReportSorEvent (ADR 010).
type sorWorkerInterceptor struct {
	interceptor.WorkerInterceptorBase
}

func (s *sorWorkerInterceptor) InterceptWorkflow(
	_ workflow.Context,
	next interceptor.WorkflowInboundInterceptor,
) interceptor.WorkflowInboundInterceptor {
	i := &sorWorkflowInbound{}
	i.Next = next
	return i
}

func (s *sorWorkerInterceptor) InterceptActivity(
	ctx context.Context,
	next interceptor.ActivityInboundInterceptor,
) interceptor.ActivityInboundInterceptor {
	i := &sorActivityInbound{}
	i.Next = next
	return i
}

type sorWorkflowInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (s *sorWorkflowInbound) ExecuteWorkflow(
	ctx workflow.Context,
	in *interceptor.ExecuteWorkflowInput,
) (any, error) {
	ret, err := s.Next.ExecuteWorkflow(ctx, in)
	reportWorkflowTerminal(ctx, err)
	return ret, err
}

func reportWorkflowTerminal(ctx workflow.Context, runErr error) {
	info := workflow.GetInfo(ctx)
	eventType := "workflow.completed"
	payload := map[string]string{}
	if runErr != nil {
		switch {
		case temporal.IsCanceledError(runErr):
			eventType = "workflow.canceled"
		case temporal.IsTimeoutError(runErr):
			eventType = "workflow.timed_out"
			payload["error"] = "timed_out"
		default:
			eventType = "workflow.failed"
			payload["error"] = truncateErr(runErr)
		}
	}
	in := ReportSorEventInput{
		WorkflowID: info.WorkflowExecution.ID,
		RunID:      info.WorkflowExecution.RunID,
		DedupeKey:  fmt.Sprintf("wf-terminal-%s-%s", info.WorkflowExecution.RunID, eventType),
		Type:       eventType,
		Kind:       "event",
		Payload:    payload,
	}
	// Local activity only — never network from the workflow sandbox.
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	// Swallow failures so SoR never fails the workflow task (ADR 010).
	_ = workflow.ExecuteLocalActivity(laCtx, ReportSorEventName, in).Get(ctx, nil)
}

type sorActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
}

func (s *sorActivityInbound) ExecuteActivity(
	ctx context.Context,
	in *interceptor.ExecuteActivityInput,
) (any, error) {
	info := activity.GetInfo(ctx)
	if info.ActivityType.Name == ReportSorEventName {
		return s.Next.ExecuteActivity(ctx, in)
	}
	_ = postSorIngest(ctx, ReportSorEventInput{
		WorkflowID:   info.WorkflowExecution.ID,
		RunID:        info.WorkflowExecution.RunID,
		DedupeKey:    fmt.Sprintf("act-%s-%d-started", info.ActivityID, info.Attempt),
		Type:         "activity.started",
		Kind:         "activity",
		ActivityID:   info.ActivityID,
		ActivityType: info.ActivityType.Name,
		Status:       "RUNNING",
		Attempt:      info.Attempt,
	})
	ret, err := s.Next.ExecuteActivity(ctx, in)
	status := "COMPLETED"
	eventType := "activity.completed"
	payload := map[string]string{}
	if err != nil {
		if temporal.IsCanceledError(err) {
			status = "CANCELED"
			eventType = "activity.canceled"
		} else {
			status = "FAILED"
			eventType = "activity.failed"
			payload["error"] = truncateErr(err)
		}
	}
	_ = postSorIngest(ctx, ReportSorEventInput{
		WorkflowID:   info.WorkflowExecution.ID,
		RunID:        info.WorkflowExecution.RunID,
		DedupeKey:    fmt.Sprintf("act-%s-%d-%s", info.ActivityID, info.Attempt, strings.ToLower(status)),
		Type:         eventType,
		Kind:         "activity",
		ActivityID:   info.ActivityID,
		ActivityType: info.ActivityType.Name,
		Status:       status,
		Attempt:      info.Attempt,
		Payload:      payload,
	})
	return ret, err
}

func truncateErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 256 {
		return msg[:256]
	}
	return msg
}

func registerSorActivities(w worker.Worker) {
	w.RegisterActivityWithOptions(ReportSorEvent, activity.RegisterOptions{
		Name: ReportSorEventName,
	})
}
