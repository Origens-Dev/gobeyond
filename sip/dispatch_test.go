package sip_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gbsip "github.com/Origens-Dev/gobeyond/sip"
)

func TestDecideDefaultsAndDispatch(t *testing.T) {
	t.Parallel()
	empty := gbsip.Handlers{}
	got, err := empty.Decide(context.Background(), gbsip.Request{Method: gbsip.MethodInvite})
	if err != nil || got.Decision != gbsip.DecisionAccept {
		t.Fatalf("invite default: %+v err=%v", got, err)
	}
	got, err = empty.Decide(context.Background(), gbsip.Request{Method: gbsip.MethodInfo})
	if err != nil || got.Decision != gbsip.DecisionReject || got.SIPStatus != 501 {
		t.Fatalf("info default: %+v err=%v", got, err)
	}
	got, err = empty.Decide(context.Background(), gbsip.Request{Method: gbsip.Method("FOO")})
	if err != nil || got.SIPStatus != 501 {
		t.Fatalf("unknown: %+v err=%v", got, err)
	}
	if _, err := empty.Decide(context.Background(), gbsip.Request{Method: gbsip.MethodAck}); err == nil {
		t.Fatal("ack via Decide must error")
	}
	if err := empty.Observe(context.Background(), gbsip.Request{Method: gbsip.MethodAck}); err != nil {
		t.Fatalf("nil ack observe: %v", err)
	}
	if err := empty.Observe(context.Background(), gbsip.Request{Method: gbsip.MethodInvite}); err == nil {
		t.Fatal("invite via Observe must error")
	}

	called := false
	h := gbsip.Handlers{
		Invite: func(_ context.Context, req gbsip.Request) (gbsip.Response, error) {
			called = true
			if req.CallID != "c1" {
				t.Fatalf("call_id %q", req.CallID)
			}
			return gbsip.Reject(486, "Busy Here"), nil
		},
		Ack: func(context.Context, gbsip.Request) error {
			return errors.New("ack fail")
		},
	}
	got, err = h.Decide(context.Background(), gbsip.Request{Method: gbsip.MethodInvite, CallID: "c1"})
	if err != nil || !called || got.SIPStatus != 486 {
		t.Fatalf("custom invite: %+v err=%v called=%v", got, err, called)
	}
	if err := h.Observe(context.Background(), gbsip.Request{Method: gbsip.MethodAck}); err == nil || err.Error() != "ack fail" {
		t.Fatalf("custom ack: %v", err)
	}
}

func TestRegistryHTTPDecideAndObserve(t *testing.T) {
	t.Parallel()
	reg := gbsip.NewRegistry()
	if err := reg.Register("support", gbsip.Handlers{
		Invite: func(context.Context, gbsip.Request) (gbsip.Response, error) {
			return gbsip.Reject(603, "Decline"), nil
		},
		Ack: func(context.Context, gbsip.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("support", gbsip.Handlers{}); err == nil {
		t.Fatal("duplicate register must fail")
	}

	mux := http.NewServeMux()
	mux.Handle("/internal/sip/", reg.Handler("sekrit"))

	public, _ := json.Marshal(gbsip.Request{Method: gbsip.MethodInvite, AgentID: "support", CallID: "x"})
	body, _ := json.Marshal(gbsip.PlatformRequest{
		IdempotencyKey: "idem-1",
		EndpointID:     "ep_abc",
		Public:         public,
	})
	req := httptest.NewRequest(http.MethodPost, gbsip.DecidePath, bytes.NewReader(body))
	req.Header.Set(gbsip.AuthHeader, "sekrit")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("decide status %d body=%s", rec.Code, rec.Body.String())
	}
	var resp gbsip.PlatformResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Decision != gbsip.DecisionReject || resp.SIPStatus != 603 {
		t.Fatalf("%+v", resp)
	}

	obsPublic, _ := json.Marshal(gbsip.Request{Method: gbsip.MethodAck, AgentID: "support"})
	obsBody, _ := json.Marshal(gbsip.PlatformRequest{Public: obsPublic})
	obsReq := httptest.NewRequest(http.MethodPost, gbsip.ObservePath, bytes.NewReader(obsBody))
	obsReq.Header.Set(gbsip.AuthHeader, "sekrit")
	obsRec := httptest.NewRecorder()
	mux.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusNoContent {
		t.Fatalf("observe status %d", obsRec.Code)
	}

	badAuth := httptest.NewRequest(http.MethodPost, gbsip.DecidePath, bytes.NewReader(body))
	badAuth.Header.Set(gbsip.AuthHeader, "wrong")
	badRec := httptest.NewRecorder()
	mux.ServeHTTP(badRec, badAuth)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("auth status %d", badRec.Code)
	}

	missingPublic, _ := json.Marshal(gbsip.Request{Method: gbsip.MethodInvite, AgentID: "missing"})
	missingBody, _ := json.Marshal(gbsip.PlatformRequest{Public: missingPublic})
	missingReq := httptest.NewRequest(http.MethodPost, gbsip.DecidePath, bytes.NewReader(missingBody))
	missingReq.Header.Set(gbsip.AuthHeader, "sekrit")
	missingRec := httptest.NewRecorder()
	mux.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing agent status %d", missingRec.Code)
	}

	failReg := gbsip.NewRegistry()
	_ = failReg.Register("boom", gbsip.Handlers{
		Invite: func(context.Context, gbsip.Request) (gbsip.Response, error) {
			return gbsip.Response{}, errors.New("handler down")
		},
	})
	failMux := http.NewServeMux()
	failMux.Handle("/internal/sip/", failReg.Handler(""))
	failPublic, _ := json.Marshal(gbsip.Request{Method: gbsip.MethodInvite, AgentID: "boom"})
	failBody, _ := json.Marshal(gbsip.PlatformRequest{Public: failPublic})
	failReq := httptest.NewRequest(http.MethodPost, gbsip.DecidePath, bytes.NewReader(failBody))
	failRec := httptest.NewRecorder()
	failMux.ServeHTTP(failRec, failReq)
	if failRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("decide error status %d", failRec.Code)
	}
}

func TestPlatformEnvelopeJSONFieldNames(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"idempotency_key":"k","attempt":2,"timeout_ms":1500,"endpoint_id":"ep_1","public":{"method":"INVITE"}}`)
	var req gbsip.PlatformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.IdempotencyKey != "k" || req.Attempt != 2 || req.TimeoutMS != 1500 || req.EndpointID != "ep_1" {
		t.Fatalf("%+v", req)
	}
	out, err := json.Marshal(gbsip.PlatformResponse{Decision: "reject", SIPStatus: 486, Reason: "Busy"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{`"decision"`, `"sip_status"`, `"reason"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
}
