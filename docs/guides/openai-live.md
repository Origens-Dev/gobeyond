# OpenAI Live voice adapter

The `openai` voice provider uses GPT-Live-1 with managed Responses delegation.
Set `OPENAI_API_KEY` in the server runtime. The default speech model is
`gpt-live-1`, voice is `marin`, and delegated reasoning model is `gpt-5.6-luna`.
`voice.StartConfig.VoiceBackendModel` overrides the reasoning model;
`GOBEYOND_OPENAI_LIVE_BACKEND_MODEL` is the runtime fallback. Gemini remains the
default provider when applications do not select OpenAI.

The adapter accepts matching mono input/output formats: PCMU or PCMA at 8 kHz,
or PCM16LE at 16 or 24 kHz. `SelectOpenAILiveFormat` exposes the same negotiation
to hosted runtimes so their declared media format matches the provider session.
Session startup checks the provider's resolved format, model, and voice.

Only enabled application tools are included. An authorized web-search tool
becomes the native Responses `web_search` tool. Other functions execute through
the existing authenticated application executor, with input/output validation.
Function calls are collected from completed output items, associated with their
delegation and response, deduplicated, and executed after response completion.
All function results are submitted before the adapter continues the response.

Live duration is cumulative, separate from delegated backend token usage.
`voice.Usage` includes provider session/response IDs, finality, search-call count,
and duration. Consumers must compute positive duration deltas rather than sum
cumulative snapshots. Closing a session retains a bounded window for final
usage; abrupt provider or network loss can prevent final accounting.

## Call controls

Conversation and `hang_up` are supported. Dial/transfer additionally require a
trusted `CallControlConfig.Announcement` callback returning finite audio in the
negotiated output format (at most five seconds). The adapter suppresses provider
audio, clears unfinished speech, plays the application clip, and requires the
existing `OnPlayoutBarrier` before executing the verified control. The callback
must not execute the action itself. Do not treat discarded or timed-out media as
successful playout. A failed announcement or drain returns a tool failure and
allows the conversation to continue; a trusted terminal handoff closes Live and
suppresses remaining controls and output. `hang_up` uses its existing bounded
400 ms drain gate without requiring a separate announcement.

This is application-controlled playback, not a GPT-Live speech-completion event.
Backend response completion is not speech completion. Applications must provide
a suitable announcement and verify the actual media/control path before enabling
transfer. Provider voice previews must be real samples or explicitly unavailable;
browser speech synthesis is not a provider preview.

Protocol references: [Live API](https://developers.openai.com/api/docs/guides/live),
[Responses delegation](https://developers.openai.com/api/docs/guides/live-delegation).
