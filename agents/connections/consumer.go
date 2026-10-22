package connections

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PendingAuthorization is secret server-side state. CodeVerifier must be
// protected like a credential; it never belongs in a browser cookie or URL.
type PendingAuthorization struct {
	StateHash    string
	UserID       string
	NetworkID    string
	CodeVerifier string
	ExpiresAt    time.Time
}

// ConsumerStateStore must be durable. Consume atomically removes/marks a state
// as used and rejects missing, used, or expired states with ErrInvalidGrant.
type ConsumerStateStore interface {
	Save(context.Context, PendingAuthorization) error
	Consume(context.Context, string, time.Time) (PendingAuthorization, error)
}

type Consumer struct {
	ClientID                 string
	ClientSecret             string
	RedirectURI              string
	ProviderAuthorizationURL string
	ProviderTokenURL         string
	Store                    ConsumerStateStore
	HTTPClient               *http.Client
	Now                      func() time.Time
}

func (c *Consumer) clock() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func (c *Consumer) ready() bool {
	return c != nil && identifier(c.ClientID) && len(c.ClientSecret) >= 32 && validRedirect(c.RedirectURI) && validRedirect(c.ProviderAuthorizationURL) && validRedirect(c.ProviderTokenURL) && c.Store != nil
}

// Begin binds the OAuth flow to the initiating authenticated user/network. Apps
// must check administrator authority before Begin and again before installing
// the connection after Complete. values may contain provider-specific resource
// selection hints; protected OAuth parameters are always overwritten.
func (c *Consumer) Begin(ctx context.Context, userID, networkID string, values url.Values) (string, error) {
	if !c.ready() || !identifier(userID) || !identifier(networkID) {
		return "", ErrUnauthorized
	}
	state, err := secret()
	if err != nil {
		return "", err
	}
	verifier, err := secret()
	if err != nil {
		return "", err
	}
	if err := c.Store.Save(ctx, PendingAuthorization{StateHash: HashSecret(state), UserID: userID, NetworkID: networkID, CodeVerifier: verifier, ExpiresAt: c.clock().Add(10 * time.Minute)}); err != nil {
		return "", err
	}
	u, _ := url.Parse(c.ProviderAuthorizationURL)
	q := u.Query()
	for key, vals := range values {
		q[key] = append([]string(nil), vals...)
	}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", c.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", PKCEChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Complete consumes state before making the confidential server-to-server
// exchange. Failure requires restarting the linking flow; state is never reused.
func (c *Consumer) Complete(ctx context.Context, userID, networkID, state, code string) (TokenResponse, error) {
	if !c.ready() || !identifier(userID) || !identifier(networkID) || len(state) != 43 || len(code) != 43 {
		return TokenResponse{}, ErrInvalidGrant
	}
	pending, err := c.Store.Consume(ctx, HashSecret(state), c.clock())
	if err != nil || pending.StateHash != HashSecret(state) || pending.UserID != userID || pending.NetworkID != networkID || !c.clock().Before(pending.ExpiresAt) || !validVerifier(pending.CodeVerifier) {
		return TokenResponse{}, ErrInvalidGrant
	}
	return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.RedirectURI}, "code_verifier": {pending.CodeVerifier}})
}

func (c *Consumer) Refresh(ctx context.Context, refreshToken string) (TokenResponse, error) {
	if !c.ready() || len(refreshToken) != 43 {
		return TokenResponse{}, ErrInvalidGrant
	}
	return c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

func (c *Consumer) exchange(ctx context.Context, form url.Values) (TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ProviderTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.ClientSecret))
	// Never follow redirects with a token exchange or allow an unbounded default
	// HTTP client. Copy the app's transport but preserve these protocol limits.
	client := http.Client{Timeout: 10 * time.Second}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
		if client.Timeout <= 0 || client.Timeout > 10*time.Second {
			client.Timeout = 10 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return TokenResponse{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TokenResponse{}, ErrInvalidGrant
	}
	if strings.Split(response.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return TokenResponse{}, ErrInvalidGrant
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(body) > 16<<10 {
		return TokenResponse{}, ErrInvalidGrant
	}
	var out TokenResponse
	if err := json.Unmarshal(body, &out); err != nil || len(out.AccessToken) != 43 || len(out.RefreshToken) != 43 || !strings.EqualFold(out.TokenType, "Bearer") || out.ExpiresIn <= 0 || out.ExpiresIn > 3600 || !identifier(out.GrantID) || out.GrantVersion == 0 {
		return TokenResponse{}, errors.New("invalid provider token response")
	}
	return out, nil
}
