package temporalruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"google.golang.org/genai"
)

func terminalFixture(t *testing.T) voicecontract.TerminalResult {
	t.Helper()
	raw, e := os.ReadFile("../voicecontract/testdata/terminal.json")
	if e != nil {
		t.Fatal(e)
	}
	var r voicecontract.TerminalResult
	if e = voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func terminalConfig(t *testing.T) voice.StartConfig {
	drained := false
	return voice.StartConfig{OnPlayoutBarrier: func(_ context.Context, id uint64) error {
		if id == 0 {
			t.Fatal("zero barrier")
		}
		drained = true
		return nil
	}, CallControl: &voice.CallControlConfig{ToolNames: []string{"dial_contact"}, Execute: func(context.Context, ai.ToolCall) (any, error) {
		if !drained {
			t.Fatal("command before drain")
		}
		return nil, &voice.TerminalHandoff{Result: terminalFixture(t)}
	}}}
}
func TestGeminiTrustedTerminalSuppressesEverything(t *testing.T) {
	session := newFakeLiveSession()
	out := make(chan voice.AudioFrame, 4)
	h := &geminiLiveHandle{session: session, cfg: terminalConfig(t), pcmOut: out}
	call := &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{{ID: "one", Name: "dial_contact"}}}
	if e := h.dispatchToolCall(context.Background(), call); e != nil {
		t.Fatal(e)
	}
	h.toolWG.Wait()
	if !h.control.stopped() {
		t.Fatal("not terminal")
	}
	if e := h.sendRealtimeInput(genai.LiveRealtimeInput{Text: "continue"}); e != nil {
		t.Fatal(e)
	}
	_ = h.sendToolResponse(genai.LiveToolResponseInput{})
	_ = h.control.emit(context.Background(), out, voice.AudioFrame{Data: []byte{1}})
	if e := h.dispatchToolCall(context.Background(), call); e != nil {
		t.Fatal(e)
	}
	h.toolWG.Wait()
	if len(session.inputs) != 0 || len(session.responses) != 0 || len(out) != 0 {
		t.Fatal("output after terminal")
	}
}

type controlGrokFake struct {
	mu     sync.Mutex
	writes []any
	closed bool
}

func (f *controlGrokFake) WriteJSON(v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, v)
	return nil
}
func (f *controlGrokFake) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (f *controlGrokFake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
func TestGrokTrustedTerminalNoContinuation(t *testing.T) {
	conn := &controlGrokFake{}
	h := &grokLiveHandle{conn: conn, cfg: terminalConfig(t)}
	if e := h.completeFunctionCalls(context.Background(), []grokFunctionCall{{CallID: "one", Name: "dial_contact"}}); e != nil {
		t.Fatal(e)
	}
	_ = h.writeJSON(map[string]any{"type": "response.create"})
	if !conn.closed || len(conn.writes) != 0 {
		t.Fatal("provider continuation after terminal")
	}
}
func TestControlBatchAndBarrierFailureNeverExecute(t *testing.T) {
	cfg := terminalConfig(t)
	cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) { t.Fatal("unexpected command"); return nil, nil }
	cfg.OnPlayoutBarrier = func(context.Context, uint64) error { return errors.New("not drained") }
	if _, terminal, e := invokeControl(context.Background(), cfg, ai.ToolCall{ToolName: "dial_contact"}, 1); e == nil || terminal {
		t.Fatal("failed barrier accepted")
	}
	h := &grokLiveHandle{cfg: cfg}
	if e := h.completeFunctionCalls(context.Background(), []grokFunctionCall{{}, {}}); e == nil {
		t.Fatal("batch accepted")
	}
}
func TestTerminalJSONIsNormalToolData(t *testing.T) {
	cfg := terminalConfig(t)
	cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) {
		return map[string]any{"terminal": true, "state": "ringing"}, nil
	}
	_, terminal, e := invokeControl(context.Background(), cfg, ai.ToolCall{ToolName: "dial_contact"}, 1)
	if e != nil || terminal {
		t.Fatal("JSON terminated session")
	}
}
func TestControlFailureRestoresAudioAfterResponse(t *testing.T) {
	session := newFakeLiveSession()
	out := make(chan voice.AudioFrame, 4)
	entered, release := make(chan struct{}), make(chan struct{})
	cfg := terminalConfig(t)
	cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) {
		close(entered)
		<-release
		return nil, errors.New("busy")
	}
	h := &geminiLiveHandle{session: session, cfg: cfg, pcmOut: out}
	_ = h.dispatchToolCall(context.Background(), &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{{ID: "one", Name: "dial_contact"}}})
	<-entered
	_ = h.control.emit(context.Background(), out, voice.AudioFrame{Data: []byte{1}})
	if len(out) != 0 {
		t.Fatal("audio leaked while command pending")
	}
	close(release)
	h.toolWG.Wait()
	_ = h.control.emit(context.Background(), out, voice.AudioFrame{Data: []byte{2}})
	if len(out) != 1 || len(session.responses) != 1 || h.control.stopped() {
		t.Fatal("normal failure did not restore live session")
	}
}

type blockedGrokConnection struct {
	started, closed chan struct{}
	once            sync.Once
}

func (f *blockedGrokConnection) WriteJSON(any) error               { close(f.started); <-f.closed; return io.EOF }
func (f *blockedGrokConnection) ReadMessage() (int, []byte, error) { <-f.closed; return 0, nil, io.EOF }
func (f *blockedGrokConnection) Close() error                      { f.once.Do(func() { close(f.closed) }); return nil }
func TestTerminalCloseDoesNotWaitForWriterMutex(t *testing.T) {
	f := &blockedGrokConnection{started: make(chan struct{}), closed: make(chan struct{})}
	h := &grokLiveHandle{conn: f}
	done := make(chan struct{})
	go func() { _ = h.writeJSON(map[string]any{}); close(done) }()
	<-f.started
	h.control.finish(true)
	if e := h.Close(); e != nil {
		t.Fatal(e)
	}
	<-done
}

func TestControlTurnBudgetFencesFifthTurnAndTools(t *testing.T) {
	gate := liveControlGate{maxTurns: 4}
	out := make(chan voice.AudioFrame, 9)
	for range 4 {
		if err := gate.emit(context.Background(), out, voice.AudioFrame{Data: []byte{1}}); err != nil {
			t.Fatal(err)
		}
		if err := gate.emit(context.Background(), out, voice.AudioFrame{TurnComplete: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := gate.emit(context.Background(), out, voice.AudioFrame{Data: []byte{2}}); err == nil {
		t.Fatal("fifth turn emitted")
	}
	// Speech budget must not silently drop dial_contact after the opening kick
	// and a short operator dialogue.
	if !gate.begin() {
		t.Fatal("control tool blocked after speech turn budget")
	}
	gate.finish(false)
	if len(out) != 8 {
		t.Fatalf("frames=%d", len(out))
	}
}

func TestCallControlLiveToolsDeclareDialAndRead(t *testing.T) {
	read := agents.DefineToolWithCall(agents.ToolConfig{
		Name: "search_operator_directory", Description: "lookup",
		InputSchema:     map[string]any{"type": "object", "properties": map[string]any{}},
		VoiceRemoteRead: &agents.VoiceReadPolicy{MaxResultBytes: 1024},
	}, func(context.Context, agents.Actor, ai.ToolCall, map[string]any) (any, error) {
		return map[string]any{"results": []any{}}, nil
	})
	dial := agents.DefineTool(agents.ToolConfig{
		Name: "dial_contact", Description: "dial",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{}},
		VoiceControl: &agents.VoiceToolPolicy{DestinationClasses: []string{"extension"}, TargetKinds: []string{"line"}, InputModes: []string{"destination_id"}, HandoffMode: "blind"},
	}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		return nil, errors.New("not via authored execute")
	})
	def := agents.DefineAI(agents.AIConfig{
		Tools: map[string]agents.AITool{"search_operator_directory": read, "dial_contact": dial},
	})
	selected, err := controlTools(def, voice.StartConfig{
		OnPlayoutBarrier: func(context.Context, uint64) error { return nil },
		CallControl: &voice.CallControlConfig{
			MaxAssistantTurns: 4,
			ToolNames:         []string{"dial_contact"},
			Execute:           func(context.Context, ai.ToolCall) (any, error) { return nil, nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := selected["dial_contact"]; !ok {
		t.Fatal("dial_contact missing from control selection")
	}
	if _, ok := selected["search_operator_directory"]; !ok {
		t.Fatal("search_operator_directory missing from control selection")
	}
	// Ordinary filter strips both policies; CallControl Live path must not.
	if tools := liveToolsFromDefinition(agents.AIDefinition{AI: agents.AIConfig{Tools: selected}}, nil); tools != nil {
		t.Fatal("ordinary liveToolsFromDefinition must strip control/read tools")
	}
	live := liveToolsFromSelected(selected)
	if live == nil || len(live) == 0 || live[0].FunctionDeclarations == nil {
		t.Fatal("selected live tools missing declarations")
	}
	names := map[string]bool{}
	for _, d := range live[0].FunctionDeclarations {
		names[d.Name] = true
		if d.ParametersJsonSchema != nil {
			t.Fatalf("%s still uses ParametersJsonSchema", d.Name)
		}
		if d.Parameters == nil || d.Parameters.Type != genai.TypeObject {
			t.Fatalf("%s missing typed Parameters: %#v", d.Name, d.Parameters)
		}
	}
	if !names["dial_contact"] || !names["search_operator_directory"] {
		t.Fatalf("declared=%v", names)
	}
}

func TestLiveParametersSchemaFlattensDialOneOf(t *testing.T) {
	schema := map[string]any{
		"oneOf": []any{
			map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"destination_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				},
				"required": []string{"destination_id"},
			},
			map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"phone_number": map[string]any{"type": "string", "maxLength": 16},
				},
				"required": []string{"phone_number"},
			},
		},
	}
	got := liveParametersSchema(schema)
	if got == nil || got.Type != genai.TypeObject {
		t.Fatalf("schema=%#v", got)
	}
	if len(got.Required) != 0 {
		t.Fatalf("flattened union must leave required empty: %v", got.Required)
	}
	if got.MinProperties == nil || *got.MinProperties != 1 {
		t.Fatalf("minProperties=%v", got.MinProperties)
	}
	if got.Properties["destination_id"] == nil || got.Properties["destination_id"].Type != genai.TypeString {
		t.Fatalf("destination_id=%#v", got.Properties["destination_id"])
	}
	if got.Properties["destination_id"].MinLength != nil || got.Properties["destination_id"].MaxLength != nil {
		t.Fatalf("Live schema must omit string length bounds: %#v", got.Properties["destination_id"])
	}
	if got.Properties["phone_number"] == nil || got.Properties["phone_number"].Type != genai.TypeString {
		t.Fatalf("phone_number=%#v", got.Properties["phone_number"])
	}
	live := liveToolsFromSelected(map[string]ai.Tool{
		"dial_contact": {Name: "dial_contact", Description: "dial", InputSchema: schema, Execute: func(context.Context, ai.ToolCall, ai.ToolExecutionOptions) (any, error) {
			return nil, nil
		}},
	})
	if live == nil || len(live) == 0 || len(live[0].FunctionDeclarations) != 1 {
		t.Fatalf("live=%#v", live)
	}
	decl := live[0].FunctionDeclarations[0]
	if decl.ParametersJsonSchema != nil {
		t.Fatal("ParametersJsonSchema must not be set for Live")
	}
	if decl.Parameters == nil || decl.Parameters.Properties["destination_id"] == nil {
		t.Fatalf("parameters=%#v", decl.Parameters)
	}
}

func TestCallControlSkipsDefaultOpeningKick(t *testing.T) {
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "")
	fake := &fakeLiveSession{}
	dial := agents.DefineTool(agents.ToolConfig{
		Name: "dial_contact", Description: "dial",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{}},
		VoiceControl: &agents.VoiceToolPolicy{DestinationClasses: []string{"extension"}, TargetKinds: []string{"line"}, InputModes: []string{"destination_id"}, HandoffMode: "blind"},
	}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		return nil, errors.New("not via authored execute")
	})
	def := agents.DefineAI(agents.AIConfig{
		LiveModel: "gemini-live-test",
		Tools:     map[string]agents.AITool{"dial_contact": dial},
	})
	adapter := &GeminiLiveAdapter{
		definition: def,
		dial: func(_ context.Context, _ agents.AIDefinition, _ string, cfg *genai.LiveConnectConfig) (liveSession, error) {
			if cfg == nil || len(cfg.Tools) == 0 {
				t.Fatal("expected Live tools")
			}
			return fake, nil
		},
	}
	pcmIn := make(chan []byte)
	pcmOut := make(chan voice.AudioFrame, 1)
	cfg := terminalConfig(t)
	cfg.Instructions = "Open with a greeting."
	cfg.AgentID = "call-operator"
	cfg.SessionID = "vs_test"
	cfg.Actor = agents.Actor{ID: "line-1", Kind: "line"}
	_, _, err := adapter.Start(context.Background(), cfg, pcmIn, pcmOut)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, input := range fake.inputs {
		if strings.TrimSpace(input.Text) != "" {
			t.Fatalf("CallControl must not send default opening text: %#v", input.Text)
		}
	}
}
