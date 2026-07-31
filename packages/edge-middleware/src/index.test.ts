import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { createPassThroughMiddleware } from './index.js'

describe('edge-middleware stub', () => {
  it('exports a fetch handler', () => {
    const mw = createPassThroughMiddleware()
    assert.equal(typeof mw.fetch, 'function')
  })
})
