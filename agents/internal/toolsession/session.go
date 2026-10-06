// Package toolsession carries runtime-owned conversation identity to tools.
// It is internal so authored agents can read identity without minting it.
package toolsession

import (
	"context"
	"strings"
)

const serializedKey = "gobeyondSessionID"
const writeIDKey = "gobeyondWriteID"

type contextKey struct{}
type writeContextKey struct{}

// WithID replaces any inherited identity, including when id is empty.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, strings.TrimSpace(id))
}

func ID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, _ := ctx.Value(contextKey{}).(string)
	return id, id != ""
}

// WithWriteID replaces any inherited write idempotency key, including when id is empty.
func WithWriteID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, writeContextKey{}, strings.TrimSpace(id))
}

func WriteID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, _ := ctx.Value(writeContextKey{}).(string)
	return id, id != ""
}

// ExecutionContext is serialized by the durable SDK along with the actor.
// Neither actor metadata nor session metadata supplies the identity.
func ExecutionContext(actor any, id string) map[string]any {
	return map[string]any{"gobeyondActor": actor, serializedKey: strings.TrimSpace(id)}
}

// ExecutionContextWithWrite adds a platform-derived write idempotency key.
// Callers and model arguments cannot populate this projection.
func ExecutionContextWithWrite(actor any, sessionID, writeID string) map[string]any {
	fields := ExecutionContext(actor, sessionID)
	fields[writeIDKey] = strings.TrimSpace(writeID)
	return fields
}

// Bind restores only the reserved framework projection, not client metadata.
// Legacy execution contexts without the projection retain their runtime context.
func Bind(ctx context.Context, value any) context.Context {
	if fields, ok := value.(map[string]any); ok {
		if raw, present := fields[serializedKey]; present {
			id, _ := raw.(string)
			ctx = WithID(ctx, id)
		}
		if raw, present := fields[writeIDKey]; present {
			id, _ := raw.(string)
			ctx = WithWriteID(ctx, id)
		}
	}
	return ctx
}
