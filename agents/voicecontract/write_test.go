package voicecontract

import (
	"bytes"
	"os"
	"testing"
)

func writeFixture() Manifest {
	in := []byte(`{"additionalProperties":false,"properties":{"confirm":{"type":"boolean"},"name":{"maxLength":120,"minLength":1,"type":"string"}},"required":["confirm","name"],"type":"object"}`)
	out := []byte(`{"additionalProperties":false,"properties":{"saved":{"type":"boolean"}},"required":["saved"],"type":"object"}`)
	in, _ = CanonicalJSON(in, MaxSchemaBytes)
	out, _ = CanonicalJSON(out, MaxSchemaBytes)
	return Manifest{Version: Version, Revision: "write-1", CompiledRevision: "write-1", Tools: []Tool{{
		ID: "rename-network", Name: "rename_network", Description: "Rename a network after confirmation.",
		ExecutionKind: "write", InputSchema: in, SchemaDigest: Digest(in), OutputSchema: out, OutputSchemaDigest: Digest(out),
		MaxResultBytes: 4096, DestinationClasses: []string{},
	}}}
}

func TestWriteGoldenManifest(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest-with-write.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden Manifest
	if err = Decode(raw, MaxManifestBytes, &golden); err != nil {
		t.Fatal(err)
	}
	c, digest, err := FreezeManifest(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c, bytes.TrimSpace(raw)) {
		t.Fatalf("noncanonical write golden\n%s", c)
	}
	m := writeFixture()
	got, want, err := FreezeManifest(m)
	if err != nil || digest != want || !bytes.Equal(c, got) {
		t.Fatalf("write fixture digest drifted %s %s %v", digest, want, err)
	}
	if !m.Tools[0].IsWrite() || m.Tools[0].IsRead() || m.Tools[0].IsAction() {
		t.Fatal("write kind aliases")
	}
}

func TestWriteBooleanInputAndOutput(t *testing.T) {
	m := writeFixture()
	spec := m.Tools[0]
	for _, raw := range []string{`{"confirm":true,"name":"desk"}`, `{"confirm":false,"name":"desk"}`} {
		if _, err := ValidateToolInput(spec, []byte(raw)); err != nil {
			t.Fatalf("boolean input %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"confirm":"true","name":"desk"}`, `{"confirm":1,"name":"desk"}`, `{"confirm":null,"name":"desk"}`, `{"name":"desk"}`} {
		if _, err := ValidateToolInput(spec, []byte(raw)); err == nil {
			t.Fatalf("accepted invalid boolean input %s", raw)
		}
	}
	if _, err := ValidateToolOutput(spec, []byte(`{"saved":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateToolOutput(spec, []byte(`{"saved":false}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/write-result.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateToolOutput(spec, raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"saved":"true"}`, `{"saved":1}`, `{"saved":null}`, `{"ok":true}`, `{"saved":true,"extra":true}`} {
		if _, err = ValidateToolOutput(spec, []byte(bad)); err == nil {
			t.Fatalf("unsafe write result accepted %s", bad)
		}
	}
}

func TestWriteFreezeRejectsConflictingKinds(t *testing.T) {
	if _, _, err := FreezeManifest(writeFixture()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Tool){
		"approval":          func(t *Tool) { t.RequiresApproval = true },
		"destination":       func(t *Tool) { t.DestinationClasses = []string{"extension"} },
		"target kind":       func(t *Tool) { t.TargetKinds = []string{"line"} },
		"input mode":        func(t *Tool) { t.InputModes = []string{"destination_id"} },
		"handoff":           func(t *Tool) { t.HandoffMode = "blind" },
		"terminal behavior": func(t *Tool) { t.TerminalBehavior = "terminal" },
		"terminal":          func(t *Tool) { t.TerminalOnSuccess = true },
		"zero result":       func(t *Tool) { t.MaxResultBytes = 0 },
		"oversize result":   func(t *Tool) { t.MaxResultBytes = 4097 },
		"missing output":    func(t *Tool) { t.OutputSchema = nil; t.OutputSchemaDigest = "" },
		"control mix":       func(t *Tool) { t.ExecutionKind = "call_control" },
		"action mix":        func(t *Tool) { t.ExecutionKind = "action"; t.RequiresApproval = true },
		"playback mix":      func(t *Tool) { t.Playback = &PlaybackPolicy{CompletionToolID: "complete"} },
		"unknown kind":      func(t *Tool) { t.ExecutionKind = "write_action" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := writeFixture()
			mutate(&changed.Tools[0])
			if _, _, err := FreezeManifest(changed); err == nil {
				t.Fatal("accepted conflicting write policy")
			}
		})
	}
	read := writeFixture()
	read.Tools[0].ExecutionKind = "read"
	if _, _, err := FreezeManifest(read); err != nil {
		t.Fatal(err)
	}
	if read.Tools[0].IsWrite() || !read.Tools[0].IsRead() {
		t.Fatal("write/read kind alias")
	}
}
