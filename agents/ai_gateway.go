package agents

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/go-ai/packages/community/openrouter"
)

const (
	defaultHostReportSocket = "/run/gobeyond/host-report.sock"
	gatewayAPIKey           = "gobeyond-ai-gateway"
	gatewayBaseURL          = "http://gobeyond/v1/ai-proxy"
	gatewayPath             = "/v1/ai-proxy"
)

// hostReportSocketPath stats the configured UDS. RegisterAI / LanguageModel
// at worker start must not dial Cloud Map or otherwise contact the gateway.
func hostReportSocketPath() (string, bool) {
	path := strings.TrimSpace(os.Getenv(EnvHostReportSocket))
	if path == "" {
		path = defaultHostReportSocket
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func gatewayLanguageModel(socketPath, modelID string) ai.LanguageModel {
	return openrouter.New(openrouter.Settings{
		APIKey:  gatewayAPIKey,
		BaseURL: gatewayBaseURL,
		Client:  newGatewayDoer(socketPath),
	}).LanguageModel(modelID)
}

// gatewayDoer POSTs OpenAI-compat chat to /v1/ai-proxy over the host-report
// UDS. The dummy API key is set explicitly so empty Settings cannot pick up
// ambient OPENROUTER_API_KEY. The HTTP client has no overall timeout so
// streaming InvokeModel is not cut off.
type gatewayDoer struct {
	client *http.Client
}

func newGatewayDoer(socketPath string) *gatewayDoer {
	return &gatewayDoer{
		client: &http.Client{
			Transport: &http.Transport{
				DisableCompression: true,
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

func (d *gatewayDoer) Do(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = "http"
	cloned.URL.Host = "gobeyond"
	cloned.URL.Path = gatewayPath
	cloned.URL.RawPath = ""
	cloned.Host = "gobeyond"
	return d.client.Do(cloned)
}
