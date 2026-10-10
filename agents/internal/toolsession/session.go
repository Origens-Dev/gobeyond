// Package toolsession carries runtime-owned conversation identity to tools.
// It is internal so authored agents can read identity without minting it.
package toolsession

import (
	"context"
	"strings"
)

const serializedKey = "gobeyondSessionID"
const writeIDKey = "gobeyondWriteID"
const resourceBindingKey = "gobeyondResourceBinding"

type contextKey struct{}
type writeContextKey struct{}
type resourceBindingContextKey struct{}

// ResourceBinding is an opaque application resource projection copied from a
// verified grant. It is not a mailbox, line, or DID type: ResourceID and
// AlternateID are grant-scoped identifiers whose product meaning is defined
// by the application. Callers, model arguments, and actor metadata cannot
// populate this projection.
type ResourceBinding struct {
	Kind        string `json:"kind,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	AlternateID string `json:"alternate_id,omitempty"`
}

// Present is true when the framework supplied a kind and at least one
// resource identifier. Empty bindings are treated as absent.
func (b ResourceBinding) Present() bool {
	return strings.TrimSpace(b.Kind) != "" && (strings.TrimSpace(b.ResourceID) != "" || strings.TrimSpace(b.AlternateID) != "")
}

// Equal compares trimmed identifier fields. Both-absent bindings are equal.
func (b ResourceBinding) Equal(other ResourceBinding) bool {
	return strings.TrimSpace(b.Kind) == strings.TrimSpace(other.Kind) &&
		strings.TrimSpace(b.ResourceID) == strings.TrimSpace(other.ResourceID) &&
		strings.TrimSpace(b.AlternateID) == strings.TrimSpace(other.AlternateID)
}

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

// WithResourceBinding replaces any inherited binding, including when absent.
func WithResourceBinding(ctx context.Context, binding ResourceBinding) context.Context {
	return context.WithValue(ctx, resourceBindingContextKey{}, binding)
}

func ResourceBindingFrom(ctx context.Context) (ResourceBinding, bool) {
	if ctx == nil {
		return ResourceBinding{}, false
	}
	binding, _ := ctx.Value(resourceBindingContextKey{}).(ResourceBinding)
	return binding, binding.Present()
}

// ExecutionContext is serialized by the durable SDK along with the actor.
// Neither actor metadata nor session metadata supplies the identity.
func ExecutionContext(actor any, id string) map[string]any {
	return map[string]any{"gobeyondActor": actor, serializedKey: strings.TrimSpace(id)}
}

// ExecutionContextWithWrite adds a platform-derived write idempotency key and
// the opaque grant-sourced resource binding. Callers and model arguments
// cannot populate this projection. Session and write identity stay on the
// reserved keys; they are never copied onto actor metadata.
func ExecutionContextWithWrite(actor any, sessionID, writeID string, binding ResourceBinding) map[string]any {
	fields := ExecutionContext(actor, sessionID)
	fields[writeIDKey] = strings.TrimSpace(writeID)
	fields[resourceBindingKey] = binding
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
		if raw, present := fields[resourceBindingKey]; present {
			ctx = WithResourceBinding(ctx, decodeResourceBinding(raw))
		}
	}
	return ctx
}

func decodeResourceBinding(value any) ResourceBinding {
	switch typed := value.(type) {
	case ResourceBinding:
		return typed
	case *ResourceBinding:
		if typed == nil {
			return ResourceBinding{}
		}
		return *typed
	case map[string]any:
		kind, _ := typed["kind"].(string)
		resourceID, _ := typed["resource_id"].(string)
		alternateID, _ := typed["alternate_id"].(string)
		return ResourceBinding{Kind: kind, ResourceID: resourceID, AlternateID: alternateID}
	default:
		return ResourceBinding{}
	}
}
