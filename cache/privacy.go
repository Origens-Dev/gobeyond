package cache

import "net/http"

// Header names checked by IsPrivateRequest / IsPrivateResponse. AuthContextHeader
// and OIDCTokenHeader mirror middleware/proxy's contract: AuthContextHeader
// is proxy.AuthContextHeader (asserted exclusively by the middleware hop) and
// OIDCTokenHeader is "X-Origens-Oidc-Token" from proxy.PreservedHeaders. This
// package does not import middleware/proxy to avoid pulling its reverse-proxy
// dependency into every cache consumer; the header names are a stable wire
// contract, not an implementation detail of that package.
const (
	AuthContextHeader = "X-Gobeyond-Auth-Context"
	OIDCTokenHeader   = "X-Origens-Oidc-Token"
)

// IsPrivateRequest is the Get-gate (Locked decision 6): it reports whether
// request headers carry any signal that this request is bound to viewer
// identity. It must be evaluated before any cache read, and its result
// should be captured once, before middleware or a loader can strip or add
// headers, so later privacy checks stay consistent for the whole request.
//
// A request is private when it carries a Cookie, an Authorization header, a
// non-empty AuthContextHeader, or a non-empty OIDCTokenHeader. Forged or
// stray copies of these headers still trip the gate: fail-closed privacy
// treats "we cannot prove this is anonymous" as private (design principle:
// forged auth headers => private).
func IsPrivateRequest(header http.Header) bool {
	if header == nil {
		return false
	}
	return header.Get("Cookie") != "" ||
		header.Get("Authorization") != "" ||
		header.Get(AuthContextHeader) != "" ||
		header.Get(OIDCTokenHeader) != ""
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
