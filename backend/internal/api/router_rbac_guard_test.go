package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// adminOnlyPrefixes lists route prefixes whose responses carry
// infrastructure detail no non-admin should read:
//
//   - /api/backup/*  destination paths, remote hosts, ports, VM names,
//     schedules and error messages from every job.
//   - /api/nodes     hypervisor connection descriptors (libvirt URIs).
//   - /api/notify    SMTP recipients, webhook URLs, Telegram chat ids,
//     plus the fleet-wide alert event log.
//
// Each of these was, at some point, reachable by a plain viewer: the
// handlers themselves perform no role check, so the ONLY thing standing
// between them and an unauthorised read is the middleware placement in
// NewRouter. `GET /api/backup/jobs` in particular sat at the top level,
// outside both /api/backup/* groups, and leaked the last 50 jobs.
//
// This test parses router.go itself rather than re-declaring the route
// table, because a hand-written replica of the router is exactly the
// kind of test that keeps passing while the real router regresses.
var adminOnlyPrefixes = []string{
	"/api/backup",
	"/api/nodes",
	"/api/notify",
}

func TestRouterAdminOnlyRoutesAreGated(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing router.go: %v", err)
	}

	type route struct {
		method, path string
		gated        bool
		line         int
	}
	var routes []route

	// Walk the call tree keeping track of how many enclosing scopes
	// applied an admin RequireRole/RequireAtLeast, and of the path
	// prefix accumulated by the enclosing r.Route(...) calls.
	var walk func(n ast.Node, prefix string, gated bool)
	walk = func(n ast.Node, prefix string, gated bool) {
		body, ok := n.(*ast.BlockStmt)
		if !ok {
			return
		}
		// First pass: does this block install an admin gate via r.Use?
		blockGated := gated
		for _, stmt := range body.List {
			es, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Use" {
				continue
			}
			if callMentionsAdmin(call) {
				blockGated = true
			}
		}
		// Second pass: record routes and recurse into nested closures.
		for _, stmt := range body.List {
			es, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			switch sel.Sel.Name {
			case "Get", "Post", "Put", "Delete", "Patch", "Head", "Options":
				if len(call.Args) == 0 {
					continue
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				routes = append(routes, route{
					method: strings.ToUpper(sel.Sel.Name),
					path:   joinRoute(prefix, strings.Trim(lit.Value, `"`)),
					gated:  blockGated,
					line:   fset.Position(lit.Pos()).Line,
				})
			case "Route", "Group", "With":
				sub := prefix
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						sub = joinRoute(prefix, strings.Trim(lit.Value, `"`))
					}
					if fn, ok := arg.(*ast.FuncLit); ok {
						walk(fn.Body, sub, blockGated)
					}
				}
			}
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "NewRouter" {
			return true
		}
		walk(fd.Body, "", false)
		return false
	})

	if len(routes) == 0 {
		t.Fatal("parsed no routes from router.go: the test needs updating")
	}

	checked := 0
	for _, rt := range routes {
		for _, prefix := range adminOnlyPrefixes {
			if !strings.HasPrefix(rt.path, prefix) {
				continue
			}
			checked++
			if !rt.gated {
				t.Errorf("router.go:%d: %s %s is reachable without an admin role gate",
					rt.line, rt.method, rt.path)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("matched none of %v against %d parsed routes", adminOnlyPrefixes, len(routes))
	}
}

// callMentionsAdmin reports whether an r.Use(...) call installs a role
// gate that admits only admins.
func callMentionsAdmin(call *ast.CallExpr) bool {
	found := false
	ast.Inspect(call, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			// models.RoleAdmin
			if v.Sel.Name == "RoleAdmin" {
				found = true
			}
		case *ast.Ident:
			// modelsRoleAdmin() helper
			if v.Name == "modelsRoleAdmin" {
				found = true
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING && strings.Trim(v.Value, `"`) == "admin" {
				found = true
			}
		}
		return true
	})
	return found
}

func joinRoute(prefix, p string) string {
	switch {
	case p == "" || p == "/":
		return prefix
	case prefix == "":
		return p
	}
	return strings.TrimSuffix(prefix, "/") + p
}
