package sip

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Registerer is the narrow interface accepted by compiler-generated
// GobeyondRegisterSIP functions.
type Registerer interface {
	Register(agentID string, h Handlers) error
}

// Registry maps agent ID → Handlers for platform SIP decision RPC.
type Registry map[string]Handlers

// NewRegistry returns an empty Registry.
func NewRegistry() Registry {
	return Registry{}
}

// Register stores handlers for agentID. Duplicate IDs return an error.
func (reg Registry) Register(agentID string, h Handlers) error {
	if reg == nil {
		return errors.New("sip registry is required")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent ID is required")
	}
	if _, exists := reg[agentID]; exists {
		return fmt.Errorf("sip agent %q is already registered", agentID)
	}
	reg[agentID] = h
	return nil
}

// Handler returns an http.Handler serving DecidePath and ObservePath.
// When token is non-empty, requests must present AuthHeader with that value.
func (reg Registry) Handler(token string) http.Handler {
	token = strings.TrimSpace(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get(AuthHeader) != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var envelope PlatformRequest
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req Request
		if len(envelope.Public) > 0 {
			if err := json.Unmarshal(envelope.Public, &req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		agentID := strings.TrimSpace(req.AgentID)
		handlers, ok := reg[agentID]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case DecidePath:
			resp, err := handlers.Decide(r.Context(), req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			out := PlatformResponse{
				Decision:  resp.Decision,
				SIPStatus: resp.SIPStatus,
				Reason:    resp.Reason,
				Metadata:  resp.Metadata,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		case ObservePath:
			if err := handlers.Observe(r.Context(), req); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}
