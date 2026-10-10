package agents

import (
	"context"

	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
)

// ResourceBinding is an opaque application resource projection supplied to
// VoiceWrite handlers. It is copied from a verified grant, never from the
// HTTP body, model arguments, or actor metadata.
//
// Interim grant mapping (documented boundary, not a product contract):
// Kind is grant Scope.Kind. Admitted v2 agent scopes copy LineID →
// ResourceID; admitted legacy screener scopes copy DIDID → AlternateID.
// One grant never carries both. The SDK does not name mailbox, line, or
// DID fields; the application maps this binding onto its own resources.
type ResourceBinding = toolsession.ResourceBinding

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

// ToolResourceBinding returns the grant-sourced opaque resource projection
// supplied to VoiceWrite handlers. It never reads actor metadata, model
// input, or a caller-forged envelope field.
func ToolResourceBinding(ctx context.Context) (ResourceBinding, bool) {
	return toolsession.ResourceBindingFrom(ctx)
}

// ToolWriteContext is the framework projection for VoiceWrite handlers.
// Session and write identity are read with ToolSessionID / ToolWriteID;
// the opaque binding is read with ToolResourceBinding. None of those
// values are placed on actor metadata.
func ToolWriteContext(actor Actor, sessionID, writeID string, binding ResourceBinding) map[string]any {
	return toolsession.ExecutionContextWithWrite(actor, sessionID, writeID, binding)
}
