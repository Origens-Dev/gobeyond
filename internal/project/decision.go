package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	gbagents "github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"gopkg.in/yaml.v3"
)

// compileDecisionRoutes reads the route-local YAML and prompt source into the
// frozen v1 contract. route.yaml uses the contract's JSON field names directly;
// it does not add a second route configuration model.
func compileDecisionRoutes(root, agentDir, routesDir string, definition decisionv1.Definition) (decisionv1.Definition, error) {
	if routesDir == "" || filepath.IsAbs(routesDir) {
		return decisionv1.Definition{}, errors.New("decision RoutesDir must be a non-empty relative path")
	}
	routesDir = filepath.Clean(filepath.FromSlash(routesDir))
	if routesDir == "." || routesDir == ".." || strings.HasPrefix(routesDir, ".."+string(filepath.Separator)) {
		return decisionv1.Definition{}, errors.New("decision RoutesDir must stay inside its agent directory")
	}
	routesRoot := filepath.Join(agentDir, routesDir)
	info, err := os.Lstat(routesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return decisionv1.Definition{}, fmt.Errorf("decision routes directory %s is missing", authorPath(root, routesRoot))
		}
		return decisionv1.Definition{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return decisionv1.Definition{}, errors.New("decision RoutesDir must be a real directory inside the agent package")
	}
	if err := validateDecisionPathWithinAgent(agentDir, routesRoot); err != nil {
		return decisionv1.Definition{}, fmt.Errorf("decision RoutesDir: %w", err)
	}
	if len(definition.Graph.Routes) != 0 {
		return decisionv1.Definition{}, errors.New("decision Graph.Routes must be empty; route.yaml files are the route source")
	}
	definition.SchemaVersion = decisionv1.SchemaVersion
	definition.DigestInputs = decisionv1.ReleaseDigestInputs{}
	definition.ReleaseSHA256 = ""

	messageTemplates := make(map[string]decisionv1.MessageFamily, len(definition.Graph.Messages))
	for i := range definition.Graph.Messages {
		message := &definition.Graph.Messages[i]
		if _, exists := messageTemplates[message.ID]; exists {
			return decisionv1.Definition{}, fmt.Errorf("decision graph duplicates message family %q", message.ID)
		}
		if len(message.Variants) != 0 {
			return decisionv1.Definition{}, fmt.Errorf("decision message family %q must load prompt bytes from route-local Markdown files", message.ID)
		}
		message.Variants = nil
		messageTemplates[message.ID] = *message
	}

	var routeFiles []string
	var promptFiles []string
	routeDirectories := make(map[string]bool)
	err = filepath.WalkDir(routesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("decision route tree cannot contain symlinks: %s", authorPath(root, path))
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("decision route source must be a regular file: %s", authorPath(root, path))
		}
		name := entry.Name()
		if name == "route.yaml" {
			routeFiles = append(routeFiles, path)
			routeDirectories[filepath.Dir(path)] = true
			return nil
		}
		if strings.HasPrefix(name, "prompt.") && strings.HasSuffix(name, ".md") {
			promptFiles = append(promptFiles, path)
			return nil
		}
		return fmt.Errorf("unsupported file in decision RoutesDir: %s", authorPath(root, path))
	})
	if err != nil {
		return decisionv1.Definition{}, err
	}
	sources := append(append([]string(nil), routeFiles...), promptFiles...)
	for _, source := range sources {
		if err := validateDecisionPathWithinAgent(agentDir, source); err != nil {
			return decisionv1.Definition{}, fmt.Errorf("decision route source %s: %w", authorPath(root, source), err)
		}
	}
	sort.Strings(routeFiles)
	sort.Strings(promptFiles)
	if len(routeFiles) == 0 {
		return decisionv1.Definition{}, errors.New("decision RoutesDir must contain at least one route.yaml")
	}

	routes := make([]decisionv1.Route, 0, len(routeFiles))
	seenRoutes := make(map[decisionv1.RouteID]string, len(routeFiles))
	routeIDsByDirectory := make(map[string]decisionv1.RouteID, len(routeFiles))
	for _, routeFile := range routeFiles {
		relative, relErr := filepath.Rel(routesRoot, filepath.Dir(routeFile))
		if relErr != nil || relative == "." {
			return decisionv1.Definition{}, fmt.Errorf("route.yaml must be inside a route directory: %s", authorPath(root, routeFile))
		}
		routeID := decisionv1.RouteID("/" + filepath.ToSlash(relative))
		file, openErr := os.Open(routeFile)
		if openErr != nil {
			return decisionv1.Definition{}, openErr
		}
		yamlDecoder := yaml.NewDecoder(file)
		var routeDocument map[string]any
		decodeErr := yamlDecoder.Decode(&routeDocument)
		if decodeErr == nil && routeDocument == nil {
			decodeErr = errors.New("route.yaml must contain a mapping document")
		}
		var route decisionv1.Route
		if decodeErr == nil {
			var extra any
			extraErr := yamlDecoder.Decode(&extra)
			if !errors.Is(extraErr, io.EOF) {
				if extraErr == nil {
					decodeErr = errors.New("route.yaml must contain one YAML document")
				} else {
					decodeErr = extraErr
				}
			}
		}
		closeErr := file.Close()
		if decodeErr != nil {
			return decisionv1.Definition{}, fmt.Errorf("decode %s: %w", authorPath(root, routeFile), decodeErr)
		}
		if closeErr != nil {
			return decisionv1.Definition{}, closeErr
		}
		if decodeErr == nil {
			var routeJSON []byte
			routeJSON, decodeErr = json.Marshal(routeDocument)
			if decodeErr == nil {
				jsonDecoder := json.NewDecoder(bytes.NewReader(routeJSON))
				jsonDecoder.DisallowUnknownFields()
				decodeErr = jsonDecoder.Decode(&route)
			}
		}
		if decodeErr != nil {
			return decisionv1.Definition{}, fmt.Errorf("decode %s: %w", authorPath(root, routeFile), decodeErr)
		}
		if route.ID != "" && route.ID != routeID {
			if previous, exists := seenRoutes[route.ID]; exists {
				return decisionv1.Definition{}, fmt.Errorf("duplicate decision route path %q in %s and %s", route.ID, authorPath(root, previous), authorPath(root, routeFile))
			}
			return decisionv1.Definition{}, fmt.Errorf("route ID %q in %s must match its directory path %q", route.ID, authorPath(root, routeFile), routeID)
		}
		if previous, exists := seenRoutes[routeID]; exists {
			return decisionv1.Definition{}, fmt.Errorf("duplicate decision route path %q in %s and %s", routeID, authorPath(root, previous), authorPath(root, routeFile))
		}
		seenRoutes[routeID] = routeFile
		routeIDsByDirectory[filepath.Dir(routeFile)] = routeID
		route.ID = routeID
		routes = append(routes, route)
	}
	promptVariants := make(map[string]map[string]string, len(promptFiles))
	for _, promptFile := range promptFiles {
		routeID, exists := routeIDsByDirectory[filepath.Dir(promptFile)]
		if !exists || !routeDirectories[filepath.Dir(promptFile)] {
			return decisionv1.Definition{}, fmt.Errorf("prompt file must live beside a route.yaml: %s", authorPath(root, promptFile))
		}
		localFamilyID, locale, parseErr := decisionPromptIdentity(promptFile)
		if parseErr != nil {
			return decisionv1.Definition{}, fmt.Errorf("%s: %w", authorPath(root, promptFile), parseErr)
		}
		familyID := decisionPromptFamilyID(routeID, localFamilyID)
		content, readErr := os.ReadFile(promptFile)
		if readErr != nil {
			return decisionv1.Definition{}, readErr
		}
		if !utf8.Valid(content) {
			return decisionv1.Definition{}, fmt.Errorf("prompt file %s must contain valid UTF-8", authorPath(root, promptFile))
		}
		variants := promptVariants[familyID]
		if variants == nil {
			variants = make(map[string]string)
			promptVariants[familyID] = variants
		}
		if _, duplicate := variants[locale]; duplicate {
			return decisionv1.Definition{}, fmt.Errorf("duplicate route-local prompt family/locale %q/%q", familyID, locale)
		}
		variants[locale] = string(content)
	}
	compiledMessages := make([]decisionv1.MessageFamily, 0, len(promptVariants))
	compiledMessageIDs := make(map[string]int, len(promptVariants))
	usedPromptFamilies := make(map[string]bool, len(promptVariants))
	usedTemplateIDs := make(map[string]bool, len(messageTemplates))
	for routeIndex := range routes {
		route := &routes[routeIndex]
		if route.Say == nil {
			continue
		}
		for familyIndex, localFamilyID := range route.Say.Families {
			isShared := strings.HasPrefix(localFamilyID, "shared:")
			messageFamilyID := localFamilyID
			if isShared {
				messageFamilyID = strings.TrimPrefix(localFamilyID, "shared:")
				if messageFamilyID == "" {
					return decisionv1.Definition{}, fmt.Errorf("route %s has an empty shared message family reference", route.ID)
				}
			}
			localPromptID := decisionPromptFamilyID(route.ID, messageFamilyID)
			templateID := localPromptID
			compiledFamilyID := localPromptID
			if isShared {
				templateID = messageFamilyID
				compiledFamilyID = messageFamilyID
			}
			template, exists := messageTemplates[templateID]
			if !exists {
				if isShared {
					return decisionv1.Definition{}, fmt.Errorf("route %s references undeclared shared message family %q", route.ID, messageFamilyID)
				}
				return decisionv1.Definition{}, fmt.Errorf("route %s references undeclared route-local message family %q; define metadata as %q", route.ID, messageFamilyID, localPromptID)
			}
			variants, exists := promptVariants[localPromptID]
			if !exists {
				return decisionv1.Definition{}, fmt.Errorf("route %s is missing route-local prompt family %q", route.ID, localFamilyID)
			}
			if isShared {
				if previousIndex, shared := compiledMessageIDs[compiledFamilyID]; shared {
					if !equalDecisionPromptVariants(compiledMessages[previousIndex].Variants, variants) {
						return decisionv1.Definition{}, fmt.Errorf("shared prompt family %q has different route-local source bytes", compiledFamilyID)
					}
				} else {
					message := template
					message.ID = compiledFamilyID
					message.Variants = variants
					compiledMessages = append(compiledMessages, message)
					compiledMessageIDs[compiledFamilyID] = len(compiledMessages) - 1
				}
			} else if _, exists := compiledMessageIDs[compiledFamilyID]; !exists {
				message := template
				message.ID = compiledFamilyID
				message.Variants = variants
				compiledMessages = append(compiledMessages, message)
				compiledMessageIDs[compiledFamilyID] = len(compiledMessages) - 1
			}
			route.Say.Families[familyIndex] = compiledFamilyID
			usedPromptFamilies[localPromptID] = true
			usedTemplateIDs[templateID] = true
		}
	}
	for familyID := range promptVariants {
		if !usedPromptFamilies[familyID] {
			return decisionv1.Definition{}, fmt.Errorf("route-local prompt family %q is not referenced by its route", familyID)
		}
	}
	for templateID := range messageTemplates {
		if !usedTemplateIDs[templateID] {
			return decisionv1.Definition{}, fmt.Errorf("decision graph message family template %q is not referenced by a route", templateID)
		}
	}
	definition.Graph.Routes = routes
	definition.Graph.Messages = compiledMessages
	if _, _, _, err = gbagents.FreezeDecisionManifest(decisionWithParentPlaceholder(definition)); err != nil {
		return decisionv1.Definition{}, fmt.Errorf("decision definition: %w", err)
	}
	return definition, nil
}

// validateDecisionPathWithinAgent rejects symlinks in every component below
// the decision agent directory. Checking only the final RoutesDir path misses
// an ancestor such as routes-link/routes that resolves outside the package.
func validateDecisionPathWithinAgent(agentDir, path string) error {
	agentRoot, err := filepath.Abs(agentDir)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(agentRoot, target)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("path must stay inside its agent directory")
	}
	rootInfo, err := os.Lstat(agentRoot)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("agent directory must be a real directory")
	}
	cursor := agentRoot
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cursor = filepath.Join(cursor, part)
		info, statErr := os.Lstat(cursor)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %s cannot be a symlink", cursor)
		}
		if cursor != target && !info.IsDir() {
			return fmt.Errorf("path component %s must be a directory", cursor)
		}
	}
	return nil
}

func equalDecisionPromptVariants(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for locale, value := range left {
		other, exists := right[locale]
		if !exists || other != value {
			return false
		}
	}
	return true
}

func decisionPromptIdentity(path string) (familyID, locale string, err error) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "prompt.") || !strings.HasSuffix(name, ".md") {
		return "", "", errors.New("prompt filename must use prompt.<family>.<locale>.md")
	}
	stem := strings.TrimSuffix(name, ".md")
	parts := strings.Split(stem, ".")
	if len(parts) < 2 || parts[0] != "prompt" {
		return "", "", errors.New("prompt filename must include a locale")
	}
	locale = parts[len(parts)-1]
	if locale == "" {
		return "", "", errors.New("prompt filename must include a locale")
	}
	familyID = "prompt"
	if len(parts) > 2 {
		familyID += "." + strings.Join(parts[1:len(parts)-1], ".")
	}
	return familyID, locale, nil
}

// decisionPromptFamilyID makes a route-local family globally unique in the
// frozen graph while preserving the local name in route.yaml. Hex-encoding the
// full route ID avoids collisions between nested paths and dotted route IDs.
func decisionPromptFamilyID(routeID decisionv1.RouteID, localFamilyID string) string {
	return "route." + hex.EncodeToString([]byte(routeID)) + "." + localFamilyID
}

func decisionWithParentPlaceholder(definition decisionv1.Definition) decisionv1.Definition {
	if definition.Graph.Authority.ParentManifestSHA256 == "" {
		definition.Graph.Authority.ParentManifestSHA256 = strings.Repeat("0", 64)
	}
	return definition
}

func validateDecisionDefinition(definition decisionv1.Definition, tools []AgentToolDefinition) error {
	toolByID := make(map[string]AgentToolDefinition, len(tools))
	for _, tool := range tools {
		toolByID[tool.ID] = tool
	}
	for _, grant := range definition.Graph.Authority.Tools {
		tool, exists := toolByID[grant.ID]
		if !exists {
			return fmt.Errorf("decision authority references tool %q that is not declared in DecisionConfig.Tools", grant.ID)
		}
		if grant.SchemaSHA256 != tool.SchemaSHA256 {
			return fmt.Errorf("decision tool %q schema digest does not match its shared DefineTool input schema", grant.ID)
		}
		if (grant.ApprovalSHA256 != "") != tool.RequiresApproval {
			return fmt.Errorf("decision tool %q approval policy does not match its shared DefineTool RequiresApproval setting", grant.ID)
		}
	}
	if _, _, _, err := gbagents.FreezeDecisionManifest(decisionWithParentPlaceholder(definition)); err != nil {
		return err
	}
	return validateDecisionCompilerAuthoritySnapshots(definition)
}

// The parent agent projection currently freezes tools and channels, but it
// does not carry the service, binding, or budget policy needed to verify those
// AuthorityEnvelope claims. Keep those claims as unresolved author snapshots
// until a future adapter consumes and verifies the exact parent authority.
func validateDecisionCompilerAuthoritySnapshots(definition decisionv1.Definition) error {
	for _, kind := range []decisionv1.GateKind{
		decisionv1.GateJevServicePath,
		decisionv1.GateCallerAuthority,
		decisionv1.GateSessionAdapter,
	} {
		found := false
		for _, gate := range definition.PolicyGates {
			if gate.Kind != kind {
				continue
			}
			found = true
			if gate.Status != decisionv1.GateUnresolved {
				return fmt.Errorf("decision authority snapshot gate %q must remain unresolved until the frozen parent authority adapter is implemented", kind)
			}
		}
		if !found {
			return fmt.Errorf("decision authority snapshot requires unresolved %q gate", kind)
		}
	}
	for _, dimension := range decisionv1.RequiredBudgetDimensions {
		bound, exists := definition.Graph.Authority.Budgets[dimension]
		if !exists || bound.Value != nil {
			return fmt.Errorf("decision budget snapshot %q must remain unresolved until the frozen parent authority adapter is implemented", dimension)
		}
	}
	return nil
}

func decisionToolInputSchemaSHA256(call *ast.CallExpr) (string, error) {
	if call == nil || len(call.Args) == 0 {
		return "", errors.New("DefineTool requires an inline ToolConfig")
	}
	config, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return "", errors.New("decision ToolConfig must be an inline literal")
	}
	var schema ast.Expr
	for _, element := range config.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return "", errors.New("decision ToolConfig must use named fields")
		}
		key, ok := field.Key.(*ast.Ident)
		if ok && key.Name == "InputSchema" {
			schema = field.Value
			break
		}
	}
	if schema == nil {
		return "", errors.New("decision tools require a static InputSchema")
	}
	literal, err := voiceLiteral(schema, 0)
	if err != nil {
		return "", fmt.Errorf("decision tool InputSchema: %w", err)
	}
	raw, err := json.Marshal(literal)
	if err != nil {
		return "", fmt.Errorf("encode decision tool InputSchema: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func decisionToolRequiresApproval(call *ast.CallExpr) (bool, error) {
	if call == nil || len(call.Args) == 0 {
		return false, errors.New("DefineTool requires an inline ToolConfig")
	}
	config, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return false, errors.New("decision ToolConfig must be an inline literal")
	}
	for _, element := range config.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return false, errors.New("decision ToolConfig must use named fields")
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "RequiresApproval" {
			continue
		}
		value, ok := field.Value.(*ast.Ident)
		if !ok || (value.Name != "true" && value.Name != "false") {
			return false, errors.New("decision ToolConfig RequiresApproval must be a boolean literal")
		}
		return value.Name == "true", nil
	}
	return false, nil
}

// attachDecisionManifests freezes each graph inside the existing agent record.
// The parent digest is SHA-256 over that same projected agent record with its
// Decision field omitted. It proves the projected agent identity, mode, task
// queue, channels/slots, tool IDs/queues, and included voice-manifest fields
// are stable; it does not prove service, binding, or budget policy. Generic
// decision tool schemas and approval policy are checked against DefineTool
// source separately. Those authority claims remain gated until an adapter
// verifies the exact frozen parent authority without adding another ledger.
func attachDecisionManifests(manifest *AgentsManifest, definitions []AgentDefinition) error {
	for index := range definitions {
		if definitions[index].Decision == nil {
			continue
		}
		if index >= len(manifest.Agents) || manifest.Agents[index].ID != definitions[index].ID {
			return errors.New("decision compiler and parent agent manifest order differ")
		}
		parent := manifest.Agents[index]
		parentSHA, err := decisionParentManifestSHA256(parent)
		if err != nil {
			return fmt.Errorf("parent agent manifest for %s: %w", parent.ID, err)
		}
		definition := *definitions[index].Decision
		if configured := definition.Graph.Authority.ParentManifestSHA256; configured != "" && configured != parentSHA {
			return fmt.Errorf("agent %s decision parent manifest digest does not match its frozen agent record", parent.ID)
		}
		definition.Graph.Authority.ParentManifestSHA256 = parentSHA
		frozen, _, _, err := gbagents.FreezeDecisionManifest(definition)
		if err != nil {
			return fmt.Errorf("freeze decision manifest for agent %s: %w", parent.ID, err)
		}
		manifest.Agents[index].Decision = &frozen
	}
	return nil
}

func decisionParentManifestSHA256(agent AgentManifestDefinition) (string, error) {
	agent.Decision = nil
	parentBytes, err := json.Marshal(agent)
	if err != nil {
		return "", fmt.Errorf("marshal frozen agent record: %w", err)
	}
	digest := sha256.Sum256(parentBytes)
	return hex.EncodeToString(digest[:]), nil
}
