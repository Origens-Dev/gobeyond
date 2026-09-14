package temporalruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"github.com/gorilla/websocket"
)

func openAIControlTestHandle(t *testing.T) (*openAILiveHandle, chan map[string]any, chan voice.AudioFrame) {
	t.Helper()
	events := make(chan map[string]any, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		for {
			var v map[string]any
			if c.ReadJSON(&v) != nil {
				return
			}
			events <- v
		}
	}))
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); srv.Close() })
	out := make(chan voice.AudioFrame, 128)
	closed := make(chan struct{})
	close(closed)
	h := &openAILiveHandle{conn: c, out: out, closed: closed, format: voice.AudioFormat{Encoding: voice.EncodingPCMU, SampleRate: 8000, Channels: 1}, tools: map[string]ai.Tool{"dial-contact": {Name: "dial-contact", InputSchema: map[string]any{"type": "object"}}}}
	h.cfg = voice.StartConfig{CallControl: &voice.CallControlConfig{ToolNames: []string{"dial-contact"}, Announcement: func(context.Context, ai.ToolCall, voice.AudioFormat) ([]byte, error) { return make([]byte, 320), nil }}}
	return h, events, out
}
func TestOpenAIControlAnnouncementDrainsBeforeTerminal(t *testing.T) {
	h, events, out := openAIControlTestHandle(t)
	drained := false
	executions := 0
	h.cfg.OnPlayoutBarrier = func(_ context.Context, id uint64) error {
		if id != 1 {
			t.Fatalf("barrier %d", id)
		}
		if len(out) != 3 || !(<-out).Interrupted || len((<-out).Data) != 160 || len((<-out).Data) != 160 {
			t.Fatal("announcement must precede barrier")
		}
		// Provider audio cannot interleave with the application clip.
		if err := h.emitAudio(context.Background(), openAILiveAudio{frame: voice.AudioFrame{Data: []byte{1}}, epoch: h.audioEpoch.Load()}); err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatal("provider audio escaped announcement gate")
		}
		drained = true
		return nil
	}
	h.cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) {
		if !drained {
			t.Fatal("control before drain")
		}
		executions++
		return nil, &voice.TerminalHandoff{Result: terminalFixture(t)}
	}
	calls := []grokFunctionCall{{CallID: "one", Name: "dial-contact", Arguments: map[string]any{}}, {CallID: "two", Name: "dial-contact", Arguments: map[string]any{}}}
	if err := h.execute(context.Background(), calls); err != nil {
		t.Fatal(err)
	}
	if executions != 1 || !h.control.stopped() {
		t.Fatal("terminal did not fence second command")
	}
	select {
	case e := <-events:
		if e["type"] != "session.close" {
			t.Fatalf("terminal continued provider: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("session not closed")
	}
}
func TestOpenAIControlFailedDrainDoesNotTransfer(t *testing.T) {
	h, events, out := openAIControlTestHandle(t)
	h.cfg.OnPlayoutBarrier = func(context.Context, uint64) error { return context.DeadlineExceeded }
	h.cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) { t.Fatal("executed without drain"); return nil, nil }
	if err := h.execute(context.Background(), []grokFunctionCall{{CallID: "one", Name: "dial-contact", Arguments: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	for len(out) > 0 {
		<-out
	}
	if h.control.stopped() {
		t.Fatal("failed transfer terminated conversation")
	}
	if err := h.emitAudio(context.Background(), openAILiveAudio{frame: voice.AudioFrame{Data: []byte{1}}, epoch: 0}); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatal("stale provider audio replayed after failure")
	}
	if err := h.emitAudio(context.Background(), openAILiveAudio{frame: voice.AudioFrame{Data: []byte{2}}, epoch: h.audioEpoch.Load()}); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatal("conversation did not resume")
	}
	select {
	case e := <-events:
		if e["type"] != "response.item.create" || !strings.Contains(e["item"].(map[string]any)["output"].(string), "error") {
			t.Fatalf("missing tool failure: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no failure output")
	}
}
func TestOpenAIAnnouncementFailureDoesNotReachBarrier(t *testing.T) {
	h, _, _ := openAIControlTestHandle(t)
	h.cfg.CallControl.Announcement = func(context.Context, ai.ToolCall, voice.AudioFormat) ([]byte, error) {
		return nil, errors.New("clip unavailable")
	}
	h.cfg.OnPlayoutBarrier = func(context.Context, uint64) error { t.Fatal("barrier without clip"); return nil }
	h.cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) { t.Fatal("control without clip"); return nil, nil }
	if err := h.execute(context.Background(), []grokFunctionCall{{CallID: "one", Name: "dial-contact", Arguments: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
}
func TestOpenAIHangupNeedsNoAnnouncement(t *testing.T) {
	h, events, _ := openAIControlTestHandle(t)
	h.tools = map[string]ai.Tool{"hang_up": {Name: "hang_up", InputSchema: map[string]any{"type": "object"}}}
	h.cfg.CallControl.ToolNames = []string{"hang_up"}
	h.cfg.CallControl.Announcement = nil
	h.cfg.OnPlayoutBarrier = func(ctx context.Context, _ uint64) error { <-ctx.Done(); return ctx.Err() }
	h.cfg.CallControl.Execute = func(context.Context, ai.ToolCall) (any, error) {
		return nil, &voice.TerminalHandoff{Result: terminalFixture(t)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.execute(ctx, []grokFunctionCall{{CallID: "bye", Name: "hang_up", Arguments: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	if !h.control.stopped() {
		t.Fatal("hangup not terminal")
	}
	select {
	case e := <-events:
		if e["type"] != "session.close" {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("hangup did not close session")
	}
}
