package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/buildpaths"
	"github.com/Origens-Dev/gobeyond/imageopt"
)

// PlatformOriginEnv is the reserved origin used for the transparent
// route-miss fallback. Hosted adapters set it to the authenticated internal
// platform ingress; local development normally leaves it unset.
const PlatformOriginEnv = "GOBEYOND_PLATFORM_ORIGIN"

type platformOriginFetcher struct {
	origin *url.URL
	client *http.Client
}

// NewPlatformOriginFetcher creates the trusted origin transport used by
// gb.Fetch after same-slot route classification reports a miss.
func NewPlatformOriginFetcher(origin string, client *http.Client) (gb.Fetcher, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("gobeyond fetch: platform origin must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("gobeyond fetch: platform origin must use HTTP(S)")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &platformOriginFetcher{origin: parsed, client: client}, nil
}

// FetchOriginFromEnv configures the hosted fallback from the reserved
// platform environment. An unset value is valid and disables fallback, which
// is the expected local-development behavior.
func FetchOriginFromEnv() (gb.Fetcher, error) {
	origin := strings.TrimSpace(os.Getenv(PlatformOriginEnv))
	if origin == "" {
		return nil, nil
	}
	return NewPlatformOriginFetcher(origin, nil)
}

func (f *platformOriginFetcher) Fetch(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("gobeyond fetch: request URL is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	clone := request.Clone(ctx)
	originalHost := request.Host
	if originalHost == "" {
		originalHost = request.URL.Host
	}
	target := *f.origin
	target.Path = strings.TrimRight(f.origin.Path, "/") + "/" + strings.TrimLeft(request.URL.Path, "/")
	target.RawPath = ""
	target.RawQuery = request.URL.RawQuery
	clone.URL = &target
	clone.Host = originalHost
	clone.RequestURI = ""
	return f.client.Do(clone)
}

const maxFetchDepth = 8

var (
	errFetchRouteMiss = errors.New("gobeyond fetch: route is not present in the current build")
	errFetchDepth     = errors.New("gobeyond fetch: maximum nested fetch depth exceeded")
)

type fetchDepthContextKey struct{}

func fetchDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	value, _ := ctx.Value(fetchDepthContextKey{}).(int)
	return value
}

func (s *Server) fetch(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("gobeyond fetch: request URL is required")
	}
	if fetchDepth(ctx) >= maxFetchDepth {
		return nil, errFetchDepth
	}
	if ctx == nil {
		ctx = context.Background()
	}

	request = request.Clone(ctx)
	if !proxyPolicyApplied(request.Context()) {
		result, err := evaluateProxyPolicy(s.config.ProxyPolicy, request)
		if err != nil {
			return nil, err
		}
		if result.location != "" {
			response := httptest.NewRecorder()
			response.Header().Set("Location", result.location)
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(result.status)
			return response.Result(), nil
		}
		request = result.request
	}

	if !s.hasLocalFetchRoute(request) {
		if s.config.FetchOrigin == nil {
			return nil, errFetchRouteMiss
		}
		fallbackContext := context.WithValue(ctx, fetchDepthContextKey{}, fetchDepth(ctx)+1)
		return s.config.FetchOrigin.Fetch(fallbackContext, request.Clone(fallbackContext))
	}

	localContext := context.WithValue(request.Context(), fetchDepthContextKey{}, fetchDepth(request.Context())+1)
	localRequest := request.WithContext(withProxyPolicyApplied(localContext))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, localRequest)
	return response.Result(), nil
}

func (s *Server) hasLocalFetchRoute(request *http.Request) bool {
	path := request.URL.Path
	if strings.HasPrefix(path, "/__gobeyond/") || path == imageopt.Route {
		return true
	}
	if strings.HasPrefix(path, buildpaths.BuildsPrefix) {
		if buildpaths.IsStaticArtifact(path) {
			return true
		}
		if buildID, routeID, ok := buildpaths.ParseRuntimePath(path); ok {
			page, exists := s.pages[routeID]
			return buildID == s.config.BuildID && exists && page.Route.ID != ""
		}
		if buildID, actionID, ok := buildpaths.ParseActionPath(path); ok {
			action, exists := s.actions[actionID]
			return buildID == s.config.BuildID && exists && action.ID != ""
		}
		return false
	}
	if strings.HasPrefix(path, "/api/") {
		route, _, ok := s.apiTable.Resolve(path)
		if !ok {
			return false
		}
		_, ok = s.apis[route.ID].Methods[request.Method]
		return ok
	}
	_, _, ok := s.pageTable.Resolve(path)
	return ok
}
