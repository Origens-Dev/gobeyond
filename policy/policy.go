// Package policy implements the build-scoped proxy policy shared by the
// GoBeyond origin runtime and the platform edge evaluator.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const APIVersion = "gobeyond.proxy-policy/v1alpha1"

// Config is the authored gobeyond.json shape. Rules are evaluated in source
// order within each list. Redirects always run before rewrites.
type Config struct {
	Redirects []Rule `json:"redirects,omitempty"`
	Rewrites  []Rule `json:"rewrites,omitempty"`
}

// Rule is a Next-like route rule. Source and Destination are path templates;
// [name], [...name], :name, and :name* captures are supported.
type Rule struct {
	Source      string            `json:"source"`
	Destination string            `json:"destination"`
	Methods     []string          `json:"methods,omitempty"`
	Hosts       []string          `json:"hosts,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Cookies     map[string]string `json:"cookies,omitempty"`
	Query       map[string]string `json:"query,omitempty"`
	Status      int               `json:"status,omitempty"`
}

// Artifact is the immutable deployment representation emitted into
// dist/deploy/proxy-policy.json.
type Artifact struct {
	APIVersion string `json:"apiVersion"`
	BuildID    string `json:"buildId"`
	Digest     string `json:"digest"`
	Redirects  []Rule `json:"redirects"`
	Rewrites   []Rule `json:"rewrites"`
}

// Policy is a validated artifact ready for request evaluation.
type Policy struct {
	Artifact  Artifact
	redirects []compiledRule
	rewrites  []compiledRule
}

type DecisionKind string

const (
	DecisionNone     DecisionKind = "none"
	DecisionRewrite  DecisionKind = "rewrite"
	DecisionRedirect DecisionKind = "redirect"
)

// Decision describes one policy result. RewriteURL is internal-only and
// preserves the original browser URL; Location is sent for redirects.
type Decision struct {
	Kind       DecisionKind
	RewriteURL *url.URL
	Location   string
	Status     int
	RuleIndex  int
}

// LoadConfig reads gobeyond.json. A missing file means an explicit empty
// policy, which is still emitted as an artifact by the build.
func LoadConfig(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("parse proxy policy: %w", err)
	}
	return config, nil
}

// LoadFile loads an emitted policy artifact. A missing file returns nil so
// older local/custom runtimes remain runnable while the transitional host
// still serves legacy builds.
func LoadFile(filename, expectedBuildID string) (*Policy, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data, expectedBuildID)
}

// Compile validates and emits a canonical build-scoped artifact.
func Compile(config Config, buildID string) ([]byte, *Policy, error) {
	if strings.TrimSpace(buildID) == "" {
		return nil, nil, errors.New("proxy policy build ID is required")
	}
	artifact := Artifact{
		APIVersion: APIVersion,
		BuildID:    buildID,
		Redirects:  cloneRules(config.Redirects),
		Rewrites:   cloneRules(config.Rewrites),
	}
	if err := validateRules(artifact.Redirects, true); err != nil {
		return nil, nil, fmt.Errorf("validate redirects: %w", err)
	}
	if err := validateRules(artifact.Rewrites, false); err != nil {
		return nil, nil, fmt.Errorf("validate rewrites: %w", err)
	}
	digest, err := artifactDigest(artifact)
	if err != nil {
		return nil, nil, err
	}
	artifact.Digest = digest
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode proxy policy: %w", err)
	}
	data = append(data, '\n')
	compiled, err := compileArtifact(artifact)
	if err != nil {
		return nil, nil, err
	}
	return data, compiled, nil
}

// Parse validates an emitted artifact and compiles its matchers.
func Parse(data []byte, expectedBuildID string) (*Policy, error) {
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, fmt.Errorf("parse proxy policy artifact: %w", err)
	}
	if artifact.APIVersion != APIVersion {
		return nil, fmt.Errorf("unsupported proxy policy API version %q", artifact.APIVersion)
	}
	if strings.TrimSpace(artifact.BuildID) == "" || artifact.BuildID != expectedBuildID {
		return nil, errors.New("proxy policy build ID mismatch")
	}
	if strings.TrimSpace(artifact.Digest) == "" {
		return nil, errors.New("proxy policy digest is required")
	}
	if digest, err := artifactDigest(artifact); err != nil {
		return nil, err
	} else if digest != artifact.Digest {
		return nil, errors.New("proxy policy digest mismatch")
	}
	return compileArtifact(artifact)
}

func compileArtifact(artifact Artifact) (*Policy, error) {
	if err := validateRules(artifact.Redirects, true); err != nil {
		return nil, fmt.Errorf("validate redirects: %w", err)
	}
	if err := validateRules(artifact.Rewrites, false); err != nil {
		return nil, fmt.Errorf("validate rewrites: %w", err)
	}
	policy := &Policy{Artifact: artifact}
	for _, rule := range artifact.Redirects {
		compiled, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		policy.redirects = append(policy.redirects, compiled)
	}
	for _, rule := range artifact.Rewrites {
		compiled, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		policy.rewrites = append(policy.rewrites, compiled)
	}
	return policy, nil
}

func artifactDigest(artifact Artifact) (string, error) {
	artifact.Digest = ""
	data, err := json.Marshal(artifact)
	if err != nil {
		return "", fmt.Errorf("encode proxy policy digest input: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func cloneRules(rules []Rule) []Rule {
	result := make([]Rule, len(rules))
	for i, rule := range rules {
		result[i] = rule
		result[i].Methods = append([]string(nil), rule.Methods...)
		result[i].Hosts = append([]string(nil), rule.Hosts...)
		result[i].Headers = cloneMap(rule.Headers)
		result[i].Cookies = cloneMap(rule.Cookies)
		result[i].Query = cloneMap(rule.Query)
	}
	if result == nil {
		return []Rule{}
	}
	return result
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func validateRules(rules []Rule, redirect bool) error {
	for index, rule := range rules {
		if strings.TrimSpace(rule.Source) == "" || strings.TrimSpace(rule.Destination) == "" {
			return fmt.Errorf("rule %d requires source and destination", index)
		}
		if _, err := parsePattern(rule.Source); err != nil {
			return fmt.Errorf("rule %d source: %w", index, err)
		}
		if redirect {
			if rule.Status != http.StatusMovedPermanently && rule.Status != http.StatusFound && rule.Status != http.StatusTemporaryRedirect && rule.Status != http.StatusPermanentRedirect {
				return fmt.Errorf("rule %d redirect status must be 301, 302, 307, or 308", index)
			}
			if err := validateRedirectDestination(rule.Destination); err != nil {
				return fmt.Errorf("rule %d destination: %w", index, err)
			}
		} else if err := validateRewriteDestination(rule.Destination); err != nil {
			return fmt.Errorf("rule %d destination: %w", index, err)
		}
		for _, method := range rule.Methods {
			if strings.TrimSpace(method) == "" {
				return fmt.Errorf("rule %d contains an empty method condition", index)
			}
		}
		for name := range rule.Headers {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("rule %d contains an empty header condition", index)
			}
		}
		for name := range rule.Cookies {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("rule %d contains an empty cookie condition", index)
			}
		}
		for name := range rule.Query {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("rule %d contains an empty query condition", index)
			}
		}
	}
	return nil
}

func validateRewriteDestination(destination string) error {
	if strings.HasPrefix(destination, "//") || strings.ContainsAny(destination, "\r\n") {
		return errors.New("internal rewrite destination must be a same-origin path")
	}
	parsed, err := url.Parse(destination)
	if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/") || parsed.Fragment != "" {
		return errors.New("internal rewrite destination must be a same-origin path")
	}
	if strings.HasPrefix(parsed.Path, "/_gobeyond/") {
		return errors.New("internal rewrite cannot target reserved GoBeyond paths")
	}
	return nil
}

func validateRedirectDestination(destination string) error {
	if strings.ContainsAny(destination, "\r\n") {
		return errors.New("redirect destination contains invalid control characters")
	}
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Fragment != "" {
		return errors.New("redirect destination is invalid")
	}
	if parsed.IsAbs() {
		if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
			return errors.New("external redirects must use https")
		}
		return nil
	}
	if strings.HasPrefix(destination, "//") || !strings.HasPrefix(parsed.Path, "/") {
		return errors.New("redirect destination must be an absolute path or https URL")
	}
	return nil
}

type compiledRule struct {
	rule    Rule
	pattern pathPattern
}

func compileRule(rule Rule) (compiledRule, error) {
	pattern, err := parsePattern(rule.Source)
	if err != nil {
		return compiledRule{}, err
	}
	return compiledRule{rule: rule, pattern: pattern}, nil
}

// Apply evaluates redirects first, then rewrites, returning the first match.
func (p *Policy) Apply(request *http.Request) (Decision, error) {
	if p == nil || request == nil || request.URL == nil {
		return Decision{Kind: DecisionNone, RuleIndex: -1}, nil
	}
	for index, rule := range p.redirects {
		params, ok := rule.pattern.Match(request.URL.Path)
		if !ok || !matchesConditions(rule.rule, request) {
			continue
		}
		location, err := expandDestination(rule.rule.Destination, params)
		if err != nil {
			return Decision{}, err
		}
		return Decision{Kind: DecisionRedirect, Location: location, Status: rule.rule.Status, RuleIndex: index}, nil
	}
	for index, rule := range p.rewrites {
		params, ok := rule.pattern.Match(request.URL.Path)
		if !ok || !matchesConditions(rule.rule, request) {
			continue
		}
		target, err := rewriteURL(request.URL, rule.rule.Destination, params)
		if err != nil {
			return Decision{}, err
		}
		return Decision{Kind: DecisionRewrite, RewriteURL: target, RuleIndex: index}, nil
	}
	return Decision{Kind: DecisionNone, RuleIndex: -1}, nil
}

func matchesConditions(rule Rule, request *http.Request) bool {
	if len(rule.Methods) > 0 {
		matched := false
		for _, method := range rule.Methods {
			if strings.EqualFold(strings.TrimSpace(method), request.Method) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(rule.Hosts) > 0 {
		host := strings.ToLower(request.Host)
		if request.URL != nil && request.URL.Host != "" {
			host = strings.ToLower(request.URL.Host)
		}
		matched := false
		for _, candidate := range rule.Hosts {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if candidate == host || (strings.Contains(candidate, "*") && wildcardMatch(candidate, host)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for name, expected := range rule.Headers {
		if !conditionValueMatches(request.Header.Get(name), expected) {
			return false
		}
	}
	for name, expected := range rule.Cookies {
		cookie, err := request.Cookie(name)
		if err != nil || !conditionValueMatches(cookie.Value, expected) {
			return false
		}
	}
	for name, expected := range rule.Query {
		if !conditionValueMatches(request.URL.Query().Get(name), expected) {
			return false
		}
	}
	return true
}

func conditionValueMatches(value, expected string) bool {
	if expected == "*" {
		return value != ""
	}
	return value == expected
}

func wildcardMatch(pattern, value string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		return strings.HasPrefix(value, parts[0]) && strings.HasSuffix(value, parts[1]) && len(value) >= len(parts[0])+len(parts[1])
	}
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		found := strings.Index(value[position:], part)
		if found < 0 || index == len(parts)-1 && position+found+len(part) != len(value) {
			return false
		}
		position += found + len(part)
	}
	return true
}

func expandDestination(destination string, params map[string]string) (string, error) {
	for name, value := range params {
		for _, token := range []string{"[..." + name + "]", "[[..." + name + "]]", ":" + name + "*"} {
			destination = strings.ReplaceAll(destination, token, escapePath(value, true))
		}
		for _, token := range []string{"[" + name + "]", "{" + name + "}", ":" + name} {
			destination = strings.ReplaceAll(destination, token, escapePath(value, false))
		}
	}
	return destination, nil
}

func escapePath(value string, catchAll bool) string {
	if !catchAll {
		return url.PathEscape(value)
	}
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func rewriteURL(original *url.URL, destination string, params map[string]string) (*url.URL, error) {
	expanded, err := expandDestination(destination, params)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(expanded)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return nil, errors.New("internal rewrite destination must be a same-origin path")
	}
	target := *original
	target.Path = parsed.Path
	target.RawPath = parsed.RawPath
	if strings.Contains(expanded, "?") {
		target.RawQuery = parsed.RawQuery
	}
	target.Fragment = ""
	return &target, nil
}

type pathPattern struct {
	segments []pathSegment
}

type pathSegment struct {
	kind  byte
	name  string
	value string
}

func parsePattern(source string) (pathPattern, error) {
	if source == "" || !strings.HasPrefix(source, "/") || strings.ContainsAny(source, "?#\\") {
		return pathPattern{}, errors.New("source must be an absolute path")
	}
	trimmed := strings.Trim(source, "/")
	if trimmed == "" {
		return pathPattern{}, nil
	}
	parts := strings.Split(trimmed, "/")
	pattern := pathPattern{segments: make([]pathSegment, 0, len(parts))}
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return pathPattern{}, errors.New("source contains an invalid segment")
		}
		segment := pathSegment{kind: 'l', value: part}
		switch {
		case strings.HasPrefix(part, "[...") && strings.HasSuffix(part, "]"):
			segment.kind = 'c'
			segment.name = part[4 : len(part)-1]
		case strings.HasPrefix(part, "[[...") && strings.HasSuffix(part, "]]"):
			segment.kind = 'o'
			segment.name = part[5 : len(part)-2]
		case strings.HasPrefix(part, ":"):
			segment.kind = 'd'
			segment.name = strings.TrimSuffix(strings.TrimPrefix(part, ":"), "*")
			if strings.HasSuffix(part, "*") {
				segment.kind = 'c'
			}
		case strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]"):
			segment.kind = 'd'
			segment.name = part[1 : len(part)-1]
		}
		if (segment.kind == 'c' || segment.kind == 'o') && index != len(parts)-1 {
			return pathPattern{}, errors.New("catch-all source must be the final segment")
		}
		if segment.kind != 'l' && segment.name == "" {
			return pathPattern{}, errors.New("source parameter name cannot be empty")
		}
		pattern.segments = append(pattern.segments, segment)
	}
	return pattern, nil
}

func (pattern pathPattern) Match(source string) (map[string]string, bool) {
	parts, ok := splitPath(source)
	if !ok {
		return nil, false
	}
	params := make(map[string]string)
	for index, segment := range pattern.segments {
		if segment.kind == 'o' && index == len(parts) {
			params[segment.name] = ""
			return params, true
		}
		if index >= len(parts) {
			return nil, false
		}
		switch segment.kind {
		case 'l':
			if segment.value != parts[index] {
				return nil, false
			}
		case 'd':
			params[segment.name] = parts[index]
		case 'c', 'o':
			params[segment.name] = strings.Join(parts[index:], "/")
			return params, true
		}
	}
	return params, len(parts) == len(pattern.segments)
}

func splitPath(source string) ([]string, bool) {
	if source == "" || !strings.HasPrefix(source, "/") || strings.Contains(source, "\\") {
		return nil, false
	}
	trimmed := strings.Trim(source, "/")
	if trimmed == "" {
		return nil, true
	}
	parts := strings.Split(trimmed, "/")
	decoded := make([]string, len(parts))
	for index, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "." || value == ".." || strings.Contains(value, "/") {
			return nil, false
		}
		decoded[index] = value
	}
	return decoded, true
}
