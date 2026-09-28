package evener_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// testNameCitedInComment matches a Go test function name mentioned in prose.
var testNameCitedInComment = regexp.MustCompile(`\bTest[A-Za-z0-9_]+`)

// TestDecodeStrictJSONCommentCitesExistingTests pins issue #2782's correction to
// agent/transcript/transcript.go's decodeStrictJSON comment. The comment used
// to claim one unknown-field record made the ENTIRE transcript unreadable to
// every reader, citing TestPastThreadReadFailsWholeSessionOnOneUnknownTurnField,
// which no longer exists. The behavior is now split: thread reads and live
// history go through the transcript index, which quarantines the one entry,
// while the agent's whole-file readers still abort on the first undecodable
// entry. The audit reads the test names the comment cites and requires each to
// be declared somewhere in the tree, so the comment cannot drift back to
// naming a test that is gone.
func TestDecodeStrictJSONCommentCitesExistingTests(t *testing.T) {
	t.Parallel()
	comment := decodeStrictJSONComment(t)
	if comment == "" {
		t.Fatal("agent/transcript/transcript.go decodeStrictJSON has no comment; this audit no longer reads it")
	}
	cited := testNameCitedInComment.FindAllString(comment, -1)
	if len(cited) == 0 {
		t.Fatal("decodeStrictJSON comment names no test; the behavior contract moved and this audit no longer reads it")
	}
	for _, name := range cited {
		if !goTestDeclared(t, name) {
			t.Errorf("decodeStrictJSON comment cites %s, which no test in the tree declares: "+
				"the comment names a test that is gone (issue #2782)", name)
		}
	}
}

// decodeStrictJSONComment returns every comment inside decodeStrictJSON's body
// in agent/transcript/transcript.go. The strict-decoding rationale is an
// interior comment, not the function's doc comment, so a doc-only read would
// miss it.
func decodeStrictJSONComment(t *testing.T) string {
	t.Helper()
	const path = "agent/transcript/transcript.go"
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "decodeStrictJSON" {
			continue
		}
		var b strings.Builder
		for _, group := range file.Comments {
			if group.Pos() >= fn.Pos() && group.End() <= fn.End() {
				b.WriteString(group.Text())
				b.WriteByte('\n')
			}
		}
		return b.String()
	}
	return ""
}

// goTestDeclared reports whether some tracked *_test.go declares func name(.
// It greps rather than indexing the tree: a test function name is unique, and
// the trailing paren rules out a prefix match on a longer name.
func goTestDeclared(t *testing.T, name string) bool {
	t.Helper()
	out, err := exec.Command("git", "grep", "-F", "-l", "func "+name+"(", "--", "*_test.go").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false // git grep found no declaration
		}
		t.Fatalf("git grep for %s: %v", name, err)
	}
	return strings.TrimSpace(string(out)) != ""
}
