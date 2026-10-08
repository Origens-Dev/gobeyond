# Proposed decision graph contract

`v1` is a reviewable, versioned wire contract for the proposed
`DefineDecision(Graph)` surface. It defines graph and route types, semantic
events and results, validation, and qualification gates. It does not register
an agent, discover route files, compile a manifest, execute a reducer, or
activate a voice/media path.

The contract intentionally does not add a default `decision.yaml`. The
architecture draft prefers keeping the existing agent directory and
`agent.go` as the shared definition, discovering route-local files, and
emitting one canonical frozen manifest later. `say`, `listen`, `match`, `act`,
and `next` are route-local phases. The compiler and generated adapter remain
separate integration work.

## Review and activation

`Definition.ValidateForReview` checks the frozen shape and requires every
budget dimension to carry either a qualified finite value or a reference to an
explicit unresolved gate. It also checks route IDs and references, outcomes,
fallbacks, retry cycles, ICU arguments, release digest inputs, and inherited
authority.

`Definition.ValidateForActivation` additionally rejects every unresolved
policy or qualification gate, an unqualified budget, and anything other than
one qualified V0 locale. No example threshold from the architecture draft is
copied into a default. Review fixtures keep unknown thresholds as named
unresolved gates with no numeric value.

Route grants use stable IDs for inherited tools, services, and typed product
bindings, and the authority envelope pins the parent manifest digest. A
missing route selection inherits the parent set; an explicit
empty list grants nothing. Route budget overrides must reference the inherited
gate and have a finite value no greater than the qualified parent ceiling. An
unresolved parent ceiling cannot be overridden.

Every route has a fallback, and the graph identifies a fallback route. Cycles
require a retry group with logical-task counters, total and per-reason
no-input/no-match/ambiguity bounds, an admitted-turn counting point, event-ID
deduplication, and an exhaustion path outside the cycle. Unresolved retry
limits can be reviewed but cannot be used to activate a run.

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
IDs and reauthorize at execution. Unknown effect outcomes are reconciliation
states, not retry instructions. A graph-ending connect/handoff can release
graph ownership only with a matching confirmed call-control receipt; submit,
accept, cancel, and ambiguous outcomes do not count as confirmation.

Release digest inputs pin graph, prompts, bindings, normalization rules,
authority, policy, locale, and voice inputs. Session pins retain the graph and
input snapshot digests, locale, prompt family/variant, generation, and resolved
voice capability snapshot for deterministic replay. Digest calculation and
runtime persistence are left to a later integration.

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

## Integration seams

The source plan describes reuse of the existing parent agent's tool schemas,
service grants, channels, voice selection, approval rules, and signed budgets.
It also identifies a future stateful adapter behind the existing
`Start/Respond/Cancel` seam, product-registered typed matchers, a Jev service
adapter, prompt rendering, and receipt-backed effect execution. This package
does not edit or register those seams, change the existing voice contract, or
make provider calls. Dependent ORI-67/68 and Jev qualification work remain
outside this change.
