# GoBeyond workflows example

Opt-in Temporal dogfood site. The light SEO fixture stays Temporal-free; this
example declares workflows and activities under `workflows/`, starts workflows
from Go `postAction` handlers, and observes runs in the Temporal Web UI.

## Run

From the gobeyond repo root:

```bash
# 1. Temporal (gRPC :7233, UI :8233)
docker compose -f examples/durables-site/docker-compose.temporal.yml up -d

# 2. Build this website (default gobeyond build still targets seo-site)
export GOBEYOND_WEBSITE=examples/durables-site
go run ./cmd/gobeyond build

# 3. Site + worker process (queue default__local)
./dist/server/gobeyond-server &
./dist/workers/default/gobeyond-worker
```

Open http://localhost:8080/durables, click either button, then check history at
http://localhost:8233.

Browser code only calls `postAction`. Workflow start uses the Go Temporal SDK
on the server. Authored definitions use `workflows/<id>/workflow.go` and
`activity.go`; generated runtime binaries remain under `dist/workers/`. For
Node/server triggers, see `@origens-dev/temporal`
(server-only; not for the browser).

The same fixture includes both a private direct `agents/echo/agent.go` and a
private durable `agents/durable-echo/agent.go`. Under `gobeyond dev`, loopback
requests receive the typed development actor:

```bash
curl -sS http://localhost:3000/api/agents/echo/sessions \
  -H 'content-type: application/json' \
  -d '{"input":{"message":"hello"}}'
```

With Temporal and the generated `default__local` worker running, change
`echo` to `durable-echo`; the HTTP/session contract stays the same while the
handler executes as a Temporal workflow activity.

The fixture also includes real Go AI SDK definitions at `agents/assistant/`
(direct token streaming) and `agents/durable-assistant/` (model/tool steps
checkpointed by the Temporal AI SDK). Both read `OPENROUTER_API_KEY` through
the provider; authored definitions contain no credentials:

```bash
curl -sS http://localhost:3000/api/agents/assistant/sessions \
  -H 'content-type: application/json' \
  -d '{"input":{"message":"What makes direct mode different?"}}'
```

Change the ID to `durable-assistant` to use `default__local` and the stable
Temporal AI agent workflow. Each AI folder's `instructions.md` is embedded and
fenced by the finalized build identity.

`durable-scripted` is the credential-free integration fixture. Its injected
mock model calls an actor-aware typed tool and then produces a final answer, so
the complete HTTP → Temporal model → tool → model → SSE path can be smoke
tested without contacting a provider.

The response contains a session, run, and resumable SSE `eventsUrl`. Hosted
requests do not receive the loopback actor; configure an actor resolver or set
`Public: true` explicitly for public agents.
