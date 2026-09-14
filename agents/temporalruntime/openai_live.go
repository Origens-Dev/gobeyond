package temporalruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/gorilla/websocket"
)

const DefaultOpenAILiveModel = "gpt-live-1"
const DefaultOpenAILiveBackendModel = "gpt-5.6-luna"

// OpenAILiveAdapter implements the Live API, not the Realtime wire protocol.
// Responses delegation runs reasoning/search; private functions remain local
// to the verified tool executor supplied by the host.
type OpenAILiveAdapter struct {
	definition agents.AIDefinition
	endpoint   string
}

func NewOpenAILiveAdapter(d agents.AIDefinition) *OpenAILiveAdapter {
	return &OpenAILiveAdapter{definition: d, endpoint: "wss://api.openai.com/v1/live/sessions"}
}

// SelectOpenAILiveFormat selects the shared wire format used by both directions.
func SelectOpenAILiveFormat(p voice.AudioPreferences) (voice.AudioFormat, error) {
	supported := func(f voice.AudioFormat) bool {
		return f.Channels == 1 && ((f.Encoding == voice.EncodingPCMU || f.Encoding == voice.EncodingPCMA) && f.SampleRate == 8000 || f.Encoding == voice.EncodingPCM16LE && (f.SampleRate == 16000 || f.SampleRate == 24000))
	}
	defaults := []voice.AudioFormat{{Encoding: voice.EncodingPCMU, SampleRate: 8000, Channels: 1}, {Encoding: voice.EncodingPCM16LE, SampleRate: 24000, Channels: 1}}
	input, output := p.InputFormats, p.OutputFormats
	if len(input) == 0 {
		input = defaults
	}
	if len(output) == 0 {
		output = defaults
	}
	for _, in := range input {
		for _, out := range output {
			if in == out && supported(in) {
				return in, nil
			}
		}
	}
	return voice.AudioFormat{}, errors.New("openai live requires a common supported input/output format")
}

func openAILiveTools(tools map[string]ai.Tool) []map[string]any {
	keys := make([]string, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]map[string]any, 0, len(keys))
	search := false
	for _, k := range keys {
		t := tools[k]
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = k
		}
		if isWebSearchTool(name) {
			search = true
			continue
		}
		result = append(result, map[string]any{"type": "function", "name": name, "description": t.Description, "parameters": t.InputSchema})
	}
	if search {
		result = append(result, map[string]any{"type": "web_search"})
	}
	return result
}

func (a *OpenAILiveAdapter) Start(ctx context.Context, cfg voice.StartConfig, in <-chan []byte, out chan<- voice.AudioFrame) (voice.SessionHandle, voice.StartResult, error) {
	if err := cfg.Actor.Validate(); err != nil {
		return nil, voice.StartResult{}, err
	}
	// Live has no authoritative output-audio-done event. A transfer requires
	// finite application audio plus the verified downstream playout barrier.
	if cfg.CallControl != nil {
		for _, name := range cfg.CallControl.ToolNames {
			if name != "hang_up" && cfg.CallControl.Announcement == nil {
				return nil, voice.StartResult{}, errors.New("OpenAI Live transfer requires an application-controlled announcement")
			}
		}
	}
	tools, err := controlTools(a.definition, cfg)
	if err != nil {
		return nil, voice.StartResult{}, err
	}
	// Native search is a capability, not a call-control function. Merge only
	// the already selected authored search tools, never arbitrary request names.
	for k, t := range voiceToolsFromDefinition(a.definition, cfg.EnabledToolIDs) {
		if isWebSearchTool(t.Name) || isWebSearchTool(k) {
			tools[k] = t
		}
	}
	format, err := SelectOpenAILiveFormat(cfg.AudioPreferences)
	if err != nil {
		return nil, voice.StartResult{}, err
	}
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return nil, voice.StartResult{}, errors.New("OPENAI_API_KEY is required for openai live")
	}
	model := strings.TrimSpace(cfg.VoiceModel)
	if model == "" {
		model = DefaultOpenAILiveModel
	}
	if model != DefaultOpenAILiveModel {
		return nil, voice.StartResult{}, fmt.Errorf("unsupported OpenAI Live model %q", model)
	}
	backend := strings.TrimSpace(cfg.VoiceBackendModel)
	if backend == "" {
		backend = strings.TrimSpace(os.Getenv("GOBEYOND_OPENAI_LIVE_BACKEND_MODEL"))
	}
	if backend == "" {
		backend = DefaultOpenAILiveBackendModel
	}
	name := strings.TrimSpace(cfg.VoiceName)
	if name == "" {
		name = "marin"
	}
	instructions := cfg.Instructions
	if instructions == "" {
		instructions = a.definition.AI.Instructions
	}
	conversation := cfg.ConversationInstructions
	if conversation == "" {
		conversation = "Speak naturally and concisely. Delegate requests requiring facts, search, private information, or actions to the backend. Never claim an action succeeded before the backend confirms it. Follow the application's conversation instructions:\n" + instructions
	}
	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(startCtx, a.endpoint, http.Header{"Authorization": []string{"Bearer " + key}})
	if err != nil {
		if resp != nil {
			return nil, voice.StartResult{}, fmt.Errorf("openai live connect: HTTP %d", resp.StatusCode)
		}
		return nil, voice.StartResult{}, fmt.Errorf("openai live connect: %w", err)
	}
	stop := context.AfterFunc(startCtx, func() { _ = conn.Close() })
	defer stop()
	h := &openAILiveHandle{conn: conn, cfg: cfg, tools: tools, in: in, out: out, format: format, model: model, backend: backend, closed: make(chan struct{}), seen: map[string]string{}}
	wire := grokWireFormat(format)
	wire["rate"] = format.SampleRate
	err = h.write(map[string]any{"type": "session.start", "session": map[string]any{"model": model, "instructions": conversation, "store": false, "audio": map[string]any{"format": wire, "output": map[string]any{"voice": name}}, "delegation": map[string]any{"type": "responses", "responses": map[string]any{"model": backend, "instructions": instructions, "tools": openAILiveTools(tools), "parallel_tool_calls": false}}}})
	if err != nil {
		_ = conn.Close()
		return nil, voice.StartResult{}, err
	}
	conn.SetReadLimit(2 << 20)
	for {
		_, raw, e := conn.ReadMessage()
		if e != nil {
			_ = conn.Close()
			return nil, voice.StartResult{}, fmt.Errorf("openai live startup: %w", e)
		}
		var event openAILiveEvent
		if e = json.Unmarshal(raw, &event); e != nil {
			_ = conn.Close()
			return nil, voice.StartResult{}, e
		}
		if event.Type == "error" || event.Type == "session.closed" {
			_ = conn.Close()
			return nil, voice.StartResult{}, fmt.Errorf("openai live startup rejected: %s", event.Error.Code)
		}
		if event.Type == "session.started" {
			if event.Session.ID == "" || event.Session.Model != model || event.Session.Audio.Format.Type != wire["type"] || event.Session.Audio.Format.Rate != format.SampleRate || event.Session.Audio.Output.Voice != name {
				_ = conn.Close()
				return nil, voice.StartResult{}, errors.New("invalid openai live startup acknowledgment")
			}
			h.providerID = event.Session.ID
			break
		}
	}
	if ctx.Err() != nil {
		_ = conn.Close()
		return nil, voice.StartResult{}, ctx.Err()
	}
	return h, voice.StartResult{InputFormat: format, OutputFormat: format}, nil
}

type openAILiveEvent struct {
	Type         string `json:"type"`
	Delta        string `json:"delta"`
	DelegationID string `json:"delegation_id"`
	Session      struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Audio struct {
			Format struct {
				Type string `json:"type"`
				Rate int    `json:"rate"`
			} `json:"format"`
			Output struct {
				Voice string `json:"voice"`
			} `json:"output"`
		} `json:"audio"`
	} `json:"session"`
	Usage struct {
		Seconds float64 `json:"seconds"`
	} `json:"usage"`
	Error struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
	Event *struct {
		Type     string `json:"type"`
		Response struct {
			ID     string `json:"id"`
			Model  string `json:"model"`
			Status string `json:"status"`
			Usage  struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
				Total  int64 `json:"total_tokens"`
			} `json:"usage"`
		} `json:"response"`
		Item struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Status    string `json:"status"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"item"`
	} `json:"event"`
}

type openAILiveBatch struct{ calls []grokFunctionCall }
type openAILiveAudio struct {
	frame voice.AudioFrame
	epoch uint64
}
type openAILiveHandle struct {
	conn                       *websocket.Conn
	cfg                        voice.StartConfig
	tools                      map[string]ai.Tool
	in                         <-chan []byte
	out                        chan<- voice.AudioFrame
	format                     voice.AudioFormat
	model, backend, providerID string
	mu                         sync.Mutex
	closeOnce                  sync.Once
	closed                     chan struct{}
	control                    liveControlGate
	audioEpoch                 atomic.Uint64
	barrierSeq                 atomic.Uint64
	seen                       map[string]string // reader-owned call-id -> immutable argument fingerprint
}

func (h *openAILiveHandle) write(v any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return h.conn.WriteJSON(v)
}
func (h *openAILiveHandle) Close() error {
	h.closeOnce.Do(func() {
		_ = h.write(map[string]any{"type": "session.close"})
		go func() {
			select {
			case <-h.closed:
			case <-time.After(15 * time.Second):
			}
			_ = h.conn.Close()
		}()
	})
	return nil
}
func (h *openAILiveHandle) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer h.conn.Close()
	defer close(h.closed)
	batches := make(chan openAILiveBatch, 8)
	frames := make(chan openAILiveAudio, 128)
	errs := make(chan error, 2)
	report := func(err error) {
		if err != nil {
			select {
			case errs <- err:
			default:
			}
			_ = h.conn.Close()
		}
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	// Cancellation stops media/tool workers immediately. Keep the sole socket
	// reader alive for the bounded session.close handshake and final usage.
	stop := context.AfterFunc(ctx, func() { _ = h.Close() })
	defer stop()
	wg.Add(3)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if err := h.emitAudio(ctx, frame); err != nil {
					report(err)
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		var residual []byte
		for {
			select {
			case <-ctx.Done():
				return
			case b, ok := <-h.in:
				if !ok {
					_ = h.Close()
					return
				}
				if h.control.stopped() {
					return
				}
				if h.format.Encoding == voice.EncodingPCM16LE {
					b = append(residual, b...)
					n := len(b) &^ 1
					residual = append([]byte(nil), b[n:]...)
					b = b[:n]
				}
				if len(b) > 0 {
					if err := h.write(map[string]any{"type": "session.input_audio.append", "audio": base64.StdEncoding.EncodeToString(b)}); err != nil {
						report(err)
						return
					}
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-batches:
				if err := h.execute(ctx, b.calls); err != nil {
					report(err)
					return
				}
			}
		}
	}()
	defer cancel() // cancel workers before waiting for them
	opening := strings.TrimSpace(os.Getenv("GOBEYOND_LIVE_OPENING_TURN"))
	if opening != "-" {
		if opening == "" {
			opening = "Greet the caller briefly now, then listen."
		}
		if err := h.write(map[string]any{"type": "session.instructions.append", "delegation_id": nil, "content": opening}); err != nil {
			return err
		}
	}
	pending := map[string][]grokFunctionCall{}
	activeResponses := map[string]string{}
	completed := map[string]bool{}
	searches := map[string]int64{}
	searchSeen := map[string]bool{}
	for {
		_, raw, err := h.conn.ReadMessage()
		if err != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			select {
			case e := <-errs:
				return e
			default:
			}
			if h.control.stopped() {
				return nil
			}
			return err
		}
		var e openAILiveEvent
		if err = json.Unmarshal(raw, &e); err != nil {
			return err
		}
		switch e.Type {
		case "session.output_audio.delta":
			if ctx.Err() != nil || h.control.stopped() {
				continue
			}
			b, err := base64.StdEncoding.DecodeString(e.Delta)
			if err != nil {
				return err
			}
			select {
			case frames <- openAILiveAudio{frame: voice.AudioFrame{Data: b}, epoch: h.audioEpoch.Load()}:
			default:
				return errors.New("OpenAI audio queue full")
			}
		case "session.usage.updated", "session.closed":
			if h.cfg.OnUsage != nil {
				h.cfg.OnUsage(voice.Usage{Model: h.model, Backend: "openai", DurationSeconds: e.Usage.Seconds, ProviderSessionID: h.providerID, Final: e.Type == "session.closed"})
			}
			if e.Type == "session.closed" {
				return nil
			}
		case "error":
			return fmt.Errorf("openai live error: %s (%s)", e.Error.Code, e.Error.Type)
		case "response.event":
			n := e.Event
			if n == nil {
				return errors.New("missing nested OpenAI response event")
			}
			if n.Type == "response.created" {
				if e.DelegationID == "" || n.Response.ID == "" {
					return errors.New("missing OpenAI delegation identity")
				}
				if previous := activeResponses[e.DelegationID]; previous != "" && previous != n.Response.ID && !completed[previous] {
					return errors.New("overlapping OpenAI responses in one delegation")
				}
				if len(activeResponses) >= 8192 {
					return errors.New("OpenAI delegation budget exhausted")
				}
				activeResponses[e.DelegationID] = n.Response.ID
			}
			responseID := activeResponses[e.DelegationID]
			if n.Type == "response.output_item.done" && responseID == "" {
				return errors.New("OpenAI output has no owning response")
			}
			if n.Type == "response.output_item.done" && n.Item.Type == "web_search_call" && n.Item.ID != "" && n.Item.Status == "completed" && !searchSeen[n.Item.ID] {
				if len(searchSeen) >= 4096 {
					return errors.New("OpenAI search budget exhausted")
				}
				searchSeen[n.Item.ID] = true
				searches[responseID]++
			}
			if n.Type == "response.output_item.done" && n.Item.Type == "function_call" && ctx.Err() == nil && !h.control.stopped() {
				item := n.Item
				if item.CallID == "" || item.Name == "" || e.DelegationID == "" || len(item.Arguments) > 16384 {
					return errors.New("invalid OpenAI function identity")
				}
				fingerprint := e.DelegationID + "\x00" + responseID + "\x00" + item.Name + "\x00" + item.Arguments
				if prev, ok := h.seen[item.CallID]; ok {
					if prev != fingerprint {
						return errors.New("conflicting OpenAI function replay")
					}
					continue
				}
				var args map[string]any
				if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil || args == nil {
					return errors.New("invalid OpenAI function arguments")
				}
				if len(h.seen) >= 256 {
					return errors.New("OpenAI function budget exhausted")
				}
				h.seen[item.CallID] = fingerprint
				pending[responseID] = append(pending[responseID], grokFunctionCall{CallID: item.CallID, Name: item.Name, Arguments: args})
			}
			if n.Type == "response.completed" || n.Type == "response.failed" || n.Type == "response.incomplete" {
				if n.Response.ID == "" {
					return errors.New("missing OpenAI response identity")
				}
				if completed[n.Response.ID] {
					continue
				}
				if responseID != n.Response.ID {
					return errors.New("OpenAI response completed outside its delegation")
				}
				if len(completed) >= 8192 {
					return errors.New("OpenAI response budget exhausted")
				}
				completed[n.Response.ID] = true
				if h.cfg.OnUsage != nil {
					u := n.Response.Usage
					model := n.Response.Model
					if model == "" {
						model = h.backend
					}
					h.cfg.OnUsage(voice.Usage{WebSearchCalls: searches[responseID], Model: model, Backend: "openai", PromptTokens: u.Input, CompletionTokens: u.Output, TotalTokens: u.Total, ProviderSessionID: h.providerID, ProviderResponseID: n.Response.ID, Final: true})
				}
				delete(searches, responseID)
				if n.Type != "response.completed" {
					return errors.New("OpenAI delegated response failed")
				}
				if ctx.Err() != nil || h.control.stopped() {
					continue
				}
				calls := pending[responseID]
				delete(pending, responseID)
				if len(calls) > 0 {
					select {
					case batches <- openAILiveBatch{calls: calls}:
					default:
						return errors.New("OpenAI tool queue full")
					}
				}
			}
		}
	}
}
func (h *openAILiveHandle) execute(ctx context.Context, calls []grokFunctionCall) error {
	for _, c := range calls {
		if h.control.stopped() {
			return nil
		}
		var result any
		var err error
		terminal := false
		toolCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var selected ai.Tool
		known := false
		for k, t := range h.tools {
			if k == c.Name || t.Name == c.Name {
				selected = t
				known = true
				break
			}
		}
		if !known {
			err = errors.New("tool unavailable")
		} else if validation := ai.ValidateToolInput(selected, c.Arguments); validation != nil {
			err = errors.New("invalid tool arguments")
		} else if h.cfg.CallControl != nil && callControlAllows(h.cfg, c.Name) {
			if !h.control.begin() {
				cancel()
				return nil
			}
			h.audioEpoch.Add(1)
			call := ai.ToolCall{ToolCallID: c.CallID, ToolName: c.Name, Input: c.Arguments}
			if c.Name != "hang_up" {
				err = h.announce(toolCtx, call)
			}
			if err == nil {
				result, terminal, err = invokeControl(toolCtx, h.cfg, call, h.barrierSeq.Add(1))
			}
			// Invalidate speech generated while the announcement/control was in
			// progress, including queued frames after a failed transfer.
			h.audioEpoch.Add(1)
			h.control.finish(terminal)
		} else if selected.Execute == nil {
			err = errors.New("tool unavailable")
		} else {
			result, err = selected.Execute(toolCtx, ai.ToolCall{ToolCallID: c.CallID, ToolName: c.Name, Input: c.Arguments}, ai.ToolExecutionOptions{Context: map[string]any{"gobeyondActor": h.cfg.Actor}})
			if err == nil {
				err = ai.ValidateToolOutput(selected, result)
			}
		}
		cancel()
		if terminal {
			_ = h.Close()
			return nil
		}
		output := map[string]any{"result": result}
		if err != nil {
			output = map[string]any{"error": "tool could not be completed"}
		}
		if err := h.write(map[string]any{"type": "response.item.create", "item": map[string]any{"type": "function_call_output", "call_id": c.CallID, "output": openAILiveToolOutput(output)}}); err != nil {
			return err
		}
	}
	return h.write(map[string]any{"type": "response.create"})
}

// Bound private tool results before putting them on the provider connection.
func openAILiveToolOutput(output any) string {
	b, err := json.Marshal(output)
	if err != nil || len(b) > 64*1024 {
		return `{"error":"tool result unavailable or too large"}`
	}
	return string(b)
}

// emitAudio fences queued provider audio against an application announcement.
func (h *openAILiveHandle) emitAudio(ctx context.Context, f openAILiveAudio) error {
	h.control.mu.Lock()
	defer h.control.mu.Unlock()
	if h.control.pending || h.control.stopped() || f.epoch != h.audioEpoch.Load() {
		return nil
	}
	select {
	case h.out <- f.frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *openAILiveHandle) announce(ctx context.Context, call ai.ToolCall) error {
	if h.cfg.CallControl.Announcement == nil {
		return errors.New("control announcement unavailable")
	}
	audio, err := h.cfg.CallControl.Announcement(ctx, call, h.format)
	if err != nil {
		return err
	}
	bytesPerSecond := h.format.SampleRate
	if h.format.Encoding == voice.EncodingPCM16LE {
		bytesPerSecond *= 2
	}
	if len(audio) == 0 || len(audio) > bytesPerSecond*5 || (h.format.Encoding == voice.EncodingPCM16LE && len(audio)%2 != 0) {
		return errors.New("invalid control announcement audio")
	}
	// Replace any unfinished model speech with the finite application clip.
	select {
	case h.out <- voice.AudioFrame{Interrupted: true}:
	case <-ctx.Done():
		return ctx.Err()
	}
	// 20ms frames keep the media bridge's queue bounded and preserve framing.
	frameSize := bytesPerSecond / 50
	for len(audio) > 0 {
		n := frameSize
		if n > len(audio) {
			n = len(audio)
		}
		select {
		case h.out <- voice.AudioFrame{Data: append([]byte(nil), audio[:n]...)}:
		case <-ctx.Done():
			return ctx.Err()
		}
		audio = audio[n:]
	}
	return nil
}
