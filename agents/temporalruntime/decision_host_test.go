package temporalruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/connections"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/decisions/callback"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"go.temporal.io/sdk/client"
)

type hostCallbackAdapter struct {
	*decisionSessionAdapterFake
	says        []callback.SayEffect
	listens     []callback.ListenEffect
	matches     []callback.MatchRequest
	sayErr      error
	listenHints []string
	matchClient *callback.MatchClient
	matchHost   callback.HostCallback
	bind        DecisionSessionUpdate
	bindCalls   int
}

func (adapter *hostCallbackAdapter) Say(_ context.Context, effect callback.SayEffect) (callback.PlaybackReceipt, error) {
	adapter.says = append(adapter.says, effect)
	if adapter.sayErr != nil {
		return callback.PlaybackReceipt{}, adapter.sayErr
	}
	return callback.PlaybackReceipt{TurnID: effect.TurnID, Status: callback.PlaybackPlayed}, nil
}

func (adapter *hostCallbackAdapter) Listen(_ context.Context, effect callback.ListenEffect) error {
	adapter.listens = append(adapter.listens, effect)
	return nil
}

func (*hostCallbackAdapter) Act(context.Context, callback.ActEffect) error { return nil }

func (adapter *hostCallbackAdapter) PrepareListen(effect decisions.Effect) (callback.ListenEffect, error) {
	return callback.ListenEffectFrom(effect, 4000, 15000, adapter.listenHints)
}

func (adapter *hostCallbackAdapter) PrepareDecisionMatch(_ context.Context, _ httpruntime.RespondCall, _ DecisionSessionIdentity, _ decisions.Effect) (callback.MatchRequest, error) {
	request := callback.MatchRequest{SessionID: adapter.input.Admission.SessionID, Utterance: "call sam", AttemptCount: 1}
	adapter.matches = append(adapter.matches, request)
	return request, nil
}

func (adapter *hostCallbackAdapter) DecisionMatchClient() *callback.MatchClient {
	return adapter.matchClient
}

func (adapter *hostCallbackAdapter) BindDecisionMatch(callback.MatchResponse, httpruntime.RespondCall, DecisionSessionIdentity, decisions.Effect) (DecisionSessionUpdate, error) {
	adapter.bindCalls++
	return adapter.bind, nil
}

func (adapter *hostCallbackAdapter) Match(ctx context.Context, request callback.MatchRequest) (callback.MatchResponse, error) {
	if adapter.matchHost != nil {
		return adapter.matchHost.Match(ctx, request)
	}
	return callback.MatchResponse{Result: callback.MatchNoMatch}, nil
}

var (
	_ callback.DecisionEffectAdapter = (*hostCallbackAdapter)(nil)
	_ callback.ListenPolicy          = (*hostCallbackAdapter)(nil)
	_ callback.HostCallback          = (*hostCallbackAdapter)(nil)
	_ DecisionMatchAdapter           = (*hostCallbackAdapter)(nil)
)

func TestOptionalHostEffectsDefaultOff(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_host_off", 201)
	adapter := &decisionSessionAdapterFake{config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, input: input}
	fake := &fakeClient{}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	err := dispatcher.dispatchOptionalHostEffects(context.Background(), adapter, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects: []decisions.Effect{
			{Kind: decisions.EffectSay, RouteEntryID: "entry_1", MessageFamilies: []string{"greeting"}},
			{Kind: decisions.EffectListen, RouteEntryID: "entry_1"},
			{Kind: decisions.EffectRunDecision, RouteEntryID: "entry_1"},
		},
	}, nil)
	if err != nil || fake.updateCalls != 0 {
		t.Fatalf("default-off host path err=%v updates=%d", err, fake.updateCalls)
	}
}

func TestOptionalHostSayAndListenHooks(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_host_say", 202)
	prompts := input.Definition.Prompts()
	if len(prompts) == 0 {
		t.Fatal("fixture has no compiled prompts")
	}
	adapter := &hostCallbackAdapter{
		decisionSessionAdapterFake: &decisionSessionAdapterFake{
			config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, input: input,
		},
		listenHints: []string{"sam", "alex"},
	}
	dispatcher := newDecisionEffectTestDispatcher(t, &fakeClient{})
	err := dispatcher.dispatchOptionalHostEffects(context.Background(), adapter, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects: []decisions.Effect{
			{Kind: decisions.EffectSay, RouteEntryID: "entry_1", MessageFamilies: []string{prompts[0].ID}},
			{Kind: decisions.EffectListen, RouteEntryID: "entry_1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("host say/listen: %v", err)
	}
	if len(adapter.says) != 1 || adapter.says[0].PromptID != prompts[0].ID || adapter.says[0].TurnID != "entry_1" {
		t.Fatalf("say = %#v", adapter.says)
	}
	if len(adapter.listens) != 1 || len(adapter.listens[0].Hints) != 2 || adapter.listens[0].Hints[0] != "sam" {
		t.Fatalf("listen = %#v", adapter.listens)
	}
}

func TestOptionalHostSayUnknownPromptFailsClosed(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_host_say_missing", 203)
	adapter := &hostCallbackAdapter{decisionSessionAdapterFake: &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, input: input,
	}}
	dispatcher := newDecisionEffectTestDispatcher(t, &fakeClient{})
	err := dispatcher.dispatchOptionalHostEffects(context.Background(), adapter, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects:  []decisions.Effect{{Kind: decisions.EffectSay, RouteEntryID: "entry_1", MessageFamilies: []string{"missing"}}},
	}, nil)
	if !errors.Is(err, decisionv1.ErrPromptUnavailable) || len(adapter.says) != 0 {
		t.Fatalf("unknown say prompt: err=%v says=%d", err, len(adapter.says))
	}
}

func TestOptionalHostListenRejectsOverCapHints(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_host_listen_cap", 204)
	hints := make([]string, callback.MaxListenHints+1)
	for i := range hints {
		hints[i] = "hint"
	}
	adapter := &hostCallbackAdapter{
		decisionSessionAdapterFake: &decisionSessionAdapterFake{
			config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, input: input,
		},
		listenHints: hints,
	}
	dispatcher := newDecisionEffectTestDispatcher(t, &fakeClient{})
	err := dispatcher.dispatchOptionalHostEffects(context.Background(), adapter, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects:  []decisions.Effect{{Kind: decisions.EffectListen, RouteEntryID: "entry_1"}},
	}, nil)
	if err == nil || len(adapter.listens) != 0 {
		t.Fatalf("over-cap hints err=%v listens=%d", err, len(adapter.listens))
	}
}

func TestRunDecisionMatchClientSignsAndAdvancesReducer(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_host_match", 205)
	key := bytes.Repeat([]byte{11}, 32)
	now := time.Unix(1800000000, 0)
	var eventIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(writer, "read", http.StatusBadRequest)
			return
		}
		eventID, err := connections.VerifyDelivery(req.Header, key, body, now)
		if err != nil {
			http.Error(writer, "auth", http.StatusUnauthorized)
			return
		}
		eventIDs = append(eventIDs, eventID)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":"matched","targetId":"target-1"}`))
	}))
	t.Cleanup(server.Close)

	identity := decisionSessionIdentity(input)
	call := decisionEffectTestCall(input)
	adapter := &hostCallbackAdapter{
		decisionSessionAdapterFake: &decisionSessionAdapterFake{
			config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, input: input,
		},
		matchClient: &callback.MatchClient{BaseURL: server.URL, Key: key, Now: func() time.Time { return now }, HTTPClient: server.Client()},
		bind: DecisionSessionUpdate{
			Identity: identity, ExpectedRouteEntryID: "entry_1",
			Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
				ID: "match-advance-1", Kind: decisionv1.EventSpeechStarted, TenantID: input.Admission.TenantID,
				SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
				RouteID: input.Definition.Graph.Entry, RouteEntryID: "entry_1",
				InputWindowID: "window_1", Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
			}},
		},
	}
	fake := &fakeClient{updateFunc: func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		if options.UpdateName != DecisionSessionAdvanceUpdate {
			t.Errorf("update %q", options.UpdateName)
		}
		return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionRunning}}, nil
	}}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	err := dispatcher.dispatchOptionalHostEffects(context.Background(), adapter, call, DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionRunning},
		Effects:  []decisions.Effect{{Kind: decisions.EffectRunDecision, RouteEntryID: "entry_1"}},
	}, &recordingDecisionEmitter{})
	if err != nil {
		t.Fatalf("run_decision match: %v", err)
	}
	if adapter.bindCalls != 1 || fake.updateCalls != 1 {
		t.Fatalf("bind=%d updates=%d", adapter.bindCalls, fake.updateCalls)
	}
	wantID := callback.MatchEventID(input.Admission.SessionID, 1)
	if len(eventIDs) != 1 || eventIDs[0] != wantID {
		t.Fatalf("signed Event-ID = %v want %q", eventIDs, wantID)
	}
	_, retryErr := adapter.matchClient.Match(context.Background(), input.Definition, adapter.matches[0])
	if retryErr != nil || len(eventIDs) != 2 || eventIDs[1] != wantID {
		t.Fatalf("retry Event-ID = %v err=%v", eventIDs, retryErr)
	}
}

func TestRunDecisionUnknownClarifyFailsClosed(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_host_clarify", 206)
	key := bytes.Repeat([]byte{12}, 32)
	now := time.Unix(1800000000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":"clarify","promptId":"missing"}`))
	}))
	t.Cleanup(server.Close)
	adapter := &hostCallbackAdapter{
		decisionSessionAdapterFake: &decisionSessionAdapterFake{
			config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, input: input,
		},
		matchClient: &callback.MatchClient{BaseURL: server.URL, Key: key, Now: func() time.Time { return now }, HTTPClient: server.Client()},
	}
	fake := &fakeClient{}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	err := dispatcher.dispatchOptionalHostEffects(context.Background(), adapter, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects:  []decisions.Effect{{Kind: decisions.EffectRunDecision, RouteEntryID: "entry_1"}},
	}, nil)
	if !errors.Is(err, decisionv1.ErrPromptUnavailable) || adapter.bindCalls != 0 || fake.updateCalls != 0 {
		t.Fatalf("unknown clarify err=%v bind=%d updates=%d", err, adapter.bindCalls, fake.updateCalls)
	}
}

func TestHasOptionalHostEffectWork(t *testing.T) {
	t.Parallel()
	if hasOptionalHostEffectWork([]decisions.Effect{{Kind: decisions.EffectDispatchIntent}}) {
		t.Fatal("dispatch intent is not optional host work")
	}
	for _, kind := range []decisions.EffectKind{decisions.EffectSay, decisions.EffectListen, decisions.EffectRunDecision} {
		if !hasOptionalHostEffectWork([]decisions.Effect{{Kind: kind}}) {
			t.Fatalf("%q should be optional host work", kind)
		}
	}
}
