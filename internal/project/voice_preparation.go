package project

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	VoicePreparationVersion              = 1
	MaxVoicePreparationBytes             = 16 << 20
	MaxVoicePreparationAgents            = 1000
	MaxVoicePreparationInstructionsBytes = 64 << 10
)

// VoicePreparationManifest is a private compiler artifact. Its instructions
// must never be added to routes, public agent metadata, or browser assets.
type VoicePreparationManifest struct {
	Version int                     `json:"version"`
	BuildID string                  `json:"buildId"`
	Agents  []VoicePreparationAgent `json:"agents"`
}
type VoicePreparationAgent struct {
	AgentID             string `json:"agentId"`
	CompiledRevision    string `json:"compiledRevision"`
	VoiceManifestDigest string `json:"voiceManifestDigest"`
	BaseInstructions    string `json:"baseInstructions"`
}

func preparationIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 512 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func preparationManifestDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	raw := s[7:]
	v, e := hex.DecodeString(raw)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == raw
}
func ValidateVoicePreparationManifest(m VoicePreparationManifest) error {
	if m.Version != VoicePreparationVersion || !preparationIdentifier(m.BuildID) || m.Agents == nil || len(m.Agents) > MaxVoicePreparationAgents {
		return errors.New("invalid private voice preparation manifest")
	}
	seen := map[string]bool{}
	for _, a := range m.Agents {
		if !preparationIdentifier(a.AgentID) || !preparationIdentifier(a.CompiledRevision) || !preparationManifestDigest(a.VoiceManifestDigest) || seen[a.AgentID] || !utf8.ValidString(a.BaseInstructions) || strings.TrimSpace(a.BaseInstructions) == "" || len(a.BaseInstructions) > MaxVoicePreparationInstructionsBytes {
			return errors.New("invalid private voice preparation agent")
		}
		seen[a.AgentID] = true
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > MaxVoicePreparationBytes {
		return errors.New("private voice preparation manifest too large")
	}
	return nil
}
func ReadVoicePreparationManifest(raw []byte) (VoicePreparationManifest, error) {
	var m VoicePreparationManifest
	if len(raw) == 0 || len(raw) > MaxVoicePreparationBytes || !utf8.Valid(raw) {
		return m, errors.New("invalid private voice preparation manifest size")
	}
	fields, err := preparationWireFields(raw, map[string]bool{"version": true, "buildId": true, "agents": true})
	if err != nil {
		return m, err
	}
	agentDecoder := json.NewDecoder(bytes.NewReader(fields["agents"]))
	token, err := agentDecoder.Token()
	if err != nil || token != json.Delim('[') {
		return m, errors.New("invalid private voice preparation agents")
	}
	for agentDecoder.More() {
		var agentRaw json.RawMessage
		if err := agentDecoder.Decode(&agentRaw); err != nil {
			return m, err
		}
		if _, err := preparationWireFields(agentRaw, map[string]bool{"agentId": true, "compiledRevision": true, "voiceManifestDigest": true, "baseInstructions": true}); err != nil {
			return m, err
		}
	}
	if token, err := agentDecoder.Token(); err != nil || token != json.Delim(']') {
		return m, errors.New("invalid private voice preparation agents")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return m, errors.New("trailing private voice preparation data")
	}
	if err := ValidateVoicePreparationManifest(m); err != nil {
		return m, err
	}
	return m, nil
}

// compiledVoicePreparation uses the exact finalized IR consumed by generated
// GobeyondDefinition, including its resolved revision and instruction bytes.
func compiledVoicePreparation(definitions []AgentDefinition, published AgentsManifest) (VoicePreparationManifest, error) {
	out := VoicePreparationManifest{Version: VoicePreparationVersion, BuildID: published.BuildID, Agents: []VoicePreparationAgent{}}
	metadata := make(map[string]AgentManifestDefinition, len(published.Agents))
	for _, a := range published.Agents {
		if _, exists := metadata[a.ID]; exists {
			return out, fmt.Errorf("duplicate agent manifest ID %q", a.ID)
		}
		metadata[a.ID] = a
	}
	seen := map[string]bool{}
	for _, d := range definitions {
		if seen[d.ID] {
			return out, fmt.Errorf("duplicate compiled agent ID %q", d.ID)
		}
		seen[d.ID] = true
		if d.Kind != AgentKindAI || !d.Durable || !d.Realtime {
			continue
		}
		a, ok := metadata[d.ID]
		if !ok || a.Revision != d.Revision || a.VoiceManifest == nil || a.VoiceManifestDigest == "" || a.VoiceManifest.CompiledRevision != d.Revision {
			return out, fmt.Errorf("agent %q lacks matching compiled voice identity", d.ID)
		}
		_, digest, err := voicecontract.FreezeManifest(*a.VoiceManifest)
		if err != nil || digest != a.VoiceManifestDigest || a.Kind != AgentKindAI || !a.Durable || !a.Realtime {
			return out, fmt.Errorf("agent %q has inconsistent compiled voice identity", d.ID)
		}
		out.Agents = append(out.Agents, VoicePreparationAgent{AgentID: d.ID, CompiledRevision: d.Revision, VoiceManifestDigest: a.VoiceManifestDigest, BaseInstructions: d.Instructions})
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].AgentID < out.Agents[j].AgentID })
	if err := ValidateVoicePreparationManifest(out); err != nil {
		return out, err
	}
	return out, nil
}

// preparationWireFields rejects duplicate fields and case aliases before Go's
// case-insensitive struct decoder could discard evidence of malformed input.
func preparationWireFields(raw []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	t, e := dec.Token()
	if e != nil || t != json.Delim('{') {
		return nil, errors.New("invalid private voice preparation object")
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		t, e := dec.Token()
		if e != nil {
			return nil, e
		}
		key, ok := t.(string)
		if !ok || !allowed[key] {
			return nil, errors.New("unknown private voice preparation field")
		}
		if _, exists := out[key]; exists {
			return nil, errors.New("duplicate private voice preparation field")
		}
		var value json.RawMessage
		if e := dec.Decode(&value); e != nil {
			return nil, e
		}
		out[key] = value
	}
	if t, e := dec.Token(); e != nil || t != json.Delim('}') {
		return nil, errors.New("invalid private voice preparation object")
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing private voice preparation object")
	}
	if len(out) != len(allowed) {
		return nil, errors.New("missing private voice preparation field")
	}
	return out, nil
}
