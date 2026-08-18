package project

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
)

// DiscoverMiddlewareSource returns the one supported authored middleware
// entry. New GoBeyond projects author a root middleware.go with package
// middleware and exactly one function:
//
//	func Middleware(next gb.Handler) gb.Handler
//
// The previous TypeScript/JavaScript edge-worker contract is intentionally
// rejected so a build cannot silently ship a second request pipeline.
func DiscoverMiddlewareSource(root string) (string, error) {
	goSource, err := DiscoverGoMiddleware(root)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"middleware.ts", "middleware.js", "edge-middleware.ts", "edge-middleware.js"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr == nil {
			return "", fmt.Errorf("%s is no longer supported; move request middleware to root middleware.go", name)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "edge-middleware")); statErr == nil {
		return "", errors.New("edge-middleware/ is no longer supported; move request middleware to root middleware.go")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	return goSource, nil
}

// DiscoverGoMiddleware validates the compiler-visible root Go middleware
// contract and returns its source path. A missing middleware.go is valid.
func DiscoverGoMiddleware(root string) (string, error) {
	filename := filepath.Join(root, "middleware.go")
	content, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	file, err := parser.ParseFile(token.NewFileSet(), filename, content, 0)
	if err != nil {
		return "", fmt.Errorf("parse middleware.go: %w", err)
	}
	if file.Name == nil || file.Name.Name != "middleware" {
		return "", errors.New("middleware.go must declare package middleware")
	}
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		path, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil {
			return "", fmt.Errorf("parse middleware import: %w", unquoteErr)
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				continue
			}
			name = spec.Name.Name
		}
		imports[name] = path
	}
	var middleware *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name == nil || function.Name.Name != "Middleware" {
			continue
		}
		if middleware != nil {
			return "", errors.New("middleware.go must declare exactly one Middleware function")
		}
		middleware = function
	}
	if middleware == nil {
		return "", errors.New("middleware.go must declare Middleware(next gb.Handler) gb.Handler")
	}
	if middleware.Type.Params == nil || len(middleware.Type.Params.List) != 1 || middleware.Type.Results == nil || len(middleware.Type.Results.List) != 1 {
		return "", errors.New("Middleware must have exactly one parameter and one result")
	}
	if !isHandlerType(middleware.Type.Params.List[0].Type, imports) || !isHandlerType(middleware.Type.Results.List[0].Type, imports) {
		return "", errors.New("Middleware must have signature func Middleware(next gb.Handler) gb.Handler")
	}
	return filename, nil
}

func isHandlerType(expression ast.Expr, imports map[string]string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || selector.Sel.Name != "Handler" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	return imports[packageName.Name] == gobeyondModulePath
}
