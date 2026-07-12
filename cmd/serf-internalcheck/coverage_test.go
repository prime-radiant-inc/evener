package main

import (
	"bytes"
	"errors"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestRunWithAllResults(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := runWith(func() ([]string, error) { return nil, nil }, &out, &errOut); got != 0 {
		t.Fatalf("clean = %d", got)
	}
	if got := runWith(func() ([]string, error) { return []string{"leak"}, nil }, &out, &errOut); got != 1 || !strings.Contains(out.String(), "leak") {
		t.Fatalf("leak = %d %q", got, out.String())
	}
	if got := runWith(func() ([]string, error) { return nil, errors.New("load") }, &out, &errOut); got != 2 || !strings.Contains(errOut.String(), "load") {
		t.Fatalf("error = %d %q", got, errOut.String())
	}
}

func TestMainUsesExit(t *testing.T) {
	oldExit, oldPkgs := osExit, libraryPackages
	t.Cleanup(func() { osExit, libraryPackages = oldExit, oldPkgs })
	libraryPackages = nil
	got := -1
	osExit = func(code int) { got = code }
	main()
	if got != 0 {
		t.Fatalf("exit = %d", got)
	}
}

func TestWalkTypeAllShapesAndObjects(t *testing.T) {
	internal := syntheticInternal("primeradiant.com/serf/x/internal/y", "Secret")
	pkg := types.NewPackage("primeradiant.com/serf/pub", "pub")
	into := map[string]bool{}
	seen := map[types.Type]bool{}
	typesToWalk := []types.Type{
		types.NewPointer(internal), types.NewSlice(internal), types.NewArray(internal, 2),
		types.NewMap(internal, internal), types.NewChan(types.SendRecv, internal),
		types.NewSignatureType(nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, pkg, "x", internal)), types.NewTuple(types.NewVar(token.NoPos, pkg, "y", internal)), false),
		types.NewStruct([]*types.Var{types.NewField(token.NoPos, pkg, "Hidden", internal, false), types.NewField(token.NoPos, pkg, "hidden", internal, false)}, nil),
		types.NewInterfaceType([]*types.Func{types.NewFunc(token.NoPos, pkg, "Exported", types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, pkg, "", internal)), false)), types.NewFunc(token.NoPos, pkg, "hidden", types.NewSignatureType(nil, nil, nil, nil, nil, false))}, nil).Complete(),
	}
	for _, typ := range typesToWalk {
		walkType(typ, into, seen)
	}
	walkType(nil, into, seen)
	walkType(internal, into, seen)
	checkObject(types.NewFunc(token.NoPos, pkg, "F", types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, pkg, "", internal)), false)), into)
	checkObject(types.NewVar(token.NoPos, pkg, "V", internal), into)
	checkObject(types.NewConst(token.NoPos, pkg, "C", types.Typ[types.Int], nil), into)
	checkObject(types.NewPkgName(token.NoPos, pkg, "x", pkg), into)
	if len(into) != 1 {
		t.Fatalf("leaks = %v", into)
	}
}

func TestFindLeaksLoaderFailuresAndLeak(t *testing.T) {
	oldLoad, oldVisit := packagesLoad, packagesVisit
	t.Cleanup(func() { packagesLoad, packagesVisit = oldLoad, oldVisit })
	packagesLoad = func(*packages.Config, ...string) ([]*packages.Package, error) { return nil, errors.New("boom") }
	if _, err := findLeaks(); err == nil || !strings.Contains(err.Error(), "load packages") {
		t.Fatalf("err = %v", err)
	}

	packagesLoad = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return []*packages.Package{{PkgPath: "bad", Errors: []packages.Error{{Msg: "broken"}}}}, nil
	}
	if _, err := findLeaks(); err == nil || !strings.Contains(err.Error(), "loading bad") {
		t.Fatalf("err = %v", err)
	}

	internal := syntheticInternal("primeradiant.com/serf/a/internal/b", "Secret")
	pkg := types.NewPackage("primeradiant.com/serf/public", "public")
	pkg.Scope().Insert(types.NewVar(token.NoPos, pkg, "Leaky", internal))
	pkg.Scope().Insert(types.NewVar(token.NoPos, pkg, "hidden", internal))
	packagesLoad = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return []*packages.Package{{PkgPath: pkg.Path(), Types: pkg}}, nil
	}
	leaks, err := findLeaks()
	if err != nil || len(leaks) != 1 || !strings.Contains(leaks[0], "Leaky exposes") {
		t.Fatalf("leaks = %v, err = %v", leaks, err)
	}
}

func TestWalkTypePublicNamedTypeArguments(t *testing.T) {
	pkg := types.NewPackage("primeradiant.com/serf/public", "public")
	internal := syntheticInternal("primeradiant.com/serf/a/internal/b", "Secret")
	tp := types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, "T", nil), types.NewInterfaceType(nil, nil).Complete())
	origin := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Box", nil), types.NewStruct(nil, nil), nil)
	origin.SetTypeParams([]*types.TypeParam{tp})
	inst, err := types.Instantiate(nil, origin, []types.Type{internal}, true)
	if err != nil {
		t.Fatal(err)
	}
	into := map[string]bool{}
	walkType(inst, into, map[types.Type]bool{})
	if len(into) != 1 {
		t.Fatalf("leaks = %v", into)
	}
}
