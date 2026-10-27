package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/connections"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
)

type compileHost struct{}

func (compileHost) Match(context.Context, MatchRequest) (MatchResponse, error) {
	return MatchResponse{Result: MatchNoMatch}, nil
}

type compileEffects struct{}

func (compileEffects) Say(context.Context, SayEffect) (PlaybackReceipt, error) {
	return PlaybackReceipt{}, decisionv1.ErrPromptUnavailable
}
func (compileEffects) Listen(context.Context, ListenEffect) error { return nil }
func (compileEffects) Act(context.Context, ActEffect) error       { return nil }

type stickyHost struct{}

func (stickyHost) Match(_ context.Context, request MatchRequest) (MatchResponse, error) {
	if request.Utterance == "call sam" {
		return MatchResponse{Result: MatchMatched, TargetID: "target-1"}, nil
	}
	return MatchResponse{Result: MatchNoMatch}, nil
}

var (
	_ HostCallback          = compileHost{}
	_ HostCallback          = stickyHost{}
	_ DecisionEffectAdapter = compileEffects{}
	_                       = decisions.EffectSay
	_                       = decisions.EffectListen
	_                       = decisions.EffectDispatchIntent
	_                       = decisions.EffectRunDecision
)

func greetingDefinition() decisionv1.Definition {
	return decisionv1.Definition{Graph: decisionv1.Graph{Messages: []decisionv1.MessageFamily{{
		ID: "greeting", BaseLocale: "en", FallbackLocale: "en",
		Variants: map[string]string{"en": "Hello."},
	}}}}
}

func TestMatchPathUsesConnectionsDelivery(t *testing.T) {
	if PathMatch != "/v1/decision/callbacks/match" {
		t.Fatalf("unexpected match path %q", PathMatch)
	}
	if connections.DeliveryIDHeader == "" || connections.DeliveryTimestampHeader == "" || connections.DeliverySignatureHeader == "" {
		t.Fatal("connections delivery headers are required")
	}
}

func TestMatchRetrySameEventIDIsAnswered(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{7}, 32)
	body := []byte(`{"sessionId":"session-1","utterance":"call sam","attemptCount":0}`)
	now := time.Unix(1800000000, 0)
	header := http.Header{}
	if err := connections.SignDelivery(header, key, "match-1", body, now); err != nil {
		t.Fatal(err)
	}
	if _, err := connections.VerifyDelivery(header, key, body, now); err != nil {
		t.Fatal(err)
	}
	if _, err := connections.VerifyDelivery(header, key, body, now); err != nil {
		t.Fatalf("retried Event-ID rejected: %v", err)
	}
	request, err := DecodeMatchRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var host stickyHost
	first, err := host.Match(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Match(context.Background(), request)
	if err != nil || first != second || first.TargetID != "target-1" {
		t.Fatalf("retry diverged: first=%#v second=%#v err=%v", first, second, err)
	}
}

func TestMatchResponseTaggedUnion(t *testing.T) {
	t.Parallel()
	ok := []struct {
		body string
		want MatchResponse
	}{
		{`{"result":"matched","targetId":"target-1"}`, MatchResponse{Result: MatchMatched, TargetID: "target-1"}},
		{`{"result":"clarify","promptId":"greeting"}`, MatchResponse{Result: MatchClarify, PromptID: "greeting"}},
		{`{"result":"no_match"}`, MatchResponse{Result: MatchNoMatch}},
	}
	for _, tc := range ok {
		got, err := DecodeMatchResponse([]byte(tc.body))
		if err != nil || got != tc.want {
			t.Fatalf("body %s: got %#v err=%v", tc.body, got, err)
		}
	}
	rejected := []string{
		`{"result":"matched","targetId":"target-1","candidates":[{"name":"x"}]}`,
		`{"result":"matched"}`,
		`{"result":"clarify","promptId":"greeting","targetId":"target-1"}`,
		`{"result":"no_match","targetId":"target-1"}`,
		`{"result":"authorize"}`,
		`{"result":"matched","targetId":"target-1","extra":{}}`,
	}
	for _, body := range rejected {
		if _, err := DecodeMatchResponse([]byte(body)); err == nil {
			t.Fatalf("accepted extra or invalid match body %s", body)
		}
	}
}

func TestClarifyUnknownPromptIDFailsClosed(t *testing.T) {
	t.Parallel()
	definition := greetingDefinition()
	known, err := DecodeMatchResponse([]byte(`{"result":"clarify","promptId":"greeting"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := known.ValidateFor(definition); err != nil {
		t.Fatalf("known clarify prompt rejected: %v", err)
	}
	unknown, err := DecodeMatchResponse([]byte(`{"result":"clarify","promptId":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	err = unknown.ValidateFor(definition)
	if !errors.Is(err, decisionv1.ErrPromptUnavailable) {
		t.Fatalf("unknown clarify prompt: %v", err)
	}
	matched, err := DecodeMatchResponse([]byte(`{"result":"matched","targetId":"target-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := matched.ValidateFor(definition); err != nil {
		t.Fatalf("matched result: %v", err)
	}
}

func TestSayIsManifestPromptID(t *testing.T) {
	t.Parallel()
	effect, err := DecodeSayEffect([]byte(`{"turnId":"entry-1","promptId":"greeting"}`))
	if err != nil || effect.PromptID != "greeting" || effect.TurnID != "entry-1" {
		t.Fatalf("say = %#v err=%v", effect, err)
	}
	sayType := reflect.TypeOf(SayEffect{})
	for _, forbidden := range []string{"Fallback", "Cache", "Voice", "Model", "Text", "PromptKey"} {
		if _, found := sayType.FieldByName(forbidden); found {
			t.Fatalf("say must not carry %s", forbidden)
		}
		for i := 0; i < sayType.NumField(); i++ {
			tag := sayType.Field(i).Tag.Get("json")
			if strings.Contains(strings.ToLower(tag), strings.ToLower(forbidden)) {
				t.Fatalf("say json tag leaked %s", forbidden)
			}
		}
	}
	if _, err := DecodeSayEffect([]byte(`{"turnId":"entry-1","promptId":"greeting","text":"hello"}`)); err == nil {
		t.Fatal("free-text say accepted")
	}
}

func TestPlaybackReceiptAndListenEventsCarryTurnID(t *testing.T) {
	t.Parallel()
	receipt, err := DecodePlaybackReceipt([]byte(`{"turnId":"entry-1","status":"played"}`))
	if err != nil || receipt.Status != PlaybackPlayed {
		t.Fatalf("receipt = %#v err=%v", receipt, err)
	}
	if _, err := DecodePlaybackReceipt([]byte(`{"turnId":"entry-1","status":"failed"}`)); err == nil {
		t.Fatal("unknown playback status accepted")
	}
	final, err := DecodeListenEvent([]byte(`{"kind":"final_transcript","turnId":"entry-1","text":"call sam"}`))
	if err != nil || final.Text != "call sam" {
		t.Fatalf("transcript = %#v err=%v", final, err)
	}
	for _, body := range []string{
		`{"kind":"partial","turnId":"entry-1","text":"ca"}`,
		`{"kind":"final_transcript","turnId":"entry-1"}`,
		`{"kind":"no_input","turnId":"entry-1","text":"x"}`,
		`{"kind":"no_input"}`,
	} {
		if _, err := DecodeListenEvent([]byte(body)); err == nil {
			t.Fatalf("accepted invalid listen event %s", body)
		}
	}
	if !StaleTurn("entry-old", "entry-1") || StaleTurn("entry-1", "entry-1") {
		t.Fatal("stale turn helper")
	}
}

func TestEffectMappingUsesReducerTypes(t *testing.T) {
	t.Parallel()
	say, err := SayEffectFrom(decisions.Effect{
		Kind: decisions.EffectSay, RouteEntryID: "entry-1", MessageFamilies: []string{"greeting"},
	})
	if err != nil || say != (SayEffect{TurnID: "entry-1", PromptID: "greeting"}) {
		t.Fatalf("say mapping = %#v err=%v", say, err)
	}
	if _, err := SayEffectFrom(decisions.Effect{Kind: decisions.EffectSay, RouteEntryID: "entry-1"}); !errors.Is(err, decisionv1.ErrPromptUnavailable) {
		t.Fatalf("empty say families: %v", err)
	}
	listen, err := ListenEffectFrom(decisions.Effect{Kind: decisions.EffectListen, RouteEntryID: "entry-1"}, 4000, 15000, nil)
	if err != nil || listen.NoInputTimeoutMillis != 4000 || listen.TurnID != "entry-1" {
		t.Fatalf("listen mapping = %#v err=%v", listen, err)
	}
	act, err := ActEffectFrom(decisions.Effect{
		Kind: decisions.EffectDispatchIntent, RouteEntryID: "entry-1", ActionID: "connect",
		Request: &decisionv1.EffectRequest{TargetOpaqueID: "target-1"},
	})
	if err != nil || act != (ActEffect{TurnID: "entry-1", TargetID: "target-1", Action: "connect"}) {
		t.Fatalf("act mapping = %#v err=%v", act, err)
	}
}

func TestMatchJSONRoundTrip(t *testing.T) {
	t.Parallel()
	request := MatchRequest{SessionID: "session-1", Utterance: "call sam", AttemptCount: 1}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMatchRequest(raw)
	if err != nil || got != request {
		t.Fatalf("request = %#v err=%v", got, err)
	}
}

func TestListenHintsCapsRejectWithoutTruncation(t *testing.T) {
	t.Parallel()
	okHints := make([]string, MaxListenHints)
	for i := range okHints {
		okHints[i] = "hint"
	}
	ok, err := ListenEffectFrom(decisions.Effect{Kind: decisions.EffectListen, RouteEntryID: "entry-1"}, 4000, 15000, okHints)
	if err != nil || len(ok.Hints) != MaxListenHints {
		t.Fatalf("max hints rejected: %#v err=%v", ok, err)
	}
	fifty := strings.Repeat("a", MaxListenHintChars)
	okFifty, err := DecodeListenEffect([]byte(`{"turnId":"entry-1","noInputTimeoutMillis":4000,"maxUtteranceDurationMillis":15000,"hints":["` + fifty + `"]}`))
	if err != nil || len(okFifty.Hints) != 1 || okFifty.Hints[0] != fifty {
		t.Fatalf("50-char hint rejected: %#v err=%v", okFifty, err)
	}
	overCount := append(append([]string{}, okHints...), "one-more")
	if _, err := ListenEffectFrom(decisions.Effect{Kind: decisions.EffectListen, RouteEntryID: "entry-1"}, 4000, 15000, overCount); err == nil {
		t.Fatal("101 hints were truncated or accepted")
	}
	overLen := strings.Repeat("a", MaxListenHintChars+1)
	if _, err := DecodeListenEffect([]byte(`{"turnId":"entry-1","noInputTimeoutMillis":4000,"maxUtteranceDurationMillis":15000,"hints":["` + overLen + `"]}`)); err == nil {
		t.Fatal("51-char hint was truncated or accepted")
	}
	if _, err := DecodeListenEffect([]byte(`{"turnId":"entry-1","noInputTimeoutMillis":4000,"maxUtteranceDurationMillis":15000,"hints":["   "]}`)); err == nil {
		t.Fatal("whitespace-only hint accepted")
	}
	if _, err := DecodeListenEffect([]byte(`{"turnId":"entry-1","noInputTimeoutMillis":4000,"maxUtteranceDurationMillis":15000}`)); err != nil {
		t.Fatalf("omitted hints: %v", err)
	}
}
