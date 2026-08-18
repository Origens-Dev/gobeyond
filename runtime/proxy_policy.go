package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/policy"
)

type proxyPolicyResult struct {
	request  *http.Request
	location string
	status   int
}

// ProxyPolicyHandler applies the immutable build policy before the wrapped
// origin handler, including when that handler is StaticFiles. It is a
// platform/origin routing concern, not the authored Go middleware hook.
// Reserved GoBeyond paths remain outside customer policy.
func ProxyPolicyHandler(proxyPolicy *gb.ProxyPolicy, next http.Handler) http.Handler {
	if proxyPolicy == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		result, err := evaluateProxyPolicy(proxyPolicy, request)
		if err != nil {
			writeProxyPolicyError(writer, http.StatusServiceUnavailable, "proxy_policy_error", request)
			return
		}
		if result.location != "" {
			writer.Header().Set("Location", result.location)
			writer.Header().Set("Cache-Control", "no-store")
			writer.WriteHeader(result.status)
			return
		}
		next.ServeHTTP(writer, result.request)
	})
}

func evaluateProxyPolicy(proxyPolicy *gb.ProxyPolicy, request *http.Request) (proxyPolicyResult, error) {
	if proxyPolicy == nil || request == nil || request.URL == nil ||
		strings.HasPrefix(request.URL.Path, "/_gobeyond/") ||
		strings.HasPrefix(request.URL.Path, "/__gobeyond/") {
		return proxyPolicyResult{request: request}, nil
	}
	current := request
	seen := map[string]struct{}{}
	for hop := 0; hop <= maxRewrites; hop++ {
		key := current.Method + " " + current.URL.RequestURI()
		if _, exists := seen[key]; exists {
			return proxyPolicyResult{}, errors.New("proxy policy rewrite loop")
		}
		seen[key] = struct{}{}
		decision, err := proxyPolicy.Apply(current)
		if err != nil {
			return proxyPolicyResult{}, err
		}
		switch decision.Kind {
		case policy.DecisionNone:
			return proxyPolicyResult{request: current}, nil
		case policy.DecisionRedirect:
			return proxyPolicyResult{location: decision.Location, status: decision.Status}, nil
		case policy.DecisionRewrite:
			if decision.RewriteURL == nil {
				return proxyPolicyResult{}, errors.New("proxy policy returned a nil rewrite URL")
			}
			next := current.Clone(current.Context())
			next.URL = decision.RewriteURL
			current = next
		default:
			return proxyPolicyResult{}, errors.New("proxy policy returned an unknown decision")
		}
	}
	return proxyPolicyResult{}, errors.New("proxy policy rewrite limit exceeded")
}

func writeProxyPolicyError(writer http.ResponseWriter, status int, code string, request *http.Request) {
	requestID := ""
	if request != nil {
		requestID = request.Header.Get("X-Request-ID")
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": code, "requestId": requestID})
}
