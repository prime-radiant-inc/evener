//go:build serffuzz

package main

import (
	"errors"
	"go/build/constraint"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	exitProcess = func(int) {}
	host := fuzzcovSystem
	fuzzcovSystem = fuzzcovSystemOps{
		open: func(name string) (*os.File, error) {
			if strings.Contains(name, "fuzzcov-open-error") {
				return nil, errors.New("open failed")
			}
			return host.open(name)
		},
		abs: func(name string) (string, error) {
			if strings.Contains(name, "fuzzcov-abs-error") {
				return "", errors.New("abs failed")
			}
			return host.abs(name)
		},
		rel: func(base, target string) (string, error) {
			if strings.Contains(base, "fuzzcov-rel-error") || strings.Contains(target, "fuzzcov-rel-error") {
				return "", errors.New("rel failed")
			}
			return host.rel(base, target)
		},
		readFile: func(name string) ([]byte, error) {
			if strings.Contains(name, "fuzzcov-read-error") {
				return nil, errors.New("read failed")
			}
			return host.readFile(name)
		},
		stat: func(name string) (os.FileInfo, error) {
			if strings.Contains(name, "fuzzcov-stat-error") {
				return nil, errors.New("stat failed")
			}
			return host.stat(name)
		},
		writeFile: func(name string, content []byte, mode os.FileMode) error {
			if strings.Contains(name, "fuzzcov-write-error") {
				return errors.New("write failed")
			}
			return host.writeFile(name, content, mode)
		},
	}
}

type fuzzcovErrorReader struct {
	data string
	err  error
}

type fuzzcovErrorWriter struct{}

func (fuzzcovErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (r *fuzzcovErrorReader) Read(p []byte) (int, error) {
	if r.data != "" {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func fuzzcovWantError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
}

func FuzzCoveragePolicyEdges(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2, 3} {
		f.Add(seed, "mutant")
	}
	f.Fuzz(func(t *testing.T, selector uint8, mutant string) {
		switch selector % 4 {
		case 0:
			fuzzCoveragePureEdges(t, mutant)
		case 1:
			fuzzCoverageReaderEdges(t)
		case 2:
			fuzzCoverageFilesystemEdges(t)
		case 3:
			fuzzCoverageAccountingEdges(t)
		}
	})
}

func FuzzCoverageCLI(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2, 3, 4, 5, 6, 7} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		dir := t.TempDir()
		stdout, err := os.CreateTemp(dir, "stdout")
		if err != nil {
			t.Fatal(err)
		}
		defer stdout.Close()
		stderr, err := os.CreateTemp(dir, "stderr")
		if err != nil {
			t.Fatal(err)
		}
		defer stderr.Close()

		switch selector % 8 {
		case 0:
			_, err = runCLI([]string{"-not-a-flag"}, stdout, stderr)
			fuzzcovWantError(t, err)
		case 1:
			_, err = runCLI(nil, stdout, stderr)
			fuzzcovWantError(t, err)
		case 2:
			_, err = runCLI([]string{"-global-json"}, stdout, stderr)
			fuzzcovWantError(t, err)
		case 3:
			_, err = runCLI([]string{"-global-manifest=x", "-manifest=y"}, stdout, stderr)
			fuzzcovWantError(t, err)
		case 4:
			_, err = runCLI([]string{"-manifest=" + filepath.Join(dir, "missing")}, stdout, stderr)
			fuzzcovWantError(t, err)
		case 5:
			_, err = runCLI([]string{"-global-manifest=" + filepath.Join(dir, "missing")}, stdout, stderr)
			fuzzcovWantError(t, err)
		case 6:
			fuzzCoverageRunCLIFixture(t, dir, stdout, stderr)
		case 7:
			main()
			fatal("seed boundary")
		}
	})
}

func fuzzCoverageRunCLIFixture(t *testing.T, dir string, stdout, stderr *os.File) {
	goMod := filepath.Join(dir, "go.mod")
	source := filepath.Join(dir, "parse.go")
	profile := filepath.Join(dir, "coverage.out")
	manifest := filepath.Join(dir, "manifest.tsv")
	floors := filepath.Join(dir, "floors.txt")
	ignore := filepath.Join(dir, "ignore.txt")
	for name, content := range map[string]string{
		goMod:    "module example.test/mod\n",
		source:   "package mod\nfunc ParseThing() {}\n",
		profile:  "mode: set\nexample.test/mod/parse.go:2.1,2.21 1 1\n",
		manifest: ".\t.\tFuzzThing\t.\t\t" + profile + "\n",
		floors:   "FuzzThing 50\n",
		ignore:   "",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-manifest=" + manifest, "-repo-root=" + dir, "-modules=.", "-floors=" + floors, "-ignore=" + ignore, "-check", "-bless"}
	if code, err := runCLI(args, stdout, stderr); err != nil || code != 0 {
		t.Fatalf("runCLI = %d, %v", code, err)
	}
	if code, err := runCLI(args[:len(args)-2], stdout, stderr); err != nil || code != 0 {
		t.Fatalf("advisory runCLI = %d, %v", code, err)
	}
	readErrorSource := filepath.Join(dir, "fuzzcov-read-error.go")
	if err := os.WriteFile(readErrorSource, []byte("package mod\nfunc ParseBad() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(args[:len(args)-2], stdout, stderr)
	fuzzcovWantError(t, err)
	if err := os.Remove(readErrorSource); err != nil {
		t.Fatal(err)
	}
	blessWriteArgs := []string{"-manifest=" + manifest, "-repo-root=" + dir, "-modules=.", "-floors=" + filepath.Join(dir, "fuzzcov-write-error"), "-ignore=" + ignore, "-bless"}
	_, err = runCLI(blessWriteArgs, stdout, stderr)
	fuzzcovWantError(t, err)
	profileZero := filepath.Join(dir, "coverage-zero.out")
	if err := os.WriteFile(profileZero, []byte("mode: set\nexample.test/mod/parse.go:2.1,2.21 1 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unionManifest := filepath.Join(dir, "union-manifest")
	unionFloors := filepath.Join(dir, "union-floors")
	unionText := ".\t.\tFuzzZero\t.\t\t" + profileZero + "\n.\t.\tFuzzOne\t.\t\t" + profile + "\n"
	if err := os.WriteFile(unionManifest, []byte(unionText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unionFloors, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, err := runCLI([]string{"-manifest=" + unionManifest, "-repo-root=" + dir, "-modules=.", "-floors=" + unionFloors, "-ignore=" + ignore}, stdout, stderr); err != nil || code != 0 {
		t.Fatalf("union runCLI = %d, %v", code, err)
	}
	for _, badArgs := range [][]string{
		{"-manifest=" + manifest, "-repo-root=" + filepath.Join(dir, "missing"), "-modules=.", "-floors=" + floors, "-ignore=" + ignore},
		{"-manifest=" + manifest, "-repo-root=" + dir, "-modules=.", "-floors=\x00", "-ignore=" + ignore},
		{"-manifest=" + manifest, "-repo-root=" + dir, "-modules=.", "-floors=" + floors, "-ignore=\x00"},
	} {
		_, err := runCLI(badArgs, stdout, stderr)
		fuzzcovWantError(t, err)
	}
	badManifest := filepath.Join(dir, "bad-manifest")
	if err := os.WriteFile(badManifest, []byte(".\t.\tBad\t.\t\tmissing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = runCLI([]string{"-manifest=" + badManifest, "-repo-root=" + dir, "-modules=.", "-floors=" + floors, "-ignore=" + ignore}, stdout, stderr)
	fuzzcovWantError(t, err)
	focusManifest := filepath.Join(dir, "focus-manifest")
	if err := os.WriteFile(focusManifest, []byte(".\t.\tBadFocus\t.\tparse.go#Missing\t"+profile+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = runCLI([]string{"-manifest=" + focusManifest, "-repo-root=" + dir, "-modules=.", "-floors=" + floors, "-ignore=" + ignore}, stdout, stderr)
	fuzzcovWantError(t, err)
	if _, err := computeTarget(dir, map[string]string{".": "example.test/mod"}, target{module: ".", pkg: ".", focus: "parse.go"}, []block{{file: "other/x.go"}, {file: "example.test/mod/parse.go", start: 1, stmts: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(dir, "registry")
	if err := os.WriteFile(registry, []byte("native:.:.:FuzzThing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, err := runCLI([]string{"-gap-only", "-registry=" + registry, "-repo-root=" + dir, "-modules=.", "-ignore=" + ignore}, stdout, stderr); err != nil || code != 0 {
		t.Fatalf("gap runCLI = %d, %v", code, err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := runGapOnlyE(registry, filepath.Join(dir, "missing"), []string{"."}, ignore)
			return err
		},
		func() error { _, err := runGapOnlyE(registry, dir, []string{"."}, "\x00"); return err },
	} {
		fuzzcovWantError(t, call())
	}

	globalManifest := filepath.Join(dir, "global-manifest")
	globalExclusions := filepath.Join(dir, "global-exclusions")
	globalFloors := filepath.Join(dir, "global-floors")
	for name, content := range map[string]string{
		globalManifest:   ".\t.\t" + profile + "\n",
		globalExclusions: "",
		globalFloors:     "",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, err := runCLI([]string{"-global-manifest=" + globalManifest, "-global-exclusions=" + globalExclusions, "-global-floors=" + globalFloors, "-repo-root=" + dir}, stdout, stderr); err != nil || code != 0 {
		t.Fatalf("global runCLI = %d, %v", code, err)
	}
}

func fuzzCoveragePureEdges(t *testing.T, mutant string) {
	for _, input := range []string{"", "../x", "/x", "a/../../x"} {
		_, err := cleanGlobalModule(input)
		fuzzcovWantError(t, err)
	}
	for _, input := range []string{"", "/x", `x\\y`, "x/../y", ".."} {
		_, err := cleanGlobalModulePath(input)
		fuzzcovWantError(t, err)
	}
	for _, input := range []string{"", "x", "./..", "./../x", "./"} {
		_, err := cleanGlobalPackage(input)
		fuzzcovWantError(t, err)
	}
	for _, input := range []string{"", "a/b.go", `a\\b.go`, "x.txt", "x_test.go"} {
		fuzzcovWantError(t, validateExclusionFilename(input))
	}
	for _, minimum := range []float64{math.NaN(), math.Inf(1), -1, 100} {
		fuzzcovWantError(t, validateGlobalMinimum(minimum))
	}
	fuzzcovWantError(t, validateGlobalModeMinimum(94.9))
	if globalStrictlyExceeds(1, 0, 95) || globalStrictlyExceeds(1, 1, math.NaN()) {
		t.Fatal("invalid strict comparison passed")
	}
	if globalMeetsFloor(1, 0, globalFloor{}) || globalMeetsFloor(1, 1, globalFloor{}) {
		t.Fatal("invalid floor comparison passed")
	}
	if globalPercent(1, 0) != 0 || globalFloorPercent(globalFloor{}) != 0 {
		t.Fatal("zero denominator changed")
	}
	if globalFloorText(globalFloor{}) != "0" || globalFloorText(globalFloorFromCoverage(1, 2)) != "1/2" {
		t.Fatal("unexpected floor text")
	}
	if globalFloorText(globalFloor{ratio: globalFloorFromCoverage(1, 3).ratio}) != "1/3" {
		t.Fatal("ratio-only floor text changed")
	}
	fuzzcovWantError(t, validateGlobalModeMinimum(100))
	for _, value := range []string{"-1/2", "2/1", "-1", "101", mutant + "?"} {
		_, err := parseGlobalFloor(value)
		fuzzcovWantError(t, err)
	}
	for _, value := range []string{"", "1", ".1", "1.", "x.1", "1.x", "0.1", "1.0"} {
		_, _, err := parseGlobalPosition(value)
		fuzzcovWantError(t, err)
	}
	for _, line := range []string{
		"x", "x 1 0", "x.go:1.1 1 0", "x.go:a.1,2.2 1 0", "x.go:1.1,b.2 1 0",
		"x.go:2.2,1.1 1 0", "x.go:1.1,2.2 x 0", "x.go:1.1,2.2 0 0", "x.go:1.1,2.2 1 x", "x.go:1.1,2.2 1 -1",
	} {
		_, err := parseGlobalBlock(line)
		fuzzcovWantError(t, err)
	}
	for _, file := range []string{".linux.go", "_linux.go", "plain.go", "x_.go"} {
		_ = hasGlobalPlatformFilenameSuffix(file)
	}
	if !isGlobalPlatformBuildTag("linux") || isGlobalPlatformBuildTag("feature") {
		t.Fatal("platform tag classification changed")
	}
	expr, err := constraint.Parse("//go:build linux && (!amd64 || arm64)")
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]bool{}
	collectBuildConstraintTags(expr, tags)
	if len(tags) != 3 {
		t.Fatalf("collected tags = %v", tags)
	}
	if _, err := joinWithin(t.TempDir(), "../escape"); err == nil {
		t.Fatal("escaping path accepted")
	}
	_ = staticFuzzedPackages([]target{{module: "missing"}, {module: ".", coverpkg: " , "}}, map[string]string{".": "m"})
	if _, err := computeTarget(".", nil, target{module: "missing"}, nil, nil); err == nil {
		t.Fatal("missing module accepted")
	}
	if pctForPackage(nil, "m") != 0 {
		t.Fatal("empty package profile was nonzero")
	}
	_ = parseFocus(" ; x.go ; ")
	_ = gapMap(map[string]string{"z": "z", "a": "a"}, nil, nil)
}

func fuzzCoverageReaderEdges(t *testing.T) {
	reader := func(prefix string) io.Reader {
		return &fuzzcovErrorReader{data: prefix, err: errors.New("read failed")}
	}
	_, err := ReadGlobalProfiles(reader(".\t.\tp\n"))
	fuzzcovWantError(t, err)
	_, err = ReadGlobalExclusions(t.TempDir(), reader(""))
	fuzzcovWantError(t, err)
	_, err = ReadGlobalFloors(reader(". 95\n"))
	fuzzcovWantError(t, err)

	for _, manifest := range []string{
		".\t.\t\n", "../x\t.\tp\n", ".\tx\tp\n", ".\t.\tp\n.\t.\tp\n", "\n# comment\n",
	} {
		_, err := ReadGlobalProfiles(strings.NewReader(manifest))
		fuzzcovWantError(t, err)
	}
	for _, floors := range []string{"x\n", "../x 95\n", ". 95\n. 96\n", ". nope\n"} {
		_, err := ReadGlobalFloors(strings.NewReader(floors))
		fuzzcovWantError(t, err)
	}
	for _, exclusions := range []string{
		"x\n", "../x\t.\tx.go\tgenerated\tr\n", ".\tx\tx.go\tgenerated\tr\n",
		".\t.\tx.txt\tgenerated\tr\n", ".\t.\tx.go\tbad\tr\n", ".\t.\tx.go\tgenerated\t\n",
	} {
		_, err := ReadGlobalExclusions(t.TempDir(), strings.NewReader(exclusions))
		fuzzcovWantError(t, err)
	}
}

func fuzzCoverageFilesystemEdges(t *testing.T) {
	dir := t.TempDir()
	for _, fn := range []func() error{
		func() error { _, err := ResolveGlobalProfiles("fuzzcov-abs-error", nil); return err },
		func() error { _, err := ReadGlobalExclusions("fuzzcov-abs-error", strings.NewReader("")); return err },
		func() error {
			_, err := ReportGlobal([]GlobalProfile{{Module: ".", Package: ".", Path: "fuzzcov-abs-error", ModulePath: "m"}}, nil, 95)
			return err
		},
		func() error { _, err := joinWithin("fuzzcov-abs-error", "x"); return err },
		func() error { _, err := joinWithin(filepath.Join(dir, "fuzzcov-rel-error"), "x"); return err },
		func() error {
			_, err := ResolveGlobalProfiles(filepath.Join(dir, "fuzzcov-rel-error"), []GlobalProfile{{Module: "m", Package: ".", Path: "p"}})
			return err
		},
		func() error { _, err := readGlobalProfilesFile(filepath.Join(dir, "missing")); return err },
		func() error { _, err := readGlobalExclusionsFile(dir, filepath.Join(dir, "missing")); return err },
		func() error { _, err := readGlobalCoverageProfile(filepath.Join(dir, "missing")); return err },
		func() error { _, err := readGlobalFloorsFile(dir); return err },
		func() error { _, err := hasCodeGeneratedHeader(dir); return err },
		func() error { _, _, err := sourceModuleAndPackage(filepath.Join(dir, "x.go")); return err },
		func() error { _, err := readGlobalProfilesFile("\x00"); return err },
		func() error { _, err := readGlobalExclusionsFile(dir, "\x00"); return err },
		func() error { _, err := readGlobalFloorsFile("\x00"); return err },
		func() error { _, err := readGlobalCoverageProfile("\x00"); return err },
		func() error { _, err := readManifest("\x00"); return err },
		func() error { _, err := readRegistry("\x00"); return err },
		func() error { _, err := readFloors("\x00"); return err },
		func() error { _, err := readIgnore("\x00"); return err },
		func() error {
			_, err := scanUniverse(filepath.Join(dir, "missing"), map[string]string{".": "m"})
			return err
		},
	} {
		fuzzcovWantError(t, fn())
	}
	if floors, err := readGlobalFloorsFile(filepath.Join(dir, "missing")); err != nil || len(floors) != 0 {
		t.Fatalf("missing floors: %v, %v", floors, err)
	}
	if ignored, err := readIgnore(filepath.Join(dir, "missing-ignore")); err != nil || len(ignored) != 0 {
		t.Fatalf("missing ignore: %v, %v", ignored, err)
	}
	profiles := filepath.Join(dir, "profiles")
	if err := os.WriteFile(profiles, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readGlobalCoverageProfile(profiles)
	fuzzcovWantError(t, err)
	if err := os.WriteFile(profiles, []byte("mode: count\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = readGlobalCoverageProfile(profiles)
	fuzzcovWantError(t, err)
	tooLong := filepath.Join(dir, "too-long.cov")
	if err := os.WriteFile(tooLong, []byte("mode: set\n"+strings.Repeat("x", (1<<24)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = readGlobalCoverageProfile(tooLong)
	fuzzcovWantError(t, err)
	if err := os.WriteFile(profiles, []byte("mode: set\n\ninvalid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = readGlobalCoverageProfile(profiles)
	fuzzcovWantError(t, err)

	for name, source := range map[string]string{
		"block.go":        "/* open\nclose */ package p\n",
		"block-eof.go":    "/* open\nclose */\n",
		"comments.go":     "// ordinary\n\n",
		"inline.go":       "/* done */ package p\n",
		"constraint.go":   "//go:build (\npackage p\n",
		"suffix_linux.go": "package p\n",
	} {
		filename := filepath.Join(dir, name)
		if err := os.WriteFile(filename, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = leadingBuildConstraintExpressions(filename)
	}
	_, err = leadingBuildConstraintExpressions(filepath.Join(dir, "missing.go"))
	fuzzcovWantError(t, err)
	tooLongSource := filepath.Join(dir, "source-too-long.go")
	if err := os.WriteFile(tooLongSource, []byte(strings.Repeat("x", 70<<10)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = leadingBuildConstraintExpressions(tooLongSource)
	fuzzcovWantError(t, err)
	ctx := globalCoverageBuildContext()
	otherOS := "windows"
	if ctx.GOOS == otherOS {
		otherOS = "linux"
	}
	openErrorSource := filepath.Join(dir, "fuzzcov-open-error.go")
	if err := os.WriteFile(openErrorSource, []byte("//go:build "+otherOS+"\npackage root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = hasPlatformDerivedUnavailability(openErrorSource, "fuzzcov-open-error.go", ctx)
	fuzzcovWantError(t, err)
	_ = hasUnavailablePlatformFilenameSuffix(filepath.Join(dir, "suffix_linux.go"), "suffix_linux.go", ctx)
	_, _ = hasPlatformDerivedUnavailability(filepath.Join(dir, "suffix_linux.go"), "suffix_linux.go", ctx)
	_, err = hasPlatformDerivedUnavailability(filepath.Join(dir, "missing-source.go"), "x.go", ctx)
	fuzzcovWantError(t, err)
	for _, file := range []string{"x.go", "_linux.go"} {
		_ = hasUnavailablePlatformFilenameSuffix(filepath.Join(dir, file), file, ctx)
	}

	moduleDir := filepath.Join(dir, "module")
	if err := os.Mkdir(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []GlobalProfile{
		{Module: "../x", Package: ".", Path: "p"},
		{Module: ".", Package: "x", Path: "p"},
		{Module: ".", Package: ".", Path: ""},
		{Module: "module", Package: ".", Path: "p"},
	} {
		_, err := ResolveGlobalProfiles(dir, []GlobalProfile{profile})
		fuzzcovWantError(t, err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("not a module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveGlobalProfiles(dir, []GlobalProfile{{Module: "module", Package: ".", Path: "p"}})
	fuzzcovWantError(t, err)
	_, err = resolveGlobalExclusion(dir, "../escape", ".", "x.go", "generated", "r")
	fuzzcovWantError(t, err)
	_, err = resolveGlobalExclusion(dir, ".", "./../escape", "x.go", "generated", "r")
	fuzzcovWantError(t, err)
	_, err = resolveGlobalExclusion(dir, ".", ".", "../x.go", "generated", "r")
	fuzzcovWantError(t, err)
	_, err = resolveGlobalExclusion(dir, ".", ".", "x.go", "generated", "r")
	fuzzcovWantError(t, err)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = resolveGlobalExclusion(dir, ".", ".", "x.go", "generated", "r")
	fuzzcovWantError(t, err)
	_, _, err = sourceModuleAndPackage(filepath.Join(dir, "x.go"))
	fuzzcovWantError(t, err)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, pkg, err := sourceModuleAndPackage(filepath.Join(dir, "x.go")); err != nil || pkg != "." {
		t.Fatalf("root source ownership = %q, %v", pkg, err)
	}
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "x.go", Kind: "generated", Reason: "r", SourcePath: dir, ProfileFile: "example.test/root/x.go"}))
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: filepath.Base(dir), Kind: "generated", Reason: "r", SourcePath: dir, ProfileFile: "example.test/root/" + filepath.Base(dir)}))
	badSource := filepath.Join(dir, "bad.go")
	if err := os.WriteFile(badSource, []byte("not go"), 0o644); err != nil {
		t.Fatal(err)
	}
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "bad.go", Kind: "generated", Reason: "r", SourcePath: badSource, ProfileFile: "example.test/root/bad.go"}))
	missingSource := filepath.Join(dir, "missing.go")
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "missing.go", Kind: "generated", Reason: "r", SourcePath: missingSource, ProfileFile: "example.test/root/missing.go"}))
	constraintSource := filepath.Join(dir, "platform.go")
	if err := os.WriteFile(constraintSource, []byte("//go:build (\npackage root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "platform.go", Kind: "platform", Reason: "r", SourcePath: constraintSource, ProfileFile: "example.test/root/platform.go"}))
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "fuzzcov-open-error.go", Kind: "platform", Reason: "r", SourcePath: openErrorSource, ProfileFile: "example.test/root/fuzzcov-open-error.go"}))
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "x.go", Kind: "bad", Reason: "r", SourcePath: filepath.Join(dir, "x.go"), ProfileFile: "example.test/root/x.go"}))
	if err := os.WriteFile(filepath.Join(dir, "ignore"), []byte("# ok\n # still comment\n#bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = readIgnore(filepath.Join(dir, "ignore"))
	if err := os.WriteFile(filepath.Join(dir, "ignore"), []byte("x#\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = readIgnore(filepath.Join(dir, "ignore"))
	fuzzcovWantError(t, err)
	blankProfile := filepath.Join(dir, "blank.cov")
	if err := os.WriteFile(blankProfile, []byte("mode: set\n\nexample.test/x.go:1.1,1.2 1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = parseProfile(blankProfile)
	_, err = runGapOnlyE("", dir, nil, filepath.Join(dir, "ignore"))
	fuzzcovWantError(t, err)
	_, err = runGapOnlyE(filepath.Join(dir, "missing"), dir, nil, filepath.Join(dir, "ignore"))
	fuzzcovWantError(t, err)
	_ = runGapOnly("", dir, nil, filepath.Join(dir, "ignore"))
	brokenRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenRepo, "go.mod"), []byte("module broken.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(brokenRepo, "absent"), filepath.Join(brokenRepo, "broken.go")); err != nil {
		t.Fatal(err)
	}
	brokenRegistry := filepath.Join(brokenRepo, "registry")
	if err := os.WriteFile(brokenRegistry, []byte("native:.:.:FuzzBroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = runGapOnlyE(brokenRegistry, brokenRepo, []string{"."}, filepath.Join(brokenRepo, "ignore"))
	fuzzcovWantError(t, err)
	readErrorGo := filepath.Join(brokenRepo, "fuzzcov-read-error.go")
	if err := os.WriteFile(readErrorGo, []byte("package m\nfunc ParseX() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = scanUniverse(brokenRepo, map[string]string{".": "broken.test/m"})
	fuzzcovWantError(t, err)
	readErrorModule := filepath.Join(dir, "fuzzcov-read-error")
	if err := os.Mkdir(readErrorModule, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readErrorModule, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveGlobalProfiles(dir, []GlobalProfile{{Module: "fuzzcov-read-error", Package: ".", Path: "p"}})
	fuzzcovWantError(t, err)
	_, _, err = sourceModuleAndPackage(filepath.Join(readErrorModule, "x.go"))
	fuzzcovWantError(t, err)
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "x.go", Kind: "generated", Reason: "r", SourcePath: filepath.Join(readErrorModule, "x.go"), ProfileFile: "x/x.go"}))
	relErrorModule := filepath.Join(dir, "fuzzcov-rel-error")
	if err := os.Mkdir(relErrorModule, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relErrorModule, "go.mod"), []byte("module rel.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = sourceModuleAndPackage(filepath.Join(relErrorModule, "x.go"))
	fuzzcovWantError(t, err)
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: ".", File: "fuzzcov-stat-error.go", Kind: "generated", Reason: "r", SourcePath: filepath.Join(dir, "fuzzcov-stat-error.go"), ProfileFile: "example.test/root/fuzzcov-stat-error.go"}))
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: ".", Package: "bad", File: "x.go", Kind: "generated", Reason: "r", SourcePath: filepath.Join(dir, "x.go"), ProfileFile: "example.test/root/x.go"}))
	if err := writeFloors(filepath.Join(dir, "fuzzcov-write-error"), []result{{name: "x", focusPct: 1}}, nil); err == nil {
		t.Fatal("injected write error was ignored")
	}
}

func fuzzCoverageAccountingEdges(t *testing.T) {
	_, err := ReportGlobal([]GlobalProfile{{}}, nil, math.NaN())
	fuzzcovWantError(t, err)
	_, err = ReportGlobal(nil, nil, 95)
	fuzzcovWantError(t, err)
	for _, profile := range []GlobalProfile{
		{Module: "../x", Package: ".", Path: "p", ModulePath: "m"},
		{Module: ".", Package: "x", Path: "p", ModulePath: "m"},
		{Module: ".", Package: ".", Path: "", ModulePath: "m"},
		{Module: ".", Package: ".", Path: "p", ModulePath: ""},
	} {
		_, err := ReportGlobal([]GlobalProfile{profile}, nil, 95)
		fuzzcovWantError(t, err)
	}
	repo, profilePath := globalExclusionFixture(t, "generated.go", "// Code generated by fuzz. DO NOT EDIT.\npackage pkg\n")
	base := resolvedGlobalProfile("m", "example.com/m", "./pkg", profilePath)
	resolved, err := ReadGlobalExclusions(repo, strings.NewReader("m\t./pkg\tgenerated.go\tgenerated\tseed\n"))
	if err != nil {
		t.Fatal(err)
	}
	valid := resolved[0]
	for _, exclusion := range []Exclusion{
		{},
		{Module: "../x", Package: ".", File: "x.go"},
		{Module: ".", Package: "x", File: "x.go"},
		{Module: ".", Package: ".", File: "x.txt", Kind: "generated", Reason: "r"},
		{Module: ".", Package: ".", File: "x.go", Kind: "bad", Reason: "r"},
		{Module: ".", Package: ".", File: "x.go", Kind: "generated"},
	} {
		_, err := ReportGlobal([]GlobalProfile{base}, []Exclusion{exclusion}, 95)
		fuzzcovWantError(t, err)
	}
	for _, exclusion := range []Exclusion{
		func() Exclusion { x := valid; x.SourcePath = ""; return x }(),
		func() Exclusion {
			x := valid
			x.SourcePath = filepath.Join(filepath.Dir(x.SourcePath), "ordinary.go")
			return x
		}(),
		func() Exclusion { x := valid; x.ProfileFile = "wrong/path.go"; return x }(),
		func() Exclusion { x := valid; x.Package = "."; return x }(),
		func() Exclusion { x := valid; x.Kind = "bad"; return x }(),
	} {
		_, err := ReportGlobal([]GlobalProfile{base}, []Exclusion{exclusion}, 95)
		fuzzcovWantError(t, err)
	}
	_, err = ReportGlobal([]GlobalProfile{base}, []Exclusion{valid, valid}, 95)
	fuzzcovWantError(t, err)
	otherPackage := valid
	otherPackage.Package = "./other"
	_, err = ReportGlobal([]GlobalProfile{base}, []Exclusion{otherPackage}, 95)
	fuzzcovWantError(t, err)
	otherModule := valid
	otherModule.Module = "other"
	_, err = ReportGlobal([]GlobalProfile{base}, []Exclusion{otherModule}, 95)
	fuzzcovWantError(t, err)

	conflicting := base
	conflicting.Path = writeGlobalProfile(t, repo, "other.cov", "mode: set\nexample.net/m/pkg/x.go:1.1,1.2 1 1\n")
	conflicting.ModulePath = "example.net/m"
	_, err = ReportGlobal([]GlobalProfile{base, conflicting}, nil, 95)
	fuzzcovWantError(t, err)
	duplicate := base
	_, err = ReportGlobal([]GlobalProfile{base, duplicate}, nil, 95)
	fuzzcovWantError(t, err)
	badBlock := base
	badBlock.Path = writeGlobalProfile(t, repo, "badblock.cov", "mode: set\nother.test/pkg/x.go:1.1,1.2 1 1\n")
	_, err = ReportGlobal([]GlobalProfile{badBlock}, nil, 95)
	fuzzcovWantError(t, err)

	if err := validateGlobalBlockOwnership(globalBlock{file: "other/x.go"}, "m", "."); err == nil {
		t.Fatal("foreign block accepted")
	}
	if err := validateGlobalBlockOwnership(globalBlock{file: "."}, ".", "."); err == nil {
		t.Fatal("nameless block accepted")
	}
	rootSource := filepath.Join(repo, "m", "pkg", "generated.go")
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: "m", Package: "./other", File: "generated.go", Kind: "generated", Reason: "r", SourcePath: rootSource, ProfileFile: "example.com/m/pkg/generated.go"}))
	fuzzcovWantError(t, validateResolvedExclusion(Exclusion{Module: "m", Package: "./pkg", File: "generated.go", Kind: "generated", Reason: "r", SourcePath: rootSource, ProfileFile: "example.com/m/pkg/wrong.go"}))

	zeroRepo, zeroProfile := globalExclusionFixture(t, "only.go", "// Code generated by fuzz. DO NOT EDIT.\npackage pkg\n")
	if err := os.WriteFile(zeroProfile, []byte("mode: set\nexample.com/m/pkg/only.go:1.1,1.2 1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	zeroExclusions, err := ReadGlobalExclusions(zeroRepo, strings.NewReader("m\t./pkg\tonly.go\tgenerated\tall code\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReportGlobal([]GlobalProfile{resolvedGlobalProfile("m", "example.com/m", "./pkg", zeroProfile)}, zeroExclusions, 95)
	fuzzcovWantError(t, err)

	fuzzCoverageGlobalModeErrors(t)

	// Exercise deterministic sort tie-breakers for modules, packages, and files.
	_, profile2 := globalExclusionFixture(t, "z.go", "// Code generated by fuzz. DO NOT EDIT.\npackage pkg\n")
	second := resolvedGlobalProfile("z", "example.com/m", "./pkg", profile2)
	second.Module = "z"
	_, _ = ReportGlobal([]GlobalProfile{second, base}, nil, 95)
	otherPkgProfile := writeGlobalProfile(t, repo, "pkg-sort.cov", "mode: set\nexample.com/m/aaa/x.go:1.1,1.2 1 1\n")
	_, _ = ReportGlobal([]GlobalProfile{base, resolvedGlobalProfile("m", "example.com/m", "./aaa", otherPkgProfile)}, nil, 95)
	keys := []globalPackageKey{{module: "z", packagePath: "z"}, {module: "a", packagePath: "z"}, {module: "a", packagePath: "a"}}
	sortGlobalPackageKeys(keys)
	applied := []AppliedExclusion{{Module: "z", Package: "z", File: "z.go"}, {Module: "a", Package: "z", File: "z.go"}, {Module: "a", Package: "a", File: "z.go"}, {Module: "a", Package: "a", File: "a.go"}}
	sortAppliedExclusions(applied)
}

func fuzzCoverageGlobalModeErrors(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest")
	exclusions := filepath.Join(dir, "exclusions")
	floors := filepath.Join(dir, "floors")
	stdout, stderr := new(strings.Builder), new(strings.Builder)
	write := func(name, content string) {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(manifest, ".\t.\tmissing.cov\n")
	write(exclusions, "")
	write(floors, "")
	base := globalModeOptions{manifestPath: manifest, exclusionsPath: exclusions, floorsPath: floors, repoRoot: dir, minimum: 95}
	_, err := runGlobalMode(base, stdout, stderr)
	fuzzcovWantError(t, err)

	write(filepath.Join(dir, "go.mod"), "module example.test/m\n")
	_, err = runGlobalMode(base, stdout, stderr)
	fuzzcovWantError(t, err)
	profile := filepath.Join(dir, "p.cov")
	write(profile, "mode: count\n")
	write(manifest, ".\t.\t"+profile+"\n")
	_, err = runGlobalMode(base, stdout, stderr)
	fuzzcovWantError(t, err)
	write(profile, "mode: set\nexample.test/m/x.go:1.1,1.2 1 1\n")
	write(exclusions, "bad\n")
	_, err = runGlobalMode(base, stdout, stderr)
	fuzzcovWantError(t, err)
	write(exclusions, "")
	write(profile, "mode: set\nexample.test/m/x.go:1.1,1.2 1 0\n")
	if err := os.Remove(floors); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(floors, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = runGlobalMode(base, stdout, stderr)
	fuzzcovWantError(t, err)
	if err := os.Remove(floors); err != nil {
		t.Fatal(err)
	}
	write(floors, "")
	jsonOptions := base
	jsonOptions.json = true
	_, err = runGlobalMode(jsonOptions, fuzzcovErrorWriter{}, stderr)
	fuzzcovWantError(t, err)
	bless := base
	bless.bless = true
	if code, err := runGlobalMode(bless, stdout, stderr); err != nil || code != 1 {
		t.Fatalf("failed bless = %d, %v", code, err)
	}
	write(profile, "mode: set\nexample.test/m/x.go:1.1,1.2 100 1\n")
	bless.floorsPath = filepath.Join(dir, "missing-parent", "floors")
	_, err = runGlobalMode(bless, stdout, stderr)
	fuzzcovWantError(t, err)
}
