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

Adapters remain responsible for the activation-ready definition check and
current authorization. Call-control effects must keep using the existing
server-created `voicecontract.Command` and operation identity, authoritative
terminal receipt, and host owner/scope compare-and-swap. The reducer's
`GraphOwnership` is only the graph's semantic pending/release projection; it is
not a second operation or receipt ledger. A graph receipt requests the
existing adapter CAS and cannot perform it.

Retry counters are held by logical task and survive route revisits, reducer
replay, and candidate snapshot refresh. Duplicate event IDs are idempotent;
the same ID with changed content is rejected. Retry, decision, route-visit,
and effect-attempt ceilings come from the frozen contract. Full-agent
escalation is disabled unless an explicit session limit is supplied; there is
no default threshold. Time checks use event timestamps supplied by the
adapter.

`RunTextTrace` renders semantic effects as deterministic text. It does not
invent playback completion, DTMF timing, or other transport acknowledgments.
The executable traces derive from ORI-66's frozen review fixture. Their finite
retry and session counters are test-only values; the checked-in review fixture
and its unresolved activation gates are unchanged. The package is not wired to
the compiler or runtime.

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
