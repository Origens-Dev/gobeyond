# Exact-text playback declarations

This is an inactive source foundation. It does not synthesize or play audio,
execute completion mutations, advertise capability, or enable an application.
The current workflow rejects the new playback policy until its separately
versioned authenticated runtime is integrated. Legacy policies and histories keep
existing limits.

`ToolConfig.VoicePlayback` is a compiler-visible inline `VoicePlaybackPolicy`.
It is mutually exclusive with remote read, action, call control, and completion.
The manifest binds its input/output schemas, exact top-level output mapping, and
one hidden completion tool. `VoicePlaybackCompletion: true` declares the paired
mutation. It remains in the frozen registry but is removed from ordinary direct,
durable, and Live provider tool projections. It cannot be classified as a read.
Decoded metadata cannot acquire either typed playback or completion markers.

Use `VoiceBudgetPolicy: "operator_mailbox_playback_v1"` only after host admission
and complete-fleet capability gates are implemented. This declaration requires
`list-text-messages`, `get-text-message`, `dial-contact`, `play-text-message`, and
`complete-text-message-playback`; directory search and hangup remain optional.
Existing buckets remain list 4/get 12/placement 2/hangup 1. New independent buckets
are playback 12/completion 12. Classification alone never authorizes completion.
`operator_mailbox_v1` rejects these tools and remains unchanged.

The playback output is one closed object with six required, bounded strings:
exact text, message ID, canonical UTC RFC3339Nano creation time, raw UTF-8 SHA256
lowercase hex digest (no prefix), message expiry, and short authorization expiry.
The field names are mapped statically; JSON paths or runtime mappings are rejected.
Text is at most 4096 bytes; result at most 8192 bytes. The implementation rejects
oversize text rather than trimming it. Creation time, including nanoseconds,
remains part of immutable message identity. Authorization expires within five
minutes and no later than the message. Schema/digest/identity/expiry failures must
not play audio or invoke the completion handler.

`ResolvePlaybackSource` validates only a producer result; it does not authenticate
an actor, producer, caller, mailbox, session, owner, or transport. The host must
first verify the signed grant, frozen digest, enabled tools, current owner and
message permission. It must bind the exact result to a durable clip identity,
owner/media/pacer generations, audio digest and byte count before enqueue.

The hidden completion handler must receive a framework-built authenticated
receipt after the matching clip fully drains, never model-supplied arguments or
an ordinary tool result. Cancellation, interruption, generation change, expiry,
missing final marker, partial synthesis and stale/replayed identities cannot
create such a receipt. Retries must reuse the immutable clip identity and mutation
operation. Completion means transport delivery, not proof that a human heard the
message. Marking and metering remain gated on the separate integration.

`agents.PlaybackCompletionFromContext(ctx)` returns a text-free
`voicecontract.PlaybackCompletionReceipt` only when the framework has installed
its private marker and the receipt remains fresh. There is no exported setter.
A decoded receipt or actor metadata cannot supply that authority. No runtime
currently populates the marker; application completion handlers must return an
unavailable error when the accessor returns false. After runtime integration,
they must still match the exact authenticated calling-line context and perform
an immutable-message CAS over ID, creation timestamp, text digest and expiry.
The accessor is not a substitute for that catalog authorization or mutation fence.

Clip IDs are `clip_` followed by lowercase SHA256 of canonical context JSON,
a NUL byte, then the tool-call ID. Public/private contexts use canonical JSON so
Go field declaration order cannot alter identity. Completion timestamps use UTC
`time.Time`, retaining RFC3339Nano precision on the wire. Validation alone never
authenticates a receipt and must not be used to install a trusted marker.
