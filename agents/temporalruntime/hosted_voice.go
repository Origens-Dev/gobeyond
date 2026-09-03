package temporalruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/voice"
)

const (
	hostAgentVoiceStartPath = voice.HostedStartPath
	hostAgentVoiceStopPath  = voice.HostedStopPath
)

// HostedVoiceClient is the G5 client for gbhost voice start/stop/PCM over the
// slot-private host-report UDS. It does not implement the Live adapter; it
// only brokers sessions and binary PCM frames for voice-worker / G5b.
type HostedVoiceClient struct {
	httpClient *http.Client
	transport  *http.Transport
	socketPath string
}

type pcmOpenResult struct {
	resp *http.Response
	err  error
}

// NewHostedVoiceClientFromEnv dials GOBEYOND_HOST_REPORT_SOCKET like the
// hosted Temporal agent dispatcher.
func NewHostedVoiceClientFromEnv() (*HostedVoiceClient, error) {
	socketPath := strings.TrimSpace(os.Getenv(EnvHostReportSocket))
	if socketPath == "" {
		return nil, fmt.Errorf("hosted voice client: %s is required", EnvHostReportSocket)
	}
	return NewHostedVoiceClient(socketPath), nil
}

// NewHostedVoiceClient builds a client that dials the given Unix socket path.
func NewHostedVoiceClient(socketPath string) *HostedVoiceClient {
	socketPath = strings.TrimSpace(socketPath)
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &HostedVoiceClient{
		httpClient: &http.Client{Transport: transport},
		transport:  transport,
		socketPath: socketPath,
	}
}

// Start POSTs /v1/agents/voice/start and returns the session token plus PCM
// endpoint. The token is a secret capability — use voice.RedactToken in logs.
func (client *HostedVoiceClient) Start(ctx context.Context, request voice.HostedStartRequest) (voice.HostedStartResponse, error) {
	var response voice.HostedStartResponse
	if client == nil || client.httpClient == nil {
		return response, ErrClosed
	}
	if strings.TrimSpace(request.AgentID) == "" || strings.TrimSpace(request.SessionID) == "" || strings.TrimSpace(request.RunID) == "" {
		return response, errors.New("hosted voice start requires agent_id, session_id, and run_id")
	}
	if err := client.postJSON(ctx, hostAgentVoiceStartPath, request, &response); err != nil {
		return response, err
	}
	if strings.TrimSpace(response.SessionToken) == "" {
		return response, errors.New("hosted voice start returned empty session_token")
	}
	response.PCMEndpoint.Normalize(client.socketPath)
	if err := response.PCMEndpoint.Validate(); err != nil {
		return response, fmt.Errorf("hosted voice start pcm_endpoint_spec: %w", err)
	}
	return response, nil
}

// Stop POSTs /v1/agents/voice/stop. Callers may retry; the server is idempotent.
func (client *HostedVoiceClient) Stop(ctx context.Context, request voice.HostedStopRequest) error {
	if client == nil || client.httpClient == nil {
		return ErrClosed
	}
	if strings.TrimSpace(request.SessionToken) == "" {
		return errors.New("hosted voice stop requires session_token")
	}
	return client.postJSON(ctx, hostAgentVoiceStopPath, request, nil)
}

// OpenPCM opens a bidirectional length-prefixed PCM stream to the endpoint
// returned by Start. Uplink frames are written on the request body; downlink
// frames are read from the response body (POST /v1/agents/voice/pcm/{token}).
func (client *HostedVoiceClient) OpenPCM(ctx context.Context, sessionToken string, spec voice.PCMEndpointSpec) (*HostedPCMStream, error) {
	if client == nil || client.httpClient == nil {
		return nil, ErrClosed
	}
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil, errors.New("hosted voice PCM requires session_token")
	}
	spec.Normalize(client.socketPath)
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	frame := strings.ToLower(strings.TrimSpace(spec.Frame))
	if frame != voice.FrameLengthPrefixedLE && frame != voice.FrameLengthPrefixedLEV2 {
		return nil, fmt.Errorf("hosted voice PCM frame %q is not supported", spec.Frame)
	}

	path := strings.TrimSpace(spec.PCMPath)
	if path == "" {
		path = voice.HostedPCMPath(sessionToken)
	}
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobeyond"+path, pr)
	if err != nil {
		_ = pw.Close()
		return nil, fmt.Errorf("hosted voice PCM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	authHeader := strings.TrimSpace(spec.AuthHeader)
	if authHeader == "" {
		authHeader = voice.DefaultAuthHeader
	}
	req.Header.Set(authHeader, "Bearer "+sessionToken)

	httpClient := client.httpClientForPCM(spec)
	started := make(chan pcmOpenResult, 1)
	go func() {
		resp, err := httpClient.Do(req)
		started <- pcmOpenResult{resp: resp, err: err}
	}()

	return &HostedPCMStream{
		token:      sessionToken,
		pipeWriter: pw,
		started:    started,
		httpClient: httpClient,
		frame:      frame,
	}, nil
}

func (client *HostedVoiceClient) httpClientForPCM(spec voice.PCMEndpointSpec) *http.Client {
	socketPath := strings.TrimSpace(spec.Path)
	if socketPath == "" || socketPath == client.socketPath {
		return client.httpClient
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport}
}

func (client *HostedVoiceClient) postJSON(ctx context.Context, path string, input, output any) error {
	hosted := &hostedAgentClient{client: client.httpClient, transport: client.transport}
	return hosted.post(ctx, path, input, output)
}

// Close releases idle UDS connections.
func (client *HostedVoiceClient) Close() {
	if client != nil && client.transport != nil {
		client.transport.CloseIdleConnections()
	}
}

// HostedPCMStream is a duplex length-prefixed PCM exchange over HTTP UDS.
type HostedPCMStream struct {
	token      string
	pipeWriter *io.PipeWriter
	body       io.ReadCloser
	started    <-chan pcmOpenResult
	httpClient *http.Client
	frame      string

	mu     sync.Mutex
	closed bool
}

// WritePCM encodes and sends one uplink PCM payload.
func (stream *HostedPCMStream) WritePCM(pcm []byte) error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream == nil || stream.closed || stream.pipeWriter == nil {
		return errors.New("hosted voice PCM stream is closed")
	}
	frame, err := voice.EncodeFrame(pcm)
	if err != nil {
		return err
	}
	_, err = stream.pipeWriter.Write(frame)
	return err
}

// ReadPCM reads one downlink PCM payload.
func (stream *HostedPCMStream) ReadPCM() ([]byte, error) {
	frame, err := stream.ReadAudioFrame()
	if err != nil {
		return nil, err
	}
	return frame.Data, nil
}

// ReadAudioFrame reads one downlink audio frame, including interruption and
// turn-completion control markers when the endpoint uses v2 framing.
func (stream *HostedPCMStream) ReadAudioFrame() (voice.AudioFrame, error) {
	if err := stream.ensureResponse(); err != nil {
		return voice.AudioFrame{}, err
	}
	stream.mu.Lock()
	body := stream.body
	closed := stream.closed
	stream.mu.Unlock()
	if closed || body == nil {
		return voice.AudioFrame{}, errors.New("hosted voice PCM stream is closed")
	}
	if stream.frame == voice.FrameLengthPrefixedLEV2 {
		return voice.DecodeAudioFrame(body)
	}
	data, err := voice.DecodeFrame(body)
	return voice.AudioFrame{Data: data}, err
}

func (stream *HostedPCMStream) ensureResponse() error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return errors.New("hosted voice PCM stream is closed")
	}
	if stream.body != nil {
		return nil
	}
	if stream.started == nil {
		return errors.New("hosted voice PCM response is unavailable")
	}
	got := <-stream.started
	if got.err != nil {
		return fmt.Errorf("hosted voice PCM open (token %s): %w", voice.RedactToken(stream.token), got.err)
	}
	if got.resp.StatusCode < http.StatusOK || got.resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(got.resp.Body, 4096))
		_ = got.resp.Body.Close()
		return fmt.Errorf("hosted voice PCM open (token %s) returned status %d", voice.RedactToken(stream.token), got.resp.StatusCode)
	}
	stream.body = got.resp.Body
	return nil
}

// CloseUplink closes the request body so servers that buffer the full upload
// can finish and send the response. Optional for true duplex servers.
func (stream *HostedPCMStream) CloseUplink() error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream == nil || stream.pipeWriter == nil {
		return nil
	}
	err := stream.pipeWriter.Close()
	stream.pipeWriter = nil
	return err
}

// Close ends the uplink pipe and response body. Safe to call multiple times.
func (stream *HostedPCMStream) Close() error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return nil
	}
	stream.closed = true
	var errs []error
	if stream.pipeWriter != nil {
		if err := stream.pipeWriter.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if stream.body != nil {
		if err := stream.body.Close(); err != nil {
			errs = append(errs, err)
		}
	} else if stream.started != nil {
		select {
		case got := <-stream.started:
			if got.resp != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(got.resp.Body, 4096))
				_ = got.resp.Body.Close()
			}
		default:
		}
	}
	if stream.httpClient != nil {
		if transport, ok := stream.httpClient.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	return errors.Join(errs...)
}

// voiceStart / voiceStop keep the same private client used by execute/signal.
func (client *hostedAgentClient) voiceStart(ctx context.Context, request voice.HostedStartRequest) (voice.HostedStartResponse, error) {
	wrapper := &HostedVoiceClient{httpClient: client.client, transport: client.transport}
	return wrapper.Start(ctx, request)
}

func (client *hostedAgentClient) voiceStop(ctx context.Context, request voice.HostedStopRequest) error {
	wrapper := &HostedVoiceClient{httpClient: client.client, transport: client.transport}
	return wrapper.Stop(ctx, request)
}
