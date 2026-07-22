// Package router implements GoBeyond's deterministic route-pattern matching.
package router

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

type SegmentKind uint8

const (
	Literal SegmentKind = iota
	Dynamic
	CatchAll
	OptionalCatchAll
)

type Segment struct {
	Kind  SegmentKind
	Value string
}

type Pattern struct {
	Source   string
	Segments []Segment
}

func Parse(source string) (Pattern, error) {
	if source == "" || source[0] != '/' {
		return Pattern{}, errors.New("route pattern must begin with /")
	}
	clean := strings.Trim(source, "/")
	if clean == "" {
		return Pattern{Source: "/"}, nil
	}
	parts := strings.Split(clean, "/")
	segments := make([]Segment, 0, len(parts))
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return Pattern{}, errors.New("route pattern contains an invalid segment")
		}
		segment := Segment{Kind: Literal, Value: part}
		switch {
		case strings.HasPrefix(part, "[[...") && strings.HasSuffix(part, "]]"):
			segment = Segment{Kind: OptionalCatchAll, Value: part[5 : len(part)-2]}
		case strings.HasPrefix(part, "[...") && strings.HasSuffix(part, "]"):
			segment = Segment{Kind: CatchAll, Value: part[4 : len(part)-1]}
		case strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]"):
			segment = Segment{Kind: Dynamic, Value: part[1 : len(part)-1]}
		case strings.ContainsAny(part, "[]"):
			return Pattern{}, errors.New("route pattern has malformed parameter syntax")
		}
		if segment.Value == "" {
			return Pattern{}, errors.New("route parameter name cannot be empty")
		}
		if (segment.Kind == CatchAll || segment.Kind == OptionalCatchAll) && i != len(parts)-1 {
			return Pattern{}, errors.New("catch-all parameters must be the final segment")
		}
		segments = append(segments, segment)
	}
	return Pattern{Source: source, Segments: segments}, nil
}

func (p Pattern) Match(path string) (map[string]string, bool) {
	parts, ok := splitPath(path)
	if !ok {
		return nil, false
	}
	params := make(map[string]string)
	for i, segment := range p.Segments {
		if segment.Kind == OptionalCatchAll && i == len(parts) {
			params[segment.Value] = ""
			return params, true
		}
		if i >= len(parts) {
			return nil, false
		}
		switch segment.Kind {
		case Literal:
			if segment.Value != parts[i] {
				return nil, false
			}
		case Dynamic:
			params[segment.Value] = parts[i]
		case CatchAll, OptionalCatchAll:
			params[segment.Value] = strings.Join(parts[i:], "/")
			return params, true
		}
	}
	return params, len(parts) == len(p.Segments)
}

func (p Pattern) specificity() []int {
	result := make([]int, len(p.Segments)+1)
	result[0] = len(p.Segments)
	for i, segment := range p.Segments {
		switch segment.Kind {
		case Literal:
			result[i+1] = 4
		case Dynamic:
			result[i+1] = 3
		case CatchAll:
			result[i+1] = 2
		case OptionalCatchAll:
			result[i+1] = 1
		}
	}
	return result
}

type Mode string

const (
	ModeStatic  Mode = "static"
	ModeDynamic Mode = "dynamic"
	ModeAPI     Mode = "api"
)

type Route struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Mode    Mode   `json:"mode"`
	Reason  string `json:"reason,omitempty"`
	parsed  Pattern
}

type Table struct {
	routes []Route
}

func NewTable(routes []Route) (*Table, error) {
	seen := make(map[string]struct{}, len(routes))
	parsed := make([]Route, len(routes))
	for i, route := range routes {
		if route.ID == "" {
			return nil, errors.New("route ID is required")
		}
		if _, exists := seen[route.Pattern]; exists {
			return nil, errors.New("duplicate route pattern: " + route.Pattern)
		}
		seen[route.Pattern] = struct{}{}
		pattern, err := Parse(route.Pattern)
		if err != nil {
			return nil, err
		}
		route.parsed = pattern
		parsed[i] = route
	}
	sort.SliceStable(parsed, func(i, j int) bool {
		return compareSpecificity(parsed[i].parsed.specificity(), parsed[j].parsed.specificity()) > 0
	})
	return &Table{routes: parsed}, nil
}

func (t *Table) Resolve(path string) (Route, map[string]string, bool) {
	for _, route := range t.routes {
		if params, ok := route.parsed.Match(path); ok {
			return route, params, true
		}
	}
	return Route{}, nil, false
}

func (t *Table) Routes() []Route {
	result := make([]Route, len(t.routes))
	copy(result, t.routes)
	for i := range result {
		result[i].parsed = Pattern{}
	}
	return result
}

func compareSpecificity(a, b []int) int {
	max := len(a)
	if len(b) > max {
		max = len(b)
	}
	for i := 1; i < max; i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	if len(a) > len(b) {
		return 1
	}
	if len(a) < len(b) {
		return -1
	}
	return 0
}

func splitPath(path string) ([]string, bool) {
	if path == "" || path[0] != '/' || strings.Contains(path, "\\") {
		return nil, false
	}
	clean := strings.Trim(path, "/")
	if clean == "" {
		return nil, true
	}
	encodedParts := strings.Split(clean, "/")
	parts := make([]string, len(encodedParts))
	for i, part := range encodedParts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "." || decoded == ".." || strings.Contains(decoded, "/") {
			return nil, false
		}
		parts[i] = decoded
	}
	return parts, true
}
