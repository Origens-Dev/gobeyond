package voicecontract

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ValidateToolInput validates model arguments against an already resolved
// registry schema and returns the exact canonical bytes used for idempotency.
// It never accepts additional schema vocabulary or returns caller text in errors.
func ValidateToolInput(tool Tool, raw []byte) ([]byte, error) {
	canonicalSchema, err := CanonicalJSON(tool.InputSchema, MaxSchemaBytes)
	if err != nil {
		return nil, err
	}
	if Digest(canonicalSchema) != tool.SchemaDigest {
		return nil, errors.New("schema digest mismatch")
	}
	var definition any
	if err = json.Unmarshal(canonicalSchema, &definition); err != nil {
		return nil, err
	}
	if err = schema(definition, 0); err != nil {
		return nil, err
	}
	canonical, err := CanonicalJSON(raw, MaxSchemaBytes)
	if err != nil {
		return nil, err
	}
	var value any
	if err = json.Unmarshal(canonical, &value); err != nil {
		return nil, err
	}
	root, ok := definition.(map[string]any)
	if !ok || !matchesSchema(root, value) {
		return nil, errors.New("tool input does not match deployed schema")
	}
	return canonical, nil
}
func matchesSchema(s map[string]any, v any) bool {
	if union, ok := s["oneOf"].([]any); ok {
		matches := 0
		for _, variant := range union {
			variantSchema, ok := variant.(map[string]any)
			if ok && matchesSchema(variantSchema, v) {
				matches++
			}
		}
		return matches == 1
	}
	if union, ok := s["anyOf"].([]any); ok {
		for _, variant := range union {
			if variantSchema, ok := variant.(map[string]any); ok && matchesSchema(variantSchema, v) {
				return true
			}
		}
		return false
	}
	switch s["type"] {
	case "object":
		object, ok := v.(map[string]any)
		if !ok {
			return false
		}
		props := s["properties"].(map[string]any)
		for k, value := range object {
			p, ok := props[k]
			if !ok || !matchesSchema(p.(map[string]any), value) {
				return false
			}
		}
		for _, key := range s["required"].([]any) {
			if _, ok := object[key.(string)]; !ok {
				return false
			}
		}
		return true
	case "array":
		values, ok := v.([]any)
		if !ok || float64(len(values)) > s["maxItems"].(float64) {
			return false
		}
		for _, value := range values {
			if !matchesSchema(s["items"].(map[string]any), value) {
				return false
			}
		}
		return true
	case "string":
		value, ok := v.(string)
		if !ok || !utf8.ValidString(value) {
			return false
		}
		length := float64(utf8.RuneCountInString(value))
		if length > s["maxLength"].(float64) {
			return false
		}
		if minimum, ok := s["minLength"].(float64); ok && length < minimum {
			return false
		}
		if choices, ok := s["enum"].([]any); ok {
			for _, choice := range choices {
				if value == choice {
					return true
				}
			}
			return false
		}
		return true
	}
	return false
}

func ValidateToolOutput(tool Tool, raw []byte) ([]byte, error) {
	if !tool.IsRead() || tool.MaxResultBytes < 1 || tool.MaxResultBytes > 4096 || len(raw) > tool.MaxResultBytes {
		return nil, errors.New("read result exceeds policy")
	}
	return ValidateToolInput(Tool{InputSchema: tool.OutputSchema, SchemaDigest: tool.OutputSchemaDigest}, raw)
}
