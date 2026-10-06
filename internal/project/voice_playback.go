package project

import (
	"encoding/json"
	"fmt"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go/ast"
	"go/token"
)

func parsePlaybackVoiceTool(id string, fields map[string]ast.Expr) (*voicecontract.Tool, bool, error) {
	expr, hasPlayback := fields["VoicePlayback"]
	completionExpr, hasCompletion := fields["VoicePlaybackCompletion"]
	completion := false
	if hasCompletion {
		value, err := voiceLiteral(completionExpr, 0)
		b, ok := value.(bool)
		if err != nil || !ok {
			return nil, true, fmt.Errorf("playback completion must be a literal boolean")
		}
		completion = b
	}
	playback := hasPlayback && !isNilVoicePolicy(expr)
	if !playback && !completion {
		return nil, false, nil
	}
	for _, key := range []string{"VoiceControl", "VoiceRemoteRead", "VoiceAction", "VoiceMailboxMessage"} {
		if _, ok := fields[key]; ok {
			return nil, true, fmt.Errorf("playback cannot combine execution policies")
		}
	}
	if playback && completion {
		return nil, true, fmt.Errorf("playback and completion cannot combine")
	}
	t := voicecontract.Tool{ID: id, Name: id, ExecutionKind: "playback_completion"}
	for key, dst := range map[string]*string{"Name": &t.Name, "Description": &t.Description} {
		if field, ok := fields[key]; ok {
			v, err := staticString(field, "playback "+key)
			if err != nil {
				return nil, true, err
			}
			*dst = v
		}
	}
	if field, ok := fields["RequiresApproval"]; ok {
		value, err := voiceLiteral(field, 0)
		b, valid := value.(bool)
		if err != nil || !valid || b {
			return nil, true, fmt.Errorf("playback uses authenticated completion, not model approval")
		}
	}
	schemaValue, err := voiceLiteral(fields["InputSchema"], 0)
	if err != nil {
		return nil, true, err
	}
	raw, _ := json.Marshal(schemaValue)
	raw, err = voicecontract.CanonicalJSON(raw, voicecontract.MaxSchemaBytes)
	if err != nil {
		return nil, true, err
	}
	t.InputSchema = raw
	t.SchemaDigest = voicecontract.Digest(raw)
	if playback {
		if u, ok := expr.(*ast.UnaryExpr); ok && u.Op == token.AND {
			expr = u.X
		}
		lit, ok := expr.(*ast.CompositeLit)
		if !ok {
			return nil, true, fmt.Errorf("playback policy must be an inline literal")
		}
		p := voicecontract.PlaybackPolicy{}
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				return nil, true, fmt.Errorf("playback fields must be named")
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return nil, true, fmt.Errorf("invalid playback field")
			}
			switch key.Name {
			case "MaxTextBytes", "MaxResultBytes":
				value, err := voiceLiteral(kv.Value, 0)
				n, ok := value.(int64)
				if err != nil || !ok || n < 1 || n > 8192 {
					return nil, true, fmt.Errorf("invalid playback bound")
				}
				if key.Name == "MaxTextBytes" {
					p.MaxTextBytes = int(n)
				} else {
					p.MaxResultBytes = int(n)
				}
			default:
				targets := map[string]*string{"TextField": &p.TextField, "MessageIDField": &p.MessageIDField, "CreatedAtField": &p.CreatedAtField, "TextDigestField": &p.TextDigestField, "MessageExpiresAtField": &p.MessageExpiresAtField, "ExpiresAtField": &p.ExpiresAtField, "CompletionToolID": &p.CompletionToolID}
				dst := targets[key.Name]
				if dst == nil {
					return nil, true, fmt.Errorf("unknown playback mapping field")
				}
				value, err := staticString(kv.Value, "playback field")
				if err != nil {
					return nil, true, err
				}
				*dst = value
			}
		}
		t.ExecutionKind = "playback"
		t.Playback = &p
		t.MaxResultBytes = p.MaxResultBytes
		value, err := voiceLiteral(fields["OutputSchema"], 0)
		if err != nil {
			return nil, true, err
		}
		raw, _ := json.Marshal(value)
		raw, err = voicecontract.CanonicalJSON(raw, voicecontract.MaxSchemaBytes)
		if err != nil {
			return nil, true, err
		}
		t.OutputSchema = raw
		t.OutputSchemaDigest = voicecontract.Digest(raw)
	}
	// Validate the ordinary input schema separately; paired declarations are
	// validated together by the final frozen manifest, never by tenant values.
	probe := voicecontract.Tool{ID: t.ID, Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, SchemaDigest: t.SchemaDigest, DestinationClasses: []string{"extension"}}
	if _, _, err = voicecontract.FreezeManifest(voicecontract.Manifest{Version: voicecontract.Version, Revision: "validation", CompiledRevision: "validation", Tools: []voicecontract.Tool{probe}}); err != nil {
		return nil, true, err
	}
	if err = voicecontract.ValidatePlaybackTool(t); err != nil {
		return nil, true, err
	}
	return &t, true, nil
}
