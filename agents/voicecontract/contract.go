// Package voicecontract defines the versioned provider-neutral call-control wire
// boundary. These values are server-owned; decoding a value does not authenticate it.
package voicecontract

import "encoding/json"

const Version = "1"

// Scope is a tagged union. SelectedLineID is added only by a fenced transition.
type Scope struct {
	Kind                 string `json:"kind"`
	LineID               string `json:"line_id,omitempty"`
	DIDID                string `json:"did_id,omitempty"`
	RecipientSetRevision string `json:"recipient_set_revision,omitempty"`
	SelectedLineID       string `json:"selected_line_id,omitempty"`
}

// Context must be constructed from a verified current grant and active owner.
// Neither model arguments nor caller metadata may populate these fields.
type Context struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	EnvironmentID  string `json:"environment_id"`
	NetworkID      string `json:"network_id"`
	CallID         string `json:"call_id"`
	SessionID      string `json:"session_id"`
	ActorID        string `json:"actor_id"`
	ActorKind      string `json:"actor_kind"`
	AgentID        string `json:"agent_id"`
	AgentRevision  string `json:"agent_revision"`
	ManifestDigest string `json:"manifest_digest"`
	Generation     uint64 `json:"generation"`
	Scope          Scope  `json:"scope"`
}

type GrantClaims struct {
	Version            string   `json:"version"`
	GrantVersion       int      `json:"grant_version"`
	Capabilities       []string `json:"capabilities"`
	KeyID              string   `json:"kid"`
	Context            Context  `json:"context"`
	Nonce              string   `json:"nonce"`
	ExpiresAt          int64    `json:"expires_at"`
	DestinationClasses []string `json:"destination_classes"`
}

type Manifest struct {
	Version          string `json:"version"`
	Revision         string `json:"revision"`
	CompiledRevision string `json:"compiled_revision"`
	Tools            []Tool `json:"tools"`
}

type Tool struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	InputSchema        json.RawMessage `json:"input_schema"`
	SchemaDigest       string          `json:"schema_digest"`
	DestinationClasses []string        `json:"destination_classes"`
	TerminalOnSuccess  bool            `json:"terminal_on_success"`
}

// Command is also the Temporal LocalActivity request. OperationID and the
// idempotency tuple are allocated by the authenticated server before execution.
type Command struct {
	AnnouncementBarrierID uint64          `json:"announcement_barrier_id"`
	Version               string          `json:"version"`
	Context               Context         `json:"context"`
	OperationID           string          `json:"operation_id"`
	ToolID                string          `json:"tool_id"`
	ToolCallID            string          `json:"tool_call_id"`
	InputDigest           string          `json:"input_digest"`
	Arguments             json.RawMessage `json:"arguments"`
}

type Operation struct {
	Version     string  `json:"version"`
	Context     Context `json:"context"`
	OperationID string  `json:"operation_id"`
	Sequence    uint64  `json:"sequence"`
	State       string  `json:"state"`
	FailureCode string  `json:"failure_code,omitempty"`
}

// TerminalResult suppresses provider tools, continuations and audio only after
// announcement drain and authoritative ringing/answered event acceptance.
type TerminalResult struct {
	Context     Context `json:"context"`
	Version     string  `json:"version"`
	OperationID string  `json:"operation_id"`
	Generation  uint64  `json:"generation"`
	Sequence    uint64  `json:"sequence"`
	State       string  `json:"state"`
	Terminal    bool    `json:"terminal"`
}

// Envelope carries ordered compact context; grant is opaque and never a schema.
type Envelope struct {
	Screening      *ScreeningContext `json:"screening,omitempty"`
	Version        string            `json:"version"`
	Route          string            `json:"route"`
	Grant          string            `json:"grant"`
	ManifestDigest string            `json:"manifest_digest"`
	CommonContext  string            `json:"common_context"`
	CallContext    string            `json:"call_context"`
}

// RemoteParty contains public presentation data only, never caller purpose.
type RemoteParty struct {
	DestinationID string `json:"destination_id"`
	PublicLabel   string `json:"public_label"`
}

type SoftphoneEvent struct {
	Version     string      `json:"version"`
	Type        string      `json:"type"`
	CallID      string      `json:"call_id"`
	Generation  uint64      `json:"generation"`
	Sequence    uint64      `json:"sequence"`
	State       string      `json:"state"`
	RemoteParty RemoteParty `json:"remote_party"`
}

// ScopeTransition is a server-authorized recipient selection CAS. The old full
// context must still match the owner and grant at ExpectedGeneration.
type ScopeTransition struct {
	Version            string  `json:"version"`
	Context            Context `json:"context"`
	ExpectedGeneration uint64  `json:"expected_generation"`
	NextGeneration     uint64  `json:"next_generation"`
	SelectedLineID     string  `json:"selected_line_id"`
}

// ScreeningContext is an extension to the existing assistant session config,
// not a replacement. Platform identity is authoritative; ANI never grants bypass.
type ScreeningContext struct {
	DIDID                 string   `json:"did_id"`
	IdentityClass         string   `json:"identity_class"`
	RecipientIDs          []string `json:"recipient_ids"`
	ScreeningEnabled      bool     `json:"screening_enabled"`
	AllowUnsolicitedCalls bool     `json:"allow_unsolicited_calls"`
	VoicemailEnabled      bool     `json:"voicemail_enabled"`
}
