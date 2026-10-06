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
	TargetKinds        []string
	InputModes         []string
	HandoffMode        string
	TerminalBehavior   string
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
	p.TargetKinds = append([]string(nil), p.TargetKinds...)
	p.InputModes = append([]string(nil), p.InputModes...)
	return p, ok
}

// voiceWriteMarker is installed only by DefineTool. Decoded provider/client
// metadata maps cannot acquire write dispatch by setting a boolean flag.
type voiceWriteMarker struct{}

// VoiceWritePolicy identifies an authored app mutation selected for authenticated
// voice dispatch. The typed marker can only be emitted by DefineTool.
func VoiceWritePolicy(tool AITool) bool {
	ns, ok := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	if !ok {
		return false
	}
	_, enabled := ns["voiceWrite"].(voiceWriteMarker)
	return enabled
}

// CompileVoiceManifest freezes opt-in tools from the compiled definition. The
// generated registration injects AI.Revision before this function is called.
// Unmarked tools (including native search) never enter the remote manifest.
// This manifest authorizes remote execution; it does not replace the session's
// authored-tool selection. Native provider capabilities keep their own selection.
// Voice-channel agents with zero VoiceControl tools still freeze an
// identity-only manifest for platform admission.
func (d AIDefinition) CompileVoiceManifest() (voicecontract.Manifest, []byte, string, error) {
	m := voicecontract.Manifest{BudgetPolicy: d.AI.VoiceBudgetPolicy, Version: voicecontract.Version, Revision: d.AI.Revision, CompiledRevision: d.AI.Revision, Tools: []voicecontract.Tool{}}
	for id, t := range d.AI.Tools {
		p, ok := VoiceControlPolicy(t)
		read, isRead := VoiceRemoteReadPolicy(t)
		write := VoiceWritePolicy(t)
		playback, isPlayback := VoicePlaybackPolicyFor(t)
		completion := VoicePlaybackCompletionPolicy(t)
		count := 0
		for _, present := range []bool{ok, isRead, write, isPlayback, completion} {
			if present {
				count++
			}
		}
		if count > 1 {
			return m, nil, "", errors.New("voice tool cannot have multiple execution policies")
		}
		if (isPlayback || completion) && (t.RequiresApproval || t.NeedsApproval != nil) {
			return m, nil, "", errors.New("playback requires authenticated receipt policy, not provider approval")
		}
		if !ok && !isRead && !write && !isPlayback && !completion {
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
		spec := voicecontract.Tool{ID: id, Name: name, Description: t.Description, InputSchema: c, SchemaDigest: voicecontract.Digest(c), DestinationClasses: p.DestinationClasses, TargetKinds: p.TargetKinds, InputModes: p.InputModes, HandoffMode: p.HandoffMode, TerminalBehavior: p.TerminalBehavior, TerminalOnSuccess: p.TerminalOnSuccess, ExecutionKind: "call_control"}
		if isPlayback || completion {
			spec.ExecutionKind = "playback_completion"
			if isPlayback {
				spec.ExecutionKind = "playback"
				spec.Playback = &playback
				output, e := json.Marshal(t.OutputSchema)
				if e != nil {
					return m, nil, "", e
				}
				output, e = voicecontract.CanonicalJSON(output, voicecontract.MaxSchemaBytes)
				if e != nil {
					return m, nil, "", e
				}
				spec.OutputSchema = output
				spec.OutputSchemaDigest = voicecontract.Digest(output)
				spec.MaxResultBytes = playback.MaxResultBytes
			}
		} else if write {
			if t.OutputSchema == nil {
				return m, nil, "", fmt.Errorf("voice write %s requires a closed output schema", id)
			}
			output, e := json.Marshal(t.OutputSchema)
			if e != nil {
				return m, nil, "", e
			}
			output, e = voicecontract.CanonicalJSON(output, voicecontract.MaxSchemaBytes)
			if e != nil {
				return m, nil, "", e
			}
			spec.ExecutionKind = "write"
			spec.RequiresApproval = t.RequiresApproval
			spec.DestinationClasses = []string{}
			spec.TerminalOnSuccess = false
			spec.OutputSchema = output
			spec.OutputSchemaDigest = voicecontract.Digest(output)
			spec.MaxResultBytes = 4096
		} else if isRead {
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
	if m.BudgetPolicy == "" && len(m.Tools) == 0 && !definitionHasVoiceChannel(d.Slots) {
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

func definitionHasVoiceChannel(slots Slots) bool {
	for _, channel := range slots.Channels {
		if channel.ID == "voice" {
			return true
		}
	}
	return false
}
