package voicecontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"unicode/utf8"
)

const (
	MaxManifestBytes = 32768
	MaxEnvelopeBytes = 16384
	MaxSchemaBytes   = 4096
	MaxTools         = 8
	MaxDepth         = 12
	MaxKeys          = 1024
)

// CanonicalJSON uses sorted UTF-8 keys, compact Go JSON escaping, and integer
// numbers only. It is a deliberately restricted protocol, not RFC 8785.
func CanonicalJSON(raw []byte, limit int) ([]byte, error) {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, errors.New("invalid JSON size or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	keys := 0
	v, err := readValue(d, 0, &keys)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return json.Marshal(v)
}
func readValue(d *json.Decoder, depth int, keys *int) (any, error) {
	if depth > MaxDepth {
		return nil, errors.New("JSON depth exceeded")
	}
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	switch v := t.(type) {
	case json.Delim:
		if v == '{' {
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				s, ok := k.(string)
				if !ok {
					return nil, errors.New("object key required")
				}
				*keys++
				if *keys > MaxKeys {
					return nil, errors.New("JSON key limit exceeded")
				}
				if _, ok = m[s]; ok {
					return nil, errors.New("duplicate JSON key")
				}
				x, e := readValue(d, depth+1, keys)
				if e != nil {
					return nil, e
				}
				m[s] = x
			}
			_, e = d.Token()
			return m, e
		}
		if v == '[' {
			a := []any{}
			for d.More() {
				if len(a) >= MaxKeys {
					return nil, errors.New("JSON array limit exceeded")
				}
				x, e := readValue(d, depth+1, keys)
				if e != nil {
					return nil, e
				}
				a = append(a, x)
			}
			_, e = d.Token()
			return a, e
		}
		return nil, errors.New("unexpected delimiter")
	case json.Number:
		n, e := strconv.ParseInt(string(v), 10, 64)
		if e != nil {
			return nil, errors.New("only signed 64-bit integers allowed")
		}
		return n, nil
	default:
		return t, nil
	}
}
func Digest(canonical []byte) string {
	s := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(s[:])
}
func Decode(raw []byte, limit int, out any) error {
	c, e := CanonicalJSON(raw, limit)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(c))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func schema(v any, depth int) error {
	m, ok := v.(map[string]any)
	if !ok || depth > MaxDepth {
		return errors.New("schema object required")
	}
	allowed := map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true, "maxLength": true, "minLength": true, "enum": true, "anyOf": true, "oneOf": true, "items": true, "maxItems": true}
	for k := range m {
		if !allowed[k] {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	if alternatives, ok := m["anyOf"]; ok {
		a, ok := alternatives.([]any)
		if !ok || len(a) < 2 || len(a) > 4 || len(m) != 1 {
			return errors.New("bounded standalone anyOf required")
		}
		for _, v := range a {
			if e := schema(v, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if alternatives, ok := m["oneOf"]; ok {
		a, ok := alternatives.([]any)
		if !ok || len(a) < 2 || len(a) > 4 || len(m) != 1 {
			return errors.New("bounded standalone oneOf required")
		}
		for _, v := range a {
			if e := schema(v, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	switch m["type"] {
	case "object":
		for k := range m {
			if k != "type" && k != "properties" && k != "required" && k != "additionalProperties" {
				return errors.New("invalid object schema keyword")
			}
		}
		if m["additionalProperties"] != false {
			return errors.New("closed schema object required")
		}
		p, ok := m["properties"].(map[string]any)
		if !ok || len(p) > 16 {
			return errors.New("bounded properties required")
		}
		for _, v := range p {
			if e := schema(v, depth+1); e != nil {
				return e
			}
		}
		r, ok := m["required"].([]any)
		if !ok {
			return errors.New("required array required")
		}
		seen := map[string]bool{}
		for _, v := range r {
			s, ok := v.(string)
			if !ok || seen[s] {
				return errors.New("invalid required property")
			}
			if _, ok = p[s]; !ok {
				return errors.New("unknown required property")
			}
			seen[s] = true
		}
	case "array":
		for k := range m {
			if k != "type" && k != "items" && k != "maxItems" {
				return errors.New("invalid array schema keyword")
			}
		}
		max, ok := m["maxItems"].(float64)
		if !ok || max < 1 || max > 5 || max != float64(int(max)) {
			return errors.New("bounded array required")
		}
		if err := schema(m["items"], depth+1); err != nil {
			return err
		}
	case "string":
		for k := range m {
			if k != "type" && k != "minLength" && k != "maxLength" && k != "enum" {
				return errors.New("invalid string schema keyword")
			}
		}
		max, ok := m["maxLength"].(float64)
		if !ok || max < 1 || max > 500 {
			return errors.New("bounded string required")
		}
		if min, ok := m["minLength"]; ok {
			n, ok := min.(float64)
			if !ok || n < 0 || n > max {
				return errors.New("invalid minimum length")
			}
		}
		if en, ok := m["enum"]; ok {
			a, ok := en.([]any)
			if !ok || len(a) == 0 || len(a) > 16 {
				return errors.New("invalid enum")
			}
			for _, v := range a {
				s, ok := v.(string)
				if !ok || len([]rune(s)) > int(max) {
					return errors.New("invalid enum value")
				}
			}
		}
	default:
		return errors.New("schema permits only objects, strings, and bounded anyOf")
	}
	return nil
}

// FreezeManifest validates the compiled registry value and returns canonical
// bytes and its digest. Every schema digest must match; no caller schemas merge.
func FreezeManifest(m Manifest) ([]byte, string, error) {
	if !validVersion(m.Version) || !identifier(m.Revision) || !identifier(m.CompiledRevision) || len(m.Tools) == 0 || len(m.Tools) > MaxTools {
		return nil, "", errors.New("invalid manifest header")
	}
	m.Tools = append([]Tool(nil), m.Tools...)
	seen := map[string]bool{}
	names := map[string]bool{}
	for i := range m.Tools {
		t := &m.Tools[i]
		if !identifier(t.ID) || !identifier(t.Name) || seen[t.ID] || names[t.Name] || len(t.Description) == 0 || !safeText(t.Description, 512) {
			return nil, "", errors.New("invalid tool identity")
		}
		if m.Version == Version && (t.ID == ToolIDHangUp || t.Name == ToolIDHangUp) {
			return nil, "", errors.New("reserved platform tool")
		}
		seen[t.ID] = true
		names[t.Name] = true
		c, e := CanonicalJSON(t.InputSchema, MaxSchemaBytes)
		if e != nil {
			return nil, "", e
		}
		var s any
		if e = json.Unmarshal(c, &s); e != nil {
			return nil, "", e
		}
		if obj, ok := s.(map[string]any); !ok || (obj["type"] != "object" && obj["oneOf"] == nil && obj["anyOf"] == nil) {
			return nil, "", errors.New("tool root schema must be object")
		}
		if e = schema(s, 0); e != nil {
			return nil, "", e
		}
		if t.SchemaDigest != Digest(c) {
			return nil, "", errors.New("schema digest mismatch")
		}
		t.InputSchema = c
		if t.IsRead() {
			if len(t.DestinationClasses) != 0 || t.TerminalOnSuccess || t.MaxResultBytes < 1 || t.MaxResultBytes > 4096 {
				return nil, "", errors.New("invalid read policy")
			}
			output, e := CanonicalJSON(t.OutputSchema, MaxSchemaBytes)
			if e != nil {
				return nil, "", e
			}
			var definition any
			if e = json.Unmarshal(output, &definition); e != nil {
				return nil, "", e
			}
			if e = schema(definition, 0); e != nil {
				return nil, "", e
			}
			if root, ok := definition.(map[string]any); !ok || root["type"] != "object" {
				return nil, "", errors.New("read output root must be object")
			}
			if Digest(output) != t.OutputSchemaDigest {
				return nil, "", errors.New("read output schema digest mismatch")
			}
			t.OutputSchema = output
		} else {
			if t.ExecutionKind != "" && t.ExecutionKind != "call_control" || len(t.OutputSchema) > 0 || t.OutputSchemaDigest != "" || t.MaxResultBytes != 0 {
				return nil, "", errors.New("invalid execution policy")
			}
			if e := classes(t.DestinationClasses); e != nil {
				return nil, "", e
			}
			if e := targetKinds(t.TargetKinds); e != nil {
				return nil, "", e
			}
			if e := inputModes(t.InputModes); e != nil {
				return nil, "", e
			}
			if t.HandoffMode != "" && t.HandoffMode != "blind" {
				return nil, "", errors.New("invalid handoff mode")
			}
			if t.TerminalBehavior != "" && t.TerminalBehavior != "terminal" {
				return nil, "", errors.New("invalid terminal behavior")
			}
		}
	}
	sort.Slice(m.Tools, func(i, j int) bool { return m.Tools[i].ID < m.Tools[j].ID })
	raw, e := json.Marshal(m)
	if e != nil {
		return nil, "", e
	}
	c, e := CanonicalJSON(raw, MaxManifestBytes)
	if e != nil {
		return nil, "", e
	}
	return c, Digest(c), nil
}
func identifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
func classes(a []string) error {
	if len(a) == 0 || len(a) > 4 {
		return errors.New("invalid destination classes")
	}
	seen := map[string]bool{}
	for _, c := range a {
		if seen[c] || (c != "extension" && c != "managed" && c != "pstn" && c != "assistant" && c != "line" && c != "connected_line") {
			return errors.New("invalid destination class")
		}
		seen[c] = true
	}
	return nil
}

func targetKinds(a []string) error {
	if len(a) > 4 {
		return errors.New("invalid target kinds")
	}
	seen := map[string]bool{}
	for _, kind := range a {
		if seen[kind] || (kind != "assistant" && kind != "line" && kind != "pstn" && kind != "connected_line") {
			return errors.New("invalid target kind")
		}
		seen[kind] = true
	}
	return nil
}

func inputModes(a []string) error {
	if len(a) > 2 {
		return errors.New("invalid input modes")
	}
	seen := map[string]bool{}
	for _, mode := range a {
		if seen[mode] || (mode != "destination_id" && mode != "phone_number") {
			return errors.New("invalid input mode")
		}
		seen[mode] = true
	}
	return nil
}

// Reject isolated UTF-16 surrogate escapes before encoding/json can normalize
// them to replacement characters. Valid surrogate pairs retain their meaning.
func validEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			lo, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || lo < 0xdc00 || lo > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
