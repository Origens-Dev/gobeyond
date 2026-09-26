package temporalruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
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
