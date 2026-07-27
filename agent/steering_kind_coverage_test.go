package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// Every kind in the enum must be produced by at least one non-test call site.
// This is the net that catches a kind going stale — the failure mode the
// deleted read-only classifier rule showed, where the UI kept a rule for a
// message the daemon had stopped sending and nothing noticed.
func TestEverySteeringKindHasAProducer(t *testing.T) {
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
	for _, kind := range events.AllSteeringKinds {
		constName := steeringKindConstName(kind)
		references := 0
		for _, file := range files {
			references += steeringKindReferenceCount(file, constName)
		}
		if references == 0 {
			t.Errorf("kind %q (events.%s) has no producer in agent/*.go", kind, constName)
		}
	}
}

func TestSteeringKindProducerReferencesIgnoreCommentsAndStrings(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "producer.go", []byte(`package agent

// events.SteeringKindTasksDone
var text = "SteeringKindTasksDone"
var _ = events.SteeringKindTasksDone
`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := steeringKindReferenceCount(file, "SteeringKindTasksDone"); got != 1 {
		t.Fatalf("steering kind reference count = %d, want 1", got)
	}
}

// steeringKindReferenceCount counts identifier references in parsed Go source;
// comments and string literals are not AST identifiers.
func steeringKindReferenceCount(file *ast.File, constName string) (count int) {
	ast.Inspect(file, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && ident.Name == constName {
			count++
		}
		return true
	})
	return count
}

// steeringKindConstName maps "tasks-done" to "SteeringKindTasksDone".
func steeringKindConstName(kind string) string {
	var out strings.Builder
	out.WriteString("SteeringKind")
	for part := range strings.SplitSeq(kind, "-") {
		if part == "" {
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]))
		out.WriteString(part[1:])
	}
	return out.String()
}

// TestNoProducerPassesEmptySourceAndEmptyKindToTrySteerEnqueue is the
// producer-side counterpart to TestEverySteeringKindHasAProducer: that test
// catches a kind that stops being produced anywhere; this one catches a call
// site that never carried one in the first place. trySteerWithImagesAndProvenance
// (the provenance-carrying steer path) once hardcoded BOTH source and kind to
// "" on the way to trySteerEnqueue, so every steer that carried causal
// provenance rendered as a bare, unlabelled divider -- forever, not just on
// old transcripts, because nothing checked producer -> kind the way this does.
//
// An empty SOURCE alone is legitimate (SteerKind's daemon-authored callers
// have no source and a real kind; see steeringKindConstName's sibling test
// above), so this only flags a call that carries NEITHER: no source AND no
// kind, the shape that renders unlabelled with no way to tell live or on
// reload.
func TestNoProducerPassesEmptySourceAndEmptyKindToTrySteerEnqueue(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "trySteerEnqueue" || len(call.Args) != 5 {
				return true
			}
			source, kind := call.Args[3], call.Args[4]
			if isEmptyStringLiteral(source) && isEmptyStringLiteral(kind) {
				t.Errorf("%s: trySteerEnqueue call passes literal \"\" for both source and kind; this steer renders unlabelled with no way to tell live or on reload", fset.Position(call.Pos()))
			}
			return true
		})
	}
}

// isEmptyStringLiteral reports whether e is the literal "" -- a static check,
// not a runtime one: a variable that happens to hold "" at runtime does not
// match, matching this test's target (a call site that never wired a kind
// through) rather than a value that could be non-empty depending on the
// caller.
func isEmptyStringLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && lit.Value == `""`
}

func TestMaybeInjectTaskReminderReturnsItsKind(t *testing.T) {
	s := newTestSession(t)
	// Trigger 3: task_list never used, 10+ rounds in.
	s.totalRounds = 10
	text, kind := s.maybeInjectTaskReminder()
	if text == "" {
		t.Fatal("expected a reminder text")
	}
	if kind != events.SteeringKindTaskNudge {
		t.Errorf("kind = %q, want %q", kind, events.SteeringKindTaskNudge)
	}
}
