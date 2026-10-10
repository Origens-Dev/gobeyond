# Decision agents

> **Synthetic, review-only example.** This guide describes the compiler and pure
> reducer available in the current source. It does not enable a live decision
> agent, directory lookup, provider call, voice session, or speech cache.

## What is implemented

The merged [decision contract](https://github.com/Origens-Dev/gobeyond/pull/138),
[pure reducer and text traces](https://github.com/Origens-Dev/gobeyond/pull/139),
[file compiler](https://github.com/Origens-Dev/gobeyond/pull/140), and
[dormant registration seam](https://github.com/Origens-Dev/gobeyond/pull/141)
are the basis for this page:

- **agents.DefineDecision** is a compiler-visible declaration. It returns a
  **DecisionDefinition**, which has no **Invoke** method.
- The compiler reads the inline **DecisionConfig** and route files, then
  freezes the definition into the existing **.gobeyond/agents.json** manifest.
- **agents/decisions** reduces one normalized event against that frozen graph
  and returns semantic effects. It does not perform I/O.
- The generated per-agent registration hook accepts an injected adapter
  factory. Generated registries do not install a decision adapter by default,
  and unresolved activation gates fail closed.

Main also contains the dormant durable-session workflow added by
[PR #142](https://github.com/Origens-Dev/gobeyond/pull/142). This example does
not install a `DefineDecision` session adapter or start a session. No Jev path,
caller-authority policy, V0 voice, or locale is qualified; [ORI-72](https://linear.app/mensagens/issue/ORI-72/qualify-one-jev-service-path-and-typed-response-contract)
remains In Progress. Draft cache-key and exact-speech work is not provider,
cache, or activation evidence. Prewarm and lookahead are later work. This guide
makes no live-provider, live-session, cached-speech, or production-activation
claim.

## Add a separate operator

Agent definitions are separate packages under **agents/<id>/**. The checked-in
[synthetic example tree](../examples/decision-agents/agents/operator/agent.go)
keeps a decision Operator and a separate direct Operator side by side:

~~~text
docs/examples/decision-agents/agents/
├── operator/
│   ├── agent.go
│   └── routes/
│       ├── start/{route.yaml,prompt.en.md,prompt.choice_count.en.md}
│       ├── clarify/{route.yaml,prompt.en.md,prompt.choice_count.en.md}
│       └── help/{route.yaml,prompt.help.en.md}
└── alternate-operator/
    └── agent.go
~~~

The [`operator/agent.go`](../examples/decision-agents/agents/operator/agent.go)
declares tools with **agents.DefineTool** and an exported
**var Agent = agents.DefineDecision(...)**. Its **DecisionConfig** has
**RoutesDir: "routes"** and an inline frozen **decisionv1.Definition** for
graph-wide messages, grants, budgets, locales, and policy gates. The compiler
requires that definition to be a static literal; it does not evaluate a
constructor or import another authored agent package. **Graph.Routes** is
empty because the route files supply those routes. The contract has no default
**decision.yaml**. The sample tool handlers are inert compile stubs; the
decision declaration has no **Invoke** method.

[`alternate-operator/agent.go`](../examples/decision-agents/agents/alternate-operator/agent.go)
is a second, separate **agents.Define** package with its own ID and deterministic
handler. It demonstrates that adding the decision Operator does not replace
the existing agent identity or imply a handoff between the two.

The checked-in routes are [`start`](../examples/decision-agents/agents/operator/routes/start/route.yaml),
[`clarify`](../examples/decision-agents/agents/operator/routes/clarify/route.yaml),
and [`help`](../examples/decision-agents/agents/operator/routes/help/route.yaml).
Each route's Markdown prompt resources live beside its **route.yaml**.

See the [decision contract reference](../../agents/decisioncontract/README.md)
for the full schema. Under **RoutesDir**, the compiler accepts only
**route.yaml** and **prompt.*.md** files; it rejects symlinks, path escapes,
unknown files, and unknown route fields. The general
[agent guide](agents.md) describes the shared agent layout.

The route example below is the source tree's complete synthetic **/start**
route. The example also includes compiler-valid **/clarify** and **/help**
routes and every prompt file. The compiler test checks both the full tree and
that this documented route matches the source file; none of the files defines
a deployable directory service.

<!-- decision-guide-start-route -->
~~~yaml
id: /start
say:
  families: [prompt, prompt.choice_count]
  localeSource: session
  argumentBindings:
    - name: name
      source: context.greeting_name
    - name: count
      source: context.candidate_count
listen:
  accept: [speech_final, dtmf_complete, text]
  bargeIn: true
match:
  bindingId: recipient_matcher
  onNoMatch: decide
decide:
  serviceId: jev
  resultPolicy: recipient_selection
act:
  - id: connect-recipient
    toolId: connect
    kind: connect
    targetBinding: selected_candidate_id
    reauthorizeAtExecution: true
    releasesCallOwnership: true
next:
  - source: playback
    outcome: completed
    target:
      phase: listen
  - source: playback
    outcome: cleared
    target:
      route: /help
  - source: playback
    outcome: failed
    target:
      route: /help
  - source: input
    outcome: no_input
    target:
      route: /clarify
  - source: input
    outcome: utterance_limit
    target:
      route: /clarify
  - source: input
    outcome: error
    target:
      route: /help
  - source: match
    outcome: candidate
    target:
      phase: act
  - source: match
    outcome: ambiguous
    target:
      route: /clarify
  - source: match
    outcome: error
    target:
      route: /help
  - source: decision
    outcome: candidate
    target:
      phase: act
  - source: decision
    outcome: no_match
    target:
      route: /clarify
  - source: decision
    outcome: ambiguous
    target:
      route: /clarify
  - source: decision
    outcome: refusal
    target:
      route: /help
  - source: decision
    outcome: error
    target:
      route: /help
  - source: decision
    outcome: unavailable
    target:
      route: /help
  - source: effect
    outcome: accepted
    target:
      terminal: await_receipt
  - source: effect
    outcome: confirmed
    target:
      terminal: release_call_ownership
  - source: effect
    outcome: failed
    target:
      route: /help
  - source: effect
    outcome: unknown
    target:
      terminal: await_receipt
  - source: control
    outcome: cancelled
    target:
      terminal: end_session
  - source: control
    outcome: disconnected
    target:
      terminal: end_session
fallback:
  route: /help
~~~
<!-- /decision-guide-start-route -->

Route IDs come from directory paths (**start/route.yaml** becomes **/start**);
any authored **id** must match that path. Each route file uses the contract's
JSON field names and is decoded with unknown fields rejected. A route declares
explicit outcome transitions for each phase it uses. When a route omits
**useTools**, **useServices**, or **useBindings**, it inherits the corresponding
parent grants; an explicit empty list grants nothing. It cannot add grants.

## Inputs, directory results, and actions

The **say.argumentBindings** entries pair declared message arguments with
source names. For example, **name** maps to **context.greeting_name**. The
compiler preserves these names and validates the argument schema; it does not
resolve runtime context or render prompts. In this example those source names
and all directory data are synthetic.

**match.bindingId** and **decide.serviceId** are references into the inherited
binding and service envelopes. The compiler checks the references and freezes
their digests; it does not connect to a matcher, directory, or Jev. The
synthetic **jev** name is not a URL or an endpoint. No endpoint or credential
belongs in route YAML.

### Guest and verified data boundaries

The example contains no caller identity, relationship record, or candidate
data. Its **context.greeting_name** and **context.candidate_count** bindings
are source names only; they do not prove who supplied a value or whether a
caller is verified. The frozen fixture marks **g-caller-authority** unresolved,
so it defines neither a verification method nor which relationship fields a
verified caller may access.

| Application input posture | Data in this example | Contract boundary |
| --- | --- | --- |
| Guest or unverified | No protected relationship snapshot is supplied. | The example grants no caller authority and makes no claim about guest-visible product data. |
| Verified | No verified-caller fixture is supplied. | A future app must qualify caller authority and provide a scoped snapshot. Match and Jev results must resolve to candidate IDs in that snapshot; this check does not establish caller identity or qualify the source fields. |

The example does not implement a guest/verified switch. Do not attach protected
relationship data to this example or infer authority from a name, utterance,
or model result. The owner decision and evidence for
**g-caller-authority** remain required before such data can be used.

The intended bounded path is deterministic matching first, then the declared
decision service only when the match policy sends no-match there. A candidate
can reach **act**; ambiguity and no-match route to clarification; refusal,
error, and unavailable results route to help. No-input and utterance-limit
events route to clarification without calling Jev. The reducer accepts only
candidates from the active bound snapshot; an effect ID or candidate ID is not
execution authorization.

Connect and handoff actions must name an inherited tool and target binding and
set **reauthorizeAtExecution**. In the example, **accepted** and **unknown**
wait for the authoritative receipt. Only **confirmed** can request
call-ownership release, and the surrounding adapter must verify the exact
pending effect and target. The reducer emits intent; it does not connect a call
or execute a tool.

The tool map on **DecisionConfig** reuses the same **agents.DefineTool** values
as **DefineAI**; tool schemas, approval-policy presence, and IDs are checked
against those declarations. **Slots.Tools** and **Slots.Channels** likewise
use the existing agent surface. A channel declaration records a slot; it does
not select a speech provider. The source tree's **voice-provider** connector
label and tool handlers are synthetic placeholders; nothing connects or
executes. Service, caller-authority, budget, and approval provenance still
require qualified adapter evidence.

The **clarify** route in the fixture is bounded by its **recipient_selection**
retry group. Retry counters belong to a logical task and have separate
no-input, no-match, ambiguity, and total ceilings. The review fixture leaves
those values unresolved and activation-blocking; no numeric default is
suggested here. The exhaustion target is **/help**, outside the clarification
cycle.

## Prompt files and frozen identities

Prompt Markdown is route-local. For example, **prompt.en.md** and
**prompt.choice_count.en.md** supply the local **prompt** and
**prompt.choice_count** families under **start/**. Their frozen IDs include
the full route path: **/start** plus **prompt** becomes
**route.2f7374617274.prompt**. The same local name under **/clarify** gets a
different frozen identity, so each route can own its arguments and bytes.

Message metadata in **Definition.Graph.Messages** declares each family's base
and fallback locale plus its named arguments. The compiler loads the prompt
bytes from Markdown and includes them in the frozen release digest. Sharing is
explicit: use **shared:<family>** and declare one shared message identity;
every referencing route must provide the same locale set and exact bytes.

The example's English prompt variants are syntax fixtures, not evidence that
English is a qualified voice locale. The locale and voice-profile gates remain
unresolved. Prompt loading, interpolation, and speech generation are not
implemented by this compiler or reducer.

## Pure reducer trace

This text trace is copied from the executable synthetic
[jev-clarify fixture](../../agents/decisions/testdata/jev-clarify.trace).
It shows semantic effects only. The **jev** service name and all IDs are test
data; no provider is called. The [reducer guide](../../agents/decisions/README.md)
explains the reducer boundary; the reducer test verifies the trace.

<!-- decision-guide-jev-trace -->
~~~text
enter_route route=/start entry=entry-jev
listen route=/start entry=entry-jev window=window-defb97395df5b0ed8dd0ff59
say route=/start entry=entry-jev playback=playback-f61d7794eef457475c3d2a03 messages=operator.prompt,operator.choice_count
run_matcher route=/start entry=entry-jev binding=recipient_matcher input=input-jev reason=da950b880eaf928f528c5bd5fead3a7f875ca7de2d58efc9c18444d5afa8c8e3
run_decision route=/start entry=entry-jev input=input-jev service=jev reason=da950b880eaf928f528c5bd5fead3a7f875ca7de2d58efc9c18444d5afa8c8e3
enter_route route=/clarify entry=entry-9f725ebfc007b910e117ef03
listen route=/clarify entry=entry-9f725ebfc007b910e117ef03 window=window-98d52f6645f92178b99595b9
say route=/clarify entry=entry-9f725ebfc007b910e117ef03 playback=playback-c46498dd9e4a7034ebe5d0ae messages=operator.prompt,operator.choice_count
~~~
<!-- /decision-guide-jev-trace -->

## Local checks and release boundary

The feature-bearing GoBeyond source is available at the exact
[**v0.1.0-alpha.121** tag](https://github.com/Origens-Dev/gobeyond/tree/v0.1.0-alpha.121).
Pin the module in the consuming application's **go.mod** and commit **go.sum**;
do not follow a floating **@main** or **@latest** reference:

~~~sh
go get github.com/Origens-Dev/gobeyond@v0.1.0-alpha.121
~~~

Run the contract, reducer, compiler, and example-package checks locally:

~~~sh
go test ./agents/decisioncontract/v1 ./agents/decisions -count=1
go test ./internal/project -run 'TestCompileDecisionRoutesMatchesFrozenManifestAndDigests|TestDecisionCompilerSupportsExplicitSharedPromptFamilies|TestDecisionCompilerRejectsUnsafeFixtures|TestDecisionAgentsGuide|TestDecisionAgentsExample' -count=1
go test ./docs/examples/decision-agents/... -count=1
~~~

The module tag pins compiler and contract code. Separately, the compiler freezes
the definition and prompt bytes into the existing agent manifest;
**agents.FreezeDecisionManifest** uses the canonicalizer and returns the
definition's release digest. Keep that digest with the compiled release
artifacts. A changed route, grant, locale, or prompt byte changes the frozen
identity. This is source/build validation, not a live session pin or a
traffic-selection procedure.

This page does not qualify caller authority, Jev service path, retry ceilings,
voice/locale, or prompt policy. The compiled definition stays blocked by those
unresolved gates even when source tests pass. The example cannot show a real
directory or Jev result, a `DefineDecision` session lifecycle, provider
response, cached speech, or prewarming. The Markdown and example tree are
repository source; documentation sync and publication remain separate work.
