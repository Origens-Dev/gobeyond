# Security policy

GoBeyond is pre-release software. Do not deploy it for sensitive production
workloads without an application-specific security review.

Please report suspected vulnerabilities privately by emailing
[andrew.holbrook@gmail.com](mailto:andrew.holbrook@gmail.com) rather than
opening a public issue. Include the affected revision, route shape, request,
impact, and a minimal reproduction when possible. Do not include real
credentials, cookies, personal data, or production secrets.

Only the latest alpha line receives security fixes. A report is considered in
scope when it affects the compiler trust boundary, contextual escaping,
generated contracts, metadata/JSON-LD serialization, route/rewrite handling,
CSRF/origin checks, internal headers, caching of private data, build mismatch
safety, or the Node-free production artifact.

Application sanitization remains an application responsibility. Construct
`renderplan.SafeHTML` only from output that has been sanitized for the intended
HTML context.
