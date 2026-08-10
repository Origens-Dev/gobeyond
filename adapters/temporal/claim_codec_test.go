package temporal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
)

func TestClaimPayloadCodecRoundTripArgs(t *testing.T) {
	dek := bytesRepeat(7, 32)
	id := claimIdentity{
		org: "org", project: "proj", env: "env",
		workflowID: "gobeyond-agent-run/session-1/run-1", runID: "pending", ulid: "ulid-1",
	}
	plaintext := []byte(`["hello from studiofallon-go"]`)
	body := mustEncryptClaim(t, dek, plaintext, claimAAD(id))
	ref := claimRef{
		Version:   claimCodecVersion,
		BucketKey: "org/proj/env/claims/gobeyond-agent-run%2Fsession-1%2Frun-1/pending/ulid-1",
		Digest:    sha256Hex(plaintext),
		ULID:      id.ulid,
		Body:      base64.RawURLEncoding.EncodeToString(body),
	}
	refJSON, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	codec := &claimPayloadCodec{dek: dek}
	decoded, err := codec.Decode([]*commonpb.Payload{{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     refJSON,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 {
		t.Fatalf("want 1 payload, got %d", len(decoded))
	}
	var got string
	if err := json.Unmarshal(decoded[0].Data, &got); err != nil {
		t.Fatalf("payload data: %s err=%v", decoded[0].Data, err)
	}
	if got != "hello from studiofallon-go" {
		t.Fatalf("got %q", got)
	}
}

func TestClaimPayloadCodecPassthrough(t *testing.T) {
	codec := &claimPayloadCodec{dek: bytesRepeat(1, 32)}
	in := []*commonpb.Payload{{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte(`"plain"`),
	}}
	out, err := codec.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(out[0].Data) != `"plain"` {
		t.Fatalf("got %s", out[0].Data)
	}
}

func TestParseClaimIdentityUnescapesOpaqueSegments(t *testing.T) {
	key := "org%2Fone/project%20one/env%25one/claims/gobeyond-agent-run%2Fsession-1%2Frun-1/pending%2Frun/ulid%2F1"
	id, err := parseClaimIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	if id.org != "org/one" || id.project != "project one" || id.env != "env%one" ||
		id.workflowID != "gobeyond-agent-run/session-1/run-1" || id.runID != "pending/run" || id.ulid != "ulid/1" {
		t.Fatalf("unexpected identity: %+v", id)
	}
}

func TestClaimCodecFromEnvLocalNoop(t *testing.T) {
	t.Setenv(EnvClaimMode, "local")
	t.Setenv(EnvClaimDEK, "")
	codec, err := claimCodecFromEnv()
	if err != nil || codec != nil {
		t.Fatalf("want nil noop, got %v %v", codec, err)
	}
}

func TestClaimCodecFromEnvHosted(t *testing.T) {
	dek := bytesRepeat(9, 32)
	t.Setenv(EnvClaimMode, "hosted")
	t.Setenv(EnvClaimDEK, base64.RawURLEncoding.EncodeToString(dek))
	codec, err := claimCodecFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if codec == nil {
		t.Fatal("expected codec")
	}
}

func mustEncryptClaim(t *testing.T, key, plaintext, aad []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	ct := valueCiphertext{
		Version:    valueCipherVersion,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, aad),
	}
	body, err := json.Marshal(ct)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
