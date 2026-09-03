// Package voice defines the public Live PCM adapter contract for GoBeyond
// AI agents.
//
// # Audio framing
//
// Bidirectional audio between the voice bridge (voice-worker / gbhost) and an
// [Adapter] uses length-prefixed frames:
//
//   - Gemini audio is signed 16-bit little-endian mono (no WAV/RIFF header).
//   - Grok may use raw G.711 μ-law (PCMU) mono at 8 kHz.
//   - Frame layout: uint32 little-endian payload length, then payload bytes.
//   - Maximum payload per frame: [MaxFrameBytes] (64 KiB)
//
// Channel values (pcmIn / pcmOut) carry raw PCM payloads only (no length
// prefix). Length-prefix framing applies at stream boundaries (G5) and when
// using [EncodeFrame] / [DecodeFrame].
package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/Origens-Dev/gobeyond/agents"
	"google.golang.org/genai"
)

const (
	// DefaultPCMInSampleRate is Gemini Live microphone input (16-bit LE mono).
	DefaultPCMInSampleRate = 16000
	// DefaultPCMOutSampleRate is Gemini Live speaker output (16-bit LE mono).
	DefaultPCMOutSampleRate = 24000
	// MaxFrameBytes caps one length-prefixed PCM payload.
	MaxFrameBytes = 64 * 1024

	EncodingPCM16LE = "pcm_s16le"
	EncodingPCMU    = "pcmu"
)

// Compile-time pin: keep google.golang.org/genai in go.mod for the Live client
// used by temporalruntime (G4). go-ai has no Live package yet — see
// docs/spikes/gemini-live-g4a.md.
var _ = genai.LiveConnectConfig{}

// Usage is one Gemini Live UsageMetadata snapshot (typically per model turn).
// PromptTokens maps prompt_token_count; CompletionTokens maps
// response_token_count. Hosts emit this as usage.llm.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Model            string
	Backend          string
}

// StartConfig is the transport-neutral input to a Live voice session.
// Instructions and VoiceName should already include any session metadata
// overlay (agents.ResolveInstructions / ResolveVoiceName) before Start.
type StartConfig struct {
	AgentID          string
	SessionID        string
	RunID            string
	CompiledRevision string
	Actor            agents.Actor
	VoiceName        string
	Instructions     string
	Metadata         map[string]string
	PCMInSampleRate  int
	PCMOutSampleRate int
	// OnUsage, when set, is invoked for each Live UsageMetadata the adapter
	// observes. The callback must not block the audio path for long.
	OnUsage func(Usage)
}

// AudioFormat describes the bytes exchanged by one voice session direction.
type AudioFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}

// StartResult describes the formats selected by an adapter before media starts.
type StartResult struct {
	InputFormat  AudioFormat `json:"input_format"`
	OutputFormat AudioFormat `json:"output_format"`
}

// AudioFrame is one model output frame. Control-only frames have empty Data.
type AudioFrame struct {
	Data         []byte
	Interrupted  bool
	TurnComplete bool
}

// Adapter opens a Live voice session bound to PCM channels.
type Adapter interface {
	Start(ctx context.Context, cfg StartConfig, audioIn <-chan []byte, audioOut chan<- AudioFrame) (SessionHandle, StartResult, error)
}

// SessionHandle runs until the session ends or the context is cancelled.
type SessionHandle interface {
	Run(ctx context.Context) error
	Close() error
}

// EncodeFrame prepends a little-endian uint32 length to a PCM payload.
func EncodeFrame(pcm []byte) ([]byte, error) {
	if len(pcm) > MaxFrameBytes {
		return nil, fmt.Errorf("voice PCM frame exceeds MaxFrameBytes (%d > %d)", len(pcm), MaxFrameBytes)
	}
	frame := make([]byte, 4+len(pcm))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(pcm)))
	copy(frame[4:], pcm)
	return frame, nil
}

// DecodeFrame reads one length-prefixed PCM payload from r.
func DecodeFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n > MaxFrameBytes {
		return nil, fmt.Errorf("voice PCM frame length %d exceeds MaxFrameBytes %d", n, MaxFrameBytes)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) && n > 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return payload, nil
}

// NormalizeSampleRates fills zero sample rates with Gemini Live defaults.
func NormalizeSampleRates(cfg *StartConfig) {
	if cfg.PCMInSampleRate <= 0 {
		cfg.PCMInSampleRate = DefaultPCMInSampleRate
	}
	if cfg.PCMOutSampleRate <= 0 {
		cfg.PCMOutSampleRate = DefaultPCMOutSampleRate
	}
}
