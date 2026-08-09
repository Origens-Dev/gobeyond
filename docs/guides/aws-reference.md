# AWS reference deployment

The OpenTofu reference is a deployable topology, not a framework-managed
hosting command.

```text
CloudFront → private S3: immutable HTML, JS, CSS, images, static route data
           → private ALB → ECS/Fargate: Go loaders, actions, APIs, documents
                              → optional ElastiCache Serverless Valkey (L2)
```

Browser assets use content-hashed, build-ID paths. Go runtime/action requests
include the build ID; an incompatible version returns a typed mismatch before
business logic runs, allowing a guarded browser reload.

`dist/deploy/route-trie.json` includes `staticAssetPaths` for every file copied
from `public/`. Pass that list to OpenTofu's `public_asset_paths`; these exact
URLs are sent to private S3 instead of falling through to the Go origin.

Public social cards, `/brand/*`, and generated favicons stay on private S3 via
`staticAssetPaths` and ordered static behaviors. `/_gobeyond/image` is a
sibling Go runtime route, not a `/_gobeyond/builds/<id>/...` artifact. Local
preview uses `GOBEYOND_STATIC_DIR` with `runtime.StaticFiles` (gzip for
compressible types; immutable Cache-Control for content-addressed
`/_gobeyond/builds/...` artifacts, not for `public/` files). Production needs
an S3-backed `imageopt.Loader` plus a CloudFront cache key covering `url`, `w`,
`q`, `f`, and the trusted viewer host.

Multi-site hosting infrastructure may add that IAM and edge routing, but it is
outside this public single-site reference. The module under `infra/opentofu/`
does not include those resources.

For a deployment, upload immutable static artifacts first, start and validate
the new Go target, then switch document/runtime routing. Retain previous static
assets for the configured period. The current alpha does not guarantee that old browser
clients remain pinned to old Go deployments.

Before an apply:

```bash
tofu fmt -check
tofu validate
tofu plan
pnpm build
```

Verify S3 is private behind CloudFront origin access control, the ALB has no
public ingress, secrets are not present in static props/manifests, cache keys
include build versions where required, and the final production image contains
no Node or npm executable.

Optional shared cache: set OpenTofu `create_cache = true` for a private
ElastiCache Serverless Valkey endpoint. Tasks receive `GOBEYOND_CACHE_*`
environment variables; keys are prefixed per deployment. Wire the cache with
`cache/openfromenv.OpenFromEnv` (bounded L1 + optional Redis L2 + tag-bump
watcher)—see `cache/example_test.go`. There is no application-level Redis
encryption—do not cache secrets or viewer-specific data. CloudFront's dynamic
cache key does not include OIDC or auth-context headers; for those requests the
origin's `private, no-store` downgrade is the sole edge-cache isolator. See
`infra/opentofu/README.md`.
