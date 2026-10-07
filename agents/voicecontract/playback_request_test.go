package voicecontract

import (
	"testing"
	"time"
)

func TestPlaybackTypedRequestsRejectAlteredAuthority(t *testing.T) {
	c := completionFixture(time.Now().UTC()).Context
	r := SourcePlaybackRequest{Version: Version, Context: c, ToolID: "play-text-message", ToolCallID: "source", CompletionToolID: "complete-text-message-playback", Arguments: []byte(`{}`), InputDigest: Digest([]byte(`{}`))}
	h := HiddenCompletionRequest{Version: Version, Context: c, ToolID: "complete-text-message-playback", ToolCallID: PlaybackCompletionCallID(c, "source"), SourceToolCallID: "source", ClipID: PlaybackClipID(c, "source")}
	if r.Validate() != nil || h.Validate() != nil || h.ToolCallID == r.ToolCallID {
		t.Fatal("valid pair rejected")
	}
	// Non-Operator agent IDs are valid when the frozen manifest says so.
	other := r
	other.Context.AgentID = "support-agent"
	if other.Validate() != nil {
		t.Fatal("generic frozen agent rejected")
	}
	for _, field := range []string{"external", "scope", "agent", "tool", "pair", "digest", "version"} {
		t.Run(field, func(t *testing.T) {
			x := r
			switch field {
			case "external":
				x.Context.ActorKind = "external_call"
			case "scope":
				x.Context.Scope.Kind = "operator"
			case "agent":
				x.Context.AgentID = ""
			case "tool":
				x.ToolID = ""
			case "pair":
				x.CompletionToolID = x.ToolID
			case "digest":
				x.InputDigest = Digest([]byte(`{"other":true}`))
			case "version":
				x.Version = LegacyVersion
			}
			if x.Validate() == nil {
				t.Fatal("alteration admitted")
			}
		})
	}
	for _, field := range []string{"source", "clip", "call", "context", "tool"} {
		t.Run(field, func(t *testing.T) {
			x := h
			switch field {
			case "source":
				x.SourceToolCallID = "other"
			case "clip":
				x.ClipID = "clip_other"
			case "call":
				x.ToolCallID = r.ToolCallID
			case "context":
				x.Context.Generation++
			case "tool":
				x.ToolID = ""
			}
			if x.Validate() == nil {
				t.Fatal("completion alteration admitted")
			}
		})
	}
}
