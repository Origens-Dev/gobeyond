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
calls the voice workflow. A second durable dispatch-commit update is the
linearization point against cancellation. Cancellation accepted before this
commit prevents the voice update. Once the commit is accepted, a later
cancellation does not erase the authorized in-flight intent: recovery first
checks the existing receipt authority, and if it proves no attempt occurred,
re-authorizes the same opaque target and signed tool ceiling before resuming
the stable voice update. The update is bound to the reducer-emitted request;
its effect ID derives from tenant, session, generation, route entry, input,
action, and frozen graph identity. The existing voice write path derives its
`ToolWriteID` from that effect ID and persists through `VoiceWriteStore` and the
product handler. Call-control continues through the existing signed-grant and
operation path.

Every retry first reconciles against the existing authority. A known receipt
goes directly to the reducer receipt update; a pending or unknown result never
repeats the voice tool update. Before the dispatch-commit point, a proven
no-attempt intent may be retried only while the decision session is still
running and current authorization succeeds. After commit, recovery may resume
the exact intent after cancellation only when current reconciliation proves no
attempt occurred and current authorization still succeeds. Cancellation before
submission or before dispatch commit abandons an intent only after the receipt
authority proves it was never attempted. A late confirmed receipt after commit
can still reach the reducer and existing owner CAS. The workflow closes that
reconciliation path only after the owner CAS acknowledgement, or after a
cancelled pre-commit intent is authoritatively proven never attempted.

For tools requiring user approval, the dispatcher emits an approval event with
the exact effect identity, voice tool-call ID, input digest, interaction ID,
actor binding, and expiry. It omits raw tool input. A response must carry that
same effect identity, interaction ID, tool-call ID, and input digest. The
decision workflow durably commits the authenticated choice before the
dispatcher calls the existing `VoiceSessionApproveToolUpdate`. The voice
workflow checks the pending call's actor, identity, and input hash and makes
identical retries stable while rejecting changed replays. Approval recovery
reconciles first; the durable approval commit is the cancellation boundary for
that exact response. Recovery never repeats a known receipt or changes the
selected target. The exact pending-approval query includes expired approvals so the
existing voice workflow can return its definitive expiry result without
executing the tool. An explicit denial or expiry returned by the voice approval
update becomes a deterministic failure receipt only after the decision
workflow has durably committed the exact authenticated choice. The receipt
update accepts those statuses only when they match the stored approval commit.
Provider outcomes still require the product's existing receipt authority; an
unresolved approval update remains pending and fails closed.

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
