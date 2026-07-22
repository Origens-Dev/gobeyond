# `@gobeyond/vite`

This Vite plugin consumes the exact client-boundary manifest emitted by
`@gobeyond/compiler`. It wraps only compiler-approved downgraded call sites;
ordinary `use client` modules that compiled portably are left unchanged.

```ts
import { defineConfig } from 'vite'
import { goBeyond } from '@gobeyond/vite'

export default defineConfig({ plugins: [goBeyond()] })
```

The build orchestrator passes `GOBEYOND_CLIENT_BOUNDARIES` as a path to either
the standalone `gobeyond.client-boundaries/v1alpha1` manifest or the complete
compiler project output. A path may also be passed as `clientBoundaries`.

Each transformed boundary renders its explicit fallback, or `null` when no
fallback exists, on React's first pass and mounts the original component after
an effect. Stale source spans fail the Vite build and require recompilation.
