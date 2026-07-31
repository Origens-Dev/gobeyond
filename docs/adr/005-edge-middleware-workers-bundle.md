# ADR: Edge middleware TypeScript bundle (Workers for Platforms)

- Status: Proposed (Stream 4 stub; lock details with gobeyond-internal ADR 013)
- Date: 2026-07-31
- Related: package `@go-beyond/edge-middleware`; hosting-platform ADR 013 in
  `gobeyond-internal`

## Context

Hosted HTTP middleware moves from Go UDS pairs on gbhost to Cloudflare
Workers for Platforms User Workers. Authors need a public TypeScript contract
the CLI can emit and the control plane can upload into a dispatch namespace.

## Decision (stub)

Ship `@go-beyond/edge-middleware` as the User Worker entry contract:

- `export default { fetch }` compatible with WfP upload
- No origin credentials; outbound Worker owns mTLS / origin-verify / Maglev /
  static SigV4
- No `x-gobeyond-auth-context` until hosting Stream 6
- No direct origin fetch from User Workers

Local workspace stub is development-only. Releases pin a tagged public version
from consuming repos.

## Consequences

- Stream 5 CLI emit blocked until this package is published (non-private) and
  tagged.
- Product docs must list WfP CPU/body/subrequest ceilings as customer limits.
