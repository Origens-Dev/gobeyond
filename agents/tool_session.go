package agents

import (
	"context"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
)

// ToolSessionID returns the conversation identity supplied by the framework
// runtime to handlers created with DefineTool or DefineToolWithCall. It is suitable for session-scoped limits and attribution, not
// authorization. It never reads actor metadata, model input, or caller-provided
// session metadata. Standalone and legacy executions may have no identity;
// callers must handle ok=false rather than inventing a session ID.
func ToolSessionID(ctx context.Context) (id string, ok bool) {
	return toolsession.ID(ctx)
}

// ToolWriteID returns the platform-derived write idempotency key supplied to
// VoiceWrite handlers. It never reads actor metadata, model input, or a
// caller-forged envelope field.
func ToolWriteID(ctx context.Context) (id string, ok bool) {
	return toolsession.WriteID(ctx)
}
