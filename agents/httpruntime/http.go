package httpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
)

const maxRequestBodyBytes = 1 << 20

type startRequest struct {
	AgentID  string               `json:"agentId,omitempty"`
	Input    json.RawMessage      `json:"input"`
	Metadata map[string]string    `json:"metadata,omitempty"`
	Model    agents.ModelMetadata `json:"model,omitempty"`
}

type startResponse struct {
	Session   agents.Session `json:"session"`
	Run       agents.Run     `json:"run"`
	EventsURL string         `json:"eventsUrl"`
}

type respondRequest struct {
	RunID         string          `json:"runId,omitempty"`
	Response      json.RawMessage `json:"response"`
	InteractionID string          `json:"interactionId,omitempty"`
	Answers       json.RawMessage `json:"answers,omitempty"`
}

type cancelRequest struct {
	RunID  string `json:"runId,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Mount registers the agent HTTP API below prefix. For example:
//
//	runtime.Mount(mux, "/api/agents")
//
// registers POST /api/agents/{agentID}/sessions plus the session GET, events,
// respond, and cancel endpoints.
func (runtime *Runtime) Mount(mux *http.ServeMux, prefix string) error {
	if runtime == nil {
		return errors.New("agent runtime is required")
	}
	if mux == nil {
		return errors.New("HTTP serve mux is required")
	}
	prefix, err := normalizePrefix(prefix)
	if err != nil {
		return err
	}
	mux.HandleFunc("POST "+prefix+"/{agentID}/sessions", runtime.handleStart(prefix))
	// The agent-specific route is canonical. This alias keeps the browser-safe
	// TypeScript client usable without endpoint overrides.
	mux.HandleFunc("POST "+prefix+"/sessions", runtime.handleStartByRequest(prefix))
	mux.HandleFunc("GET "+prefix+"/sessions/{sessionID}", runtime.handleGetSession)
	mux.HandleFunc("GET "+prefix+"/sessions/{sessionID}/events", runtime.handleEvents)
	mux.HandleFunc("GET "+prefix+"/sessions/{sessionID}/runs/{runID}/events", runtime.handleEvents)
	mux.HandleFunc("POST "+prefix+"/sessions/{sessionID}/resume", runtime.handleResume(prefix))
	mux.HandleFunc("POST "+prefix+"/sessions/{sessionID}/respond", runtime.handleRespond)
	mux.HandleFunc("POST "+prefix+"/sessions/{sessionID}/cancel", runtime.handleCancel)
	mux.HandleFunc("POST "+prefix+"/sessions/{sessionID}/runs/{runID}/cancel", runtime.handleCancel)
	return nil
}

// Handler returns a standalone mux mounted at prefix.
func (runtime *Runtime) Handler(prefix string) (http.Handler, error) {
	mux := http.NewServeMux()
	if err := runtime.Mount(mux, prefix); err != nil {
		return nil, err
	}
	return mux, nil
}

func normalizePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return "", errors.New("agent HTTP prefix must be a non-root absolute path")
	}
	if !strings.HasPrefix(prefix, "/") {
		return "", errors.New("agent HTTP prefix must start with /")
	}
	return strings.TrimRight(prefix, "/"), nil
}

func (runtime *Runtime) handleStart(prefix string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		agentID := request.PathValue("agentID")
		var input startRequest
		if err := decodeJSONBody(writer, request, &input, false); err != nil {
			return
		}
		runtime.startSession(writer, request, prefix, agentID, input)
	}
}

func (runtime *Runtime) handleStartByRequest(prefix string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var input startRequest
		if err := decodeJSONBody(writer, request, &input, false); err != nil {
			return
		}
		if strings.TrimSpace(input.AgentID) == "" {
			writeError(writer, http.StatusBadRequest, "agent_id_required", "agentId is required")
			return
		}
		runtime.startSession(writer, request, prefix, input.AgentID, input)
	}
}

func (runtime *Runtime) startSession(writer http.ResponseWriter, request *http.Request, prefix, agentID string, input startRequest) {
	adapter, ok := runtime.registry.Lookup(agentID)
	if !ok {
		writeError(writer, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	config := adapter.Config()
	if config.Mode() == agents.DurableMode && runtime.dispatcher == nil {
		writeError(writer, http.StatusServiceUnavailable, "durable_dispatcher_unavailable", "durable agent dispatcher is unavailable")
		return
	}
	actor, err := runtime.actorForRequest(request, config.Public)
	if err != nil {
		writeActorError(writer, err)
		return
	}
	if len(input.Input) == 0 {
		input.Input = json.RawMessage("null")
	}
	session, run, err := runtime.createSession(agentID, adapter, actor, input.Metadata, input.Model)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "session_create_failed", "could not create agent session")
		return
	}
	runtime.launchRun(session, run, adapter, actor, input.Input, true)
	writeStartResponse(writer, prefix, session, run)
}

func (runtime *Runtime) handleResume(prefix string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		sessionID := request.PathValue("sessionID")
		actor, err := runtime.sessionAuthorization(sessionID, request)
		if err != nil {
			writeSessionAccessError(writer, err)
			return
		}
		var input startRequest
		if err := decodeJSONBody(writer, request, &input, true); err != nil {
			return
		}
		if len(input.Input) == 0 {
			input.Input = json.RawMessage("null")
		}
		adapter, session, run, err := runtime.createRun(sessionID, input.Metadata, input.Model)
		if err != nil {
			switch {
			case errors.Is(err, errSessionNotFound):
				writeError(writer, http.StatusNotFound, "session_not_found", "agent session not found")
			case errors.Is(err, errRunActive):
				writeError(writer, http.StatusConflict, "run_active", "agent session already has an active run")
			default:
				writeError(writer, http.StatusInternalServerError, "run_create_failed", "could not create agent run")
			}
			return
		}
		runtime.launchRun(session, run, adapter, actor, input.Input, false)
		writeStartResponse(writer, prefix, session, run)
	}
}

func (runtime *Runtime) launchRun(session agents.Session, run agents.Run, adapter Adapter, actor agents.Actor, input json.RawMessage, created bool) {
	if created {
		_, _ = runtime.emit(session.ID, run.ID, "session.created", map[string]any{"sessionId": session.ID, "agentId": session.AgentID})
	}
	_, _ = runtime.emit(session.ID, run.ID, "run.started", map[string]any{"runId": run.ID, "mode": run.Mode})
	runContext, cancel := context.WithCancel(runtime.baseContext)
	runtime.setRunCancel(session.ID, run.ID, cancel)
	call := StartCall{Session: session, Run: run, Actor: actor, Input: cloneRaw(input)}
	emitter := boundEmitter{runtime: runtime, sessionID: session.ID, runID: run.ID}
	go runtime.executeStart(runContext, adapter, call, emitter)
}

func writeStartResponse(writer http.ResponseWriter, prefix string, session agents.Session, run agents.Run) {
	writeJSON(writer, http.StatusAccepted, startResponse{
		Session: session, Run: run,
		EventsURL: prefix + "/sessions/" + session.ID + "/events",
	})
}

func (runtime *Runtime) executeStart(ctx context.Context, adapter Adapter, call StartCall, emitter EventEmitter) {
	var err error
	if call.Run.Mode == agents.DurableMode {
		err = runtime.dispatcher.Start(ctx, adapter, call, emitter)
	} else {
		err = adapter.Start(ctx, call, emitter)
	}
	runtime.finishRun(call.Session.ID, call.Run.ID, err)
}

func (runtime *Runtime) handleGetSession(writer http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("sessionID")
	if _, err := runtime.sessionAuthorization(sessionID, request); err != nil {
		writeSessionAccessError(writer, err)
		return
	}
	view, ok := runtime.sessionView(sessionID)
	if !ok {
		writeError(writer, http.StatusNotFound, "session_not_found", "agent session not found")
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (runtime *Runtime) handleRespond(writer http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("sessionID")
	actor, err := runtime.sessionAuthorization(sessionID, request)
	if err != nil {
		writeSessionAccessError(writer, err)
		return
	}
	var input respondRequest
	if err := decodeJSONBody(writer, request, &input, false); err != nil {
		return
	}
	if len(input.Response) == 0 && input.InteractionID != "" {
		response, marshalErr := json.Marshal(map[string]any{
			"interactionId": input.InteractionID,
			"answers":       json.RawMessage(input.Answers),
		})
		if marshalErr != nil {
			writeError(writer, http.StatusBadRequest, "invalid_response", "response must be valid JSON")
			return
		}
		input.Response = response
	}
	if len(input.Response) == 0 {
		writeError(writer, http.StatusBadRequest, "response_required", "response is required")
		return
	}
	adapter, session, run, ok := runtime.latestRun(sessionID, input.RunID)
	if !ok {
		writeError(writer, http.StatusNotFound, "run_not_found", "agent run not found")
		return
	}
	if run.Status != RunStatusRunning {
		writeError(writer, http.StatusConflict, "run_not_active", "agent run is not active")
		return
	}
	call := RespondCall{Session: session, Run: run, Actor: actor, Response: cloneRaw(input.Response)}
	emitter := boundEmitter{runtime: runtime, sessionID: sessionID, runID: run.ID}
	if _, err := runtime.emitForRun(sessionID, run.ID, "run.response.received", map[string]any{"runId": run.ID}); err != nil {
		if errors.Is(err, errRunNotActive) {
			writeError(writer, http.StatusConflict, "run_not_active", "agent run is not active")
			return
		}
		writeError(writer, http.StatusNotFound, "run_not_found", "agent run not found")
		return
	}
	if run.Mode == agents.DurableMode {
		err = runtime.dispatcher.Respond(request.Context(), adapter, call, emitter)
	} else {
		err = adapter.Respond(request.Context(), call, emitter)
	}
	if err != nil {
		if errors.Is(err, ErrRespondUnsupported) {
			writeError(writer, http.StatusConflict, "response_unsupported", ErrRespondUnsupported.Error())
			return
		}
		writeError(writer, http.StatusBadGateway, "response_dispatch_failed", "agent response could not be dispatched")
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"accepted":  true,
		"sessionId": sessionID,
		"runId":     run.ID,
		"session":   session,
		"run":       run,
	})
}

func (runtime *Runtime) handleCancel(writer http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("sessionID")
	actor, err := runtime.sessionAuthorization(sessionID, request)
	if err != nil {
		writeSessionAccessError(writer, err)
		return
	}
	var input cancelRequest
	if err := decodeJSONBody(writer, request, &input, true); err != nil {
		return
	}
	pathRunID := request.PathValue("runID")
	if pathRunID != "" && input.RunID != "" && input.RunID != pathRunID {
		writeError(writer, http.StatusBadRequest, "run_id_mismatch", "runId does not match the request path")
		return
	}
	if input.RunID == "" {
		input.RunID = pathRunID
	}
	adapter, session, run, ok := runtime.latestRun(sessionID, input.RunID)
	if !ok {
		writeError(writer, http.StatusNotFound, "run_not_found", "agent run not found")
		return
	}
	adapter, call, cancel, changed := runtime.beginCancel(sessionID, run.ID, input.Reason)
	if !changed {
		writeError(writer, http.StatusConflict, "run_not_active", "agent run is not active")
		return
	}
	call.Actor = actor
	emitter := boundEmitter{runtime: runtime, sessionID: sessionID, runID: run.ID}
	if run.Mode == agents.DurableMode {
		err = runtime.dispatcher.Cancel(request.Context(), adapter, call, emitter)
	} else {
		err = adapter.Cancel(request.Context(), call, emitter)
	}
	if err != nil {
		runtime.abortCancel(sessionID, run.ID)
		writeError(writer, http.StatusBadGateway, "cancel_dispatch_failed", "agent cancellation could not be dispatched")
		return
	}
	session, run, committed := runtime.commitCancel(sessionID, run.ID, input.Reason)
	if !committed {
		writeError(writer, http.StatusConflict, "run_not_active", "agent run is not active")
		return
	}
	if cancel != nil {
		cancel()
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"cancelled": true,
		"sessionId": session.ID,
		"runId":     run.ID,
		"session":   session,
		"run":       run,
	})
}

func (runtime *Runtime) handleEvents(writer http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("sessionID")
	if _, err := runtime.sessionAuthorization(sessionID, request); err != nil {
		writeSessionAccessError(writer, err)
		return
	}
	cursor := request.URL.Query().Get("after")
	if cursor == "" {
		cursor = request.URL.Query().Get("cursor")
	}
	if cursor == "" {
		cursor = request.Header.Get("Last-Event-ID")
	}
	after, err := parseEventCursor(sessionID, cursor)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	runID := request.PathValue("runID")
	backlog, updates, cleanup, terminal, err := runtime.subscribe(sessionID, runID, after)
	if err != nil {
		if errors.Is(err, errSessionNotFound) {
			writeError(writer, http.StatusNotFound, "session_not_found", "agent session not found")
			return
		}
		if errors.Is(err, errRunNotFound) {
			writeError(writer, http.StatusNotFound, "run_not_found", "agent run not found")
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	defer cleanup()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "streaming_unsupported", "HTTP streaming is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)

	for _, event := range backlog {
		if err := writeSSEEvent(writer, event); err != nil {
			return
		}
	}
	flusher.Flush()
	if terminal {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, ok := <-updates:
			if !ok {
				return
			}
			if runID != "" && event.RunID != runID {
				continue
			}
			if err := writeSSEEvent(writer, event); err != nil {
				return
			}
			flusher.Flush()
			if event.Type == "run.completed" || event.Type == "run.failed" || event.Type == "run.cancelled" {
				return
			}
		case <-keepalive.C:
			if _, err := io.WriteString(writer, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSEEvent(writer io.Writer, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, payload)
	return err
}

func decodeJSONBody(writer http.ResponseWriter, request *http.Request, target any, allowEmpty bool) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	reader := bufio.NewReader(request.Body)
	if allowEmpty {
		if _, err := reader.Peek(1); errors.Is(err, io.EOF) {
			return nil
		}
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value")
		return errors.New("request body contains trailing JSON")
	}
	return nil
}

func writeActorError(writer http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnauthenticated) {
		writeError(writer, http.StatusUnauthorized, "actor_required", "authenticated agent actor is required")
		return
	}
	writeError(writer, http.StatusInternalServerError, "actor_resolution_failed", "agent actor could not be resolved")
}

func writeSessionAccessError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSessionNotFound):
		writeError(writer, http.StatusNotFound, "session_not_found", "agent session not found")
	case errors.Is(err, errActorForbidden):
		writeError(writer, http.StatusForbidden, "actor_forbidden", "agent actor does not own this session")
	default:
		writeActorError(writer, err)
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
