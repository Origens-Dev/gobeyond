package temporal

import (
	"context"
	"log"
	"sync"
	"time"

	service "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

// sorTaskHandoff fences SoR timeline stamps onto the process before workflow
// task acknowledgement (ORI-2 / ADR 010). Workflow interceptors enqueue; the
// gRPC interceptor POSTs host-report UDS before RespondWorkflowTaskCompleted
// so sibling schedule wakes still run before ScheduleActivityTask is visible.
//
// Unlike recovery registration, SoR is best-effort: a failed post logs and
// still allows acknowledgement so SoR never fails the workflow task.
type sorTaskHandoff struct {
	mu      sync.Mutex
	tokens  map[string]string
	pending map[string][]*ReportSorEventInput
	post    func(context.Context, ReportSorEventInput) error
}

func newSorTaskHandoff() *sorTaskHandoff {
	return &sorTaskHandoff{
		tokens:  map[string]string{},
		pending: map[string][]*ReportSorEventInput{},
		post:    postSorIngest,
	}
}

func (h *sorTaskHandoff) enqueue(in ReportSorEventInput) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, prior := range h.pending[in.RunID] {
		if prior.Type == in.Type && prior.DedupeKey == in.DedupeKey {
			return
		}
	}
	h.pending[in.RunID] = append(h.pending[in.RunID], &in)
}

func (h *sorTaskHandoff) remember(task *service.PollWorkflowTaskQueueResponse) {
	if h == nil || task == nil || len(task.TaskToken) == 0 || task.WorkflowExecution == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	run := task.WorkflowExecution.RunId
	for token, previous := range h.tokens {
		if previous == run {
			delete(h.tokens, token)
		}
	}
	h.tokens[string(task.TaskToken)] = run
}

func (h *sorTaskHandoff) flush(ctx context.Context, token []byte) {
	if h == nil {
		return
	}
	h.mu.Lock()
	run, known := h.tokens[string(token)]
	h.mu.Unlock()
	if !known {
		return
	}
	for {
		h.mu.Lock()
		batch := h.pending[run]
		if len(batch) == 0 {
			h.mu.Unlock()
			return
		}
		in := batch[0]
		h.mu.Unlock()

		input := *in
		input.Payload = make(map[string]string, len(in.Payload))
		for k, v := range in.Payload {
			input.Payload[k] = v
		}
		postCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := h.post(postCtx, input)
		cancel()
		if err != nil {
			log.Printf("temporal adapter: SoR handoff post failed (best-effort): type=%s dedupe=%s: %v",
				input.Type, input.DedupeKey, err)
		}

		h.mu.Lock()
		if batch := h.pending[run]; len(batch) > 0 && batch[0] == in {
			h.pending[run] = batch[1:]
			if len(h.pending[run]) == 0 {
				delete(h.pending, run)
			}
		}
		h.mu.Unlock()
	}
}

func (h *sorTaskHandoff) intercept(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if h == nil {
		return invoke(ctx, method, req, reply, cc, opts...)
	}
	if r, ok := req.(*service.RespondWorkflowTaskCompletedRequest); ok {
		h.flush(ctx, r.TaskToken)
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
