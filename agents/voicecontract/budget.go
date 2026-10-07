package voicecontract

import "errors"

const BudgetPolicyOperatorMailboxV1 = "operator_mailbox_v1"
const OperatorMailboxAssistantTurns = 32

// BudgetPolicyGenericV1 is an authorable budget declaration with no product
// agent-id bind (not call-operator, voice-mail, or leave-message) and no
// mailbox tool allowlist. Numeric generic buckets are unassigned; do not treat
// the inventory below as generic_v1 defaults.
const BudgetPolicyGenericV1 = "generic_v1"

// Inventoried host/admission/workflow voice limits (current code, not generic_v1 defaults):
//
//	Envelope/schema: MaxManifestBytes 32768, MaxEnvelopeBytes 16384, MaxSchemaBytes 4096,
//	MaxTools 8, MaxDepth 12, MaxKeys 1024, MaxHops 20, envelope grant 4096 bytes,
//	activity grant 8192 bytes, common_context 12288, call_context 1024, read arguments 1024,
//	description 512, identifier 128, read/write max_result_bytes 1–4096,
//	playback text 4096 / result 8192.
//	Session/admission: maxVoiceSessionToolCalls 8, default read/control legacyLimit 2,
//	default MaxAssistantTurns 10, mailbox MaxAssistantTurns 32, CallControl ToolNames 1–8,
//	playback dispatch attempts 3, LocalActivity StartToClose 30s, session TTL 30m,
//	approval expiry 5m.
//	operator_mailbox_v1 buckets (allowlist unchanged): list 4, get 12, placement 2, hangup 1.
//	operator_mailbox_playback_v1 adds playback 12 and completion 12.

func IsMailboxBudgetPolicy(policy string) bool {
	return policy == BudgetPolicyOperatorMailboxV1 || policy == BudgetPolicyOperatorMailboxPlaybackV1
}

func IsGenericBudgetPolicy(policy string) bool {
	return policy == BudgetPolicyGenericV1
}

func RecognizedBudgetPolicy(policy string) bool {
	return policy == "" || IsMailboxBudgetPolicy(policy) || IsGenericBudgetPolicy(policy)
}

// ValidateBudgetPolicy binds a signed policy to its frozen authored declaration.
// Callers must first verify the grant signature, manifest digest, and live owner.
func ValidateBudgetPolicy(policy string, c Context, m Manifest) error {
	if policy != m.BudgetPolicy {
		return errors.New("voice budget declaration mismatch")
	}
	if policy == "" {
		return nil
	}
	if c.Validate() != nil {
		return errors.New("voice budget scope unavailable")
	}
	if IsGenericBudgetPolicy(policy) {
		return validateBudgetManifest(m)
	}
	if !IsMailboxBudgetPolicy(policy) || c.AgentID != "call-operator" || c.Scope.Kind != "agent" {
		return errors.New("voice budget scope unavailable")
	}
	return validateBudgetManifest(m)
}

func validateBudgetManifest(m Manifest) error {
	if m.BudgetPolicy == "" {
		return nil
	}
	if m.Version != Version {
		return errors.New("unsupported voice budget policy")
	}
	if IsGenericBudgetPolicy(m.BudgetPolicy) {
		// Skeleton only: no product tool allowlist and no numeric buckets.
		return nil
	}
	if !IsMailboxBudgetPolicy(m.BudgetPolicy) {
		return errors.New("unsupported voice budget policy")
	}
	required := map[string]bool{"list-text-messages": false, "get-text-message": false, "dial-contact": false}
	if m.BudgetPolicy == BudgetPolicyOperatorMailboxPlaybackV1 {
		required["play-text-message"] = false
		required["complete-text-message-playback"] = false
	}
	for _, t := range m.Tools {
		switch t.ID {
		case "list-text-messages", "get-text-message":
			if !t.IsRead() {
				return errors.New("mailbox budget requires read tools")
			}
			required[t.ID] = true
		case "search-operator-directory":
			if !t.IsRead() {
				return errors.New("directory budget requires read tool")
			}
		case "play-text-message":
			if m.BudgetPolicy != BudgetPolicyOperatorMailboxPlaybackV1 || !t.IsPlayback() || t.Playback == nil || t.Playback.CompletionToolID != "complete-text-message-playback" {
				return errors.New("playback budget requires exact paired tool")
			}
			required[t.ID] = true
		case "complete-text-message-playback":
			if m.BudgetPolicy != BudgetPolicyOperatorMailboxPlaybackV1 || !t.IsPlaybackCompletion() {
				return errors.New("completion budget requires hidden mutation")
			}
			required[t.ID] = true
		case "dial-contact":
			if t.IsRead() || t.IsWrite() {
				return errors.New("placement budget requires call control")
			}
			required[t.ID] = true
		case ToolIDHangUp, "hang-up":
			if t.IsRead() || t.IsWrite() {
				return errors.New("hangup budget requires call control")
			}
		default:
			return errors.New("tool outside operator mailbox budget")
		}
	}
	for _, present := range required {
		if !present {
			return errors.New("operator mailbox declaration incomplete")
		}
	}
	return nil
}

// ToolBudget selects independent fixed buckets after ValidateBudgetPolicy.
// Completion is a separate mutation bucket, available only to the new policy.
// Classification does not authorize it: trusted clip receipt dispatch is mandatory.
// generic_v1 has no assigned buckets; callers must not invent defaults.
func ToolBudget(policy, toolID string) (bucket string, limit int, err error) {
	if IsGenericBudgetPolicy(policy) {
		return "", 0, errors.New("generic budget buckets are unassigned")
	}
	if !IsMailboxBudgetPolicy(policy) {
		return "", 0, errors.New("unsupported voice budget policy")
	}
	switch toolID {
	case "play-text-message":
		if policy == BudgetPolicyOperatorMailboxPlaybackV1 {
			return "playback", 12, nil
		}
	case "complete-text-message-playback":
		if policy == BudgetPolicyOperatorMailboxPlaybackV1 {
			return "completion", 12, nil
		}
	case "list-text-messages":
		return "list", 4, nil
	case "get-text-message":
		return "get", 12, nil
	case "search-operator-directory", "dial-contact":
		return "placement", 2, nil
	case ToolIDHangUp, "hang-up":
		return "hangup", 1, nil
	}
	return "", 0, errors.New("tool outside operator mailbox budget")
}

// ManifestDeclaresPlayback reports authored playback/completion tools. It does
// not authorize execution; grant, owner, and capability checks remain separate.
func ManifestDeclaresPlayback(m Manifest) bool {
	for _, t := range m.Tools {
		if t.IsPlayback() || t.IsPlaybackCompletion() {
			return true
		}
	}
	return false
}
