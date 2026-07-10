package main

import (
	"go/build"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRegistry(t *testing.T) {
	got, err := ParseRegistry(strings.NewReader(strings.Join([]string{
		"native:agent:.:FuzzTurn::turn.go",
		"rapid:agent:./core:TestStateful",
		"test:.:./appwire:TestStructuredFrameReachesDecoder",
		"",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{
		{Kind: "native", Module: "agent", Package: ".", Name: "FuzzTurn"},
		{Kind: "rapid", Module: "agent", Package: "./core", Name: "TestStateful"},
		{Kind: "test", Module: ".", Package: "./appwire", Name: "TestStructuredFrameReachesDecoder"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRegistry() = %#v, want %#v", got, want)
	}
}

func TestDiscoverWorkspaceFindsNativeAndMarkedRapidTargets(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse (\n\t.\n\t./agent\n)\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"root_fuzz_test.go": `package root

import "testing"

func FuzzRoot(f *testing.F) {}
`,
		"agent/go.mod": "module example.test/agent\n\ngo 1.25.0\n",
		"agent/turn_fuzz_test.go": `package agent

import "testing"

func FuzzTurn(f *testing.F) {}
`,
		"agent/core/state_test.go": `package core

import (
	"testing"
	"pgregory.net/rapid"
)

// serf:fuzz rapid
func TestStateful(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {})
}
`,
	})

	got, err := DiscoverWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{
		{Kind: "native", Module: ".", Package: ".", Name: "FuzzRoot"},
		{Kind: "native", Module: "agent", Package: ".", Name: "FuzzTurn"},
		{Kind: "rapid", Module: "agent", Package: "./core", Name: "TestStateful"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverWorkspace() = %#v, want %#v", got, want)
	}
	if err := CheckTargets(want, got); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverWorkspaceUsesLogicalLabelForSymlinkedModule(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse (\n\t.\n\t./alias\n)\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"root_fuzz_test.go": `package root

import "testing"

func FuzzRoot(f *testing.F) {}
`,
		"nested/go.mod": "module example.test/nested\n\ngo 1.25.0\n",
		"nested/alias_fuzz_test.go": `//go:build serffuzz

package nested

import "testing"

func FuzzAlias(f *testing.F) {}
`,
	})
	mustSymlink(t, "nested", filepath.Join(root, "alias"))

	got, err := DiscoverWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{
		{Kind: "native", Module: ".", Package: ".", Name: "FuzzRoot"},
		{Kind: "native", Module: "alias", Package: ".", Name: "FuzzAlias"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverWorkspace() = %#v, want %#v", got, want)
	}
	registered, err := ParseRegistry(strings.NewReader("native:.:.:FuzzRoot\nnative:alias:.:FuzzAlias\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckTargets(registered, got); err != nil {
		t.Fatalf("CheckTargets() = %v, want alias registry row to match", err)
	}
}

func TestDiscoverWorkspaceRejectsSymlinkedModuleOutsideRepository(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse ./alias\n",
	})
	outside := t.TempDir()
	writeFile(t, outside, "go.mod", "module example.test/outside\n\ngo 1.25.0\n")
	mustSymlink(t, outside, filepath.Join(root, "alias"))

	_, err := DiscoverWorkspace(root)
	assertErrorContains(t, err, `go.work module "./alias" is outside repository root`)
}

func TestDiscoverWorkspaceRejectsDuplicateResolvedModuleDirectories(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work":       "go 1.25.0\n\nuse (\n\t./nested\n\t./alias\n)\n",
		"nested/go.mod": "module example.test/nested\n\ngo 1.25.0\n",
	})
	mustSymlink(t, "nested", filepath.Join(root, "alias"))

	_, err := DiscoverWorkspace(root)
	assertErrorContains(t, err, `go.work lists duplicate module directory "./alias"`)
}

func TestCheckTargetsReportsMissingNativeFuzzer(t *testing.T) {
	registered := []Target{{Kind: "rapid", Module: "agent", Package: ".", Name: "TestStateful"}}
	discovered := append([]Target{{Kind: "native", Module: "agent", Package: ".", Name: "FuzzTurn"}}, registered...)
	err := CheckTargets(registered, discovered)
	assertErrorContains(t, err, "missing registration: native:agent:.:FuzzTurn")
}

func TestCheckTargetsReportsStalePackageRow(t *testing.T) {
	registered := []Target{{Kind: "native", Module: "agent", Package: "./stale", Name: "FuzzTurn"}}
	discovered := []Target{{Kind: "native", Module: "agent", Package: ".", Name: "FuzzTurn"}}
	err := CheckTargets(registered, discovered)
	assertErrorContains(t, err, "missing registration: native:agent:.:FuzzTurn")
	assertErrorContains(t, err, "stale registration: native:agent:./stale:FuzzTurn")
}

func TestCheckTargetsReportsDuplicateIdentity(t *testing.T) {
	target := Target{Kind: "native", Module: "agent", Package: ".", Name: "FuzzTurn"}
	err := CheckTargets([]Target{target, target}, []Target{target})
	assertErrorContains(t, err, "duplicate registered target: native:agent:.:FuzzTurn")
}

func TestCheckTargetsDistinguishesColonContainingTupleFields(t *testing.T) {
	targets := []Target{
		{Kind: "native", Module: "agent:./internal", Package: "./tool", Name: "FuzzTurn"},
		{Kind: "native", Module: "agent", Package: "./internal:./tool", Name: "FuzzTurn"},
	}

	if err := CheckTargets(targets, targets); err != nil {
		t.Fatalf("CheckTargets() = %v, want distinct exact tuple identities", err)
	}
}

func TestDiscoverWorkspaceIgnoresFuzzLikeProductionDeclaration(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse .\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"fuzz_like.go": `package root

import "testing"

func FuzzProduction(f *testing.F) {}
`,
	})

	got, err := DiscoverWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("DiscoverWorkspace() = %#v, want no targets from production files", got)
	}
}

func TestDiscoverWorkspaceHonorsGoBuildTestFileSelection(t *testing.T) {
	inactiveGOOS := "windows"
	if build.Default.GOOS == inactiveGOOS {
		inactiveGOOS = "linux"
	}

	files := map[string]string{
		"go.work": "go 1.25.0\n\nuse .\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"active_fuzz_test.go": `//go:build serffuzz

package root

import "testing"

func FuzzActive(f *testing.F) {}
`,
		"inactive_build_fuzz_test.go": `//go:build ` + inactiveGOOS + `

package root

import "testing"

func FuzzInactiveBuild(f *testing.F) {}
`,
		"_ignored_fuzz_test.go": `package root

import "testing"

func FuzzIgnoredFile(f *testing.F) {}
`,
		"_ignored/ignored_fuzz_test.go": `package ignored

import "testing"

func FuzzIgnoredDirectory(f *testing.F) {}
`,
	}
	files["inactive_"+inactiveGOOS+"_test.go"] = `package root

import "testing"

func FuzzInactivePlatform(f *testing.F) {}
`

	got, err := DiscoverWorkspace(newWorkspace(t, files))
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Kind: "native", Module: ".", Package: ".", Name: "FuzzActive"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverWorkspace() = %#v, want %#v", got, want)
	}
}

func TestDiscoverWorkspaceRecognizesAliasedAndUnnamedNativeFuzzers(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse .\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"alias_fuzz_test.go": `package root

import check "testing"

type F = check.F

func FuzzAlias(f *check.F) {}
func Fuzz(*check.F) {}
func FuzzDirect(*F) {}
`,
	})

	got, err := DiscoverWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{
		{Kind: "native", Module: ".", Package: ".", Name: "Fuzz"},
		{Kind: "native", Module: ".", Package: ".", Name: "FuzzAlias"},
		{Kind: "native", Module: ".", Package: ".", Name: "FuzzDirect"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverWorkspace() = %#v, want %#v", got, want)
	}
}

func TestDiscoverWorkspaceIgnoresMalformedNativeFuzzers(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse .\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"malformed_fuzz_test.go": `package root

import "testing"

func Fuzzlower(f *testing.F) {}
func FuzzMultiple(first, second *testing.F) {}
func FuzzResult(f *testing.F) error { return nil }
func FuzzValue(f testing.F) {}
func FuzzManyParams(f *testing.F, extra string) {}
`,
	})

	got, err := DiscoverWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("DiscoverWorkspace() = %#v, want no malformed targets", got)
	}
}

func TestDiscoverWorkspaceRejectsUnmarkedRapidTest(t *testing.T) {
	root := newWorkspace(t, map[string]string{
		"go.work": "go 1.25.0\n\nuse .\n",
		"go.mod":  "module example.test/root\n\ngo 1.25.0\n",
		"state_test.go": `package root

import (
	"testing"
	"pgregory.net/rapid"
)

func TestStateful(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {})
}
`,
	})

	_, err := DiscoverWorkspace(root)
	assertErrorContains(t, err, "TestStateful calls rapid.Check without // serf:fuzz rapid marker")
}

func TestEmitPlanSortsCoverageTargetsAndExcludesSupportTests(t *testing.T) {
	var out strings.Builder
	err := EmitPlan(&out, []Target{
		{Kind: "test", Module: ".", Package: ".", Name: "TestSupport"},
		{Kind: "native", Module: "agent", Package: "./core", Name: "FuzzTurn"},
		{Kind: "rapid", Module: "agent", Package: ".", Name: "TestStateful"},
		{Kind: "native", Module: ".", Package: "./appwire", Name: "FuzzWire"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "native\t.\t./appwire\tFuzzWire\n" +
		"rapid\tagent\t.\tTestStateful\n" +
		"native\tagent\t./core\tFuzzTurn\n"
	if got := out.String(); got != want {
		t.Fatalf("EmitPlan() = %q, want %q", got, want)
	}
}

func newWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	return root
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err, want)
	}
}
