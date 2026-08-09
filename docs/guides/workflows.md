# Workflows and activities

Authored durable definitions live under `workflows/`, sibling to `app/` and
`agents/`. Use them for work that must survive request boundaries, retries, or
process restarts. A definition owns one Go package and declares one exported
compiler-visible var:

```text
workflows/<id>/workflow.go                         # top-level workflow
workflows/<id>/activity.go                         # standalone activity
workflows/<id>/activities/<id>/activity.go         # workflow-owned activity
workflows/<id>/subworkflows/<id>/workflow.go       # workflow-owned child
```

Root `workflows/*.go` files are not allowed. A definition directory contains
exactly one of `workflow.go` or `activity.go`; a standalone activity cannot own
`activities/` or `subworkflows/`.

```go
package orders

import (
  "go.temporal.io/sdk/workflow"
  gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(_ workflow.Context, input string) (string, error) { return input, nil }

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
  TaskQueue: "orders", // logical queue name; no environment suffix
}, Run)
```

Activities use `workflows.DefineActivity`. Their empty `TaskQueue` inherits the
nearest owning workflow; a top-level standalone activity with no queue uses the
default logical queue. Generated registrations use each definition's stable
name. Share ordinary Go code through `internal/`, not imports between authored
workflow packages.

## Local Temporal

Use the opt-in durable example (not the SEO fixture):

```bash
docker compose -f examples/durables-site/docker-compose.temporal.yml up -d
export GOBEYOND_WEBSITE=examples/durables-site
go run ./cmd/gobeyond dev
```

Open `/durables` to start workflows from Go actions, then observe runs in the
Temporal Web UI at http://localhost:8233.

Task queues are environment-suffixed only at the worker boundary:
`{taskQueueId}__{environment}`. Local default is `default__local`.

`gobeyond dev` builds and supervises every local queue worker. It retries when
the user-managed Temporal service is unavailable; it does not start or manage
the container. Use `--no-workflows` to run the site and direct agents without
Temporal pollers.
`gobeyond preview` supervises the queue binaries from the existing `dist/`
build with the same retry behavior and accepts the same opt-out flag.

## Build and runtime

`gobeyond build` projects `workflows/**` into generated-safe packages under
`generated/workflows/` and generated process mains under
`generated/cmd/workflows/`. It emits the Temporal poller binaries under
`dist/workers/<id>/gobeyond-worker`. The deploy manifest remains
`dist/deploy/workers.json` with
`"adapter": "temporal"`; these names describe runtime workers and do not
change with the authored workflow layout.

Worker process auth (hosted): set both `GOBEYOND_TEMPORAL_TLS_CERT` and
`GOBEYOND_TEMPORAL_TLS_KEY` for mTLS, or `GOBEYOND_TEMPORAL_API_KEY` for
API-key TLS — not both. Local Docker Temporal uses neither (plaintext).

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
