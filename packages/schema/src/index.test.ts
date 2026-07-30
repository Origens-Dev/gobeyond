import assert from 'node:assert/strict'
import test from 'node:test'

import { createHTMLSanitizer, defineAction, defineManifest, definePage, defineRobots, defineSitemap, schema } from './index.js'

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

test('page schemas omit route caching when it is not declared', () => {
  const page = definePage({ props: schema.object({ title: schema.string() }) })

  assert.equal('revalidate' in page, false)
  assert.equal('tags' in page, false)
})

test('page schemas carry the origin revalidate window and its tags', () => {
  const page = definePage({
    props: schema.object({ title: schema.string() }),
    revalidate: 60,
    tags: ['products', 'product'],
  })

  assert.equal(page.revalidate, 60)
  assert.deepEqual(page.tags, ['products', 'product'])
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

test('crawler helpers preserve MetadataRoute-shaped values', () => {
  const robots = defineRobots({
    rules: { userAgent: '*', allow: '/', disallow: '/private' },
    sitemap: 'https://example.com/sitemap.xml',
  })
  assert.equal((robots.rules as { userAgent?: string }).userAgent, '*')
  const sitemap = defineSitemap([{ url: 'https://example.com/' }])
  assert.equal(sitemap[0]?.url, 'https://example.com/')
  const manifest = defineManifest({ name: 'Example', start_url: '/' })
  assert.equal(manifest.name, 'Example')
})
