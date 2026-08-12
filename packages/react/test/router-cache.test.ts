import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_ROUTER_CACHE_TTL_MS,
  createRouterCache,
  routerCacheKey,
  routerCacheTTLMs,
  type CachePolicy,
  type RuntimeNavigationPayload,
} from "../dist/browser.js";

function samplePayload(cache?: CachePolicy): RuntimeNavigationPayload {
  return {
    apiVersion: "gobeyond.render/v1alpha1",
    buildId: "build-1",
    routeId: "product",
    result: {
      kind: "ok",
      props: { name: "Trail" },
      metadata: { lang: "en", title: "Trail" },
      status: 200,
      cache,
    },
  };
}

test("routerCacheTTLMs never caches private/no-store or absent policies", () => {
  assert.equal(routerCacheTTLMs(undefined), 0);
  assert.equal(routerCacheTTLMs({ mode: "private_no_store" }), 0);
  assert.equal(routerCacheTTLMs({ mode: "private_no_store", maxAge: 300 }), 0);
});

test("routerCacheTTLMs prefers maxAge, falls back to sharedMaxAge, and caps the result", () => {
  assert.equal(routerCacheTTLMs({ mode: "public", maxAge: 10 }), 10_000);
  assert.equal(
    routerCacheTTLMs({ mode: "public", sharedMaxAge: 10 }),
    10_000,
    "falls back to sharedMaxAge when maxAge is unset (PublicRevalidate only sets sharedMaxAge)",
  );
  assert.equal(
    routerCacheTTLMs({ mode: "public", maxAge: 5, sharedMaxAge: 600 }),
    5_000,
    "maxAge wins over sharedMaxAge when both are set",
  );
  assert.equal(
    routerCacheTTLMs({ mode: "public", maxAge: 3_600 }),
    DEFAULT_ROUTER_CACHE_TTL_MS,
    "a long origin freshness window is capped for the client memory copy",
  );
  assert.equal(
    routerCacheTTLMs({ mode: "public", maxAge: 3_600 }, 120_000),
    120_000,
    "an explicit cap overrides the default",
  );
  assert.equal(
    routerCacheTTLMs({ mode: "public" }),
    0,
    "public with no positive freshness window is not cached",
  );
});

test("routerCacheKey is path + search, excluding the fragment", () => {
  assert.equal(
    routerCacheKey(new URL("https://example.gobeyond.dev/products/trail?view=full#reviews")),
    "/products/trail?view=full",
  );
  assert.equal(routerCacheKey(new URL("https://example.gobeyond.dev/")), "/");
});

test("a new cache generation drops previously stored route payloads", () => {
  const cache = createRouterCache({ now: () => 0, ttlCapMs: 60_000 });
  const first = { apiVersion: "gobeyond.render/v1alpha1", buildId: "b", routeId: "r", result: { kind: "ok", props: {}, cache: { mode: "public", maxAge: 60 }, cacheGeneration: "1" } } as any;
  const second = { ...first, result: { ...first.result, cacheGeneration: "2" } } as any;
  cache.set("/", first);
  cache.set("/next", second);
  assert.equal(cache.get("/"), undefined);
  assert.equal(cache.get("/next"), second);
});

test("createRouterCache stores and returns fresh public entries, keyed as given", () => {
  let now = 0;
  const cache = createRouterCache({ now: () => now });
  const stored = cache.set("/products/trail", samplePayload({ mode: "public", maxAge: 30 }));
  assert.equal(stored, true);
  assert.deepEqual(cache.get("/products/trail")?.result.props, { name: "Trail" });

  now = 29_999;
  assert.ok(cache.get("/products/trail"), "still fresh just under the TTL");

  now = 30_000;
  assert.equal(cache.get("/products/trail"), undefined, "expired entries are evicted on read");
});

test("createRouterCache.set is a no-op for private/no-store payloads", () => {
  const cache = createRouterCache();
  const stored = cache.set("/account", samplePayload({ mode: "private_no_store" }));
  assert.equal(stored, false);
  assert.equal(cache.get("/account"), undefined);
});

test("createRouterCache.set is a no-op for payloads without a cache policy", () => {
  const cache = createRouterCache();
  const stored = cache.set("/account", samplePayload(undefined));
  assert.equal(stored, false);
  assert.equal(cache.get("/account"), undefined);
});

test("createRouterCache.setPrivate retains an explicit private payload for one minute", () => {
  let now = 0;
  const cache = createRouterCache({ now: () => now });
  const stored = cache.setPrivate("/account", samplePayload({ mode: "private_no_store" }));
  assert.equal(stored, true);
  assert.ok(cache.get("/account"));
  now = 59_999;
  assert.ok(cache.get("/account"));
  now = 60_000;
  assert.equal(cache.get("/account"), undefined);
});

test("createRouterCache.delete removes a single entry", () => {
  const cache = createRouterCache();
  cache.set("/products/trail", samplePayload({ mode: "public", maxAge: 30 }));
  cache.set("/about", samplePayload({ mode: "public", maxAge: 30 }));
  cache.delete("/products/trail");
  assert.equal(cache.get("/products/trail"), undefined);
  assert.ok(cache.get("/about"));
});

test("createRouterCache.invalidatePaths drops only entries matching the given paths", () => {
  const cache = createRouterCache();
  cache.set("/products/trail", samplePayload({ mode: "public", maxAge: 30 }));
  cache.set("/products/trail?view=full", samplePayload({ mode: "public", maxAge: 30 }));
  cache.set("/about", samplePayload({ mode: "public", maxAge: 30 }));

  cache.invalidatePaths(["/products/trail/"]);
  assert.equal(cache.get("/products/trail"), undefined, "normalizes trailing slashes when matching");
  assert.equal(
    cache.get("/products/trail?view=full"),
    undefined,
    "matches by pathname regardless of search",
  );
  assert.ok(cache.get("/about"), "non-matching entries are untouched");
});

test("createRouterCache.invalidatePaths clears everything when given no paths", () => {
  const cache = createRouterCache();
  cache.set("/products/trail", samplePayload({ mode: "public", maxAge: 30 }));
  cache.set("/about", samplePayload({ mode: "public", maxAge: 30 }));
  cache.invalidatePaths([]);
  assert.equal(cache.get("/products/trail"), undefined);
  assert.equal(cache.get("/about"), undefined);
});

test("createRouterCache.clear removes every entry", () => {
  const cache = createRouterCache();
  cache.set("/products/trail", samplePayload({ mode: "public", maxAge: 30 }));
  cache.clear();
  assert.equal(cache.get("/products/trail"), undefined);
});
