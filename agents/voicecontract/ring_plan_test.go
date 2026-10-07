package voicecontract

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestRingPlanGoldenEncodeDecode(t *testing.T) {
	raw, err := os.ReadFile("testdata/ring-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan RingPlan
	if err = Decode(raw, MaxRingPlanBytes, &plan); err != nil {
		t.Fatal(err)
	}
	if err = plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if plan.Version != Version || plan.AttachedFallback == nil || plan.Events.OnNoAnswer == nil || plan.Events.OnBusy == nil {
		t.Fatalf("plan=%#v", plan)
	}
	encoded, digest, err := FreezeRingPlan(plan)
	if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(raw)) {
		t.Fatalf("noncanonical ring plan\n%s", encoded)
	}
	if digest != Digest(encoded) {
		t.Fatalf("digest drifted %s", digest)
	}
	marshaled, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	round, err := CanonicalJSON(marshaled, MaxRingPlanBytes)
	if err != nil || !bytes.Equal(round, encoded) {
		t.Fatalf("encode/decode drifted\n%s\n%s", round, encoded)
	}
}

func TestRingPlanMultiGoldenFreeze(t *testing.T) {
	raw, err := os.ReadFile("testdata/ring-plan-multi.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan RingPlan
	if err = Decode(raw, MaxRingPlanBytes, &plan); err != nil {
		t.Fatal(err)
	}
	if err = plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Recipients) != 2 || !plan.Observes(RingEventOnFailed) || !plan.Observes(RingEventOnCancelled) {
		t.Fatalf("plan=%#v", plan)
	}
	if _, ok := plan.Recipient("recipient_revision1_opaque2"); !ok {
		t.Fatal("missing second recipient")
	}
	encoded, _, err := FreezeRingPlan(plan)
	if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(raw)) {
		t.Fatalf("noncanonical multi ring plan\n%s", encoded)
	}
}

func TestRingLifecycleEventGoldens(t *testing.T) {
	for _, name := range []string{"ring-event-no-answer", "ring-event-busy", "ring-event-failed", "ring-event-cancelled"} {
		raw, err := os.ReadFile("testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var event RingLifecycleEvent
		if err = Decode(raw, MaxRingPlanBytes, &event); err != nil {
			t.Fatal(err)
		}
		if err = event.Validate(); err != nil {
			t.Fatal(err)
		}
		encoded, digest, err := FreezeRingLifecycleEvent(event)
		if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(raw)) {
			t.Fatalf("%s noncanonical\n%s", name, encoded)
		}
		if digest != Digest(encoded) {
			t.Fatalf("%s digest drifted %s", name, digest)
		}
		marshaled, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		round, err := CanonicalJSON(marshaled, MaxRingPlanBytes)
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
	if err = Decode(bytes.TrimSpace(raw), MaxRingPlanBytes, &RingPlan{}); err != nil {
		t.Fatal(err)
	}
	for _, injected := range []string{
		`{"events":{"on_no_answer":{"destination_id":"minted"}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_busy":{"mint":true}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_failed":{"grant":"g"}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_cancelled":{"hop_id":"hop_1"}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"attached_fallback":{"destination_id":"destination_1","grant":"g"},"events":{},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_answered":{}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
		`{"events":{"on_transfer":{}},"recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`,
	} {
		if err := Decode([]byte(injected), MaxRingPlanBytes, &RingPlan{}); err == nil {
			t.Fatalf("accepted mint or ownership event %s", injected)
		}
	}
	for _, kind := range []string{"answered", "transfer", "mint", "fallback", "on_answered", "grant", "hop"} {
		event := RingLifecycleEvent{Version: Version, Type: kind, CallID: "call_1", RecipientSetRevision: "recipients_1", RecipientID: "recipient_1", Generation: 1, Sequence: 1}
		if err := event.Validate(); err == nil {
			t.Fatalf("accepted ownership/mint event %s", kind)
		}
	}
	for _, injected := range []string{
		`{"call_id":"call_1","destination_id":"destination_1","generation":1,"recipient_id":"recipient_1","recipient_set_revision":"recipients_1","sequence":1,"type":"on_no_answer","version":"2"}`,
		`{"call_id":"call_1","generation":1,"grant":"g","recipient_id":"recipient_1","recipient_set_revision":"recipients_1","sequence":1,"type":"on_busy","version":"2"}`,
		`{"call_id":"call_1","generation":1,"hop_id":"hop_1","recipient_id":"recipient_1","recipient_set_revision":"recipients_1","sequence":1,"type":"on_failed","version":"2"}`,
		`{"call_id":"call_1","fallback":{"destination_id":"destination_1"},"generation":1,"recipient_id":"recipient_1","recipient_set_revision":"recipients_1","sequence":1,"type":"on_cancelled","version":"2"}`,
	} {
		if err := Decode([]byte(injected), MaxRingPlanBytes, &RingLifecycleEvent{}); err == nil {
			t.Fatalf("accepted minting ring event %s", injected)
		}
	}
	event := RingLifecycleEvent{Version: Version, Type: RingEventOnNoAnswer, CallID: "call_1", RecipientSetRevision: "recipients_1", RecipientID: "recipient_1", Generation: 1, Sequence: 1}
	encoded, _, err := FreezeRingLifecycleEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"grant"`, `"hop_id"`, `"destination_id"`, `"mint"`, `"fallback"`} {
		if bytes.Contains(encoded, []byte(banned)) {
			t.Fatalf("event freeze minted %s: %s", banned, encoded)
		}
	}
}

func TestAttachedFallbackIsSoleAutomaticActivation(t *testing.T) {
	raw, err := os.ReadFile("testdata/ring-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan RingPlan
	if err = Decode(raw, MaxRingPlanBytes, &plan); err != nil {
		t.Fatal(err)
	}
	dest, ok := plan.AutomaticFallbackDestination()
	if !ok || dest != "destination_fallback_1" {
		t.Fatalf("attached fallback %q %v", dest, ok)
	}
	noFallback := plan
	noFallback.AttachedFallback = nil
	if _, ok = noFallback.AutomaticFallbackDestination(); ok {
		t.Fatal("empty plan activated fallback")
	}
	event := RingLifecycleEvent{Version: Version, Type: RingEventOnNoAnswer, CallID: "call_1", RecipientSetRevision: plan.RecipientSetRevision, RecipientID: plan.Recipients[0].RecipientID, Generation: 1, Sequence: 1}
	if err = event.Matches(plan); err != nil {
		t.Fatal(err)
	}
	encoded, _, err := FreezeRingLifecycleEvent(event)
	if err != nil || bytes.Contains(encoded, []byte("destination_fallback_1")) {
		t.Fatalf("event carried fallback destination %s %v", encoded, err)
	}
}

func TestRingEventMatchesPlan(t *testing.T) {
	raw, err := os.ReadFile("testdata/ring-plan-multi.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan RingPlan
	if err = Decode(raw, MaxRingPlanBytes, &plan); err != nil {
		t.Fatal(err)
	}
	event := RingLifecycleEvent{Version: Version, Type: RingEventOnFailed, CallID: "call_1", RecipientSetRevision: "recipients_1", RecipientID: "recipient_revision1_opaque2", Generation: 2, Sequence: 3}
	if err = event.Matches(plan); err != nil {
		t.Fatal(err)
	}
	unobserved := plan
	unobserved.Events.OnFailed = nil
	if err = event.Matches(unobserved); err == nil {
		t.Fatal("matched unobserved event")
	}
	event.RecipientSetRevision = "recipients_other"
	if err = event.Matches(plan); err == nil {
		t.Fatal("matched foreign revision")
	}
	event.RecipientSetRevision = "recipients_1"
	event.RecipientID = "recipient_unknown"
	if err = event.Matches(plan); err == nil {
		t.Fatal("matched unknown recipient")
	}
}

func TestRingPlanDoesNotInterpretPolicyRefs(t *testing.T) {
	injected := `{"events":{},"policy_ref":"operator_mailbox_v1","recipient_set_revision":"recipients_1","recipients":[{"recipient_id":"recipient_1"}],"version":"2"}`
	if err := Decode([]byte(injected), MaxRingPlanBytes, &RingPlan{}); err == nil {
		t.Fatal("accepted policy_ref on ring plan")
	}
	plan := RingPlan{Version: Version, RecipientSetRevision: "recipients_1", Recipients: []RingRecipient{{RecipientID: "recipient_1", PublicLabel: "call-operator"}}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if dest, ok := plan.AutomaticFallbackDestination(); ok || dest != "" {
		t.Fatal("interpreted product label as fallback")
	}
}

func TestRingPlanValidateRejects(t *testing.T) {
	valid := RingPlan{Version: Version, RecipientSetRevision: "recipients_1", Recipients: []RingRecipient{{RecipientID: "recipient_1"}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FreezeRingPlan(valid); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]RingRecipient, MaxRingRecipients+1)
	for i := range tooMany {
		tooMany[i].RecipientID = "recipient_" + strconv.Itoa(i)
	}
	cases := []RingPlan{
		{Version: LegacyVersion, RecipientSetRevision: "recipients_1", Recipients: valid.Recipients},
		{Version: Version, Recipients: valid.Recipients},
		{Version: Version, RecipientSetRevision: "recipients_1"},
		{Version: Version, RecipientSetRevision: "recipients_1", Recipients: []RingRecipient{{RecipientID: "recipient_1"}, {RecipientID: "recipient_1"}}},
		{Version: Version, RecipientSetRevision: "recipients_1", Recipients: tooMany},
		{Version: Version, RecipientSetRevision: "recipients_1", Recipients: []RingRecipient{{RecipientID: "recipient_1", DestinationID: "bad dest"}}},
	}
	for i, p := range cases {
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted invalid plan %d %#v", i, p)
		}
		if _, _, err := FreezeRingPlan(p); err == nil {
			t.Fatalf("froze invalid plan %d", i)
		}
	}
	if RecognizedRingEvent("on_answered") || !RecognizedRingEvent(RingEventOnBusy) {
		t.Fatal("event type set drifted")
	}
	if got := RingEventTypes(); len(got) != 4 || got[0] != RingEventOnNoAnswer {
		t.Fatalf("event types %v", got)
	}
}
