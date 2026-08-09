package agents

import (
	"context"
	"testing"
)

func TestConfigDefaultsToDirect(t *testing.T) {
	if got := (Config{}).Mode(); got != DirectMode {
		t.Fatalf("mode = %q, want %q", got, DirectMode)
	}
	if got := (Config{Durable: true}).Mode(); got != DurableMode {
		t.Fatalf("durable mode = %q, want %q", got, DurableMode)
	}
}

func TestDefinitionInvokesTypedHandlerWithLoopbackActor(t *testing.T) {
	definition := Define(Config{}, func(_ context.Context, actor Actor, input string) (string, error) {
		return actor.Kind + ":" + input, nil
	}, Slots{Tools: []Tool{{ID: "search"}}, Channels: []Channel{{ID: "updates"}}})

	output, err := definition.Invoke(context.Background(), LoopbackDevActor(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if output != "loopback:hello" || definition.Slots.Tools[0].ID != "search" {
		t.Fatalf("output/slots = %q %#v", output, definition.Slots)
	}
}

func TestActorValidation(t *testing.T) {
	if err := (Actor{}).Validate(); err == nil {
		t.Fatal("expected missing actor error")
	}
	if err := (Actor{ID: "actor"}).Validate(); err == nil {
		t.Fatal("expected missing actor kind error")
	}
}
