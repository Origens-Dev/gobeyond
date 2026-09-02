# G4a spike: Gemini Live client path

Status: confirmed 2026-09-02 on go-ai `v0.1.0-alpha.2` and
`google.golang.org/genai` `v1.63.0`.

## Finding

go-ai has **no Live client package**. Its public surface is limited to:

- `packages/ai`
- `packages/anthropic`, `bedrock`, `google`, `vertex`
- `packages/community/openrouter`

There is no `packages/live` (or equivalent) WebSocket / realtime audio client.

`google.golang.org/genai` **does** expose Live:

- `client.Live.Connect(ctx, model, *LiveConnectConfig) (*Session, error)`
- `Session.SendRealtimeInput` / `SendClientContent` / `SendToolResponse` / `Receive` / `Close`
- Examples under `examples/live/`

## Unblock path for G4

**v1 (ship now):** implement the GoBeyond voice adapter
(`agents/temporalruntime`) against `google.golang.org/genai` Live directly.

**Follow-up:** request a thin go-ai Live wrapper so authored apps and
framework adapters share one SDK surface (model id helpers, tool schema
bridging, test fakes). Do not block Call AI Assistants on that wrapper.

Dependency pin: gobeyond requires `google.golang.org/genai` directly (see
`agents/voice` compile stub). Prefer sticking near `v1.63.0` until Live
APIs stabilize; bump deliberately when adopting newer Live features.

## Target models

| Role | Model id (candidate) | Notes |
| --- | --- | --- |
| Primary Live | `gemini-3.1-flash-live-preview` | Must be verified in Vertex Model Garden for the chosen region |
| Fallback Live | Vertex: `gemini-live-2.5-flash-native-audio`; Google API: `gemini-2.5-flash-native-audio-preview-12-2025` | Vertex id is rejected on Gemini Developer API `bidiGenerateContent`; Google id verified 2026-09-02 |

These ids are **not** guaranteed. Before enabling prod traffic:

1. Confirm the exact publisher model resource in Vertex Model Garden for the
   worker project/region.
2. Smoke `Live.Connect` from a hosted worker identity (not only local ADC).
3. Record the verified ids in agent `AIConfig.LiveModel` (and keep fallback
   selection in the adapter or config, not hard-coded forever).

`ToolModel` remains a normal text/tool-loop catalog id (G1); it is not a Live
WebSocket model.

## Inference → genai client backends

Reuse `AIConfig.Inference` (no `LiveInference` field).

| `Inference` | genai `ClientConfig.Backend` | Auth |
| --- | --- | --- |
| `vertex` (dogfood / hosted default for Live) | `BackendVertexAI` | Application Default Credentials; `GOOGLE_CLOUD_PROJECT`; `GOOGLE_CLOUD_LOCATION` or `GOOGLE_CLOUD_REGION`. Optional: `GOOGLE_GENAI_USE_VERTEXAI=true` when relying on env defaults. |
| `google` | `BackendGeminiAPI` | API key via `GOOGLE_API_KEY` / `GEMINI_API_KEY` (genai) or `GOOGLE_GENERATIVE_AI_API_KEY` (go-ai google text path — align keys in worker env). |

Live also requires an API version on the client HTTP options (genai Live
rejects empty `APIVersion`): use `v1beta1` for Vertex and `v1alpha` for the
Gemini Developer API unless genai docs change.

Empty / unsupported Inference for Live should fail closed at adapter start
(do not silently fall through to OpenRouter).

## Auth and hosted worker IAM

- **Local:** `gcloud auth application-default login` + project/location env for
  Vertex; or a Gemini API key for `Inference: "google"`.
- **Hosted agent worker:** task role / workload identity must grant Vertex AI
  user (or equivalent) on the Live model in the target region. Prefer ADC on
  the worker; do not put long-lived user keys in manifests.
- Host-report UDS / `GOBEYOND_HOSTED_RUNTIME` is orthogonal: Live dials Google
  from the agent worker process, not through gbhost JSON execute (G5 owns the
  PCM bridge into that worker).

## Regions and allowlisting

- Live model availability is region-scoped on Vertex. Pin
  `GOOGLE_CLOUD_LOCATION` to a region where the chosen Live model is listed.
- Org policy / VPC-SC / model allowlists may block preview Live models even
  when text Gemini works — treat allowlisting as a G4a/F1 checklist item.
- Cross-region failover is out of scope for v1; fail with a clear connect
  error and use the documented fallback model id only when configured.

## PCM framing (aligned with G3/G4/G5)

Gemini Live audio contract (public Live docs):

- **Input:** raw 16-bit little-endian PCM mono @ **16 kHz**
- **Output:** raw 16-bit little-endian PCM mono @ **24 kHz**

GoBeyond transport framing between voice-worker / gbhost and the adapter
(see `agents/voice`):

- Payload: 16-bit LE mono PCM samples (no WAV header)
- Frame: **uint32 little-endian length** + payload
- Max frame payload: `voice.MaxFrameBytes` (64 KiB)
- Default sample rates on `StartConfig`: in `16000`, out `24000`

G5 PCM stream endpoints must use the same length-prefixed framing.

## Adapter sketch (G4)

1. Resolve instructions / voice via G2 overlay helpers before connect.
2. `genai.NewClient` from Inference mapping; `Live.Connect` with
   `LiveModel`, audio modality, prebuilt `VoiceName`, system instruction,
   and tool declarations derived from `DefineTool`.
3. Forward `pcmIn` → `SendRealtimeInput` (`audio/pcm`).
4. `Receive` loop: audio parts → `pcmOut`; `LiveServerToolCall` → invoke
   `DefineTool` with `gobeyondActor` context → `SendToolResponse`.
5. `SessionHandle.Run` cancels on context cancel / `Close`.

## Open items before calling G4 “done”

- [ ] Verify primary + fallback Live model ids on Vertex in the dogfood region
- [ ] Confirm hosted worker IAM can `Live.Connect` (not only text generate)
- [ ] Tool schema bridge: AI SDK JSON Schema → genai `Tool` / `FunctionDeclaration`
- [ ] Decision: local-activity Temporal boundary vs in-process tool Execute for
      realtime voice (plan prefers direct Execute for pure-Go tools in v1)
