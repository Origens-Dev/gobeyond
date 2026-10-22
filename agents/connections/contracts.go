// Package connections defines the versioned app-to-app agent boundary. Values
// are data contracts, not authorization: providers must resolve grants from their
// durable store and revalidate caller and resource authority before admission
// and each tool invocation.
package connections

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const Version = "1"
const ProviderFunded = "provider_funded"

type Tenant struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	EnvironmentID  string `json:"environment_id"`
}

func (t Tenant) Validate() error {
	if !identifier(t.OrganizationID) || !identifier(t.ProjectID) || !identifier(t.EnvironmentID) {
		return errors.New("invalid tenant")
	}
	return nil
}

type Resource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (r Resource) Validate() error {
	if !identifier(r.Type) || !identifier(r.ID) {
		return errors.New("invalid resource")
	}
	return nil
}

// Caller is a consumer-server-resolved principal. A line grant authorizes the
// authenticated line, not an inferred human identity. AssignmentGeneration must
// change whenever the line's authorization assignment changes.
type Caller struct {
	Kind                 string `json:"kind"`
	ID                   string `json:"id"`
	NetworkID            string `json:"network_id,omitempty"`
	AssignmentGeneration uint64 `json:"assignment_generation,omitempty"`
}

func (c Caller) Validate() error {
	if !identifier(c.ID) {
		return errors.New("invalid caller")
	}
	switch c.Kind {
	case "member":
		if !identifier(c.NetworkID) {
			return errors.New("member requires network")
		}
	case "line":
		if !identifier(c.NetworkID) || c.AssignmentGeneration == 0 {
			return errors.New("line requires network and assignment generation")
		}
	case "user":
		if c.NetworkID != "" || c.AssignmentGeneration != 0 {
			return errors.New("local user cannot carry line identity")
		}
	default:
		return errors.New("invalid caller kind")
	}
	return nil
}

type Grant struct {
	ID           string    `json:"id"`
	Version      uint64    `json:"version"`
	OwnerID      string    `json:"owner_id"`
	Consumer     Tenant    `json:"consumer"`
	Provider     Tenant    `json:"provider"`
	Resource     Resource  `json:"resource"`
	AgentID      string    `json:"agent_id"`
	Capabilities []string  `json:"capabilities"`
	Callers      []Caller  `json:"callers"`
	ExpiresAt    time.Time `json:"expires_at"`
	RevokedAt    time.Time `json:"revoked_at,omitempty"`
}

func (g Grant) Validate() error {
	if !identifier(g.ID) || g.Version == 0 || !identifier(g.OwnerID) || !identifier(g.AgentID) || g.Consumer.Validate() != nil || g.Provider.Validate() != nil || g.Resource.Validate() != nil || g.ExpiresAt.IsZero() {
		return errors.New("invalid grant")
	}
	if err := validateCapabilities(g.Capabilities); err != nil {
		return err
	}
	if len(g.Callers) == 0 || len(g.Callers) > 1000 {
		return errors.New("invalid approved callers")
	}
	seen := map[Caller]bool{}
	for _, c := range g.Callers {
		if c.Validate() != nil || c.Kind == "user" || seen[c] {
			return errors.New("invalid approved caller")
		}
		seen[c] = true
	}
	return nil
}
func (g Grant) Active(now time.Time) bool {
	return g.Validate() == nil && g.RevokedAt.IsZero() && now.Before(g.ExpiresAt)
}
func (g Grant) Allows(c Caller, capability string, now time.Time) bool {
	if !g.Active(now) || c.Validate() != nil || !contains(g.Capabilities, capability) {
		return false
	}
	for _, approved := range g.Callers {
		if approved == c {
			return true
		}
	}
	return false
}

// Admission is provider-authorized input for a fresh target execution. The
// hosting platform authenticates the request and mints its own runtime grant;
// neither this struct nor an OAuth access token is itself a voice grant.
type Admission struct {
	Version        string    `json:"version"`
	ConnectionID   string    `json:"connection_id"`
	GrantID        string    `json:"grant_id"`
	GrantVersion   uint64    `json:"grant_version"`
	Source         Tenant    `json:"source"`
	Target         Tenant    `json:"target"`
	Caller         Caller    `json:"caller"`
	Resource       Resource  `json:"resource"`
	AgentID        string    `json:"agent_id"`
	SessionID      string    `json:"session_id"`
	CallID         string    `json:"call_id"`
	ExecutionID    string    `json:"execution_id"`
	ConfigRevision uint64    `json:"config_revision"`
	AgentRevision  string    `json:"agent_revision"`
	ManifestDigest string    `json:"manifest_digest"`
	Capabilities   []string  `json:"capabilities"`
	BillingPolicy  string    `json:"billing_policy"`
	IssuedAt       time.Time `json:"issued_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (a Admission) Validate() error {
	if a.Version != Version || a.Source.Validate() != nil || a.Target.Validate() != nil || a.Caller.Validate() != nil || a.Resource.Validate() != nil || !identifier(a.AgentID) || !identifier(a.SessionID) || !identifier(a.CallID) || !identifier(a.ExecutionID) || a.ConfigRevision == 0 || !identifier(a.AgentRevision) || !digest(a.ManifestDigest) || a.BillingPolicy != ProviderFunded || a.IssuedAt.IsZero() || !a.ExpiresAt.After(a.IssuedAt) || a.ExpiresAt.Sub(a.IssuedAt) > 5*time.Minute {
		return errors.New("invalid admission")
	}
	if a.Caller.Kind == "user" {
		// Portal/local-owner authority is checked by the provider service, never
		// inferred from these fields. It has no fabricated OAuth self-connection.
		if a.Source != a.Target || a.ConnectionID != "" || a.GrantID != "" || a.GrantVersion != 0 {
			return errors.New("invalid local admission")
		}
	} else if !identifier(a.ConnectionID) || !identifier(a.GrantID) || a.GrantVersion == 0 {
		return errors.New("invalid linked admission")
	}
	return validateCapabilities(a.Capabilities)
}

// Experience is owned by the provider app. Resolving this configuration must
// never enlarge the deployed tool registry or an invocation's permissions.
type Experience struct {
	Revision     uint64   `json:"revision"`
	Name         string   `json:"name"`
	Avatar       string   `json:"avatar,omitempty"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	BackendModel string   `json:"backend_model,omitempty"`
	Voice        string   `json:"voice"`
	Language     string   `json:"language,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	EnabledTools []string `json:"enabled_tools"`
}

func (e Experience) Validate() error {
	if e.Revision == 0 || !text(e.Name, 128) || !identifier(e.Provider) || !identifier(e.Model) || !text(e.Voice, 128) || len(e.Instructions) > 16000 || len(e.Avatar) > 2048 || len(e.Language) > 35 {
		return errors.New("invalid experience")
	}
	if e.BackendModel != "" && !identifier(e.BackendModel) {
		return errors.New("invalid backend model")
	}
	if len(e.EnabledTools) > 128 {
		return errors.New("too many enabled tools")
	}
	seen := map[string]bool{}
	for _, tool := range e.EnabledTools {
		if !identifier(tool) || seen[tool] {
			return errors.New("invalid enabled tools")
		}
		seen[tool] = true
	}
	return nil
}

// UsageEvent carries trusted lifecycle or reported model consumption. Mode is
// delta or cumulative for usage records; cumulative snapshots replace earlier
// snapshots for the same execution/provider/metric scope, never add to them.
// EventID deduplicates delivery. Sequence orders snapshots. Unknown costs must
// remain unknown rather than becoming zero charges.
type UsageEvent struct {
	Version                 string    `json:"version"`
	EventID                 string    `json:"event_id"`
	Tenant                  Tenant    `json:"tenant"`
	Resource                Resource  `json:"resource"`
	SessionID               string    `json:"session_id"`
	CallID                  string    `json:"call_id"`
	ExecutionID             string    `json:"execution_id"`
	ConnectionID            string    `json:"connection_id,omitempty"`
	Kind                    string    `json:"kind"`
	Sequence                uint64    `json:"sequence"`
	OccurredAt              time.Time `json:"occurred_at"`
	Provider                string    `json:"provider,omitempty"`
	Model                   string    `json:"model,omitempty"`
	ProviderSessionID       string    `json:"provider_session_id,omitempty"`
	ProviderResponseID      string    `json:"provider_response_id,omitempty"`
	Metric                  string    `json:"metric,omitempty"`
	Mode                    string    `json:"mode,omitempty"`
	InputTokens             int64     `json:"input_tokens,omitempty"`
	OutputTokens            int64     `json:"output_tokens,omitempty"`
	TotalTokens             int64     `json:"total_tokens,omitempty"`
	VoiceDurationMillis     int64     `json:"voice_duration_millis,omitempty"`
	ConnectedDurationMillis int64     `json:"connected_duration_millis,omitempty"`
	ConnectedDurationKnown  bool      `json:"connected_duration_known,omitempty"`
	Final                   bool      `json:"final"`
	Outcome                 string    `json:"outcome,omitempty"`
	CostMicros              *int64    `json:"cost_micros,omitempty"`
	Currency                string    `json:"currency,omitempty"`
	CostUnknown             bool      `json:"cost_unknown"`
}

func (u UsageEvent) Validate() error {
	if u.Version != Version || !identifier(u.EventID) || u.Tenant.Validate() != nil || u.Resource.Validate() != nil || !identifier(u.SessionID) || !identifier(u.CallID) || !identifier(u.ExecutionID) || u.Sequence == 0 || u.OccurredAt.IsZero() {
		return errors.New("invalid usage event")
	}
	if u.ConnectionID != "" && !identifier(u.ConnectionID) {
		return errors.New("invalid connection")
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 || u.VoiceDurationMillis < 0 || u.ConnectedDurationMillis < 0 {
		return errors.New("negative usage")
	}
	if u.CostMicros != nil && (*u.CostMicros < 0 || u.CostUnknown || len(u.Currency) != 3) {
		return errors.New("invalid known cost")
	}
	if u.CostMicros == nil && !u.CostUnknown {
		return errors.New("cost completeness is required")
	}
	switch u.Kind {
	case "usage":
		if u.ConnectedDurationKnown || u.ConnectedDurationMillis != 0 {
			return errors.New("model usage cannot assert connected wall time")
		}
		if !identifier(u.Provider) || !identifier(u.Model) || !identifier(u.Metric) || (u.Mode != "delta" && u.Mode != "cumulative") {
			return errors.New("invalid usage metric")
		}
	case "started", "connected", "ended":
		if u.Mode != "" || u.Metric != "" || u.InputTokens != 0 || u.OutputTokens != 0 || u.TotalTokens != 0 || u.VoiceDurationMillis != 0 {
			return errors.New("lifecycle cannot report model consumption")
		}
	default:
		return errors.New("invalid usage kind")
	}
	return nil
}

func identifier(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-/", r)) {
			return false
		}
	}
	return true
}
func text(s string, limit int) bool {
	return s != "" && len(s) <= limit && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func digest(s string) bool {
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
func validateCapabilities(c []string) error {
	if len(c) == 0 || len(c) > 128 {
		return errors.New("capabilities required")
	}
	seen := map[string]bool{}
	for _, v := range c {
		if !identifier(v) || seen[v] {
			return fmt.Errorf("invalid capability %q", v)
		}
		seen[v] = true
	}
	return nil
}
func contains(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

func validRedirect(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}
