import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  ENV_TOKEN_ORIGENS,
  HEADER_ORIGENS,
  getGoBeyondOidcToken,
  getGoBeyondOidcTokenSync,
  withGoBeyondOidcRequest,
} from './index.js'

test('request token wins over environment token', async () => {
  process.env[ENV_TOKEN_ORIGENS] = 'environment-token'
  const request = new Request('https://example.test', { headers: { [HEADER_ORIGENS]: 'request-token' } })
  assert.equal(await getGoBeyondOidcToken({ request }), 'request-token')
  assert.equal(getGoBeyondOidcTokenSync({ request }), 'request-token')
  delete process.env[ENV_TOKEN_ORIGENS]
})

test('request context supplies the source token', async () => {
  const request = new Request('https://example.test', { headers: { [HEADER_ORIGENS]: 'context-token' } })
  const token = await withGoBeyondOidcRequest(request, () => getGoBeyondOidcToken())
  assert.equal(token, 'context-token')
})

test('sync access fails when only the hosted broker is available', () => {
  delete process.env[ENV_TOKEN_ORIGENS]
  assert.throws(() => getGoBeyondOidcTokenSync(), /synchronous token access/)
})
