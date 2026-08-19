package project

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type pageWire struct {
	Route      Route
	Alias      string
	ImportPath string
	Params     []string // exported Params field names mapped from ctx.Params
	ParamKeys  map[string]string
	HasParams  bool
	HasPage    bool
	PageResult bool
	Indexable  *bool
	HasConfig  bool
	Contract   string // import path for contracts/routes/<id> if present
}

type apiWire struct {
	Key        string
	Pattern    string
	Alias      string
	ImportPath string
	Methods    []string
}

type actionWire struct {
	Alias          string
	RouteImport    string
	RouteAlias     string
	ContractImport string
	FuncName       string
}

func generateSiteArtifacts(root, websiteImport string, routes []Route) (map[string][]byte, error) {
	pages := make([]pageWire, 0, len(routes))
	var actions []actionWire
	for index, route := range routes {
		alias := fmt.Sprintf("page%d", index)
		wire := pageWire{
			Route:      route,
			Alias:      alias,
			ImportPath: path.Join(websiteImport, GeneratedDir, "routes", route.ID),
		}
		if route.ServerFile != "" {
			wire.HasPage = true
			authorFile := websiteFile(root, route.ServerFile)
			params, hasParams, indexable, hasConfig, pageResult, err := inspectPageFile(authorFile)
			if err != nil {
				return nil, err
			}
			wire.Params = params
			wire.ParamKeys = paramKeys(route.Pattern, params)
			wire.HasParams = hasParams
			wire.Indexable = indexable
			wire.HasConfig = hasConfig
			wire.PageResult = pageResult
			if contractPath, found, findErr := findRouteContract(root, websiteImport, route.ID); findErr != nil {
				return nil, findErr
			} else if found {
				wire.Contract = contractPath
			}
			actionFile := filepath.Join(filepath.Dir(authorFile), "actions.go")
			if _, err := os.Stat(actionFile); err == nil {
				funcs, inspectErr := exportedFuncs(actionFile)
				if inspectErr != nil {
					return nil, inspectErr
				}
				for _, name := range funcs {
					contractPath, found, findErr := findActionContract(root, websiteImport, route.ID, name)
					if findErr != nil {
						return nil, findErr
					}
					if !found {
						continue
					}
					actions = append(actions, actionWire{
						Alias:          fmt.Sprintf("action%d", len(actions)),
						RouteImport:    wire.ImportPath,
						RouteAlias:     alias,
						ContractImport: contractPath,
						FuncName:       name,
					})
				}
			}
		}
		pages = append(pages, wire)
	}

	apis, err := discoverAPIWires(root, websiteImport)
	if err != nil {
		return nil, err
	}
	agents, err := DiscoverAgentDefinitions(root)
	if err != nil {
		return nil, err
	}
	middlewareSource, err := DiscoverGoMiddleware(root)
	if err != nil {
		return nil, err
	}

	registry, err := renderRegistry(websiteImport, pages, apis, actions, agents, middlewareSource != "")
	if err != nil {
		return nil, err
	}
	hasDurableAgents := false
	for _, definition := range agents {
		if definition.Durable {
			hasDurableAgents = true
			break
		}
	}
	siteMain, err := renderSiteMain(websiteImport, len(agents) > 0, hasDurableAgents)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		filepath.Join(root, GeneratedDir, "registry", "site.go"):    registry,
		filepath.Join(root, GeneratedDir, "cmd", "site", "main.go"): siteMain,
	}, nil
}

func findRouteContract(root, websiteImport, routeID string) (string, bool, error) {
	routesRoot := filepath.Join(root, GeneratedDir, "contracts", "routes")
	match, found, err := findGeneratedContract(routesRoot, "RouteID", routeID)
	if err != nil || !found {
		return "", found, err
	}
	return path.Join(websiteImport, GeneratedDir, "contracts/routes", match), true, nil
}

func findActionContract(root, websiteImport, routeID, funcName string) (string, bool, error) {
	actionsRoot := filepath.Join(root, GeneratedDir, "contracts", "actions")
	actionID := routeID + ":" + lowerFirst(funcName)
	match, found, err := findGeneratedContract(actionsRoot, "ActionID", actionID)
	if err != nil || !found {
		return "", found, err
	}
	return path.Join(websiteImport, GeneratedDir, "contracts/actions", match), true, nil
}

func findGeneratedContract(root, constName, constValue string) (string, bool, error) {
	entries, err := os.ReadDir(root)
	if errorsIsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ok, inspectErr := generatedContractConst(filepath.Join(root, entry.Name(), "types.gobeyond_gen.go"), constName, constValue)
		if inspectErr != nil {
			return "", false, inspectErr
		}
		if ok {
			matches = append(matches, entry.Name())
		}
	}
	if len(matches) != 1 {
		return "", false, nil
	}
	return matches[0], true, nil
}

func generatedContractConst(file, constName, constValue string) (bool, error) {
	content, err := os.ReadFile(file)
	if errorsIsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, content, 0)
	if err != nil {
		return false, err
	}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range valueSpec.Names {
				if name.Name != constName || index >= len(valueSpec.Values) {
					continue
				}
				literal, ok := valueSpec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				value, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr != nil {
					return false, unquoteErr
				}
				return value == constValue, nil
			}
		}
	}
	return false, nil
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func lowerFirst(value string) string {
	if value == "" {
		return value
	}
	return strings.ToLower(value[:1]) + value[1:]
}

func inspectPageFile(file string) (params []string, hasParams bool, indexable *bool, hasConfig bool, pageResult bool, err error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, false, nil, false, false, err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, content, 0)
	if err != nil {
		return nil, false, nil, false, false, err
	}
	for _, decl := range parsed.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == "Page" {
			pageResult = pageReturnsPageResult(fn)
			continue
		}
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			switch item := spec.(type) {
			case *ast.TypeSpec:
				if item.Name.Name == "Params" {
					hasParams = true
					structType, ok := item.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range structType.Fields.List {
						for _, name := range field.Names {
							params = append(params, name.Name)
						}
					}
				}
			case *ast.ValueSpec:
				for i, name := range item.Names {
					switch name.Name {
					case "Config":
						hasConfig = true
					case "Indexable":
						if i < len(item.Values) {
							if ident, ok := item.Values[i].(*ast.Ident); ok {
								value := ident.Name == "true"
								indexable = &value
							}
						}
					}
				}
			}
		}
	}
	return params, hasParams, indexable, hasConfig, pageResult, nil
}

func pageReturnsPageResult(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	return isPageResultType(fn.Type.Results.List[0].Type)
}

func isPageResultType(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name == "PageResult"
	case *ast.IndexExpr:
		return isPageResultType(typed.X)
	case *ast.IndexListExpr:
		return isPageResultType(typed.X)
	}
	return false
}

func paramKeys(pattern string, fields []string) map[string]string {
	keys := make(map[string]string, len(fields))
	params := routeParamNames(pattern)
	for _, field := range fields {
		keys[field] = strings.ToLower(field)
		for _, param := range params {
			if exportedParamName(param) == field {
				keys[field] = param
				break
			}
		}
	}
	return keys
}

func routeParamNames(pattern string) []string {
	var params []string
	for _, segment := range strings.Split(strings.Trim(pattern, "/"), "/") {
		switch {
		case strings.HasPrefix(segment, "[[...") && strings.HasSuffix(segment, "]]"):
			params = append(params, segment[5:len(segment)-2])
		case strings.HasPrefix(segment, "[...") && strings.HasSuffix(segment, "]"):
			params = append(params, segment[4:len(segment)-1])
		case strings.HasPrefix(segment, "[") && strings.HasSuffix(segment, "]"):
			params = append(params, segment[1:len(segment)-1])
		}
	}
	return params
}

func exportedParamName(param string) string {
	parts := strings.FieldsFunc(param, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	})
	var out strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		for i, word := range splitCamel(part) {
			if i == 0 && out.Len() == 0 {
				out.WriteString(strings.ToUpper(word[:1]))
				out.WriteString(word[1:])
				continue
			}
			if strings.EqualFold(word, "id") {
				out.WriteString("ID")
			} else {
				out.WriteString(strings.ToUpper(word[:1]))
				out.WriteString(word[1:])
			}
		}
	}
	return out.String()
}

func splitCamel(value string) []string {
	if value == "" {
		return nil
	}
	var words []string
	start := 0
	for index := 1; index < len(value); index++ {
		if value[index] >= 'A' && value[index] <= 'Z' {
			words = append(words, value[start:index])
			start = index
		}
	}
	return append(words, value[start:])
}

func exportedFuncs(file string) ([]string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, content, 0)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil || !fn.Name.IsExported() {
			continue
		}
		names = append(names, fn.Name.Name)
	}
	sort.Strings(names)
	return names, nil
}

func discoverAPIWires(root, websiteImport string) ([]apiWire, error) {
	apiRoot := filepath.Join(root, "app", "api")
	var apis []apiWire
	err := filepath.WalkDir(apiRoot, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errorsIsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "route.go" {
			return nil
		}
		rel, relErr := filepath.Rel(apiRoot, filepath.Dir(file))
		if relErr != nil {
			return relErr
		}
		key := APIKey(rel)
		methods, methodErr := httpMethodFuncs(file)
		if methodErr != nil {
			return methodErr
		}
		pattern := "/api"
		if rel != "." {
			pattern += "/" + filepath.ToSlash(rel)
		}
		apis = append(apis, apiWire{
			Key:        key,
			Pattern:    pattern,
			Alias:      fmt.Sprintf("api%d", len(apis)),
			ImportPath: path.Join(websiteImport, GeneratedDir, "api", key),
			Methods:    methods,
		})
		return nil
	})
	if err != nil && !errorsIsNotExist(err) {
		return nil, err
	}
	return apis, nil
}

func httpMethodFuncs(file string) ([]string, error) {
	funcs, err := exportedFuncs(file)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
	}
	var methods []string
	for _, name := range funcs {
		if allowed[name] {
			methods = append(methods, name)
		}
	}
	return methods, nil
}

func renderRegistry(websiteImport string, pages []pageWire, apis []apiWire, actions []actionWire, agents []AgentDefinition, hasMiddleware bool) ([]byte, error) {
	needsGB := true
	for _, page := range pages {
		if page.HasPage {
			needsGB = true
			break
		}
	}
	var imports []string
	imports = append(imports,
		`"net/http"`,
	)
	if needsGB {
		imports = append(imports, `gb "github.com/Origens-Dev/gobeyond"`)
	}
	imports = append(imports,
		`"github.com/Origens-Dev/gobeyond/browserassets"`,
		`"github.com/Origens-Dev/gobeyond/cache/openfromenv"`,
		`"github.com/Origens-Dev/gobeyond/renderplan"`,
		`"github.com/Origens-Dev/gobeyond/router"`,
		`gbruntime "github.com/Origens-Dev/gobeyond/runtime"`,
		`routes "`+path.Join(websiteImport, GeneratedDir, "routes")+`"`,
	)
	if len(agents) > 0 {
		imports = append(imports, `httpruntime "github.com/Origens-Dev/gobeyond/agents/httpruntime"`)
		for index, definition := range agents {
			imports = append(imports, fmt.Sprintf(`agent%d "%s"`, index, path.Join(websiteImport, GeneratedDir, "agents", definition.Key)))
		}
	}
	if hasMiddleware {
		imports = append(imports, `middleware "`+websiteImport+`"`)
	}
	for _, page := range pages {
		if page.HasPage {
			imports = append(imports, fmt.Sprintf(`%s "%s"`, page.Alias, page.ImportPath))
		}
		if page.Contract != "" {
			imports = append(imports, fmt.Sprintf(`%scontract "%s"`, page.Alias, page.Contract))
		}
	}
	for _, api := range apis {
		imports = append(imports, fmt.Sprintf(`%s "%s"`, api.Alias, api.ImportPath))
	}
	for _, action := range actions {
		imports = append(imports, fmt.Sprintf(`%s "%s"`, action.Alias, action.ContractImport))
	}
	imports = uniqueStrings(imports)

	var b strings.Builder
	b.WriteString(generatedSourceMarker)
	b.WriteString("\npackage registry\n\nimport (\n")
	for _, imp := range imports {
		b.WriteString("\t")
		b.WriteString(imp)
		b.WriteString("\n")
	}
	b.WriteString(")\n\n")
	b.WriteString(`type Options struct {
	BuildID       string
	PublicOrigin  string
	Middleware    gb.Middleware
	ProxyPolicy   *gb.ProxyPolicy
	FetchOrigin   gb.Fetcher
	BrowserAssets *browserassets.Manifest
	PlanStore     gbruntime.PlanStore
	Static        gbruntime.StaticEntries
	Plans         map[string]*renderplan.Plan
	Loads         map[string]gbruntime.PageLoader
	ClientScript  string
	Styles        []string
`)
	if len(agents) > 0 {
		b.WriteString("\tAgentDispatcher          httpruntime.Dispatcher\n")
		b.WriteString("\tResolveAgentActor        httpruntime.ActorResolver\n")
		b.WriteString("\tAllowLoopbackAgentActor bool\n")
	}
	b.WriteString(`}

func New(opts Options) (*gbruntime.Server, func() error, error) {
	pages := []gbruntime.PageRoute{
`)
	for _, page := range pages {
		mode := "router.ModeStatic"
		if page.Route.Mode == "dynamic" {
			mode = "router.ModeDynamic"
		}
		indexable := "true"
		if page.Indexable != nil && !*page.Indexable {
			indexable = "false"
		}
		b.WriteString("\t\t{\n")
		b.WriteString(fmt.Sprintf("\t\t\tRoute: router.Route{ID: %q, Pattern: %q, Mode: %s},\n", page.Route.ID, page.Route.Pattern, mode))
		b.WriteString(fmt.Sprintf("\t\t\tIndexable: %s,\n", indexable))
		if page.Contract != "" {
			b.WriteString(fmt.Sprintf("\t\t\tRevalidate: %scontract.Revalidate,\n", page.Alias))
			b.WriteString(fmt.Sprintf("\t\t\tTags: %scontract.Tags,\n", page.Alias))
		}
		if page.HasPage {
			b.WriteString("\t\t\tLoad: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {\n")
			b.WriteString("\t\t\t\t_ = opts.PublicOrigin\n")
			if page.PageResult {
				b.WriteString("\t\t\t\tresult, err := ")
				writePageCall(&b, page)
				b.WriteString("\t\t\t\tif err != nil {\n\t\t\t\t\treturn gbruntime.LoadedPage{}, err\n\t\t\t\t}\n")
				b.WriteString("\t\t\t\treturn gbruntime.FromPageResult(result), nil\n")
			} else {
				b.WriteString("\t\t\t\treturn ")
				writePageCall(&b, page)
			}
			b.WriteString("\t\t\t},\n")
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}\n")
	b.WriteString(`	for i := range pages {
		if opts.Plans != nil {
			pages[i].Plan = opts.Plans[pages[i].Route.ID]
		}
		if opts.Loads != nil {
			if load, ok := opts.Loads[pages[i].Route.ID]; ok {
				pages[i].Load = load
			}
		}
		if opts.ClientScript != "" {
			pages[i].ClientScript = opts.ClientScript
			pages[i].Styles = opts.Styles
		}
	}
`)
	b.WriteString("\tactions := []gbruntime.Action{\n")
	for _, action := range actions {
		b.WriteString(fmt.Sprintf("\t\t%s.Register(%s.%s),\n", action.Alias, action.RouteAlias, action.FuncName))
	}
	b.WriteString("\t}\n")
	b.WriteString("\tapis := []gbruntime.APIRoute{\n")
	for _, api := range apis {
		b.WriteString("\t\t{\n")
		b.WriteString(fmt.Sprintf("\t\t\tRoute: router.Route{ID: %q, Pattern: %q, Mode: router.ModeAPI},\n", api.Key, api.Pattern))
		b.WriteString("\t\t\tMethods: map[string]gb.Handler{\n")
		for _, method := range api.Methods {
			b.WriteString(fmt.Sprintf("\t\t\t\thttp.Method%s: %s.%s,\n", titleHTTP(method), api.Alias, method))
		}
		b.WriteString("\t\t\t},\n")
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}\n")
	b.WriteString(`	cfg := gbruntime.Config{
		BuildID:       opts.BuildID,
		PublicOrigin:  opts.PublicOrigin,
		Middleware:    opts.Middleware,
		ProxyPolicy:   opts.ProxyPolicy,
		FetchOrigin:   opts.FetchOrigin,
		BrowserAssets: opts.BrowserAssets,
		PlanStore:     opts.PlanStore,
		Static:        opts.Static,
		Pages:         pages,
		Actions:       actions,
		APIs:          apis,
	}
`)
	b.WriteString(`	if opts.FetchOrigin == nil {
		fetchOrigin, err := gbruntime.FetchOriginFromEnv()
		if err != nil {
			return nil, nil, err
		}
		cfg.FetchOrigin = fetchOrigin
	}
`)
	if hasMiddleware {
		b.WriteString(`	if cfg.Middleware == nil {
		cfg.Middleware = middleware.Middleware
	}
`)
	}
	b.WriteString(`	var closeFn func() error
	if opts.Static != nil {
		cacheConfig, cacheClose, err := openfromenv.OpenFromEnv()
		if err != nil {
			return nil, nil, err
		}
		cfg.Cache = cacheConfig
		closeFn = cacheClose
	}
	server, err := gbruntime.New(cfg)
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, nil, err
	}
	return server, closeFn, nil
}

func Handler(opts Options) (http.Handler, func() error, error) {
	server, closeFn, err := New(opts)
	if err != nil {
		return nil, nil, err
	}
`)
	if len(agents) > 0 {
		b.WriteString("\tagentRegistry := httpruntime.NewRegistry()\n")
		for index := range agents {
			b.WriteString(fmt.Sprintf("\tif err := agent%d.GobeyondRegister(agentRegistry); err != nil {\n", index))
			b.WriteString("\t\tif closeFn != nil { _ = closeFn() }\n\t\treturn nil, nil, err\n\t}\n")
		}
		b.WriteString(`	agentRuntime, err := httpruntime.New(httpruntime.Options{
		Registry:           agentRegistry,
		Dispatcher:         opts.AgentDispatcher,
		ResolveActor:       opts.ResolveAgentActor,
		AllowLoopbackActor: opts.AllowLoopbackAgentActor,
	})
	if err != nil {
		if closeFn != nil { _ = closeFn() }
		return nil, nil, err
	}
	mux := http.NewServeMux()
	if err := agentRuntime.Mount(mux, "/api/agents"); err != nil {
		if closeFn != nil { _ = closeFn() }
		return nil, nil, err
	}
	mux.Handle("/", server)
	return mux, closeFn, nil
`)
	} else {
		b.WriteString("\treturn server, closeFn, nil\n")
	}
	b.WriteString("}\n\nvar _ = routes.BuildID\n")
	return []byte(b.String()), nil
}

func writePageCall(b *strings.Builder, page pageWire) {
	if !page.HasParams {
		b.WriteString(fmt.Sprintf("%s.Page(ctx)\n", page.Alias))
		return
	}
	if len(page.Params) == 0 {
		b.WriteString(fmt.Sprintf("%s.Page(ctx, %s.Params{})\n", page.Alias, page.Alias))
		return
	}
	b.WriteString(fmt.Sprintf("%s.Page(ctx, %s.Params{\n", page.Alias, page.Alias))
	for _, field := range page.Params {
		key := page.ParamKeys[field]
		b.WriteString(fmt.Sprintf("\t\t\t\t\t%s: ctx.Params[%q],\n", field, key))
	}
	b.WriteString("\t\t\t\t})\n")
}

func titleHTTP(method string) string {
	if method == "" {
		return method
	}
	return strings.ToUpper(method[:1]) + strings.ToLower(method[1:])
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func renderSiteMain(websiteImport string, hasAgents, hasDurableAgents bool) ([]byte, error) {
	registryImport := path.Join(websiteImport, GeneratedDir, "registry")
	routesImport := path.Join(websiteImport, GeneratedDir, "routes")
	agentOptions := ""
	agentStandardImports := ""
	agentPackageImport := ""
	agentSetup := ""
	if hasAgents {
		agentOptions = "\t\tAllowLoopbackAgentActor: os.Getenv(\"GOBEYOND_AGENT_DEV_LOOPBACK\") == \"1\",\n"
	}
	if hasDurableAgents {
		agentStandardImports = "\t\"context\"\n"
		agentPackageImport = "\ttemporalruntime \"github.com/Origens-Dev/gobeyond/agents/temporalruntime\"\n"
		agentSetup = `	dispatcher, err := temporalruntime.NewLazyFromEnv(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer dispatcher.Close()
`
		agentOptions += "\t\tAgentDispatcher:          dispatcher,\n"
	}
	source := fmt.Sprintf(`%s
package main

import (
%s	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"

	gblisten "github.com/Origens-Dev/gobeyond/adapters/listen"
%s	"github.com/Origens-Dev/gobeyond/browserassets"
	registry %q
	routes %q
	gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

func main() {
%s
	planPack := os.Getenv("GOBEYOND_PLAN_PACK")
	if planPack == "" {
		planPack = filepath.Join("dist", "server", "render-plans.gbp")
	}
	staticPack := os.Getenv("GOBEYOND_STATIC_PACK")
	if staticPack == "" {
		staticPack = filepath.Join("dist", "server", "runtime-data", "static-build.gbs")
	}
	planStore, err := gbruntime.OpenPlanStore(planPack)
	if err != nil {
		log.Fatal(err)
	}
	defer planStore.Close()
	staticStore, err := gbruntime.OpenStaticStore(staticPack, filepath.Join(filepath.Dir(staticPack), "contracts.json"))
	if err != nil {
		log.Fatal(err)
	}
	defer staticStore.Close()
	origin := os.Getenv("GOBEYOND_PUBLIC_ORIGIN")
	if origin == "" {
		origin = "http://localhost:8080"
	}
	buildID := os.Getenv("GOBEYOND_BUILD_ID")
	if buildID == "" {
		buildID = planStore.BuildID()
	}
	if buildID == "" {
		buildID = routes.BuildID
	}
	proxyPolicyPath := os.Getenv("GOBEYOND_PROXY_POLICY")
	if proxyPolicyPath == "" {
		proxyPolicyPath = filepath.Join("dist", "deploy", "proxy-policy.json")
	}
	proxyPolicy, err := gbruntime.LoadProxyPolicy(proxyPolicyPath, buildID)
	if err != nil {
		log.Fatal(err)
	}
	assets, err := loadBrowserAssets(filepath.Join(filepath.Dir(planPack), "runtime-manifest.json"), buildID)
	if err != nil {
		log.Fatal(err)
	}
	handler, closeFn, err := registry.Handler(registry.Options{
		BuildID:       buildID,
		PublicOrigin:  origin,
		ProxyPolicy:   proxyPolicy,
		BrowserAssets: assets,
		PlanStore:     planStore,
		Static:        staticStore,
%s
	})
	if err != nil {
		log.Fatal(err)
	}
	if closeFn != nil {
		defer closeFn()
	}
	staticDirectory := os.Getenv(gbruntime.EnvStaticDir)
	if staticDirectory == "" {
		staticDirectory = "dist/static"
	}
	handler = gbruntime.StaticFiles(staticDirectory, handler)
	handler = gbruntime.ProxyPolicyHandler(proxyPolicy, handler)
	if err := gblisten.Serve(handler); err != nil {
		log.Fatal(err)
	}
}

func loadBrowserAssets(path, buildID string) (*browserassets.Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		BuildID string          `+"`json:\"buildId\"`"+`
		Assets  json.RawMessage `+"`json:\"assets\"`"+`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.BuildID != "" && manifest.BuildID != buildID {
		return nil, errors.New("runtime asset manifest build ID does not match server build ID")
	}
	assets, err := browserassets.Parse(manifest.Assets)
	if err != nil {
		return nil, err
	}
	return &assets, nil
}
`, generatedSourceMarker+"\n", agentStandardImports, agentPackageImport, registryImport, routesImport, agentSetup, agentOptions)
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return nil, fmt.Errorf("format generated site main: %w\n%s", err, source)
	}
	return formatted, nil
}
