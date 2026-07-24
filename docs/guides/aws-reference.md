# AWS reference deployment

The OpenTofu reference is a deployable topology, not a framework-managed
hosting command.

```text
CloudFront → private S3: immutable HTML, JS, CSS, images, static route data
           → private ALB → ECS/Fargate: Go loaders, actions, APIs, documents
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
preview uses `GOBEYOND_STATIC_DIR`; production needs an S3-backed
`imageopt.Loader` plus a CloudFront cache key covering `url`, `w`, `q`, `f`,
and the trusted viewer host.

The multi-site hosting OpenTofu codes that IAM and edge routing per
[ADR 002](https://github.com/Origens-Dev/gobeyond-internal/blob/main/docs/adr/002-image-optimizer-design-lock.md),
but it is not applied in AWS. This single-site reference under
`infra/opentofu/` does not yet include those resources.

For a deployment, upload immutable static artifacts first, start and validate
the new Go target, then switch document/runtime routing. Retain previous static
assets for the configured period. The MVP does not promise that old browser
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
