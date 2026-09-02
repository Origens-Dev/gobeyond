package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultHostReportSocket = "/run/gobeyond/host/host-report.sock"

type udsHTTPClient struct {
	client    *http.Client
	transport *http.Transport
	socket    string
}

func newUDSHTTPClient(socketPath string) *udsHTTPClient {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		socketPath = defaultHostReportSocket
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &udsHTTPClient{
		socket: socketPath,
		client: &http.Client{Transport: transport},
		transport: transport,
	}
}

func (c *udsHTTPClient) close() {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}

type udsHTTPResponse struct {
	Status int
	JSON   json.RawMessage
}

func (c *udsHTTPClient) post(ctx context.Context, path string, body any) (udsHTTPResponse, error) {
	if c == nil || c.client == nil {
		return udsHTTPResponse{}, fmt.Errorf("temporal trigger: uds client closed")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return udsHTTPResponse{}, fmt.Errorf("temporal trigger: encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobeyond"+path, bytes.NewReader(payload))
	if err != nil {
		return udsHTTPResponse{}, fmt.Errorf("temporal trigger: request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return udsHTTPResponse{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return udsHTTPResponse{}, fmt.Errorf("temporal trigger: read %s: %w", path, err)
	}
	if len(raw) == 0 {
		return udsHTTPResponse{Status: res.StatusCode}, nil
	}
	return udsHTTPResponse{Status: res.StatusCode, JSON: raw}, nil
}

func parseUDSObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func udsUnavailable(status int, raw json.RawMessage) bool {
	if status == http.StatusServiceUnavailable {
		return true
	}
	obj, err := parseUDSObject(raw)
	if err != nil {
		return false
	}
	fallback, ok := obj["fallback"]
	if !ok {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(fallback)) == "api"
}
