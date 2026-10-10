package temporalruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func voiceWriteClosedSchemas() (input any, output any) {
	input = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string", "maxLength": 20}}, "required": []string{"q"}, "additionalProperties": false}
	output = map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false}
	return input, output
}

func voiceWriteTestBinding() agents.ResourceBinding {
	return agents.ResourceBinding{Kind: "agent", ResourceID: "line-1"}
}

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
	resetVoiceWriteLedger()
	reg := NewVoiceRegistry()
	schema, output := voiceWriteClosedSchemas()
	var gotWriteID string
	var gotBinding agents.ResourceBinding
	var gotBindingOK bool
	var gotSessionID string
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(ctx context.Context, actor agents.Actor, input map[string]any) (any, error) {
			gotWriteID, _ = agents.ToolWriteID(ctx)
			gotSessionID, _ = agents.ToolSessionID(ctx)
			gotBinding, gotBindingOK = agents.ToolResourceBinding(ctx)
			if _, mailbox := actor.Metadata["mailbox_line_id"]; mailbox {
				t.Error("mailbox product field leaked onto actor metadata")
			}
			if _, session := actor.Metadata["session_id"]; session {
				t.Error("session identity leaked onto actor metadata")
			}
			return map[string]any{"ok": true}, nil
		}),
	}})
	manifest, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil || len(manifest.Tools) != 1 || !manifest.Tools[0].IsWrite() || manifest.Tools[0].RequiresApproval {
		t.Fatalf("compile write manifest: %#v %v", manifest, err)
	}
	reg.mu.Lock()
	reg.definitions["call-operator"] = definition
	reg.manifests = map[string][]byte{"call-operator": rawManifest}
	reg.manifestDigests = map[string]string{"call-operator": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(nil) })

	input, _ := json.Marshal(map[string]any{"q": "hello"})
	req := VoiceSessionExecuteToolInput{
		AgentID: "call-operator", ToolName: "lookup", ToolCallID: "call_1", Input: input,
		ActorID: "user-1", ActorKind: "user", AllowedToolIDs: []string{"lookup"}, ManifestDigest: manifestDigest, AgentRevision: "revision-1",
		SessionID: "session-1", CallID: "call-1", ResourceBinding: voiceWriteTestBinding(),
	}
	out, err := VoiceSessionExecuteToolActivity(context.Background(), req)
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
	if gotWriteID == "" {
		t.Fatal("handler missing platform write id")
	}
	if gotSessionID != "session-1" {
		t.Fatalf("session id %q", gotSessionID)
	}
	if !gotBindingOK || !gotBinding.Equal(voiceWriteTestBinding()) {
		t.Fatalf("resource binding %#v ok=%v", gotBinding, gotBindingOK)
	}
	if _, key := voiceWriteTestKey(t, req); gotWriteID != key {
		t.Fatalf("write id %s want %s", gotWriteID, key)
	}
}

func TestVoiceSessionToolRequiresServerConfirmedApproval(t *testing.T) {
	resetVoiceWriteLedger()
	calls := 0
	reg := NewVoiceRegistry()
	schema := map[string]any{"type": "object", "properties": map[string]any{"networkId": map[string]any{"type": "string", "maxLength": 64}, "name": map[string]any{"type": "string", "maxLength": 64}}, "required": []string{"networkId", "name"}, "additionalProperties": false}
	output := map[string]any{"type": "object", "properties": map[string]any{"renamed": map[string]any{"type": "boolean"}}, "required": []string{"renamed"}, "additionalProperties": false}
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"rename_network": agents.DefineTool(agents.ToolConfig{Name: "rename_network", Description: "Rename", InputSchema: schema, OutputSchema: output, VoiceWrite: true, RequiresApproval: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
			calls++
			return map[string]any{"renamed": true}, nil
		}),
	}})
	manifest, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil || len(manifest.Tools) != 1 || !manifest.Tools[0].RequiresApproval {
		t.Fatalf("compile write manifest: %#v %v", manifest, err)
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
	req := VoiceSessionExecuteToolInput{AgentID: "support", ToolName: "rename_network", ToolCallID: "call-1", Input: input, ActorID: "user-1", ActorKind: "user", NetworkID: "net-1", AllowedToolIDs: []string{"rename_network"}, ManifestDigest: manifestDigest, AgentRevision: "revision-1", SessionID: "session-1", CallID: "call-1", ResourceBinding: voiceWriteTestBinding()}
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

func TestVoiceWriteActivityCommitThenLookupIsOneMutation(t *testing.T) {
	resetVoiceWriteLedger()
	calls := 0
	reg := NewVoiceRegistry()
	schema, output := voiceWriteClosedSchemas()
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
			calls++
			return map[string]any{"ok": true}, nil
		}),
	}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(nil) })
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	req := VoiceSessionExecuteToolInput{AgentID: "support", ToolName: "lookup", ToolCallID: "call-1", Input: input, ActorID: "user-1", ActorKind: "user", AllowedToolIDs: []string{"lookup"}, ManifestDigest: manifestDigest, AgentRevision: "revision-1", SessionID: "session-write", CallID: "call-write", ResourceBinding: voiceWriteTestBinding()}
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("first=%#v err=%v calls=%d", first, err, calls)
	}
	second, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || second.Error != "" || calls != 1 || string(second.Result) != string(first.Result) {
		t.Fatalf("lookup retry mutated first=%s second=%s err=%v calls=%d", first.Result, second.Result, err, calls)
	}
	changed := req
	changed.Input, _ = json.Marshal(map[string]any{"q": "other"})
	if _, err = VoiceSessionExecuteToolActivity(context.Background(), changed); err == nil {
		t.Fatal("tool_call_id reuse with changed input accepted")
	}
	if calls != 1 {
		t.Fatalf("conflict reused handler calls=%d", calls)
	}
	forged := req
	forged.IdempotencyKey = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err = VoiceSessionExecuteToolActivity(context.Background(), forged); !errors.Is(err, errWriteForgedKey) {
		t.Fatalf("forged key err=%v", err)
	}
}

func TestVoiceWriteActivityCoalescesInFlightAndUnknownDoesNotRetry(t *testing.T) {
	resetVoiceWriteLedger()
	var mu sync.Mutex
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	reg := NewVoiceRegistry()
	schema, output := voiceWriteClosedSchemas()
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			close(started)
			<-release
			return map[string]any{"ok": true}, nil
		}),
	}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(nil) })
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	req := VoiceSessionExecuteToolInput{AgentID: "support", ToolName: "lookup", ToolCallID: "inflight-1", Input: input, ActorID: "user-1", ActorKind: "user", AllowedToolIDs: []string{"lookup"}, ManifestDigest: manifestDigest, AgentRevision: "revision-1", SessionID: "session-write", CallID: "call-write", ResourceBinding: voiceWriteTestBinding()}
	done := make(chan VoiceSessionExecuteToolResult, 1)
	go func() {
		out, activityErr := VoiceSessionExecuteToolActivity(context.Background(), req)
		if activityErr != nil {
			t.Errorf("inflight: %v", activityErr)
		}
		done <- out
	}()
	<-started
	secondDone := make(chan struct{})
	var second VoiceSessionExecuteToolResult
	go func() {
		var activityErr error
		second, activityErr = VoiceSessionExecuteToolActivity(context.Background(), req)
		if activityErr != nil {
			t.Errorf("coalesce: %v", activityErr)
		}
		close(secondDone)
	}()
	close(release)
	first := <-done
	<-secondDone
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 || first.Error != "" || second.Error != "" || string(first.Result) != string(second.Result) {
		t.Fatalf("inflight coalesce calls=%d first=%#v second=%#v", gotCalls, first, second)
	}

	resetVoiceWriteLedger()
	canonical, err := voicecontract.CanonicalJSON(input, voicecontract.MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	req.Input = canonical
	digest := voicecontract.Digest(canonical)
	key, err := deriveVoiceWriteKey(req.SessionID, req.CallID, req.ToolName, req.ToolCallID, digest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := voiceWriteReplayDigest(req, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedVoiceWriteUnknown(voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID), key, replay); err != nil {
		t.Fatal(err)
	}
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("unknown outcome err=%v", err)
	}
	mu.Lock()
	gotCalls = calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("unknown retry calls=%d", gotCalls)
	}
}

func voiceWriteTestKey(t *testing.T, req VoiceSessionExecuteToolInput) (digest, key string) {
	t.Helper()
	canonical, err := voicecontract.CanonicalJSON(req.Input, voicecontract.MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	digest = voicecontract.Digest(canonical)
	key, err = deriveVoiceWriteKey(req.SessionID, req.CallID, req.ToolName, req.ToolCallID, digest)
	if err != nil {
		t.Fatal(err)
	}
	return digest, key
}
