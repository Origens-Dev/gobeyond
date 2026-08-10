package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/go-temporal-ai-sdk/activities"
	hostreviewconnector "github.com/Origens-Dev/go-temporal-ai-sdk/connectors/hostreview"
	"github.com/Origens-Dev/go-temporal-ai-sdk/temporalai"
	"github.com/Origens-Dev/go-temporal-ai-sdk/updates"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

type recordingDurableUpdates struct {
	mu    sync.Mutex
	order []string
}

func (s *recordingDurableUpdates) append(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, value)
}

func (s *recordingDurableUpdates) BeginPreview(context.Context, updates.PreviewBeginEvent) error {
	s.append("durable")
	return nil
}
func (s *recordingDurableUpdates) CheckpointPreview(context.Context, updates.PreviewSnapshotEvent) error {
	s.append("durable")
	return nil
}
func (s *recordingDurableUpdates) EndPreview(context.Context, updates.PreviewEndEvent) error {
	s.append("durable")
	return nil
}
func (s *recordingDurableUpdates) UpsertRecord(context.Context, updates.RecordUpsertEvent) error {
	s.append("durable")
	return nil
}
func (s *recordingDurableUpdates) EndStream(context.Context, updates.StreamEndEvent) error {
	s.append("durable")
	return nil
}

func TestAIRegistryHostedConnectorCommitsThenPublishesBoundIdentity(t *testing.T) {
	store := &recordingDurableUpdates{}
	var published updates.RecordUpsertEvent
	temporarySocket, err := os.CreateTemp("", "gb-agent-review-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket := temporarySocket.Name()
	if err := temporarySocket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.append("broker")
		if r.URL.Path != hostreviewconnector.IngestPath || r.Header.Get("X-Origens-Agent-Review-Protocol") != hostreviewconnector.Protocol {
			t.Errorf("review request path=%q protocol=%q", r.URL.Path, r.Header.Get("X-Origens-Agent-Review-Protocol"))
		}
		if err := json.NewDecoder(r.Body).Decode(&published); err != nil {
			t.Errorf("decode review event: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = os.Remove(socket)
	})
	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv(EnvHostReportSocket, socket)

	model := ai.NewMockLanguageModel("assistant")
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	registry := NewAIRegistry()
	if err := RegisterAI(nil, registry, "support", agents.DefineAI(agents.AIConfig{
		Durable: true, Model: "assistant", Provider: provider, Revision: "revision-1",
		DurableUpdates: store,
	})); err != nil {
		t.Fatal(err)
	}
	event := updates.NewRecordUpsertEvent("execution-1", updates.WorkflowRecord{
		RecordID: "message:1", RecordVersion: 1, Kind: updates.RecordKindMessage,
		Status: "completed", Data: map[string]any{"text": "hello"},
		Scope: updates.Scope{AgentID: "support"}, UpdatedAt: 1,
	}, "", 1)
	event.ConversationID = "conversation-1"
	event.CompiledRevision = "revision-1"
	if err := (agentUpdateRouter{registry: registry}).UpsertRecord(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.order, []string{"durable", "broker"}) {
		t.Fatalf("connector order = %v", store.order)
	}
	if published.AgentID != "support" || published.StreamID != "execution-1" || published.ConversationID != "conversation-1" {
		t.Fatalf("published identity = %#v", published.BaseEvent)
	}
}

func TestConventionHelpers(t *testing.T) {
	workflowName, err := WorkflowName("Support-Agent")
	if err != nil || workflowName != "gobeyond.agent.v1.support-agent.workflow" {
		t.Fatalf("WorkflowName = %q, %v", workflowName, err)
	}
	activityName, err := ActivityName("support-agent")
	if err != nil || activityName != "gobeyond.agent.v1.support-agent.activity" {
		t.Fatalf("ActivityName = %q, %v", activityName, err)
	}
	workflowID, err := WorkflowID("ses_1", "run_2")
	if err != nil || workflowID != "gobeyond-agent-run/ses_1/run_2" {
		t.Fatalf("WorkflowID = %q, %v", workflowID, err)
	}
	for _, agentID := range []string{"", "has_underscore", "has/slash"} {
		if _, err := WorkflowName(agentID); err == nil {
			t.Fatalf("WorkflowName(%q) succeeded", agentID)
		}
	}
	if _, err := WorkflowID("bad/session", "run_2"); err == nil {
		t.Fatal("WorkflowID accepted an invalid session ID")
	}
}

func TestHostedDispatcherUsesBoundUnixBrokerWithoutTemporalCredentials(t *testing.T) {
	temporarySocket, err := os.CreateTemp("", "gb-agent-host-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket := temporarySocket.Name()
	if err := temporarySocket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case hostAgentExecutePath:
			var request hostedAgentExecuteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode execute: %v", err)
			}
			if request.AgentID != "support-agent" || request.Kind != "typed" || request.WorkerID != "support" || request.SessionID != "ses_1" || request.RunID != "run_2" {
				t.Errorf("execute request = %#v", request)
			}
			_, _ = w.Write([]byte(`{"result":{"output":{"answer":42}}}`))
		case hostWorkflowSignalPath, hostWorkflowCancelPath:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = os.Remove(socket)
	})

	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv(EnvHostReportSocket, socket)
	t.Setenv(EnvEnvironment, "production")
	t.Setenv(EnvAddress, "")
	t.Setenv(EnvNamespace, "")
	dispatcher, err := NewLazyFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	emitter := &recordingEmitter{}
	adapter := httpruntime.AdapterFuncs{AgentConfig: agents.Config{Durable: true, TaskQueue: "support"}}
	call := durableStartCall()
	if err := dispatcher.Start(context.Background(), adapter, call, emitter); err != nil {
		t.Fatal(err)
	}
	if emitter.eventType != "agent.output" || string(emitter.data.(json.RawMessage)) != `{"answer":42}` {
		t.Fatalf("emitted = %q %#v", emitter.eventType, emitter.data)
	}
	if err := dispatcher.hosted.signal(context.Background(), "workflow-1", "approve", map[string]bool{"approved": true}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Cancel(context.Background(), adapter, httpruntime.CancelCall{Session: call.Session, Run: call.Run}, emitter); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{hostAgentExecutePath, hostWorkflowSignalPath, hostWorkflowCancelPath}) {
		t.Fatalf("paths = %v", paths)
	}
}

func TestStartUsesGeneratedContractQueueAndEmitsJSONOutput(t *testing.T) {
	fake := &fakeClient{run: &fakeRun{output: json.RawMessage(`{"answer":42}`)}}
	dispatcher, err := New(context.Background(), Options{Client: fake, Environment: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	emitter := &recordingEmitter{}
	adapter := httpruntime.AdapterFuncs{AgentConfig: agents.Config{Durable: true, TaskQueue: "support"}}
	call := durableStartCall()
	if err := dispatcher.Start(context.Background(), adapter, call, emitter); err != nil {
		t.Fatal(err)
	}

	if fake.options.ID != "gobeyond-agent-run/ses_1/run_2" || fake.options.TaskQueue != "support__preview" {
		t.Fatalf("start options = %#v", fake.options)
	}
	if fake.workflow != "gobeyond.agent.v1.support-agent.workflow" {
		t.Fatalf("workflow = %#v", fake.workflow)
	}
	if len(fake.args) != 1 {
		t.Fatalf("args = %#v", fake.args)
	}
	input, ok := fake.args[0].(agents.DurableRunInput)
	if !ok || input.Session.ID != "ses_1" || input.Run.ID != "run_2" || string(input.Input) != `{"question":"hi"}` {
		t.Fatalf("durable input = %#v", fake.args[0])
	}
	if emitter.eventType != "agent.output" || string(emitter.data.(json.RawMessage)) != `{"answer":42}` {
		t.Fatalf("emitted = %q %#v", emitter.eventType, emitter.data)
	}
}

func TestStartAIAgentUsesStableSDKRootAndCompiledRuntime(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, TaskQueue: "support", Model: "assistant", Provider: provider,
		Instructions: "Be useful.", Revision: "revision-1", MaxSteps: 4,
	})
	fake := &fakeClient{run: &fakeRun{output: temporalai.AgentResult{
		AgentID: "support-agent", ModelID: "assistant", Text: "hello", FinishReason: ai.FinishStop,
	}}}
	dispatcher, err := New(context.Background(), Options{Client: fake, Environment: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	emitter := &recordingEmitter{}
	call := durableStartCall()
	call.Input = json.RawMessage(`{"messages":[{"role":"user","parts":[{"type":"text","text":"hi"}]}]}`)
	if err := dispatcher.Start(context.Background(), httpruntime.AdaptAI(definition), call, emitter); err != nil {
		t.Fatal(err)
	}
	if fake.workflow != temporalai.AgentWorkflowName || fake.options.TaskQueue != "support__preview" {
		t.Fatalf("AI workflow/options = %#v/%#v", fake.workflow, fake.options)
	}
	input, ok := fake.args[0].(temporalai.AgentInput)
	if !ok || input.AgentID != "support-agent" || input.CompiledRevision != "revision-1" || input.ModelID != "assistant" || input.MaxSteps != 4 || input.Stream.StreamID != "run_2" || input.Stream.ConversationID != "ses_1" || len(input.Messages) != 1 {
		t.Fatalf("AI Temporal input = %#v", fake.args[0])
	}
	if output, ok := emitter.data.(agents.AIOutput); !ok || output.Text != "hello" {
		t.Fatalf("AI emitted output = %#v", emitter.data)
	}

	runtimes := NewAIRegistry()
	if err := RegisterAI(nil, runtimes, "support-agent", definition); err != nil {
		t.Fatal(err)
	}
	runtime, err := runtimes.ResolveAgentRuntime(context.Background(), activities.RuntimeScope{AgentID: "support-agent", CompiledRevision: "revision-1"})
	if err != nil || runtime.AgentID != "support-agent" || runtime.CompiledRevision != "revision-1" || runtime.ModelProvider.LanguageModel("assistant") == nil {
		t.Fatalf("resolved AI runtime = %#v, err = %v", runtime, err)
	}
	for _, scope := range []activities.RuntimeScope{
		{AgentID: "support-agent"},
		{AgentID: "support-agent", CompiledRevision: "revision-stale"},
		{AgentID: "missing-agent", CompiledRevision: "revision-1"},
	} {
		_, err := runtimes.ResolveAgentRuntime(context.Background(), scope)
		var applicationErr *temporal.ApplicationError
		if !errors.As(err, &applicationErr) || applicationErr.Type() != activities.RuntimeMismatchErrorType || !applicationErr.NonRetryable() {
			t.Fatalf("ResolveAgentRuntime(%#v) error = %v", scope, err)
		}
	}
	secondRevision := definition
	secondRevision.AI.Revision = "revision-2"
	if err := RegisterAI(nil, runtimes, "support-agent", secondRevision); err != nil {
		t.Fatalf("register second exact revision: %v", err)
	}
	resolvedSecond, err := runtimes.ResolveAgentRuntime(context.Background(), activities.RuntimeScope{AgentID: "support-agent", CompiledRevision: "revision-2"})
	if err != nil || resolvedSecond.CompiledRevision != "revision-2" {
		t.Fatalf("resolved second revision = %#v, err = %v", resolvedSecond, err)
	}
}

func TestStartAIAgentResolvesToolQueuesAndRealtimeBoundaries(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	tool := agents.DefineTool(agents.ToolConfig{TaskQueue: "tools"}, func(context.Context, agents.Actor, string) (string, error) {
		return "ok", nil
	})
	fake := &fakeClient{run: &fakeRun{output: temporalai.AgentResult{ModelID: "assistant"}}}
	dispatcher, err := New(context.Background(), Options{Client: fake, Environment: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, TaskQueue: "support", Model: "assistant", Provider: provider,
		Revision: "revision-1", Tools: map[string]ai.Tool{"lookup": tool},
	})
	if err := dispatcher.Start(context.Background(), httpruntime.AdaptAI(definition), durableStartCall(), &recordingEmitter{}); err != nil {
		t.Fatal(err)
	}
	input := fake.args[0].(temporalai.AgentInput)
	if len(input.Tools) != 1 || input.Tools[0].TaskQueue != "tools__preview" {
		t.Fatalf("durable tool definitions = %#v", input.Tools)
	}

	fake.run = &fakeRun{output: temporalai.AgentResult{ModelID: "assistant"}}
	realtime := definition
	realtime.Config.Realtime = true
	realtime.AI.Realtime = true
	if err := dispatcher.Start(context.Background(), httpruntime.AdaptAI(realtime), durableStartCall(), &recordingEmitter{}); err != nil {
		t.Fatal(err)
	}
	realtimeInput := fake.args[0].(temporalai.AgentInput)
	if realtimeInput.DefaultModelBoundary != activities.ToolExecutionBoundaryLocalActivity ||
		realtimeInput.DefaultToolBoundary != activities.ToolExecutionBoundaryLocalActivity ||
		realtimeInput.LocalToolTimeoutFallback != temporalai.LocalToolTimeoutFallbackNone ||
		len(realtimeInput.Tools) != 1 || realtimeInput.Tools[0].TaskQueue != "" {
		t.Fatalf("realtime input = %#v", realtimeInput)
	}
}

func TestRegisterAIRejectsApprovalPolicies(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	tests := map[string]ai.Tool{
		"static":  {RequiresApproval: true},
		"dynamic": {NeedsApproval: func(context.Context, ai.ToolCall) (ai.ApprovalDecision, error) { return ai.ApprovalDecision{}, nil }},
	}
	for name, tool := range tests {
		t.Run(name, func(t *testing.T) {
			definition := agents.DefineAI(agents.AIConfig{
				Durable: true, Model: "assistant", Provider: provider, Revision: "revision-1",
				Tools: map[string]ai.Tool{"dangerous": tool},
			})
			runtimes := NewAIRegistry()
			err := RegisterAI(nil, runtimes, "support-agent", definition)
			if err == nil || !strings.Contains(err.Error(), "native approval delivery is not available") {
				t.Fatalf("RegisterAI error = %v", err)
			}
		})
	}
}

func TestExecuteActivityDecodesInvokesAndEncodesTypedDefinition(t *testing.T) {
	type input struct {
		Question string `json:"question"`
	}
	type output struct {
		Answer string `json:"answer"`
	}
	definition := agents.Define(agents.Config{Durable: true}, func(_ context.Context, actor agents.Actor, value input) (output, error) {
		return output{Answer: actor.ID + ":" + value.Question}, nil
	})
	result, err := executeActivity(context.Background(), definition, agents.DurableRunInput{
		Actor: agents.Actor{ID: "user-1", Kind: "user"},
		Input: json.RawMessage(`{"question":"hello"}`),
	})
	if err != nil || string(result.Output) != `{"answer":"user-1:hello"}` {
		t.Fatalf("executeActivity = %s, %v", result.Output, err)
	}
	if _, err := executeActivity(context.Background(), definition, agents.DurableRunInput{
		Actor: agents.Actor{ID: "user-1", Kind: "user"}, Input: json.RawMessage(`{"question":`),
	}); err == nil {
		t.Fatal("executeActivity accepted invalid JSON")
	}
}

func TestStartUsesDefaultLogicalQueueAndRejectsInvalidOutput(t *testing.T) {
	fake := &fakeClient{run: &fakeRun{output: json.RawMessage(`not-json`)}}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	adapter := httpruntime.AdapterFuncs{AgentConfig: agents.Config{Durable: true}}
	err = dispatcher.Start(context.Background(), adapter, durableStartCall(), &recordingEmitter{})
	if err == nil || fake.options.TaskQueue != "default__local" {
		t.Fatalf("Start error = %v, queue = %q", err, fake.options.TaskQueue)
	}
}

func TestCancelRespondAndCloseLifecycle(t *testing.T) {
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	call := durableStartCall()
	if err := dispatcher.Cancel(context.Background(), nil, httpruntime.CancelCall{Session: call.Session, Run: call.Run}, nil); err != nil {
		t.Fatal(err)
	}
	if fake.cancelWorkflowID != "gobeyond-agent-run/ses_1/run_2" || fake.cancelRunID != "" {
		t.Fatalf("cancel = %q %q", fake.cancelWorkflowID, fake.cancelRunID)
	}
	if err := dispatcher.Respond(context.Background(), nil, httpruntime.RespondCall{}, nil); !errors.Is(err, httpruntime.ErrRespondUnsupported) {
		t.Fatalf("Respond error = %v", err)
	}
	dispatcher.Close()
	dispatcher.Close()
	if fake.closeCalls != 1 {
		t.Fatalf("Close calls = %d", fake.closeCalls)
	}
	if err := dispatcher.Cancel(context.Background(), nil, httpruntime.CancelCall{Session: call.Session, Run: call.Run}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Cancel after Close error = %v", err)
	}
}

func TestRespondSignalsDurableAIApproval(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, Model: "assistant", Provider: provider, Revision: "revision-1",
	})
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	call := durableStartCall()
	err = dispatcher.Respond(context.Background(), httpruntime.AdaptAI(definition), httpruntime.RespondCall{
		Session: call.Session, Run: call.Run,
		Response: json.RawMessage(`{"interactionId":"approval-1","answers":{"approved":true,"reason":"looks safe"}}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.signalWorkflowID != "gobeyond-agent-run/ses_1/run_2" || fake.signalName != temporalai.ToolApprovalResponseSignalName("approval-1") {
		t.Fatalf("signal = %q %q", fake.signalWorkflowID, fake.signalName)
	}
	response, ok := fake.signalValue.(temporalai.ToolApprovalResponse)
	if !ok || !response.Approved || response.Reason != "looks safe" {
		t.Fatalf("signal value = %#v", fake.signalValue)
	}
}

func TestFactoryAndLocalEnvironmentOptions(t *testing.T) {
	fake := &fakeClient{}
	var got client.Options
	factory := func(_ context.Context, options client.Options) (Client, error) {
		got = options
		return fake, nil
	}
	dispatcher, err := New(context.Background(), Options{Factory: factory, Address: "temporal:7233", Namespace: "site"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.Close()
	if got.HostPort != "temporal:7233" || got.Namespace != "site" {
		t.Fatalf("client options = %#v", got)
	}

	t.Setenv(EnvAddress, "env-temporal:7233")
	t.Setenv(EnvNamespace, "env-site")
	t.Setenv(EnvEnvironment, "development")
	if options := localOptionsFromEnv(); !reflect.DeepEqual(options, Options{
		Address: "env-temporal:7233", Namespace: "env-site", Environment: "development",
	}) {
		t.Fatalf("local options = %#v", options)
	}
}

func durableStartCall() httpruntime.StartCall {
	actor := agents.Actor{ID: "user-1", Kind: "user"}
	session := agents.Session{ID: "ses_1", AgentID: "support-agent", Actor: actor}
	run := agents.Run{ID: "run_2", SessionID: session.ID, AgentID: session.AgentID, Mode: agents.DurableMode}
	return httpruntime.StartCall{Session: session, Run: run, Actor: actor, Input: json.RawMessage(`{"question":"hi"}`)}
}

type fakeClient struct {
	mu               sync.Mutex
	run              client.WorkflowRun
	options          client.StartWorkflowOptions
	workflow         interface{}
	args             []interface{}
	cancelWorkflowID string
	cancelRunID      string
	closeCalls       int
	signalWorkflowID string
	signalName       string
	signalValue      interface{}
}

func (fake *fakeClient) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.options = options
	fake.workflow = workflow
	fake.args = args
	return fake.run, nil
}

func (fake *fakeClient) CancelWorkflow(_ context.Context, workflowID, runID string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.cancelWorkflowID = workflowID
	fake.cancelRunID = runID
	return nil
}

func (fake *fakeClient) SignalWorkflow(_ context.Context, workflowID, _ string, signalName string, value interface{}) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.signalWorkflowID = workflowID
	fake.signalName = signalName
	fake.signalValue = value
	return nil
}

func (fake *fakeClient) Close() {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.closeCalls++
}

type fakeRun struct {
	output interface{}
}

func (*fakeRun) GetID() string    { return "workflow-id" }
func (*fakeRun) GetRunID() string { return "temporal-run-id" }
func (run *fakeRun) Get(_ context.Context, value interface{}) error {
	switch output := value.(type) {
	case *agents.DurableRunOutput:
		output.Output = append(json.RawMessage(nil), run.output.(json.RawMessage)...)
	case *temporalai.AgentResult:
		*output = run.output.(temporalai.AgentResult)
	default:
		return errors.New("unexpected workflow result type")
	}
	return nil
}
func (run *fakeRun) GetWithOptions(ctx context.Context, value interface{}, _ client.WorkflowRunGetOptions) error {
	return run.Get(ctx, value)
}

type recordingEmitter struct {
	eventType string
	data      interface{}
}

func (emitter *recordingEmitter) Emit(_ context.Context, eventType string, data interface{}) error {
	emitter.eventType = eventType
	emitter.data = data
	return nil
}
