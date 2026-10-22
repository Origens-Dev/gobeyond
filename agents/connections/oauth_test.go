package connections

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testRegistry struct{ client Client }

func (r testRegistry) Client(_ context.Context, id string) (Client, error) {
	if id != r.client.ID {
		return Client{}, ErrUnauthorized
	}
	return r.client, nil
}

type testPolicy struct {
	grant       Grant
	ownerActive bool
}

func (p *testPolicy) Authorize(_ context.Context, owner string, _ Client, _ GrantRequest) (Grant, error) {
	if !p.ownerActive || owner != p.grant.OwnerID {
		return Grant{}, ErrUnauthorized
	}
	return p.grant, nil
}
func (p *testPolicy) Resolve(_ context.Context, id string) (Grant, error) {
	if !p.ownerActive || id != p.grant.ID {
		return Grant{}, ErrInvalidGrant
	}
	return p.grant, nil
}

type testOAuthStore struct {
	mu      sync.Mutex
	codes   map[string]AuthorizationCode
	used    map[string]bool
	access  map[string]*TokenRecord
	refresh map[string]*TokenRecord
}

func newTestStore() *testOAuthStore {
	return &testOAuthStore{codes: map[string]AuthorizationCode{}, used: map[string]bool{}, access: map[string]*TokenRecord{}, refresh: map[string]*TokenRecord{}}
}
func (s *testOAuthStore) SaveCode(_ context.Context, code AuthorizationCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.codes[code.CodeHash]; exists {
		return ErrReplay
	}
	s.codes[code.CodeHash] = code
	return nil
}
func (s *testOAuthStore) LoadCode(_ context.Context, hash string) (AuthorizationCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[hash]
	if !ok || s.used[hash] {
		return AuthorizationCode{}, ErrInvalidGrant
	}
	return c, nil
}
func (s *testOAuthStore) ConsumeCode(_ context.Context, hash string, token TokenRecord, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[hash]
	if !ok || s.used[hash] || !now.Before(c.ExpiresAt) {
		return ErrReplay
	}
	s.used[hash] = true
	s.access[token.AccessHash] = &token
	s.refresh[token.RefreshHash] = &token
	return nil
}
func (s *testOAuthStore) LoadAccessToken(_ context.Context, hash string) (TokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.access[hash]
	if !ok {
		return TokenRecord{}, ErrNotFound
	}
	return *t, nil
}
func (s *testOAuthStore) LoadRefreshToken(_ context.Context, hash string) (TokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.refresh[hash]
	if !ok {
		return TokenRecord{}, ErrNotFound
	}
	return *t, nil
}
func (s *testOAuthStore) RotateToken(_ context.Context, hash string, token TokenRecord, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.refresh[hash]
	if !ok || !old.RotatedAt.IsZero() || !old.RevokedAt.IsZero() || !now.Before(old.RefreshExpiresAt) {
		return ErrReplay
	}
	old.RotatedAt = now
	s.access[token.AccessHash] = &token
	s.refresh[token.RefreshHash] = &token
	return nil
}
func (s *testOAuthStore) RevokeFamily(_ context.Context, family string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.access {
		if token.FamilyID == family {
			token.RevokedAt = now
		}
	}
	return nil
}

func fixture(t *testing.T) (*Provider, *testPolicy, AuthorizationRequest, string) {
	t.Helper()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	consumer := Tenant{OrganizationID: "consumer-org", ProjectID: "candlestick", EnvironmentID: "prod"}
	provider := Tenant{OrganizationID: "provider-org", ProjectID: "seguridade", EnvironmentID: "prod"}
	caller := Caller{Kind: "line", ID: "line-1", NetworkID: "family", AssignmentGeneration: 3}
	policy := &testPolicy{ownerActive: true, grant: Grant{ID: "grant-1", Version: 1, OwnerID: "owner-1", Consumer: consumer, Provider: provider, Resource: Resource{Type: "home", ID: "cedar"}, AgentID: "home", Capabilities: []string{"read", "configure"}, Callers: []Caller{caller}, ExpiresAt: now.Add(24 * time.Hour)}}
	secret := strings.Repeat("s", 43)
	registry := testRegistry{client: Client{ID: "client-1", Tenant: consumer, SecretHash: HashSecret(secret), RedirectURIs: []string{"https://consumer.example/callback"}}}
	p := &Provider{Tenant: provider, Clients: registry, Policy: policy, Store: newTestStore(), Now: func() time.Time { return now }}
	verifier := strings.Repeat("v", 43)
	req := AuthorizationRequest{ClientID: "client-1", RedirectURI: "https://consumer.example/callback", State: strings.Repeat("x", 43), CodeChallenge: PKCEChallenge(verifier), CodeChallengeMethod: "S256", Grant: GrantRequest{Resource: policy.grant.Resource, AgentID: "home", Capabilities: policy.grant.Capabilities, Callers: policy.grant.Callers}}
	return p, policy, req, secret
}
func authorize(t *testing.T, p *Provider, req AuthorizationRequest) AuthorizationResponse {
	t.Helper()
	r, err := p.Authorize(context.Background(), "owner-1", req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAuthorizeRequiresExactRegisteredRedirectAndS256(t *testing.T) {
	p, _, req, _ := fixture(t)
	for _, redirect := range []string{"https://consumer.example/callback?next=evil", "https://consumer.example/callback/", "https://evil.example/callback", "http://consumer.example/callback"} {
		bad := req
		bad.RedirectURI = redirect
		if _, err := p.Authorize(context.Background(), "owner-1", bad); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("redirect %s accepted: %v", redirect, err)
		}
	}
	bad := req
	bad.CodeChallengeMethod = "plain"
	if _, err := p.Authorize(context.Background(), "owner-1", bad); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("plain PKCE accepted")
	}
	r := authorize(t, p, req)
	u, err := url.Parse(r.RedirectURI)
	if err != nil || u.Query().Get("state") != req.State || u.Query().Get("code") != r.Code {
		t.Fatalf("bad registered redirect: %s", r.RedirectURI)
	}
}

func TestCodeExchangeBindsClientRedirectAndVerifier(t *testing.T) {
	p, _, req, secret := fixture(t)
	r := authorize(t, p, req)
	for _, args := range [][3]string{{secret, req.RedirectURI, strings.Repeat("w", 43)}, {secret, "https://consumer.example/other", strings.Repeat("v", 43)}, {strings.Repeat("bad", 20), req.RedirectURI, strings.Repeat("v", 43)}} {
		if _, err := p.ExchangeCode(context.Background(), req.ClientID, args[0], r.Code, args[1], args[2]); err == nil {
			t.Fatal("invalid credential exchange succeeded")
		}
	}
	token, err := p.ExchangeCode(context.Background(), req.ClientID, secret, r.Code, req.RedirectURI, strings.Repeat("v", 43))
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.GrantID != "grant-1" {
		t.Fatal("missing token binding")
	}
	if _, err := p.ExchangeCode(context.Background(), req.ClientID, secret, r.Code, req.RedirectURI, strings.Repeat("v", 43)); err == nil {
		t.Fatal("replayed code accepted")
	}
}

func TestCodeConsumedExactlyOnceUnderConcurrentExchange(t *testing.T) {
	p, _, req, secret := fixture(t)
	r := authorize(t, p, req)
	var successes atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.ExchangeCode(context.Background(), req.ClientID, secret, r.Code, req.RedirectURI, strings.Repeat("v", 43)); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("exchanged %d times", successes.Load())
	}
}

func TestRefreshRotationReplayRevokesSuccessorAndOwnerLossDenies(t *testing.T) {
	p, policy, req, secret := fixture(t)
	r := authorize(t, p, req)
	old, err := p.ExchangeCode(context.Background(), req.ClientID, secret, r.Code, req.RedirectURI, strings.Repeat("v", 43))
	if err != nil {
		t.Fatal(err)
	}
	next, err := p.Refresh(context.Background(), req.ClientID, secret, old.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ResolveAccessToken(context.Background(), old.AccessToken, policy.grant.Consumer); err == nil {
		t.Fatal("rotated access token accepted")
	}
	if _, err := p.ResolveAccessToken(context.Background(), next.AccessToken, policy.grant.Consumer); err != nil {
		t.Fatal(err)
	}
	policy.ownerActive = false
	if _, err := p.ResolveAccessToken(context.Background(), next.AccessToken, policy.grant.Consumer); err == nil {
		t.Fatal("former owner authority accepted")
	}
	policy.ownerActive = true
	if _, err := p.Refresh(context.Background(), req.ClientID, secret, old.RefreshToken); !errors.Is(err, ErrReplay) {
		t.Fatalf("expected replay, got %v", err)
	}
	if _, err := p.ResolveAccessToken(context.Background(), next.AccessToken, policy.grant.Consumer); err == nil {
		t.Fatal("refresh replay did not revoke successor family")
	}
}

func TestTokenEndpointRejectsQueryCredentialsAndDuplicateForm(t *testing.T) {
	p, _, _, _ := fixture(t)
	for _, body := range []string{"grant_type=refresh_token&refresh_token=x", "grant_type=refresh_token&grant_type=authorization_code"} {
		r := httptest.NewRequest(http.MethodPost, "/token?client_id=client-1&client_secret=secret", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		p.TokenHandler().ServeHTTP(w, r)
		if w.Code == http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("accepted bad token form: %d", w.Code)
		}
	}
}

type testStateStore struct {
	mu     sync.Mutex
	states map[string]PendingAuthorization
}

func (s *testStateStore) Save(_ context.Context, p PendingAuthorization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[p.StateHash] = p
	return nil
}
func (s *testStateStore) Consume(_ context.Context, hash string, now time.Time) (PendingAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.states[hash]
	delete(s.states, hash)
	if !ok || !now.Before(p.ExpiresAt) {
		return PendingAuthorization{}, ErrInvalidGrant
	}
	return p, nil
}

func TestConsumerCompleteBindsStateToUserAndNetwork(t *testing.T) {
	p, _, req, secret := fixture(t)
	server := httptest.NewTLSServer(p.TokenHandler())
	defer server.Close()
	c := Consumer{ClientID: req.ClientID, ClientSecret: secret, RedirectURI: req.RedirectURI, ProviderAuthorizationURL: "https://provider.example/authorize", ProviderTokenURL: server.URL, Store: &testStateStore{states: map[string]PendingAuthorization{}}, HTTPClient: server.Client(), Now: p.Now}
	for _, mismatch := range [][2]string{{"other-user", "family"}, {"owner-1", "other-network"}} {
		start, err := c.Begin(context.Background(), "owner-1", "family", url.Values{"state": {"injected"}, "client_id": {"evil"}})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(start)
		state := u.Query().Get("state")
		if state == "injected" || u.Query().Get("client_id") != req.ClientID {
			t.Fatal("reserved OAuth params overridden")
		}
		if _, err := c.Complete(context.Background(), mismatch[0], mismatch[1], state, strings.Repeat("c", 43)); err == nil {
			t.Fatal("cross-user/network state accepted")
		}
		if _, err := c.Complete(context.Background(), "owner-1", "family", state, strings.Repeat("c", 43)); err == nil {
			t.Fatal("consumed state replay accepted")
		}
	}
	start, err := c.Begin(context.Background(), "owner-1", "family", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start)
	req.State = u.Query().Get("state")
	req.CodeChallenge = u.Query().Get("code_challenge")
	response := authorize(t, p, req)
	token, err := c.Complete(context.Background(), "owner-1", "family", req.State, response.Code)
	if err != nil {
		t.Fatal(err)
	}
	if token.GrantID != "grant-1" {
		t.Fatal("wrong grant")
	}
	if _, err := c.Refresh(context.Background(), token.RefreshToken); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredAndCrossEnvironmentTokensDeny(t *testing.T) {
	p, policy, req, secret := fixture(t)
	r := authorize(t, p, req)
	token, err := p.ExchangeCode(context.Background(), req.ClientID, secret, r.Code, req.RedirectURI, strings.Repeat("v", 43))
	if err != nil {
		t.Fatal(err)
	}
	wrong := policy.grant.Consumer
	wrong.EnvironmentID = "preview"
	if _, err := p.ResolveAccessToken(context.Background(), token.AccessToken, wrong); err == nil {
		t.Fatal("cross-environment token accepted")
	}
	now := p.clock()
	p.Now = func() time.Time { return now.Add(6 * time.Minute) }
	if _, err := p.ResolveAccessToken(context.Background(), token.AccessToken, policy.grant.Consumer); err == nil {
		t.Fatal("expired access token accepted")
	}
}

func TestOAuthBasicCredentialsUseFormEncoding(t *testing.T) {
	p, _, req, _ := fixture(t)
	secret := strings.Repeat("s", 32) + "+%: &"
	registry := p.Clients.(testRegistry)
	registry.client.ID = "consumer:home"
	registry.client.SecretHash = HashSecret(secret)
	p.Clients = registry
	req.ClientID = registry.client.ID
	server := httptest.NewTLSServer(p.TokenHandler())
	defer server.Close()
	c := Consumer{ClientID: req.ClientID, ClientSecret: secret, RedirectURI: req.RedirectURI, ProviderAuthorizationURL: "https://provider.example/authorize", ProviderTokenURL: server.URL, Store: &testStateStore{states: map[string]PendingAuthorization{}}, HTTPClient: server.Client(), Now: p.Now}
	start, err := c.Begin(context.Background(), "owner-1", "family", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start)
	req.State = u.Query().Get("state")
	req.CodeChallenge = u.Query().Get("code_challenge")
	response := authorize(t, p, req)
	token, err := c.Complete(context.Background(), "owner-1", "family", req.State, response.Code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Refresh(context.Background(), token.RefreshToken); err != nil {
		t.Fatal(err)
	}
}

type faultyRevocationStore struct {
	OAuthStore
	refreshErr, errorAccess error
	accessCalls             int
}

func (s *faultyRevocationStore) LoadRefreshToken(context.Context, string) (TokenRecord, error) {
	return TokenRecord{}, s.refreshErr
}
func (s *faultyRevocationStore) LoadAccessToken(context.Context, string) (TokenRecord, error) {
	s.accessCalls++
	return TokenRecord{}, s.errorAccess
}
func TestRevokeNeverConfusesStorageFailureWithUnknownToken(t *testing.T) {
	p, _, req, secret := fixture(t)
	failure := errors.New("database unavailable")
	for _, test := range []struct {
		refresh, access error
		wantAccess      int
	}{{failure, ErrNotFound, 0}, {ErrNotFound, failure, 1}} {
		store := &faultyRevocationStore{OAuthStore: p.Store, refreshErr: test.refresh, errorAccess: test.access}
		p.Store = store
		if err := p.Revoke(context.Background(), req.ClientID, secret, strings.Repeat("r", 43)); !errors.Is(err, failure) {
			t.Fatalf("storage failure became success: %v", err)
		}
		if store.accessCalls != test.wantAccess {
			t.Fatalf("unexpected fallback lookups %d", store.accessCalls)
		}
	}
	store := &faultyRevocationStore{OAuthStore: p.Store, refreshErr: ErrNotFound, errorAccess: ErrNotFound}
	p.Store = store
	if err := p.Revoke(context.Background(), req.ClientID, secret, strings.Repeat("r", 43)); err != nil {
		t.Fatalf("truly absent token was not idempotent: %v", err)
	}
}
