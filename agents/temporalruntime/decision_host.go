package temporalruntime

import (
	"context"
	"errors"
	"fmt"

	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/decisions/callback"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
)

// DecisionMatchAdapter is an optional httpruntime.DecisionAdapter extension.
// When an adapter does not implement it, EffectRunDecision stays dormant
// (default-off). Implementations reuse DecisionSessionWorkflow and the
// decisions reducer; they must not start a second runtime.
//
// MatchClient, when non-nil, is the host-side signed POST and calls
// connections.SignDelivery directly. A nil client may fall back to an
// in-process callback.HostCallback. Either path must ValidateFor the frozen
// definition before BindDecisionMatch; unknown clarify prompt IDs fail closed
// and must not emit say.
type DecisionMatchAdapter interface {
	httpruntime.DecisionAdapter
	PrepareDecisionMatch(context.Context, httpruntime.RespondCall, DecisionSessionIdentity, decisions.Effect) (callback.MatchRequest, error)
	DecisionMatchClient() *callback.MatchClient
	BindDecisionMatch(callback.MatchResponse, httpruntime.RespondCall, DecisionSessionIdentity, decisions.Effect) (DecisionSessionUpdate, error)
}

func hasOptionalHostEffectWork(effects []decisions.Effect) bool {
	for _, effect := range effects {
		switch effect.Kind {
		case decisions.EffectSay, decisions.EffectListen, decisions.EffectRunDecision:
			return true
		}
	}
	return false
}

func (dispatcher *Dispatcher) dispatchOptionalHostEffects(ctx context.Context, adapter httpruntime.Adapter, call httpruntime.RespondCall, result DecisionSessionAdvanceResult, emit httpruntime.EventEmitter) error {
	if adapter == nil {
		return nil
	}
	for _, effect := range result.Effects {
		switch effect.Kind {
		case decisions.EffectSay:
			if err := dispatcher.dispatchHostSay(ctx, adapter, effect); err != nil {
				return err
			}
		case decisions.EffectListen:
			if err := dispatcher.dispatchHostListen(ctx, adapter, effect); err != nil {
				return err
			}
		case decisions.EffectRunDecision:
			if err := dispatcher.dispatchHostRunDecision(ctx, adapter, call, result, effect, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (dispatcher *Dispatcher) dispatchHostSay(ctx context.Context, adapter httpruntime.Adapter, effect decisions.Effect) error {
	host, ok := adapter.(callback.DecisionEffectAdapter)
	if !ok {
		return nil
	}
	say, err := callback.SayEffectFrom(effect)
	if err != nil {
		return err
	}
	definition, ok := decisionDefinitionOf(adapter)
	if !ok {
		return nil
	}
	if err := definition.RequirePrompt(say.PromptID); err != nil {
		return err
	}
	_, err = host.Say(ctx, say)
	return err
}

func (dispatcher *Dispatcher) dispatchHostListen(ctx context.Context, adapter httpruntime.Adapter, effect decisions.Effect) error {
	host, ok := adapter.(callback.DecisionEffectAdapter)
	if !ok {
		return nil
	}
	preparer, ok := adapter.(callback.ListenPolicy)
	if !ok {
		return nil
	}
	listen, err := preparer.PrepareListen(effect)
	if err != nil {
		return err
	}
	if err := listen.Validate(); err != nil {
		return err
	}
	return host.Listen(ctx, listen)
}

func (dispatcher *Dispatcher) dispatchHostRunDecision(ctx context.Context, adapter httpruntime.Adapter, call httpruntime.RespondCall, result DecisionSessionAdvanceResult, effect decisions.Effect, emit httpruntime.EventEmitter) error {
	matchAdapter, ok := adapter.(DecisionMatchAdapter)
	if !ok {
		return nil
	}
	if result.Snapshot.Status != DecisionSessionRunning {
		return errors.New("cancelled or stopped decision sessions cannot run a decision match")
	}
	request, err := matchAdapter.PrepareDecisionMatch(ctx, call, result.Snapshot.Identity, effect)
	if err != nil {
		return fmt.Errorf("prepare decision match: %w", err)
	}
	definition := matchAdapter.DecisionDefinition()
	response, invoked, err := invokeDecisionMatch(ctx, adapter, matchAdapter, definition, request)
	if err != nil {
		return err
	}
	if !invoked {
		return nil
	}
	if err := response.ValidateFor(definition); err != nil {
		return err
	}
	update, err := matchAdapter.BindDecisionMatch(response, call, result.Snapshot.Identity, effect)
	if err != nil {
		return fmt.Errorf("bind decision match: %w", err)
	}
	if update.Identity.SessionID != call.Session.ID || update.Identity.RunID != call.Run.ID {
		return errors.New("decision match update identity does not match the requested session and run")
	}
	if err := validateDecisionSessionUpdatePrivacy(update, nil); err != nil {
		return fmt.Errorf("decision match update failed privacy or contract validation: %w", err)
	}
	updateID, err := decisionUpdateID(update.Event)
	if err != nil {
		return err
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	var advanced DecisionSessionAdvanceResult
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionAdvanceUpdate, updateID, update, &advanced); err != nil {
		return fmt.Errorf("advance durable decision session after match: %w", err)
	}
	if advanced.Stopped {
		if emit != nil {
			if err := emit.Emit(ctx, "agent.decision.stopped", advanced.Snapshot); err != nil {
				return fmt.Errorf("emit stopped decision session after match: %w", err)
			}
		}
		return nil
	}
	if !advanced.Accepted {
		return errors.New("durable decision session did not accept the match result")
	}
	if len(advanced.Effects) == 0 {
		return nil
	}
	if err := dispatcher.emitDecisionTransition(ctx, emit, advanced); err != nil {
		return err
	}
	if hasDecisionEffectWork(advanced.Effects) {
		authority, ok := adapter.(DecisionEffectAdapter)
		if !ok {
			return ErrDecisionEffectAuthorityRequired
		}
		if err := dispatcher.dispatchDecisionTransition(ctx, authority, call, advanced, emit); err != nil {
			return err
		}
		return nil
	}
	if hasOptionalHostEffectWork(advanced.Effects) {
		return dispatcher.dispatchOptionalHostEffects(ctx, adapter, call, advanced, emit)
	}
	return nil
}

func invokeDecisionMatch(ctx context.Context, adapter httpruntime.Adapter, matchAdapter DecisionMatchAdapter, definition decisionv1.Definition, request callback.MatchRequest) (callback.MatchResponse, bool, error) {
	if client := matchAdapter.DecisionMatchClient(); client != nil {
		response, err := client.Match(ctx, definition, request)
		return response, true, err
	}
	host, ok := adapter.(callback.HostCallback)
	if !ok {
		return callback.MatchResponse{}, false, nil
	}
	response, err := host.Match(ctx, request)
	if err != nil {
		return callback.MatchResponse{}, true, err
	}
	if err := response.ValidateFor(definition); err != nil {
		return callback.MatchResponse{}, true, err
	}
	return response, true, nil
}

func decisionDefinitionOf(adapter httpruntime.Adapter) (decisionv1.Definition, bool) {
	decision, ok := adapter.(httpruntime.DecisionAdapter)
	if !ok || isNilDecisionAdapter(decision) {
		return decisionv1.Definition{}, false
	}
	return decision.DecisionDefinition(), true
}

func isNilDecisionAdapter(adapter httpruntime.DecisionAdapter) bool {
	return adapter == nil
}
