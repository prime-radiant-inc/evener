// serf-internalcheck enforces that the public library packages stay externally
// importable: no exported symbol in agent, llm, or llm/providercfg may name a
// serf-internal type (a type whose package path is under primeradiant.com/serf
// and contains "/internal/") through its public signature.
//
// Such a leak makes a constructor or field uncallable/unnameable from outside
// the module — exactly the §2.0 blocker that promoting providerconfig and
// WorkspaceInfo fixed. This check locks that in: it walks every exported
// package-level object's public type surface (func signatures; var/const types;
// and, for each defined type, its exported struct fields, exported interface
// methods, and exported method-set signatures) and reports any reachable
// serf-internal named type.
//
// Each exported named type is itself a top-level object, so the walk does not
// expand other (non-internal) exported types — the transitive chain is covered
// by checking each link independently. Composite types (pointer/slice/map/chan/
// signature/tuple) are followed.
//
// Run via `make lint-internal` (or directly: `go run ./cmd/serf-internalcheck`).
// Exits non-zero, one `message` per line, when a leak exists.
package main

import (
	"fmt"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// libraryPackages are the caller-facing library packages whose exported surface
// must not name serf-internal types.
var libraryPackages = []string{
	"primeradiant.com/serf/agent",
	"primeradiant.com/serf/agent/execenv",
	"primeradiant.com/serf/agent/mcpconfig",
	"primeradiant.com/serf/agent/provider",
	"primeradiant.com/serf/agent/schema",
	"primeradiant.com/serf/agent/skill",
	"primeradiant.com/serf/agent/task",
	"primeradiant.com/serf/agent/transcript",
	"primeradiant.com/serf/llm",
	"primeradiant.com/serf/llm/providercfg",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "serf-internalcheck:", err)
		os.Exit(2)
	}
}

func run() error {
	leaks, err := findLeaks()
	if err != nil {
		return err
	}
	if len(leaks) == 0 {
		return nil
	}
	for _, v := range leaks {
		fmt.Println(v)
	}
	os.Exit(1)
	return nil
}

// findLeaks loads the library packages and returns one sorted message per
// exported symbol that names a serf-internal type. Empty result means clean.
func findLeaks() ([]string, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
	}
	pkgs, err := packages.Load(cfg, libraryPackages...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	var loadErr error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			if loadErr == nil {
				loadErr = fmt.Errorf("loading %s: %w", p.PkgPath, e)
			}
		}
	})
	if loadErr != nil {
		return nil, loadErr
	}

	violations := map[string]bool{}
	for _, pkg := range pkgs {
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			leaks := map[string]bool{}
			checkObject(obj, leaks)
			for leak := range leaks {
				violations[fmt.Sprintf("%s.%s exposes internal type %s", pkg.PkgPath, name, leak)] = true
			}
		}
	}

	out := make([]string, 0, len(violations))
	for v := range violations {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

// checkObject walks the public type surface of a top-level exported object.
func checkObject(obj types.Object, into map[string]bool) {
	seen := map[types.Type]bool{}
	switch o := obj.(type) {
	case *types.TypeName:
		named, ok := o.Type().(*types.Named)
		if !ok {
			walkType(o.Type(), into, seen)
			return
		}
		// The defined type's own exported surface: its underlying composite
		// (struct fields / interface methods) and its exported methods.
		walkType(named.Underlying(), into, seen)
		for i := 0; i < named.NumMethods(); i++ {
			if m := named.Method(i); m.Exported() {
				walkType(m.Type(), into, seen)
			}
		}
	case *types.Func:
		walkType(o.Type(), into, seen)
	case *types.Var, *types.Const:
		walkType(o.Type(), into, seen)
	}
}

// walkType collects serf-internal named types reachable through t's public
// surface. It flags an internal *types.Named and stops; it does not expand a
// non-internal named type (that type is checked independently as its own
// top-level object).
func walkType(t types.Type, into map[string]bool, seen map[types.Type]bool) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Named:
		if pkg := u.Obj().Pkg(); pkg != nil && isSerfInternal(pkg.Path()) {
			into[pkg.Path()+"."+u.Obj().Name()] = true
			return
		}
		if ta := u.TypeArgs(); ta != nil {
			for i := 0; i < ta.Len(); i++ {
				walkType(ta.At(i), into, seen)
			}
		}
	case *types.Pointer:
		walkType(u.Elem(), into, seen)
	case *types.Slice:
		walkType(u.Elem(), into, seen)
	case *types.Array:
		walkType(u.Elem(), into, seen)
	case *types.Map:
		walkType(u.Key(), into, seen)
		walkType(u.Elem(), into, seen)
	case *types.Chan:
		walkType(u.Elem(), into, seen)
	case *types.Signature:
		walkTuple(u.Params(), into, seen)
		walkTuple(u.Results(), into, seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if f := u.Field(i); f.Exported() {
				walkType(f.Type(), into, seen)
			}
		}
	case *types.Interface:
		for i := 0; i < u.NumMethods(); i++ {
			if m := u.Method(i); m.Exported() {
				walkType(m.Type(), into, seen)
			}
		}
	}
}

func walkTuple(tup *types.Tuple, into map[string]bool, seen map[types.Type]bool) {
	for i := 0; i < tup.Len(); i++ {
		walkType(tup.At(i).Type(), into, seen)
	}
}

func isSerfInternal(pkgPath string) bool {
	return strings.HasPrefix(pkgPath, "primeradiant.com/serf/") && strings.Contains(pkgPath, "/internal/")
}
