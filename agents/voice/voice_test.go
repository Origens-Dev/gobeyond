package voice_test

import (
	"bytes"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voice"
)

func TestEncodeDecodeFrameRoundTrip(t *testing.T) {
	pcm := []byte{0x01, 0x00, 0xff, 0x7f}
	frame, err := voice.EncodeFrame(pcm)
	if err != nil {
		t.Fatal(err)
	}
	got, err := voice.DecodeFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("payload = %v, want %v", got, pcm)
	}
}

func TestEncodeFrameRejectsOversizedPayload(t *testing.T) {
	pcm := make([]byte, voice.MaxFrameBytes+1)
	if _, err := voice.EncodeFrame(pcm); err == nil {
		t.Fatal("expected oversized frame error")
	}
}

func TestNormalizeSampleRates(t *testing.T) {
	cfg := voice.StartConfig{}
	voice.NormalizeSampleRates(&cfg)
	if cfg.PCMInSampleRate != voice.DefaultPCMInSampleRate || cfg.PCMOutSampleRate != voice.DefaultPCMOutSampleRate {
		t.Fatalf("rates = %d/%d", cfg.PCMInSampleRate, cfg.PCMOutSampleRate)
	}
}
