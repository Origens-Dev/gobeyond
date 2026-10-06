package voicecontract

import (
	"os"
	"testing"
)

func TestValidateDeployedToolInput(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err = Decode(raw, MaxManifestBytes, &m); err != nil {
		t.Fatal(err)
	}
	tool := m.Tools[0]
	for _, raw := range []string{`{"destination_id":"opaque-1"}`, `{ "destination_id": "opaque-1" }`} {
		got, err := ValidateToolInput(tool, []byte(raw))
		if err != nil || string(got) != `{"destination_id":"opaque-1"}` {
			t.Fatalf("canonical input %q %v", got, err)
		}
	}
	for _, raw := range []string{`{}`, `{"destination_id":12}`, `{"destination_id":"a","network_id":"other"}`, `{"destination_id":"a","destination_id":"b"}`, `null`, `[]`} {
		if _, err := ValidateToolInput(tool, []byte(raw)); err == nil {
			t.Fatalf("accepted invalid input %s", raw)
		}
	}
}

func TestBooleanPropertyInput(t *testing.T) {
	schema := []byte(`{"additionalProperties":false,"properties":{"enabled":{"type":"boolean"}},"required":["enabled"],"type":"object"}`)
	canonical, err := CanonicalJSON(schema, MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	tool := Tool{ID: "set-flag", Name: "set_flag", Description: "Toggle a bounded flag.", InputSchema: canonical, SchemaDigest: Digest(canonical), DestinationClasses: []string{"extension"}}
	m := Manifest{Version: Version, Revision: "boolean-1", CompiledRevision: "boolean-1", Tools: []Tool{tool}}
	if _, _, err = FreezeManifest(m); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"enabled":true}`, `{"enabled":false}`} {
		got, err := ValidateToolInput(tool, []byte(raw))
		if err != nil {
			t.Fatalf("boolean input %s: %v", raw, err)
		}
		if string(got) != raw {
			t.Fatalf("canonical boolean %q", got)
		}
	}
	for _, raw := range []string{`{"enabled":"true"}`, `{"enabled":1}`, `{"enabled":0}`, `{"enabled":null}`, `{}`} {
		if _, err := ValidateToolInput(tool, []byte(raw)); err == nil {
			t.Fatalf("accepted invalid boolean input %s", raw)
		}
	}
}
