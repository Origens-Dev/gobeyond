package voicecontract

import (
	"os"
	"testing"
)

func TestReadFixtureBoundedResult(t *testing.T) {
	raw, e := os.ReadFile("testdata/manifest-with-read.json")
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = Decode(raw, MaxManifestBytes, &m); e != nil {
		t.Fatal(e)
	}
	_, digest, e := FreezeManifest(m)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile("testdata/remote-read.json")
	var request ReadRequest
	if e = Decode(raw, 16384, &request); e != nil || request.Validate() != nil || request.Context.ManifestDigest != digest {
		t.Fatalf("read fixture %v", e)
	}
	var spec Tool
	for _, tool := range m.Tools {
		if tool.IsRead() {
			spec = tool
		}
	}
	if _, e = ValidateToolInput(spec, request.Arguments); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile("testdata/read-result.json")
	if _, e = ValidateToolOutput(spec, raw); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{`{"results":null}`, `{"results":[],"terminal":true}`, `{"results":[{"destination_id":"x","label":"a","line_id":"secret"}]}`} {
		if _, e = ValidateToolOutput(spec, []byte(bad)); e == nil {
			t.Fatalf("unsafe read result accepted %s", bad)
		}
	}
	spec.TerminalOnSuccess = true
	m.Tools = []Tool{spec}
	if _, _, e = FreezeManifest(m); e == nil {
		t.Fatal("read terminal policy accepted")
	}
}
