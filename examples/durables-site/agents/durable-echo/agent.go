package durableecho

import (
	"context"

	gbagents "github.com/Origens-Dev/gobeyond/agents"
)

type Input struct {
	Message string `json:"message"`
}

type Output struct {
	ActorID string `json:"actorId"`
	Message string `json:"message"`
}

func Run(_ context.Context, actor gbagents.Actor, input Input) (Output, error) {
	return Output{ActorID: actor.ID, Message: input.Message}, nil
}

var Agent = gbagents.Define(gbagents.Config{Durable: true}, Run, gbagents.Slots{
	Channels: []gbagents.Channel{{ID: "web"}},
})
