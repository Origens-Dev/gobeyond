import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import test from 'node:test'
import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { JSDOM } from 'jsdom'

import { compileSource } from '@gobeyond/compiler'

test('portable equality excludes JavaScript reference-identity values', () => {
  const value = { id: 'same' }
  assert.equal(value === { id: 'same' }, false)
  assert.deepEqual(value, { id: 'same' })

  const compiled = compileSource({
    routeId: 'object-reference-equality',
    sourceText: `export default function Page(props: { value: { id: string } }) {
      return props.value === { id: 'same' } ? <p>same</p> : <p>different</p>
    }`,
  })
  assert.equal(compiled.ok, false)
  if (!compiled.ok) {
    assert.ok(
      compiled.diagnostics.some((diagnostic) => diagnostic.code === 'GB1082'),
    )
  }
})

test('portable case conversion cannot silently use Go Unicode mappings', () => {
  assert.equal('ß'.toUpperCase(), 'SS')

  const compiled = compileSource({
    routeId: 'unicode-case-conversion',
    sourceText: `export default function Page(props: { value: string }) {
      return <p>{upper(props.value)}</p>
    }`,
  })
  assert.equal(compiled.ok, false)
  if (!compiled.ok) {
    assert.ok(
      compiled.diagnostics.some((diagnostic) => diagnostic.code === 'GB1083'),
    )
  }
})

test('dynamic script text is rejected before it can disagree during hydration', () => {
  const compiled = compileSource({
    routeId: 'dynamic-script-text',
    sourceText: `export default function Page(props: { json: string }) {
      return <script type="application/json">{props.json}</script>
    }`,
  })
  assert.equal(compiled.ok, false)
  if (!compiled.ok) {
    const diagnostic = compiled.diagnostics.find(
      (candidate) => candidate.code === 'GB1037',
    )
    assert.ok(diagnostic)
    assert.match(diagnostic.suggestion ?? '', /typed metadata JSON-LD API/)
  }
})

test('static raw-text and RCDATA children are rejected conservatively', () => {
  for (const [tag, body] of [
    ['iframe', 'a&amp;b'],
    ['noembed', 'a&amp;b'],
    ['noframes', 'a&amp;b'],
    ['noscript', 'a&amp;b'],
    ['plaintext', 'a&amp;b'],
    ['script', 'window.label = "a&amp;b";'],
    ['style', '.label::before { content: "a&amp;b"; }'],
    ['textarea', 'a&amp;b'],
    ['title', 'A &amp; B'],
    ['xmp', 'a&amp;b'],
  ]) {
    const compiled = compileSource({
      routeId: `static-${tag}`,
      sourceText: `export default function Page() { return <${tag}>${body}</${tag}> }`,
    })
    assert.equal(compiled.ok, false, tag)
    if (!compiled.ok) {
      assert.ok(
        compiled.diagnostics.some((diagnostic) => diagnostic.code === 'GB1037'),
        tag,
      )
    }
  }
})

test('textarea value uses the React-compatible parser-newline path', async () => {
  const compiled = compileSource({
    routeId: 'textarea-value',
    sourceText: `export default function Page(props: { value: string }) {
      return <textarea readOnly value={props.value} />
    }`,
  })
  assert.equal(
    compiled.ok,
    true,
    compiled.ok ? '' : JSON.stringify(compiled.diagnostics),
  )
  if (!compiled.ok) return

  const value = '\n<&'
  const goMarkup = await renderWithGo(compiled.plan, { value })
  const reactMarkup = renderToString(
    createElement('textarea', { readOnly: true, value }),
  )
  const goDOM = new JSDOM(`<body>${goMarkup}</body>`)
  const reactDOM = new JSDOM(`<body>${reactMarkup}</body>`)
  try {
    const goTextarea = goDOM.window.document.querySelector('textarea')
    const reactTextarea = reactDOM.window.document.querySelector('textarea')
    assert.ok(goTextarea)
    assert.ok(reactTextarea)
    assert.equal(goTextarea.outerHTML, reactTextarea.outerHTML)
    assert.equal(goTextarea.value, value)
  } finally {
    goDOM.window.close()
    reactDOM.window.close()
  }
})

function renderWithGo(plan, props) {
  return new Promise((resolve, reject) => {
    const process = spawn('go', ['run', '../../cmd/render-fixture'], {
      cwd: new URL('..', import.meta.url).pathname,
      stdio: ['pipe', 'pipe', 'pipe'],
    })
    let stdout = ''
    let stderr = ''
    process.stdout.setEncoding('utf8').on('data', (chunk) => (stdout += chunk))
    process.stderr.setEncoding('utf8').on('data', (chunk) => (stderr += chunk))
    process.on('error', reject)
    process.on('close', (code) => {
      if (code === 0) resolve(stdout)
      else reject(new Error(`Go renderer failed (${code}): ${stderr}`))
    })
    process.stdin.end(JSON.stringify({ plan, props }))
  })
}
