package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestMailboxWorkflowBudgetVersionPreservesLegacyAndRejectsRequestSelector(t *testing.T) {
	raw, _ := os.ReadFile("../voicecontract/testdata/command.json")
	var command voicecontract.Command
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	command.Version = voicecontract.Version
	command.Context.Scope.Kind = "agent"
	command.Context.TransportCallID = "transport"
	command.Context.ParentCallID = "parent"
	command.Context.HopID = "hop"
	command.Context.HopCount = 1
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			version := 1
			if old {
				version = int(workflow.DefaultVersion)
			}
			env.OnGetVersion(mailboxBudgetVersionChange, workflow.DefaultVersion, 1).Return(workflow.Version(version))
			id, _ := WorkflowID(command.Context.SessionID, command.Context.ExecutionID)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
			calls := 0
			env.RegisterActivityWithOptions(func(_ context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
				calls++
				return VoiceSessionExecuteToolResult{Result: []byte(`{}`)}, nil
			}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				control := newVoiceControlWorkflowState()
				reads := newVoiceReadWorkflowState()
				reads.budget = control.budget
				in := VoiceSessionInput{BudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxV1, Context: &command.Context, AgentID: command.Context.AgentID, CallID: command.Context.CallID, SessionID: command.Context.SessionID, ExecutionID: command.Context.ExecutionID}
				if err := configureVoiceToolBudget(ctx, in, control.budget); err != nil {
					return err
				}
				for i := 0; i < 12; i++ {
					read := voicecontract.ReadRequest{Version: voicecontract.Version, Context: command.Context, ToolID: "get-text-message", ToolCallID: fmt.Sprintf("get_%d", i), Arguments: []byte(`{}`), InputDigest: voicecontract.Digest([]byte(`{}`))}
					req := VoiceSessionExecuteToolInput{Grant: "opaque", RemoteRead: &read}
					result, err := reads.execute(ctx, in, req)
					if old && i >= 2 {
						if err == nil && result.Error == "" {
							return errors.New("legacy budget expanded")
						}
						continue
					}
					if err != nil || result.Error != "" {
						return fmt.Errorf("read %d rejected: %v/%s", i, err, result.Error)
					}
					// Replay is free and changing its input is rejected.
					if _, err = reads.execute(ctx, in, req); err != nil {
						return err
					}
					changed := read
					changed.Arguments = []byte(`{"different":"input"}`)
					changed.InputDigest = voicecontract.Digest(changed.Arguments)
					req.RemoteRead = &changed
					if _, err = reads.execute(ctx, in, req); err == nil {
						return errors.New("changed replay admitted")
					}
				}
				if old {
					return nil
				}
				// Exhausting all message reads still leaves two placement attempts and hangup.
				for i := 0; i < 3; i++ {
					c := command
					c.ToolCallID = fmt.Sprintf("dial_%d", i)
					c.OperationID = fmt.Sprintf("operation_%d", i)
					c.AnnouncementBarrierID = uint64(i + 1)
					_, err := control.execute(ctx, in, VoiceSessionExecuteToolInput{Grant: "opaque", CallControl: &c})
					if i < 2 && err != nil {
						return err
					}
					if i < 2 {
						if _, err := control.execute(ctx, in, VoiceSessionExecuteToolInput{Grant: "opaque", CallControl: &c}); err != nil {
							return errors.New("control replay charged")
						}
						changed := c
						changed.OperationID = "altered_operation"
						if _, err := control.execute(ctx, in, VoiceSessionExecuteToolInput{Grant: "opaque", CallControl: &changed}); err == nil {
							return errors.New("altered control identity admitted")
						}
					}

					if i == 2 && err == nil {
						return errors.New("third placement admitted")
					}
				}
				c := command
				c.ToolID = voicecontract.ToolIDHangUp
				c.ToolCallID = "hangup"
				c.OperationID = "hangup_op"
				c.AnnouncementBarrierID = 4
				if _, err := control.execute(ctx, in, VoiceSessionExecuteToolInput{Grant: "opaque", CallControl: &c}); err != nil {
					return err
				}
				read := voicecontract.ReadRequest{Version: voicecontract.Version, Context: command.Context, ToolID: "list-text-messages", ToolCallID: "get_0", Arguments: []byte(`{}`), InputDigest: voicecontract.Digest([]byte(`{}`))}
				if result, err := reads.execute(ctx, in, VoiceSessionExecuteToolInput{RemoteRead: &read}); err == nil && result.Error == "" {
					return errors.New("cross-tool replay admitted")
				}
				return nil
			})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			want := 15
			if old {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls=%d want%d", calls, want)
			}
		})
	}
}
func TestMailboxBudgetRejectsRequestOnlyPolicyAndReservedMutation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		reads := newVoiceReadWorkflowState()
		_, err := reads.execute(ctx, VoiceSessionInput{}, VoiceSessionExecuteToolInput{BudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxV1})
		if err == nil {
			return errors.New("request-only selector admitted")
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}

	b := &voiceToolBudget{}
	for i := 0; i < 2; i++ {
		if err := b.consume("list-text-messages", fmt.Sprint(i), "digest", 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.consume("get-text-message", "third", "digest", 2); err == nil {
		t.Fatal("legacy budget expanded")
	}
	b = &voiceToolBudget{policy: voicecontract.BudgetPolicyOperatorMailboxV1}
	for i := 0; i < 4; i++ {
		if err := b.consume("list-text-messages", fmt.Sprint(i), "digest", 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.consume("list-text-messages", "fifth", "digest", 2); err == nil {
		t.Fatal("list budget expanded")
	}
	if err := b.consume("mark-text-message-read", "mark", "digest", 2); err == nil {
		t.Fatal("reserved mutation admitted")
	}
}
