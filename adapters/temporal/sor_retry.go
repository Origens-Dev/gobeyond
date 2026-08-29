package temporal

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	// retryPolicyHeaderKey carries the scheduled RetryPolicy from workflow
	// outbound ExecuteActivity into the activity inbound interceptor so
	// activity.failed can stamp Dynamo-first wake fields (ADR 010).
	retryPolicyHeaderKey = "gobeyond-retry-policy"

	// workflowTaskQueueHeaderKey carries the parent workflow's physical task
	// queue so a cross-queue activity can wake that primary on terminal
	// (activity.completed / failed / canceled) — sibling schedule is one-way
	// at schedule time; this closes the return path (ADR 010).
	workflowTaskQueueHeaderKey = "gobeyond-workflow-task-queue"

	// Temporal server defaults when ActivityOptions.RetryPolicy is nil.
	defaultInitialInterval    = time.Second
	defaultBackoffCoefficient = 2.0
	defaultMaximumInterval    = 100 * time.Second
	defaultMaximumAttempts    = int32(0) // unlimited
)

// retryPolicyStamp is the JSON / SoR payload shape for activity retry wake.
type retryPolicyStamp struct {
	InitialIntervalMS    int64   `json:"initial_interval_ms"`
	BackoffCoefficient   float64 `json:"backoff_coefficient"`
	MaximumIntervalMS    int64   `json:"maximum_interval_ms"`
	MaximumAttempts      int32   `json:"maximum_attempts"`
	NonRetryableErrTypes string  `json:"non_retryable_error_types,omitempty"`
}

func stampFromRetryPolicy(rp *temporal.RetryPolicy) retryPolicyStamp {
	if rp == nil {
		return retryPolicyStamp{
			InitialIntervalMS:  defaultInitialInterval.Milliseconds(),
			BackoffCoefficient: defaultBackoffCoefficient,
			MaximumIntervalMS:  defaultMaximumInterval.Milliseconds(),
			MaximumAttempts:    defaultMaximumAttempts,
		}
	}
	initial := rp.InitialInterval
	if initial <= 0 {
		initial = defaultInitialInterval
	}
	coeff := rp.BackoffCoefficient
	if coeff <= 0 {
		coeff = defaultBackoffCoefficient
	}
	maxInterval := rp.MaximumInterval
	if maxInterval <= 0 {
		maxInterval = initial * 100
		if maxInterval <= 0 {
			maxInterval = defaultMaximumInterval
		}
	}
	types := ""
	if len(rp.NonRetryableErrorTypes) > 0 {
		types = strings.Join(rp.NonRetryableErrorTypes, ",")
	}
	return retryPolicyStamp{
		InitialIntervalMS:    initial.Milliseconds(),
		BackoffCoefficient:   coeff,
		MaximumIntervalMS:    maxInterval.Milliseconds(),
		MaximumAttempts:      rp.MaximumAttempts,
		NonRetryableErrTypes: types,
	}
}

func injectRetryPolicyHeader(ctx workflow.Context) {
	opts := workflow.GetActivityOptions(ctx)
	stamp := stampFromRetryPolicy(opts.RetryPolicy)
	raw, err := json.Marshal(stamp)
	if err != nil {
		return
	}
	h := interceptor.WorkflowHeader(ctx)
	if h == nil {
		return
	}
	h[retryPolicyHeaderKey] = &commonpb.Payload{
		Metadata: map[string][]byte{
			converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON),
		},
		Data: raw,
	}
}

func injectWorkflowTaskQueueHeader(ctx workflow.Context) {
	info := workflow.GetInfo(ctx)
	if info == nil {
		return
	}
	tq := strings.TrimSpace(info.TaskQueueName)
	if tq == "" {
		return
	}
	raw, err := json.Marshal(tq)
	if err != nil {
		return
	}
	h := interceptor.WorkflowHeader(ctx)
	if h == nil {
		return
	}
	h[workflowTaskQueueHeaderKey] = &commonpb.Payload{
		Metadata: map[string][]byte{
			converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON),
		},
		Data: raw,
	}
}

func readWorkflowTaskQueueFromActivityHeader(header map[string]*commonpb.Payload) string {
	if header == nil {
		return ""
	}
	p, ok := header[workflowTaskQueueHeaderKey]
	if !ok || p == nil || len(p.Data) == 0 {
		return ""
	}
	var tq string
	if err := json.Unmarshal(p.Data, &tq); err != nil {
		// Accept raw UTF-8 as a fallback for older stamps.
		return strings.TrimSpace(string(p.Data))
	}
	return strings.TrimSpace(tq)
}

func readRetryPolicyFromActivityHeader(header map[string]*commonpb.Payload) (retryPolicyStamp, bool) {
	if header == nil {
		return retryPolicyStamp{}, false
	}
	p, ok := header[retryPolicyHeaderKey]
	if !ok || p == nil || len(p.Data) == 0 {
		return retryPolicyStamp{}, false
	}
	var stamp retryPolicyStamp
	if err := json.Unmarshal(p.Data, &stamp); err != nil {
		return retryPolicyStamp{}, false
	}
	if stamp.InitialIntervalMS <= 0 {
		stamp.InitialIntervalMS = defaultInitialInterval.Milliseconds()
	}
	if stamp.BackoffCoefficient <= 0 {
		stamp.BackoffCoefficient = defaultBackoffCoefficient
	}
	if stamp.MaximumIntervalMS <= 0 {
		stamp.MaximumIntervalMS = defaultMaximumInterval.Milliseconds()
	}
	return stamp, true
}

func applyRetryStampPayload(payload map[string]string, stamp retryPolicyStamp) {
	payload["initial_interval_ms"] = strconv.FormatInt(stamp.InitialIntervalMS, 10)
	payload["backoff_coefficient"] = strconv.FormatFloat(stamp.BackoffCoefficient, 'f', -1, 64)
	payload["maximum_interval_ms"] = strconv.FormatInt(stamp.MaximumIntervalMS, 10)
	payload["maximum_attempts"] = strconv.FormatInt(int64(stamp.MaximumAttempts), 10)
	if stamp.NonRetryableErrTypes != "" {
		payload["non_retryable_error_types"] = stamp.NonRetryableErrTypes
	}
}

func isNonRetryableActivityErr(err error) bool {
	var app *temporal.ApplicationError
	if errors.As(err, &app) && app != nil {
		return app.NonRetryable()
	}
	return false
}

func nextRetryDelayMS(err error) int64 {
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app == nil {
		return 0
	}
	d := app.NextRetryDelay()
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}
