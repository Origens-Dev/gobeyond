# Connect resource-scoped agents between apps

`agents/connections` is the versioned app-to-app boundary for an agent owned by
one app and callable from another. A connection identifies a provider agent and
one resource, such as a home; it does not copy its instructions, tools, or owner
identity into the consumer app.

## Ownership

The provider app owns grants, resource membership, configuration, and tools. The
consumer owns its directory placement and authenticates its current callers.
GoBeyond hosting authenticates the app/environment and starts a fresh execution
in the destination project. Existing same-project voice grants are never reused
or broadened for cross-app calls.

Contracts validate shape, not authority. Load current grants, check the granting
owner still has authority, and revalidate source membership or line assignment at
admission and before each tool. If those checks are unavailable, deny access.
Treat a shared-line grant as access by whoever uses that authenticated line;
do not infer a person's identity from a caller ID or display name.

## OAuth helpers

Register confidential clients explicitly per environment with exact HTTPS
redirect URIs and random secrets. Store `HashSecret(secret)` in registration.
The consumer's `Consumer.Begin` generates PKCE S256 and a one-use state bound to
the initiating authenticated user and selected network. `Consumer.Complete`
consumes that state and exchanges the code server-to-server. Check network-admin
authority before beginning and again before installing the returned connection.
Keep the resulting credentials server-side and encrypted under app policy.

The provider owns its login and consent UI. After the authenticated resource
owner approves the selected callers and capabilities, call `Provider.Authorize`.
Its `GrantPolicy.Authorize` implementation must verify and persist the consent;
the requested owner, resource, or caller list is not evidence of authorization.
Register `Provider.TokenHandler()` as the token endpoint. It requires confidential
client HTTP Basic authentication, PKCE, exact redirect matching, bounded form
requests, and no-store responses.

Implement the persistence interfaces in the provider's database:

- `SaveCode`/`LoadCode` store code hashes and client/redirect/challenge/grant binding.
- `ConsumeCode` atomically marks an unexpired code used and inserts its tokens.
- `RotateToken` atomically marks a refresh token rotated and inserts its successor.
  Retain old refresh hashes as replay tombstones. A replay revokes the entire
  family, including the newly issued successor.
- `RevokeFamily` makes every access and refresh lookup in that family inactive.
- Token lookups return `connections.ErrNotFound` only for an absent credential.
  Propagate storage/transport errors. Revocation may try the alternate token
  index or acknowledge an unknown token only after a true not-found result.
- `GrantPolicy.Resolve` reads current grant version, revocation, and current owner
  authority. `ResolveAccessToken` checks that state on every resolution.

Do not implement consumption or rotation as a read followed by an unguarded
write. The database operation must provide a transaction or compare-and-swap.
Persist hashes of provider codes/tokens, never their raw values. Consumer pending
state includes a secret PKCE verifier and must be protected accordingly.

OAuth grants and access tokens are not voice-session grants. An authorized
invocation exchanges resource authority for a fresh target `Admission` pinned to
the deployed revision/manifest. Source lineage remains distinct from destination
execution identity. `ProviderFunded` means target AI consumption belongs to the
provider; it does not erase separate telephone transport or source-agent usage.

Direct portal conversations use an explicit local-owner admission: caller kind
`user`, identical source and target tenants, and empty connection/grant fields.
The provider must authorize that local owner before registration. This structural
variant is not a way to bypass authorization or cross tenant boundaries, and it
does not require an artificial OAuth connection back to the provider itself.

## Configuration and usage

`Experience` is provider-owned, versioned configuration. Apply changes to new
sessions and pin the effective revision. Validate model/voice/tool choices against
deployed capabilities; configuration cannot enlarge the grant or tool registry.

`UsageEvent` describes lifecycle or model consumption with explicit units.
Authenticate its delivery and bind it to a durably registered target session,
tenant, and resource before accepting it. `EventID` deduplicates retries.
`Sequence` orders cumulative snapshots for the same execution/provider/metric;
replace earlier cumulative values rather than adding a final report again. Delta
reports are added once. Never mix delta and cumulative reports in one stream.
Keep provider-reported voice milliseconds separate from connected wall time.
Unknown usage or cost remains unknown, not zero. Late reports may update terminal
sessions, and reconnect executions must retain their parent logical call.

Hosting owns authoritative lifecycle/usage production and durable retry delivery;
provider receipts are projections, not new platform billing events. Never accept
browser-supplied timestamps, token counts, payer labels, or callback URLs as
authoritative. No audio or transcript retention is needed for usage accounting.

The package is an application integration seam, not an automatically deployed
OAuth server or a central home-permissions database. Apps must supply durable
stores and policy; hosting must wire admission and trusted event delivery.
