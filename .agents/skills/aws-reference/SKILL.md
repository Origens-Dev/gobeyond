---
name: aws-reference
description: >
  Work on GoBeyond's AWS OpenTofu reference deployment. Use for CloudFront,
  private S3 assets, private ALB/ECS Go runtime, route manifests, deployment
  ordering, asset retention, or Node-free production image validation.
user-invocable: false
---

# Use the AWS reference

Use this skill when changing or validating the optional AWS deployment
reference. It is not a hidden framework deploy command.

1. Read the generated route manifest and classify documents, static data,
   browser assets, runtime payloads, and actions before changing routing.
2. Keep assets private in S3 behind CloudFront origin access control. The Go
   runtime stays behind a private ALB/ECS service.
3. Preserve build IDs in cache keys and immutable asset paths. Runtime/action
   mismatches must return the typed mismatch response before loaders run.
4. Upload new immutable assets before switching public routing; validate the
   new Go target before cutover. Retain old assets for the configured window.
5. Keep secrets in AWS-managed configuration, never static props, plans, or
   client manifests.

```bash
tofu fmt -check
tofu validate
tofu plan
pnpm build
```

Verify CloudFront cannot access arbitrary S3 objects, the ALB is not public,
the final image has no Node binary, and rollback results in a safe reload.
See `docs/guides/aws-reference.md`.
