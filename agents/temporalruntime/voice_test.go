package temporalruntime

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"google.golang.org/genai"
)

type fakeLiveSession struct {
	mu          sync.Mutex
	inputs      []genai.LiveRealtimeInput
	clientTurns []genai.LiveClientContentInput
	responses   []genai.LiveToolResponseInput
	messages    chan *genai.LiveServerMessage
	closed      chan struct{}
}

func newFakeLiveSession(messages ...*genai.LiveServerMessage) *fakeLiveSession {
	session := &fakeLiveSession{
		messages: make(chan *genai.LiveServerMessage, len(messages)+1),
		closed:   make(chan struct{}),
	}
	for _, message := range messages {
		session.messages <- message
	}
	return session
}

func (session *fakeLiveSession) SendRealtimeInput(input genai.LiveRealtimeInput) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.inputs = append(session.inputs, input)
	return nil
}

func (session *fakeLiveSession) SendClientContent(input genai.LiveClientContentInput) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.clientTurns = append(session.clientTurns, input)
	return nil
}

func (session *fakeLiveSession) SendToolResponse(input genai.LiveToolResponseInput) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.responses = append(session.responses, input)
	return nil
}

func (session *fakeLiveSession) Receive() (*genai.LiveServerMessage, error) {
	select {
	case <-session.closed:
		return nil, io.EOF
	case message, ok := <-session.messages:
		if !ok {
			return nil, io.EOF
		}
		return message, nil
	}
}

func (session *fakeLiveSession) Close() error {
	select {
	case <-session.closed:
	default:
		close(session.closed)
	}
	return nil
}

func TestGeminiLiveAdapterPumpsPCMAndTools(t *testing.T) {
	var toolSeen agents.Actor
	tool := agents.DefineTool(agents.ToolConfig{Name: "lookup"}, func(_ context.Context, actor agents.Actor, input map[string]any) (map[string]any, error) {
		toolSeen = actor
		return map[string]any{"ok": true, "q": input["q"]}, nil
	})
	provider := ai.NewMockProvider()
	provider.LanguageModels["tool-model"] = ai.NewMockLanguageModel("tool-model")
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, Model: "tool-model", ToolModel: "tool-model", LiveModel: "gemini-live",
		Provider: provider, VoiceName: "Kore", Instructions: "Base.",
		Tools: map[string]ai.Tool{"lookup": tool},
	})

	fake := newFakeLiveSession(&genai.LiveServerMessage{
		ToolCall: &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{{
			ID: "call-1", Name: "lookup", Args: map[string]any{"q": "sports"},
		}}},
	}, &genai.LiveServerMessage{
		ServerContent: &genai.LiveServerContent{
			ModelTurn: &genai.Content{Parts: []*genai.Part{{
				InlineData: &genai.Blob{Data: []byte{0x10, 0x20}, MIMEType: pcmMIMEType},
			}}},
		},
	})

	adapter := &GeminiLiveAdapter{
		definition: definition,
		dial: func(context.Context, agents.AIDefinition, string, *genai.LiveConnectConfig) (liveSession, error) {
			return fake, nil
		},
	}

	pcmIn := make(chan []byte, 1)
	pcmOut := make(chan []byte, 1)

	handle, err := adapter.Start(context.Background(), voice.StartConfig{
		AgentID: "operator", SessionID: "sess", RunID: "run",
		Actor:    agents.Actor{ID: "user-1", Kind: "user", Metadata: map[string]string{"network_id": "net-1"}},
		Metadata: map[string]string{"instructions": "Overlay.", "voice_name": "Puck"},
	}, pcmIn, pcmOut)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- handle.Run(ctx) }()

	pcmIn <- []byte{0x01, 0x02}

	deadline := time.Now().Add(time.Second)
	for {
		fake.mu.Lock()
		gotInput := len(fake.inputs) == 1
		fake.mu.Unlock()
		if gotInput {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for pcm input")
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case out := <-pcmOut:
		if string(out) != string([]byte{0x10, 0x20}) {
			t.Fatalf("pcm out = %v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pcm out")
	}
	close(pcmIn)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	var pcmSeen bool
	for _, in := range fake.inputs {
		if in.Audio != nil && string(in.Audio.Data) == string([]byte{0x01, 0x02}) {
			pcmSeen = true
		}
	}
	if !pcmSeen {
		t.Fatalf("inputs = %#v", fake.inputs)
	}
	if len(fake.responses) != 1 || fake.responses[0].FunctionResponses[0].Name != "lookup" {
		t.Fatalf("tool responses = %#v", fake.responses)
	}
	if toolSeen.ID != "user-1" || toolSeen.Metadata["network_id"] != "net-1" {
		t.Fatalf("tool actor = %#v", toolSeen)
	}
}

func TestGeminiLiveAdapterReportsUsageMetadata(t *testing.T) {
	provider := ai.NewMockProvider()
	provider.LanguageModels["tool-model"] = ai.NewMockLanguageModel("tool-model")
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, Model: "tool-model", ToolModel: "tool-model",
		LiveModel: "gemini-live-test", Inference: "google", Provider: provider,
	})
	fake := newFakeLiveSession(&genai.LiveServerMessage{
		UsageMetadata: &genai.UsageMetadata{
			PromptTokenCount:        11,
			ResponseTokenCount:      4,
			ToolUsePromptTokenCount: 2,
			ThoughtsTokenCount:      1,
			TotalTokenCount:         18,
		},
	})
	adapter := &GeminiLiveAdapter{
		definition: definition,
		dial: func(context.Context, agents.AIDefinition, string, *genai.LiveConnectConfig) (liveSession, error) {
			return fake, nil
		},
	}
	pcmIn := make(chan []byte)
	pcmOut := make(chan []byte)
	got := make(chan voice.Usage, 1)
	handle, err := adapter.Start(context.Background(), voice.StartConfig{
		AgentID: "call-operator", SessionID: "vs_sess", RunID: "vx_exec",
		Actor:   agents.Actor{ID: "user-1", Kind: "user"},
		OnUsage: func(u voice.Usage) { got <- u },
	}, pcmIn, pcmOut)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- handle.Run(ctx) }()

	var usage voice.Usage
	select {
	case usage = <-got:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage")
	}
	if usage.PromptTokens != 13 || usage.CompletionTokens != 5 || usage.TotalTokens != 18 {
		t.Fatalf("usage=%+v", usage)
	}
	if usage.Model != "gemini-live-test" || usage.Backend != "google" {
		t.Fatalf("model/backend=%+v", usage)
	}

	close(pcmIn)
	_ = fake.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRegisterVoiceStoresAdapter(t *testing.T) {
	provider := ai.NewMockProvider()
	provider.LanguageModels["tool-model"] = ai.NewMockLanguageModel("tool-model")
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, Model: "tool-model", ToolModel: "tool-model", LiveModel: "gemini-live",
		Provider: provider, Revision: "rev-1",
	})
	registry := NewVoiceRegistry()
	if err := RegisterVoice(registry, "operator", definition); err != nil {
		t.Fatal(err)
	}
	adapter, ok := registry.Lookup("operator")
	if !ok || adapter == nil {
		t.Fatal("expected registered adapter")
	}
	RetainVoiceRegistry(registry)
	if ProcessVoiceRegistry() != registry {
		t.Fatal("process registry not retained")
	}
}

func TestGenaiClientConfigMapsInference(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "proj")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	cfg, err := genaiClientConfig("vertex")
	if err != nil || cfg.Backend != genai.BackendVertexAI || cfg.Project != "proj" {
		t.Fatalf("vertex config = %#v, err = %v", cfg, err)
	}
	t.Setenv("GOOGLE_API_KEY", "key")
	cfg, err = genaiClientConfig("google")
	if err != nil || cfg.Backend != genai.BackendGeminiAPI || cfg.APIKey != "key" {
		t.Fatalf("google config = %#v, err = %v", cfg, err)
	}
	if _, err := genaiClientConfig("anthropic"); err == nil {
		t.Fatal("expected unsupported inference error")
	}
}
