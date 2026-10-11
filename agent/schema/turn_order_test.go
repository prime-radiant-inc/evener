package schema

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestTurnFieldsStayDeclaredInProjectionOrder enforces the declaration
// invariant documented on Turn: publicTranscriptLine re-marshals the turn
// through string-keyed maps, which sort keys alphabetically, so the
// struct's fields must stay declared in sorted JSON-key order for a
// projected line to remain byte-identical to the persisted one. The
// byte-identity fixture pins every key the projection keeps, but the
// projection-deleted keys (attention_id, attention_resolution,
// delegate_delivery_commits) are invisible to it, so their order is
// enforced here, against the declaration itself.
func TestTurnFieldsStayDeclaredInProjectionOrder(t *testing.T) {
	src, err := os.ReadFile("turn.go")
	if err != nil {
		t.Fatalf("read turn.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "turn.go", src, 0)
	if err != nil {
		t.Fatalf("parse turn.go: %v", err)
	}
	var keys []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Turn" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				t.Fatal("Turn declaration is not a struct")
			}
			for _, field := range st.Fields.List {
				if field.Tag == nil || len(field.Names) == 0 {
					t.Fatalf("Turn field %v lacks a name or json tag", field.Names)
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					t.Fatalf("unquote tag %s: %v", field.Tag.Value, err)
				}
				name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
				if name == "" || name == "-" {
					continue
				}
				keys = append(keys, name)
			}
		}
	}
	if len(keys) == 0 {
		t.Fatal("Turn struct not found in turn.go")
	}
	if !sort.StringsAreSorted(keys) {
		sorted := append([]string(nil), keys...)
		sort.Strings(sorted)
		t.Fatalf("Turn fields must stay declared in sorted JSON-key order (publicTranscriptLine re-marshals through sorted maps); got %v, want %v", keys, sorted)
	}
}
