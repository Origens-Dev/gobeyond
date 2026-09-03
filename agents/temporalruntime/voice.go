package temporalruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"google.golang.org/genai"
)

const (
	// Vertex-oriented Live id; Gemini Developer API rejects this for bidiGenerateContent.
	defaultLiveModelFallbackVertex = "gemini-live-2.5-flash-native-audio"
	// Verified on generativelanguage.googleapis.com v1alpha (2026-09-02).
	defaultLiveModelFallbackGoogle = "gemini-2.5-flash-native-audio-preview-12-2025"
	pcmMIMEType                    = "audio/pcm;rate=16000"
)

// liveSession is the narrow genai Live session surface used by the adapter so
// unit tests can inject a fake without dialing Google.
type liveSession interface {
	SendRealtimeInput(input genai.LiveRealtimeInput) error
	SendClientContent(input genai.LiveClientContentInput) error
	SendToolResponse(input genai.LiveToolResponseInput) error
	Receive() (*genai.LiveServerMessage, error)
	Close() error
}

type liveDialer func(ctx context.Context, definition agents.AIDefinition, model string, config *genai.LiveConnectConfig) (liveSession, error)

// GeminiLiveAdapter implements voice.Adapter with google.golang.org/genai Live.
type GeminiLiveAdapter struct {
	definition agents.AIDefinition
	dial       liveDialer
}

// NewGeminiLiveAdapter returns the production Live adapter for definition.
func NewGeminiLiveAdapter(definition agents.AIDefinition) *GeminiLiveAdapter {
	return &GeminiLiveAdapter{definition: definition, dial: dialGenaiLive}
}

// Start opens a Live session and returns a handle that pumps PCM and tools.
func (adapter *GeminiLiveAdapter) Start(ctx context.Context, cfg voice.StartConfig, pcmIn <-chan []byte, pcmOut chan<- voice.AudioFrame) (voice.SessionHandle, voice.StartResult, error) {
	if adapter == nil {
		return nil, voice.StartResult{}, errors.New("gemini live adapter is required")
	}
	if err := cfg.Actor.Validate(); err != nil {
		return nil, voice.StartResult{}, err
	}
	voice.NormalizeSampleRates(&cfg)
	cfg.Instructions = agents.ResolveInstructions(cfg.Instructions, cfg.Metadata)
	if strings.TrimSpace(cfg.Instructions) == "" {
		cfg.Instructions = agents.ResolveInstructions(adapter.definition.AI.Instructions, cfg.Metadata)
	}
	cfg.VoiceName = agents.ResolveVoiceName(cfg.VoiceName, cfg.Metadata)
	if strings.TrimSpace(cfg.VoiceName) == "" {
		cfg.VoiceName = agents.ResolveVoiceName(adapter.definition.AI.VoiceName, cfg.Metadata)
	}

	model := strings.TrimSpace(adapter.definition.AI.LiveModel)
	if model == "" {
		return nil, voice.StartResult{}, errors.New("AI agent LiveModel is required for voice")
	}
	connectCfg := &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		Tools:              liveToolsFromDefinition(adapter.definition),
	}
	// Gemini Developer API rejects system_instruction parts with an empty
	// oneof (close 1007). Omit the field when instructions resolve empty.
	if instr := strings.TrimSpace(cfg.Instructions); instr != "" {
		connectCfg.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: instr}},
		}
	}
	if voiceName := strings.TrimSpace(cfg.VoiceName); voiceName != "" {
		connectCfg.SpeechConfig = &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: voiceName},
			},
		}
	}
	dial := adapter.dial
	if dial == nil {
		dial = dialGenaiLive
	}
	connectedModel := model
	session, err := dial(ctx, adapter.definition, model, connectCfg)
	if err != nil {
		fallback := strings.TrimSpace(os.Getenv("GOBEYOND_LIVE_MODEL_FALLBACK"))
		if fallback == "" {
			if strings.EqualFold(strings.TrimSpace(adapter.definition.AI.Inference), "google") {
				fallback = defaultLiveModelFallbackGoogle
			} else {
				fallback = defaultLiveModelFallbackVertex
			}
		}
		if fallback != model {
			session, err = dial(ctx, adapter.definition, fallback, connectCfg)
			if err == nil {
				connectedModel = fallback
			}
		}
		if err != nil {
			return nil, voice.StartResult{}, fmt.Errorf("gemini live connect: %w", err)
		}
	}
	// Kick an opening model turn so duplex/smoke gets downlink without
	// waiting on VAD over silence/tones. Use realtime text — not
	// SendClientContent — so gemini-3.1-flash-live-preview keeps accepting
	// subsequent SendRealtimeInput audio (mixing client_content + realtime
	// after TurnComplete leaves the session deaf to the mic).
	opening := strings.TrimSpace(os.Getenv("GOBEYOND_LIVE_OPENING_TURN"))
	if opening == "" {
		opening = "Please greet the caller briefly now."
	}
	if opening != "-" {
		if sendErr := session.SendRealtimeInput(genai.LiveRealtimeInput{Text: opening}); sendErr != nil {
			_ = session.Close()
			return nil, voice.StartResult{}, fmt.Errorf("gemini live opening turn: %w", sendErr)
		}
	}
	return &geminiLiveHandle{
			cfg:     cfg,
			session: session,
			tools:   adapter.definition.AI.Tools,
			model:   connectedModel,
			backend: liveUsageBackend(adapter.definition.AI.Inference),
			pcmIn:   pcmIn,
			pcmOut:  pcmOut,
		}, voice.StartResult{
			InputFormat:  voice.AudioFormat{Encoding: voice.EncodingPCM16LE, SampleRate: voice.DefaultPCMInSampleRate, Channels: 1},
			OutputFormat: voice.AudioFormat{Encoding: voice.EncodingPCM16LE, SampleRate: voice.DefaultPCMOutSampleRate, Channels: 1},
		}, nil
}

type geminiLiveHandle struct {
	cfg     voice.StartConfig
	session liveSession
	tools   map[string]ai.Tool
	model   string
	backend string
	pcmIn   <-chan []byte
	pcmOut  chan<- voice.AudioFrame

	closeOnce sync.Once
	closed    chan struct{}
	usageSeq  atomic.Uint64
}

func (handle *geminiLiveHandle) Run(ctx context.Context) error {
	if handle == nil || handle.session == nil {
		return errors.New("gemini live session is not started")
	}
	if handle.closed == nil {
		handle.closed = make(chan struct{})
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = handle.Close() }()

	sendErrCh := make(chan error, 1)
	recvErrCh := make(chan error, 1)
	go func() { sendErrCh <- handle.sendPCM(ctx) }()
	go func() { recvErrCh <- handle.receiveLoop(ctx) }()

	select {
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	case err := <-recvErrCh:
		cancel()
		<-sendErrCh
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	case err := <-sendErrCh:
		if err != nil {
			cancel()
			<-recvErrCh
			if !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		}
		// pcmIn closed: keep receiving model audio/tool calls until cancel or EOF.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvErrCh:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}
	}
}

func (handle *geminiLiveHandle) Close() error {
	var err error
	handle.closeOnce.Do(func() {
		if handle.closed != nil {
			close(handle.closed)
		}
		if handle.session != nil {
			err = handle.session.Close()
		}
	})
	return err
}

func (handle *geminiLiveHandle) sendPCM(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-handle.closed:
			return nil
		case chunk, ok := <-handle.pcmIn:
			if !ok {
				return nil
			}
			if len(chunk) == 0 {
				continue
			}
			if err := handle.session.SendRealtimeInput(genai.LiveRealtimeInput{
				Audio: &genai.Blob{Data: chunk, MIMEType: pcmMIMEType},
			}); err != nil {
				return err
			}
		}
	}
}

func (handle *geminiLiveHandle) receiveLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-handle.closed:
			return nil
		default:
		}
		message, err := handle.session.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if message == nil {
			continue
		}
		handle.reportUsage(message.UsageMetadata)
		if message.ServerContent != nil && message.ServerContent.Interrupted {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case handle.pcmOut <- voice.AudioFrame{Interrupted: true}:
			}
			// Interruption wins over co-present model content. Do not allow
			// stale audio from the interrupted turn into the playout queue.
			continue
		}
		if message.ToolCall != nil {
			if err := handle.dispatchToolCall(ctx, message.ToolCall); err != nil {
				return err
			}
			continue
		}
		if message.ServerContent == nil || message.ServerContent.ModelTurn == nil {
			continue
		}
		for _, part := range message.ServerContent.ModelTurn.Parts {
			if part == nil || part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case handle.pcmOut <- voice.AudioFrame{Data: append([]byte(nil), part.InlineData.Data...)}:
			}
		}
		if message.ServerContent.TurnComplete || message.ServerContent.GenerationComplete {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case handle.pcmOut <- voice.AudioFrame{TurnComplete: true}:
			}
		}
	}
}

func (handle *geminiLiveHandle) dispatchToolCall(ctx context.Context, call *genai.LiveServerToolCall) error {
	if call == nil {
		return nil
	}
	responses := make([]*genai.FunctionResponse, 0, len(call.FunctionCalls))
	for _, functionCall := range call.FunctionCalls {
		if functionCall == nil {
			continue
		}
		name := strings.TrimSpace(functionCall.Name)
		tool, ok := handle.lookupTool(name)
		if !ok || tool.Execute == nil {
			responses = append(responses, &genai.FunctionResponse{
				ID: functionCall.ID, Name: name,
				Response: map[string]any{"error": fmt.Sprintf("unknown tool %q", name)},
			})
			continue
		}
		result, err := tool.Execute(ctx, ai.ToolCall{
			ToolCallID: functionCall.ID, ToolName: name, Input: functionCall.Args,
		}, ai.ToolExecutionOptions{
			Context: map[string]any{"gobeyondActor": handle.cfg.Actor},
		})
		response := map[string]any{"result": result}
		if err != nil {
			response = map[string]any{"error": err.Error()}
		}
		responses = append(responses, &genai.FunctionResponse{
			ID: functionCall.ID, Name: name, Response: response,
		})
	}
	if len(responses) == 0 {
		return nil
	}
	return handle.session.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: responses})
}

func (handle *geminiLiveHandle) lookupTool(name string) (ai.Tool, bool) {
	if tool, ok := handle.tools[name]; ok {
		return tool, true
	}
	for key, tool := range handle.tools {
		if strings.TrimSpace(tool.Name) == name || key == name {
			return tool, true
		}
	}
	return ai.Tool{}, false
}

func (handle *geminiLiveHandle) reportUsage(meta *genai.UsageMetadata) {
	if handle == nil || handle.cfg.OnUsage == nil || meta == nil {
		return
	}
	prompt := int64(meta.PromptTokenCount) + int64(meta.ToolUsePromptTokenCount)
	completion := int64(meta.ResponseTokenCount) + int64(meta.ThoughtsTokenCount)
	total := int64(meta.TotalTokenCount)
	if prompt == 0 && completion == 0 && total == 0 {
		return
	}
	if prompt == 0 && completion == 0 && total > 0 {
		prompt = total
	}
	handle.usageSeq.Add(1)
	handle.cfg.OnUsage(voice.Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		Model:            handle.model,
		Backend:          handle.backend,
	})
}

func liveUsageBackend(inference string) string {
	switch strings.ToLower(strings.TrimSpace(inference)) {
	case "google":
		return "google"
	default:
		return "vertex"
	}
}

func liveToolsFromDefinition(definition agents.AIDefinition) []*genai.Tool {
	if len(definition.AI.Tools) == 0 {
		return nil
	}
	declarations := make([]*genai.FunctionDeclaration, 0, len(definition.AI.Tools))
	for key, tool := range definition.AI.Tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			name = key
		}
		decl := &genai.FunctionDeclaration{
			Name: name, Description: tool.Description,
		}
		if schema, ok := tool.InputSchema.(map[string]any); ok {
			decl.ParametersJsonSchema = schema
		}
		declarations = append(declarations, decl)
	}
	if len(declarations) == 0 {
		return nil
	}
	return []*genai.Tool{{FunctionDeclarations: declarations}}
}

func dialGenaiLive(ctx context.Context, definition agents.AIDefinition, model string, config *genai.LiveConnectConfig) (liveSession, error) {
	clientCfg, err := genaiClientConfig(definition.AI.Inference)
	if err != nil {
		return nil, err
	}
	client, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	session, err := client.Live.Connect(ctx, model, config)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func genaiClientConfig(inference string) (*genai.ClientConfig, error) {
	switch strings.ToLower(strings.TrimSpace(inference)) {
	case "vertex", "":
		// Hosted Live dogfood defaults to Vertex + ADC. Empty Inference still
		// selects Vertex so catalog text agents that add LiveModel without
		// restating Inference keep the secure hosted path.
		project := strings.TrimSpace(firstEnv("GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"))
		location := strings.TrimSpace(firstEnv("GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_REGION"))
		if project == "" || location == "" {
			return nil, errors.New("vertex Live requires GOOGLE_CLOUD_PROJECT and GOOGLE_CLOUD_LOCATION (or GOOGLE_CLOUD_REGION)")
		}
		return &genai.ClientConfig{
			Backend:     genai.BackendVertexAI,
			Project:     project,
			Location:    location,
			HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta1"},
		}, nil
	case "google":
		apiKey := strings.TrimSpace(firstEnv("GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"))
		if apiKey == "" {
			return nil, errors.New("google Live requires GOOGLE_API_KEY, GEMINI_API_KEY, or GOOGLE_GENERATIVE_AI_API_KEY")
		}
		return &genai.ClientConfig{
			Backend:     genai.BackendGeminiAPI,
			APIKey:      apiKey,
			HTTPOptions: genai.HTTPOptions{APIVersion: "v1alpha"},
		}, nil
	default:
		return nil, fmt.Errorf("Live Inference %q is not supported; use vertex or google", inference)
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
