import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import test from 'node:test'

import { compileFile } from '@gobeyond/compiler'
import {
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
    /__gbCreateElement\(__gbClientOnly, null, <Widget \/>\)/,
  )
  assert.match(result.code, /<Portable \/>/)
  assert.doesNotMatch(
    result.code,
    /__gbCreateElement\(__gbClientOnly, null, <Portable/,
  )
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
  return root
}
