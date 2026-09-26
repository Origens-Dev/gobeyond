package temporalruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
)

func TestVoiceSessionExecuteToolActivityRegistryMiss(t *testing.T) {
	RetainVoiceRegistry(nil)
	out, err := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{
		AgentID: "call-operator", ToolName: "lookup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Error == "" {
		t.Fatalf("expected registry error, got %+v", out)
	}
}

func TestVoiceSessionExecuteToolActivityRunsTool(t *testing.T) {
	reg := NewVoiceRegistry()
	schema := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string", "maxLength": 20}}, "required": []string{"q"}, "additionalProperties": false}
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, VoiceAction: true, RequiresApproval: true}, func(ctx context.Context, actor agents.Actor, input map[string]any) (any, error) {
			return map[string]any{"ok": true, "q": input["q"]}, nil
		}),
	}})
	manifest, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil || len(manifest.Tools) != 1 {
		t.Fatalf("compile action manifest: %#v %v", manifest, err)
	}
	reg.mu.Lock()
	reg.definitions["call-operator"] = definition
	reg.manifests = map[string][]byte{"call-operator": rawManifest}
	reg.manifestDigests = map[string]string{"call-operator": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(nil) })

	input, _ := json.Marshal(map[string]any{"q": "hello"})
	out, err := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{
		AgentID: "call-operator", ToolName: "lookup", ToolCallID: "call_1", Input: input,
		ActorID: "user-1", ActorKind: "user", AllowedToolIDs: []string{"lookup"}, ManifestDigest: manifestDigest, AgentRevision: "revision-1", ApprovalConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Result, &got); err != nil {
		t.Fatalf("result=%#v err=%v", out, err)
	}
	if got["ok"] != true {
		t.Fatalf("%v", got)
	}
}

func TestVoiceSessionToolRequiresServerConfirmedApproval(t *testing.T) {
	calls := 0
	reg := NewVoiceRegistry()
	schema := map[string]any{"type": "object", "properties": map[string]any{"networkId": map[string]any{"type": "string", "maxLength": 64}, "name": map[string]any{"type": "string", "maxLength": 64}}, "required": []string{"networkId", "name"}, "additionalProperties": false}
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"rename_network": agents.DefineTool(agents.ToolConfig{Name: "rename_network", Description: "Rename", InputSchema: schema, VoiceAction: true, RequiresApproval: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
			calls++
			return map[string]any{"renamed": true}, nil
		}),
	}})
	manifest, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil || len(manifest.Tools) != 1 {
		t.Fatalf("compile action manifest: %#v %v", manifest, err)
	}
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(old)
	input, _ := json.Marshal(map[string]any{"networkId": "net-1", "name": "New"})
	req := VoiceSessionExecuteToolInput{AgentID: "support", ToolName: "rename_network", ToolCallID: "call-1", Input: input, ActorID: "user-1", ActorKind: "user", NetworkID: "net-1", AllowedToolIDs: []string{"rename_network"}, ManifestDigest: manifestDigest, AgentRevision: "revision-1"}
	result, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || result.Approval == nil || result.Approval.InteractionID == "" || result.Approval.InputHash == "" || result.Approval.ToolCallID != "call-1" || calls != 0 {
		t.Fatalf("approval=%#v err=%v calls=%d", result.Approval, err, calls)
	}
	if _, err := hex.DecodeString(result.Approval.InputHash); err != nil {
		t.Fatalf("input hash is not hex SHA-256: %v", err)
	}
	req.ApprovalConfirmed = true
	result, err = VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || result.Approval != nil || result.Error != "" || calls != 1 {
		t.Fatalf("approved result=%#v err=%v calls=%d", result, err, calls)
	}
}
