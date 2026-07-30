# GoBeyond durables example

Opt-in Temporal dogfood site. The light SEO fixture stays Temporal-free; this
example starts workflows from Go `postAction` handlers and observes runs in the
Temporal Web UI.

## Run

From the gobeyond repo root:

```bash
# 1. Temporal (gRPC :7233, UI :8233)
docker compose -f examples/durables-site/docker-compose.temporal.yml up -d

# 2. Build this website (default gobeyond build still targets seo-site)
export GOBEYOND_WEBSITE=examples/durables-site
go run ./cmd/gobeyond build

# 3. Site + worker (queue default__local)
./dist/server/gobeyond-server &
./dist/workers/default/gobeyond-worker
```

Open http://localhost:8080/durables, click either button, then check history at
http://localhost:8233.

Browser code only calls `postAction`. Workflow start uses the Go Temporal SDK
on the server. For Node/server triggers, see `@origens-dev/temporal`
(server-only; not for the browser).
