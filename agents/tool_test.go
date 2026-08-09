package agents

import (
	"context"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

func TestDefineToolDecodesInputAndPropagatesActor(t *testing.T) {
	type input struct {
		OrderID string `json:"orderId"`
	}
	tool := DefineTool(ToolConfig{Description: "Look up an order", TaskQueue: "lookups"}, func(_ context.Context, actor Actor, input input) (string, error) {
		return actor.ID + ":" + input.OrderID, nil
	})
	output, err := tool.Execute(context.Background(), ai.ToolCall{Input: map[string]any{"orderId": "order-1"}}, ai.ToolExecutionOptions{
		Context: map[string]any{toolActorContextKey: map[string]any{"id": "user-1", "kind": "user"}},
	})
	if err != nil || output != "user-1:order-1" {
		t.Fatalf("tool output = %#v, err = %v", output, err)
	}
	if queue := ToolTaskQueue(tool); queue != "lookups" {
		t.Fatalf("tool queue = %q", queue)
	}
}
