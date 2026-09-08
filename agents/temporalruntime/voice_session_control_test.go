package temporalruntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func TestControlActivityRegistryAndAcknowledgement(t *testing.T) {
	raw, e := os.ReadFile("../voicecontract/testdata/command.json")
	if e != nil {
		t.Fatal(e)
	}
	var command voicecontract.Command
	if e = voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &command); e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile("../voicecontract/testdata/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture voicecontract.Manifest
	if e = voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &fixture); e != nil {
		t.Fatal(e)
	}
	var schema any
	if e = json.Unmarshal(fixture.Tools[0].InputSchema, &schema); e != nil {
		t.Fatal(e)
	}
	calls := 0
	tool := agents.DefineTool(agents.ToolConfig{Name: "dial_contact", Description: fixture.Tools[0].Description, InputSchema: schema, VoiceControl: &agents.VoiceToolPolicy{DestinationClasses: []string{"extension"}, TerminalOnSuccess: true}}, func(_ context.Context, actor agents.Actor, input map[string]any) (voicecontract.Operation, error) {
		calls++
		if actor.Metadata["voice_session_grant"] != "opaque" || actor.Metadata["execution_id"] != command.Context.ExecutionID || actor.Metadata["announcement_barrier_id"] != "1" || actor.Metadata["operation_id"] != command.OperationID {
			t.Fatal("grant-derived metadata lost")
		}
		return voicecontract.Operation{Version: "1", Context: command.Context, OperationID: command.OperationID, Sequence: 1, State: "accepted"}, nil
	})
	definition := agents.DefineAI(agents.AIConfig{Revision: command.Context.AgentRevision, Tools: map[string]ai.Tool{"dial-contact": tool}})
	_, manifest, digest, e := definition.CompileVoiceManifest()
	if e != nil {
		t.Fatal(e)
	}
	command.Context.ManifestDigest = digest
	reg := NewVoiceRegistry()
	reg.definitions[command.Context.AgentID] = definition
	reg.manifests = map[string][]byte{command.Context.AgentID: manifest}
	reg.manifestDigests = map[string]string{command.Context.AgentID: digest}
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(old)
	req := VoiceSessionExecuteToolInput{Grant: "opaque", CallControl: &command}
	out, e := VoiceSessionExecuteToolActivity(context.Background(), req)
	if e != nil || out.Operation == nil || out.Operation.State != "accepted" || calls != 1 {
		t.Fatalf("out=%+v err=%v calls=%d", out, e, calls)
	}
	changed := command
	changed.Context.ManifestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	req.CallControl = &changed
	if _, e = VoiceSessionExecuteToolActivity(context.Background(), req); e == nil {
		t.Fatal("schema substitution allowed")
	}
	changed = command
	changed.Arguments = []byte(`{"destination_id":"x","line_id":"forged"}`)
	canonical, _ := voicecontract.CanonicalJSON(changed.Arguments, 4096)
	changed.InputDigest = voicecontract.Digest(canonical)
	req.CallControl = &changed
	if _, e = VoiceSessionExecuteToolActivity(context.Background(), req); e == nil {
		t.Fatal("extra scope accepted")
	}
	if calls != 1 {
		t.Fatal("invalid request invoked tool")
	}
}
