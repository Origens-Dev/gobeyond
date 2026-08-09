// Package middleware composes the legacy low-level Go runtime middleware hook.
//
// Deprecated: GoBeyond applications author exactly one root middleware.ts or
// middleware.js default export. This package remains for runtime compatibility
// during the alpha line and is no longer compiler-discovered.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/router"
)

type Rule struct {
	Name       string
	Config     gb.MiddlewareConfig
	Middleware gb.Middleware
}

func Chain(rules []Rule, final gb.Handler) (gb.Handler, error) {
	if final == nil {
		return nil, errors.New("middleware final handler is required")
	}
	compiled := make([]compiledRule, len(rules))
	for i, rule := range rules {
		if rule.Name == "" || rule.Middleware == nil {
			return nil, errors.New("middleware rules require a name and implementation")
		}
		patterns := make([]router.Pattern, len(rule.Config.Patterns))
		for j, source := range rule.Config.Patterns {
			pattern, err := router.Parse(source)
			if err != nil {
				return nil, errors.New("invalid middleware matcher " + rule.Name + ": " + err.Error())
			}
			patterns[j] = pattern
		}
		compiled[i] = compiledRule{rule: rule, patterns: patterns}
	}
	return func(ctx *gb.RequestContext) (gb.Response, error) {
		handler := final
		for i := len(compiled) - 1; i >= 0; i-- {
			if compiled[i].matches(ctx.Request) {
				handler = compiled[i].rule.Middleware(handler)
			}
		}
		return handler(ctx)
	}, nil
}

type compiledRule struct {
	rule     Rule
	patterns []router.Pattern
}

func (r compiledRule) matches(request *http.Request) bool {
	if request == nil {
		return false
	}
	if len(r.rule.Config.Methods) > 0 {
		matched := false
		for _, method := range r.rule.Config.Methods {
			if strings.EqualFold(method, request.Method) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(r.patterns) == 0 {
		return true
	}
	for _, pattern := range r.patterns {
		if _, ok := pattern.Match(request.URL.Path); ok {
			return true
		}
	}
	return false
}
