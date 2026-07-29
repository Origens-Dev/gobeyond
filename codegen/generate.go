package codegen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultOutputDir      = "internal/gobeyondgen/contracts"
	defaultSafeHTMLImport = "github.com/Origens-Dev/gobeyond/renderplan"
)

type Options struct {
	// OutputDir is the slash-separated relative directory used as the root of
	// returned paths. It defaults to internal/gobeyondgen/contracts.
	OutputDir string
	// SafeHTMLImportPath supplies the package containing SafeHTML. It defaults
	// to the GoBeyond renderplan package.
	SafeHTMLImportPath string
}

// Generate returns deterministic, go/format-formatted generated files keyed by
// slash-separated project-relative path. Generate never writes to disk.
func Generate(document Document, options Options) (map[string][]byte, error) {
	if err := Validate(document); err != nil {
		return nil, err
	}
	outputDir := options.OutputDir
	if outputDir == "" {
		outputDir = defaultOutputDir
	}
	if err := validateOutputDir(outputDir); err != nil {
		return nil, err
	}
	safeHTMLImport := options.SafeHTMLImportPath
	if safeHTMLImport == "" {
		safeHTMLImport = defaultSafeHTMLImport
	}
	if strings.TrimSpace(safeHTMLImport) != safeHTMLImport || safeHTMLImport == "" || strings.ContainsAny(safeHTMLImport, "\\\r\n\"\t ") {
		return nil, fmt.Errorf("invalid SafeHTML import path %q", safeHTMLImport)
	}

	files := make(map[string][]byte, len(document.Routes)+len(document.Actions))
	routePackages := map[string]string{}
	for _, route := range document.Routes {
		packageName, err := packageIdentifier(route.RouteID)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.RouteID, err)
		}
		if previous, exists := routePackages[packageName]; exists {
			return nil, fmt.Errorf("route IDs %q and %q map to the same Go package %q", previous, route.RouteID, packageName)
		}
		routePackages[packageName] = route.RouteID
		generated, err := generatePackage(packageName, "RouteID", route.RouteID, []rootDefinition{{Name: "Props", Value: route.Props}}, safeHTMLImport, &route, nil)
		if err != nil {
			return nil, fmt.Errorf("generate route %q: %w", route.RouteID, err)
		}
		files[path.Join(outputDir, "routes", packageName, "types.gobeyond_gen.go")] = generated
	}

	actionPackages := map[string]string{}
	for _, action := range document.Actions {
		packageName, err := packageIdentifier(action.ActionID)
		if err != nil {
			return nil, fmt.Errorf("action %q: %w", action.ActionID, err)
		}
		if previous, exists := actionPackages[packageName]; exists {
			return nil, fmt.Errorf("action IDs %q and %q map to the same Go package %q", previous, action.ActionID, packageName)
		}
		actionPackages[packageName] = action.ActionID
		generated, err := generatePackage(packageName, "ActionID", action.ActionID, []rootDefinition{
			{Name: "Input", Value: action.Input},
			{Name: "Output", Value: action.Output},
		}, safeHTMLImport, nil, &action)
		if err != nil {
			return nil, fmt.Errorf("generate action %q: %w", action.ActionID, err)
		}
		files[path.Join(outputDir, "actions", packageName, "types.gobeyond_gen.go")] = generated
	}
	return files, nil
}

// GenerateRouteSchema projects one route contract into the route's authored Go
// package. The returned source is intended for page.schema.go: it gives route
// authors local Props and cache metadata without importing generated internals.
func GenerateRouteSchema(route Route, packageName string, options Options) ([]byte, error) {
	if err := Validate(Document{APIVersion: APIVersionV1Alpha1, Routes: []Route{route}, Actions: []Action{}}); err != nil {
		return nil, err
	}
	if strings.TrimSpace(packageName) == "" {
		return nil, errors.New("Go package name is required")
	}
	safeHTMLImport := options.SafeHTMLImportPath
	if safeHTMLImport == "" {
		safeHTMLImport = defaultSafeHTMLImport
	}
	generator := &generator{
		packageName:    packageName,
		safeHTMLImport: safeHTMLImport,
		imports:        map[string]string{},
		typeNames:      map[string]string{},
	}
	if _, err := generator.namedType("Props", route.Props, "Props"); err != nil {
		return nil, err
	}
	generator.addRouteCache(route)
	generator.imports["json"] = "encoding/json"
	generator.imports["fmt"] = "fmt"
	generator.declarations = append(generator.declarations, `// PropsFrom projects a JSON-compatible domain value into this route's Props.
// Prefer constructing Props directly when the mapping is already clear.
func PropsFrom(value any) (Props, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Props{}, fmt.Errorf("encode props: %w", err)
	}
	var props Props
	if err := json.Unmarshal(encoded, &props); err != nil {
		return Props{}, fmt.Errorf("decode props: %w", err)
	}
	return props, nil
}`)
	return generatedSource(generator, "RouteID", route.RouteID)
}

type rootDefinition struct {
	Name  string
	Value Value
}

type generator struct {
	packageName    string
	safeHTMLImport string
	imports        map[string]string
	declarations   []string
	typeNames      map[string]string
}

func generatePackage(packageName, identityName, identityValue string, roots []rootDefinition, safeHTMLImport string, route *Route, action *Action) ([]byte, error) {
	generator := &generator{
		packageName:    packageName,
		safeHTMLImport: safeHTMLImport,
		imports:        map[string]string{},
		typeNames:      map[string]string{},
	}
	for _, root := range roots {
		if _, err := generator.namedType(root.Name, root.Value, root.Name); err != nil {
			return nil, err
		}
	}
	if route != nil {
		generator.addRouteCache(*route)
	}
	if action != nil {
		generator.addActionBoundary(*action)
	}

	return generatedSource(generator, identityName, identityValue)
}

func generatedSource(generator *generator, identityName, identityValue string) ([]byte, error) {
	var source bytes.Buffer
	source.WriteString("// Code generated by GoBeyond. DO NOT EDIT.\n")
	source.WriteString("// Contract API: " + APIVersionV1Alpha1 + "\n\n")
	source.WriteString("package " + generator.packageName + "\n")
	if len(generator.imports) > 0 {
		aliases := make([]string, 0, len(generator.imports))
		for alias := range generator.imports {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		source.WriteString("\nimport (\n")
		for _, alias := range aliases {
			importPath := generator.imports[alias]
			if path.Base(importPath) == alias {
				fmt.Fprintf(&source, "\t%q\n", importPath)
			} else {
				fmt.Fprintf(&source, "\t%s %q\n", alias, importPath)
			}
		}
		source.WriteString(")\n")
	}
	source.WriteString("\nconst " + identityName + " = " + strconv.Quote(identityValue) + "\n")
	for _, declaration := range generator.declarations {
		source.WriteString("\n")
		source.WriteString(declaration)
		if !strings.HasSuffix(declaration, "\n") {
			source.WriteString("\n")
		}
	}
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated package %s: %w\n%s", generator.packageName, err, source.String())
	}
	return formatted, nil
}

// addRouteCache projects definePage's route caching into the same package the
// route's props type lives in, so a loader registration reads its ISR window
// from the schema that declared it instead of repeating the number in Go.
// Both declarations are always emitted, including for uncached routes, so
// registration code can reference them unconditionally.
func (g *generator) addRouteCache(route Route) {
	g.imports["time"] = "time"
	tags := "var Tags []string"
	if len(route.Tags) > 0 {
		quoted := make([]string, len(route.Tags))
		for index, tag := range route.Tags {
			quoted[index] = strconv.Quote(tag)
		}
		tags = "var Tags = []string{" + strings.Join(quoted, ", ") + "}"
	}
	g.declarations = append(g.declarations,
		"// Revalidate is how long the origin may reuse this route's computed props,\n"+
			"// from definePage({ revalidate }). Zero leaves the route uncached. It is not\n"+
			"// an HTTP directive: the edge Cache-Control stays the loader's gb.CachePolicy,\n"+
			"// which should be derived from this window rather than drifting from it.\n"+
			"const Revalidate = "+strconv.Itoa(route.Revalidate)+" * time.Second",
		"// Tags are this route's invalidation handles, from definePage({ tags }).\n"+
			"// cache.RevalidateTag on any of them drops the route's cached props.\n"+
			tags,
	)
}

func (g *generator) addActionBoundary(action Action) {
	g.imports["json"] = "encoding/json"
	g.imports["gb"] = "github.com/Origens-Dev/gobeyond"
	g.imports["codegen"] = "github.com/Origens-Dev/gobeyond/codegen"
	g.imports["gbruntime"] = "github.com/Origens-Dev/gobeyond/runtime"
	g.declarations = append(g.declarations,
		"var inputContract = "+valueContractLiteral(action.Input),
		"var outputContract = "+valueContractLiteral(action.Output),
		`func DecodeInput(raw json.RawMessage) (Input, error) {
	var input Input
	if err := codegen.DecodeJSON(inputContract, raw, &input); err != nil {
		return Input{}, err
	}
	return input, nil
}`,
		`func ValidateOutput(output Output) error {
	return codegen.ValidateEncodedValue(outputContract, output)
}`,
		`func Register(handler func(*gb.ActionContext, Input) (Output, error)) gbruntime.Action {
	return gbruntime.RegisterAction(ActionID, DecodeInput, ValidateOutput, handler)
}`,
	)
}

func valueContractLiteral(value Value) string {
	var fields []string
	fields = append(fields, "Kind: codegen.Kind"+contractKindSuffix(value.Kind))
	if value.Optional {
		fields = append(fields, "Optional: true")
	}
	if value.Nullable {
		fields = append(fields, "Nullable: true")
	}
	switch value.Kind {
	case KindLiteral:
		fields = append(fields, "Literal: "+contractLiteral(value.Literal))
	case KindEnum:
		values := make([]string, len(value.Values))
		for index, item := range value.Values {
			values[index] = strconv.Quote(item)
		}
		fields = append(fields, "Values: []string{"+strings.Join(values, ", ")+"}")
	case KindArray:
		if value.Items != nil {
			fields = append(fields, "Items: &"+valueContractLiteral(*value.Items))
		}
	case KindObject:
		names := make([]string, 0, len(value.Shape))
		for name := range value.Shape {
			names = append(names, name)
		}
		sort.Strings(names)
		properties := make([]string, 0, len(names))
		for _, name := range names {
			properties = append(properties, strconv.Quote(name)+": "+valueContractLiteral(value.Shape[name]))
		}
		fields = append(fields, "Shape: map[string]codegen.Value{"+strings.Join(properties, ", ")+"}")
	case KindUnion:
		variants := make([]string, len(value.Variants))
		for index, variant := range value.Variants {
			variants[index] = valueContractLiteral(variant)
		}
		fields = append(fields, "Variants: []codegen.Value{"+strings.Join(variants, ", ")+"}")
	}
	return "codegen.Value{" + strings.Join(fields, ", ") + "}"
}

func contractKindSuffix(kind Kind) string {
	switch kind {
	case KindSafeHTML:
		return "SafeHTML"
	case KindDateTime:
		return "DateTime"
	default:
		return strings.ToUpper(string(kind[:1])) + string(kind[1:])
	}
}

func contractLiteral(value any) string {
	switch literal := value.(type) {
	case string:
		return strconv.Quote(literal)
	case bool:
		return strconv.FormatBool(literal)
	case json.Number:
		return "json.Number(" + strconv.Quote(string(literal)) + ")"
	case float64:
		return strconv.FormatFloat(literal, 'g', -1, 64)
	case int:
		return strconv.Itoa(literal)
	case int64:
		return strconv.FormatInt(literal, 10)
	default:
		return "nil"
	}
}

func (g *generator) namedType(name string, value Value, location string) (string, error) {
	if prior, exists := g.typeNames[name]; exists {
		return "", fmt.Errorf("generated type name %q collides between %s and %s", name, prior, location)
	}
	g.typeNames[name] = location

	switch value.Kind {
	case KindObject:
		return g.objectType(name, value, location)
	case KindEnum:
		return g.enumType(name, value.Values, value, location)
	case KindUnion:
		values, err := stringUnionValues(value)
		if err != nil {
			return "", err
		}
		return g.enumType(name, values, value, location)
	default:
		underlying, err := g.unnamedType(name, value, location)
		if err != nil {
			return "", err
		}
		g.declarations = append(g.declarations, fmt.Sprintf("type %s %s", name, underlying))
		return pointerIfNeeded(name, value), nil
	}
}

func (g *generator) objectType(name string, value Value, location string) (string, error) {
	declarationIndex := len(g.declarations)
	g.declarations = append(g.declarations, "")
	propertyNames := make([]string, 0, len(value.Shape))
	for propertyName := range value.Shape {
		propertyNames = append(propertyNames, propertyName)
	}
	sort.Strings(propertyNames)
	fields := make([]string, 0, len(propertyNames))
	fieldNames := map[string]string{}
	for _, propertyName := range propertyNames {
		fieldName, err := exportedIdentifier(propertyName)
		if err != nil {
			return "", fmt.Errorf("%s property %q: %w", location, propertyName, err)
		}
		if previous, exists := fieldNames[fieldName]; exists {
			return "", fmt.Errorf("%s properties %q and %q map to the same Go field %q", location, previous, propertyName, fieldName)
		}
		fieldNames[fieldName] = propertyName
		fieldType, err := g.typeFor(name+fieldName, value.Shape[propertyName], location+"."+propertyName)
		if err != nil {
			return "", err
		}
		tag := propertyName
		if value.Shape[propertyName].Optional {
			tag += ",omitempty"
		}
		comment := ""
		if value.Shape[propertyName].Optional && value.Shape[propertyName].Nullable {
			comment = " // MVP: absent and null both decode as nil."
		}
		fields = append(fields, fmt.Sprintf("\t%s %s `json:%s`%s", fieldName, fieldType, strconv.Quote(tag), comment))
	}
	declaration := "type " + name + " struct {\n" + strings.Join(fields, "\n") + "\n}"
	// Parent declarations precede their nested declarations for readable output.
	g.declarations[declarationIndex] = declaration
	return pointerIfNeeded(name, value), nil
}

func (g *generator) enumType(name string, values []string, value Value, location string) (string, error) {
	constants := make([]string, 0, len(values))
	constantNames := map[string]string{}
	for _, enumValue := range values {
		suffix := enumConstantSuffix(enumValue)
		constantName := name + suffix
		if previous, exists := constantNames[constantName]; exists {
			return "", fmt.Errorf("%s enum values %q and %q map to the same Go constant %q", location, previous, enumValue, constantName)
		}
		constantNames[constantName] = enumValue
		constants = append(constants, fmt.Sprintf("\t%s %s = %s", constantName, name, strconv.Quote(enumValue)))
	}
	declaration := fmt.Sprintf("type %s string\n\nconst (\n%s\n)", name, strings.Join(constants, "\n"))
	g.declarations = append(g.declarations, declaration)
	return pointerIfNeeded(name, value), nil
}

func (g *generator) typeFor(name string, value Value, location string) (string, error) {
	switch value.Kind {
	case KindObject, KindEnum, KindUnion:
		return g.namedType(name, value, location)
	default:
		base, err := g.unnamedType(name, value, location)
		if err != nil {
			return "", err
		}
		return pointerIfNeeded(base, value), nil
	}
}

func (g *generator) unnamedType(name string, value Value, location string) (string, error) {
	switch value.Kind {
	case KindString:
		return "string", nil
	case KindNumber:
		return "float64", nil
	case KindInteger:
		return "int64", nil
	case KindBoolean:
		return "bool", nil
	case KindDateTime:
		g.imports["time"] = "time"
		return "time.Time", nil
	case KindBytes:
		return "[]byte", nil
	case KindSafeHTML:
		g.imports["renderplan"] = g.safeHTMLImport
		return "renderplan.SafeHTML", nil
	case KindLiteral:
		switch literal := value.Literal.(type) {
		case nil:
			return "any", nil
		case string:
			return "string", nil
		case bool:
			return "bool", nil
		case json.Number:
			if strings.ContainsAny(string(literal), ".eE") {
				return "float64", nil
			}
			return "int64", nil
		case float64:
			return "float64", nil
		case int, int64:
			return "int64", nil
		default:
			return "", fmt.Errorf("%s has unsupported literal type %T", location, value.Literal)
		}
	case KindArray:
		if value.Items == nil {
			return "", fmt.Errorf("%s array has no items", location)
		}
		itemType, err := g.typeFor(name+"Item", *value.Items, location+"[]")
		if err != nil {
			return "", err
		}
		return "[]" + itemType, nil
	default:
		return "", fmt.Errorf("%s has unsupported unnamed kind %q", location, value.Kind)
	}
}

func pointerIfNeeded(typeName string, value Value) string {
	if value.Optional || value.Nullable {
		return "*" + typeName
	}
	return typeName
}

func validateOutputDir(outputDir string) error {
	if strings.Contains(outputDir, "\\") || path.IsAbs(outputDir) {
		return fmt.Errorf("output directory must be a slash-separated project-relative path: %q", outputDir)
	}
	clean := path.Clean(outputDir)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != outputDir {
		return fmt.Errorf("output directory must be a clean project-relative path: %q", outputDir)
	}
	return nil
}

func packageIdentifier(value string) (string, error) {
	words := identifierWords(value)
	if len(words) == 0 {
		return "", fmt.Errorf("ID %q does not contain an ASCII letter or digit", value)
	}
	identifier := strings.ToLower(strings.Join(words, "_"))
	if identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "contract_" + identifier
	}
	if goKeywords[identifier] {
		identifier = "contract_" + identifier
	}
	return identifier, nil
}

func exportedIdentifier(value string) (string, error) {
	words := identifierWords(value)
	if len(words) == 0 {
		return "", fmt.Errorf("name does not contain an ASCII letter or digit")
	}
	var result strings.Builder
	for _, word := range words {
		lower := strings.ToLower(word)
		if initialism, exists := initialisms[lower]; exists {
			result.WriteString(initialism)
			continue
		}
		result.WriteString(strings.ToUpper(lower[:1]))
		result.WriteString(lower[1:])
	}
	identifier := result.String()
	if identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "Value" + identifier
	}
	return identifier, nil
}

func enumConstantSuffix(value string) string {
	if value == "" {
		return "Empty"
	}
	identifier, err := exportedIdentifier(value)
	if err != nil {
		return "Value" + strings.ToUpper(strconv.FormatInt(stableHash(value), 36))
	}
	return identifier
}

func identifierWords(value string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = nil
		}
	}
	characters := []rune(value)
	for index, char := range characters {
		if char > unicode.MaxASCII || !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			flush()
			continue
		}
		if len(current) > 0 && unicode.IsUpper(char) {
			previous := current[len(current)-1]
			nextIsLower := index+1 < len(characters) && unicode.IsLower(characters[index+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || unicode.IsUpper(previous) && nextIsLower {
				flush()
			}
		}
		current = append(current, char)
	}
	flush()
	return words
}

func stableHash(value string) int64 {
	var hash int64 = 1469598103934665603
	for index := 0; index < len(value); index++ {
		hash ^= int64(value[index])
		hash *= 1099511628211
	}
	if hash < 0 {
		return -hash
	}
	return hash
}

var initialisms = map[string]string{
	"api":   "API",
	"css":   "CSS",
	"html":  "HTML",
	"http":  "HTTP",
	"https": "HTTPS",
	"id":    "ID",
	"ip":    "IP",
	"json":  "JSON",
	"seo":   "SEO",
	"sql":   "SQL",
	"svg":   "SVG",
	"url":   "URL",
	"uri":   "URI",
	"uuid":  "UUID",
	"xml":   "XML",
}

var goKeywords = map[string]bool{
	"break": true, "default": true, "func": true, "interface": true, "select": true,
	"case": true, "defer": true, "go": true, "map": true, "struct": true,
	"chan": true, "else": true, "goto": true, "package": true, "switch": true,
	"const": true, "fallthrough": true, "if": true, "range": true, "type": true,
	"continue": true, "for": true, "import": true, "return": true, "var": true,
}
