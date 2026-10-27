// Package callback is the typed host seam for decision say/listen/act and the
// product-owned match callback.
//
// This package is schema only. It does not start sessions, call the reducer,
// play audio, or change default runtime behavior. Session execution stays on
// httpruntime.DecisionAdapter and DecisionSessionWorkflow; the pure reducer
// stays in package decisions. Registering nothing leaves the path inert.
//
// # Prompt identity
//
// Say names a manifest prompt ID (for example "greeting") plus a turn ID. The
// ID is declared on the decision graph as a compiled prompt (id, locale, text)
// and frozen into the agent manifest. The host maps that ID onto a preloaded
// cached artifact. The SDK never sees cache keys, voice ids, or model ids.
// If the ID is not in the preloaded set, say fails with prompt_unavailable and
// the session fails closed. There is no free-text fallback.
//
// # Turn identity
//
// TurnID is the host name for the reducer's RouteEntryID. Every say, listen,
// act, playback receipt, and listen event carries it. DecisionSessionWorkflow
// and the reducer already drop events whose route-entry identity does not
// match the active turn; hosts must do the same for receipts and listen
// events. A stale turn is ignored, never applied.
//
// # Say and listen
//
// Say is {promptId, turnId}. The host returns {turnId, status: played|cancelled}.
// The listen no-input timer starts when that receipt is observed for the
// same turn.
//
// Listen is {turnId, noInputTimeoutMillis, maxUtteranceDurationMillis}.
// Events back are final_transcript{turnId, text}, no_input{turnId},
// cancelled{turnId}, and dtmf_unsupported{turnId}. Partial transcripts never
// cross this seam.
//
// Act carries the same turn ID, an opaque target ID, and an action name.
//
// # Match
//
// Product-owned matching is one signed callback. The request is session id,
// final utterance, and attempt count. The response is exactly one of
// matched{targetId}, clarify{promptId}, or no_match. The SDK and reducer see
// only that tagged result and opaque target IDs. Clarify names a manifest
// prompt ID, not a cache key. Targets returned as matched are already
// authorized to act; there is no separate authorize op.
//
// # Authentication
//
// Platform-to-app match calls reuse agents/connections delivery signatures:
// SignDelivery / VerifyDelivery and the X-GoBeyond-Event-* headers. Receivers
// still deduplicate Event-ID after verification. This package does not add a
// second HMAC, timestamp, or nonce scheme.
package callback
