# @go-beyond/oidc

Public GoBeyond workload-identity helpers for GoBeyond apps, workers, builds,
and Node-compatible tooling. Hosted app and worker slots retrieve renewable
source tokens through the per-slot broker; local and build environments may
use `ORIGENS_OIDC_TOKEN` or `GOBEYOND_OIDC_TOKEN`.

```ts
import { getGoBeyondOidcToken } from '@go-beyond/oidc'

const token = await getGoBeyondOidcToken({ audience: 'sts.amazonaws.com' })
```

Request headers take precedence over request context, environment variables,
and the hosted-slot broker. The synchronous API never performs socket or
network I/O.
