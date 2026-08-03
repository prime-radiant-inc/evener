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

// The rule these tests protect, and the reason it is enforceable at all.
//
// With an authoritative consumer attached, sendEvent BLOCKS rather than
// dropping (session_events.go). A goroutine that emits while holding a lock
// that consumer takes therefore wedges the pair: the consumer cannot drain, the
// emitter cannot proceed, and because the emitter blocks holding
// eventsMu.RLock the session can no longer even be closed. Nothing in the
// compiler, in `go vet`, or in any linter here checks that.
//
// What DOES check it is the emit path itself — incidentally, as a side effect
// of two functions doing their real jobs. The daemon's consumer reaches exactly
// two mutexes that live in package agent and that agent code can plausibly hold
// across an emit: Session.mu and jobManager.mu (server/bridge.go's LOCK RULE
// note; cmd/serf/serve.go's liveThreadEnvelopeSource is the whole of the
// consumer's reach into this package). The emit path re-acquires BOTH:
//
//	emit                 -> activeCausalProvenance -> Session.mu
//	emitWithProvenance   -> jobManager.onSessionEvent -> jobManager.mu
//
// Go mutexes are not reentrant, so a violation of the rule at either lock
// self-deadlocks on its FIRST execution, with no concurrency and no full buffer
// required — you find it by running the code, not by reviewing it. Both halves
// were confirmed by experiment (kata cb1k): an emit hoisted into either
// critical section parks the emitting goroutine at session_provenance.go's
// s.mu.Lock or at job_watch.go's jm.mu.Lock respectively.
//
// That guard is incidental to both functions, which is exactly why it needs a
// test. Two plausible, well-intentioned refactors delete it in silence and
// convert every violation of the rule from a loud local hang into a silent
// daemon wedge, and neither would fail any other test in this repo:
//
//   - caching the active provenance, so emit no longer takes Session.mu;
//   - evaluating watches off the emitting goroutine (`go jm.onSessionEvent(ev)`),
//     so emitWithProvenance no longer takes jobManager.mu.
//
// This file covers one direction only: an EMIT that happens under a lock the
// consumer takes. The mirror hazard — a SAMPLE the consumer takes under a lock
// an emitter holds — has no self-deadlock to lean on and is covered by
// session_envelope_sampling.go and its test instead.
//
// Known residual, deliberately not tested here: emitDiagnosticWarning reaches
// sendEvent WITHOUT going through onSessionEvent, so it carries the Session.mu
// guard but not the jobManager.mu one. Its call sites are all outside any
// critical section today. Proving that statically needs an interprocedural
// held-lock analysis over the whole package, which is a different and much
// noisier tool than these four assertions.

// TestEmitReacquiresSessionMu pins the first guard: emit's own first act must
// take Session.mu, so emitting from inside a Session.mu critical section
// deadlocks immediately instead of wedging the daemon later.
//
// session_prompts.go's refreshSystemPromptCache is the worked example of the
// rule this enforces — it RETURNS its diagnostic rather than emitting it,
// because three of its four callers run under s.mu.
func TestEmitReacquiresSessionMu(t *testing.T) {
	files := agentSourceFiles(t)

	emit := methodDecl(t, files, "Session", "emit")
	if !callsOnTheSameGoroutine(emit, "activeCausalProvenance") {
		t.Error("Session.emit no longer calls activeCausalProvenance on the emitting goroutine: " +
			"an emit under Session.mu now wedges the daemon silently instead of self-deadlocking on the spot")
	}

	stamp := methodDecl(t, files, "Session", "activeCausalProvenance")
	if !acquiresBeforeAnyReturn(stamp, "mu") {
		t.Error("Session.activeCausalProvenance no longer takes s.mu unconditionally: " +
			"emit stops being self-guarding, and an emit under Session.mu becomes a silent daemon wedge")
	}
}

// TestEmitWithProvenanceReacquiresJobManagerMu pins the second guard, which is
// the one that covers every jobManager emit — jm.emit is wired to
// emitWithProvenance, and emitWithProvenance hands the event to
// jobManager.onSessionEvent, which locks jm.mu.
//
// It matters most at the four jm.emit call sites in agent/jobs.go,
// agent/job_shell.go and agent/job_delegate.go, each of which unlocks jm.mu on
// the line immediately before emitting. Hoisting any of those three lines up is
// caught today only because this guard turns it into an immediate hang.
func TestEmitWithProvenanceReacquiresJobManagerMu(t *testing.T) {
	files := agentSourceFiles(t)

	emitWith := methodDecl(t, files, "Session", "emitWithProvenance")
	if !callsOnTheSameGoroutine(emitWith, "onSessionEvent") {
		t.Error("Session.emitWithProvenance no longer calls jobManager.onSessionEvent on the emitting goroutine: " +
			"an emit under jobManager.mu now wedges the daemon silently instead of self-deadlocking on the spot")
	}

	onEvent := methodDecl(t, files, "jobManager", "onSessionEvent")
	if !acquiresBeforeAnyReturn(onEvent, "mu") {
		t.Error("jobManager.onSessionEvent no longer takes jm.mu unconditionally: " +
			"emitWithProvenance stops being self-guarding, and an emit under jobManager.mu becomes a silent daemon wedge")
	}
}

// TestJobManagerEmitIsWiredToTheGuardedPath pins the link the guard above rides
// on. jm.emit is a func field; wiring it to anything that reaches sendEvent
// without passing through onSessionEvent — sendEvent itself, or a future
// leaner emitter — removes the guard from every job emit while leaving both
// halves of TestEmitWithProvenanceReacquiresJobManagerMu green. A synchronous
// wrapper is allowed only when it still calls emitWithProvenance on the same
// goroutine.
func TestJobManagerEmitIsWiredToTheGuardedPath(t *testing.T) {
	files := agentSourceFiles(t)
	jobTreeEmitter := methodDecl(t, files, "Session", "emitWithJobTreeRevision")
	if !callsOnTheSameGoroutine(jobTreeEmitter, "emitWithProvenance") {
		t.Fatal("Session.emitWithJobTreeRevision no longer calls emitWithProvenance on the emitting goroutine: " +
			"the job tree wrapper has bypassed the jobManager.mu self-deadlock guard")
	}

	wirings := 0
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "emit" {
					continue
				}
				if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "jm" {
					continue
				}
				wirings++
				if got := exprText(assign.Rhs[i]); got != "s.emitWithProvenance" && got != "s.emitWithJobTreeRevision" {
					t.Errorf("jm.emit is wired to %s, want s.emitWithProvenance or its guarded s.emitWithJobTreeRevision wrapper: "+
						"a job emit that skips onSessionEvent skips the jobManager.mu self-deadlock guard", got)
				}
			}
			return true
		})
	}
	if wirings == 0 {
		t.Fatal("found no jm.emit assignment in agent/*.go; this test has stopped checking anything")
	}
}

// agentSourceFiles parses the package's non-test sources. The guards live in
// production code, so a mutation applied to a _test.go file must not satisfy
// these tests.
func agentSourceFiles(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, b, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, file)
	}
	return files
}

// methodDecl returns the named method on the named receiver type, failing if it
// is absent — a renamed guard must fail loudly rather than silently pass.
func methodDecl(t *testing.T, files []*ast.File, recvType, name string) *ast.FuncDecl {
	t.Helper()
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if receiverTypeName(fn.Recv.List[0].Type) == recvType {
				return fn
			}
		}
	}
	t.Fatalf("no method (*%s).%s in agent/*.go: the emit lock guard it anchors can no longer be checked", recvType, name)
	return nil
}

func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// callsOnTheSameGoroutine reports whether fn calls the named method somewhere
// that is NOT inside a `go` statement. The distinction is the whole point: a
// call moved behind `go` still appears in the body and still runs, but it no
// longer takes its lock on the emitting goroutine, so it no longer turns an
// emit-under-that-lock into a self-deadlock.
func callsOnTheSameGoroutine(fn *ast.FuncDecl, method string) bool {
	var spawned []*ast.GoStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		if g, ok := n.(*ast.GoStmt); ok {
			spawned = append(spawned, g)
		}
		return true
	})

	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method {
			return true
		}
		for _, g := range spawned {
			if g.Pos() <= call.Pos() && call.End() <= g.End() {
				return true
			}
		}
		found = true
		return false
	})
	return found
}

// acquiresBeforeAnyReturn reports whether fn locks the named mutex field at the
// top level of its body with no return ahead of it. An acquisition guarded by
// an early return is not a guard: the caller that skips it emits unprotected.
func acquiresBeforeAnyReturn(fn *ast.FuncDecl, mutexField string) bool {
	for _, stmt := range fn.Body.List {
		if locksMutexField(stmt, mutexField) {
			return true
		}
		returns := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.ReturnStmt); ok {
				returns = true
			}
			return !returns
		})
		if returns {
			return false
		}
	}
	return false
}

// locksMutexField reports whether stmt is `x.<mutexField>.Lock()` or
// `x.<mutexField>.RLock()`.
func locksMutexField(stmt ast.Stmt, mutexField string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	lock, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (lock.Sel.Name != "Lock" && lock.Sel.Name != "RLock") {
		return false
	}
	field, ok := lock.X.(*ast.SelectorExpr)
	return ok && field.Sel.Name == mutexField
}

// exprText renders a selector or identifier for a failure message.
func exprText(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	default:
		return "a non-selector expression"
	}
}
