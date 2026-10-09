# Decision effect dispatch

`DecisionSessionWorkflow` remains a deterministic reducer host. The dispatcher
executes only `EffectDispatchIntent` through the existing
`VoiceSessionExecuteToolUpdate` and only releases call ownership through the
existing host compare-and-swap. Route entry, prompt, listen, matcher, and Jev
effects remain graph orchestration; they do not consume voice tool or transfer
hops.

## Required host authority

An adapter that can produce an executable decision action must implement
`DecisionEffectAdapter`. This is an opt-in host integration seam; the package
does not register an adapter, activate decision agents, create a receipt store,
or provide product-specific recipient lookup.

The adapter must:

- Return `NotAttempted` from `ReconcileDecisionEffect` only when the existing
  operation or receipt authority has no reservation and no possible provider
  outcome. Return `Pending` for every uncertain result. Return a receipt only
  from that existing authority.
- Reauthorize the exact opaque target from the reducer request against current
  product membership and permission in `PrepareDecisionEffect`. The old
  candidate snapshot and the current target must resolve to the same opaque
  destination. A menu position, rank, or newly selected candidate is not a
  destination.
- Return the already-running voice workflow's execution ID, a voice-tool
  request within the current session scope, and its current signed tool grant.
  `VerifyDecisionEffectToolCeiling` must verify that grant against the exact
  tool and scope before the request reaches Temporal. The dispatcher also
  checks the frozen decision `ToolGrant`, current voice manifest schema and
  revision, authored write/control policy, and the request's tool ceiling.
- Verify authoritative receipts and apply ownership changes through the
  existing host owner CAS. CAS must be idempotent for the same effect and
  receipt IDs.

The voice execution ID is separate from the decision run ID: the decision
workflow owns the latter, while the existing voice workflow owns the former.
The dispatcher rejects a request that aliases those workflow IDs.

## Recovery sequence

The decision workflow records a stable submission update before the dispatcher
calls the voice workflow. The update is bound to the reducer-emitted request;
its effect ID derives from tenant, session, generation, route entry, input,
action, and frozen graph identity. The existing voice write path derives its
`ToolWriteID` from that effect ID and persists through `VoiceWriteStore` and the
product handler. Call-control continues through the existing signed-grant and
operation path.

Every retry first reconciles against the existing authority. A known receipt
goes directly to the reducer receipt update; a pending or unknown result never
repeats the voice tool update. A proven no-attempt result may retry only while
the decision session is still running and current authorization succeeds.
Cancellation before submission discards the unsubmitted reducer intent.
Cancellation after submission keeps the receipt callback alive; a late
confirmed receipt can still reach the existing owner CAS. The workflow closes
that reconciliation path only after the owner CAS acknowledgement, or after a
cancelled submitted intent is authoritatively proven never attempted.

Receipt event IDs exclude observation timestamps so duplicate deliveries of
the same semantic receipt share the same update ID. New receipt states, such as
an unknown outcome followed by confirmation, remain distinct events. Receipt
and approval events contain minimal typed outcome data; approvals omit tool
input. The reducer workflow does not receive raw prompts or transcripts.

## Integration boundary

The reducer, stable updates, voice workflow dispatch, existing write ledger,
receipt reconciliation, and owner-CAS acknowledgement are implemented here.
The product-owned adapter implementation remains required for current
recipient resolution, signed ceiling verification, authoritative receipt
lookup, and owner CAS. Those sources are not defined by the generic repository
contract, so this change fails closed until a host supplies them. Production
recipient acceptance and the existing shared `VoiceWriteStore`/product receipt
store attachment remain activation gates.
