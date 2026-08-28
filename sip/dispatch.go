package sip

import (
	"context"
	"fmt"
)

// Decide runs a BlockingDecision handler for req.Method.
// Ack/Prack must use Observe — Decide returns an error for those methods.
// Nil handlers use platform defaults: Accept for Invite and other
// auto-admit methods; Reject(501) for unsupported methods.
func (h Handlers) Decide(ctx context.Context, req Request) (Response, error) {
	switch req.Method {
	case MethodAck, MethodPrack:
		return Response{}, fmt.Errorf("sip: %s must use Observe, not Decide", req.Method)
	case MethodInvite:
		if h.Invite == nil {
			return Accept(), nil
		}
		return h.Invite(ctx, req)
	case MethodBye:
		if h.Bye == nil {
			return Accept(), nil
		}
		return h.Bye(ctx, req)
	case MethodCancel:
		if h.Cancel == nil {
			return Accept(), nil
		}
		return h.Cancel(ctx, req)
	case MethodRegister:
		if h.Register == nil {
			return Accept(), nil
		}
		return h.Register(ctx, req)
	case MethodOptions:
		if h.Options == nil {
			return Accept(), nil
		}
		return h.Options(ctx, req)
	case MethodUpdate:
		if h.Update == nil {
			return Accept(), nil
		}
		return h.Update(ctx, req)
	case MethodInfo:
		if h.Info == nil {
			return Reject(501, "Not Implemented"), nil
		}
		return h.Info(ctx, req)
	case MethodRefer:
		if h.Refer == nil {
			return Reject(501, "Not Implemented"), nil
		}
		return h.Refer(ctx, req)
	case MethodSubscribe:
		if h.Subscribe == nil {
			return Reject(501, "Not Implemented"), nil
		}
		return h.Subscribe(ctx, req)
	case MethodNotify:
		if h.Notify == nil {
			return Reject(501, "Not Implemented"), nil
		}
		return h.Notify(ctx, req)
	case MethodMessage:
		if h.Message == nil {
			return Reject(501, "Not Implemented"), nil
		}
		return h.Message(ctx, req)
	case MethodPublish:
		if h.Publish == nil {
			return Reject(501, "Not Implemented"), nil
		}
		return h.Publish(ctx, req)
	default:
		return Reject(501, "Not Implemented"), nil
	}
}

// Observe runs a NonBlockingObserve handler (Ack or Prack). Nil is a no-op.
func (h Handlers) Observe(ctx context.Context, req Request) error {
	switch req.Method {
	case MethodAck:
		if h.Ack == nil {
			return nil
		}
		return h.Ack(ctx, req)
	case MethodPrack:
		if h.Prack == nil {
			return nil
		}
		return h.Prack(ctx, req)
	default:
		return fmt.Errorf("sip: Observe only supports ACK and PRACK, got %s", req.Method)
	}
}
