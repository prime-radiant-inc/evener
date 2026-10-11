package evener_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestProjRefDocNamesItsActualCallers pins issue #2624's correction to
// agent/doctor/locate.go's projRef doc comment. The comment claimed "refFor
// calls it after identifier.ValidateProjectID accepts the name", but refFor now
// delegates to bucketref.RefFor and no longer calls projRef at all; projRef's
// only callers are followSelector and readSelector in audit.go. The audit reads
// the actual call sites out of the source, so the comment cannot drift back to
// naming a function that no longer calls it.
func TestProjRefDocNamesItsActualCallers(t *testing.T) {
	t.Parallel()
	doc, callers := projRefAuditDocAndCallers(t)
	if doc == "" {
		t.Fatal("agent/doctor/locate.go projRef has no doc comment; the caller contract moved and this audit no longer reads it")
	}
	// Both callers exist today (audit.go followSelector and readSelector), so a
	// missing one means the scan broke, not that the comment is right.
	for _, want := range []string{"followSelector", "readSelector"} {
		if !callers[want] {
			t.Fatalf("projRef scan found no call from %s; the audit is reading the wrong thing (callers: %v)", want, callers)
		}
	}
	if strings.Contains(doc, "refFor calls") {
		t.Errorf("projRef doc still claims refFor calls it; refFor delegates to bucketref.RefFor and does not call projRef (callers: %v)", callers)
	}
	for caller := range callers {
		if !strings.Contains(doc, caller) {
			t.Errorf("projRef doc does not name its caller %q (callers: %v)", caller, callers)
		}
	}
}

// TestTranscriptLookupSymlinkPolicyCites2205 pins issue #2624's correction to
// agent/transcript_lookup.go's enumerateBuckets Symlink-policy comment. The
// comment cited only the #2275 owner ruling and left out the #2205
// security-boundary context that the shared bucketref.RefuseSymlinks doc
// carries. The audit reads that shared doc as its oracle, so the local comment
// keeps the cite the shared policy already states.
func TestTranscriptLookupSymlinkPolicyCites2205(t *testing.T) {
	t.Parallel()
	shared := declDocByName(t, "agent/internal/bucketref/bucketref.go", "RefuseSymlinks")
	if !strings.Contains(shared, "#2205") {
		t.Fatal("agent/internal/bucketref/bucketref.go RefuseSymlinks doc no longer cites #2205; this audit's oracle moved")
	}
	local := declDocByName(t, "agent/transcript_lookup.go", "enumerateBuckets")
	if local == "" {
		t.Fatal("agent/transcript_lookup.go enumerateBuckets has no doc comment; the Symlink-policy contract moved and this audit no longer reads it")
	}
	if !strings.Contains(local, "#2205") {
		t.Errorf("enumerateBuckets Symlink-policy comment does not cite the #2205 security boundary the shared RefuseSymlinks doc names:\n%s", local)
	}
	if !strings.Contains(local, "#2275") {
		t.Errorf("enumerateBuckets Symlink-policy comment dropped the #2275 owner ruling:\n%s", local)
	}
}

// projRefAuditDocAndCallers parses agent/doctor's locate.go and audit.go and
// returns projRef's doc comment plus the set of top-level functions whose bodies
// call projRef. The call sites are the oracle, not the comment.
func projRefAuditDocAndCallers(t *testing.T) (string, map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	callers := map[string]bool{}
	var doc string
	for _, path := range []string{"agent/doctor/locate.go", "agent/doctor/audit.go"} {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.Name == "projRef" && fn.Doc != nil {
				doc = fn.Doc.Text()
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "projRef" {
					callers[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	return doc, callers
}

// declDocByName returns the doc comment on the declaration named name in a Go
// file. name may be a function or a const/var spec (the shared bucketref
// symlink-policy docs live on const specs). It returns "" when the file has no
// such declaration, so a moved comment is read as missing rather than accepted.
func declDocByName(t *testing.T, path, name string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Name.Name == name && decl.Doc != nil {
				return decl.Doc.Text()
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Doc == nil {
					continue
				}
				for _, ident := range vs.Names {
					if ident.Name == name {
						return vs.Doc.Text()
					}
				}
			}
		}
	}
	return ""
}
