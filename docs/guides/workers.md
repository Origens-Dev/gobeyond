# Workers and durables (Phase 1)

Authored durable tasks live under `workers/`, sibling to `app/`:

```text
workers/demo/durables.go
server/cmd/worker/main.go   # or server/cmd/workers/<id>/
```

## Local Temporal (Docker)

```bash
cd examples/seo-site
docker compose -f docker-compose.temporal.yml up -d
```

Task queues are environment-suffixed: `{workerId}__{environment}`.
Local default environment is `local` → e.g. `demo__local`.

## Build

`gobeyond build` projects `workers/**/durables.go` into
`internal/gobeyondgen/workers/` and emits
`dist/workers/<id>/gobeyond-worker` when a worker cmd exists.

## Trigger client

`@go-beyond/workflows` requires a configured World. Local/preview/hosted
Worlds are provided by `@origens/temporal` (Origens). Phase 1 ships the
portable client interface only.
