package sip_test

import (
	"context"
	"testing"

	gbsip "github.com/Origens-Dev/gobeyond/sip"
)

func TestRegisteredMethodsSubset(t *testing.T) {
	h := gbsip.Handlers{
		Invite: func(context.Context, gbsip.Request) (gbsip.Response, error) {
			return gbsip.Accept(), nil
		},
		Ack: func(context.Context, gbsip.Request) error { return nil },
	}
	got := h.RegisteredMethods()
	if len(got) != 2 || got[0] != gbsip.MethodInvite || got[1] != gbsip.MethodAck {
		t.Fatalf("%v", got)
	}
	if !h.HasCustom(gbsip.MethodInvite) || h.HasCustom(gbsip.MethodBye) {
		t.Fatal("HasCustom mismatch")
	}
	if gbsip.ClassOf(gbsip.MethodAck) != gbsip.ClassNonBlockingObserve {
		t.Fatal("ack class")
	}
	if gbsip.ClassOf(gbsip.MethodInvite) != gbsip.ClassBlockingDecision {
		t.Fatal("invite class")
	}
}

func TestClosedEnumCount(t *testing.T) {
	if n := len(gbsip.AllMethods()); n != 14 {
		t.Fatalf("closed enum size %d", n)
	}
}
