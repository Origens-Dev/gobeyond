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

func TestEncodeDecodeAudioFrameV3RoundTrip(t *testing.T) {
	want := voice.AudioFrame{Data: []byte{0x01, 0x02, 0x03}, Flush: true, BarrierID: 42}
	encoded, err := voice.EncodeAudioFrameV3(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := voice.DecodeAudioFrameV3(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, want.Data) || !got.Flush || got.BarrierID != want.BarrierID {
		t.Fatalf("frame = %+v, want %+v", got, want)
	}
}

func TestEncodeAudioFrameRejectsV3ControlOnV2(t *testing.T) {
	if _, err := voice.EncodeAudioFrame(voice.AudioFrame{Flush: true, BarrierID: 1}); err == nil {
		t.Fatal("expected v2 flush barrier error")
	}
}

func TestPCMEndpointAcceptsV3Framing(t *testing.T) {
	spec := voice.PCMEndpointSpec{
		Transport: voice.TransportUnix,
		Path:      "/run/gobeyond/host/host-report.sock",
		Frame:     voice.FrameLengthPrefixedLEV3,
	}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeSampleRates(t *testing.T) {
	cfg := voice.StartConfig{}
	voice.NormalizeSampleRates(&cfg)
	if cfg.PCMInSampleRate != voice.DefaultPCMInSampleRate || cfg.PCMOutSampleRate != voice.DefaultPCMOutSampleRate {
		t.Fatalf("rates = %d/%d", cfg.PCMInSampleRate, cfg.PCMOutSampleRate)
	}
}
