import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
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
      export default function Page(props: { value: string }) {
        const formatted = useMemo(() => props.value.toUpperCase(), [props.value])
        return <h1>{formatted}</h1>
      }
    `,
  })
  assert.equal(result.ok, false)
  if (result.ok) return
  assert.ok(
    result.diagnostics.some((diagnostic) => diagnostic.code === 'GB1076'),
  )
  assert.ok(result.diagnostics.every((diagnostic) => diagnostic.line > 0))
  assert.match(
    result.diagnostics[0]!.suggestion ?? '',
    /Calculate the initial value in Go/,
  )
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
          openGraph: { type: 'website', title: props.title, description: 'Static description', url: 'https://example.com/', images: [image] },
          twitter: { card: 'summary_large_image', title: props.title, description: 'Static description', images: [image] },
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
              images: ['https://example.com/social.png'],
            },
            twitter: {
              card: 'summary_large_image',
              title: 'Built by Node',
              description: 'Static description',
              images: ['https://example.com/social.png'],
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

test('downgrades a valid class package component only at its outer use-client wrapper', async (t) => {
  const projectRoot = await fixtureProject(t, {
    'app/page.tsx': `import { Gallery } from '../components/gallery.js'; export default function Page(){ return <Gallery /> }`,
    'components/gallery.tsx': `
      'use client'
      import Masonry from 'react-masonry-css'
      export function Gallery() { return <Masonry><p>Image</p></Masonry> }
    `,
    'node_modules/react-masonry-css/package.json': JSON.stringify({
      name: 'react-masonry-css',
      type: 'module',
      exports: './index.js',
    }),
    'node_modules/react-masonry-css/index.js': `
      export default class Masonry {
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
