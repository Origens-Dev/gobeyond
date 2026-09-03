package temporalruntime

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
)

// VoiceRegistry holds Live voice adapters for AI agents on a realtime worker.
// Generated realtime-{agentId} mains register adapters alongside AIRegistry.
type VoiceRegistry struct {
	mu          sync.RWMutex
	adapters    map[string]voice.Adapter
	definitions map[string]agents.AIDefinition
}

var processVoiceRegistry atomic.Pointer[VoiceRegistry]

// NewVoiceRegistry returns an empty registry for generated worker mains.
func NewVoiceRegistry() *VoiceRegistry {
	return &VoiceRegistry{
		adapters:    map[string]voice.Adapter{},
		definitions: map[string]agents.AIDefinition{},
	}
}

// RetainVoiceRegistry keeps the worker's voice registry reachable for the
// process lifetime so G5 / in-process Live dispatch can Lookup adapters.
func RetainVoiceRegistry(registry *VoiceRegistry) {
	processVoiceRegistry.Store(registry)
}

// ProcessVoiceRegistry returns the registry retained by the generated worker
// main, if any.
func ProcessVoiceRegistry() *VoiceRegistry {
	return processVoiceRegistry.Load()
}

// RegisterVoice installs the Gemini Live adapter for an AI agent that declares
// LiveModel. Agents without LiveModel are a no-op so shared registration
// helpers stay safe.
func RegisterVoice(registry *VoiceRegistry, agentID string, definition agents.AIDefinition) error {
	if registry == nil {
		return errors.New("agent voice registry is required")
	}
	if strings.TrimSpace(definition.AI.LiveModel) == "" {
		return nil
	}
	agentID, err := normalizeAgentID(agentID)
	if err != nil {
		return err
	}
	if err := definition.ValidateRegistration(); err != nil {
		return fmt.Errorf("AI agent %q: %w", agentID, err)
	}
	if err := definition.ProbeLiveModel(); err != nil {
		return fmt.Errorf("AI agent %q: %w", agentID, err)
	}
	var adapter voice.Adapter
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOBEYOND_VOICE_PROVIDER"))) {
	case "", "gemini":
		adapter = NewGeminiLiveAdapter(definition)
	case "grok":
		adapter = NewGrokLiveAdapter(definition)
	default:
		return fmt.Errorf("unsupported GOBEYOND_VOICE_PROVIDER %q (use gemini or grok)", os.Getenv("GOBEYOND_VOICE_PROVIDER"))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.adapters == nil {
		registry.adapters = map[string]voice.Adapter{}
	}
	if registry.definitions == nil {
		registry.definitions = map[string]agents.AIDefinition{}
	}
	if _, exists := registry.adapters[agentID]; exists {
		return fmt.Errorf("voice adapter for agent %q is already registered", agentID)
	}
	registry.adapters[agentID] = adapter
	registry.definitions[agentID] = definition
	return nil
}

// Lookup returns the registered voice adapter for agentID.
func (registry *VoiceRegistry) Lookup(agentID string) (voice.Adapter, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	adapter, ok := registry.adapters[strings.TrimSpace(agentID)]
	return adapter, ok
}

// Definition returns the AI definition registered for voice, if any.
func (registry *VoiceRegistry) Definition(agentID string) (agents.AIDefinition, bool) {
	if registry == nil {
		return agents.AIDefinition{}, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	definition, ok := registry.definitions[strings.TrimSpace(agentID)]
	return definition, ok
}
