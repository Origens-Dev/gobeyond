package temporalruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
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
