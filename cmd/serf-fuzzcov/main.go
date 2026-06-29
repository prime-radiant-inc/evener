// Command serf-fuzzcov is the fuzz coverage reporter: it turns the per-target
// coverage profiles produced by replaying each fuzz target's COMMITTED corpus
// into an honest, drivable-to-100% coverage map.
//
// For each fuzz target it computes a FOCUS-SET coverage % — the line coverage
// the corpus drives into the specific decode/parse seam the target is meant to
// fuzz (declared as the trailing `focus` field of scripts/run-fuzz.sh's TARGETS)
// — plus the whole-package % as a secondary visibility number. It enforces a
// no-regression ratchet against scripts/fuzzcov-floors.txt and emits a GAP MAP:
// every decode/parse package across the workspace that has zero fuzz coverage.
//
// It does not run any tests; scripts/fuzz-coverage.sh produces the profiles and
// the target manifest, then invokes this reporter. Run it via:
//
//	make fuzz-coverage           # advisory: print the report, always exit 0
//	make fuzz-coverage CHECK=1   # ratchet + gap floor: exit non-zero on a breach
//
// The --bless mode raises each floor in scripts/fuzzcov-floors.txt upward to the
// current measured focus % (it never lowers a floor), locking in corpus gains.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// target is one fuzz target plus the metadata the reporter needs to attribute
// its profile. The first five fields mirror scripts/run-fuzz.sh's TARGETS entry;
// profile is the path to that target's replayed -coverprofile.
type target struct {
	tag      string // "native" (testing.F) or "rapid" (rapid.Check Test func)
	module   string // go.work module dir, e.g. "agent" or "."
	pkg      string // package relpath within the module, e.g. "./appwire" or "."
	name     string // FuzzName
	coverpkg string // go test -coverpkg value (defaults to pkg)
	focus    string // ";"-separated focus specs; empty means "whole SUT package"
	profile  string // path to the replayed coverage profile
}

// focusSpec is one focus entry: a file relative to the SUT package dir, with an
// optional function name to narrow to that function's line range.
type focusSpec struct {
	relpath string
	fn      string // empty means the whole file
}

// block is a single coverage block parsed from a profile line.
type block struct {
	file  string // import-path-qualified, e.g. primeradiant.com/serf/appwire/jsonrpc.go
	start int    // start line of the block
	stmts int    // number of statements
	count int    // execution count (0 or 1 under mode: set)
}

// result holds the computed numbers for one target's report row.
type result struct {
	name       string
	focusLabel string
	focusPct   float64
	floor      float64
	pkgPct     float64
}

func main() {
	manifest := flag.String("manifest", "", "path to the target manifest written by fuzz-coverage.sh (required)")
	floorsPath := flag.String("floors", "scripts/fuzzcov-floors.txt", "ratchet floors file")
	ignorePath := flag.String("ignore", "scripts/fuzzcov-ignore.txt", "gap-map ignore-list")
	repoRoot := flag.String("repo-root", ".", "repository root")
	modulesArg := flag.String("modules", ". agent llm auth fuzz", "space-separated go.work module dirs to scan for the gap map")
	check := flag.Bool("check", false, "exit non-zero on a focus-set regression or a gap breach")
	bless := flag.Bool("bless", false, "raise each floor upward to the current measured focus %")
	tolerance := flag.Float64("tolerance", 0.5, "ratchet tolerance band (percentage points) absorbing nondeterministic wobble")
	gapOnly := flag.Bool("gap-only", false, "STATIC gap gate: derive the fuzzed set from the registry (no coverage replay) and exit non-zero on any unfuzzed, unignored parse package")
	registry := flag.String("registry", "", "path to scripts/run-fuzz.sh --list output (required with -gap-only)")
	flag.Parse()

	if *gapOnly {
		os.Exit(runGapOnly(*registry, *repoRoot, strings.Fields(*modulesArg), *ignorePath))
	}

	if *manifest == "" {
		fatal("--manifest is required")
	}

	targets, err := readManifest(*manifest)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	modulePaths, err := readModulePaths(*repoRoot, strings.Fields(*modulesArg))
	if err != nil {
		fatal("read module paths: %v", err)
	}
	floors, err := readFloors(*floorsPath)
	if err != nil {
		fatal("read floors: %v", err)
	}

	// Parse every profile once; build per-target blocks and the merged union.
	merged := map[string]block{} // file:start -> block (union of counts)
	perTarget := map[string][]block{}
	for _, t := range targets {
		blocks, err := parseProfile(t.profile)
		if err != nil {
			fatal("%s: %v", t.name, err)
		}
		perTarget[t.name] = blocks
		for _, b := range blocks {
			key := fmt.Sprintf("%s:%d", b.file, b.start)
			if prev, ok := merged[key]; ok {
				if b.count > prev.count {
					prev.count = b.count
					merged[key] = prev
				}
				continue
			}
			merged[key] = b
		}
	}

	results := make([]result, 0, len(targets))
	for _, t := range targets {
		r, err := computeTarget(*repoRoot, modulePaths, t, perTarget[t.name], floors)
		if err != nil {
			fatal("%s: %v", t.name, err)
		}
		results = append(results, r)
	}

	// Gap map: every decode/parse package minus the fuzzed set minus the ignore-list.
	fuzzed := fuzzedPackages(merged)
	universe, err := scanUniverse(*repoRoot, modulePaths)
	if err != nil {
		fatal("scan parse universe: %v", err)
	}
	ignore, err := readIgnore(*ignorePath)
	if err != nil {
		fatal("read ignore-list: %v", err)
	}
	gaps := gapMap(universe, fuzzed, ignore)

	if *bless {
		if err := writeFloors(*floorsPath, results, floors); err != nil {
			fatal("bless floors: %v", err)
		}
		fmt.Printf("serf-fuzzcov: raised floors in %s\n", *floorsPath)
	}

	printReport(results, gaps)

	if *check {
		os.Exit(checkExit(results, gaps, *tolerance))
	}
}

// runGapOnly is the fast STATIC gap gate. It never replays a corpus: it derives
// the fuzzed package set from the registry's declared target packages, scans the
// parse-signature universe, subtracts the ignore-list, and exits non-zero if any
// parse package is left un-fuzzed and un-ignored. Seconds, deterministic — safe
// for the PR gate, unlike the slow coverage-driven --check.
func runGapOnly(registryPath, repoRoot string, modules []string, ignorePath string) int {
	if registryPath == "" {
		fatal("-gap-only requires -registry (the scripts/run-fuzz.sh --list output)")
	}
	targets, err := readRegistry(registryPath)
	if err != nil {
		fatal("read registry: %v", err)
	}
	modulePaths, err := readModulePaths(repoRoot, modules)
	if err != nil {
		fatal("read module paths: %v", err)
	}
	fuzzed := staticFuzzedPackages(targets, modulePaths)
	universe, err := scanUniverse(repoRoot, modulePaths)
	if err != nil {
		fatal("scan parse universe: %v", err)
	}
	ignore, err := readIgnore(ignorePath)
	if err != nil {
		fatal("read ignore-list: %v", err)
	}
	gaps := gapMap(universe, fuzzed, ignore)

	if len(gaps) == 0 {
		fmt.Printf("fuzz gap check: all %d decode/parse package(s) have a registered target or a reasoned ignore\n", len(universe))
		return 0
	}
	fmt.Fprintln(os.Stderr, "GAP MAP — decode/parse packages with NO registered fuzz target")
	for _, g := range gaps {
		fmt.Fprintf(os.Stderr, "  %-52s (%s)\n", g[0], g[1])
	}
	fmt.Fprintf(os.Stderr, "serf-fuzzcov: GAP BREACH: %d decode/parse package(s) have no fuzz target and are not ignored\n", len(gaps))
	return 1
}

// staticFuzzedPackages returns the package import paths a target registry claims
// to fuzz, derived purely from each entry's declared package (its coverpkg, or
// the package relpath when no coverpkg is set). The coverpkg may name several
// comma-separated packages. No coverage data is consulted.
func staticFuzzedPackages(targets []target, modulePaths map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, t := range targets {
		modulePath := modulePaths[t.module]
		if modulePath == "" {
			continue
		}
		claimed := t.coverpkg
		if claimed == "" {
			claimed = t.pkg
		}
		for _, part := range strings.Split(claimed, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out[joinImport(modulePath, pkgSubdir(part))] = true
		}
	}
	return out
}

// computeTarget resolves the focus set, attributes the target's profile to it,
// and computes both the primary focus % and the secondary whole-package %.
func computeTarget(repoRoot string, modulePaths map[string]string, t target, blocks []block, floors map[string]float64) (result, error) {
	r := result{name: t.name, floor: floors[t.name]}

	modulePath := modulePaths[t.module]
	if modulePath == "" {
		return r, fmt.Errorf("no module path for module %q", t.module)
	}
	pkgSub := pkgSubdir(t.pkg)
	sutImport := joinImport(modulePath, pkgSub)

	specs := parseFocus(t.focus)
	if len(specs) == 0 {
		// Whole-package focus: the seam is the entire SUT package.
		r.focusLabel = "(whole package)"
		r.focusPct = pctForPackage(blocks, sutImport)
		r.pkgPct = r.focusPct
		return r, nil
	}

	var labels []string
	covered, total := 0, 0
	pkgImport := ""
	for _, s := range specs {
		fileImport := joinImport(modulePath, path.Join(pkgSub, s.relpath))
		if pkgImport == "" {
			pkgImport = path.Dir(fileImport)
		}
		lo, hi := 0, 1<<30
		if s.fn != "" {
			srcPath := filepath.Join(repoRoot, t.module, pkgSub, s.relpath)
			var err error
			lo, hi, err = funcLineRange(srcPath, s.fn)
			if err != nil {
				return r, err
			}
			labels = append(labels, s.relpath+"#"+s.fn)
		} else {
			labels = append(labels, s.relpath)
		}
		for _, b := range blocks {
			if b.file != fileImport || b.start < lo || b.start > hi {
				continue
			}
			total += b.stmts
			if b.count > 0 {
				covered += b.stmts
			}
		}
	}
	r.focusLabel = strings.Join(labels, "; ")
	if total > 0 {
		r.focusPct = 100 * float64(covered) / float64(total)
	}
	// The secondary whole-package % is measured over the package that holds the
	// focus set (which, for FuzzToolArgsValidate, is internal/tool — the real SUT
	// — not the agent root package the target's go test lives in).
	r.pkgPct = pctForPackage(blocks, pkgImport)
	return r, nil
}

// pctForPackage computes covered/total over the blocks whose file sits directly
// in the given package import path.
func pctForPackage(blocks []block, pkgImport string) float64 {
	covered, total := 0, 0
	for _, b := range blocks {
		if path.Dir(b.file) != pkgImport {
			continue
		}
		total += b.stmts
		if b.count > 0 {
			covered += b.stmts
		}
	}
	if total == 0 {
		return 0
	}
	return 100 * float64(covered) / float64(total)
}

// funcLineRange parses srcPath and returns the [start,end] line range of the
// top-level function (or method) named fn.
func funcLineRange(srcPath, fn string) (int, int, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("parse %s: %w", srcPath, err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		return fset.Position(fd.Pos()).Line, fset.Position(fd.End()).Line, nil
	}
	return 0, 0, fmt.Errorf("function %s not found in %s", fn, srcPath)
}

// fuzzedPackages returns the set of package import paths with at least one
// covered statement in the merged union profile.
func fuzzedPackages(merged map[string]block) map[string]bool {
	out := map[string]bool{}
	for _, b := range merged {
		if b.count > 0 {
			out[path.Dir(b.file)] = true
		}
	}
	return out
}

// parseSignatures mark a package as a wire-decode/parse surface. Tuned to
// over-include: the gap map is a prompt for human judgement, backed by the
// reason-required ignore-list, not a proof.
var parseSignatures = []*regexp.Regexp{
	regexp.MustCompile(`func\s*\([^)]*\)\s*UnmarshalJSON`),
	regexp.MustCompile(`func\s*\([^)]*\)\s*UnmarshalText`),
	regexp.MustCompile(`json\.Unmarshal\(`),
	regexp.MustCompile(`json\.NewDecoder\(`),
	regexp.MustCompile(`func\s+Parse`),
	regexp.MustCompile(`func\s+\w*Decode`),
	regexp.MustCompile(`toml\.Decode`),
	regexp.MustCompile(`toml\.Unmarshal`),
}

// scanUniverse walks every module and returns each package import path that
// contains a decode/parse signature, mapped to the first signature found.
func scanUniverse(repoRoot string, modulePaths map[string]string) (map[string]string, error) {
	universe := map[string]string{}
	for module, modulePath := range modulePaths {
		moduleDir := filepath.Join(repoRoot, module)
		err := filepath.WalkDir(moduleDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				base := d.Name()
				if p != moduleDir && (base == "testdata" || base == "vendor" || base == "node_modules" || strings.HasPrefix(base, ".")) {
					return filepath.SkipDir
				}
				// Do not descend into a nested module — it is walked on its own.
				if p != moduleDir {
					if _, e := os.Stat(filepath.Join(p, "go.mod")); e == nil {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			content, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			sig := matchSignature(content)
			if sig == "" {
				return nil
			}
			relDir, _ := filepath.Rel(moduleDir, filepath.Dir(p))
			imp := joinImport(modulePath, filepath.ToSlash(relDir))
			if _, ok := universe[imp]; !ok {
				universe[imp] = sig
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return universe, nil
}

// matchSignature returns the first parse signature present in src, or "". The
// trailing "(" of a call signature (json.Unmarshal() is trimmed for display.
func matchSignature(src []byte) string {
	for _, re := range parseSignatures {
		if loc := re.Find(src); loc != nil {
			return strings.TrimSuffix(string(loc), "(")
		}
	}
	return ""
}

// gapMap returns the parse packages with zero fuzz coverage, minus the
// ignore-list, sorted by import path.
func gapMap(universe map[string]string, fuzzed, ignore map[string]bool) [][2]string {
	var out [][2]string
	for imp, sig := range universe {
		if fuzzed[imp] || ignore[imp] {
			continue
		}
		out = append(out, [2]string{imp, sig})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func printReport(results []result, gaps [][2]string) {
	fmt.Println("FUZZ SURFACE COVERAGE  (committed corpus, deterministic replay — goal: 100%)")
	fmt.Println()
	fmt.Printf("  %-40s %-44s %8s %8s %8s\n", "TARGET", "FOCUS SET", "FOCUS %", "FLOOR", "PKG %")
	for _, r := range results {
		// Compare against the 1-decimal floor with a matching band so a target at
		// its floor reads "=", not a perpetual "^" from sub-0.1 rounding noise.
		var mark string
		switch {
		case r.focusPct < r.floor-0.05:
			mark = "!"
		case r.focusPct > r.floor+0.05:
			mark = "^"
		default:
			mark = "="
		}
		fmt.Printf("  %-40s %-44s %6.1f%% %s %6.1f%% %6.1f%%\n",
			r.name, truncate(r.focusLabel, 44), r.focusPct, mark, r.floor, r.pkgPct)
	}
	fmt.Println()
	fmt.Println("  (^ above floor — ratchet will rise; = at floor; ! below floor fails --check)")
	fmt.Println()
	if len(gaps) == 0 {
		fmt.Println("GAP MAP — decode/parse packages with ZERO fuzz coverage: none (all covered or ignored)")
		return
	}
	fmt.Println("GAP MAP — decode/parse packages with ZERO fuzz coverage")
	for _, g := range gaps {
		fmt.Printf("  %-52s (%s)\n", g[0], g[1])
	}
}

// checkExit returns the process exit code for --check: non-zero on any focus-set
// regression (beyond the tolerance band) or any unignored gap.
func checkExit(results []result, gaps [][2]string, tolerance float64) int {
	code := 0
	for _, r := range results {
		if r.focusPct+tolerance+1e-9 < r.floor {
			fmt.Fprintf(os.Stderr, "serf-fuzzcov: REGRESSION %s: focus %.1f%% < floor %.1f%% (tolerance %.1f)\n",
				r.name, r.focusPct, r.floor, tolerance)
			code = 1
		}
	}
	if len(gaps) > 0 {
		fmt.Fprintf(os.Stderr, "serf-fuzzcov: GAP BREACH: %d decode/parse package(s) have zero fuzz coverage and are not ignored\n", len(gaps))
		code = 1
	}
	return code
}

// --- parsing helpers ---

func readManifest(p string) ([]target, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []target
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			return nil, fmt.Errorf("malformed manifest line (want 6 tab-separated fields): %q", line)
		}
		out = append(out, target{
			module: fields[0], pkg: fields[1], name: fields[2],
			coverpkg: fields[3], focus: fields[4], profile: fields[5],
		})
	}
	return out, sc.Err()
}

// readRegistry parses scripts/run-fuzz.sh --list output: one colon-separated
// "tag:module:pkg:name[:coverpkg[:focus]]" entry per line. Comments and blank
// lines are skipped. Profiles are not part of a registry entry.
func readRegistry(p string) ([]target, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []target
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			return nil, fmt.Errorf("malformed registry line (want \"tag:module:pkg:name[:coverpkg[:focus]]\"): %q", line)
		}
		t := target{tag: fields[0], module: fields[1], pkg: fields[2], name: fields[3]}
		if len(fields) > 4 {
			t.coverpkg = fields[4]
		}
		if len(fields) > 5 {
			t.focus = fields[5]
		}
		out = append(out, t)
	}
	return out, sc.Err()
}

func parseFocus(focus string) []focusSpec {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return nil
	}
	var out []focusSpec
	for _, part := range strings.Split(focus, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		s := focusSpec{relpath: part}
		if rel, fn, ok := strings.Cut(part, "#"); ok {
			s.relpath = rel
			s.fn = fn
		}
		out = append(out, s)
	}
	return out
}

func parseProfile(p string) ([]block, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open profile: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	if !sc.Scan() {
		return nil, fmt.Errorf("empty profile %s", p)
	}
	if mode := strings.TrimSpace(sc.Text()); mode != "mode: set" {
		return nil, fmt.Errorf("profile %s has mode %q; only \"mode: set\" is supported", p, mode)
	}
	var out []block
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		b, err := parseBlock(line)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", p, err)
		}
		out = append(out, b)
	}
	return out, sc.Err()
}

// parseBlock parses one coverage line: "file:sl.sc,el.ec stmts count".
func parseBlock(line string) (block, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return block{}, fmt.Errorf("malformed block line %q", line)
	}
	stmts, err := strconv.Atoi(fields[1])
	if err != nil {
		return block{}, fmt.Errorf("bad stmt count in %q: %w", line, err)
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return block{}, fmt.Errorf("bad count in %q: %w", line, err)
	}
	loc := fields[0]
	colon := strings.LastIndex(loc, ":")
	if colon < 0 {
		return block{}, fmt.Errorf("no position in %q", line)
	}
	file := loc[:colon]
	rng := loc[colon+1:] // sl.sc,el.ec
	comma := strings.Index(rng, ",")
	if comma < 0 {
		return block{}, fmt.Errorf("no range in %q", line)
	}
	startLine, err := strconv.Atoi(strings.SplitN(rng[:comma], ".", 2)[0])
	if err != nil {
		return block{}, fmt.Errorf("bad start line in %q: %w", line, err)
	}
	return block{file: file, start: startLine, stmts: stmts, count: count}, nil
}

func readModulePaths(repoRoot string, modules []string) (map[string]string, error) {
	out := map[string]string{}
	for _, m := range modules {
		gomod := filepath.Join(repoRoot, m, "go.mod")
		content, err := os.ReadFile(gomod)
		if err != nil {
			return nil, err
		}
		mp := modulePathFromGoMod(content)
		if mp == "" {
			return nil, fmt.Errorf("no module path in %s", gomod)
		}
		out[m] = mp
	}
	return out, nil
}

func modulePathFromGoMod(content []byte) string {
	sc := bufio.NewScanner(strings.NewReader(string(content)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

func readFloors(p string) (map[string]float64, error) {
	out := map[string]float64{}
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed floors line %q (want \"FuzzName PCT\")", line)
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, fmt.Errorf("bad floor %q: %w", line, err)
		}
		out[fields[0]] = v
	}
	return out, sc.Err()
}

// writeFloors rewrites the floors file, raising each target's floor upward to
// its current measured focus %. It never lowers an existing floor.
func writeFloors(p string, results []result, old map[string]float64) error {
	raised := map[string]float64{}
	for k, v := range old {
		raised[k] = v
	}
	for _, r := range results {
		if r.focusPct > raised[r.name] {
			raised[r.name] = r.focusPct
		}
	}
	names := make([]string, 0, len(raised))
	for k := range raised {
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("# fuzzcov focus-set ratchet floors — one line per fuzz target: \"FuzzName PCT\".\n")
	sb.WriteString("# A target's focus-set coverage may never drop below its floor (serf-fuzzcov --check).\n")
	sb.WriteString("# Raised upward only, by `make fuzz-coverage CHECK=1` with --bless; never edit downward.\n")
	for _, n := range names {
		sb.WriteString(fmt.Sprintf("%s %.1f\n", n, raised[n]))
	}
	return os.WriteFile(p, []byte(sb.String()), 0o644)
}

// readIgnore reads the gap-map ignore-list. Every entry must carry a reason
// comment ("<import-path>  # <reason>"); a reasonless entry is an error so the
// file is reviewed like code.
func readIgnore(p string) (map[string]bool, error) {
	out := map[string]bool{}
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hash := strings.Index(line, "#")
		if hash < 0 {
			return nil, fmt.Errorf("%s:%d: ignore entry %q has no reason comment (use \"<import-path>  # <reason>\")", p, n, line)
		}
		imp := strings.TrimSpace(line[:hash])
		reason := strings.TrimSpace(line[hash+1:])
		if imp == "" || reason == "" {
			return nil, fmt.Errorf("%s:%d: ignore entry %q needs both an import path and a reason", p, n, line)
		}
		out[imp] = true
	}
	return out, sc.Err()
}

// --- small utilities ---

// pkgSubdir turns a package relpath ("./appwire", ".") into a module-relative
// subdirectory ("appwire", "").
func pkgSubdir(pkg string) string {
	return strings.Trim(strings.TrimPrefix(pkg, "."), "/")
}

// joinImport joins a module import path with a slash-separated subpath, treating
// "" and "." as the module root.
func joinImport(modulePath, sub string) string {
	sub = strings.Trim(sub, "/")
	if sub == "" || sub == "." {
		return modulePath
	}
	return modulePath + "/" + sub
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "serf-fuzzcov: "+format+"\n", args...)
	os.Exit(2)
}
