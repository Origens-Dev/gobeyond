// Package security contains framework-enforced HTTP boundary protections.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const (
	CSRFCookie = "__Host-gobeyond-csrf"
	CSRFHeader = "X-GoBeyond-CSRF"
)

var reservedHeaders = map[string]struct{}{
	"x-gobeyond-build":      {},
	"x-gobeyond-route":      {},
	"x-gobeyond-action":     {},
	"x-gobeyond-internal":   {},
	"x-gobeyond-rewrite":    {},
	"x-gobeyond-request-id": {},
}

func StripReservedHeaders(header http.Header) {
	for name := range header {
		if _, reserved := reservedHeaders[strings.ToLower(name)]; reserved {
			header.Del(name)
		}
	}
}

func ValidateHost(request *http.Request, allowed []string) error {
	host := strings.ToLower(request.Host)
	for _, candidate := range allowed {
		if host == strings.ToLower(candidate) {
			return nil
		}
	}
	return errors.New("request host is not allowed")
}

func ValidateSameOrigin(request *http.Request, publicOrigin string) error {
	origin, err := url.Parse(publicOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return errors.New("configured public origin is invalid")
	}
	candidate := request.Header.Get("Origin")
	if candidate == "" {
		candidate = request.Header.Get("Referer")
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != origin.Scheme || parsed.Host != origin.Host {
		return errors.New("request origin is not allowed")
	}
	return nil
}

type CSRF struct {
	secret []byte
}

func NewCSRF(secret []byte) (*CSRF, error) {
	if len(secret) < 32 {
		return nil, errors.New("CSRF secret must contain at least 32 bytes")
	}
	copySecret := append([]byte(nil), secret...)
	return &CSRF{secret: copySecret}, nil
}

func (c *CSRF) Token() (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	signature := c.sign(nonce)
	return base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *CSRF) Verify(request *http.Request, publicOrigin string) error {
	if err := ValidateSameOrigin(request, publicOrigin); err != nil {
		return err
	}
	cookie, err := request.Cookie(CSRFCookie)
	if err != nil {
		return errors.New("CSRF cookie is missing")
	}
	headerToken := request.Header.Get(CSRFHeader)
	if headerToken == "" || !hmac.Equal([]byte(cookie.Value), []byte(headerToken)) {
		return errors.New("CSRF token mismatch")
	}
	parts := strings.Split(headerToken, ".")
	if len(parts) != 2 {
		return errors.New("CSRF token is malformed")
	}
	nonce, decodeErr := base64.RawURLEncoding.DecodeString(parts[0])
	if decodeErr != nil || len(nonce) != 32 {
		return errors.New("CSRF token nonce is malformed")
	}
	signature, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
	if decodeErr != nil || !hmac.Equal(signature, c.sign(nonce)) {
		return errors.New("CSRF signature is invalid")
	}
	return nil
}

func (c *CSRF) Cookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     CSRFCookie,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (c *CSRF) sign(nonce []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(nonce)
	return mac.Sum(nil)
}
