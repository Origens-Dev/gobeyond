// Package cache implements GoBeyond's request-time caching primitives:
// per-request memoization (cache.Memo), the data cache (cache.Load), the route
// props cache (cache.LoadRoute), their invalidation entry points (RevalidateTag
// / RevalidatePath), the byte Store tiers those sit on, the shared privacy
// predicate that gates every cache layer, and the key/envelope contracts the
// action-refresh client is built against.
//
// # RequestScope
//
// Every cache primitive in this package requires a *RequestScope on the
// context, not a bare context.Context. The runtime attaches one at each
// request entry point (runtime.serveDocument, runtime's applyMiddleware,
// which covers APIs/actions/soft-nav "runtime" requests) via WithRequestScope.
// context.WithTimeout / context.WithValue children (e.g. the loader and
// action deadlines runtime.Server wraps requests in) still resolve
// RequestScopeFrom because context value lookups walk the parent chain; no
// second attachment is needed past the entry point.
//
// A RequestScope holds three things for the lifetime of one request:
//
//   - the request's privacy flag (the Get-gate result, computed once from
//     request headers before any middleware or loader runs);
//   - a refresh recorder actions use to accumulate RevalidatePath / RevalidateTag
//     calls (the paths/tags land in the action envelope's "refresh" field);
//   - the per-request memo bag cache.Memo reads and writes.
//
// # Store tiers
//
// A Store is a byte tier: Get/Set/Delete plus the tag-version primitives that
// make invalidation possible. Tiered composes the two implementations into
// the shape a deployment runs:
//
//	store := cache.Tiered(memstore.New(memstore.Options{}), shared, cache.TieredOptions{})
//
// cache/memstore is the bounded in-process L1 (TTL + LRU, synchronous writes).
// cache/redisstore is the shared L2 (ElastiCache/Valkey, write-behind through
// a bounded worker pool, write-time compare-and-set on tag versions). Passing
// a nil L2 is the supported degraded mode for a deployment with no cache
// endpoint configured - redisstore.FromEnv reports exactly that case, and
// everything above the Store behaves identically either way.
//
// Invalidation is versioned rather than broadcast-dependent. Every entry
// records the version of each of its tags at the moment its value was
// computed. A bump makes the shared counter disagree with the entry, which
// both refuses the entry on read and refuses a late write of a value computed
// before the bump (Locked decision 13). L1 additionally drops matching entries
// synchronously on a bump, and bounds its own TTL, so the only thing the
// redisstore pub/sub channel buys is dropping another instance's L1 copies
// sooner - it is an optimization, never a correctness requirement.
//
// # Runtime handle: deploy prefix and BuildID
//
// cache.Load builds its key from the deploy prefix and the BuildID, neither of
// which a call site can be trusted to supply. They live on a *Runtime built
// once at startup and installed on each request's RequestScope
// (WithRuntimeHandle), which the runtime server does for every document,
// runtime-data, API, and action request:
//
//	runtime.Config{BuildID: buildID, Cache: &cache.RuntimeConfig{DeployPrefix: prefix, Store: store}}
//
// The handle rides on the scope rather than a package-level global for the
// same reason the memo bag does: two servers in one process (or two builds in
// one test binary) must not share a cache, and a scope-less context must fall
// through to the uncached path rather than reach for ambient state. The server
// owns Cache.BuildID and rejects a handle configured for a different build, so
// keys can never claim a build the process is not serving.
//
// # Privacy: Get-gate vs Set-gate
//
// IsPrivateRequest is the Get-gate: it inspects only request headers and
// answers "must this request be treated as carrying viewer identity" before
// any cache read or origin load happens. IsPrivateResponse is the Set-gate:
// it additionally inspects the loaded response's Set-Cookie header, because
// a response can mint viewer identity even when the inbound request had
// none. Every cache layer must fail closed - private wins over any requested
// CachePublic policy. cache.Load applies the Get gate by skipping the store
// entirely on a private request and computing the value instead; invalidation
// is deliberately not gated, because a mutation made by an authenticated
// viewer still has to evict the public data it changed.
//
// # Keys
//
// Route keys and cache.Load ("data") keys share one schema root:
//
//	{deployPrefix}/{buildId}/route/{routeId}?{path}{rawQuery}@{publicOrigin}
//	{deployPrefix}/{buildId}/data/{name}/{argsDigest}
//
// deployPrefix namespaces one tenant/deploy inside a possibly shared Redis
// instance (Locked decision 15 - no app-level encryption, the prefix is the
// isolation boundary). buildId namespaces one build's schema/shape inside a
// deploy: a new build must never read a previous build's cached shapes, so
// buildId is part of the key, not a value that gets version-checked after
// the fact. Route keys additionally fold in routeId, the normalized request
// path, the raw query, and PublicOrigin so that two hosts/origins served by
// the same build never collide. Data keys combine cache.Load's caller-chosen
// Name (deploy-unique, e.g. "catalog.product") with a canonical encoding of
// Args - see DataKey and canonicalArgs.
//
// # Data cache
//
// cache.Load is the request-time data cache:
//
//	product, err := cache.Load(ctx, cache.Options{
//		Name:       "catalog.product",
//		Args:       []any{slug},
//		Revalidate: 60 * time.Second,
//		Tags:       []string{"products", "product:" + slug},
//	}, cache.JSONCodec[Product](), fetchProduct)
//
// A fresh entry is returned as-is. An entry past its Revalidate deadline but
// inside RuntimeConfig.MaxStale is returned immediately while one background
// goroutine - detached from the request, time-bounded, panic-guarded, and
// holding a distributed lease so one instance rather than all of them does the
// work - recomputes it. Past that window the entry is gone and the caller
// waits for a fill, deduplicated in-process by singleflight.
//
// Revalidate must be positive for anything to be cached. "Cache until a tag
// bump" is deliberately not what an omitted Revalidate means: the zero value
// of an Options literal must not be the one that pins data in a shared cache
// indefinitely.
//
// RevalidateTag and RevalidatePath bump a tag's version synchronously before
// returning, so an action can respond as soon as they do, and record the
// tag/path on the RequestScope for the action envelope's "refresh" field.
// RevalidatePath bumps PathTag(path) - one normalized path, not everything a
// route ever produced - so an entry that must react to it has to list that tag
// in Options.Tags.
//
// # Route cache (props ISR)
//
// cache.LoadRoute is the same read path keyed by URL instead of by name. The
// runtime calls it for a page route whose definePage declared a revalidate
// window, so the route's loader runs once per window per URL rather than once
// per request:
//
//	page, err := cache.LoadRoute(ctx, cache.RouteOptions{
//		RouteID: routeID, Path: request.URL.Path, RawQuery: request.URL.RawQuery,
//		PublicOrigin: origin, Revalidate: 60 * time.Second, Tags: []string{"products"},
//	}, codec, storable, load)
//
// It differs from Load in two ways. It adds PathTag(Path) to the entry's tags
// itself, so RevalidatePath("/products/widget") drops exactly that page while
// its siblings survive, without every route author remembering the tag. And it
// takes a storable predicate consulted after the loader ran, because whether a
// page may be shared is only knowable from its result: the runtime uses it to
// keep non-OK results, and responses that mint a cookie, out of a store other
// visitors read. What gets stored is the loader's data, never its response
// headers and never rendered HTML - each request re-renders so it gets its own
// CSP nonce and render clock.
//
// # Action envelope
//
// ActionEnvelope is the frozen wire shape runtime.serveAction emits on every
// successful action, and the packages/react client parses and refreshes any
// paths the action recorded.
//
// # Edge mapping guidance for schema revalidate
//
// definePage's schema-level `revalidate` describes origin props ISR: how
// often the Go loader is allowed to recompute props for a route (see
// LoadRoute). It is not, by itself, an HTTP cache directive - a loader's
// gb.CachePolicy is. When a route sets both, document the edge policy as an
// explicit function of the schema revalidate window rather than inventing an
// automatic mapping the runtime cannot verify was intentional:
//
//	gb.PublicRevalidate(revalidate, k*revalidate, staleIfError)
//
// e.g. shared max-age equal to the origin revalidate window, stale-while-
// revalidate a small multiple k of it (so the edge serves stale content for
// roughly one extra origin-refresh cycle while a background revalidation is
// in flight), and an explicit stale-if-error window. Authors set
// gb.CachePolicy themselves; the runtime never infers it from revalidate,
// because gb.OK always sets CachePrivateNoStore and there is no sentinel
// that distinguishes "author omitted CachePolicy" from "author chose
// private" (Locked decision 7).
//
// # Dual-TTL lint rule (design, lint itself ships later)
//
// Because CachePolicy has no "unset" sentinel, the runtime cannot detect
// when a route's schema `revalidate` and its loader's gb.CachePolicy
// disagree by accident versus by design. The intended lint is warn-only: for
// any route that sets both a schema
// `revalidate` and a public gb.CachePolicy, compare the schema window
// against the policy's SharedMaxAge and warn when they diverge by more than
// a small tolerance. It must never auto-correct or auto-default either
// value - silently rewriting an explicit choice is worse than a noisy
// warning, and an "auto-default" would have to guess at the very
// omitted-vs-chosen distinction this section says is undetectable.
package cache
