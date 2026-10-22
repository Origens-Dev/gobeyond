package voicecontract

import (
	"strings"
	"testing"
)

func TestResourceAgentScopeKeepsHomeSeparateFromNetworkAndSupport(t *testing.T) {
	c := Context{OrganizationID: "org", ProjectID: "project", EnvironmentID: "env", CallID: "call_home", SessionID: "sph_home", ExecutionID: "execution_home", ActorID: "owner", ActorKind: "user", AgentID: "home-agent", AgentRevision: "rev", ManifestDigest: "sha256:" + strings.Repeat("a", 64), Generation: 1, TransportCallID: "call_home", ParentCallID: "call_home", HopID: "hop_home", HopCount: 1, Scope: Scope{Kind: "resource_agent", ResourceType: "home", ResourceID: "cedar", ConfigRevision: 1}}
	if err := c.ValidateForVersion(Version); err != nil {
		t.Fatal(err)
	}
	if c.ValidateForVersion(LegacyVersion) == nil {
		t.Fatal("resource scope admitted on legacy wire")
	}
	for _, change := range []func(*Context){func(c *Context) { c.NetworkID = "fake_network" }, func(c *Context) { c.Scope.LineID = "fake_line" }, func(c *Context) { c.Scope.ResourceID = "" }, func(c *Context) { c.Scope.ConfigRevision = 0 }, func(c *Context) { c.ActorKind = "external_call" }, func(c *Context) { c.Scope.Kind = "platform_support" }, func(c *Context) { c.Scope.Kind = "agent"; c.Scope.LineID = "line"; c.NetworkID = "network" }} {
		other := c
		change(&other)
		if other.Validate() == nil {
			t.Fatalf("unfenced resource scope: %+v", other)
		}
	}
}
