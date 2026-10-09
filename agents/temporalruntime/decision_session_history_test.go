package temporalruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
)

const (
	decisionHistoryAudioSentinel      = "RAW_AUDIO_HISTORY_SENTINEL"
	decisionHistoryTranscriptSentinel = "RAW_TRANSCRIPT_HISTORY_SENTINEL"
	decisionHistoryDirectorySentinel  = "RAW_DIRECTORY_HISTORY_SENTINEL"
	decisionHistoryPromptSentinel     = "RENDERED_PERSONAL_HISTORY_SENTINEL"
)

const (
	decisionHistorySawAudio uint32 = 1 << iota
	decisionHistorySawTranscript
	decisionHistorySawDirectory
	decisionHistorySawPrompt
	decisionHistorySawAll = decisionHistorySawAudio | decisionHistorySawTranscript | decisionHistorySawDirectory | decisionHistorySawPrompt
)

// TestDecisionSessionCapturedHistoryReplaysAcrossReplacementHost uses a real
// Temporal server history. Set GOBEYOND_TEMPORAL_START_DEV_SERVER=1 to launch
// an isolated local dev server, or GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS to
// use an already running loopback dev server. The replacement worker and the
// WorkflowReplayer register only the deterministic Decision workflow; neither
// has provider or directory adapters or activities available to re-query.
func TestDecisionSessionCapturedHistoryReplaysAcrossReplacementHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	historyClient, hostPort := decisionSessionHistoryClient(t, ctx)
	dispatcherClient, err := client.DialContext(ctx, client.Options{HostPort: hostPort, Namespace: "default"})
	if err != nil {
		t.Fatalf("dial isolated Temporal server for host dispatcher: %v", err)
	}
	input := decisionSessionInputFixture(t, fmt.Sprintf("ses_history_%d", time.Now().UnixNano()), 41)
	baseTime := time.Now().UTC().Add(-10 * time.Second)
	input.Admission.ReceivedAt = baseTime
	if err := ValidateDecisionSessionInput(input); err != nil {
		t.Fatalf("captured history input: %v", err)
	}
	identity := decisionSessionIdentity(input)
	workflowID, err := decisionSessionWorkflowID(httpruntime.StartCall{
		Session: agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:     agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
	})
	if err != nil {
		t.Fatal(err)
	}

	taskQueue := fmt.Sprintf("decision-history-%d", time.Now().UnixNano())
	physicalQueue := taskQueue + "__history"
	dispatcher, err := New(ctx, Options{Client: dispatcherClient, Environment: "history"})
	if err != nil {
		dispatcherClient.Close()
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)

	var originalWorker, replacementWorker worker.Worker
	t.Cleanup(func() {
		if replacementWorker != nil {
			replacementWorker.Stop()
		}
		if originalWorker != nil {
			originalWorker.Stop()
		}
	})
	startWorker := func(identity string) worker.Worker {
		w := worker.New(historyClient, physicalQueue, worker.Options{Identity: identity, WorkerStopTimeout: time.Second})
		RegisterDecisionSessionWorkflow(w)
		if err := w.Start(); err != nil {
			t.Fatalf("start %s: %v", identity, err)
		}
		return w
	}
	workerSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	originalWorker = startWorker("decision-history-original-" + workerSuffix)

	probe := &decisionSessionCapturedHistoryProbe{}
	transcriptRef := decisionRef("protected_transcript_history", "final-transcript", "b", input, time.Now().Add(time.Hour))
	candidateRef := decisionRef("snapshot_history_ref", "directory-snapshot", "a", input, time.Now().Add(time.Hour))
	candidateSet := decisionv1.CandidateSetSnapshot{
		CandidateIDs: []string{"candidate_opaque_history"}, ProtectedSnapshot: candidateRef,
	}
	candidateSet.SHA256 = candidateSet.CanonicalSHA256()
	entryRoute := decisionRouteByID(t, input.Definition, input.Definition.Graph.Entry)
	adapter := newDecisionSessionCapturedHistoryAdapter(input, taskQueue, identity, baseTime, transcriptRef, candidateRef, candidateSet, entryRoute.Match.BindingID, probe)
	startCall := httpruntime.StartCall{
		Session: agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:     agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Input: json.RawMessage(fmt.Sprintf(
			`{"audio":%q,"transcript":%q,"renderedPrompt":%q}`,
			decisionHistoryAudioSentinel, decisionHistoryTranscriptSentinel, decisionHistoryPromptSentinel,
		)),
	}
	emitter := &recordingDecisionEmitter{}
	startDone := make(chan error, 1)
	go func() { startDone <- dispatcher.Start(ctx, adapter, startCall, emitter) }()

	snapshot := waitDecisionSessionHistorySnapshot(t, ctx, historyClient, workflowID, "")
	if snapshot.Identity != identity || snapshot.Status != DecisionSessionRunning {
		t.Fatalf("initial captured session snapshot = %#v", snapshot)
	}
	respond := func(host *decisionSessionCapturedHistoryAdapter, current DecisionSessionSnapshot, step string) DecisionSessionSnapshot {
		t.Helper()
		host.setSnapshot(current)
		response := decisionSessionHistoryResponse(step)
		if err := dispatcher.Respond(ctx, host, httpruntime.RespondCall{
			Session: startCall.Session, Run: startCall.Run, Response: response,
		}, emitter); err != nil {
			t.Fatalf("respond %q: %v", step, err)
		}
		return waitDecisionSessionHistorySnapshot(t, ctx, historyClient, workflowID, "")
	}

	snapshot = respond(adapter, snapshot, "input")
	snapshot = respond(adapter, snapshot, "snapshot")
	snapshot = respond(adapter, snapshot, "match")
	snapshot = respond(adapter, snapshot, "decision")
	if snapshot.View.DecisionCalls != 1 || snapshot.View.RouteID != "/clarify" || snapshot.Status != DecisionSessionRunning {
		t.Fatalf("recorded decision transition = %#v", snapshot)
	}
	if got := probe.directoryQueries.Load(); got != 1 {
		t.Fatalf("synthetic host directory lookups before replacement = %d, want 1", got)
	}
	if got := probe.providerCalls.Load(); got != 1 {
		t.Fatalf("synthetic host provider requests before replacement = %d, want 1", got)
	}

	// Stop the first host worker with the workflow still open. The next query is
	// serviced by a fresh worker with no adapters or activities registered.
	originalWorker.Stop()
	originalWorker = nil
	replacementWorker = startWorker("decision-history-replacement-" + workerSuffix)

	replayedSnapshot := waitDecisionSessionHistorySnapshot(t, ctx, historyClient, workflowID, "")
	if replayedSnapshot.Identity != identity || replayedSnapshot.View.DecisionCalls != 1 || replayedSnapshot.View.RouteID != "/clarify" {
		t.Fatalf("replacement worker replay changed recorded decision: %#v", replayedSnapshot)
	}
	assertDecisionHistoryHostLookupsUnchanged(t, probe, 1, 1, "replacement worker replay")

	replacementAdapter := newDecisionSessionCapturedHistoryAdapter(input, taskQueue, identity, baseTime, transcriptRef, candidateRef, candidateSet, entryRoute.Match.BindingID, probe)
	replacementAdapter.setSnapshot(replayedSnapshot)
	stale := decisionSessionHistoryResponse("stale-owner")
	if err := dispatcher.Respond(ctx, replacementAdapter, httpruntime.RespondCall{
		Session: startCall.Session, Run: startCall.Run, Response: stale,
	}, emitter); err == nil || !strings.Contains(err.Error(), "decision callback identity does not match the pinned session") {
		t.Fatalf("stale prior-owner/generation update was not rejected by the pinned identity validator: %v", err)
	}
	stillPinned := waitDecisionSessionHistorySnapshot(t, ctx, historyClient, workflowID, "")
	if stillPinned.Identity != identity || stillPinned.View.DecisionCalls != 1 || stillPinned.View.RouteID != "/clarify" {
		t.Fatalf("stale owner update changed the pinned session: %#v", stillPinned)
	}
	assertDecisionHistoryHostLookupsUnchanged(t, probe, 1, 1, "stale owner rejection")

	if err := dispatcher.Cancel(ctx, replacementAdapter, httpruntime.CancelCall{Session: startCall.Session, Run: startCall.Run}, emitter); err != nil {
		t.Fatalf("cancel from replacement host: %v", err)
	}
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("original dispatcher Start completion: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("decision session did not complete after replacement-host cancellation")
	}
	cancelled := waitDecisionSessionHistorySnapshot(t, ctx, historyClient, workflowID, "")
	if cancelled.Status != DecisionSessionCancelled || cancelled.Identity != identity || cancelled.View.DecisionCalls != 1 {
		t.Fatalf("replacement-host cancellation snapshot = %#v", cancelled)
	}
	assertDecisionHistoryHostLookupsUnchanged(t, probe, 1, 1, "replacement-host cancellation")

	// Stop the replacement worker before fetching history to ensure replay below
	// consumes only the persisted server history.
	replacementWorker.Stop()
	replacementWorker = nil

	info, err := historyClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		t.Fatalf("describe captured decision execution: %v", err)
	}
	if info.WorkflowExecutionInfo == nil || info.WorkflowExecutionInfo.Execution == nil || info.WorkflowExecutionInfo.Execution.RunId == "" {
		t.Fatal("Temporal did not return a concrete execution identity")
	}
	runID := info.WorkflowExecutionInfo.Execution.RunId
	history := &historypb.History{}
	iterator := historyClient.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatalf("read captured Temporal history: %v", err)
		}
		history.Events = append(history.Events, event)
	}
	if len(history.Events) == 0 {
		t.Fatal("Temporal returned empty workflow history")
	}
	serializedHistory, err := proto.Marshal(history)
	if err != nil {
		t.Fatalf("marshal captured Temporal history: %v", err)
	}
	for _, sentinel := range []string{
		decisionHistoryAudioSentinel, decisionHistoryTranscriptSentinel,
		decisionHistoryDirectorySentinel, decisionHistoryPromptSentinel,
	} {
		if bytes.Contains(serializedHistory, []byte(sentinel)) {
			t.Fatalf("captured Temporal history contains sensitive sentinel %q", sentinel)
		}
	}
	for _, stableIdentity := range []string{"recorded-provider-history", "recorded-model-history", "recorded-revision-history", candidateSet.SHA256} {
		if !bytes.Contains(serializedHistory, []byte(stableIdentity)) {
			t.Fatalf("captured Temporal history omitted recorded outcome identity %q", stableIdentity)
		}
	}
	for _, event := range history.Events {
		if event.GetActivityTaskScheduledEventAttributes() != nil {
			t.Fatal("decision history scheduled an activity; provider and directory work must remain host-side")
		}
	}

	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(DecisionSessionWorkflow, workflow.RegisterOptions{Name: DecisionSessionWorkflowName})
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatalf("replay actual Temporal server history: %v", err)
	}
	assertDecisionHistoryHostLookupsUnchanged(t, probe, 1, 1, "WorkflowReplayer")
	if got := probe.rawSentinels.Load(); got != decisionHistorySawAll {
		t.Fatalf("host test adapter did not receive all raw sentinel inputs: mask=%04b", got)
	}
}

func decisionSessionHistoryClient(t *testing.T, ctx context.Context) (client.Client, string) {
	t.Helper()
	if address := os.Getenv("GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS"); address != "" {
		host, _, err := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			t.Fatal("captured-history test requires a loopback Temporal dev server")
		}
		c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: "default"})
		if err != nil {
			t.Fatalf("dial isolated Temporal dev server: %v", err)
		}
		t.Cleanup(c.Close)
		return c, address
	}
	if os.Getenv("GOBEYOND_TEMPORAL_START_DEV_SERVER") != "1" {
		t.Skip("set GOBEYOND_TEMPORAL_START_DEV_SERVER=1 or GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS to run real-history replay")
	}
	server, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{Version: "default", DestDir: t.TempDir()},
		ClientOptions:  &client.Options{Namespace: "default"},
		LogLevel:       "error",
		LogFormat:      "json",
		Stdout:         io.Discard,
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatalf("start isolated Temporal dev server: %v", err)
	}
	c := server.Client()
	t.Cleanup(func() {
		c.Close()
		if err := server.Stop(); err != nil {
			t.Errorf("stop isolated Temporal dev server: %v", err)
		}
	})
	return c, server.FrontendHostPort()
}

func waitDecisionSessionHistorySnapshot(t *testing.T, ctx context.Context, c client.Client, workflowID, runID string) DecisionSessionSnapshot {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		value, err := c.QueryWorkflow(ctx, workflowID, runID, DecisionSessionSnapshotQuery)
		if err == nil {
			var snapshot DecisionSessionSnapshot
			if err := value.Get(&snapshot); err == nil {
				return snapshot
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Decision session query: %v (last query error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func assertDecisionHistoryHostLookupsUnchanged(t *testing.T, probe *decisionSessionCapturedHistoryProbe, wantDirectory, wantProvider int32, phase string) {
	t.Helper()
	if got := probe.directoryQueries.Load(); got != wantDirectory {
		t.Fatalf("directory queries after %s = %d, want %d", phase, got, wantDirectory)
	}
	if got := probe.providerCalls.Load(); got != wantProvider {
		t.Fatalf("provider calls after %s = %d, want %d", phase, got, wantProvider)
	}
}

type decisionSessionCapturedHistoryProbe struct {
	directoryQueries atomic.Int32
	providerCalls    atomic.Int32
	rawSentinels     atomic.Uint32
}

type decisionSessionCapturedHistoryAdapter struct {
	*decisionSessionAdapterFake
	probe        *decisionSessionCapturedHistoryProbe
	input        DecisionSessionInput
	transcript   *decisionv1.ProtectedReference
	candidateRef *decisionv1.ProtectedReference
	candidateSet decisionv1.CandidateSetSnapshot
	bindingID    string
	baseTime     time.Time
	snapshotMu   sync.Mutex
	snapshot     DecisionSessionSnapshot
}

func newDecisionSessionCapturedHistoryAdapter(
	input DecisionSessionInput,
	taskQueue string,
	identity DecisionSessionIdentity,
	baseTime time.Time,
	transcript, candidateRef *decisionv1.ProtectedReference,
	candidateSet decisionv1.CandidateSetSnapshot,
	bindingID string,
	probe *decisionSessionCapturedHistoryProbe,
) *decisionSessionCapturedHistoryAdapter {
	base := &decisionSessionAdapterFake{
		config:       agents.Config{Durable: true, TaskQueue: taskQueue},
		definition:   input.Definition,
		input:        input,
		cancellation: DecisionSessionCancel{Identity: identity},
	}
	return &decisionSessionCapturedHistoryAdapter{
		decisionSessionAdapterFake: base, probe: probe, input: input,
		transcript: transcript, candidateRef: candidateRef, candidateSet: candidateSet,
		bindingID: bindingID, baseTime: baseTime,
	}
}

func (adapter *decisionSessionCapturedHistoryAdapter) PrepareDecisionSession(call httpruntime.StartCall) (DecisionSessionInput, error) {
	adapter.observeRaw(string(call.Input))
	return adapter.decisionSessionAdapterFake.PrepareDecisionSession(call)
}

func (adapter *decisionSessionCapturedHistoryAdapter) PrepareDecisionResponse(call httpruntime.RespondCall) (DecisionSessionUpdate, error) {
	adapter.observeRaw(string(call.Response))
	var request struct {
		Step string `json:"step"`
	}
	if err := json.Unmarshal(call.Response, &request); err != nil {
		return DecisionSessionUpdate{}, fmt.Errorf("decode captured-history test step: %w", err)
	}
	snapshot := adapter.currentSnapshot()
	if snapshot.Identity.Generation == 0 {
		return DecisionSessionUpdate{}, errors.New("captured-history adapter has no current workflow snapshot")
	}
	entry := snapshot.View.RouteEntryID
	routeID := adapter.input.Definition.Graph.Entry
	scope := adapter.input.Admission
	base := adapter.baseTime
	switch request.Step {
	case "input":
		event := decisionv1.NormalizedEvent{
			ID: "history_input_final", Kind: decisionv1.EventInputFinal, TenantID: scope.TenantID,
			SessionID: scope.SessionID, Generation: adapter.input.Pin.Generation,
			RouteID: routeID, RouteEntryID: entry, InputWindowID: snapshot.View.InputWindowID,
			Channel: decisionv1.ChannelVoice, Modality: decisionv1.ModalitySpeechFinal,
			Locale: adapter.input.Pin.Locale, ProtectedInput: adapter.transcript,
			SourceEventIDs: []string{"history_source_input"}, ReceivedAt: base.Add(time.Second),
		}
		return sessionUpdate(adapter.input, entry, decisions.Event{Normalized: &event}), nil
	case "snapshot":
		adapter.probe.directoryQueries.Add(1)
		refresh := decisions.SnapshotRefresh{
			ID: "history_snapshot_refresh", TenantID: scope.TenantID, SessionID: scope.SessionID,
			Generation: adapter.input.Pin.Generation, RouteID: routeID, RouteEntryID: entry,
			ReceivedAt: base.Add(2 * time.Second),
			Bound: decisions.BoundCandidateSnapshot{
				BindingID: adapter.bindingID, RouteID: routeID, RouteEntryID: entry,
				Snapshot: adapter.candidateSet,
			},
		}
		update := sessionUpdate(adapter.input, entry, decisions.Event{Snapshot: &refresh})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*adapter.candidateRef}
		return update, nil
	case "match":
		match := decisionv1.NormalizedEvent{
			ID: "history_match_completed", Kind: decisionv1.EventMatchCompleted, TenantID: scope.TenantID,
			SessionID: scope.SessionID, Generation: adapter.input.Pin.Generation,
			RouteID: routeID, RouteEntryID: entry, Channel: decisionv1.ChannelVoice,
			ReceivedAt: base.Add(3 * time.Second),
			Match: &decisionv1.MatchResult{
				Outcome: decisionv1.OutcomeNoMatch, SnapshotSHA256: adapter.candidateSet.SHA256,
				BindingID: adapter.bindingID,
			},
		}
		update := sessionUpdate(adapter.input, entry, decisions.Event{Normalized: &match, InputID: "history_input_final"})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*adapter.candidateRef}
		return update, nil
	case "decision":
		adapter.probe.providerCalls.Add(1)
		result := &decisionv1.DecisionResult{
			Outcome: decisionv1.OutcomeNoMatch, ProviderRef: "recorded-provider-history",
			ModelRef: "recorded-model-history", Revision: "recorded-revision-history",
			ResultSchemaSHA256: strings.Repeat("d", 64), CandidateSetSHA256: adapter.candidateSet.SHA256,
			Usage: decisionv1.UsageRecord{Status: decisionv1.UsageMissing},
		}
		decision := decisionv1.NormalizedEvent{
			ID: "history_decision_completed", Kind: decisionv1.EventDecisionCompleted, TenantID: scope.TenantID,
			SessionID: scope.SessionID, Generation: adapter.input.Pin.Generation,
			RouteID: routeID, RouteEntryID: entry, Channel: decisionv1.ChannelVoice,
			ReceivedAt: base.Add(4 * time.Second), Decision: result,
		}
		update := sessionUpdate(adapter.input, entry, decisions.Event{Normalized: &decision, InputID: "history_input_final"})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*adapter.candidateRef}
		return update, nil
	case "stale-owner":
		staleTenant := "tenant_previous_owner"
		staleGeneration := adapter.input.Pin.Generation - 1
		staleEvent := decisionv1.NormalizedEvent{
			ID: "history_stale_owner_event", Kind: decisionv1.EventSpeechStarted, TenantID: staleTenant,
			SessionID: scope.SessionID, Generation: staleGeneration,
			RouteID: routeID, RouteEntryID: entry, InputWindowID: snapshot.View.InputWindowID,
			Channel: decisionv1.ChannelVoice, ReceivedAt: base.Add(5 * time.Second),
		}
		update := sessionUpdate(adapter.input, entry, decisions.Event{Normalized: &staleEvent})
		update.Identity.TenantID = staleTenant
		update.Identity.Generation = staleGeneration
		return update, nil
	default:
		return DecisionSessionUpdate{}, fmt.Errorf("unknown captured-history test step %q", request.Step)
	}
}

func (adapter *decisionSessionCapturedHistoryAdapter) setSnapshot(snapshot DecisionSessionSnapshot) {
	adapter.snapshotMu.Lock()
	adapter.snapshot = snapshot
	adapter.snapshotMu.Unlock()
}

func (adapter *decisionSessionCapturedHistoryAdapter) currentSnapshot() DecisionSessionSnapshot {
	adapter.snapshotMu.Lock()
	defer adapter.snapshotMu.Unlock()
	return adapter.snapshot
}

func (adapter *decisionSessionCapturedHistoryAdapter) observeRaw(value string) {
	var seen uint32
	if strings.Contains(value, decisionHistoryAudioSentinel) {
		seen |= decisionHistorySawAudio
	}
	if strings.Contains(value, decisionHistoryTranscriptSentinel) {
		seen |= decisionHistorySawTranscript
	}
	if strings.Contains(value, decisionHistoryDirectorySentinel) {
		seen |= decisionHistorySawDirectory
	}
	if strings.Contains(value, decisionHistoryPromptSentinel) {
		seen |= decisionHistorySawPrompt
	}
	adapter.probe.rawSentinels.Or(seen)
}

func decisionSessionHistoryResponse(step string) json.RawMessage {
	var content string
	switch step {
	case "input":
		content = decisionHistoryTranscriptSentinel
	case "snapshot":
		content = decisionHistoryDirectorySentinel
	case "decision":
		content = decisionHistoryPromptSentinel
	}
	payload, _ := json.Marshal(struct {
		Step    string `json:"step"`
		Content string `json:"content,omitempty"`
	}{Step: step, Content: content})
	return payload
}
