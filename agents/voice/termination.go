package voice

import "context"

// TerminationWaiter is an optional capability on an already started session.
// Waiting does not request shutdown, and cancelling a wait does not cancel Run.
// Close alone does not acknowledge consumer return: Run must be called and exit.
// This capability describes local termination and received facts, never complete
// provider usage coverage, billing finality, or downstream media drain.
type TerminationWaiter interface {
	WaitTermination(context.Context) (SessionTermination, error)
}

// SessionTermination is a point-in-time, immutable-by-copy local termination
// result. A result is published only after Run's reader and workers return,
// synchronous evidence callbacks return, and the local socket close is attempted.
// Error strings describe failures; their absence proves no provider completeness.
type SessionTermination struct {
	Provider, Model, ProviderSessionID string
	ConsumerReturned, WorkersExited    bool
	RunError, ReadError                string
	CloseRequested                     bool
	CloseWriteError                    string
	LocalCloseAttempted                bool
	LocalCloseError                    string
	// ProviderClosed means a session.closed frame with matching identity was
	// received. The digest identifies that frame, not a usage-completeness proof.
	ProviderClosed                         bool
	ProviderClosedPayloadDigest            string
	CallbackConfigured                     bool
	CallbackAttempts, CallbackAcknowledged uint64
	CallbackError                          string
	// OutstandingResponseIDs lists observed response.created IDs for which no
	// matching terminal event was received. Empty does not prove that all
	// provider work or usage was observed. Terminal events can omit usage.
	OutstandingResponseIDs []string
}

// Clone separates caller-owned slices from a stored termination result.
func (t SessionTermination) Clone() SessionTermination {
	t.OutstandingResponseIDs = append([]string(nil), t.OutstandingResponseIDs...)
	return t
}
