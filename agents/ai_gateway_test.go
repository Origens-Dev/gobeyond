package agents

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

func TestLanguageModelCatalogIDsUseGateway(t *testing.T) {
	socket, requests := startGatewaySocket(t)
	t.Setenv(EnvHostReportSocket, socket)
	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv("OPENROUTER_API_KEY", "ambient-openrouter-key")

	for _, modelRef := range []string{"openai/gpt-4o-mini", "google/gemini-2.5-flash", "x-ai/grok-4.6"} {
		t.Run(modelRef, func(t *testing.T) {
			definition := DefineAI(AIConfig{Model: modelRef})
			model, via, err := definition.resolveLanguageModel()
			if err != nil {
				t.Fatal(err)
			}
			if via != languageModelViaGateway {
				t.Fatalf("via = %q, want %s", via, languageModelViaGateway)
			}
			if got := model.ModelID(); got != modelRef {
				t.Fatalf("model id = %q", got)
			}
			if _, err := model.DoGenerate(context.Background(), ai.LanguageModelCallOptions{
				Prompt: []ai.Message{ai.UserMessage("hi")},
			}); err != nil {
				t.Fatal(err)
			}
			req := lastGatewayRequest(t, requests)
			if req.Path != gatewayPath {
				t.Fatalf("path = %q, want %s", req.Path, gatewayPath)
			}
			if req.Authorization != "Bearer "+gatewayAPIKey {
				t.Fatalf("authorization = %q", req.Authorization)
			}
			if req.Authorization == "Bearer ambient-openrouter-key" {
				t.Fatal("ambient OPENROUTER_API_KEY leaked onto the gateway path")
			}
			if req.Model != modelRef {
				t.Fatalf("posted model = %q", req.Model)
			}
		})
	}
}

func TestLanguageModelOpenRouterPrefixUsesSDK(t *testing.T) {
	socket, requests := startGatewaySocket(t)
	t.Setenv(EnvHostReportSocket, socket)
	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv("OPENROUTER_API_KEY", "ambient-openrouter-key")

	model, via, err := DefineAI(AIConfig{Model: "openrouter/openai/gpt-4o-mini"}).resolveLanguageModel()
	if err != nil {
		t.Fatal(err)
	}
	if via != languageModelViaLegacy {
		t.Fatalf("via = %q, want %s", via, languageModelViaLegacy)
	}
	if got := model.ModelID(); got != "openai/gpt-4o-mini" {
		t.Fatalf("model id = %q", got)
	}
	if n := requests.count(); n != 0 {
		t.Fatalf("legacy OpenRouter path dialed the gateway %d times", n)
	}
}

func TestLanguageModelHostedEnvKeyStillUsesGatewayUnlessInference(t *testing.T) {
	socket, requests := startGatewaySocket(t)
	t.Setenv(EnvHostReportSocket, socket)
	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv("OPENROUTER_API_KEY", "ambient-openrouter-key")

	_, via, err := DefineAI(AIConfig{Model: "openai/gpt-4o-mini"}).resolveLanguageModel()
	if err != nil {
		t.Fatal(err)
	}
	if via != languageModelViaGateway {
		t.Fatalf("hosted catalog id via = %q", via)
	}

	model, via, err := DefineAI(AIConfig{Model: "openai/gpt-4o-mini", Inference: "openrouter"}).resolveLanguageModel()
	if err != nil {
		t.Fatal(err)
	}
	if via != languageModelViaInference {
		t.Fatalf("inference via = %q, want %s", via, languageModelViaInference)
	}
	if got := model.ModelID(); got != "openai/gpt-4o-mini" {
		t.Fatalf("inference model id = %q", got)
	}
	if n := requests.count(); n != 0 {
		t.Fatalf("Inference bypass dialed the gateway %d times", n)
	}
}

func TestLanguageModelLocalEnvKeyWithoutSocket(t *testing.T) {
	t.Setenv(EnvHostReportSocket, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(EnvHostedRuntime, "")
	t.Setenv("OPENROUTER_API_KEY", "local-openrouter-key")

	model, via, err := DefineAI(AIConfig{Model: "openai/gpt-4o-mini"}).resolveLanguageModel()
	if err != nil {
		t.Fatal(err)
	}
	if via != languageModelViaLocalEnv {
		t.Fatalf("via = %q, want %s", via, languageModelViaLocalEnv)
	}
	if got := model.ModelID(); got != "openai/gpt-4o-mini" {
		t.Fatalf("model id = %q", got)
	}
}

func TestLanguageModelHostedWithoutSocketFailsClosed(t *testing.T) {
	t.Setenv(EnvHostReportSocket, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(EnvHostedRuntime, "1")
	t.Setenv("OPENROUTER_API_KEY", "ambient-openrouter-key")

	_, via, err := DefineAI(AIConfig{Model: "openai/gpt-4o-mini"}).resolveLanguageModel()
	if err == nil || via != "" || !strings.Contains(err.Error(), "host-report socket") {
		t.Fatalf("hosted missing socket err = %v via = %q", err, via)
	}
}

func TestLanguageModelDoesNotDialGatewayAtResolve(t *testing.T) {
	socket := shortSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var accepted atomic.Bool
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted.Store(true)
			_ = conn.Close()
		}
	}()
	t.Setenv(EnvHostReportSocket, socket)
	t.Setenv("OPENROUTER_API_KEY", "")

	if _, via, err := DefineAI(AIConfig{Model: "openai/gpt-4o-mini"}).resolveLanguageModel(); err != nil || via != languageModelViaGateway {
		t.Fatalf("resolve err = %v via = %q", err, via)
	}
	time.Sleep(50 * time.Millisecond)
	if accepted.Load() {
		t.Fatal("LanguageModel dialed the host-report socket at resolve time")
	}
}

func TestLanguageModelRejectsUnsupportedInference(t *testing.T) {
	t.Setenv(EnvHostReportSocket, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(EnvHostedRuntime, "")
	t.Setenv("OPENROUTER_API_KEY", "")

	for _, inference := range []string{"grok", "x-ai", "openai", "google"} {
		_, _, err := DefineAI(AIConfig{Model: "x-ai/grok-4.6", Inference: inference}).resolveLanguageModel()
		if err == nil || !strings.Contains(err.Error(), "Inference") {
			t.Fatalf("inference %q err = %v", inference, err)
		}
	}
}

func TestLanguageModelDoesNotTreatCatalogVendorsAsBuiltInProviders(t *testing.T) {
	t.Setenv(EnvHostReportSocket, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(EnvHostedRuntime, "")
	t.Setenv("OPENROUTER_API_KEY", "")

	for _, modelRef := range []string{"openai/gpt-4o-mini", "google/gemini-2.5-flash", "x-ai/grok-4.6", "grok/grok-4.6"} {
		_, via, err := DefineAI(AIConfig{Model: modelRef}).resolveLanguageModel()
		if err == nil || via == languageModelViaLegacy {
			t.Fatalf("%s resolved as built-in provider: via = %q err = %v", modelRef, via, err)
		}
	}
}

type recordedGatewayRequest struct {
	Path          string
	Authorization string
	Model         string
}

type gatewayRequestLog struct {
	mu       sync.Mutex
	requests []recordedGatewayRequest
}

func (log *gatewayRequestLog) add(req recordedGatewayRequest) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.requests = append(log.requests, req)
}

func (log *gatewayRequestLog) count() int {
	log.mu.Lock()
	defer log.mu.Unlock()
	return len(log.requests)
}

func lastGatewayRequest(t *testing.T, log *gatewayRequestLog) recordedGatewayRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		log.mu.Lock()
		n := len(log.requests)
		var req recordedGatewayRequest
		if n > 0 {
			req = log.requests[n-1]
		}
		log.mu.Unlock()
		if n > 0 {
			return req
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("gateway received no request")
	return recordedGatewayRequest{}
}

func startGatewaySocket(t *testing.T) (string, *gatewayRequestLog) {
	t.Helper()
	socket := shortSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	log := &gatewayRequestLog{}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &payload)
			log.add(recordedGatewayRequest{
				Path:          r.URL.Path,
				Authorization: r.Header.Get("Authorization"),
				Model:         payload.Model,
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"id":"chatcmpl_test",
				"model":"`+payload.Model+`",
				"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)
		}),
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket, log
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "gb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "host.sock")
}
