# Go workflow triggers

Use `github.com/Origens-Dev/gobeyond/adapters/temporal` to start or signal
workflows from Go server code (actions, API routes, background jobs). Browser
code must not dial Temporal; post into a Go handler that owns the trigger
client.

The Go trigger client mirrors the Node `@origens-dev/temporal` surface: one
client selects **local**, **preview**, or **hosted** transport from
environment variables, connects lazily on the first RPC, and never sends
server-derived fields (`environment_id`, `namespace`, `task_queue`) on
preview/hosted starts.

## Quick start (local)

1. Start local Temporal:

```bash
docker compose -f examples/durables-site/docker-compose.temporal.yml up -d
export GOBEYOND_WEBSITE=examples/durables-site
go run ./cmd/gobeyond dev
```

2. Start a workflow from an action:

```go
import (
	"context"

	gbtemporal "github.com/Origens-Dev/gobeyond/adapters/temporal"
)

func StartDemo(ctx *gb.ActionContext, _ Input) (Output, error) {
	client, err := gbtemporal.NewClientFromEnv(gbtemporal.ClientOptions{
		WorkerID: "default",
	})
	if err != nil {
		return Output{}, err
	}
	defer client.Close()

	handle, err := client.Start(ctx.Context, gbtemporal.StartOptions{
		WorkflowName: "default.demo",
		Args:         []any{"hello from durables-site"},
	})
	if err != nil {
		return Output{}, err
	}
	return Output{WorkflowID: handle.WorkflowID}, nil
}
```

Local mode dials `GOBEYOND_TEMPORAL_ADDRESS` (default `localhost:7233`) and
targets task queue `{workerId}__{environment}` (default `default__local`).

Optional: block on a local result with `gbtemporal.Wait(ctx, client, handle, &result)`.

## Modes

Set `GOBEYOND_TEMPORAL_MODE` to `local`, `preview`, or `hosted`.

When the mode variable is **unset**:

- If `GOBEYOND_HOST_REPORT_SOCKET` or `GOBEYOND_ENVIRONMENT_ID` is set →
  **hosted** (or **preview** when `GOBEYOND_TEMPORAL_ENVIRONMENT=preview`).
- Otherwise → **local**.

| Mode | Transport | When to use |
|------|-----------|-------------|
| `local` | Temporal SDK `ExecuteWorkflow` | `gobeyond dev`, Docker Compose, unit tests |
| `preview` / `hosted` | Host-report UDS, API fallback | Deployed GoBeyond slots with platform host |

## Hosted / preview

Hosted sandboxes prefer the host-report Unix socket
(`GOBEYOND_HOST_REPORT_SOCKET`, default `/run/gobeyond/host/host-report.sock`):

- `POST /v1/workflows/start`
- `POST /v1/workflows/signal`

Start body fields (only these — never send queue/namespace/environment):

| Field | Required | Notes |
|-------|----------|-------|
| `workflow_name` | yes | Registered workflow type |
| `workflow_id` | no | Server may mint when omitted |
| `worker_id` | yes* | Logical queue leaf (`orders`, `default`) |
| `args` | no | JSON array |
| `idempotency_key` | no | Dedup key for retries |

\*Supply via `StartOptions.WorkerID` or `ClientOptions.WorkerID`.

When UDS returns unavailable (`503` with `{"fallback":"api"}`, or socket
`ENOENT` / `ECONNREFUSED`), the client falls back to the Origens workflows API
when `GOBEYOND_API_URL` is set:

- `POST {GOBEYOND_API_URL}/internal/workflows/start`
- `POST {GOBEYOND_API_URL}/internal/workflows/signal`

Optional headers:

- `Authorization` from `GOBEYOND_API_AUTHORIZATION`
- `x-gobeyond-internal-token` from `GOBEYOND_INTERNAL_API_TOKEN`

Detect fallback with `errors.Is(err, gbtemporal.ErrTriggerUnavailable)`.

## Environment reference

| Variable | Purpose |
|----------|---------|
| `GOBEYOND_TEMPORAL_MODE` | `local` \| `preview` \| `hosted` |
| `GOBEYOND_TEMPORAL_ADDRESS` | Local Temporal host:port (default `localhost:7233`) |
| `GOBEYOND_TEMPORAL_NAMESPACE` | Local namespace (default `default`) |
| `GOBEYOND_TEMPORAL_ENVIRONMENT` | Queue environment suffix (default `local`) |
| `GOBEYOND_HOST_REPORT_SOCKET` | Host-report UDS path |
| `GOBEYOND_ENVIRONMENT_ID` | Hosted slot id; enables UDS preference when mode unset |
| `GOBEYOND_API_URL` | API fallback base URL |
| `GOBEYOND_API_AUTHORIZATION` | Bearer/session token for API fallback |
| `GOBEYOND_INTERNAL_API_TOKEN` | Internal start token for Function URL callers |

Worker binaries use separate env vars (`GOBEYOND_TEMPORAL_TASK_QUEUE`, TLS
material, deployment versioning). Trigger clients do not need worker TLS keys.

## Node / TypeScript twin

Node services can use `@origens-dev/temporal` `createClient()` with the same
mode and env conventions. Go actions in the same project should use this package
instead of raw `client.Dial` so local and hosted behavior stay aligned.

## Testing

Inject fakes through `ClientOptions`:

```go
client, err := gbtemporal.NewClient(gbtemporal.ClientOptions{
	Mode: gbtemporal.ModeHosted,
	Host: fakeHostBackend,
	API:  fakeAPIBackend,
})
```

Local tests can set `LocalDial` to avoid a running Temporal server.

Run package tests:

```bash
go test ./adapters/temporal/...
```

## Guardrails

- Never import the trigger client from browser bundles.
- Do not pass `TaskQueue` in preview/hosted starts (server-derived).
- Do not embed Temporal admin credentials in web sandboxes.
- Share business logic through `internal/`; keep trigger wiring in handlers.
