package temporalruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

var errVoiceApprovalUnavailable = errors.New("voice client approval interaction is unavailable")

// executeVoiceAgentTool applies the authored tool approval policy to Live
// voice tool calls. Voice transports do not yet expose a user-bound approval
// interaction, so any call requiring user approval is denied before Execute.
func executeVoiceAgentTool(ctx context.Context, tool ai.Tool, call ai.ToolCall, options ai.ToolExecutionOptions) (any, error) {
	if tool.Execute == nil {
		return nil, errors.New("voice tool execution handler is unavailable")
	}
	if tool.RequiresApproval {
		return nil, errVoiceApprovalUnavailable
	}
	if tool.NeedsApproval != nil {
		decision, err := ai.ResolveToolApproval(ctx, map[string]ai.Tool{call.ToolName: tool}, call)
		if err != nil {
			return nil, fmt.Errorf("resolve voice tool approval policy: %w", err)
		}
		if ai.ApprovalBlocksToolExecution(decision) {
			if decision.Type == ai.ApprovalDecisionUserApproval {
				return nil, errVoiceApprovalUnavailable
			}
			return nil, errors.New(decision.Reason)
		}
	}
	return tool.Execute(ctx, call, options)
}
