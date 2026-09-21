# Public runtime configuration and portable builds

Declare browser-visible deployment settings explicitly in `gobeyond.json`:

```json
{
  "portableBuild": true,
  "publicRuntime": ["CLERK_PUBLISHABLE_KEY", "CLERK_DOMAIN"],
  "requiredPublicRuntime": ["CLERK_PUBLISHABLE_KEY"]
}
```

The `publicRuntime` declaration is the explicit browser-public classification.
Configure the corresponding platform variables with runtime scope. The declaration
is independent of the platform variable sensitivity and build/runtime scopes. Never put secret keys or
credentials in this list. Required public variables are validated before deployment
activation. Optional missing values remain absent.

Read them in browser code with `publicEnv` from `@go-beyond/react`, including at
module initialization. The server emits safely serialized configuration in its
bootstrap document before application modules evaluate, with no extra request.
The same deployment snapshot supplies server, static render-plan, ISR, navigation
and action rendering. Compiled JavaScript/assets are shared and remain immutable.

Custom document hosts using a nondefault bootstrap element ID must mark that JSON
script with `data-gobeyond-bootstrap` and pass the matching ID to browser bootstrap.
The marker makes configuration discoverable before eager application imports run.
Use the framework serializer instead of interpolating values into raw HTML.

A configuration-only deployment retains compiled build identity and changes the
deployment revision. Navigation and client caches compare both identities;
a changed revision reloads stale client state. Host and deployment isolation must
also be maintained by the platform's rendered-content cache.

Portable compilation disables ambient dotenv loading and strips deployment
variables from child commands. Go builds use readonly modules and the local pinned
toolchain. Lockfiles, generator and build tools must be pinned; build scripts that
fetch mutable content or generate environment-dependent code require migration
before opting in. A changed environment variable should produce a configuration
revision, not a new compilation. Changes to actual compilation inputs still build.

Portable/public-config static routes retain their compiled render plans and data;
HTML is rendered under the deployment snapshot and can be cached by the hosting
platform. This avoids sharing environment-specific HTML between deployments,
with a first-render cost compared with pre-exported HTML.
