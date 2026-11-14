package repoaudit

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"

	"primeradiant.com/evener/internal/devtool/copyonly"
)

// testSleepBaseline is the most time.Sleep calls Go tests may hold. When the
// check's NOTE reports a lower count, set this to that count in the same
// change. See "Waiting in Go tests" in docs/developing-evener/testing.md.
const testSleepBaseline = 284

// testSleepsStayWithinBaseline is the ratchet behind the testing doc's "never
// a fixed sleep": the time.Sleep calls in tracked Go tests may not outnumber
// testSleepBaseline. A lower count passes with a note to lower the baseline.
//
// A file that imports testing/synctest is left out whole: a sleep inside a
// bubble advances the fake clock and costs nothing. A sleep outside the bubble
// in such a file goes uncounted too, which is the price of a scan that reads
// imports rather than following calls. Files under testdata are fixtures, not
// tests the gate runs.
func testSleepsStayWithinBaseline(a *audit) {
	files, err := copyonly.TrackedFiles(".")
	if err != nil {
		a.Fatalf("%v", err)
	}
	count := 0
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") || strings.HasPrefix(file, "testdata/") || strings.Contains(file, "/testdata/") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			a.Fatalf("read %s: %v", file, err)
		}
		n, err := countSleeps(file, src)
		if err != nil {
			a.Fatalf("%v", err)
		}
		count += n
	}
	judgeSleeps(a, count, testSleepBaseline)
}

// judgeSleeps holds count to baseline: above it is a finding, below it a note.
func judgeSleeps(a *audit, count, baseline int) {
	switch {
	case count > baseline:
		a.Errorf("Go tests call time.Sleep %d time(s), over the baseline of %d. "+
			"Await the completion, run the test under testing/synctest, or poll with internal/testwait "+
			"(docs/developing-evener/testing.md, \"Waiting in Go tests\")",
			count, baseline)
	case count < baseline:
		a.Notef("Go tests call time.Sleep %d time(s), under the baseline of %d; "+
			"set testSleepBaseline in internal/devtool/repoaudit/testsleeps.go to %d",
			count, baseline, count)
	}
}

// countSleeps counts the calls to time.Sleep in one Go file, or none when the
// file imports testing/synctest.
func countSleeps(name string, src []byte) (int, error) {
	// Most test files never sleep; skip parsing them.
	if !bytes.Contains(src, []byte("time.Sleep(")) {
		return 0, nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	for _, imp := range file.Imports {
		if importPath, _ := strconv.Unquote(imp.Path.Value); importPath == "testing/synctest" {
			return 0, nil
		}
	}
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sleep" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
				count++
			}
		}
		return true
	})
	return count, nil
}
