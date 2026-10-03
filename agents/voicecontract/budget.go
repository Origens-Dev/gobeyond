package voicecontract

import "errors"

const BudgetPolicyOperatorMailboxV1 = "operator_mailbox_v1"
const OperatorMailboxAssistantTurns = 32

// ValidateBudgetPolicy binds a signed policy to its frozen authored declaration.
// Callers must first verify the grant signature, manifest digest, and live owner.
func ValidateBudgetPolicy(policy string, c Context, m Manifest) error {
	if policy != m.BudgetPolicy {
		return errors.New("voice budget declaration mismatch")
	}
	if policy == "" {
		return nil
	}
	if policy != BudgetPolicyOperatorMailboxV1 || c.Validate() != nil || c.AgentID != "call-operator" || c.Scope.Kind != "agent" {
		return errors.New("voice budget scope unavailable")
	}
	return validateBudgetManifest(m)
}

func validateBudgetManifest(m Manifest) error {
	if m.BudgetPolicy == "" {
		return nil
	}
	if m.BudgetPolicy != BudgetPolicyOperatorMailboxV1 || m.Version != Version {
		return errors.New("unsupported voice budget policy")
	}
	required := map[string]bool{"list-text-messages": false, "get-text-message": false, "dial-contact": false}
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
		case "dial-contact":
			if t.IsRead() || t.IsAction() {
				return errors.New("placement budget requires call control")
			}
			required[t.ID] = true
		case ToolIDHangUp, "hang-up":
			if t.IsRead() || t.IsAction() {
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
// Marking is deliberately reserved and unavailable until scoped mutations exist.
func ToolBudget(policy, toolID string) (bucket string, limit int, err error) {
	if policy != BudgetPolicyOperatorMailboxV1 {
		return "", 0, errors.New("unsupported voice budget policy")
	}
	switch toolID {
	case "list-text-messages":
		return "list", 4, nil
	case "get-text-message":
		return "get", 12, nil
	case "search-operator-directory", "dial-contact":
		return "placement", 2, nil
	case ToolIDHangUp, "hang-up":
		return "hangup", 1, nil
	default:
		return "", 0, errors.New("tool outside operator mailbox budget")
	}
}
