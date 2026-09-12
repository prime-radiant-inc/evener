package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conditionFiresField is the counter the condition-fire circuit breaker reads.
const conditionFiresField = "conditionFires"

// noteConditionFireFunc is the one function allowed to write it.
const noteConditionFireFunc = "noteConditionFireLocked"

// TestConditionFireBudget_EveryFireGoesThroughOneHelper is a source scan, not a
// behavior test, because the property it protects has no behavior to observe: a
// match site that counts a fire without consulting the breaker looks exactly
// like a correct one until a watch reaches 50 fires on that rail, and the attach
// scan — the site that got this wrong — can only walk a fresh config's counter
// from 0 to 1, so no reachable state distinguishes the two. What CAN be checked
// is that counting and latching stay one operation: every write to
// conditionFires in this package's non-test sources lives inside
// noteConditionFireLocked, which returns the crossing its caller must act on.
//
// A stray `cfg.conditionFires++` elsewhere reintroduces exactly the reviewed
// defect — a counted fire that never reports its crossing — and the unit test
// for the helper cannot see it, because the helper itself still behaves.
func TestConditionFireBudget_EveryFireGoesThroughOneHelper(t *testing.T) {
	t.Parallel()
	writes := conditionFireWrites(t)
	if len(writes) == 0 {
		t.Fatalf("no write to %s anywhere in agent/*.go: the counter or its helper was renamed and this guard now checks nothing", conditionFiresField)
	}
	var strays []string
	inHelper := 0
	for _, w := range writes {
		if w.fn == noteConditionFireFunc {
			inHelper++
			continue
		}
		strays = append(strays, w.where+" (in "+w.fn+")")
	}
	if inHelper == 0 {
		t.Fatalf("%s no longer writes %s: the single seam every condition match goes through is gone", noteConditionFireFunc, conditionFiresField)
	}
	if len(strays) > 0 {
		t.Fatalf("%s written outside %s:\n%s\n\nFix: call %s, which counts the fire AND reports whether it crossed the budget, then schedule autoClearWatchOverBudget after releasing jm.mu.",
			conditionFiresField, noteConditionFireFunc, strings.Join(strays, "\n"), noteConditionFireFunc)
	}
}

// conditionFireWrite is one write to the counter: the enclosing function's name
// (empty at package level) and its file:line.
type conditionFireWrite struct {
	fn    string
	where string
}

// conditionFireWrites parses this package's non-test sources (agent/*.go,
// non-recursive — the whole of package agent) and returns every write to
// conditionFires: an increment, an assignment, or a composite-literal field.
// Reads are not writes and are not collected; the breaker's own comparison in
// tripConditionFireBudgetLocked is one.
func conditionFireWrites(t *testing.T) []conditionFireWrite {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var writes []conditionFireWrite
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			owner := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				for _, pos := range conditionFireWritePositions(n) {
					writes = append(writes, conditionFireWrite{fn: owner, where: fset.Position(pos).String()})
				}
				return true
			})
		}
	}
	return writes
}

func conditionFireWritePositions(n ast.Node) []token.Pos {
	var out []token.Pos
	switch node := n.(type) {
	case *ast.IncDecStmt:
		if isConditionFiresField(node.X) {
			out = append(out, node.Pos())
		}
	case *ast.AssignStmt:
		for _, lhs := range node.Lhs {
			if isConditionFiresField(lhs) {
				out = append(out, lhs.Pos())
			}
		}
	case *ast.KeyValueExpr:
		if key, ok := node.Key.(*ast.Ident); ok && key.Name == conditionFiresField {
			out = append(out, node.Pos())
		}
	}
	return out
}

// isConditionFiresField reports whether expr is an `x.conditionFires` selector.
func isConditionFiresField(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == conditionFiresField
}
