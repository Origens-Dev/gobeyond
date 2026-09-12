package voicecontract

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validVersion(v string) bool { return v == Version || v == LegacyVersion }
func v2(v string) bool           { return v == Version }

// ValidateForVersion applies the version fence in addition to structural
// context validation. Context.Validate remains useful for callers that only
// have a decoded value and therefore accepts both generations.
func (c Context) ValidateForVersion(version string) error {
	if !validVersion(version) || c.Validate() != nil {
		return errors.New("invalid context version")
	}
	if v2(version) {
		if c.Scope.Kind != "agent" || !identifier(c.TransportCallID) || !identifier(c.ParentCallID) || !identifier(c.HopID) || c.HopCount == 0 || c.HopCount > MaxHops {
			return errors.New("invalid agent context identity")
		}
		return nil
	}
	if c.Scope.Kind == "agent" || c.TransportCallID != "" || c.ParentCallID != "" || c.HopID != "" || c.HopCount != 0 {
		return errors.New("v2 context on legacy wire")
	}
	return nil
}

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
	case "agent":
		if !identifier(s.LineID) || s.DIDID != "" || s.RecipientSetRevision != "" || s.SelectedLineID != "" {
			return errors.New("invalid agent scope")
		}
	default:
		return errors.New("unknown scope kind")
	}
	return nil
}
func (c Context) Validate() error {
	for _, s := range []string{c.ExecutionID, c.OrganizationID, c.ProjectID, c.EnvironmentID, c.NetworkID, c.CallID, c.SessionID, c.ActorID, c.AgentID, c.AgentRevision} {
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
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if c.TransportCallID != "" || c.ParentCallID != "" || c.HopID != "" || c.HopCount != 0 {
		if !identifier(c.TransportCallID) || !identifier(c.ParentCallID) || !identifier(c.HopID) || c.HopCount == 0 || c.HopCount > MaxHops || c.Scope.Kind != "agent" {
			return errors.New("invalid per-hop identity")
		}
	}
	return nil
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
	if s.Version != LegacyVersion || s.Context.ValidateForVersion(LegacyVersion) != nil || s.Context.Scope.Kind != "screener" || s.Context.Scope.SelectedLineID != "" || s.ExpectedGeneration != s.Context.Generation || s.NextGeneration != s.ExpectedGeneration+1 || s.NextGeneration == 0 || !identifier(s.SelectedLineID) {
		return errors.New("invalid scope transition")
	}
	return nil
}
func (c Command) Validate() error {
	if c.ToolID == "dial-contact" && c.AnnouncementBarrierID == 0 {
		return errors.New("announcement drain barrier required")
	}
	if !validVersion(c.Version) || c.Context.ValidateForVersion(c.Version) != nil || !identifier(c.OperationID) || !identifier(c.ToolID) || !identifier(c.ToolCallID) {
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
	if !validVersion(o.Version) || o.Context.ValidateForVersion(o.Version) != nil || !identifier(o.OperationID) || o.Sequence == 0 {
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
	if !validVersion(t.Version) || t.Context.ValidateForVersion(t.Version) != nil || !identifier(t.OperationID) || t.Generation != t.Context.Generation || t.Sequence == 0 || !t.Terminal || (t.State != "ringing" && t.State != "answered" && (t.Version != Version || t.State != "ended")) {
		return errors.New("invalid terminal result")
	}
	if t.ToolID != "" && !identifier(t.ToolID) {
		return errors.New("invalid terminal tool")
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

func (e Envelope) Validate() error {
	if !validVersion(e.Version) || (e.Route != "assistant" && e.Route != "inbound_assistant/screener") || len(e.Grant) == 0 || len(e.Grant) > 4096 || !digestValid(e.ManifestDigest) || len(e.CommonContext) > 12288 || len(e.CallContext) > 1024 || !utf8.ValidString(e.CommonContext) || !utf8.ValidString(e.CallContext) {
		return errors.New("invalid envelope")
	}
	if e.Route == "inbound_assistant/screener" && e.Screening == nil {
		return errors.New("screening context required")
	}
	if s := e.Screening; s != nil {
		if !identifier(s.DIDID) || len(s.RecipientIDs) > 64 {
			return errors.New("invalid screening context")
		}
		switch s.IdentityClass {
		case "unknown", "platform_internal", "active_network_connection":
		default:
			return errors.New("invalid identity classification")
		}
		for _, id := range s.RecipientIDs {
			if !identifier(id) {
				return errors.New("invalid recipient ID")
			}
		}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(raw) > MaxEnvelopeBytes {
		return errors.New("envelope size exceeded")
	}
	return nil
}

// Validate checks current structural claims only. Cryptographic verification and
// time, registration, nonce, owner and registry checks remain mandatory upstream.
func (g GrantClaims) Validate() error {
	if !validVersion(g.Version) || g.GrantVersion != 3 || !identifier(g.KeyID) || g.Context.ValidateForVersion(g.Version) != nil || !identifier(g.Nonce) || g.ExpiresAt <= 0 {
		return errors.New("invalid current grant")
	}
	if len(g.Capabilities) != 3 || g.Capabilities[0] != "start" || g.Capabilities[1] != "cancel" || g.Capabilities[2] != "execute" {
		return errors.New("invalid current capabilities")
	}
	// Identity-only manifests mint with no VoiceControl tools and therefore no
	// destination classes. Empty is allowed; non-empty values must still be valid.
	if len(g.DestinationClasses) > 0 {
		if err := classes(g.DestinationClasses); err != nil {
			return err
		}
	}
	return targetKinds(g.TargetKinds)
}
func (s SoftphoneEvent) Validate() error {
	if !validVersion(s.Version) || s.Type != "call_state" || !identifier(s.CallID) || s.Generation == 0 || s.Sequence == 0 || !identifier(s.RemoteParty.DestinationID) || len(s.RemoteParty.PublicLabel) == 0 || !safeText(s.RemoteParty.PublicLabel, 128) {
		return errors.New("invalid softphone event")
	}
	if v2(s.Version) {
		if !identifier(s.TransportCallID) || !identifier(s.ParentCallID) || !identifier(s.HopID) {
			return errors.New("invalid softphone hop identity")
		}
	} else if s.TransportCallID != "" || s.ParentCallID != "" || s.HopID != "" {
		return errors.New("v2 softphone identity on legacy wire")
	}
	switch s.State {
	case "ringing", "answered", "failed", "cancelled":
		return nil
	default:
		return errors.New("invalid softphone state")
	}
}

func (r ReadRequest) Validate() error {
	if !validVersion(r.Version) || r.Context.ValidateForVersion(r.Version) != nil || (r.Version == LegacyVersion && r.Context.Scope.Kind != "operator") || (r.Version == Version && r.Context.Scope.Kind != "agent") || !identifier(r.ToolID) || !identifier(r.ToolCallID) {
		return errors.New("invalid remote read")
	}
	raw, err := CanonicalJSON(r.Arguments, 1024)
	if err != nil {
		return err
	}
	if Digest(raw) != r.InputDigest {
		return errors.New("read input digest mismatch")
	}
	return nil
}
