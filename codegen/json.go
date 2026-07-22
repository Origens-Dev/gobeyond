package codegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"time"
)

const maxJavaScriptInteger = 1<<53 - 1

// DecodeJSON validates one JSON value against a generated value contract and
// then decodes it into target. Validation is schema-driven rather than based
// on Go reflection, so missing required properties and invalid enum values do
// not collapse into otherwise-valid Go zero values.
func DecodeJSON(schema Value, data []byte, target any) error {
	if err := validateValue(schema, "$", false); err != nil {
		return err
	}
	if err := ValidateJSON(schema, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode validated value: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

// ValidateJSON validates exactly one JSON value against schema. Object
// properties are closed: unknown properties are rejected at every depth.
func ValidateJSON(schema Value, data []byte) error {
	if err := validateRawJSON(schema, json.RawMessage(data), "$", false); err != nil {
		return fmt.Errorf("value contract validation failed: %w", err)
	}
	return nil
}

// ValidateEncodedValue marshals a typed Go value and validates the JSON that
// would be delivered to the browser against schema.
func ValidateEncodedValue(schema Value, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode action output: %w", err)
	}
	return ValidateJSON(schema, encoded)
}

func validateRawJSON(schema Value, raw json.RawMessage, location string, objectField bool) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("%s must contain one JSON value", location)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		if schema.Nullable {
			return nil
		}
		return fmt.Errorf("%s must not be null", location)
	}
	if schema.Optional && !objectField {
		return fmt.Errorf("%s optional is only valid for an object property", location)
	}

	switch schema.Kind {
	case KindString, KindSafeHTML:
		var value string
		return decodeOneJSON(raw, &value, location)
	case KindDateTime:
		var value string
		if err := decodeOneJSON(raw, &value, location); err != nil {
			return err
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("%s must be an RFC 3339 timestamp", location)
		}
		return nil
	case KindBytes:
		var value string
		if err := decodeOneJSON(raw, &value, location); err != nil {
			return err
		}
		if _, err := base64.StdEncoding.DecodeString(value); err != nil {
			return fmt.Errorf("%s must be base64: %w", location, err)
		}
		return nil
	case KindBoolean:
		var value bool
		return decodeOneJSON(raw, &value, location)
	case KindNumber, KindInteger:
		var number json.Number
		if err := decodeOneJSON(raw, &number, location); err != nil {
			return err
		}
		value, err := number.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must be a finite number", location)
		}
		if schema.Kind == KindInteger {
			if math.Trunc(value) != value {
				return fmt.Errorf("%s must be an integer", location)
			}
			if value < -maxJavaScriptInteger || value > maxJavaScriptInteger {
				return fmt.Errorf("%s exceeds JavaScript's safe integer range", location)
			}
		}
		return nil
	case KindLiteral:
		return validateLiteralJSON(schema.Literal, raw, location)
	case KindEnum:
		var value string
		if err := decodeOneJSON(raw, &value, location); err != nil {
			return err
		}
		for _, allowed := range schema.Values {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("%s is not an allowed enum value", location)
	case KindArray:
		if schema.Items == nil {
			return fmt.Errorf("%s array contract has no items", location)
		}
		var values []json.RawMessage
		if err := decodeOneJSON(raw, &values, location); err != nil {
			return err
		}
		for index, value := range values {
			if err := validateRawJSON(*schema.Items, value, fmt.Sprintf("%s[%d]", location, index), false); err != nil {
				return err
			}
		}
		return nil
	case KindObject:
		var object map[string]json.RawMessage
		if err := decodeOneJSON(raw, &object, location); err != nil {
			return err
		}
		for name := range object {
			if _, exists := schema.Shape[name]; !exists {
				return fmt.Errorf("%s has unknown property %q", location, name)
			}
		}
		for name, property := range schema.Shape {
			value, exists := object[name]
			if !exists {
				if property.Optional {
					continue
				}
				return fmt.Errorf("%s.%s is required", location, name)
			}
			if err := validateRawJSON(property, value, location+"."+name, true); err != nil {
				return err
			}
		}
		return nil
	case KindUnion:
		matches := 0
		for _, variant := range schema.Variants {
			if validateRawJSON(variant, raw, location, false) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s must match exactly one union variant", location)
		}
		return nil
	default:
		return fmt.Errorf("%s has unsupported contract kind %q", location, schema.Kind)
	}
}

func decodeOneJSON(raw json.RawMessage, target any, location string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", location, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("%s: %w", location, err)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON after first value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func validateLiteralJSON(literal any, raw json.RawMessage, location string) error {
	switch expected := literal.(type) {
	case string:
		var actual string
		if err := decodeOneJSON(raw, &actual, location); err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("%s must equal %q", location, expected)
		}
	case bool:
		var actual bool
		if err := decodeOneJSON(raw, &actual, location); err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("%s must equal %t", location, expected)
		}
	case json.Number:
		var actual json.Number
		if err := decodeOneJSON(raw, &actual, location); err != nil {
			return err
		}
		actualNumber, actualOK := new(big.Rat).SetString(string(actual))
		expectedNumber, expectedOK := new(big.Rat).SetString(string(expected))
		if !actualOK || !expectedOK || actualNumber.Cmp(expectedNumber) != 0 {
			return fmt.Errorf("%s must equal %s", location, expected)
		}
	case float64:
		var actual json.Number
		if err := decodeOneJSON(raw, &actual, location); err != nil {
			return err
		}
		value, err := actual.Float64()
		if err != nil || value != expected {
			return fmt.Errorf("%s must equal %v", location, expected)
		}
	case int:
		return validateLiteralJSON(json.Number(fmt.Sprint(expected)), raw, location)
	case int64:
		return validateLiteralJSON(json.Number(fmt.Sprint(expected)), raw, location)
	case nil:
		return fmt.Errorf("%s null literals are not supported by generated Go contracts", location)
	default:
		return fmt.Errorf("%s has unsupported literal type %T", location, literal)
	}
	return nil
}
