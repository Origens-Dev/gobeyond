package voice

import (
	"encoding/binary"
	"fmt"
	"io"
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
	// FrameLengthPrefixedLEV2 carries AudioFrame control flags.
	FrameLengthPrefixedLEV2 = "length-prefixed-le-v2"
	// FrameLengthPrefixedLEV3 adds an explicit playout flush barrier.
	FrameLengthPrefixedLEV3 = "length-prefixed-le-v3"
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
	VoiceProvider    string            `json:"voice_provider,omitempty"`
	VoiceModel       string            `json:"voice_model,omitempty"`
	CompiledRevision string            `json:"compiled_revision,omitempty"`
	VoiceName        string            `json:"voice_name,omitempty"`
	Instructions     string            `json:"instructions,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Actor            ActorDTO          `json:"actor"`
	// EnabledToolIDs is a canonical allowlist; hosts map IDs to fixed schemas.
	EnabledToolIDs []string `json:"enabled_tool_ids,omitempty"`
	// PCMProtocolVersion 3 enables explicit telephone playout barriers.
	PCMProtocolVersion int               `json:"pcm_protocol_version,omitempty"`
	PCMInSampleRate    int               `json:"pcm_in_sample_rate,omitempty"`
	PCMOutSampleRate   int               `json:"pcm_out_sample_rate,omitempty"`
	AudioPreferences   AudioPreferences  `json:"audio_preferences,omitempty"`
	AudioCapabilities  AudioCapabilities `json:"audio_capabilities,omitempty"`
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
	Transport    string      `json:"transport"`
	Path         string      `json:"path"`
	AuthHeader   string      `json:"auth_header"`
	Frame        string      `json:"frame"`
	InputFormat  AudioFormat `json:"input_format,omitempty"`
	OutputFormat AudioFormat `json:"output_format,omitempty"`
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
	case FrameLengthPrefixedLE, FrameLengthPrefixedLEV2, FrameLengthPrefixedLEV3, "":
	default:
		return fmt.Errorf("unsupported PCM frame %q", spec.Frame)
	}
	if strings.TrimSpace(spec.Path) == "" {
		return fmt.Errorf("PCM endpoint path is required")
	}
	return nil
}

// EncodeAudioFrame encodes one v2 output frame. The first payload byte is
// flags: bit 0 is interruption and bit 1 is turn completion. v3-only Flush
// frames must use EncodeAudioFrameV3.
func EncodeAudioFrame(frame AudioFrame) ([]byte, error) {
	if frame.Flush || frame.BarrierID != 0 {
		return nil, fmt.Errorf("flush barriers require v3 voice framing")
	}
	if len(frame.Data)+1 > MaxFrameBytes {
		return nil, fmt.Errorf("voice audio frame exceeds MaxFrameBytes")
	}
	flags := byte(0)
	if frame.Interrupted {
		flags |= 1
	}
	if frame.TurnComplete {
		flags |= 2
	}
	payload := make([]byte, 1+len(frame.Data))
	payload[0] = flags
	copy(payload[1:], frame.Data)
	out := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(payload)))
	copy(out[4:], payload)
	return out, nil
}

// EncodeAudioFrameV3 encodes one output frame. v3 adds bit 2 (Flush) and an
// eight-byte little-endian BarrierID after the flags byte for Flush frames.
func EncodeAudioFrameV3(frame AudioFrame) ([]byte, error) {
	extra := 0
	if frame.Flush {
		extra = 8
	}
	if len(frame.Data)+1+extra > MaxFrameBytes {
		return nil, fmt.Errorf("voice audio frame exceeds MaxFrameBytes")
	}
	flags := byte(0)
	if frame.Interrupted {
		flags |= 1
	}
	if frame.TurnComplete {
		flags |= 2
	}
	if frame.Flush {
		flags |= 4
	}
	payload := make([]byte, 1+extra+len(frame.Data))
	payload[0] = flags
	if frame.Flush {
		binary.LittleEndian.PutUint64(payload[1:9], frame.BarrierID)
	}
	copy(payload[1+extra:], frame.Data)
	out := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(payload)))
	copy(out[4:], payload)
	return out, nil
}

// DecodeAudioFrame reads one v2 output frame.
func DecodeAudioFrame(r io.Reader) (AudioFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return AudioFrame{}, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n < 1 || n > MaxFrameBytes {
		return AudioFrame{}, fmt.Errorf("voice audio frame length %d is invalid", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return AudioFrame{}, err
	}
	return AudioFrame{
		Interrupted:  payload[0]&1 != 0,
		TurnComplete: payload[0]&2 != 0,
		Data:         append([]byte(nil), payload[1:]...),
	}, nil
}

// DecodeAudioFrameV3 reads one v3 output frame.
func DecodeAudioFrameV3(r io.Reader) (AudioFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return AudioFrame{}, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n < 1 || n > MaxFrameBytes {
		return AudioFrame{}, fmt.Errorf("voice audio frame length %d is invalid", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return AudioFrame{}, err
	}
	frame := AudioFrame{
		Interrupted:  payload[0]&1 != 0,
		TurnComplete: payload[0]&2 != 0,
		Flush:        payload[0]&4 != 0,
	}
	dataOffset := 1
	if frame.Flush {
		if len(payload) < 9 {
			return AudioFrame{}, fmt.Errorf("voice flush frame is missing barrier id")
		}
		frame.BarrierID = binary.LittleEndian.Uint64(payload[1:9])
		dataOffset = 9
	}
	frame.Data = append([]byte(nil), payload[dataOffset:]...)
	return frame, nil
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
