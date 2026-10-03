package voicecontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// PlaybackCompletionReceipt is a text-free projection of a verified durable
// completion. Decoding/validating it does not authenticate it. Only the runtime
// may project a receipt after verifying durable completion and current ownership.
// Completion means full transport delivery, not proof a human heard the message.
type PlaybackCompletionReceipt struct {
	Context          Context   `json:"context"`
	ToolCallID       string    `json:"tool_call_id"`
	ToolID           string    `json:"tool_id"`
	CompletionToolID string    `json:"completion_tool_id"`
	MessageID        string    `json:"message_id"`
	MessageCreatedAt time.Time `json:"message_created_at"`
	MessageExpiresAt time.Time `json:"message_expires_at"`
	TextDigest       string    `json:"text_digest"`
	ExpiresAt        time.Time `json:"expires_at"`
	ClipID           string    `json:"clip_id"`
}

// PlaybackClipID uses canonical context JSON, a NUL separator, and ToolCallID.
// This matches private context encoding regardless of Go struct field order;
// private-only nonempty fields are not valid for ordinary mailbox playback.
func PlaybackClipID(c Context, toolCallID string) string {
	if c.ValidateForVersion(Version) != nil || !identifier(toolCallID) {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	canonical, err := CanonicalJSON(raw, MaxSchemaBytes)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append(append(canonical, 0), []byte(toolCallID)...))
	return "clip_" + hex.EncodeToString(sum[:])
}

// Validate checks structural identity and freshness only. It grants no receipt
// authority; callers must use the trusted context accessor, not decoded JSON.
func (r PlaybackCompletionReceipt) Validate(now time.Time) error {
	if r.Context.ValidateForVersion(Version) != nil || r.Context.Scope.Kind != "agent" || r.Context.AgentID != "call-operator" || r.Context.ActorKind == "external_call" || !identifier(r.Context.Scope.LineID) || !identifier(r.ToolCallID) || r.ToolID != "play-text-message" || r.CompletionToolID != "complete-text-message-playback" || !identifier(r.MessageID) || r.MessageCreatedAt.IsZero() || r.MessageCreatedAt.After(now.Add(time.Second)) || r.MessageCreatedAt.Location() != time.UTC || r.MessageExpiresAt.Location() != time.UTC || r.ExpiresAt.Location() != time.UTC || !r.MessageExpiresAt.After(now) || !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(5*time.Minute)) || r.ExpiresAt.After(r.MessageExpiresAt) {
		return errors.New("invalid playback completion identity")
	}
	digest, err := hex.DecodeString(r.TextDigest)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != r.TextDigest || r.ClipID != PlaybackClipID(r.Context, r.ToolCallID) {
		return errors.New("invalid playback completion fence")
	}
	return nil
}
