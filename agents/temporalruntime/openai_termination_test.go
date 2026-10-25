package temporalruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/gorilla/websocket"
)

func startTerminationTest(t *testing.T, cfg voice.StartConfig, serve func(*websocket.Conn)) *openAILiveHandle {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "test-termination")
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "-")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var start map[string]any
		if c.ReadJSON(&start) != nil {
			return
		}
		if c.WriteJSON(map[string]any{"type": "session.started", "session": openAITestStartedSession(start, "termination-session")}) != nil {
			return
		}
		serve(c)
	}))
	t.Cleanup(server.Close)
	adapter := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	adapter.endpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	cfg.Actor = agents.Actor{Kind: "service", ID: "termination-test"}
	cfg.EnabledToolIDs = []string{}
	h, _, err := adapter.Start(context.Background(), cfg, make(chan []byte), make(chan voice.AudioFrame))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.(*openAILiveHandle).closeLocal() })
	return h.(*openAILiveHandle)
}
func waitTerminationTest(t *testing.T, h *openAILiveHandle) voice.SessionTermination {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outcome, err := h.WaitTermination(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}
func assertLocalTerminationTest(t *testing.T, o voice.SessionTermination) {
	t.Helper()
	if !o.ConsumerReturned || !o.WorkersExited || !o.LocalCloseAttempted || o.Provider != "openai" || o.ProviderSessionID != "termination-session" {
		t.Fatalf("missing local termination acknowledgement: %+v", o)
	}
}

func TestOpenAILiveTerminationPreservesUnfinishedResponseAndCallbackAcknowledgements(t *testing.T) {
	raw := `{"type":"session.closed","usage":{"seconds":0}}`
	h := startTerminationTest(t, voice.StartConfig{OnProviderEvent: func(context.Context, voice.ProviderEvent) error { return nil }}, func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.event","delegation_id":"delegation","event":{"type":"response.created","response":{"id":"unfinished"}}}`))
		_ = c.WriteMessage(websocket.TextMessage, []byte(raw))
	})
	if err := h.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	o := waitTerminationTest(t, h)
	assertLocalTerminationTest(t, o)
	if !o.ProviderClosed || o.ProviderClosedPayloadDigest != openAIProviderPayloadDigest([]byte(raw)) || !o.CallbackConfigured || o.CallbackAttempts != 2 || o.CallbackAcknowledged != 2 || len(o.OutstandingResponseIDs) != 1 || o.OutstandingResponseIDs[0] != "unfinished" || o.RunError != "" || o.ReadError != "" {
		t.Fatalf("incorrect termination evidence: %+v", o)
	}
	o.OutstandingResponseIDs[0] = "mutated"
	again := waitTerminationTest(t, h)
	if again.OutstandingResponseIDs[0] != "unfinished" {
		t.Fatal("wait aliases sealed inventory")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	afterClose := waitTerminationTest(t, h)
	if afterClose.CloseRequested != again.CloseRequested || afterClose.CloseWriteError != again.CloseWriteError {
		t.Fatal("post-termination Close changed sealed result")
	}
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("second Run accepted")
	}
}

func TestOpenAILiveTerminationWaitCannotAcknowledgeBlockedCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := startTerminationTest(t, voice.StartConfig{OnProviderEvent: func(context.Context, voice.ProviderEvent) error { close(entered); <-release; return nil }}, func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`))
	})
	done := make(chan error, 1)
	go func() { done <- h.Run(context.Background()) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.WaitTermination(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked callback acknowledged: %v", err)
	}
	select {
	case <-done:
		t.Fatal("Run returned while callback blocked")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	o := waitTerminationTest(t, h)
	assertLocalTerminationTest(t, o)
	if o.CallbackAttempts != 1 || o.CallbackAcknowledged != 1 {
		t.Fatalf("lost callback acknowledgement: %+v", o)
	}
}

func TestOpenAILiveTerminationPreservesCallbackFailure(t *testing.T) {
	failure := errors.New("sink did not acknowledge")
	h := startTerminationTest(t, voice.StartConfig{OnProviderEvent: func(context.Context, voice.ProviderEvent) error { return failure }}, func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`))
	})
	if err := h.Run(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("lost Run error: %v", err)
	}
	o := waitTerminationTest(t, h)
	assertLocalTerminationTest(t, o)
	if !o.ProviderClosed || o.CallbackAttempts != 1 || o.CallbackAcknowledged != 0 || o.CallbackError != failure.Error() || !strings.Contains(o.RunError, failure.Error()) {
		t.Fatalf("failed sink acknowledged: %+v", o)
	}
}

func TestOpenAILiveTerminationLostCloseAcknowledgement(t *testing.T) {
	command := make(chan struct{})
	h := startTerminationTest(t, voice.StartConfig{}, func(c *websocket.Conn) {
		var v map[string]any
		if c.ReadJSON(&v) == nil && v["type"] == "session.close" {
			close(command)
		}
	})
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	<-command
	// Close before Run cannot certify that the consumer has returned.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.WaitTermination(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close alone acknowledged consumer: %v", err)
	}
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("socket EOF treated as provider acknowledgement")
	}
	o := waitTerminationTest(t, h)
	assertLocalTerminationTest(t, o)
	if o.ProviderClosed || o.ProviderClosedPayloadDigest != "" || !o.CloseRequested || o.ReadError == "" || o.RunError == "" || o.CallbackConfigured || o.CallbackAttempts != 0 {
		t.Fatalf("invented provider coverage: %+v", o)
	}
}

func TestOpenAILiveTerminationCancellationRetainsActualProviderClosure(t *testing.T) {
	h := startTerminationTest(t, voice.StartConfig{OnProviderEvent: func(context.Context, voice.ProviderEvent) error { return nil }}, func(c *websocket.Conn) {
		var v map[string]any
		if c.ReadJSON(&v) == nil && v["type"] == "session.close" {
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`))
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Run(ctx); err != nil {
		t.Fatal(err)
	}
	o := waitTerminationTest(t, h)
	assertLocalTerminationTest(t, o)
	if !o.ProviderClosed || !o.CloseRequested || o.CallbackAcknowledged != 1 || o.RunError != "" {
		t.Fatalf("actual cancellation handshake lost: %+v", o)
	}
}

func TestOpenAILiveTerminationConcurrentCloseAndWait(t *testing.T) {
	h := startTerminationTest(t, voice.StartConfig{}, func(c *websocket.Conn) {
		var v map[string]any
		if c.ReadJSON(&v) == nil {
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`))
		}
	})
	run := make(chan error, 1)
	go func() { run <- h.Run(context.Background()) }()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			o, err := h.WaitTermination(ctx)
			if err != nil || !o.ConsumerReturned || !o.LocalCloseAttempted {
				t.Errorf("uncertain concurrent wait: %+v %v", o, err)
			}
		}()
	}
	wg.Wait()
	if err := <-run; err != nil {
		t.Fatal(err)
	}
}

func TestOpenAILiveTerminationCloseErrorsRemainVisible(t *testing.T) {
	h := startTerminationTest(t, voice.StartConfig{}, func(c *websocket.Conn) { _, _, _ = c.ReadMessage() })
	// Force the actual injected local transport to fail subsequent writes/closes.
	_ = h.conn.UnderlyingConn().Close()
	_ = h.Close()
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("closed local transport succeeded")
	}
	o := waitTerminationTest(t, h)
	assertLocalTerminationTest(t, o)
	if o.CloseWriteError == "" || o.LocalCloseError == "" || o.ProviderClosed {
		t.Fatalf("cleanup errors erased: %+v", o)
	}
}

func TestOpenAILiveTerminationEarlyStartFailureReturnsNoHandle(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	adapter := NewOpenAILiveAdapter(agents.DefineAI(agents.AIConfig{}))
	h, _, err := adapter.Start(context.Background(), voice.StartConfig{Actor: agents.Actor{Kind: "service", ID: "test"}}, make(chan []byte), make(chan voice.AudioFrame))
	if err == nil || h != nil {
		t.Fatal("failed Start invented a termination-capable session")
	}
}

func TestOpenAILiveTerminationInventoryRetainsEveryObservedResponse(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		name := "distinct_delegations"
		if overlap {
			name = "overlap_rejected"
		}
		t.Run(name, func(t *testing.T) {
			h := startTerminationTest(t, voice.StartConfig{}, func(c *websocket.Conn) {
				second := "second"
				if overlap {
					second = "first"
				}
				_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": "first", "event": map[string]any{"type": "response.created", "response": map[string]any{"id": "response-a"}}})
				_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": second, "event": map[string]any{"type": "response.created", "response": map[string]any{"id": "response-b"}}})
				_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": second, "event": map[string]any{"type": "response.completed", "response": map[string]any{"id": "response-b"}}})
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`))
			})
			err := h.Run(context.Background())
			if (err != nil) != overlap {
				t.Fatalf("overlap policy changed: %v", err)
			}
			o := waitTerminationTest(t, h)
			if len(o.OutstandingResponseIDs) == 0 || o.OutstandingResponseIDs[0] != "response-a" {
				t.Fatalf("unfinished A disappeared: %+v", o)
			}
			want := 1
			if overlap {
				want = 2
			}
			if len(o.OutstandingResponseIDs) != want {
				t.Fatalf("inventory mismatches received terminal events: %+v", o)
			}
		})
	}
}
