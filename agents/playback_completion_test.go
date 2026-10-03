package agents

import (
	"context"
	"encoding/json"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"os"
	"strings"
	"testing"
	"time"
)

func trustedCompletionFixture(t *testing.T) voicecontract.PlaybackCompletionReceipt {
	t.Helper()
	raw, err := os.ReadFile("voicecontract/testdata/command.json")
	if err != nil {
		t.Fatal(err)
	}
	var c voicecontract.Command
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("fixture")
	}
	c.Context.Scope.Kind = "agent"
	c.Context.Scope.DIDID = ""
	c.Context.Scope.RecipientSetRevision = ""
	c.Context.Scope.SelectedLineID = ""
	c.Context.AgentID = "call-operator"
	c.Context.TransportCallID = "transport"
	c.Context.ParentCallID = "parent"
	c.Context.HopID = "hop"
	c.Context.HopCount = 1
	now := time.Now().UTC()
	r := voicecontract.PlaybackCompletionReceipt{Context: c.Context, ToolCallID: "call", ToolID: "play-text-message", CompletionToolID: "complete-text-message-playback", MessageID: "message", MessageCreatedAt: now.Add(-time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond), MessageExpiresAt: now.Add(time.Hour), TextDigest: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute)}
	r.ClipID = voicecontract.PlaybackClipID(r.Context, r.ToolCallID)
	if err := r.Validate(now); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestPlaybackCompletionContextPrivateFreshAndImmutable(t *testing.T) {
	r := trustedCompletionFixture(t)
	ctx := withPlaybackCompletion(context.Background(), r)
	got, ok := PlaybackCompletionFromContext(ctx)
	if !ok || got != r {
		t.Fatal("trusted receipt lost")
	}
	got.Context.Generation++
	again, ok := PlaybackCompletionFromContext(ctx)
	if !ok || again != r {
		t.Fatal("returned receipt mutated context")
	}
	expired := r
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if _, ok := PlaybackCompletionFromContext(withPlaybackCompletion(ctx, expired)); ok {
		t.Fatal("expired marker reused inherited receipt")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := PlaybackCompletionFromContext(canceled); ok {
		t.Fatal("canceled context exposed receipt")
	}
	for _, untrusted := range []context.Context{context.Background(), context.WithValue(context.Background(), "playback_completion", r), context.WithValue(context.Background(), struct{}{}, r), context.WithValue(context.Background(), struct{}{}, true)} {
		if _, ok := PlaybackCompletionFromContext(untrusted); ok {
			t.Fatal("decoded or boolean receipt acquired authority")
		}
	}
}
