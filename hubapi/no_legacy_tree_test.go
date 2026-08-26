package hubapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoLegacyTreeTypes asserts that the tree-only hubapi types and the
// Client.Tree method are not defined. These were retired by Task 14
// (R50: zero legacy now); the navigation service's bounded HTTP resources
// are the sole authority.
//
// This test scans the package's own source files for forbidden type or method
// declarations. If any are re-introduced, the test fails.
func TestNoLegacyTreeTypes(t *testing.T) {
	forbiddenTypes := []string{"TreeResponse", "TreeProject", "TreeNode", "PinSectionTree", "TreeProjectPage"}
	forbiddenMethods := []string{"Tree"}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range node.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						for _, forbidden := range forbiddenTypes {
							if ts.Name.Name == forbidden {
								t.Errorf("%s: forbidden legacy type %s re-introduced", file, forbidden)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil && d.Name != nil {
					for _, forbidden := range forbiddenMethods {
						if d.Name.Name == forbidden {
							t.Errorf("%s: forbidden legacy method %s re-introduced", file, forbidden)
						}
					}
				}
			}
		}
	}
}
