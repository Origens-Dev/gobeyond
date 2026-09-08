package voice

import (
	"context"
	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

// CallControlConfig is an in-process verified runtime boundary, never decoded
// from session metadata. Execute must enforce grant/owner/context/sequence and
// schema validation, and return only after authoritative operation acceptance.
type CallControlConfig struct {
	MaxAssistantTurns int
	ToolNames         []string
	Execute           func(context.Context, ai.ToolCall) (any, error)
}

// TerminalHandoff is trusted only when returned by CallControlConfig.Execute.
// Tool JSON and ordinary authored Execute errors cannot terminate a session.
type TerminalHandoff struct{ Result voicecontract.TerminalResult }

func (*TerminalHandoff) Error() string { return "voice handed off" }
