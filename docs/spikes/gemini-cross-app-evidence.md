# Gemini cross-app voice evidence

Gemini remains the configured provider. The supported `google.golang.org/genai`
v1.63.0 Live client cannot currently satisfy `voice.ProviderEvent`: `Receive`
returns a converted typed message rather than the received frame bytes, and
`UsageMetadata` scalar token counts cannot distinguish absence from measured
zero. Hashing a JSON reserialization would not be a received-frame digest.
`Session.SetupComplete.SessionID` does expose a provider session identifier, but
that alone does not repair those evidence gaps. A `GoAway` notice, local close,
or successful turn is not final usage coverage.

Consequently the Gemini adapter rejects a nonnil `OnProviderEvent` before
connecting. Ordinary Gemini sessions with legacy `OnUsage` remain supported.
Legacy usage is an observed normalized metric, not a qualified billing receipt.
The adapter closes the session before joining its receive pump on cancellation
or an uplink failure. This local join does not attest remote termination or
complete provider usage. No cross-app calling capability is enabled by this work.

## Bounded pilot design requiring separate implementation and review

A supported-client pilot need not pretend typed observations are raw evidence.
A separate versioned observation type could explicitly carry:

- provider session ID from `SetupComplete`, model, backend and local receipt order;
- `provenance=genai_typed`, `metric_presence=unknown`, and
  `digest_kind=normalized_observation` (never `received_frame`);
- the exact observed typed token counters without imputing absent counters,
  plus an explicit unqualified/reconciliation-required status;
- immutable provider-project funding identity and a durable local outbox.

This is a new, weaker observation class, not an implementation of the existing
qualified `ProviderEvent` contract. Qualified admission/billing gates must remain
closed unless a separately reviewed pilot policy explicitly accepts these
limitations. Such a policy would need to define provisional provider-funded
metering, ambiguous cumulative-versus-per-turn semantics, idempotency across
retries, and reconciliation against an authoritative provider billing source.
Never charge Candlestick target-AI allowance for missing or uncertain usage.

Local cancellation could be tracked separately with the supported `Session.Close`
and joined pump acknowledgement. Remote provider termination and billing finality
would remain unknown; cancellation cleanup and unresolved reconciliation must
remain durable rather than claiming completion from local socket closure.

This route avoids a custom WebSocket transport or a provider switch. It still
requires an explicit implementation/review of the observation contract and pilot
policy, provider billing reconciliation, target store installation, host/media
wiring and an authenticated one-home end-to-end test. It is not smoke-ready.
