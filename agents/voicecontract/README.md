# Voice contract v1 (foundation only)

`testdata/*.json` are normative cross-repository golden examples. Copy byte-for-byte
into consumers; protocol version changes require coordinated fixture changes.
This package is not an active transport, signer, verifier, or runtime integration.

Manifest registry key is organization/project/environment/agent/compiled_revision/
manifest digest. `revision` identifies the manifest definition, `compiled_revision`
identifies its deployed build. Digest is `sha256:` plus lowercase SHA256 over canonical
JSON, with no self-digest field. Schema digests cover only canonical input_schema.
Canonical JSON has sorted keys, compact Go encoding/json escaping, integer-only
numbers, valid UTF-8, no duplicate keys, at most 8 nested levels, 1024 total object
keys, and 1024 array entries. Manifests allow 8 tools, 32768 bytes total, 4096 bytes
per schema, 512 description bytes, 128 identifier bytes. Schemas are closed objects,
bounded strings, and standalone anyOf with 2–4 variants. No refs, recursive schemas,
unknown keywords, provider metadata, or client schemas are accepted.

The assistant envelope is an **extension alongside existing session config**;
it does not replace existing model/audio/session settings. Ordered common_context
then call_context are untrusted data, not instructions. Combined serialized envelope
must fit 16384 bytes; common context budget is 12288 bytes, call context 1024 bytes.
Screening context is authored by the authenticated policy resolver. Only platform
internal endpoint/active network identities bypass screening; ANI never does.

Contract version `1` is independent of signing grant version. Proposed platform
asymmetric grants use grant_version `3`, kid, expiry and nonce. Existing HMAC v1/v2
are legacy, retained solely for start/cancel and MUST NOT execute these tools.
Grant examples describe verified claims, never the bearer token format. Signature,
issuer/audience, KMS key rotation, authenticated environment registration, expiry,
nonce/replay, registry resolution and active-owner validation are consumer duties.
The model sees only bounded opaque destination/recipient IDs and bounded optional
quoted data. Context and operation/idempotency IDs are server-derived.

Scope generation is a call-owner/scope CAS fence, not operation sequence or media
source generation. A screener selection advances generation exactly once using the
old full context. Every command/event compares the entire active context. The
idempotency key is session/call/tool/tool_call_id/input_digest: changed input for
an existing first four fields is rejected, never a second operation.

Accepted acknowledges an asynchronous command. Ringing means authoritative first
180/183; direct 200 produces answered without requiring ringing. Either is model
terminal only after announcement audio drains. Answered does not mean bridge commit;
call/media lifecycle and transfer records independently commit bridge. Pre-ring
failed events can return a concise retry; post-ring failure plays one deterministic
announcement and hangs up without restarting Live. Consumers enforce sequence gaps,
duplicates, generation and state transitions; decoding alone does not do this.

Browser events come only from authenticated server state and expose public identity,
never purpose, voicemail, transcripts, policy or internal party identity. Screening
purpose is disclosed only after explicit acceptance via validated UPDATE to the
winning managed dialog. No fixture grants SIP/header serialization authority.

Registry remote reads use `execution_kind: "read"`, a closed output object schema,
`output_schema_digest`, and `max_result_bytes` (1–4096). They have no destinations
and cannot be terminal. Arrays are bounded to at most five items. Current
compilation emits `call_control` for operation tools; omitted execution kind in
existing version-1 fixtures retains the same call-control meaning and digest.
`remote-read.json` binds the full verified Context and input digest but contains
no operation ID or announcement barrier. Read requests are limited to 1 KiB and
two distinct read tool calls per session; ordinary results must match the compiled
output schema. Only typed authored opt-in can enter this manifest.
