package temporalruntime

import (
	"context"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"google.golang.org/genai"
)

// Exercise the real Live startup gate rather than contract validation alone:
// a verified playback policy must reach provider dialing with its bounded turns.
func TestGeminiStartupAcceptsVerifiedPlaybackBudget(t *testing.T) {
	for _, name := range []string{"playback", "mailbox", "legacy", "unknown", "overflow", "missing_executor", "missing_barrier"} {
		t.Run(name, func(t *testing.T) {
			cfg := terminalConfig(t)
			cfg.AgentID = "call-operator"
			cfg.SessionID = "vs_budget"
			cfg.Actor = agents.Actor{ID: "line", Kind: "line"}
			cfg.CallControl.ToolNames = []string{voicecontract.ToolIDHangUp}
			cfg.CallControl.BudgetPolicy = voicecontract.BudgetPolicyOperatorMailboxPlaybackV1
			cfg.CallControl.MaxAssistantTurns = 32
			switch name {
			case "mailbox":
				cfg.CallControl.BudgetPolicy = voicecontract.BudgetPolicyOperatorMailboxV1
			case "legacy":
				cfg.CallControl.BudgetPolicy = ""
			case "unknown":
				cfg.CallControl.BudgetPolicy = "forged"
			case "overflow":
				cfg.CallControl.MaxAssistantTurns = 33
			case "missing_executor":
				cfg.CallControl.Execute = nil
			case "missing_barrier":
				cfg.OnPlayoutBarrier = nil
			}
			dialed := false
			adapter := &GeminiLiveAdapter{definition: agents.DefineAI(agents.AIConfig{LiveModel: "gemini-live-test"}), dial: func(context.Context, agents.AIDefinition, string, *genai.LiveConnectConfig) (liveSession, error) {
				dialed = true
				return newFakeLiveSession(), nil
			}}
			input := make(chan []byte)
			output := make(chan voice.AudioFrame, 40)
			handle, _, err := adapter.Start(context.Background(), cfg, input, output)
			allowed := name == "playback" || name == "mailbox"
			if (err == nil) != allowed || dialed != allowed {
				t.Fatalf("startup authority changed: dialed=%t err=%v", dialed, err)
			}
			if !allowed {
				return
			}
			defer handle.Close()
			live := handle.(*geminiLiveHandle)
			if live.control.maxTurns != 32 {
				t.Fatal("configured turn bound lost")
			}
			gate := liveControlGate{maxTurns: live.control.maxTurns}
			for i := 0; i < 32; i++ {
				if err := gate.emit(context.Background(), output, voice.AudioFrame{TurnComplete: true}); err != nil {
					t.Fatalf("turn%d unexpectedly blocked", i)
				}
			}
			if err := gate.emit(context.Background(), output, voice.AudioFrame{TurnComplete: true}); err == nil {
				t.Fatal("turn33 allowed")
			}
		})
	}
}
