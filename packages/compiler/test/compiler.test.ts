import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { relative, resolve } from 'node:path'
import test from 'node:test'

import {
  compileActionContractSource,
  compileFile,
  compilePageContractSource,
  compileProject,
  compileSource,
} from '../src/index.js'

const testDirectory = resolve(process.cwd(), 'test')

test('compiles the portable SEO fixture to the v1alpha1 golden plan', async () => {
  const sourceText = await readFile(
    resolve(testDirectory, 'fixtures/product.tsx'),
    'utf8',
  )
  const expected = JSON.parse(
    await readFile(resolve(testDirectory, 'goldens/product.plan.json'), 'utf8'),
  ) as unknown
  const result = compileSource({
    sourceText,
    fileName: 'app/products/[slug]/page.tsx',
    routeId: 'products_slug',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (result.ok) assert.deepEqual(result.plan, expected)
})

test('keeps events and effects out of the server plan while preserving initial state', () => {
  const result = compileSource({
    routeId: 'counter',
    sourceText: `
      import { useEffect, useState } from 'react'
      export default function Page({ initial }: { initial: number }) {
        const [count, setCount] = useState(initial)
        useEffect(() => console.log(count), [count])
        return <button onClick={() => setCount(count + 1)}>Count: {count}</button>
      }
    `,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'button',
    namespace: 'html',
    attributes: [],
    children: [
      { kind: 'text', value: { kind: 'literal', value: 'Count: ' } },
      { kind: 'text', value: { kind: 'path', path: ['initial'] } },
    ],
  })
})

test('compiles only a ClientOnly fallback and ignores browser-only child syntax', () => {
  const result = compileSource({
    routeId: 'map',
    sourceText: `
      import ThirdPartyMap from 'third-party-map'
      export default function Page(props: { label: string }) {
        return (
          <ClientOnly fallback={<div aria-label={props.label}>Map loading</div>}>
            <ThirdPartyMap center={window.location.hash} />
          </ClientOnly>
        )
      }
    `,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (result.ok) assert.equal(result.plan.root.kind, 'clientOnly')
})

test('reports unsupported render calls with an actionable location', () => {
  const result = compileSource({
    routeId: 'unsupported',
    fileName: 'app/page.tsx',
    sourceText: `
      import { useMemo } from 'react'
      export default function Page(props: { value: string }) {
        const formatted = useMemo(() => props.value.toUpperCase(), [props.value])
        return <h1>{formatted}</h1>
      }
    `,
  })
  assert.equal(result.ok, false)
  if (result.ok) return
  assert.ok(
    result.diagnostics.some(
      (diagnostic) =>
        diagnostic.code === 'GB1088' ||
        diagnostic.code === 'GB1076' ||
        diagnostic.code === 'GB1077',
    ),
  )
  assert.ok(result.diagnostics.every((diagnostic) => diagnostic.line > 0))
  assert.match(
    result.diagnostics[0]!.suggestion ?? '',
    /Calculate the initial value in Go|ClientOnly|portable|Go/,
  )
})

test('bakes useRef initial .current like useState', () => {
  const result = compileSource({
    routeId: 'ref',
    sourceText: `
      import { useRef } from 'react'
      export default function Page(props: { label: string }) {
        const root = useRef<HTMLDivElement>(null)
        const label = useRef(props.label)
        return <div ref={root}>{label.current}</div>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'div',
    namespace: 'html',
    attributes: [],
    children: [{ kind: 'text', value: { kind: 'path', path: ['label'] } }],
  })
})

test('splits unsupported hooks, array methods, and arbitrary calls', () => {
  const hook = compileSource({
    routeId: 'hook',
    sourceText: `
      export default function Page() {
        const x = useFancy()
        return <p>{x}</p>
      }
      function useFancy() { return 'x' }
    `,
  })
  assert.equal(hook.ok, false)
  if (!hook.ok) {
    assert.ok(hook.diagnostics.some((d) => d.code === 'GB1086'))
  }

  const array = compileSource({
    routeId: 'array',
    sourceText: `
      export default function Page(props: { items: { id: string; name: string }[] }) {
        return <ul>{props.items.find((item) => item.id === 'a')?.name}</ul>
      }
    `,
  })
  assert.equal(array.ok, false)
  if (!array.ok) {
    assert.ok(array.diagnostics.some((d) => d.code === 'GB1087'))
  }

  const call = compileSource({
    routeId: 'call',
    sourceText: `
      export default function Page() {
        return <p>{Math.random()}</p>
      }
    `,
  })
  assert.equal(call.ok, false)
  if (!call.ok) {
    assert.ok(call.diagnostics.some((d) => d.code === 'GB1088'))
  }
})

test('desugars early return if into conditional plan nodes', () => {
  const result = compileSource({
    routeId: 'modal',
    sourceText: `
      export default function Page(props: { open: boolean; title: string }) {
        if (!props.open) return null
        return <dialog>{props.title}</dialog>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'conditional')
})

test('compiles presentational class components with this.props', () => {
  const result = compileSource({
    routeId: 'class',
    sourceText: `
      import { Component } from 'react'
      export default class Card extends Component {
        props!: { title: string }
        render() {
          return <h1>{this.props.title}</h1>
        }
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.clientBoundaries.length, 0)
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'h1',
    namespace: 'html',
    attributes: [],
    children: [{ kind: 'text', value: { kind: 'path', path: ['title'] } }],
  })
})

test('bakes class field state for first paint', () => {
  const result = compileSource({
    routeId: 'class-state',
    sourceText: `
      import { Component } from 'react'
      export default class Counter extends Component {
        state = { count: 3 }
        render() {
          return <span>{this.state.count}</span>
        }
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'span',
    namespace: 'html',
    attributes: [],
    children: [{ kind: 'text', value: { kind: 'literal', value: 3 } }],
  })
})

test('compiles filter().map and dynamic index expressions', () => {
  const result = compileSource({
    routeId: 'gallery',
    sourceText: `
      export default function Page(props: {
        items: { id: string; name: string; ok: boolean }[]
        index: number
      }) {
        const current = props.items[props.index]
        return (
          <div>
            <p>{current.name}</p>
            <ul>
              {props.items.filter((item) => item.ok).map((item) => (
                <li key={item.id}>{item.name}</li>
              ))}
            </ul>
          </div>
        )
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  const json = JSON.stringify(result.plan)
  assert.match(json, /"kind":"index"/)
  assert.match(json, /"when"/)
})

test('bakes usePathname from request locals path', () => {
  const result = compileSource({
    routeId: 'nav',
    sourceText: `
      import { usePathname } from '@go-beyond/react'
      export default function Page() {
        const pathname = usePathname()
        return <nav data-path={pathname}>GoBeyond</nav>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.match(JSON.stringify(result.plan), /__gobeyond/)
})

test('compiles Columns into a styled div', () => {
  const result = compileSource({
    routeId: 'columns',
    sourceText: `
      export default function Page(props: { items: { id: string; src: string }[] }) {
        return (
          <Columns columnCount={3} gap="1rem">
            {props.items.map((item) => (
              <img key={item.id} src={item.src} alt="" />
            ))}
          </Columns>
        )
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind === 'element') {
    assert.equal(result.plan.root.tag, 'div')
  }
})

test('compiles portable useMemo by inlining the factory expression', () => {
  const result = compileSource({
    routeId: 'memo',
    sourceText: `
      import { useMemo } from 'react'
      export default function Page(props: { value: string }) {
        const label = useMemo(() => props.value + '!', [props.value])
        return <h1>{label}</h1>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
})

test('compiles lazy useState and useReducer initial state', () => {
  const result = compileSource({
    routeId: 'lazy-state',
    sourceText: `
      import { useState, useReducer } from 'react'
      export default function Page(props: { start: number }) {
        const [count] = useState(() => props.start + 1)
        const [total] = useReducer((n: number) => n, props.start)
        return <p>{count}-{total}</p>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('useId inside .map is parametric; nested component useId under map bakes parametric plan', () => {
  const ok = compileSource({
    routeId: 'map-id',
    sourceText: `
      import { useId } from 'react'
      export default function Page(props: { items: Array<{ id: string }> }) {
        return (
          <ul>
            {props.items.map((item) => {
              const id = useId()
              return <li key={item.id} id={id}>{item.id}</li>
            })}
          </ul>
        )
      }
    `,
  })
  assert.equal(ok.ok, true, ok.ok ? '' : JSON.stringify(ok.diagnostics))
  if (ok.ok) {
    assert.equal(ok.useIdSites[0]?.keyExpression, 'item.id')
    assert.equal(ok.useIdSites[0]?.skipViteRewrite, undefined)
  }

  const nested = compileSource({
    routeId: 'map-nested-id',
    // Use createElement to avoid TS2322 on JSX `key` without React's JSX types.
    sourceText: `
      import { createElement, useId } from 'react'
      function Row(props: { item: { id: string } }) {
        const id = useId()
        return <li id={id}>{props.item.id}</li>
      }
      export default function Page(props: { items: Array<{ id: string }> }) {
        return (
          <ul>
            {props.items.map((item) =>
              createElement(Row, { key: item.id, item })
            )}
          </ul>
        )
      }
    `,
  })
  assert.equal(
    nested.ok,
    true,
    nested.ok ? '' : JSON.stringify(nested.diagnostics),
  )
  if (nested.ok) {
    assert.equal(nested.useIdSites[0]?.keyExpression, 'item.id')
    assert.equal(nested.useIdSites[0]?.skipViteRewrite, true)
  }
})

test('binds the init argument of useReducer(reducer, initArg, init)', () => {
  const result = compileSource({
    routeId: 'reducer-init',
    sourceText: `
      import { useReducer } from 'react'
      export default function Page(props: { start: number }) {
        const [total] = useReducer((n: number) => n, props.start, (n: number) => n + 1)
        return <p>{total}</p>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind !== 'element') return
  assert.deepEqual(result.plan.root.children?.[0], {
    kind: 'text',
    value: {
      kind: 'binary',
      operator: '+',
      left: { kind: 'path', path: ['start'] },
      right: { kind: 'literal', value: 1 },
    },
  })
})

test('parametric useId in nested maps composes every enclosing key', () => {
  const result = compileSource({
    routeId: 'nested-ids',
    sourceText: `
      import { useId } from 'react'
      type Group = { id: string; rows: Array<{ id: string }> }
      export default function Page(props: { groups: Group[] }) {
        return (
          <ul>
            {props.groups.map((group) => (
              <li key={group.id}>
                {group.rows.map((row) => {
                  const id = useId()
                  return <span key={row.id} id={id}>{row.id}</span>
                })}
              </li>
            ))}
          </ul>
        )
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(
    result.useIdSites[0]?.keyExpression,
    'group.id + "-" + row.id',
  )
  assert.match(result.useIdSites[0]?.id ?? '', /^gb-[a-f0-9]{8}-0-$/)
})

test('records one useId site per inlined instance of a shared component', () => {
  const result = compileSource({
    routeId: 'logos',
    fileName: 'page.tsx',
    sourceText: `
      import { useId } from 'react'
      function Logo() {
        const id = useId()
        return <svg aria-labelledby={id} />
      }
      export default function Page() {
        return <header><Logo /><Logo /></header>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.useIdSites.length, 2)
  assert.match(result.useIdSites[0]?.id ?? '', /^gb-[a-f0-9]{8}-0$/)
  assert.match(result.useIdSites[1]?.id ?? '', /^gb-[a-f0-9]{8}-1$/)
  assert.equal(
    result.useIdSites[0]?.id?.replace(/-0$/, ''),
    result.useIdSites[1]?.id?.replace(/-1$/, ''),
  )
  // One source span, two plan literals: the Vite transform sequences them.
  assert.equal(result.useIdSites[0]?.start, result.useIdSites[1]?.start)
  assert.equal(result.useIdSites[0]?.end, result.useIdSites[1]?.end)
})

test('compiles nested-module useId under .map with skipViteRewrite', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import { Row } from '../components/row.js'
      export default function Page(props: { items: Array<{ id: string }> }) {
        return <ul>{props.items.map((item) => <Row key={item.id} label={item.id} />)}</ul>
      }
    `,
    'components/row.tsx': `
      import { useId } from 'react'
      export function Row({ label }: { label: string }) {
        const id = useId()
        return <li id={id}>{label}</li>
      }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'rows',
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.useIdSites[0]?.skipViteRewrite, true)
  assert.equal(result.useIdSites[0]?.keyExpression, 'item.id')
})

test('Suspense passthrough emits children; useContext reads Provider value', () => {
  const result = compileSource({
    routeId: 'suspense-ctx',
    sourceText: `
      import { Suspense, createContext, useContext } from 'react'
      const Label = createContext('x')
      function Show() {
        const value = useContext(Label)
        return <span>{value}</span>
      }
      export default function Page() {
        return (
          <Suspense fallback={<p>loading</p>}>
            <Label.Provider value="hello">
              <Show />
            </Label.Provider>
          </Suspense>
        )
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('rejects an imported component in initial markup', () => {
  const result = compileSource({
    routeId: 'third-party',
    sourceText: `
      import Chart from 'chart-package'
      export default function Page() { return <Chart /> }
    `,
  })
  assert.equal(result.ok, false)
  if (!result.ok) assert.equal(result.diagnostics[0]?.code, 'GB1051')
})

test('requires stable keys on mapped markup', () => {
  const result = compileSource({
    routeId: 'missing-key',
    sourceText: `
      export default function Page(props: { items: { id: string }[] }) {
        return <ul>{props.items.map((item) => <li>{item.id}</li>)}</ul>
      }
    `,
  })
  assert.equal(result.ok, false)
  if (!result.ok)
    assert.ok(
      result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1064'),
    )
})

test('normalizes multiline JSX text without hydration-changing edge spaces', () => {
  const result = compileSource({
    routeId: 'whitespace',
    sourceText: `
      export default function Page() {
        return (
          <p>
            First
            second
          </p>
        )
      }
    `,
  })
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'p',
    namespace: 'html',
    attributes: [],
    children: [
      { kind: 'text', value: { kind: 'literal', value: 'First second' } },
    ],
  })
})

test('decodes JSX text and attribute HTML entities like Vite/React', () => {
  const result = compileSource({
    routeId: 'entities',
    sourceText: `
      export default function Page() {
        return (
          <p title="A&hellip;B">Preparing sign-in&hellip;</p>
        )
      }
    `,
  })
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'p',
    namespace: 'html',
    attributes: [{ name: 'title', mode: 'string', value: { kind: 'literal', value: 'A…B' } }],
    children: [
      { kind: 'text', value: { kind: 'literal', value: 'Preparing sign-in…' } },
    ],
  })
})

test('surfaces TypeScript parse errors instead of emitting a partial plan', () => {
  const result = compileSource({
    routeId: 'broken',
    fileName: 'app/broken/page.tsx',
    sourceText: 'export default function Page() { return <main>broken</div> }',
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    assert.ok(
      result.diagnostics.some((diagnostic) => diagnostic.code.startsWith('TS')),
    )
  }
})

test('never emits an invalid empty path for a bare props object', () => {
  const result = compileSource({
    routeId: 'bare-props',
    sourceText:
      'export default function Page(props: object) { return <p>{props}</p> }',
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    assert.ok(
      result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1069'),
    )
  }
})

test('uses null for omitted nested component props instead of leaking root props', () => {
  const result = compileSource({
    routeId: 'omitted-local-prop',
    sourceText: `
      function Card({ title }: { title?: string }) { return <p>{title}</p> }
      export default function Page(props: { title: string }) {
        return <main><Card /></main>
      }
    `,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'main',
    namespace: 'html',
    attributes: [],
    children: [
      {
        kind: 'element',
        tag: 'p',
        namespace: 'html',
        attributes: [],
        children: [{ kind: 'text', value: { kind: 'literal', value: null } }],
      },
    ],
  })
})

test('compiles nested component default props and scalar ternaries', () => {
  const result = compileSource({
    routeId: 'nested-defaults',
    sourceText: `
      function Mark({ prefix = 'example', tone }: { prefix?: string; tone?: string }) {
        const id = tone ? \`\${prefix}-\${tone}\` : \`\${prefix}-navy\`
        return <svg id={id} />
      }
      export default function Page() {
        return <Mark />
      }
    `,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind !== 'element') return
  assert.equal(result.plan.root.tag, 'svg')
  const id = result.plan.root.attributes?.find((attribute) => attribute.name === 'id')
  assert.equal(id?.value.kind, 'ternary')
  const serialized = JSON.stringify(id?.value)
  assert.match(serialized, /"value":"example"/)
  assert.match(serialized, /"value":"-navy"/)
  assert.match(serialized, /"kind":"ternary"/)
})

test('compiles portable module-level const bindings into plan expressions', () => {
  const literal = compileSource({
    routeId: 'module-const-literal',
    sourceText: `
      const NAVY_ID = 'example-navy'
      const BLUE_ID = 'example-blue'
      export default function Page() {
        return (
          <svg>
            <linearGradient id={NAVY_ID} />
            <path fill={'url(#' + NAVY_ID + ')'} />
            <linearGradient id={BLUE_ID} />
          </svg>
        )
      }
    `,
  })
  assert.equal(
    literal.ok,
    true,
    !literal.ok ? JSON.stringify(literal.diagnostics, null, 2) : '',
  )
  if (!literal.ok) return
  assert.equal(literal.plan.root.kind, 'element')
  if (literal.plan.root.kind !== 'element') return
  const navy = literal.plan.root.children?.[0]
  assert.equal(navy?.kind, 'element')
  if (navy?.kind !== 'element') return
  assert.deepEqual(navy.attributes?.[0]?.value, {
    kind: 'literal',
    value: 'example-navy',
  })

  const derived = compileSource({
    routeId: 'module-const-derived',
    sourceText: `
      const PREFIX = 'example'
      const NAVY_ID = PREFIX + '-navy'
      const BLUE_ID = \`\${PREFIX}-blue\`
      export default function Page() {
        return (
          <svg>
            <linearGradient id={NAVY_ID} />
            <linearGradient id={BLUE_ID} />
          </svg>
        )
      }
    `,
  })
  assert.equal(
    derived.ok,
    true,
    !derived.ok ? JSON.stringify(derived.diagnostics, null, 2) : '',
  )
  if (!derived.ok) return
  const serialized = JSON.stringify(derived.plan.root)
  assert.match(serialized, /"value":"example"/)
  assert.match(serialized, /"value":"-navy"/)
  assert.match(serialized, /"value":"-blue"/)
  assert.match(serialized, /"kind":"binary"/)
})

test('rejects non-portable module-level bindings used in portable expressions', () => {
  const dynamic = compileSource({
    routeId: 'module-const-dynamic',
    sourceText: `
      const PREFIX = process.env.PREFIX ?? 'example'
      export default function Page() {
        return <svg id={PREFIX} />
      }
    `,
  })
  assert.equal(dynamic.ok, false)
  const dynamicDiagnostic = dynamic.diagnostics.find((entry) => entry.code === 'GB1068')
  assert.ok(dynamicDiagnostic)
  assert.match(dynamicDiagnostic?.message ?? '', /Module-level const PREFIX/)
  assert.match(dynamicDiagnostic?.suggestion ?? '', /portable literal|calculate it in Go/i)

  const mutable = compileSource({
    routeId: 'module-let',
    sourceText: `
      let PREFIX = 'example'
      export default function Page() {
        return <svg id={PREFIX} />
      }
    `,
  })
  assert.equal(mutable.ok, false)
  const mutableDiagnostic = mutable.diagnostics.find((entry) => entry.code === 'GB1068')
  assert.ok(mutableDiagnostic)
  assert.match(mutableDiagnostic?.message ?? '', /Module-level let PREFIX/)
})

test('allows unused non-portable module consts without failing the plan', () => {
  const result = compileSource({
    routeId: 'unused-module-const',
    sourceText: `
      const UNUSED = process.env.PREFIX ?? 'x'
      export default function Page() {
        return <p>ok</p>
      }
    `,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
})

test('compiles React useId() to a stable call-site literal for hydration', () => {
  const result = compileSource({
    routeId: 'use-id',
    fileName: 'page.tsx',
    sourceText: `
      import { useId } from 'react'
      export default function Page() {
        const id = useId()
        return <svg id={id} />
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind !== 'element') return
  const id = result.plan.root.attributes?.find((attribute) => attribute.name === 'id')
  assert.equal(result.useIdSites.length, 1)
  assert.match(result.useIdSites[0]?.id ?? '', /^gb-[a-f0-9]{8}-0$/)
  assert.deepEqual(id?.value, {
    kind: 'literal',
    value: result.useIdSites[0]?.id,
  })
  assert.equal(result.useIdSites[0]?.source, 'page.tsx')
})

test('useId span tokens match across routes for a shared module', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'components/logo.tsx': `
      import { useId } from 'react'
      export function Logo() {
        const id = useId()
        return <svg id={id} />
      }
    `,
    'app/a/page.tsx': `
      import { Logo } from '../../components/logo.js'
      export default function Page() { return <Logo /> }
    `,
    'app/b/page.tsx': `
      import { Logo } from '../../components/logo.js'
      export default function Page() { return <Logo /> }
    `,
  })
  const first = await compileFile({
    projectRoot,
    entryFile: 'app/a/page.tsx',
    routeId: 'a',
  })
  const second = await compileFile({
    projectRoot,
    entryFile: 'app/b/page.tsx',
    routeId: 'b',
  })
  assert.equal(first.ok, true, first.ok ? '' : JSON.stringify(first.diagnostics))
  assert.equal(second.ok, true, second.ok ? '' : JSON.stringify(second.diagnostics))
  if (!first.ok || !second.ok) return
  assert.equal(first.useIdSites[0]?.id, second.useIdSites[0]?.id)
  assert.equal(first.useIdSites[0]?.source, 'components/logo.tsx')
  assert.equal(second.useIdSites[0]?.source, 'components/logo.tsx')
})

test('expands portable JSX spreads from rest props and object literals', () => {
  const result = compileSource({
    routeId: 'spreads',
    sourceText: `
      function Mark({ className, ...props }: { className?: string; role?: string }) {
        return <svg className={className} {...props} />
      }
      export default function Page() {
        const attrs = { role: 'img' }
        return <Mark className="logo" {...attrs} />
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind !== 'element') return
  const names = result.plan.root.attributes?.map((attribute) => attribute.name) ?? []
  assert.deepEqual(names.sort(), ['className', 'role'].sort())
  const role = result.plan.root.attributes?.find((attribute) => attribute.name === 'role')
  assert.deepEqual(role?.value, { kind: 'literal', value: 'img' })
})

test('rejects loose equality and direct table rows as hydration-unsafe', () => {
  const loose = compileSource({
    routeId: 'loose',
    sourceText: `export default function Page(props: { number: number, text: string }) {
      return props.number == props.text ? <p>equal</p> : <p>different</p>
    }`,
  })
  assert.equal(loose.ok, false)
  if (!loose.ok)
    assert.ok(
      loose.diagnostics.some((diagnostic) => diagnostic.code === 'GB1075'),
    )

  const table = compileSource({
    routeId: 'table',
    sourceText:
      'export default function Page(){return <table><tr><td>Cell</td></tr></table>}',
  })
  assert.equal(table.ok, false)
  if (!table.ok)
    assert.ok(
      table.diagnostics.some((diagnostic) => diagnostic.code === 'GB1033'),
    )
})

test('preserves scalar strict equality but rejects object and array reference equality', () => {
  const scalar = compileSource({
    routeId: 'scalar-equality',
    sourceText: `export default function Page(props: { name: string; count: number; active: boolean }) {
      return props.name === 'portable' && props.count !== 0 && props.active === true
        ? <p>match</p>
        : <p>different</p>
    }`,
  })
  assert.equal(
    scalar.ok,
    true,
    scalar.ok ? '' : JSON.stringify(scalar.diagnostics, null, 2),
  )

  for (const [label, expression] of [
    ['object', 'props.value === props.value'],
    ['array', 'props.items !== props.items'],
    ['object literal', "props.value === { id: 'same' }"],
    ['array literal', 'props.items === []'],
  ]) {
    const result = compileSource({
      routeId: `reference-equality-${label}`,
      sourceText: `export default function Page(props: { value: { id: string }; items: string[] }) {
        return ${expression} ? <p>same</p> : <p>different</p>
      }`,
    })
    assert.equal(result.ok, false, label)
    if (!result.ok) {
      const diagnostic = result.diagnostics.find(
        (candidate) => candidate.code === 'GB1082',
      )
      assert.ok(diagnostic, label)
      assert.match(diagnostic.suggestion ?? '', /stable scalar property/)
    }
  }
})

test('restricts portable case helpers to statically known ASCII strings', () => {
  const ascii = compileSource({
    routeId: 'ascii-case',
    sourceText: `export default function Page() { return <p>{upper('portable')} {lower('GOBEYOND')}</p> }`,
  })
  assert.equal(
    ascii.ok,
    true,
    ascii.ok ? '' : JSON.stringify(ascii.diagnostics, null, 2),
  )

  for (const [label, expression] of [
    ['dynamic', 'upper(props.label)'],
    ['unicode', "upper('ß')"],
  ]) {
    const result = compileSource({
      routeId: `unicode-case-${label}`,
      sourceText: `export default function Page(props: { label: string }) { return <p>{${expression}}</p> }`,
    })
    assert.equal(result.ok, false, label)
    if (!result.ok) {
      const diagnostic = result.diagnostics.find(
        (candidate) => candidate.code === 'GB1083',
      )
      assert.ok(diagnostic, label)
      assert.match(
        diagnostic.suggestion ?? '',
        /Calculate Unicode-aware casing in Go/,
      )
    }
  }
})

test('compiles the current local year from a zero-argument Date', () => {
  const result = compileSource({
    routeId: 'current-year',
    sourceText: `export default function Page() {
      return <footer>© {new Date().getFullYear()} Studio</footer>
    }`,
  })
  assert.equal(
    result.ok,
    true,
    result.ok ? '' : JSON.stringify(result.diagnostics, null, 2),
  )
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'footer',
    namespace: 'html',
    attributes: [],
    children: [
      { kind: 'text', value: { kind: 'literal', value: '© ' } },
      {
        kind: 'text',
        value: {
          kind: 'intrinsic',
          name: 'ecmascript.Date.prototype.getFullYear',
          arguments: [],
        },
      },
      { kind: 'text', value: { kind: 'literal', value: ' Studio' } },
    ],
  })
  assert.equal(result.dateIntrinsicSites.length, 1)
  assert.equal(result.dateIntrinsicSites[0]?.getter, 'getFullYear')
})

test('compiles portable useCallback for event handlers', () => {
  const result = compileSource({
    routeId: 'callback',
    sourceText: `
      import { useCallback } from 'react'
      export default function Page(props: { label: string }) {
        const onClick = useCallback(() => props.label, [props.label])
        return <button onClick={onClick}>{props.label}</button>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('reports GB1085 for conditional protected hooks', () => {
  const result = compileSource({
    routeId: 'cond-hook',
    sourceText: `
      import { useMemo } from 'react'
      export default function Page(props: { on: boolean; value: string }) {
        const label = props.on ? useMemo(() => props.value, [props.value]) : props.value
        return <h1>{label}</h1>
      }
    `,
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    assert.ok(result.diagnostics.some((d) => d.code === 'GB1085'))
  }
})

test('compiles keyed Fragment roots in .map', () => {
  const result = compileSource({
    routeId: 'frag-map',
    sourceText: `
      import { Fragment } from 'react'
      export default function Page(props: { items: Array<{ id: string; label: string }> }) {
        return (
          <ul>
            {props.items.map((item) => (
              <Fragment key={item.id}>
                <li>{item.label}</li>
              </Fragment>
            ))}
          </ul>
        )
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('folds function defaultProps into missing props', () => {
  const result = compileSource({
    routeId: 'defaults',
    sourceText: `
      function Card({ title }: { title?: string }) {
        return <p>{title}</p>
      }
      Card.defaultProps = { title: 'Untitled' }
      export default function Page() {
        return <Card />
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'p',
    namespace: 'html',
    attributes: [],
    children: [{ kind: 'text', value: { kind: 'literal', value: 'Untitled' } }],
  })
})

test('compiles static style object locals', () => {
  const result = compileSource({
    routeId: 'style-local',
    sourceText: `
      export default function Page(props: { gap: number }) {
        const style = { color: 'red', marginTop: props.gap }
        return <div style={style} />
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('rejects React.lazy with ClientOnly guidance', () => {
  const result = compileSource({
    routeId: 'lazy',
    sourceText: `
      import { lazy } from 'react'
      const Chart = lazy(() => import('chart-package'))
      export default function Page() {
        return <Chart />
      }
    `,
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    assert.ok(result.diagnostics.some((d) => d.code === 'GB1098'))
  }
})

test('compiles form defaultValue and defaultChecked', () => {
  const result = compileSource({
    routeId: 'form-defaults',
    sourceText: `
      export default function Page(props: { name: string; ok: boolean }) {
        return (
          <form>
            <input name="title" defaultValue={props.name} />
            <input type="checkbox" name="ok" defaultChecked={props.ok} />
          </form>
        )
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('compiles static Children.toArray over a fragment', () => {
  const result = compileSource({
    routeId: 'children-toarray',
    sourceText: `
      import { Children } from 'react'
      export default function Page() {
        return <div>{Children.toArray(<><span>a</span><span>b</span></>)}</div>
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('compiles limited cloneElement over a JSX local', () => {
  const result = compileSource({
    routeId: 'clone',
    sourceText: `
      import { cloneElement } from 'react'
      export default function Page(props: { label: string }) {
        const el = <button type="button">x</button>
        return cloneElement(el, { 'aria-label': props.label })
      }
    `,
  })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
})

test('rejects all children in HTML raw-text and RCDATA elements', () => {
  for (const tag of [
    'iframe',
    'noembed',
    'noframes',
    'noscript',
    'plaintext',
    'script',
    'style',
    'textarea',
    'title',
    'xmp',
  ]) {
    for (const [kind, child] of [
      ['dynamic', '{props.value}'],
      ['static', 'static & initial text'],
    ]) {
      const result = compileSource({
        routeId: `${kind}-raw-text-${tag}`,
        sourceText: `export default function Page(props: { value: string }) {
          return <${tag}>${child}</${tag}>
        }`,
      })
      assert.equal(result.ok, false, `${tag} ${kind}`)
      if (!result.ok) {
        const diagnostic = result.diagnostics.find(
          (candidate) => candidate.code === 'GB1037',
        )
        assert.ok(diagnostic, `${tag} ${kind}`)
        assert.match(diagnostic.message, new RegExp(`<${tag}>`))
      }
    }
  }

  const controlledTextarea = compileSource({
    routeId: 'controlled-textarea',
    sourceText: `export default function Page(props: { value: string }) {
      return <textarea readOnly value={props.value} />
    }`,
  })
  assert.equal(
    controlledTextarea.ok,
    true,
    controlledTextarea.ok
      ? ''
      : JSON.stringify(controlledTextarea.diagnostics, null, 2),
  )
})

test('inherits SVG namespace for ambiguous descendant tags', () => {
  const result = compileSource({
    routeId: 'svg-link',
    sourceText:
      'export default function Page(){return <svg><a xlinkHref="#dot"><circle id="dot" cx={5} cy={5} r={5}/></a></svg>}',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics) : '',
  )
  if (!result.ok) return
  const svg = result.plan.root
  assert.equal(svg.kind, 'element')
  if (svg.kind !== 'element') return
  assert.equal(svg.namespace, 'svg')
  const link = svg.children?.[0]
  assert.equal(link?.kind, 'element')
  if (link?.kind === 'element') assert.equal(link.namespace, 'svg')
})

test('rejects browser-reparsed HTML nesting after component expansion', () => {
  const paragraph = compileSource({
    routeId: 'invalid-paragraph',
    sourceText:
      'export default function Page(){return <p><span><div>Moved</div></span></p>}',
  })
  assert.equal(paragraph.ok, false)
  if (!paragraph.ok)
    assert.ok(
      paragraph.diagnostics.some((diagnostic) => diagnostic.code === 'GB1036'),
    )

  const mappedRows = compileSource({
    routeId: 'mapped-rows',
    sourceText:
      'export default function Page(props: { rows: string[] }) { return <table>{props.rows.map(row => <tr key={row}><td>{row}</td></tr>)}</table> }',
  })
  assert.equal(mappedRows.ok, false)
  if (!mappedRows.ok)
    assert.ok(
      mappedRows.diagnostics.some((diagnostic) => diagnostic.code === 'GB1033'),
    )
})

test('preserves inline style source order in the render plan', () => {
  const result = compileSource({
    routeId: 'styles',
    sourceText: `export default function Page(props: { gap: number }) {
      return <div style={{ color: 'red', backgroundColor: 'blue', marginTop: props.gap, aspectRatio: 2 }} />
    }`,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics) : '',
  )
  if (!result.ok || result.plan.root.kind !== 'element') return
  assert.deepEqual(result.plan.root.attributes?.[0]?.value, {
    kind: 'helper',
    name: 'style',
    arguments: [
      { kind: 'literal', value: 'color' },
      { kind: 'literal', value: 'red' },
      { kind: 'literal', value: 'backgroundColor' },
      { kind: 'literal', value: 'blue' },
      { kind: 'literal', value: 'marginTop' },
      { kind: 'path', path: ['gap'] },
      { kind: 'literal', value: 'aspectRatio' },
      { kind: 'literal', value: 2 },
    ],
  })
})

test('compiles imageSrc as a portable helper', () => {
  const result = compileSource({
    routeId: 'image-source',
    sourceText: `export default function Page(props: { source: string }) {
      return <img src={imageSrc(props.source, { w: 640, q: 82, f: 'jpeg' })} alt="" />
    }`,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics) : '',
  )
  if (!result.ok || result.plan.root.kind !== 'element') return
  assert.deepEqual(result.plan.root.attributes?.find(({ name }) => name === 'src')?.value, {
    kind: 'helper',
    name: 'imageSrc',
    arguments: [
      { kind: 'path', path: ['source'] },
      { kind: 'literal', value: { w: 640, q: 82, f: 'jpeg' } },
    ],
  })
})

test('recursively compiles relative default and named project components', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import Card from '../components/card.js'
      export default function Page(props: { title: string }) {
        return <main><Card title={props.title} /></main>
      }
    `,
    'components/card.tsx': `
      import { Badge } from './badge.js'
      export default function Card({ title }: { title: string }) {
        return <section><h1>{title}</h1><Badge label="Portable" /></section>
      }
    `,
    'components/badge.tsx': `
      export function Badge({ label }: { label: string }) {
        return <strong>{label}</strong>
      }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'root',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.plan.root, {
    kind: 'element',
    tag: 'main',
    namespace: 'html',
    attributes: [],
    children: [
      {
        kind: 'element',
        tag: 'section',
        namespace: 'html',
        attributes: [],
        children: [
          {
            kind: 'element',
            tag: 'h1',
            namespace: 'html',
            attributes: [],
            children: [
              { kind: 'text', value: { kind: 'path', path: ['title'] } },
            ],
          },
          {
            kind: 'element',
            tag: 'strong',
            namespace: 'html',
            attributes: [],
            children: [
              { kind: 'text', value: { kind: 'literal', value: 'Portable' } },
            ],
          },
        ],
      },
    ],
  })
})

test('resolves project components through an explicit source-root alias', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import { Hero } from '@site/hero.js'
      export default function Page(props: { heading: string }) {
        return <Hero heading={props.heading} />
      }
    `,
    'website/hero.tsx': `
      export function Hero({ heading }: { heading: string }) {
        return <h1>{heading}</h1>
      }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'root',
    sourceRoots: [{ prefix: '@site/', directory: 'website' }],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
})

test('flattens cross-file JSX children composition into the render plan', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import { Shell } from '../components/shell.js'
      export default function Page(props: { title: string }) {
        return <Shell><article><h1>{props.title}</h1></article></Shell>
      }
    `,
    'components/shell.tsx': `
      export function Shell(props: { children: unknown }) {
        return <div className="shell"><header>Site</header>{props.children}</div>
      }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'root',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind !== 'element') return
  assert.equal(result.plan.root.tag, 'div')
  assert.equal(result.plan.root.children?.[1]?.kind, 'element')
  const article = result.plan.root.children?.[1]
  if (article?.kind === 'element') assert.equal(article.tag, 'article')
})

test('allows an npm component inside ClientOnly without traversing it', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import Map from 'third-party-map'
      export default function Page(props: { label: string }) {
        return <ClientOnly fallback={<p>{props.label}</p>}><Map center={window.location.hash} /></ClientOnly>
      }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'map',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (result.ok) assert.equal(result.plan.root.kind, 'clientOnly')
})

test('rejects an npm component outside ClientOnly', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import Chart from 'chart-package'
      export default function Page() { return <Chart /> }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'chart',
  })
  assert.equal(result.ok, false)
  if (!result.ok)
    assert.ok(
      result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1053'),
    )
})

test('rejects cross-file component render cycles with the complete chain', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import { First } from '../components/first.js'
      export default function Page() { return <First /> }
    `,
    'components/first.tsx': `
      import { Second } from './second.js'
      export function First() { return <Second /> }
    `,
    'components/second.tsx': `
      import { First } from './first.js'
      export function Second() { return <First /> }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'cycle',
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    const cycle = result.diagnostics.find(
      (diagnostic) => diagnostic.code === 'GB1010',
    )
    assert.ok(cycle)
    assert.match(cycle.message, /First.*Second.*First/)
  }
})

test('reports missing project imports at their source location', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import Missing from '../components/missing.js'
      export default function Page() { return <Missing /> }
    `,
  })
  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'missing',
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    const diagnostic = result.diagnostics.find(
      (candidate) => candidate.code === 'GB1104',
    )
    assert.ok(diagnostic)
    assert.match(diagnostic.fileName, /app\/page\.tsx$/)
    assert.ok(diagnostic.line > 0 && diagnostic.column > 0)
  }
})

test('compileProject compiles the real SEO article and imported product component', async () => {
  const repositoryRoot = resolve(process.cwd(), '../..')
  const result = await compileProject({
    projectRoot: repositoryRoot,
    routes: [
      {
        routeId: 'articles_slug',
        entryFile: 'examples/seo-site/app/articles/[slug]/page.tsx',
      },
      {
        routeId: 'products_slug',
        entryFile: 'examples/seo-site/app/products/[slug]/page.tsx',
      },
    ],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.equal(result.output.apiVersion, 'gobeyond.compiler-project/v1alpha1')
  assert.deepEqual(result.output.clientBoundaries, {
    apiVersion: 'gobeyond.client-boundaries/v1alpha1',
    boundaries: [],
    useIdSites: [],
    dateIntrinsicSites: [],
  })
  assert.deepEqual(
    result.output.plans.map((plan) => plan.routeId),
    ['articles_slug', 'products_slug'],
  )
  assert.deepEqual(
    result.output.contracts.routes.map((contract) => contract.routeId),
    ['articles_slug', 'products_slug'],
  )
  assert.deepEqual(
    result.output.contracts.actions.map((contract) => contract.actionId),
    ['products_slug:addToCart'],
  )
})

test('normalizes the real SEO product page schema without executing TypeScript', async () => {
  const repositoryRoot = resolve(process.cwd(), '../..')
  const fileName = resolve(
    repositoryRoot,
    'examples/seo-site/app/products/[slug]/page.schema.ts',
  )
  const result = compilePageContractSource({
    sourceText: await readFile(fileName, 'utf8'),
    fileName,
    routeId: 'products_slug',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.contract.props, {
    kind: 'object',
    shape: {
      slug: { kind: 'string' },
      name: { kind: 'string' },
      description: { kind: 'string' },
      canonical: { kind: 'string' },
      image: { kind: 'string' },
      imageAlt: { kind: 'string' },
      price: { kind: 'number' },
      priceLabel: { kind: 'string' },
      currency: { kind: 'string' },
      availability: { kind: 'enum', values: ['InStock', 'OutOfStock'] },
    },
  })
})

test('normalizes the real SEO action with a stable route-qualified ID', async () => {
  const repositoryRoot = resolve(process.cwd(), '../..')
  const fileName = resolve(
    repositoryRoot,
    'examples/seo-site/app/products/[slug]/actions.ts',
  )
  const result = compileActionContractSource({
    sourceText: await readFile(fileName, 'utf8'),
    fileName,
    routeId: 'products_slug',
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.contracts, [
    {
      actionId: 'products_slug:addToCart',
      input: {
        kind: 'object',
        shape: {
          productSlug: { kind: 'string' },
          quantity: { kind: 'integer' },
        },
      },
      output: {
        kind: 'object',
        shape: {
          saved: { kind: 'boolean' },
          cartItemCount: { kind: 'integer' },
        },
      },
    },
  ])
})

test('carries definePage route caching into the value contract', () => {
  const result = compilePageContractSource({
    routeId: 'products_slug',
    sourceText: `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({
        props: schema.object({ title: schema.string() }),
        revalidate: 60,
        tags: ['products', 'product'],
      })
    `,
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.equal(result.contract.revalidate, 60)
  assert.deepEqual(result.contract.tags, ['products', 'product'])
})

test('leaves route caching absent when definePage omits it', () => {
  const result = compilePageContractSource({
    routeId: 'products_slug',
    sourceText: `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({ title: schema.string() }) })
    `,
  })
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.equal(result.contract.revalidate, undefined)
  assert.equal(result.contract.tags, undefined)
})

test('rejects unknown definePage keys instead of dropping them', () => {
  const result = compilePageContractSource({
    routeId: 'products_slug',
    sourceText: `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({
        props: schema.object({ title: schema.string() }),
        revalidte: 60,
      })
    `,
  })
  assert.equal(result.ok, false)
  if (result.ok) return
  const diagnostic = result.diagnostics.find(
    (candidate) => candidate.code === 'GB1203',
  )
  assert.ok(diagnostic)
  assert.match(diagnostic.message, /revalidte/)
})

test('rejects definePage revalidate values that are not positive whole seconds', () => {
  for (const revalidate of ['0', '-60', '1.5', 'sixty', 'Number(60)']) {
    const result = compilePageContractSource({
      routeId: 'products_slug',
      sourceText: `
        import { definePage, schema } from '@go-beyond/schema'
        export const page = definePage({
          props: schema.object({ title: schema.string() }),
          revalidate: ${revalidate},
        })
      `,
    })
    assert.equal(result.ok, false, `expected ${revalidate} to be rejected`)
    if (result.ok) continue
    assert.ok(
      result.diagnostics.some((candidate) => candidate.code === 'GB1204'),
      `expected GB1204 for ${revalidate}`,
    )
  }
})

test('rejects definePage tags that are not unique non-empty string literals', () => {
  for (const tags of ["['a', 'a']", "['']", '[]', '[tag]', "'products'"]) {
    const result = compilePageContractSource({
      routeId: 'products_slug',
      sourceText: `
        import { definePage, schema } from '@go-beyond/schema'
        const tag = 'products'
        export const page = definePage({
          props: schema.object({ title: schema.string() }),
          tags: ${tags},
        })
      `,
    })
    assert.equal(result.ok, false, `expected ${tags} to be rejected`)
    if (result.ok) continue
    assert.ok(
      result.diagnostics.some((candidate) => candidate.code === 'GB1205'),
      `expected GB1205 for ${tags}`,
    )
  }
})

test('rejects definePage tags that have no revalidate window to invalidate', () => {
  const result = compilePageContractSource({
    routeId: 'products_slug',
    sourceText: `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({
        props: schema.object({ title: schema.string() }),
        tags: ['products'],
      })
    `,
  })
  assert.equal(result.ok, false)
  if (result.ok) return
  assert.ok(
    result.diagnostics.some((candidate) => candidate.code === 'GB1206'),
  )
})

test('rejects definePage route caching on a route without a Go loader', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx':
      'export default function Page() { return <main>Static</main> }',
    'app/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({}), revalidate: 60 })
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx' }],
  })
  assert.equal(result.ok, false)
  if (result.ok) return
  const diagnostic = result.diagnostics.find(
    (candidate) => candidate.code === 'GB1235',
  )
  assert.ok(diagnostic)
  assert.match(diagnostic.message, /page\.go/)
})

test('accepts definePage route caching beside a Go loader', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx':
      'export default function Page() { return <main>Dynamic</main> }',
    'app/page.go': 'package root\n',
    'app/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({
        props: schema.object({}),
        revalidate: 60,
        tags: ['products'],
      })
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx', kind: 'dynamic' }],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.equal(result.output.contracts.routes[0]?.revalidate, 60)
  assert.deepEqual(result.output.contracts.routes[0]?.tags, ['products'])
})

test('rejects executable helpers in value contracts', () => {
  const result = compilePageContractSource({
    routeId: 'unsafe',
    sourceText: `
      import { definePage, schema } from '@go-beyond/schema'
      const choose = () => schema.string()
      export const page = definePage({ props: choose() })
    `,
  })
  assert.equal(result.ok, false)
  if (!result.ok)
    assert.ok(
      result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1210'),
    )
})

test('compileProject follows the real en/fr direct page schema forwards', async () => {
  const repositoryRoot = resolve(process.cwd(), '../..')
  const result = await compileProject({
    projectRoot: repositoryRoot,
    routes: [
      {
        routeId: 'en_articles_slug',
        entryFile: 'examples/seo-site/app/en/articles/[slug]/page.tsx',
      },
      {
        routeId: 'fr_articles_slug',
        entryFile: 'examples/seo-site/app/fr/articles/[slug]/page.tsx',
      },
    ],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(
    result.output.contracts.routes.map((contract) => contract.routeId),
    ['en_articles_slug', 'fr_articles_slug'],
  )
  assert.deepEqual(
    result.output.contracts.routes[0]?.props,
    result.output.contracts.routes[1]?.props,
  )
})

test('reports direct page schema forwarding cycles', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx':
      'export default function Page() { return <main>Cycle</main> }',
    'app/page.schema.ts': `export { page } from './other.schema.js'`,
    'app/other.schema.ts': `export { page } from './page.schema.js'`,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx' }],
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    const diagnostic = result.diagnostics.find(
      (candidate) => candidate.code === 'GB1233',
    )
    assert.ok(diagnostic)
    assert.match(
      diagnostic.message,
      /page\.schema\.ts.*other\.schema\.ts.*page\.schema\.ts/,
    )
  }
})

test('reports missing direct page schema forwarding targets at the export', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx':
      'export default function Page() { return <main>Missing</main> }',
    'app/page.schema.ts': `export { page } from './missing.schema.js'`,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx' }],
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    const diagnostic = result.diagnostics.find(
      (candidate) => candidate.code === 'GB1232',
    )
    assert.ok(diagnostic)
    assert.match(diagnostic.fileName, /page\.schema\.ts$/)
    assert.ok(diagnostic.line > 0 && diagnostic.column > 0)
  }
})

test('executes literal static props and metadata in build-only Node modules', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      export default function Page(props: { title: string }) { return <h1>{props.title}</h1> }
    `,
    'app/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({ title: schema.string() }) })
    `,
    'app/page.build.ts': `
      export async function loadStaticProps() { return { title: 'Built by Node' } }
    `,
    'app/page.metadata.ts': `
      export function metadata(props: { title: string }) {
        const image = 'https://example.com/social.png'
        return {
          lang: 'en', title: props.title, description: 'Static description',
          canonical: 'https://example.com/', robots: 'index, follow',
          openGraph: {
            type: 'website', title: props.title, description: 'Static description',
            url: 'https://example.com/', siteName: 'Example', locale: 'en_US',
            image: { url: image, width: 1200, height: 630, alt: 'Example card', type: 'image/png' },
          },
          twitter: {
            card: 'summary_large_image', title: props.title, description: 'Static description',
            site: '@example', imageAlt: 'Example card', images: [image],
          },
          icons: { icon: '/favicon-32x32.png', appleTouch: '/apple-touch-icon.png' },
          jsonLd: [{ '@context': 'https://schema.org', '@type': 'WebSite', name: props.title }],
        }
      }
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx', kind: 'static' }],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.output.staticBuild.routes, [
    {
      routeId: 'root',
      buildFile: 'app/page.build.ts',
      metadataFile: 'app/page.metadata.ts',
      layoutFiles: [],
      entries: [
        {
          params: {},
          props: { title: 'Built by Node' },
          metadata: {
            lang: 'en',
            title: 'Built by Node',
            description: 'Static description',
            canonical: 'https://example.com/',
            robots: 'index, follow',
            openGraph: {
              type: 'website',
              title: 'Built by Node',
              description: 'Static description',
              url: 'https://example.com/',
              siteName: 'Example',
              locale: 'en_US',
              image: {
                url: 'https://example.com/social.png',
                width: 1200,
                height: 630,
                alt: 'Example card',
                type: 'image/png',
              },
            },
            twitter: {
              card: 'summary_large_image',
              title: 'Built by Node',
              description: 'Static description',
              site: '@example',
              imageAlt: 'Example card',
              images: ['https://example.com/social.png'],
            },
            icons: {
              icon: '/favicon-32x32.png',
              appleTouch: '/apple-touch-icon.png',
            },
            jsonLd: [
              {
                '@context': 'https://schema.org',
                '@type': 'WebSite',
                name: 'Built by Node',
              },
            ],
          },
        },
      ],
    },
  ])
})

test('rejects non-HTTPS static social images', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `export default function Page() { return <h1>Home</h1> }`,
    'app/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({}) })
    `,
    'app/page.metadata.ts': `
      export function metadata() {
        const image = 'http://example.com/social.png'
        return {
          lang: 'en', title: 'Home', description: 'Home',
          canonical: 'https://example.com/', robots: 'index, follow',
          openGraph: {
            type: 'website', title: 'Home', description: 'Home',
            url: 'https://example.com/', image: { url: image, width: 1200, height: 630 },
          },
          twitter: {
            card: 'summary_large_image', title: 'Home', description: 'Home', images: [image],
          },
        }
      }
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx', kind: 'static' }],
  })
  assert.equal(result.ok, false)
  if (!result.ok) {
    assert.ok(result.diagnostics.some(
      (entry) => entry.code === 'GB1245' && entry.message.includes('absolute HTTPS URL'),
    ))
  }
})

test('generates and validates parameterized static route entries', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/articles/[slug]/page.tsx': `
      export default function Page(props: { slug: string; title: string }) { return <h1>{props.title}</h1> }
    `,
    'app/articles/[slug]/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({ slug: schema.string(), title: schema.string() }) })
    `,
    'app/articles/[slug]/page.build.ts': `
      export function generateStaticParams() { return [{ slug: 'first' }, { slug: 'second' }] }
      export function loadStaticProps(params: { slug: string }) {
        return { slug: params.slug, title: params.slug === 'first' ? 'First' : 'Second' }
      }
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [
      {
        routeId: 'articles_slug',
        entryFile: 'app/articles/[slug]/page.tsx',
        routePattern: '/articles/[slug]',
        kind: 'static',
      },
    ],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.output.staticBuild.routes[0]?.entries, [
    { params: { slug: 'first' }, props: { slug: 'first', title: 'First' } },
    { params: { slug: 'second' }, props: { slug: 'second', title: 'Second' } },
  ])
})

test('rejects schema-invalid and non-serializable static props', async (t) => {
  const schema = `
    import { definePage, schema } from '@go-beyond/schema'
    export const page = definePage({ props: schema.object({ count: schema.integer() }) })
  `
  const invalidRoot = await fixtureProject(t, {
    'app/page.tsx': `export default function Page(props: { count: number }) { return <p>{props.count}</p> }`,
    'app/page.schema.ts': schema,
    'app/page.build.ts': `export function loadStaticProps() { return { count: 'not-a-number' } }`,
  })
  const invalid = await compileProject({
    projectRoot: invalidRoot,
    routes: [{ routeId: 'invalid', entryFile: 'app/page.tsx', kind: 'static' }],
  })
  assert.equal(invalid.ok, false)
  if (!invalid.ok)
    assert.ok(invalid.diagnostics.some((entry) => entry.code === 'GB1242'))

  const exoticRoot = await fixtureProject(t, {
    'app/page.tsx': `export default function Page() { return <p>Date</p> }`,
    'app/page.schema.ts': schema,
    'app/page.build.ts': `export function loadStaticProps() { return { count: new Date() } }`,
  })
  const exotic = await compileProject({
    projectRoot: exoticRoot,
    routes: [{ routeId: 'exotic', entryFile: 'app/page.tsx', kind: 'static' }],
  })
  assert.equal(exotic.ok, false)
  if (!exotic.ok)
    assert.ok(exotic.diagnostics.some((entry) => entry.code === 'GB1243'))
})

test('emits an empty-props entry when a literal static route has no build file', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `export default function Page() { return <p>Static</p> }`,
    'app/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({}) })
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [{ routeId: 'root', entryFile: 'app/page.tsx', kind: 'static' }],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.output.staticBuild.routes[0]?.entries, [
    { params: {}, props: {} },
  ])
  assert.equal(result.output.staticBuild.routes[0]?.buildFile, undefined)
})

test('composes root and nested layouts and emits browser registry module order', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/layout.tsx': `export default function Root({ children }: { children: unknown }) { return <div id="root">{children}</div> }`,
    'app/products/layout.tsx': `export default function Products({ children }: { children: unknown }) { return <section id="products">{children}</section> }`,
    'app/products/[slug]/page.tsx': `export default function Page(props: { name: string }) { return <h1>{props.name}</h1> }`,
    'app/products/[slug]/page.schema.ts': `
      import { definePage, schema } from '@go-beyond/schema'
      export const page = definePage({ props: schema.object({ name: schema.string() }) })
    `,
  })
  const result = await compileProject({
    projectRoot,
    routes: [
      { routeId: 'products_slug', entryFile: 'app/products/[slug]/page.tsx' },
    ],
  })
  assert.equal(
    result.ok,
    true,
    !result.ok ? JSON.stringify(result.diagnostics, null, 2) : '',
  )
  if (!result.ok) return
  assert.deepEqual(result.output.routeModules, [
    {
      routeId: 'products_slug',
      entryFile: 'app/products/[slug]/page.tsx',
      layoutFiles: ['app/layout.tsx', 'app/products/layout.tsx'],
    },
  ])
  const root = result.output.plans[0]!.root
  assert.equal(root.kind, 'element')
  if (root.kind !== 'element') return
  assert.equal(root.tag, 'div')
  assert.equal(root.children?.[0]?.kind, 'element')
  const nested = root.children?.[0]
  if (nested?.kind === 'element') assert.equal(nested.tag, 'section')
})

test('keeps portable use-client components in the Go plan without a downgrade', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import { Badge } from '../components/badge.js'
      export default function Page() { return <main><Badge label="Portable" /></main> }
    `,
    'components/badge.tsx': `
      'use client'
      export function Badge({ label }: { label: string }) { return <strong>{label}</strong> }
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
  if (!result.ok) return
  assert.deepEqual(result.clientBoundaries, [])
  assert.equal(result.plan.root.kind, 'element')
})

test('downgrades unsupported rendering at the nearest use-client call site and reports it', async (t) => {
  const files = {
    'app/page.tsx': `
      import { Widget } from '../components/widget.js'
      export default function Page() { return <main><Widget /></main> }
    `,
    'components/widget.tsx': `
      'use client'
      export function Widget() {
        const width = window.innerWidth
        return <p>{width}</p>
      }
    `,
  }
  const projectRoot = await fixtureProject(t, files)
  const first = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  const second = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(first.ok, true, first.ok ? '' : JSON.stringify(first.diagnostics))
  assert.equal(second.ok, true, second.ok ? '' : JSON.stringify(second.diagnostics))
  if (!first.ok || !second.ok) return
  assert.deepEqual(first.clientBoundaries, second.clientBoundaries)
  assert.equal(first.clientBoundaries.length, 1)
  assert.deepEqual(
    {
      routeId: first.clientBoundaries[0]?.routeId,
      source: first.clientBoundaries[0]?.source,
      component: first.clientBoundaries[0]?.component,
      boundary: first.clientBoundaries[0]?.boundary,
      target: first.clientBoundaries[0]?.target,
    },
    {
      routeId: 'root',
      source: 'app/page.tsx',
      component: 'Widget',
      boundary: 'components/widget.tsx',
      target: 'callSite',
    },
  )
  assert.match(first.clientBoundaries[0]?.id ?? '', /^gbc_[0-9a-f]{20}$/)
  assert.match(first.clientBoundaries[0]?.reason ?? '', /GB1071/)
  assert.equal(first.plan.root.kind, 'element')
  if (first.plan.root.kind === 'element') {
    assert.deepEqual(first.plan.root.children, [{ kind: 'clientOnly' }])
  }
})

test('keeps unmarked unsupported code and client-boundary module errors fatal', async (t) => {
  const unmarkedRoot = await fixtureProject(t, {
    'app/page.tsx': `export default function Page() { return <p>{window.innerWidth}</p> }`,
  })
  const unmarked = await compileFile({ projectRoot: unmarkedRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(unmarked.ok, false)
  if (!unmarked.ok) assert.ok(unmarked.diagnostics.some((entry) => entry.code === 'GB1071'))

  const brokenRoot = await fixtureProject(t, {
    'app/page.tsx': `import Widget from '../components/widget.js'; export default function Page(){ return <Widget /> }`,
    'components/widget.tsx': `'use client'; import Missing from './missing.js'; export default function Widget(){ return <Missing /> }`,
  })
  const broken = await compileFile({ projectRoot: brokenRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(broken.ok, false)
  if (!broken.ok) assert.ok(broken.diagnostics.some((entry) => entry.code === 'GB1104'))

  const parseRoot = await fixtureProject(t, {
    'app/page.tsx': `import Widget from '../components/widget.js'; export default function Page(){ return <Widget /> }`,
    'components/widget.tsx': `'use client'; export default function Widget(){ return <p>broken</div> }`,
  })
  const parse = await compileFile({ projectRoot: parseRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(parse.ok, false)
  if (!parse.ok) assert.ok(parse.diagnostics.some((entry) => entry.code.startsWith('TS')))

  const typeRoot = await fixtureProject(t, {
    'app/page.tsx': `import Widget from '../components/widget.js'; export default function Page(){ return <Widget /> }`,
    'components/widget.tsx': `'use client'; export default function Widget(){ const label: string = 42; return <p>{window.innerWidth}{label}</p> }`,
  })
  const type = await compileFile({ projectRoot: typeRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(type.ok, false)
  if (!type.ok) assert.ok(type.diagnostics.some((entry) => entry.code === 'TS2322'))
})

test('type-checks every discovered TypeScript module with the shared project program', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import Widget from '../components/widget.js'
      const pageLabel: string = 42
      export default function Page() { return <Widget label={pageLabel} /> }
    `,
    'components/widget.tsx': `
      const widgetLabel: string = 42
      export default function Widget({ label }: { label: string }) {
        return <p>{label}{widgetLabel}</p>
      }
    `,
  })

  const result = await compileFile({
    projectRoot,
    entryFile: 'app/page.tsx',
    routeId: 'root',
  })
  assert.equal(result.ok, false)
  if (result.ok) return
  const typeErrorFiles = result.diagnostics
    .filter((entry) => entry.code === 'TS2322')
    .map((entry) => relative(projectRoot, entry.fileName))
  assert.deepEqual(typeErrorFiles, [
    'app/page.tsx',
    'components/widget.tsx',
  ])
})

test('resolves package exports and barrels to a package-authored use-client leaf', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `import { Card } from '@synthetic/react'; export default function Page(){ return <Card /> }`,
    'node_modules/@synthetic/react/package.json': JSON.stringify({
      name: '@synthetic/react',
      type: 'module',
      exports: { '.': './dist/index.mjs' },
    }),
    'node_modules/@synthetic/react/dist/index.mjs': `export * from '@synthetic/card'`,
    'node_modules/@synthetic/card/package.json': JSON.stringify({
      name: '@synthetic/card',
      type: 'module',
      exports: { '.': { import: './dist/index.mjs' } },
    }),
    'node_modules/@synthetic/card/dist/index.mjs': `
      'use client'
      export function Card() {
        const width = window.innerWidth
        return <div>{width}</div>
      }
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics, null, 2))
  if (!result.ok) return
  assert.equal(result.clientBoundaries.length, 1)
  assert.equal(result.clientBoundaries[0]?.boundary, 'node_modules/@synthetic/card/dist/index.mjs')
  assert.equal(result.clientBoundaries[0]?.component, 'Card')
})

test('follows imported export aliases before downgrading a client package leaf', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `import { Navbar } from '@synthetic/ui'; export default function Page(){ return <Navbar /> }`,
    'node_modules/@synthetic/ui/package.json': JSON.stringify({
      name: '@synthetic/ui',
      type: 'module',
      exports: { '.': './dist/index.mjs' },
    }),
    'node_modules/@synthetic/ui/dist/index.mjs': `export * from '@synthetic/navbar'`,
    'node_modules/@synthetic/navbar/package.json': JSON.stringify({
      name: '@synthetic/navbar',
      type: 'module',
      exports: { '.': './dist/index.mjs' },
    }),
    'node_modules/@synthetic/navbar/dist/index.mjs': `
      'use client'
      import { navbar_default } from './chunk.mjs'
      export { navbar_default as Navbar }
    `,
    'node_modules/@synthetic/navbar/dist/chunk.mjs': `
      export class navbar_default {
        render() { return null }
      }
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics, null, 2))
  if (!result.ok) return
  assert.equal(result.clientBoundaries.length, 1)
  assert.equal(result.clientBoundaries[0]?.component, 'Navbar')
  assert.equal(
    result.clientBoundaries[0]?.boundary,
    'node_modules/@synthetic/navbar/dist/index.mjs',
  )
  assert.match(result.clientBoundaries[0]?.reason ?? '', /GB1056/)
})

test('resolves export-star dependencies from a pnpm package realpath without traversing later stars', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `import { Navbar } from '@synthetic/ui'; export default function Page(){ return <Navbar /> }`,
    'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/ui/package.json': JSON.stringify({
      name: '@synthetic/ui',
      type: 'module',
      exports: './dist/index.mjs',
    }),
    'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/ui/dist/index.mjs': `
      export * from '@synthetic/navbar'
      export * from '@synthetic/invalid-after-navbar'
    `,
    'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/navbar/package.json': JSON.stringify({
      name: '@synthetic/navbar',
      type: 'module',
      exports: './dist/index.mjs',
    }),
    'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/navbar/dist/index.mjs': `
      'use client'
      import { navbar_default } from './chunk.mjs'
      export { navbar_default as Navbar }
    `,
    'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/navbar/dist/chunk.mjs': `
      export class navbar_default { render() { return null } }
    `,
    'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/invalid-after-navbar/package.json': '{broken',
  })
  const logicalPackage = resolve(projectRoot, 'node_modules/@synthetic/ui')
  await mkdir(resolve(logicalPackage, '..'), { recursive: true })
  await symlink(
    resolve(
      projectRoot,
      'node_modules/.pnpm/@synthetic+ui@1.0.0/node_modules/@synthetic/ui',
    ),
    logicalPackage,
    'dir',
  )

  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics, null, 2))
  if (!result.ok) return
  assert.equal(result.clientBoundaries.length, 1)
  assert.equal(result.clientBoundaries[0]?.component, 'Navbar')
  assert.match(
    result.clientBoundaries[0]?.boundary ?? '',
    /\.pnpm\/@synthetic\+ui@1\.0\.0\/node_modules\/@synthetic\/navbar\/dist\/index\.mjs$/,
  )
})

test('downgrades a viewport-stateful class package only at its outer use-client wrapper', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `import { Gallery } from '../components/gallery.js'; export default function Page(){ return <Gallery /> }`,
    'components/gallery.tsx': `
      'use client'
      import ViewportLayout from '@synthetic/viewport-layout'
      export function Gallery() { return <ViewportLayout><p>Image</p></ViewportLayout> }
    `,
    'node_modules/@synthetic/viewport-layout/package.json': JSON.stringify({
      name: '@synthetic/viewport-layout',
      type: 'module',
      exports: './index.js',
    }),
    'node_modules/@synthetic/viewport-layout/index.js': `
      export default class ViewportLayout {
        render() { return this.props.children }
      }
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics, null, 2))
  if (!result.ok) return
  assert.equal(result.clientBoundaries.length, 1)
  assert.equal(result.clientBoundaries[0]?.component, 'Gallery')
  assert.equal(result.clientBoundaries[0]?.boundary, 'components/gallery.tsx')
  assert.match(result.clientBoundaries[0]?.reason ?? '', /GB1056/)
})

// Models react-masonry-css: a class component whose constructor does more
// than super/bind/state (declaration + branching), whose defaultProps is a
// module identifier rather than an inline literal, and whose lifecycle
// methods touch window. Its declaration-shape diagnostics must be deferred
// to compile time so a use-client wrapper can downgrade them.
const masonryPackageFiles = {
  'node_modules/@synthetic/masonry/package.json': JSON.stringify({
    name: '@synthetic/masonry',
    type: 'module',
    module: 'dist/masonry.module.js',
    main: 'dist/masonry.cjs.js',
  }),
  'node_modules/@synthetic/masonry/dist/masonry.module.js': `
    import React from 'react'
    const defaultProps = {
      breakpointCols: undefined,
      className: undefined,
      columnClassName: undefined,
      children: undefined,
    }
    const DEFAULT_COLUMNS = 2
    class Masonry extends React.Component {
      constructor(props) {
        super(props)
        this.reCalculateColumnCount = this.reCalculateColumnCount.bind(this)
        let columnCount
        if (this.props.breakpointCols && this.props.breakpointCols.default) {
          columnCount = this.props.breakpointCols.default
        } else {
          columnCount = parseInt(this.props.breakpointCols) || DEFAULT_COLUMNS
        }
        this.state = { columnCount }
      }
      componentDidMount() {
        if (window) window.addEventListener('resize', this.reCalculateColumnCount)
      }
      componentWillUnmount() {
        if (window) window.removeEventListener('resize', this.reCalculateColumnCount)
      }
      reCalculateColumnCount() {
        this.setState({ columnCount: Math.max(1, this.state.columnCount) })
      }
      render() {
        const { className, columnClassName, children } = this.props
        return React.createElement(
          'div',
          { className },
          React.createElement('div', { className: columnClassName }, children),
        )
      }
    }
    Masonry.defaultProps = defaultProps
    export default Masonry
  `,
}

test('downgrades a use-client gallery that renders a constructor/defaultProps class package', async (t) => {
  const projectRoot = await fixtureProject(t, {
    ...masonryPackageFiles,
    'app/page.tsx': `
      import MasonryGallery from '../components/masonry-gallery.js'
      export default function Page() {
        return <main><h1>Work</h1><MasonryGallery /></main>
      }
    `,
    'components/masonry-gallery.tsx': `
      'use client'
      import Masonry from '@synthetic/masonry'
      export default function MasonryGallery() {
        return (
          <Masonry breakpointCols={{ default: 4 }} className="flex" columnClassName="flex flex-col">
            <img src="/a.jpg" alt="" />
          </Masonry>
        )
      }
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics, null, 2))
  if (!result.ok) return
  assert.equal(result.clientBoundaries.length, 1)
  assert.equal(result.clientBoundaries[0]?.component, 'MasonryGallery')
  assert.equal(result.clientBoundaries[0]?.boundary, 'components/masonry-gallery.tsx')
  assert.match(result.clientBoundaries[0]?.reason ?? '', /GB10\d\d/)
  // The portable sibling markup must survive: only the gallery downgrades.
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind === 'element') {
    assert.deepEqual(result.plan.root.children, [
      {
        kind: 'element',
        tag: 'h1',
        namespace: 'html',
        attributes: [],
        children: [{ kind: 'text', value: { kind: 'literal', value: 'Work' } }],
      },
      { kind: 'clientOnly' },
    ])
  }
})

test('keeps the constructor/defaultProps class package fatal without a client boundary', async (t) => {
  const projectRoot = await fixtureProject(t, {
    ...masonryPackageFiles,
    'app/page.tsx': `
      import Masonry from '@synthetic/masonry'
      export default function Page() {
        return <Masonry className="flex"><img src="/a.jpg" alt="" /></Masonry>
      }
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, false)
  if (result.ok) return
  assert.ok(
    result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1099'),
    JSON.stringify(result.diagnostics, null, 2),
  )
  assert.ok(
    result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1018'),
    JSON.stringify(result.diagnostics, null, 2),
  )
})

test('normalizes Heroicon-style compiled JavaScript components into portable markup', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `
      import { SparkIcon } from '@synthetic/icons/24/outline'
      export default function Page() { return <SparkIcon className="size-6" title="Spark" /> }
    `,
    'node_modules/@synthetic/icons/package.json': JSON.stringify({
      name: '@synthetic/icons',
      type: 'module',
      exports: { './24/outline': './dist/outline/index.js' },
    }),
    'node_modules/@synthetic/icons/dist/outline/index.js': `export { default as SparkIcon } from './SparkIcon.js'`,
    'node_modules/@synthetic/icons/dist/outline/SparkIcon.js': `
      import React, { forwardRef, memo } from 'react'
      const SparkIcon = memo(forwardRef(function SparkIcon({ title, titleId, ...rest }, ref) {
        return React.createElement(
          'svg',
          Object.assign({ viewBox: '0 0 24 24', fill: 'none', ref: ref }, rest),
          title ? React.createElement('title', { id: titleId }, title) : null,
          React.createElement('path', { d: 'M12 2v20' }),
        )
      }))
      export default SparkIcon
    `,
  })
  const result = await compileFile({ projectRoot, entryFile: 'app/page.tsx', routeId: 'root' })
  assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics, null, 2))
  if (!result.ok) return
  assert.deepEqual(result.clientBoundaries, [])
  assert.equal(result.plan.root.kind, 'element')
  if (result.plan.root.kind !== 'element') return
  assert.equal(result.plan.root.tag, 'svg')
  assert.deepEqual(
    result.plan.root.attributes?.map((attribute) => attribute.name),
    ['viewBox', 'fill', 'className'],
  )
  assert.equal(result.plan.root.children?.[0]?.kind, 'conditional')
  assert.equal(result.plan.root.children?.[1]?.kind, 'element')
})

test('ClientOnly accepts an omitted or explicitly null fallback', () => {
  for (const sourceText of [
    `export default function Page(){ return <ClientOnly><canvas /></ClientOnly> }`,
    `export default function Page(){ return <ClientOnly fallback={null}><canvas /></ClientOnly> }`,
  ]) {
    const result = compileSource({ routeId: 'root', sourceText })
    assert.equal(result.ok, true, result.ok ? '' : JSON.stringify(result.diagnostics))
    if (result.ok) assert.equal(result.plan.root.kind, 'clientOnly')
  }
})

async function fixtureProject(
  t: test.TestContext,
  files: Record<string, string>,
): Promise<string> {
  const projectRoot = await mkdtemp(resolve(tmpdir(), 'gobeyond-compiler-'))
  t.after(async () => rm(projectRoot, { recursive: true, force: true }))
  for (const [fileName, sourceText] of Object.entries(files)) {
    const destination = resolve(projectRoot, fileName)
    await mkdir(resolve(destination, '..'), { recursive: true })
    await writeFile(destination, sourceText)
  }
  return projectRoot
}
