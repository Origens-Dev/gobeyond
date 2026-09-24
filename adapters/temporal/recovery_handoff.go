package temporal

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	service "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

// recoveryTaskHandoff fences workflow-task acknowledgement on durable recovery
// registration. Network retries run in the RPC goroutine, never workflow code.
// A crash before acknowledgement leaves the task available for replay; a crash
// afterwards is covered by the registrations persisted before acknowledgement.
type recoveryTaskHandoff struct {
	mu        sync.Mutex
	tokens    map[string]string
	pending   map[string][]*ReportSorEventInput
	uncovered *atomic.Int64
	post      func(context.Context, ReportSorEventInput) error
}

func newRecoveryTaskHandoff(uncovered *atomic.Int64) *recoveryTaskHandoff {
	return &recoveryTaskHandoff{tokens: map[string]string{}, pending: map[string][]*ReportSorEventInput{}, uncovered: uncovered, post: postRecoveryRegistration}
}
func (h *recoveryTaskHandoff) enqueue(in ReportSorEventInput) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending[in.RunID] = append(h.pending[in.RunID], &in)
	if h.uncovered != nil {
		h.uncovered.Add(1)
	}
}
func (h *recoveryTaskHandoff) remember(task *service.PollWorkflowTaskQueueResponse) {
	if task == nil || len(task.TaskToken) == 0 || task.WorkflowExecution == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	run := task.WorkflowExecution.RunId
	// A new delivery supersedes any expired token for this execution.
	for token, previous := range h.tokens {
		if previous == run {
			delete(h.tokens, token)
		}
	}
	h.tokens[string(task.TaskToken)] = run
}
func (h *recoveryTaskHandoff) flush(ctx context.Context, token []byte) error {
	h.mu.Lock()
	run, known := h.tokens[string(token)]
	h.mu.Unlock()
	if !known {
		return errors.New("recovery handoff: unknown workflow task")
	}
	for {
		h.mu.Lock()
		batch := h.pending[run]
		if len(batch) == 0 {
			h.mu.Unlock()
			return nil
		}
		in := batch[0]
		h.mu.Unlock()
		delay := 100 * time.Millisecond
		reported := false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Identity enrichment may add deployment metadata; do not mutate
			// the queued record when expired task RPCs overlap.
			input := *in
			input.Payload = make(map[string]string, len(in.Payload))
			for k, v := range in.Payload {
				input.Payload[k] = v
			}
			if err := h.post(ctx, input); err == nil {
				break
			}
			if !reported {
				log.Printf("temporal adapter: recovery registration retry; workflow task acknowledgement withheld")
				reported = true
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			if delay < time.Second {
				delay *= 2
			}
		}
		h.mu.Lock()
		// Task redelivery can overlap an expired RPC. Only the first
		// successful flush removes an entry; registration itself is idempotent.
		if batch := h.pending[run]; len(batch) > 0 && batch[0] == in {
			h.pending[run] = batch[1:]
			if len(h.pending[run]) == 0 {
				delete(h.pending, run)
			}
			if h.uncovered != nil {
				h.uncovered.Add(-1)
			}
		}
		h.mu.Unlock()
	}
}
func (h *recoveryTaskHandoff) intercept(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if r, ok := req.(*service.RespondWorkflowTaskCompletedRequest); ok {
		if err := h.flush(ctx, r.TaskToken); err != nil {
			return err
		}
	}
	err := invoke(ctx, method, req, reply, cc, opts...)
	if err != nil {
		return err
	}
	switch r := reply.(type) {
	case *service.PollWorkflowTaskQueueResponse:
		h.remember(r)
	case *service.RespondWorkflowTaskCompletedResponse:
		if request, ok := req.(*service.RespondWorkflowTaskCompletedRequest); ok {
			h.mu.Lock()
			delete(h.tokens, string(request.TaskToken))
			h.mu.Unlock()
		}
		h.remember(r.WorkflowTask)
	}
	return nil
}
