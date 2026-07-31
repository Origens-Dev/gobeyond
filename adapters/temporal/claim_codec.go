package temporal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
)

const (
	// EnvClaimDEK is the sealed env-scoped AES-256 DEK (base64.RawURLEncoding)
	// used to decrypt claim-check Temporal payloads (ADR 010).
	EnvClaimDEK = "GOBEYOND_CLAIM_DEK"
	// EnvClaimMode selects codec behavior. Empty / "local" → noop.
	// "hosted" / "preview" require EnvClaimDEK.
	EnvClaimMode = "GOBEYOND_CLAIM_MODE"

	claimAADSchema     = "gobeyond.workflow-claim.v1"
	claimCodecVersion  = 1
	claimRefEncoding   = "json/origens-claim-ref+v1"
	valueCipherVersion = 2
)

// claimRef matches gobeyond-internal/packages/claimcodec.ClaimRef wire shape.
type claimRef struct {
	Version   int    `json:"v"`
	BucketKey string `json:"key"`
	Digest    string `json:"digest"`
	ULID      string `json:"ulid"`
	Body      string `json:"body,omitempty"`
}

type valueCiphertext struct {
	Version    int    `json:"Version"`
	Nonce      []byte `json:"Nonce"`
	Ciphertext []byte `json:"Ciphertext"`
}

// claimPayloadCodec decodes hosted claim-check Temporal payloads so authors
// see plaintext types again. Encode is currently a pass-through; the hosted
// API claim-checks inputs before ExecuteWorkflow.
type claimPayloadCodec struct {
	dek []byte
}

func claimCodecFromEnv() (converter.PayloadCodec, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvClaimMode)))
	dekB64 := strings.TrimSpace(os.Getenv(EnvClaimDEK))
	if mode == "" || mode == "local" {
		if dekB64 == "" {
			return nil, nil
		}
		// DEK sealed without explicit mode → hosted decode.
		mode = "hosted"
	}
	if mode != "hosted" && mode != "preview" {
		return nil, fmt.Errorf("temporal adapter: unknown %s %q", EnvClaimMode, mode)
	}
	if dekB64 == "" {
		return nil, fmt.Errorf("temporal adapter: %s required when claim mode is %s", EnvClaimDEK, mode)
	}
	dek, err := base64.RawURLEncoding.DecodeString(dekB64)
	if err != nil {
		dek, err = base64.StdEncoding.DecodeString(dekB64)
	}
	if err != nil {
		return nil, fmt.Errorf("temporal adapter: decode %s: %w", EnvClaimDEK, err)
	}
	if len(dek) != 32 {
		return nil, fmt.Errorf("temporal adapter: %s must be 32 bytes", EnvClaimDEK)
	}
	return &claimPayloadCodec{dek: append([]byte(nil), dek...)}, nil
}

func (c *claimPayloadCodec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	// Hosted API encodes inputs; worker-side output claim-check is a follow-up.
	return payloads, nil
}

func (c *claimPayloadCodec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	if c == nil || len(c.dek) != 32 {
		return payloads, nil
	}
	out := make([]*commonpb.Payload, 0, len(payloads))
	for _, p := range payloads {
		if p == nil {
			continue
		}
		if !isClaimRefPayload(p) {
			out = append(out, p)
			continue
		}
		expanded, err := c.decodeClaimPayload(p)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
	}
	return out, nil
}

func isClaimRefPayload(p *commonpb.Payload) bool {
	if p == nil || len(p.Data) == 0 {
		return false
	}
	if enc := string(p.Metadata["encoding"]); enc == claimRefEncoding {
		return true
	}
	var ref claimRef
	if err := json.Unmarshal(p.Data, &ref); err != nil {
		return false
	}
	return ref.Version == claimCodecVersion && ref.BucketKey != "" && ref.ULID != "" && ref.Digest != ""
}

func (c *claimPayloadCodec) decodeClaimPayload(p *commonpb.Payload) ([]*commonpb.Payload, error) {
	var ref claimRef
	if err := json.Unmarshal(p.Data, &ref); err != nil {
		return nil, fmt.Errorf("claim ref: %w", err)
	}
	if ref.Body == "" {
		return nil, fmt.Errorf("claim ref %s missing inline body (worker cannot fetch S3)", ref.BucketKey)
	}
	body, err := base64.RawURLEncoding.DecodeString(ref.Body)
	if err != nil {
		return nil, fmt.Errorf("claim body: %w", err)
	}
	id, err := parseClaimIdentity(ref.BucketKey)
	if err != nil {
		return nil, err
	}
	if ref.ULID != "" {
		id.ulid = ref.ULID
	}
	var encrypted valueCiphertext
	if err := json.Unmarshal(body, &encrypted); err != nil {
		return nil, fmt.Errorf("claim ciphertext: %w", err)
	}
	plaintext, err := decryptClaimValue(c.dek, encrypted, claimAAD(id))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(plaintext)
	if ref.Digest != "" && ref.Digest != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("claim digest mismatch")
	}
	return plaintextToPayloads(plaintext)
}

type claimIdentity struct {
	org, project, env, workflowID, runID, ulid string
}

func parseClaimIdentity(key string) (claimIdentity, error) {
	parts := strings.Split(strings.TrimSpace(key), "/")
	if len(parts) != 7 || parts[3] != "claims" {
		return claimIdentity{}, fmt.Errorf("invalid claim object key %q", key)
	}
	id := claimIdentity{
		org: parts[0], project: parts[1], env: parts[2],
		workflowID: parts[4], runID: parts[5], ulid: parts[6],
	}
	if id.org == "" || id.project == "" || id.env == "" ||
		id.workflowID == "" || id.runID == "" || id.ulid == "" {
		return claimIdentity{}, fmt.Errorf("incomplete claim identity in key %q", key)
	}
	return id, nil
}

func claimAAD(id claimIdentity) []byte {
	return []byte(claimAADSchema + "\x00" +
		id.org + "\x00" +
		id.project + "\x00" +
		id.env + "\x00" +
		id.workflowID + "\x00" +
		id.runID + "\x00" +
		id.ulid + "\x00" +
		fmt.Sprintf("%d", claimCodecVersion))
}

func decryptClaimValue(key []byte, encrypted valueCiphertext, aad []byte) ([]byte, error) {
	if encrypted.Version != valueCipherVersion {
		return nil, fmt.Errorf("unsupported claim cipher version %d", encrypted.Version)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("claim DEK must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt claim: %w", err)
	}
	return plaintext, nil
}

// plaintextToPayloads expands claim plaintext (JSON args array or single
// value) into Temporal payloads matching the author's workflow signature.
func plaintextToPayloads(plaintext []byte) ([]*commonpb.Payload, error) {
	raw := strings.TrimSpace(string(plaintext))
	if raw == "" || raw == "null" {
		return nil, nil
	}
	dc := converter.GetDefaultDataConverter()
	if raw[0] == '[' {
		var elems []json.RawMessage
		if err := json.Unmarshal(plaintext, &elems); err != nil {
			return nil, fmt.Errorf("claim args array: %w", err)
		}
		out := make([]*commonpb.Payload, 0, len(elems))
		for _, el := range elems {
			var v any
			if err := json.Unmarshal(el, &v); err != nil {
				p, err := dc.ToPayload(el)
				if err != nil {
					return nil, err
				}
				out = append(out, p)
				continue
			}
			p, err := dc.ToPayload(v)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
	var v any
	if err := json.Unmarshal(plaintext, &v); err != nil {
		p, err := dc.ToPayload(json.RawMessage(append([]byte(nil), plaintext...)))
		if err != nil {
			return nil, err
		}
		return []*commonpb.Payload{p}, nil
	}
	p, err := dc.ToPayload(v)
	if err != nil {
		return nil, err
	}
	return []*commonpb.Payload{p}, nil
}
