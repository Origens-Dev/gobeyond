// Package gobeyond defines the public request-time contracts used by GoBeyond
// page loaders, actions, API handlers, and middleware.
package gobeyond

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const RenderAPIVersion = "gobeyond.render/v1alpha1"

type ResultKind string

const (
	ResultOK            ResultKind = "ok"
	ResultRedirect      ResultKind = "redirect"
	ResultNotFound      ResultKind = "not_found"
	ResultPublicError   ResultKind = "public_error"
	ResultInternalError ResultKind = "internal_error"
)

type CacheMode string

const (
	CachePrivateNoStore CacheMode = "private_no_store"
	CachePublic         CacheMode = "public"
)

type CachePolicy struct {
	Mode                 CacheMode `json:"mode"`
	MaxAge               int       `json:"maxAge,omitempty"`
	SharedMaxAge         int       `json:"sharedMaxAge,omitempty"`
	StaleWhileRevalidate int       `json:"staleWhileRevalidate,omitempty"`
	StaleIfError         int       `json:"staleIfError,omitempty"`
}

func (p CachePolicy) HeaderValue() string {
	if p.Mode != CachePublic {
		return "private, no-store"
	}
	value := "public, max-age=" + itoaNonNegative(p.MaxAge)
	if p.SharedMaxAge > 0 {
		value += ", s-maxage=" + itoaNonNegative(p.SharedMaxAge)
	}
	if p.StaleWhileRevalidate > 0 {
		value += ", stale-while-revalidate=" + itoaNonNegative(p.StaleWhileRevalidate)
	}
	if p.StaleIfError > 0 {
		value += ", stale-if-error=" + itoaNonNegative(p.StaleIfError)
	}
	return value
}

// PublicRevalidate returns a public policy that keeps browser responses stale
// while allowing shared caches to retain and asynchronously refresh them.
// Non-positive durations disable their corresponding directive.
func PublicRevalidate(fresh, stale, staleIfError time.Duration) CachePolicy {
	return CachePolicy{
		Mode:                 CachePublic,
		SharedMaxAge:         durationSeconds(fresh),
		StaleWhileRevalidate: durationSeconds(stale),
		StaleIfError:         durationSeconds(staleIfError),
	}
}

func durationSeconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int(value / time.Second)
}

type Alternate struct {
	Language string `json:"language"`
	URL      string `json:"url"`
}

type OpenGraph struct {
	Type        string   `json:"type,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url,omitempty"`
	Images      []string `json:"images,omitempty"`
}

type Twitter struct {
	Card        string   `json:"card,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Images      []string `json:"images,omitempty"`
}

// JSONLD is serialized by the document renderer with script-safe escaping.
// Values must be composed solely of JSON-compatible primitives, arrays, and maps.
type JSONLD map[string]any

type Metadata struct {
	Lang        string      `json:"lang"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Canonical   string      `json:"canonical,omitempty"`
	Robots      string      `json:"robots,omitempty"`
	OpenGraph   OpenGraph   `json:"openGraph,omitempty"`
	Twitter     Twitter     `json:"twitter,omitempty"`
	Alternates  []Alternate `json:"alternates,omitempty"`
	JSONLD      []JSONLD    `json:"jsonLd,omitempty"`
}

func (m Metadata) Validate(publicOrigin string, indexable bool) error {
	if strings.TrimSpace(m.Lang) == "" {
		return errors.New("metadata lang is required")
	}
	if strings.TrimSpace(m.Title) == "" {
		return errors.New("metadata title is required")
	}
	if !indexable {
		return nil
	}
	if m.Canonical == "" {
		return errors.New("indexable metadata requires a canonical URL")
	}
	if strings.TrimSpace(m.Description) == "" || strings.TrimSpace(m.Robots) == "" {
		return errors.New("indexable metadata requires description and robots directives")
	}
	origin, err := url.Parse(publicOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return errors.New("public origin must be an absolute URL")
	}
	canonical, err := url.Parse(m.Canonical)
	if err != nil || !canonical.IsAbs() {
		return errors.New("canonical URL must be absolute")
	}
	if canonical.Scheme != origin.Scheme || canonical.Host != origin.Host {
		return errors.New("canonical URL must use the configured public origin")
	}
	if m.OpenGraph.Type == "" || m.OpenGraph.Title == "" || m.OpenGraph.Description == "" || m.OpenGraph.URL == "" || len(m.OpenGraph.Images) == 0 {
		return errors.New("indexable metadata requires complete Open Graph fields and an image")
	}
	if err := validateAbsoluteURL(m.OpenGraph.URL, "Open Graph URL"); err != nil {
		return err
	}
	if m.Twitter.Card == "" || m.Twitter.Title == "" || m.Twitter.Description == "" || len(m.Twitter.Images) == 0 {
		return errors.New("indexable metadata requires complete Twitter fields and an image")
	}
	for _, image := range append(append([]string(nil), m.OpenGraph.Images...), m.Twitter.Images...) {
		if err := validateAbsoluteURL(image, "social image URL"); err != nil {
			return err
		}
	}
	for _, alternate := range m.Alternates {
		if alternate.Language == "" || validateAbsoluteURL(alternate.URL, "alternate URL") != nil {
			return errors.New("metadata alternates require a language and absolute URL")
		}
	}
	return nil
}

func validateAbsoluteURL(value, label string) error {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New(label + " must be an absolute HTTP(S) URL")
	}
	return nil
}

type PageContext struct {
	Context context.Context
	Request *http.Request
	// PublicOrigin is the absolute origin resolved for this request.
	PublicOrigin string
	Params       map[string]string
	Values       map[string]any
	BuildID      string
}

type PageResult[T any] struct {
	Kind       ResultKind        `json:"kind"`
	Props      T                 `json:"props,omitempty"`
	Metadata   Metadata          `json:"metadata,omitempty"`
	Status     int               `json:"status,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Cache      CachePolicy       `json:"cache"`
	RedirectTo string            `json:"redirectTo,omitempty"`
	ErrorCode  string            `json:"errorCode,omitempty"`
	Message    string            `json:"message,omitempty"`
}

func OK[T any](props T, metadata Metadata) PageResult[T] {
	return PageResult[T]{
		Kind:     ResultOK,
		Props:    props,
		Metadata: metadata,
		Status:   http.StatusOK,
		Cache:    CachePolicy{Mode: CachePrivateNoStore},
	}
}

func NotFound[T any](metadata Metadata) PageResult[T] {
	return PageResult[T]{
		Kind:     ResultNotFound,
		Metadata: metadata,
		Status:   http.StatusNotFound,
		Cache:    CachePolicy{Mode: CachePrivateNoStore},
	}
}

func Redirect[T any](location string, permanent bool) PageResult[T] {
	status := http.StatusTemporaryRedirect
	if permanent {
		status = http.StatusPermanentRedirect
	}
	return PageResult[T]{
		Kind:       ResultRedirect,
		RedirectTo: location,
		Status:     status,
		Cache:      CachePolicy{Mode: CachePrivateNoStore},
	}
}

type ActionContext struct {
	Context      context.Context
	Request      *http.Request
	PublicOrigin string
	Params       map[string]string
	Values       map[string]any
	BuildID      string
}

type ActionResult[T any] struct {
	Data          T                 `json:"data,omitempty"`
	FieldErrors   map[string]string `json:"fieldErrors,omitempty"`
	RedirectTo    string            `json:"redirectTo,omitempty"`
	RefreshRoutes []string          `json:"refreshRoutes,omitempty"`
}

type RequestContext struct {
	Context      context.Context
	Request      *http.Request
	PublicOrigin string
	Params       map[string]string
	Values       map[string]any
	BuildID      string
}

type Response struct {
	Status    int
	Headers   http.Header
	Body      []byte
	RewriteTo string
}

func Rewrite(path string) Response {
	return Response{RewriteTo: path}
}

type Handler func(*RequestContext) (Response, error)

type Middleware func(Handler) Handler

type MiddlewareConfig struct {
	Patterns []string
	Methods  []string
}

type DeadlinePolicy struct {
	Loader time.Duration
	Render time.Duration
	Action time.Duration
	API    time.Duration
}

func itoaNonNegative(value int) string {
	if value < 0 {
		value = 0
	}
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
