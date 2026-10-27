package callback

import (
	"context"
	"fmt"
	"strings"

	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

const (
	// PathMatch is the product-owned match callback. Authenticate it with
	// connections.SignDelivery / connections.VerifyDelivery.
	PathMatch = "/v1/decision/callbacks/match"
	Method    = "POST"
)

// HostCallback is the product-owned match seam. One op: the app returns only
// targets the caller may act on.
//
// Match is synchronous. The host may retry the same signed Event-ID;
// implementations must return the same tagged result (idempotent store or
// deterministic recompute). A repeated Event-ID is not a failure.
type HostCallback interface {
	Match(context.Context, MatchRequest) (MatchResponse, error)
}

// MatchKind is the tagged match result. The SDK never receives a separate
// authorize step.
type MatchKind string

const (
	MatchMatched MatchKind = "matched"
	MatchClarify MatchKind = "clarify"
	MatchNoMatch MatchKind = "no_match"
)

// MatchRequest is the signed match callback body.
type MatchRequest struct {
	SessionID    string `json:"sessionId"`
	Utterance    string `json:"utterance"`
	AttemptCount uint64 `json:"attemptCount"`
}

// MatchResponse is exactly one of matched{targetId}, clarify{promptId}, or
// no_match. PromptID is a manifest prompt identity, not a cache key.
type MatchResponse struct {
	Result   MatchKind `json:"result"`
	TargetID string    `json:"targetId,omitempty"`
	PromptID string    `json:"promptId,omitempty"`
}

const maxUtteranceBytes = 8 << 10

func (request MatchRequest) Validate() error {
	if err := decisionv1.ValidateIdentifier(request.SessionID); err != nil {
		return fmt.Errorf("match session id: %w", err)
	}
	if request.Utterance == "" || strings.TrimSpace(request.Utterance) != request.Utterance {
		return fmt.Errorf("match utterance is required")
	}
	if len(request.Utterance) > maxUtteranceBytes {
		return fmt.Errorf("match utterance exceeds %d bytes", maxUtteranceBytes)
	}
	return nil
}

func (response MatchResponse) Validate() error {
	switch response.Result {
	case MatchMatched:
		if response.PromptID != "" {
			return fmt.Errorf("matched result cannot carry a prompt id")
		}
		if err := decisionv1.ValidateIdentifier(response.TargetID); err != nil {
			return fmt.Errorf("matched target id: %w", err)
		}
		return nil
	case MatchClarify:
		if response.TargetID != "" {
			return fmt.Errorf("clarify result cannot carry a target id")
		}
		if err := decisionv1.ValidateIdentifier(response.PromptID); err != nil {
			return fmt.Errorf("clarify prompt id: %w", err)
		}
		return nil
	case MatchNoMatch:
		if response.TargetID != "" || response.PromptID != "" {
			return fmt.Errorf("no_match cannot carry a target or prompt id")
		}
		return nil
	default:
		return fmt.Errorf("match result %q is not matched, clarify, or no_match", response.Result)
	}
}

// ValidateFor binds a structurally valid match result to the frozen
// definition. Clarify prompt IDs must be in that manifest; otherwise the
// session fails closed with prompt_unavailable and no say is emitted.
func (response MatchResponse) ValidateFor(definition decisionv1.Definition) error {
	if err := response.Validate(); err != nil {
		return err
	}
	if response.Result != MatchClarify {
		return nil
	}
	if err := definition.RequirePrompt(response.PromptID); err != nil {
		return err
	}
	return nil
}

// DecodeMatchRequest rejects unknown fields so extra product data cannot enter
// the SDK.
func DecodeMatchRequest(data []byte) (MatchRequest, error) {
	var request MatchRequest
	if err := decodeJSON(data, &request); err != nil {
		return MatchRequest{}, fmt.Errorf("decode match request: %w", err)
	}
	if err := request.Validate(); err != nil {
		return MatchRequest{}, err
	}
	return request, nil
}

// DecodeMatchResponse rejects unknown fields. The SDK only accepts the tagged
// match result. Call ValidateFor with the frozen definition before acting on
// a clarify prompt ID.
func DecodeMatchResponse(data []byte) (MatchResponse, error) {
	var response MatchResponse
	if err := decodeJSON(data, &response); err != nil {
		return MatchResponse{}, fmt.Errorf("decode match response: %w", err)
	}
	if err := response.Validate(); err != nil {
		return MatchResponse{}, err
	}
	return response, nil
}

func DecodeSayEffect(data []byte) (SayEffect, error) {
	var effect SayEffect
	if err := decodeJSON(data, &effect); err != nil {
		return SayEffect{}, fmt.Errorf("decode say effect: %w", err)
	}
	if err := effect.Validate(); err != nil {
		return SayEffect{}, err
	}
	return effect, nil
}

func DecodePlaybackReceipt(data []byte) (PlaybackReceipt, error) {
	var receipt PlaybackReceipt
	if err := decodeJSON(data, &receipt); err != nil {
		return PlaybackReceipt{}, fmt.Errorf("decode playback receipt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return PlaybackReceipt{}, err
	}
	return receipt, nil
}

func DecodeListenEvent(data []byte) (ListenEvent, error) {
	var event ListenEvent
	if err := decodeJSON(data, &event); err != nil {
		return ListenEvent{}, fmt.Errorf("decode listen event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return ListenEvent{}, err
	}
	return event, nil
}
