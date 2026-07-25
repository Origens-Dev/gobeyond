// Package renderplan defines the versioned, language-neutral rendering plan
// consumed by GoBeyond's production renderer.
package renderplan

import (
	"encoding/json"
	"fmt"
)

const APIVersionV1Alpha1 = "gobeyond.render/v1alpha1"

// Plan is a complete rendering plan for one route.
type Plan struct {
	APIVersion string `json:"apiVersion"`
	RouteID    string `json:"routeId"`
	Root       Node   `json:"root"`
}

// Node is implemented by every rendering-plan node.
type Node interface{ isNode() }

type Element struct {
	Kind       string      `json:"kind"`
	Tag        string      `json:"tag"`
	Namespace  Namespace   `json:"namespace,omitempty"`
	Attributes []Attribute `json:"attributes,omitempty"`
	Children   []Node      `json:"children,omitempty"`
}

func (*Element) isNode() {}

type Text struct {
	Kind  string     `json:"kind"`
	Value Expression `json:"value"`
}

func (*Text) isNode() {}

type Fragment struct {
	Kind     string `json:"kind"`
	Children []Node `json:"children"`
}

func (*Fragment) isNode() {}

type Conditional struct {
	Kind       string     `json:"kind"`
	Test       Expression `json:"test"`
	Consequent Node       `json:"consequent"`
	Alternate  Node       `json:"alternate,omitempty"`
}

func (*Conditional) isNode() {}

type Each struct {
	Kind  string     `json:"kind"`
	Items Expression `json:"items"`
	Item  string     `json:"item"`
	Index string     `json:"index,omitempty"`
	Key   Expression `json:"key"`
	When  Expression `json:"when,omitempty"`
	Body  Node       `json:"body"`
}

func (*Each) isNode() {}

type ClientOnly struct {
	Kind     string `json:"kind"`
	Fallback Node   `json:"fallback,omitempty"`
}

func (*ClientOnly) isNode() {}

type RawHTML struct {
	Kind  string     `json:"kind"`
	Value Expression `json:"value"`
}

func (*RawHTML) isNode() {}

type Namespace string

const (
	NamespaceHTML Namespace = "html"
	NamespaceSVG  Namespace = "svg"
)

type AttributeMode string

const (
	AttributeString  AttributeMode = "string"
	AttributeBoolean AttributeMode = "boolean"
	AttributeURL     AttributeMode = "url"
	AttributeStyle   AttributeMode = "style"
)

type Attribute struct {
	Name  string        `json:"name"`
	Value Expression    `json:"value"`
	Mode  AttributeMode `json:"mode,omitempty"`
}

// Expression is implemented by portable rendering-plan expressions.
type Expression interface{ isExpression() }

type Literal struct {
	Kind  string `json:"kind"`
	Value any    `json:"value"`
}

func (*Literal) isExpression() {}

// PathSegment is either a property name or a non-negative array index.
type PathSegment struct {
	Name  string
	Index int
	IsIdx bool
}

func Property(name string) PathSegment { return PathSegment{Name: name} }
func Index(index int) PathSegment      { return PathSegment{Index: index, IsIdx: true} }

func (s PathSegment) MarshalJSON() ([]byte, error) {
	if s.IsIdx {
		return json.Marshal(s.Index)
	}
	return json.Marshal(s.Name)
}

func (s *PathSegment) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		s.Name, s.Index, s.IsIdx = name, 0, false
		return nil
	}
	var index int
	if err := json.Unmarshal(data, &index); err == nil {
		s.Name, s.Index, s.IsIdx = "", index, true
		return nil
	}
	return fmt.Errorf("path segment must be a string or integer")
}

type Path struct {
	Kind string        `json:"kind"`
	Path []PathSegment `json:"path"`
}

func (*Path) isExpression() {}

// IndexExpr is a dynamic property or array-element lookup (object[index]).
// Named IndexExpr to avoid clashing with the PathSegment Index helper.
type IndexExpr struct {
	Kind   string     `json:"kind"` // "index"
	Object Expression `json:"object"`
	Index  Expression `json:"index"`
}

func (*IndexExpr) isExpression() {}

type Binary struct {
	Kind     string     `json:"kind"`
	Operator string     `json:"operator"`
	Left     Expression `json:"left"`
	Right    Expression `json:"right"`
}

func (*Binary) isExpression() {}

type Unary struct {
	Kind     string     `json:"kind"`
	Operator string     `json:"operator"`
	Operand  Expression `json:"operand"`
}

func (*Unary) isExpression() {}

// Ternary is a scalar conditional expression (test ? consequent : alternate).
// Node-level branching remains Conditional.
type Ternary struct {
	Kind       string     `json:"kind"`
	Test       Expression `json:"test"`
	Consequent Expression `json:"consequent"`
	Alternate  Expression `json:"alternate"`
}

func (*Ternary) isExpression() {}

type Helper struct {
	Kind      string       `json:"kind"`
	Name      string       `json:"name"`
	Arguments []Expression `json:"arguments"`
}

func (*Helper) isExpression() {}

// Intrinsic is a compiler-recognized platform operation with equivalent Go
// runtime semantics. Names are stable protocol identifiers, not Go function
// names, so the registry can grow without adding expression kinds.
type Intrinsic struct {
	Kind      string       `json:"kind"`
	Name      string       `json:"name"`
	Arguments []Expression `json:"arguments"`
}

func (*Intrinsic) isExpression() {}

// SafeHTML marks HTML that was sanitized by the application before it crossed
// the rendering boundary. The unexported representation prevents plain strings
// from being used accidentally.
type SafeHTML struct{ value string }

// TrustedHTML establishes an explicit trust boundary. Callers should only pass
// output from their configured sanitizer.
func TrustedHTML(value string) SafeHTML { return SafeHTML{value: value} }

func (h SafeHTML) String() string { return h.value }

// MarshalJSON preserves the sanitized string in hydration props. SafeHTML
// deliberately has no UnmarshalJSON method: untrusted JSON cannot establish
// this trust marker.
func (h SafeHTML) MarshalJSON() ([]byte, error) { return json.Marshal(h.value) }
