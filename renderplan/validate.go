package renderplan

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxValidationDepth = 256

func Validate(plan *Plan) error {
	if plan == nil {
		return invalid("$", "plan is nil")
	}
	if plan.APIVersion != APIVersionV1Alpha1 {
		return &Error{Code: CodeVersion, Path: "$.apiVersion", Message: fmt.Sprintf("expected %q, got %q", APIVersionV1Alpha1, plan.APIVersion)}
	}
	if strings.TrimSpace(plan.RouteID) == "" {
		return invalid("$.routeId", "routeId is required")
	}
	if plan.Root == nil {
		return invalid("$.root", "root is required")
	}
	return validateNode(plan.Root, "$.root", 0)
}

func validateNode(node Node, path string, depth int) error {
	if depth > maxValidationDepth {
		return invalid(path, "maximum nesting depth exceeded")
	}
	if node == nil {
		return invalid(path, "node is nil")
	}
	switch n := node.(type) {
	case *Element:
		if n == nil {
			return invalid(path, "element is nil")
		}
		if n.Kind != "element" {
			return invalid(path+".kind", "element kind must be element")
		}
		if !validTagName(n.Tag) {
			return invalid(path+".tag", "invalid element name")
		}
		if n.Namespace != "" && n.Namespace != NamespaceHTML && n.Namespace != NamespaceSVG {
			return invalid(path+".namespace", "namespace must be html or svg")
		}
		seen := map[string]bool{}
		for i, attr := range n.Attributes {
			ap := fmt.Sprintf("%s.attributes[%d]", path, i)
			if !validName(attr.Name) {
				return invalid(ap+".name", "invalid attribute name")
			}
			name := strings.ToLower(attr.Name)
			if seen[name] {
				return invalid(ap+".name", "duplicate attribute")
			}
			seen[name] = true
			if attr.Mode != "" && attr.Mode != AttributeString && attr.Mode != AttributeBoolean && attr.Mode != AttributeURL && attr.Mode != AttributeStyle {
				return invalid(ap+".mode", "unsupported attribute mode")
			}
			if err := validateExpression(attr.Value, ap+".value", depth+1); err != nil {
				return err
			}
		}
		for i, child := range n.Children {
			if err := validateNode(child, fmt.Sprintf("%s.children[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	case *Text:
		if n == nil || n.Kind != "text" {
			return invalid(path+".kind", "text kind must be text")
		}
		return validateExpression(n.Value, path+".value", depth+1)
	case *Fragment:
		if n == nil || n.Kind != "fragment" {
			return invalid(path+".kind", "fragment kind must be fragment")
		}
		for i, child := range n.Children {
			if err := validateNode(child, fmt.Sprintf("%s.children[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	case *Conditional:
		if n == nil || n.Kind != "conditional" {
			return invalid(path+".kind", "conditional kind must be conditional")
		}
		if err := validateExpression(n.Test, path+".test", depth+1); err != nil {
			return err
		}
		if err := validateNode(n.Consequent, path+".consequent", depth+1); err != nil {
			return err
		}
		if n.Alternate != nil {
			return validateNode(n.Alternate, path+".alternate", depth+1)
		}
	case *Each:
		if n == nil || n.Kind != "each" {
			return invalid(path+".kind", "each kind must be each")
		}
		if !validIdentifier(n.Item) {
			return invalid(path+".item", "invalid item binding")
		}
		if n.Index != "" && !validIdentifier(n.Index) {
			return invalid(path+".index", "invalid index binding")
		}
		if n.Item == n.Index {
			return invalid(path+".index", "item and index bindings must differ")
		}
		if err := validateExpression(n.Items, path+".items", depth+1); err != nil {
			return err
		}
		if err := validateExpression(n.Key, path+".key", depth+1); err != nil {
			return err
		}
		if n.When != nil {
			if err := validateExpression(n.When, path+".when", depth+1); err != nil {
				return err
			}
		}
		return validateNode(n.Body, path+".body", depth+1)
	case *ClientOnly:
		if n == nil || n.Kind != "clientOnly" {
			return invalid(path+".kind", "clientOnly kind must be clientOnly")
		}
		if n.Fallback != nil {
			return validateNode(n.Fallback, path+".fallback", depth+1)
		}
	case *RawHTML:
		if n == nil || n.Kind != "rawHtml" {
			return invalid(path+".kind", "rawHtml kind must be rawHtml")
		}
		return validateExpression(n.Value, path+".value", depth+1)
	default:
		return invalid(path, fmt.Sprintf("unsupported node type %T", node))
	}
	return nil
}

func validateExpression(expr Expression, path string, depth int) error {
	if depth > maxValidationDepth {
		return invalid(path, "maximum nesting depth exceeded")
	}
	if expr == nil {
		return invalid(path, "expression is required")
	}
	switch e := expr.(type) {
	case *Literal:
		if e == nil || e.Kind != "literal" {
			return invalid(path+".kind", "literal kind must be literal")
		}
	case *Path:
		if e == nil || e.Kind != "path" {
			return invalid(path+".kind", "path kind must be path")
		}
		if len(e.Path) == 0 {
			return invalid(path+".path", "path cannot be empty")
		}
		for i, segment := range e.Path {
			if segment.IsIdx && segment.Index < 0 {
				return invalid(fmt.Sprintf("%s.path[%d]", path, i), "array index cannot be negative")
			}
			if !segment.IsIdx && segment.Name == "" {
				return invalid(fmt.Sprintf("%s.path[%d]", path, i), "property cannot be empty")
			}
		}
	case *IndexExpr:
		if e == nil || e.Kind != "index" {
			return invalid(path+".kind", "index kind must be index")
		}
		if err := validateExpression(e.Object, path+".object", depth+1); err != nil {
			return err
		}
		return validateExpression(e.Index, path+".index", depth+1)
	case *Binary:
		if e == nil || e.Kind != "binary" {
			return invalid(path+".kind", "binary kind must be binary")
		}
		if !oneOf(e.Operator, "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||", "??") {
			return invalid(path+".operator", "unsupported binary operator")
		}
		if err := validateExpression(e.Left, path+".left", depth+1); err != nil {
			return err
		}
		return validateExpression(e.Right, path+".right", depth+1)
	case *Unary:
		if e == nil || e.Kind != "unary" {
			return invalid(path+".kind", "unary kind must be unary")
		}
		if !oneOf(e.Operator, "!", "-") {
			return invalid(path+".operator", "unsupported unary operator")
		}
		return validateExpression(e.Operand, path+".operand", depth+1)
	case *Ternary:
		if e == nil || e.Kind != "ternary" {
			return invalid(path+".kind", "ternary kind must be ternary")
		}
		if err := validateExpression(e.Test, path+".test", depth+1); err != nil {
			return err
		}
		if err := validateExpression(e.Consequent, path+".consequent", depth+1); err != nil {
			return err
		}
		return validateExpression(e.Alternate, path+".alternate", depth+1)
	case *Helper:
		if e == nil || e.Kind != "helper" {
			return invalid(path+".kind", "helper kind must be helper")
		}
		if !oneOf(e.Name, "string", "lower", "upper", "join", "url", "imageSrc", "style") {
			return invalid(path+".name", "unsupported helper")
		}
		for i, arg := range e.Arguments {
			if err := validateExpression(arg, fmt.Sprintf("%s.arguments[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		if e.Name == "style" && len(e.Arguments)%2 != 0 {
			return invalid(path+".arguments", "style helper requires name/value pairs")
		}
	case *Intrinsic:
		if e == nil || e.Kind != "intrinsic" {
			return invalid(path+".kind", "intrinsic kind must be intrinsic")
		}
		spec, ok := IntrinsicDefinition(e.Name)
		if !ok {
			return invalid(path+".name", "unsupported intrinsic")
		}
		if len(e.Arguments) != spec.Arity {
			return invalid(path+".arguments", fmt.Sprintf("intrinsic expects %d arguments", spec.Arity))
		}
		for i, arg := range e.Arguments {
			if err := validateExpression(arg, fmt.Sprintf("%s.arguments[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	default:
		return invalid(path, fmt.Sprintf("unsupported expression type %T", expr))
	}
	return nil
}

func validName(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if i == 0 && !(unicode.IsLetter(r) || r == '_' || r == ':') {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == ':' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validTagName(value string) bool {
	if !validName(value) {
		return false
	}
	first, _ := utf8.DecodeRuneInString(value)
	return unicode.IsLetter(first)
}

func validIdentifier(value string) bool {
	for i, r := range value {
		if !(unicode.IsLetter(r) || r == '_' || (i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return value != ""
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
func invalid(path, message string) error {
	return &Error{Code: CodeValidation, Path: path, Message: message}
}
