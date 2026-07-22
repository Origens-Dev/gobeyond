package renderplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type rawPlan struct {
	APIVersion string          `json:"apiVersion"`
	RouteID    string          `json:"routeId"`
	Root       json.RawMessage `json:"root"`
}

func Parse(data []byte) (*Plan, error) { return Decode(bytes.NewReader(data)) }

func Decode(reader io.Reader) (*Plan, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var raw rawPlan
	if err := decoder.Decode(&raw); err != nil {
		return nil, decodeError("$", "cannot decode plan", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, decodeError("$", "plan must contain one JSON value", err)
	}
	root, err := decodeNode(raw.Root, "$.root")
	if err != nil {
		return nil, err
	}
	plan := &Plan{APIVersion: raw.APIVersion, RouteID: raw.RouteID, Root: root}
	if err := Validate(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func (p *Plan) UnmarshalJSON(data []byte) error {
	decoded, err := Parse(data)
	if err != nil {
		return err
	}
	*p = *decoded
	return nil
}

func decodeNode(data []byte, path string) (Node, error) {
	kind, err := objectKind(data, path)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "element":
		var raw struct {
			Kind       string            `json:"kind"`
			Tag        string            `json:"tag"`
			Namespace  Namespace         `json:"namespace,omitempty"`
			Attributes []json.RawMessage `json:"attributes,omitempty"`
			Children   []json.RawMessage `json:"children,omitempty"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid element", err)
		}
		n := &Element{Kind: raw.Kind, Tag: raw.Tag, Namespace: raw.Namespace}
		for i, value := range raw.Attributes {
			attr, err := decodeAttribute(value, fmt.Sprintf("%s.attributes[%d]", path, i))
			if err != nil {
				return nil, err
			}
			n.Attributes = append(n.Attributes, attr)
		}
		for i, value := range raw.Children {
			child, err := decodeNode(value, fmt.Sprintf("%s.children[%d]", path, i))
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, child)
		}
		return n, nil
	case "text":
		var raw struct {
			Kind  string          `json:"kind"`
			Value json.RawMessage `json:"value"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid text", err)
		}
		expr, err := decodeExpression(raw.Value, path+".value")
		if err != nil {
			return nil, err
		}
		return &Text{Kind: raw.Kind, Value: expr}, nil
	case "fragment":
		var raw struct {
			Kind     string            `json:"kind"`
			Children []json.RawMessage `json:"children"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid fragment", err)
		}
		n := &Fragment{Kind: raw.Kind}
		for i, value := range raw.Children {
			child, err := decodeNode(value, fmt.Sprintf("%s.children[%d]", path, i))
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, child)
		}
		return n, nil
	case "conditional":
		var raw struct {
			Kind                        string `json:"kind"`
			Test, Consequent, Alternate json.RawMessage
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid conditional", err)
		}
		test, err := decodeExpression(raw.Test, path+".test")
		if err != nil {
			return nil, err
		}
		consequent, err := decodeNode(raw.Consequent, path+".consequent")
		if err != nil {
			return nil, err
		}
		var alternate Node
		if len(raw.Alternate) > 0 && string(raw.Alternate) != "null" {
			alternate, err = decodeNode(raw.Alternate, path+".alternate")
			if err != nil {
				return nil, err
			}
		}
		return &Conditional{Kind: raw.Kind, Test: test, Consequent: consequent, Alternate: alternate}, nil
	case "each":
		var raw struct {
			Kind  string          `json:"kind"`
			Items json.RawMessage `json:"items"`
			Item  string          `json:"item"`
			Index string          `json:"index,omitempty"`
			Key   json.RawMessage `json:"key"`
			Body  json.RawMessage `json:"body"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid each", err)
		}
		items, err := decodeExpression(raw.Items, path+".items")
		if err != nil {
			return nil, err
		}
		key, err := decodeExpression(raw.Key, path+".key")
		if err != nil {
			return nil, err
		}
		body, err := decodeNode(raw.Body, path+".body")
		if err != nil {
			return nil, err
		}
		return &Each{Kind: raw.Kind, Items: items, Item: raw.Item, Index: raw.Index, Key: key, Body: body}, nil
	case "clientOnly":
		var raw struct {
			Kind     string          `json:"kind"`
			Fallback json.RawMessage `json:"fallback"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid clientOnly", err)
		}
		var fallback Node
		if len(raw.Fallback) > 0 && string(raw.Fallback) != "null" {
			fallback, err = decodeNode(raw.Fallback, path+".fallback")
			if err != nil {
				return nil, err
			}
		}
		return &ClientOnly{Kind: raw.Kind, Fallback: fallback}, nil
	case "rawHtml":
		var raw struct {
			Kind  string          `json:"kind"`
			Value json.RawMessage `json:"value"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid rawHtml", err)
		}
		expr, err := decodeExpression(raw.Value, path+".value")
		if err != nil {
			return nil, err
		}
		return &RawHTML{Kind: raw.Kind, Value: expr}, nil
	default:
		return nil, decodeError(path+".kind", fmt.Sprintf("unsupported node kind %q", kind), nil)
	}
}

func decodeAttribute(data []byte, path string) (Attribute, error) {
	var raw struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
		Mode  AttributeMode   `json:"mode,omitempty"`
	}
	if err := strictUnmarshal(data, &raw); err != nil {
		return Attribute{}, decodeError(path, "invalid attribute", err)
	}
	value, err := decodeExpression(raw.Value, path+".value")
	if err != nil {
		return Attribute{}, err
	}
	return Attribute{Name: raw.Name, Value: value, Mode: raw.Mode}, nil
}

func decodeExpression(data []byte, path string) (Expression, error) {
	kind, err := objectKind(data, path)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "literal":
		var raw struct {
			Kind  string          `json:"kind"`
			Value json.RawMessage `json:"value"`
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid literal", err)
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw.Value))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, decodeError(path+".value", "invalid literal value", err)
		}
		return &Literal{Kind: raw.Kind, Value: value}, nil
	case "path":
		var raw Path
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid path", err)
		}
		return &raw, nil
	case "binary":
		var raw struct {
			Kind, Operator string
			Left, Right    json.RawMessage
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid binary expression", err)
		}
		left, err := decodeExpression(raw.Left, path+".left")
		if err != nil {
			return nil, err
		}
		right, err := decodeExpression(raw.Right, path+".right")
		if err != nil {
			return nil, err
		}
		return &Binary{Kind: raw.Kind, Operator: raw.Operator, Left: left, Right: right}, nil
	case "unary":
		var raw struct {
			Kind, Operator string
			Operand        json.RawMessage
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid unary expression", err)
		}
		operand, err := decodeExpression(raw.Operand, path+".operand")
		if err != nil {
			return nil, err
		}
		return &Unary{Kind: raw.Kind, Operator: raw.Operator, Operand: operand}, nil
	case "helper":
		var raw struct {
			Kind, Name string
			Arguments  []json.RawMessage
		}
		if err := strictUnmarshal(data, &raw); err != nil {
			return nil, decodeError(path, "invalid helper", err)
		}
		n := &Helper{Kind: raw.Kind, Name: raw.Name}
		for i, value := range raw.Arguments {
			arg, err := decodeExpression(value, fmt.Sprintf("%s.arguments[%d]", path, i))
			if err != nil {
				return nil, err
			}
			n.Arguments = append(n.Arguments, arg)
		}
		return n, nil
	default:
		return nil, decodeError(path+".kind", fmt.Sprintf("unsupported expression kind %q", kind), nil)
	}
}

func objectKind(data []byte, path string) (string, error) {
	var raw struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", decodeError(path, "expected object with kind", err)
	}
	if raw.Kind == "" {
		return "", decodeError(path+".kind", "kind is required", nil)
	}
	return raw.Kind, nil
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
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeError(path, message string, cause error) error {
	return &Error{Code: CodeDecode, Path: path, Message: message, Cause: cause}
}
