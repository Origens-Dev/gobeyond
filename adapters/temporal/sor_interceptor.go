package temporal

import (
	"context"
	"fmt"
	"strconv"
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

func (s *sorWorkflowInbound) Init(outbound interceptor.WorkflowOutboundInterceptor) error {
	o := &sorWorkflowOutbound{}
	o.Next = outbound
	return s.Next.Init(o)
}

func (s *sorWorkflowInbound) ExecuteWorkflow(
	ctx workflow.Context,
	in *interceptor.ExecuteWorkflowInput,
) (any, error) {
	// The start gateway writes the WF# row immediately after Temporal accepts
	// the start, but that write is deliberately best-effort. Report the same
	// identity from the worker as a fallback before user code runs so a cold
	// slot, direct Temporal starter, or transient gateway failure cannot leave
	// the app execution without a product-level run header.
	reportWorkflowStarted(ctx)

	// The wrapped workflow must finish before the terminal event can be
	// classified, but scheduling a local activity after ExecuteWorkflow returns
	// is too late: the SDK is already serializing the completion of the current
	// workflow task. Run the wrapped workflow in a deterministic coroutine and
	// keep this interceptor's coroutine alive while the terminal report is
	// committed.
	done := workflow.NewChannel(ctx)
	var (
		ret any
		err error
	)
	workflow.Go(ctx, func(ctx workflow.Context) {
		ret, err = s.Next.ExecuteWorkflow(ctx, in)
		done.Send(ctx, struct{}{})
	})
	done.Receive(ctx, nil)
	reportWorkflowTerminal(ctx, err)
	return ret, err
}

func reportWorkflowStarted(ctx workflow.Context) {
	info := workflow.GetInfo(ctx)
	if info == nil || strings.TrimSpace(info.WorkflowExecution.RunID) == "" {
		return
	}
	payload := map[string]string{
		"workflow_name":  info.WorkflowType.Name,
		"execution_kind": "workflow",
		"task_queue":     info.TaskQueueName,
		"namespace":      info.Namespace,
		"status":         "RUNNING",
		"visibility":     "root",
	}
	if parent := info.ParentWorkflowExecution; parent != nil {
		payload["visibility"] = "internal"
		payload["parent_workflow_id"] = parent.ID
		payload["parent_run_id"] = parent.RunID
		if info.ParentWorkflowNamespace != "" {
			payload["parent_namespace"] = info.ParentWorkflowNamespace
		}
	}
	in := ReportSorEventInput{
		WorkflowID: info.WorkflowExecution.ID,
		RunID:      info.WorkflowExecution.RunID,
		DedupeKey:  "wf-started-" + info.WorkflowExecution.RunID,
		Type:       "workflow.started",
		Kind:       "run",
		Status:     "RUNNING",
		Payload:    payload,
	}
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	if err := workflow.ExecuteLocalActivity(laCtx, ReportSorEventName, in).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Error("GoBeyond SoR workflow-start report failed", "error", err)
	}
}

// sorWorkflowOutbound emits SoR timer / child / retry-policy header stamps
// (ADR 010 Dynamo-first wake).
type sorWorkflowOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	timerSeq int
	childSeq int
}

func (s *sorWorkflowOutbound) NewTimer(ctx workflow.Context, d time.Duration) workflow.Future {
	return s.NewTimerWithOptions(ctx, d, workflow.TimerOptions{})
}

func (s *sorWorkflowOutbound) NewTimerWithOptions(
	ctx workflow.Context,
	d time.Duration,
	options workflow.TimerOptions,
) workflow.Future {
	if d <= 0 {
		return s.Next.NewTimerWithOptions(ctx, d, options)
	}
	s.timerSeq++
	seq := s.timerSeq
	reportTimerEvent(ctx, seq, "timer.started", d, options.Summary)
	fut := s.Next.NewTimerWithOptions(ctx, d, options)
	// Await fire/cancel without requiring the caller to Get (Select-safe).
	workflow.Go(ctx, func(ctx workflow.Context) {
		err := fut.Get(ctx, nil)
		if temporal.IsCanceledError(err) {
			reportTimerEvent(ctx, seq, "timer.canceled", d, options.Summary)
			return
		}
		reportTimerEvent(ctx, seq, "timer.fired", d, options.Summary)
	})
	return fut
}

func (s *sorWorkflowOutbound) ExecuteActivity(
	ctx workflow.Context,
	activityType string,
	args ...interface{},
) workflow.Future {
	injectRetryPolicyHeader(ctx)
	return s.Next.ExecuteActivity(ctx, activityType, args...)
}

func (s *sorWorkflowOutbound) ExecuteChildWorkflow(
	ctx workflow.Context,
	childWorkflowType string,
	args ...interface{},
) workflow.ChildWorkflowFuture {
	fut := s.Next.ExecuteChildWorkflow(ctx, childWorkflowType, args...)
	s.childSeq++
	seq := s.childSeq
	parent := workflow.GetInfo(ctx)
	workflow.Go(ctx, func(ctx workflow.Context) {
		var childExec workflow.Execution
		if err := fut.GetChildWorkflowExecution().Get(ctx, &childExec); err != nil {
			return
		}
		reportChildStarted(ctx, seq, parent, childExec)
	})
	return fut
}

func reportTimerEvent(ctx workflow.Context, seq int, eventType string, d time.Duration, summary string) {
	info := workflow.GetInfo(ctx)
	fireAt := workflow.Now(ctx).UTC().Add(d)
	payload := map[string]string{
		"duration_ms": strconv.FormatInt(d.Milliseconds(), 10),
		"timer_seq":   strconv.Itoa(seq),
		"fire_at":     fireAt.Format(time.RFC3339),
	}
	if s := strings.TrimSpace(summary); s != "" {
		payload["summary"] = s
	}
	stampParentHint(payload, info)
	in := ReportSorEventInput{
		WorkflowID: info.WorkflowExecution.ID,
		RunID:      info.WorkflowExecution.RunID,
		DedupeKey:  fmt.Sprintf("timer-%s-%d-%s", info.WorkflowExecution.RunID, seq, eventType),
		Type:       eventType,
		Kind:       "event",
		Payload:    payload,
	}
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	if err := workflow.ExecuteLocalActivity(laCtx, ReportSorEventName, in).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Error("GoBeyond SoR timer report failed", "error", err)
	}
}

func reportChildStarted(ctx workflow.Context, seq int, parent *workflow.Info, child workflow.Execution) {
	if parent == nil || strings.TrimSpace(child.ID) == "" {
		return
	}
	payload := map[string]string{
		"child_seq":            strconv.Itoa(seq),
		"child_workflow_id":    child.ID,
		"child_run_id":         child.RunID,
		"parent_workflow_id":   parent.WorkflowExecution.ID,
		"parent_run_id":        parent.WorkflowExecution.RunID,
		"parent_namespace":     parent.Namespace,
		"parent_task_queue":    parent.TaskQueueName,
		"parent_workflow_type": parent.WorkflowType.Name,
	}
	stampParentHint(payload, parent)
	in := ReportSorEventInput{
		WorkflowID: parent.WorkflowExecution.ID,
		RunID:      parent.WorkflowExecution.RunID,
		DedupeKey:  fmt.Sprintf("child-started-%s-%d-%s", parent.WorkflowExecution.RunID, seq, child.RunID),
		Type:       "child.started",
		Kind:       "event",
		Payload:    payload,
	}
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	if err := workflow.ExecuteLocalActivity(laCtx, ReportSorEventName, in).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Error("GoBeyond SoR child report failed", "error", err)
	}
}

func stampParentHint(payload map[string]string, info *workflow.Info) {
	if info == nil || payload == nil {
		return
	}
	if strings.TrimSpace(payload["task_queue"]) == "" && info.TaskQueueName != "" {
		payload["task_queue"] = info.TaskQueueName
	}
	if strings.TrimSpace(payload["namespace"]) == "" && info.Namespace != "" {
		payload["namespace"] = info.Namespace
	}
}

func reportWorkflowTerminal(ctx workflow.Context, runErr error) {
	// ContinueAsNew is not a true terminal for parent-wake (ADR 010).
	if workflow.IsContinueAsNewError(runErr) {
		return
	}
	info := workflow.GetInfo(ctx)
	eventType := "workflow.completed"
	status := "COMPLETED"
	payload := map[string]string{}
	if runErr != nil {
		switch {
		case temporal.IsCanceledError(runErr):
			eventType = "workflow.canceled"
			status = "CANCELED"
		case temporal.IsTimeoutError(runErr):
			eventType = "workflow.timed_out"
			status = "FAILED"
			payload["error"] = "timed_out"
		default:
			eventType = "workflow.failed"
			status = "FAILED"
			payload["error"] = truncateErr(runErr)
		}
	}
	stampParentHint(payload, info)
	if parent := info.ParentWorkflowExecution; parent != nil {
		payload["parent_workflow_id"] = parent.ID
		payload["parent_run_id"] = parent.RunID
		if info.ParentWorkflowNamespace != "" {
			payload["parent_namespace"] = info.ParentWorkflowNamespace
		}
	}
	in := ReportSorEventInput{
		WorkflowID: info.WorkflowExecution.ID,
		RunID:      info.WorkflowExecution.RunID,
		DedupeKey:  fmt.Sprintf("wf-terminal-%s-%s", info.WorkflowExecution.RunID, eventType),
		Type:       eventType,
		Kind:       "event",
		Status:     status,
		Payload:    payload,
	}
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	if err := workflow.ExecuteLocalActivity(laCtx, ReportSorEventName, in).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Error("GoBeyond SoR workflow-terminal report failed", "error", err)
	}
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
	if reportErr := postSorIngest(ctx, ReportSorEventInput{
		WorkflowID:   info.WorkflowExecution.ID,
		RunID:        info.WorkflowExecution.RunID,
		DedupeKey:    fmt.Sprintf("act-%s-%d-started", info.ActivityID, info.Attempt),
		Type:         "activity.started",
		Kind:         "activity",
		ActivityID:   info.ActivityID,
		ActivityType: info.ActivityType.Name,
		Status:       "RUNNING",
		Attempt:      info.Attempt,
		Payload: map[string]string{
			"task_queue": info.TaskQueue,
			"attempt":    strconv.FormatInt(int64(info.Attempt), 10),
		},
	}); reportErr != nil {
		activity.GetLogger(ctx).Error("GoBeyond SoR activity-start report failed", "error", reportErr)
	}
	ret, err := s.Next.ExecuteActivity(ctx, in)
	status := "COMPLETED"
	eventType := "activity.completed"
	payload := map[string]string{
		"task_queue": info.TaskQueue,
		"attempt":    strconv.FormatInt(int64(info.Attempt), 10),
	}
	if err != nil {
		if temporal.IsCanceledError(err) {
			status = "CANCELED"
			eventType = "activity.canceled"
		} else {
			status = "FAILED"
			eventType = "activity.failed"
			payload["error"] = truncateErr(err)
			if isNonRetryableActivityErr(err) {
				payload["non_retryable"] = "true"
			} else {
				payload["non_retryable"] = "false"
			}
			if ms := nextRetryDelayMS(err); ms > 0 {
				payload["next_retry_delay_ms"] = strconv.FormatInt(ms, 10)
			}
			if stamp, ok := readRetryPolicyFromActivityHeader(interceptor.Header(ctx)); ok {
				applyRetryStampPayload(payload, stamp)
			} else {
				applyRetryStampPayload(payload, stampFromRetryPolicy(nil))
			}
		}
	}
	if reportErr := postSorIngest(ctx, ReportSorEventInput{
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
	}); reportErr != nil {
		activity.GetLogger(ctx).Error("GoBeyond SoR activity-terminal report failed", "error", reportErr)
	}
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
