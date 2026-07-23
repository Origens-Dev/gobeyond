import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import test from 'node:test'
import { build } from 'vite'

import { compileFile } from '@gobeyond/compiler'
import {
  goBeyond,
  loadClientBoundaryManifest,
  transformClientBoundaries,
} from '../src/index.js'

test('transforms only compiler-downgraded JSX call sites', async (t) => {
  const root = await fixture(t, {
    'app/page.tsx': `
      import { Widget } from '../components/widget.js'
      function Portable() { return <p>portable</p> }
      export default function Page() { return <main><Widget /><Portable /></main> }
    `,
    'components/widget.tsx': `
      'use client'
      export function Widget() {
        const width = window.innerWidth
        return <p>{width}</p>
      }
    `,
  })
  const code = await readFile(resolve(root, 'app/page.tsx'), 'utf8')
  const compiled = await compileFile({
    projectRoot: root,
    entryFile: 'app/page.tsx',
    routeId: 'root',
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  if (!compiled.ok) return
  const result = transformClientBoundaries(
    code,
    `${resolve(root, 'app/page.tsx')}?v=1`,
    compiled.clientBoundaries,
    root,
  )
  assert.ok(result)
  assert.match(
    result.code,
    /<__gbClientOnly>{<Widget \/>}<\/__gbClientOnly>/,
  )
  assert.doesNotMatch(result.code, /createElement as __gbCreateElement/)
  assert.match(result.code, /<Portable \/>/)
  assert.doesNotMatch(
    result.code,
    /__gbCreateElement\(__gbClientOnly, null, <Portable/,
  )
})

test('keeps normalized JavaScript call-site replacements in expression syntax', () => {
  const root = resolve('/project')
  const code = `export function Page() { return jsx(Widget, {}) }`
  const start = code.indexOf('jsx(Widget')
  const result = transformClientBoundaries(
    code,
    resolve(root, 'app/page.js'),
    [{
      id: 'gbc_normalized',
      routeId: 'root',
      source: 'app/page.js',
      component: 'Widget',
      boundary: 'components/widget.js',
      reason: 'GB1056: unsupported class component',
      target: 'callSite',
      start,
      end: start + 'jsx(Widget, {})'.length,
      line: 1,
      column: start + 1,
    }],
    root,
  )
  assert.ok(result)
  assert.match(result.code, /import { createElement as __gbCreateElement } from 'react'/)
  assert.match(
    result.code,
    /return __gbCreateElement\(__gbClientOnly, null, jsx\(Widget, {}\)\)/,
  )
})

test('bundles a direct JSX child as an executable empty-first-render boundary', async (t) => {
  const root = await fixture(t, {
    'app.tsx': `
      import { renderElement } from './jsx-runtime.js'
      import { Widget } from './widget.js'

      function Portable() { return <p>portable</p> }
      export default function Page() { return <main><Widget /><Portable /></main> }

      export function renderInitial() {
        return renderElement(<Page />)
      }
    `,
    'widget.tsx': `
      'use client'
      export function Widget() {
        globalThis.__gbWidgetRenders = (globalThis.__gbWidgetRenders ?? 0) + 1
        const width = window.innerWidth
        return <nav>{width}</nav>
      }
    `,
    'jsx-runtime.js': `
      export const Fragment = Symbol('Fragment')
      export function jsx(type, props) { return { type, props: props ?? {} } }
      export const jsxs = jsx
      export function renderElement(value) {
        if (value == null || value === false || value === true) return ''
        if (Array.isArray(value)) return value.map(renderElement).join('')
        if (typeof value !== 'object') return String(value)
        if (typeof value.type === 'function') return renderElement(value.type(value.props))
        const children = renderElement(value.props.children)
        return '<' + value.type + '>' + children + '</' + value.type + '>'
      }
    `,
    'gobeyond-react.js': `
      export function ClientOnly() { return null }
    `,
  })
  const compiled = await compileFile({
    projectRoot: root,
    entryFile: 'app.tsx',
    routeId: 'root',
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  if (!compiled.ok) return

  await build({
    root,
    configFile: false,
    logLevel: 'silent',
    plugins: [goBeyond({
      clientBoundaries: {
        apiVersion: 'gobeyond.client-boundaries/v1alpha1',
        boundaries: compiled.clientBoundaries,
      },
    })],
    resolve: {
      alias: {
        '@gobeyond/react': resolve(root, 'gobeyond-react.js'),
        'react/jsx-runtime': resolve(root, 'jsx-runtime.js'),
      },
    },
    build: {
      outDir: 'dist',
      lib: {
        entry: resolve(root, 'app.tsx'),
        formats: ['es'],
        fileName: () => 'app.js',
      },
      minify: false,
    },
  })

  const bundled = await readFile(resolve(root, 'dist/app.js'), 'utf8')
  assert.match(bundled, /ClientOnly/)
  assert.doesNotMatch(bundled, /__gbCreateElement\(__gbClientOnly/)
  assert.doesNotMatch(bundled, /"__gbCreateElement\(__gbClientOnly/)
  Reflect.deleteProperty(globalThis, '__gbWidgetRenders')
  const module = await import(`${pathToFileURL(resolve(root, 'dist/app.js')).href}?test=${Date.now()}`) as {
    renderInitial(): string
  }
  assert.equal(module.renderInitial(), '<main><p>portable</p></main>')
  assert.equal(Reflect.get(globalThis, '__gbWidgetRenders'), undefined)
})

test('wraps a downgraded use-client route root after its directive', async (t) => {
  const root = await fixture(t, {
    'app/page.tsx': `
      'use client'
      export default function Page() {
        const width = window.innerWidth
        return <p>{width}</p>
      }
    `,
  })
  const code = await readFile(resolve(root, 'app/page.tsx'), 'utf8')
  const compiled = await compileFile({
    projectRoot: root,
    entryFile: 'app/page.tsx',
    routeId: 'root',
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  if (!compiled.ok) return
  assert.equal(compiled.clientBoundaries[0]?.target, 'component')
  const result = transformClientBoundaries(
    code,
    resolve(root, 'app/page.tsx'),
    compiled.clientBoundaries,
    root,
  )
  assert.ok(result)
  assert.ok(result.code.indexOf(`'use client'`) < result.code.indexOf('import { deferClientRender'))
  assert.match(result.code, /function Page\(\)/)
  assert.match(result.code, /export default __gbDeferClientRender\(Page\)/)
  assert.doesNotMatch(result.code, /export default function Page/)
})

test('loads a boundary manifest from complete compiler output and rejects stale spans', async (t) => {
  const root = await fixture(t, {
    'boundaries.json': JSON.stringify({
      apiVersion: 'gobeyond.compiler-project/v1alpha1',
      clientBoundaries: {
        apiVersion: 'gobeyond.client-boundaries/v1alpha1',
        boundaries: [],
      },
    }),
  })
  const manifest = await loadClientBoundaryManifest('boundaries.json', root)
  assert.deepEqual(manifest.boundaries, [])

  assert.throws(
    () => transformClientBoundaries(
      `export default function Page(){ return <main /> }`,
      resolve(root, 'app/page.tsx'),
      [{
        id: 'gbc_stale',
        routeId: 'root',
        source: 'app/page.tsx',
        component: 'Widget',
        boundary: 'components/widget.tsx',
        reason: 'GB1077: unsupported',
        target: 'callSite',
        start: 1,
        end: 2,
        line: 1,
        column: 1,
      }],
      root,
    ),
    /Stale GoBeyond client boundary/,
  )
})

async function fixture(
  t: test.TestContext,
  files: Record<string, string>,
): Promise<string> {
  const root = await mkdtemp(resolve(tmpdir(), 'gobeyond-vite-'))
  t.after(async () => rm(root, { recursive: true, force: true }))
  for (const [name, content] of Object.entries(files)) {
    const destination = resolve(root, name)
    await mkdir(resolve(destination, '..'), { recursive: true })
    await writeFile(destination, content)
  }
  return realpath(root)
}
