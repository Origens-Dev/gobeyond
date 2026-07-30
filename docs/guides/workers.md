# Workers and durables

Authored durable tasks live under `workers/`, sibling to `app/`:

```text
workers/durables.go            # default worker id
workers/<id>/durables.go       # named worker
```

Authors export `Register(worker.Worker)` from each durables package.
Process mains are generated under `generated/cmd/workers/<id>/`.

## Local Temporal (Docker)

Use the opt-in durables example (not the SEO fixture):

```bash
docker compose -f examples/durables-site/docker-compose.temporal.yml up -d
export GOBEYOND_WEBSITE=examples/durables-site
go run ./cmd/gobeyond build
./dist/workers/default/gobeyond-worker
./dist/server/gobeyond-server
```

Open `/durables` to start workflows from Go actions, then observe runs in the
Temporal Web UI at http://localhost:8233.

Task queues are environment-suffixed: `{workerId}__{environment}`.
Local default environment is `local` → e.g. `default__local`.

## Build

`gobeyond build` projects `workers/**/durables.go` into
`generated/workers/` and emits `dist/workers/<id>/gobeyond-worker`
from `generated/cmd/workers/<id>`.

## Trigger client

Configure a client before starting or signaling workflows. Browser code must
not dial Temporal; use `postAction` into a Go (or Node server) handler.

For Node/server triggers:

```ts
import { workflows } from "@go-beyond/workflows"
import { createClient } from "@origens-dev/temporal"

workflows.use(createClient())
await workflows.start({ workflowName: "default.demo", taskQueue: "default__local" })
```

`@origens-dev/temporal` is **server/Node only** — do not import it in browser
bundles. The durables-site example starts workflows with the Go Temporal SDK
inside action handlers instead.
