package events

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Every sealed payload type is bound to exactly one EventKind, and no two
// payload types share a kind: emit derives the kind from the payload alone, so
// a shared or ambiguous binding would make the stream's kind lie.
func TestEveryPayloadTypeMapsToExactlyOneKind(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "eventdata.go", nil, 0)
	if err != nil {
		t.Fatalf("parse eventdata.go: %v", err)
	}
	kindByType := map[string]string{}
	typeByKind := map[string]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "eventKind" || fn.Recv == nil {
			continue
		}
		recv, ok := fn.Recv.List[0].Type.(*ast.Ident)
		if !ok {
			t.Fatalf("eventKind receiver is not a plain type name: %T", fn.Recv.List[0].Type)
		}
		if len(fn.Body.List) != 1 {
			t.Fatalf("%s.eventKind must be a single return of an EventKind constant", recv.Name)
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			t.Fatalf("%s.eventKind must be a single return of an EventKind constant", recv.Name)
		}
		kind, ok := ret.Results[0].(*ast.Ident)
		if !ok {
			t.Fatalf("%s.eventKind must return an EventKind constant", recv.Name)
		}
		if _, dup := kindByType[recv.Name]; dup {
			t.Fatalf("%s has more than one eventKind method", recv.Name)
		}
		if other, dup := typeByKind[kind.Name]; dup {
			t.Fatalf("%s and %s both map to %s", other, recv.Name, kind.Name)
		}
		kindByType[recv.Name] = kind.Name
		typeByKind[kind.Name] = recv.Name
	}
	if len(kindByType) == 0 {
		t.Fatal("found no eventKind methods; the parser no longer matches how payloads are sealed")
	}
}
