# Contributing

Start by reading `AGENTS.md`, `docs/architecture.md`, and the skill under
`.agents/skills/` matching the part of the framework you are changing.

GoBeyond has one non-negotiable compatibility rule: a portable component's Go
output, React server-render reference output, browser-normalized DOM, and first
hydration render must agree. New syntax is unsupported until compiler,
renderer, conformance, and diagnostic cases land together.

Before opening a change, run:

```bash
go run ./cmd/gobeyond generate --check
go test ./...
go test -race ./...
go vet ./...
pnpm -r build
pnpm -r typecheck
pnpm -r test
go run ./cmd/gobeyond build
./scripts/verify-node-free-server.sh
```

Keep website examples in React. Go owns data, status, metadata, mutations,
middleware, and APIs; it must not become a second component/template tree.
Changes to JSON schemas, generated IDs, React pins, or release versions require
root maintainer review and a changelog entry.
