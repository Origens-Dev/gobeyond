package voicecontract

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s Scope) Validate() error {
	switch s.Kind {
	case "operator":
		if !identifier(s.LineID) || s.DIDID != "" || s.RecipientSetRevision != "" || s.SelectedLineID != "" {
			return errors.New("invalid operator scope")
		}
	case "screener":
		if s.LineID != "" || !identifier(s.DIDID) || !identifier(s.RecipientSetRevision) || (s.SelectedLineID != "" && !identifier(s.SelectedLineID)) {
			return errors.New("invalid screener scope")
		}
	default:
		return errors.New("unknown scope kind")
	}
	return nil
}
func (c Context) Validate() error {
	for _, s := range []string{c.OrganizationID, c.ProjectID, c.EnvironmentID, c.NetworkID, c.CallID, c.SessionID, c.ActorID, c.AgentID, c.AgentRevision} {
		if !identifier(s) {
			return errors.New("invalid context identifier")
		}
	}
	if c.ActorKind != "user" && c.ActorKind != "external_call" && c.ActorKind != "service" {
		return errors.New("invalid actor kind")
	}
	if c.Generation == 0 || !digestValid(c.ManifestDigest) {
		return errors.New("invalid context fence")
	}
	return c.Scope.Validate()
}
func digestValid(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, r := range s[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func (s ScopeTransition) Validate() error {
	if s.Version != Version || s.Context.Validate() != nil || s.Context.Scope.Kind != "screener" || s.Context.Scope.SelectedLineID != "" || s.ExpectedGeneration != s.Context.Generation || s.NextGeneration != s.ExpectedGeneration+1 || s.NextGeneration == 0 || !identifier(s.SelectedLineID) {
		return errors.New("invalid scope transition")
	}
	return nil
}
func (c Command) Validate() error {
	if c.Version != Version || c.Context.Validate() != nil || !identifier(c.OperationID) || !identifier(c.ToolID) || !identifier(c.ToolCallID) {
		return errors.New("invalid command")
	}
	v, e := CanonicalJSON(c.Arguments, MaxSchemaBytes)
	if e != nil {
		return e
	}
	if Digest(v) != c.InputDigest {
		return errors.New("input digest mismatch")
	}
	return nil
}
func (o Operation) Validate() error {
	if o.Version != Version || o.Context.Validate() != nil || !identifier(o.OperationID) || o.Sequence == 0 {
		return errors.New("invalid operation")
	}
	switch o.State {
	case "accepted", "ringing", "answered", "cancelled":
		if o.FailureCode != "" {
			return errors.New("unexpected failure code")
		}
	case "failed":
		switch o.FailureCode {
		case "validation", "busy", "control", "no_answer", "owner_lost", "media_failed":
		default:
			return errors.New("invalid failure code")
		}
	default:
		return errors.New("invalid operation state")
	}
	return nil
}
func (t TerminalResult) Validate() error {
	if t.Version != Version || t.Context.Validate() != nil || !identifier(t.OperationID) || t.Generation != t.Context.Generation || t.Sequence == 0 || !t.Terminal || (t.State != "ringing" && t.State != "answered") {
		return errors.New("invalid terminal result")
	}
	return nil
}
func safeText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
