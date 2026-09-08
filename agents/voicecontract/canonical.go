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
	MaxDepth         = 8
	MaxKeys          = 128
)

// CanonicalJSON uses sorted UTF-8 keys, compact Go JSON escaping, and integer
// numbers only. It is a deliberately restricted protocol, not RFC 8785.
func CanonicalJSON(raw []byte, limit int) ([]byte, error) {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
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
	allowed := map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true, "maxLength": true, "minLength": true, "enum": true, "anyOf": true}
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
	switch m["type"] {
	case "object":
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
	case "string":
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
	if m.Version != Version || m.Revision == "" || len(m.Revision) > 128 || len(m.Tools) == 0 || len(m.Tools) > MaxTools {
		return nil, "", errors.New("invalid manifest header")
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for i := range m.Tools {
		t := &m.Tools[i]
		if !identifier(t.ID) || !identifier(t.Name) || seen[t.ID] || names[t.Name] || len(t.Description) == 0 || len(t.Description) > 512 {
			return nil, "", errors.New("invalid tool identity")
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
		if e = schema(s, 0); e != nil {
			return nil, "", e
		}
		if t.SchemaDigest != Digest(c) {
			return nil, "", errors.New("schema digest mismatch")
		}
		t.InputSchema = c
		if e := classes(t.DestinationClasses); e != nil {
			return nil, "", e
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
	if len(a) == 0 || len(a) > 3 {
		return errors.New("invalid destination classes")
	}
	seen := map[string]bool{}
	for _, c := range a {
		if seen[c] || (c != "extension" && c != "managed" && c != "pstn") {
			return errors.New("invalid destination class")
		}
		seen[c] = true
	}
	return nil
}
