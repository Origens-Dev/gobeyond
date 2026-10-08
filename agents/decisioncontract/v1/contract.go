// Package v1 defines the versioned, transport-neutral wire contract proposed
// for bounded decision agents. It is data and validation only; it does not
// register an agent or execute a conversation.
package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = "decision.graph/v1"

type RouteID string

// Definition is the frozen wire representation for the proposed
// DefineDecision(Graph) authoring API.
type Definition struct {
	SchemaVersion string              `json:"schemaVersion"`
	DigestInputs  ReleaseDigestInputs `json:"digestInputs"`
	ReleaseSHA256 string              `json:"releaseSha256"`
	Graph         Graph               `json:"graph"`
	PolicyGates   []PolicyGate        `json:"policyGates"`
}

// ReleaseDigestInputs carries canonical hashes of the frozen Definition's
// content sections. ValidateForReview recomputes and compares each component.
type ReleaseDigestInputs struct {
	Graph          string `json:"graphSha256"`
	Prompts        string `json:"promptsSha256"`
	Bindings       string `json:"bindingsSha256"`
	Normalization  string `json:"normalizationSha256"`
	Authority      string `json:"authoritySha256"`
	Policy         string `json:"policySha256"`
	LocaleAndVoice string `json:"localeAndVoiceSha256"`
}

type Graph struct {
	ID                        string                `json:"id"`
	Entry                     RouteID               `json:"entry"`
	Fallback                  RouteID               `json:"fallback"`
	Routes                    []Route               `json:"routes"`
	Authority                 AuthorityEnvelope     `json:"authority"`
	RetryGroups               []RetryGroup          `json:"retryGroups"`
	Messages                  []MessageFamily       `json:"messages"`
	Locale                    LocalePolicy          `json:"locale"`
	RequiredVoiceCapabilities []VoiceCapabilityName `json:"requiredVoiceCapabilities"`
	NormalizationRulesDigest  string                `json:"normalizationRulesDigest"`
}

// AuthorityEnvelope contains the capabilities inherited from the surrounding
// agent definition. Routes can select a subset and lower numeric ceilings;
// they cannot add a grant or raise an inherited budget.
type AuthorityEnvelope struct {
	ParentManifestSHA256 string                    `json:"parentManifestSha256"`
	Tools                []ToolGrant               `json:"tools"`
	Services             []ServiceGrant            `json:"services"`
	Bindings             []BindingGrant            `json:"bindings"`
	Budgets              map[BudgetDimension]Bound `json:"budgets"`
}

type ToolGrant struct {
	ID             string `json:"id"`
	SchemaSHA256   string `json:"schemaSha256"`
	ApprovalSHA256 string `json:"approvalSha256,omitempty"`
}

type ServiceGrant struct {
	ID                string `json:"id"`
	ContractSHA256    string `json:"contractSha256"`
	UsageSchemaSHA256 string `json:"usageSchemaSha256"`
}

type BindingGrant struct {
	ID             string `json:"id"`
	ContractSHA256 string `json:"contractSha256"`
}

type BudgetDimension string

const (
	BudgetSessionDuration        BudgetDimension = "session_duration_ms"
	BudgetRouteVisits            BudgetDimension = "route_visits"
	BudgetDecisionCalls          BudgetDimension = "decision_calls"
	BudgetReprompts              BudgetDimension = "reprompts"
	BudgetNoInputReprompts       BudgetDimension = "no_input_reprompts"
	BudgetNoMatchReprompts       BudgetDimension = "no_match_reprompts"
	BudgetAmbiguousReprompts     BudgetDimension = "ambiguous_reprompts"
	BudgetInputDuration          BudgetDimension = "input_duration_ms"
	BudgetTTSCharacters          BudgetDimension = "tts_characters"
	BudgetQueuedAudioBytes       BudgetDimension = "queued_audio_bytes"
	BudgetArtifactBytes          BudgetDimension = "artifact_bytes"
	BudgetEffectAttempts         BudgetDimension = "effect_attempts"
	BudgetSessionSpendMicrounits BudgetDimension = "session_spend_microunits"
	BudgetFirstAudioLatency      BudgetDimension = "first_audio_latency_ms"
	BudgetFirstResponseLatency   BudgetDimension = "first_response_latency_ms"
	BudgetProtectedPayloadTTL    BudgetDimension = "protected_payload_ttl_seconds"
)

var RequiredBudgetDimensions = []BudgetDimension{
	BudgetSessionDuration,
	BudgetRouteVisits,
	BudgetDecisionCalls,
	BudgetReprompts,
	BudgetNoInputReprompts,
	BudgetNoMatchReprompts,
	BudgetAmbiguousReprompts,
	BudgetInputDuration,
	BudgetTTSCharacters,
	BudgetQueuedAudioBytes,
	BudgetArtifactBytes,
	BudgetEffectAttempts,
	BudgetSessionSpendMicrounits,
	BudgetFirstAudioLatency,
	BudgetFirstResponseLatency,
	BudgetProtectedPayloadTTL,
}

type GateKind string

const (
	GateSessionDuration      GateKind = "session_duration_ceiling"
	GateRouteVisits          GateKind = "route_visit_ceiling"
	GateDecisionCalls        GateKind = "decision_call_ceiling"
	GateReprompts            GateKind = "retry_ceiling"
	GateNoInputReprompts     GateKind = "no_input_retry_ceiling"
	GateNoMatchReprompts     GateKind = "no_match_retry_ceiling"
	GateAmbiguousReprompts   GateKind = "ambiguity_retry_ceiling"
	GateInputDuration        GateKind = "input_duration_ceiling"
	GateTTSCharacters        GateKind = "tts_character_ceiling"
	GateQueuedAudioBytes     GateKind = "queued_audio_ceiling"
	GateArtifactBytes        GateKind = "artifact_byte_ceiling"
	GateEffectAttempts       GateKind = "effect_attempt_ceiling"
	GateSessionSpend         GateKind = "session_spend_ceiling"
	GateFirstAudioLatency    GateKind = "first_audio_latency_ceiling"
	GateFirstResponseLatency GateKind = "first_response_latency_ceiling"
	GateProtectedPayloadTTL  GateKind = "protected_payload_ttl"
	GateWrongRecipientRate   GateKind = "wrong_recipient_rate"
	GateClarificationRate    GateKind = "clarification_rate"
	GateLocaleProfile        GateKind = "qualified_locale_profile"
	GateVoiceProfile         GateKind = "qualified_voice_profile"
	GateVoiceFallback        GateKind = "voice_capability_fallback"
	GateJevServicePath       GateKind = "jev_service_path_and_usage"
	GateCallerAuthority      GateKind = "caller_and_relationship_authority"
	GateDTMFNegotiation      GateKind = "dtmf_negotiation"
	GatePromptRetention      GateKind = "prompt_retention_and_revocation"
	GatePromptPreparation    GateKind = "prompt_preparation_policy"
	GateRouteAuthoring       GateKind = "route_authoring_and_graph_discovery"
	GateSessionAdapter       GateKind = "stateful_session_adapter_contract"
	GatePromptRuntimeParity  GateKind = "prompt_loader_and_icu_runtime_parity"
)

var RequiredNonBudgetGateKinds = []GateKind{
	GateWrongRecipientRate,
	GateClarificationRate,
	GateLocaleProfile,
	GateVoiceProfile,
	GateVoiceFallback,
	GateJevServicePath,
	GateCallerAuthority,
	GateDTMFNegotiation,
	GatePromptRetention,
	GatePromptPreparation,
	GateRouteAuthoring,
	GateSessionAdapter,
	GatePromptRuntimeParity,
}

type GateStatus string

const (
	GateUnresolved GateStatus = "unresolved"
	GateQualified  GateStatus = "qualified"
)

// PolicyGate records a decision or threshold without inventing its value.
// Unresolved gates are valid in a review fixture and block activation.
type PolicyGate struct {
	ID               string     `json:"id"`
	Kind             GateKind   `json:"kind"`
	Status           GateStatus `json:"status"`
	Value            string     `json:"value,omitempty"`
	EvidenceRef      string     `json:"evidenceRef,omitempty"`
	UnresolvedReason string     `json:"unresolvedReason,omitempty"`
	OwnerDecision    string     `json:"ownerDecision,omitempty"`
}

// Bound references the policy gate for a ceiling. A nil Value records an
// unresolved threshold; a finite Value is permitted only after qualification.
type Bound struct {
	Value  *uint64 `json:"value,omitempty"`
	GateID string  `json:"gateId,omitempty"`
}

type Route struct {
	ID RouteID `json:"id"`
	// A nil selection inherits all grants; an explicit empty array grants none.
	UseTools        []string                  `json:"useTools"`
	UseServices     []string                  `json:"useServices"`
	UseBindings     []string                  `json:"useBindings"`
	BudgetOverrides map[BudgetDimension]Bound `json:"budgetOverrides,omitempty"`
	Say             *SayStep                  `json:"say,omitempty"`
	Listen          *ListenStep               `json:"listen,omitempty"`
	Match           *MatchStep                `json:"match,omitempty"`
	Decide          *DecideStep               `json:"decide,omitempty"`
	Act             []ActStep                 `json:"act,omitempty"`
	Next            OutcomeMap                `json:"next"`
	Fallback        Target                    `json:"fallback"`
	RetryGroup      string                    `json:"retryGroup,omitempty"`
}

type SayStep struct {
	Families         []string                 `json:"families"`
	LocaleSource     string                   `json:"localeSource"`
	ArgumentBindings []MessageArgumentBinding `json:"argumentBindings,omitempty"`
}

type MessageArgumentBinding struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type ListenStep struct {
	Accept  []InputModality `json:"accept"`
	BargeIn bool            `json:"bargeIn"`
}

type NoMatchBehavior string

const (
	NoMatchToDecision NoMatchBehavior = "decide"
	NoMatchToRoute    NoMatchBehavior = "route"
)

type MatchStep struct {
	BindingID string          `json:"bindingId"`
	OnNoMatch NoMatchBehavior `json:"onNoMatch"`
}

type DecideStep struct {
	ServiceID    string `json:"serviceId"`
	ResultPolicy string `json:"resultPolicy"`
}

type EffectKind string

const (
	EffectConnect EffectKind = "connect"
	EffectHandoff EffectKind = "handoff"
	EffectWrite   EffectKind = "write"
	EffectCustom  EffectKind = "custom"
)

type ActStep struct {
	ID                     string     `json:"id"`
	ToolID                 string     `json:"toolId"`
	Kind                   EffectKind `json:"kind"`
	TargetBinding          string     `json:"targetBinding,omitempty"`
	ReauthorizeAtExecution bool       `json:"reauthorizeAtExecution"`
	ReleasesCallOwnership  bool       `json:"releasesCallOwnership,omitempty"`
}

type ResultSource string

const (
	SourceInput    ResultSource = "input"
	SourceMatch    ResultSource = "match"
	SourceDecision ResultSource = "decision"
	SourceEffect   ResultSource = "effect"
	SourcePlayback ResultSource = "playback"
	SourceControl  ResultSource = "control"
)

type Outcome string

const (
	OutcomeFinalInput     Outcome = "final_input"
	OutcomeNoInput        Outcome = "no_input"
	OutcomeUtteranceLimit Outcome = "utterance_limit"
	OutcomeCandidate      Outcome = "candidate"
	OutcomeNoMatch        Outcome = "no_match"
	OutcomeAmbiguous      Outcome = "ambiguous"
	OutcomeRefusal        Outcome = "refusal"
	OutcomeError          Outcome = "error"
	OutcomeUnavailable    Outcome = "unavailable"
	OutcomeAccepted       Outcome = "accepted"
	OutcomeConfirmed      Outcome = "confirmed"
	OutcomeFailed         Outcome = "failed"
	OutcomeUnknown        Outcome = "unknown"
	OutcomeCompleted      Outcome = "completed"
	OutcomeCleared        Outcome = "cleared"
	OutcomeCancelled      Outcome = "cancelled"
	OutcomeDisconnected   Outcome = "disconnected"
	OutcomeExhausted      Outcome = "exhausted"
)

// Target is either a stable route ID or a typed terminal action.
type Target struct {
	Route    RouteID        `json:"route,omitempty"`
	Phase    RoutePhase     `json:"phase,omitempty"`
	Terminal TerminalAction `json:"terminal,omitempty"`
}

type RoutePhase string

const (
	PhaseSay    RoutePhase = "say"
	PhaseListen RoutePhase = "listen"
	PhaseMatch  RoutePhase = "match"
	PhaseDecide RoutePhase = "decide"
	PhaseAct    RoutePhase = "act"
)

type TerminalAction string

const (
	TerminalEndSession           TerminalAction = "end_session"
	TerminalEndGraph             TerminalAction = "end_graph"
	TerminalReleaseCallOwnership TerminalAction = "release_call_ownership"
	TerminalAwaitReceipt         TerminalAction = "await_receipt"
	TerminalSafeStop             TerminalAction = "safe_stop"
)

type OutcomeTransition struct {
	Source  ResultSource `json:"source"`
	Outcome Outcome      `json:"outcome"`
	Target  Target       `json:"target"`
}

// OutcomeMap is represented as an array so duplicate source/outcome pairs are
// detectable during validation instead of being silently overwritten by JSON.
type OutcomeMap []OutcomeTransition

type RetryGroup struct {
	ID                    string `json:"id"`
	CounterScope          string `json:"counterScope"`
	CountOn               string `json:"countOn"`
	DeduplicateByEventID  bool   `json:"deduplicateByEventId"`
	MaxReprompts          Bound  `json:"maxReprompts"`
	MaxNoInputReprompts   Bound  `json:"maxNoInputReprompts"`
	MaxNoMatchReprompts   Bound  `json:"maxNoMatchReprompts"`
	MaxAmbiguousReprompts Bound  `json:"maxAmbiguousReprompts"`
	Exhausted             Target `json:"exhausted"`
}

// RetryCounter is the persisted logical-task counter shape. Event IDs make an
// admitted retry idempotent under replay; session-wide ceilings are separate.
type RetryCounter struct {
	GroupID                  string   `json:"groupId"`
	LogicalTaskID            string   `json:"logicalTaskId"`
	Reprompts                uint64   `json:"reprompts"`
	NoInputReprompts         uint64   `json:"noInputReprompts"`
	NoMatchReprompts         uint64   `json:"noMatchReprompts"`
	AmbiguousReprompts       uint64   `json:"ambiguousReprompts"`
	CountedAdmissionEventIDs []string `json:"countedAdmissionEventIds"`
}

type LocalePolicy struct {
	BaseLocale           string   `json:"baseLocale"`
	FallbackLocale       string   `json:"fallbackLocale"`
	WholeMessageFallback bool     `json:"wholeMessageFallback"`
	EnabledLocales       []string `json:"enabledLocales"`
	QualificationGateID  string   `json:"qualificationGateId"`
}

type MessageFamily struct {
	ID             string            `json:"id"`
	BaseLocale     string            `json:"baseLocale"`
	FallbackLocale string            `json:"fallbackLocale"`
	Arguments      []MessageArgument `json:"arguments,omitempty"`
	Variants       map[string]string `json:"variants"`
}

type MessageArgument struct {
	Name string       `json:"name"`
	Type ArgumentType `json:"type"`
}

type ArgumentType string

const (
	ArgumentString ArgumentType = "string"
	ArgumentNumber ArgumentType = "number"
	ArgumentDate   ArgumentType = "date"
	ArgumentTime   ArgumentType = "time"
)

type VoiceCapabilityName string

const (
	VoiceExactTextSynthesis VoiceCapabilityName = "exact_text_synthesis"
	VoiceFinalSpeechText    VoiceCapabilityName = "final_speech_text"
	VoiceDTMFInput          VoiceCapabilityName = "dtmf_input"
	VoiceTextInput          VoiceCapabilityName = "text_input"
	VoiceBargeIn            VoiceCapabilityName = "barge_in"
	VoiceStreaming          VoiceCapabilityName = "streaming"
	VoiceCancellation       VoiceCapabilityName = "cancellation"
)

type CapabilityStatus string

const (
	CapabilityUnknown     CapabilityStatus = "unknown"
	CapabilitySupported   CapabilityStatus = "supported"
	CapabilityUnsupported CapabilityStatus = "unsupported"
)

// VoiceCapabilities is a resolved session snapshot supplied by a future
// adapter. It contains capability evidence and opaque profile references, not
// credentials or provider calls.
type VoiceCapabilities struct {
	ProfileRef       string                                   `json:"profileRef"`
	Revision         string                                   `json:"revision"`
	VoiceID          string                                   `json:"voiceId"`
	Locale           string                                   `json:"locale"`
	SupportedLocales []string                                 `json:"supportedLocales"`
	Formats          []AudioFormat                            `json:"formats"`
	Capabilities     map[VoiceCapabilityName]CapabilityStatus `json:"capabilities"`
	EvidenceRef      string                                   `json:"evidenceRef,omitempty"`
}

// QualificationKey binds a qualified voice profile to its immutable revision.
func (capabilities VoiceCapabilities) QualificationKey() string {
	return capabilities.ProfileRef + "@" + capabilities.Revision
}

type AudioFormat struct {
	Codec        string `json:"codec"`
	SampleRateHz uint32 `json:"sampleRateHz"`
	Channels     uint8  `json:"channels"`
}

type EventKind string

const (
	EventSessionAdmitted   EventKind = "session_admitted"
	EventRouteEntered      EventKind = "route_entered"
	EventSpeechStarted     EventKind = "speech_started"
	EventInputFinal        EventKind = "input_final"
	EventInitialSilence    EventKind = "initial_silence"
	EventUtteranceLimit    EventKind = "utterance_limit"
	EventRetryExhausted    EventKind = "retry_exhausted"
	EventPlaybackStarted   EventKind = "playback_started"
	EventPlaybackCompleted EventKind = "playback_completed"
	EventPlaybackCleared   EventKind = "playback_cleared"
	EventPlaybackFailed    EventKind = "playback_failed"
	EventMatchCompleted    EventKind = "match_completed"
	EventDecisionCompleted EventKind = "decision_completed"
	EventEffectSubmitted   EventKind = "effect_submitted"
	EventEffectReceipt     EventKind = "effect_receipt"
	EventHandoffAccepted   EventKind = "handoff_accepted"
	EventDisconnected      EventKind = "disconnected"
	EventCancelled         EventKind = "cancelled"
)

type InputModality string

const (
	ModalitySpeechFinal  InputModality = "speech_final"
	ModalityDTMFComplete InputModality = "dtmf_complete"
	ModalityText         InputModality = "text"
)

type ChannelKind string

const (
	ChannelVoice ChannelKind = "voice"
	ChannelText  ChannelKind = "text"
)

// NormalizedEvent carries server-derived identity and semantic event data.
// User text/audio are represented by protected references, never inline in a
// durable event.
type NormalizedEvent struct {
	ID             string              `json:"id"`
	Kind           EventKind           `json:"kind"`
	TenantID       string              `json:"tenantId"`
	SessionID      string              `json:"sessionId"`
	Generation     uint64              `json:"generation"`
	RouteID        RouteID             `json:"routeId,omitempty"`
	RouteEntryID   string              `json:"routeEntryId,omitempty"`
	InputWindowID  string              `json:"inputWindowId,omitempty"`
	Channel        ChannelKind         `json:"channel"`
	Modality       InputModality       `json:"modality,omitempty"`
	Locale         string              `json:"locale,omitempty"`
	ProtectedInput *ProtectedReference `json:"protectedInput,omitempty"`
	Digits         string              `json:"digits,omitempty"`
	SourceEventIDs []string            `json:"sourceEventIds,omitempty"`
	ReceivedAt     time.Time           `json:"receivedAt"`
	Match          *MatchResult        `json:"match,omitempty"`
	Decision       *DecisionResult     `json:"decision,omitempty"`
	EffectRequest  *EffectRequest      `json:"effectRequest,omitempty"`
	Effect         *EffectReceipt      `json:"effect,omitempty"`
	Playback       *PlaybackReceipt    `json:"playback,omitempty"`
}

type ProtectedReference struct {
	ID         string    `json:"id"`
	Purpose    string    `json:"purpose"`
	TenantID   string    `json:"tenantId"`
	SessionID  string    `json:"sessionId"`
	Generation uint64    `json:"generation"`
	SHA256     string    `json:"sha256"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type MatchResult struct {
	Outcome        Outcome `json:"outcome"`
	CandidateID    string  `json:"candidateId,omitempty"`
	SnapshotSHA256 string  `json:"snapshotSha256"`
	BindingID      string  `json:"bindingId"`
}

// CandidateSetSnapshot contains the opaque IDs that were authorized for one
// deterministic or Jev decision. Descriptions stay behind ProtectedSnapshot.
type CandidateSetSnapshot struct {
	SHA256            string              `json:"sha256"`
	CandidateIDs      []string            `json:"candidateIds"`
	ProtectedSnapshot *ProtectedReference `json:"protectedSnapshot"`
}

func (snapshot CandidateSetSnapshot) CanonicalSHA256() string {
	ids := append([]string(nil), snapshot.CandidateIDs...)
	sort.Strings(ids)
	protectedDigest := ""
	if snapshot.ProtectedSnapshot != nil {
		protectedDigest = snapshot.ProtectedSnapshot.SHA256
	}
	canonical := protectedDigest + "\x00" + strings.Join(ids, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

type DecisionResult struct {
	Outcome             Outcome                    `json:"outcome"`
	CandidateID         string                     `json:"candidateId,omitempty"`
	ProviderRef         string                     `json:"providerRef"`
	ModelRef            string                     `json:"modelRef"`
	Revision            string                     `json:"revision"`
	ResultSchemaSHA256  string                     `json:"resultSchemaSha256"`
	CandidateSetSHA256  string                     `json:"candidateSetSha256"`
	ProviderScoreFields map[string]json.RawMessage `json:"providerScoreFields,omitempty"`
	ASRConfidence       *float64                   `json:"asrConfidence,omitempty"`
	OptionProbability   *float64                   `json:"optionProbability,omitempty"`
	Usage               UsageRecord                `json:"usage"`
}

type UsageRecord struct {
	RequestID  string      `json:"requestId,omitempty"`
	Status     UsageStatus `json:"status"`
	Units      []UsageUnit `json:"units,omitempty"`
	CostMicros *uint64     `json:"costMicros,omitempty"`
}

type UsageStatus string

const (
	UsageMeasured         UsageStatus = "measured"
	UsageProviderReported UsageStatus = "provider_reported"
	UsageEstimated        UsageStatus = "estimated"
	UsageMissing          UsageStatus = "missing"
)

type UsageUnit struct {
	Kind   string  `json:"kind"`
	Amount float64 `json:"amount"`
}

type EffectIdentity struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenantId"`
	SessionID    string `json:"sessionId"`
	Generation   uint64 `json:"generation"`
	RouteEntryID string `json:"routeEntryId"`
	InputID      string `json:"inputId"`
	ActionID     string `json:"actionId"`
	GraphSHA256  string `json:"graphSha256"`
}

// CanonicalID provides a deterministic idempotency key for one logical effect.
func (identity EffectIdentity) CanonicalID() string {
	parts := []string{
		identity.TenantID,
		identity.SessionID,
		strconv.FormatUint(identity.Generation, 10),
		identity.RouteEntryID,
		identity.InputID,
		identity.ActionID,
		identity.GraphSHA256,
	}
	joined := strings.Join(parts, "\x00")
	digest := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(digest[:])
}

type EffectRequest struct {
	Identity               EffectIdentity      `json:"identity"`
	ToolID                 string              `json:"toolId"`
	Kind                   EffectKind          `json:"kind"`
	TargetBinding          string              `json:"targetBinding,omitempty"`
	TargetOpaqueID         string              `json:"targetOpaqueId,omitempty"`
	CandidateSetSHA256     string              `json:"candidateSetSha256,omitempty"`
	Payload                *ProtectedReference `json:"payload,omitempty"`
	ReauthorizeAtExecution bool                `json:"reauthorizeAtExecution"`
}

type EffectStatus string

const (
	EffectSubmitted EffectStatus = "submitted"
	EffectAccepted  EffectStatus = "accepted"
	EffectConfirmed EffectStatus = "confirmed"
	EffectFailed    EffectStatus = "failed"
	EffectUnknown   EffectStatus = "unknown"
)

type EffectReceipt struct {
	Identity          EffectIdentity `json:"identity"`
	Status            EffectStatus   `json:"status"`
	ReceiptID         string         `json:"receiptId,omitempty"`
	ProviderRequestID string         `json:"providerRequestId,omitempty"`
	TargetOpaqueID    string         `json:"targetOpaqueId,omitempty"`
	FailureCode       string         `json:"failureCode,omitempty"`
	ObservedAt        time.Time      `json:"observedAt"`
}

// ReleasesCallOwnership is true only for a declared call-owner release and a
// confirmed receipt for the exact intended target. Local graph termination
// alone does not release call ownership.
func (receipt EffectReceipt) ReleasesCallOwnership(releaseCallOwnership bool, intendedTargetOpaqueID string) bool {
	return releaseCallOwnership && intendedTargetOpaqueID != "" && receipt.Status == EffectConfirmed &&
		receipt.TargetOpaqueID == intendedTargetOpaqueID && receipt.Validate() == nil
}

type PlaybackReceipt struct {
	OperationID string    `json:"operationId"`
	Outcome     Outcome   `json:"outcome"`
	ReceiptID   string    `json:"receiptId,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
}

type OwnershipState string

const (
	OwnershipOwned          OwnershipState = "owned"
	OwnershipReceiptPending OwnershipState = "receipt_pending"
	OwnershipReleased       OwnershipState = "released"
)

type GraphOwnership struct {
	State                     OwnershipState `json:"state"`
	PendingEffectID           string         `json:"pendingEffectId,omitempty"`
	PendingTargetBinding      string         `json:"pendingTargetBinding,omitempty"`
	PendingTargetOpaqueID     string         `json:"pendingTargetOpaqueId,omitempty"`
	PendingCandidateSetSHA256 string         `json:"pendingCandidateSetSha256,omitempty"`
	ReleaseReceiptID          string         `json:"releaseReceiptId,omitempty"`
}

// ValidateOwnershipTransition enforces receipt-confirmed call ownership
// release without doing any call-control work. TerminalEndGraph only stops the
// local decision graph; this transition applies solely to call ownership.
func ValidateOwnershipTransition(from, to GraphOwnership, receipt *EffectReceipt, request *EffectRequest, action *ActStep) error {
	if from.State != OwnershipOwned && from.State != OwnershipReceiptPending && from.State != OwnershipReleased {
		return fmt.Errorf("unknown prior call ownership state %q", from.State)
	}
	if to.State != OwnershipOwned && to.State != OwnershipReceiptPending && to.State != OwnershipReleased {
		return fmt.Errorf("unknown next call ownership state %q", to.State)
	}
	if err := validateOwnershipState(from); err != nil {
		return fmt.Errorf("invalid prior call ownership state: %w", err)
	}
	if err := validateOwnershipState(to); err != nil {
		return fmt.Errorf("invalid next call ownership state: %w", err)
	}
	if from.State == OwnershipReleased {
		if to != from {
			return fmt.Errorf("released call ownership state is immutable")
		}
		return nil
	}
	if to.State != OwnershipReleased {
		return nil
	}
	if action == nil || request == nil || receipt == nil || !action.ReleasesCallOwnership ||
		(action.Kind != EffectConnect && action.Kind != EffectHandoff) || !action.ReauthorizeAtExecution {
		return fmt.Errorf("call ownership release requires its declared reauthorized route action")
	}
	if request.Identity.ActionID != action.ID || request.ToolID != action.ToolID || request.Kind != action.Kind || request.TargetBinding != action.TargetBinding {
		return fmt.Errorf("pending effect does not match the call-owner-releasing route action")
	}
	if !receipt.ReleasesCallOwnership(action.ReleasesCallOwnership, from.PendingTargetOpaqueID) {
		return fmt.Errorf("call ownership may be released only by a confirmed receipt for the intended target")
	}
	if err := request.Validate(time.Time{}); err != nil || request.Kind != EffectConnect && request.Kind != EffectHandoff || !request.ReauthorizeAtExecution {
		return fmt.Errorf("call ownership release requires the validated reauthorized call-control request")
	}
	if from.State != OwnershipReceiptPending {
		return fmt.Errorf("call ownership release requires a pending receipt state")
	}
	if from.PendingEffectID == "" || from.PendingEffectID != receipt.Identity.ID || from.PendingEffectID != request.Identity.ID {
		return fmt.Errorf("confirmed receipt does not match the pending effect")
	}
	if request.TargetOpaqueID == "" || request.TargetOpaqueID != from.PendingTargetOpaqueID || request.TargetOpaqueID != receipt.TargetOpaqueID ||
		request.TargetBinding == "" || request.TargetBinding != from.PendingTargetBinding ||
		request.CandidateSetSHA256 != from.PendingCandidateSetSHA256 {
		return fmt.Errorf("confirmed receipt target does not match the intended effect target")
	}
	if to.ReleaseReceiptID == "" || to.ReleaseReceiptID != receipt.ReceiptID {
		return fmt.Errorf("released call ownership must retain the confirming receipt ID")
	}
	return nil
}

func validateOwnershipState(state GraphOwnership) error {
	pendingFields := state.PendingEffectID != "" || state.PendingTargetBinding != "" || state.PendingTargetOpaqueID != "" || state.PendingCandidateSetSHA256 != ""
	switch state.State {
	case OwnershipOwned:
		if pendingFields || state.ReleaseReceiptID != "" {
			return fmt.Errorf("owned state cannot carry a pending effect or release receipt")
		}
	case OwnershipReceiptPending:
		if !isIdentifier(state.PendingEffectID) {
			return fmt.Errorf("receipt-pending state requires a stable pending effect ID")
		}
		anyTargetField := state.PendingTargetBinding != "" || state.PendingTargetOpaqueID != "" || state.PendingCandidateSetSHA256 != ""
		if anyTargetField && (!isIdentifier(state.PendingTargetBinding) || !isIdentifier(state.PendingTargetOpaqueID) || !isSHA256(state.PendingCandidateSetSHA256)) {
			return fmt.Errorf("pending call-control target requires binding, opaque ID, and candidate-set digest")
		}
		if state.ReleaseReceiptID != "" {
			return fmt.Errorf("receipt-pending state cannot carry a release receipt")
		}
	case OwnershipReleased:
		if !isIdentifier(state.ReleaseReceiptID) || pendingFields {
			return fmt.Errorf("released state requires only its confirming receipt ID")
		}
	default:
		return fmt.Errorf("unknown graph ownership state %q", state.State)
	}
	return nil
}

type SessionPin struct {
	SchemaVersion        string            `json:"schemaVersion"`
	ReleaseSHA256        string            `json:"releaseSha256"`
	GraphSHA256          string            `json:"graphSha256"`
	PromptFamiliesSHA256 string            `json:"promptFamiliesSha256"`
	BindingsSHA256       string            `json:"bindingsSha256"`
	NormalizationSHA256  string            `json:"normalizationSha256"`
	AuthoritySHA256      string            `json:"authoritySha256"`
	PolicySHA256         string            `json:"policySha256"`
	LocaleAndVoiceSHA256 string            `json:"localeAndVoiceSha256"`
	SnapshotSHA256       string            `json:"snapshotSha256"`
	VoiceSHA256          string            `json:"voiceSha256"`
	Generation           uint64            `json:"generation"`
	Locale               string            `json:"locale"`
	PromptFamily         string            `json:"promptFamily"`
	PromptVariantLocale  string            `json:"promptVariantLocale"`
	Voice                VoiceCapabilities `json:"voice"`
}

func finiteProbability(value *float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1)
}
