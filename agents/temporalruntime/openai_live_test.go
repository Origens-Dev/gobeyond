package temporalruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

func TestOpenAILiveOpeningPromptsSpeechAfterAcknowledgment(t *testing.T) {
	for _, alreadySpeaking := range []bool{false, true} {
		t.Run(fmt.Sprintf("already-speaking-%t", alreadySpeaking), func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "test-key")
			t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			commands := make(chan map[string]any, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer c.Close()
				var start, opening map[string]any
				if c.ReadJSON(&start) != nil {
					return
				}
				c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "opening-test")})
				if c.ReadJSON(&opening) != nil {
					return
				}
				commands <- opening
				if alreadySpeaking {
					c.WriteJSON(map[string]any{"type": "session.output_transcript.delta", "delta": "Hello"})
				}
				c.WriteJSON(map[string]any{"type": "session.instructions.appended", "client_event_id": "unrelated"})
				c.WriteJSON(map[string]any{"type": "session.instructions.appended", "client_event_id": "gobeyond_opening"})
				c.WriteJSON(map[string]any{"type": "session.instructions.appended", "client_event_id": "gobeyond_opening"})
				c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
				for {
					var cmd map[string]any
					if c.ReadJSON(&cmd) != nil {
						break
					}
					commands <- cmd
				}
				close(commands)
			}))
			defer server.Close()
			a := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
			a.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
			h, _, err := a.Start(ctx, voice.StartConfig{Actor: agents.Actor{ID: "test", Kind: "service"}}, make(chan []byte), make(chan voice.AudioFrame, 4))
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			_ = h.Run(ctx)
			var got []map[string]any
			for cmd := range commands {
				got = append(got, cmd)
			}
			want := 2
			if alreadySpeaking {
				want = 1
			}
			if len(got) != want {
				t.Fatalf("commands=%v want count=%d", got, want)
			}
			if got[0]["type"] != "session.instructions.append" || got[0]["event_id"] != "gobeyond_opening" {
				t.Fatalf("opening=%v", got[0])
			}
			if !alreadySpeaking && got[1]["type"] != "session.commentary.append" {
				t.Fatalf("speech trigger=%v", got[1])
			}
		})
	}
}

// Provider evidence tests use exact raw fake-server frames. They establish the
// decoder and callback boundary, not live provider support or billing finality.
func runOpenAIProviderEvidence(t *testing.T, frames []string, cfg voice.StartConfig) error {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "test-provider-evidence")
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
		if c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "provider_evidence_session")}) != nil {
			return
		}
		for _, raw := range frames {
			if c.WriteMessage(websocket.TextMessage, []byte(raw)) != nil {
				return
			}
		}
	}))
	defer server.Close()
	adapter := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	adapter.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	cfg.Actor = agents.Actor{ID: "actor", Kind: "user"}
	cfg.EnabledToolIDs = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, _, err := adapter.Start(ctx, cfg, make(chan []byte), make(chan voice.AudioFrame, 8))
	if err != nil {
		return err
	}
	return h.Run(ctx)
}
func providerEvidenceDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestOpenAILiveProviderClosurePreservesMetricPresence(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		known     bool
	}{
		{"missing usage", `{"type":"session.closed"}`, false},
		{"missing seconds", `{"type":"session.closed","usage":{}}`, false},
		{"null seconds", `{"type":"session.closed","usage":{"seconds":null}}`, false},
		{"explicit zero", `{ "type": "session.closed", "usage": {"seconds":0} }`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []voice.ProviderEvent
			var legacy []voice.Usage
			var order []string
			err := runOpenAIProviderEvidence(t, []string{tc.raw}, voice.StartConfig{OnProviderEvent: func(ctx context.Context, e voice.ProviderEvent) error {
				if err := e.Validate(); err != nil {
					t.Error(err)
				}
				events = append(events, e.Clone())
				order = append(order, e.Kind)
				if e.DurationSeconds != nil {
					*e.DurationSeconds = 999
				}
				return nil
			}, OnUsage: func(u voice.Usage) { legacy = append(legacy, u); order = append(order, "legacy") }})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.known {
				want = 2
			}
			if len(events) != want || len(legacy) != 1 || legacy[0].DurationSeconds != 0 || !legacy[0].Final {
				t.Fatalf("events=%+v legacy=%+v", events, legacy)
			}
			last := events[len(events)-1]
			if last.Kind != voice.ProviderEventSessionClosed || last.DurationSeconds != nil || last.InputTokens != nil || last.OutputTokens != nil || last.TotalTokens != nil || last.Final || last.PayloadDigest != providerEvidenceDigest(tc.raw) || last.ProviderSessionID != "provider_evidence_session" || last.SourceEventType != "session.closed" {
				t.Fatalf("invalid termination fact: %+v", last)
			}
			if tc.known {
				if first := events[0]; first.Kind != voice.ProviderEventUsage || first.DurationSeconds == nil || *first.DurationSeconds != 0 || !first.Final || first.PayloadDigest != last.PayloadDigest {
					t.Fatalf("explicit zero lost: %+v", first)
				}
			}
			if order[len(order)-1] != "legacy" {
				t.Fatalf("legacy ran before typed observations: %v", order)
			}
		})
	}
}

func TestOpenAILiveProviderResponsePresenceAndLegacyIsolation(t *testing.T) {
	created := `{"type":"response.event","delegation_id":"delegation","event":{"type":"response.created","response":{"id":"response"}}}`
	complete := `{"type":"response.event","delegation_id":"delegation","event":{"type":"response.completed","response":{"id":"response","model":"reported-backend","usage":{"input_tokens":0,"total_tokens":7}}}}`
	closed := `{"type":"session.closed"}`
	var events []voice.ProviderEvent
	var legacy []voice.Usage
	err := runOpenAIProviderEvidence(t, []string{created, complete, closed}, voice.StartConfig{OnProviderEvent: func(_ context.Context, e voice.ProviderEvent) error {
		events = append(events, e.Clone())
		if e.InputTokens != nil {
			*e.InputTokens = 99
		}
		return nil
	}, OnUsage: func(u voice.Usage) { legacy = append(legacy, u) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || len(legacy) != 2 {
		t.Fatalf("events=%+v legacy=%+v", events, legacy)
	}
	e := events[0]
	if e.Kind != voice.ProviderEventUsage || e.InputTokens == nil || *e.InputTokens != 0 || e.OutputTokens != nil || e.TotalTokens == nil || *e.TotalTokens != 7 || e.DurationSeconds != nil || !e.Final || e.ProviderResponseID != "response" || e.Model != "reported-backend" || e.SourceEventType != "response.event" || e.PayloadDigest != providerEvidenceDigest(complete) {
		t.Fatalf("partial metrics changed: %+v", e)
	}
	if legacy[0].PromptTokens != 0 || legacy[0].CompletionTokens != 0 || legacy[0].TotalTokens != 7 {
		t.Fatal("observer mutated decoder-owned metrics")
	}
}

func TestOpenAILiveProviderObserverFailureStopsBeforeLegacy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
		failAt int
	}{
		{"usage", []string{`{"type":"session.usage.updated","usage":{"seconds":2}}`, `{"type":"session.closed"}`}, 1},
		{"closed without usage", []string{`{"type":"session.closed"}`}, 1},
		{"closed after sameframe usage", []string{`{"type":"session.closed","usage":{"seconds":2}}`}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New("durable observer unavailable")
			calls, legacy := 0, 0
			err := runOpenAIProviderEvidence(t, tc.frames, voice.StartConfig{OnProviderEvent: func(context.Context, voice.ProviderEvent) error {
				calls++
				if calls == tc.failAt {
					return sentinel
				}
				return nil
			}, OnUsage: func(voice.Usage) { legacy++ }})
			if !errors.Is(err, sentinel) || calls != tc.failAt || legacy != 0 {
				t.Fatalf("err=%v typed=%d legacy=%d", err, calls, legacy)
			}
		})
	}
}

func TestOpenAILiveProviderEOFDoesNotInventClosure(t *testing.T) {
	raw := `{"type":"session.usage.updated","usage":{"seconds":2}}`
	var events []voice.ProviderEvent
	err := runOpenAIProviderEvidence(t, []string{raw}, voice.StartConfig{OnProviderEvent: func(_ context.Context, e voice.ProviderEvent) error { events = append(events, e.Clone()); return nil }})
	if err == nil {
		t.Fatal("unexpected clean EOF")
	}
	if len(events) != 1 || events[0].Kind != voice.ProviderEventUsage || events[0].Final {
		t.Fatalf("EOF invented terminal evidence: %+v", events)
	}
}

func TestOpenAILiveProviderSessionMismatchFailsBeforeObservation(t *testing.T) {
	for _, raw := range []string{`{"type":"session.closed","session":{"id":"foreign"}}`, `{"type":"session.closed","session":{"model":"foreign"}}`} {
		calls, legacy := 0, 0
		err := runOpenAIProviderEvidence(t, []string{raw}, voice.StartConfig{OnProviderEvent: func(context.Context, voice.ProviderEvent) error { calls++; return nil }, OnUsage: func(voice.Usage) { legacy++ }})
		if err == nil || calls != 0 || legacy != 0 {
			t.Fatalf("mismatched session/model accepted: %v typed=%d legacy=%d", err, calls, legacy)
		}
	}
}

type openAIProviderEvidenceContextKey struct{}

func TestOpenAILiveProviderFinalEvidenceSurvivesMediaCancellation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-evidence-cancel")
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
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "cancel_evidence")})
		var command map[string]any
		if c.ReadJSON(&command) != nil {
			return
		}
		if command["type"] != "session.close" {
			t.Error("wrong close handshake command")
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed","usage":{"seconds":1.25}}`))
	}))
	defer server.Close()
	adapter := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	adapter.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	var events []voice.ProviderEvent
	h, _, err := adapter.Start(context.Background(), voice.StartConfig{Actor: agents.Actor{ID: "actor", Kind: "user"}, EnabledToolIDs: []string{}, OnProviderEvent: func(ctx context.Context, e voice.ProviderEvent) error {
		if ctx.Err() != nil || ctx.Value(openAIProviderEvidenceContextKey{}) != "trace-value" {
			t.Error("final observation lost detached context values")
		}
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > 2*time.Second {
			t.Errorf("evidence context not bounded to two seconds: %v %v", ok, remaining)
		}
		events = append(events, e.Clone())
		return nil
	}}, make(chan []byte), make(chan voice.AudioFrame))
	if err != nil {
		t.Fatal(err)
	}
	parent := context.WithValue(context.Background(), openAIProviderEvidenceContextKey{}, "trace-value")
	ctx, cancel := context.WithCancel(parent)
	cancel()
	if err = h.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != voice.ProviderEventUsage || events[0].DurationSeconds == nil || *events[0].DurationSeconds != 1.25 || !events[0].Final || events[1].Kind != voice.ProviderEventSessionClosed {
		t.Fatalf("actual final close evidence lost: %+v", events)
	}
}

func TestOpenAILiveProviderLocalCloseWithoutServerAckIsNotTermination(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-local-close")
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "-")
	commandReceived := make(chan struct{})
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
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "local_close_evidence")})
		var command map[string]any
		if c.ReadJSON(&command) == nil && command["type"] == "session.close" {
			close(commandReceived)
		}
		// Deliberately close the socket without a provider session.closed event.
	}))
	defer server.Close()
	adapter := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	adapter.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	events := 0
	h, _, err := adapter.Start(context.Background(), voice.StartConfig{Actor: agents.Actor{ID: "actor", Kind: "user"}, OnProviderEvent: func(context.Context, voice.ProviderEvent) error { events++; return nil }}, make(chan []byte), make(chan voice.AudioFrame))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-commandReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("close command not sent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = h.Run(ctx); err == nil {
		t.Fatal("local close unexpectedly proved clean provider end")
	}
	if events != 0 {
		t.Fatal("local socket close fabricated provider termination")
	}
}

func TestOpenAILiveProviderObserverTimeoutDoesNotAcknowledgeClosure(t *testing.T) {
	calls, legacy := 0, 0
	err := runOpenAIProviderEvidence(t, []string{`{"type":"session.closed","usage":{"seconds":2}}`}, voice.StartConfig{
		OnProviderEvent: func(ctx context.Context, e voice.ProviderEvent) error {
			calls++
			if e.Kind != voice.ProviderEventUsage {
				t.Error("timed-out usage was followed by closure")
			}
			<-ctx.Done()
			return ctx.Err()
		},
		OnUsage: func(voice.Usage) { legacy++ },
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || legacy != 0 {
		t.Fatalf("timeout acknowledged evidence: err=%v typed=%d legacy=%d", err, calls, legacy)
	}
}
