package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestHarnessContextSitesDeriveFromPredicate is the site half of issue #4538's
// completeness guard: each of the four production lists the issue named must
// call TurnKind.HarnessContext, so a new context kind cannot silently miss one
// of these sites. The schema half lives in
// agent/schema/turn_identity_test.go and fails when a new TurnKind is declared
// without a harness-context decision.
func TestHarnessContextSitesDeriveFromPredicate(t *testing.T) {
	t.Parallel()
	sites := []struct {
		file       string
		function   string
		allowedRef string
	}{
		{"session_init.go", "conversationSignals", ""},
		{"session_outline.go", "outlineRoleLabel", ""},
		{"session_model_call.go", "expandHistory", "TurnNotesContext"},
		{"responses_continuation_eligibility.go", "responsesContinuationDeltaIneligibleReason", ""},
	}
	for _, site := range sites {
		file, err := parser.ParseFile(token.NewFileSet(), site.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", site.file, err)
		}
		fn := findFuncDecl(file, site.function)
		if fn == nil {
			t.Errorf("%s: %s not found; update this guard with the function's new name or location", site.file, site.function)
			continue
		}
		if got := harnessContextCallCount(fn.Body); got != 1 {
			t.Errorf("%s: %s calls HarnessContext %d times, want 1; the site must derive its harness-context kinds from the schema predicate", site.file, site.function, got)
		}
		for _, ref := range harnessContextKindRefs(fn.Body) {
			if ref != site.allowedRef {
				t.Errorf("%s: %s still names schema.%s directly; harness-context kinds belong in TurnKind.HarnessContext", site.file, site.function, ref)
			}
		}
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func harnessContextCallCount(body *ast.BlockStmt) (count int) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "HarnessContext" {
			count++
		}
		return true
	})
	return count
}

// harnessContextKindRefs lists the schema.TurnKind constants from the
// harness-context set that a function body names directly. A site that still
// carries a literal list names them; a site that derives from the predicate
// does not (expandHistory keeps one deliberate TurnNotesContext self-echo
// check). Adding a harness-context kind means adding it here too.
func harnessContextKindRefs(body *ast.BlockStmt) []string {
	var refs []string
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "schema" {
			return true
		}
		switch sel.Sel.Name {
		case "TurnEnvironment", "TurnNotesContext", "TurnMemoryContext":
			refs = append(refs, sel.Sel.Name)
		}
		return true
	})
	return refs
}
