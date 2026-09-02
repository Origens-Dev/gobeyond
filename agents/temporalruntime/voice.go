package temporalruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
)

// NewGeminiLiveAdapter returns the Live voice adapter for definition.
// G4 replaces this stub with the google.golang.org/genai Live implementation.
func NewGeminiLiveAdapter(definition agents.AIDefinition) voice.Adapter {
	return &geminiLiveAdapterStub{definition: definition}
}

type geminiLiveAdapterStub struct {
	definition agents.AIDefinition
}

func (adapter *geminiLiveAdapterStub) Start(context.Context, voice.StartConfig, <-chan []byte, chan<- []byte) (voice.SessionHandle, error) {
	if strings.TrimSpace(adapter.definition.AI.LiveModel) == "" {
		return nil, errors.New("AI agent LiveModel is required for voice")
	}
	return nil, errors.New("gemini live adapter is not implemented yet (G4)")
}
