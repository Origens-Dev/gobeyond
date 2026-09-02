package project

import (
	"fmt"
	"go/format"
	"path"
	"strconv"
	"strings"
)

func generatedWorkflowRegistration(definition WorkflowDefinition) ([]byte, error) {
	register := "RegisterWorkflow"
	value := "Workflow"
	if definition.Kind == ActivityKind {
		register = "RegisterActivity"
		value = "Activity"
	}
	var source strings.Builder
	source.WriteString(generatedSourceMarker)
	source.WriteString("\npackage ")
	source.WriteString(definition.PackageName)
	source.WriteString("\n\nimport (\n")
	source.WriteString("\tgbworkflows \"github.com/Origens-Dev/gobeyond/workflows\"\n")
	source.WriteString("\t\"go.temporal.io/sdk/worker\"\n")
	if definition.Standalone {
		source.WriteString("\ttemporalworkflow \"go.temporal.io/sdk/workflow\"\n")
	}
	source.WriteString(")\n\n")
	source.WriteString("func GobeyondRegister(registry worker.Registry) {\n")
	source.WriteString("\tgbworkflows.")
	source.WriteString(register)
	source.WriteString("(registry, ")
	source.WriteString(value)
	source.WriteString(", ")
	source.WriteString(strconv.Quote(definition.Name))
	source.WriteString(")\n")
	if definition.Standalone {
		source.WriteString("\tregistry.RegisterWorkflowWithOptions(gobeyondStandaloneWorkflow, temporalworkflow.RegisterOptions{Name: ")
		source.WriteString(strconv.Quote(definition.Name))
		source.WriteString("})\n")
	}
	source.WriteString("}\n")
	if definition.Standalone {
		if strings.Contains(definition.InputType, ".") || strings.Contains(definition.OutputType, ".") {
			return nil, fmt.Errorf("%s: standalone activity input/output types must be declared in the activity package for generated wrapper support", definition.EntryFile)
		}
		source.WriteString("\nfunc gobeyondStandaloneWorkflow(ctx temporalworkflow.Context")
		if definition.HandlerHasInput {
			source.WriteString(", input ")
			source.WriteString(definition.InputType)
		}
		source.WriteString(")")
		if definition.HandlerHasOutput {
			source.WriteString(" (")
			source.WriteString(definition.OutputType)
			source.WriteString(", error)")
		} else {
			source.WriteString(" error")
		}
		source.WriteString(" {\n")
		if definition.HandlerHasOutput {
			source.WriteString("\tvar output ")
			source.WriteString(definition.OutputType)
			source.WriteString("\n")
		}
		source.WriteString("\tfuture := gbworkflows.ExecuteActivity(ctx, Activity")
		if definition.HandlerHasInput {
			source.WriteString(", input")
		}
		source.WriteString(")\n")
		if definition.HandlerHasOutput {
			source.WriteString("\tif err := future.Get(ctx, &output); err != nil {\n\t\tvar zero ")
			source.WriteString(definition.OutputType)
			source.WriteString("\n\t\treturn zero, err\n\t}\n\treturn output, nil\n")
		} else {
			source.WriteString("\treturn future.Get(ctx, nil)\n")
		}
		source.WriteString("}\n")
	}
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated registration for %s: %w\n%s", definition.ID, err, source.String())
	}
	return formatted, nil
}

func generatedWorkflowReferences(definitions []WorkflowDefinition) ([]byte, error) {
	var source strings.Builder
	source.WriteString(generatedSourceMarker)
	source.WriteString("\npackage references\n\n")
	source.WriteString("import gbworkflows \"github.com/Origens-Dev/gobeyond/workflows\"\n\n")
	usedNames := make(map[string]struct{})
	for _, definition := range definitions {
		if definition.Kind == WorkflowKind || definition.Standalone {
			writeWorkflowReference(&source, usedNames, definition)
		}
		if definition.Kind == ActivityKind {
			writeActivityReference(&source, usedNames, definition)
		}
	}
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated workflow references: %w\n%s", err, source.String())
	}
	return formatted, nil
}

func writeWorkflowReference(source *strings.Builder, usedNames map[string]struct{}, definition WorkflowDefinition) {
	name := uniqueWorkflowReferenceName("Workflow", definition, usedNames)
	source.WriteString("var ")
	source.WriteString(name)
	source.WriteString(" = gbworkflows.WorkflowReference{Name: ")
	source.WriteString(strconv.Quote(definition.Name))
	source.WriteString(", TaskQueue: ")
	source.WriteString(strconv.Quote(definition.TaskQueue))
	source.WriteString("}\n")
}

func writeActivityReference(source *strings.Builder, usedNames map[string]struct{}, definition WorkflowDefinition) {
	name := uniqueWorkflowReferenceName("Activity", definition, usedNames)
	source.WriteString("var ")
	source.WriteString(name)
	source.WriteString(" = gbworkflows.ActivityReference{Name: ")
	source.WriteString(strconv.Quote(definition.Name))
	source.WriteString(", TaskQueue: ")
	source.WriteString(strconv.Quote(definition.TaskQueue))
	source.WriteString("}\n")
}

func uniqueWorkflowReferenceName(kind string, definition WorkflowDefinition, usedNames map[string]struct{}) string {
	base := kind + strings.TrimPrefix(goName(safePart(definition.ID)), "Route")
	name := base
	if _, exists := usedNames[name]; exists {
		// A top-level `foo-bar` and nested `foo/bar` are distinct valid
		// definition IDs but both normalize to FooBar. The generated key holds a
		// stable short digest, so it preserves a deterministic public reference.
		name += "_" + definition.Key
	}
	usedNames[name] = struct{}{}
	return name
}

func generatedWorkflowMain(websiteImport string, queue WorkflowQueue) ([]byte, error) {
	var imports strings.Builder
	var registrations strings.Builder
	hasAIAgents := false
	hasVoiceAgents := false
	for index, definition := range queue.Definitions {
		alias := fmt.Sprintf("definition%d", index)
		definitionImport := path.Join(websiteImport, GeneratedDir, "workflows", definition.Key)
		imports.WriteString("\t")
		imports.WriteString(alias)
		imports.WriteString(" ")
		imports.WriteString(strconv.Quote(definitionImport))
		imports.WriteString("\n")
		registrations.WriteString("\t\t\t")
		registrations.WriteString(alias)
		registrations.WriteString(".GobeyondRegister(w)\n")
	}
	for index, definition := range queue.Agents {
		alias := fmt.Sprintf("agent%d", index)
		definitionImport := path.Join(websiteImport, GeneratedDir, "agents", definition.Key)
		imports.WriteString("\t")
		imports.WriteString(alias)
		imports.WriteString(" ")
		imports.WriteString(strconv.Quote(definitionImport))
		imports.WriteString("\n")
		registrations.WriteString("\t\t\tif err := ")
		registrations.WriteString(alias)
		if definition.Kind == AgentKindAI {
			hasAIAgents = true
			registrations.WriteString(".GobeyondRegisterTemporalAI(w, aiRuntimes); err != nil {\n")
		} else {
			registrations.WriteString(".GobeyondRegisterTemporal(w); err != nil {\n")
		}
		registrations.WriteString("\t\t\t\tlog.Fatal(err)\n")
		registrations.WriteString("\t\t\t}\n")
		if definition.Kind == AgentKindAI && definition.LiveModel != "" {
			hasVoiceAgents = true
			registrations.WriteString("\t\t\tif err := ")
			registrations.WriteString(alias)
			registrations.WriteString(".GobeyondRegisterVoice(voiceRuntimes); err != nil {\n")
			registrations.WriteString("\t\t\t\tlog.Fatal(err)\n")
			registrations.WriteString("\t\t\t}\n")
		}
	}
	registrySetup := ""
	registryFinish := ""
	if hasAIAgents || hasVoiceAgents {
		imports.WriteString("\ttemporalruntime \"github.com/Origens-Dev/gobeyond/agents/temporalruntime\"\n")
	}
	if hasAIAgents {
		registrySetup += "\t\t\taiRuntimes := temporalruntime.NewAIRegistry()\n"
		registryFinish += "\t\t\tif err := aiRuntimes.Register(w); err != nil {\n\t\t\t\tlog.Fatal(err)\n\t\t\t}\n"
	}
	if hasVoiceAgents {
		registrySetup += "\t\t\tvoiceRuntimes := temporalruntime.NewVoiceRegistry()\n"
		registryFinish += "\t\t\ttemporalruntime.RetainVoiceRegistry(voiceRuntimes)\n"
	}
	source := fmt.Sprintf(`%s
package main

import (
	"context"
	"log"
	"os"

	gb "github.com/Origens-Dev/gobeyond"
	gbtemporal "github.com/Origens-Dev/gobeyond/adapters/temporal"
	"go.temporal.io/sdk/worker"
%s)

func main() {
	if os.Getenv("GOBEYOND_TEMPORAL_TASK_QUEUE") == "" {
		environment := os.Getenv("GOBEYOND_TEMPORAL_ENVIRONMENT")
		if environment == "" {
			environment = gb.LocalEnvironment
		}
		queue, err := gb.TaskQueueName(%q, environment)
		if err != nil {
			log.Fatal(err)
		}
		_ = os.Setenv("GOBEYOND_TEMPORAL_TASK_QUEUE", queue)
	}
	if os.Getenv("GOBEYOND_TEMPORAL_NAMESPACE") == "" {
		_ = os.Setenv("GOBEYOND_TEMPORAL_NAMESPACE", "default")
	}
	if err := gbtemporal.Serve(context.Background(), gbtemporal.Options{
		Register: func(w worker.Worker) {
%s%s%s		},
	}); err != nil {
		log.Fatal(err)
	}
}
`, generatedSourceMarker+"\n", imports.String(), queue.ID, registrySetup, registrations.String(), registryFinish)
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return nil, fmt.Errorf("format generated workflow worker for queue %s: %w\n%s", queue.ID, err, source)
	}
	return formatted, nil
}
