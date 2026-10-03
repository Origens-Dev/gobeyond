package voicecontract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func completionFixture(now time.Time) PlaybackCompletionReceipt {
	c := v2TestContext()
	c.AgentID = "call-operator"
	return PlaybackCompletionReceipt{Context: c, ToolCallID: "tool_call", ToolID: "play-text-message", CompletionToolID: "complete-text-message-playback", MessageID: "message", MessageCreatedAt: now.Add(-time.Hour), MessageExpiresAt: now.Add(time.Hour), TextDigest: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute), ClipID: PlaybackClipID(c, "tool_call")}
}
func TestPlaybackCompletionReceiptExactFenceAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	r := completionFixture(now)
	if err := r.Validate(now); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	var restored PlaybackCompletionReceipt
	if json.Unmarshal(raw, &restored) != nil || restored.Validate(now) != nil || !restored.MessageCreatedAt.Equal(r.MessageCreatedAt) {
		t.Fatal("Nano receipt lost")
	}
	for _, name := range []string{"clip", "generation", "session", "line", "actor", "tool", "completion", "timestamp", "expired", "message_expired", "ttl", "digest"} {
		t.Run(name, func(t *testing.T) {
			r := completionFixture(now)
			switch name {
			case "clip":
				r.ClipID = "clip_other"
			case "generation":
				r.Context.Generation++
			case "session":
				r.Context.SessionID = "other"
			case "line":
				r.Context.Scope.LineID = "other"
			case "actor":
				r.Context.ActorKind = "external_call"
			case "tool":
				r.ToolID = "get-text-message"
			case "completion":
				r.CompletionToolID = "mark-text-message-read"
			case "timestamp":
				r.MessageCreatedAt = r.MessageCreatedAt.In(time.FixedZone("offset", 3600))
			case "expired":
				r.ExpiresAt = now
			case "message_expired":
				r.MessageExpiresAt = now
			case "ttl":
				r.ExpiresAt = now.Add(time.Hour)
			case "digest":
				r.TextDigest = strings.Repeat("A", 64)
			}
			if r.Validate(now) == nil {
				t.Fatal("altered receipt accepted")
			}
		})
	}
}
