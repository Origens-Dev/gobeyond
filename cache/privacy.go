package cache

import "net/http"

// Header names shared with the hosted request pipeline. AuthContextHeader is
// asserted exclusively by application middleware and represents viewer identity.
// WorkloadIdentityHeader carries a platform-issued credential for the deployed
// application itself; it does not identify the viewer and therefore must not
// make otherwise-public content private.
//
// The header names are a stable wire contract, not an implementation detail of
// any transport package.
const (
	AuthContextHeader      = "X-Gobeyond-Auth-Context"
	WorkloadIdentityHeader = "X-Origens-Oidc-Token"

	// OIDCTokenHeader is retained for source compatibility. The token is
	// workload identity, not a viewer-privacy signal.
	// Deprecated: use WorkloadIdentityHeader.
	OIDCTokenHeader = WorkloadIdentityHeader
)

// IsPrivateRequest is the Get-gate (Locked decision 6): it reports whether
// request headers carry any signal that this request is bound to viewer
// identity. It must be evaluated before any cache read, and its result
// should be captured once, before middleware or a loader can strip or add
// headers, so later privacy checks stay consistent for the whole request.
//
// A request is private when it carries a Cookie, an Authorization header, or a
// non-empty AuthContextHeader. Forged or stray copies of viewer-auth headers
// still trip the gate: fail-closed privacy treats "we cannot prove this is
// anonymous" as private (design principle: forged auth headers => private).
//
// WorkloadIdentityHeader is intentionally excluded. Hosting layers inject it
// after stripping any inbound copy, and it authenticates the application to
// downstream services rather than personalizing the response for a viewer.
func IsPrivateRequest(header http.Header) bool {
	if header == nil {
		return false
	}
	return header.Get("Cookie") != "" ||
		header.Get("Authorization") != "" ||
		header.Get(AuthContextHeader) != ""
}

// IsPrivateResponse is the Set-gate (Locked decision 6): the Get-gate signals
// plus the loaded response's Set-Cookie header. A response that mints a
// cookie establishes viewer identity even when the inbound request carried
// none, so anything gated on IsPrivateResponse (writing to a cache layer,
// serving a public Cache-Control) must fail closed here too.
func IsPrivateResponse(requestHeader, responseHeader http.Header) bool {
	if IsPrivateRequest(requestHeader) {
		return true
	}
	return len(responseHeader.Values("Set-Cookie")) > 0
}
