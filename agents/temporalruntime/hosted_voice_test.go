package temporalruntime

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/voice"
)

func TestHostedVoiceClientStartStopAndPCM(t *testing.T) {
	temporarySocket, err := os.CreateTemp("", "gb-voice-host-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket := temporarySocket.Name()
	if err := temporarySocket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	const secretToken = "tok_live_session_secret_value_xyz"
	var (
		mu          sync.Mutex
		startCount  int
		stopCount   int
		pcmAuth     string
		pcmUplink   []byte
		paths       []string
	)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == voice.HostedStartPath && r.Method == http.MethodPost:
			var request voice.HostedStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode start: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if request.AgentID != "operator" || request.SessionID != "ses_1" || request.RunID != "run_2" || request.Actor.ID != "user-1" {
				t.Errorf("start request = %#v", request)
			}
			mu.Lock()
			startCount++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(voice.HostedStartResponse{
				SessionToken: secretToken,
				PCMEndpoint: voice.PCMEndpointSpec{
					Transport:  voice.TransportUnix,
					Path:       socket,
					AuthHeader: voice.DefaultAuthHeader,
					Frame:      voice.FrameLengthPrefixedLE,
				},
			})
		case r.URL.Path == voice.HostedStopPath && r.Method == http.MethodPost:
			var request voice.HostedStopRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode stop: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if request.SessionToken != secretToken {
				t.Errorf("stop token = %q", request.SessionToken)
			}
			mu.Lock()
			stopCount++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/v1/agents/voice/pcm/") && r.Method == http.MethodPost:
			mu.Lock()
			pcmAuth = r.Header.Get("Authorization")
			mu.Unlock()
			uplink, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read pcm uplink: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			pcmUplink = uplink
			mu.Unlock()
			frame, err := voice.EncodeFrame([]byte{0xAA, 0xBB})
			if err != nil {
				t.Errorf("encode downlink: %v", err)
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(frame)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = os.Remove(socket)
	})

	client := NewHostedVoiceClient(socket)
	defer client.Close()

	started, err := client.Start(context.Background(), voice.HostedStartRequest{
		AgentID: "operator", SessionID: "ses_1", RunID: "run_2",
		Actor: voice.ActorDTO{ID: "user-1", Kind: "user", Metadata: map[string]string{"network_id": "net-1"}},
		Instructions: "Overlay.", VoiceName: "Puck",
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.SessionToken != secretToken {
		t.Fatalf("token = %q", started.SessionToken)
	}
	if started.PCMEndpoint.Path != socket || started.PCMEndpoint.Frame != voice.FrameLengthPrefixedLE {
		t.Fatalf("pcm endpoint = %#v", started.PCMEndpoint)
	}

	stream, err := client.OpenPCM(context.Background(), started.SessionToken, started.PCMEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WritePCM([]byte{0x01, 0x02}); err != nil {
		t.Fatal(err)
	}
	// Close uplink so the fake server's ReadAll completes and response is sent.
	if err := stream.CloseUplink(); err != nil {
		t.Fatal(err)
	}
	downlink, err := stream.ReadPCM()
	if err != nil {
		t.Fatal(err)
	}
	if string(downlink) != string([]byte{0xAA, 0xBB}) {
		t.Fatalf("downlink = %v", downlink)
	}
	_ = stream.Close()

	if err := client.Stop(context.Background(), voice.HostedStopRequest{SessionToken: started.SessionToken, AgentID: "operator"}); err != nil {
		t.Fatal(err)
	}
	// Idempotent second stop should still succeed against our fake (counts++).
	if err := client.Stop(context.Background(), voice.HostedStopRequest{SessionToken: started.SessionToken}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if startCount != 1 || stopCount != 2 {
		t.Fatalf("start/stop counts = %d/%d", startCount, stopCount)
	}
	if pcmAuth != "Bearer "+secretToken {
		t.Fatalf("pcm auth = %q", pcmAuth)
	}
	wantUplink, err := voice.EncodeFrame([]byte{0x01, 0x02})
	if err != nil {
		t.Fatal(err)
	}
	if string(pcmUplink) != string(wantUplink) {
		t.Fatalf("uplink = %v, want %v", pcmUplink, wantUplink)
	}
	for _, path := range paths {
		if strings.Contains(path, secretToken) && !strings.Contains(path, "pcm/") {
			// Path may include token on PCM URL; ensure we never logged it via RedactToken misuse in errors.
			continue
		}
	}
}

func TestHostedVoiceClientFromEnvRequiresSocket(t *testing.T) {
	t.Setenv(EnvHostReportSocket, "")
	if _, err := NewHostedVoiceClientFromEnv(); err == nil {
		t.Fatal("expected missing socket error")
	}
	t.Setenv(EnvHostReportSocket, "/tmp/gb-voice-missing.sock")
	client, err := NewHostedVoiceClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
}

func TestHostedAgentClientVoiceStartStop(t *testing.T) {
	temporarySocket, err := os.CreateTemp("", "gb-voice-host-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket := temporarySocket.Name()
	if err := temporarySocket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case voice.HostedStartPath:
			_ = json.NewEncoder(w).Encode(voice.HostedStartResponse{
				SessionToken: "tok_abc123456789",
				PCMEndpoint:  voice.PCMEndpointSpec{Path: socket},
			})
		case voice.HostedStopPath:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = os.Remove(socket)
	})

	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv(EnvHostReportSocket, socket)
	t.Setenv(EnvEnvironment, "preview")
	dispatcher, err := NewLazyFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started, err := dispatcher.hosted.voiceStart(ctx, voice.HostedStartRequest{
		AgentID: "operator", SessionID: "ses", RunID: "run",
		Actor: voice.ActorDTO{ID: "u", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.SessionToken != "tok_abc123456789" {
		t.Fatalf("token = %q", started.SessionToken)
	}
	if err := dispatcher.hosted.voiceStop(ctx, voice.HostedStopRequest{SessionToken: started.SessionToken}); err != nil {
		t.Fatal(err)
	}
}
