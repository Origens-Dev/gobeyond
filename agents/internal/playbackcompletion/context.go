// Package playbackcompletion holds the framework-private completion marker.
// Only framework execution code may install it, after authenticated durable
// receipt resolution. Neither a tool argument nor caller metadata is authority.
package playbackcompletion

import (
	"context"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

type contextKey struct{}
type marker struct {
	receipt voicecontract.PlaybackCompletionReceipt
}

// With is internal to the framework. It does not authenticate its receipt;
// callers must resolve the live durable authorization before using it.
func With(ctx context.Context, receipt voicecontract.PlaybackCompletionReceipt) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, contextKey{}, marker{receipt: receipt})
}

// From returns only an unexpired marker from an uncanceled invocation.
func From(ctx context.Context) (voicecontract.PlaybackCompletionReceipt, bool) {
	if ctx == nil || ctx.Err() != nil {
		return voicecontract.PlaybackCompletionReceipt{}, false
	}
	value, ok := ctx.Value(contextKey{}).(marker)
	if !ok || value.receipt.Validate(time.Now()) != nil {
		return voicecontract.PlaybackCompletionReceipt{}, false
	}
	return value.receipt, true
}
