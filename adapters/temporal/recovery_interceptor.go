package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const recoveryRegisterName = "GoBeyondRecoveryRegisterV1"
const recoveryEnabledEnv = "GOBEYOND_DURABLE_RECOVERY_ENABLED"

type recoveryWorkerInterceptor struct {
	interceptor.WorkerInterceptorBase
	enabled   bool
	uncovered *atomic.Int64
}

func (r *recoveryWorkerInterceptor) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	in := &recoveryWorkflowInbound{configured: r.enabled, state: &recoveryWorkflowState{}, uncovered: r.uncovered}
	in.Next = next
	return in
}

type recoveryRegistrationContextKey struct{}

type recoveryWorkflowState struct {
	enabled    bool
	timerSeq   int
	commandSeq int
}
type recoveryWorkflowInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	configured bool
	state      *recoveryWorkflowState
	uncovered  *atomic.Int64
}

func (r *recoveryWorkflowInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	out := &recoveryWorkflowOutbound{state: r.state, uncovered: r.uncovered}
	out.Next = next
	return r.Next.Init(out)
}
func (r *recoveryWorkflowInbound) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (any, error) {
	if r.uncovered != nil {
		r.uncovered.Add(1)
	}
	registered := false
	defer func() {
		if r.uncovered != nil && !registered {
			r.uncovered.Add(-1)
		}
	}()
	// GetVersion preserves pre-rollout histories. The enrollment decision is
	// recorded in history: restarting on a differently configured host cannot
	// change command order during replay.
	if workflow.GetVersion(ctx, "gobeyond-durable-recovery-v1", workflow.DefaultVersion, 1) == 1 {
		if err := workflow.SideEffect(ctx, func(workflow.Context) any { return r.configured }).Get(&r.state.enabled); err != nil {
			return nil, err
		}
	}
	if r.state.enabled {
		if err := registerWorkflowRecovery(ctx, "run", "", time.Time{}); err != nil {
			return nil, err
		}
	}
	if r.state.enabled && r.uncovered != nil {
		r.uncovered.Add(-1)
		registered = true
	}
	return r.Next.ExecuteWorkflow(ctx, in)
}

type recoveryWorkflowOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	state     *recoveryWorkflowState
	uncovered *atomic.Int64
}

func (r *recoveryWorkflowOutbound) NewTimer(ctx workflow.Context, d time.Duration) workflow.Future {
	return r.NewTimerWithOptions(ctx, d, workflow.TimerOptions{})
}
func (r *recoveryWorkflowOutbound) NewTimerWithOptions(ctx workflow.Context, d time.Duration, options workflow.TimerOptions) workflow.Future {
	if !r.state.enabled || d <= 0 || ctx.Value(recoveryRegistrationContextKey{}) == true {
		return r.Next.NewTimerWithOptions(ctx, d, options)
	}
	r.state.timerSeq++
	key := strconv.Itoa(r.state.timerSeq)
	if err := r.register(ctx, "timer.started", key, workflow.Now(ctx).Add(d)); err != nil {
		f, set := workflow.NewFuture(ctx)
		set.Set(nil, err)
		return f
	}
	future := r.Next.NewTimerWithOptions(ctx, d, options)
	workflow.Go(ctx, func(ctx workflow.Context) {
		_ = future.Get(ctx, nil)
		// Cancelling a branch must not leave its old deadline waking a still-open
		// workflow forever. A disconnected context can remove that timer hint;
		// closing the whole workflow remains covered by the execution guard.
		completionCtx, _ := workflow.NewDisconnectedContext(ctx)
		_ = r.register(completionCtx, "timer.finished", key, time.Time{})
	})
	return future
}
func (r *recoveryWorkflowOutbound) ExecuteActivity(ctx workflow.Context, name string, args ...interface{}) workflow.Future {
	if r.state.enabled {
		r.state.commandSeq++
		options := workflow.GetActivityOptions(ctx)
		queue := options.TaskQueue
		if queue == "" {
			queue = workflow.GetInfo(ctx).TaskQueueName
		}
		if err := r.register(ctx, "checkpoint", strconv.Itoa(r.state.commandSeq), time.Time{}, map[string]string{"activity_queue": queue}); err != nil {
			future, set := workflow.NewFuture(ctx)
			set.Set(nil, err)
			return future
		}
	}
	return r.Next.ExecuteActivity(ctx, name, args...)
}

type recoveryFailedChild struct{ workflow.Future }

func (f recoveryFailedChild) GetChildWorkflowExecution() workflow.Future { return f.Future }
func (f recoveryFailedChild) SignalChildWorkflow(workflow.Context, string, interface{}) workflow.Future {
	return f.Future
}
func (r *recoveryWorkflowOutbound) ExecuteChildWorkflow(ctx workflow.Context, name string, args ...interface{}) workflow.ChildWorkflowFuture {
	if r.state.enabled {
		r.state.commandSeq++
		if err := r.register(ctx, "checkpoint", strconv.Itoa(r.state.commandSeq), time.Time{}); err != nil {
			future, set := workflow.NewFuture(ctx)
			set.Set(nil, err)
			return recoveryFailedChild{future}
		}
	}
	return r.Next.ExecuteChildWorkflow(ctx, name, args...)
}

func registerWorkflowRecovery(ctx workflow.Context, kind, key string, deadline time.Time, metadata ...map[string]string) error {
	info := workflow.GetInfo(ctx)
	in := ReportSorEventInput{WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID, Kind: "recovery", Type: kind, DedupeKey: kind + ":" + key, Payload: map[string]string{"operation_key": key, "first_run_id": info.FirstRunID}}
	for _, fields := range metadata {
		for key, value := range fields {
			in.Payload[key] = value
		}
	}
	if !deadline.IsZero() {
		in.Payload["deadline"] = deadline.UTC().Format(time.RFC3339Nano)
	}
	return executeRecoveryRegistration(ctx, in)
}

func executeRecoveryRegistration(ctx workflow.Context, in ReportSorEventInput) error {
	info := workflow.GetInfo(ctx)
	// SDK local-activity retry backoff uses workflow.Sleep. Bypass timer
	// registration for that infrastructure retry to avoid recursive registration.
	ctx = workflow.WithValue(ctx, recoveryRegistrationContextKey{}, true)
	// Local activities default their total retry budget to StartToCloseTimeout.
	// Keep registration alive for the workflow lifetime instead; there is no
	// application-visible infrastructure failure after five seconds. When a
	// workflow has no execution timeout, cancellation/server closure bounds
	// this maximum representable duration. No retry loop runs in user code.
	lifetime := info.WorkflowExecutionTimeout
	if lifetime <= 0 {
		lifetime = time.Duration(1<<63 - 1)
	}
	la := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{ScheduleToCloseTimeout: lifetime, StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second}})
	return workflow.ExecuteLocalActivity(la, recoveryRegisterName, in).Get(ctx, nil)
}
func registerRecoveryActivity(w worker.Worker) {
	w.RegisterActivityWithOptions(postRecoveryRegistration, activity.RegisterOptions{Name: recoveryRegisterName})
}

// Required registration never uses the best-effort SoR fallback. Missing
// identity/transport is an error, not success, and only the authenticated host
// forwarder may supply tenant authority.
func postRecoveryRegistration(ctx context.Context, in ReportSorEventInput) error {
	in = fillSorIdentity(in)
	if in.EnvironmentID == "" || in.WorkerID == "" || in.WorkflowID == "" || in.RunID == "" {
		return errors.New("recovery identity unavailable")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	socket := strings.TrimSpace(os.Getenv(envHostReportSocket))
	if socket == "" {
		socket = defaultReportSocket
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 4 * time.Second, Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://host/v1/sor-ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("recovery registration transport unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("recovery registration status %d", response.StatusCode)
	}
	return nil
}

func (r *recoveryWorkflowOutbound) register(ctx workflow.Context, kind, key string, deadline time.Time, metadata ...map[string]string) error {
	if r.uncovered != nil {
		r.uncovered.Add(1)
		defer r.uncovered.Add(-1)
	}
	return registerWorkflowRecovery(ctx, kind, key, deadline, metadata...)
}

func (r *recoveryWorkflowOutbound) protectExternal(ctx workflow.Context, target, runID string, send func() workflow.Future) workflow.Future {
	if !r.state.enabled {
		return send()
	}
	r.state.commandSeq++
	info := workflow.GetInfo(ctx)
	in := ReportSorEventInput{WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID, Kind: "recovery", Type: "target.pending", Payload: map[string]string{"operation_key": strconv.Itoa(r.state.commandSeq), "target_workflow_id": target, "target_run_id": runID, "target_namespace": workflow.GetChildWorkflowOptions(ctx).Namespace}}
	register := func(regCtx workflow.Context, kind string) error {
		if r.uncovered != nil {
			r.uncovered.Add(1)
			defer r.uncovered.Add(-1)
		}
		input := in
		input.Type = kind
		return executeRecoveryRegistration(regCtx, input)
	}
	out, set := workflow.NewFuture(ctx)
	if err := register(ctx, "target.pending"); err != nil {
		set.Set(nil, err)
		return out
	}
	sent := send()
	workflow.Go(ctx, func(ctx workflow.Context) {
		if err := sent.Get(ctx, nil); err != nil {
			set.Set(nil, err)
			return
		}
		set.Set(nil, register(ctx, "target.accepted"))
	})
	return out
}
func (r *recoveryWorkflowOutbound) SignalExternalWorkflow(ctx workflow.Context, wfid, runID, name string, arg interface{}) workflow.Future {
	return r.protectExternal(ctx, wfid, runID, func() workflow.Future { return r.Next.SignalExternalWorkflow(ctx, wfid, runID, name, arg) })
}
func (r *recoveryWorkflowOutbound) SignalChildWorkflow(ctx workflow.Context, wfid, name string, arg interface{}) workflow.Future {
	return r.protectExternal(ctx, wfid, "", func() workflow.Future { return r.Next.SignalChildWorkflow(ctx, wfid, name, arg) })
}
func (r *recoveryWorkflowOutbound) RequestCancelExternalWorkflow(ctx workflow.Context, wfid, runID string) workflow.Future {
	return r.protectExternal(ctx, wfid, runID, func() workflow.Future { return r.Next.RequestCancelExternalWorkflow(ctx, wfid, runID) })
}
