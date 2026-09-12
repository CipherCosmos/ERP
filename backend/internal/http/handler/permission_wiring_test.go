package handler_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEveryRouteCarriesAPermission reads this package's own source and asserts
// that every route registration names a permission.
//
// It is a source check rather than a behavioural one on purpose. Behaviourally,
// "can role X reach route Y" can only be tested against the permission table,
// which is circular — the assertion passes with every guard removed, because the
// table still says what it said. And some permissions really are held by every
// role (reading the catalogue, for one), so "somebody must be refused" is not a
// rule either.
//
// The rule worth enforcing is the one a reviewer would apply: a route
// registration that does not name a permission is a hole. That is a property of
// the text, so the text is what gets checked.
//
// This exists because 106 of 112 routes had no permission check at all. The
// layered rewrite moved every route out of the old router, where each one was
// registered with its permission inline, and did not carry the guards across.
// The rbac package, the constants and the role table all survived; only the
// wiring was lost, so nothing looked missing. A cashier could create a store,
// write off inventory, void an invoice and read the profit and loss.
func TestEveryRouteCarriesAPermission(t *testing.T) {
	// The only routes that may be registered bare, with the reason recorded at
	// the registration site too.
	exempt := map[string]bool{
		"POST /auth/logout":          true, // ends the caller's own session
		"POST /auth/change-password": true, // changes the caller's own password
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	var unguarded []string
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			route := sel.Sel.Name + " " + strings.Trim(lit.Value, `"`)
			seen++
			if exempt[route] {
				return true
			}
			for _, arg := range call.Args[1:] {
				inner, ok := arg.(*ast.CallExpr)
				if !ok {
					continue
				}
				fn, ok := inner.Fun.(*ast.SelectorExpr)
				if ok && fn.Sel.Name == "RequirePermission" {
					return true
				}
			}
			unguarded = append(unguarded, route+"  ("+name+":"+
				fset.Position(call.Pos()).String()[len(name)+1:]+")")
			return true
		})
	}

	// A guard against the check silently passing because it found nothing.
	if seen < 100 {
		t.Fatalf("only %d route registrations found; this check is not reading the router", seen)
	}

	if len(unguarded) > 0 {
		sort.Strings(unguarded)
		t.Errorf("%d route(s) registered with no permission:\n  %s\n\n"+
			"Add mw.RequirePermission(rbac.PermX) to the registration, or list it in this "+
			"test's exempt map with a reason.", len(unguarded), strings.Join(unguarded, "\n  "))
	}
	t.Logf("%d route registrations checked, %d exempt", seen, len(exempt))
}
