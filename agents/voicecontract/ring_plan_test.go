package voicecontract

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestRingPlanGoldenEncodeDecode(t *testing.T) {
	raw, err := os.ReadFile("testdata/ring-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan RingPlan
	if err = Decode(raw, MaxEnvelopeBytes, &plan); err != nil {
		t.Fatal(err)
	}
	if err = plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if plan.Version != Version || plan.AttachedFallback == nil || plan.Events.OnNoAnswer == nil || plan.Events.OnBusy == nil {
		t.Fatalf("plan=%#v", plan)
	}
	encoded, err := CanonicalJSON(raw, MaxEnvelopeBytes)
	if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(raw)) {
		t.Fatalf("noncanonical ring plan\n%s", encoded)
	}
	marshaled, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	round, err := CanonicalJSON(marshaled, MaxEnvelopeBytes)
	if err != nil || !bytes.Equal(round, encoded) {
		t.Fatalf("encode/decode drifted\n%s\n%s", round, encoded)
	}
}

func TestRingLifecycleEventGoldens(t *testing.T) {
	for _, name := range []string{"ring-event-no-answer", "ring-event-busy"} {
		raw, err := os.ReadFile("testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var event RingLifecycleEvent
		if err = Decode(raw, MaxEnvelopeBytes, &event); err != nil {
			t.Fatal(err)
		}
		if err = event.Validate(); err != nil {
			t.Fatal(err)
		}
		encoded, err := CanonicalJSON(raw, MaxEnvelopeBytes)
		if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(raw)) {
			t.Fatalf("%s noncanonical\n%s", name, encoded)
		}
		marshaled, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		round, err := CanonicalJSON(marshaled, MaxEnvelopeBytes)
		if err != nil || !bytes.Equal(round, encoded) {
			t.Fatalf("%s encode/decode drifted\n%s\n%s", name, round, encoded)
		}
	}
}

func TestRingEventsDoNotMintAndDoNotOwnAnswerTransfer(t *testing.T) {
	raw, err := os.ReadFile("testdata/ring-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = Decode(bytes.TrimSpace(raw), MaxEnvelopeBytes, &RingPlan{}); err != nil {
		t.Fatal(err)
	}
	for _, injected := range []string{
		`{"events":{"on_no_answer":{"destination_id":"minted"}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_busy":{"mint":true}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_answered":{}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_transfer":{}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
	} {
		if err := Decode([]byte(injected), MaxEnvelopeBytes, &RingPlan{}); err == nil {
			t.Fatalf("accepted mint or ownership event %s", injected)
		}
	}
	for _, kind := range []string{"answered", "transfer", "mint", "fallback", "on_answered"} {
		event := RingLifecycleEvent{Version: Version, Type: kind, CallID: "call_1", RecipientSetRevision: "recipients_1", RecipientID: "recipient_1", Generation: 1, Sequence: 1}
		if err := event.Validate(); err == nil {
			t.Fatalf("accepted ownership/mint event %s", kind)
		}
	}
}
