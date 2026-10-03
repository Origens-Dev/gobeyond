package agents

import (
	"context"

	"github.com/Origens-Dev/gobeyond/agents/internal/playbackcompletion"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

// PlaybackCompletionFromContext returns only a current runtime-owned receipt.
// Actor/session metadata, decoded payloads and tool options cannot populate it.
// No runtime currently installs this marker: durable authentication and hidden
// mutation dispatch remain separate integration gates.
func PlaybackCompletionFromContext(ctx context.Context) (voicecontract.PlaybackCompletionReceipt, bool) {
	return playbackcompletion.From(ctx)
}

// Only code inside the framework can install the private marker. It must first
// verify durable completion, exact frozen declaration and live ownership. This
// helper alone provides no such authentication and is intentionally unused.
func withPlaybackCompletion(ctx context.Context, receipt voicecontract.PlaybackCompletionReceipt) context.Context {
	return playbackcompletion.With(ctx, receipt)
}
