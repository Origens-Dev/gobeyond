package voicecontract

import "errors"

// RingPlan is the M2 typed recipient-set contract. It is encode/decode only:
// there is no edge wiring, second ringer, or recipient_coordinator
// implementation in this package.
//
// AttachedFallback is the sole automatic fallback activation. Lifecycle
// events (on_no_answer, on_busy, on_failed, on_cancelled) are observations
// and must not mint. Answer and transfer remain separate ownership
// transitions (Operation / SoftphoneEvent), not RingPlan fields.
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
// authorize mint, fallback activation, answer, or transfer.
type RingLifecycleEvent struct {
	Version              string `json:"version"`
	Type                 string `json:"type"`
	CallID               string `json:"call_id"`
	RecipientSetRevision string `json:"recipient_set_revision"`
	RecipientID          string `json:"recipient_id"`
	Generation           uint64 `json:"generation"`
	Sequence             uint64 `json:"sequence"`
}

func (p RingPlan) Validate() error {
	if p.Version != Version || !identifier(p.RecipientSetRevision) || len(p.Recipients) == 0 || len(p.Recipients) > 64 {
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
	switch e.Type {
	case RingEventOnNoAnswer, RingEventOnBusy, RingEventOnFailed, RingEventOnCancelled:
		return nil
	default:
		return errors.New("invalid ring lifecycle event")
	}
}
