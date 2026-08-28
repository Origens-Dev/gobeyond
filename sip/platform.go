package sip

import "encoding/json"

// Platform control paths and auth header — wire-compatible with
// gobeyond-internal packages/voicesipdecision.
const (
	DecidePath  = "/internal/sip/decide"
	ObservePath = "/internal/sip/observe"
	AuthHeader  = "x-gobeyond-sip-decision-token"
)

// PlatformRequest is the proprietary edge→tenant envelope. Public carries
// Request JSON; EndpointID and other routing fields stay platform-only.
type PlatformRequest struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Attempt        int             `json:"attempt"`
	TimeoutMS      int             `json:"timeout_ms"`
	EndpointID     string          `json:"endpoint_id"`
	Public         json.RawMessage `json:"public"`
}

// PlatformResponse is the tenant→edge decision envelope.
type PlatformResponse struct {
	Decision  string          `json:"decision"`
	SIPStatus int             `json:"sip_status,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}
