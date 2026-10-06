package voicecontract

import (
	"encoding/json"
	"errors"
)

const (
	MailboxMessageAgentID        = "voice-mail"
	MailboxMessageToolID         = "leave-text-message"
	MailboxMessageToolName       = "leave_text_message"
	MaxMailboxMessageInputBytes  = 4096
	MaxMailboxMessageResultBytes = 4096
)

// MailboxMessageRequest is constructed only after current grant and owner verification.
// Its destination is Context.NetworkID/Scope.LineID; arguments never select a mailbox.
type MailboxMessageRequest struct {
	Version     string          `json:"version"`
	Context     Context         `json:"context"`
	ToolID      string          `json:"tool_id"`
	ToolCallID  string          `json:"tool_call_id"`
	InputDigest string          `json:"input_digest"`
	Arguments   json.RawMessage `json:"arguments"`
}

func (r MailboxMessageRequest) Validate() error {
	if r.Version != Version || r.Context.ValidateForVersion(r.Version) != nil || r.Context.AgentID != MailboxMessageAgentID || r.Context.Scope.Kind != "agent" || r.ToolID != MailboxMessageToolID || !identifier(r.ToolCallID) {
		return errors.New("invalid mailbox message request")
	}
	raw, err := CanonicalJSON(r.Arguments, MaxMailboxMessageInputBytes)
	if err != nil {
		return err
	}
	if Digest(raw) != r.InputDigest {
		return errors.New("mailbox message input digest mismatch")
	}
	return nil
}
