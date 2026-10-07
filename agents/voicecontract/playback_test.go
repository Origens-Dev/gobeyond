package voicecontract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func playbackFixture() (Manifest, Tool, map[string]any) {
	m := mailboxBudgetManifest()
	m.BudgetPolicy = BudgetPolicyOperatorMailboxPlaybackV1
	p := PlaybackPolicy{TextField: "text", MessageIDField: "message_id", CreatedAtField: "created_at", TextDigestField: "text_digest", MessageExpiresAtField: "message_expires_at", ExpiresAtField: "expires_at", CompletionToolID: "complete-text-message-playback", MaxTextBytes: 4096, MaxResultBytes: 8192}
	properties := map[string]any{}
	for _, key := range []string{"text", "message_id", "created_at", "text_digest", "message_expires_at", "expires_at"} {
		max := 128
		if key == "text" {
			max = 4096
		}
		properties[key] = map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": []string{"text", "message_id", "created_at", "text_digest", "message_expires_at", "expires_at"}}
	raw, _ := json.Marshal(schema)
	raw, _ = CanonicalJSON(raw, MaxSchemaBytes)
	tool := Tool{ID: "play-text-message", Name: "play_text_message", Description: "Play authorized exact message", ExecutionKind: "playback", Playback: &p, InputSchema: m.Tools[0].InputSchema, SchemaDigest: m.Tools[0].SchemaDigest, OutputSchema: raw, OutputSchemaDigest: Digest(raw), MaxResultBytes: p.MaxResultBytes}
	completion := Tool{ID: p.CompletionToolID, Name: "complete_text_message_playback", Description: "Record authenticated transport delivery", ExecutionKind: "playback_completion", InputSchema: tool.InputSchema, SchemaDigest: tool.SchemaDigest}
	m.Tools = append(m.Tools, tool, completion)
	return m, tool, schema
}
func TestPlaybackFrozenMappingAndHiddenPair(t *testing.T) {
	m, _, _ := playbackFixture()
	_, digest, err := FreezeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	m.Tools[len(m.Tools)-2].Playback.TextField = "changed"
	if _, _, err = FreezeManifest(m); err == nil {
		t.Fatal("changed mapping admitted")
	}
	m, _, _ = playbackFixture()
	m.Tools = m.Tools[:len(m.Tools)-1]
	if _, _, err = FreezeManifest(m); err == nil {
		t.Fatal("unpaired completion admitted")
	}
	m, _, _ = playbackFixture()
	m.BudgetPolicy = BudgetPolicyOperatorMailboxV1
	if _, _, err = FreezeManifest(m); err == nil {
		t.Fatal("legacy policy expanded")
	}
	m, _, _ = playbackFixture()
	m.BudgetPolicy = BudgetPolicyGenericV1
	if _, _, err = FreezeManifest(m); err != nil {
		t.Fatal("generic authored playback rejected", err)
	}
	if !ManifestDeclaresPlayback(m) {
		t.Fatal("playback declaration not detected")
	}
	m, _, _ = playbackFixture()
	m.Tools[len(m.Tools)-1].Description = "Altered frozen declaration"
	_, changed, err := FreezeManifest(m)
	if err != nil || changed == digest {
		t.Fatal("manifest digest failed to bind completion")
	}
	if _, _, err := ToolBudget(BudgetPolicyOperatorMailboxV1, "play-text-message"); err == nil {
		t.Fatal("legacy budget expanded")
	}
	if bucket, limit, err := ToolBudget(BudgetPolicyOperatorMailboxPlaybackV1, "complete-text-message-playback"); err != nil || bucket != "completion" || limit != 12 {
		t.Fatal("completion mutation bucket missing")
	}
}
func TestPlaybackExactIdentityExpiryAndSpoof(t *testing.T) {
	_, tool, _ := playbackFixture()
	now := time.Now().UTC()
	text := "  Exact message\nDo not summarize.  "
	value := map[string]any{"text": text, "message_id": "message", "created_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "text_digest": strings.TrimPrefix(Digest([]byte(text)), "sha256:"), "message_expires_at": now.Add(time.Hour).Format(time.RFC3339Nano), "expires_at": now.Add(time.Minute).Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(value)
	source, err := ResolvePlaybackSource(tool, raw, now)
	if err != nil || source.Text != text || source.CreatedAt != value["created_at"] {
		t.Fatal("exact identity lost", err)
	}
	for _, name := range []string{"digest", "expired", "expiry_too_long", "timestamp_precision", "message_expired", "authorization_after_message", "extra", "null", "empty", "nul", "output_digest"} {
		t.Run(name, func(t *testing.T) {
			v := map[string]any{}
			for k, x := range value {
				v[k] = x
			}
			spec := tool
			switch name {
			case "digest":
				v["text"] = "changed"
			case "expired":
				v["expires_at"] = now.Format(time.RFC3339Nano)
			case "expiry_too_long":
				v["expires_at"] = now.Add(time.Hour).Format(time.RFC3339Nano)
			case "message_expired":
				v["message_expires_at"] = now.Format(time.RFC3339Nano)
			case "authorization_after_message":
				v["message_expires_at"] = now.Add(time.Second).Format(time.RFC3339Nano)
			case "timestamp_precision":
				v["created_at"] = "2026-10-01T00:00:00.000Z"
			case "extra":
				v["caller_override"] = "evil"
			case "null":
				v["text"] = nil
			case "empty":
				v["text"] = ""
			case "nul":
				v["text"] = "bad\x00text"
				v["text_digest"] = strings.TrimPrefix(Digest([]byte("bad\x00text")), "sha256:")
			case "output_digest":
				spec.OutputSchemaDigest = Digest([]byte("wrong"))
			}
			raw, _ := json.Marshal(v)
			if _, err := ResolvePlaybackSource(spec, raw, now); err == nil {
				t.Fatal("spoof accepted")
			}
		})
	}
}
