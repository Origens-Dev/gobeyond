package temporalruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
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

func TestVoiceWriteRejectedOnLiveExecutePath(t *testing.T) {
	calls := 0
	schema, output := voiceWriteClosedSchemas()
	tool := agents.DefineTool(agents.ToolConfig{Name: "lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	_, err := executeVoiceAgentTool(context.Background(), tool, ai.ToolCall{ToolCallID: "call-1", ToolName: tool.Name, Input: map[string]any{"q": "x"}}, ai.ToolExecutionOptions{})
	if err == nil || !strings.Contains(err.Error(), "durable session") || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
