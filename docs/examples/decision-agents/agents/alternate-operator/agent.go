package alternate_operator

import (
	"context"

	"github.com/Origens-Dev/gobeyond/agents"
)

type Request struct {
	Text string `json:"text"`
}

type Response struct {
	Message string `json:"message"`
}

func run(_ context.Context, actor agents.Actor, _ Request) (Response, error) {
	if err := actor.Validate(); err != nil {
		return Response{}, err
	}
	return Response{Message: "Synthetic alternate Operator."}, nil
}

// Agent has the distinct agents/alternate-operator identity. It is a small
// direct handler example, not a handoff from the decision graph.
var Agent = agents.Define(agents.Config{}, run)
