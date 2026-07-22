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
