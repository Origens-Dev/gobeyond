package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const APIVersionV1Alpha1 = "gobeyond.contract/v1alpha1"

type Kind string

const (
	KindString   Kind = "string"
	KindNumber   Kind = "number"
	KindInteger  Kind = "integer"
	KindBoolean  Kind = "boolean"
	KindDateTime Kind = "datetime"
	KindBytes    Kind = "bytes"
	KindSafeHTML Kind = "safeHtml"
	KindLiteral  Kind = "literal"
	KindEnum     Kind = "enum"
	KindArray    Kind = "array"
	KindObject   Kind = "object"
	KindUnion    Kind = "union"
)

type Document struct {
	APIVersion string
	Routes     []Route
	Actions    []Action
}

type Route struct {
	RouteID string
	Props   Value
}

type Action struct {
	ActionID string
	Input    Value
	Output   Value
}

type Value struct {
	Kind     Kind
	Optional bool
	Nullable bool
	Literal  any
	Values   []string
	Items    *Value
	Shape    map[string]Value
	Variants []Value
}

// Parse strictly decodes one value-contract JSON document.
func Parse(data []byte) (Document, error) {
	return Decode(bytes.NewReader(data))
}

// Decode strictly decodes and validates one value-contract JSON document.
// Unknown fields, trailing JSON, invalid kind-specific fields, duplicate IDs,
// and schemas that cannot be represented safely by the MVP generator fail.
func Decode(reader io.Reader) (Document, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	var raw rawDocument
	if err := decoder.Decode(&raw); err != nil {
		return Document{}, fmt.Errorf("decode value contract: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Document{}, err
	}

	document, err := raw.decode()
	if err != nil {
		return Document{}, err
	}
	if err := Validate(document); err != nil {
		return Document{}, err
	}
	return document, nil
}

type rawDocument struct {
	APIVersion string      `json:"apiVersion"`
	Routes     []rawRoute  `json:"routes"`
	Actions    []rawAction `json:"actions"`
}

type rawRoute struct {
	RouteID string          `json:"routeId"`
	Props   json.RawMessage `json:"props"`
}

type rawAction struct {
	ActionID string          `json:"actionId"`
	Input    json.RawMessage `json:"input"`
	Output   json.RawMessage `json:"output"`
}

type rawValue struct {
	Kind     Kind                        `json:"kind"`
	Optional *bool                       `json:"optional"`
	Nullable *bool                       `json:"nullable"`
	Value    json.RawMessage             `json:"value"`
	Values   *[]string                   `json:"values"`
	Items    json.RawMessage             `json:"items"`
	Shape    *map[string]json.RawMessage `json:"shape"`
	Variants *[]json.RawMessage          `json:"variants"`
}

func (raw rawDocument) decode() (Document, error) {
	if raw.Routes == nil {
		return Document{}, invalid("$.routes", "routes is required")
	}
	if raw.Actions == nil {
		return Document{}, invalid("$.actions", "actions is required")
	}
	document := Document{
		APIVersion: raw.APIVersion,
		Routes:     make([]Route, 0, len(raw.Routes)),
		Actions:    make([]Action, 0, len(raw.Actions)),
	}
	for index, route := range raw.Routes {
		value, err := decodeValue(route.Props, fmt.Sprintf("$.routes[%d].props", index), false)
		if err != nil {
			return Document{}, err
		}
		document.Routes = append(document.Routes, Route{RouteID: route.RouteID, Props: value})
	}
	for index, action := range raw.Actions {
		input, err := decodeValue(action.Input, fmt.Sprintf("$.actions[%d].input", index), false)
		if err != nil {
			return Document{}, err
		}
		output, err := decodeValue(action.Output, fmt.Sprintf("$.actions[%d].output", index), false)
		if err != nil {
			return Document{}, err
		}
		document.Actions = append(document.Actions, Action{
			ActionID: action.ActionID,
			Input:    input,
			Output:   output,
		})
	}
	return document, nil
}

func decodeValue(data json.RawMessage, location string, objectField bool) (Value, error) {
	if len(data) == 0 {
		return Value{}, invalid(location, "value schema is required")
	}
	var raw rawValue
	if err := strictUnmarshal(data, &raw); err != nil {
		return Value{}, invalid(location, err.Error())
	}
	value := Value{Kind: raw.Kind}
	if raw.Optional != nil {
		value.Optional = *raw.Optional
	}
	if raw.Nullable != nil {
		value.Nullable = *raw.Nullable
	}
	if value.Optional && !objectField {
		return Value{}, invalid(location+".optional", "optional is only valid for an object property")
	}

	switch raw.Kind {
	case KindString, KindNumber, KindInteger, KindBoolean, KindDateTime, KindBytes, KindSafeHTML:
		if err := rejectPayload(raw, location); err != nil {
			return Value{}, err
		}
	case KindLiteral:
		if len(raw.Value) == 0 {
			return Value{}, invalid(location+".value", "literal value is required")
		}
		if raw.Values != nil || len(raw.Items) != 0 || raw.Shape != nil || raw.Variants != nil {
			return Value{}, invalid(location, "literal accepts only value, optional, and nullable")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw.Value))
		decoder.UseNumber()
		if err := decoder.Decode(&value.Literal); err != nil {
			return Value{}, invalid(location+".value", err.Error())
		}
		switch value.Literal.(type) {
		case nil, string, bool, json.Number:
		default:
			return Value{}, invalid(location+".value", "literal must be string, number, boolean, or null")
		}
	case KindEnum:
		if raw.Values == nil {
			return Value{}, invalid(location+".values", "enum values are required")
		}
		if len(raw.Value) != 0 || len(raw.Items) != 0 || raw.Shape != nil || raw.Variants != nil {
			return Value{}, invalid(location, "enum accepts only values, optional, and nullable")
		}
		value.Values = append([]string(nil), (*raw.Values)...)
		if len(value.Values) == 0 {
			return Value{}, invalid(location+".values", "enum requires at least one value")
		}
		seen := make(map[string]struct{}, len(value.Values))
		for index, enumValue := range value.Values {
			if _, exists := seen[enumValue]; exists {
				return Value{}, invalid(fmt.Sprintf("%s.values[%d]", location, index), "enum values must be unique")
			}
			seen[enumValue] = struct{}{}
		}
	case KindArray:
		if len(raw.Items) == 0 {
			return Value{}, invalid(location+".items", "array items are required")
		}
		if len(raw.Value) != 0 || raw.Values != nil || raw.Shape != nil || raw.Variants != nil {
			return Value{}, invalid(location, "array accepts only items, optional, and nullable")
		}
		items, err := decodeValue(raw.Items, location+".items", false)
		if err != nil {
			return Value{}, err
		}
		value.Items = &items
	case KindObject:
		if raw.Shape == nil {
			return Value{}, invalid(location+".shape", "object shape is required")
		}
		if len(raw.Value) != 0 || raw.Values != nil || len(raw.Items) != 0 || raw.Variants != nil {
			return Value{}, invalid(location, "object accepts only shape, optional, and nullable")
		}
		value.Shape = make(map[string]Value, len(*raw.Shape))
		names := make([]string, 0, len(*raw.Shape))
		for name := range *raw.Shape {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			childRaw := (*raw.Shape)[name]
			if name == "" {
				return Value{}, invalid(location+".shape", "object property names must not be empty")
			}
			child, err := decodeValue(childRaw, location+".shape["+quotePath(name)+"]", true)
			if err != nil {
				return Value{}, err
			}
			value.Shape[name] = child
		}
	case KindUnion:
		if raw.Variants == nil || len(*raw.Variants) < 2 {
			return Value{}, invalid(location+".variants", "union requires at least two variants")
		}
		if len(raw.Value) != 0 || raw.Values != nil || len(raw.Items) != 0 || raw.Shape != nil {
			return Value{}, invalid(location, "union accepts only variants, optional, and nullable")
		}
		for index, variantRaw := range *raw.Variants {
			variant, err := decodeValue(variantRaw, fmt.Sprintf("%s.variants[%d]", location, index), false)
			if err != nil {
				return Value{}, err
			}
			if variant.Optional || variant.Nullable {
				return Value{}, invalid(fmt.Sprintf("%s.variants[%d]", location, index), "union variants cannot be optional or nullable")
			}
			value.Variants = append(value.Variants, variant)
		}
		if _, err := stringUnionValues(value); err != nil {
			return Value{}, invalid(location, err.Error())
		}
	default:
		if raw.Kind == "" {
			return Value{}, invalid(location+".kind", "kind is required")
		}
		return Value{}, invalid(location+".kind", fmt.Sprintf("unsupported kind %q", raw.Kind))
	}
	return value, nil
}

func rejectPayload(raw rawValue, location string) error {
	if len(raw.Value) != 0 || raw.Values != nil || len(raw.Items) != 0 || raw.Shape != nil || raw.Variants != nil {
		return invalid(location, fmt.Sprintf("%s does not accept value, values, items, shape, or variants", raw.Kind))
	}
	return nil
}

func strictUnmarshal(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("value contract must contain one JSON value")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func invalid(location, message string) error {
	return fmt.Errorf("invalid value contract at %s: %s", location, message)
}

func quotePath(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// Validate checks a programmatically constructed document using the same
// semantic restrictions as Decode.
func Validate(document Document) error {
	if document.APIVersion != APIVersionV1Alpha1 {
		return invalid("$.apiVersion", fmt.Sprintf("must be %q", APIVersionV1Alpha1))
	}
	if document.Routes == nil {
		return invalid("$.routes", "routes is required")
	}
	if document.Actions == nil {
		return invalid("$.actions", "actions is required")
	}
	routeIDs := make(map[string]struct{}, len(document.Routes))
	for index, route := range document.Routes {
		location := fmt.Sprintf("$.routes[%d]", index)
		if strings.TrimSpace(route.RouteID) == "" {
			return invalid(location+".routeId", "routeId is required")
		}
		if _, exists := routeIDs[route.RouteID]; exists {
			return invalid(location+".routeId", fmt.Sprintf("duplicate routeId %q", route.RouteID))
		}
		routeIDs[route.RouteID] = struct{}{}
		if route.Props.Nullable {
			return invalid(location+".props.nullable", "root route props cannot be nullable; make individual properties nullable")
		}
		if err := validateValue(route.Props, location+".props", false); err != nil {
			return err
		}
	}
	actionIDs := make(map[string]struct{}, len(document.Actions))
	for index, action := range document.Actions {
		location := fmt.Sprintf("$.actions[%d]", index)
		if strings.TrimSpace(action.ActionID) == "" {
			return invalid(location+".actionId", "actionId is required")
		}
		if _, exists := actionIDs[action.ActionID]; exists {
			return invalid(location+".actionId", fmt.Sprintf("duplicate actionId %q", action.ActionID))
		}
		actionIDs[action.ActionID] = struct{}{}
		if action.Input.Nullable {
			return invalid(location+".input.nullable", "root action input cannot be nullable; make individual properties nullable")
		}
		if action.Output.Nullable {
			return invalid(location+".output.nullable", "root action output cannot be nullable; make individual properties nullable")
		}
		if err := validateValue(action.Input, location+".input", false); err != nil {
			return err
		}
		if err := validateValue(action.Output, location+".output", false); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(value Value, location string, objectField bool) error {
	if value.Optional && !objectField {
		return invalid(location+".optional", "optional is only valid for an object property")
	}
	switch value.Kind {
	case KindString, KindNumber, KindInteger, KindBoolean, KindDateTime, KindBytes, KindSafeHTML:
		if value.Literal != nil || len(value.Values) != 0 || value.Items != nil || value.Shape != nil || len(value.Variants) != 0 {
			return invalid(location, fmt.Sprintf("%s does not accept literal, values, items, shape, or variants", value.Kind))
		}
		return nil
	case KindLiteral:
		if len(value.Values) != 0 || value.Items != nil || value.Shape != nil || len(value.Variants) != 0 {
			return invalid(location, "literal accepts only its literal value, optional, and nullable")
		}
		switch value.Literal.(type) {
		case string, bool, json.Number, float64, int, int64:
			return nil
		case nil:
			return invalid(location+".value", "null literals have no safe generated Go type; use nullable on the underlying schema")
		default:
			return invalid(location+".value", "literal must be string, number, boolean, or null")
		}
	case KindEnum:
		if value.Literal != nil || value.Items != nil || value.Shape != nil || len(value.Variants) != 0 {
			return invalid(location, "enum accepts only values, optional, and nullable")
		}
		if len(value.Values) == 0 {
			return invalid(location+".values", "enum requires at least one value")
		}
		seen := map[string]struct{}{}
		for _, enumValue := range value.Values {
			if _, exists := seen[enumValue]; exists {
				return invalid(location+".values", "enum values must be unique")
			}
			seen[enumValue] = struct{}{}
		}
		return nil
	case KindArray:
		if value.Literal != nil || len(value.Values) != 0 || value.Shape != nil || len(value.Variants) != 0 {
			return invalid(location, "array accepts only items, optional, and nullable")
		}
		if value.Items == nil {
			return invalid(location+".items", "array items are required")
		}
		return validateValue(*value.Items, location+".items", false)
	case KindObject:
		if value.Literal != nil || len(value.Values) != 0 || value.Items != nil || len(value.Variants) != 0 {
			return invalid(location, "object accepts only shape, optional, and nullable")
		}
		if value.Shape == nil {
			return invalid(location+".shape", "object shape is required")
		}
		names := make([]string, 0, len(value.Shape))
		for name := range value.Shape {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child := value.Shape[name]
			if name == "" {
				return invalid(location+".shape", "object property names must not be empty")
			}
			if err := validateValue(child, location+".shape["+quotePath(name)+"]", true); err != nil {
				return err
			}
		}
		return nil
	case KindUnion:
		if value.Literal != nil || len(value.Values) != 0 || value.Items != nil || value.Shape != nil {
			return invalid(location, "union accepts only variants, optional, and nullable")
		}
		if len(value.Variants) < 2 {
			return invalid(location+".variants", "union requires at least two variants")
		}
		for index, variant := range value.Variants {
			if variant.Optional || variant.Nullable {
				return invalid(fmt.Sprintf("%s.variants[%d]", location, index), "union variants cannot be optional or nullable")
			}
			if err := validateValue(variant, fmt.Sprintf("%s.variants[%d]", location, index), false); err != nil {
				return err
			}
		}
		if _, err := stringUnionValues(value); err != nil {
			return invalid(location, err.Error())
		}
		return nil
	default:
		return invalid(location+".kind", fmt.Sprintf("unsupported kind %q", value.Kind))
	}
}

func stringUnionValues(value Value) ([]string, error) {
	var values []string
	seen := map[string]struct{}{}
	for _, variant := range value.Variants {
		var candidates []string
		switch variant.Kind {
		case KindLiteral:
			literal, ok := variant.Literal.(string)
			if !ok {
				return nil, fmt.Errorf("MVP unions support only string literal and string enum variants")
			}
			candidates = []string{literal}
		case KindEnum:
			candidates = variant.Values
		default:
			return nil, fmt.Errorf("MVP unions support only string literal and string enum variants")
		}
		for _, candidate := range candidates {
			if _, exists := seen[candidate]; exists {
				return nil, fmt.Errorf("union string values must be unique: %q", candidate)
			}
			seen[candidate] = struct{}{}
			values = append(values, candidate)
		}
	}
	return values, nil
}
