package temporalruntime

import (
	"context"
	"encoding/json"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"os"
	"testing"
)

func TestRemoteReadRegistryNormalResultAndLegacyExclusion(t *testing.T) {
	raw, _ := os.ReadFile("../voicecontract/testdata/manifest-with-read.json")
	var m voicecontract.Manifest
	if e := voicecontract.Decode(raw, 32768, &m); e != nil {
		t.Fatal(e)
	}
	var spec voicecontract.Tool
	for _, tool := range m.Tools {
		if tool.IsRead() {
			spec = tool
		}
	}
	raw, _ = os.ReadFile("../voicecontract/testdata/remote-read.json")
	var r voicecontract.ReadRequest
	_ = voicecontract.Decode(raw, 16384, &r)
	var input, output any
	_ = json.Unmarshal(spec.InputSchema, &input)
	_ = json.Unmarshal(spec.OutputSchema, &output)
	calls := 0
	badResult := false
	tool := agents.DefineTool(agents.ToolConfig{Name: spec.Name, Description: spec.Description, InputSchema: input, OutputSchema: output, VoiceRemoteRead: &agents.VoiceReadPolicy{MaxResultBytes: 4096}}, func(_ context.Context, actor agents.Actor, _ map[string]any) (any, error) {
		calls++
		if actor.Metadata["line_id"] != r.Context.Scope.LineID || actor.Metadata["operation_id"] != "" || actor.Metadata["announcement_barrier_id"] != "" {
			t.Error("read scope or control metadata incorrect")
		}
		if badResult {
			return map[string]any{"terminal": true}, nil
		}
		return map[string]any{"results": []any{map[string]any{"destination_id": "opaque", "label": "Front desk"}}}, nil
	})
	d := agents.DefineAI(agents.AIConfig{Revision: r.Context.AgentRevision, Tools: map[string]agents.AITool{spec.ID: tool}})
	_, manifest, digest, e := d.CompileVoiceManifest()
	if e != nil {
		t.Fatal(e)
	}
	r.Context.ManifestDigest = digest
	reg := NewVoiceRegistry()
	reg.definitions[r.Context.AgentID] = d
	reg.manifests = map[string][]byte{r.Context.AgentID: manifest}
	reg.manifestDigests = map[string]string{r.Context.AgentID: digest}
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(old)
	req := VoiceSessionExecuteToolInput{Grant: "opaque-current", RemoteRead: &r}
	result, e := VoiceSessionExecuteToolActivity(context.Background(), req)
	if e != nil || len(result.Result) == 0 || result.Operation != nil || result.Terminal != nil || calls != 1 {
		t.Fatalf("read failed %+v %v", result, e)
	}
	badResult = true
	result, e = VoiceSessionExecuteToolActivity(context.Background(), req)
	if e != nil || result.Error == "" || len(result.Result) > 0 {
		t.Fatalf("unsafe output returned %+v %v", result, e)
	}
	_, e = VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{AgentID: r.Context.AgentID, ToolName: spec.Name})
	if e == nil || calls != 2 {
		t.Fatal("legacy gained remote read")
	}
	r.Context.ManifestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, e = VoiceSessionExecuteToolActivity(context.Background(), req); e == nil || calls != 2 {
		t.Fatal("wrong manifest executed")
	}
}
