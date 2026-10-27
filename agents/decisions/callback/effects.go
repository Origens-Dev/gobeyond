package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
)

// TurnID is the host-facing identity of one route entry. It is the reducer's
// RouteEntryID. Every host effect, receipt, and event must carry the turn that
// produced it so stale deliveries can be dropped.
type TurnID string

// SayEffect asks the host to play one preloaded prompt. PromptID is a
// compiled manifest identity, not a cache key or free text.
type SayEffect struct {
	TurnID   TurnID `json:"turnId"`
	PromptID string `json:"promptId"`
}

// PlaybackStatus is the host's terminal say result. The listen no-input
// timer starts when this receipt is observed for the same turn.
type PlaybackStatus string

const (
	PlaybackPlayed    PlaybackStatus = "played"
	PlaybackCancelled PlaybackStatus = "cancelled"
)

// PlaybackReceipt is the host's say result.
type PlaybackReceipt struct {
	TurnID TurnID         `json:"turnId"`
	Status PlaybackStatus `json:"status"`
}

// ListenEffect arms host capture for one turn. Endpointing and timers are
// host-owned. Timeouts are milliseconds.
type ListenEffect struct {
	TurnID                     TurnID `json:"turnId"`
	NoInputTimeoutMillis       int64  `json:"noInputTimeoutMillis"`
	MaxUtteranceDurationMillis int64  `json:"maxUtteranceDurationMillis"`
}

// ListenEventKind is a host-to-reducer listen outcome. There is no partial kind.
type ListenEventKind string

const (
	ListenFinalTranscript ListenEventKind = "final_transcript"
	ListenNoInput         ListenEventKind = "no_input"
	ListenCancelled       ListenEventKind = "cancelled"
	ListenDTMFUnsupported ListenEventKind = "dtmf_unsupported"
)

// ListenEvent is a final listen outcome. Text is present only on
// final_transcript. Partial transcripts must not be encoded here.
type ListenEvent struct {
	Kind   ListenEventKind `json:"kind"`
	TurnID TurnID          `json:"turnId"`
	Text   string          `json:"text,omitempty"`
}

// ActEffect asks the host to perform one already-matched action. TargetID is
// opaque; the SDK does not inspect its contents.
type ActEffect struct {
	TurnID   TurnID `json:"turnId"`
	TargetID string `json:"targetId"`
	Action   string `json:"action"`
}

// DecisionEffectAdapter is the host executor for reducer say/listen/act
// effects. Implementations run against DecisionSessionWorkflow and
// decisions.Effect; they must not start a second reducer. Say must call
// Definition.RequirePrompt and fail with prompt_unavailable when the ID is
// not preloaded.
type DecisionEffectAdapter interface {
	Say(context.Context, SayEffect) (PlaybackReceipt, error)
	Listen(context.Context, ListenEffect) error
	Act(context.Context, ActEffect) error
}

// StaleTurn reports that an event or receipt does not match the active turn
// and must be dropped.
func StaleTurn(got, want TurnID) bool {
	return got == "" || want == "" || got != want
}

// TurnIDFromEffect reads the reducer route-entry identity as a turn ID.
func TurnIDFromEffect(effect decisions.Effect) TurnID {
	return TurnID(effect.RouteEntryID)
}

// SayEffectFrom maps a reducer say onto the host prompt-id seam. v0 names
// exactly one compiled prompt ID. Missing or multiple families are not
// converted into free text.
func SayEffectFrom(effect decisions.Effect) (SayEffect, error) {
	if effect.Kind != decisions.EffectSay {
		return SayEffect{}, fmt.Errorf("effect kind %q is not say", effect.Kind)
	}
	turnID := TurnIDFromEffect(effect)
	if err := validateTurnID(turnID); err != nil {
		return SayEffect{}, err
	}
	if len(effect.MessageFamilies) != 1 || strings.TrimSpace(effect.MessageFamilies[0]) == "" {
		return SayEffect{}, fmt.Errorf("%w: say requires exactly one manifest prompt id", decisionv1.ErrPromptUnavailable)
	}
	promptID := effect.MessageFamilies[0]
	if err := decisionv1.ValidateIdentifier(promptID); err != nil {
		return SayEffect{}, fmt.Errorf("%w: %v", decisionv1.ErrPromptUnavailable, err)
	}
	return SayEffect{TurnID: turnID, PromptID: promptID}, nil
}

// ListenEffectFrom maps a reducer listen onto the host seam. Timeouts are
// supplied by the host from its preloaded policy; they are not inferred here.
func ListenEffectFrom(effect decisions.Effect, noInputTimeoutMillis, maxUtteranceDurationMillis int64) (ListenEffect, error) {
	if effect.Kind != decisions.EffectListen {
		return ListenEffect{}, fmt.Errorf("effect kind %q is not listen", effect.Kind)
	}
	turnID := TurnIDFromEffect(effect)
	if err := validateTurnID(turnID); err != nil {
		return ListenEffect{}, err
	}
	listen := ListenEffect{
		TurnID:                     turnID,
		NoInputTimeoutMillis:       noInputTimeoutMillis,
		MaxUtteranceDurationMillis: maxUtteranceDurationMillis,
	}
	if err := listen.Validate(); err != nil {
		return ListenEffect{}, err
	}
	return listen, nil
}

// ActEffectFrom maps a reducer dispatch intent onto the host act seam.
func ActEffectFrom(effect decisions.Effect) (ActEffect, error) {
	if effect.Kind != decisions.EffectDispatchIntent {
		return ActEffect{}, fmt.Errorf("effect kind %q is not act", effect.Kind)
	}
	if effect.Request == nil {
		return ActEffect{}, fmt.Errorf("act requires the reducer-emitted target")
	}
	act := ActEffect{
		TurnID:   TurnIDFromEffect(effect),
		TargetID: effect.Request.TargetOpaqueID,
		Action:   effect.ActionID,
	}
	if err := act.Validate(); err != nil {
		return ActEffect{}, err
	}
	return act, nil
}

func (effect SayEffect) Validate() error {
	if err := validateTurnID(effect.TurnID); err != nil {
		return err
	}
	if err := decisionv1.ValidateIdentifier(effect.PromptID); err != nil {
		return fmt.Errorf("say prompt id: %w", err)
	}
	return nil
}

func (receipt PlaybackReceipt) Validate() error {
	if err := validateTurnID(receipt.TurnID); err != nil {
		return err
	}
	switch receipt.Status {
	case PlaybackPlayed, PlaybackCancelled:
		return nil
	default:
		return fmt.Errorf("playback status %q is not played or cancelled", receipt.Status)
	}
}

func (effect ListenEffect) Validate() error {
	if err := validateTurnID(effect.TurnID); err != nil {
		return err
	}
	if effect.NoInputTimeoutMillis <= 0 || effect.MaxUtteranceDurationMillis <= 0 {
		return fmt.Errorf("listen timeouts must be positive")
	}
	return nil
}

func (event ListenEvent) Validate() error {
	if err := validateTurnID(event.TurnID); err != nil {
		return err
	}
	switch event.Kind {
	case ListenFinalTranscript:
		if strings.TrimSpace(event.Text) == "" || strings.TrimSpace(event.Text) != event.Text {
			return fmt.Errorf("final transcript text is required")
		}
		return nil
	case ListenNoInput, ListenCancelled, ListenDTMFUnsupported:
		if event.Text != "" {
			return fmt.Errorf("listen event %q cannot carry transcript text", event.Kind)
		}
		return nil
	default:
		return fmt.Errorf("listen event kind %q is not a final host event", event.Kind)
	}
}

func (effect ActEffect) Validate() error {
	if err := validateTurnID(effect.TurnID); err != nil {
		return err
	}
	if err := decisionv1.ValidateIdentifier(string(effect.TargetID)); err != nil {
		return fmt.Errorf("act target id: %w", err)
	}
	if err := decisionv1.ValidateIdentifier(effect.Action); err != nil {
		return fmt.Errorf("act action: %w", err)
	}
	return nil
}

func validateTurnID(id TurnID) error {
	if err := decisionv1.ValidateIdentifier(string(id)); err != nil {
		return fmt.Errorf("turn id: %w", err)
	}
	return nil
}

func decodeJSON(data []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("expected exactly one JSON value")
		}
		return err
	}
	return nil
}
