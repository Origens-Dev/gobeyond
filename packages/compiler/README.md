# `@go-beyond/compiler`

This package compiles GoBeyond's portable TSX profile into the canonical
`gobeyond.render/v1alpha1` JSON rendering plan. It is a build-time package;
it is never part of the Go production runtime.

```ts
import { compileFile, compileSourceOrThrow } from '@go-beyond/compiler'

const plan = compileSourceOrThrow({
  routeId: 'products_slug',
  fileName: 'app/products/[slug]/page.tsx',
  sourceText,
})

// The normal build API follows relative and explicitly aliased source imports.
const result = await compileFile({
  projectRoot: '/workspace/site',
  entryFile: 'app/products/[slug]/page.tsx',
  routeId: 'products_slug',
  sourceRoots: [{ prefix: '@/', directory: '.' }],
})
```

The initial compiler supports intrinsic HTML/SVG, fragments, project-owned
function components across relative imports and explicit source-root aliases,
JSX `children` composition, props paths, portable unary/binary expressions,
conditional markup, keyed `.map()` output, deterministic `useState` initial
values, `SafeHTML`, and `ClientOnly` with an optional fallback. Event handlers and `useEffect`
bodies remain browser code and do not enter the server plan.

The compiler attempts portable compilation even for `use client` modules.
Unsupported render behavior may downgrade only at the nearest marked boundary;
the result includes a deterministic `gobeyond.client-boundaries/v1alpha1`
record with its route, source span, component, boundary module, and reason.
Unmarked unsupported code remains a source-located compile error. Parse, type,
module, contract, and internal errors are never downgraded.

The CLI follows the source graph and writes a plan to stdout or a selected output file:

```bash
gobeyond-compile --route products_slug --out product.plan.json \
  'app/products/[slug]/page.tsx'
```

For a route set, `--project` accepts JSON matching `CompileProjectOptions`:

```json
{
  "projectRoot": "../site",
  "sourceRoots": [{ "prefix": "@/", "directory": "." }],
  "routes": [
    {
      "routeId": "products_slug",
      "entryFile": "app/products/[slug]/page.tsx"
    }
  ]
}
```

```bash
gobeyond-compile --project routes.json --out compiler-output.json
```

The output is `gobeyond.compiler-project/v1alpha1` with ordered `plans` and a
canonical `gobeyond.contract/v1alpha1` `contracts` object. It also includes
`routeModules`, whose outer-to-inner `layoutFiles` order is the canonical input
for the browser route registry, a `clientBoundaries` transform manifest, and a `gobeyond.static-build/v1alpha1`
`staticBuild` artifact. A route defaults to
`page.schema.ts` and optional `actions.ts` beside its entry file. Actions use
the stable, case-sensitive ID `<routeId>:<exportedActionName>`.
Localized routes may directly forward the page descriptor with
`export { page } from './relative/page.schema.js'`; forwarding cycles and
missing targets are build errors.

Set a project route to `"kind": "static"` to evaluate its optional
`page.build.ts` in an isolated Node subprocess during the build. Literal routes
without a build module receive one `{ params: {}, props: {} }` entry.
Parameterized routes must export `generateStaticParams()`; the compiler calls
`loadStaticProps(params)` once for every generated parameter object. Returned
values must be plain, finite, acyclic JSON data and must exactly satisfy the
page schema (including no undeclared object fields).

An optional `page.metadata.ts` may export `metadata(props, params)`. It executes
in the same build-only subprocess and emits validated, serializable document
metadata with each static entry. These build modules and their imports are
never traversed by the React render-plan graph and are not production inputs.

Applicable `app/layout.tsx` files are compiled around every route plan from the
root layout through the nearest nested layout. Consumers must use the emitted
`routeModules` order to create the identical React wrapper in the browser.

The programmatic build API is:

```ts
const result = await compileProject({
  projectRoot: '/workspace/site',
  routes: [{
    routeId: 'articles_slug',
    entryFile: 'app/articles/[slug]/page.tsx',
    routePattern: '/articles/[slug]',
    kind: 'static',
  }],
})

// result.output.plans
// result.output.contracts
// result.output.routeModules
// result.output.clientBoundaries
// result.output.staticBuild.routes[0].entries
```

`compilePageContractSource` and `compileActionContractSource` expose the same
syntax-only AST normalization independently. Descriptor modules are parsed,
never imported or executed.

## Intentional MVP boundaries

- Package components are resolved through package exports and source barrels.
  Portable package components join the Go plan; unsupported package code still
  requires a package-authored or project-owned `use client` boundary.
- Source aliases must be explicit prefix/directory pairs.
- `useContext`, `useId`, `useLayoutEffect`, `useMemo`, and `useReducer` are
  rejected in initial rendering.
- Suspense, streaming, arbitrary function calls, dynamic computed properties,
  and complex map callback bodies are not portable.
- Metadata and JSON-LD use GoBeyond's separate document metadata contract; they
  are not body render-plan nodes.
