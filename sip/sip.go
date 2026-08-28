// Package sip is the public SIP decision surface for the agents voice channel
// (ADR 018). It is channel transport — not an agent package.
//
// Sites author handlers in a sibling file agents/<id>/sip.go next to agent.go,
// importing this module as gbsip. Omitted methods use platform defaults in
// gobeyond-internal docs/voice-sip-contracts.md. This package must not expose
// Contact rewrite, RTP addresses, RouteID, Dynamo keys, or raw ep_* as
// customer routing knobs.
package sip

import (
	"context"
	"encoding/json"
)

// Method is the closed SIP method enum. Do not extend without a new ADR.
type Method string

const (
	MethodInvite    Method = "INVITE"
	MethodAck       Method = "ACK"
	MethodBye       Method = "BYE"
	MethodCancel    Method = "CANCEL"
	MethodRegister  Method = "REGISTER"
	MethodOptions   Method = "OPTIONS"
	MethodUpdate    Method = "UPDATE"
	MethodInfo      Method = "INFO"
	MethodPrack     Method = "PRACK"
	MethodRefer     Method = "REFER"
	MethodSubscribe Method = "SUBSCRIBE"
	MethodNotify    Method = "NOTIFY"
	MethodMessage   Method = "MESSAGE"
	MethodPublish   Method = "PUBLISH"
)

// AllMethods is the frozen closed enum order.
func AllMethods() []Method {
	return []Method{
		MethodInvite, MethodAck, MethodBye, MethodCancel, MethodRegister,
		MethodOptions, MethodUpdate, MethodInfo, MethodPrack, MethodRefer,
		MethodSubscribe, MethodNotify, MethodMessage, MethodPublish,
	}
}

// Class is the wire timing class for a method.
type Class string

const (
	ClassBlockingDecision   Class = "blocking_decision"
	ClassNonBlockingObserve Class = "non_blocking_observe"
)

// ClassOf returns the handler class for a closed method.
func ClassOf(m Method) Class {
	switch m {
	case MethodAck, MethodPrack:
		return ClassNonBlockingObserve
	default:
		return ClassBlockingDecision
	}
}

// Decision values for Response.Decision.
const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
)

// SDPSummary is a sanitized codec/direction summary — never a raw SDP body.
type SDPSummary struct {
	Codecs   []string `json:"codecs,omitempty"`
	SendRecv string   `json:"sendrecv,omitempty"` // sendrecv | sendonly | recvonly | inactive
	Hold     bool     `json:"hold,omitempty"`
}

// Request is the public handler input. Proprietary routing stays in gobeyond-internal.
type Request struct {
	Method         Method            `json:"method"`
	InDialog       bool              `json:"in_dialog,omitempty"`
	CallID         string            `json:"call_id"`
	Direction      string            `json:"direction,omitempty"`
	OrganizationID string            `json:"organization_id,omitempty"`
	ProjectID      string            `json:"project_id,omitempty"`
	EnvironmentID  string            `json:"environment_id,omitempty"`
	AgentID        string            `json:"agent_id,omitempty"`
	ChannelID      string            `json:"channel_id,omitempty"`
	ConnectorName  string            `json:"connector_name,omitempty"`
	SDPSummary     SDPSummary        `json:"sdp_summary,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"` // allowlisted only
}

// Response is the public handler output for BlockingDecision methods.
type Response struct {
	Decision  string          `json:"decision"` // accept | reject
	SIPStatus int             `json:"sip_status,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// DecisionFunc is a BlockingDecision handler.
type DecisionFunc func(context.Context, Request) (Response, error)

// ObserveFunc is a NonBlockingObserve handler (Ack, Prack). Edge never waits.
type ObserveFunc func(context.Context, Request) error

// Handlers is the site-registered subset. Nil fields use documented platform defaults.
type Handlers struct {
	Invite    DecisionFunc
	Ack       ObserveFunc
	Bye       DecisionFunc
	Cancel    DecisionFunc
	Register  DecisionFunc
	Options   DecisionFunc
	Update    DecisionFunc
	Info      DecisionFunc
	Prack     ObserveFunc
	Refer     DecisionFunc
	Subscribe DecisionFunc
	Notify    DecisionFunc
	Message   DecisionFunc
	Publish   DecisionFunc
}

// RegisteredMethods returns the closed-enum methods that have a non-nil handler.
func (h Handlers) RegisteredMethods() []Method {
	out := make([]Method, 0, 14)
	if h.Invite != nil {
		out = append(out, MethodInvite)
	}
	if h.Ack != nil {
		out = append(out, MethodAck)
	}
	if h.Bye != nil {
		out = append(out, MethodBye)
	}
	if h.Cancel != nil {
		out = append(out, MethodCancel)
	}
	if h.Register != nil {
		out = append(out, MethodRegister)
	}
	if h.Options != nil {
		out = append(out, MethodOptions)
	}
	if h.Update != nil {
		out = append(out, MethodUpdate)
	}
	if h.Info != nil {
		out = append(out, MethodInfo)
	}
	if h.Prack != nil {
		out = append(out, MethodPrack)
	}
	if h.Refer != nil {
		out = append(out, MethodRefer)
	}
	if h.Subscribe != nil {
		out = append(out, MethodSubscribe)
	}
	if h.Notify != nil {
		out = append(out, MethodNotify)
	}
	if h.Message != nil {
		out = append(out, MethodMessage)
	}
	if h.Publish != nil {
		out = append(out, MethodPublish)
	}
	return out
}

// HasCustom reports whether a method has a site-registered handler.
func (h Handlers) HasCustom(m Method) bool {
	switch m {
	case MethodInvite:
		return h.Invite != nil
	case MethodAck:
		return h.Ack != nil
	case MethodBye:
		return h.Bye != nil
	case MethodCancel:
		return h.Cancel != nil
	case MethodRegister:
		return h.Register != nil
	case MethodOptions:
		return h.Options != nil
	case MethodUpdate:
		return h.Update != nil
	case MethodInfo:
		return h.Info != nil
	case MethodPrack:
		return h.Prack != nil
	case MethodRefer:
		return h.Refer != nil
	case MethodSubscribe:
		return h.Subscribe != nil
	case MethodNotify:
		return h.Notify != nil
	case MethodMessage:
		return h.Message != nil
	case MethodPublish:
		return h.Publish != nil
	default:
		return false
	}
}

// Accept is a convenience accept response.
func Accept() Response {
	return Response{Decision: DecisionAccept}
}

// Reject builds a reject response with an optional SIP status hint.
func Reject(sipStatus int, reason string) Response {
	return Response{Decision: DecisionReject, SIPStatus: sipStatus, Reason: reason}
}
