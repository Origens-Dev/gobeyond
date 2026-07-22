// Package runtime provides GoBeyond's Node-free production HTTP server.
package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	gb "github.com/gobeyond-dev/gobeyond"
	"github.com/gobeyond-dev/gobeyond/document"
	"github.com/gobeyond-dev/gobeyond/internal/jsvalue"
	gbmiddleware "github.com/gobeyond-dev/gobeyond/middleware"
	"github.com/gobeyond-dev/gobeyond/renderer"
	"github.com/gobeyond-dev/gobeyond/renderplan"
	"github.com/gobeyond-dev/gobeyond/router"
	"github.com/gobeyond-dev/gobeyond/security"
)

const maxRewrites = 8
const privateRequestValue = "gobeyond_private_request"

type LoadedPage struct {
	Kind       gb.ResultKind  `json:"kind"`
	Props      any            `json:"props,omitempty"`
	Metadata   gb.Metadata    `json:"metadata,omitempty"`
	Status     int            `json:"status,omitempty"`
	Headers    http.Header    `json:"headers,omitempty"`
	Cache      gb.CachePolicy `json:"cache"`
	RedirectTo string         `json:"redirectTo,omitempty"`
	ErrorCode  string         `json:"errorCode,omitempty"`
	Message    string         `json:"message,omitempty"`
}

type PageLoader func(*gb.PageContext) (LoadedPage, error)

type PageRoute struct {
	Route        router.Route
	Plan         *renderplan.Plan
	Load         PageLoader
	Static       *LoadedPage
	Indexable    bool
	ClientScript string
	Styles       []string
}

type Action struct {
	ID      string
	MaxBody int64
	execute func(*gb.ActionContext, json.RawMessage) (any, error)
}

// RegisterAction binds a generated input decoder and output validator to a
// typed application handler. The decoder runs before application code and the
// validator runs before a successful result can cross the HTTP boundary.
func RegisterAction[Input, Output any](
	id string,
	decode func(json.RawMessage) (Input, error),
	validateOutput func(Output) error,
	handler func(*gb.ActionContext, Input) (Output, error),
) Action {
	if decode == nil || validateOutput == nil || handler == nil {
		return Action{ID: id}
	}
	return Action{
		ID: id,
		execute: func(ctx *gb.ActionContext, raw json.RawMessage) (any, error) {
			input, err := decode(raw)
			if err != nil {
				return nil, &actionInputError{err: err}
			}
			output, err := handler(ctx, input)
			if err != nil {
				return nil, err
			}
			if err := validateOutput(output); err != nil {
				return nil, &actionOutputError{err: err}
			}
			return output, nil
		},
	}
}

type actionInputError struct{ err error }

func (e *actionInputError) Error() string { return "invalid action input: " + e.err.Error() }
func (e *actionInputError) Unwrap() error { return e.err }

type actionOutputError struct{ err error }

func (e *actionOutputError) Error() string { return "invalid action output: " + e.err.Error() }
func (e *actionOutputError) Unwrap() error { return e.err }

type APIRoute struct {
	Route   router.Route
	Methods map[string]gb.Handler
}

type Config struct {
	BuildID       string
	PublicOrigin  string
	AllowedHosts  []string
	Pages         []PageRoute
	Actions       []Action
	APIs          []APIRoute
	Middleware    []gbmiddleware.Rule
	CSRF          *security.CSRF
	Logger        *slog.Logger
	Deadlines     gb.DeadlinePolicy
	MaxHeaderSize int
}

type Server struct {
	config       Config
	pages        map[string]PageRoute
	pageTable    *router.Table
	actions      map[string]Action
	apis         map[string]APIRoute
	apiTable     *router.Table
	documentPipe gb.Handler
	logger       *slog.Logger
}

func New(config Config) (*Server, error) {
	if config.BuildID == "" {
		return nil, errors.New("runtime build ID is required")
	}
	if config.PublicOrigin == "" {
		return nil, errors.New("runtime public origin is required")
	}
	publicOrigin, err := url.Parse(config.PublicOrigin)
	if err != nil || publicOrigin.Scheme == "" || publicOrigin.Host == "" {
		return nil, errors.New("runtime public origin must be an absolute URL")
	}
	if len(config.AllowedHosts) == 0 {
		config.AllowedHosts = []string{publicOrigin.Host}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Deadlines.Loader <= 0 {
		config.Deadlines.Loader = 10 * time.Second
	}
	if config.Deadlines.Render <= 0 {
		config.Deadlines.Render = 2 * time.Second
	}
	if config.Deadlines.Action <= 0 {
		config.Deadlines.Action = 15 * time.Second
	}
	if config.Deadlines.API <= 0 {
		config.Deadlines.API = 15 * time.Second
	}
	pageRoutes := make([]router.Route, len(config.Pages))
	pages := make(map[string]PageRoute, len(config.Pages))
	for i, page := range config.Pages {
		if page.Route.ID == "" || page.Plan == nil {
			return nil, errors.New("page routes require an ID and render plan")
		}
		if page.Plan.RouteID != page.Route.ID {
			return nil, fmt.Errorf("page %s render-plan route ID mismatch", page.Route.ID)
		}
		if page.Static == nil && page.Load == nil {
			return nil, fmt.Errorf("page %s requires static data or a loader", page.Route.ID)
		}
		pageRoutes[i] = page.Route
		pages[page.Route.ID] = page
	}
	pageTable, err := router.NewTable(pageRoutes)
	if err != nil {
		return nil, err
	}
	actions := make(map[string]Action, len(config.Actions))
	for _, action := range config.Actions {
		if action.ID == "" || action.execute == nil {
			return nil, errors.New("actions require an ID and a generated contract registration")
		}
		if _, exists := actions[action.ID]; exists {
			return nil, errors.New("duplicate action ID: " + action.ID)
		}
		if action.MaxBody <= 0 {
			action.MaxBody = 1 << 20
		}
		actions[action.ID] = action
	}
	apiRoutes := make([]router.Route, len(config.APIs))
	apis := make(map[string]APIRoute, len(config.APIs))
	for i, api := range config.APIs {
		if api.Route.ID == "" || len(api.Methods) == 0 {
			return nil, errors.New("API routes require an ID and methods")
		}
		apiRoutes[i] = api.Route
		apis[api.Route.ID] = api
	}
	apiTable, err := router.NewTable(apiRoutes)
	if err != nil {
		return nil, err
	}
	server := &Server{
		config:    config,
		pages:     pages,
		pageTable: pageTable,
		actions:   actions,
		apis:      apis,
		apiTable:  apiTable,
		logger:    config.Logger,
	}
	pipe, err := gbmiddleware.Chain(config.Middleware, server.documentHandler)
	if err != nil {
		return nil, err
	}
	server.documentPipe = pipe
	return server, nil
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if acceptsGzip(request) {
		compressed := &gzipResponseWriter{ResponseWriter: writer}
		s.serveHTTP(compressed, request)
		_ = compressed.Close()
		return
	}
	s.serveHTTP(writer, request)
}

func (s *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = randomID()
	}
	security.StripReservedHeaders(request.Header)
	// Load balancers probe the task by its private address or generated DNS
	// name. Health endpoints disclose no application data and must remain
	// reachable without weakening Host validation for documents, APIs, runtime
	// data, or actions.
	if request.URL.Path == "/__gobeyond/healthz" || request.URL.Path == "/__gobeyond/readyz" {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/__gobeyond/healthz" {
			_, _ = writer.Write([]byte(`{"ok":true}`))
		} else {
			_, _ = writer.Write([]byte(`{"ready":true}`))
		}
		return
	}
	if len(s.config.AllowedHosts) > 0 {
		if err := security.ValidateHost(request, s.config.AllowedHosts); err != nil {
			s.writeError(writer, http.StatusBadRequest, "invalid_host", requestID)
			return
		}
	}
	writer.Header().Set("X-GoBeyond-Request-ID", requestID)
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("request panic", "request_id", requestID)
			s.writeError(writer, http.StatusInternalServerError, "internal_error", requestID)
		}
		s.logger.Info("request complete", "request_id", requestID, "method", request.Method, "path", request.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	}()

	switch {
	case strings.HasPrefix(request.URL.Path, "/_gobeyond/runtime/"):
		s.serveRuntime(writer, request, requestID)
	case strings.HasPrefix(request.URL.Path, "/_gobeyond/actions/"):
		s.serveAction(writer, request, requestID)
	case strings.HasPrefix(request.URL.Path, "/api/"):
		s.serveAPI(writer, request, requestID)
	default:
		s.serveDocument(writer, request, requestID)
	}
}

type gzipResponseWriter struct {
	http.ResponseWriter
	compressor *gzip.Writer
	eligible   bool
	wroteHead  bool
}

func (writer *gzipResponseWriter) WriteHeader(status int) {
	if writer.wroteHead {
		return
	}
	writer.wroteHead = true
	contentType := writer.Header().Get("Content-Type")
	writer.eligible = status != http.StatusNoContent && status != http.StatusNotModified &&
		(strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") || strings.Contains(contentType, "javascript") || strings.Contains(contentType, "svg"))
	if writer.eligible {
		writer.Header().Del("Content-Length")
		writer.Header().Set("Content-Encoding", "gzip")
		writer.Header().Add("Vary", "Accept-Encoding")
		writer.compressor = gzip.NewWriter(writer.ResponseWriter)
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *gzipResponseWriter) Write(data []byte) (int, error) {
	if !writer.wroteHead {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.eligible {
		return writer.compressor.Write(data)
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *gzipResponseWriter) Close() error {
	if writer.compressor != nil {
		return writer.compressor.Close()
	}
	return nil
}

func acceptsGzip(request *http.Request) bool {
	for _, value := range strings.Split(request.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

func (s *Server) serveDocument(writer http.ResponseWriter, request *http.Request, requestID string) {
	current := request.Clone(request.Context())
	privateRequest := request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != ""
	for rewrites := 0; rewrites <= maxRewrites; rewrites++ {
		_, params, _ := s.pageTable.Resolve(current.URL.Path)
		ctx := &gb.RequestContext{
			Context: current.Context(),
			Request: current,
			Params:  params,
			Values:  map[string]any{"request_id": requestID, privateRequestValue: privateRequest},
			BuildID: s.config.BuildID,
		}
		response, err := s.documentPipe(ctx)
		if err != nil {
			s.writeError(writer, http.StatusInternalServerError, "internal_error", requestID)
			return
		}
		if response.RewriteTo == "" {
			s.writeResponse(writer, response)
			return
		}
		target, err := url.Parse(response.RewriteTo)
		if err != nil || !strings.HasPrefix(target.Path, "/") || strings.HasPrefix(target.Path, "/_gobeyond/") {
			s.writeError(writer, http.StatusInternalServerError, "invalid_rewrite", requestID)
			return
		}
		current = current.Clone(current.Context())
		current.URL.Path = target.Path
		if target.RawQuery != "" {
			current.URL.RawQuery = target.RawQuery
		}
	}
	s.writeError(writer, http.StatusLoopDetected, "rewrite_loop", requestID)
}

func (s *Server) documentHandler(ctx *gb.RequestContext) (gb.Response, error) {
	route, params, ok := s.pageTable.Resolve(ctx.Request.URL.Path)
	if !ok {
		return gb.Response{Status: http.StatusNotFound, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte("<!doctype html><title>Not found</title><h1>Not found</h1>")}, nil
	}
	ctx.Params = params
	page := s.pages[route.ID]
	loadStarted := time.Now()
	loaded, err := s.loadPage(ctx.Context, ctx.Request, params, ctx.Values, page)
	if err != nil {
		return gb.Response{}, err
	}
	loadDuration := time.Since(loadStarted)
	if loaded.Kind == gb.ResultRedirect {
		if !validRedirectTarget(loaded.RedirectTo) {
			return gb.Response{}, errors.New("page loader returned an invalid redirect target")
		}
		return gb.Response{Status: loaded.Status, Headers: http.Header{"Location": {loaded.RedirectTo}}}, nil
	}
	if loaded.Kind == gb.ResultInternalError {
		return gb.Response{}, errors.New("page loader internal error")
	}
	if loaded.Status == 0 {
		loaded.Status = http.StatusOK
	}
	if err := jsvalue.Validate(loaded.Props); err != nil {
		return gb.Response{}, fmt.Errorf("page props are not JavaScript-compatible: %w", err)
	}
	renderStarted := time.Now()
	body, err := renderer.Render(page.Plan, loaded.Props)
	if err != nil {
		return gb.Response{}, err
	}
	renderDuration := time.Since(renderStarted)
	if renderDuration > s.config.Deadlines.Render {
		return gb.Response{}, errors.New("page render exceeded its time budget")
	}
	s.logger.Info("page rendered", "request_id", ctx.Values["request_id"], "build_id", s.config.BuildID, "route_id", route.ID, "loader_ms", loadDuration.Milliseconds(), "render_ms", renderDuration.Milliseconds())
	var output bytes.Buffer
	styles := make([]document.Asset, len(page.Styles))
	for i, style := range page.Styles {
		styles[i] = document.Asset{URL: style}
	}
	scripts := []document.Asset{}
	if page.ClientScript != "" {
		scripts = append(scripts, document.Asset{URL: page.ClientScript})
	}
	indexable := page.Indexable && loaded.Kind != gb.ResultNotFound
	if err := document.Render(&output, document.Input{
		PublicOrigin: s.config.PublicOrigin,
		Indexable:    indexable,
		Metadata:     loaded.Metadata,
		Body:         document.BodyHTML(body),
		Hydration: document.HydrationData{
			BuildID: s.config.BuildID,
			RouteID: route.ID,
			Props:   loaded.Props,
		},
		Styles:  styles,
		Scripts: scripts,
	}); err != nil {
		return gb.Response{}, err
	}
	headers := cloneHeader(loaded.Headers)
	headers.Set("Content-Type", "text/html; charset=utf-8")
	headers.Set("Cache-Control", loaded.Cache.HeaderValue())
	if loaded.Cache.Mode == gb.CachePublic && (ctx.Values[privateRequestValue] == true || ctx.Request.Header.Get("Authorization") != "" || ctx.Request.Header.Get("Cookie") != "" || len(headers.Values("Set-Cookie")) > 0) {
		headers.Set("Cache-Control", gb.CachePolicy{Mode: gb.CachePrivateNoStore}.HeaderValue())
	}
	headers.Set("X-GoBeyond-Build", s.config.BuildID)
	return gb.Response{Status: loaded.Status, Headers: headers, Body: output.Bytes()}, nil
}

func (s *Server) loadPage(parent context.Context, request *http.Request, params map[string]string, values map[string]any, page PageRoute) (LoadedPage, error) {
	if page.Static != nil {
		return *page.Static, nil
	}
	ctx, cancel := context.WithTimeout(parent, s.config.Deadlines.Loader)
	defer cancel()
	return page.Load(&gb.PageContext{
		Context: ctx,
		Request: request.WithContext(ctx),
		Params:  params,
		Values:  values,
		BuildID: s.config.BuildID,
	})
}

func (s *Server) serveRuntime(writer http.ResponseWriter, request *http.Request, requestID string) {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/_gobeyond/runtime/"), "/")
	if len(parts) != 2 {
		s.writeError(writer, http.StatusNotFound, "not_found", requestID)
		return
	}
	if parts[0] != s.config.BuildID {
		s.writeBuildMismatch(writer, requestID)
		return
	}
	page, exists := s.pages[parts[1]]
	if !exists {
		s.writeError(writer, http.StatusNotFound, "not_found", requestID)
		return
	}
	target, err := url.ParseRequestURI(request.URL.Query().Get("path"))
	if err != nil || target.Host != "" || !strings.HasPrefix(target.Path, "/") {
		s.writeError(writer, http.StatusBadRequest, "invalid_runtime_path", requestID)
		return
	}
	route, params, ok := s.pageTable.Resolve(target.Path)
	if !ok || route.ID != page.Route.ID {
		s.writeError(writer, http.StatusBadRequest, "route_mismatch", requestID)
		return
	}
	publicRequest := request.Clone(request.Context())
	publicURL := *request.URL
	publicURL.Path = target.Path
	publicURL.RawPath = target.RawPath
	publicURL.RawQuery = target.RawQuery
	publicRequest.URL = &publicURL
	response, err := s.applyMiddleware(publicRequest, requestID, params, func(ctx *gb.RequestContext) (gb.Response, error) {
		loaded, loadErr := s.loadPage(ctx.Context, ctx.Request, params, ctx.Values, page)
		if loadErr != nil {
			return gb.Response{}, loadErr
		}
		if err := jsvalue.Validate(loaded.Props); err != nil {
			return gb.Response{}, fmt.Errorf("page props are not JavaScript-compatible: %w", err)
		}
		status := loaded.Status
		if status == 0 {
			status = http.StatusOK
		}
		return jsonResponse(status, map[string]any{
			"apiVersion": gb.RenderAPIVersion,
			"buildId":    s.config.BuildID,
			"routeId":    route.ID,
			"result":     loaded,
		})
	})
	if err != nil {
		s.writeError(writer, http.StatusInternalServerError, "internal_error", requestID)
		return
	}
	if response.RewriteTo != "" {
		s.writeError(writer, http.StatusBadRequest, "rewrite_not_supported", requestID)
		return
	}
	s.writeResponse(writer, response)
}

func (s *Server) serveAction(writer http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		s.writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/_gobeyond/actions/"), "/")
	if len(parts) != 2 {
		s.writeError(writer, http.StatusNotFound, "not_found", requestID)
		return
	}
	if parts[0] != s.config.BuildID {
		s.writeBuildMismatch(writer, requestID)
		return
	}
	action, exists := s.actions[parts[1]]
	if !exists {
		s.writeError(writer, http.StatusNotFound, "not_found", requestID)
		return
	}
	if err := security.ValidateSameOrigin(request, s.config.PublicOrigin); err != nil {
		s.writeError(writer, http.StatusForbidden, "origin_failed", requestID)
		return
	}
	if s.config.CSRF != nil {
		if err := s.config.CSRF.Verify(request, s.config.PublicOrigin); err != nil {
			s.writeError(writer, http.StatusForbidden, "csrf_failed", requestID)
			return
		}
	}
	body := http.MaxBytesReader(writer, request.Body, action.MaxBody)
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, "invalid_json", requestID)
		return
	}
	response, err := s.applyMiddleware(request, requestID, nil, func(ctx *gb.RequestContext) (gb.Response, error) {
		actionContext, cancel := context.WithTimeout(ctx.Context, s.config.Deadlines.Action)
		defer cancel()
		result, actionErr := action.execute(&gb.ActionContext{Context: actionContext, Request: ctx.Request.WithContext(actionContext), Values: ctx.Values, BuildID: s.config.BuildID}, json.RawMessage(raw))
		if actionErr != nil {
			return gb.Response{}, actionErr
		}
		if err := jsvalue.Validate(result); err != nil {
			return gb.Response{}, fmt.Errorf("action result is not JavaScript-compatible: %w", err)
		}
		return jsonResponse(http.StatusOK, map[string]any{"data": result, "buildId": s.config.BuildID})
	})
	if err != nil {
		var inputError *actionInputError
		if errors.As(err, &inputError) {
			s.writeError(writer, http.StatusBadRequest, "invalid_action_input", requestID)
			return
		}
		s.writeError(writer, http.StatusInternalServerError, "action_failed", requestID)
		return
	}
	if response.RewriteTo != "" {
		s.writeError(writer, http.StatusBadRequest, "rewrite_not_supported", requestID)
		return
	}
	s.writeResponse(writer, response)
}

func (s *Server) serveAPI(writer http.ResponseWriter, request *http.Request, requestID string) {
	route, params, ok := s.apiTable.Resolve(request.URL.Path)
	if !ok {
		s.writeError(writer, http.StatusNotFound, "not_found", requestID)
		return
	}
	api := s.apis[route.ID]
	handler, exists := api.Methods[request.Method]
	if !exists {
		s.writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	response, err := s.applyMiddleware(request, requestID, params, func(ctx *gb.RequestContext) (gb.Response, error) {
		apiContext, cancel := context.WithTimeout(ctx.Context, s.config.Deadlines.API)
		defer cancel()
		ctx.Context = apiContext
		ctx.Request = ctx.Request.WithContext(apiContext)
		return handler(ctx)
	})
	if err != nil {
		s.writeError(writer, http.StatusInternalServerError, "api_failed", requestID)
		return
	}
	s.writeResponse(writer, response)
}

func (s *Server) applyMiddleware(request *http.Request, requestID string, params map[string]string, final gb.Handler) (gb.Response, error) {
	pipe, err := gbmiddleware.Chain(s.config.Middleware, final)
	if err != nil {
		return gb.Response{}, err
	}
	return pipe(&gb.RequestContext{
		Context: request.Context(),
		Request: request,
		Params:  params,
		Values:  map[string]any{"request_id": requestID},
		BuildID: s.config.BuildID,
	})
}

func jsonResponse(status int, value any) (gb.Response, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return gb.Response{}, err
	}
	body = append(body, '\n')
	return gb.Response{
		Status:  status,
		Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"private, no-store"}},
		Body:    body,
	}, nil
}

func (s *Server) writeResponse(writer http.ResponseWriter, response gb.Response) {
	for name, values := range response.Headers {
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

func (s *Server) writeBuildMismatch(writer http.ResponseWriter, requestID string) {
	s.writeJSON(writer, http.StatusConflict, map[string]any{
		"error":     "build_mismatch",
		"reload":    true,
		"buildId":   s.config.BuildID,
		"requestId": requestID,
	})
}

func (s *Server) writeError(writer http.ResponseWriter, status int, code, requestID string) {
	s.writeJSON(writer, status, map[string]any{"error": code, "requestId": requestID})
}

func (s *Server) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func cloneHeader(header http.Header) http.Header {
	if header == nil {
		return make(http.Header)
	}
	return header.Clone()
}

func randomID() string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(data[:])
}

func validRedirectTarget(target string) bool {
	parsed, err := url.Parse(target)
	if err != nil || target == "" {
		return false
	}
	if parsed.IsAbs() {
		return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
	}
	return strings.HasPrefix(parsed.Path, "/") && !strings.HasPrefix(parsed.Path, "//")
}
