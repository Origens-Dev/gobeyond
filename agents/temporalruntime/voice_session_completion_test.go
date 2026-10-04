package temporalruntime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func completionActivityFixture(t *testing.T, handler func(context.Context, agents.Actor, map[string]any) (string, error)) (VoiceSessionExecuteToolInput, voicecontract.PlaybackCompletionReceipt) {
	t.Helper()
	r := sourceRequestFixture(t)
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}, "required": []string{}}
	rawSchema, _ := json.Marshal(schema)
	rawSchema, _ = voicecontract.CanonicalJSON(rawSchema, 4096)
	policy := voicecontract.PlaybackPolicy{CompletionToolID: "complete-text-message-playback"}
	hidden := voicecontract.Tool{ID: policy.CompletionToolID, Name: "complete_text_message_playback", ExecutionKind: "playback_completion", InputSchema: rawSchema, SchemaDigest: voicecontract.Digest(rawSchema)}
	m := voicecontract.Manifest{Version: voicecontract.Version, CompiledRevision: r.Context.AgentRevision, BudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1, Tools: []voicecontract.Tool{{ID: "play-text-message", ExecutionKind: "playback", Playback: &policy}, hidden, {ID: "list-text-messages", ExecutionKind: "read"}, {ID: "get-text-message", ExecutionKind: "read"}, {ID: "dial-contact", ExecutionKind: "control"}}}
	raw, _ := json.Marshal(m)
	digest := voicecontract.Digest(raw)
	r.Context.ManifestDigest = digest
	tool := agents.DefineTool(agents.ToolConfig{Name: hidden.Name, InputSchema: schema, VoicePlaybackCompletion: true}, handler)
	reg := NewVoiceRegistry()
	reg.definitions[r.Context.AgentID] = agents.DefineAI(agents.AIConfig{Revision: r.Context.AgentRevision, Tools: map[string]agents.AITool{hidden.ID: tool}})
	reg.manifests = map[string][]byte{r.Context.AgentID: raw}
	reg.manifestDigests = map[string]string{r.Context.AgentID: digest}
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(old) })
	h := voicecontract.HiddenCompletionRequest{Version: r.Version, Context: r.Context, ToolID: hidden.ID, SourceToolCallID: r.ToolCallID, ToolCallID: voicecontract.PlaybackCompletionCallID(r.Context, r.ToolCallID), ClipID: voicecontract.PlaybackClipID(r.Context, r.ToolCallID)}
	now := time.Now().UTC()
	receipt := voicecontract.PlaybackCompletionReceipt{Context: r.Context, ToolCallID: r.ToolCallID, ToolID: r.ToolID, CompletionToolID: hidden.ID, MessageID: "message", MessageCreatedAt: now.Add(-time.Hour), MessageExpiresAt: now.Add(time.Hour), TextDigest: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute), ClipID: h.ClipID}
	return VoiceSessionExecuteToolInput{HiddenCompletion: &h, Grant: "opaque", BudgetPolicy: m.BudgetPolicy}, receipt
}

func completionSocket(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gb-pc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "host.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	t.Setenv(EnvHostReportSocket, socket)
}

func TestPlaybackCompletionAuthenticatesFixedRelayBeforeHiddenTool(t *testing.T) {
	calls := 0
	var req VoiceSessionExecuteToolInput
	var receipt voicecontract.PlaybackCompletionReceipt
	req, receipt = completionActivityFixture(t, func(ctx context.Context, actor agents.Actor, input map[string]any) (string, error) {
		calls++
		got, ok := agents.PlaybackCompletionFromContext(ctx)
		if !ok || got != receipt || len(input) != 0 || actor.Metadata["voice_session_grant"] != "opaque" || actor.Metadata["transport_call_id"] != receipt.Context.TransportCallID || actor.Metadata["hop_id"] != receipt.Context.HopID {
			t.Fatal("trusted receipt or grant projection lost")
		}
		if id, ok := agents.ToolSessionID(ctx); !ok || id != receipt.Context.SessionID {
			t.Fatal("session lost")
		}
		return "read", nil
	})
	requests := 0
	completionSocket(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.URL.Path != playbackCompletionPath {
			t.Error("nonfixed route")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["grant"] != "opaque" || body["tool_call_id"] != receipt.ToolCallID {
			t.Error("untrusted receipt/request shape")
		}
		json.NewEncoder(w).Encode(map[string]any{"receipt": receipt})
	})
	for i := 0; i < 2; i++ {
		out, err := VoiceSessionExecuteToolActivity(context.Background(), req)
		if err != nil || string(out.Result) != `"read"` {
			t.Fatal(out, err)
		}
	}
	if calls != 2 || requests != 2 {
		t.Fatal("retry must freshly authenticate durable idempotent completion")
	}
	for _, mutation := range []func(*VoiceSessionExecuteToolInput){func(r *VoiceSessionExecuteToolInput) { r.Input = []byte(`{"receipt":true}`) }, func(r *VoiceSessionExecuteToolInput) { r.ActorID = "foreign" }, func(r *VoiceSessionExecuteToolInput) { r.BudgetPolicy = voicecontract.BudgetPolicyOperatorMailboxV1 }, func(r *VoiceSessionExecuteToolInput) { r.Grant = "" }} {
		changed := req
		mutation(&changed)
		if _, err := VoiceSessionExecuteToolActivity(context.Background(), changed); err == nil {
			t.Fatal("forged dispatch accepted")
		}
	}
	if calls != 2 || requests != 2 {
		t.Fatal("bad input reached resolver or mutation")
	}
}

func TestPlaybackCompletionRejectsAlteredExpiredAndMissingProof(t *testing.T) {
	for _, kind := range []string{"context", "clip", "source", "pair", "digest", "expired", "message-expired", "unknown", "interrupted", "redirect", "oversized", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			req, receipt := completionActivityFixture(t, func(context.Context, agents.Actor, map[string]any) (string, error) { calls++; return "read", nil })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completionSocket(t, func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "context":
					receipt.Context.Generation++
				case "clip":
					receipt.ClipID = strings.Repeat("b", 64)
				case "source":
					receipt.ToolCallID = "other"
				case "pair":
					receipt.CompletionToolID = "other"
				case "digest":
					receipt.TextDigest = "wrong"
				case "expired":
					receipt.ExpiresAt = time.Now().Add(-time.Second)
				case "message-expired":
					receipt.MessageExpiresAt = time.Now().Add(-time.Second)
				case "unknown":
					w.Write([]byte(`{"receipt":{},"trusted":true}`))
					return
				case "interrupted":
					w.WriteHeader(http.StatusConflict)
					return
				case "redirect":
					http.Redirect(w, r, "http://example.invalid", http.StatusTemporaryRedirect)
					return
				case "oversized":
					w.Write([]byte(strings.Repeat("x", voicecontract.MaxEnvelopeBytes+1)))
					return
				case "cancel":
					cancel()
				}
				json.NewEncoder(w).Encode(map[string]any{"receipt": receipt})
			})
			if _, err := VoiceSessionExecuteToolActivity(ctx, req); err == nil || calls != 0 {
				t.Fatal("unverified completion invoked hidden mutation")
			}
		})
	}
}

func TestPlaybackCompletionNoHostedSocketFailsClosed(t *testing.T) {
	req, _ := completionActivityFixture(t, func(context.Context, agents.Actor, map[string]any) (string, error) {
		t.Fatal("unhosted mutation")
		return "", nil
	})
	t.Setenv(EnvHostReportSocket, "")
	t.Setenv("GOBEYOND_API_URL", "https://example.invalid")
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); err == nil {
		t.Fatal("public API fallback")
	}
}
