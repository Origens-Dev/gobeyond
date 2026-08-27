package project

import (
	"bytes"
	"encoding/json"
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
)

// WakeManifest is the dist/deploy/wake.json payload. Keys are
// WorkflowDefinition.Name values; values are logical worker IDs (no __env).
type WakeManifest struct {
	V         int                 `json:"v"`
	Workflows map[string][]string `json:"workflows"`
}

// BuildWakeMap computes the per-workflow wake closure:
// own queue ∪ reachable activity queues ∪ transitive subworkflow queues.
func BuildWakeMap(root string, definitions []WorkflowDefinition) (map[string][]string, error) {
	refQueues := referenceTaskQueues(definitions)

	direct := make(map[string]map[string]struct{})
	children := make(map[string]map[string]struct{})
	for _, definition := range definitions {
		if definition.Kind != WorkflowKind {
			continue
		}
		queues := map[string]struct{}{}
		if definition.TaskQueue != "" {
			queues[definition.TaskQueue] = struct{}{}
		}
		childNames := map[string]struct{}{}
		refs, err := scanWorkflowWakeReferences(root, definition)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			queue, ok := refQueues[ref.VarName]
			if !ok {
				return nil, fmt.Errorf("%s: unknown reference %s in Execute%s", definition.EntryFile, ref.VarName, ref.Kind)
			}
			if queue != "" {
				queues[queue] = struct{}{}
			}
			if ref.Kind == "Subworkflow" {
				if childName := workflowNameForReferenceVar(definitions, ref.VarName); childName != "" && childName != definition.Name {
					childNames[childName] = struct{}{}
				}
			}
		}
		direct[definition.Name] = queues
		children[definition.Name] = childNames
	}

	out := make(map[string][]string, len(direct))
	for name := range direct {
		closed, err := closeWakeQueues(name, direct, children, map[string]bool{})
		if err != nil {
			return nil, err
		}
		out[name] = closed
	}
	return out, nil
}

func referenceTaskQueues(definitions []WorkflowDefinition) map[string]string {
	refQueues := make(map[string]string)
	usedNames := make(map[string]struct{})
	for _, definition := range definitions {
		if definition.Kind == WorkflowKind || definition.Standalone {
			refQueues[uniqueWorkflowReferenceName("Workflow", definition, usedNames)] = definition.TaskQueue
		}
		if definition.Kind == ActivityKind {
			refQueues[uniqueWorkflowReferenceName("Activity", definition, usedNames)] = definition.TaskQueue
		}
	}
	return refQueues
}

type wakeRef struct {
	Kind    string // ActivityReference | Subworkflow
	VarName string
}

func scanWorkflowWakeReferences(root string, definition WorkflowDefinition) ([]wakeRef, error) {
	var refs []wakeRef
	seen := make(map[string]struct{})
	fset := token.NewFileSet()
	for _, relative := range definition.SourceFiles {
		path := filepath.FromSlash(relative)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: read source: %w", definition.EntryFile, err)
		}
		file, err := parser.ParseFile(fset, path, content, parser.AllErrors)
		if err != nil {
			return nil, fmt.Errorf("%s: parse: %w", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			kind := wakeCallKind(call.Fun)
			if kind == "" || len(call.Args) < 2 {
				return true
			}
			varName := referenceVarName(call.Args[1])
			if varName == "" {
				return true
			}
			key := kind + ":" + varName
			if _, exists := seen[key]; exists {
				return true
			}
			seen[key] = struct{}{}
			refs = append(refs, wakeRef{Kind: kind, VarName: varName})
			return true
		})
	}
	return refs, nil
}

func wakeCallKind(fun ast.Expr) string {
	switch typed := fun.(type) {
	case *ast.SelectorExpr:
		switch typed.Sel.Name {
		case "ExecuteActivityReference":
			return "ActivityReference"
		case "ExecuteSubworkflow":
			return "Subworkflow"
		}
	case *ast.Ident:
		switch typed.Name {
		case "ExecuteActivityReference":
			return "ActivityReference"
		case "ExecuteSubworkflow":
			return "Subworkflow"
		}
	}
	return ""
}

func referenceVarName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.Ident:
		return typed.Name
	default:
		return ""
	}
}

func workflowNameForReferenceVar(definitions []WorkflowDefinition, varName string) string {
	usedNames := make(map[string]struct{})
	for _, definition := range definitions {
		if definition.Kind == WorkflowKind || definition.Standalone {
			name := uniqueWorkflowReferenceName("Workflow", definition, usedNames)
			if name == varName {
				return definition.Name
			}
		}
		if definition.Kind == ActivityKind {
			_ = uniqueWorkflowReferenceName("Activity", definition, usedNames)
		}
	}
	return ""
}

func closeWakeQueues(
	name string,
	direct map[string]map[string]struct{},
	children map[string]map[string]struct{},
	stack map[string]bool,
) ([]string, error) {
	if stack[name] {
		return nil, fmt.Errorf("wake graph cycle involving %q", name)
	}
	stack[name] = true
	defer delete(stack, name)

	queues := map[string]struct{}{}
	for queue := range direct[name] {
		queues[queue] = struct{}{}
	}
	for child := range children[name] {
		childQueues, err := closeWakeQueues(child, direct, children, stack)
		if err != nil {
			return nil, err
		}
		for _, queue := range childQueues {
			queues[queue] = struct{}{}
		}
	}
	out := make([]string, 0, len(queues))
	for queue := range queues {
		if queue != "" {
			out = append(out, queue)
		}
	}
	sort.Strings(out)
	return out, nil
}

// PortableWakeManifest builds the JSON-serializable wake manifest.
func PortableWakeManifest(root string, definitions []WorkflowDefinition) (WakeManifest, error) {
	wakeMap, err := BuildWakeMap(root, definitions)
	if err != nil {
		return WakeManifest{}, err
	}
	manifest := WakeManifest{V: 1, Workflows: wakeMap}
	if manifest.Workflows == nil {
		manifest.Workflows = map[string][]string{}
	}
	return manifest, nil
}

func generatedWakeSource(root string, definitions []WorkflowDefinition) ([]byte, error) {
	wakeMap, err := BuildWakeMap(root, definitions)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(wakeMap))
	for name := range wakeMap {
		names = append(names, name)
	}
	sort.Strings(names)

	var source strings.Builder
	source.WriteString(generatedSourceMarker)
	source.WriteString("\npackage wake\n\n")
	source.WriteString("// WakeWorkers returns the compiler-derived logical worker IDs that must\n")
	source.WriteString("// be warmed for workflow name (WorkflowDefinition.Name). Unknown names\n")
	source.WriteString("// return nil.\n")
	source.WriteString("func WakeWorkers(name string) []string {\n")
	source.WriteString("\tswitch name {\n")
	for _, name := range names {
		queues := wakeMap[name]
		source.WriteString("\tcase ")
		source.WriteString(strconv.Quote(name))
		source.WriteString(":\n\t\treturn []string{")
		for i, queue := range queues {
			if i > 0 {
				source.WriteString(", ")
			}
			source.WriteString(strconv.Quote(queue))
		}
		source.WriteString("}\n")
	}
	source.WriteString("\tdefault:\n\t\treturn nil\n\t}\n}\n\n")
	source.WriteString("// Manifest returns the portable wake.json projection.\n")
	source.WriteString("func Manifest() map[string][]string {\n")
	source.WriteString("\treturn map[string][]string{\n")
	for _, name := range names {
		source.WriteString("\t\t")
		source.WriteString(strconv.Quote(name))
		source.WriteString(": {")
		for i, queue := range wakeMap[name] {
			if i > 0 {
				source.WriteString(", ")
			}
			source.WriteString(strconv.Quote(queue))
		}
		source.WriteString("},\n")
	}
	source.WriteString("\t}\n}\n")

	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated wake source: %w\n%s", err, source.String())
	}
	return formatted, nil
}

// MarshalWakeManifest returns indented wake.json bytes with stable key order.
func MarshalWakeManifest(root string, definitions []WorkflowDefinition) ([]byte, error) {
	manifest, err := PortableWakeManifest(root, definitions)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("{\n  \"v\": 1,\n  \"workflows\": {")
	names := make([]string, 0, len(manifest.Workflows))
	for name := range manifest.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString("\n    ")
		nameJSON, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf.Write(nameJSON)
		buf.WriteString(": ")
		queuesJSON, err := json.Marshal(manifest.Workflows[name])
		if err != nil {
			return nil, err
		}
		buf.Write(queuesJSON)
	}
	if len(names) > 0 {
		buf.WriteString("\n  ")
	}
	buf.WriteString("}\n}\n")
	return buf.Bytes(), nil
}
