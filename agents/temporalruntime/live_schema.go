package temporalruntime

import (
	"strings"

	"google.golang.org/genai"
)

// liveParametersSchema converts authored JSON Schema into Gemini Live's typed
// OpenAPI Schema Object. Live ignores ParametersJsonSchema (empty tool args),
// and rejects several JSON Schema keywords such as oneOf on the wire.
func liveParametersSchema(raw any) *genai.Schema {
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return &genai.Schema{Type: genai.TypeObject}
	}
	if schema := liveSchemaFromMap(m); schema != nil {
		return schema
	}
	return &genai.Schema{Type: genai.TypeObject}
}

func liveSchemaFromMap(m map[string]any) *genai.Schema {
	if alts, ok := m["oneOf"].([]any); ok {
		return liveFlattenObjectUnion(alts)
	}
	if alts, ok := m["anyOf"].([]any); ok {
		return liveFlattenObjectUnion(alts)
	}
	schema := &genai.Schema{Type: liveSchemaType(m["type"])}
	if schema.Type == "" {
		schema.Type = genai.TypeObject
	}
	if desc, ok := m["description"].(string); ok {
		schema.Description = desc
	}
	if props, ok := m["properties"].(map[string]any); ok {
		schema.Properties = make(map[string]*genai.Schema, len(props))
		for name, raw := range props {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			schema.Properties[name] = liveSchemaFromMap(child)
		}
	}
	if required, ok := m["required"].([]any); ok {
		for _, item := range required {
			name, ok := item.(string)
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if name != "" {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	if items, ok := m["items"].(map[string]any); ok {
		schema.Items = liveSchemaFromMap(items)
	}
	if enums, ok := m["enum"].([]any); ok {
		for _, item := range enums {
			if s, ok := item.(string); ok {
				schema.Enum = append(schema.Enum, s)
			}
		}
	}
	// Omit minLength/maxLength/minItems/maxItems on the Live wire. Hosted Live
	// has rejected or ignored several JSON Schema constraints; Maglev already
	// validates authored bounds after the model call.
	return schema
}

// liveFlattenObjectUnion rewrites standalone oneOf/anyOf object variants into a
// single object with optional properties. Live does not accept oneOf, and the
// Maglev dial_contact handler already enforces exclusive destination_id /
// phone_number selection after the model call.
func liveFlattenObjectUnion(alts []any) *genai.Schema {
	minProps := int64(1)
	out := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{},
		// Flattened unions cannot keep per-variant required. Require at least
		// one property so the model does not emit empty FunctionCalls.
		MinProperties: &minProps,
		Description:   "Provide exactly one of the listed properties.",
	}
	for _, raw := range alts {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if liveSchemaType(m["type"]) != genai.TypeObject && m["properties"] == nil {
			continue
		}
		props, ok := m["properties"].(map[string]any)
		if !ok {
			continue
		}
		for name, childRaw := range props {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, exists := out.Properties[name]; exists {
				continue
			}
			child, ok := childRaw.(map[string]any)
			if !ok {
				continue
			}
			prop := liveSchemaFromMap(child)
			if prop != nil && strings.TrimSpace(prop.Description) == "" {
				prop.Description = "Exclusive alternative; omit the other properties."
			}
			out.Properties[name] = prop
		}
	}
	if len(out.Properties) == 0 {
		return &genai.Schema{Type: genai.TypeObject}
	}
	return out
}

func liveSchemaType(raw any) genai.Type {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	case "null":
		return genai.TypeNULL
	default:
		return ""
	}
}
