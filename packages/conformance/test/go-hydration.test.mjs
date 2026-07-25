import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import test from 'node:test'
import { createElement, useState, Component } from 'react'
import { act } from 'react'
import { JSDOM } from 'jsdom'

import { compileSource } from '@go-beyond/compiler'
import { ClientOnly } from '@go-beyond/react'
import { bootstrap, BROWSER_PROTOCOL_VERSION } from '@go-beyond/react/browser'

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

test('empty Go client boundary hydrates before mounting browser content', async () => {
  const compiled = compileSource({
    routeId: 'client-boundary',
    sourceText: `export default function Page() {
      return <ClientOnly><p>{window.innerWidth}</p></ClientOnly>
    }`,
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  if (!compiled.ok) return
  const markup = await renderWithGo(compiled.plan, {})
  assert.equal(markup, '')

  function ClientPage() {
    return createElement(
      ClientOnly,
      null,
      createElement('p', null, 'Browser content'),
    )
  }
  const payload = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: 'client-boundary-build',
    routeId: 'client-boundary',
    props: {},
  })
  const dom = new JSDOM(
    `<!doctype html><body><div id="__gobeyond">${markup}</div>` +
      `<script id="__GOBEYOND_DATA__" type="application/json">${payload}</script></body>`,
    { url: 'https://example.com/' },
  )
  const restore = installDOM(dom)
  const recoverable = []
  try {
    let result
    await act(async () => {
      result = bootstrap({
        routes: { 'client-boundary': ClientPage },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      })
    })
    assert.equal(dom.window.document.querySelector('p')?.textContent, 'Browser content')
    assert.deepEqual(recoverable, [])
    await act(async () => result.root.unmount())
  } finally {
    restore()
    dom.window.close()
  }
})

test('dynamic index missing and out-of-range resolve like React undefined', async () => {
  const compiled = compileSource({
    routeId: 'index-expr',
    sourceText: `
      export default function Page(props) {
        const current = props.items[props.index]
        const missing = props.items[props.missing]
        return <main>
          <p id="current">{current.name}</p>
          <p id="missing">{missing}</p>
        </main>
      }
    `,
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  const props = {
    items: [{ name: 'one' }, { name: 'two' }],
    index: 1,
    missing: 99,
  }
  const markup = await renderWithGo(compiled.plan, props)
  assert.match(markup, />two</)
  assert.match(markup, /id="missing"><\/p>/)

  function IndexPage(pageProps) {
    const current = pageProps.items[pageProps.index]
    const missing = pageProps.items[pageProps.missing]
    return createElement(
      'main',
      null,
      createElement('p', { id: 'current' }, current?.name),
      createElement('p', { id: 'missing' }, missing),
    )
  }
  const payload = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: 'index-build',
    routeId: 'index-expr',
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
        routes: { 'index-expr': IndexPage },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      })
    })
    assert.equal(dom.window.document.querySelector('#__gobeyond').innerHTML, before)
    assert.deepEqual(recoverable, [])
    await act(async () => result.root.unmount())
  } finally {
    restore()
    dom.window.close()
  }
})

test('array and string length drive dynamic first-paint indexing', async () => {
  const compiled = compileSource({
    routeId: 'length-index',
    sourceText: `
      export default function Page(props) {
        const current = props.items[
          (props.index + props.items.length) % props.items.length
        ]
        return <p>{current.name}: {props.label.length}</p>
      }
    `,
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  const props = {
    items: [{ name: 'one' }, { name: 'two' }],
    index: -1,
    label: 'A😀',
  }
  const markup = await renderWithGo(compiled.plan, props)
  assert.match(markup, /two<!-- -->: <!-- -->3/)

  function LengthIndexPage(pageProps) {
    const current = pageProps.items[
      (pageProps.index + pageProps.items.length) % pageProps.items.length
    ]
    return createElement('p', null, current.name, ': ', pageProps.label.length)
  }
  const payload = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: 'length-index-build',
    routeId: 'length-index',
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
        routes: { 'length-index': LengthIndexPage },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      })
    })
    assert.equal(dom.window.document.querySelector('#__gobeyond').innerHTML, before)
    assert.deepEqual(recoverable, [])
    await act(async () => result.root.unmount())
  } finally {
    restore()
    dom.window.close()
  }
})

test('class first-paint hydrates then allows post-mount setState', async () => {
  const compiled = compileSource({
    routeId: 'class-paint',
    sourceText: `
      // @ts-nocheck
      import { Component } from 'react'
      export default class Counter extends Component {
        state = { count: 1 }
        componentDidMount() {
          this.setState({ count: 2 })
        }
        render() {
          return <span id="count">{this.state.count}</span>
        }
      }
    `,
  })
  assert.equal(compiled.ok, true, compiled.ok ? '' : JSON.stringify(compiled.diagnostics))
  const markup = await renderWithGo(compiled.plan, {})
  assert.match(markup, />1</)

  class Counter extends Component {
    state = { count: 1 }
    componentDidMount() {
      this.setState({ count: 2 })
    }
    render() {
      return createElement('span', { id: 'count' }, this.state.count)
    }
  }
  const payload = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: 'class-build',
    routeId: 'class-paint',
    props: {},
  })
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
        routes: { 'class-paint': Counter },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      })
    })
    // First paint matches Go (count=1); didMount then updates without hydration errors.
    assert.equal(before.includes('>1<') || before.includes('>1</'), true)
    assert.deepEqual(recoverable, [])
    assert.equal(dom.window.document.querySelector('#count')?.textContent, '2')
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
