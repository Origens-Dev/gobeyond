package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestVoiceWriteWorkflowLostResponseReconcilesOneMutation(t *testing.T) {
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), newMemoryVoiceWriteStore())
	schema, output := voiceWriteClosedSchemas()
	calls := 0
	writeTool := agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": writeTool,
	}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg := NewVoiceRegistry()
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() {
		RetainVoiceRegistry(nil)
		resetVoiceWriteLedger()
	})

	ctxn := voicecontract.Context{
		ExecutionID: "execution-1", OrganizationID: "org-1", ProjectID: "project-1", EnvironmentID: "env-1", NetworkID: "network-1",
		CallID: "call-write", SessionID: "session-write", ActorID: "user-1", ActorKind: "user", AgentID: "support", AgentRevision: "revision-1",
		ManifestDigest: manifestDigest, Generation: 1, Scope: voicecontract.Scope{Kind: "agent", LineID: "line-1"},
	}
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	base := VoiceSessionExecuteToolInput{
		AgentID: ctxn.AgentID, ToolName: "lookup", ToolCallID: "call-1", Input: input,
		ActorID: ctxn.ActorID, ActorKind: ctxn.ActorKind, NetworkID: ctxn.NetworkID,
		AllowedToolIDs: []string{"lookup"},
	}
	in := VoiceSessionInput{Context: &ctxn, AgentID: ctxn.AgentID, CallID: ctxn.CallID, SessionID: ctxn.SessionID, ExecutionID: ctxn.ExecutionID}

	drop := true
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		out, activityErr := VoiceSessionExecuteToolActivity(ctx, req)
		if drop {
			drop = false
			if activityErr != nil {
				return out, activityErr
			}
			// Replacement host/container: empty process cache and a new local
			// directory. Reconcile must find the result in the shared store.
			replaceHostVoiceWriteLedger(t.TempDir())
			return VoiceSessionExecuteToolResult{}, errors.New("lost response")
		}
		return out, activityErr
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		writes := newVoiceWriteWorkflowState()
		req, identity, replay, bindErr := bindVoiceWriteRequest(in, base)
		if bindErr != nil {
			return bindErr
		}
		if _, done, reserveErr := writes.reserve(ctx, identity, replay); reserveErr != nil || done {
			return errors.New("first reserve")
		}
		result, execErr := executeVoiceWriteToolLocal(ctx, req)
		if _, finishErr := writes.finish(identity, result, execErr); !errors.Is(finishErr, errWriteOutcomeUnknown) {
			return errors.New("lost response was not marked unknown")
		}
		if _, done, reserveErr := writes.reserve(ctx, identity, replay); reserveErr != nil || done {
			return errors.New("unknown reserve must lookup")
		}
		req.WriteReconcileOnly = true
		result, execErr = executeVoiceWriteToolLocal(ctx, req)
		result, execErr = writes.finish(identity, result, execErr)
		if execErr != nil || result.Error != "" || len(result.Result) == 0 {
			return errors.New("reconcile failed")
		}
		cached, done, reserveErr := writes.reserve(ctx, identity, replay)
		if reserveErr != nil || !done || string(cached.Result) != string(result.Result) {
			return errors.New("cached write missing")
		}
		changed := base
		changed.Input, _ = json.Marshal(map[string]any{"q": "other"})
		_, _, changedReplay, bindErr := bindVoiceWriteRequest(in, changed)
		if bindErr != nil {
			return bindErr
		}
		if _, _, reserveErr = writes.reserve(ctx, identity, changedReplay); !errors.Is(reserveErr, errWriteConflict) {
			return errors.New("changed input reused tool_call_id")
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("mutations=%d want 1", calls)
	}
	if drop {
		t.Fatal("lost-response wrapper never ran")
	}
	ledgerID := voiceWriteLedgerIdentity(in.SessionID, base.ToolName, base.ToolCallID)
	complete, unknown, metered, result := voiceWriteLedgerState(ledgerID)
	if !complete || unknown || !metered || len(result.Result) == 0 {
		t.Fatalf("durable ledger complete=%v unknown=%v metered=%v result=%s", complete, unknown, metered, result.Result)
	}
}

func TestVoiceWriteEmptyCacheRetryAfterCommitIsOneMutation(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("commit first=%#v err=%v calls=%d", first, err, calls)
	}
	reopenVoiceWriteLedger()
	second, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || second.Error != "" || calls != 1 || string(second.Result) != string(first.Result) {
		t.Fatalf("empty-cache retry first=%s second=%s err=%v calls=%d", first.Result, second.Result, err, calls)
	}
	complete, unknown, metered, result := voiceWriteLedgerState(voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID))
	if !complete || unknown || !metered || string(result.Result) != string(first.Result) {
		t.Fatalf("metering not retained complete=%v unknown=%v metered=%v result=%s", complete, unknown, metered, result.Result)
	}
}

func TestVoiceWriteEmptyCacheUnknownDoesNotExecute(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	canonical, err := voicecontract.CanonicalJSON(req.Input, voicecontract.MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	req.Input = canonical
	digest := voicecontract.Digest(canonical)
	key, err := deriveVoiceWriteKey(req.SessionID, req.CallID, req.ToolName, req.ToolCallID, digest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := voiceWriteReplayDigest(req, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedVoiceWriteUnknown(voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID), key, replay); err != nil {
		t.Fatal(err)
	}
	reopenVoiceWriteLedger()
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("unknown after empty cache err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("unknown empty-cache retry executed handler calls=%d", calls)
	}

	resetVoiceWriteLedgerWithDir(t.TempDir())
	req.WriteReconcileOnly = true
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("reconcile-only miss err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("reconcile-only miss executed handler calls=%d", calls)
	}
}

func TestVoiceWriteHostLossReconcilesFromAuthoritativeStore(t *testing.T) {
	authority := newMemoryVoiceWriteStore()
	hostA := t.TempDir()
	resetVoiceWriteLedgerWithAuthority(hostA, authority)
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("commit first=%#v err=%v calls=%d", first, err, calls)
	}
	ledgerID := voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID)
	// Authoritative mutation/result lives in the shared store — not only on host A.
	auth, ok, loadErr := authority.Load(ledgerID)
	if loadErr != nil || !ok || !auth.Complete || !auth.Metered || string(auth.Result.Result) != string(first.Result) {
		t.Fatalf("authority before host loss: ok=%v complete=%v metered=%v result=%s err=%v", ok, auth.Complete, auth.Metered, auth.Result.Result, loadErr)
	}
	hostB := t.TempDir()
	if hostA == hostB {
		t.Fatal("replacement host must use a distinct empty ledger directory")
	}
	// Replacement host/container: fresh local storage, empty process cache.
	replaceHostVoiceWriteLedger(hostB)
	if entries, err := os.ReadDir(hostB); err != nil || len(entries) != 0 {
		t.Fatalf("host B local ledger not empty: entries=%v err=%v", entries, err)
	}
	second, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || second.Error != "" || calls != 1 || string(second.Result) != string(first.Result) {
		t.Fatalf("host-loss retry first=%s second=%s err=%v calls=%d", first.Result, second.Result, err, calls)
	}
	if voiceWriteAuthorityStore() != authority {
		t.Fatal("replacement host lost the authoritative store handle")
	}
	auth, ok, loadErr = authority.Load(ledgerID)
	if loadErr != nil || !ok || !auth.Complete || !auth.Metered || string(auth.Result.Result) != string(first.Result) {
		t.Fatalf("authority after host loss: ok=%v complete=%v metered=%v result=%s err=%v", ok, auth.Complete, auth.Metered, auth.Result.Result, loadErr)
	}
}

func TestVoiceWriteRetainAuthorityFreshHostRecoversViaReceiptLookup(t *testing.T) {
	// Production path: default host-local ledger, then RetainVoiceWriteAuthority
	// with a product receipt-style SoR (keyed by ToolWriteID), not the test-only
	// resetVoiceWriteLedgerWithAuthority helper.
	hostA := t.TempDir()
	t.Setenv(voiceWriteLedgerDirEnv, hostA)
	resetVoiceWriteLedgerWithDir(hostA)
	if ProcessVoiceWriteAuthority() != nil {
		t.Fatal("production default must not invent a shared authority")
	}
	receipts := newReceiptLookupAuthority()
	RetainVoiceWriteAuthority(receipts)
	if ProcessVoiceWriteAuthority() != receipts {
		t.Fatal("RetainVoiceWriteAuthority did not attach production authority")
	}
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("commit first=%#v err=%v calls=%d", first, err, calls)
	}
	ledgerID := voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID)
	auth, ok, loadErr := receipts.Load(ledgerID)
	if loadErr != nil || !ok || !auth.Complete || auth.Key == "" || string(auth.Result.Result) != string(first.Result) {
		t.Fatalf("receipt authority before host loss: ok=%v complete=%v key=%q result=%s err=%v", ok, auth.Complete, auth.Key, auth.Result.Result, loadErr)
	}
	if _, byKey := receipts.loadByWriteID(auth.Key); !byKey {
		t.Fatal("receipt authority did not index by ToolWriteID")
	}
	hostB := t.TempDir()
	replaceHostVoiceWriteLedger(hostB)
	if entries, err := os.ReadDir(hostB); err != nil || len(entries) != 0 {
		t.Fatalf("fresh host local ledger not empty: entries=%v err=%v", entries, err)
	}
	if ProcessVoiceWriteAuthority() != receipts {
		t.Fatal("fresh host lost retained authority")
	}
	second, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || second.Error != "" || calls != 1 || string(second.Result) != string(first.Result) {
		t.Fatalf("fresh-host receipt reconcile first=%s second=%s err=%v calls=%d", first.Result, second.Result, err, calls)
	}
}

func TestVoiceWritePendingRefreshesWhenPeerCompletes(t *testing.T) {
	authority := newMemoryVoiceWriteStore()
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), authority)
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	canonical, err := voicecontract.CanonicalJSON(req.Input, voicecontract.MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	req.Input = canonical
	digest := voicecontract.Digest(canonical)
	key, err := deriveVoiceWriteKey(req.SessionID, req.CallID, req.ToolName, req.ToolCallID, digest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := voiceWriteReplayDigest(req, digest)
	if err != nil {
		t.Fatal(err)
	}
	ledgerID := voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID)
	pending := VoiceWritePersistedRecord{Identity: ledgerID, Key: key, Digest: replay}
	if err := authority.Reserve(ledgerID, pending); err != nil {
		t.Fatal(err)
	}
	// Worker B observes pending from authority and caches it (fail closed).
	req.WriteReconcileOnly = true
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("pending reconcile err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("pending reconcile executed handler calls=%d", calls)
	}
	// Peer worker finishes; authority advances to complete+metered.
	complete := VoiceWritePersistedRecord{
		Identity: ledgerID, Key: key, Digest: replay, Complete: true, Metered: true,
		Result: VoiceSessionExecuteToolResult{Result: []byte(`{"ok":true}`)},
	}
	if err := authority.Persist(ledgerID, complete); err != nil {
		t.Fatal(err)
	}
	// Same process must refresh pending/unknown from authority, not sticky-cache.
	second, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || second.Error != "" || calls != 0 || string(second.Result) != `{"ok":true}` {
		t.Fatalf("peer-complete refresh second=%s err=%v calls=%d", second.Result, err, calls)
	}
}

func TestVoiceWriteHostLossWithoutAuthorityFailsClosed(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("commit first=%#v err=%v calls=%d", first, err, calls)
	}
	replaceHostVoiceWriteLedger(t.TempDir())
	// Workflow unknown path: reconcile-only with no recoverable authority.
	req.WriteReconcileOnly = true
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("host-loss without authority err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("host-loss without authority re-executed calls=%d", calls)
	}
}

func TestVoiceWritePersistErrorFailsClosedWithoutReexecute(t *testing.T) {
	inner := newMemoryVoiceWriteStore()
	failing := persistFailStore{inner: inner, failComplete: true}
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), failing)
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	_, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if !errors.Is(err, errWriteOutcomeUnknown) || !errors.Is(err, errWriteLedgerPersist) {
		t.Fatalf("persist failure err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("persist failure mutations=%d", calls)
	}
	ledgerID := voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID)
	auth, ok, loadErr := inner.Load(ledgerID)
	if loadErr != nil || !ok || auth.Complete || !auth.Unknown || !auth.Metered {
		t.Fatalf("authority after persist fail: ok=%v complete=%v unknown=%v metered=%v err=%v", ok, auth.Complete, auth.Unknown, auth.Metered, loadErr)
	}
	// Replacement host with empty local storage still sees the fail-closed mark.
	replaceHostVoiceWriteLedger(t.TempDir())
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("persist-failure retry err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("persist-failure retry re-executed calls=%d", calls)
	}
}

func TestVoiceWritePersistAlwaysFailLeavesReservationFailClosed(t *testing.T) {
	inner := newMemoryVoiceWriteStore()
	failing := persistFailStore{inner: inner, err: errors.New("disk full")}
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), failing)
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	_, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if !errors.Is(err, errWriteOutcomeUnknown) || !errors.Is(err, errWriteLedgerPersist) {
		t.Fatalf("persist failure err=%v", err)
	}
	ledgerID := voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID)
	auth, ok, loadErr := inner.Load(ledgerID)
	// Exclusive reserve survived; complete mark did not. Pending is fail-closed.
	if loadErr != nil || !ok || auth.Complete || auth.Unknown {
		t.Fatalf("reserved pending after persist fail: ok=%v complete=%v unknown=%v err=%v", ok, auth.Complete, auth.Unknown, loadErr)
	}
	replaceHostVoiceWriteLedger(t.TempDir())
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("reserved-pending host-loss err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("reserved-pending host-loss re-executed calls=%d", calls)
	}
}

func TestVoiceWriteHostLocalPersistErrorFailsClosed(t *testing.T) {
	localDir := t.TempDir()
	failing := persistFailStore{inner: &fileVoiceWriteStore{dir: localDir}, failComplete: true}
	resetVoiceWriteLedgerStores(failing, nil)
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	_, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if !errors.Is(err, errWriteOutcomeUnknown) || !errors.Is(err, errWriteLedgerPersist) {
		t.Fatalf("host-local persist failure err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("host-local persist failure mutations=%d", calls)
	}
	reopen := openVoiceWriteLedger(localDir)
	rec, ok, loadErr := reopen.loadDurableLocked(voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID))
	if loadErr != nil || !ok || rec.complete || !rec.unknown || !rec.metered {
		t.Fatalf("host-local after persist fail: ok=%v complete=%v unknown=%v metered=%v err=%v", ok, rec.complete, rec.unknown, rec.metered, loadErr)
	}
	resetVoiceWriteLedgerWithDir(localDir)
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("host-local persist-fail retry err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("host-local persist-fail retry re-executed calls=%d", calls)
	}
}

func TestVoiceWriteWorkflowPersistErrorHostLossDoesNotReexecute(t *testing.T) {
	inner := newMemoryVoiceWriteStore()
	failing := persistFailStore{inner: inner, failComplete: true}
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), failing)
	schema, output := voiceWriteClosedSchemas()
	calls := 0
	writeTool := agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{"lookup": writeTool}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg := NewVoiceRegistry()
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() {
		RetainVoiceRegistry(nil)
		resetVoiceWriteLedger()
	})
	ctxn := voicecontract.Context{
		ExecutionID: "execution-1", OrganizationID: "org-1", ProjectID: "project-1", EnvironmentID: "env-1", NetworkID: "network-1",
		CallID: "call-write", SessionID: "session-write", ActorID: "user-1", ActorKind: "user", AgentID: "support", AgentRevision: "revision-1",
		ManifestDigest: manifestDigest, Generation: 1, Scope: voicecontract.Scope{Kind: "agent", LineID: "line-1"},
	}
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	base := VoiceSessionExecuteToolInput{
		AgentID: ctxn.AgentID, ToolName: "lookup", ToolCallID: "call-persist", Input: input,
		ActorID: ctxn.ActorID, ActorKind: ctxn.ActorKind, NetworkID: ctxn.NetworkID, AllowedToolIDs: []string{"lookup"},
	}
	in := VoiceSessionInput{Context: &ctxn, AgentID: ctxn.AgentID, CallID: ctxn.CallID, SessionID: ctxn.SessionID, ExecutionID: ctxn.ExecutionID}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		writes := newVoiceWriteWorkflowState()
		req, identity, replay, bindErr := bindVoiceWriteRequest(in, base)
		if bindErr != nil {
			return bindErr
		}
		if _, done, reserveErr := writes.reserve(ctx, identity, replay); reserveErr != nil || done {
			return errors.New("first reserve")
		}
		result, execErr := executeVoiceWriteToolLocal(ctx, req)
		if _, finishErr := writes.finish(identity, result, execErr); !errors.Is(finishErr, errWriteOutcomeUnknown) {
			return fmt.Errorf("persist failure finish: %v / %v", execErr, finishErr)
		}
		replaceHostVoiceWriteLedger(t.TempDir())
		if _, done, reserveErr := writes.reserve(ctx, identity, replay); reserveErr != nil || done {
			return errors.New("unknown reserve must lookup")
		}
		req.WriteReconcileOnly = true
		result, execErr = executeVoiceWriteToolLocal(ctx, req)
		if _, finishErr := writes.finish(identity, result, execErr); !errors.Is(finishErr, errWriteOutcomeUnknown) {
			return fmt.Errorf("host-loss reconcile: %v / %v", execErr, finishErr)
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("mutations=%d want 1", calls)
	}
	auth, ok, loadErr := inner.Load(voiceWriteLedgerIdentity(in.SessionID, base.ToolName, base.ToolCallID))
	if loadErr != nil || !ok || auth.Complete || !auth.Unknown {
		t.Fatalf("authority after workflow persist fail: ok=%v complete=%v unknown=%v err=%v", ok, auth.Complete, auth.Unknown, loadErr)
	}
}

type persistFailStore struct {
	inner        VoiceWriteStore
	err          error
	failComplete bool
}

func (s persistFailStore) Load(identity string) (VoiceWritePersistedRecord, bool, error) {
	if s.inner == nil {
		return VoiceWritePersistedRecord{}, false, nil
	}
	return s.inner.Load(identity)
}

func (s persistFailStore) Reserve(identity string, rec VoiceWritePersistedRecord) error {
	if s.inner == nil {
		return nil
	}
	return s.inner.Reserve(identity, rec)
}

func (s persistFailStore) Persist(identity string, rec VoiceWritePersistedRecord) error {
	if s.failComplete && !rec.Complete {
		if s.inner == nil {
			return nil
		}
		return s.inner.Persist(identity, rec)
	}
	if s.err != nil {
		return s.err
	}
	return errWriteLedgerPersist
}

func retainVoiceWriteLookup(t *testing.T, handler func(context.Context, agents.Actor, map[string]any) (any, error)) VoiceSessionExecuteToolInput {
	t.Helper()
	schema, output := voiceWriteClosedSchemas()
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, handler),
	}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg := NewVoiceRegistry()
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() {
		RetainVoiceRegistry(nil)
		resetVoiceWriteLedger()
	})
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	return VoiceSessionExecuteToolInput{
		AgentID: "support", ToolName: "lookup", ToolCallID: "call-1", Input: input,
		ActorID: "user-1", ActorKind: "user", AllowedToolIDs: []string{"lookup"},
		ManifestDigest: manifestDigest, AgentRevision: "revision-1",
		SessionID: "session-write", CallID: "call-write",
	}
}

// receiptLookupAuthority simulates product durable receipts keyed by
// ToolWriteID (VoiceWritePersistedRecord.Key), the Candlestick-style SoR that
// production attaches via RetainVoiceWriteAuthority.
type receiptLookupAuthority struct {
	mu        sync.Mutex
	byID      map[string]VoiceWritePersistedRecord
	byWriteID map[string]VoiceWritePersistedRecord
}

func newReceiptLookupAuthority() *receiptLookupAuthority {
	return &receiptLookupAuthority{
		byID:      map[string]VoiceWritePersistedRecord{},
		byWriteID: map[string]VoiceWritePersistedRecord{},
	}
}

func (s *receiptLookupAuthority) loadByWriteID(writeID string) (VoiceWritePersistedRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byWriteID[writeID]
	return rec, ok
}

func (s *receiptLookupAuthority) Load(identity string) (VoiceWritePersistedRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.byID[identity]; ok {
		return rec, true, nil
	}
	return VoiceWritePersistedRecord{}, false, nil
}

func (s *receiptLookupAuthority) Reserve(identity string, rec VoiceWritePersistedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[identity]; ok {
		return os.ErrExist
	}
	if rec.Key != "" {
		if _, ok := s.byWriteID[rec.Key]; ok {
			return os.ErrExist
		}
		s.byWriteID[rec.Key] = rec
	}
	s.byID[identity] = rec
	return nil
}

func (s *receiptLookupAuthority) Persist(identity string, rec VoiceWritePersistedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[identity] = rec
	if rec.Key != "" {
		s.byWriteID[rec.Key] = rec
	}
	return nil
}
