# GoBeyond documentation

GoBeyond applications can combine web routes, durable workflows, and agents.
Choose the surface you are building, then use the shared architecture and
operations references when you cross a runtime boundary.

## Agents

- [Agents](guides/agents.md): typed handlers, AI agents, tools, direct versus
  durable execution, sessions, streaming, identity, and local development.

## Workflows

- [Workflows and activities](guides/workflows.md): definition layout, task
  queue inheritance, Temporal workers, local supervision, and triggers.

## Web

- [Add a React page](guides/add-page.md)
- [Connect request-time Go data](guides/connect-go-data.md)
- [Add a typed action](guides/add-action.md)
- [Add a Go API](guides/add-api.md)
- [Add request middleware](guides/middleware.md)
- [Debug generated contracts and hydration](guides/debug-contracts.md)
- [Configure public origins](guides/public-origin.md)
- [Configure icons and social sharing](guides/icons-and-social.md)
- [Optimize runtime images](guides/images.md)

## Shared runtime and operations

- [Architecture and runtime boundaries](architecture.md): source ownership,
  build outputs, site/worker execution, render plans, caching, and security
  boundaries.
- [Cache profiles](cache-profiles.md)
- [AWS deployment reference](guides/aws-reference.md)

Historical implementation plans and rejected spikes are intentionally kept out
of the public documentation tree. Shipped changes belong in the changelog;
durable behavior belongs in architecture and task-focused guides.
