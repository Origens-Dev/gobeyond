package temporalruntime

import (
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const VoiceSessionWorkflowName = "gobeyond.agents.voice_session.v1"

// VoiceSessionInput is the durable voice-call workflow argument.
type VoiceSessionInput struct {
	AgentID     string `json:"agent_id"`
	CallID      string `json:"call_id"`
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
}

// VoiceSessionWorkflow is the P0 lifecycle workflow for an AI phone/softphone
// call. Media stays on Maglev; this workflow exists so Origens Agents SoR can
// track the run. Later phases add LocalActivity tool Updates.
func VoiceSessionWorkflow(ctx workflow.Context, in VoiceSessionInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("voice session started",
		"agent_id", in.AgentID,
		"call_id", in.CallID,
		"session_id", in.SessionID,
		"execution_id", in.ExecutionID,
	)
	// Block until cancel/terminate from Maglev hangup (or a future complete signal).
	// 30m matches Maglev Live session token TTL.
	_ = workflow.Sleep(ctx, 30*time.Minute)
	logger.Info("voice session timed out", "session_id", in.SessionID)
	return nil
}

// RegisterVoiceSessionWorkflow registers the platform voice-session workflow.
func RegisterVoiceSessionWorkflow(w worker.Registry) {
	if w == nil {
		return
	}
	w.RegisterWorkflowWithOptions(VoiceSessionWorkflow, workflow.RegisterOptions{Name: VoiceSessionWorkflowName})
}
