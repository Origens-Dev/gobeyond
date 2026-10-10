package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
	"github.com/Origens-Dev/gobeyond/agents/voice"
)

var errVoiceApprovalUnavailable = errors.New("voice client approval interaction is unavailable")
var errVoiceWriteRequiresDurableDispatcher = errors.New("voice write requires durable session dispatch")
var errVoiceWriteIdentityUnavailable = errors.New("voice write identity is unavailable")

// executeVoiceAgentTool applies the authored tool approval policy to Live
// voice tool calls. Voice transports do not yet expose a user-bound approval
// interaction, so any call requiring user approval is denied before Execute.
//
// VoiceWritePolicy selects app mutations generically (Candlestick owns
// leave_text_message). VoiceWritePolicy is not a trusted-host assertion.
// BindVoiceDurableDispatcher marks the existing Execute callback; it does
// not make Execute durable. Maglev must bind it only on the real
// /internal/workflows/voice-session/execute-tool wrapper. Approval is always
// resolved before Execute.
func executeVoiceAgentTool(ctx context.Context, tool ai.Tool, call ai.ToolCall, options ai.ToolExecutionOptions) (any, error) {
	if tool.Execute == nil {
		return nil, errors.New("voice tool execution handler is unavailable")
	}
	if err := denyVoiceApprovalIfRequired(ctx, tool, call); err != nil {
		return nil, err
	}
	if agents.VoiceWritePolicy(tool) {
		if err := requireDurableVoiceWriteIdentity(options); err != nil {
			return nil, err
		}
		if !agents.VoiceDurableWriteDispatcher(tool) {
			return nil, errVoiceWriteRequiresDurableDispatcher
		}
	}
	return tool.Execute(ctx, call, options)
}

func denyVoiceApprovalIfRequired(ctx context.Context, tool ai.Tool, call ai.ToolCall) error {
	if tool.RequiresApproval {
		return errVoiceApprovalUnavailable
	}
	if tool.NeedsApproval == nil {
		return nil
	}
	decision, err := ai.ResolveToolApproval(ctx, map[string]ai.Tool{call.ToolName: tool}, call)
	if err != nil {
		return fmt.Errorf("resolve voice tool approval policy: %w", err)
	}
	if !ai.ApprovalBlocksToolExecution(decision) {
		return nil
	}
	if decision.Type == ai.ApprovalDecisionUserApproval {
		return errVoiceApprovalUnavailable
	}
	return errors.New(decision.Reason)
}

func requireDurableVoiceWriteIdentity(options ai.ToolExecutionOptions) error {
	bound := toolsession.Bind(context.Background(), options.Context)
	sessionID, haveSession := toolsession.ID(bound)
	writeID, haveWrite := toolsession.WriteID(bound)
	if !haveSession || !haveWrite || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(writeID) == "" {
		return errVoiceWriteIdentityUnavailable
	}
	if _, err := voiceWriteActor(options.Context); err != nil {
		return errVoiceWriteIdentityUnavailable
	}
	return nil
}

func voiceWriteActor(value any) (agents.Actor, error) {
	if fields, ok := value.(map[string]any); ok {
		value = fields["gobeyondActor"]
	}
	if actor, ok := value.(agents.Actor); ok {
		return actor, actor.Validate()
	}
	data, err := json.Marshal(value)
	if err != nil {
		return agents.Actor{}, errVoiceWriteIdentityUnavailable
	}
	var actor agents.Actor
	if err := json.Unmarshal(data, &actor); err != nil || actor.Validate() != nil {
		return agents.Actor{}, errVoiceWriteIdentityUnavailable
	}
	return actor, nil
}

func liveVoiceToolOptions(cfg voice.StartConfig, tool ai.Tool, call ai.ToolCall) ai.ToolExecutionOptions {
	if !agents.VoiceWritePolicy(tool) {
		return ai.ToolExecutionOptions{Context: toolsession.ExecutionContext(cfg.Actor, cfg.SessionID)}
	}
	return ai.ToolExecutionOptions{Context: toolsession.ExecutionContextWithWrite(cfg.Actor, cfg.SessionID, liveVoiceWriteIdentity(cfg.SessionID, call.ToolName, call.ToolCallID), agents.ResourceBinding{})}
}

func liveVoiceWriteIdentity(sessionID, toolName, toolCallID string) string {
	return strings.TrimSpace(sessionID) + "/" + strings.TrimSpace(toolName) + "/" + strings.TrimSpace(toolCallID)
}
