# Changelog

GoBeyond follows semantic versioning. The portable React, render-plan, value
contract, browser payload, and deployment manifests are versioned compatibility
surfaces; alpha releases may revise them with explicit changelog entries.

## 0.1.0-alpha.0 - Unreleased

- Move the Go module path to `github.com/Origens-Dev/gobeyond`.
- Rename npm packages to the `@go-beyond/*` scope.
- Add `adapters/lambda` Function URL helper (`Serve` / `Dispatch`) for
  `provided.al2023` site Lambdas that keep `dist/static` on object storage.
- Establish the `gobeyond.render/v1alpha1` portable TSX compiler and Go
  renderer contract.
- Pin React and React DOM to 19.2.8 and add cross-language hydration
  conformance tests.
- Add deterministic route discovery, generated Go page/action contracts,
  dynamic Go documents, middleware, APIs, actions, and build mismatch safety.
- Co-locate route-owned Go with React under `app/`, project it into import-safe
  generated packages, and add managed route modules for bracket-path IDE support.
- Add the SEO acceptance website, Node-free artifact audit, starter generator,
  AWS OpenTofu reference, and website-first contributor documentation.

The alpha does not claim arbitrary React SSR or Next.js compatibility. See the
portable profile and deferred list in the main README before adopting it.
