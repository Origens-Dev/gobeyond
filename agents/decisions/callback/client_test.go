package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/connections"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

func TestMatchClientSignsDeliveryAndRetriesSameEventID(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{9}, 32)
	now := time.Unix(1800000000, 0)
	definition := greetingDefinition()
	var eventIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(writer, "read", http.StatusBadRequest)
			return
		}
		eventID, err := connections.VerifyDelivery(req.Header, key, body, now)
		if err != nil {
			t.Errorf("verify: %v", err)
			http.Error(writer, "auth", http.StatusUnauthorized)
			return
		}
		eventIDs = append(eventIDs, eventID)
		if req.URL.Path != PathMatch || req.Method != Method {
			t.Errorf("request %s %s", req.Method, req.URL.Path)
		}
		if _, err := DecodeMatchRequest(body); err != nil {
			t.Errorf("request body: %v", err)
			http.Error(writer, "body", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":"matched","targetId":"target-1"}`))
	}))
	t.Cleanup(server.Close)

	client := &MatchClient{BaseURL: server.URL, Key: key, Now: func() time.Time { return now }, HTTPClient: server.Client()}
	request := MatchRequest{SessionID: "session-1", Utterance: "call sam", AttemptCount: 2}
	first, err := client.Match(context.Background(), definition, request)
	if err != nil || first.Result != MatchMatched || first.TargetID != "target-1" {
		t.Fatalf("first match = %#v err=%v", first, err)
	}
	second, err := client.Match(context.Background(), definition, request)
	if err != nil || second != first {
		t.Fatalf("retry diverged: first=%#v second=%#v err=%v", first, second, err)
	}
	wantID := MatchEventID(request.SessionID, request.AttemptCount)
	if wantID != "match/session-1/2" {
		t.Fatalf("event id = %q", wantID)
	}
	if len(eventIDs) != 2 || eventIDs[0] != wantID || eventIDs[1] != wantID {
		t.Fatalf("retried Event-ID not reused: %v want %q", eventIDs, wantID)
	}
}

func TestMatchClientClarifyUnknownPromptFailsClosed(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{3}, 32)
	now := time.Unix(1800000000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":"clarify","promptId":"missing"}`))
	}))
	t.Cleanup(server.Close)
	client := &MatchClient{BaseURL: server.URL, Key: key, Now: func() time.Time { return now }, HTTPClient: server.Client()}
	_, err := client.Match(context.Background(), greetingDefinition(), MatchRequest{
		SessionID: "session-1", Utterance: "who", AttemptCount: 0,
	})
	if !errors.Is(err, decisionv1.ErrPromptUnavailable) {
		t.Fatalf("unknown clarify: %v", err)
	}
}

func TestMatchClientKnownClarifyPassesValidateFor(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{4}, 32)
	now := time.Unix(1800000000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":"clarify","promptId":"greeting"}`))
	}))
	t.Cleanup(server.Close)
	client := &MatchClient{BaseURL: server.URL, Key: key, Now: func() time.Time { return now }, HTTPClient: server.Client()}
	got, err := client.Match(context.Background(), greetingDefinition(), MatchRequest{
		SessionID: "session-1", Utterance: "who", AttemptCount: 1,
	})
	if err != nil || got.Result != MatchClarify || got.PromptID != "greeting" {
		t.Fatalf("known clarify = %#v err=%v", got, err)
	}
}

func TestMatchEventIDIsStableForSameAttempt(t *testing.T) {
	t.Parallel()
	first := MatchEventID("session-1", 0)
	second := MatchEventID("session-1", 0)
	if first == "" || first != second || first == MatchEventID("session-1", 1) {
		t.Fatalf("stable event id failed: %q %q", first, second)
	}
	raw, _ := json.Marshal(MatchRequest{SessionID: "session-1", Utterance: "call sam", AttemptCount: 0})
	if !bytes.Contains(raw, []byte("session-1")) {
		t.Fatal("request lost session id")
	}
}
