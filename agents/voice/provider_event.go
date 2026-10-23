package voice

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode"
)

const (
	ProviderEventUsage         = "usage"
	ProviderEventSessionClosed = "session_closed"
)

// ProviderEvent is evidence observed in a received provider server frame. It is
// not a billing receipt, a host-stop instruction, or proof of complete usage.
// Adapters must hash the actual received frame, preserve absent metric fields,
// and report callback failures rather than silently dropping evidence.
//
// A session_closed event describes a provider-reported closure only. Whether
// that event qualifies a host to stop, or closes any usage stream, is a separate
// decision requiring the provider's supported protocol and stream inventory.
// If one frame includes both usage and closure, adapters emit a usage event
// first and a separate session_closed event with the same PayloadDigest.
type ProviderEvent struct {
	Kind               string `json:"kind"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	ProviderSessionID  string `json:"provider_session_id"`
	ProviderResponseID string `json:"provider_response_id,omitempty"`
	// SourceEventType identifies the received event parsed by the adapter. It is
	// evidence provenance, never a client-generated provider event identifier.
	SourceEventType string `json:"source_event_type"`
	// PayloadDigest is sha256:<lowercase hex> of the complete received frame.
	// Raw provider payloads, which can contain private content, are not retained.
	PayloadDigest string `json:"payload_digest"`
	// DurationSeconds is a cumulative provider session measurement. Tokens are
	// response measurements. A nil pointer means absent; a pointer to zero means
	// an explicit measured zero. Totals must never be synthesized from omissions.
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	InputTokens     *int64   `json:"input_tokens,omitempty"`
	OutputTokens    *int64   `json:"output_tokens,omitempty"`
	TotalTokens     *int64   `json:"total_tokens,omitempty"`
	// Final qualifies only the explicit usage measurements in this event. It
	// never implies session closure, provider readiness, or billing completeness.
	Final bool `json:"final"`
}

var ErrInvalidProviderEvent = errors.New("invalid voice provider event")

func providerEventIdentifier(s string, limit int) bool {
	if s == "" || len(s) > limit || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// Validate checks the bounded event shape. It cannot authenticate a payload
// digest or establish provider semantics; the receiving adapter owns both.
func (e ProviderEvent) Validate() error {
	if !providerEventIdentifier(e.Provider, 64) || !providerEventIdentifier(e.Model, 512) || !providerEventIdentifier(e.ProviderSessionID, 512) || !providerEventIdentifier(e.SourceEventType, 128) || (e.ProviderResponseID != "" && !providerEventIdentifier(e.ProviderResponseID, 512)) {
		return ErrInvalidProviderEvent
	}
	if len(e.PayloadDigest) != len("sha256:")+64 || !strings.HasPrefix(e.PayloadDigest, "sha256:") {
		return ErrInvalidProviderEvent
	}
	digest := strings.TrimPrefix(e.PayloadDigest, "sha256:")
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != digest {
		return ErrInvalidProviderEvent
	}
	present := e.DurationSeconds != nil || e.InputTokens != nil || e.OutputTokens != nil || e.TotalTokens != nil
	switch e.Kind {
	case ProviderEventUsage:
		if !present {
			return ErrInvalidProviderEvent
		}
	case ProviderEventSessionClosed:
		if present || e.Final {
			return ErrInvalidProviderEvent
		}
	default:
		return ErrInvalidProviderEvent
	}
	if e.DurationSeconds != nil {
		seconds := *e.DurationSeconds
		// Keep conversion to signed int64 milliseconds strictly below its first
		// unrepresentable value. float64(MaxInt64) rounds up to 2^63.
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds*1000 >= math.Exp2(63) {
			return ErrInvalidProviderEvent
		}
	}
	for _, value := range []*int64{e.InputTokens, e.OutputTokens, e.TotalTokens} {
		if value != nil && *value < 0 {
			return ErrInvalidProviderEvent
		}
	}
	return nil
}

// Clone detaches optional measurements so callback consumers cannot mutate
// decoder-owned values or another observer's copy of the same evidence.
func (e ProviderEvent) Clone() ProviderEvent {
	if e.DurationSeconds != nil {
		value := *e.DurationSeconds
		e.DurationSeconds = &value
	}
	if e.InputTokens != nil {
		value := *e.InputTokens
		e.InputTokens = &value
	}
	if e.OutputTokens != nil {
		value := *e.OutputTokens
		e.OutputTokens = &value
	}
	if e.TotalTokens != nil {
		value := *e.TotalTokens
		e.TotalTokens = &value
	}
	return e
}
