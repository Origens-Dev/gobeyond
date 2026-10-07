package voicecontract

import (
	"encoding/json"
	"errors"
)

const (
	MaxRingRecipients = 64
	MaxRingPlanBytes  = MaxEnvelopeBytes
)

// RingPlan is the public M2 typed recipient-set contract. Consumers freeze,
// validate, and observe it; edge wiring, a second ringer, and
// recipient_coordinator remain out of this package (I7).
//
// AttachedFallback is the sole automatic fallback activation. Lifecycle
// events (on_no_answer, on_busy, on_failed, on_cancelled) are observations:
// they must not mint grants or hops and cannot carry a destination. Answer
// and transfer remain separate ownership transitions (Operation /
// SoftphoneEvent), not RingPlan fields. The platform does not interpret the
// plan as product policy; opaque policy refs remain validate-only elsewhere.
type RingPlan struct {
	AttachedFallback     *RingAttachedFallback `json:"attached_fallback,omitempty"`
	Events               RingPlanEvents        `json:"events"`
	Version              string                `json:"version"`
	RecipientSetRevision string                `json:"recipient_set_revision"`
	Recipients           []RingRecipient       `json:"recipients"`
}

// RingAttachedFallback is authored onto the plan at admission. It is the only
// automatic fallback; event payloads cannot carry a destination or mint.
type RingAttachedFallback struct {
	DestinationID string `json:"destination_id"`
}

type RingRecipient struct {
	RecipientID   string `json:"recipient_id"`
	DestinationID string `json:"destination_id,omitempty"`
	PublicLabel   string `json:"public_label,omitempty"`
}

// RingPlanEvents lists observed lifecycle keys. Bindings are empty objects so
// unknown mint/fallback/ownership fields fail closed at decode.
type RingPlanEvents struct {
	OnNoAnswer  *RingEventBinding `json:"on_no_answer,omitempty"`
	OnBusy      *RingEventBinding `json:"on_busy,omitempty"`
	OnFailed    *RingEventBinding `json:"on_failed,omitempty"`
	OnCancelled *RingEventBinding `json:"on_cancelled,omitempty"`
}

type RingEventBinding struct{}

const (
	RingEventOnNoAnswer  = "on_no_answer"
	RingEventOnBusy      = "on_busy"
	RingEventOnFailed    = "on_failed"
	RingEventOnCancelled = "on_cancelled"
)

// RingLifecycleEvent is a decoded observation against a RingPlan. It does not
// authorize mint, fallback activation, answer, or transfer. It has no grant,
// hop, or destination fields; decoding rejects them.
type RingLifecycleEvent struct {
	Version              string `json:"version"`
	Type                 string `json:"type"`
	CallID               string `json:"call_id"`
	RecipientSetRevision string `json:"recipient_set_revision"`
	RecipientID          string `json:"recipient_id"`
	Generation           uint64 `json:"generation"`
	Sequence             uint64 `json:"sequence"`
}

// RingEventTypes is the closed observation set. Answer and transfer are not
// ring lifecycle events.
func RingEventTypes() []string {
	return []string{RingEventOnNoAnswer, RingEventOnBusy, RingEventOnFailed, RingEventOnCancelled}
}

func RecognizedRingEvent(typ string) bool {
	switch typ {
	case RingEventOnNoAnswer, RingEventOnBusy, RingEventOnFailed, RingEventOnCancelled:
		return true
	default:
		return false
	}
}

func (p RingPlan) Observes(typ string) bool {
	switch typ {
	case RingEventOnNoAnswer:
		return p.Events.OnNoAnswer != nil
	case RingEventOnBusy:
		return p.Events.OnBusy != nil
	case RingEventOnFailed:
		return p.Events.OnFailed != nil
	case RingEventOnCancelled:
		return p.Events.OnCancelled != nil
	default:
		return false
	}
}

func (p RingPlan) Recipient(id string) (RingRecipient, bool) {
	for _, r := range p.Recipients {
		if r.RecipientID == id {
			return r, true
		}
	}
	return RingRecipient{}, false
}

// AutomaticFallbackDestination returns the plan-authored attached fallback.
// Lifecycle events never supply this destination and cannot mint one.
func (p RingPlan) AutomaticFallbackDestination() (string, bool) {
	if p.AttachedFallback == nil || !identifier(p.AttachedFallback.DestinationID) {
		return "", false
	}
	return p.AttachedFallback.DestinationID, true
}

func (p RingPlan) Validate() error {
	if p.Version != Version || !identifier(p.RecipientSetRevision) || len(p.Recipients) == 0 || len(p.Recipients) > MaxRingRecipients {
		return errors.New("invalid ring plan")
	}
	seen := map[string]bool{}
	for _, r := range p.Recipients {
		if !identifier(r.RecipientID) || seen[r.RecipientID] || (r.DestinationID != "" && !identifier(r.DestinationID)) || (r.PublicLabel != "" && !safeText(r.PublicLabel, 128)) {
			return errors.New("invalid ring recipient")
		}
		seen[r.RecipientID] = true
	}
	if p.AttachedFallback != nil && !identifier(p.AttachedFallback.DestinationID) {
		return errors.New("invalid attached fallback")
	}
	return nil
}

func (e RingLifecycleEvent) Validate() error {
	if e.Version != Version || !callIdentifier(e.CallID) || !identifier(e.RecipientSetRevision) || !identifier(e.RecipientID) || e.Generation == 0 || e.Sequence == 0 {
		return errors.New("invalid ring lifecycle event")
	}
	if !RecognizedRingEvent(e.Type) {
		return errors.New("invalid ring lifecycle event")
	}
	return nil
}

// Matches binds an observation to a frozen plan. Sequence and generation
// machines remain consumer duties; this checks revision, recipient membership,
// and that the plan observes the event type. Events still do not mint or
// activate fallback.
func (e RingLifecycleEvent) Matches(p RingPlan) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if e.RecipientSetRevision != p.RecipientSetRevision {
		return errors.New("ring event revision mismatch")
	}
	if _, ok := p.Recipient(e.RecipientID); !ok {
		return errors.New("ring event recipient unknown")
	}
	if !p.Observes(e.Type) {
		return errors.New("ring event not observed")
	}
	return nil
}

// FreezeRingPlan validates and returns canonical bytes plus digest. Recipient
// order is preserved (ring order).
func FreezeRingPlan(p RingPlan) ([]byte, string, error) {
	if err := p.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, "", err
	}
	c, err := CanonicalJSON(raw, MaxRingPlanBytes)
	if err != nil {
		return nil, "", err
	}
	return c, Digest(c), nil
}

func FreezeRingLifecycleEvent(e RingLifecycleEvent) ([]byte, string, error) {
	if err := e.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, "", err
	}
	c, err := CanonicalJSON(raw, MaxRingPlanBytes)
	if err != nil {
		return nil, "", err
	}
	return c, Digest(c), nil
}
