package voicecontract

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func v2TestContext() Context {
	return Context{
		ExecutionID: "execution_2", OrganizationID: "org_1", ProjectID: "project_1", EnvironmentID: "env_1", NetworkID: "network_1",
		CallID: "call_2", SessionID: "session_2", ActorID: "actor_1", ActorKind: "user", AgentID: "assistant_1", AgentRevision: "revision_2",
		ManifestDigest: "sha256:" + strings.Repeat("0", 64), Generation: 1,
		TransportCallID: "transport_1", ParentCallID: "call_2", HopID: "hop_1", HopCount: 1,
		Scope: Scope{Kind: "agent", LineID: "line_1"},
	}
}

func TestV2AgentContextAndTargetFence(t *testing.T) {
	c := v2TestContext()
	if err := c.ValidateForVersion(Version); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateForVersion(LegacyVersion); err == nil {
		t.Fatal("accepted v2 context on legacy wire")
	}
	for _, target := range []VoiceTarget{
		{Kind: "assistant", Class: "assistant", DestinationID: "assistant_1", PublicLabel: "Parker"},
		{Kind: "line", Class: "line", DestinationID: "line_2", PublicLabel: "Desk"},
		{Kind: "pstn", Class: "pstn", PhoneNumber: "+14155552671"},
	} {
		if err := target.Validate(); err != nil {
			t.Fatalf("target %+v: %v", target, err)
		}
	}
	for _, target := range []VoiceTarget{
		{Kind: "assistant", Class: "assistant", DestinationID: "a", PhoneNumber: "+14155552671"},
		{Kind: "pstn", Class: "pstn", PhoneNumber: "+01234567"},
		{Kind: "line", Class: "extension", DestinationID: "line_2"},
	} {
		if target.Validate() == nil {
			t.Fatalf("accepted invalid target %+v", target)
		}
	}
}

func TestV2OneOfInputIsClosedAndExclusive(t *testing.T) {
	schema := []byte(`{"oneOf":[{"type":"object","properties":{"destination_id":{"type":"string","maxLength":128,"minLength":1}},"required":["destination_id"],"additionalProperties":false},{"type":"object","properties":{"phone_number":{"type":"string","maxLength":16,"minLength":8}},"required":["phone_number"],"additionalProperties":false}]}`)
	canonicalSchema, err := CanonicalJSON(schema, MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: Version, Revision: "revision_2", CompiledRevision: "revision_2", Tools: []Tool{{ID: "dial-contact", Name: "dial_contact", Description: "Dial", InputSchema: canonicalSchema, SchemaDigest: Digest(canonicalSchema), DestinationClasses: []string{"assistant", "pstn"}, TargetKinds: []string{"assistant", "pstn"}, InputModes: []string{"destination_id", "phone_number"}}}}
	if _, _, err := FreezeManifest(m); err != nil {
		t.Fatal(err)
	}
	tool := m.Tools[0]
	for _, raw := range []string{`{"destination_id":"assistant_1"}`, `{"phone_number":"+14155552671"}`} {
		if _, err := ValidateToolInput(tool, []byte(raw)); err != nil {
			t.Fatalf("accepted variant input %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"destination_id":"a","phone_number":"+14155552671"}`, `{"destination_id":"a","extra":"x"}`, `{}`} {
		if _, err := ValidateToolInput(tool, []byte(raw)); err == nil {
			t.Fatalf("accepted invalid oneOf input %s", raw)
		}
	}
}

func TestGoldenManifest(t *testing.T) {
	raw, e := os.ReadFile("testdata/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = Decode(raw, MaxManifestBytes, &m); e != nil {
		t.Fatal(e)
	}
	c, d, e := FreezeManifest(m)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(c, bytes.TrimSpace(raw)) {
		t.Fatal("noncanonical golden")
	}
	var g GrantClaims
	raw, e = os.ReadFile("testdata/grant.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = Decode(raw, MaxManifestBytes, &g); e != nil {
		t.Fatal(e)
	}
	if g.Context.ManifestDigest != d {
		t.Fatal("registry digest mismatch")
	}
}
func TestCanonicalRejects(t *testing.T) {
	for _, s := range []string{`{"x":1,"x":2}`, `{"x":1.0}`, `{"x":1} {}`, `{"x":1e2}`, string([]byte{'"', 255, '"'})} {
		if _, e := CanonicalJSON([]byte(s), 100); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestGoldenCommands(t *testing.T) {
	raw, e := os.ReadFile("testdata/command.json")
	if e != nil {
		t.Fatal(e)
	}
	var c Command
	if e = Decode(raw, MaxManifestBytes, &c); e != nil {
		t.Fatal(e)
	}
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.Arguments = []byte(`{"destination_id":"other"}`)
	if c.Validate() == nil {
		t.Fatal("accepted changed replay input")
	}
	c.Context.Scope.DIDID = "did_1"
	if c.Context.Validate() == nil {
		t.Fatal("accepted mixed scope")
	}
}
func TestScopeTransition(t *testing.T) {
	raw, e := os.ReadFile("testdata/scope-transition.json")
	if e != nil {
		t.Fatal(e)
	}
	var s ScopeTransition
	if e = Decode(raw, MaxManifestBytes, &s); e != nil {
		t.Fatal(e)
	}
	if e = s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.NextGeneration = 1
	if s.Validate() == nil {
		t.Fatal("accepted stale generation")
	}
}
func TestSchemaSubstitution(t *testing.T) {
	raw, e := os.ReadFile("testdata/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = Decode(raw, MaxManifestBytes, &m); e != nil {
		t.Fatal(e)
	}
	m.Tools[0].InputSchema = []byte(`{"$ref":"https://evil.invalid"}`)
	c, _ := CanonicalJSON(m.Tools[0].InputSchema, MaxSchemaBytes)
	m.Tools[0].SchemaDigest = Digest(c)
	if _, _, e = FreezeManifest(m); e == nil {
		t.Fatal("accepted ref")
	}
}
func TestSurrogateAliases(t *testing.T) {
	for _, s := range []string{`"\ud800"`, `"\udfff"`, `"\ud800x"`} {
		if _, e := CanonicalJSON([]byte(s), 100); e == nil {
			t.Fatal("accepted surrogate", s)
		}
	}
	if _, e := CanonicalJSON([]byte(`"\ud83d\ude00"`), 100); e != nil {
		t.Fatal(e)
	}
}
func TestFreezeDoesNotMutate(t *testing.T) {
	raw, e := os.ReadFile("testdata/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = Decode(raw, MaxManifestBytes, &m); e != nil {
		t.Fatal(e)
	}
	original := append([]byte(nil), m.Tools[0].InputSchema...)
	if _, _, e = FreezeManifest(m); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(original, m.Tools[0].InputSchema) {
		t.Fatal("mutated registry")
	}
}
func TestAllGoldenContracts(t *testing.T) {
	cases := []struct {
		name string
		v    interface{ Validate() error }
	}{
		{"grant", &GrantClaims{}}, {"event-accepted", &Operation{}}, {"event-ringing", &Operation{}}, {"event-answered", &Operation{}}, {"terminal", &TerminalResult{}}, {"assistant-envelope", &Envelope{}}, {"screener-envelope", &Envelope{}}, {"softphone-event", &SoftphoneEvent{}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, e := os.ReadFile("testdata/" + tt.name + ".json")
			if e != nil {
				t.Fatal(e)
			}
			if e = Decode(raw, MaxManifestBytes, tt.v); e != nil {
				t.Fatal(e)
			}
			if e = tt.v.Validate(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestSchemaKeywordMismatch(t *testing.T) {
	raw, e := os.ReadFile("testdata/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = Decode(raw, MaxManifestBytes, &m); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{`{"type":"object","properties":{},"required":[],"additionalProperties":false,"maxLength":3}`, `{"type":"string","maxLength":128}`} {
		m.Tools[0].InputSchema = []byte(s)
		c, _ := CanonicalJSON([]byte(s), MaxSchemaBytes)
		m.Tools[0].SchemaDigest = Digest(c)
		if _, _, e = FreezeManifest(m); e == nil {
			t.Fatal("accepted invalid schema", s)
		}
	}
}
