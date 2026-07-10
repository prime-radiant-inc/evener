package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestReadGlobalProfiles(t *testing.T) {
	profiles, err := ReadGlobalProfiles(strings.NewReader("# module\tpackage\tprofile\n\n.\t.\t/tmp/root.cov\nagent\t./sandbox\t/tmp/sandbox.cov\n"))
	if err != nil {
		t.Fatalf("ReadGlobalProfiles: %v", err)
	}
	want := []GlobalProfile{
		{Module: ".", Package: ".", Path: "/tmp/root.cov"},
		{Module: "agent", Package: "./sandbox", Path: "/tmp/sandbox.cov"},
	}
	if !reflect.DeepEqual(profiles, want) {
		t.Fatalf("ReadGlobalProfiles = %#v, want %#v", profiles, want)
	}

	for _, manifest := range []string{
		"agent\t./sandbox\n",
		"agent\t./sandbox\t/tmp/a.cov\textra\n",
		"agent\t\t/tmp/a.cov\n",
		"agent\t./sandbox\t/tmp/a.cov\nagent\t./sandbox\t/tmp/a.cov\n",
	} {
		if _, err := ReadGlobalProfiles(strings.NewReader(manifest)); err == nil {
			t.Fatalf("ReadGlobalProfiles(%q) unexpectedly succeeded", manifest)
		}
	}
}

func TestReportGlobalUnionsExactPackageBlocks(t *testing.T) {
	dir := t.TempDir()
	rootA := writeGlobalProfile(t, dir, "root-a.cov", "mode: set\n"+
		"example.com/root/root.go:1.1,1.2 2 0\n"+
		"example.com/root/root.go:3.1,3.2 1 1\n")
	rootB := writeGlobalProfile(t, dir, "root-b.cov", "mode: set\n"+
		// Same exact block: union must retain it once and mark it covered.
		"example.com/root/root.go:1.1,1.2 2 1\n"+
		// Same start line but a different exact source range: it is a distinct block.
		"example.com/root/root.go:3.1,4.2 1 0\n")
	agent := writeGlobalProfile(t, dir, "agent.cov", "mode: set\n"+
		"example.com/root/agent/sandbox/policy.go:1.1,2.2 4 1\n")

	report, err := ReportGlobal([]GlobalProfile{
		resolvedGlobalProfile("agent", "example.com/root/agent", "./sandbox", agent),
		resolvedGlobalProfile(".", "example.com/root", ".", rootA),
		resolvedGlobalProfile(".", "example.com/root", ".", rootB),
	}, nil, 95)
	if err != nil {
		t.Fatalf("ReportGlobal: %v", err)
	}
	if len(report.Modules) != 2 {
		t.Fatalf("module count = %d, want 2", len(report.Modules))
	}
	if got := report.Modules[0]; got.Module != "." || got.Covered != 3 || got.Total != 4 {
		t.Fatalf("root report = %#v, want 3/4", got)
	}
	if got := report.Modules[1]; got.Module != "agent" || got.Covered != 4 || got.Total != 4 {
		t.Fatalf("agent report = %#v, want 4/4", got)
	}
	if report.RawPass {
		t.Fatal("a module below the raw threshold must make RawPass false")
	}
}

func TestReportGlobalRequiresStrictRawThreshold(t *testing.T) {
	dir := t.TempDir()
	exact := writeGlobalProfile(t, dir, "exact.cov", "mode: set\n"+
		"example.com/m/p.go:1.1,1.2 95 1\n"+
		"example.com/m/p.go:2.1,2.2 5 0\n")
	report, err := ReportGlobal([]GlobalProfile{resolvedGlobalProfile(".", "example.com/m", ".", exact)}, nil, 95)
	if err != nil {
		t.Fatal(err)
	}
	if report.Modules[0].Pass {
		t.Fatal("95.0000% must not satisfy >95.0%")
	}
	if report.RawPass {
		t.Fatal("95.0000% must not satisfy the raw global threshold")
	}

	above := writeGlobalProfile(t, dir, "above.cov", "mode: set\n"+
		"example.com/m/p.go:1.1,1.2 950001 1\n"+
		"example.com/m/p.go:2.1,2.2 49999 0\n")
	report, err = ReportGlobal([]GlobalProfile{resolvedGlobalProfile(".", "example.com/m", ".", above)}, nil, 95)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Modules[0].Pass || !report.RawPass {
		t.Fatalf("95.0001%% must satisfy >95.0%%: %#v", report)
	}
}

func TestGlobalFloorsNeverLowerAndNeverBlessFailure(t *testing.T) {
	old := map[string]globalFloor{".": mustGlobalFloor(t, "96"), "agent": mustGlobalFloor(t, "54.2")}
	report := GlobalReport{Modules: []ModuleReport{
		{Module: ".", Covered: 955, Total: 1000, Pass: true},
		{Module: "agent", Covered: 950, Total: 1000, Pass: false},
	}}
	got := RaiseGlobalFloors(old, report)
	want := map[string]globalFloor{".": mustGlobalFloor(t, "96"), "agent": mustGlobalFloor(t, "54.2")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RaiseGlobalFloors = %v, want %v", got, want)
	}

	report.Modules[0] = ModuleReport{Module: ".", Covered: 970, Total: 1000, Pass: true}
	got = RaiseGlobalFloors(old, report)
	if got["."].ratio.Cmp(mustGlobalFloor(t, "97").ratio) != 0 {
		t.Fatalf("raised root floor = %v, want 97", got["."])
	}
	if got["agent"].ratio.Cmp(mustGlobalFloor(t, "54.2").ratio) != 0 {
		t.Fatalf("failed module floor = %v, want preserved 54.2", got["agent"])
	}
}

func TestGlobalBlessRoundTripDoesNotRegressExactRatio(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	profile := writeGlobalProfile(t, repo, "pass.cov", "mode: set\n"+
		"example.com/m/p.go:1.1,1.2 96 1\n"+
		"example.com/m/p.go:2.1,2.2 5 0\n")
	manifest := filepath.Join(repo, "profiles.tsv")
	mustWrite(t, manifest, ".\t.\t"+profile+"\n")
	exclusions := filepath.Join(repo, "exclusions.tsv")
	mustWrite(t, exclusions, "# intentionally empty\n")
	floors := filepath.Join(repo, "floors.txt")

	var stdout, stderr bytes.Buffer
	options := globalModeOptions{
		manifestPath: manifest, exclusionsPath: exclusions, floorsPath: floors, repoRoot: repo,
		minimum: 95,
	}
	options.bless = true
	code, err := runGlobalMode(options, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("initial bless = code %d, err %v, stdout %q, stderr %q", code, err, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	options.bless = false
	options.check = true
	code, err = runGlobalMode(options, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("identical recheck regressed exact 96/101 coverage: code %d, err %v, stdout %q, stderr %q", code, err, stdout.String(), stderr.String())
	}
}

func TestReportGlobalRejectsBlocksOutsideResolvedProfileOwnership(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.com/root\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(repo, "agent", "go.mod"), "module example.com/root/agent\n\ngo 1.25\n")

	wrongModule := writeGlobalProfile(t, repo, "wrong-module.cov", "mode: set\n"+
		"example.com/root/root.go:1.1,1.2 1 1\n")
	if _, err := ReportGlobal([]GlobalProfile{{
		Module: "agent", Package: "./sandbox", Path: wrongModule,
	}}, nil, 95); err == nil || !strings.Contains(err.Error(), "unresolved module ownership") {
		t.Fatalf("unresolved profile error = %v, want ownership resolution rejection", err)
	}
	profiles, err := ResolveGlobalProfiles(repo, []GlobalProfile{{
		Module: "agent", Package: "./sandbox", Path: wrongModule,
	}})
	if err != nil {
		t.Fatalf("ResolveGlobalProfiles: %v", err)
	}
	if _, err := ReportGlobal(profiles, nil, 95); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("wrong-module block error = %v, want ownership rejection", err)
	}

	wrongPackage := writeGlobalProfile(t, repo, "wrong-package.cov", "mode: set\n"+
		"example.com/root/agent/other.go:1.1,1.2 1 1\n")
	profiles, err = ResolveGlobalProfiles(repo, []GlobalProfile{{
		Module: "agent", Package: "./sandbox", Path: wrongPackage,
	}})
	if err != nil {
		t.Fatalf("ResolveGlobalProfiles: %v", err)
	}
	if _, err := ReportGlobal(profiles, nil, 95); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("wrong-package block error = %v, want ownership rejection", err)
	}

	sharedA := writeGlobalProfile(t, repo, "shared-a.cov", "mode: set\n"+
		"example.com/root/agent/sandbox/policy.go:1.1,1.2 1 1\n")
	sharedB := writeGlobalProfile(t, repo, "shared-b.cov", "mode: set\n"+
		"example.com/root/agent/sandbox/policy.go:1.1,1.2 1 1\n")
	if _, err := ReportGlobal([]GlobalProfile{
		{Module: "agent", ModulePath: "example.com/root/agent", Package: "./sandbox", Path: sharedA},
		{Module: "agent-sandbox", ModulePath: "example.com/root/agent/sandbox", Package: ".", Path: sharedB},
	}, nil, 95); err == nil || !strings.Contains(err.Error(), "assigned to multiple") {
		t.Fatalf("duplicate source ownership error = %v, want duplicate ownership rejection", err)
	}
}

func TestGlobalBlessDoesNotWaiveOrLowerAFloor(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	profile := writeGlobalProfile(t, dir, "pass.cov", "mode: set\n"+
		"example.com/m/p.go:1.1,1.2 96 1\n"+
		"example.com/m/p.go:2.1,2.2 4 0\n")
	manifest := filepath.Join(dir, "profiles.tsv")
	mustWrite(t, manifest, ".\t.\t"+profile+"\n")
	exclusions := filepath.Join(dir, "exclusions.tsv")
	mustWrite(t, exclusions, "# intentionally empty\n")
	floors := filepath.Join(dir, "floors.txt")
	mustWrite(t, floors, ". 97\n")

	var stdout, stderr bytes.Buffer
	code, err := runGlobalMode(globalModeOptions{
		manifestPath: manifest, exclusionsPath: exclusions, floorsPath: floors, repoRoot: dir,
		minimum: 95, bless: true,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(stderr.String(), "REGRESSION") {
		t.Fatalf("bless must not waive a floor regression: code=%d stderr=%q", code, stderr.String())
	}
	f, err := os.Open(floors)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := ReadGlobalFloors(f)
	if err != nil {
		t.Fatal(err)
	}
	if got["."].ratio.Cmp(mustGlobalFloor(t, "97").ratio) != 0 {
		t.Fatalf("bless lowered floor to %v, want 97", got["."])
	}
}

func TestGlobalJSONRemainsValidWhenBlessing(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	profile := writeGlobalProfile(t, dir, "pass.cov", "mode: set\n"+
		"example.com/m/p.go:1.1,1.2 96 1\n"+
		"example.com/m/p.go:2.1,2.2 4 0\n")
	manifest := filepath.Join(dir, "profiles.tsv")
	mustWrite(t, manifest, ".\t.\t"+profile+"\n")
	exclusions := filepath.Join(dir, "exclusions.tsv")
	mustWrite(t, exclusions, "# intentionally empty\n")

	var stdout, stderr bytes.Buffer
	code, err := runGlobalMode(globalModeOptions{
		manifestPath: manifest, exclusionsPath: exclusions, floorsPath: filepath.Join(dir, "floors.txt"),
		repoRoot: dir, minimum: 95, bless: true, json: true,
	}, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("JSON bless = code %d, err %v, stderr %q", code, err, stderr.String())
	}
	var report GlobalReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("JSON output was polluted by blessing text: %v\n%s", err, stdout.String())
	}
	if !report.RawPass || !strings.Contains(stderr.String(), "raised global floors") {
		t.Fatalf("unexpected JSON bless result: report=%#v stderr=%q", report, stderr.String())
	}
}

func TestGeneratedExclusionRequiresHeaderAndRemovesProfileBlocks(t *testing.T) {
	repo, profile := globalExclusionFixture(t, "generated.go", "// Code generated by global_test. DO NOT EDIT.\n\npackage pkg\n\nfunc Generated() {}\n")
	exclusions, err := ReadGlobalExclusions(repo, strings.NewReader("m\t./pkg\tgenerated.go\tgenerated\tfixture generated source\n"))
	if err != nil {
		t.Fatalf("ReadGlobalExclusions: %v", err)
	}
	report, err := ReportGlobal([]GlobalProfile{resolvedGlobalProfile("m", "example.com/m", "./pkg", profile)}, exclusions, 95)
	if err != nil {
		t.Fatalf("ReportGlobal: %v", err)
	}
	if got := report.Modules[0]; got.Covered != 1 || got.Total != 1 {
		t.Fatalf("excluded report = %#v, want ordinary block only", got)
	}
	if len(report.AppliedExclusions) != 1 || report.AppliedExclusions[0].Statements != 3 {
		t.Fatalf("applied exclusions = %#v, want generated.go with 3 statements", report.AppliedExclusions)
	}

	ordinaryRepo, _ := globalExclusionFixture(t, "ordinary.go", "package pkg\n\nfunc Ordinary() {}\n")
	if _, err := ReadGlobalExclusions(ordinaryRepo, strings.NewReader("m\t./pkg\tordinary.go\tgenerated\tnot generated\n")); err == nil {
		t.Fatal("ordinary production source must not be accepted as generated")
	}

	ordinarySource := filepath.Join(ordinaryRepo, "m", "pkg", "ordinary.go")
	if _, err := ReportGlobal([]GlobalProfile{resolvedGlobalProfile("m", "example.com/m", "./pkg", profile)}, []Exclusion{{
		Module: "m", Package: "./pkg", File: "ordinary.go", Kind: "generated", Reason: "forged direct exclusion",
		SourcePath: ordinarySource, ProfileFile: "example.com/m/pkg/ordinary.go",
	}}, 95); err == nil {
		t.Fatal("ReportGlobal must reject a hand-built ordinary-source exclusion")
	}
}

func TestPlatformExclusionMustBeUnavailableAndExclusionsRejectInvalidRows(t *testing.T) {
	platformSource := "//go:build !" + runtime.GOOS + "\n\npackage pkg\n\nfunc PlatformOnly() {}\n"
	repo, profile := globalExclusionFixture(t, "platform.go", platformSource)
	exclusions, err := ReadGlobalExclusions(repo, strings.NewReader("m\t./pkg\tplatform.go\tplatform\tnot built on this platform\n"))
	if err != nil {
		t.Fatalf("unavailable platform source must be accepted: %v", err)
	}
	if _, err := ReportGlobal([]GlobalProfile{resolvedGlobalProfile("m", "example.com/m", "./pkg", profile)}, exclusions, 95); err != nil {
		t.Fatalf("platform exclusion should apply to its profile block: %v", err)
	}

	available := "//go:build " + runtime.GOOS + "\n\npackage pkg\n\nfunc Available() {}\n"
	availableRepo, _ := globalExclusionFixture(t, "available.go", available)
	if _, err := ReadGlobalExclusions(availableRepo, strings.NewReader("m\t./pkg\tavailable.go\tplatform\tavailable source\n")); err == nil {
		t.Fatal("available platform source must not be excluded")
	}

	if _, err := ReadGlobalExclusions(repo, strings.NewReader(
		"m\t./pkg\tplatform.go\tplatform\tfirst\n"+
			"m\t./pkg\tplatform.go\tplatform\tduplicate\n")); err == nil {
		t.Fatal("duplicate exclusions must fail")
	}
	if _, err := ReadGlobalExclusions(repo, strings.NewReader("m\t./pkg\tmissing.go\tgenerated\tmissing source\n")); err == nil {
		t.Fatal("missing exclusion source must fail")
	}

	generatedRepo, _ := globalExclusionFixture(t, "generated.go", "// Code generated by global_test. DO NOT EDIT.\n\npackage pkg\n")
	generated, err := ReadGlobalExclusions(generatedRepo, strings.NewReader("m\t./pkg\tgenerated.go\tgenerated\tno matching block\n"))
	if err != nil {
		t.Fatal(err)
	}
	wrongProfile := writeGlobalProfile(t, t.TempDir(), "wrong.cov", "mode: set\nexample.com/m/pkg/other.go:1.1,1.2 1 1\n")
	if _, err := ReportGlobal([]GlobalProfile{resolvedGlobalProfile("m", "example.com/m", "./pkg", wrongProfile)}, generated, 95); err == nil {
		t.Fatal("exclusion that removes zero profile blocks must fail")
	}
}

func TestGlobalReportPrintsAppliedExclusionsInTextAndJSON(t *testing.T) {
	repo, profile := globalExclusionFixture(t, "generated.go", "// Code generated by global_test. DO NOT EDIT.\n\npackage pkg\n\nfunc Generated() {}\n")
	exclusions, err := ReadGlobalExclusions(repo, strings.NewReader("m\t./pkg\tgenerated.go\tgenerated\tfixture generated source\n"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := ReportGlobal([]GlobalProfile{resolvedGlobalProfile("m", "example.com/m", "./pkg", profile)}, exclusions, 95)
	if err != nil {
		t.Fatal(err)
	}

	var text bytes.Buffer
	PrintGlobalReport(&text, report)
	if !strings.Contains(text.String(), "generated.go") || !strings.Contains(text.String(), "APPLIED EXCLUSIONS") {
		t.Fatalf("text report does not name the applied exclusion: %q", text.String())
	}

	var jsonReport bytes.Buffer
	if err := WriteGlobalReportJSON(&jsonReport, report); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(jsonReport.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON report: %v\n%s", err, jsonReport.String())
	}
	applied, ok := decoded["applied_exclusions"].([]any)
	if !ok || len(applied) != 1 {
		t.Fatalf("JSON applied_exclusions = %#v, want one entry", decoded["applied_exclusions"])
	}
}

func writeGlobalProfile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func resolvedGlobalProfile(module, modulePath, pkg, profilePath string) GlobalProfile {
	return GlobalProfile{Module: module, ModulePath: modulePath, Package: pkg, Path: profilePath}
}

func mustGlobalFloor(t *testing.T, value string) globalFloor {
	t.Helper()
	floor, err := parseGlobalFloor(value)
	if err != nil {
		t.Fatalf("parseGlobalFloor(%q): %v", value, err)
	}
	return floor
}

func globalExclusionFixture(t *testing.T, filename, source string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	moduleDir := filepath.Join(repo, "m")
	pkgDir := filepath.Join(moduleDir, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(moduleDir, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(pkgDir, filename), source)
	mustWrite(t, filepath.Join(pkgDir, "ordinary.go"), "package pkg\n\nfunc Ordinary() {}\n")

	profile := writeGlobalProfile(t, repo, "profile.cov", "mode: set\n"+
		"example.com/m/pkg/"+filename+":1.1,1.2 3 1\n"+
		"example.com/m/pkg/ordinary.go:1.1,1.2 1 1\n")
	return repo, profile
}
