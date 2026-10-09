package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"go.temporal.io/sdk/client"
)

const decisionCancelHTTPPrefix = "/api/agents"

type cancelWaitWorkflowRun struct {
	started chan struct{}
	once    sync.Once
}

func (*cancelWaitWorkflowRun) GetID() string    { return "decision-workflow" }
func (*cancelWaitWorkflowRun) GetRunID() string { return "temporal-run" }

func (run *cancelWaitWorkflowRun) Get(ctx context.Context, _ interface{}) error {
	run.once.Do(func() { close(run.started) })
	<-ctx.Done()
	return ctx.Err()
}

func (run *cancelWaitWorkflowRun) GetWithOptions(ctx context.Context, value interface{}, _ client.WorkflowRunGetOptions) error {
	return run.Get(ctx, value)
}

func TestDecisionCancelHTTPKeepsAcceptedCancellationAndRetriesOwnerCAS(t *testing.T) {
	const sessionID = "ses_http_cancel_retry"
	const runID = "run_http_cancel_retry"
	input, dispatch, _ := decisionEffectTestFixture(t, sessionID, 130)
	input.RunID = runID
	identity := decisionSessionIdentity(input)
	receipt := decisionv1.EffectReceipt{
		Identity: dispatch.Request.Identity, Status: decisionv1.EffectConfirmed,
		ReceiptID: "receipt_http_cancel", ProviderRequestID: "provider_http_cancel",
		TargetOpaqueID: dispatch.Request.TargetOpaqueID, ObservedAt: time.Now().UTC(),
	}
	release := decisions.Effect{
		Kind: decisions.EffectReleaseOwnership, RouteID: dispatch.RouteID, RouteEntryID: dispatch.RouteEntryID,
		InputID: dispatch.InputID, ActionID: dispatch.ActionID, Request: cloneDecisionEffectRequest(dispatch.Request),
		ReceiptID: receipt.ReceiptID,
	}
	pending := DecisionSessionPendingEffect{
		Identity: identity, Effect: &release, Submitted: true, DispatchCommitted: true,
		Status: DecisionSessionCancelled,
		Snapshot: DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionCancelled,
			View: decisions.View{Ownership: decisionv1.GraphOwnership{State: decisionv1.OwnershipReleased, ReleaseReceiptID: receipt.ReceiptID}}},
	}

	authority := newDecisionEffectTestAdapter(input)
	authority.decisionSessionAdapterFake.input = input
	authority.decisionSessionAdapterFake.config.Public = true
	authority.cancellation = DecisionSessionCancel{Identity: identity}
	authority.reconcileErrors = []error{errors.New("operation authority temporarily unavailable")}
	authority.reconciliations = []DecisionEffectReconciliation{
		{},
		{State: DecisionEffectHasReceipt, Receipt: &receipt},
		{State: DecisionEffectHasReceipt, Receipt: &receipt},
	}
	authority.casErr = errors.New("owner CAS temporarily unavailable")

	run := &cancelWaitWorkflowRun{started: make(chan struct{})}
	fake := &fakeClient{run: run, queryOutput: pending}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	fake.updateFunc = func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case DecisionSessionCancelUpdate:
			return DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionCancelled}, nil
		case DecisionSessionOwnershipCASUpdate:
			ack, ok := options.Args[0].(DecisionSessionOwnershipCAS)
			if !ok || ack.Identity != identity || ack.EffectID != dispatch.Request.Identity.ID || ack.ReceiptID != receipt.ReceiptID {
				return nil, errors.New("owner CAS acknowledgement did not retain its exact receipt binding")
			}
			fake.mu.Lock()
			fake.queryOutput = DecisionSessionPendingEffect{Identity: identity, Status: DecisionSessionCancelled,
				Snapshot: DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionCancelled}}
			fake.mu.Unlock()
			return DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionCancelled}, nil
		default:
			return nil, errors.New("unexpected update during HTTP cancellation recovery: " + options.UpdateName)
		}
	}

	registry := httpruntime.NewRegistry()
	if err := registry.Register("decision", authority); err != nil {
		t.Fatal(err)
	}
	httpRuntime, err := httpruntime.New(httpruntime.Options{
		Registry: registry, Dispatcher: dispatcher,
		NewID: func(prefix string) (string, error) {
			if prefix == "ses" {
				return sessionID, nil
			}
			if prefix == "run" {
				return runID, nil
			}
			return "", errors.New("unexpected generated ID prefix")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpRuntime.Handler(decisionCancelHTTPPrefix)
	if err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodPost, decisionCancelHTTPPrefix+"/decision/sessions", strings.NewReader(`{"input":null}`))
	startRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(start, startRequest)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", start.Code, start.Body.String())
	}
	select {
	case <-run.started:
	case <-time.After(time.Second):
		t.Fatal("durable dispatcher did not enter the active workflow wait")
	}

	postCancel := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, decisionCancelHTTPPrefix+"/sessions/"+sessionID+"/cancel",
			strings.NewReader(`{"runId":"`+runID+`","reason":"caller ended"}`))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	assertResponse := func(response *httptest.ResponseRecorder, cleanupPending bool) {
		t.Helper()
		if response.Code != http.StatusAccepted {
			t.Fatalf("cancel status = %d, body = %s", response.Code, response.Body.String())
		}
		var body struct {
			Cancelled      bool       `json:"cancelled"`
			CleanupPending bool       `json:"cleanupPending"`
			Run            agents.Run `json:"run"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.Cancelled || body.CleanupPending != cleanupPending || body.Run.Status != httpruntime.RunStatusCancelled {
			t.Fatalf("cancel response = %#v, want accepted/cancelled with cleanupPending=%v", body, cleanupPending)
		}
	}

	// CancelUpdate commits before the authority fails. The HTTP runtime must
	// publish cancellation and preserve cleanup state instead of aborting it.
	assertResponse(postCancel(), true)
	// The next request replays the same durable cancel update, reconciles the
	// exact receipt, and attempts the idempotent existing owner CAS. Its failure
	// still must not roll back the already-cancelled local run.
	assertResponse(postCancel(), true)
	authority.casErr = nil
	assertResponse(postCancel(), false)

	updates := append([]client.UpdateWorkflowOptions(nil), fake.updateHistory...)
	var cancelUpdates, ownerCASUpdates, voiceUpdates int
	for _, update := range updates {
		switch update.UpdateName {
		case DecisionSessionCancelUpdate:
			cancelUpdates++
			if update.UpdateID != "cancel-"+runID {
				t.Fatalf("cancellation retry changed its idempotency key: %#v", update)
			}
		case DecisionSessionOwnershipCASUpdate:
			ownerCASUpdates++
		case VoiceSessionExecuteToolUpdate:
			voiceUpdates++
		}
	}
	if cancelUpdates != 3 || ownerCASUpdates != 1 || voiceUpdates != 0 {
		t.Fatalf("recovery update counts cancel=%d ownerCAS=%d voice=%d history=%#v", cancelUpdates, ownerCASUpdates, voiceUpdates, updates)
	}
	if authority.reconcileCalls != 3 || authority.casCalls != 2 || authority.verifyCalls != 2 {
		t.Fatalf("recovery counts reconcile=%d CAS=%d receipt-verification=%d", authority.reconcileCalls, authority.casCalls, authority.verifyCalls)
	}
	if len(authority.reconciledIDs) != 3 || authority.reconciledIDs[0] != dispatch.Request.Identity.ID ||
		authority.reconciledIDs[1] != dispatch.Request.Identity.ID || authority.reconciledIDs[2] != dispatch.Request.Identity.ID ||
		len(authority.casReceiptIDs) != 2 || authority.casReceiptIDs[0] != receipt.ReceiptID || authority.casReceiptIDs[1] != receipt.ReceiptID {
		t.Fatalf("retry lost or changed effect/receipt identity: effects=%#v receipts=%#v", authority.reconciledIDs, authority.casReceiptIDs)
	}
}
