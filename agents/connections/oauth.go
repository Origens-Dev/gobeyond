package connections

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrUnauthorized = errors.New("connection authorization denied")
	ErrInvalidGrant = errors.New("invalid connection grant")
	ErrReplay       = errors.New("connection credential replay")
	ErrUnavailable  = errors.New("connection service unavailable")
	ErrNotFound     = errors.New("connection credential not found")
)

// Client registrations are explicit and environment-specific. SecretHash is
// HashSecret(clientSecret); clients must use random secrets of at least 32 bytes.
type Client struct {
	ID           string
	Tenant       Tenant
	SecretHash   string
	RedirectURIs []string
}

type ClientRegistry interface {
	Client(context.Context, string) (Client, error)
}

type GrantRequest struct {
	Resource     Resource
	AgentID      string
	Capabilities []string
	Callers      []Caller
}

// GrantPolicy owns resource consent and current authority. Authorize must verify
// the authenticated owner and consumer-resolved caller list, persist the grant,
// and never derive authority from a requested owner ID or caller display name.
// Resolve must load current durable grant state and current owner authority.
type GrantPolicy interface {
	Authorize(context.Context, string, Client, GrantRequest) (Grant, error)
	Resolve(context.Context, string) (Grant, error)
}

type AuthorizationRequest struct {
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Grant               GrantRequest
}
type AuthorizationResponse struct {
	RedirectURI string
	Code        string
	State       string
}
type AuthorizationCode struct {
	CodeHash      string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	GrantID       string
	GrantVersion  uint64
	ExpiresAt     time.Time
}
type TokenRecord struct {
	AccessHash       string
	RefreshHash      string
	FamilyID         string
	ClientID         string
	GrantID          string
	GrantVersion     uint64
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	RotatedAt        time.Time
	RevokedAt        time.Time
}

// OAuthStore is provider-local durable storage. ConsumeCode must atomically
// require an unconsumed, unexpired code and insert the token record. RotateToken
// must atomically require the current unrotated/unrevoked/unexpired refresh token,
// retain its hash as a replay tombstone, and insert its successor in the SAME
// family. Both return ErrReplay when another request has already consumed it.
// RevokeFamily invalidates every access/refresh token in that family, including
// future lookups of retained rotated tokens. Never implement with read+write
// without a transaction/CAS. Raw codes and tokens must not be persisted.
// LoadAccessToken and LoadRefreshToken must return ErrNotFound only for an
// absent token; transport/database failures must remain distinguishable errors.
type OAuthStore interface {
	SaveCode(context.Context, AuthorizationCode) error
	LoadCode(context.Context, string) (AuthorizationCode, error)
	ConsumeCode(context.Context, string, TokenRecord, time.Time) error
	LoadAccessToken(context.Context, string) (TokenRecord, error)
	LoadRefreshToken(context.Context, string) (TokenRecord, error)
	RotateToken(context.Context, string, TokenRecord, time.Time) error
	RevokeFamily(context.Context, string, time.Time) error
}

type Provider struct {
	Tenant     Tenant
	Clients    ClientRegistry
	Policy     GrantPolicy
	Store      OAuthStore
	Now        func() time.Time
	CodeTTL    time.Duration
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	GrantID      string `json:"grant_id"`
	GrantVersion uint64 `json:"grant_version"`
}

func HashSecret(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func secret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	return true
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (p *Provider) clock() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
func duration(value, fallback, maximum time.Duration) time.Duration {
	if value <= 0 || value > maximum {
		return fallback
	}
	return value
}
func (p *Provider) ready() bool {
	return p != nil && p.Tenant.Validate() == nil && p.Clients != nil && p.Policy != nil && p.Store != nil
}
func (p *Provider) client(ctx context.Context, id string) (Client, error) {
	if !p.ready() || !identifier(id) {
		return Client{}, ErrUnavailable
	}
	c, err := p.Clients.Client(ctx, id)
	if err != nil || c.ID != id || c.Tenant.Validate() != nil || len(c.SecretHash) != 64 || len(c.RedirectURIs) == 0 {
		return Client{}, ErrUnauthorized
	}
	for _, uri := range c.RedirectURIs {
		if !validRedirect(uri) {
			return Client{}, ErrUnauthorized
		}
	}
	return c, nil
}
func (p *Provider) authenticate(ctx context.Context, id, clientSecret string) (Client, error) {
	c, err := p.client(ctx, id)
	if err != nil || len(clientSecret) < 32 || !equal(c.SecretHash, HashSecret(clientSecret)) {
		return Client{}, ErrUnauthorized
	}
	return c, nil
}

// Authorize runs only after the provider has authenticated the owner and shown
// consent. It returns a redirect exclusively from the registered client allowlist.
func (p *Provider) Authorize(ctx context.Context, ownerID string, req AuthorizationRequest) (AuthorizationResponse, error) {
	c, err := p.client(ctx, req.ClientID)
	if err != nil {
		return AuthorizationResponse{}, err
	}
	challenge, err := base64.RawURLEncoding.DecodeString(req.CodeChallenge)
	if !identifier(ownerID) || !contains(c.RedirectURIs, req.RedirectURI) || req.CodeChallengeMethod != "S256" || err != nil || len(challenge) != 32 || len(req.CodeChallenge) != 43 || !text(req.State, 512) || req.Grant.Resource.Validate() != nil || !identifier(req.Grant.AgentID) || validateCapabilities(req.Grant.Capabilities) != nil || len(req.Grant.Callers) == 0 || len(req.Grant.Callers) > 1000 {
		return AuthorizationResponse{}, ErrUnauthorized
	}
	for _, caller := range req.Grant.Callers {
		if caller.Validate() != nil || caller.Kind == "user" {
			return AuthorizationResponse{}, ErrUnauthorized
		}
	}
	g, err := p.Policy.Authorize(ctx, ownerID, c, req.Grant)
	if err != nil {
		return AuthorizationResponse{}, ErrUnauthorized
	}
	if !g.Active(p.clock()) || g.OwnerID != ownerID || g.Consumer != c.Tenant || g.Provider != p.Tenant || g.Resource != req.Grant.Resource || g.AgentID != req.Grant.AgentID || !sameStrings(g.Capabilities, req.Grant.Capabilities) || !sameCallers(g.Callers, req.Grant.Callers) {
		return AuthorizationResponse{}, ErrUnauthorized
	}
	code, err := secret()
	if err != nil {
		return AuthorizationResponse{}, err
	}
	expires := p.clock().Add(duration(p.CodeTTL, 2*time.Minute, 10*time.Minute))
	if g.ExpiresAt.Before(expires) {
		expires = g.ExpiresAt
	}
	if err := p.Store.SaveCode(ctx, AuthorizationCode{CodeHash: HashSecret(code), ClientID: c.ID, RedirectURI: req.RedirectURI, CodeChallenge: req.CodeChallenge, GrantID: g.ID, GrantVersion: g.Version, ExpiresAt: expires}); err != nil {
		return AuthorizationResponse{}, err
	}
	u, _ := url.Parse(req.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	q.Set("state", req.State)
	u.RawQuery = q.Encode()
	return AuthorizationResponse{RedirectURI: u.String(), Code: code, State: req.State}, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !contains(b, v) {
			return false
		}
	}
	return true
}
func sameCallers(a, b []Caller) bool {
	if len(a) != len(b) {
		return false
	}
	for _, c := range a {
		found := false
		for _, other := range b {
			if other == c {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (p *Provider) current(ctx context.Context, client Client, grantID string, version uint64) (Grant, error) {
	g, err := p.Policy.Resolve(ctx, grantID)
	if err != nil || !g.Active(p.clock()) || g.ID != grantID || g.Version != version || g.Consumer != client.Tenant || g.Provider != p.Tenant {
		return Grant{}, ErrInvalidGrant
	}
	return g, nil
}
func (p *Provider) tokens(c Client, g Grant, family string, refreshExpiry time.Time) (TokenRecord, TokenResponse, error) {
	access, err := secret()
	if err != nil {
		return TokenRecord{}, TokenResponse{}, err
	}
	refresh, err := secret()
	if err != nil {
		return TokenRecord{}, TokenResponse{}, err
	}
	if family == "" {
		family, err = secret()
		if err != nil {
			return TokenRecord{}, TokenResponse{}, err
		}
	}
	now := p.clock()
	expires := now.Add(duration(p.AccessTTL, 5*time.Minute, time.Hour))
	if refreshExpiry.IsZero() {
		refreshExpiry = now.Add(duration(p.RefreshTTL, 30*24*time.Hour, 90*24*time.Hour))
	}
	if g.ExpiresAt.Before(refreshExpiry) {
		refreshExpiry = g.ExpiresAt
	}
	if refreshExpiry.Before(expires) {
		expires = refreshExpiry
	}
	record := TokenRecord{AccessHash: HashSecret(access), RefreshHash: HashSecret(refresh), FamilyID: family, ClientID: c.ID, GrantID: g.ID, GrantVersion: g.Version, ExpiresAt: expires, RefreshExpiresAt: refreshExpiry}
	return record, TokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(expires.Sub(now) / time.Second), RefreshToken: refresh, GrantID: g.ID, GrantVersion: g.Version}, nil
}

func (p *Provider) ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI, verifier string) (TokenResponse, error) {
	c, err := p.authenticate(ctx, clientID, clientSecret)
	if err != nil {
		return TokenResponse{}, err
	}
	if !validVerifier(verifier) || len(code) != 43 || !contains(c.RedirectURIs, redirectURI) {
		return TokenResponse{}, ErrInvalidGrant
	}
	hash := HashSecret(code)
	a, err := p.Store.LoadCode(ctx, hash)
	if err != nil || a.CodeHash != hash || a.ClientID != c.ID || a.RedirectURI != redirectURI || !p.clock().Before(a.ExpiresAt) || !equal(a.CodeChallenge, PKCEChallenge(verifier)) {
		return TokenResponse{}, ErrInvalidGrant
	}
	g, err := p.current(ctx, c, a.GrantID, a.GrantVersion)
	if err != nil {
		return TokenResponse{}, err
	}
	record, response, err := p.tokens(c, g, "", time.Time{})
	if err != nil {
		return TokenResponse{}, err
	}
	if err := p.Store.ConsumeCode(ctx, hash, record, p.clock()); err != nil {
		if errors.Is(err, ErrReplay) {
			return TokenResponse{}, ErrInvalidGrant
		}
		return TokenResponse{}, err
	}
	return response, nil
}

func (p *Provider) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (TokenResponse, error) {
	c, err := p.authenticate(ctx, clientID, clientSecret)
	if err != nil {
		return TokenResponse{}, err
	}
	if len(refreshToken) != 43 {
		return TokenResponse{}, ErrInvalidGrant
	}
	hash := HashSecret(refreshToken)
	old, err := p.Store.LoadRefreshToken(ctx, hash)
	if err != nil || old.RefreshHash != hash || old.ClientID != c.ID || old.FamilyID == "" || !old.RevokedAt.IsZero() || !p.clock().Before(old.RefreshExpiresAt) {
		return TokenResponse{}, ErrInvalidGrant
	}
	if !old.RotatedAt.IsZero() {
		if err := p.Store.RevokeFamily(ctx, old.FamilyID, p.clock()); err != nil {
			return TokenResponse{}, err
		}
		return TokenResponse{}, ErrReplay
	}
	g, err := p.current(ctx, c, old.GrantID, old.GrantVersion)
	if err != nil {
		return TokenResponse{}, err
	}
	record, response, err := p.tokens(c, g, old.FamilyID, old.RefreshExpiresAt)
	if err != nil {
		return TokenResponse{}, err
	}
	if err := p.Store.RotateToken(ctx, hash, record, p.clock()); err != nil {
		if errors.Is(err, ErrReplay) {
			if revokeErr := p.Store.RevokeFamily(ctx, old.FamilyID, p.clock()); revokeErr != nil {
				return TokenResponse{}, revokeErr
			}
			return TokenResponse{}, ErrReplay
		}
		return TokenResponse{}, err
	}
	return response, nil
}

// ResolveAccessToken authenticates a confidential-client token against current
// provider-local grant state. The consumer tenant must come from authenticated
// platform identity, never a request body. Tool policy still checks the caller.
func (p *Provider) ResolveAccessToken(ctx context.Context, accessToken string, consumer Tenant) (Grant, error) {
	if !p.ready() || consumer.Validate() != nil || len(accessToken) != 43 {
		return Grant{}, ErrUnauthorized
	}
	hash := HashSecret(accessToken)
	token, err := p.Store.LoadAccessToken(ctx, hash)
	if err != nil || token.AccessHash != hash || !token.RevokedAt.IsZero() || !token.RotatedAt.IsZero() || !p.clock().Before(token.ExpiresAt) {
		return Grant{}, ErrUnauthorized
	}
	c, err := p.client(ctx, token.ClientID)
	if err != nil || c.Tenant != consumer {
		return Grant{}, ErrUnauthorized
	}
	return p.current(ctx, c, token.GrantID, token.GrantVersion)
}

func (p *Provider) Revoke(ctx context.Context, clientID, clientSecret, rawToken string) error {
	c, err := p.authenticate(ctx, clientID, clientSecret)
	if err != nil {
		return err
	}
	if len(rawToken) != 43 {
		return nil
	}
	hash := HashSecret(rawToken)
	t, err := p.Store.LoadRefreshToken(ctx, hash)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		t, err = p.Store.LoadAccessToken(ctx, hash)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if t.ClientID != c.ID {
		return nil
	}
	return p.Store.RevokeFamily(ctx, t.FamilyID, p.clock())
}

// TokenHandler is a bounded confidential-client OAuth token endpoint. Apps own
// route registration and the consent/login UI; no browser login is implemented
// by this package. Credentials are accepted through HTTP Basic only.
func (p *Provider) TokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Type", "application/json")
		fail := func(code int, message string) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			fail(http.StatusMethodNotAllowed, "invalid_request")
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/x-www-form-urlencoded" {
			fail(http.StatusBadRequest, "invalid_request")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "invalid_request")
			return
		}
		for key, values := range r.PostForm {
			if len(values) != 1 || !contains([]string{"grant_type", "code", "redirect_uri", "code_verifier", "refresh_token"}, key) {
				fail(http.StatusBadRequest, "invalid_request")
				return
			}
		}
		clientID, secret, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="agent-connections"`)
			fail(http.StatusUnauthorized, "invalid_client")
			return
		}
		clientID, idErr := url.QueryUnescape(clientID)
		secret, secretErr := url.QueryUnescape(secret)
		if idErr != nil || secretErr != nil {
			fail(http.StatusUnauthorized, "invalid_client")
			return
		}
		var response TokenResponse
		var err error
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			response, err = p.ExchangeCode(r.Context(), clientID, secret, r.PostForm.Get("code"), r.PostForm.Get("redirect_uri"), r.PostForm.Get("code_verifier"))
		case "refresh_token":
			response, err = p.Refresh(r.Context(), clientID, secret, r.PostForm.Get("refresh_token"))
		default:
			fail(http.StatusBadRequest, "unsupported_grant_type")
			return
		}
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				fail(http.StatusUnauthorized, "invalid_client")
			} else if errors.Is(err, ErrInvalidGrant) || errors.Is(err, ErrReplay) {
				fail(http.StatusBadRequest, "invalid_grant")
			} else {
				fail(http.StatusServiceUnavailable, "temporarily_unavailable")
			}
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	})
}
