# GoBeyond AWS reference

This is an intentionally small, runnable **single-site** OpenTofu reference for GoBeyond's deployment model. It is not a hosted platform: it creates one CloudFront distribution, one canonical public origin, one S3 bucket, and one Go service for one site.

```text
browser
  -> CloudFront Function (static route selector)
       -> private S3 via OAC: immutable browser assets, static route data, static documents
       -> CloudFront VPC origin -> private ALB -> ECS/Fargate Go server: dynamic documents, APIs, actions
```

The final ECS image is the Node-free Go server plus its rendering plans and runtime manifests. Browser JavaScript, CSS, fonts, images, static data, and static HTML are private S3 objects exposed only through CloudFront.

## What the reference creates

- a VPC, two or more private task/ALB subnets, and optional NAT egress;
- a private application load balancer reachable only through AWS's CloudFront VPC-origin managed prefix list;
- Fargate tasks with a selectable `ARM64` or `X86_64` Linux runtime platform;
- ECS execution/task roles, CloudWatch logs, ALB health checks compatible with the scratch image, graceful rolling replacement settings, and request-count autoscaling;
- a private, encrypted, versioned S3 bucket with a CloudFront Origin Access Control policy;
- a standard (not multi-tenant) CloudFront distribution, VPC origin, and CloudFront Function using `selectRequestOriginById()` to select S3 or the private ALB;
- immutable one-year browser/cache headers for build-ID-addressed browser assets and manifests; and
- cache-disabled behaviors for actions, APIs, and `/__gobeyond/*` health/runtime diagnostics; and
- a 14-day default retention window for noncurrent static artifacts.

The selector is deliberately data-free in the MVP: `static_route_paths`, `static_route_prefixes`, and `public_asset_paths` are injected into the function when OpenTofu applies. Feed those variables from the generated deployment route trie during release preparation. The build writes every copied `public/` file to `staticAssetPaths`, so images, fonts, and crawler-control files cannot accidentally fall through to Go. The fixed function selects S3 for those paths and the reserved static namespaces; all other paths reach Go. It rewrites an S3 document route such as `/about` to `/about/index.html`.

CloudFront's origin-selection helper can select a configured VPC origin, so the ALB remains private. The function is edge JavaScript, not a Node application runtime.

Dynamic documents and `/_gobeyond/runtime/*` data use the origin's cache headers. A page using `gb.PublicRevalidate(60*time.Second, 5*time.Minute, 24*time.Hour)` is therefore stored by CloudFront while browsers revalidate each navigation. The dynamic cache key includes all cookies, query strings, and `Authorization`; the Go runtime also downgrades cookie, authorization, `Set-Cookie`, and middleware-private responses to `private, no-store`. This is intentionally conservative for a public single-site reference. It avoids cross-visitor reuse without introducing an application cache.

## Validate

Copy the example values and replace the placeholder image URI and public origin:

```bash
cd infra/opentofu
cp terraform.tfvars.example terraform.tfvars
tofu init
tofu fmt -check -recursive
tofu validate
tofu plan
```

`terraform.tfvars` is intentionally ignored by convention; do not put secrets in it. This repository supplies no deploy script. Review the plan, IAM permissions, AWS limits, DNS, image digest, and generated route mapping before an authorized operator applies it.

## Release ordering and build mismatches

The MVP favors safe reload over seamless old-runtime continuity:

1. Build the project and run the Node-free artifact audit.
2. Upload immutable browser assets and manifests under their new build ID. CloudFront forces those paths to `Cache-Control: public, max-age=31536000, immutable`; static documents and static data should carry the route's generated cache policy when uploaded.
3. Push the immutable Go server image, then update the task definition/service and wait for `/__gobeyond/healthz` to be healthy.
4. Update `static_route_paths`/`static_route_prefixes` from the generated route trie and apply the CloudFront configuration.
5. Switch DNS/aliases only after both origins serve the same release.

The Go runtime rejects an incompatible `/_gobeyond/runtime/<build-id>/...` or action request with `409 {"error":"build_mismatch","reload":true}`. Browser clients reload once and never automatically replay an action. This reference retains old S3 versions for the configured static retention window, but it does **not** route old browser runtime/action requests to a former ECS target. That is intentionally deferred beyond the MVP.

## Required application behavior

- Dynamic public documents and runtime-navigation JSON may return an explicit public shared-cache policy such as `gb.PublicRevalidate`; authenticated, API, action, error, and private responses must return `Cache-Control: private, no-store`.
- Canonical URLs and SEO metadata must use the configured public origin, never an untrusted incoming `Host` header.
- The image must expose `GET /__gobeyond/healthz` and `GET /__gobeyond/readyz` on `container_port`.
- The reference forwards the viewer `Host` to the Go service for canonical-host validation. Health endpoints intentionally accept the ALB's internal Host and expose only liveness/readiness state.
- Static assets must never contain secrets: all S3 static objects are publicly readable through CloudFront.
- If `create_nat_gateway = false`, provide equivalent private egress/VPC endpoints for ECR, CloudWatch Logs, and any application dependencies before starting ECS tasks.

## Costs and intentional boundaries

The default creates one NAT gateway because that is the least surprising runnable private-task setup; it is not a low-cost development topology. The reference does not create an ECR repository, DNS records, ACM certificates, WAF, database, secrets, CI deployment credentials, or a production multi-region strategy. Multi-tenant CloudFront, tenant routing, domain onboarding, and durable ISR belong in a separate private hosting platform. Add those explicitly for a real deployment.
