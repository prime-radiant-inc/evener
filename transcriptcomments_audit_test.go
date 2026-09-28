package evener_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// decodeStrictJSONCitedTests are the tests the decodeStrictJSON comment must
// name for the two halves of the reader split it describes: the transcript
// index's quarantine (cmd/evener-hub) and the agent's whole-file readers'
// refusal (agent). Naming them is the comment's contract.
var decodeStrictJSONCitedTests = []string{
	"TestPastThreadReadQuarantinesOneUnknownTurnField",
	"TestTranscriptReadersAndRawOutputRejectUnknownFields",
}

// TestDecodeStrictJSONCommentCitesExistingTests pins issue #2782's correction
// to agent/transcript/transcript.go's decodeStrictJSON comment. The comment
// used to claim one unknown-field record made the ENTIRE transcript unreadable
// to every reader, citing
// TestPastThreadReadFailsWholeSessionOnOneUnknownTurnField, which no longer
// exists. The behavior is now split: thread reads and live history go through
// the transcript index, which quarantines the one entry, while the agent's
// whole-file readers still abort on the first undecodable entry. The audit
// requires the comment to cite a test for each half and each cited test to be
// declared somewhere in the tree, so the comment cannot drift back to naming a
// test that is gone.
func TestDecodeStrictJSONCommentCitesExistingTests(t *testing.T) {
	t.Parallel()
	comment := decodeStrictJSONComment(t)
	if comment == "" {
		t.Fatal("agent/transcript/transcript.go decodeStrictJSON has no comment; this audit no longer reads it")
	}
	declared := declaredTestNames(t)
	for _, name := range decodeStrictJSONCitedTests {
		if !strings.Contains(comment, name) {
			t.Errorf("decodeStrictJSON comment no longer cites %s; the reader split it describes moved and this audit no longer pins it", name)
			continue
		}
		if !declared[name] {
			t.Errorf("decodeStrictJSON comment cites %s, which no test in the tree declares: the comment names a test that is gone (issue #2782)", name)
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

// declaredTestNames returns every func Test... declared in the tree's
// *_test.go files, read straight from disk so the audit depends on the files
// present in the checkout, not on the git binary or VCS tracked-state.
func declaredTestNames(t *testing.T) map[string]bool {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	names := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && ignoredAuditDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pattern.FindAllStringSubmatch(string(raw), -1) {
			names[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree for test declarations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("found no test functions in the tree; the declaration scan is broken")
	}
	return names
}

// ignoredAuditDir reports whether a directory is outside the source tree the
// audit reads: VCS metadata, dependency trees, and dotdirs.
func ignoredAuditDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "mobile-native":
		return true
	}
	return strings.HasPrefix(name, ".")
}
