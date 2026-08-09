package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
)

const (
	WorkflowKind           = "workflow"
	ActivityKind           = "activity"
	DefaultWorkflowQueueID = gb.DefaultTaskQueueID
)

// WorkflowDefinition describes one compiler-owned definition in workflows/.
// A definition maps to one Go package; queue workers may register several
// definitions from different packages.
type WorkflowDefinition struct {
	ID               string
	Key              string
	Kind             string
	Name             string
	TaskQueue        string
	SourceDir        string
	EntryFile        string
	PackageName      string
	ParentID         string
	Handler          string
	Public           bool
	Standalone       bool
	SourceFiles      []string
	InputType        string
	OutputType       string
	HandlerHasInput  bool
	HandlerHasOutput bool
}

// WorkflowQueue is one logical Temporal task queue and the definitions that
// the generated poller registers on it.
type WorkflowQueue struct {
	ID          string
	Key         string
	Definitions []WorkflowDefinition
	Agents      []AgentDefinition
}

// DiscoverWorkflowDefinitions validates and discovers the authored workflow
// tree. Legacy workers/ is intentionally a hard error for the new contract.
func DiscoverWorkflowDefinitions(root string) ([]WorkflowDefinition, error) {
	if info, err := os.Stat(filepath.Join(root, "workers")); err == nil && info.IsDir() {
		return nil, errors.New("legacy workers/ is no longer supported; move each definition to workflows/<name>/workflow.go (or activity.go for a standalone activity), declare var Workflow/Activity with workflows.Define, and remove manual Register functions")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	workflowsRoot := filepath.Join(root, "workflows")
	entries, err := os.ReadDir(workflowsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var definitions []WorkflowDefinition
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() {
			if strings.HasSuffix(entry.Name(), ".go") {
				return nil, fmt.Errorf("workflows/%s is not allowed; put each definition in workflows/<name>/", entry.Name())
			}
			continue
		}
		id, normalizeErr := normalizeDefinitionPart(entry.Name())
		if normalizeErr != nil {
			return nil, fmt.Errorf("workflows/%s: %w", entry.Name(), normalizeErr)
		}
		dir := filepath.Join(workflowsRoot, entry.Name())
		discovered, discoverErr := discoverTopLevelDefinition(root, dir, id)
		if discoverErr != nil {
			return nil, discoverErr
		}
		definitions = append(definitions, discovered...)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	seenNames := make(map[string]string)
	seenIDs := make(map[string]string)
	for _, definition := range definitions {
		if previous, ok := seenIDs[definition.ID]; ok {
			return nil, fmt.Errorf("workflow definition id %q is duplicated by %s and %s; an activity and subworkflow cannot share the same owner-local name", definition.ID, previous, definition.EntryFile)
		}
		seenIDs[definition.ID] = definition.EntryFile
		if previous, ok := seenNames[definition.Name]; ok {
			return nil, fmt.Errorf("workflow definition name %q is duplicated by %s and %s", definition.Name, previous, definition.EntryFile)
		}
		seenNames[definition.Name] = definition.EntryFile
	}
	return definitions, nil
}

func discoverTopLevelDefinition(root, dir, id string) ([]WorkflowDefinition, error) {
	workflowFile := filepath.Join(dir, "workflow.go")
	activityFile := filepath.Join(dir, "activity.go")
	hasWorkflow := regularFile(workflowFile)
	hasActivity := regularFile(activityFile)
	if hasWorkflow == hasActivity {
		return nil, fmt.Errorf("%s must contain exactly one of workflow.go or activity.go", authorPath(root, dir))
	}
	if hasActivity {
		definition, err := parseWorkflowDefinition(root, dir, activityFile, id, ActivityKind, "", DefaultWorkflowQueueID, true, true)
		if err != nil {
			return nil, err
		}
		if regularDir(filepath.Join(dir, "activities")) || regularDir(filepath.Join(dir, "subworkflows")) {
			return nil, fmt.Errorf("%s is a standalone activity and cannot own activities/ or subworkflows/", definition.EntryFile)
		}
		return []WorkflowDefinition{definition}, nil
	}
	definition, err := parseWorkflowDefinition(root, dir, workflowFile, id, WorkflowKind, "", DefaultWorkflowQueueID, true, false)
	if err != nil {
		return nil, err
	}
	definitions := []WorkflowDefinition{definition}
	children, err := discoverOwnedDefinitions(root, dir, definition)
	if err != nil {
		return nil, err
	}
	return append(definitions, children...), nil
}

func discoverOwnedDefinitions(root, ownerDir string, owner WorkflowDefinition) ([]WorkflowDefinition, error) {
	var definitions []WorkflowDefinition
	activitiesDir := filepath.Join(ownerDir, "activities")
	activityEntries, err := readDefinitionDirs(activitiesDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range activityEntries {
		part, normalizeErr := normalizeDefinitionPart(entry.Name())
		if normalizeErr != nil {
			return nil, fmt.Errorf("%s: %w", authorPath(root, filepath.Join(activitiesDir, entry.Name())), normalizeErr)
		}
		dir := filepath.Join(activitiesDir, entry.Name())
		entryFile := filepath.Join(dir, "activity.go")
		if !regularFile(entryFile) {
			return nil, fmt.Errorf("%s must contain activity.go", authorPath(root, dir))
		}
		if regularFile(filepath.Join(dir, "workflow.go")) {
			return nil, fmt.Errorf("%s cannot contain both activity.go and workflow.go", authorPath(root, dir))
		}
		id := owner.ID + "." + part
		definition, parseErr := parseWorkflowDefinition(root, dir, entryFile, id, ActivityKind, owner.ID, owner.TaskQueue, false, false)
		if parseErr != nil {
			return nil, parseErr
		}
		definitions = append(definitions, definition)
	}

	subworkflowsDir := filepath.Join(ownerDir, "subworkflows")
	subworkflowEntries, err := readDefinitionDirs(subworkflowsDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range subworkflowEntries {
		part, normalizeErr := normalizeDefinitionPart(entry.Name())
		if normalizeErr != nil {
			return nil, fmt.Errorf("%s: %w", authorPath(root, filepath.Join(subworkflowsDir, entry.Name())), normalizeErr)
		}
		dir := filepath.Join(subworkflowsDir, entry.Name())
		entryFile := filepath.Join(dir, "workflow.go")
		if !regularFile(entryFile) {
			return nil, fmt.Errorf("%s must contain workflow.go; subworkflows cannot be standalone activities", authorPath(root, dir))
		}
		if regularFile(filepath.Join(dir, "activity.go")) {
			return nil, fmt.Errorf("%s cannot contain both workflow.go and activity.go", authorPath(root, dir))
		}
		id := owner.ID + "." + part
		definition, parseErr := parseWorkflowDefinition(root, dir, entryFile, id, WorkflowKind, owner.ID, owner.TaskQueue, false, false)
		if parseErr != nil {
			return nil, parseErr
		}
		definitions = append(definitions, definition)
		children, childErr := discoverOwnedDefinitions(root, dir, definition)
		if childErr != nil {
			return nil, childErr
		}
		definitions = append(definitions, children...)
	}
	return definitions, nil
}

func parseWorkflowDefinition(root, dir, entryFile, id, kind, parentID, inheritedQueue string, public, standalone bool) (WorkflowDefinition, error) {
	files, packageName, parsed, err := parseDefinitionPackage(root, dir)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	entry := parsed[entryFile]
	if entry == nil {
		return WorkflowDefinition{}, fmt.Errorf("cannot parse %s", authorPath(root, entryFile))
	}
	variable := "Workflow"
	constructor := "Define"
	if kind == ActivityKind {
		variable = "Activity"
		constructor = "DefineActivity"
	}
	config, handler, err := findDefinitionCall(entry, variable, constructor)
	if err != nil {
		return WorkflowDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	name, err := staticConfigString(config, "Name")
	if err != nil {
		return WorkflowDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	if name == "" {
		name = id
	}
	queue, err := staticConfigString(config, "TaskQueue")
	if err != nil {
		return WorkflowDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	if queue == "" {
		queue = inheritedQueue
	}
	queue, err = normalizeLogicalQueue(queue)
	if err != nil {
		return WorkflowDefinition{}, fmt.Errorf("%s: %w", authorPath(root, entryFile), err)
	}
	definition := WorkflowDefinition{
		ID:          id,
		Key:         WorkflowDefinitionKey(id),
		Kind:        kind,
		Name:        name,
		TaskQueue:   queue,
		SourceDir:   authorPath(root, dir),
		EntryFile:   authorPath(root, entryFile),
		PackageName: packageName,
		ParentID:    parentID,
		Handler:     handler,
		Public:      public,
		Standalone:  standalone,
		SourceFiles: files,
	}
	if standalone {
		if err := populateStandaloneSignature(parsed, &definition); err != nil {
			return WorkflowDefinition{}, fmt.Errorf("%s: %w", definition.EntryFile, err)
		}
	} else if !hasPackageFunction(parsed, definition.Handler) {
		return WorkflowDefinition{}, fmt.Errorf("%s: handler function %s was not found in its definition folder", definition.EntryFile, definition.Handler)
	}
	return definition, nil
}

func hasPackageFunction(files map[string]*ast.File, name string) bool {
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Name.Name == name {
				return true
			}
		}
	}
	return false
}

func parseDefinitionPackage(root, dir string) ([]string, string, map[string]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", nil, err
	}
	parsed := make(map[string]*ast.File)
	var files []string
	packageName := ""
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		parsedFile, parseErr := parser.ParseFile(token.NewFileSet(), file, nil, parser.AllErrors)
		if parseErr != nil {
			return nil, "", nil, parseErr
		}
		if packageName != "" && packageName != parsedFile.Name.Name {
			return nil, "", nil, fmt.Errorf("%s mixes packages %q and %q", authorPath(root, dir), packageName, parsedFile.Name.Name)
		}
		packageName = parsedFile.Name.Name
		parsed[file] = parsedFile
		files = append(files, authorPath(root, file))
	}
	sort.Strings(files)
	return files, packageName, parsed, nil
}

func findDefinitionCall(file *ast.File, variable, constructor string) (ast.Expr, string, error) {
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
				if name.Name != variable {
					continue
				}
				if index >= len(value.Values) {
					return nil, "", fmt.Errorf("var %s must be initialized with %s(config, handler)", variable, constructor)
				}
				call, ok := value.Values[index].(*ast.CallExpr)
				if !ok || calledName(call.Fun) != constructor || len(call.Args) != 2 {
					return nil, "", fmt.Errorf("var %s must be initialized with %s(config, handler)", variable, constructor)
				}
				handler, ok := call.Args[1].(*ast.Ident)
				if !ok {
					return nil, "", fmt.Errorf("%s handler must be a package function identifier", variable)
				}
				return call.Args[0], handler.Name, nil
			}
		}
	}
	return nil, "", fmt.Errorf("missing exported var %s = workflows.%s(config, handler)", variable, constructor)
}

func staticConfigString(config ast.Expr, field string) (string, error) {
	composite, ok := config.(*ast.CompositeLit)
	if !ok {
		return "", fmt.Errorf("definition config must be an inline struct literal so the compiler can resolve %s", field)
	}
	for _, element := range composite.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := keyValue.Key.(*ast.Ident)
		if !ok || key.Name != field {
			continue
		}
		literal, ok := keyValue.Value.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return "", fmt.Errorf("%s must be a string literal", field)
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return "", err
		}
		return value, nil
	}
	return "", nil
}

func populateStandaloneSignature(files map[string]*ast.File, definition *WorkflowDefinition) error {
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != definition.Handler {
				continue
			}
			params := flattenFieldTypes(function.Type.Params)
			if len(params) > 0 && isContextType(params[0]) {
				params = params[1:]
			}
			if len(params) > 1 {
				return fmt.Errorf("standalone activity handler %s may accept at most one application input", definition.Handler)
			}
			if len(params) == 1 {
				definition.HandlerHasInput = true
				definition.InputType = expressionString(params[0])
			}
			results := flattenFieldTypes(function.Type.Results)
			if len(results) == 0 || !isErrorType(results[len(results)-1]) {
				return fmt.Errorf("standalone activity handler %s must return error or (output, error)", definition.Handler)
			}
			if len(results) > 2 {
				return fmt.Errorf("standalone activity handler %s may return at most one output plus error", definition.Handler)
			}
			if len(results) == 2 {
				definition.HandlerHasOutput = true
				definition.OutputType = expressionString(results[0])
			}
			return nil
		}
	}
	return fmt.Errorf("handler function %s was not found in its definition folder", definition.Handler)
}

func flattenFieldTypes(fields *ast.FieldList) []ast.Expr {
	if fields == nil {
		return nil
	}
	var expressions []ast.Expr
	for _, field := range fields.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			expressions = append(expressions, field.Type)
		}
	}
	return expressions
}

func calledName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func expressionString(expression ast.Expr) string {
	// The standalone activity wrapper re-emits handler types into generated Go.
	// ast.Fprint is diagnostic output for composite types (for example an
	// anonymous struct), not source code. go/format preserves any legal type
	// expression as valid generated source.
	var output bytes.Buffer
	if err := format.Node(&output, token.NewFileSet(), expression); err == nil {
		return output.String()
	}
	return "invalid_type_expression"
}

func isContextType(expression ast.Expr) bool {
	return expressionString(expression) == "context.Context"
}
func isErrorType(expression ast.Expr) bool { return expressionString(expression) == "error" }

func normalizeDefinitionPart(value string) (string, error) {
	return gb.NormalizeTaskQueueID(value)
}

func normalizeLogicalQueue(queue string) (string, error) {
	if strings.Contains(queue, gb.TaskQueueSeparator) {
		return "", fmt.Errorf("task queue %q must be logical; omit the environment suffix", queue)
	}
	return gb.NormalizeTaskQueueID(queue)
}

func WorkflowDefinitionKey(id string) string {
	digest := sha256.Sum256([]byte("workflow-definition:" + id))
	name := strings.Trim(safePart(id), "_")
	if name == "" {
		name = "workflow"
	}
	return "d_" + name + "_" + hex.EncodeToString(digest[:4])
}

func WorkflowQueueKey(id string) string {
	digest := sha256.Sum256([]byte("workflow-queue:" + id))
	name := strings.Trim(safePart(id), "_")
	if name == "" {
		name = "queue"
	}
	return "q_" + name + "_" + hex.EncodeToString(digest[:4])
}

func GroupWorkflowQueues(definitions []WorkflowDefinition) []WorkflowQueue {
	return GroupWorkerQueues(definitions, nil)
}

// GroupWorkerQueues combines authored workflows and durable agents by their
// effective logical Temporal task queue. Direct agents never create pollers.
func GroupWorkerQueues(definitions []WorkflowDefinition, agents []AgentDefinition) []WorkflowQueue {
	byQueue := make(map[string][]WorkflowDefinition)
	for _, definition := range definitions {
		byQueue[definition.TaskQueue] = append(byQueue[definition.TaskQueue], definition)
	}
	agentsByQueue := make(map[string]map[string]AgentDefinition)
	for _, definition := range agents {
		if !definition.Durable {
			continue
		}
		queues := []string{definition.TaskQueue}
		for _, tool := range definition.Tools {
			if tool.TaskQueue != "" && !containsString(queues, tool.TaskQueue) {
				queues = append(queues, tool.TaskQueue)
			}
		}
		for _, queue := range queues {
			if agentsByQueue[queue] == nil {
				agentsByQueue[queue] = map[string]AgentDefinition{}
			}
			agentsByQueue[queue][definition.ID] = definition
		}
	}
	ids := make([]string, 0, len(byQueue))
	for id := range byQueue {
		ids = append(ids, id)
	}
	for id := range agentsByQueue {
		if _, exists := byQueue[id]; !exists {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	queues := make([]WorkflowQueue, 0, len(ids))
	for _, id := range ids {
		definitions := byQueue[id]
		sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
		agentDefinitions := make([]AgentDefinition, 0, len(agentsByQueue[id]))
		for _, definition := range agentsByQueue[id] {
			agentDefinitions = append(agentDefinitions, definition)
		}
		sort.Slice(agentDefinitions, func(i, j int) bool { return agentDefinitions[i].ID < agentDefinitions[j].ID })
		queues = append(queues, WorkflowQueue{
			ID: id, Key: WorkflowQueueKey(id), Definitions: definitions, Agents: agentDefinitions,
		})
	}
	return queues
}

func readDefinitionDirs(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var directories []os.DirEntry
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() {
			return nil, fmt.Errorf("%s may contain only definition folders", filepath.ToSlash(dir))
		}
		directories = append(directories, entry)
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].Name() < directories[j].Name() })
	return directories, nil
}

func regularFile(file string) bool {
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular()
}

func regularDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
