# Agents

Agents live under `agents/`, sibling to `app/` and `workflows/`. Each immediate
child is one isolated Go package with an exported compiler-visible definition:

```text
agents/<id>/agent.go
```

An agent can be a typed application handler or a framework-owned AI loop.
Typed handlers remain useful for deterministic and custom runtimes:

```go
package support

import (
  "context"
  gbagents "github.com/Origens-Dev/gobeyond/agents"
)

type Input struct { Message string `json:"message"` }
type Output struct { Message string `json:"message"` }

func Run(_ context.Context, actor gbagents.Actor, input Input) (Output, error) {
  return Output{Message: actor.ID + ": " + input.Message}, nil
}

var Agent = gbagents.Define(gbagents.Config{}, Run, gbagents.Slots{
  Channels: []gbagents.Channel{{ID: "web"}},
})
```

The zero-value config is direct: the handler runs in the site process for the
lowest latency. Set `Durable: true` to compile the same typed handler into a
Temporal workflow plus activity. `TaskQueue` is a logical queue name; an empty
durable queue resolves to `default`. Direct agents do not create Temporal
pollers.

```go
var Agent = gbagents.Define(gbagents.Config{
  Durable: true,
  TaskQueue: "support",
}, Run)
```

## AI agents

`DefineAI` hides provider construction, prompt conversion, streaming, the tool
loop, and Temporal model/tool activities. Its folder adds the always-on system
prompt as Markdown:

```text
agents/support/
├── agent.go
├── instructions.md
└── tools.go       # optional; ordinary files in the same Go package
```

```go
package support

import (
  "context"
  gbagents "github.com/Origens-Dev/gobeyond/agents"
)

type LookupInput struct { OrderID string `json:"orderId"` }
type LookupOutput struct { Status string `json:"status"` }

var lookupOrder = gbagents.DefineTool(
  gbagents.ToolConfig{
    Description: "Look up an order visible to the current customer.",
    InputSchema: map[string]any{
      "type": "object",
      "properties": map[string]any{"orderId": map[string]any{"type": "string"}},
      "required": []string{"orderId"},
    },
  },
  func(ctx context.Context, actor gbagents.Actor, input LookupInput) (LookupOutput, error) {
    return lookupForActor(ctx, actor.ID, input.OrderID)
  },
)

var Agent = gbagents.DefineAI(gbagents.AIConfig{
  Model: "openrouter/openai/gpt-4o-mini",
  Tools: map[string]gbagents.AITool{"lookup-order": lookupOrder},
})
```

Built-in model references use `anthropic/...`, `openrouter/...`, `bedrock/...`,
or `vertex/...` and read the provider package's normal environment credentials.
`AIConfig.Provider` accepts a custom Go AI SDK provider without copying it or
its credentials into the generated manifest or Temporal input. In the current
alpha, the authored package is linked into both the site registration and durable
worker binaries, so custom provider construction must remain lazy and free of
secret-loading side effects; splitting transport metadata from worker-only
executors belongs to the hosted integration.

Direct AI agents stream `agent.text.delta` events and finish with one
`agent.output`. Set `Durable: true` to use the stable
`go-temporal-ai-sdk.AgentWorkflow`: every model and tool step becomes a
separate Temporal activity, selected through a queue-wide AgentID + compiled
revision resolver. The same HTTP/session contract is used in both modes.

The compiler requires non-empty `instructions.md`, embeds it in generated Go,
and uses the finalized GoBeyond build identity as the durable runtime revision.
Workers resolve an exact AgentID + revision pair. A stale worker therefore fails
with the Temporal SDK's non-retryable runtime mismatch before it can execute a
different build's provider or tools. The current alpha intentionally fails an old
in-flight run after its local revision worker is replaced; retaining old
revisions through Temporal Worker Deployment Versioning is a hosted rollout
concern.

Tools, skills, subagents, schedules, and channels are compiler-visible slots.
The current alpha records their stable IDs in `.gobeyond/agents.json`; provider binding
and scheduled invocation are later layers. Put reusable application code under
`internal/`, not imports between authored agent packages.

## HTTP and TypeScript client

The generated Go server mounts the agent API at `/api/agents`. Start a session,
read its state, or consume its resumable SSE stream:

```bash
curl -sS http://localhost:3000/api/agents/support/sessions \
  -H 'content-type: application/json' \
  -d '{"input":{"message":"hello"}}'
```

The response contains `session`, `run`, and `eventsUrl`. SSE events have stable
session cursors and include `session.created`, `run.started`, `agent.output`,
and a terminal `run.completed`, `run.failed`, or `run.cancelled` event.

The browser-safe client uses the same contract:

```ts
import { createAgentClient } from "@go-beyond/agents"

const agents = createAgentClient()
const { session, run } = await agents.start({
  agentId: "support",
  input: { message: "hello" },
})

for await (const event of agents.events(session, { run })) {
  console.log(event.type, event.data)
}
```

The root TypeScript entry is Fetch-only. React and AI SDK v7 integrations use
`@go-beyond/agents/react` and `@go-beyond/agents/ai-sdk`; the protocol-v2
Temporal delivery connectors remain available through their dedicated exports.

## Identity and local development

Agents are private by default. A host must resolve an authenticated
`agents.Actor`; sessions are then owner-bound. `gobeyond dev` and
`gobeyond preview` enable a typed loopback-only development actor explicitly.
A deployed production server never enables that fallback from the request
address alone. Set `Public: true` only for an agent intentionally callable
without an authenticated owner.

For durable agents, run local Temporal and start `gobeyond dev` normally. The
generated queue pollers are shared with authored workflows, use physical queue
names such as `support__local`, and retry while user-managed Temporal is absent.
`--no-workflows` disables all local Temporal pollers; direct agents still work.
`gobeyond preview` uses the separate `support__preview` suffix for both the
site dispatcher and its supervised poller.

## Current alpha limitations

Session and event storage is process-local in the public framework runtime;
hosted persistence belongs to the out-of-scope hosting integration. Durable
typed handlers keep their legacy one-activity workflow. Durable AI agents use
the released `github.com/Origens-Dev/go-ai` and
`github.com/Origens-Dev/go-temporal-ai-sdk` packages for granular
model/tool durability, cancellation, records, and terminal ownership. Tools
whose static or dynamic policy requires human approval are rejected at agent
registration in the current alpha: the durable workflow can wait on the signal, but the
native local event store cannot yet expose its pending interaction safely.
Native SSE currently carries session lifecycle, direct deltas, and final
output; shared protocol-v2 preview/replay and approval delivery are a hosting
adapter boundary rather than an in-process worker shortcut. Skills, subagents,
schedules, and non-HTTP channels remain compiler-visible extension slots for
later framework slices.
