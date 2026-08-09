package assistant

import gbagents "github.com/Origens-Dev/gobeyond/agents"

var Agent = gbagents.DefineAI(gbagents.AIConfig{
	Model:  "openrouter/openai/gpt-4o-mini",
	Public: true,
}, gbagents.Slots{
	Channels: []gbagents.Channel{{ID: "web"}},
})
