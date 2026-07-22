import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import test from 'node:test'
import { createElement, useState } from 'react'
import { act } from 'react'
import { JSDOM } from 'jsdom'

import { compileSource } from '@gobeyond/compiler'
import { bootstrap, BROWSER_PROTOCOL_VERSION } from '@gobeyond/react/browser'

const source = `
  import { useEffect, useState } from 'react'
  export default function Page(props) {
    const [count, setCount] = useState(props.initial)
    useEffect(() => {}, [])
    return <main>
      <h1>{props.title}</h1>
      {props.show ? <p>{props.description}</p> : null}
      <ul>{props.items.map((item) => <li key={item.id}>{item.label}</li>)}</ul>
      <button type="button" onClick={() => setCount(count + 1)}>Count: {count}</button>
    </main>
  }
`

function Page(props) {
  const [count, setCount] = useState(props.initial)
  return createElement(
    'main',
    null,
    createElement('h1', null, props.title),
    props.show ? createElement('p', null, props.description) : null,
    createElement(
      'ul',
      null,
      props.items.map((item) => createElement('li', { key: item.id }, item.label)),
    ),
    createElement(
      'button',
      { type: 'button', onClick: () => setCount(count + 1) },
      `Count: ${count}`,
    ),
  )
}

test('Go output hydrates with pinned React and preserves interaction', async () => {
  const compiled = compileSource({ sourceText: source, fileName: 'page.tsx', routeId: 'conformance' })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  const props = {
    title: 'Go <beyond>',
    description: 'Crawler-visible content',
    show: true,
    initial: 2,
    items: [
      { id: 'a', label: 'Article' },
      { id: 'b', label: 'Product' },
    ],
  }
  const markup = await renderWithGo(compiled.plan, props)
  assert.match(markup, /Go &lt;beyond&gt;/)
  assert.match(markup, /Crawler-visible content/)

  const payload = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: 'conformance-build',
    routeId: 'conformance',
    props,
  }).replaceAll('<', '\\u003c')
  const dom = new JSDOM(
    `<!doctype html><body><div id="__gobeyond">${markup}</div>` +
      `<script id="__GOBEYOND_DATA__" type="application/json">${payload}</script></body>`,
    { url: 'https://example.com/' },
  )
  const restore = installDOM(dom)
  const recoverable = []
  try {
    let result
    const before = dom.window.document.querySelector('#__gobeyond').innerHTML
    await act(async () => {
      result = bootstrap({
        routes: { conformance: Page },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      })
    })
    const immediatelyAfter = dom.window.document.querySelector('#__gobeyond').innerHTML
    assert.equal(immediatelyAfter, before)
    assert.deepEqual(recoverable, [])
    const button = dom.window.document.querySelector('button')
    await act(async () => button.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true })))
    assert.equal(button.textContent, 'Count: 3')
    await act(async () => result.root.unmount())
  } finally {
    restore()
    dom.window.close()
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

function installDOM(dom) {
  const previous = {
    window: globalThis.window,
    document: globalThis.document,
    HTMLElement: globalThis.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: globalThis.IS_REACT_ACT_ENVIRONMENT,
  }
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    HTMLElement: dom.window.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: true,
  })
  return () => Object.assign(globalThis, previous)
}
