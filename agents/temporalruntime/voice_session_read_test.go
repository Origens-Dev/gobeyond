package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
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

func TestRemoteReadWorkflowReplayAndScope(t *testing.T) {
	raw, _ := os.ReadFile("../voicecontract/testdata/remote-read.json")
	var r voicecontract.ReadRequest
	_ = voicecontract.Decode(raw, 16384, &r)
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "wrong_execution"}[wrong], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			id, _ := WorkflowID(r.Context.SessionID, r.Context.ExecutionID)
			if wrong {
				id = "gobeyond-agent-run/other/execution"
			}
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
			calls := 0
			env.RegisterActivityWithOptions(func(context.Context, VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
				calls++
				return VoiceSessionExecuteToolResult{Result: []byte(`{"results":[]}`)}, nil
			}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				state := newVoiceReadWorkflowState()
				in := VoiceSessionInput{Context: &r.Context, AgentID: r.Context.AgentID, CallID: r.Context.CallID, SessionID: r.Context.SessionID, ExecutionID: r.Context.ExecutionID}
				req := VoiceSessionExecuteToolInput{Grant: "opaque", RemoteRead: &r}
				first, e := state.execute(ctx, in, req)
				if wrong {
					if e == nil {
						return errors.New("wrong workflow accepted")
					}
					return nil
				}
				if e != nil {
					return e
				}
				second, e := state.execute(ctx, in, req)
				if e != nil || string(first.Result) != string(second.Result) {
					return errors.New("read replay changed")
				}
				changed := r
				changed.Arguments = []byte(`{"query":"other"}`)
				changed.InputDigest = voicecontract.Digest(changed.Arguments)
				req.RemoteRead = &changed
				if _, e = state.execute(ctx, in, req); e == nil {
					return errors.New("changed read replay accepted")
				}
				return nil
			})
			if e := env.GetWorkflowError(); e != nil {
				t.Fatal(e)
			}
			want := 1
			if wrong {
				want = 0
			}
			if calls != want {
				t.Fatalf("read executions=%d want%d", calls, want)
			}
		})
	}
}

func TestOperatorWorkflowSharesTwoToolsAcrossReadAndControl(t *testing.T) {
	raw, _ := os.ReadFile("../voicecontract/testdata/command.json")
	var command voicecontract.Command
	_ = voicecontract.Decode(raw, 16384, &command)
	raw, _ = os.ReadFile("../voicecontract/testdata/remote-read.json")
	var read voicecontract.ReadRequest
	_ = voicecontract.Decode(raw, 16384, &read)
	read.Context = command.Context
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	id, _ := WorkflowID(command.Context.SessionID, command.Context.ExecutionID)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
	calls := 0
	env.RegisterActivityWithOptions(func(_ context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		calls++
		if req.RemoteRead != nil {
			return VoiceSessionExecuteToolResult{Result: []byte(`{"results":[]}`)}, nil
		}
		return VoiceSessionExecuteToolResult{Operation: &voicecontract.Operation{Version: "1", Context: command.Context, OperationID: command.OperationID, State: "accepted", Sequence: 1}}, nil
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		control := newVoiceControlWorkflowState()
		reads := newVoiceReadWorkflowState()
		reads.budget = control.budget
		in := VoiceSessionInput{Context: &command.Context, AgentID: command.Context.AgentID, CallID: command.Context.CallID, SessionID: command.Context.SessionID, ExecutionID: command.Context.ExecutionID}
		rreq := VoiceSessionExecuteToolInput{Grant: "opaque", RemoteRead: &read}
		creq := VoiceSessionExecuteToolInput{Grant: "opaque", CallControl: &command}
		if result, e := reads.execute(ctx, in, rreq); e != nil || result.Error != "" {
			return errors.New("first read rejected")
		}
		if _, e := control.execute(ctx, in, creq); e != nil {
			return e
		}
		if result, e := reads.execute(ctx, in, rreq); e != nil || result.Error != "" {
			return errors.New("exact read replay charged")
		}
		if _, e := control.execute(ctx, in, creq); e != nil {
			return errors.New("exact control replay charged")
		}
		nextRead := read
		nextRead.ToolCallID = "third-read"
		rreq.RemoteRead = &nextRead
		if result, e := reads.execute(ctx, in, rreq); e == nil && result.Error == "" {
			return errors.New("third read accepted")
		}
		nextControl := command
		nextControl.ToolCallID = "third-control"
		nextControl.OperationID = "new-op"
		nextControl.AnnouncementBarrierID++
		creq.CallControl = &nextControl
		if _, e := control.execute(ctx, in, creq); e == nil {
			return errors.New("third control accepted")
		}
		return nil
	})
	if e := env.GetWorkflowError(); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("tool executions=%d", calls)
	}
}
