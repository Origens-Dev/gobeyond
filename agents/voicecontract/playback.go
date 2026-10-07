package voicecontract

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const BudgetPolicyOperatorMailboxPlaybackV1 = "operator_mailbox_playback_v1"
const MaxPlaybackTextBytes = 4096

// PlaybackPolicy is frozen authoring state. Field names select required string
// properties of one closed output object; no JSON paths or model mapping exist.
type PlaybackPolicy struct {
	TextField             string `json:"text_field"`
	MessageIDField        string `json:"message_id_field"`
	CreatedAtField        string `json:"created_at_field"`
	TextDigestField       string `json:"text_digest_field"`
	MessageExpiresAtField string `json:"message_expires_at_field"`
	ExpiresAtField        string `json:"expires_at_field"`
	CompletionToolID      string `json:"completion_tool_id"`
	MaxTextBytes          int    `json:"max_text_bytes"`
	MaxResultBytes        int    `json:"max_result_bytes"`
}

type PlaybackSource struct {
	Text             string
	MessageID        string
	CreatedAt        string
	TextDigest       string
	MessageExpiresAt time.Time
	ExpiresAt        time.Time
}

// ValidatePlaybackTool validates a declaration, not execution authority.
func ValidatePlaybackTool(t Tool) error {
	if t.RequiresApproval || len(t.DestinationClasses) != 0 || len(t.TargetKinds) != 0 || len(t.InputModes) != 0 || t.HandoffMode != "" || t.TerminalBehavior != "" || t.TerminalOnSuccess {
		return errors.New("invalid playback execution policy")
	}
	if t.IsPlaybackCompletion() {
		if t.Playback != nil || len(t.OutputSchema) != 0 || t.OutputSchemaDigest != "" || t.MaxResultBytes != 0 {
			return errors.New("invalid hidden completion policy")
		}
		return nil
	}
	p := t.Playback
	if !t.IsPlayback() || p == nil || !identifier(p.CompletionToolID) || p.CompletionToolID == t.ID || p.MaxTextBytes < 1 || p.MaxTextBytes > MaxPlaybackTextBytes || p.MaxResultBytes < 1 || p.MaxResultBytes > 8192 || t.MaxResultBytes != p.MaxResultBytes {
		return errors.New("invalid playback mapping")
	}
	raw, err := CanonicalJSON(t.OutputSchema, MaxSchemaBytes)
	if err != nil || Digest(raw) != t.OutputSchemaDigest {
		return errors.New("playback output digest mismatch")
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil || root["type"] != "object" || root["additionalProperties"] != false || len(root) != 4 {
		return errors.New("closed playback output required")
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok || len(properties) != 6 {
		return errors.New("exact playback identity properties required")
	}
	required, ok := root["required"].([]any)
	if !ok || len(required) != 6 {
		return errors.New("required playback identity properties missing")
	}
	seen := map[string]bool{}
	for _, v := range required {
		key, ok := v.(string)
		if !ok || seen[key] {
			return errors.New("invalid required playback field")
		}
		seen[key] = true
	}
	fields := []string{p.TextField, p.MessageIDField, p.CreatedAtField, p.TextDigestField, p.MessageExpiresAtField, p.ExpiresAtField}
	mapped := map[string]bool{}
	for i, key := range fields {
		if !identifier(key) || mapped[key] || !seen[key] {
			return errors.New("invalid playback output mapping")
		}
		mapped[key] = true
		prop, ok := properties[key].(map[string]any)
		if !ok || prop["type"] != "string" || len(prop) != 3 || prop["minLength"] != float64(1) {
			return errors.New("bounded playback string required")
		}
		max, ok := prop["maxLength"].(float64)
		bound := 128
		if i == 0 {
			bound = p.MaxTextBytes
		}
		if !ok || max < 1 || max > float64(bound) || max != float64(int(max)) {
			return errors.New("invalid playback string bound")
		}
	}
	return nil
}

func validatePlaybackPairs(m Manifest) error {
	paired := map[string]bool{}
	for _, t := range m.Tools {
		if !t.IsPlayback() {
			continue
		}
		// Authored playback freezes under generic_v1 (X2 product contract). The
		// retired mailbox playback policy name remains accepted only so older
		// fixtures keep their digests during the pin cutover.
		if m.Version != Version || !(IsGenericBudgetPolicy(m.BudgetPolicy) || m.BudgetPolicy == BudgetPolicyOperatorMailboxPlaybackV1) {
			return errors.New("playback requires frozen generic or mailbox playback policy")
		}
		if paired[t.Playback.CompletionToolID] {
			return errors.New("completion must have one playback source")
		}
		paired[t.Playback.CompletionToolID] = true
		found := false
		for _, completion := range m.Tools {
			if completion.ID == t.Playback.CompletionToolID && completion.IsPlaybackCompletion() {
				found = true
			}
		}
		if !found {
			return errors.New("hidden playback completion missing")
		}
	}
	for _, t := range m.Tools {
		if t.IsPlaybackCompletion() && !paired[t.ID] {
			return errors.New("unpaired hidden completion")
		}
	}
	return nil
}

// ResolvePlaybackSource preserves exact bytes, including RFC3339Nano identity.
// Callers must authenticate the result producer and current owner independently.
// It never trims, rewrites, summarizes, or substitutes message text.
func ResolvePlaybackSource(t Tool, raw []byte, now time.Time) (PlaybackSource, error) {
	var out PlaybackSource
	if ValidatePlaybackTool(t) != nil || !t.IsPlayback() || len(raw) > t.MaxResultBytes {
		return out, errors.New("invalid playback source")
	}
	canonical, err := CanonicalJSON(raw, t.MaxResultBytes)
	if err != nil {
		return out, err
	}
	var definition map[string]any
	_ = json.Unmarshal(t.OutputSchema, &definition)
	var value map[string]any
	if json.Unmarshal(canonical, &value) != nil || !matchesSchema(definition, value) {
		return out, errors.New("playback source schema mismatch")
	}
	p := t.Playback
	out.Text = value[p.TextField].(string)
	out.MessageID = value[p.MessageIDField].(string)
	out.CreatedAt = value[p.CreatedAtField].(string)
	out.TextDigest = value[p.TextDigestField].(string)
	if !identifier(out.MessageID) || !utf8.ValidString(out.Text) || len(out.Text) > p.MaxTextBytes || strings.TrimSpace(out.Text) == "" || strings.ContainsRune(out.Text, 0) || out.TextDigest != strings.TrimPrefix(Digest([]byte(out.Text)), "sha256:") {
		return PlaybackSource{}, errors.New("playback immutable identity mismatch")
	}
	at, err := time.Parse(time.RFC3339Nano, out.CreatedAt)
	if err != nil || at.IsZero() || at.After(now) || at.UTC().Format(time.RFC3339Nano) != out.CreatedAt {
		return PlaybackSource{}, errors.New("invalid canonical message timestamp")
	}
	messageExpiry, err := time.Parse(time.RFC3339Nano, value[p.MessageExpiresAtField].(string))
	if err != nil || !messageExpiry.After(now) || messageExpiry.UTC().Format(time.RFC3339Nano) != value[p.MessageExpiresAtField].(string) {
		return PlaybackSource{}, errors.New("expired message")
	}
	out.MessageExpiresAt = messageExpiry
	expiry, err := time.Parse(time.RFC3339Nano, value[p.ExpiresAtField].(string))
	if err != nil || !expiry.After(now) || expiry.After(now.Add(5*time.Minute)) || expiry.After(messageExpiry) || expiry.UTC().Format(time.RFC3339Nano) != value[p.ExpiresAtField].(string) {
		return PlaybackSource{}, errors.New("expired playback source")
	}
	out.ExpiresAt = expiry
	return out, nil
}
