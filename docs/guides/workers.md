# Workers and durables (Phase 1)

Authored durable tasks live under `workers/`, sibling to `app/`:

```text
workers/durables.go            # default worker id
workers/<id>/durables.go       # named worker
```

Authors export `Register(worker.Worker)` from each durables package.
Process mains are generated under `.generated/cmd/workers/<id>/`.

## Local Temporal (Docker)

```bash
cd examples/seo-site
docker compose -f docker-compose.temporal.yml up -d
```

Task queues are environment-suffixed: `{workerId}__{environment}`.
Local default environment is `local` → e.g. `default__local`.

## Build

`gobeyond build` projects `workers/**/durables.go` into
`.generated/workers/` and emits `dist/workers/<id>/gobeyond-worker`
from `.generated/cmd/workers/<id>`.

## Trigger client

`@go-beyond/workflows` needs a client via `workflows.use(...)`. Local/preview/hosted
clients come from `@origens-dev/temporal`. Phase 1 ships the portable
`start` / `signal` surface only.
