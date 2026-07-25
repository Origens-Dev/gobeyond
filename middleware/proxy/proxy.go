// Package proxy implements the gobeyond.builds/v2 middleware artifact:
// a separately-runnable reverse proxy that sits between the hosting
// supervisor and the app server (gobeyond-internal data-plane contracts
// §7). Per request it either responds itself (redirects, denials,
// synthetic responses) or forwards a possibly-rewritten request to the
// upstream app socket.
//
// Wiring (§7.3):
//
//	supervisor -> GOBEYOND_LISTEN   (middleware ingress, unix socket)
//	middleware -> GOBEYOND_UPSTREAM (app socket, unix:///run/gobeyond/app.sock)
//
// Header contract on the middleware -> app hop:
//
//   - Preserved verbatim: x-gobeyond-viewer-host, x-forwarded-proto,
//     x-forwarded-for, x-origens-oidc-token. The proxy restores the
//     inbound values after the Middleware hook runs, so they cannot be
//     rewritten or dropped.
//   - Mutable: method, path (+query), body, and all other request
//     headers; rewrites are expressed by forwarding a modified request.
//   - Additive auth context: x-gobeyond-auth-context may be set only by
//     the middleware (base64url-encoded JSON, <= 8 KiB); any inbound
//     value is stripped before the hook runs.
package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"os"

	"github.com/Origens-Dev/gobeyond/adapters/listen"
)

const (
	// EnvUpstream is the app socket the middleware forwards to
	// (unix://<absolute path> or tcp://<host:port>).
	EnvUpstream = "GOBEYOND_UPSTREAM"
	// AuthContextHeader carries middleware-asserted auth context to the
	// app. It is additive and set exclusively by the middleware.
	AuthContextHeader = "X-Gobeyond-Auth-Context"
	// MaxAuthContextBytes caps the encoded auth-context header value.
	MaxAuthContextBytes = 8 << 10
)

// PreservedHeaders cross the middleware->app hop verbatim.
var PreservedHeaders = []string{
	"X-Gobeyond-Viewer-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-For",
	"X-Origens-Oidc-Token",
}

// Middleware decides one request. Returning a non-nil Response answers
// the request without contacting the app (redirects, denials, synthetic
// bodies). Returning nil forwards the request - including any mutations
// made to it - upstream.
type Middleware func(*http.Request) (*Response, error)

// Response is a synthetic middleware response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Handler is the middleware reverse proxy.
type Handler struct {
	middleware Middleware
	reverse    *httputil.ReverseProxy
}

// New builds a Handler forwarding to upstream (unix://path or
// tcp://host:port). middleware may be nil for a pass-through proxy.
func New(upstream string, middleware Middleware) (*Handler, error) {
	network, address, err := listen.ParseTarget(upstream)
	if err != nil {
		return nil, fmt.Errorf("middleware upstream: %w", err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}
	reverse := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			// The transport dials the configured socket; the URL host is
			// never resolved. The Host header passes through unchanged.
			request.Out.URL.Host = "gobeyond-app"
			request.Out.Host = request.In.Host
			// ReverseProxy.Rewrite strips inbound X-Forwarded-*; the hop
			// contract preserves them verbatim instead.
			for _, name := range PreservedHeaders {
				if values := request.In.Header.Values(name); len(values) > 0 {
					request.Out.Header[textproto.CanonicalMIMEHeaderKey(name)] = values
				}
			}
			if values := request.In.Header.Values(AuthContextHeader); len(values) > 0 {
				request.Out.Header[textproto.CanonicalMIMEHeaderKey(AuthContextHeader)] = values
			}
		},
		Transport: transport,
	}
	return &Handler{middleware: middleware, reverse: reverse}, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	// Only the middleware may assert auth context; drop anything inbound.
	request.Header.Del(AuthContextHeader)

	// Snapshot the preserved headers before user code runs.
	preserved := make(map[string][]string, len(PreservedHeaders))
	for _, name := range PreservedHeaders {
		key := textproto.CanonicalMIMEHeaderKey(name)
		if values, ok := request.Header[key]; ok {
			preserved[key] = append([]string(nil), values...)
		}
	}

	if h.middleware != nil {
		response, err := h.middleware(request)
		if err != nil {
			http.Error(writer, "middleware error", http.StatusInternalServerError)
			return
		}
		if response != nil {
			writeSynthetic(writer, response)
			return
		}
	}

	// Preserved headers cross the hop verbatim regardless of middleware
	// edits: restore the inbound values (including inbound absence).
	for _, name := range PreservedHeaders {
		key := textproto.CanonicalMIMEHeaderKey(name)
		delete(request.Header, key)
		if values, ok := preserved[key]; ok {
			request.Header[key] = values
		}
	}

	if value := request.Header.Get(AuthContextHeader); value != "" {
		if err := ValidateAuthContext(value); err != nil {
			http.Error(writer, "invalid auth context", http.StatusInternalServerError)
			return
		}
	}

	h.reverse.ServeHTTP(writer, request)
}

func writeSynthetic(writer http.ResponseWriter, response *Response) {
	for name, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(response.Body)
}

// SetAuthContext encodes value as base64url JSON and sets it on header,
// enforcing the <= 8 KiB encoded budget.
func SetAuthContext(header http.Header, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode auth context: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > MaxAuthContextBytes {
		return fmt.Errorf("auth context exceeds %d bytes encoded", MaxAuthContextBytes)
	}
	header.Set(AuthContextHeader, encoded)
	return nil
}

// ValidateAuthContext checks a candidate x-gobeyond-auth-context value:
// base64url (padded or unpadded) decoding to JSON, <= 8 KiB encoded.
func ValidateAuthContext(value string) error {
	if len(value) > MaxAuthContextBytes {
		return fmt.Errorf("auth context exceeds %d bytes encoded", MaxAuthContextBytes)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		if decoded, err = base64.URLEncoding.DecodeString(value); err != nil {
			return errors.New("auth context is not base64url")
		}
	}
	if !json.Valid(decoded) {
		return errors.New("auth context is not JSON")
	}
	return nil
}

// Serve runs middleware as the gobeyond-middleware artifact: it forwards
// to GOBEYOND_UPSTREAM and serves the listen contract (GOBEYOND_LISTEN,
// /_gobeyond/healthz, SIGTERM drain) via adapters/listen.
func Serve(middleware Middleware) error {
	upstream := os.Getenv(EnvUpstream)
	if upstream == "" {
		return errors.New(EnvUpstream + " is required for the middleware artifact")
	}
	handler, err := New(upstream, middleware)
	if err != nil {
		return err
	}
	return listen.Serve(handler)
}
