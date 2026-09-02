package project

import (
	"fmt"
	"go/format"
	"strings"
)

func generatedAgentRegistration(definition AgentDefinition) ([]byte, error) {
	var source strings.Builder
	source.WriteString(generatedSourceMarker)
	source.WriteString("\npackage ")
	source.WriteString(definition.PackageName)
	source.WriteString("\n\nimport httpruntime \"github.com/Origens-Dev/gobeyond/agents/httpruntime\"\n")
	source.WriteString("import gbsip \"github.com/Origens-Dev/gobeyond/sip\"\n")
	if definition.Durable {
		source.WriteString("import temporalruntime \"github.com/Origens-Dev/gobeyond/agents/temporalruntime\"\n")
		source.WriteString("import \"go.temporal.io/sdk/worker\"\n")
	}
	source.WriteString("\nfunc GobeyondRegister(registry httpruntime.Registerer) error {\n")
	source.WriteString("\tdefinition := Agent\n")
	source.WriteString(fmt.Sprintf("\tdefinition.Config.TaskQueue = %q\n", definition.TaskQueue))
	if definition.Kind == AgentKindAI {
		source.WriteString(fmt.Sprintf("\tdefinition.AI.TaskQueue = %q\n", definition.TaskQueue))
		source.WriteString(fmt.Sprintf("\tdefinition.AI.Instructions = %q\n", definition.Instructions))
		source.WriteString(fmt.Sprintf("\tdefinition.AI.Revision = %q\n", definition.Revision))
		if definition.LiveModel != "" {
			source.WriteString(fmt.Sprintf("\tdefinition.AI.LiveModel = %q\n", definition.LiveModel))
		}
		if definition.ToolModel != "" {
			source.WriteString(fmt.Sprintf("\tdefinition.AI.ToolModel = %q\n", definition.ToolModel))
		}
		if definition.VoiceName != "" {
			source.WriteString(fmt.Sprintf("\tdefinition.AI.VoiceName = %q\n", definition.VoiceName))
		}
		source.WriteString(fmt.Sprintf("\treturn httpruntime.RegisterAI(registry, %q, definition)\n", definition.ID))
	} else {
		source.WriteString(fmt.Sprintf("\treturn registry.Register(%q, httpruntime.Adapt(definition))\n", definition.ID))
	}
	source.WriteString("}\n")
	source.WriteString("\nfunc GobeyondRegisterSIP(registry gbsip.Registerer) error {\n")
	if len(definition.SIPHandlers) > 0 {
		source.WriteString(fmt.Sprintf("\treturn registry.Register(%q, SIP)\n", definition.ID))
	} else {
		source.WriteString("\treturn nil\n")
	}
	source.WriteString("}\n")
	if definition.Durable {
		if definition.Kind == AgentKindAI {
			source.WriteString("\nfunc GobeyondRegisterTemporalAI(registry worker.Worker, runtimes *temporalruntime.AIRegistry) error {\n")
		} else {
			source.WriteString("\nfunc GobeyondRegisterTemporal(registry worker.Registry) error {\n")
		}
		source.WriteString("\tdefinition := Agent\n")
		source.WriteString(fmt.Sprintf("\tdefinition.Config.TaskQueue = %q\n", definition.TaskQueue))
		if definition.Kind == AgentKindAI {
			source.WriteString(fmt.Sprintf("\tdefinition.AI.TaskQueue = %q\n", definition.TaskQueue))
			source.WriteString(fmt.Sprintf("\tdefinition.AI.Instructions = %q\n", definition.Instructions))
			source.WriteString(fmt.Sprintf("\tdefinition.AI.Revision = %q\n", definition.Revision))
			if definition.LiveModel != "" {
				source.WriteString(fmt.Sprintf("\tdefinition.AI.LiveModel = %q\n", definition.LiveModel))
			}
			if definition.ToolModel != "" {
				source.WriteString(fmt.Sprintf("\tdefinition.AI.ToolModel = %q\n", definition.ToolModel))
			}
			if definition.VoiceName != "" {
				source.WriteString(fmt.Sprintf("\tdefinition.AI.VoiceName = %q\n", definition.VoiceName))
			}
			source.WriteString(fmt.Sprintf("\treturn temporalruntime.RegisterAI(registry, runtimes, %q, definition)\n", definition.ID))
		} else {
			source.WriteString(fmt.Sprintf("\treturn temporalruntime.Register(registry, %q, definition)\n", definition.ID))
		}
		source.WriteString("}\n")
	}
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated agent registration for %s: %w\n%s", definition.ID, err, source.String())
	}
	return formatted, nil
}
