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

func TestResolveInstructionsAndVoiceName(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		metadata map[string]string
		want     string
	}{
		{name: "base when metadata nil", base: "Base.", metadata: nil, want: "Base."},
		{name: "base when key missing", base: "Base.", metadata: map[string]string{}, want: "Base."},
		{name: "overlay wins", base: "Base.", metadata: map[string]string{"instructions": "Overlay."}, want: "Overlay."},
		{name: "blank overlay ignored", base: "Base.", metadata: map[string]string{"instructions": "  "}, want: "Base."},
		{name: "trims overlay", base: "Base.", metadata: map[string]string{"instructions": "  Overlay.  "}, want: "Overlay."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveInstructions(test.base, test.metadata); got != test.want {
				t.Fatalf("ResolveInstructions = %q, want %q", got, test.want)
			}
		})
	}
	if got := ResolveVoiceName("Kore", map[string]string{"voice_name": "Puck"}); got != "Puck" {
		t.Fatalf("ResolveVoiceName overlay = %q", got)
	}
	if got := ResolveVoiceName("Kore", map[string]string{"voice_name": "  "}); got != "Kore" {
		t.Fatalf("ResolveVoiceName blank overlay = %q", got)
	}
	if got := ResolveVoiceName("Kore", nil); got != "Kore" {
		t.Fatalf("ResolveVoiceName default = %q", got)
	}
}
