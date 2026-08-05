package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseBlock(t *testing.T) {
	b, err := parseBlock("primeradiant.com/serf/appwire/jsonrpc.go:113.45,120.2 3 1")
	if err != nil {
		t.Fatalf("parseBlock: %v", err)
	}
	want := block{file: "primeradiant.com/serf/appwire/jsonrpc.go", start: 113, stmts: 3, count: 1}
	if b != want {
		t.Fatalf("parseBlock = %+v, want %+v", b, want)
	}
}

func TestParseProfileRejectsNonSetMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "count.cov")
	if err := os.WriteFile(p, []byte("mode: count\nx.go:1.1,2.2 1 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseProfile(p); err == nil {
		t.Fatal("expected parseProfile to refuse mode: count")
	}
}

func TestParseFocus(t *testing.T) {
	got := parseFocus("adapter.go#decodeStream ; sse.go")
	want := []focusSpec{{relpath: "adapter.go", fn: "decodeStream"}, {relpath: "sse.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFocus = %+v, want %+v", got, want)
	}
	if parseFocus("") != nil {
		t.Fatal("empty focus must yield nil (whole package)")
	}
}

func TestJoinImportAndPkgSubdir(t *testing.T) {
	cases := []struct{ pkg, sub, imp string }{
		{".", "", "m"},
		{"./appwire", "appwire", "m/appwire"},
		{"./cmd/serf-hub", "cmd/serf-hub", "m/cmd/serf-hub"},
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

func TestWriteFloorsUpwardOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "floors.txt")
	old := map[string]float64{"FuzzA": 80.0, "FuzzB": 50.0}
	results := []result{
		{name: "FuzzA", focusPct: 70.0}, // below existing floor: must NOT lower
		{name: "FuzzB", focusPct: 60.0}, // above: must rise
		{name: "FuzzC", focusPct: 30.0}, // new
	}
	if err := writeFloors(p, results, old); err != nil {
		t.Fatal(err)
	}
	got, err := readFloors(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"FuzzA": 80.0, "FuzzB": 60.0, "FuzzC": 30.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("floors after bless = %v, want %v", got, want)
	}
}

func TestValidateFloorTargetsRejectsOrphan(t *testing.T) {
	targets := []target{
		{name: "FuzzRegistered"},
		{name: "FuzzRegisteredWithoutFloor"},
	}
	if err := validateFloorTargets(map[string]float64{"FuzzRegistered": 42}, targets); err != nil {
		t.Fatalf("registered floor validation: %v", err)
	}

	err := validateFloorTargets(map[string]float64{
		"FuzzRegistered": 42,
		"FuzzOrphan":     99,
	}, targets)
	if err == nil || !strings.Contains(err.Error(), "FuzzOrphan") || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("orphan floor error = %v", err)
	}
}

func TestRunGlobalModeCheckUsesStrictThreshold(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	profile := filepath.Join(repo, "exact.cov")
	mustWrite(t, profile, "mode: set\n"+
		"example.com/m/p.go:1.1,1.2 95 1\n"+
		"example.com/m/p.go:2.1,2.2 5 0\n")
	manifest := filepath.Join(repo, "profiles.tsv")
	mustWrite(t, manifest, ".\t.\t"+profile+"\n")
	exclusions := filepath.Join(repo, "exclusions.tsv")
	mustWrite(t, exclusions, "# intentionally empty\n")

	var stdout, stderr bytes.Buffer
	code, err := runGlobalMode(globalModeOptions{
		manifestPath: manifest, exclusionsPath: exclusions, floorsPath: filepath.Join(repo, "floors.txt"),
		repoRoot: repo, minimum: 95, check: true,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runGlobalMode: %v", err)
	}
	if code != 1 {
		t.Fatalf("strict 95.0000%% run exit = %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "RAW THRESHOLD BREACH") {
		t.Fatalf("missing strict threshold error: %q", stderr.String())
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
		"native:llm:.:FuzzParseSSE::sse.go\n" +
		"native:.:./appwire:FuzzWireTypes::\n" +
		"native:agent:.:FuzzToolArgsValidate:./internal/tool,.:internal/tool/definitions.go\n" +
		"rapid:.:./internal/appserver:TestRouterSeqFuzz\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRegistry(p)
	if err != nil {
		t.Fatalf("readRegistry: %v", err)
	}
	want := []target{
		{tag: "native", module: "llm", pkg: ".", name: "FuzzParseSSE", coverpkg: "", focus: "sse.go"},
		{tag: "native", module: ".", pkg: "./appwire", name: "FuzzWireTypes", coverpkg: "", focus: ""},
		{tag: "native", module: "agent", pkg: ".", name: "FuzzToolArgsValidate", coverpkg: "./internal/tool,.", focus: "internal/tool/definitions.go"},
		{tag: "rapid", module: ".", pkg: "./internal/appserver", name: "TestRouterSeqFuzz", coverpkg: "", focus: ""},
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

// TestComputeTargetFocusAttribution builds a tiny module on disk plus a synthetic
// profile and verifies the focus % is computed over the named function's line
// range only, while the package % spans the whole file.
func TestComputeTargetFocusAttribution(t *testing.T) {
	repo := t.TempDir()
	mod := filepath.Join(repo, "m")
	pkgDir := filepath.Join(mod, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/m\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := "package pkg\n\n" +
		"func Decode(b []byte) int {\n" + // line 3
		"\tif len(b) > 0 {\n" + //          line 4
		"\t\treturn 1\n" + //               line 5
		"\t}\n" + //                        line 6
		"\treturn 0\n" + //                 line 7
		"}\n\n" + //                        line 8
		"func Other() int {\n" + //         line 10
		"\treturn 2\n" + //                 line 11
		"}\n" //                            line 12
	if err := os.WriteFile(filepath.Join(pkgDir, "decode.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	// Profile: two blocks inside Decode (one covered, one not) and one block in
	// Other (covered). Focus = Decode only.
	prof := filepath.Join(repo, "x.cov")
	profContent := "mode: set\n" +
		"example.com/m/pkg/decode.go:3.27,4.18 1 1\n" + // in Decode, covered
		"example.com/m/pkg/decode.go:5.3,5.11 1 0\n" + //  in Decode, NOT covered
		"example.com/m/pkg/decode.go:10.20,11.10 1 1\n" //  in Other, covered
	if err := os.WriteFile(prof, []byte(profContent), 0o600); err != nil {
		t.Fatal(err)
	}

	modulePaths, err := readModulePaths(repo, []string{"m"})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := parseProfile(prof)
	if err != nil {
		t.Fatal(err)
	}
	tgt := target{module: "m", pkg: "./pkg", name: "FuzzDecode", focus: "decode.go#Decode", profile: prof}
	r, err := computeTarget(repo, modulePaths, tgt, blocks, map[string]float64{})
	if err != nil {
		t.Fatal(err)
	}
	// Focus: 1 of 2 statements in Decode = 50%.
	if r.focusPct != 50.0 {
		t.Errorf("focusPct = %.1f, want 50.0", r.focusPct)
	}
	// Package: 2 of 3 statements across decode.go = 66.7%.
	if got := r.pkgPct; got < 66.0 || got > 67.0 {
		t.Errorf("pkgPct = %.1f, want ~66.7", got)
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
