package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestJoinImportAndPkgSubdir(t *testing.T) {
	cases := []struct{ pkg, sub, imp string }{
		{".", "", "m"},
		{"./appwire", "appwire", "m/appwire"},
		{"./cmd/evener-hub", "cmd/evener-hub", "m/cmd/evener-hub"},
	}
	for _, c := range cases {
		if got := pkgSubdir(c.pkg); got != c.sub {
			t.Errorf("pkgSubdir(%q) = %q, want %q", c.pkg, got, c.sub)
		}
		if got := joinImport("m", pkgSubdir(c.pkg)); got != c.imp {
			t.Errorf("joinImport for %q = %q, want %q", c.pkg, got, c.imp)
		}
	}
}

func TestReadIgnoreRequiresReason(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("# header\nexample.com/x  # out of scope: dev tool\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := readIgnore(good)
	if err != nil {
		t.Fatalf("readIgnore(good): %v", err)
	}
	if !m["example.com/x"] {
		t.Fatal("expected example.com/x in ignore set")
	}

	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("example.com/y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIgnore(bad); err == nil {
		t.Fatal("expected readIgnore to reject a reasonless entry")
	}
}

func TestGapMap(t *testing.T) {
	universe := map[string]string{
		"m/a": "json.Unmarshal",
		"m/b": "func Parse",
		"m/c": "toml.Decode",
	}
	fuzzed := map[string]bool{"m/a": true}
	ignore := map[string]bool{"m/c": true}
	got := gapMap(universe, fuzzed, ignore)
	if len(got) != 1 || got[0][0] != "m/b" {
		t.Fatalf("gapMap = %v, want only m/b", got)
	}
}

func TestReadRegistry(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "registry.txt")
	content := "" +
		"# a comment\n" +
		"\n" +
		"native:llm:.:FuzzParseSSE\n" +
		"native:.:./appwire:FuzzWireTypes\n" +
		"native:agent:.:FuzzToolArgsValidate:./internal/tool,.\n" +
		"rapid:.:./internal/appserver:TestRouterSeqFuzz\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRegistry(p)
	if err != nil {
		t.Fatalf("readRegistry: %v", err)
	}
	want := []target{
		{tag: "native", module: "llm", pkg: ".", name: "FuzzParseSSE", coverpkg: ""},
		{tag: "native", module: ".", pkg: "./appwire", name: "FuzzWireTypes", coverpkg: ""},
		{tag: "native", module: "agent", pkg: ".", name: "FuzzToolArgsValidate", coverpkg: "./internal/tool,."},
		{tag: "rapid", module: ".", pkg: "./internal/appserver", name: "TestRouterSeqFuzz", coverpkg: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readRegistry =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadRegistryRejectsShortLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "registry.txt")
	if err := os.WriteFile(p, []byte("native:llm:.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegistry(p); err == nil {
		t.Fatal("expected readRegistry to reject a line with fewer than 4 fields")
	}
}

func TestStaticFuzzedPackages(t *testing.T) {
	modulePaths := map[string]string{".": "m", "agent": "m/agent", "llm": "m/llm"}
	targets := []target{
		{tag: "native", module: "llm", pkg: ".", name: "FuzzParseSSE"},
		{tag: "native", module: ".", pkg: "./appwire", name: "FuzzWireTypes"},
		// coverpkg overrides pkg, and is comma-split into multiple packages.
		{tag: "native", module: "agent", pkg: ".", name: "FuzzToolArgsValidate", coverpkg: "./internal/tool,."},
		{tag: "rapid", module: ".", pkg: "./internal/appserver", name: "TestRouterSeqFuzz"},
	}
	got := staticFuzzedPackages(targets, modulePaths)
	want := map[string]bool{
		"m/llm":                 true,
		"m/appwire":             true,
		"m/agent/internal/tool": true,
		"m/agent":               true, // the trailing ",." in the coverpkg
		"m/internal/appserver":  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staticFuzzedPackages =\n%v\nwant\n%v", got, want)
	}
}

func TestGoWorkModules(t *testing.T) {
	repo := t.TempDir()
	work := "go 1.25.6\n" +
		"\n" +
		"use (\n" +
		"\t.\n" +
		"\t./agent\n" +
		"\t./invariant\n" +
		")\n" +
		"\n" +
		"replace example.com/agent v0.0.0 => ./agent\n"
	if err := os.WriteFile(filepath.Join(repo, "go.work"), []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}
	mods, err := goWorkModules(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "agent", "invariant"}
	if !slices.Equal(mods, want) {
		t.Errorf("goWorkModules = %v, want %v", mods, want)
	}
}

func TestGoWorkModulesSingleLineUse(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.work"), []byte("go 1.25.6\nuse ./llm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mods, err := goWorkModules(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mods, []string{"llm"}) {
		t.Errorf("goWorkModules = %v, want [llm]", mods)
	}
}
