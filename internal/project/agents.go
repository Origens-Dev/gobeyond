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
	TaskQueueSet bool
	Durable      bool
	Realtime     bool
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
	Tools        []AgentToolDefinition
	// SIPHandlers lists closed-enum method names with a site-registered
	// sip.Handlers field in agents/<id>/sip.go (subset). Empty means
	// edge-local defaults.
	SIPHandlers []string
}

// AgentToolDefinition is the compiler-visible execution metadata for one
// DefineAI tool. Variable is retained only for source validation; manifests
// expose the stable map key and resolved logical queue.
type AgentToolDefinition struct {
	ID           string `json:"id"`
	Variable     string `json:"-"`
	TaskQueue    string `json:"taskQueue,omitempty"`
	TaskQueueSet bool   `json:"-"`
}

// AgentSlots contains all author-visible extension references.
type AgentSlots struct {
	Tools     []string       `json:"tools"`
	Skills    []string       `json:"skills"`
	Subagents []string       `json:"subagents"`
	Schedules []string       `json:"schedules"`
	Channels  []AgentChannel `json:"channels"`
}

// AgentChannel is the compiler projection of agents.Channel. Empty Connector
// is allowed (web) and is emitted as an empty string under v1alpha3.
type AgentChannel struct {
	ID        string `json:"id"`
	Connector string `json:"connector"`
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
	if err := resolveAgentGraph(definitions); err != nil {
		return nil, err
	}
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
	taskQueue, durable, realtime, public, model, maxSteps, err := parseAgentConfig(call.Config, call.Kind)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	if taskQueue != "" {
		taskQueue, err = normalizeLogicalQueue(taskQueue)
		if err != nil {
			return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
		}
	}
	mode := AgentModeDirect
	if durable {
		mode = AgentModeDurable
	}
	tools, err := parseAgentTools(call.Config, call.Kind, parsed)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	sipHandlers, err := parseSIPHandlers(parsed)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("%s: %w", authorPath(root, dir), err)
	}
	definition := AgentDefinition{
		ID: id, Key: AgentDefinitionKey(id), Kind: call.Kind, Mode: mode, TaskQueue: taskQueue,
		TaskQueueSet: taskQueue != "", Durable: durable, Realtime: realtime, Public: public, SourceDir: authorPath(root, dir),
		EntryFile: authorPath(root, entryFile), PackageName: packageName,
		Handler: call.Handler, SourceFiles: files, Slots: call.Slots,
		Model: model, MaxSteps: maxSteps, Tools: tools, SIPHandlers: sipHandlers,
	}
	for _, tool := range tools {
		if !containsString(definition.Slots.Tools, tool.ID) {
			definition.Slots.Tools = append(definition.Slots.Tools, tool.ID)
		}
	}
	sort.Strings(definition.Slots.Tools)
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

func parseAgentConfig(config ast.Expr, kind string) (taskQueue string, durable bool, realtime bool, public bool, model string, maxSteps int, err error) {
	composite, ok := config.(*ast.CompositeLit)
	if !ok {
		return "", false, false, false, "", 0, errors.New("agent config must be an inline literal so the compiler can resolve its build metadata")
	}
	seen := map[string]bool{}
	for _, element := range composite.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return "", false, false, false, "", 0, errors.New("agent config must use named fields")
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok {
			return "", false, false, false, "", 0, errors.New("agent config field must be named")
		}
		if seen[key.Name] {
			return "", false, false, false, "", 0, fmt.Errorf("agent config field %s is duplicated", key.Name)
		}
		seen[key.Name] = true
		switch key.Name {
		case "TaskQueue":
			taskQueue, err = staticString(field.Value, "TaskQueue")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
		case "Durable":
			durable, err = staticBool(field.Value, "Durable")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
		case "Realtime":
			realtime, err = staticBool(field.Value, "Realtime")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
		case "Public":
			public, err = staticBool(field.Value, "Public")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
		case "Model":
			if kind != AgentKindAI {
				return "", false, false, false, "", 0, errors.New("agent config field Model is only supported by DefineAI")
			}
			model, err = staticString(field.Value, "Model")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
		case "MaxSteps":
			if kind != AgentKindAI {
				return "", false, false, false, "", 0, errors.New("agent config field MaxSteps is only supported by DefineAI")
			}
			maxSteps, err = staticInt(field.Value, "MaxSteps")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
		case "Inference":
			if kind != AgentKindAI {
				return "", false, false, false, "", 0, errors.New("agent config field Inference is only supported by DefineAI")
			}
			inference, err := staticString(field.Value, "Inference")
			if err != nil {
				return "", false, false, false, "", 0, err
			}
			if err := validateAIInference(inference); err != nil {
				return "", false, false, false, "", 0, err
			}
		case "Tools", "Provider", "DurableUpdates", "OnReviewPublicationFailure":
			if kind != AgentKindAI {
				return "", false, false, false, "", 0, fmt.Errorf("agent config field %s is only supported by DefineAI", key.Name)
			}
		default:
			return "", false, false, false, "", 0, fmt.Errorf("agent config field %s is not supported", key.Name)
		}
	}
	if kind == AgentKindAI && strings.TrimSpace(model) == "" {
		return "", false, false, false, "", 0, errors.New("AI agent Model is required")
	}
	return taskQueue, durable, realtime, public, model, maxSteps, nil
}

func parseAgentTools(config ast.Expr, kind string, files map[string]*ast.File) ([]AgentToolDefinition, error) {
	if kind != AgentKindAI {
		return nil, nil
	}
	composite, ok := config.(*ast.CompositeLit)
	if !ok {
		return nil, nil
	}
	var toolsExpression ast.Expr
	for _, element := range composite.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if ok && key.Name == "Tools" {
			toolsExpression = field.Value
			break
		}
	}
	if toolsExpression == nil {
		return nil, nil
	}
	toolsMap, ok := toolsExpression.(*ast.CompositeLit)
	if !ok {
		return nil, errors.New("AI agent Tools must be an inline map literal so the compiler can resolve tool queues")
	}
	definitions := make([]AgentToolDefinition, 0, len(toolsMap.Elts))
	seen := map[string]struct{}{}
	for _, element := range toolsMap.Elts {
		entry, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil, errors.New("AI agent Tools entries must use explicit string keys")
		}
		id, err := staticString(entry.Key, "AI tool ID")
		if err != nil || strings.TrimSpace(id) == "" {
			return nil, errors.New("AI agent Tools entries require non-empty string literal IDs")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("AI agent Tools duplicates ID %q", id)
		}
		seen[id] = struct{}{}
		definition := AgentToolDefinition{ID: id}
		switch value := entry.Value.(type) {
		case *ast.Ident:
			definition.Variable = value.Name
			call := findVariableCall(files, value.Name)
			if call != nil && calledName(call.Fun) == "DefineTool" {
				definition.TaskQueue, definition.TaskQueueSet, err = parseToolTaskQueue(call)
				if err != nil {
					return nil, fmt.Errorf("AI tool %q: %w", id, err)
				}
			}
		case *ast.CallExpr:
			if calledName(value.Fun) != "DefineTool" {
				return nil, fmt.Errorf("AI tool %q must reference a variable or inline agents.DefineTool", id)
			}
			definition.TaskQueue, definition.TaskQueueSet, err = parseToolTaskQueue(value)
			if err != nil {
				return nil, fmt.Errorf("AI tool %q: %w", id, err)
			}
		default:
			return nil, fmt.Errorf("AI tool %q must reference a variable or inline agents.DefineTool", id)
		}
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	return definitions, nil
}

func findVariableCall(files map[string]*ast.File, name string) *ast.CallExpr {
	for _, file := range files {
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
				for index, candidate := range value.Names {
					if candidate.Name != name || index >= len(value.Values) {
						continue
					}
					call, _ := value.Values[index].(*ast.CallExpr)
					return call
				}
			}
		}
	}
	return nil
}

func parseToolTaskQueue(call *ast.CallExpr) (string, bool, error) {
	if call == nil || len(call.Args) < 1 {
		return "", false, errors.New("DefineTool requires an inline ToolConfig")
	}
	config, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return "", false, errors.New("DefineTool ToolConfig must be an inline literal so the compiler can resolve TaskQueue")
	}
	for _, element := range config.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "TaskQueue" {
			continue
		}
		queue, err := staticString(field.Value, "Tool TaskQueue")
		if err != nil {
			return "", false, err
		}
		return queue, strings.TrimSpace(queue) != "", nil
	}
	return "", false, nil
}

func resolveAgentGraph(definitions []AgentDefinition) error {
	byID := make(map[string]int, len(definitions))
	parents := make(map[string][]string, len(definitions))
	for index := range definitions {
		definition := &definitions[index]
		byID[definition.ID] = index
		if definition.Realtime {
			if !definition.Durable {
				return fmt.Errorf("%s: Realtime requires Durable: true", definition.EntryFile)
			}
			if definition.TaskQueueSet {
				return fmt.Errorf("%s: Realtime agents use a compiler-derived unique TaskQueue", definition.EntryFile)
			}
		}
		if !definition.Durable && definition.TaskQueueSet {
			return fmt.Errorf("%s: direct agents cannot set TaskQueue", definition.EntryFile)
		}
	}
	for index := range definitions {
		definition := &definitions[index]
		for _, childID := range definition.Slots.Subagents {
			childIndex, ok := byID[childID]
			if !ok {
				return fmt.Errorf("%s: subagent %q does not exist", definition.EntryFile, childID)
			}
			child := &definitions[childIndex]
			if definition.Durable != child.Durable {
				return fmt.Errorf("%s: subagent %q must use the same durability mode", definition.EntryFile, childID)
			}
			parents[childID] = append(parents[childID], definition.ID)
		}
	}
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("agent subagent graph contains a cycle at %q", id)
		case 2:
			return nil
		}
		state[id] = 1
		definition := definitions[byID[id]]
		for _, childID := range definition.Slots.Subagents {
			if err := visit(childID); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	resolved := map[string]string{}
	var resolveQueue func(string) (string, error)
	resolveQueue = func(id string) (string, error) {
		if queue, ok := resolved[id]; ok {
			return queue, nil
		}
		definition := &definitions[byID[id]]
		if !definition.Durable {
			resolved[id] = ""
			return "", nil
		}
		if definition.Realtime {
			queue := realtimeAgentQueueID(definition.ID)
			resolved[id] = queue
			return queue, nil
		}
		if definition.TaskQueueSet {
			resolved[id] = definition.TaskQueue
			return definition.TaskQueue, nil
		}
		parentIDs := parents[id]
		if len(parentIDs) == 0 {
			resolved[id] = DefaultWorkflowQueueID
			return DefaultWorkflowQueueID, nil
		}
		queues := map[string]struct{}{}
		for _, parentID := range parentIDs {
			queue, err := resolveQueue(parentID)
			if err != nil {
				return "", err
			}
			queues[queue] = struct{}{}
		}
		if len(queues) != 1 {
			return "", fmt.Errorf("%s: subagent %q has parents on different task queues; set TaskQueue explicitly", definition.EntryFile, id)
		}
		for queue := range queues {
			resolved[id] = queue
			return queue, nil
		}
		return "", errors.New("unreachable agent queue resolution")
	}
	for index := range definitions {
		definition := &definitions[index]
		queue, err := resolveQueue(definition.ID)
		if err != nil {
			return err
		}
		definition.TaskQueue = queue
		definition.Mode = AgentModeDirect
		if definition.Durable {
			definition.Mode = AgentModeDurable
		}
		for toolIndex := range definition.Tools {
			tool := &definition.Tools[toolIndex]
			if tool.TaskQueueSet {
				if !definition.Durable {
					return fmt.Errorf("%s: direct agent tool %q cannot set TaskQueue", definition.EntryFile, tool.ID)
				}
				if definition.Realtime {
					return fmt.Errorf("%s: realtime agent tool %q cannot set TaskQueue because it executes locally", definition.EntryFile, tool.ID)
				}
				normalized, err := normalizeLogicalQueue(tool.TaskQueue)
				if err != nil {
					return fmt.Errorf("%s: tool %q: %w", definition.EntryFile, tool.ID, err)
				}
				tool.TaskQueue = normalized
			} else {
				tool.TaskQueue = queue
			}
		}
	}
	return nil
}

func realtimeAgentQueueID(id string) string {
	candidate := "realtime-" + id
	if normalized, err := normalizeLogicalQueue(candidate); err == nil {
		return normalized
	}
	digest := sha256.Sum256([]byte("realtime-agent-queue:" + id))
	return "realtime-" + hex.EncodeToString(digest[:8])
}

func validateAIInference(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openrouter", "vertex", "anthropic", "bedrock":
		return nil
	default:
		return fmt.Errorf("AI agent Inference %q is not supported; use openrouter, vertex, anthropic, or bedrock", value)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
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
		switch key.Name {
		case "Tools", "Skills", "Subagents", "Schedules":
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
			}
		case "Channels":
			channels, err := parseChannels(field.Value)
			if err != nil {
				return AgentSlots{}, err
			}
			slots.Channels = channels
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
			// Non-ID fields (for example Schedule.Cron) are compiler-visible on
			// the authored struct but are not part of the ID projection.
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

func parseChannels(value ast.Expr) ([]AgentChannel, error) {
	composite, ok := value.(*ast.CompositeLit)
	if !ok {
		return nil, errors.New("agent slot Channels must be an inline []agents.Channel literal")
	}
	channels := make([]AgentChannel, 0, len(composite.Elts))
	seen := map[string]struct{}{}
	for _, element := range composite.Elts {
		item, ok := element.(*ast.CompositeLit)
		if !ok {
			return nil, errors.New("agent slot Channels entries must be inline literals with an ID")
		}
		var channel AgentChannel
		for _, field := range item.Elts {
			keyValue, ok := field.(*ast.KeyValueExpr)
			if !ok {
				return nil, errors.New("agent slot Channels entries must use named fields")
			}
			key, ok := keyValue.Key.(*ast.Ident)
			if !ok {
				return nil, errors.New("agent slot Channels field must be named")
			}
			switch key.Name {
			case "ID":
				id, err := staticString(keyValue.Value, "Channels ID")
				if err != nil {
					return nil, err
				}
				channel.ID = id
			case "Connector":
				connector, err := staticString(keyValue.Value, "Channels Connector")
				if err != nil {
					return nil, err
				}
				channel.Connector = connector
			default:
				return nil, fmt.Errorf("agent slot Channels field %s is not supported", key.Name)
			}
		}
		if channel.ID == "" {
			return nil, errors.New("agent slot Channels entries require a non-empty ID")
		}
		if _, exists := seen[channel.ID]; exists {
			return nil, fmt.Errorf("agent slot Channels duplicates ID %q", channel.ID)
		}
		seen[channel.ID] = struct{}{}
		channels = append(channels, channel)
	}
	return channels, nil
}

// parseSIPHandlers finds package-level sip.Handlers{...} composites and returns
// the closed-enum method names with non-nil fields (Invite, Ack, ...).
func parseSIPHandlers(files map[string]*ast.File) ([]string, error) {
	methods := map[string]struct{}{}
	for _, file := range files {
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
				for index := range value.Names {
					if index >= len(value.Values) {
						continue
					}
					composite, ok := value.Values[index].(*ast.CompositeLit)
					if !ok || !isSIPHandlersType(composite.Type) {
						continue
					}
					for _, element := range composite.Elts {
						kv, ok := element.(*ast.KeyValueExpr)
						if !ok {
							return nil, errors.New("sip.Handlers entries must use named fields")
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok {
							return nil, errors.New("sip.Handlers field must be named")
						}
						method, ok := sipHandlerFieldMethod(key.Name)
						if !ok {
							return nil, fmt.Errorf("sip.Handlers field %s is not in the closed method enum", key.Name)
						}
						if isNilExpr(kv.Value) {
							continue
						}
						methods[method] = struct{}{}
					}
				}
			}
		}
	}
	out := make([]string, 0, len(methods))
	for method := range methods {
		out = append(out, method)
	}
	sort.Strings(out)
	return out, nil
}

func isSIPHandlersType(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name == "Handlers"
	case *ast.SelectorExpr:
		return typed.Sel.Name == "Handlers"
	default:
		return false
	}
}

func sipHandlerFieldMethod(field string) (string, bool) {
	switch field {
	case "Invite":
		return "INVITE", true
	case "Ack":
		return "ACK", true
	case "Bye":
		return "BYE", true
	case "Cancel":
		return "CANCEL", true
	case "Register":
		return "REGISTER", true
	case "Options":
		return "OPTIONS", true
	case "Update":
		return "UPDATE", true
	case "Info":
		return "INFO", true
	case "Prack":
		return "PRACK", true
	case "Refer":
		return "REFER", true
	case "Subscribe":
		return "SUBSCRIBE", true
	case "Notify":
		return "NOTIFY", true
	case "Message":
		return "MESSAGE", true
	case "Publish":
		return "PUBLISH", true
	default:
		return "", false
	}
}

func isNilExpr(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
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
