import { AsyncLocalStorage } from 'node:async_hooks'
import { request as httpRequest } from 'node:http'

export const ENV_TOKEN_ORIGENS = 'ORIGENS_OIDC_TOKEN'
export const ENV_TOKEN_GOBEYOND = 'GOBEYOND_OIDC_TOKEN'
export const ENV_ISSUER_BASE = 'GOBEYOND_OIDC_ISSUER_BASE_URL'
export const ENV_HOST_REPORT_SOCKET = 'GOBEYOND_HOST_REPORT_SOCKET'
export const HEADER_ORIGENS = 'x-origens-oidc-token'
export const HEADER_GOBEYOND = 'x-gobeyond-oidc-token'
export const DEFAULT_SOURCE_AUDIENCE = 'origens-platform'
export const AWS_STS_AUDIENCE = 'sts.amazonaws.com'

export type OidcTokenOptions = {
  request?: Request
  audience?: string
  jti?: string
}

type BrokerResponse = {
  token?: string
  expires_at?: number
}

const requestStorage = new AsyncLocalStorage<Request>()

function requestToken(request?: Request): string {
  const headers = request?.headers
  if (!headers) return ''
  return headers.get(HEADER_ORIGENS)?.trim() || headers.get(HEADER_GOBEYOND)?.trim() || ''
}

function environmentToken(): string {
  return process.env[ENV_TOKEN_ORIGENS]?.trim() || process.env[ENV_TOKEN_GOBEYOND]?.trim() || ''
}

function issuerBase(): string {
  return process.env[ENV_ISSUER_BASE]?.trim().replace(/\/$/, '') || ''
}

async function brokerToken(): Promise<string> {
  const socketPath = process.env[ENV_HOST_REPORT_SOCKET]?.trim()
  if (!socketPath) {
    throw new Error(
      '@go-beyond/oidc: no request token, environment token, or hosted slot broker is configured',
    )
  }

  const body = await new Promise<string>((resolve, reject) => {
    const request = httpRequest(
      {
        method: 'POST',
        socketPath,
        path: '/v1/oidc/token',
        headers: { 'content-type': 'application/json' },
        timeout: 5000,
      },
      response => {
        let raw = ''
        response.setEncoding('utf8')
        response.on('data', chunk => { raw += chunk })
        response.on('end', () => {
          if (response.statusCode !== 200) {
            reject(new Error(`@go-beyond/oidc: hosted slot broker returned ${response.statusCode}`))
            return
          }
          resolve(raw)
        })
      },
    )
    request.on('timeout', () => request.destroy(new Error('hosted slot broker timed out')))
    request.on('error', reject)
    request.end('{}')
  })

  let result: BrokerResponse
  try {
    result = JSON.parse(body) as BrokerResponse
  } catch {
    throw new Error('@go-beyond/oidc: hosted slot broker returned invalid JSON')
  }
  if (!result.token?.trim()) {
    throw new Error('@go-beyond/oidc: hosted slot broker returned an empty token')
  }
  return result.token.trim()
}

async function exchange(token: string, audience: string, jti?: string): Promise<string> {
  const base = issuerBase()
  if (!base) {
    throw new Error('@go-beyond/oidc: issuer base URL is required for audience exchange')
  }
  const response = await fetch(`${base}/~token`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ token, aud: audience, ...(jti?.trim() ? { jti: jti.trim() } : {}) }),
  })
  if (!response.ok) {
    throw new Error(`@go-beyond/oidc: audience exchange returned ${response.status}`)
  }
  const result = await response.json() as BrokerResponse
  if (!result.token?.trim()) {
    throw new Error('@go-beyond/oidc: audience exchange returned an empty token')
  }
  return result.token.trim()
}

export async function getGoBeyondOidcToken(options: OidcTokenOptions = {}): Promise<string> {
  const request = options.request ?? requestStorage.getStore()
  const source = requestToken(request) || environmentToken() || await brokerToken()
  const audience = options.audience?.trim() || ''
  if (!audience || audience === DEFAULT_SOURCE_AUDIENCE) return source
  return exchange(source, audience, options.jti)
}

export function getGoBeyondOidcTokenSync(options: { request?: Request } = {}): string {
  const request = options.request ?? requestStorage.getStore()
  const token = requestToken(request) || environmentToken()
  if (!token) {
    throw new Error('@go-beyond/oidc: synchronous token access requires a request or environment token')
  }
  return token
}

export function withGoBeyondOidcRequest<T>(request: Request, fn: () => Promise<T>): Promise<T> {
  return requestStorage.run(request, fn)
}
