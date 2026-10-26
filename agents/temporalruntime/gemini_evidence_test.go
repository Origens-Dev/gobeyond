package temporalruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voice"
	"google.golang.org/genai"
)

func TestGeminiRequiredEvidenceRejectsBeforeDial(t *testing.T) {
	calls := 0
	adapter := &GeminiLiveAdapter{dial: func(context.Context, agents.AIDefinition, string, *genai.LiveConnectConfig) (liveSession, error) {
		calls++
		return newFakeLiveSession(), nil
	}}
	legacy := 0
	h, _, err := adapter.Start(context.Background(), voice.StartConfig{Actor: agents.Actor{ID: "actor", Kind: "user"}, OnProviderEvent: func(context.Context, voice.ProviderEvent) error { t.Fatal("unsupported callback invoked"); return nil }, OnUsage: func(voice.Usage) { legacy++ }}, nil, nil)
	if !errors.Is(err, ErrGeminiProviderEvidenceUnsupported) || h != nil || calls != 0 || legacy != 0 {
		t.Fatalf("handle=%v error=%v dials=%d legacy=%d", h, err, calls, legacy)
	}
}

type joiningGeminiSession struct {
	*fakeLiveSession
	entered     chan struct{}
	release     chan struct{}
	sendFailure error
}

func (s *joiningGeminiSession) Receive() (*genai.LiveServerMessage, error) {
	close(s.entered)
	<-s.closed
	<-s.release
	return nil, context.Canceled
}
func (s *joiningGeminiSession) SendRealtimeInput(genai.LiveRealtimeInput) error {
	<-s.entered
	return s.sendFailure
}

func TestGeminiRunClosesAndJoinsReader(t *testing.T) {
	for _, cause := range []string{"owner_cancel", "uplink_error"} {
		t.Run(cause, func(t *testing.T) {
			s := &joiningGeminiSession{fakeLiveSession: newFakeLiveSession(), entered: make(chan struct{}), release: make(chan struct{})}
			in := make(chan []byte, 1)
			if cause == "uplink_error" {
				s.sendFailure = errors.New("uplink failed")
				in <- []byte{1, 2}
			}
			h := &geminiLiveHandle{session: s, pcmIn: in, closed: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- h.Run(ctx) }()
			select {
			case <-s.entered:
			case <-time.After(time.Second):
				t.Fatal("reader not entered")
			}
			if cause == "owner_cancel" {
				cancel()
			}
			select {
			case <-s.closed:
			case <-time.After(time.Second):
				t.Fatal("socket not closed before joining reader")
			}
			select {
			case err := <-done:
				t.Fatalf("Run returned before reader exited: %v", err)
			default:
			}
			close(s.release)
			select {
			case err := <-done:
				if cause == "owner_cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
				if cause == "uplink_error" && !errors.Is(err, s.sendFailure) {
					t.Fatalf("error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Run did not join reader")
			}
		})
	}
}

type blockedGeminiWriter struct {
	*fakeLiveSession
	entered chan struct{}
	release chan struct{}
}

func (s *blockedGeminiWriter) Receive() (*genai.LiveServerMessage, error) {
	<-s.entered
	return nil, errors.New("provider reader failed")
}
func (s *blockedGeminiWriter) SendRealtimeInput(genai.LiveRealtimeInput) error {
	close(s.entered)
	<-s.closed
	<-s.release
	return context.Canceled
}
func TestGeminiRunReadFailureClosesAndJoinsWriter(t *testing.T) {
	s := &blockedGeminiWriter{fakeLiveSession: newFakeLiveSession(), entered: make(chan struct{}), release: make(chan struct{})}
	in := make(chan []byte, 1)
	in <- []byte{1, 2}
	h := &geminiLiveHandle{session: s, pcmIn: in, closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- h.Run(context.Background()) }()
	select {
	case <-s.closed:
	case <-time.After(time.Second):
		t.Fatal("reader failure did not close socket")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned before writer exited: %v", err)
	default:
	}
	close(s.release)
	select {
	case err := <-done:
		if err == nil || err.Error() != "provider reader failed" {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not join writer")
	}
}
