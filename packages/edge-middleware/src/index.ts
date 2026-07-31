/**
 * @go-beyond/edge-middleware — Stream 4 stub (not published).
 *
 * Hard rules (also documented in gobeyond-internal
 * docs/cloudflare-edge-middleware-bundle.md):
 * - Do NOT set x-gobeyond-auth-context (Stream 6).
 * - Do NOT origin-fetch or hold mTLS / origin-verify / Maglev / S3 secrets.
 * - Subrequests to origin/static must go through platform outbound intercept.
 */

/** Minimal waitUntil surface (Cloudflare ExecutionContext subset). */
export type EdgeExecutionContext = {
  waitUntil(promise: Promise<unknown>): void
  passThroughOnException(): void
}

/** Platform-injected env for User Workers. No customer origin secrets. */
export type EdgeMiddlewareEnv = {
  /**
   * Reserved for future non-secret platform params passed via outbound
   * parameters. Must never include mTLS, origin-verify, or Maglev keys.
   */
  readonly [key: string]: unknown
}

export type EdgeMiddlewareHandler = {
  fetch(
    request: Request,
    env: EdgeMiddlewareEnv,
    ctx: EdgeExecutionContext,
  ): Promise<Response> | Response
}

/**
 * Minimal pass-through stub. Continues the request so Outbound can apply
 * origin/static credentials. Does not mutate auth-context headers.
 */
export function createPassThroughMiddleware(): EdgeMiddlewareHandler {
  return {
    async fetch(request: Request): Promise<Response> {
      // Relative fetch → platform outbound intercept (not a direct origin call).
      return fetch(request)
    },
  }
}

/** Default User Worker entry Dispatch can load. */
const worker: EdgeMiddlewareHandler = createPassThroughMiddleware()
export default worker
