package agents

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

// VoiceToolPolicy is compile-owned policy, never a model/client schema. The
// manifest always takes its input schema directly from the authored tool.
type VoiceToolPolicy struct {
	DestinationClasses []string
	TerminalOnSuccess  bool
}

// VoiceControlPolicy accepts only the typed value installed by DefineTool.
// Decoded provider/client metadata maps cannot acquire this capability.
func VoiceControlPolicy(tool AITool) (VoiceToolPolicy, bool) {
	ns, ok := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	if !ok {
		return VoiceToolPolicy{}, false
	}
	p, ok := ns["voiceControl"].(VoiceToolPolicy)
	p.DestinationClasses = append([]string(nil), p.DestinationClasses...)
	return p, ok
}

// CompileVoiceManifest freezes opt-in tools from the compiled definition. The
// generated registration injects AI.Revision before this function is called.
// Unmarked tools (including native search) never enter the remote manifest.
func (d AIDefinition) CompileVoiceManifest() (voicecontract.Manifest, []byte, string, error) {
	m := voicecontract.Manifest{Version: voicecontract.Version, Revision: d.AI.Revision, CompiledRevision: d.AI.Revision}
	for id, t := range d.AI.Tools {
		p, ok := VoiceControlPolicy(t)
		if !ok {
			continue
		}
		name := t.Name
		if name == "" {
			name = id
		}
		raw, e := json.Marshal(t.InputSchema)
		if e != nil {
			return m, nil, "", fmt.Errorf("voice tool %s: %w", id, e)
		}
		c, e := voicecontract.CanonicalJSON(raw, voicecontract.MaxSchemaBytes)
		if e != nil {
			return m, nil, "", e
		}
		m.Tools = append(m.Tools, voicecontract.Tool{ID: id, Name: name, Description: t.Description, InputSchema: c, SchemaDigest: voicecontract.Digest(c), DestinationClasses: p.DestinationClasses, TerminalOnSuccess: p.TerminalOnSuccess})
	}
	if len(m.Tools) == 0 {
		return m, nil, "", nil
	}
	if d.AI.Revision == "" {
		return m, nil, "", errors.New("voice manifest requires compiled agent revision")
	}
	raw, digest, e := voicecontract.FreezeManifest(m)
	if e != nil {
		return m, nil, "", e
	}
	if e = voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &m); e != nil {
		return m, nil, "", e
	}
	return m, raw, digest, nil
}
