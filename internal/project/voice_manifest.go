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
	if tool, handled, err := parsePlaybackVoiceTool(id, fields); handled {
		return tool, err
	}
	mailbox := false
	if expr, present := fields["VoiceMailboxMessage"]; present {
		value, err := voiceLiteral(expr, 0)
		var ok bool
		mailbox, ok = value.(bool)
		if err != nil || !ok {
			return nil, fmt.Errorf("voice mailbox message flag must be a literal boolean")
		}
	}
	controlExpr, hasControl := fields["VoiceControl"]
	readExpr, hasRead := fields["VoiceRemoteRead"]
	readPolicy := hasRead && !isNilVoicePolicy(readExpr)
	actionExpr, hasAction := fields["VoiceAction"]
	action := false
	if hasAction {
		value, err := voiceLiteral(actionExpr, 0)
		if err != nil {
			return nil, fmt.Errorf("voice action flag must be a literal boolean")
		}
		var ok bool
		action, ok = value.(bool)
		if !ok {
			return nil, fmt.Errorf("voice action flag must be a literal boolean")
		}
	}
	if hasControl && hasRead {
		return nil, fmt.Errorf("tool cannot be read and call control")
	}
	var tool voicecontract.Tool
	if mailbox {
		if action || hasControl && !isNilVoicePolicy(controlExpr) || hasRead && !isNilVoicePolicy(readExpr) {
			return nil, fmt.Errorf("mailbox message cannot combine voice policies")
		}
		if expr, present := fields["RequiresApproval"]; present {
			value, err := voiceLiteral(expr, 0)
			if err != nil || value != false {
				return nil, fmt.Errorf("mailbox message cannot require approval")
			}
		}
		tool = voicecontract.Tool{ID: id, Name: id, ExecutionKind: "mailbox_message"}
	} else if action {
		if hasControl && !isNilVoicePolicy(controlExpr) || hasRead && !isNilVoicePolicy(readExpr) {
			return nil, fmt.Errorf("tool cannot combine voice action with read or call control")
		}
		approvalExpr, ok := fields["RequiresApproval"]
		if !ok {
			return nil, fmt.Errorf("voice action requires RequiresApproval: true")
		}
		approval, err := voiceLiteral(approvalExpr, 0)
		approved, isBool := approval.(bool)
		if err != nil || !isBool || !approved {
			return nil, fmt.Errorf("voice action requires RequiresApproval: true")
		}
		tool = voicecontract.Tool{ID: id, Name: id, ExecutionKind: "action", RequiresApproval: true}
	} else {
		if !hasControl && !hasRead {
			return nil, nil
		}
		policyExpr := controlExpr
		if hasRead {
			policyExpr = readExpr
		}
		if isNilVoicePolicy(policyExpr) {
			return nil, nil
		}
		if u, ok := policyExpr.(*ast.UnaryExpr); ok && u.Op == token.AND {
			policyExpr = u.X
		}
		pl, ok := policyExpr.(*ast.CompositeLit)
		if !ok {
			if hasRead {
				return nil, fmt.Errorf("voice remote read policy must be an inline literal")
			}
			return nil, fmt.Errorf("voice control policy must be an inline literal")
		}
		tool = voicecontract.Tool{ID: id, Name: id, ExecutionKind: "call_control"}
		if hasRead {
			tool.ExecutionKind = "read"
		}
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
			if hasRead {
				switch key.Name {
				case "MaxResultBytes":
					n, ok := v.(int64)
					if !ok || n < 1 || n > 4096 {
						return nil, fmt.Errorf("max result bytes must be an int between 1 and 4096")
					}
					tool.MaxResultBytes = int(n)
				default:
					return nil, fmt.Errorf("unsupported voice read policy field %s", key.Name)
				}
				continue
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
	if readPolicy || mailbox {
		if mailbox {
			tool.MaxResultBytes = voicecontract.MaxMailboxMessageResultBytes
		}
		if tool.MaxResultBytes < 1 {
			return nil, fmt.Errorf("voice remote read requires MaxResultBytes")
		}
		output, err := voiceLiteral(fields["OutputSchema"], 0)
		if err != nil {
			return nil, fmt.Errorf("voice output schema: %w", err)
		}
		outRaw, err := json.Marshal(output)
		if err != nil {
			return nil, err
		}
		outRaw, err = voicecontract.CanonicalJSON(outRaw, voicecontract.MaxSchemaBytes)
		if err != nil {
			return nil, err
		}
		tool.OutputSchema = outRaw
		tool.OutputSchemaDigest = voicecontract.Digest(outRaw)
	}
	_, _, err = voicecontract.FreezeManifest(voicecontract.Manifest{Version: voicecontract.Version, Revision: "validation", CompiledRevision: "validation", Tools: []voicecontract.Tool{tool}})
	if err != nil {
		return nil, err
	}
	return &tool, nil
}

func isNilVoicePolicy(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
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
		m := voicecontract.Manifest{BudgetPolicy: d.VoiceBudgetPolicy, Version: voicecontract.Version, Revision: d.Revision, CompiledRevision: d.Revision, Tools: []voicecontract.Tool{}}
		for _, tool := range d.Tools {
			// VoiceControl holds a call-control, remote-read, or approval-gated
			// action tool after parseVoiceTool; ExecutionKind distinguishes them.
			if tool.VoiceControl != nil {
				m.Tools = append(m.Tools, *tool.VoiceControl)
			}
		}
		// Voice-channel agents publish an identity-only manifest even with zero
		// authored VoiceControl/VoiceRemoteRead tools so platform mint can resolve admission.
		if m.BudgetPolicy == "" && len(m.Tools) == 0 && !hasVoiceChannel(d.Slots) {
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
