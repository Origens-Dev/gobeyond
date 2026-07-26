# ADR 004: Lazy route residency and pack-only plan/static artifacts

- Status: Accepted (design lock; implementation in progress)
- Date: 2026-07-25
- Related: `docs/architecture.md` (startup load → lazy decode); implementation on `cursor/lazy-route-residency-f903`

## Context

Render plans and packaged static props are eagerly decoded at process start
today (`PageRoute.Plan` required; `LoadStaticStore` unmarshals every entry).
That bounds tenant density for large sites. Nothing is live in production yet,
so a dual-format compatibility window is unnecessary.

## Decision

### Pack-only runtime artifacts

Ship two immutable binary containers as the only runtime plan/static inputs:

| File | Role |
| --- | --- |
| `dist/server/render-plans.gbp` | All route render plans |
| `dist/server/runtime-data/static-build.gbs` | All packaged static props/metadata entries |

Optional pretty JSON under `dist/` may remain for human inspection and
conformance; the Go runtime and hosting ingest **never** load those JSON files
as the residency store. CDN per-route static JSON under
`_gobeyond/builds/<id>/static/<routeId>` is unchanged (edge/static origin).

### Container shape

Binary header + sorted index + length-prefixed records:

- Magic, format version, build ID, `weigherVersion`, `recordCodec`
- Index: keys, offsets, lengths, digests, `encoded_len`,
  `estimated_decoded_weight`, `estimated_peak_weight`
- Open validates format/bounds/overlaps/duplicate keys/build ID/size limits
  without decoding records
- Cold load verifies selected digest + route/entry identity

### Record codec v1: `json+zstd`

Each record is zstd-compressed JSON preserving
`gobeyond.render/v1alpha1` / static-entry semantics. Decode path: ReadAt →
decompress → existing `renderplan.Parse` / static decode. Whole-file
compression is rejected (breaks random access). A future binary plan IR may
register as another `recordCodec` without redesigning the container.

### Public APIs

```go
type PlanStore interface {
    BuildID() string
    Has(routeID string) bool
    Plan(ctx context.Context, routeID string) (*renderplan.Plan, error)
}

type StaticEntries interface {
    BuildID() string
    Has(routeID string) bool
    Entry(ctx context.Context, routeID string, params map[string]string) (LoadedPage, bool, error)
    Contracts() *codegen.Document
}
```

- Inline `PageRoute.Plan` wins over the store (mixed-mode tests/tiny apps).
- `Plan` may be nil iff `PlanStore != nil && Has(routeID)`.
- `PlanStore.BuildID()` must equal `Config.BuildID` exactly.
- Acquire plan/entry only after loaders/redirects establish rendering is needed.
- File stores use `ReadAt`/`SectionReader`, implement `Close`/`Stats`/`Trim`;
  apps own Close. mmap is deferred until measured Graviton benefit.

### Static entry keys

`routeID + "?" + sorted queryEscape(name)=queryEscape(value)`. Empty params →
`routeID + "?"`. Optional catch-all with zero segments → present empty value.

### Weight model

Pack-time deterministic weigher over the decoded value; store weights in the
index. Peak = decoded + encoded×3 (plans) / ×2 (static). Oversized when
decoded > `MaxResidentBytes/8` (render, do not cache). Unknown weigher version
falls back to encoded×8 / ×5. Cache accounting uses estimates; acceptance
harnesses measure attributable live heap separately. No forced GC.

### Residency (public defaults)

SLRU + singleflight by immutable build + record key. Plans: 64 entries / 32 MiB
estimated decoded. Static: 128 / 32 MiB. Idle expire 10 minutes. Negative cache
only for immutable integrity/decode failures. Canceled waiters do not cancel
shared decode.

### Validation

| Stage | Checks |
| --- | --- |
| CLI build | Full parse every record; digests; write packs |
| Runtime open | Header + index only |
| Cold request | Selected digest + decode + identity |

### Out of scope here

Host admission, Maglev retry, sandbox idle injection — private hosting
(`gobeyond-internal`). Public route-trie optimization must not block this work.
Density marketing requires Graviton/cgroup trials (≥20% more tenants).

## Consequences

- Softens “load all plans at startup” in architecture docs to “packaged locally;
  decoded on demand; bounded residency; no remote plan fetch.”
- In-repo scaffolds/examples cut over to packs in the same delivery.
- Go code and package globals remain resident; process stop is full reclaim.
