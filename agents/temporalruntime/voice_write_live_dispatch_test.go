package temporalruntime

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"google.golang.org/genai"
)

// independentlyNamedVoiceWrite is an app mutation that is not leave_text_message.
// Candlestick owns that name; Live filtering must stay on VoiceWritePolicy.
// BindVoiceDurableDispatcher here is a trusted-host assertion on this test
// Execute; it does not wrap Maglev /execute-tool or make Execute durable.
func independentlyNamedVoiceWrite(t *testing.T, calls *atomic.Int32, result map[string]any) ai.Tool {
	t.Helper()
	schema, output := voiceWriteClosedSchemas()
	tool := agents.DefineTool(agents.ToolConfig{
		Name: "archive_desk_note", Description: "Archive a short desk note.",
		VoiceWrite: true, InputSchema: schema, OutputSchema: output,
	}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls.Add(1)
		return result, nil
	})
	if tool.Name == "leave_text_message" || !agents.VoiceWritePolicy(tool) {
		t.Fatal("write dispatch must key off VoiceWritePolicy, not leave_text_message")
	}
	bound, err := agents.BindVoiceDurableDispatcher(tool)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func TestGeminiLiveOrdinaryVoiceWriteNeverInvokesHandler(t *testing.T) {
	var calls atomic.Int32
	schema, output := voiceWriteClosedSchemas()
	tool := agents.DefineTool(agents.ToolConfig{
		Name: "archive_desk_note", Description: "Archive a short desk note.",
		VoiceWrite: true, InputSchema: schema, OutputSchema: output,
	}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls.Add(1)
		return map[string]any{"ok": true}, nil
	})
	session := newFakeLiveSession()
	h := &geminiLiveHandle{
		session: session,
		cfg:     hangUpBesideWrite(t),
		tools:   map[string]ai.Tool{tool.Name: tool},
	}
	call := &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{{
		ID: "write-1", Name: "archive_desk_note", Args: map[string]any{"q": "x"},
	}}}
	if err := h.dispatchToolCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { h.toolWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Gemini ordinary write dispatch did not finish")
	}
	if calls.Load() != 0 {
		t.Fatalf("ordinary VoiceWrite handler invoked calls=%d", calls.Load())
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.responses) != 1 || len(session.responses[0].FunctionResponses) != 1 {
		t.Fatalf("Gemini FunctionResponse missing: %#v", session.responses)
	}
	got := session.responses[0].FunctionResponses[0]
	if _, hadErr := got.Response["error"]; !hadErr {
		t.Fatalf("ordinary VoiceWrite must be rejected: %#v", got.Response)
	}
}

func hangUpBesideWrite(t *testing.T) voice.StartConfig {
	t.Helper()
	return voice.StartConfig{
		Actor:            agents.Actor{ID: "user-1", Kind: "user"},
		SessionID:        "sess-1",
		OnPlayoutBarrier: func(context.Context, uint64) error { return nil },
		CallControl: &voice.CallControlConfig{
			MaxAssistantTurns: 4,
			ToolNames:         []string{"hang_up"},
			Execute: func(context.Context, ai.ToolCall) (any, error) {
				t.Fatal("VoiceWrite must not use the hang_up CallControl executor")
				return nil, nil
			},
		},
	}
}

func TestGeminiLiveWriteInvocationReachesBoundExecute(t *testing.T) {
	var calls atomic.Int32
	want := map[string]any{"ok": true}
	tool := independentlyNamedVoiceWrite(t, &calls, want)
	session := newFakeLiveSession()
	h := &geminiLiveHandle{
		session: session,
		cfg:     hangUpBesideWrite(t),
		tools:   map[string]ai.Tool{tool.Name: tool},
	}
	call := &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{{
		ID: "write-1", Name: "archive_desk_note", Args: map[string]any{"q": "x"},
	}}}
	if err := h.dispatchToolCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { h.toolWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Gemini write dispatch did not finish")
	}
	if calls.Load() != 1 {
		t.Fatalf("Execute calls=%d want 1 (trusted-host assertion on existing Execute)", calls.Load())
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.responses) != 1 || len(session.responses[0].FunctionResponses) != 1 {
		t.Fatalf("Gemini FunctionResponse missing: %#v", session.responses)
	}
	got := session.responses[0].FunctionResponses[0]
	if got.Name != "archive_desk_note" || got.ID != "write-1" {
		t.Fatalf("response identity %#v", got)
	}
	if _, hadErr := got.Response["error"]; hadErr {
		t.Fatalf("Gemini returned error instead of Execute result: %#v", got.Response)
	}
	result, _ := got.Response["result"].(map[string]any)
	if result["ok"] != true {
		t.Fatalf("Gemini result %#v", got.Response)
	}
}

func TestGrokLiveWriteInvocationReachesBoundExecute(t *testing.T) {
	var calls atomic.Int32
	want := map[string]any{"ok": true}
	tool := independentlyNamedVoiceWrite(t, &calls, want)
	conn := &controlGrokFake{}
	h := &grokLiveHandle{
		conn:  conn,
		cfg:   hangUpBesideWrite(t),
		tools: map[string]ai.Tool{tool.Name: tool},
	}
	if err := h.completeFunctionCalls(context.Background(), []grokFunctionCall{{
		CallID: "write-1", Name: "archive_desk_note", Arguments: map[string]any{"q": "x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Execute calls=%d want 1 (trusted-host assertion on existing Execute)", calls.Load())
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) != 2 {
		t.Fatalf("Grok writes=%d want function_call_output + response.create", len(conn.writes))
	}
	raw, err := json.Marshal(conn.writes[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `"function_call_output"`) || !strings.Contains(body, `"write-1"`) {
		t.Fatalf("Grok function_call_output missing: %s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Fatalf("Grok returned error instead of Execute result: %s", body)
	}
	if !strings.Contains(body, `"ok":true`) && !strings.Contains(body, `"ok\":true`) {
		t.Fatalf("Grok result missing: %s", body)
	}
}

func TestOpenAILiveWriteInvocationReachesBoundExecute(t *testing.T) {
	var calls atomic.Int32
	want := map[string]any{"ok": true}
	tool := independentlyNamedVoiceWrite(t, &calls, want)
	h, events, _ := openAIControlTestHandle(t)
	h.cfg = hangUpBesideWrite(t)
	h.tools = map[string]ai.Tool{tool.Name: tool}
	if err := h.execute(context.Background(), []grokFunctionCall{{
		CallID: "write-1", Name: "archive_desk_note", Arguments: map[string]any{"q": "x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Execute calls=%d want 1 (trusted-host assertion on existing Execute)", calls.Load())
	}
	var output string
	deadline := time.After(2 * time.Second)
	for output == "" {
		select {
		case ev := <-events:
			if ev["type"] == "response.item.create" {
				item, _ := ev["item"].(map[string]any)
				if item["type"] == "function_call_output" && item["call_id"] == "write-1" {
					output, _ = item["output"].(string)
				}
			}
		case <-deadline:
			t.Fatal("OpenAI function_call_output not written")
		}
	}
	if strings.Contains(output, `"error"`) {
		t.Fatalf("OpenAI returned error instead of Execute result: %s", output)
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		t.Fatalf("OpenAI output %s", output)
	}
	result, _ := parsed["result"].(map[string]any)
	if result["ok"] != true {
		t.Fatalf("OpenAI result %#v", parsed)
	}
}
