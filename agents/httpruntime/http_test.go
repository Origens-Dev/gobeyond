package httpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
)

const testPrefix = "/api/agents"

func TestPublicTypedAdapterLifecycleAndSSEResume(t *testing.T) {
	definition := agents.Define(agents.Config{Public: true}, func(_ context.Context, actor agents.Actor, input string) (string, error) {
		return actor.Kind + ":" + input, nil
	})
	runtime, handler := newTestRuntime(t, "echo", Adapt(definition), Options{})
	started := startTestSession(t, handler, "echo", `{"input":"hello"}`, "203.0.113.10:1234", "")
	view := waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.10:1234", "")
	if view.Session.Actor.ID != PublicActorID || view.Session.Actor.Kind != PublicActorKind {
		t.Fatalf("public actor = %#v", view.Session.Actor)
	}

	events := getTerminalEvents(t, handler, started.Session.ID, "", "")
	if got, want := eventTypes(events), []string{"session.created", "run.started", "agent.output", "run.completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %#v, want %#v", got, want)
	}
	for index, event := range events {
		want := fmt.Sprintf("%s:%020d", started.Session.ID, index+1)
		if event.ID != want {
			t.Fatalf("event[%d].ID = %q, want %q", index, event.ID, want)
		}
	}
	var output string
	if err := json.Unmarshal(events[2].Data, &output); err != nil || output != "public:hello" {
		t.Fatalf("output = %q, err = %v", output, err)
	}

	resumed := getTerminalEvents(t, handler, started.Session.ID, events[1].ID, "")
	if got, want := eventIDs(resumed), eventIDs(events[2:]); !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor resume IDs = %#v, want %#v", got, want)
	}
	resumed = getTerminalEvents(t, handler, started.Session.ID, "", events[2].ID)
	if got, want := eventIDs(resumed), eventIDs(events[3:]); !reflect.DeepEqual(got, want) {
		t.Fatalf("Last-Event-ID resume IDs = %#v, want %#v", got, want)
	}

	request := httptest.NewRequest(http.MethodGet, testPrefix+"/sessions/"+started.Session.ID+"/events?cursor="+url.QueryEscape("other:00000000000000000001"), nil)
	request.RemoteAddr = "203.0.113.10:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, body = %s", response.Code, response.Body.String())
	}

	if _, ok := runtime.registry.Lookup("echo"); !ok {
		t.Fatal("typed adapter was not registered")
	}
}

func TestDirectAIAdapterStreamsTextAndFinalOutput(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	model.StreamFunc = func(context.Context, ai.LanguageModelCallOptions) (*ai.LanguageModelStreamResult, error) {
		stream := make(chan ai.StreamPart, 3)
		stream <- ai.StreamPart{Type: "text-delta", ID: "text-1", TextDelta: "hel"}
		stream <- ai.StreamPart{Type: "text-delta", ID: "text-1", TextDelta: "lo"}
		stream <- ai.StreamPart{Type: "finish", FinishReason: ai.FinishReason{Unified: ai.FinishStop, Raw: "stop"}}
		close(stream)
		return &ai.LanguageModelStreamResult{Stream: stream}, nil
	}
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	definition := agents.DefineAI(agents.AIConfig{
		Public: true, Model: "assistant", Provider: provider, Instructions: "Reply.",
	})
	_, handler := newTestRuntime(t, "assistant", AdaptAI(definition), Options{})
	started := startTestSession(t, handler, "assistant", `{"input":{"message":"hi"}}`, "203.0.113.10:1234", "")
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.10:1234", "")
	events := getTerminalEvents(t, handler, started.Session.ID, "", "")
	if got, want := eventTypes(events), []string{"session.created", "run.started", "agent.text.delta", "agent.text.delta", "agent.output", "run.completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AI event types = %#v, want %#v", got, want)
	}
	var output agents.AIOutput
	if err := json.Unmarshal(events[4].Data, &output); err != nil || output.Text != "hello" || output.FinishReason != ai.FinishStop {
		t.Fatalf("AI output = %#v, err = %v", output, err)
	}
}

func TestDirectAIAdapterAppliesSessionInstructionOverlay(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	model.StreamFunc = func(context.Context, ai.LanguageModelCallOptions) (*ai.LanguageModelStreamResult, error) {
		stream := make(chan ai.StreamPart, 2)
		stream <- ai.StreamPart{Type: "text-delta", ID: "text-1", TextDelta: "ok"}
		stream <- ai.StreamPart{Type: "finish", FinishReason: ai.FinishReason{Unified: ai.FinishStop, Raw: "stop"}}
		close(stream)
		return &ai.LanguageModelStreamResult{Stream: stream}, nil
	}
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	definition := agents.DefineAI(agents.AIConfig{
		Public: true, Model: "assistant", Provider: provider, Instructions: "Base instructions.",
	})
	_, handler := newTestRuntime(t, "assistant", AdaptAI(definition), Options{})
	started := startTestSession(t, handler, "assistant",
		`{"input":{"message":"hi"},"metadata":{"instructions":"Session overlay instructions."}}`,
		"203.0.113.10:1234", "")
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.10:1234", "")
	if len(model.StreamCalls) != 1 {
		t.Fatalf("stream calls = %d", len(model.StreamCalls))
	}
	if got := model.StreamCalls[0].Prompt[0].Text; got != "Session overlay instructions." {
		t.Fatalf("effective instructions = %q, want session overlay", got)
	}
}

func TestPrivateActorEnforcementAndLoopbackFallback(t *testing.T) {
	actors := make(chan agents.Actor, 2)
	adapter := AdapterFuncs{
		StartFunc: func(_ context.Context, call StartCall, _ EventEmitter) error {
			actors <- call.Actor
			return nil
		},
	}
	resolver := func(request *http.Request) (agents.Actor, error) {
		id := request.Header.Get("X-Test-Actor")
		if id == "" {
			return agents.Actor{}, ErrUnauthenticated
		}
		return agents.Actor{ID: id, Kind: "user"}, nil
	}
	_, handler := newTestRuntime(t, "private", adapter, Options{ResolveActor: resolver, AllowLoopbackActor: true})

	request := httptest.NewRequest(http.MethodPost, testPrefix+"/private/sessions", strings.NewReader(`{"input":null}`))
	request.RemoteAddr = "203.0.113.11:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, body = %s", response.Code, response.Body.String())
	}

	loopback := startTestSession(t, handler, "private", `{"input":null}`, "127.0.0.1:4444", "")
	select {
	case actor := <-actors:
		if !sameActor(actor, agents.LoopbackDevActor()) {
			t.Fatalf("loopback actor = %#v", actor)
		}
	case <-time.After(time.Second):
		t.Fatal("loopback agent did not start")
	}
	waitForRunStatus(t, handler, loopback.Session.ID, RunStatusCompleted, "127.0.0.1:4444", "")

	_, hostedHandler := newTestRuntime(t, "private", adapter, Options{ResolveActor: resolver})
	request = httptest.NewRequest(http.MethodPost, testPrefix+"/private/sessions", strings.NewReader(`{"input":null}`))
	request.RemoteAddr = "127.0.0.1:4444"
	response = httptest.NewRecorder()
	hostedHandler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled loopback fallback status = %d, body = %s", response.Code, response.Body.String())
	}

	owned := startTestSession(t, handler, "private", `{"input":null}`, "203.0.113.11:1234", "actor-a")
	select {
	case <-actors:
	case <-time.After(time.Second):
		t.Fatal("owned agent did not start")
	}
	request = httptest.NewRequest(http.MethodGet, testPrefix+"/sessions/"+owned.Session.ID, nil)
	request.RemoteAddr = "203.0.113.11:1234"
	request.Header.Set("X-Test-Actor", "actor-b")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-actor status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestRespondAndCancelActiveRuns(t *testing.T) {
	t.Run("respond", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		adapter := AdapterFuncs{
			AgentConfig: agents.Config{Public: true},
			StartFunc: func(ctx context.Context, _ StartCall, _ EventEmitter) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					return nil
				}
			},
			RespondFunc: func(ctx context.Context, call RespondCall, emit EventEmitter) error {
				var response string
				if err := json.Unmarshal(call.Response, &response); err != nil {
					return err
				}
				if err := emit.Emit(ctx, "agent.response", map[string]string{"value": response}); err != nil {
					return err
				}
				releaseOnce.Do(func() { close(release) })
				return nil
			},
		}
		_, handler := newTestRuntime(t, "interactive", adapter, Options{})
		started := startTestSession(t, handler, "interactive", `{"input":null}`, "203.0.113.12:1234", "")
		response := postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/respond", `{"response":"approved"}`, "203.0.113.12:1234", "")
		if response.Code != http.StatusAccepted {
			t.Fatalf("respond status = %d, body = %s", response.Code, response.Body.String())
		}
		waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.12:1234", "")
		events := getTerminalEvents(t, handler, started.Session.ID, "", "")
		if !containsEventTypes(events, "run.response.received", "agent.response", "run.completed") {
			t.Fatalf("respond events = %#v", eventTypes(events))
		}
	})

	t.Run("cancel", func(t *testing.T) {
		cancelled := make(chan struct{}, 1)
		cancelEntered := make(chan struct{})
		releaseCancel := make(chan struct{})
		adapter := AdapterFuncs{
			AgentConfig: agents.Config{Public: true},
			StartFunc: func(ctx context.Context, _ StartCall, _ EventEmitter) error {
				<-ctx.Done()
				return ctx.Err()
			},
			CancelFunc: func(context.Context, CancelCall, EventEmitter) error {
				close(cancelEntered)
				<-releaseCancel
				cancelled <- struct{}{}
				return nil
			},
		}
		runtime, handler := newTestRuntime(t, "cancelable", adapter, Options{})
		started := startTestSession(t, handler, "cancelable", `{"input":null}`, "203.0.113.13:1234", "")
		responseResult := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			responseResult <- postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/cancel", `{"reason":"user request"}`, "203.0.113.13:1234", "")
		}()
		select {
		case <-cancelEntered:
		case <-time.After(time.Second):
			t.Fatal("adapter cancellation was not dispatched")
		}
		view, ok := runtime.sessionView(started.Session.ID)
		if !ok || view.Runs[0].Status != RunStatusRunning {
			t.Fatalf("run changed before cancellation acknowledgement: %#v", view.Runs)
		}
		select {
		case response := <-responseResult:
			t.Fatalf("cancel returned before adapter acknowledgement: %d %s", response.Code, response.Body.String())
		default:
		}
		close(releaseCancel)
		response := <-responseResult
		if response.Code != http.StatusAccepted {
			t.Fatalf("cancel status = %d, body = %s", response.Code, response.Body.String())
		}
		waitForRunStatus(t, handler, started.Session.ID, RunStatusCancelled, "203.0.113.13:1234", "")
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("adapter cancellation was not dispatched")
		}
		events := getTerminalEvents(t, handler, started.Session.ID, "", "")
		if !containsEventTypes(events, "run.cancelled") || containsEventTypes(events, "run.failed") {
			t.Fatalf("cancel events = %#v", eventTypes(events))
		}
	})
}

func TestCancelDispatchFailurePreservesRunningState(t *testing.T) {
	releaseRun := make(chan struct{})
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(context.Context, StartCall, EventEmitter) error {
			<-releaseRun
			return nil
		},
		CancelFunc: func(context.Context, CancelCall, EventEmitter) error {
			return errors.New("cancel backend unavailable")
		},
	}
	runtime, handler := newTestRuntime(t, "cancelable", adapter, Options{})
	started := startTestSession(t, handler, "cancelable", `{"input":null}`, "203.0.113.23:1234", "")
	response := postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/cancel", "", "203.0.113.23:1234", "")
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "cancel_dispatch_failed") {
		t.Fatalf("cancel failure status = %d, body = %s", response.Code, response.Body.String())
	}
	view, ok := runtime.sessionView(started.Session.ID)
	if !ok || view.Runs[0].Status != RunStatusRunning {
		t.Fatalf("failed cancellation changed run state: %#v", view.Runs)
	}
	runtime.mu.Lock()
	events := append([]Event(nil), runtime.sessions[started.Session.ID].events...)
	runtime.mu.Unlock()
	if containsEventTypes(events, "run.cancelled") {
		t.Fatalf("failed cancellation emitted terminal event: %#v", eventTypes(events))
	}
	close(releaseRun)
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.23:1234", "")
}

func TestCancelDispatchFailurePublishesRacingCompletion(t *testing.T) {
	cancelEntered := make(chan struct{})
	releaseCancel := make(chan struct{})
	finishStart := make(chan struct{})
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(context.Context, StartCall, EventEmitter) error {
			<-finishStart
			return nil
		},
		CancelFunc: func(context.Context, CancelCall, EventEmitter) error {
			close(cancelEntered)
			<-releaseCancel
			return errors.New("cancel failed")
		},
	}
	runtime, handler := newTestRuntime(t, "cancelable", adapter, Options{})
	started := startTestSession(t, handler, "cancelable", `{"input":null}`, "203.0.113.24:1234", "")
	responseResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseResult <- postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/cancel", "", "203.0.113.24:1234", "")
	}()
	<-cancelEntered
	close(finishStart)
	deadline := time.Now().Add(time.Second)
	for {
		runtime.mu.Lock()
		pendingFinish := runtime.sessions[started.Session.ID].runs[started.Run.ID].pendingFinish
		runtime.mu.Unlock()
		if pendingFinish {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run completion did not race with pending cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseCancel)
	response := <-responseResult
	if response.Code != http.StatusBadGateway {
		t.Fatalf("cancel failure status = %d, body = %s", response.Code, response.Body.String())
	}
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.24:1234", "")
	runtime.mu.Lock()
	events := append([]Event(nil), runtime.sessions[started.Session.ID].events...)
	runtime.mu.Unlock()
	if !containsEventTypes(events, "run.completed") || containsEventTypes(events, "run.cancelled") {
		t.Fatalf("racing terminal events = %#v", eventTypes(events))
	}
}

func TestRegisterAIRejectsApprovalPolicies(t *testing.T) {
	tests := map[string]ai.Tool{
		"static":  {RequiresApproval: true},
		"dynamic": {NeedsApproval: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) { return ai.ApprovalDecision{}, nil }},
	}
	for name, tool := range tests {
		t.Run(name, func(t *testing.T) {
			registry := NewRegistry()
			definition := agents.DefineAI(agents.AIConfig{Tools: map[string]ai.Tool{"dangerous": tool}})
			err := RegisterAI(registry, "assistant", definition)
			if err == nil || !strings.Contains(err.Error(), "native approval delivery is not available") {
				t.Fatalf("RegisterAI error = %v", err)
			}
			if _, ok := registry.Lookup("assistant"); ok {
				t.Fatal("approval-gated AI agent was registered")
			}
			if err := registry.Register("bypass", AdaptAI(definition)); err == nil || !strings.Contains(err.Error(), "native approval delivery is not available") {
				t.Fatalf("direct registry bypass error = %v", err)
			}
		})
	}
}

func TestTypeScriptCompatibleRoutesResumeAndRunEvents(t *testing.T) {
	var inputs []string
	var inputsMu sync.Mutex
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(ctx context.Context, call StartCall, emit EventEmitter) error {
			var input string
			if err := json.Unmarshal(call.Input, &input); err != nil {
				return err
			}
			inputsMu.Lock()
			inputs = append(inputs, input)
			inputsMu.Unlock()
			return emit.Emit(ctx, "agent.output", map[string]string{"value": input})
		},
	}
	_, handler := newTestRuntime(t, "echo", adapter, Options{})
	startedResponse := postJSON(t, handler, testPrefix+"/sessions", `{"agentId":"echo","input":"first","metadata":{"phase":"initial"},"model":{"provider":"anthropic","model":"old"}}`, "203.0.113.20:1234", "")
	if startedResponse.Code != http.StatusAccepted {
		t.Fatalf("TypeScript-compatible start status = %d, body = %s", startedResponse.Code, startedResponse.Body.String())
	}
	var started startResponse
	if err := json.Unmarshal(startedResponse.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.20:1234", "")

	resumedResponse := postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/resume", `{"input":"second","metadata":{"phase":"resumed"},"model":{"provider":"openrouter","model":"new"}}`, "203.0.113.20:1234", "")
	if resumedResponse.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, body = %s", resumedResponse.Code, resumedResponse.Body.String())
	}
	var resumed startResponse
	if err := json.Unmarshal(resumedResponse.Body.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Session.ID != started.Session.ID || resumed.Run.ID == started.Run.ID {
		t.Fatalf("resume descriptor = %#v, want same session and a new run", resumed)
	}
	if resumed.Session.Metadata["phase"] != "resumed" || resumed.Session.Model.Provider != "openrouter" || resumed.Session.Model.Model != "new" {
		t.Fatalf("resume session metadata/model = %#v/%#v", resumed.Session.Metadata, resumed.Session.Model)
	}
	if resumed.Run.Model != resumed.Session.Model {
		t.Fatalf("resume run model = %#v, want session model %#v", resumed.Run.Model, resumed.Session.Model)
	}
	waitForRunIDStatus(t, handler, started.Session.ID, resumed.Run.ID, RunStatusCompleted, "203.0.113.20:1234", "")

	request := httptest.NewRequest(http.MethodGet, testPrefix+"/sessions/"+started.Session.ID+"/runs/"+resumed.Run.ID+"/events?after="+url.QueryEscape(started.Session.ID+":00000000000000000000"), nil)
	request.RemoteAddr = "203.0.113.20:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("run events status = %d, body = %s", response.Code, response.Body.String())
	}
	events := parseSSEEvents(t, response.Body.String())
	if got, want := eventTypes(events), []string{"run.started", "agent.output", "run.completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("run events = %#v, want %#v", got, want)
	}
	inputsMu.Lock()
	defer inputsMu.Unlock()
	if got, want := inputs, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agent inputs = %#v, want %#v", got, want)
	}
}

func TestTypeScriptCompatibleRespondAndCancelRoutes(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	responses := make(chan json.RawMessage, 1)
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(ctx context.Context, _ StartCall, _ EventEmitter) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
		RespondFunc: func(_ context.Context, call RespondCall, _ EventEmitter) error {
			responses <- cloneRaw(call.Response)
			releaseOnce.Do(func() { close(release) })
			return nil
		},
	}
	_, handler := newTestRuntime(t, "interactive", adapter, Options{})
	started := startTestSession(t, handler, "interactive", `{"input":null}`, "203.0.113.21:1234", "")
	responded := postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/respond", `{"interactionId":"approval-1","answers":{"approved":true}}`, "203.0.113.21:1234", "")
	if responded.Code != http.StatusAccepted {
		t.Fatalf("TypeScript-compatible respond status = %d, body = %s", responded.Code, responded.Body.String())
	}
	select {
	case response := <-responses:
		var value map[string]json.RawMessage
		if err := json.Unmarshal(response, &value); err != nil {
			t.Fatal(err)
		}
		if string(value["interactionId"]) != `"approval-1"` || string(value["answers"]) != `{"approved":true}` {
			t.Fatalf("response payload = %s", response)
		}
	case <-time.After(time.Second):
		t.Fatal("adapter did not receive interaction response")
	}
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.21:1234", "")

	cancelAdapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(ctx context.Context, _ StartCall, _ EventEmitter) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	_, cancelHandler := newTestRuntime(t, "cancelable", cancelAdapter, Options{})
	cancelled := startTestSession(t, cancelHandler, "cancelable", `{"input":null}`, "203.0.113.21:1234", "")
	cancelledResponse := postJSON(t, cancelHandler, testPrefix+"/sessions/"+cancelled.Session.ID+"/runs/"+cancelled.Run.ID+"/cancel", "", "203.0.113.21:1234", "")
	if cancelledResponse.Code != http.StatusAccepted {
		t.Fatalf("TypeScript-compatible cancel status = %d, body = %s", cancelledResponse.Code, cancelledResponse.Body.String())
	}
	waitForRunStatus(t, cancelHandler, cancelled.Session.ID, RunStatusCancelled, "203.0.113.21:1234", "")
}

func TestCancelledRunCannotEmitAfterTerminalEvent(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(ctx context.Context, _ StartCall, emit EventEmitter) error {
			close(entered)
			<-release // Deliberately ignore ctx to exercise a late adapter emission.
			return emit.Emit(ctx, "agent.late", map[string]bool{"unexpected": true})
		},
	}
	_, handler := newTestRuntime(t, "late", adapter, Options{})
	started := startTestSession(t, handler, "late", `{"input":null}`, "203.0.113.22:1234", "")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("adapter did not start")
	}
	cancelled := postJSON(t, handler, testPrefix+"/sessions/"+started.Session.ID+"/cancel", "", "203.0.113.22:1234", "")
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, body = %s", cancelled.Code, cancelled.Body.String())
	}
	close(release)
	waitForRunStatus(t, handler, started.Session.ID, RunStatusCancelled, "203.0.113.22:1234", "")
	events := getTerminalEvents(t, handler, started.Session.ID, "", "")
	if containsEventTypes(events, "agent.late") {
		t.Fatalf("late event survived cancellation: %#v", eventTypes(events))
	}
}

func TestSSEStreamsLiveEventsAfterLastEventID(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Public: true},
		StartFunc: func(ctx context.Context, _ StartCall, _ EventEmitter) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
		RespondFunc: func(ctx context.Context, _ RespondCall, emit EventEmitter) error {
			if err := emit.Emit(ctx, "agent.live", map[string]bool{"ok": true}); err != nil {
				return err
			}
			releaseOnce.Do(func() { close(release) })
			return nil
		},
	}
	_, handler := newTestRuntime(t, "live", adapter, Options{})
	server := httptest.NewServer(handler)
	defer server.Close()

	startHTTPResponse, err := http.Post(server.URL+testPrefix+"/live/sessions", "application/json", strings.NewReader(`{"input":null}`))
	if err != nil {
		t.Fatal(err)
	}
	defer startHTTPResponse.Body.Close()
	if startHTTPResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(startHTTPResponse.Body)
		t.Fatalf("start status = %d, body = %s", startHTTPResponse.StatusCode, body)
	}
	var started startResponse
	if err := json.NewDecoder(startHTTPResponse.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}

	getResponse, err := http.Get(server.URL + testPrefix + "/sessions/" + started.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var view SessionView
	if err := json.NewDecoder(getResponse.Body).Decode(&view); err != nil {
		getResponse.Body.Close()
		t.Fatal(err)
	}
	getResponse.Body.Close()

	eventsRequest, err := http.NewRequest(http.MethodGet, server.URL+testPrefix+"/sessions/"+started.Session.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventsRequest.Header.Set("Last-Event-ID", view.Cursor)
	eventsResponse, err := server.Client().Do(eventsRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer eventsResponse.Body.Close()

	respondHTTPResponse, err := http.Post(server.URL+testPrefix+"/sessions/"+started.Session.ID+"/respond", "application/json", strings.NewReader(`{"response":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	respondHTTPResponse.Body.Close()
	if respondHTTPResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("respond status = %d", respondHTTPResponse.StatusCode)
	}
	eventBody, err := io.ReadAll(eventsResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	events := parseSSEEvents(t, string(eventBody))
	if got, want := eventTypes(events), []string{"run.response.received", "agent.live", "run.completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("live event types = %#v, want %#v", got, want)
	}
}

func TestDurableDispatcherSeam(t *testing.T) {
	var directCalled atomic.Bool
	adapter := AdapterFuncs{
		AgentConfig: agents.Config{Durable: true, Public: true, TaskQueue: "support"},
		StartFunc: func(context.Context, StartCall, EventEmitter) error {
			directCalled.Store(true)
			return nil
		},
	}
	dispatcher := &testDispatcher{}
	_, handler := newTestRuntime(t, "durable", adapter, Options{Dispatcher: dispatcher})
	started := startTestSession(t, handler, "durable", `{"input":{"message":"hello"}}`, "203.0.113.14:1234", "")
	view := waitForRunStatus(t, handler, started.Session.ID, RunStatusCompleted, "203.0.113.14:1234", "")
	if directCalled.Load() {
		t.Fatal("durable agent executed its direct handler")
	}
	if !dispatcher.started.Load() || view.Runs[0].Mode != agents.DurableMode || view.Runs[0].TaskQueue != "support" {
		t.Fatalf("durable dispatch/view = started=%v run=%#v", dispatcher.started.Load(), view.Runs[0])
	}

	_, unavailable := newTestRuntime(t, "durable", adapter, Options{})
	response := postJSON(t, unavailable, testPrefix+"/durable/sessions", `{"input":null}`, "203.0.113.14:1234", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing dispatcher status = %d, body = %s", response.Code, response.Body.String())
	}
}

type testDispatcher struct {
	started atomic.Bool
}

func (dispatcher *testDispatcher) Start(ctx context.Context, _ Adapter, _ StartCall, emit EventEmitter) error {
	dispatcher.started.Store(true)
	return emit.Emit(ctx, "durable.dispatched", map[string]bool{"ok": true})
}

func (*testDispatcher) Respond(context.Context, Adapter, RespondCall, EventEmitter) error {
	return nil
}

func (*testDispatcher) Cancel(context.Context, Adapter, CancelCall, EventEmitter) error {
	return nil
}

func newTestRuntime(t *testing.T, agentID string, adapter Adapter, options Options) (*Runtime, http.Handler) {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(agentID, adapter); err != nil {
		t.Fatal(err)
	}
	var nextID atomic.Uint64
	options.Registry = registry
	options.Now = func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) }
	options.NewID = func(prefix string) (string, error) {
		return fmt.Sprintf("%s_%d", prefix, nextID.Add(1)), nil
	}
	runtime, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := runtime.Handler(testPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, handler
}

func startTestSession(t *testing.T, handler http.Handler, agentID, body, remoteAddr, actorID string) startResponse {
	t.Helper()
	response := postJSON(t, handler, testPrefix+"/"+agentID+"/sessions", body, remoteAddr, actorID)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", response.Code, response.Body.String())
	}
	var started startResponse
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	return started
}

func postJSON(t *testing.T, handler http.Handler, path, body, remoteAddr, actorID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Content-Type", "application/json")
	if actorID != "" {
		request.Header.Set("X-Test-Actor", actorID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func waitForRunStatus(t *testing.T, handler http.Handler, sessionID, status, remoteAddr, actorID string) SessionView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, testPrefix+"/sessions/"+sessionID, nil)
		request.RemoteAddr = remoteAddr
		if actorID != "" {
			request.Header.Set("X-Test-Actor", actorID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("get session status = %d, body = %s", response.Code, response.Body.String())
		}
		var view SessionView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Runs) == 1 && view.Runs[0].Status == status {
			return view
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run did not reach status %q", status)
	return SessionView{}
}

func waitForRunIDStatus(t *testing.T, handler http.Handler, sessionID, runID, status, remoteAddr, actorID string) SessionView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, testPrefix+"/sessions/"+sessionID, nil)
		request.RemoteAddr = remoteAddr
		if actorID != "" {
			request.Header.Set("X-Test-Actor", actorID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("get session status = %d, body = %s", response.Code, response.Body.String())
		}
		var view SessionView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		for _, run := range view.Runs {
			if run.ID == runID && run.Status == status {
				return view
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %q did not reach status %q", runID, status)
	return SessionView{}
}

func getTerminalEvents(t *testing.T, handler http.Handler, sessionID, cursor, lastEventID string) []Event {
	t.Helper()
	path := testPrefix + "/sessions/" + sessionID + "/events"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = "203.0.113.10:1234"
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("events status = %d, body = %s", response.Code, response.Body.String())
	}
	return parseSSEEvents(t, response.Body.String())
}

func parseSSEEvents(t *testing.T, body string) []Event {
	t.Helper()
	var events []Event
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
		}
	}
	return events
}

func eventTypes(events []Event) []string {
	types := make([]string, len(events))
	for index, event := range events {
		types[index] = event.Type
	}
	return types
}

func eventIDs(events []Event) []string {
	ids := make([]string, len(events))
	for index, event := range events {
		ids[index] = event.ID
	}
	return ids
}

func containsEventTypes(events []Event, required ...string) bool {
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Type] = true
	}
	for _, eventType := range required {
		if !seen[eventType] {
			return false
		}
	}
	return true
}
