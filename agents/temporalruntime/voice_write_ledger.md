# VoiceWrite ledger: durable reservation vs process-local cache

The execute-tool envelope is unchanged: platform-derived idempotency key,
workflow reserve/coalesce/unknown, LocalActivity dispatch, handler
`ToolWriteID`. This note covers only where the write result store lives.

**Before.** `processWriteLedger` was an in-memory map. Lost-response retry
worked in the same worker because the cache still held the committed result.
A replacement worker with an empty map treated a miss as a first attempt and
re-invoked the handler.

**After.** The in-memory map is only the in-flight coalesce (wait channels).
The source of truth is a durable per-identity record:

1. Exclusive reserve is persisted **before** the handler runs.
2. Success persists the recoverable result and a `metered` bit together.
3. `run()` error persists `unknown`.
4. A recovered pending/unknown/corrupt record is **fail closed** (reconcile
   only). It never becomes a second execute.
5. Workflow unknown retries set `WriteReconcileOnly` so even a durable miss
   cannot re-execute.

Empty process cache + same durable dir recovers the committed result and
retains metering (one logical write). Unknown after restart does not run the
handler. Product handlers (Candlestick) still must durably dedupe by
`ToolWriteID`; this file is the platform envelope/ledger side only.
