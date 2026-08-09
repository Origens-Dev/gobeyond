package durableassistant

import gbagents "github.com/Origens-Dev/gobeyond/agents"

var Agent = gbagents.DefineAI(gbagents.AIConfig{
	Model:    "openrouter/openai/gpt-4o-mini",
	Durable:  true,
	Public:   true,
	MaxSteps: 8,
}, gbagents.Slots{
	Channels: []gbagents.Channel{{ID: "web"}},
})
