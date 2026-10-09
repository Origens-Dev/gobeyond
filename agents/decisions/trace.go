package decisions

import (
	"fmt"
	"strings"

	contract "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

// RunTextTrace runs a supplied semantic event sequence through the pure
// reducer and renders only deterministic effect names and opaque test IDs.
// It performs no provider, clock, audio, DTMF, or transport work.
func RunTextTrace(definition contract.Definition, admission contract.NormalizedEvent, policy Policy, events ...Event) (State, string, error) {
	state, effects, err := Start(definition, admission, policy)
	if err != nil {
		return State{}, "", err
	}
	var trace strings.Builder
	writeEffects(&trace, effects)
	for index, event := range events {
		state, effects, err = Reduce(state, event)
		if err != nil {
			return state, trace.String(), fmt.Errorf("trace event %d: %w", index+1, err)
		}
		writeEffects(&trace, effects)
	}
	return state, trace.String(), nil
}

// FormatTextTrace renders semantic effects as stable single-line text.
func FormatTextTrace(effects []Effect) string {
	var trace strings.Builder
	writeEffects(&trace, effects)
	return trace.String()
}

func writeEffects(trace *strings.Builder, effects []Effect) {
	for _, effect := range effects {
		trace.WriteString(effect.String())
		trace.WriteByte('\n')
	}
}
