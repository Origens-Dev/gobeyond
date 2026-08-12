import { normalizeComparablePath } from "./path-utils.js";
import type { CachePolicy, RuntimeNavigationPayload } from "./navigation.js";

/**
 * Upper bound, in milliseconds, on how long a public soft-nav payload may sit
 * in the client Router Cache regardless of how long-lived the origin's
 * `CachePolicy` freshness window is. The origin's `maxAge`/`sharedMaxAge`
 * still governs the HTTP/CDN cache; this cap only keeps the *in-memory* copy
 * short-lived so an in-tab back/forward never replays arbitrarily stale
 * props just because a route set a long edge TTL. Mirrors Next.js's default
 * Router Cache `staleTimes.dynamic` (30s).
 */
export const DEFAULT_ROUTER_CACHE_TTL_MS = 30_000;
export const DEFAULT_PRIVATE_ROUTER_CACHE_TTL_MS = 60_000;

/**
 * Compute how long (ms) a runtime-navigation payload may be kept in the
 * client Router Cache, derived from its `CachePolicy`. Returns `0` (never
 * cache) for `private_no_store` policies, an absent policy, or a public
 * policy with no positive freshness window. Otherwise returns the policy's
 * `maxAge` (falling back to `sharedMaxAge` when `maxAge` is unset - GoBeyond
 * routes commonly rely on `PublicRevalidate`, which only sets
 * `sharedMaxAge`), capped by `capMs`.
 */
export function routerCacheTTLMs(
  policy: CachePolicy | undefined,
  capMs: number = DEFAULT_ROUTER_CACHE_TTL_MS,
): number {
  if (!policy || policy.mode !== "public") return 0;
  const freshSeconds =
    policy.maxAge && policy.maxAge > 0
      ? policy.maxAge
      : policy.sharedMaxAge && policy.sharedMaxAge > 0
        ? policy.sharedMaxAge
        : 0;
  if (freshSeconds <= 0) return 0;
  return Math.min(freshSeconds * 1000, Math.max(capMs, 0));
}

/** Router Cache key: path + search, deliberately excluding the fragment. */
export function routerCacheKey(url: URL): string {
  return url.pathname + url.search;
}

interface RouterCacheEntry {
  payload: RuntimeNavigationPayload;
  storedAt: number;
  expiresAt: number;
}

export interface RouterCacheOptions {
  /** Upper bound applied to every entry's TTL; see `DEFAULT_ROUTER_CACHE_TTL_MS`. */
  ttlCapMs?: number;
  /** TTL for explicitly opted-in private prefetched data. */
  privateTtlMs?: number;
  /** Injectable clock for tests. */
  now?: () => number;
}

/**
 * In-memory client Router Cache for soft-navigation payloads, keyed by
 * `path + search`. Mirrors the Next.js Router Cache's role: instant
 * back/forward and warmed-prefetch navigation without re-hitting the
 * origin, bounded by the route's own `CachePolicy` so private data is never
 * retained and public data does not outlive its freshness window.
 */
export interface RouterCache {
  /** Derive this cache's key for a URL (path + search, no fragment). */
  keyFor(url: URL): string;
  /** Fresh payload for `key`, or `undefined` on a miss or expired entry. */
  get(key: string): RuntimeNavigationPayload | undefined;
  /**
   * Store `payload` under `key` if its `CachePolicy` allows caching
   * (`mode: "public"` with a positive freshness window). Removes any
   * existing entry and returns `false` otherwise - callers do not need to
   * branch on privacy themselves.
   */
  set(key: string, payload: RuntimeNavigationPayload): boolean;
  /** Store an explicitly opted-in private payload in tab memory only. */
  setPrivate(key: string, payload: RuntimeNavigationPayload): boolean;
  /** Remove a single entry, if present. */
  delete(key: string): void;
  /**
   * Remove every entry whose path (ignoring search/fragment) matches one of
   * `paths` (compared via `normalizeComparablePath`). An empty array clears
   * the entire cache - the conservative choice when a caller knows
   * *something* changed but not exactly what.
   */
  invalidatePaths(paths: readonly string[]): void;
  /** Remove every entry. */
  clear(): void;
}

function keyPathname(key: string): string {
  const queryIndex = key.indexOf("?");
  return queryIndex === -1 ? key : key.slice(0, queryIndex);
}

export function createRouterCache(options: RouterCacheOptions = {}): RouterCache {
  const now = options.now ?? (() => Date.now());
  const ttlCapMs = options.ttlCapMs ?? DEFAULT_ROUTER_CACHE_TTL_MS;
  const privateTtlMs = options.privateTtlMs ?? DEFAULT_PRIVATE_ROUTER_CACHE_TTL_MS;
  const entries = new Map<string, RouterCacheEntry>();
  let generation: string | undefined;

  return {
    keyFor(url) {
      return routerCacheKey(url);
    },
    get(key) {
      const entry = entries.get(key);
      if (!entry) return undefined;
      if (entry.expiresAt <= now()) {
        entries.delete(key);
        return undefined;
      }
      return entry.payload;
    },
    set(key, payload) {
      const ttl = routerCacheTTLMs(payload.result.cache, ttlCapMs);
      if (ttl <= 0) {
        entries.delete(key);
        return false;
      }
      const nextGeneration = payload.result.cacheGeneration;
      if (nextGeneration && generation && nextGeneration !== generation) {
        entries.clear();
      }
      if (nextGeneration) generation = nextGeneration;
      const storedAt = now();
      entries.set(key, { payload, storedAt, expiresAt: storedAt + ttl });
      return true;
    },
    setPrivate(key, payload) {
      if (privateTtlMs <= 0 || payload.result.kind !== "ok") {
        entries.delete(key);
        return false;
      }
      const nextGeneration = payload.result.cacheGeneration;
      if (nextGeneration && generation && nextGeneration !== generation) entries.clear();
      if (nextGeneration) generation = nextGeneration;
      const storedAt = now();
      entries.set(key, { payload, storedAt, expiresAt: storedAt + privateTtlMs });
      return true;
    },
    delete(key) {
      entries.delete(key);
    },
    invalidatePaths(paths) {
      if (paths.length === 0) {
        entries.clear();
        return;
      }
      const targets = new Set(paths.map(normalizeComparablePath));
      for (const key of [...entries.keys()]) {
        if (targets.has(normalizeComparablePath(keyPathname(key)))) {
          entries.delete(key);
        }
      }
    },
    clear() {
      entries.clear();
    },
  };
}
