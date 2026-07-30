package temporal

import (
	"context"

	"go.temporal.io/sdk/interceptor"
)

type healthInterceptor struct {
	interceptor.WorkerInterceptorBase
	tracker *healthTracker
}

func (h *healthInterceptor) InterceptActivity(
	ctx context.Context,
	next interceptor.ActivityInboundInterceptor,
) interceptor.ActivityInboundInterceptor {
	i := &healthActivityInbound{tracker: h.tracker}
	i.Next = next
	return i
}

type healthActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
	tracker *healthTracker
}

func (h *healthActivityInbound) ExecuteActivity(
	ctx context.Context,
	in *interceptor.ExecuteActivityInput,
) (any, error) {
	h.tracker.begin()
	defer h.tracker.end()
	return h.Next.ExecuteActivity(ctx, in)
}
