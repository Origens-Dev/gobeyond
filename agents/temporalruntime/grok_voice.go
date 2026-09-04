package temporalruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/gorilla/websocket"
)

const defaultGrokVoiceModel = "grok-voice-think-fast-2.0"

const defaultGrokOpeningTurn = "Please greet the caller briefly now."

type GrokLiveAdapter struct{ definition agents.AIDefinition }

func NewGrokLiveAdapter(definition agents.AIDefinition) *GrokLiveAdapter {
	return &GrokLiveAdapter{definition: definition}
}

func (adapter *GrokLiveAdapter) Start(ctx context.Context, cfg voice.StartConfig, audioIn <-chan []byte, audioOut chan<- voice.AudioFrame) (voice.SessionHandle, voice.StartResult, error) {
	if adapter == nil {
		return nil, voice.StartResult{}, errors.New("grok live adapter is required")
	}
	if err := cfg.Actor.Validate(); err != nil {
		return nil, voice.StartResult{}, err
	}
	key := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	if key == "" {
		return nil, voice.StartResult{}, errors.New("XAI_API_KEY is required when GOBEYOND_VOICE_PROVIDER=grok")
	}
	model := strings.TrimSpace(cfg.VoiceModel)
	if model == "" {
		model = strings.TrimSpace(os.Getenv("GOBEYOND_GROK_VOICE_MODEL"))
	}
	if model == "" {
		model = defaultGrokVoiceModel
	}
	u := url.URL{Scheme: "wss", Host: "api.x.ai", Path: "/v1/realtime", RawQuery: "model=" + url.QueryEscape(model)}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), map[string][]string{"Authorization": {"Bearer " + key}})
	if err != nil {
		return nil, voice.StartResult{}, fmt.Errorf("grok voice connect: %w", err)
	}
	h := &grokLiveHandle{
		conn: conn, audioIn: audioIn, audioOut: audioOut,
		cfg: cfg, tools: voiceToolsFromDefinition(adapter.definition, cfg.EnabledToolIDs),
	}
	instructions := strings.TrimSpace(cfg.Instructions)
	if instructions == "" {
		instructions = strings.TrimSpace(adapter.definition.AI.Instructions)
	}
	voiceName := strings.TrimSpace(cfg.VoiceName)
	if voiceName == "" {
		voiceName = strings.TrimSpace(adapter.definition.AI.VoiceName)
	}
	if voiceName == "" {
		voiceName = "eve"
	}
	inputFormat := grokAudioFormat(cfg.AudioPreferences.InputFormats)
	outputFormat := grokAudioFormat(cfg.AudioPreferences.OutputFormats)
	if err := h.writeJSON(map[string]any{"type": "session.update", "session": map[string]any{
		"voice": voiceName, "instructions": instructions, "turn_detection": map[string]any{"type": "server_vad"},
		"tools": grokSessionTools(h.tools),
		"audio": map[string]any{
			"input":  map[string]any{"format": grokWireFormat(inputFormat), "transport": "json"},
			"output": map[string]any{"format": grokWireFormat(outputFormat), "transport": "json"},
		},
	}}); err != nil {
		_ = conn.Close()
		return nil, voice.StartResult{}, err
	}
	if opening, ok := grokOpeningResponse(); ok {
		if err := h.writeJSON(opening); err != nil {
			_ = conn.Close()
			return nil, voice.StartResult{}, fmt.Errorf("grok voice opening response: %w", err)
		}
	}
	return h, voice.StartResult{
		InputFormat: inputFormat, OutputFormat: outputFormat,
	}, nil
}

func grokOpeningResponse() (map[string]any, bool) {
	opening := strings.TrimSpace(os.Getenv("GOBEYOND_LIVE_OPENING_TURN"))
	if opening == "-" {
		return nil, false
	}
	if opening == "" {
		opening = defaultGrokOpeningTurn
	}
	return map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"instructions": opening,
		},
	}, true
}

func grokAudioFormat(preferred []voice.AudioFormat) voice.AudioFormat {
	fallback := voice.AudioFormat{Encoding: voice.EncodingPCMU, SampleRate: 8000, Channels: 1}
	for _, format := range preferred {
		if format.Validate() != nil {
			continue
		}
		if format.Encoding == voice.EncodingOpus {
			format.SampleRate, format.Channels = 24000, 1
			return format
		}
		if format.Encoding == voice.EncodingPCMU || format.Encoding == voice.EncodingPCMA {
			return format
		}
	}
	return fallback
}

func grokWireFormat(format voice.AudioFormat) map[string]any {
	typeName := "audio/pcmu"
	switch format.Encoding {
	case voice.EncodingOpus:
		typeName = "audio/opus"
	case voice.EncodingPCMA:
		typeName = "audio/pcma"
	case voice.EncodingPCM16LE:
		typeName = "audio/pcm"
	}
	result := map[string]any{"type": typeName}
	if typeName == "audio/pcm" {
		result["rate"] = format.SampleRate
	}
	return result
}

func grokSessionTools(tools map[string]ai.Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	nativeWebSearch := false
	functions := make([]map[string]any, 0, len(tools))
	for key, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			name = key
		}
		if isWebSearchTool(name) {
			nativeWebSearch = true
			continue
		}
		parameters, _ := tool.InputSchema.(map[string]any)
		functions = append(functions, map[string]any{"type": "function", "name": name, "description": tool.Description, "parameters": parameters})
	}
	if nativeWebSearch {
		return append([]map[string]any{{"type": "web_search"}}, functions...)
	}
	return functions
}

func isWebSearchTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "web_search" || name == "web-search" || name == "search_web"
}

type grokLiveHandle struct {
	conn     *websocket.Conn
	audioIn  <-chan []byte
	audioOut chan<- voice.AudioFrame
	cfg      voice.StartConfig
	tools    map[string]ai.Tool
	mu       sync.Mutex
	toolWG   sync.WaitGroup
	barrier  atomic.Uint64
}

func (h *grokLiveHandle) writeJSON(v any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn.WriteJSON(v)
}

func (h *grokLiveHandle) Run(ctx context.Context) error {
	defer h.toolWG.Wait()
	writeErr := make(chan error, 1)
	go func() { writeErr <- h.SendAudio(ctx) }()
	go func() { <-ctx.Done(); _ = h.Close() }()
	var pending []grokFunctionCall
	for {
		kind, data, err := h.conn.ReadMessage()
		if err != nil {
			h.logReadError(err)
			return err
		}
		select {
		case err := <-writeErr:
			if err != nil {
				return err
			}
			writeErr = nil
		default:
		}
		if kind == websocket.BinaryMessage {
			select {
			case h.audioOut <- voice.AudioFrame{Data: data}:
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		var e struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("grok voice event: %w", err)
		}
		h.logProviderEvent(e.Type)
		switch e.Type {
		case "session.updated", "response.created", "response.output_audio.done":
		case "response.output_audio.delta", "response.audio.delta":
			b, err := base64.StdEncoding.DecodeString(e.Delta)
			if err != nil {
				return err
			}
			select {
			case h.audioOut <- voice.AudioFrame{Data: b}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case "input_audio_buffer.speech_started":
			select {
			case h.audioOut <- voice.AudioFrame{Interrupted: true}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case "response.function_call_arguments.done":
			call, err := decodeGrokFunctionCall(data)
			if err != nil {
				return err
			}
			pending = append(pending, call)
		case "response.done":
			if len(pending) > 0 {
				calls := append([]grokFunctionCall(nil), pending...)
				pending = nil
				h.toolWG.Add(1)
				go func() {
					defer h.toolWG.Done()
					if err := h.completeFunctionCalls(ctx, calls); err != nil && ctx.Err() == nil {
						logGrokToolError(h, err)
					}
				}()
				continue
			}
			select {
			case h.audioOut <- voice.AudioFrame{TurnComplete: true}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case "error":
			return fmt.Errorf("grok voice provider error: %s", string(data))
		}
	}
}

func (h *grokLiveHandle) logProviderEvent(eventType string) {
	switch eventType {
	case "session.updated", "response.created", "response.done",
		"response.output_audio.done", "input_audio_buffer.speech_started",
		"input_audio_buffer.speech_stopped", "error":
		log.Printf("grok voice provider event session=%s type=%s", strings.TrimSpace(h.cfg.SessionID), eventType)
	}
}

func (h *grokLiveHandle) logReadError(err error) {
	if err == nil {
		return
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		log.Printf("grok voice websocket closed session=%s code=%d text=%s",
			strings.TrimSpace(h.cfg.SessionID), closeErr.Code, closeErr.Text)
		return
	}
	log.Printf("grok voice websocket read failed session=%s err=%v",
		strings.TrimSpace(h.cfg.SessionID), err)
}

type grokFunctionCall struct {
	CallID    string
	Name      string
	Arguments map[string]any
}

func decodeGrokFunctionCall(data []byte) (grokFunctionCall, error) {
	var event struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return grokFunctionCall{}, fmt.Errorf("grok function call: %w", err)
	}
	var args map[string]any
	if strings.TrimSpace(event.Arguments) != "" {
		if err := json.Unmarshal([]byte(event.Arguments), &args); err != nil {
			return grokFunctionCall{}, fmt.Errorf("grok function call arguments: %w", err)
		}
	}
	return grokFunctionCall{CallID: strings.TrimSpace(event.CallID), Name: strings.TrimSpace(event.Name), Arguments: args}, nil
}

func (h *grokLiveHandle) completeFunctionCalls(ctx context.Context, calls []grokFunctionCall) error {
	if len(calls) == 0 {
		return nil
	}
	toolCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	outputs := make([]map[string]any, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call grokFunctionCall) {
			defer wg.Done()
			tool, ok := h.lookupTool(call.Name)
			if !ok || tool.Execute == nil {
				outputs[i] = map[string]any{"call_id": call.CallID, "output": map[string]any{"error": fmt.Sprintf("unknown tool %q", call.Name)}}
				return
			}
			result, err := tool.Execute(toolCtx, ai.ToolCall{ToolCallID: call.CallID, ToolName: call.Name, Input: call.Arguments}, ai.ToolExecutionOptions{
				Context: map[string]any{"gobeyondActor": h.cfg.Actor},
			})
			output := map[string]any{"result": result}
			if err != nil {
				output = map[string]any{"error": err.Error()}
			}
			outputs[i] = map[string]any{"call_id": call.CallID, "output": output}
		}(i, call)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if h.cfg.OnPlayoutBarrier != nil {
		if err := h.cfg.OnPlayoutBarrier(ctx, h.barrier.Add(1)); err != nil {
			return err
		}
	}
	for _, output := range outputs {
		if output == nil {
			continue
		}
		if err := h.writeJSON(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type":    "function_call_output",
				"call_id": output["call_id"],
				"output":  mustJSON(output["output"]),
			},
		}); err != nil {
			return err
		}
	}
	return h.writeJSON(map[string]any{"type": "response.create"})
}

func (h *grokLiveHandle) lookupTool(name string) (ai.Tool, bool) {
	if tool, ok := h.tools[name]; ok {
		return tool, true
	}
	for key, tool := range h.tools {
		if strings.TrimSpace(tool.Name) == name || key == name {
			return tool, true
		}
	}
	return ai.Tool{}, false
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"error":"encode tool output"}`
	}
	return string(data)
}

func logGrokToolError(handle *grokLiveHandle, _ error) {
	// The receive loop owns provider reads. Closing the socket wakes it so a
	// failed barrier/write cannot leave the call silently waiting forever.
	if handle != nil {
		_ = handle.Close()
	}
}

func (h *grokLiveHandle) SendAudio(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b, ok := <-h.audioIn:
			if !ok {
				return nil
			}
			if err := h.writeJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(b)}); err != nil {
				return err
			}
		}
	}
}

func (h *grokLiveHandle) Close() error {
	if h == nil || h.conn == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn.Close()
}
