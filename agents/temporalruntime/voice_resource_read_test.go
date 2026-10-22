package temporalruntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func TestResourceRemoteReadCarriesVerifiedOwnerHomeAndSession(t *testing.T) {
	raw, err := os.ReadFile("../voicecontract/testdata/manifest-with-read.json")
	if err != nil {
		t.Fatal(err)
	}
	var m voicecontract.Manifest
	if voicecontract.Decode(raw, 32768, &m) != nil {
		t.Fatal("fixture manifest")
	}
	var spec voicecontract.Tool
	for _, tool := range m.Tools {
		if tool.IsRead() {
			spec = tool
		}
	}
	raw, err = os.ReadFile("../voicecontract/testdata/remote-read.json")
	if err != nil {
		t.Fatal(err)
	}
	var r voicecontract.ReadRequest
	if voicecontract.Decode(raw, 16384, &r) != nil {
		t.Fatal("fixture read")
	}
	r.Version = voicecontract.Version
	r.Context.AgentID = "home-agent"
	r.Context.NetworkID = ""
	r.Context.ActorKind = "user"
	r.Context.ActorID = "home-owner"
	r.Context.Scope = voicecontract.Scope{Kind: "resource_agent", ResourceType: "home", ResourceID: "cedar", ConfigRevision: 3}
	r.Context.TransportCallID = r.Context.CallID
	r.Context.ParentCallID = r.Context.CallID
	r.Context.HopID = "hop_home"
	r.Context.HopCount = 1
	var input, output any
	_ = json.Unmarshal(spec.InputSchema, &input)
	_ = json.Unmarshal(spec.OutputSchema, &output)
	calls := 0
	tool := agents.DefineTool(agents.ToolConfig{Name: spec.Name, Description: spec.Description, InputSchema: input, OutputSchema: output, VoiceRemoteRead: &agents.VoiceReadPolicy{MaxResultBytes: 4096}}, func(ctx context.Context, actor agents.Actor, _ map[string]any) (any, error) {
		calls++
		session, ok := agents.ToolSessionID(ctx)
		if !ok || session != r.Context.SessionID || actor.ID != "home-owner" || actor.Metadata["resource_id"] != "cedar" || actor.Metadata["resource_type"] != "home" || actor.Metadata["configuration_revision"] != "3" || actor.Metadata["network_id"] != "" || actor.Metadata["line_id"] != "" {
			t.Fatalf("resource authority lost: %+v", actor)
		}
		return map[string]any{"results": []any{}}, nil
	})
	d := agents.DefineAI(agents.AIConfig{Revision: r.Context.AgentRevision, Tools: map[string]agents.AITool{spec.ID: tool}})
	_, manifest, digest, err := d.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	r.Context.ManifestDigest = digest
	reg := NewVoiceRegistry()
	reg.definitions[r.Context.AgentID] = d
	reg.manifests = map[string][]byte{r.Context.AgentID: manifest}
	reg.manifestDigests = map[string]string{r.Context.AgentID: digest}
	previous := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(previous)
	out, err := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{Grant: "verified-resource-grant", RemoteRead: &r})
	if err != nil || len(out.Result) == 0 || calls != 1 {
		t.Fatalf("resource read denied: %+v %v calls=%d", out, err, calls)
	}
}
