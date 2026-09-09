package temporalruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

// The same mutex linearizes terminal commit against provider writes and output.
// No network Close occurs while this mutex is held.
type liveControlGate struct {
	mu       sync.Mutex
	serial   sync.Mutex
	pending  bool
	maxTurns int
	turns    int
	terminal atomic.Bool
}

func (g *liveControlGate) write(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal.Load() {
		return nil
	}
	return fn()
}
func (g *liveControlGate) emit(ctx context.Context, out chan<- voice.AudioFrame, f voice.AudioFrame) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending || g.terminal.Load() {
		return nil
	}
	if g.maxTurns > 0 && g.turns >= g.maxTurns {
		return errors.New("voice assistant turn budget exhausted")
	}
	select {
	case out <- f:
		if f.TurnComplete {
			g.turns++
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *liveControlGate) stopped() bool { return g.terminal.Load() }
func (g *liveControlGate) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal.Load() || (g.maxTurns > 0 && g.turns >= g.maxTurns) {
		return false
	}
	g.pending = true
	return true
}
func (g *liveControlGate) finish(terminal bool) {
	if terminal {
		g.terminal.Store(true)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pending = false
}
func controlTools(d agents.AIDefinition, cfg voice.StartConfig) (map[string]ai.Tool, error) {
	if cfg.CallControl == nil {
		return voiceToolsFromDefinition(d, cfg.EnabledToolIDs), nil
	}
	c := cfg.CallControl
	if c.MaxAssistantTurns < 0 || c.MaxAssistantTurns > 10 || c.Execute == nil || cfg.OnPlayoutBarrier == nil || len(c.ToolNames) == 0 || len(c.ToolNames) > 8 {
		return nil, errors.New("verified call-control executor and playout barrier required")
	}
	out := map[string]ai.Tool{}
	for _, name := range c.ToolNames {
		if name == "" || isWebSearchTool(name) {
			return nil, errors.New("invalid control tool")
		}
		if _, ok := out[name]; ok {
			return nil, errors.New("duplicate control tool")
		}
		if name == voicecontract.ToolIDHangUp {
			// hang_up is platform injected. It must not be supplied by the
			// customer definition or acquire an authored handler.
			out[name] = ai.Tool{Name: name, Description: "End the current call.", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false,
			}}
			continue
		}
		t, ok := lookupDefinitionTool(d, name)
		if !ok {
			return nil, errors.New("control tool absent from resolved definition")
		}
		out[name] = t
	}
	return out, nil
}
func invokeControl(ctx context.Context, cfg voice.StartConfig, call ai.ToolCall, barrier uint64) (any, bool, error) {
	allowed := false
	for _, name := range cfg.CallControl.ToolNames {
		allowed = allowed || name == call.ToolName
	}
	if !allowed {
		return nil, false, errors.New("tool outside verified control manifest")
	}
	if err := cfg.OnPlayoutBarrier(ctx, barrier); err != nil {
		return nil, false, err
	}
	result, err := cfg.CallControl.Execute(ctx, call)
	var terminal *voice.TerminalHandoff
	if errors.As(err, &terminal) {
		if terminal == nil || terminal.Result.Validate() != nil {
			return nil, false, errors.New("invalid trusted terminal result")
		}
		return nil, true, nil
	}
	return result, false, err
}

func controlTurnLimit(cfg voice.StartConfig) int {
	if cfg.CallControl == nil {
		return 0
	}
	return cfg.CallControl.MaxAssistantTurns
}
