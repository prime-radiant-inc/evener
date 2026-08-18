package main

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestLibrariesHaveNoInternalLeaks is the regression guard: the agent, llm, and
// llm/providercfg libraries must not name any evener-internal type in their
// exported surface, so they remain externally importable. (The walk itself is
// exercised continuously by CI running the binary on the real code.)
func TestLibrariesHaveNoInternalLeaks(t *testing.T) {
	leaks, err := findLeaks()
	if err != nil {
		t.Fatalf("findLeaks: %v", err)
	}
	if len(leaks) != 0 {
		t.Fatalf("exported library API leaks %d evener-internal type(s):\n  %s",
			len(leaks), strings.Join(leaks, "\n  "))
	}
}

// TestIsSerfInternal table-drives the path classifier that gates the leak detector.
func TestIsSerfInternal(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Evener-internal paths — must be flagged.
		{"primeradiant.com/evener/agent/internal/types", true},
		{"primeradiant.com/evener/foo/internal/bar", true},
		{"primeradiant.com/evener/llm/internal/x", true},
		// Public evener paths — must not be flagged.
		{"primeradiant.com/evener/agent", false},
		{"primeradiant.com/evener/llm", false},
		{"primeradiant.com/evener", false},
		// Wrong module entirely.
		{"other.com/evener/foo/internal/bar", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isSerfInternal(tc.path)
		if got != tc.want {
			t.Errorf("isSerfInternal(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// syntheticInternal builds a *types.Named whose package path is a evener-internal path,
// allowing in-process tests of walkType and checkObject without any file I/O.
func syntheticInternal(pkgPath, typeName string) *types.Named {
	pkgName := pkgPath[strings.LastIndex(pkgPath, "/")+1:]
	pkg := types.NewPackage(pkgPath, pkgName)
	obj := types.NewTypeName(token.NoPos, pkg, typeName, nil)
	return types.NewNamed(obj, types.Typ[types.Int], nil)
}

// TestWalkTypeDetectsInternalNamed confirms that walkType records a *types.Named
// whose package is a evener-internal path.
func TestWalkTypeDetectsInternalNamed(t *testing.T) {
	internal := syntheticInternal("primeradiant.com/evener/foo/internal/bar", "Secret")
	into := map[string]bool{}
	seen := map[types.Type]bool{}
	walkType(internal, into, seen)

	want := "primeradiant.com/evener/foo/internal/bar.Secret"
	if !into[want] {
		t.Errorf("walkType did not flag internal type %q; into = %v", want, into)
	}
	if len(into) != 1 {
		t.Errorf("walkType flagged unexpected extra entries: %v", into)
	}
}

// TestWalkTypeIgnoresNonInternalNamed confirms that walkType does not flag a
// *types.Named from a public (non-internal) evener package.
func TestWalkTypeIgnoresNonInternalNamed(t *testing.T) {
	pkg := types.NewPackage("primeradiant.com/evener/agent", "agent")
	obj := types.NewTypeName(token.NoPos, pkg, "Session", nil)
	types.NewNamed(obj, types.Typ[types.Int], nil)

	into := map[string]bool{}
	seen := map[types.Type]bool{}
	walkType(obj.Type(), into, seen)
	if len(into) != 0 {
		t.Errorf("walkType flagged non-internal type: %v", into)
	}
}

// TestCheckObjectStructFieldExposesInternal verifies that checkObject reports an
// exported struct field whose type is a evener-internal named type.  This is the
// central detection path that the existing regression guard cannot exercise.
func TestCheckObjectStructFieldExposesInternal(t *testing.T) {
	internal := syntheticInternal("primeradiant.com/evener/agent/internal/cfg", "Config")

	libPkg := types.NewPackage("primeradiant.com/evener/agent", "agent")
	field := types.NewField(token.NoPos, libPkg, "Cfg", internal, false)
	structType := types.NewStruct([]*types.Var{field}, nil)
	outerObj := types.NewTypeName(token.NoPos, libPkg, "Session", nil)
	types.NewNamed(outerObj, structType, nil)

	into := map[string]bool{}
	checkObject(outerObj, into)

	want := "primeradiant.com/evener/agent/internal/cfg.Config"
	if !into[want] {
		t.Errorf("checkObject did not flag exported field of internal type %q; into = %v", want, into)
	}
}
