# Pure decision reducer

`agents/decisions` applies one normalized semantic event to an immutable
session state and returns the next state plus semantic effects. It consumes the
frozen `decision.graph/v1` definition from ORI-66. It does not register an
agent, call Jev or another provider, execute tools, move media, read a clock, or
activate a voice path.

The reducer resolves routes and actions only from its private copy of the
validated frozen definition. Events must match the active generation, route,
route-entry, input window, and pending input. Candidate snapshots add the
active binding and route-entry identity to the v1 candidate set and are
rejected unless both match the frozen active route. A caller-supplied route ID
or `ActStep` cannot select an action. `ValidateDispatchIntent` checks that an
intent still matches the reducer's active frozen state; it is not an execution
grant.

Each emitted `say` effect includes a deterministic playback operation ID.
Playback receipts must match the active say. If final input was accepted while
that prompt was playing, its later completion/clear/failure cannot reopen the
consumed window or replace the pending input. Candidate snapshots stay pinned
while matcher, decision, fallback, or effect work is pending; one initial
refresh may satisfy a matcher that has no snapshot yet, but subsequent
refreshes are rejected until the operation resolves.

Adapters remain responsible for the activation-ready definition check and
current authorization. Call-control effects must keep using the existing
server-created `voicecontract.Command` and operation identity, authoritative
terminal receipt, and host owner/scope compare-and-swap. The reducer's
`GraphOwnership` is only the graph's semantic pending/release projection; it is
not a second operation or receipt ledger. A graph receipt requests the
existing adapter CAS and cannot perform it.

Retry counters are held by logical task and survive route revisits, reducer
replay, and candidate snapshot refresh. The reducer enforces session-wide
`reprompts`, `no_input_reprompts`, `no_match_reprompts`, and
`ambiguous_reprompts` ceilings across retry groups, as well as each group's
own counters. It also applies route overrides as per-route ceilings for route
visits, decision calls, effect attempts, and retry admissions; an override
cannot widen its inherited session ceiling. Duplicate event IDs are
idempotent; the same ID with changed content is rejected. Full-agent
escalation is disabled unless an explicit session limit is supplied; there is
no default threshold. Time checks use event timestamps supplied by the
adapter.

This reducer does not account for session/input duration, TTS characters,
queued audio, artifact bytes, spend, latency, or protected-payload TTL. Those
measurements need their runtime, transport, or accounting owners before
integration. Individual protected-reference expiry is still validated by the
v1 contract; only the aggregate TTL budget is outside this reducer. The
repository had no retry-counter owner outside this reducer; the future
stateful adapter must persist its session and route counters.

`RunTextTrace` renders semantic effects as deterministic text. It does not
invent playback completion, DTMF timing, or other transport acknowledgments.
The executable traces derive from ORI-66's frozen review fixture. Their finite
retry and session counters are test-only values; the checked-in review fixture
and its unresolved activation gates are unchanged. The package is not wired to
the compiler or runtime. A playback operation ID is emitted for the future
adapter to carry into an authoritative receipt; the text harness only consumes
the explicit playback receipt supplied by a trace.

## Follow-up adapter contract gate

The ORI-66 wire snapshot carries tenant, session, and generation scope but
doesn't itself include matcher binding or route-entry identity. Its structural
dispatch validators also accept a caller-provided route ID or action value.
This reducer closes those gaps locally through private frozen state and a
bound snapshot envelope. Before adapter integration, the shared contract
should expose equivalent binding/route-entry fields and dispatch validation
that resolves the action from trusted frozen session state. Until then,
adapters must keep the active session pin and action state server-owned and
must not treat a reducer effect or decoded receipt as authority.
