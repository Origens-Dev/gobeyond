package httpruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
)

type approvalTestEvent struct {
	event   string
	payload map[string]any
}
type approvalTestEmitter struct{ events chan approvalTestEvent }

func (emitter *approvalTestEmitter) Emit(_ context.Context, event string, value any) error {
	data, _ := json.Marshal(value)
	var payload map[string]any
	_ = json.Unmarshal(data, &payload)
	emitter.events <- approvalTestEvent{event: event, payload: payload}
	return nil
}

func TestApprovalDecisionWaitsForExactActorToolAndInput(t *testing.T) {
	manager := newApprovalManager()
	emitter := &approvalTestEmitter{events: make(chan approvalTestEvent, 1)}
	call := StartCall{
		Session: agents.Session{ID: "session-1"}, Run: agents.Run{ID: "run-1"},
		Actor: agents.Actor{ID: "user-1", Kind: "user"},
	}
	toolCall := ai.ToolCall{ToolCallID: "call-1", ToolName: "rename_network", Input: map[string]any{"networkId": "net-1", "name": "New name"}}
	inputHash, err := approvalInputHash(toolCall.Input)
	if err != nil {
		t.Fatal(err)
	}
	decision := make(chan ai.ApprovalDecision, 1)
	go func() {
		got, requestErr := manager.request(context.Background(), call, toolCall, ai.Tool{Name: "rename_network", Title: "Rename network"}, emitter)
		if requestErr != nil {
			decision <- ai.Denied(requestErr.Error())
			return
		}
		decision <- got
	}()
	var event approvalTestEvent
	select {
	case event = <-emitter.events:
	case <-time.After(time.Second):
		t.Fatal("approval request event was not emitted")
	}
	if event.event != "agent.interaction.requested" {
		t.Fatalf("event = %q", event.event)
	}
	interactionID, _ := event.payload["interactionId"].(string)
	if interactionID == "" || event.payload["inputHash"] != inputHash {
		t.Fatalf("interaction payload = %#v", event.payload)
	}
	respond := func(actor agents.Actor, toolCallID, name, hash string, approved bool) error {
		return manager.respond(RespondCall{Session: call.Session, Run: call.Run, Actor: actor}, interactionID, toolCallID, name, hash, approved, "confirmed")
	}
	if err := respond(agents.Actor{ID: "other", Kind: "user"}, "call-1", "rename_network", inputHash, true); err == nil {
		t.Fatal("different actor response accepted")
	}
	if err := respond(call.Actor, "call-2", "rename_network", inputHash, true); err == nil {
		t.Fatal("different tool call response accepted")
	}
	if err := respond(call.Actor, "call-1", "rename_network", "wrong-input", true); err == nil {
		t.Fatal("different input response accepted")
	}
	select {
	case <-decision:
		t.Fatal("invalid responses released approval")
	default:
	}
	if err := respond(call.Actor, "call-1", "rename_network", inputHash, true); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-decision:
		if got.Type != ai.ApprovalDecisionApproved {
			t.Fatalf("decision = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("approval response did not release the tool")
	}
	if err := respond(call.Actor, "call-1", "rename_network", inputHash, true); err == nil {
		t.Fatal("replayed response accepted")
	}
}

func TestApprovalRequestCancellationDenies(t *testing.T) {
	manager := newApprovalManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitter := &approvalTestEmitter{events: make(chan approvalTestEvent, 1)}
	decision := make(chan ai.ApprovalDecision, 1)
	go func() {
		got, _ := manager.request(ctx, StartCall{Session: agents.Session{ID: "s"}, Run: agents.Run{ID: "r"}, Actor: agents.Actor{ID: "u", Kind: "user"}}, ai.ToolCall{ToolCallID: "c", ToolName: "write", Input: map[string]any{}}, ai.Tool{Name: "write"}, emitter)
		decision <- got
	}()
	select {
	case <-emitter.events:
	case <-time.After(time.Second):
		t.Fatal("approval request event was not emitted")
	}
	cancel()
	select {
	case got := <-decision:
		if got.Type != ai.ApprovalDecisionDenied {
			t.Fatalf("decision = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled approval did not finish")
	}
}
