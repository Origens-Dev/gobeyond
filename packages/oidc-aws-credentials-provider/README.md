# @go-beyond/oidc-aws-credentials-provider

AWS SDK v3 credentials backed by GoBeyond OIDC and customer-managed IAM
roles. The provider lazily exchanges a renewable GoBeyond token with STS and
refreshes temporary credentials before expiry.

```ts
import { awsCredentialsProvider } from '@go-beyond/oidc-aws-credentials-provider'
import { S3Client } from '@aws-sdk/client-s3'

const s3 = new S3Client({
  credentials: awsCredentialsProvider({
    roleArn: 'arn:aws:iam::123456789012:role/customer-gobeyond-readonly',
  }),
})
```

The role trust policy must use the exact GoBeyond host-level issuer, require
`aud=sts.amazonaws.com`, and constrain the immutable subject
`owner:{organization_id}:project:{project_id}:environment:{environment_id}`.
