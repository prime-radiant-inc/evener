// Command serf-docscheck enforces that every exported, package-level declaration
// in the published library packages carries a Go doc comment.
//
// The serf monorepo exposes a small set of library modules — llm, agent (plus
// its public agent/events subpackage), and auth/openai — for consumption inside
// and outside Prime Radiant. A "documented public API" is part of the bar for
// those libraries, so this check fails CI if any exported type, function,
// package-level var, or const in a library package lacks a doc comment.
//
// Scope is deliberately the package-level surface: methods and struct fields are
// documented where their purpose isn't self-evident, but gating every exported
// method (String, simple accessors) and field would be stricter than Go's own
// convention and is intentionally not enforced here.
//
// Run from the repository root:
//
//	go run ./cmd/serf-docscheck
//
// It exits non-zero (and prints each offender as file:symbol) when anything is
// undocumented.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// libraryPackages are the published library package directories, relative to the
// repository root, whose exported package-level surface must be documented.
var libraryPackages = []string{
	"llm",
	"agent",
	"agent/events",
	"auth/openai",
}

// violation is a single undocumented exported declaration.
type violation struct {
	pkg  string // package dir, e.g. "agent/events"
	file string // base filename, e.g. "events.go"
	kind string // "type", "func", "const", or "var"
	name string // the exported identifier
}

func main() {
	var violations []violation
	for _, pkg := range libraryPackages {
		v, err := checkPackage(pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-docscheck: %s: %v\n", pkg, err)
			os.Exit(2)
		}
		violations = append(violations, v...)
	}

	if len(violations) == 0 {
		fmt.Println("serf-docscheck: all exported package-level declarations in the library packages are documented")
		return
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].pkg != violations[j].pkg {
			return violations[i].pkg < violations[j].pkg
		}
		return violations[i].name < violations[j].name
	})
	for _, v := range violations {
		fmt.Printf("%s/%s: %s %s is undocumented\n", v.pkg, v.file, v.kind, v.name)
	}
	fmt.Fprintf(os.Stderr, "\nserf-docscheck: %d undocumented exported declaration(s)\n", len(violations))
	os.Exit(1)
}

// checkPackage parses the non-test Go files in dir and returns a violation for
// each exported package-level declaration that lacks a doc comment.
func checkPackage(dir string) ([]violation, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var out []violation
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.Name, "_test") {
			continue
		}
		for path, file := range pkg.Files {
			base := filepath.Base(path)
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					// Package-level functions only (methods are out of scope).
					if d.Recv == nil && d.Name.IsExported() && d.Doc == nil {
						out = append(out, violation{dir, base, "func", d.Name.Name})
					}
				case *ast.GenDecl:
					// A doc on the GenDecl block (e.g. a single `// X ...` above a
					// lone `type X ...`, or a comment above a parenthesized group)
					// documents its specs.
					if d.Doc != nil {
						continue
					}
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.IsExported() && s.Doc == nil {
								out = append(out, violation{dir, base, "type", s.Name.Name})
							}
						case *ast.ValueSpec:
							if s.Doc != nil {
								continue
							}
							kind := "var"
							if d.Tok.String() == "const" {
								kind = "const"
							}
							for _, n := range s.Names {
								if n.IsExported() {
									out = append(out, violation{dir, base, kind, n.Name})
								}
							}
						}
					}
				}
			}
		}
	}
	return out, nil
}
