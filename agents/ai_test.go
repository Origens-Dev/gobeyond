package agents

import (
	"context"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

func TestAIDefinitionUsesCustomProviderInstructionsAndUIMessages(t *testing.T) {
	model := ai.NewMockLanguageModel("support")
	provider := ai.NewMockProvider()
	provider.LanguageModels["support"] = model
	definition := DefineAI(AIConfig{
		Model: "support", Provider: provider, Instructions: "Be concise.", MaxSteps: 3,
	})
	output, err := definition.Invoke(context.Background(), Actor{ID: "user-1", Kind: "user"}, AIInput{Messages: []AIMessage{
		{Role: "user", Parts: []AIMessagePart{{Type: "text", Text: "hello"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Text != "ok" || output.Model != "support" {
		t.Fatalf("output = %#v", output)
	}
	if len(model.GenerateCalls) != 1 || len(model.GenerateCalls[0].Prompt) != 2 {
		t.Fatalf("model calls = %#v", model.GenerateCalls)
	}
	if got := model.GenerateCalls[0].Prompt[0].Text; got != "Be concise." {
		t.Fatalf("system instructions = %q", got)
	}
}

func TestAIDefinitionValidatesModelAndMessageRole(t *testing.T) {
	actor := Actor{ID: "user-1", Kind: "user"}
	if _, err := DefineAI(AIConfig{}).Invoke(context.Background(), actor, AIInput{Prompt: "hello"}); err == nil {
		t.Fatal("missing model succeeded")
	}
	model := ai.NewMockLanguageModel("support")
	provider := ai.NewMockProvider()
	provider.LanguageModels["support"] = model
	definition := DefineAI(AIConfig{Model: "support", Provider: provider})
	if _, err := definition.Invoke(context.Background(), actor, AIInput{Messages: []AIMessage{{Role: "tool", Content: "unsupported"}}}); err == nil {
		t.Fatal("unsupported wire message role succeeded")
	}
}
