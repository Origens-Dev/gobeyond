package temporalruntime

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestVoiceSessionDecisionApprovalIsBoundToExactEffectAndReplayed(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		approved bool
		expired  bool
	}{
		{name: "approved", approved: true},
		{name: "denied", approved: false},
		{name: "expired", approved: true, expired: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			approved := scenario.approved
			name := scenario.name
			fixture := newSyntheticDecisionWriteFixture(t, "ses_voice_approval_"+name, 121, true)
			identity := fixture.effect.Request.Identity
			request, err := validateDecisionEffectAuthorization(fixture.input.Definition,
				decisionSessionIdentity(fixture.input), fixture.effect, fixture.authority.authorization, fixture.call)
			if err != nil {
				t.Fatalf("prepare exact voice decision request: %v", err)
			}
			voiceContext := voicecontract.Context{
				ExecutionID: "execution_voice_approval", OrganizationID: "org_approval", ProjectID: "project_approval",
				EnvironmentID: "environment_approval", NetworkID: request.NetworkID, CallID: request.CallID,
				SessionID: request.SessionID, ActorID: request.ActorID, ActorKind: request.ActorKind,
				AgentID: request.AgentID, AgentRevision: request.AgentRevision, ManifestDigest: request.ManifestDigest,
				Generation: identity.Generation, Scope: voicecontract.Scope{Kind: "agent", LineID: "line_approval"},
			}
			input := VoiceSessionInput{
				BudgetPolicy: voicecontract.BudgetPolicyGenericV1, Context: &voiceContext,
				AgentID: request.AgentID, CallID: request.CallID, SessionID: request.SessionID,
				ExecutionID: voiceContext.ExecutionID,
			}
			if err := voiceContext.Validate(); err != nil {
				t.Fatalf("synthetic voice context: %v", err)
			}

			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
			var interactionID string
			var staleErr, approvalErr, replayErr, conflictErr error
			var staleCompleted, conflictCompleted bool
			var approvalResult, replayResult VoiceSessionExecuteToolResult
			env.RegisterDelayedCallback(func() {
				env.UpdateWorkflow(VoiceSessionExecuteToolUpdate, "execute-decision-effect", &testsuite.TestUpdateCallback{
					OnReject: func(err error) { t.Errorf("initial decision tool update rejected: %v", err) },
					OnComplete: func(value interface{}, err error) {
						if err != nil {
							t.Errorf("initial decision tool update: %v", err)
							return
						}
						result, ok := value.(VoiceSessionExecuteToolResult)
						if !ok || result.Approval == nil {
							t.Errorf("initial decision effect did not produce approval: %#v", value)
							return
						}
						interactionID = result.Approval.InteractionID
					},
				}, request)
			}, time.Second)
			env.RegisterDelayedCallback(func() {
				encoded, err := env.QueryWorkflow(VoiceSessionPendingDecisionEffectApprovalQuery, identity)
				if err != nil {
					t.Errorf("query exact pending decision approval: %v", err)
					return
				}
				var snapshot VoiceSessionDecisionApprovalSnapshot
				if err := encoded.Get(&snapshot); err != nil {
					t.Errorf("decode exact pending decision approval: %v", err)
					return
				}
				if !snapshot.Found || snapshot.InteractionID != interactionID || snapshot.ToolCallID != identity.ID ||
					snapshot.InputHash != voiceToolInputHash(request.Input) || snapshot.DecisionEffectIdentity == nil ||
					*snapshot.DecisionEffectIdentity != identity {
					t.Errorf("pending approval query was not bound to the effect: %#v", snapshot)
				}
				encodedSnapshot, _ := json.Marshal(snapshot)
				if string(encodedSnapshot) == "" || json.Valid(encodedSnapshot) == false {
					t.Errorf("approval metadata did not serialize: %s", encodedSnapshot)
				}
				if bytes.Contains(encodedSnapshot, request.Input) {
					t.Error("approval query exposed raw tool input")
				}
			}, 2*time.Second)
			response := VoiceSessionApprovalResponse{
				InteractionID: "approval_placeholder", Approved: approved, ActorID: request.ActorID, ActorKind: request.ActorKind,
				ToolCallID: request.ToolCallID, InputHash: voiceToolInputHash(request.Input), DecisionEffectIdentity: &identity,
			}
			env.RegisterDelayedCallback(func() {
				response.InteractionID = interactionID
				stale := response
				staleIdentity := identity
				staleIdentity.Generation++
				staleIdentity.ID = staleIdentity.CanonicalID()
				stale.DecisionEffectIdentity = &staleIdentity
				env.UpdateWorkflow(VoiceSessionApproveToolUpdate, "stale-generation", &testsuite.TestUpdateCallback{
					OnReject: func(err error) { staleErr = err },
					OnComplete: func(_ interface{}, err error) {
						if err != nil {
							staleErr = err
						} else {
							staleCompleted = true
						}
					},
				}, stale)
			}, 3*time.Second)
			approvalDelay := 4 * time.Second
			if scenario.expired {
				approvalDelay = 6 * time.Minute
			}
			env.RegisterDelayedCallback(func() {
				env.UpdateWorkflow(VoiceSessionApproveToolUpdate, "approve-exact-effect", &testsuite.TestUpdateCallback{
					OnReject: func(err error) { approvalErr = err },
					OnComplete: func(value interface{}, err error) {
						approvalErr = err
						if err == nil {
							approvalResult, _ = value.(VoiceSessionExecuteToolResult)
						}
					},
				}, response)
			}, approvalDelay)
			env.RegisterDelayedCallback(func() {
				env.UpdateWorkflow(VoiceSessionApproveToolUpdate, "replay-exact-effect", &testsuite.TestUpdateCallback{
					OnReject: func(err error) { replayErr = err },
					OnComplete: func(value interface{}, err error) {
						replayErr = err
						if err == nil {
							replayResult, _ = value.(VoiceSessionExecuteToolResult)
						}
					},
				}, response)
			}, approvalDelay+time.Second)
			env.RegisterDelayedCallback(func() {
				conflicting := response
				conflicting.Approved = !response.Approved
				env.UpdateWorkflow(VoiceSessionApproveToolUpdate, "conflicting-effect-response", &testsuite.TestUpdateCallback{
					OnReject: func(err error) { conflictErr = err },
					OnComplete: func(_ interface{}, err error) {
						if err != nil {
							conflictErr = err
						} else {
							conflictCompleted = true
						}
					},
				}, conflicting)
			}, approvalDelay+2*time.Second)

			env.ExecuteWorkflow(VoiceSessionWorkflow, input)
			if err := env.GetWorkflowError(); err != nil {
				t.Fatalf("voice workflow: %v", err)
			}
			if staleErr == nil || staleCompleted {
				t.Fatalf("stale generation response completed=%v err=%v", staleCompleted, staleErr)
			}
			if approvalErr != nil || replayErr != nil || conflictErr == nil || conflictCompleted {
				t.Fatalf("approval/replay/conflict errors = %v / %v / %v (completed %v)", approvalErr, replayErr, conflictErr, conflictCompleted)
			}
			if string(approvalResult.Result) != string(replayResult.Result) || approvalResult.Error != replayResult.Error {
				t.Fatalf("approval replay changed the durable result: first=%#v replay=%#v", approvalResult, replayResult)
			}
			wantCalls := 0
			if scenario.expired {
				wantCalls = 0
				if approvalResult.Error != "tool approval expired" || len(approvalResult.Result) != 0 {
					t.Fatalf("expired decision write result = %#v", approvalResult)
				}
			} else if approved {
				wantCalls = 1
				if approvalResult.Error != "" || string(approvalResult.Result) != `{"ok":true}` {
					t.Fatalf("approved decision write result = %#v", approvalResult)
				}
			} else if approvalResult.Error != "tool approval denied" || len(approvalResult.Result) != 0 {
				t.Fatalf("denied decision write result = %#v", approvalResult)
			}
			if *fixture.providerCalls != wantCalls {
				t.Fatalf("provider calls = %d, want %d", *fixture.providerCalls, wantCalls)
			}
		})
	}
}

func TestParseDecisionApprovalPayloadLeavesOrdinaryInteractionResponseAlone(t *testing.T) {
	if payload, ok, err := parseDecisionApprovalPayload(json.RawMessage(`{"interactionId":"ordinary-message"}`)); err != nil || ok || payload != (decisionApprovalPayload{}) {
		t.Fatalf("ordinary interaction response parsed as approval: %#v, ok=%v, err=%v", payload, ok, err)
	}
	if _, ok, err := parseDecisionApprovalPayload(json.RawMessage(`{"interactionId":"approval-only"}`)); err != nil || ok {
		t.Fatalf("interaction ID alone should remain an ordinary response, ok=%v err=%v", ok, err)
	}
	if _, ok, err := parseDecisionApprovalPayload(json.RawMessage(`{"approved":true}`)); err == nil || !ok {
		t.Fatalf("partial approval payload should fail closed, ok=%v err=%v", ok, err)
	}
}
