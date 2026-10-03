package project

import (
	"go/ast"
	"go/parser"
	"strings"
	"testing"
)

func TestPlaybackCompilerRequiresStaticLiteralAndFrozenOutput(t *testing.T) {
	source := `agents.DefineTool(agents.ToolConfig{Name:"play_text_message",Description:"Play exact mailbox text",InputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{},"required":[]string{}},VoicePlayback:&agents.VoicePlaybackPolicy{TextField:"text",MessageIDField:"message_id",CreatedAtField:"created_at",TextDigestField:"text_digest",MessageExpiresAtField:"message_expires_at",ExpiresAtField:"expires_at",CompletionToolID:"complete-text-message-playback",MaxTextBytes:4096,MaxResultBytes:8192},OutputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"text":map[string]any{"type":"string","minLength":1,"maxLength":4096},"message_id":map[string]any{"type":"string","minLength":1,"maxLength":128},"created_at":map[string]any{"type":"string","minLength":1,"maxLength":128},"text_digest":map[string]any{"type":"string","minLength":1,"maxLength":128},"message_expires_at":map[string]any{"type":"string","minLength":1,"maxLength":128},"expires_at":map[string]any{"type":"string","minLength":1,"maxLength":128}},"required":[]string{"text","message_id","created_at","text_digest","message_expires_at","expires_at"}}},handler)`
	expr, err := parser.ParseExpr(source)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := parseVoiceTool("play-text-message", expr.(*ast.CallExpr))
	if err != nil || tool == nil || !tool.IsPlayback() || tool.Playback.TextField != "text" {
		t.Fatal("literal playback rejected", err)
	}
	for _, bad := range []string{strings.Replace(source, `TextField:"text"`, `TextField:dynamicField`, 1), strings.Replace(source, `MaxTextBytes:4096`, `MaxTextBytes:bound`, 1), strings.Replace(source, `VoicePlayback:`, `VoiceRemoteRead:&agents.VoiceReadPolicy{MaxResultBytes:4096},VoicePlayback:`, 1)} {
		expr, _ := parser.ParseExpr(bad)
		if _, err := parseVoiceTool("play-text-message", expr.(*ast.CallExpr)); err == nil {
			t.Fatal("dynamic/mixed declaration admitted")
		}
	}
	hidden := `agents.DefineTool(agents.ToolConfig{Name:"complete_text_message_playback",Description:"Record delivery",InputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{},"required":[]string{}},VoicePlaybackCompletion:true},handler)`
	expr, _ = parser.ParseExpr(hidden)
	tool, err = parseVoiceTool("complete-text-message-playback", expr.(*ast.CallExpr))
	if err != nil || tool == nil || !tool.IsPlaybackCompletion() {
		t.Fatal("hidden compiler declaration lost", err)
	}
}
