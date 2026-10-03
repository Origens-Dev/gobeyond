// Package toolsession carries runtime-owned conversation identity to tools.
// It is internal so authored agents can read identity without minting it.
package toolsession

import (
	"context"
	"strings"
)

const serializedKey = "gobeyondSessionID"

type contextKey struct{}

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

// ExecutionContext is serialized by the durable SDK along with the actor.
// Neither actor metadata nor session metadata supplies the identity.
func ExecutionContext(actor any, id string) map[string]any {
	return map[string]any{"gobeyondActor": actor, serializedKey: strings.TrimSpace(id)}
}

// Bind restores only the reserved framework projection, not client metadata.
// Legacy execution contexts without the projection retain their runtime context.
func Bind(ctx context.Context, value any) context.Context {
	if fields, ok := value.(map[string]any); ok {
		if raw, present := fields[serializedKey]; present {
			id, _ := raw.(string)
			return WithID(ctx, id)
		}
	}
	return ctx
}
