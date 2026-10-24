package runtime

import "net/http"

const (
	BuildIDHeader            = "X-Build-Id"
	BuildErrorHeader         = "X-Build-Error"
	DeploymentRevisionHeader = "X-Deployment-Revision"
)

// legacyDeploymentRevisionHeader is accepted on incoming requests for one
// release while mixed site deploys still send X-GoBeyond-Deployment.
const legacyDeploymentRevisionHeader = "X-GoBeyond-Deployment"

func requestDeploymentRevision(header http.Header) string {
	if value := header.Get(DeploymentRevisionHeader); value != "" {
		return value
	}
	return header.Get(legacyDeploymentRevisionHeader)
}
