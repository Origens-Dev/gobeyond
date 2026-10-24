package runtime

import "net/http"

const (
	BuildIDHeader            = "X-Build-Id"
	BuildErrorHeader         = "X-Build-Error"
	DeploymentRevisionHeader = "X-Deployment-Revision"
)

// TODO(follow-up): remove one-release dual-write of these names after sites
// rebuild onto this release and gobeyond-internal#1989's gbhost/Dispatch
// changes deploy. Dual-read of incoming request headers can drop at the same
// time.
const (
	legacyBuildIDHeader            = "X-GoBeyond-Build"
	legacyBuildErrorHeader         = "X-GoBeyond-Error"
	legacyDeploymentRevisionHeader = "X-GoBeyond-Deployment"
)

func setBuildIDHeader(header http.Header, value string) {
	header.Set(BuildIDHeader, value)
	header.Set(legacyBuildIDHeader, value) // TODO(follow-up): remove legacy dual-write
}

func setBuildErrorHeader(header http.Header, value string) {
	header.Set(BuildErrorHeader, value)
	header.Set(legacyBuildErrorHeader, value) // TODO(follow-up): remove legacy dual-write
}

func setDeploymentRevisionHeader(header http.Header, value string) {
	header.Set(DeploymentRevisionHeader, value)
	header.Set(legacyDeploymentRevisionHeader, value) // TODO(follow-up): remove legacy dual-write
}

func requestDeploymentRevision(header http.Header) string {
	if value := header.Get(DeploymentRevisionHeader); value != "" {
		return value
	}
	return header.Get(legacyDeploymentRevisionHeader)
}
