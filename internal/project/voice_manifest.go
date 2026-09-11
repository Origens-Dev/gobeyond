package project

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"strconv"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

// Remote-control schemas must be visible in authored source. Never execute Go
// code, consult provider metadata, or accept a schema supplied by the caller.
func parseVoiceTool(id string, call *ast.CallExpr) (*voicecontract.Tool, error) {
	if call == nil || len(call.Args) == 0 {
		return nil, nil
	}
	config, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return nil, nil
	}
	fields := map[string]ast.Expr{}
	for _, e := range config.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok {
				fields[k.Name] = kv.Value
			}
		}
	}
	policy, ok := fields["VoiceControl"]
	if !ok {
		return nil, nil
	}
	if ident, ok := policy.(*ast.Ident); ok && ident.Name == "nil" {
		return nil, nil
	}
	if u, ok := policy.(*ast.UnaryExpr); ok && u.Op == token.AND {
		policy = u.X
	}
	pl, ok := policy.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("voice control policy must be an inline literal")
	}
	tool := voicecontract.Tool{ID: id, Name: id}
	for _, e := range pl.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("voice policy requires named fields")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("invalid voice policy field")
		}
		v, err := voiceLiteral(kv.Value, 0)
		if err != nil {
			return nil, err
		}
		switch key.Name {
		case "TerminalOnSuccess":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("terminal policy must be boolean")
			}
			tool.TerminalOnSuccess = b
		case "DestinationClasses":
			a, err := literalStrings(v, "destination class")
			if err != nil {
				return nil, err
			}
			tool.DestinationClasses = a
		case "TargetKinds":
			a, err := literalStrings(v, "target kind")
			if err != nil {
				return nil, err
			}
			tool.TargetKinds = a
		case "InputModes":
			a, err := literalStrings(v, "input mode")
			if err != nil {
				return nil, err
			}
			tool.InputModes = a
		case "HandoffMode":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("handoff mode must be a literal string")
			}
			tool.HandoffMode = s
		case "TerminalBehavior":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("terminal behavior must be a literal string")
			}
			tool.TerminalBehavior = s
		default:
			return nil, fmt.Errorf("unsupported voice policy field %s", key.Name)
		}
	}
	for name, dst := range map[string]*string{"Name": &tool.Name, "Description": &tool.Description} {
		if expr, ok := fields[name]; ok {
			v, err := staticString(expr, "voice tool "+name)
			if err != nil {
				return nil, err
			}
			*dst = v
		}
	}
	schema, err := voiceLiteral(fields["InputSchema"], 0)
	if err != nil {
		return nil, fmt.Errorf("voice input schema: %w", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	raw, err = voicecontract.CanonicalJSON(raw, voicecontract.MaxSchemaBytes)
	if err != nil {
		return nil, err
	}
	tool.InputSchema = raw
	tool.SchemaDigest = voicecontract.Digest(raw)
	_, _, err = voicecontract.FreezeManifest(voicecontract.Manifest{Version: voicecontract.Version, Revision: "validation", CompiledRevision: "validation", Tools: []voicecontract.Tool{tool}})
	if err != nil {
		return nil, err
	}
	return &tool, nil
}

func literalStrings(value any, label string) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s values must be literal strings", label)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid %s", label)
		}
		out = append(out, text)
	}
	return out, nil
}

func voiceLiteral(e ast.Expr, depth int) (any, error) {
	if depth > voicecontract.MaxDepth {
		return nil, fmt.Errorf("voice literal depth exceeded")
	}
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return strconv.Unquote(v.Value)
		}
		if v.Kind == token.INT {
			return strconv.ParseInt(v.Value, 10, 64)
		}
	case *ast.Ident:
		if v.Name == "true" {
			return true, nil
		}
		if v.Name == "false" {
			return false, nil
		}
	case *ast.CompositeLit:
		if len(v.Elts) > 128 {
			return nil, fmt.Errorf("voice literal size exceeded")
		}
		switch v.Type.(type) {
		case *ast.MapType:
			out := map[string]any{}
			for _, e := range v.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					return nil, fmt.Errorf("voice map requires explicit keys")
				}
				key, err := staticString(kv.Key, "voice schema key")
				if err != nil {
					return nil, err
				}
				if _, ok := out[key]; ok {
					return nil, fmt.Errorf("duplicate voice schema key")
				}
				value, err := voiceLiteral(kv.Value, depth+1)
				if err != nil {
					return nil, err
				}
				out[key] = value
			}
			return out, nil
		case *ast.ArrayType:
			out := []any{}
			for _, e := range v.Elts {
				value, err := voiceLiteral(e, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, value)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("voice schema and policy values must be bounded literals")
}

func attachVoiceManifests(manifest *AgentsManifest, definitions []AgentDefinition) error {
	for i, d := range definitions {
		m := voicecontract.Manifest{Version: voicecontract.Version, Revision: d.Revision, CompiledRevision: d.Revision, Tools: []voicecontract.Tool{}}
		for _, tool := range d.Tools {
			if tool.VoiceControl != nil {
				m.Tools = append(m.Tools, *tool.VoiceControl)
			}
		}
		// Voice-channel agents publish an identity-only manifest even with zero
		// authored VoiceControl tools so platform mint can resolve admission.
		if len(m.Tools) == 0 && !hasVoiceChannel(d.Slots) {
			continue
		}
		raw, digest, err := voicecontract.FreezeManifest(m)
		if err != nil {
			return fmt.Errorf("agent %s voice manifest: %w", d.ID, err)
		}
		if err = voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &m); err != nil {
			return err
		}
		manifest.Agents[i].VoiceManifest = &m
		manifest.Agents[i].VoiceManifestDigest = digest
	}
	return nil
}
