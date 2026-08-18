// Package middleware composes GoBeyond's low-level Go runtime middleware hook.
//
// Rule and Chain remain useful for applications that want to compose named,
// matcher-aware functions inside the one compiler-discovered root hook.
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

// MustChain is the startup-time form of Chain for authored middleware. The
// matcher configuration is compile-time application code, so an invalid rule
// is a programmer error and should stop the process rather than become a
// request-time authorization bypass.
func MustChain(rules []Rule, final gb.Handler) gb.Handler {
	chain, err := Chain(rules, final)
	if err != nil {
		panic(err)
	}
	return chain
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
