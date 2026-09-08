package voicecontract

import (
	"bytes"
	"os"
	"testing"
)

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
