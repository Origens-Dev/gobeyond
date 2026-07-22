import assert from 'node:assert/strict'
import test from 'node:test'

import { createHTMLSanitizer, defineAction, definePage, schema } from './index.js'

test('page schemas retain a machine-readable shape', () => {
  const page = definePage({
    props: schema.object({
      title: schema.string(),
      tags: schema.array(schema.string()),
      summary: schema.optional(schema.string()),
    }),
  })

  assert.equal(page.kind, 'page')
  assert.equal(page.props.kind, 'object')
  assert.equal((page.props.shape as Record<string, { kind: string }>).title.kind, 'string')
})

test('action schemas expose input and output contracts', () => {
  const action = defineAction({
    input: schema.object({ id: schema.string() }),
    output: schema.object({ saved: schema.boolean() }),
  })

  assert.equal(action.kind, 'action')
  assert.equal(action.input.kind, 'object')
  assert.equal(action.output.kind, 'object')
})

test('safe HTML is produced through an application sanitizer', () => {
  const sanitize = createHTMLSanitizer((input) => input.replaceAll('<script>', ''))
  const value = sanitize('<script>safe body')
  assert.equal(value, 'safe body')
  assert.equal(schema.safeHTML().kind, 'safeHtml')
})
