package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	AgentModeDirect  = "direct"
	AgentModeDurable = "durable"
	AgentKindHandler = "handler"
	AgentKindAI      = "ai"
)

// AgentDefinition is the compiler-visible model for agents/<id>/agent.go.
// It intentionally records slots even though this slice does not yet bind
// their provider implementations or generate execution wiring.
type AgentDefinition struct {
	ID           string
	Key          string
	Kind         string
	Mode         string
	TaskQueue    string
	Durable      bool
	Public       bool
	Model        string
	MaxSteps     int
	Instructions string
	Revision     string
	SourceDir    string
	EntryFile    string
	PackageName  string
	Handler      string
	InputType    string
	OutputType   string
	SourceFiles  []string
	Slots        AgentSlots
}

// AgentSlots contains all author-visible extension references.
type AgentSlots struct {
	Tools     []string `json:"tools"`
	Skills    []string `json:"skills"`
	Subagents []string `json:"subagents"`
	Schedules []string `json:"schedules"`
	Channels  []string `json:"channels"`
}

// DiscoverAgentDefinitions validates and discovers top-level agent packages.
// Each agent is an isolated package under agents/<id>/ with agent.go as its
// declaration entry point; supporting Go files may live beside it.
func DiscoverAgentDefinitions(root string) ([]AgentDefinition, error) {
	agentsRoot := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	definitions := make([]AgentDefinition, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() {
			if strings.HasSuffix(entry.Name(), ".go") {
				return nil, fmt.Errorf("agents/%s is not allowed; put each definition in agents/<id>/agent.go", entry.Name())
			}
			continue
		}
		id, err := normalizeDefinitionPart(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("agents/%s: %w", entry.Name(), err)
		}
		definition, err := discoverAgentDefinition(root, filepath.Join(agentsRoot, entry.Name()), id)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	return definitions, nil
}

func discoverAgentDefinition(root, dir, id string) (AgentDefinition, error) {
	entryFile := filepath.Join(dir, "agent.go")
	if !regularFile(entryFile) {
		return AgentDefinition{}, fmt.Errorf("%s must contain agent.go", authorPath(root, dir))
	}
	files, packageName, parsed, err := parseDefinitionPackage(root, dir)
	if err != nil {
		return AgentDefinition{}, err
	}
	entry := parsed[entryFile]
	if entry == nil {
		return AgentDefinition{}, fmt.Errorf("cannot parse %s", authorPath(root, entryFile))
	}
	call, err := findAgentCall(entry)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	taskQueue, durable, public, model, maxSteps, err := parseAgentConfig(call.Config, call.Kind)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	if taskQueue != "" {
		taskQueue, err = normalizeLogicalQueue(taskQueue)
		if err != nil {
			return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
		}
	}
	if durable && taskQueue == "" {
		taskQueue = DefaultWorkflowQueueID
	}
	mode := AgentModeDirect
	if durable {
		mode = AgentModeDurable
	}
	definition := AgentDefinition{
		ID: id, Key: AgentDefinitionKey(id), Kind: call.Kind, Mode: mode, TaskQueue: taskQueue,
		Durable: durable, Public: public, SourceDir: authorPath(root, dir),
		EntryFile: authorPath(root, entryFile), PackageName: packageName,
		Handler: call.Handler, SourceFiles: files, Slots: call.Slots,
		Model: model, MaxSteps: maxSteps,
	}
	if call.Kind == AgentKindAI {
		instructionsFile := filepath.Join(dir, "instructions.md")
		instructions, readErr := os.ReadFile(instructionsFile)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				return AgentDefinition{}, fmt.Errorf("%s must contain instructions.md", authorPath(root, dir))
			}
			return AgentDefinition{}, readErr
		}
		definition.Instructions = strings.TrimSpace(string(instructions))
		if definition.Instructions == "" {
			return AgentDefinition{}, fmt.Errorf("%s must not be empty", authorPath(root, instructionsFile))
		}
		definition.Revision, err = agentRuntimeRevision(root, definition)
		if err != nil {
			return AgentDefinition{}, err
		}
	} else if err := populateAgentSignature(parsed, &definition); err != nil {
		return AgentDefinition{}, fmt.Errorf("%s: %w", definition.EntryFile, err)
	}
	return definition, nil
}

type agentCall struct {
	Kind    string
	Config  ast.Expr
	Handler string
	Slots   AgentSlots
}

func findAgentCall(file *ast.File) (agentCall, error) {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if name.Name != "Agent" {
					continue
				}
				if index >= len(value.Values) {
					return agentCall{}, errors.New("var Agent must be initialized with agents.Define(...) or agents.DefineAI(...)")
				}
				call, ok := value.Values[index].(*ast.CallExpr)
				if !ok {
					return agentCall{}, errors.New("var Agent must be initialized with agents.Define(...) or agents.DefineAI(...)")
				}
				called := calledName(call.Fun)
				if called == "DefineAI" {
					if len(call.Args) < 1 || len(call.Args) > 2 {
						return agentCall{}, errors.New("agents.DefineAI accepts an inline AIConfig and optional Slots")
					}
					slots := AgentSlots{}
					if len(call.Args) == 2 {
						parsed, err := parseAgentSlots(call.Args[1])
						if err != nil {
							return agentCall{}, err
						}
						slots = parsed
					}
					return agentCall{Kind: AgentKindAI, Config: call.Args[0], Slots: slots}, nil
				}
				if called != "Define" || len(call.Args) < 2 || len(call.Args) > 3 {
					return agentCall{}, errors.New("var Agent must be initialized with agents.Define(...) or agents.DefineAI(...)")
				}
				handler, ok := call.Args[1].(*ast.Ident)
				if !ok {
					return agentCall{}, errors.New("Agent handler must be a package function identifier")
				}
				slots := AgentSlots{}
				if len(call.Args) == 3 {
					parsed, err := parseAgentSlots(call.Args[2])
					if err != nil {
						return agentCall{}, err
					}
					slots = parsed
				}
				return agentCall{Kind: AgentKindHandler, Config: call.Args[0], Handler: handler.Name, Slots: slots}, nil
			}
		}
	}
	return agentCall{}, errors.New("missing exported var Agent = agents.Define(...) or agents.DefineAI(...)")
}

func parseAgentConfig(config ast.Expr, kind string) (taskQueue string, durable bool, public bool, model string, maxSteps int, err error) {
	composite, ok := config.(*ast.CompositeLit)
	if !ok {
		return "", false, false, "", 0, errors.New("agent config must be an inline literal so the compiler can resolve its build metadata")
	}
	seen := map[string]bool{}
	for _, element := range composite.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return "", false, false, "", 0, errors.New("agent config must use named fields")
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok {
			return "", false, false, "", 0, errors.New("agent config field must be named")
		}
		if seen[key.Name] {
			return "", false, false, "", 0, fmt.Errorf("agent config field %s is duplicated", key.Name)
		}
		seen[key.Name] = true
		switch key.Name {
		case "TaskQueue":
			taskQueue, err = staticString(field.Value, "TaskQueue")
			if err != nil {
				return "", false, false, "", 0, err
			}
		case "Durable":
			durable, err = staticBool(field.Value, "Durable")
			if err != nil {
				return "", false, false, "", 0, err
			}
		case "Public":
			public, err = staticBool(field.Value, "Public")
			if err != nil {
				return "", false, false, "", 0, err
			}
		case "Model":
			if kind != AgentKindAI {
				return "", false, false, "", 0, errors.New("agent config field Model is only supported by DefineAI")
			}
			model, err = staticString(field.Value, "Model")
			if err != nil {
				return "", false, false, "", 0, err
			}
		case "MaxSteps":
			if kind != AgentKindAI {
				return "", false, false, "", 0, errors.New("agent config field MaxSteps is only supported by DefineAI")
			}
			maxSteps, err = staticInt(field.Value, "MaxSteps")
			if err != nil {
				return "", false, false, "", 0, err
			}
		case "Tools", "Provider":
			if kind != AgentKindAI {
				return "", false, false, "", 0, fmt.Errorf("agent config field %s is only supported by DefineAI", key.Name)
			}
		default:
			return "", false, false, "", 0, fmt.Errorf("agent config field %s is not supported", key.Name)
		}
	}
	if kind == AgentKindAI && strings.TrimSpace(model) == "" {
		return "", false, false, "", 0, errors.New("AI agent Model is required")
	}
	return taskQueue, durable, public, model, maxSteps, nil
}

func parseAgentSlots(value ast.Expr) (AgentSlots, error) {
	composite, ok := value.(*ast.CompositeLit)
	if !ok {
		return AgentSlots{}, errors.New("agent slots must be an inline agents.Slots literal so the compiler can resolve tools, skills, subagents, schedules, and channels")
	}
	var slots AgentSlots
	seen := map[string]bool{}
	for _, element := range composite.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return AgentSlots{}, errors.New("agent slots must use named fields")
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok {
			return AgentSlots{}, errors.New("agent slot field must be named")
		}
		if seen[key.Name] {
			return AgentSlots{}, fmt.Errorf("agent slot %s is duplicated", key.Name)
		}
		seen[key.Name] = true
		ids, err := parseSlotIDs(field.Value, key.Name)
		if err != nil {
			return AgentSlots{}, err
		}
		switch key.Name {
		case "Tools":
			slots.Tools = ids
		case "Skills":
			slots.Skills = ids
		case "Subagents":
			slots.Subagents = ids
		case "Schedules":
			slots.Schedules = ids
		case "Channels":
			slots.Channels = ids
		default:
			return AgentSlots{}, fmt.Errorf("agent slot %s is not supported", key.Name)
		}
	}
	return slots, nil
}

func parseSlotIDs(value ast.Expr, slot string) ([]string, error) {
	composite, ok := value.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("agent slot %s must be an inline []agents.%s literal", slot, strings.TrimSuffix(slot, "s"))
	}
	ids := make([]string, 0, len(composite.Elts))
	seen := map[string]struct{}{}
	for _, element := range composite.Elts {
		item, ok := element.(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("agent slot %s entries must be inline literals with an ID", slot)
		}
		id := ""
		for _, field := range item.Elts {
			keyValue, ok := field.(*ast.KeyValueExpr)
			if !ok {
				return nil, fmt.Errorf("agent slot %s entries must use named fields", slot)
			}
			key, ok := keyValue.Key.(*ast.Ident)
			if !ok || key.Name != "ID" {
				continue
			}
			var err error
			id, err = staticString(keyValue.Value, slot+" ID")
			if err != nil {
				return nil, err
			}
		}
		if id == "" {
			return nil, fmt.Errorf("agent slot %s entries require a non-empty ID", slot)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("agent slot %s duplicates ID %q", slot, id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func staticString(expression ast.Expr, field string) (string, error) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", fmt.Errorf("%s must be a string literal", field)
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", err
	}
	return value, nil
}

func staticBool(expression ast.Expr, field string) (bool, error) {
	identifier, ok := expression.(*ast.Ident)
	if !ok || (identifier.Name != "true" && identifier.Name != "false") {
		return false, fmt.Errorf("%s must be true or false", field)
	}
	return identifier.Name == "true", nil
}

func staticInt(expression ast.Expr, field string) (int, error) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, fmt.Errorf("%s must be an integer literal", field)
	}
	value, err := strconv.Atoi(literal.Value)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", field)
	}
	return value, nil
}

func agentRuntimeRevision(root string, definition AgentDefinition) (string, error) {
	digest := sha256.New()
	_, _ = digest.Write([]byte("gobeyond-agent-runtime-v1\x00"))
	_, _ = digest.Write([]byte(definition.ID))
	_, _ = digest.Write([]byte("\x00" + definition.Model + "\x00" + strconv.Itoa(definition.MaxSteps)))
	_, _ = digest.Write([]byte("\x00" + definition.Instructions))
	for _, relativeFile := range definition.SourceFiles {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativeFile)))
		if err != nil {
			return "", err
		}
		_, _ = digest.Write([]byte("\x00" + relativeFile + "\x00"))
		_, _ = digest.Write(content)
	}
	return hex.EncodeToString(digest.Sum(nil)[:16]), nil
}

func populateAgentSignature(files map[string]*ast.File, definition *AgentDefinition) error {
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != definition.Handler {
				continue
			}
			params := flattenFieldTypes(function.Type.Params)
			if len(params) != 3 || !isContextType(params[0]) || !isAgentActorType(params[1]) {
				return fmt.Errorf("agent handler %s must accept (context.Context, agents.Actor, input)", definition.Handler)
			}
			results := flattenFieldTypes(function.Type.Results)
			if len(results) != 2 || !isErrorType(results[1]) {
				return fmt.Errorf("agent handler %s must return (output, error)", definition.Handler)
			}
			definition.InputType = expressionString(params[2])
			definition.OutputType = expressionString(results[0])
			return nil
		}
	}
	return fmt.Errorf("handler function %s was not found in its definition folder", definition.Handler)
}

func isAgentActorType(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name == "Actor"
	case *ast.SelectorExpr:
		return value.Sel.Name == "Actor"
	default:
		return false
	}
}

// AgentDefinitionKey returns the deterministic generated package key for an agent.
func AgentDefinitionKey(id string) string {
	digest := sha256.Sum256([]byte("agent-definition:" + id))
	name := strings.Trim(safePart(id), "_")
	if name == "" {
		name = "agent"
	}
	return "a_" + name + "_" + hex.EncodeToString(digest[:4])
}
