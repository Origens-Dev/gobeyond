package connections

import (
	"strings"
	"testing"
	"time"
)

func TestGrantCallerAssignmentAndCapabilityChecks(t *testing.T) {
	p, policy, _, _ := fixture(t)
	g := policy.grant
	caller := g.Callers[0]
	if !g.Allows(caller, "configure", p.clock()) {
		t.Fatal("approved line denied")
	}
	caller.AssignmentGeneration++
	if g.Allows(caller, "read", p.clock()) {
		t.Fatal("reassigned line inherited access")
	}
	if g.Allows(g.Callers[0], "unapproved", p.clock()) {
		t.Fatal("unapproved capability allowed")
	}
	g.RevokedAt = p.clock()
	if g.Allows(g.Callers[0], "read", p.clock()) {
		t.Fatal("revoked grant allowed")
	}
	g.RevokedAt = time.Time{}
	if g.Allows(g.Callers[0], "read", g.ExpiresAt) {
		t.Fatal("expired grant allowed")
	}
}

func TestAdmissionRequiresFreshTargetBoundIdentity(t *testing.T) {
	p, policy, _, _ := fixture(t)
	g := policy.grant
	a := Admission{Version: Version, ConnectionID: "link-1", GrantID: g.ID, GrantVersion: g.Version, Source: g.Consumer, Target: g.Provider, Caller: g.Callers[0], Resource: g.Resource, AgentID: g.AgentID, SessionID: "session-1", CallID: "call-1", ExecutionID: "execution-1", ConfigRevision: 1, AgentRevision: "revision-1", ManifestDigest: "sha256:" + strings.Repeat("a", 64), Capabilities: []string{"read"}, BillingPolicy: ProviderFunded, IssuedAt: p.clock(), ExpiresAt: p.clock().Add(time.Minute)}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Admission){
		func(a *Admission) { a.Target.EnvironmentID = "" },
		func(a *Admission) { a.ManifestDigest = "unverified" },
		func(a *Admission) { a.ExpiresAt = a.IssuedAt.Add(6 * time.Minute) },
		func(a *Admission) { a.BillingPolicy = "platform_support" },
		func(a *Admission) { a.GrantVersion = 0 },
	} {
		bad := a
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid admission accepted")
		}
	}
}

func TestUsageRequiresExplicitUnitsAndCostCompleteness(t *testing.T) {
	p, policy, _, _ := fixture(t)
	u := UsageEvent{Version: Version, EventID: "usage-1", Tenant: policy.grant.Provider, Resource: policy.grant.Resource, SessionID: "session-1", CallID: "call-1", ExecutionID: "execution-1", Kind: "usage", Sequence: 1, OccurredAt: p.clock(), Provider: "gemini", Model: "gemini-live", Metric: "live_voice", Mode: "cumulative", VoiceDurationMillis: 1000, CostUnknown: true}
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*UsageEvent){
		func(u *UsageEvent) { u.Mode = "seconds" },
		func(u *UsageEvent) { u.VoiceDurationMillis = -1 },
		func(u *UsageEvent) { u.CostUnknown = false },
		func(u *UsageEvent) { zero := int64(0); u.CostMicros = &zero; u.Currency = "USD" },
		func(u *UsageEvent) { u.Kind = "ended" },
	} {
		bad := u
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("ambiguous usage accepted")
		}
	}
}

func TestExperienceDoesNotAcceptDuplicateTools(t *testing.T) {
	e := Experience{Revision: 1, Name: "Cedar House", Provider: "gemini", Model: "gemini-live", Voice: "Kore", EnabledTools: []string{"inventory"}}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.EnabledTools = []string{"inventory", "inventory"}
	if e.Validate() == nil {
		t.Fatal("duplicate tool configuration accepted")
	}
}

func TestLocalAdmissionHasNoOAuthSelfLinkAndCannotCrossTenants(t *testing.T) {
	p, policy, _, _ := fixture(t)
	a := Admission{Version: Version, Source: policy.grant.Provider, Target: policy.grant.Provider, Caller: Caller{Kind: "user", ID: "owner-1"}, Resource: policy.grant.Resource, AgentID: policy.grant.AgentID, SessionID: "session-1", CallID: "call-1", ExecutionID: "execution-1", ConfigRevision: 1, AgentRevision: "revision-1", ManifestDigest: "sha256:" + strings.Repeat("a", 64), Capabilities: []string{"read"}, BillingPolicy: ProviderFunded, IssuedAt: p.clock(), ExpiresAt: p.clock().Add(time.Minute)}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Admission){
		func(a *Admission) { a.Source.EnvironmentID = "preview" },
		func(a *Admission) { a.Source.ProjectID = "other-app" },
		func(a *Admission) { a.ConnectionID = "fake-self-link" },
		func(a *Admission) { a.GrantID = "fake-grant" },
		func(a *Admission) { a.GrantVersion = 1 },
		func(a *Admission) { a.Caller = policy.grant.Callers[0] },
	} {
		bad := a
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid local authority combination accepted")
		}
	}
}
