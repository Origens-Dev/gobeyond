package httpruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
)

const (
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"

	PublicActorID   = "public"
	PublicActorKind = "public"
)

var (
	eventTypePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	generatedIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ActorResolver resolves a hosted authenticated principal. Returning
// ErrUnauthenticated permits the built-in loopback development fallback.
type ActorResolver func(*http.Request) (agents.Actor, error)

// IDGenerator returns an ID for the supplied prefix (for example, "ses" or
// "run"). It exists primarily for deterministic tests and host integration.
type IDGenerator func(prefix string) (string, error)

type Options struct {
	Registry           Registry
	Dispatcher         Dispatcher
	ResolveActor       ActorResolver
	AllowLoopbackActor bool
	BaseContext        context.Context
	Now                func() time.Time
	NewID              IDGenerator
}

// Runtime owns the in-memory sessions, runs, cursors, and subscribers for one
// local process.
type Runtime struct {
	registry           Registry
	dispatcher         Dispatcher
	resolveActor       ActorResolver
	allowLoopbackActor bool
	baseContext        context.Context
	now                func() time.Time
	newID              IDGenerator

	mu               sync.Mutex
	sessions         map[string]*sessionState
	nextSubscriberID uint64
}

type sessionState struct {
	session     agents.Session
	public      bool
	adapter     Adapter
	runs        map[string]*runState
	runOrder    []string
	events      []Event
	nextEvent   uint64
	subscribers map[uint64]chan Event
}

type runState struct {
	run              agents.Run
	cancel           context.CancelFunc
	cancelPending    bool
	pendingFinish    bool
	pendingFinishErr error
}

// Event is the canonical persisted and SSE-delivered session event.
type Event struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	RunID     string          `json:"runId,omitempty"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"createdAt"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// SessionView is returned by GET session.
type SessionView struct {
	Session agents.Session `json:"session"`
	Runs    []agents.Run   `json:"runs"`
	Cursor  string         `json:"cursor,omitempty"`
}

func New(options Options) (*Runtime, error) {
	if options.Registry == nil {
		return nil, errors.New("agent registry is required")
	}
	baseContext := options.BaseContext
	if baseContext == nil {
		baseContext = context.Background()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomID
	}
	return &Runtime{
		registry:           options.Registry,
		dispatcher:         options.Dispatcher,
		resolveActor:       options.ResolveActor,
		allowLoopbackActor: options.AllowLoopbackActor,
		baseContext:        baseContext,
		now:                now,
		newID:              newID,
		sessions:           map[string]*sessionState{},
	}, nil
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func publicActor() agents.Actor {
	return agents.Actor{ID: PublicActorID, Kind: PublicActorKind}
}

func (runtime *Runtime) actorForRequest(request *http.Request, public bool) (agents.Actor, error) {
	if public {
		return publicActor(), nil
	}
	if runtime.resolveActor != nil {
		actor, err := runtime.resolveActor(request)
		if err == nil {
			if err := actor.Validate(); err != nil {
				return agents.Actor{}, ErrUnauthenticated
			}
			return actor, nil
		}
		if !errors.Is(err, ErrUnauthenticated) {
			return agents.Actor{}, err
		}
	}
	if runtime.allowLoopbackActor && isLoopbackRequest(request) {
		return agents.LoopbackDevActor(), nil
	}
	return agents.Actor{}, ErrUnauthenticated
}

func isLoopbackRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	host := request.RemoteAddr
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameActor(left, right agents.Actor) bool {
	return left.ID == right.ID && left.Kind == right.Kind
}

func cloneActor(actor agents.Actor) agents.Actor {
	actor.Metadata = cloneStringMap(actor.Metadata)
	return actor
}

func cloneSession(session agents.Session) agents.Session {
	session.Actor = cloneActor(session.Actor)
	session.Metadata = cloneStringMap(session.Metadata)
	return session
}

func cloneRun(run agents.Run) agents.Run { return run }

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneRaw(input json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), input...)
}

func (runtime *Runtime) createSession(agentID string, adapter Adapter, actor agents.Actor, metadata map[string]string, model agents.ModelMetadata) (agents.Session, agents.Run, error) {
	sessionID, err := runtime.newID("ses")
	if err != nil {
		return agents.Session{}, agents.Run{}, fmt.Errorf("create session ID: %w", err)
	}
	runID, err := runtime.newID("run")
	if err != nil {
		return agents.Session{}, agents.Run{}, fmt.Errorf("create run ID: %w", err)
	}
	if !generatedIDPattern.MatchString(sessionID) || !generatedIDPattern.MatchString(runID) || sessionID == runID {
		return agents.Session{}, agents.Run{}, errors.New("agent ID generator returned an invalid or duplicate ID")
	}
	now := runtime.now().UTC()
	config := adapter.Config()
	session := agents.Session{
		ID: sessionID, AgentID: agentID, Actor: cloneActor(actor), Model: model,
		CreatedAt: now, UpdatedAt: now, Metadata: cloneStringMap(metadata),
	}
	run := agents.Run{
		ID: runID, SessionID: sessionID, AgentID: agentID, Mode: config.Mode(),
		TaskQueue: config.TaskQueue, Model: model, Status: RunStatusRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	runtime.mu.Lock()
	if _, exists := runtime.sessions[sessionID]; exists {
		runtime.mu.Unlock()
		return agents.Session{}, agents.Run{}, fmt.Errorf("session ID %q already exists", sessionID)
	}
	state := &sessionState{
		session: session, public: config.Public, adapter: adapter,
		runs: map[string]*runState{runID: {run: run}}, runOrder: []string{runID},
		subscribers: map[uint64]chan Event{},
	}
	runtime.sessions[sessionID] = state
	runtime.mu.Unlock()
	return cloneSession(session), cloneRun(run), nil
}

// createRun appends a new execution to an existing session. A session has at
// most one active run at a time: callers must await a terminal result
// before resuming it. This keeps respond/cancel unambiguous while preserving a
// complete run history on the session.
func (runtime *Runtime) createRun(sessionID string, metadata map[string]string, model agents.ModelMetadata) (Adapter, agents.Session, agents.Run, error) {
	runID, err := runtime.newID("run")
	if err != nil {
		return nil, agents.Session{}, agents.Run{}, fmt.Errorf("create run ID: %w", err)
	}
	if !generatedIDPattern.MatchString(runID) {
		return nil, agents.Session{}, agents.Run{}, errors.New("agent ID generator returned an invalid run ID")
	}
	now := runtime.now().UTC()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return nil, agents.Session{}, agents.Run{}, errSessionNotFound
	}
	for _, priorID := range state.runOrder {
		if prior := state.runs[priorID]; prior != nil && prior.run.Status == RunStatusRunning {
			return nil, agents.Session{}, agents.Run{}, errRunActive
		}
	}
	if metadata != nil {
		state.session.Metadata = cloneStringMap(metadata)
	}
	if model.Provider != "" || model.Model != "" {
		state.session.Model = model
	}
	if _, exists := state.runs[runID]; exists {
		return nil, agents.Session{}, agents.Run{}, fmt.Errorf("run ID %q already exists", runID)
	}
	config := state.adapter.Config()
	run := agents.Run{
		ID: runID, SessionID: sessionID, AgentID: state.session.AgentID, Mode: config.Mode(),
		TaskQueue: config.TaskQueue, Model: state.session.Model, Status: RunStatusRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	state.runs[runID] = &runState{run: run}
	state.runOrder = append(state.runOrder, runID)
	state.session.UpdatedAt = now
	return state.adapter, cloneSession(state.session), cloneRun(run), nil
}

func (runtime *Runtime) setRunCancel(sessionID, runID string, cancel context.CancelFunc) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if state := runtime.sessions[sessionID]; state != nil {
		if run := state.runs[runID]; run != nil {
			run.cancel = cancel
		}
	}
}

func (runtime *Runtime) finishRun(sessionID, runID string, runErr error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return
	}
	run := state.runs[runID]
	if run == nil || runTerminal(run.run.Status) {
		return
	}
	if run.cancelPending {
		run.pendingFinish = true
		run.pendingFinishErr = runErr
		return
	}
	runtime.finishRunLocked(state, run, sessionID, runID, runErr)
}

func (runtime *Runtime) finishRunLocked(state *sessionState, run *runState, sessionID, runID string, runErr error) {
	status := RunStatusCompleted
	eventType := "run.completed"
	if runErr != nil {
		status = RunStatusFailed
		eventType = "run.failed"
	}
	payload := any(map[string]any{"status": status})
	if runErr != nil {
		payload = map[string]any{"status": status, "message": "agent run failed"}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	now := runtime.now().UTC()
	run.run.Status = status
	run.run.UpdatedAt = now
	state.session.UpdatedAt = now
	runtime.appendEventLocked(state, sessionID, runID, eventType, encoded)
}

// beginCancel reserves the terminal transition without publishing it. The
// caller must synchronously dispatch cancellation and then call commitCancel
// or abortCancel. While reserved, finishRun is deferred so a completion racing
// cancellation cannot be overwritten or lost.
func (runtime *Runtime) beginCancel(sessionID, runID, reason string) (Adapter, CancelCall, context.CancelFunc, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return nil, CancelCall{}, nil, false
	}
	run := state.runs[runID]
	if run == nil {
		return nil, CancelCall{}, nil, false
	}
	if runTerminal(run.run.Status) || run.cancelPending {
		return state.adapter, CancelCall{Session: cloneSession(state.session), Run: cloneRun(run.run), Actor: cloneActor(state.session.Actor), Reason: reason}, nil, false
	}
	run.cancelPending = true
	call := CancelCall{Session: cloneSession(state.session), Run: cloneRun(run.run), Actor: cloneActor(state.session.Actor), Reason: reason}
	return state.adapter, call, run.cancel, true
}

func (runtime *Runtime) commitCancel(sessionID, runID, reason string) (agents.Session, agents.Run, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return agents.Session{}, agents.Run{}, false
	}
	run := state.runs[runID]
	if run == nil || !run.cancelPending || runTerminal(run.run.Status) {
		return agents.Session{}, agents.Run{}, false
	}
	run.cancelPending = false
	run.pendingFinish = false
	run.pendingFinishErr = nil
	now := runtime.now().UTC()
	run.run.Status = RunStatusCancelled
	run.run.UpdatedAt = now
	state.session.UpdatedAt = now
	payload, _ := json.Marshal(map[string]any{"runId": runID, "reason": reason})
	runtime.appendEventLocked(state, sessionID, runID, "run.cancelled", payload)
	return cloneSession(state.session), cloneRun(run.run), true
}

func (runtime *Runtime) abortCancel(sessionID, runID string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return
	}
	run := state.runs[runID]
	if run == nil || !run.cancelPending {
		return
	}
	run.cancelPending = false
	if !run.pendingFinish {
		return
	}
	runErr := run.pendingFinishErr
	run.pendingFinish = false
	run.pendingFinishErr = nil
	runtime.finishRunLocked(state, run, sessionID, runID, runErr)
}

func (runtime *Runtime) sessionView(sessionID string) (SessionView, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return SessionView{}, false
	}
	view := SessionView{Session: cloneSession(state.session), Runs: make([]agents.Run, 0, len(state.runOrder))}
	for _, runID := range state.runOrder {
		view.Runs = append(view.Runs, cloneRun(state.runs[runID].run))
	}
	if len(state.events) > 0 {
		view.Cursor = state.events[len(state.events)-1].ID
	}
	return view, true
}

func (runtime *Runtime) sessionAuthorization(sessionID string, request *http.Request) (agents.Actor, error) {
	runtime.mu.Lock()
	state := runtime.sessions[sessionID]
	if state == nil {
		runtime.mu.Unlock()
		return agents.Actor{}, errSessionNotFound
	}
	public := state.public
	owner := cloneActor(state.session.Actor)
	runtime.mu.Unlock()

	actor, err := runtime.actorForRequest(request, public)
	if err != nil {
		return agents.Actor{}, err
	}
	if !public && !sameActor(actor, owner) {
		return agents.Actor{}, errActorForbidden
	}
	return actor, nil
}

func (runtime *Runtime) latestRun(sessionID, requestedRunID string) (Adapter, agents.Session, agents.Run, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil || len(state.runOrder) == 0 {
		return nil, agents.Session{}, agents.Run{}, false
	}
	runID := requestedRunID
	if runID == "" {
		runID = state.runOrder[len(state.runOrder)-1]
	}
	run := state.runs[runID]
	if run == nil {
		return nil, agents.Session{}, agents.Run{}, false
	}
	return state.adapter, cloneSession(state.session), cloneRun(run.run), true
}

func (runtime *Runtime) emit(sessionID, runID, eventType string, data any) (Event, error) {
	if !eventTypePattern.MatchString(eventType) {
		return Event{}, fmt.Errorf("invalid agent event type %q", eventType)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode agent event %q: %w", eventType, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return Event{}, errSessionNotFound
	}
	return runtime.appendEventLocked(state, sessionID, runID, eventType, payload), nil
}

// emitForRun prevents adapters that outlive their cancellation context from
// appending application data after the run has reached a terminal state.
func (runtime *Runtime) emitForRun(sessionID, runID, eventType string, data any) (Event, error) {
	if !eventTypePattern.MatchString(eventType) {
		return Event{}, fmt.Errorf("invalid agent event type %q", eventType)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode agent event %q: %w", eventType, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return Event{}, errSessionNotFound
	}
	run := state.runs[runID]
	if run == nil {
		return Event{}, errRunNotFound
	}
	if run.run.Status != RunStatusRunning {
		return Event{}, errRunNotActive
	}
	return runtime.appendEventLocked(state, sessionID, runID, eventType, payload), nil
}

func (runtime *Runtime) appendEventLocked(state *sessionState, sessionID, runID, eventType string, payload json.RawMessage) Event {
	state.nextEvent++
	event := Event{
		ID: canonicalEventID(sessionID, state.nextEvent), SessionID: sessionID,
		RunID: runID, Type: eventType, CreatedAt: runtime.now().UTC(), Data: cloneRaw(payload),
	}
	state.events = append(state.events, event)
	for subscriberID, subscriber := range state.subscribers {
		select {
		case subscriber <- event:
		default:
			close(subscriber)
			delete(state.subscribers, subscriberID)
		}
	}
	return event
}

func canonicalEventID(sessionID string, sequence uint64) string {
	return sessionID + ":" + fmt.Sprintf("%020d", sequence)
}

func parseEventCursor(sessionID, cursor string) (uint64, error) {
	if strings.TrimSpace(cursor) == "" {
		return 0, nil
	}
	prefix := sessionID + ":"
	if !strings.HasPrefix(cursor, prefix) {
		return 0, errors.New("event cursor does not belong to this session")
	}
	sequenceText := strings.TrimPrefix(cursor, prefix)
	if len(sequenceText) != 20 {
		return 0, errors.New("event cursor is not canonical")
	}
	sequence, err := strconv.ParseUint(sequenceText, 10, 64)
	if err != nil {
		return 0, errors.New("event cursor is not canonical")
	}
	return sequence, nil
}

func (runtime *Runtime) subscribe(sessionID, runID string, after uint64) ([]Event, <-chan Event, func(), bool, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.sessions[sessionID]
	if state == nil {
		return nil, nil, nil, false, errSessionNotFound
	}
	if after > state.nextEvent {
		return nil, nil, nil, false, errors.New("event cursor is ahead of the session")
	}
	if runID != "" && state.runs[runID] == nil {
		return nil, nil, nil, false, errRunNotFound
	}
	backlog := make([]Event, 0, int(state.nextEvent-after))
	for _, event := range state.events {
		sequence, _ := parseEventCursor(sessionID, event.ID)
		if sequence > after && (runID == "" || event.RunID == runID) {
			backlog = append(backlog, event)
		}
	}
	terminal := stateTerminal(state)
	if runID != "" {
		terminal = runTerminal(state.runs[runID].run.Status)
	}
	if terminal {
		return backlog, nil, func() {}, true, nil
	}
	runtime.nextSubscriberID++
	subscriberID := runtime.nextSubscriberID
	updates := make(chan Event, 64)
	state.subscribers[subscriberID] = updates
	cleanup := func() {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		current := runtime.sessions[sessionID]
		if current == nil {
			return
		}
		if subscriber, ok := current.subscribers[subscriberID]; ok {
			delete(current.subscribers, subscriberID)
			close(subscriber)
		}
	}
	return backlog, updates, cleanup, false, nil
}

func stateTerminal(state *sessionState) bool {
	if state == nil || len(state.runOrder) == 0 {
		return false
	}
	for _, runID := range state.runOrder {
		if !runTerminal(state.runs[runID].run.Status) {
			return false
		}
	}
	return true
}

func runTerminal(status string) bool {
	return status == RunStatusCompleted || status == RunStatusFailed || status == RunStatusCancelled
}

type boundEmitter struct {
	runtime   *Runtime
	sessionID string
	runID     string
}

func (emitter boundEmitter) Emit(ctx context.Context, eventType string, data any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := emitter.runtime.emitForRun(emitter.sessionID, emitter.runID, eventType, data)
	return err
}

var (
	errSessionNotFound = errors.New("agent session not found")
	errRunNotFound     = errors.New("agent run not found")
	errRunActive       = errors.New("agent run is already active")
	errRunNotActive    = errors.New("agent run is not active")
	errActorForbidden  = errors.New("agent actor does not own this session")
)
