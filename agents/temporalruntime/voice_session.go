package temporalruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	VoiceSessionWorkflowName = "gobeyond.agents.voice_session.v1"

	// VoiceSessionExecuteToolUpdate is the Maglev/API → workflow Update that
	// runs a Live tool as a LocalActivity on the agent worker (P2 remote path
	// and P3 colocated LocalActivity tools).
	VoiceSessionExecuteToolUpdate = "gobeyond.agents.voice_session.execute_tool.v1"

	voiceSessionExecuteToolActivityName = "gobeyond.agents.voice_session.execute_tool"
	maxVoiceSessionToolCalls            = 8
)

// VoiceSessionInput is the durable voice-call workflow argument.
type VoiceSessionInput struct {
	AgentID     string `json:"agent_id"`
	CallID      string `json:"call_id"`
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
}

// VoiceSessionExecuteToolInput is the Update / LocalActivity payload for one
// Gemini Live function call.
type VoiceSessionExecuteToolInput struct {
	AgentID    string          `json:"agent_id"`
	ToolName   string          `json:"tool_name"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	ActorID    string          `json:"actor_id,omitempty"`
	ActorKind  string          `json:"actor_kind,omitempty"`
	NetworkID  string          `json:"network_id,omitempty"`
	// AllowedToolIDs is derived from the verified voice grant by the API. It is
	// optional for colocated/internal tests and older direct workflow callers.
	AllowedToolIDs []string `json:"allowed_tool_ids,omitempty"`
}

// VoiceSessionExecuteToolResult is returned to Maglev so it can SendToolResponse.
type VoiceSessionExecuteToolResult struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// VoiceSessionWorkflow is the lifecycle workflow for an AI phone/softphone
// call. Media may stay on Maglev (P0–P2) or colocate on the realtime
// RoleWorker (P3); this workflow is the Origens Agents SoR handle and the
// LocalActivity home for Live tool Updates.
func VoiceSessionWorkflow(ctx workflow.Context, in VoiceSessionInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("voice session started",
		"agent_id", in.AgentID,
		"call_id", in.CallID,
		"session_id", in.SessionID,
		"execution_id", in.ExecutionID,
	)

	toolCalls := 0
	completed := map[string]VoiceSessionExecuteToolResult{}
	if err := workflow.SetUpdateHandler(ctx, VoiceSessionExecuteToolUpdate,
		func(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
			if strings.TrimSpace(req.AgentID) == "" {
				req.AgentID = in.AgentID
			}
			callID := strings.TrimSpace(req.ToolCallID)
			if callID != "" {
				if result, ok := completed[callID]; ok {
					return result, nil
				}
			}
			if toolCalls >= maxVoiceSessionToolCalls {
				return VoiceSessionExecuteToolResult{Error: "voice session tool quota exceeded"}, nil
			}
			toolCalls++
			result, err := executeVoiceSessionToolLocal(ctx, req)
			if err == nil && callID != "" {
				completed[callID] = result
			}
			return result, err
		}); err != nil {
		return err
	}

	// Block until cancel/terminate from Maglev hangup (or a future complete signal).
	// 30m matches Maglev Live session token TTL.
	_ = workflow.Sleep(ctx, 30*time.Minute)
	logger.Info("voice session timed out", "session_id", in.SessionID)
	return nil
}

func executeVoiceSessionToolLocal(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	var out VoiceSessionExecuteToolResult
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	})
	err := workflow.ExecuteLocalActivity(laCtx, voiceSessionExecuteToolActivityName, req).Get(ctx, &out)
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: err.Error()}, nil
	}
	return out, nil
}

// VoiceSessionExecuteToolActivity resolves the agent tool from ProcessVoiceRegistry
// and runs it in-process on the realtime worker (LocalActivity). Maglev cannot
// hold customer tools; the agent worker can.
func VoiceSessionExecuteToolActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	_ = ctx
	agentID := strings.TrimSpace(req.AgentID)
	toolName := strings.TrimSpace(req.ToolName)
	if agentID == "" || toolName == "" {
		return VoiceSessionExecuteToolResult{Error: "agent_id and tool_name required"}, nil
	}
	reg := ProcessVoiceRegistry()
	if reg == nil {
		return VoiceSessionExecuteToolResult{Error: "voice registry unavailable (not colocated)"}, nil
	}
	definition, ok := reg.Definition(agentID)
	if !ok {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("agent %q has no voice definition", agentID)}, nil
	}
	tool, ok := lookupDefinitionTool(definition, toolName)
	if !ok || tool.Execute == nil {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("unknown tool %q", toolName)}, nil
	}

	var args map[string]any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &args); err != nil {
			return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("invalid tool input: %v", err)}, nil
		}
	}
	actorMetadata := map[string]string{}
	if networkID := strings.TrimSpace(req.NetworkID); networkID != "" {
		actorMetadata["network_id"] = networkID
	}
	actor := agents.Actor{
		ID:       strings.TrimSpace(req.ActorID),
		Kind:     strings.TrimSpace(req.ActorKind),
		Metadata: actorMetadata,
	}
	if err := actor.Validate(); err != nil {
		return VoiceSessionExecuteToolResult{Error: "voice session actor unavailable"}, nil
	}
	if len(req.AllowedToolIDs) > 0 && !voiceToolAllowed(req.AllowedToolIDs, toolName) {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("tool %q is not allowed", toolName)}, nil
	}
	result, err := tool.Execute(ctx, ai.ToolCall{
		ToolCallID: strings.TrimSpace(req.ToolCallID),
		ToolName:   toolName,
		Input:      args,
	}, ai.ToolExecutionOptions{
		Context: map[string]any{"gobeyondActor": actor},
	})
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: err.Error()}, nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("encode tool result: %v", err)}, nil
	}
	return VoiceSessionExecuteToolResult{Result: raw}, nil
}

func voiceToolAllowed(ids []string, name string) bool {
	name = strings.TrimSpace(name)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == name || (id == "web_search" && name == "web-search") {
			return true
		}
	}
	return false
}

func lookupDefinitionTool(definition agents.AIDefinition, name string) (ai.Tool, bool) {
	if tool, ok := definition.AI.Tools[name]; ok {
		return tool, true
	}
	for key, tool := range definition.AI.Tools {
		if strings.TrimSpace(tool.Name) == name || key == name {
			return tool, true
		}
	}
	return ai.Tool{}, false
}

// RegisterVoiceSessionWorkflow registers the platform voice-session workflow
// and its LocalActivity tool executor.
func RegisterVoiceSessionWorkflow(w worker.Registry) {
	if w == nil {
		return
	}
	w.RegisterWorkflowWithOptions(VoiceSessionWorkflow, workflow.RegisterOptions{Name: VoiceSessionWorkflowName})
	w.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{
		Name: voiceSessionExecuteToolActivityName,
	})
}
