package httpruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

var errApprovalResponseMismatch = errors.New("tool approval response does not match the pending interaction")

type approvalResult struct {
	approved bool
	reason   string
}

type pendingApproval struct {
	sessionID string
	runID     string
	actorID   string
	actorKind string
	toolCall  string
	toolName  string
	inputHash string
	result    chan approvalResult
}

type approvalManager struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
}

func newApprovalManager() *approvalManager {
	return &approvalManager{pending: make(map[string]pendingApproval)}
}

func (manager *approvalManager) request(ctx context.Context, call StartCall, toolCall ai.ToolCall, tool ai.Tool, emit EventEmitter) (ai.ApprovalDecision, error) {
	if manager == nil || emit == nil {
		return ai.ApprovalDecision{}, errors.New("native tool approval delivery is unavailable")
	}
	inputHash, err := approvalInputHash(toolCall.Input)
	if err != nil {
		return ai.ApprovalDecision{}, err
	}
	interactionID, err := randomInteractionID()
	if err != nil {
		return ai.ApprovalDecision{}, err
	}
	pending := pendingApproval{
		sessionID: call.Session.ID, runID: call.Run.ID,
		actorID: call.Actor.ID, actorKind: call.Actor.Kind,
		toolCall: toolCall.ToolCallID, toolName: toolCall.ToolName,
		inputHash: inputHash, result: make(chan approvalResult, 1),
	}
	manager.mu.Lock()
	manager.pending[interactionID] = pending
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		delete(manager.pending, interactionID)
		manager.mu.Unlock()
	}()
	title := strings.TrimSpace(tool.Title)
	if title == "" {
		title = tool.Name
	}
	if err := emit.Emit(ctx, "agent.interaction.requested", map[string]any{
		"interactionId":    interactionID,
		"interactionType":  "tool-approval",
		"toolCallId":       toolCall.ToolCallID,
		"toolName":         toolCall.ToolName,
		"title":            title,
		"input":            toolCall.Input,
		"inputHash":        inputHash,
		"requiresApproval": true,
	}); err != nil {
		return ai.ApprovalDecision{}, err
	}
	select {
	case response := <-pending.result:
		if response.approved {
			return ai.Approved(response.reason), nil
		}
		return ai.Denied(response.reason), nil
	case <-ctx.Done():
		return ai.Denied("approval request canceled or timed out"), nil
	}
}

func (manager *approvalManager) respond(call RespondCall, interactionID, toolCallID, toolName, inputHash string, approved bool, reason string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	pending, ok := manager.pending[interactionID]
	if !ok || pending.sessionID != call.Session.ID || pending.runID != call.Run.ID || pending.actorID != call.Actor.ID || pending.actorKind != call.Actor.Kind || pending.toolCall != toolCallID || pending.toolName != toolName || pending.inputHash != inputHash {
		return errApprovalResponseMismatch
	}
	select {
	case pending.result <- approvalResult{approved: approved, reason: reason}:
		return nil
	default:
		return errApprovalResponseMismatch
	}
}

func approvalInputHash(input any) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode tool approval input: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func randomInteractionID() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create tool approval interaction ID: %w", err)
	}
	return "approval_" + hex.EncodeToString(random[:]), nil
}
