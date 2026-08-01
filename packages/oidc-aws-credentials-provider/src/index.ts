import {
  AssumeRoleWithWebIdentityCommand,
  STSClient,
  type STSClientConfig,
} from '@aws-sdk/client-sts'
import type { AwsCredentialIdentity, AwsCredentialIdentityProvider } from '@aws-sdk/types'
import {
  AWS_STS_AUDIENCE,
  getGoBeyondOidcToken,
} from '@go-beyond/oidc'

export type AwsCredentialsProviderOptions = {
  roleArn: string
  audience?: string
  request?: Request
  roleSessionName?: string
  durationSeconds?: number
  clientConfig?: STSClientConfig
}

function sessionName(value?: string): string {
  const name = value?.trim() || 'gobeyond-oidc'
  if (!/^[A-Za-z0-9+=,.@_-]{2,64}$/.test(name)) {
    throw new Error('@go-beyond/oidc-aws-credentials-provider: invalid roleSessionName')
  }
  return name
}

export function awsCredentialsProvider(
  options: AwsCredentialsProviderOptions,
): AwsCredentialIdentityProvider {
  const roleArn = options.roleArn?.trim()
  if (!roleArn) {
    throw new Error('@go-beyond/oidc-aws-credentials-provider: roleArn is required')
  }
  const audience = options.audience?.trim() || AWS_STS_AUDIENCE
  const roleSession = sessionName(options.roleSessionName)
  const client = new STSClient(options.clientConfig ?? {})
  let cached: AwsCredentialIdentity | undefined
  let cachedExpiration = 0

  return async () => {
    const now = Date.now()
    if (cached && cachedExpiration > now + 60_000) {
      return cached
    }

    const token = await getGoBeyondOidcToken({
      request: options.request,
      audience,
    })
    const result = await client.send(new AssumeRoleWithWebIdentityCommand({
      RoleArn: roleArn,
      RoleSessionName: roleSession,
      WebIdentityToken: token,
      DurationSeconds: options.durationSeconds,
    }))
    const credentials = result.Credentials
    if (!credentials?.AccessKeyId || !credentials.SecretAccessKey || !credentials.SessionToken) {
      throw new Error('@go-beyond/oidc-aws-credentials-provider: STS returned incomplete credentials')
    }
    cachedExpiration = credentials.Expiration?.getTime() || now + 5 * 60_000
    cached = {
      accessKeyId: credentials.AccessKeyId,
      secretAccessKey: credentials.SecretAccessKey,
      sessionToken: credentials.SessionToken,
      expiration: credentials.Expiration,
    }
    return cached
  }
}
