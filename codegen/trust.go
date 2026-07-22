package codegen

import (
	"encoding/json"
	"fmt"

	"github.com/gobeyond-dev/gobeyond/renderplan"
)

// TrustStaticSafeHTML restores SafeHTML trust markers after build props have
// crossed the compiler's JSON protocol. The build artifact is trusted only
// after it has passed the frozen value contract.
func TrustStaticSafeHTML(document Document, routeID string, props any) (any, error) {
	for _, route := range document.Routes {
		if route.RouteID == routeID {
			return trustContractValue(route.Props, props, "props")
		}
	}
	return nil, fmt.Errorf("route contract %s is missing", routeID)
}

func trustContractValue(schema Value, value any, path string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch schema.Kind {
	case KindSafeHTML:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s SafeHTML must be a string", path)
		}
		return renderplan.TrustedHTML(text), nil
	case KindObject:
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an object", path)
		}
		for name, property := range schema.Shape {
			raw, exists := object[name]
			if !exists {
				continue
			}
			trusted, err := trustContractValue(property, raw, path+"."+name)
			if err != nil {
				return nil, err
			}
			object[name] = trusted
		}
		return object, nil
	case KindArray:
		items, ok := value.([]any)
		if !ok || schema.Items == nil {
			return nil, fmt.Errorf("%s must be an array", path)
		}
		for index, item := range items {
			trusted, err := trustContractValue(*schema.Items, item, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			items[index] = trusted
		}
		return items, nil
	case KindUnion:
		matches := make([]Value, 0, len(schema.Variants))
		for _, variant := range schema.Variants {
			if contractValueMatches(variant, value) {
				matches = append(matches, variant)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("%s SafeHTML union must have exactly one matching variant", path)
		}
		return trustContractValue(matches[0], value, path)
	default:
		return value, nil
	}
}

func contractValueMatches(schema Value, value any) bool {
	if value == nil {
		return schema.Nullable || schema.Optional || (schema.Kind == KindLiteral && schema.Literal == nil)
	}
	switch schema.Kind {
	case KindString, KindSafeHTML, KindDateTime, KindBytes, KindEnum:
		_, ok := value.(string)
		return ok
	case KindNumber, KindInteger:
		switch value.(type) {
		case json.Number, float64:
			return true
		}
	case KindBoolean:
		_, ok := value.(bool)
		return ok
	case KindObject:
		_, ok := value.(map[string]any)
		return ok
	case KindArray:
		_, ok := value.([]any)
		return ok
	case KindLiteral:
		return fmt.Sprint(value) == fmt.Sprint(schema.Literal)
	case KindUnion:
		for _, variant := range schema.Variants {
			if contractValueMatches(variant, value) {
				return true
			}
		}
	}
	return false
}
