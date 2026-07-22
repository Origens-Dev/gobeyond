# GoBeyond AWS reference

This is an intentionally small, runnable OpenTofu reference for GoBeyond's MVP deployment model:

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
- a CloudFront VPC origin and a CloudFront Function using `selectRequestOriginById()` to select S3 or the private ALB; and
- a 14-day default retention window for noncurrent static artifacts.

The selector is deliberately data-free in the MVP: `static_route_paths`, `static_route_prefixes`, and `public_asset_paths` are injected into the function when OpenTofu applies. Feed those variables from the generated deployment route trie during release preparation. The build writes every copied `public/` file to `staticAssetPaths`, so images, fonts, and crawler-control files cannot accidentally fall through to Go. The fixed function selects S3 for those paths and the reserved static namespaces; all other paths reach Go. It rewrites an S3 document route such as `/about` to `/about/index.html`.

CloudFront's origin-selection helper can select a configured VPC origin, so the ALB remains private. The function is edge JavaScript, not a Node application runtime.

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
2. Upload immutable browser assets, static data, manifests, and static documents under their new build ID. Give immutable assets a long `Cache-Control` lifetime; static documents should use the route's generated cache policy.
3. Push the immutable Go server image, then update the task definition/service and wait for `/__gobeyond/healthz` to be healthy.
4. Update `static_route_paths`/`static_route_prefixes` from the generated route trie and apply the CloudFront configuration.
5. Switch DNS/aliases only after both origins serve the same release.

The Go runtime rejects an incompatible `/_gobeyond/runtime/<build-id>/...` or action request with `409 {"error":"build_mismatch","reload":true}`. Browser clients reload once and never automatically replay an action. This reference retains old S3 versions for the configured static retention window, but it does **not** route old browser runtime/action requests to a former ECS target. That is intentionally deferred beyond the MVP.

## Required application behavior

- Dynamic, authenticated, API, and action responses must return `Cache-Control: private, no-store` unless explicitly designed otherwise.
- Canonical URLs and SEO metadata must use `GOBEYOND_PUBLIC_ORIGIN`, never an untrusted incoming `Host` header.
- The image must expose `GET /__gobeyond/healthz` and `GET /__gobeyond/readyz` on `container_port`.
- The reference forwards the viewer `Host` to the Go service for canonical-host validation. Health endpoints intentionally accept the ALB's internal Host and expose only liveness/readiness state.
- Static assets must never contain secrets: all S3 static objects are publicly readable through CloudFront.
- If `create_nat_gateway = false`, provide equivalent private egress/VPC endpoints for ECR, CloudWatch Logs, and any application dependencies before starting ECS tasks.

## Costs and intentional boundaries

The default creates one NAT gateway because that is the least surprising runnable private-task setup; it is not a low-cost development topology. The reference does not create an ECR repository, DNS records, ACM certificates, WAF, database, secrets, CI deployment credentials, or a production multi-region strategy. Add those explicitly for a real deployment.
