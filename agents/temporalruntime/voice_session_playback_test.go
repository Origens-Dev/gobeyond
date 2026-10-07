package temporalruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"time"
)

func sourceRequestFixture(t *testing.T) voicecontract.SourcePlaybackRequest {
	t.Helper()
	raw, _ := os.ReadFile("../voicecontract/testdata/command.json")
	var c voicecontract.Command
	if e := json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	c.Context.AgentID = "call-operator"
	c.Context.Scope.Kind = "agent"
	c.Context.TransportCallID = "transport"
	c.Context.ParentCallID = "parent"
	c.Context.HopID = "hop"
	c.Context.HopCount = 1
	return voicecontract.SourcePlaybackRequest{Version: voicecontract.Version, Context: c.Context, ToolID: "play-text-message", ToolCallID: "source", CompletionToolID: "complete-text-message-playback", InputDigest: voicecontract.Digest([]byte(`{}`)), Arguments: []byte(`{}`)}
}
func TestPlaybackMixedDispatchAndHiddenFailsClosed(t *testing.T) {
	r := sourceRequestFixture(t)
	h := voicecontract.HiddenCompletionRequest{Version: r.Version, Context: r.Context, ToolID: "complete-text-message-playback", ToolCallID: voicecontract.PlaybackCompletionCallID(r.Context, r.ToolCallID), SourceToolCallID: r.ToolCallID, ClipID: voicecontract.PlaybackClipID(r.Context, r.ToolCallID)}
	for _, req := range []VoiceSessionExecuteToolInput{{SourcePlayback: &r, RemoteRead: &voicecontract.ReadRequest{}}, {HiddenCompletion: &h, SourcePlayback: &r}, {HiddenCompletion: &h, CallControl: &voicecontract.Command{}}, {HiddenCompletion: &h}} {
		if _, e := VoiceSessionExecuteToolActivity(context.Background(), req); e == nil {
			t.Fatal("mixed or unauthenticated dispatch accepted")
		}
	}
}
func TestPlaybackWorkflowReplayBudgetAndVersion(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical", true: "new"}[enabled], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			version := workflow.DefaultVersion
			if enabled {
				version = 1
			}
			env.OnGetVersion(playbackExecutionVersionChange, workflow.DefaultVersion, 1).Return(version)
			r := sourceRequestFixture(t)
			id, _ := WorkflowID(r.Context.SessionID, r.Context.ExecutionID)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
			calls := 0
			env.RegisterActivityWithOptions(func(_ context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
				calls++
				if req.HiddenCompletion != nil {
					return VoiceSessionExecuteToolResult{Error: "authenticated completion unavailable"}, nil
				}
				return VoiceSessionExecuteToolResult{Result: []byte(`{"source":"exact"}`)}, nil
			}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				b := &voiceToolBudget{policy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1}
				s := configureVoicePlayback(ctx, b)
				in := VoiceSessionInput{Context: &r.Context, AgentID: r.Context.AgentID, CallID: r.Context.CallID, SessionID: r.Context.SessionID, ExecutionID: r.Context.ExecutionID}
				req := VoiceSessionExecuteToolInput{SourcePlayback: &r, Grant: "opaque"}
				_, e := s.execute(ctx, in, req)
				if !enabled {
					if e == nil {
						return errors.New("historical expanded")
					}
					return nil
				}
				if e != nil {
					return e
				}
				if _, e = s.execute(ctx, in, req); e != nil {
					return e
				}
				changed := r
				changed.Arguments = []byte(`{"other":true}`)
				changed.InputDigest = voicecontract.Digest(changed.Arguments)
				req.SourcePlayback = &changed
				if _, e = s.execute(ctx, in, req); e == nil {
					return errors.New("altered replay accepted")
				}
				if e = b.consume("get-text-message", r.ToolCallID, "different", 2); e == nil {
					return errors.New("cross-tool replay accepted")
				}
				h := voicecontract.HiddenCompletionRequest{Version: r.Version, Context: r.Context, ToolID: "complete-text-message-playback", ToolCallID: voicecontract.PlaybackCompletionCallID(r.Context, r.ToolCallID), SourceToolCallID: r.ToolCallID, ClipID: voicecontract.PlaybackClipID(r.Context, r.ToolCallID)}
				if _, e = s.execute(ctx, in, VoiceSessionExecuteToolInput{HiddenCompletion: &h, Input: []byte(`{"receipt":"forged"}`)}); e == nil {
					return errors.New("model receipt accepted")
				}
				result, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{HiddenCompletion: &h})
				if e != nil {
					return e
				}
				if result.Error == "" {
					return errors.New("unwired completion mutated")
				}
				return nil
			})
			if e := env.GetWorkflowError(); e != nil {
				t.Fatal(e)
			}
			want := 0
			if enabled {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls=%d want%d", calls, want)
			}
		})
	}
}

func TestPlaybackSourceActivityFrozenPolicyAndExactOutput(t *testing.T) {
	r := sourceRequestFixture(t)
	policy := voicecontract.PlaybackPolicy{TextField: "text", MessageIDField: "message_id", CreatedAtField: "created_at", TextDigestField: "text_digest", MessageExpiresAtField: "message_expires_at", ExpiresAtField: "expires_at", CompletionToolID: "complete-text-message-playback", MaxTextBytes: 4096, MaxResultBytes: 8192}
	properties := map[string]any{}
	keys := []string{"text", "message_id", "created_at", "text_digest", "message_expires_at", "expires_at"}
	for _, k := range keys {
		bound := 128
		if k == "text" {
			bound = 4096
		}
		properties[k] = map[string]any{"type": "string", "minLength": 1, "maxLength": bound}
	}
	input := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}, "required": []string{}}
	output := map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": keys}
	inRaw, _ := json.Marshal(input)
	inRaw, _ = voicecontract.CanonicalJSON(inRaw, 16384)
	outRaw, _ := json.Marshal(output)
	outRaw, _ = voicecontract.CanonicalJSON(outRaw, 16384)
	spec := voicecontract.Tool{ID: r.ToolID, Name: "play_text_message", ExecutionKind: "playback", Playback: &policy, InputSchema: inRaw, SchemaDigest: voicecontract.Digest(inRaw), OutputSchema: outRaw, OutputSchemaDigest: voicecontract.Digest(outRaw), MaxResultBytes: 8192}
	bad := false
	calls := 0
	tool := agents.DefineTool(agents.ToolConfig{Name: spec.Name, InputSchema: input, OutputSchema: output, VoicePlayback: &policy}, func(_ context.Context, actor agents.Actor, _ map[string]any) (any, error) {
		calls++
		if actor.Metadata["voice_session_grant"] != "opaque" || actor.Metadata["line_id"] != r.Context.Scope.LineID || actor.Metadata["operation_id"] != "" {
			t.Error("scope metadata changed")
		}
		now := time.Now().UTC()
		text := "Exact message bytes"
		sum := sha256.Sum256([]byte(text))
		digest := hex.EncodeToString(sum[:])
		if bad {
			digest = "forged"
		}
		return map[string]any{"text": text, "message_id": "message", "created_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "text_digest": digest, "message_expires_at": now.Add(time.Hour).Format(time.RFC3339Nano), "expires_at": now.Add(time.Minute).Format(time.RFC3339Nano)}, nil
	})
	m := voicecontract.Manifest{Version: voicecontract.Version, CompiledRevision: r.Context.AgentRevision, BudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1, Tools: []voicecontract.Tool{spec, {ID: "complete-text-message-playback", ExecutionKind: "playback_completion"}, {ID: "list-text-messages", ExecutionKind: "read"}, {ID: "get-text-message", ExecutionKind: "read"}, {ID: "dial-contact", ExecutionKind: "control"}}}
	raw, _ := json.Marshal(m)
	digest := voicecontract.Digest(raw)
	r.Context.ManifestDigest = digest
	reg := NewVoiceRegistry()
	reg.definitions[r.Context.AgentID] = agents.DefineAI(agents.AIConfig{Revision: r.Context.AgentRevision, Tools: map[string]agents.AITool{spec.ID: tool}})
	reg.manifests = map[string][]byte{r.Context.AgentID: raw}
	reg.manifestDigests = map[string]string{r.Context.AgentID: digest}
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(old)
	req := VoiceSessionExecuteToolInput{SourcePlayback: &r, Grant: "opaque", BudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1}
	result, e := VoiceSessionExecuteToolActivity(context.Background(), req)
	if e != nil || result.Error != "" || len(result.Result) == 0 {
		t.Fatal("source rejected", result, e)
	}
	bad = true
	if _, e = VoiceSessionExecuteToolActivity(context.Background(), req); e == nil {
		t.Fatal("altered exact digest admitted")
	}
	r.Context.ManifestDigest = "sha256:" + hex.EncodeToString(make([]byte, 32))
	if _, e = VoiceSessionExecuteToolActivity(context.Background(), req); e == nil || calls != 2 {
		t.Fatal("stale manifest executed")
	}
	if _, e = VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{AgentID: r.Context.AgentID, ToolName: spec.Name}); e == nil {
		t.Fatal("model gained playback dispatch")
	}
}
