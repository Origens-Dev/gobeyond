package temporalruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
)

func TestVoiceToolRequiresApprovalFailsClosedWithoutExecuting(t *testing.T) {
	calls := 0
	tool := ai.Tool{
		Name: "rename_network", RequiresApproval: true,
		Execute: func(context.Context, ai.ToolCall, ai.ToolExecutionOptions) (any, error) {
			calls++
			return "renamed", nil
		},
	}
	_, err := executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-1", ToolName: tool.Name, Input: map[string]any{"name": "new"}}, ai.ToolExecutionOptions{})
	if !errors.Is(err, errVoiceApprovalUnavailable) || calls != 0 {
		t.Fatalf("error=%v execute calls=%d", err, calls)
	}
}

func TestVoiceToolDynamicApprovalPolicy(t *testing.T) {
	calls := 0
	tool := ai.Tool{
		Name: "lookup", NeedsApproval: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) { return ai.Approved(), nil },
		Execute: func(context.Context, ai.ToolCall, ai.ToolExecutionOptions) (any, error) { calls++; return "ok", nil },
	}
	if _, err := executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-1", ToolName: tool.Name}, ai.ToolExecutionOptions{}); err != nil || calls != 1 {
		t.Fatalf("error=%v execute calls=%d", err, calls)
	}
	tool.NeedsApproval = func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) { return ai.UserApproval(), nil }
	if _, err := executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-2", ToolName: tool.Name}, ai.ToolExecutionOptions{}); !errors.Is(err, errVoiceApprovalUnavailable) || calls != 1 {
		t.Fatalf("user approval error=%v execute calls=%d", err, calls)
	}
}

func independentlyNamedOrdinaryWrite(t *testing.T, calls *int) ai.Tool {
	t.Helper()
	schema, output := voiceWriteClosedSchemas()
	tool := agents.DefineTool(agents.ToolConfig{Name: "archive_desk_note", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		*calls++
		return map[string]any{"ok": true}, nil
	})
	if tool.Name == "leave_text_message" || !agents.VoiceWritePolicy(tool) || agents.VoiceDurableWriteDispatcher(tool) {
		t.Fatal("ordinary VoiceWrite must stay generic and not host-bound")
	}
	return tool
}

func TestVoiceWriteOrdinaryHandlerCannotBypassApproval(t *testing.T) {
	calls := 0
	schema, output := voiceWriteClosedSchemas()
	tool := agents.DefineTool(agents.ToolConfig{Name: "archive_desk_note", InputSchema: schema, OutputSchema: output, VoiceWrite: true, RequiresApproval: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	bound, err := agents.BindVoiceDurableDispatcher(tool)
	if err != nil {
		t.Fatal(err)
	}
	opts := ai.ToolExecutionOptions{Context: toolsession.ExecutionContextWithWrite(agents.Actor{ID: "user-1", Kind: "user"}, "sess-1", "sess-1/archive_desk_note/call-1")}
	_, err = executeVoiceAgentTool(context.Background(), bound, ai.ToolCall{ToolCallID: "call-1", ToolName: bound.Name, Input: map[string]any{"q": "x"}}, opts)
	if !errors.Is(err, errVoiceApprovalUnavailable) || calls != 0 {
		t.Fatalf("approval bypassed error=%v calls=%d", err, calls)
	}
	_, err = executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-1", ToolName: tool.Name, Input: map[string]any{"q": "x"}}, opts)
	if !errors.Is(err, errVoiceApprovalUnavailable) || calls != 0 {
		t.Fatalf("ordinary write approval error=%v calls=%d", err, calls)
	}
}

func TestVoiceWriteOrdinaryHandlerRejectedWithoutIdentity(t *testing.T) {
	calls := 0
	tool := independentlyNamedOrdinaryWrite(t, &calls)
	_, err := executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-1", ToolName: tool.Name, Input: map[string]any{"q": "x"}}, ai.ToolExecutionOptions{})
	if !errors.Is(err, errVoiceWriteIdentityUnavailable) || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestVoiceWriteOrdinaryHandlerRejectedWithoutDispatcher(t *testing.T) {
	calls := 0
	tool := independentlyNamedOrdinaryWrite(t, &calls)
	opts := ai.ToolExecutionOptions{Context: toolsession.ExecutionContextWithWrite(agents.Actor{ID: "user-1", Kind: "user"}, "sess-1", "sess-1/archive_desk_note/call-1")}
	_, err := executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-1", ToolName: tool.Name, Input: map[string]any{"q": "x"}}, opts)
	if !errors.Is(err, errVoiceWriteRequiresDurableDispatcher) || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

// TestVoiceWriteDurableDispatcherExecutes proves the trusted-host assertion
// lets the existing Execute run. BindVoiceDurableDispatcher does not wrap
// Maglev /execute-tool or make Execute durable.
func TestVoiceWriteDurableDispatcherExecutes(t *testing.T) {
	calls := 0
	tool := independentlyNamedOrdinaryWrite(t, &calls)
	bound, err := agents.BindVoiceDurableDispatcher(tool)
	if err != nil {
		t.Fatal(err)
	}
	got, err := executeVoiceAgentTool(context.Background(), bound, ai.ToolCall{ToolCallID: "call-1", ToolName: bound.Name, Input: map[string]any{"q": "x"}}, ai.ToolExecutionOptions{
		Context: toolsession.ExecutionContextWithWrite(agents.Actor{ID: "user-1", Kind: "user"}, "sess-1", "sess-1/archive_desk_note/call-1"),
	})
	if err != nil || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	result, ok := got.(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("result=%#v", got)
	}
}
