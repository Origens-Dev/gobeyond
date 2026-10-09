package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	gbagents "github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"gopkg.in/yaml.v3"
)

func TestCompileDecisionRoutesMatchesFrozenManifestAndDigests(t *testing.T) {
	root := writeDecisionCompilerFixture(t, nil)
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("DiscoverAgentDefinitions = %#v, %v", definitions, err)
	}
	definition := definitions[0]
	if definition.Kind != AgentKindDecision || definition.Decision == nil || len(definition.Decision.Graph.Routes) != 3 {
		t.Fatalf("decision discovery = %#v", definition)
	}
	if got := definition.Decision.Graph.Messages[0].Variants["en"]; got != "Hi {name}, who do you want to call?" {
		t.Fatalf("compiler did not preserve exact prompt bytes: %q", got)
	}
	if len(definition.Tools) != 2 || definition.Tools[0].ID != "connect" || definition.Tools[1].ID != "operator_context" {
		t.Fatalf("shared DefineTool declarations were not retained: %#v", definition.Tools)
	}
	if !reflect.DeepEqual(definition.Slots.Tools, []string{"connect", "existing", "operator_context"}) {
		t.Fatalf("tool slots = %#v", definition.Slots.Tools)
	}
	if len(definition.Slots.Channels) != 1 || definition.Slots.Channels[0] != (AgentChannel{ID: "voice", Connector: "voice-provider"}) {
		t.Fatalf("channel slots = %#v", definition.Slots.Channels)
	}

	setAgentRevisions(definitions, "b_decision_fixture")
	manifest := portableAgentsManifest(definitions, "b_decision_fixture")
	manifest.APIVersion = "gobeyond.agents/v1alpha6"
	if err := attachVoiceManifests(&manifest, definitions); err != nil {
		t.Fatal(err)
	}
	if err := attachDecisionManifests(&manifest, definitions); err != nil {
		t.Fatal(err)
	}
	compiled := *manifest.Agents[0].Decision
	if compiled.Graph.Authority.ParentManifestSHA256 == "" || strings.Trim(compiled.Graph.Authority.ParentManifestSHA256, "0") == "" {
		t.Fatalf("parent agent record was not bound: %q", compiled.Graph.Authority.ParentManifestSHA256)
	}
	parentDigest, err := decisionParentManifestSHA256(manifest.Agents[0])
	if err != nil || parentDigest != compiled.Graph.Authority.ParentManifestSHA256 {
		t.Fatalf("parent digest = %q, want %q (err %v)", compiled.Graph.Authority.ParentManifestSHA256, parentDigest, err)
	}

	frozen, frozenBytes, digest, err := gbagents.FreezeDecisionManifest(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frozen, compiled) {
		t.Fatal("compiler output differs from the shared frozen manifest canonicalizer")
	}
	if digest != compiled.ReleaseSHA256 || digest == "" {
		t.Fatalf("release digest = %q, definition has %q", digest, compiled.ReleaseSHA256)
	}
	manifestDecisionBytes, err := json.MarshalIndent(manifest.Agents[0].Decision, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestDecisionBytes = append(manifestDecisionBytes, '\n')
	if !reflect.DeepEqual(frozenBytes, manifestDecisionBytes) {
		t.Fatal("nested manifest bytes differ from the shared frozen serializer")
	}

	permuted := compiled
	permuted.Graph.Routes = append([]decisionv1.Route(nil), compiled.Graph.Routes...)
	for left, right := 0, len(permuted.Graph.Routes)-1; left < right; left, right = left+1, right-1 {
		permuted.Graph.Routes[left], permuted.Graph.Routes[right] = permuted.Graph.Routes[right], permuted.Graph.Routes[left]
	}
	permuted.Graph.Authority.Tools = append([]decisionv1.ToolGrant(nil), compiled.Graph.Authority.Tools...)
	for left, right := 0, len(permuted.Graph.Authority.Tools)-1; left < right; left, right = left+1, right-1 {
		permuted.Graph.Authority.Tools[left], permuted.Graph.Authority.Tools[right] = permuted.Graph.Authority.Tools[right], permuted.Graph.Authority.Tools[left]
	}
	permuted.PolicyGates = append([]decisionv1.PolicyGate(nil), compiled.PolicyGates...)
	for left, right := 0, len(permuted.PolicyGates)-1; left < right; left, right = left+1, right-1 {
		permuted.PolicyGates[left], permuted.PolicyGates[right] = permuted.PolicyGates[right], permuted.PolicyGates[left]
	}
	_, permutedBytes, permutedDigest, err := gbagents.FreezeDecisionManifest(permuted)
	if err != nil {
		t.Fatal(err)
	}
	_, repeatedBytes, repeatedDigest, err := gbagents.FreezeDecisionManifest(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if digest != repeatedDigest || !reflect.DeepEqual(frozenBytes, repeatedBytes) {
		t.Fatal("repeated freeze changed manifest bytes or digest")
	}
	if digest != permutedDigest || !reflect.DeepEqual(frozenBytes, permutedBytes) {
		t.Fatal("permuting set-valued inputs changed canonical manifest bytes or digest")
	}
	changedPrompt := compiled
	changedPrompt.Graph.Messages = append([]decisionv1.MessageFamily(nil), compiled.Graph.Messages...)
	changedPrompt.Graph.Messages[0].Variants = map[string]string{"en": compiled.Graph.Messages[0].Variants["en"] + " "}
	_, _, changedDigest, err := gbagents.FreezeDecisionManifest(changedPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == digest {
		t.Fatal("prompt byte change did not change release digest")
	}

	manifestPath := filepath.Join(root, ".gobeyond", "agents.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAgentsManifest(root)
	if err != nil {
		t.Fatalf("LoadAgentsManifest rejected compiler manifest: %v", err)
	}
	loadedDecisionBytes, err := json.Marshal(loaded.Agents[0].Decision)
	if err != nil {
		t.Fatal(err)
	}
	compiledDecisionBytes, err := json.Marshal(&compiled)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loadedDecisionBytes, compiledDecisionBytes) {
		t.Fatal("loaded agent manifest does not preserve the compiler's frozen decision wire bytes")
	}
	_, loadedFrozenBytes, loadedDigest, err := gbagents.FreezeDecisionManifest(*loaded.Agents[0].Decision)
	if err != nil {
		t.Fatal(err)
	}
	if loadedDigest != digest || !bytes.Equal(loadedFrozenBytes, frozenBytes) {
		t.Fatal("loaded agent manifest differs from the shared frozen manifest serializer")
	}
}

func TestDecisionCompilerRejectsUnsafeFixtures(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, string)
		configure func(*decisionv1.Definition)
		want      string
	}{
		{name: "missing route", mutate: func(t *testing.T, root string) { removeDecisionRoute(t, root, "/help") }, want: "missing"},
		{name: "missing prompt", mutate: func(t *testing.T, root string) { removeDecisionPrompt(t, root, "prompt.help") }, want: "missing"},
		{name: "missing base locale", mutate: func(t *testing.T, root string) { renameDecisionPrompt(t, root, "prompt.help", "fr") }, want: "base message"},
		{name: "duplicate route path alias", mutate: func(t *testing.T, root string) {
			route := readDecisionRoute(t, root, "/start")
			route.ID = "/help"
			writeDecisionRoute(t, root, "/start", route)
		}, want: "duplicate decision route path"},
		{name: "unknown matcher", mutate: func(t *testing.T, root string) {
			route := readDecisionRoute(t, root, "/start")
			route.Match.BindingID = "missing_matcher"
			writeDecisionRoute(t, root, "/start", route)
		}, want: "outside its inherited binding grants"},
		{name: "unknown tool", mutate: func(t *testing.T, root string) {
			route := readDecisionRoute(t, root, "/start")
			route.Act[0].ToolID = "missing_tool"
			writeDecisionRoute(t, root, "/start", route)
		}, want: "outside its inherited tool grants"},
		{name: "incompatible tool schema", configure: func(definition *decisionv1.Definition) {
			definition.Graph.Authority.Tools[0].SchemaSHA256 = strings.Repeat("f", 64)
		}, want: "schema digest does not match"},
		{name: "unbounded retry cycle", mutate: func(t *testing.T, root string) {
			route := readDecisionRoute(t, root, "/clarify")
			route.RetryGroup = ""
			writeDecisionRoute(t, root, "/clarify", route)
		}, want: "route cycle"},
		{name: "privilege expansion", mutate: func(t *testing.T, root string) {
			route := readDecisionRoute(t, root, "/start")
			route.UseTools = []string{"undeclared"}
			writeDecisionRoute(t, root, "/start", route)
		}, want: "outside its inherited tool grants"},
		{name: "budget expansion", configure: func(definition *decisionv1.Definition) {
			one := uint64(1)
			for index := range definition.PolicyGates {
				if definition.PolicyGates[index].ID == "g-route-visits" {
					definition.PolicyGates[index].Status = decisionv1.GateQualified
					definition.PolicyGates[index].Value = "1"
					definition.PolicyGates[index].EvidenceRef = "test evidence"
					definition.PolicyGates[index].UnresolvedReason = ""
				}
			}
			definition.Graph.Authority.Budgets[decisionv1.BudgetRouteVisits] = decisionv1.Bound{Value: &one, GateID: "g-route-visits"}
		}, mutate: func(t *testing.T, root string) {
			route := readDecisionRoute(t, root, "/start")
			two := uint64(2)
			route.BudgetOverrides = map[decisionv1.BudgetDimension]decisionv1.Bound{
				decisionv1.BudgetRouteVisits: {Value: &two, GateID: "g-route-visits"},
			}
			writeDecisionRoute(t, root, "/start", route)
		}, want: "widens"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeDecisionCompilerFixture(t, test.configure)
			if test.mutate != nil {
				test.mutate(t, root)
			}
			_, err := DiscoverAgentDefinitions(root)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("DiscoverAgentDefinitions error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDecisionAgentGeneratedCodeStaysRuntimeInert(t *testing.T) {
	definition := AgentDefinition{ID: "operator", Key: "agent0", Kind: AgentKindDecision, Mode: AgentModeDurable, Durable: true, TaskQueue: "decision", PackageName: "operator"}
	registration, err := generatedAgentRegistration(definition)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(registration), "GobeyondRegister") || strings.Contains(string(registration), "AIDefinition") || strings.Contains(string(registration), "Invoke") {
		t.Fatalf("decision registration contains runtime wiring: %s", registration)
	}
	registry, err := renderRegistry("example.com/site", nil, nil, nil, []AgentDefinition{definition}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, runtimeReference := range []string{"agent0 \"", "AgentDispatcher", "agentRegistry", "GobeyondRegister"} {
		if strings.Contains(string(registry), runtimeReference) {
			t.Fatalf("decision registry output contains %q", runtimeReference)
		}
	}
	siteMain, err := renderSiteMain("example.com/site", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(siteMain), "AgentDispatcher") || strings.Contains(string(siteMain), "temporalruntime") {
		t.Fatalf("decision-only site entrypoint contains runtime wiring: %s", siteMain)
	}
	if queues := GroupWorkerQueues(nil, []AgentDefinition{definition}); len(queues) != 0 {
		t.Fatalf("decision agent created worker queues: %#v", queues)
	}
}

var decisionToolInputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"candidate_id": map[string]any{"type": "string"},
	},
}

func writeDecisionCompilerFixture(t *testing.T, configure func(*decisionv1.Definition)) string {
	t.Helper()
	root := t.TempDir()
	definitionBytes, err := os.ReadFile(filepath.Join("..", "..", "agents", "decisioncontract", "v1", "testdata", "review-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition decisionv1.Definition
	if err := json.Unmarshal(definitionBytes, &definition); err != nil {
		t.Fatal(err)
	}
	routes := append([]decisionv1.Route(nil), definition.Graph.Routes...)
	definition.Graph.Routes = nil
	definition.Graph.Authority.ParentManifestSHA256 = ""
	definition.Graph.Locale.EnabledLocales = []string{"en"}
	replacements := map[string]string{
		"operator.prompt":       "prompt",
		"operator.choice_count": "prompt.choice_count",
		"operator.help":         "prompt.help",
	}
	promptBytes := map[string]string{}
	for index := range definition.Graph.Messages {
		message := &definition.Graph.Messages[index]
		promptBytes[replacements[message.ID]] = message.Variants[message.BaseLocale]
		message.ID = replacements[message.ID]
		message.Variants = nil
	}
	for routeIndex := range routes {
		route := &routes[routeIndex]
		if route.Say != nil {
			for index, family := range route.Say.Families {
				route.Say.Families[index] = replacements[family]
			}
		}
	}
	schemaBytes, err := json.Marshal(decisionToolInputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schemaDigest := sha256.Sum256(schemaBytes)
	for index := range definition.Graph.Authority.Tools {
		definition.Graph.Authority.Tools[index].SchemaSHA256 = hex.EncodeToString(schemaDigest[:])
	}
	if configure != nil {
		configure(&definition)
	}

	for _, route := range routes {
		writeDecisionRoute(t, root, string(route.ID), route)
	}
	startDir := decisionRouteDir(root, "/start")
	for family, prompt := range promptBytes {
		filename := "prompt" + strings.TrimPrefix(family, "prompt") + ".en.md"
		writeSourceTestFile(t, filepath.Join(startDir, filename), prompt)
	}
	writeDecisionCompilerAgent(t, root, definition)
	return root
}

func writeDecisionCompilerAgent(t *testing.T, root string, definition decisionv1.Definition) {
	t.Helper()
	configLiteral := decisionGoLiteral(reflect.ValueOf(definition))
	source := fmt.Sprintf(`package operator

import (
	"context"
	gbagents "github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

var connectTool = gbagents.DefineTool(gbagents.ToolConfig{InputSchema: %s}, func(context.Context, gbagents.Actor, map[string]any) (string, error) { return "", nil })
var contextTool = gbagents.DefineTool(gbagents.ToolConfig{InputSchema: %s}, func(context.Context, gbagents.Actor, map[string]any) (string, error) { return "", nil })

var Agent = gbagents.DefineDecision(gbagents.DecisionConfig{
	Config: gbagents.Config{TaskQueue: "decision", Durable: true, Public: true},
	Tools: map[string]gbagents.AITool{"connect": connectTool, "operator_context": contextTool},
	RoutesDir: "routes",
	Definition: %s,
}, gbagents.Slots{
	Tools: []gbagents.Tool{{ID: "existing"}},
	Channels: []gbagents.Channel{{ID: "voice", Connector: "voice-provider"}},
})
`, decisionTestSchemaLiteral(), decisionTestSchemaLiteral(), configLiteral)
	writeSourceTestFile(t, filepath.Join(root, "agents", "operator", "agent.go"), source)
}

func decisionTestSchemaLiteral() string {
	return `map[string]any{"type": "object", "properties": map[string]any{"candidate_id": map[string]any{"type": "string"}}}`
}

func decisionGoLiteral(value reflect.Value) string {
	if !value.IsValid() {
		return "nil"
	}
	typeOf := value.Type()
	typeName := decisionGoType(typeOf)
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return "nil"
		}
		return "&" + decisionGoLiteral(value.Elem())
	case reflect.Struct:
		fields := make([]string, 0, value.NumField())
		for index := 0; index < value.NumField(); index++ {
			field := typeOf.Field(index)
			if !field.IsExported() {
				continue
			}
			fields = append(fields, field.Name+": "+decisionGoLiteral(value.Field(index)))
		}
		if typeOf.Name() == "" {
			return "struct{" + strings.Join(fields, ",") + "}{" + strings.Join(fields, ",") + "}"
		}
		return typeName + "{" + strings.Join(fields, ",") + "}"
	case reflect.Slice, reflect.Array:
		if value.IsNil() {
			return "nil"
		}
		items := make([]string, value.Len())
		for index := range items {
			items[index] = decisionGoLiteral(value.Index(index))
		}
		return typeName + "{" + strings.Join(items, ",") + "}"
	case reflect.Map:
		if value.IsNil() {
			return "nil"
		}
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, decisionGoLiteral(key)+":"+decisionGoLiteral(value.MapIndex(key)))
		}
		return typeName + "{" + strings.Join(items, ",") + "}"
	case reflect.String:
		literal := strconv.Quote(value.String())
		if typeOf.Name() != "" {
			return typeName + "(" + literal + ")"
		}
		return literal
	case reflect.Bool:
		return strconv.FormatBool(value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(value.Float(), 'g', -1, typeOf.Bits())
	default:
		panic("unsupported decision fixture type: " + typeOf.String())
	}
}

func decisionGoType(typeOf reflect.Type) string {
	if typeOf.Name() != "" {
		if typeOf.PkgPath() != "" {
			return "decisionv1." + typeOf.Name()
		}
		return typeOf.Name()
	}
	switch typeOf.Kind() {
	case reflect.Pointer:
		return "*" + decisionGoType(typeOf.Elem())
	case reflect.Slice:
		return "[]" + decisionGoType(typeOf.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", typeOf.Len(), decisionGoType(typeOf.Elem()))
	case reflect.Map:
		return "map[" + decisionGoType(typeOf.Key()) + "]" + decisionGoType(typeOf.Elem())
	default:
		return typeOf.String()
	}
}

func writeDecisionRoute(t *testing.T, root, routeID string, route decisionv1.Route) {
	t.Helper()
	jsonBytes, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(jsonBytes, &document); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeSourceTestFile(t, filepath.Join(decisionRouteDir(root, routeID), "route.yaml"), string(raw))
}

func decisionRouteDir(root, routeID string) string {
	return filepath.Join(root, "agents", "operator", "routes", filepath.FromSlash(strings.TrimPrefix(routeID, "/")))
}

func readDecisionRoute(t *testing.T, root, routeID string) decisionv1.Route {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(decisionRouteDir(root, routeID), "route.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var route decisionv1.Route
	if err := json.Unmarshal(jsonBytes, &route); err != nil {
		t.Fatal(err)
	}
	return route
}

func removeDecisionRoute(t *testing.T, root, routeID string) {
	t.Helper()
	if err := os.Remove(filepath.Join(decisionRouteDir(root, routeID), "route.yaml")); err != nil {
		t.Fatal(err)
	}
}

func removeDecisionPrompt(t *testing.T, root, family string) {
	t.Helper()
	path := filepath.Join(decisionRouteDir(root, "/start"), "prompt"+strings.TrimPrefix(family, "prompt")+".en.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func renameDecisionPrompt(t *testing.T, root, family, locale string) {
	t.Helper()
	path := filepath.Join(decisionRouteDir(root, "/start"), "prompt"+strings.TrimPrefix(family, "prompt")+".en.md")
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), "prompt"+strings.TrimPrefix(family, "prompt")+"."+locale+".md")); err != nil {
		t.Fatal(err)
	}
}
