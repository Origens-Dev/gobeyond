package voice_test

import (
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voice"
)

func TestRedactTokenNeverReturnsFullSecret(t *testing.T) {
	token := "tok_live_session_secret_value_xyz"
	got := voice.RedactToken(token)
	if got == token || strings.Contains(got, "session_secret") {
		t.Fatalf("RedactToken leaked secret: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("RedactToken = %q, want denylist marker", got)
	}
	if voice.RedactToken("") != "" || voice.RedactToken("short") != "***" {
		t.Fatalf("edge redaction failed")
	}
}

func TestHostedPCMPathAndEndpointNormalize(t *testing.T) {
	path := voice.HostedPCMPath("tok/a b")
	if path != "/v1/agents/voice/pcm/tok%2Fa%20b" {
		t.Fatalf("HostedPCMPath = %q", path)
	}
	spec := voice.PCMEndpointSpec{}
	spec.Normalize("/run/gobeyond/host/host-report.sock")
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if spec.Transport != voice.TransportUnix || spec.Frame != voice.FrameLengthPrefixedLE || spec.AuthHeader != voice.DefaultAuthHeader {
		t.Fatalf("normalized = %#v", spec)
	}
}
