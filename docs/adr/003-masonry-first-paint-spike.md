# ADR: First paint for viewport-stateful class components

Status: **Spike only — do not ship**  
Date: 2026-07-25  
Related: Workstream B8 Phase 3 (framework ergonomics gaps)

## Context

This spike used Studio’s `MasonryGallery` and `react-masonry-css` as a concrete
case study for a general question: can a third-party class component whose
layout depends on viewport or browser state participate in portable first
paint? The example groups children into columns using opaque class logic.
Phases 1–2 of class first-paint make presentational `render()` / baked
`this.state` portable, but they do **not** make this class of widget portable.
The investigated example still needs helper inlining, constructor
`if`/`parseInt`, dynamic `Children.toArray`, loops, and reordering React
children into columns.

The spike considered a general lowering, with no package-name special case:

1. Outer `each` over a portable `range(columnCount)` intrinsic.
2. Inner keyed `each` over children filtered by `index % columnCount`.

## Investigation

### What would need to be true

| Requirement | Current IR / compiler |
| --- | --- |
| Treat JSX children as a random-access array of plan nodes | Plan children are already a list, but there is no expression that selects “the Nth child node” as a renderable value while preserving React element identity |
| `range(n)` intrinsic | Not present; would be a small additive intrinsic |
| `index % columnCount` filter on `each.when` | Feasible after B4’s optional `when` |
| Preserve exact React child order / keys across columns | The investigated library **reorders** DOM into column buckets; React’s first paint from `Children.toArray` + column assignment must match Go byte-for-byte for hydration |
| Opaque npm class (`react-masonry-css`) | Must not special-case by package name; would require compiling the library’s `render()` or replacing call sites with a first-party primitive |

### Why a small general lowering is insufficient today

1. **Child model mismatch.** The render plan can `each` over JSON props arrays. It cannot `each` over “the children React nodes of this element” as data while still emitting those same nodes with their original keys. Encoding children as a props array loses component identity; encoding them as repeated plan clones diverges from React’s single-child ownership.

2. **Order semantics.** CSS multi-column (`column-count`) and JavaScript-managed column buckets can produce different visual orders. Matching the investigated `react-masonry-css` first paint means reproducing **its** column assignment, not CSS columns. That assignment is library-specific (breakpoint map, `columnClassName`, etc.).

3. **Hydration parity bar.** Go HTML ≡ React `renderToString` ≡ first browser render. A range+modulo lowering that approximates columns but disagrees with the actual third-party class on child order or wrapper markup will fail conformance even if it “looks similar.”

4. **Package special-case is rejected.** Teaching the compiler `react-masonry-css` by name is explicitly out of scope and would not generalize to other viewport-stateful layout widgets. This ADR does not propose support or an integration for that package.

## Recommendation

- **Ship independently:** `<Columns>` is a general portable CSS multi-column
  layout primitive. It emits real content in Go-rendered HTML without
  JavaScript; it is not an adapter for, or replacement implementation of, the
  investigated library.
- **Keep client-only:** Third-party layout widgets whose output depends on
  viewport or browser state belong behind `ClientOnly`. A portable layout can
  be used as an optional fallback when it suits the product design.
- **Do not ship:** No range+modulo lowering or package-specific integration
  follows from this spike.
- **Revisit generally:** A future IR with a first-class keyed-child-list value
  could reopen the general lowering question in a new ADR, with explicit
  hydration semantics.

## Verdict

**Spike-only / defer.** Exact hydration parity for viewport-stateful third-party
class components is not achievable with a small general `range`+`%` lowering
on today’s plan IR without interpreting arbitrary class JavaScript or defining
new framework-owned semantics. Keep those widgets client-only. Use `<Columns>`
where a general portable CSS multi-column layout is independently appropriate.
