package voicecontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// SourcePlaybackRequest is a scoped source retrieval, not delivery authority.
// CompletionToolID must name the frozen paired completion tool from the
// authored PlaybackPolicy so session admission can reserve that path.
type SourcePlaybackRequest struct {
	Version          string          `json:"version"`
	Context          Context         `json:"context"`
	ToolID           string          `json:"tool_id"`
	ToolCallID       string          `json:"tool_call_id"`
	CompletionToolID string          `json:"completion_tool_id"`
	InputDigest      string          `json:"input_digest"`
	Arguments        json.RawMessage `json:"arguments"`
}

func playbackContext(c Context) bool {
	// Any frozen agent may execute authored playback; product binds such as
	// call-operator are enforced by mailbox ValidateBudgetPolicy, not here.
	return c.ValidateForVersion(Version) == nil && identifier(c.AgentID) && c.Scope.Kind == "agent" && identifier(c.Scope.LineID) && c.ActorKind != "external_call"
}
func (r SourcePlaybackRequest) Validate() error {
	read := ReadRequest{Version: r.Version, Context: r.Context, ToolID: r.ToolID, ToolCallID: r.ToolCallID, InputDigest: r.InputDigest, Arguments: r.Arguments}
	if read.Validate() != nil || r.Version != Version || !playbackContext(r.Context) || !identifier(r.ToolID) || !identifier(r.CompletionToolID) || r.CompletionToolID == r.ToolID {
		return errors.New("invalid source playback request")
	}
	return nil
}

// HiddenCompletionRequest names a source clip only. Receipt identity must be
// resolved from authenticated durable state; JSON never carries that authority.
// ToolID is the frozen paired completion tool (not a hardcoded Operator name).
type HiddenCompletionRequest struct {
	Version          string  `json:"version"`
	Context          Context `json:"context"`
	ToolID           string  `json:"tool_id"`
	ToolCallID       string  `json:"tool_call_id"`
	SourceToolCallID string  `json:"source_tool_call_id"`
	ClipID           string  `json:"clip_id"`
}

func PlaybackCompletionCallID(c Context, source string) string {
	clip := PlaybackClipID(c, source)
	if clip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("playback-completion-v1\x00" + clip))
	return "completion_" + hex.EncodeToString(sum[:])
}
func (r HiddenCompletionRequest) Validate() error {
	if r.Version != Version || !playbackContext(r.Context) || !identifier(r.ToolID) || !identifier(r.SourceToolCallID) || r.ClipID != PlaybackClipID(r.Context, r.SourceToolCallID) || r.ToolCallID != PlaybackCompletionCallID(r.Context, r.SourceToolCallID) {
		return errors.New("invalid hidden completion request")
	}
	return nil
}
