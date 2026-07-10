package main

import (
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

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err, want)
	}
}
