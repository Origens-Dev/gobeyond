package temporalruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/gorilla/websocket"
)

func TestOpenAILiveSharedFormat(t *testing.T) {
	pcm := voice.AudioFormat{Encoding: voice.EncodingPCM16LE, SampleRate: 24000, Channels: 1}
	mulaw := voice.AudioFormat{Encoding: voice.EncodingPCMU, SampleRate: 8000, Channels: 1}
	if got, e := SelectOpenAILiveFormat(voice.AudioPreferences{InputFormats: []voice.AudioFormat{mulaw, pcm}, OutputFormats: []voice.AudioFormat{pcm}}); e != nil || got != pcm {
		t.Fatalf("format=%v err=%v", got, e)
	}
	if _, e := SelectOpenAILiveFormat(voice.AudioPreferences{InputFormats: []voice.AudioFormat{mulaw}, OutputFormats: []voice.AudioFormat{pcm}}); e == nil {
		t.Fatal("accepted mismatched provider formats")
	}
}
func TestOpenAILiveNativeSearchAuthorization(t *testing.T) {
	d := agents.DefineAI(agents.AIConfig{Tools: map[string]ai.Tool{"web-search": {Name: "web-search"}, "lookup": {Name: "lookup"}}})
	for _, enabled := range [][]string{{"lookup"}, {"lookup", "web_search"}} {
		tools := openAILiveTools(voiceToolsFromDefinition(d, enabled))
		search := 0
		for _, tool := range tools {
			if tool["type"] == "web_search" {
				search++
			}
			if tool["name"] == "web-search" {
				t.Fatal("duplicate custom search")
			}
		}
		if (search == 1) != (len(enabled) == 2) {
			t.Fatalf("tools=%v", tools)
		}
	}
}
func TestOpenAILiveDelegationAndFinalUsage(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "-")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var executions atomic.Int32
	d := agents.DefineAI(agents.AIConfig{Tools: map[string]ai.Tool{"lookup": {Name: "lookup", InputSchema: map[string]any{"type": "object"}, Execute: func(context.Context, ai.ToolCall, ai.ToolExecutionOptions) (any, error) {
		executions.Add(1)
		return "found", nil
	}}}})
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			serverErr <- e
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
		var start map[string]any
		if e = c.ReadJSON(&start); e != nil {
			serverErr <- e
			return
		}
		if start["type"] != "session.start" {
			t.Error("wrong API startup")
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "live_test")})
		_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": "delegation_test", "event": map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_test"}}})
		item := map[string]any{"type": "response.event", "delegation_id": "delegation_test", "event": map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": "call_test", "name": "lookup", "arguments": "{}"}}}
		_ = c.WriteJSON(item)
		_ = c.WriteJSON(item)
		_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": "delegation_test", "event": map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_test", "output": []any{}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}}})
		var result, continuation map[string]any
		if e = c.ReadJSON(&result); e != nil {
			serverErr <- e
			return
		}
		if result["type"] != "response.item.create" {
			t.Errorf("result=%v", result)
		}
		if e = c.ReadJSON(&continuation); e != nil {
			serverErr <- e
			return
		}
		if continuation["type"] != "response.create" {
			t.Errorf("continuation=%v", continuation)
		}
		_ = c.WriteJSON(map[string]any{"type": "session.usage.updated", "usage": map[string]any{"seconds": 2}})
		_ = c.WriteJSON(map[string]any{"type": "session.closed", "usage": map[string]any{"seconds": 3}})
		serverErr <- nil
	}))
	defer server.Close()
	a := NewOpenAILiveAdapter(d)
	a.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	var usage []voice.Usage
	h, _, e := a.Start(ctx, voice.StartConfig{Actor: agents.Actor{ID: "actor", Kind: "user"}, EnabledToolIDs: []string{"lookup"}, OnUsage: func(u voice.Usage) { usage = append(usage, u) }}, make(chan []byte), make(chan voice.AudioFrame, 8))
	if e != nil {
		t.Fatal(e)
	}
	if e = h.Run(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-serverErr; e != nil {
		t.Fatal(e)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions=%d", executions.Load())
	}
	if len(usage) != 3 || usage[0].TotalTokens != 5 || usage[2].DurationSeconds != 3 || !usage[2].Final {
		b, _ := json.Marshal(usage)
		t.Fatalf("usage=%s", b)
	}
}
func TestOpenAILiveStartupCancellation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, e = c.ReadMessage(); e != nil {
				return
			}
		}
	}))
	defer server.Close()
	a := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	a.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, e := a.Start(ctx, voice.StartConfig{Actor: agents.Actor{ID: "actor", Kind: "user"}}, nil, nil); e == nil {
		t.Fatal("startup ignored cancellation")
	}
}

func TestOpenAILiveCancellationCollectsFinalUsage(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "-")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var start map[string]any
		if c.ReadJSON(&start) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "live_cancel")})
		var end map[string]any
		if c.ReadJSON(&end) != nil {
			return
		}
		if end["type"] != "session.close" {
			t.Errorf("end=%v", end)
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.closed", "usage": map[string]any{"seconds": 1.25}})
	}))
	defer server.Close()
	a := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	a.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	var last voice.Usage
	h, _, err := a.Start(context.Background(), voice.StartConfig{Actor: agents.Actor{ID: "u", Kind: "user"}, OnUsage: func(u voice.Usage) { last = u }}, make(chan []byte), make(chan voice.AudioFrame))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = h.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !last.Final || last.DurationSeconds != 1.25 {
		t.Fatalf("usage=%+v", last)
	}
}

func openAITestStartedSession(start map[string]any, id string) map[string]any {
	session := start["session"].(map[string]any)
	session["id"] = id
	return session
}
