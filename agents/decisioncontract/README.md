# Proposed decision graph contract

`v1` is a reviewable, versioned wire contract for the proposed
`DefineDecision(Graph)` surface. It defines graph and route types, semantic
events and results, canonical release digests, validation, and qualification
gates. It does not register an agent, discover route files, compile a manifest,
execute a reducer, or activate a voice/media path.

The contract intentionally does not add a default `decision.yaml`. The
architecture draft prefers keeping the existing agent directory and
`agent.go` as the shared definition, discovering route-local files, and
emitting one canonical frozen manifest later. `say`, `listen`, `match`, `act`,
and `next` are route-local phases. The compiler and generated adapter remain
separate integration work.

## Canonical release and session pins

`CanonicalizationVersion` is `decision.canonical-json/v1`.
`CanonicalReleaseDigests` hashes fixed-order JSON projections of the frozen
definition with SHA-256. The projections cover graph shape and route content,
message families, inherited matcher bindings, the normalization-rules digest,
inherited tools/services/budgets, policy gates, and locale/required voice
capabilities. Component projections exclude their stored hashes. The release
digest covers the canonicalization version, schema version, and all component
hashes, so it has no self-referential field.

Set-like collections are sorted before hashing: routes and retry groups by ID,
outcome maps by source/outcome, message families and arguments by ID/name,
grants and gates by ID, selected grant lists, locales, and capability lists.
Maps use Go `encoding/json`'s sorted string keys. `act` order and audio-format
preference order are preserved. Nil route grant selections remain distinct
from explicit empty selections. `Definition.ValidateForReview` recomputes every
component and release digest and rejects mismatched frozen content.

`SessionPin.ValidateFor` requires every definition digest in the pin to equal
the validated frozen definition and checks `voiceSha256` against the embedded
resolved capability snapshot. The qualified voice gate binds both profile
reference and revision; the locale gate must equal the pinned locale. The
session `snapshotSha256` is separate because it identifies scoped runtime
candidate/relationship data, not another copy of the graph release.

## Review and activation

`Definition.ValidateForReview` checks the frozen shape and requires every
budget dimension to carry either a qualified finite value or a reference to an
explicit unresolved gate. It also checks route IDs and references, outcomes,
fallbacks, retry cycles, ICU arguments, canonical digest inputs, and inherited
authority.

`Definition.ValidateForActivation` additionally rejects every unresolved
policy or qualification gate, an unqualified budget, and anything other than
one qualified V0 locale. No example threshold from the architecture draft is
copied into a default. Review fixtures keep unknown thresholds as named
unresolved gates with no numeric value.

Route grants use stable IDs for inherited tools, services, and typed product
bindings, and the authority envelope pins the parent manifest digest. A
missing route selection inherits the parent set; an explicit empty list grants
nothing. Route budget overrides must reference the inherited gate and have a
finite value no greater than the qualified parent ceiling. An unresolved
parent ceiling cannot be overridden.

Every route has a fallback, and the graph identifies a fallback route. Cycles
require a retry group with logical-task counters, total and per-reason
no-input/no-match/ambiguity bounds, an admitted-turn counting point, event-ID
deduplication, and an exhaustion path outside the cycle. Unresolved retry
limits can be reviewed but cannot be used to activate a run. In each strongly
connected component, the validator removes routes whose bounded group
exhausts outside that component and checks that the remaining graph is
acyclic. One guarded route cannot bless another cycle path that bypasses its
counter.

## Event, result, and effect rules

Normalized event identity is server-derived and carries tenant, session,
generation, route-entry, input-window, channel, modality, locale, source event
IDs, and receive time. Final speech/text content uses a scoped, expiring
protected reference; durable events do not carry transcript or audio bytes.
Raw provider score fields, ASR confidence, option probability, and usage stay
separately labeled. Missing cost remains missing rather than being estimated
from an unrelated usage schema.

Deterministic match and Jev candidate results carry the candidate-snapshot
digest. `ValidateAgainst` checks that the digest and nominated opaque ID match
the scoped protected authorized snapshot; the model cannot introduce a new
route, tool, phone number, or destination.

Effect IDs are stable SHA-256 keys over tenant, session, generation,
route-entry, input, action, and graph digest. Effects reference inherited tool
IDs and reauthorize at execution. `ValidateForDispatch` resolves the action
from the activation-ready frozen route and checks its target binding, the
candidate-set digest, target membership, and protected snapshot scope before
dispatch. Structural `Validate` alone is not dispatch authority. Unknown
effect outcomes are reconciliation states, not retry instructions.

`end_graph` means only that this local decision graph has terminated; the
parent call owner remains in place. `release_call_ownership` is a separate
terminal action. It is valid only on `effect/confirmed` for a route with one
declared call-owner-releasing connect/handoff action. The pending state pins the
effect ID, binding, candidate-set digest, and intended opaque target; the
confirmed receipt must match the pending effect and exact target. Route
fallbacks, retry exhaustion, accepted/failed/unknown outcomes, and control
events cannot release call ownership.

The static synthetic trace is a contract example for expected event routing
and receipt semantics. It is not an executed reducer proof.

## Localization and voice

Message families have stable identity, typed named arguments, locale variants,
and a whole-message fallback to their declared base locale. The v1 validator
accepts named arguments and nested ICU plural branches with a required
`other`; unsupported formatter styles fail closed. Route prompt references
bind every declared argument to a runtime source. Runtime locale
canonicalization and prompt rendering must reuse the shared i18n contract.

The graph names required voice capabilities, including exact-text synthesis,
final speech text, DTMF/text input, barge-in, streaming, and cancellation. A
resolved session profile records an opaque profile/revision/voice reference,
supported locales and formats, capability states, and qualification evidence.
Unknown or unsupported capabilities do not satisfy an activation requirement.
No provider or credential is selected here.

## Adapter mapping and single authority

The enclosing agent remains the authority and registry boundary. Authors
continue to define `var Agent = agents.Define(...)` or `DefineAI(...)` in
`agents/<id>/agent.go`; the compiler-owned `.gobeyond/agents.json` projection
contains the agent ID/revision, `Slots.Channels`, and tool IDs/task queues.
Voice-capable definitions derive their existing `voiceManifest` and
`voiceManifestDigest` from those same tool declarations and the declared voice
channel. The graph adapter resolves `AuthorityEnvelope` IDs against that
frozen parent projection and reuses its current grant, schemas, channels,
approval, and budget policy. It must not register a second agent, channel,
tool, or grant ledger. The precise parent-manifest digest projection is part of
the unresolved session-adapter gate; the adapter must hash the exact frozen
agent/tool/channel inputs it consumes.

The future `Start/Respond/Cancel` adapter maps the existing session command
and result surface into normalized decision events while retaining the
platform-created owner, generation, and channel context. A normalized event
or effect ID is correlation data, not authorization. For call control, the
adapter translates an effect to the existing `voicecontract.Command`, whose
operation ID and idempotency tuple are allocated by the authenticated server,
then reconciles the existing `voicecontract.Operation`,
`voicecontract.TerminalResult`, and authoritative transport/softphone receipt.
Owner/scope compare-and-swap stays in the existing host authority, including
the applicable `ScopeTransition` fence; the graph does not create its own
operation ledger.

Voice playback continues to use the existing frozen voice tool manifest,
host-projected `voicecontract.PlaybackCompletionReceipt`, and immutable-message
CAS. A decoded graph receipt cannot mint voice authority; completion remains
tied to the framework receipt and the existing tool/owner/message fence.
`GraphOwnership` only pins the graph's pending effect and intended target while
waiting on that authority.
Durable operation state and receipt lookup remain owned by the existing voice
adapter/host. The source of truth for generic decision-session ownership and
its precise CAS seam remains an explicit integration gate.

## Open qualification gates

The following decisions are intentionally unresolved in the fixture and must
be supplied with owner evidence before downstream implementation is
activation-ready:

- Per-session duration, route-visit, Jev-call, retry, input-duration, TTS,
  queued-audio, artifact, effect-attempt, spend, first-audio, response-latency,
  and protected-payload TTL limits.
- Acceptable wrong-recipient and clarification rates.
- Which locale and exact speech profile qualify for V0, plus a compatible
  voice-capability fallback.
- Jev's TypeSafe direct versus Gateway path and the supported result revision
  and usage fields.
- Route-file authoring/discovery, graph registration, the stateful
  `Start/Respond/Cancel` adapter mapping, and ICU loader/rendering parity.
- Caller verification and authority for relationship data.
- DTMF negotiation for the first voice cohort.
- Prompt retention and revocation policy, plus bounded prompt-preparation and
  cache behavior.

No policy owner or numeric value is inferred by this package. Until the
relevant gate is qualified, activation fails closed.
