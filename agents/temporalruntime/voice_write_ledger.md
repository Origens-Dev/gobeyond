# VoiceWrite ledger: process cache vs host-local vs authority

The execute-tool envelope is unchanged: platform-derived idempotency key,
workflow reserve/coalesce/unknown, LocalActivity dispatch, handler
`ToolWriteID`. This note covers only where the write result store lives.

**Layers**

| Layer | Survives | Role |
|---|---|---|
| Process map | in-flight complete / waiters | Coalesce waiters (`wait` channels); complete sticky |
| Host-local files | process restart on the same disk | Replica used when no shared store is attached |
| Shared authoritative store | replacement onto a host with **empty local storage** | Reconcile SoR for host/container loss |

Load order: process cache → shared store → host-local files. Pending and
unknown process-cache rows are **not** sticky: they re-query the shared
store so another worker's completed result becomes visible. Reserve and
final persist go to the shared store first when one is attached.

**Production attach.** The process default is host-local files only
(`shared=nil`). Workers must call `RetainVoiceWriteAuthority(store)` at
startup (alongside `RetainVoiceRegistry`) with a `VoiceWriteStore`
implementation — hosted persistence or product durable receipt lookup
keyed by `ToolWriteID` (`VoiceWritePersistedRecord.Key`). Without that
attach, fresh-host reconcile cannot reach the receipt SoR and fails closed.

**Before.** A single in-memory map. Lost-response retry worked only while that
map still held the result. Reopening the same directory only proves process
restart, not host loss.

**After**

1. Exclusive reserve is persisted **before** the handler runs.
2. Success persists the recoverable result and a `metered` bit together.
   Only certain success (`Error` empty) is `complete`. Tool errors —
   including after a product mutation may already have committed — are
   marked `unknown`, never sticky completed-failure.
3. A **final persist error is fail closed** (`errWriteLedgerPersist` wrapped
   with `errWriteOutcomeUnknown`). The activity does not return durable
   success. Best-effort mark `unknown`; if that mark also fails, the exclusive
   reservation left **pending** is still enough for reconcile to refuse
   re-execute on a replacement host.
4. Recovered pending/unknown/corrupt records fail closed (reconcile only).
   Legacy rows that cached `Error` under `complete=true` are treated as
   unknown and refresh from authority.
5. Recovered certain successes are validated again against the frozen tool
   output schema and `max_result_bytes` without re-running the handler. Schema
   or size failure stays unknown/reconcilable and drops the sticky
   process-local complete row so a later authority repair is visible.
6. Expired tool approval blocks a new mutation, but an authorized unknown
   outcome may still reconcile via receipt lookup (`WriteReconcileOnly`).
7. Workflow unknown retries set `WriteReconcileOnly` so a miss cannot
   re-execute even when every store is empty.

**Host loss.** A replacement worker with a new empty ledger directory looks up
the store retained by `RetainVoiceWriteAuthority` (hosted persistence /
handler-keyed receipt SoR in production; in-process shared map or receipt
double in tests). Evidence that the mutation survived *somewhere else*: the
shared store still has the complete + metered record while the new host's
local directory is empty. Reconcile returns that result. If the outcome
cannot be established (no authority, or only pending/unknown after refresh),
return unknown and **do not** run the handler.

Product handlers still must durably dedupe by `ToolWriteID`. This file is
the platform envelope/ledger side only.
