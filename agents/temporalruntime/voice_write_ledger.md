# VoiceWrite ledger: process cache vs host-local vs authority

The execute-tool envelope is unchanged: platform-derived idempotency key,
workflow reserve/coalesce/unknown, LocalActivity dispatch, handler
`ToolWriteID`. This note covers only where the write result store lives.

**Layers**

| Layer | Survives | Role |
|---|---|---|
| Process map | in-flight only | Coalesce waiters (`wait` channels) |
| Host-local files | process restart on the same disk | Replica used when no shared store is attached |
| Shared authoritative store | replacement onto a host with **empty local storage** | Reconcile SoR for host/container loss |

Load order: process cache → shared store → host-local files. Reserve and
final persist go to the shared store first when one is attached.

**Before.** A single in-memory map. Lost-response retry worked only while that
map still held the result. Reopening the same directory only proves process
restart, not host loss.

**After**

1. Exclusive reserve is persisted **before** the handler runs.
2. Success persists the recoverable result and a `metered` bit together.
3. A **final persist error is fail closed** (`errWriteLedgerPersist` wrapped
   with `errWriteOutcomeUnknown`). The handler is not treated as a durable
   success, and retry does not re-execute.
4. Recovered pending/unknown/corrupt records fail closed (reconcile only).
5. Workflow unknown retries set `WriteReconcileOnly` so a miss cannot
   re-execute even when every store is empty.

**Host loss.** A replacement worker with a new empty ledger directory looks up
the shared store (hosted persistence / handler-keyed SoR in production;
in-process shared map in tests). If that store has the committed result,
reconcile returns it and metering stays with the one logical write. If the
outcome cannot be established, return unknown and **do not** run the handler.

Product handlers (Candlestick) still must durably dedupe by `ToolWriteID`.
This file is the platform envelope/ledger side only.
