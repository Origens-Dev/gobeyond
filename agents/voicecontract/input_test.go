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
