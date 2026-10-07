package temporalruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/internal/playbackcompletion"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

const playbackCompletionPath = "/v1/voice-session/playback/completion"

// This fixed slot-private relay authenticates the current environment at the
// control plane. Neither an argument nor a decoded receipt can install authority.
func resolvePlaybackCompletion(ctx context.Context, grant, sourceCallID string) (voicecontract.PlaybackCompletionReceipt, error) {
	var out struct {
		Receipt voicecontract.PlaybackCompletionReceipt `json:"receipt"`
	}
	socket := strings.TrimSpace(os.Getenv(EnvHostReportSocket))
	if socket == "" {
		return out.Receipt, errors.New("playback completion host socket required")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, err := json.Marshal(struct {
		Grant      string `json:"grant"`
		ToolCallID string `json:"tool_call_id"`
	}{grant, sourceCallID})
	if err != nil {
		return out.Receipt, err
	}
	transport := &http.Transport{DisableCompression: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("playback completion redirect refused") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobeyond"+playbackCompletionPath, bytes.NewReader(body))
	if err != nil {
		return out.Receipt, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return out.Receipt, errors.New("playback completion unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out.Receipt, errors.New("playback completion rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, voicecontract.MaxEnvelopeBytes+1))
	if err != nil || voicecontract.Decode(raw, voicecontract.MaxEnvelopeBytes, &out) != nil {
		return out.Receipt, errors.New("playback completion invalid response")
	}
	return out.Receipt, nil
}

func executeVoicePlaybackCompletionActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	fail := func() (VoiceSessionExecuteToolResult, error) {
		return VoiceSessionExecuteToolResult{}, errors.New("authenticated playback completion unavailable")
	}
	r := req.HiddenCompletion
	if ctx == nil || ctx.Err() != nil || r == nil || r.Validate() != nil || !exclusiveVoiceDispatch(req) || !(voicecontract.IsGenericBudgetPolicy(req.BudgetPolicy) || req.BudgetPolicy == voicecontract.BudgetPolicyOperatorMailboxPlaybackV1) || len(req.Grant) == 0 || len(req.Grant) > 8192 || len(req.Input) != 0 && string(req.Input) != "{}" {
		return fail()
	}
	c := r.Context
	if req.AgentID != "" && req.AgentID != c.AgentID || req.ActorID != "" && req.ActorID != c.ActorID || req.ActorKind != "" && req.ActorKind != c.ActorKind || req.NetworkID != "" && req.NetworkID != c.NetworkID || req.ToolCallID != "" && req.ToolCallID != r.ToolCallID || req.ManifestDigest != "" && req.ManifestDigest != c.ManifestDigest || req.AgentRevision != "" && req.AgentRevision != c.AgentRevision {
		return fail()
	}
	registry := ProcessVoiceRegistry()
	if registry == nil {
		return fail()
	}
	raw, digest, ok := registry.Manifest(c.AgentID)
	if !ok || digest != c.ManifestDigest {
		return fail()
	}
	var manifest voicecontract.Manifest
	if voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &manifest) != nil || manifest.CompiledRevision != c.AgentRevision || voicecontract.ValidateBudgetPolicy(req.BudgetPolicy, c, manifest) != nil {
		return fail()
	}
	var spec *voicecontract.Tool
	var sourceID string
	for i := range manifest.Tools {
		t := &manifest.Tools[i]
		if t.ID == r.ToolID && t.IsPlaybackCompletion() {
			spec = t
		}
		if t.IsPlayback() && t.Playback != nil && t.Playback.CompletionToolID == r.ToolID {
			sourceID = t.ID
		}
	}
	if spec == nil || sourceID == "" || req.ToolName != "" && req.ToolName != spec.Name {
		return fail()
	}
	input, err := voicecontract.ValidateToolInput(*spec, json.RawMessage(`{}`))
	if err != nil {
		return fail()
	}
	definition, ok := registry.Definition(c.AgentID)
	if !ok {
		return fail()
	}
	tool, ok := definition.AI.Tools[r.ToolID]
	if !ok || tool.Execute == nil || !agents.VoicePlaybackCompletionPolicy(tool) {
		return fail()
	}
	receipt, err := resolvePlaybackCompletion(ctx, req.Grant, r.SourceToolCallID)
	if err != nil {
		return fail()
	}
	if ctx.Err() != nil || receipt.Validate(time.Now().UTC()) != nil || receipt.Context != c || receipt.ToolCallID != r.SourceToolCallID || receipt.ToolID != sourceID || receipt.CompletionToolID != r.ToolID || receipt.ClipID != r.ClipID {
		return fail()
	}
	actor, err := voiceScopedActor(c, req.Grant, "", 0, true)
	if err != nil {
		return fail()
	}
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return fail()
	}
	invocation := playbackcompletion.With(ctx, receipt)
	result, err := tool.Execute(invocation, ai.ToolCall{ToolCallID: r.ToolCallID, ToolName: spec.Name, Input: args}, ai.ToolExecutionOptions{Context: toolsession.ExecutionContext(actor, c.SessionID)})
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	raw, err = json.Marshal(result)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	return VoiceSessionExecuteToolResult{Result: raw}, nil
}
