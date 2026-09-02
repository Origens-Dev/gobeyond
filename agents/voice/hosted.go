package voice

import (
	"fmt"
	"net/url"
	"strings"
)

// Hosted voice API paths on the gbhost host-report UDS (G5 client / G5b server).
const (
	HostedStartPath = "/v1/agents/voice/start"
	HostedStopPath  = "/v1/agents/voice/stop"
	hostedPCMPrefix = "/v1/agents/voice/pcm/"

	// TransportUnix is the v1 PCM endpoint transport (slot-private UDS).
	TransportUnix = "unix"
	// FrameLengthPrefixedLE matches EncodeFrame / DecodeFrame.
	FrameLengthPrefixedLE = "length-prefixed-le"
	// DefaultAuthHeader is the HTTP header carrying the session token on PCM.
	DefaultAuthHeader = "Authorization"
)

// HostedStartRequest is POST /v1/agents/voice/start JSON (client → gbhost).
// Shared with gobeyond-internal G5b; keep field names stable.
type HostedStartRequest struct {
	AgentID          string            `json:"agent_id"`
	SessionID        string            `json:"session_id"`
	RunID            string            `json:"run_id"`
	CallID           string            `json:"call_id,omitempty"`
	NetworkID        string            `json:"network_id,omitempty"`
	CompiledRevision string            `json:"compiled_revision,omitempty"`
	VoiceName        string            `json:"voice_name,omitempty"`
	Instructions     string            `json:"instructions,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Actor            ActorDTO          `json:"actor"`
	PCMInSampleRate  int               `json:"pcm_in_sample_rate,omitempty"`
	PCMOutSampleRate int               `json:"pcm_out_sample_rate,omitempty"`
}

// ActorDTO is the JSON projection of agents.Actor for hosted voice start.
// Declared here so gobeyond-internal can decode without importing agents
// runtime packages if needed; values match agents.Actor JSON tags.
type ActorDTO struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// HostedStartResponse is the start reply. SessionToken is HMAC-bound on the
// server (SEC-A3); clients must treat it as a secret capability.
type HostedStartResponse struct {
	SessionToken string          `json:"session_token"`
	PCMEndpoint  PCMEndpointSpec `json:"pcm_endpoint_spec"`
}

// PCMEndpointSpec describes how to open the PCM binary stream after start.
//
// Example:
//
//	{"transport":"unix","path":"/run/gobeyond/host/host-report.sock","auth_header":"Authorization","frame":"length-prefixed-le"}
type PCMEndpointSpec struct {
	Transport  string `json:"transport"`
	Path       string `json:"path"`
	AuthHeader string `json:"auth_header"`
	Frame      string `json:"frame"`
	// PCMPath overrides the default /v1/agents/voice/pcm/{token} when set.
	PCMPath string `json:"pcm_path,omitempty"`
}

// HostedStopRequest is POST /v1/agents/voice/stop JSON. Teardown is idempotent.
type HostedStopRequest struct {
	SessionToken string `json:"session_token"`
	AgentID      string `json:"agent_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
}

// HostedPCMPath returns GET/POST /v1/agents/voice/pcm/{token}.
func HostedPCMPath(token string) string {
	token = strings.TrimSpace(token)
	return hostedPCMPrefix + url.PathEscape(token)
}

// Normalize fills defaults on a PCM endpoint spec returned by start.
func (spec *PCMEndpointSpec) Normalize(defaultUnixPath string) {
	if spec == nil {
		return
	}
	if strings.TrimSpace(spec.Transport) == "" {
		spec.Transport = TransportUnix
	}
	if strings.TrimSpace(spec.AuthHeader) == "" {
		spec.AuthHeader = DefaultAuthHeader
	}
	if strings.TrimSpace(spec.Frame) == "" {
		spec.Frame = FrameLengthPrefixedLE
	}
	if strings.TrimSpace(spec.Path) == "" {
		spec.Path = strings.TrimSpace(defaultUnixPath)
	}
}

// Validate checks transport/frame values the G5 client understands.
func (spec PCMEndpointSpec) Validate() error {
	switch strings.ToLower(strings.TrimSpace(spec.Transport)) {
	case TransportUnix, "":
	default:
		return fmt.Errorf("unsupported PCM transport %q", spec.Transport)
	}
	switch strings.ToLower(strings.TrimSpace(spec.Frame)) {
	case FrameLengthPrefixedLE, "":
	default:
		return fmt.Errorf("unsupported PCM frame %q", spec.Frame)
	}
	if strings.TrimSpace(spec.Path) == "" {
		return fmt.Errorf("PCM endpoint path is required")
	}
	return nil
}

// RedactToken returns a log-safe preview. Never log the full session_token
// (SEC-A3 denylist). Empty input stays empty.
func RedactToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 10 {
		return "***"
	}
	return token[:4] + "…***…" + token[len(token)-2:]
}
