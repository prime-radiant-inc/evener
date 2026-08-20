// Command evener-fuzzcov is the static fuzz gap gate: it asserts that every
// decode/parse package in the workspace has a registered fuzz target (or a
// reasoned ignore-list entry), without replaying any corpus.
//
// It derives the fuzzed package set from the target registry
// (scripts/fuzz/run-fuzz.sh --list output, passed via -registry), scans the
// workspace for decode/parse signatures, subtracts the ignore-list, and exits
// non-zero on anything left over. Seconds, deterministic — safe as a blocking
// PR gate. Run it via:
//
//	make fuzz-gap-check
//
// The per-target coverage reporter that used to live here was deleted with
// the coverage-floor consolidation; `make coverage-floor` owns the "how much
// is exercised" number now.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var exitProcess = os.Exit

// target is one registry entry: the fuzz target's identity plus the optional
// coverpkg override naming the package(s) it really exercises.
type target struct {
	tag      string // "native" (testing.F) or "rapid" (rapid.Check Test func)
	module   string // go.work module dir, e.g. "agent" or "."
	pkg      string // package relpath within the module, e.g. "./appwire" or "."
	name     string // FuzzName
	coverpkg string // package(s) the target covers; defaults to pkg
}

func main() {
	code, err := runCLI(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "evener-fuzzcov: %v\n", err)
		code = 2
	}
	exitProcess(code)
}

func runCLI(args []string, stdout, stderr *os.File) (int, error) {
	flags := flag.NewFlagSet("evener-fuzzcov", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ignorePath := flags.String("ignore", "scripts/coverage/fuzzcov-ignore.txt", "gap-map ignore-list")
	repoRoot := flags.String("repo-root", ".", "repository root")
	modulesArg := flags.String("modules", "", "space-separated go.work module dirs to scan for the gap map (default: derived from go.work)")
	gapOnly := flags.Bool("gap-only", false, "STATIC gap gate: derive the fuzzed set from the registry (no coverage replay) and exit non-zero on any unfuzzed, unignored parse package")
	registry := flags.String("registry", "", "path to scripts/fuzz/run-fuzz.sh --list output (required with -gap-only)")
	if err := flags.Parse(args); err != nil {
		return 2, err
	}

	modules := strings.Fields(*modulesArg)
	if len(modules) == 0 {
		var err error
		modules, err = goWorkModules(*repoRoot)
		if err != nil {
			return 2, fmt.Errorf("derive modules from go.work: %w", err)
		}
	}

	if *gapOnly {
		return runGapOnlyE(*registry, *repoRoot, modules, *ignorePath)
	}

	return 2, errors.New("the only mode is -gap-only -registry <file>; the coverage reporter was removed")
}

// runGapOnlyE is the fast STATIC gap gate. It never replays a corpus: it
// derives the fuzzed package set from the registry's declared target packages,
// scans the parse-signature universe, subtracts the ignore-list, and exits
// non-zero if any parse package is left un-fuzzed and un-ignored.
func runGapOnlyE(registryPath, repoRoot string, modules []string, ignorePath string) (int, error) {
	if registryPath == "" {
		return 2, errors.New("-gap-only requires -registry (the scripts/fuzz/run-fuzz.sh --list output)")
	}
	targets, err := readRegistry(registryPath)
	if err != nil {
		return 2, fmt.Errorf("read registry: %w", err)
	}
	modulePaths, err := readModulePaths(repoRoot, modules)
	if err != nil {
		return 2, fmt.Errorf("read module paths: %w", err)
	}
	fuzzed := staticFuzzedPackages(targets, modulePaths)
	universe, err := scanUniverse(repoRoot, modulePaths)
	if err != nil {
		return 2, fmt.Errorf("scan parse universe: %w", err)
	}
	ignore, err := readIgnore(ignorePath)
	if err != nil {
		return 2, fmt.Errorf("read ignore-list: %w", err)
	}
	gaps := gapMap(universe, fuzzed, ignore)

	if len(gaps) == 0 {
		fmt.Printf("fuzz gap check: all %d decode/parse package(s) have a registered target or a reasoned ignore\n", len(universe))
		return 0, nil
	}
	_, _ = fmt.Fprintln(os.Stderr, "GAP MAP — decode/parse packages with NO registered fuzz target")
	for _, g := range gaps {
		_, _ = fmt.Fprintf(os.Stderr, "  %-52s (%s)\n", g[0], g[1])
	}
	_, _ = fmt.Fprintf(os.Stderr, "evener-fuzzcov: GAP BREACH: %d decode/parse package(s) have no fuzz target and are not ignored\n", len(gaps))
	return 1, nil
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
		for part := range strings.SplitSeq(claimed, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out[joinImport(modulePath, pkgSubdir(part))] = true
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
			content, e := fuzzcovSystem.readFile(p)
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

// readRegistry parses scripts/fuzz/run-fuzz.sh --list output: one colon-separated
// "tag:module:pkg:name[:coverpkg]" entry per line. Comments and blank lines are
// skipped.
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
			return nil, fmt.Errorf("malformed registry line (want \"tag:module:pkg:name[:coverpkg]\"): %q", line)
		}
		t := target{tag: fields[0], module: fields[1], pkg: fields[2], name: fields[3]}
		if len(fields) > 4 {
			t.coverpkg = fields[4]
		}
		out = append(out, t)
	}
	return out, sc.Err()
}

func goWorkModules(repoRoot string) ([]string, error) {
	content, err := fuzzcovSystem.readFile(filepath.Join(repoRoot, "go.work"))
	if err != nil {
		return nil, err
	}
	var mods []string
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(string(content)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch {
		case inBlock:
			if fields[0] == ")" {
				inBlock = false
				continue
			}
			mods = append(mods, filepath.Clean(fields[0]))
		case fields[0] == "use" && len(fields) >= 2:
			if fields[1] == "(" {
				inBlock = true
				continue
			}
			mods = append(mods, filepath.Clean(fields[1]))
		}
	}
	if len(mods) == 0 {
		return nil, fmt.Errorf("no use directives in %s", filepath.Join(repoRoot, "go.work"))
	}
	return mods, nil
}

func readModulePaths(repoRoot string, modules []string) (map[string]string, error) {
	out := map[string]string{}
	for _, m := range modules {
		gomod := filepath.Join(repoRoot, m, "go.mod")
		content, err := fuzzcovSystem.readFile(gomod)
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
		imp, reason, found := strings.Cut(line, "#")
		if !found {
			return nil, fmt.Errorf("%s:%d: ignore entry %q has no reason comment (use \"<import-path>  # <reason>\")", p, n, line)
		}
		imp = strings.TrimSpace(imp)
		reason = strings.TrimSpace(reason)
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
