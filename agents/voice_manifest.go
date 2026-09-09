package agents

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

// VoiceToolPolicy is compile-owned policy, never a model/client schema. The
// manifest always takes its input schema directly from the authored tool.
type VoiceReadPolicy struct{ MaxResultBytes int }

func VoiceRemoteReadPolicy(tool AITool) (VoiceReadPolicy, bool) {
	ns, ok := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	if !ok {
		return VoiceReadPolicy{}, false
	}
	p, ok := ns["voiceRemoteRead"].(VoiceReadPolicy)
	return p, ok
}

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
		read, isRead := VoiceRemoteReadPolicy(t)
		if ok && isRead {
			return m, nil, "", errors.New("tool cannot be read and call control")
		}
		if !ok && !isRead {
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
		spec := voicecontract.Tool{ID: id, Name: name, Description: t.Description, InputSchema: c, SchemaDigest: voicecontract.Digest(c), DestinationClasses: p.DestinationClasses, TerminalOnSuccess: p.TerminalOnSuccess, ExecutionKind: "call_control"}
		if isRead {
			output, e := json.Marshal(t.OutputSchema)
			if e != nil {
				return m, nil, "", e
			}
			output, e = voicecontract.CanonicalJSON(output, voicecontract.MaxSchemaBytes)
			if e != nil {
				return m, nil, "", e
			}
			spec.ExecutionKind = "read"
			spec.OutputSchema = output
			spec.OutputSchemaDigest = voicecontract.Digest(output)
			spec.MaxResultBytes = read.MaxResultBytes
		}
		m.Tools = append(m.Tools, spec)
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
