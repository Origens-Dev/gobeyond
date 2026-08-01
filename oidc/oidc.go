// Package oidc provides GoBeyond workload identity token access.
//
// In a hosted slot, request-scoped tokens come from the trusted
// x-origens-oidc-token header. Background work uses the per-slot broker
// exposed through GOBEYOND_HOST_REPORT_SOCKET. Local and build environments
// may provide ORIGENS_OIDC_TOKEN or GOBEYOND_OIDC_TOKEN instead.
package oidc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	EnvTokenOrigens       = "ORIGENS_OIDC_TOKEN"
	EnvTokenGoBeyond      = "GOBEYOND_OIDC_TOKEN"
	EnvIssuerBase         = "GOBEYOND_OIDC_ISSUER_BASE_URL"
	EnvHostReportSocket   = "GOBEYOND_HOST_REPORT_SOCKET"
	HeaderOrigens         = "x-origens-oidc-token"
	HeaderGoBeyond        = "x-gobeyond-oidc-token"
	DefaultSourceAudience = "origens-platform"
	AWSTSAudience         = "sts.amazonaws.com"
)

type TokenOptions struct {
	Request  *http.Request
	Audience string
	JTI      string
}

type TokenSource struct {
	HTTPClient *http.Client
	IssuerBase string
	SocketPath string
}

type brokerResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

type exchangeRequest struct {
	Token string `json:"token"`
	Aud   string `json:"aud"`
	JTI   string `json:"jti,omitempty"`
}

type tokenContextKey struct{}

// FromRequest returns the platform-injected token from an HTTP request.
func FromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if token := strings.TrimSpace(r.Header.Get(HeaderOrigens)); token != "" {
		return token
	}
	return strings.TrimSpace(r.Header.Get(HeaderGoBeyond))
}

// GetToken returns the local/build environment token, if configured.
func GetToken() string {
	if token := strings.TrimSpace(os.Getenv(EnvTokenOrigens)); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv(EnvTokenGoBeyond))
}

// ContextWithToken attaches a token to a request/work item context. It is
// useful for GoBeyond handlers that have already extracted the trusted request
// header and then call a background helper in the same operation.
func ContextWithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, strings.TrimSpace(token))
}

// TokenFromContext returns a token attached with ContextWithToken.
func TokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(tokenContextKey{}).(string)
	return strings.TrimSpace(token)
}

// Token resolves a request, environment, or hosted-slot token and optionally
// exchanges it for a downstream audience.
func (s *TokenSource) Token(ctx context.Context, options TokenOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	token := FromRequest(options.Request)
	if token == "" {
		token = TokenFromContext(ctx)
	}
	if token == "" {
		token = GetToken()
	}
	if token == "" {
		var err error
		token, err = s.brokerToken(ctx)
		if err != nil {
			return "", err
		}
	}
	audience := strings.TrimSpace(options.Audience)
	if audience == "" || audience == DefaultSourceAudience {
		return token, nil
	}
	issuer := s.issuerBase()
	if issuer == "" {
		return "", errors.New("gobeyond oidc: issuer base URL is required for audience exchange")
	}
	return exchangeAudience(ctx, s.httpClient(), issuer, token, audience, options.JTI)
}

// WebIdentityTokenForAWS returns an STS-compatible token for AWS web identity
// federation. The caller supplies it to AssumeRoleWithWebIdentity.
func (s *TokenSource) WebIdentityTokenForAWS(ctx context.Context, request *http.Request) (string, error) {
	return s.Token(ctx, TokenOptions{Request: request, Audience: AWSTSAudience})
}

func (s *TokenSource) issuerBase() string {
	if s != nil {
		if value := strings.TrimRight(strings.TrimSpace(s.IssuerBase), "/"); value != "" {
			return value
		}
	}
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvIssuerBase)), "/"); value != "" {
		return value
	}
	return ""
}

func (s *TokenSource) socketPath() string {
	if s != nil {
		if value := strings.TrimSpace(s.SocketPath); value != "" {
			return value
		}
	}
	return strings.TrimSpace(os.Getenv(EnvHostReportSocket))
}

func (s *TokenSource) brokerToken(ctx context.Context) (string, error) {
	socketPath := s.socketPath()
	if socketPath == "" {
		return "", errors.New("gobeyond oidc: no request token, environment token, or slot broker configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobeyond/v1/oidc/token", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", fmt.Errorf("gobeyond oidc: build broker request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("gobeyond oidc: broker request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("gobeyond oidc: broker status %d", response.StatusCode)
	}
	var result brokerResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("gobeyond oidc: decode broker response: %w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", errors.New("gobeyond oidc: broker returned an empty token")
	}
	return result.Token, nil
}

func (s *TokenSource) httpClient() *http.Client {
	if s != nil && s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}

func exchangeAudience(ctx context.Context, client *http.Client, issuer, token, audience, jti string) (string, error) {
	body, err := json.Marshal(exchangeRequest{Token: token, Aud: audience, JTI: strings.TrimSpace(jti)})
	if err != nil {
		return "", fmt.Errorf("gobeyond oidc: encode exchange request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(issuer, "/")+"/~token", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("gobeyond oidc: build exchange request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("gobeyond oidc: exchange request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("gobeyond oidc: exchange status %d", response.StatusCode)
	}
	var result brokerResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("gobeyond oidc: decode exchange response: %w", err)
	}
	if strings.TrimSpace(result.Token) == "" {
		return "", errors.New("gobeyond oidc: exchange returned an empty token")
	}
	return result.Token, nil
}

// TokenExpiry reads exp for cache/refresh decisions without treating the
// unsigned payload as an authentication decision.
func TokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.ExpiresAt <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.ExpiresAt, 0), true
}
