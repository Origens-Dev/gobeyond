package agents

import (
	"context"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"time"
)

type playbackCompletionContextKey struct{}
type trustedPlaybackCompletion struct {
	receipt voicecontract.PlaybackCompletionReceipt
}

// PlaybackCompletionFromContext returns only a current runtime-owned receipt.
// Actor/session metadata, decoded payloads and tool options cannot populate it.
// No runtime currently installs this marker: durable authentication and hidden
// mutation dispatch remain separate integration gates.
func PlaybackCompletionFromContext(ctx context.Context) (voicecontract.PlaybackCompletionReceipt, bool) {
	if ctx == nil || ctx.Err() != nil {
		return voicecontract.PlaybackCompletionReceipt{}, false
	}
	value, ok := ctx.Value(playbackCompletionContextKey{}).(trustedPlaybackCompletion)
	if !ok || value.receipt.Validate(time.Now()) != nil {
		return voicecontract.PlaybackCompletionReceipt{}, false
	}
	return value.receipt, true
}

// Only code inside the framework can install the private marker. It must first
// verify durable completion, exact frozen declaration and live ownership. This
// helper alone provides no such authentication and is intentionally unused.
func withPlaybackCompletion(ctx context.Context, receipt voicecontract.PlaybackCompletionReceipt) context.Context {
	return context.WithValue(ctx, playbackCompletionContextKey{}, trustedPlaybackCompletion{receipt: receipt})
}
