package temporalruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/gorilla/websocket"
)

const defaultGrokVoiceModel = "grok-voice-think-fast-2.0"

type GrokLiveAdapter struct{ definition agents.AIDefinition }

func NewGrokLiveAdapter(definition agents.AIDefinition) *GrokLiveAdapter {
	return &GrokLiveAdapter{definition: definition}
}

func (adapter *GrokLiveAdapter) Start(ctx context.Context, cfg voice.StartConfig, audioIn <-chan []byte, audioOut chan<- voice.AudioFrame) (voice.SessionHandle, voice.StartResult, error) {
	if adapter == nil {
		return nil, voice.StartResult{}, errors.New("grok live adapter is required")
	}
	key := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	if key == "" {
		return nil, voice.StartResult{}, errors.New("XAI_API_KEY is required when GOBEYOND_VOICE_PROVIDER=grok")
	}
	model := strings.TrimSpace(os.Getenv("GOBEYOND_GROK_VOICE_MODEL"))
	if model == "" {
		model = defaultGrokVoiceModel
	}
	u := url.URL{Scheme: "wss", Host: "api.x.ai", Path: "/v1/realtime", RawQuery: "model=" + url.QueryEscape(model)}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), map[string][]string{"Authorization": {"Bearer " + key}})
	if err != nil {
		return nil, voice.StartResult{}, fmt.Errorf("grok voice connect: %w", err)
	}
	h := &grokLiveHandle{conn: conn, audioIn: audioIn, audioOut: audioOut}
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
	if err := h.writeJSON(map[string]any{"type": "session.update", "session": map[string]any{
		"voice": voiceName, "instructions": instructions, "turn_detection": map[string]any{"type": "server_vad"},
		"audio": map[string]any{
			"input":  map[string]any{"format": map[string]any{"type": "audio/pcmu", "rate": 8000}, "transport": "json"},
			"output": map[string]any{"format": map[string]any{"type": "audio/pcmu", "rate": 8000}, "transport": "json"},
		},
	}}); err != nil {
		_ = conn.Close()
		return nil, voice.StartResult{}, err
	}
	return h, voice.StartResult{
		InputFormat:  voice.AudioFormat{Encoding: voice.EncodingPCMU, SampleRate: 8000, Channels: 1},
		OutputFormat: voice.AudioFormat{Encoding: voice.EncodingPCMU, SampleRate: 8000, Channels: 1},
	}, nil
}

type grokLiveHandle struct {
	conn     *websocket.Conn
	audioIn  <-chan []byte
	audioOut chan<- voice.AudioFrame
	mu       sync.Mutex
}

func (h *grokLiveHandle) writeJSON(v any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn.WriteJSON(v)
}

func (h *grokLiveHandle) Run(ctx context.Context) error {
	writeErr := make(chan error, 1)
	go func() { writeErr <- h.SendAudio(ctx) }()
	go func() { <-ctx.Done(); _ = h.Close() }()
	for {
		kind, data, err := h.conn.ReadMessage()
		if err != nil {
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
		switch e.Type {
		case "session.updated":
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
		case "response.done":
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
