package agent

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoBareWallClockDeadlineInAgentTests scans this package's own test
// sources (agent/*_test.go, non-recursive -- the exact scope kata ww3g
// measured: 237 hardcoded 5s deadlines across 29 t.Parallel files, load-flaky
// by construction) for wall-clock bounds fed straight into a timeout- or
// deadline-establishing call: context.WithTimeout, context.WithDeadline,
// time.After, time.NewTimer, time.NewTicker, time.AfterFunc, and this
// package's own waitForCondition/awaitWithin helpers.
//
// A BARE bound -- an inline numeric literal times a time.Duration unit
// (5*time.Second), or a lone time.<Unit> constant, written straight into one
// of those calls -- fails the audit unless:
//
//   - the file is on deadlineAuditAllowlist (a currently-offending file,
//     grandfathered in by kata ww3g's wave 1; the list only shrinks, a
//     conversion removes its file, it never gains a new one), or
//   - the literal carries a "// TRIPWIRE:" comment on its own line or the
//     line directly above it, explaining why the bound is raised generously
//     above the expected completion time rather than tuned tight to it (see
//     docs/developing-evener/testing.md's Flakes and Timeouts ranking: a ceiling is only
//     legitimate as a tripwire, never the mechanism).
//
// This is the ONE sanctioned way to carry a wall-clock bound in an agent
// test, and it is deliberately mechanical: a converter facing a finding
// either (a) deletes the bound and awaits the real completion signal instead
// -- a channel receive, a WaitGroup, a condition -- optionally keeping a
// generous "// TRIPWIRE:"-marked ceiling purely as a hang guard the way
// awaitWithin already does, or (b) where no completion signal exists yet in
// production code, leaves the file on the allowlist and files that gap as the
// real defect (kata ww3g bucket 3) instead of hacking around it here.
//
// Naming the bound -- a package- or file-level constant/variable with its own
// doc comment -- also clears the audit: there is no bare literal left at the
// call site once the duration is a named identifier. That is not a second
// sanctioned mechanism, it is simply outside what "bare" means; the audit
// never inspects the identifier's own declaration.
func TestNoBareWallClockDeadlineInAgentTests(t *testing.T) {
	findings, err := deadlineAuditFindings(".", deadlineAuditAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("bare wall-clock deadline(s) in agent test sources:\n%s\n\n"+
			"Fix: await the real completion signal instead of a bound, or add a "+
			"\"// TRIPWIRE: <why this sits far above the expected time>\" comment "+
			"on or directly above the literal. Only add a file to "+
			"deadlineAuditAllowlist if it is already grandfathered and this is not "+
			"a new offense.", strings.Join(findings, "\n"))
	}
}

func TestDeadlineAuditRejectsBareContextWithTimeout(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx
}
`)
	findings, err := deadlineAuditFindings(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDeadlineFindingContains(t, findings, "sample_test.go:10", "context.WithTimeout")
}

func TestDeadlineAuditAllowsSameLineTripwireMarker(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: real turn <500ms
	defer cancel()
	_ = ctx
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func TestDeadlineAuditAllowsPrecedingLineTripwireMarker(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	// TRIPWIRE: real turn typically <500ms; this only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func TestDeadlineAuditAllowsMultiLineTripwireMarkerBlock(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	// TRIPWIRE: every step here is scripted and answered in-process with no
	// I/O; this normally completes in milliseconds. 30s only fires on a
	// genuine hang, not scheduler contention under a loaded suite.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func TestDeadlineAuditRejectsMarkerTwoLinesAbove(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	// TRIPWIRE: this comment is too far from the bound to count.

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx
}
`)
	findings, err := deadlineAuditFindings(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDeadlineFindingContains(t, findings, "sample_test.go", "context.WithTimeout")
}

func TestDeadlineAuditCatchesWaitForConditionAndAwaitWithin(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	waitForCondition(t, 5*time.Second, "thing happened", func() bool { return true })
	awaitWithin(t, 5*time.Second, "thing finished", func() {})
}
`)
	findings, err := deadlineAuditFindings(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDeadlineFindingContains(t, findings, "sample_test.go", "waitForCondition")
	assertDeadlineFindingContains(t, findings, "sample_test.go", "awaitWithin")
}

func TestDeadlineAuditIgnoresNamedDurationIdentifier(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

// someTimeout documents why this bound is safe; it is not a bare literal.
const someTimeout = 5 * time.Second

func TestSomething(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), someTimeout)
	defer cancel()
	_ = ctx
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func TestDeadlineAuditIgnoresUnrelatedTimeDotSecondUsage(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"testing"
	"time"
)

type pollConfig struct {
	Interval time.Duration
}

func TestSomething(t *testing.T) {
	cfg := pollConfig{Interval: 5 * time.Second}
	_ = cfg
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func TestDeadlineAuditIgnoresSleep(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	time.Sleep(5 * time.Second)
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func TestDeadlineAuditSkipsAllowlistedFile(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample_test.go", `package agent

import (
	"context"
	"testing"
	"time"
)

func TestSomething(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx
}
`)
	assertDeadlineFindingsEmpty(t, dir, map[string]bool{"sample_test.go": true})
}

func TestDeadlineAuditIgnoresNonTestGoFiles(t *testing.T) {
	dir := writeDeadlineAuditFixture(t, "sample.go", `package agent

import (
	"context"
	"time"
)

func something() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx
}
`)
	assertDeadlineFindingsEmpty(t, dir, nil)
}

func writeDeadlineAuditFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertDeadlineFindingsEmpty(t *testing.T, dir string, allowlist map[string]bool) {
	t.Helper()
	findings, err := deadlineAuditFindings(dir, allowlist)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got: %v", findings)
	}
}

func assertDeadlineFindingContains(t *testing.T, findings []string, needles ...string) {
	t.Helper()
	for _, f := range findings {
		matchesAll := true
		for _, needle := range needles {
			if !strings.Contains(f, needle) {
				matchesAll = false
				break
			}
		}
		if matchesAll {
			return
		}
	}
	t.Fatalf("no finding matched all of %v, got: %v", needles, findings)
}

// deadlineAuditBoundCalls names every timeout/deadline-establishing call this
// audit checks. Selector calls are "pkg.Func"; local helper calls are their
// bare identifier.
var deadlineAuditBoundCalls = map[string]bool{
	"context.WithTimeout":  true,
	"context.WithDeadline": true,
	"time.After":           true,
	"time.NewTimer":        true,
	"time.NewTicker":       true,
	"time.AfterFunc":       true,
	"waitForCondition":     true,
	"awaitWithin":          true,
}

var deadlineAuditTimeUnits = map[string]bool{
	"Nanosecond": true, "Microsecond": true, "Millisecond": true,
	"Second": true, "Minute": true, "Hour": true,
}

// deadlineAuditAllowlist is the ratchet: every agent/*_test.go file that
// carried a bare wall-clock bound when kata ww3g wrote this audit (measured
// 2026-08-17, go test -run TestNoBareWallClockDeadlineInAgentTests before
// this list existed: 88 files, 448 sites). It only shrinks -- a conversion
// wave removes a file's entry once every bound in it is either replaced by
// its real completion signal or carries a "// TRIPWIRE:" marker; it must
// never gain a new entry, because that is exactly the regression this audit
// exists to catch. The list is now empty: every file's bare bounds have been
// converted (TRIPWIRE comment or named constant), and the audit catches any
// new bare wall-clock bound in any agent test file.
var deadlineAuditAllowlist = map[string]bool{}

// deadlineAuditFindings scans the non-recursive *_test.go files directly in
// dir and returns one finding per bare wall-clock bound, skipping any file
// named in allowlist. allowlist may be nil.
func deadlineAuditFindings(dir string, allowlist map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var findings []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		if allowlist[name] {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, path, raw, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		findings = append(findings, deadlineAuditFileFindings(name, file, fset)...)
	}
	sort.Strings(findings)
	return findings, nil
}

func deadlineAuditFileFindings(name string, file *ast.File, fset *token.FileSet) []string {
	// A comment group is go/parser's own notion of one contiguous comment
	// block (no blank line inside it). If the marker appears anywhere in the
	// block, the whole block -- not just the exact line carrying the word --
	// counts as marked, so a multi-line "// TRIPWIRE: ..." rationale directly
	// above a bound is recognized the same as a single terse line would be.
	markedLines := map[int]bool{}
	for _, cg := range file.Comments {
		marked := false
		for _, c := range cg.List {
			if strings.Contains(c.Text, "TRIPWIRE:") {
				marked = true
				break
			}
		}
		if !marked {
			continue
		}
		start := fset.Position(cg.Pos()).Line
		end := fset.Position(cg.End()).Line
		for line := start; line <= end; line++ {
			markedLines[line] = true
		}
	}
	var findings []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fname := deadlineAuditCallName(call.Fun)
		if !deadlineAuditBoundCalls[fname] {
			return true
		}
		for _, arg := range call.Args {
			pos := deadlineAuditBareDurationPos(arg)
			if pos == token.NoPos {
				continue
			}
			line := fset.Position(pos).Line
			if markedLines[line] || markedLines[line-1] {
				continue
			}
			findings = append(findings, fmt.Sprintf("%s:%d: bare wall-clock bound passed to %s(...)", name, line, fname))
		}
		return true
	})
	return findings
}

func deadlineAuditCallName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if pkg, ok := f.X.(*ast.Ident); ok {
			return pkg.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// deadlineAuditBareDurationPos reports the position of a bare wall-clock
// duration literal within expr -- N * time.<Unit> in either operand order, or
// a lone time.<Unit> constant -- or token.NoPos if expr names no such literal.
// An identifier (a named constant or variable, however it was itself defined)
// is never bare: the audit only ever looks at the call site.
func deadlineAuditBareDurationPos(expr ast.Expr) token.Pos {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return deadlineAuditBareDurationPos(e.X)
	case *ast.BinaryExpr:
		if e.Op == token.MUL && (deadlineAuditIsTimeUnit(e.X) || deadlineAuditIsTimeUnit(e.Y)) {
			return e.Pos()
		}
	case *ast.SelectorExpr:
		if deadlineAuditIsTimeUnit(e) {
			return e.Pos()
		}
	}
	return token.NoPos
}

func deadlineAuditIsTimeUnit(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time" && deadlineAuditTimeUnits[sel.Sel.Name]
}
