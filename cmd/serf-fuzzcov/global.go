package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"math"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GlobalProfile identifies one package-local coverage profile. Multiple target
// profiles may name the same module and package; their exact source blocks are
// unioned before the package contributes to the module total.
type GlobalProfile struct {
	Module     string
	Package    string
	Path       string
	ModulePath string
}

// Exclusion is one reviewed, whole-file denominator exclusion. SourcePath and
// ProfileFile are resolved by ReadGlobalExclusions so ReportGlobal can match the
// manifest entry to the import-path-qualified filename in a coverprofile.
type Exclusion struct {
	Module      string
	Package     string
	File        string
	Kind        string
	Reason      string
	SourcePath  string
	ProfileFile string
}

// PackageReport is the raw package-local statement total after approved file
// exclusions have been removed.
type PackageReport struct {
	Module  string  `json:"module"`
	Package string  `json:"package"`
	Covered int     `json:"covered"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
}

// ModuleReport is the raw union across every package in one workspace module.
// Pass is deliberately strict: a ratio of exactly minimum does not pass.
type ModuleReport struct {
	Module   string          `json:"module"`
	Covered  int             `json:"covered"`
	Total    int             `json:"total"`
	Percent  float64         `json:"percent"`
	Pass     bool            `json:"pass"`
	Packages []PackageReport `json:"packages"`
}

// AppliedExclusion records the exact reviewed file that was removed from a
// profile denominator. It is retained in both text and JSON reports.
type AppliedExclusion struct {
	Module     string `json:"module"`
	Package    string `json:"package"`
	File       string `json:"file"`
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	Blocks     int    `json:"blocks"`
	Statements int    `json:"statements"`
}

// GlobalReport is the deterministic whole-module coverage result. RawPass is
// true only when every measured module is strictly above Minimum.
type GlobalReport struct {
	Modules           []ModuleReport     `json:"modules"`
	RawPass           bool               `json:"raw_pass"`
	Minimum           float64            `json:"minimum"`
	AppliedExclusions []AppliedExclusion `json:"applied_exclusions"`
}

type globalPackageKey struct {
	module      string
	packagePath string
}

// globalBlock preserves the entire coverage source range. The legacy target
// reporter keys blocks by start line for focus attribution; global accounting
// must not do that because nearby or overlapping blocks are distinct statements.
type globalBlock struct {
	file       string
	key        string
	statements int
	count      int
}

// ReadGlobalProfiles reads the Task 4 replay manifest. It is headerless UTF-8
// TSV with one row per package-local profile:
//
//	module<TAB>package-relative-path<TAB>profile-path
//
// Blank lines and comments are accepted to make generated manifests readable.
func ReadGlobalProfiles(r io.Reader) ([]GlobalProfile, error) {
	var profiles []GlobalProfile
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<24)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(raw, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("global profile manifest line %d: want 3 tab-separated fields, got %q", lineNo, line)
		}
		module, err := cleanGlobalModule(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, fmt.Errorf("global profile manifest line %d: %w", lineNo, err)
		}
		pkg, err := cleanGlobalPackage(strings.TrimSpace(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("global profile manifest line %d: %w", lineNo, err)
		}
		profilePath := strings.TrimSpace(fields[2])
		if profilePath == "" {
			return nil, fmt.Errorf("global profile manifest line %d: profile path is empty", lineNo)
		}
		key := module + "\x00" + pkg + "\x00" + profilePath
		if seen[key] {
			return nil, fmt.Errorf("global profile manifest line %d: duplicate profile %s:%s:%s", lineNo, module, pkg, profilePath)
		}
		seen[key] = true
		profiles = append(profiles, GlobalProfile{Module: module, Package: pkg, Path: profilePath})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("global profile manifest contains no profiles")
	}
	return profiles, nil
}

// ResolveGlobalProfiles enriches the external three-column profile manifest
// with the module import paths read directly from the workspace's go.mod files.
// Coverage profiles are normally written in a temporary directory, so their
// paths cannot establish ownership safely. ReportGlobal requires this resolved
// ownership before it will accept profile blocks.
func ResolveGlobalProfiles(repoRoot string, profiles []GlobalProfile) ([]GlobalProfile, error) {
	root, err := fuzzcovSystem.abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	modulePaths := map[string]string{}
	resolved := make([]GlobalProfile, 0, len(profiles))
	for _, profile := range profiles {
		module, err := cleanGlobalModule(profile.Module)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", profile.Path, err)
		}
		pkg, err := cleanGlobalPackage(profile.Package)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", profile.Path, err)
		}
		if strings.TrimSpace(profile.Path) == "" {
			return nil, fmt.Errorf("profile for %s:%s has an empty path", module, pkg)
		}
		modulePath, ok := modulePaths[module]
		if !ok {
			moduleDir, err := joinWithin(root, module)
			if err != nil {
				return nil, fmt.Errorf("resolve module %s: %w", module, err)
			}
			content, err := fuzzcovSystem.readFile(filepath.Join(moduleDir, "go.mod"))
			if err != nil {
				return nil, fmt.Errorf("read module %s go.mod: %w", module, err)
			}
			modulePath, err = cleanGlobalModulePath(modulePathFromGoMod(content))
			if err != nil {
				return nil, fmt.Errorf("module %s go.mod: %w", module, err)
			}
			modulePaths[module] = modulePath
		}
		profile.Module = module
		profile.Package = pkg
		profile.ModulePath = modulePath
		resolved = append(resolved, profile)
	}
	return resolved, nil
}

// ReadGlobalExclusions resolves and validates the reviewed whole-file manifest:
//
//	module<TAB>package-relative-path<TAB>file<TAB>generated|platform<TAB>reason
//
// Generated entries require the standard Code generated header. Platform entries
// require a production .go file which the coverage replay build context cannot
// select.
func ReadGlobalExclusions(repoRoot string, r io.Reader) ([]Exclusion, error) {
	root, err := fuzzcovSystem.abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	var exclusions []Exclusion
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<24)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(raw, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("global exclusion manifest line %d: want 5 tab-separated fields, got %q", lineNo, line)
		}
		module, err := cleanGlobalModule(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, fmt.Errorf("global exclusion manifest line %d: %w", lineNo, err)
		}
		pkg, err := cleanGlobalPackage(strings.TrimSpace(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("global exclusion manifest line %d: %w", lineNo, err)
		}
		file := strings.TrimSpace(fields[2])
		if err := validateExclusionFilename(file); err != nil {
			return nil, fmt.Errorf("global exclusion manifest line %d: %w", lineNo, err)
		}
		kind := strings.TrimSpace(fields[3])
		if kind != "generated" && kind != "platform" {
			return nil, fmt.Errorf("global exclusion manifest line %d: kind %q must be generated or platform", lineNo, kind)
		}
		reason := strings.TrimSpace(fields[4])
		if reason == "" {
			return nil, fmt.Errorf("global exclusion manifest line %d: reason is required", lineNo)
		}
		key := module + "\x00" + pkg + "\x00" + file
		if seen[key] {
			return nil, fmt.Errorf("global exclusion manifest line %d: duplicate exclusion for %s:%s:%s", lineNo, module, pkg, file)
		}
		seen[key] = true

		exclusion, err := resolveGlobalExclusion(root, module, pkg, file, kind, reason)
		if err != nil {
			return nil, fmt.Errorf("global exclusion manifest line %d: %w", lineNo, err)
		}
		exclusions = append(exclusions, exclusion)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return exclusions, nil
}

func readGlobalProfilesFile(filename string) ([]GlobalProfile, error) {
	f, err := fuzzcovSystem.open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadGlobalProfiles(f)
}

func readGlobalExclusionsFile(repoRoot, filename string) ([]Exclusion, error) {
	f, err := os.Open(filename)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("open global exclusions %s: %w", filename, err)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadGlobalExclusions(repoRoot, f)
}

// ReportGlobal merges profile blocks only inside their declared package. A block
// is covered when any replay profile has a positive count for the same exact
// source range and statement count. It intentionally never coalesces blocks by
// line number or across packages.
func ReportGlobal(profiles []GlobalProfile, exclusions []Exclusion, minimum float64) (GlobalReport, error) {
	if err := validateGlobalMinimum(minimum); err != nil {
		return GlobalReport{}, err
	}
	if len(profiles) == 0 {
		return GlobalReport{}, fmt.Errorf("cannot report global coverage without profiles")
	}

	packages := map[globalPackageKey]map[string]globalBlock{}
	profileOwners := map[string]globalPackageKey{}
	blockOwners := map[string]globalPackageKey{}
	modulePaths := map[string]string{}
	for _, profile := range profiles {
		module, err := cleanGlobalModule(profile.Module)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("profile %q: %w", profile.Path, err)
		}
		pkg, err := cleanGlobalPackage(profile.Package)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("profile %q: %w", profile.Path, err)
		}
		if strings.TrimSpace(profile.Path) == "" {
			return GlobalReport{}, fmt.Errorf("profile for %s:%s has an empty path", module, pkg)
		}
		modulePath, err := cleanGlobalModulePath(profile.ModulePath)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("profile %s:%s (%s) has unresolved module ownership: %w", module, pkg, profile.Path, err)
		}
		if previous, ok := modulePaths[module]; ok && previous != modulePath {
			return GlobalReport{}, fmt.Errorf("declared module %s has conflicting resolved import paths %s and %s", module, previous, modulePath)
		}
		modulePaths[module] = modulePath
		absolutePath, err := fuzzcovSystem.abs(profile.Path)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("resolve profile %q: %w", profile.Path, err)
		}
		key := globalPackageKey{module: module, packagePath: pkg}
		if owner, ok := profileOwners[absolutePath]; ok {
			return GlobalReport{}, fmt.Errorf("profile %q is assigned more than once (%s:%s and %s:%s)", profile.Path, owner.module, owner.packagePath, module, pkg)
		}
		profileOwners[absolutePath] = key

		blocks, err := readGlobalCoverageProfile(profile.Path)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("read profile %s:%s (%s): %w", module, pkg, profile.Path, err)
		}
		union := packages[key]
		if union == nil {
			union = map[string]globalBlock{}
			packages[key] = union
		}
		for _, block := range blocks {
			if err := validateGlobalBlockOwnership(block, modulePath, pkg); err != nil {
				return GlobalReport{}, fmt.Errorf("profile %s:%s (%s): %w", module, pkg, profile.Path, err)
			}
			if owner, ok := blockOwners[block.file]; ok && owner != key {
				return GlobalReport{}, fmt.Errorf("profile block %s is assigned to multiple declared packages (%s:%s and %s:%s)", block.file, owner.module, owner.packagePath, module, pkg)
			}
			blockOwners[block.file] = key
			if previous, ok := union[block.key]; ok {
				if block.count > previous.count {
					previous.count = block.count
					union[block.key] = previous
				}
				continue
			}
			union[block.key] = block
		}
	}

	excluded := map[globalPackageKey]map[string]bool{}
	applied := make([]AppliedExclusion, 0, len(exclusions))
	seenExclusions := map[string]bool{}
	for _, exclusion := range exclusions {
		module, err := cleanGlobalModule(exclusion.Module)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("exclusion %q: %w", exclusion.File, err)
		}
		pkg, err := cleanGlobalPackage(exclusion.Package)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("exclusion %q: %w", exclusion.File, err)
		}
		if err := validateExclusionFilename(exclusion.File); err != nil {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s: %w", module, pkg, err)
		}
		if exclusion.Kind != "generated" && exclusion.Kind != "platform" {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s:%s has invalid kind %q", module, pkg, exclusion.File, exclusion.Kind)
		}
		if strings.TrimSpace(exclusion.Reason) == "" {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s:%s has no reason", module, pkg, exclusion.File)
		}
		if err := validateResolvedExclusion(exclusion); err != nil {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s:%s: %w", module, pkg, exclusion.File, err)
		}
		identity := module + "\x00" + pkg + "\x00" + exclusion.File
		if seenExclusions[identity] {
			return GlobalReport{}, fmt.Errorf("duplicate exclusion for %s:%s:%s", module, pkg, exclusion.File)
		}
		seenExclusions[identity] = true

		packageKey := globalPackageKey{module: module, packagePath: pkg}
		union := packages[packageKey]
		if union == nil {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s:%s names a package without a profile", module, pkg, exclusion.File)
		}
		profileFile := exclusion.ProfileFile
		marked := excluded[packageKey]
		if marked == nil {
			marked = map[string]bool{}
			excluded[packageKey] = marked
		}
		blocks, statements := 0, 0
		for blockKey, block := range union {
			if block.file != profileFile {
				continue
			}
			marked[blockKey] = true
			blocks++
			statements += block.statements
		}
		if blocks == 0 || statements == 0 {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s:%s removes zero profile blocks", module, pkg, exclusion.File)
		}
		applied = append(applied, AppliedExclusion{
			Module: module, Package: pkg, File: exclusion.File, Kind: exclusion.Kind, Reason: exclusion.Reason,
			Blocks: blocks, Statements: statements,
		})
	}

	keys := make([]globalPackageKey, 0, len(packages))
	for key := range packages {
		keys = append(keys, key)
	}
	sortGlobalPackageKeys(keys)

	moduleReports := map[string]*ModuleReport{}
	for _, key := range keys {
		covered, total := 0, 0
		for blockKey, block := range packages[key] {
			if excluded[key][blockKey] {
				continue
			}
			total += block.statements
			if block.count > 0 {
				covered += block.statements
			}
		}
		packageReport := PackageReport{
			Module: key.module, Package: key.packagePath, Covered: covered, Total: total, Percent: globalPercent(covered, total),
		}
		moduleReport := moduleReports[key.module]
		if moduleReport == nil {
			moduleReport = &ModuleReport{Module: key.module}
			moduleReports[key.module] = moduleReport
		}
		moduleReport.Covered += covered
		moduleReport.Total += total
		moduleReport.Packages = append(moduleReport.Packages, packageReport)
	}

	moduleNames := make([]string, 0, len(moduleReports))
	for module := range moduleReports {
		moduleNames = append(moduleNames, module)
	}
	sort.Strings(moduleNames)
	report := GlobalReport{Minimum: minimum, RawPass: true}
	for _, module := range moduleNames {
		moduleReport := moduleReports[module]
		if moduleReport.Total == 0 {
			return GlobalReport{}, fmt.Errorf("module %s has zero profile statements after exclusions", module)
		}
		moduleReport.Percent = globalPercent(moduleReport.Covered, moduleReport.Total)
		moduleReport.Pass = globalStrictlyExceeds(moduleReport.Covered, moduleReport.Total, minimum)
		if !moduleReport.Pass {
			report.RawPass = false
		}
		report.Modules = append(report.Modules, *moduleReport)
	}
	sortAppliedExclusions(applied)
	report.AppliedExclusions = applied
	return report, nil
}

func sortGlobalPackageKeys(keys []globalPackageKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].module != keys[j].module {
			return keys[i].module < keys[j].module
		}
		return keys[i].packagePath < keys[j].packagePath
	})
}

func sortAppliedExclusions(applied []AppliedExclusion) {
	sort.Slice(applied, func(i, j int) bool {
		if applied[i].Module != applied[j].Module {
			return applied[i].Module < applied[j].Module
		}
		if applied[i].Package != applied[j].Package {
			return applied[i].Package < applied[j].Package
		}
		return applied[i].File < applied[j].File
	})
}

// globalFloor stores an exact coverage fraction. Existing floor files use
// decimal percentages, while newly raised floors are serialized as exact
// covered/total ratios so an identical replay cannot regress due to rounding.
type globalFloor struct {
	ratio      *big.Rat
	serialized string
}

// ReadGlobalFloors reads a module ratchet. Existing decimal percentages remain
// valid; a slash denotes an exact covered/total ratio. It intentionally shares
// no state with the older per-target focus floors.
func ReadGlobalFloors(r io.Reader) (map[string]globalFloor, error) {
	floors := map[string]globalFloor{}
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("global floors line %d: want \"module percent-or-ratio\", got %q", lineNo, line)
		}
		module, err := cleanGlobalModule(fields[0])
		if err != nil {
			return nil, fmt.Errorf("global floors line %d: %w", lineNo, err)
		}
		if _, exists := floors[module]; exists {
			return nil, fmt.Errorf("global floors line %d: duplicate floor for %s", lineNo, module)
		}
		floor, err := parseGlobalFloor(fields[1])
		if err != nil {
			return nil, fmt.Errorf("global floors line %d: %w", lineNo, err)
		}
		floors[module] = floor
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return floors, nil
}

func parseGlobalFloor(value string) (globalFloor, error) {
	value = strings.TrimSpace(value)
	ratio, ok := new(big.Rat).SetString(value)
	if !ok {
		return globalFloor{}, fmt.Errorf("invalid floor %q", value)
	}
	if strings.Contains(value, "/") {
		if ratio.Sign() < 0 || ratio.Cmp(big.NewRat(1, 1)) > 0 {
			return globalFloor{}, fmt.Errorf("ratio %q must be in [0, 1]", value)
		}
		return globalFloor{ratio: ratio, serialized: ratio.RatString()}, nil
	}
	if ratio.Sign() < 0 || ratio.Cmp(big.NewRat(100, 1)) > 0 {
		return globalFloor{}, fmt.Errorf("percentage %q must be in [0, 100]", value)
	}
	return globalFloor{
		ratio:      new(big.Rat).Quo(ratio, big.NewRat(100, 1)),
		serialized: value,
	}, nil
}

func globalFloorFromCoverage(covered, total int) globalFloor {
	ratio := new(big.Rat).SetFrac(big.NewInt(int64(covered)), big.NewInt(int64(total)))
	return globalFloor{ratio: ratio, serialized: ratio.RatString()}
}

func globalFloorPercent(floor globalFloor) float64 {
	if floor.ratio == nil {
		return 0
	}
	percent, _ := new(big.Rat).Mul(floor.ratio, big.NewRat(100, 1)).Float64()
	return percent
}

func globalFloorText(floor globalFloor) string {
	if floor.serialized != "" {
		return floor.serialized
	}
	if floor.ratio == nil {
		return "0"
	}
	return floor.ratio.RatString()
}

func readGlobalFloorsFile(filename string) (map[string]globalFloor, error) {
	f, err := os.Open(filename)
	if os.IsNotExist(err) {
		return map[string]globalFloor{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadGlobalFloors(f)
}

// RaiseGlobalFloors returns a copy of old with only passing modules raised to
// their measured ratio. A failed raw threshold can never create or lower a floor.
func RaiseGlobalFloors(old map[string]globalFloor, report GlobalReport) map[string]globalFloor {
	raised := make(map[string]globalFloor, len(old)+len(report.Modules))
	for module, floor := range old {
		raised[module] = floor
	}
	for _, module := range report.Modules {
		if !module.Pass || module.Total == 0 {
			continue
		}
		current := globalFloorFromCoverage(module.Covered, module.Total)
		existing, ok := raised[module.Module]
		if !ok || existing.ratio == nil || current.ratio.Cmp(existing.ratio) > 0 {
			raised[module.Module] = current
		}
	}
	return raised
}

func writeGlobalFloorsFile(filename string, floors map[string]globalFloor) error {
	modules := make([]string, 0, len(floors))
	for module := range floors {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	var out strings.Builder
	out.WriteString("# Global fuzz-reachable coverage floors (whole-module statement coverage).\n")
	out.WriteString("# Managed by serf-fuzzcov global mode. Floors rise only after raw coverage is strictly above 95.0%.\n")
	out.WriteString("# Legacy decimal percentages are accepted; blessed floors use exact covered/total ratios.\n")
	out.WriteString("# A blessing never lowers an existing floor.\n")
	for _, module := range modules {
		fmt.Fprintf(&out, "%s %s\n", module, globalFloorText(floors[module]))
	}
	return fuzzcovSystem.writeFile(filename, []byte(out.String()), 0o644)
}

// PrintGlobalReport emits the human-readable companion to WriteGlobalReportJSON.
// Both forms retain every applied exclusion rather than silently changing totals.
func PrintGlobalReport(w io.Writer, report GlobalReport) {
	fmt.Fprintf(w, "GLOBAL FUZZ-REACHABLE COVERAGE (raw threshold >%.4f%%)\n\n", report.Minimum)
	fmt.Fprintf(w, "  %-12s %12s %12s %10s %8s\n", "MODULE", "COVERED", "TOTAL", "RAW %", "STATUS")
	for _, module := range report.Modules {
		status := "FAIL"
		if module.Pass {
			status = "PASS"
		}
		fmt.Fprintf(w, "  %-12s %12d %12d %9.4f%% %8s\n", module.Module, module.Covered, module.Total, module.Percent, status)
		for _, pkg := range module.Packages {
			fmt.Fprintf(w, "    %-10s %-32s %8d/%-8d %9.4f%%\n", "package", pkg.Package, pkg.Covered, pkg.Total, pkg.Percent)
		}
	}
	fmt.Fprintln(w)
	if len(report.AppliedExclusions) == 0 {
		fmt.Fprintln(w, "APPLIED EXCLUSIONS: none")
	} else {
		fmt.Fprintln(w, "APPLIED EXCLUSIONS")
		for _, exclusion := range report.AppliedExclusions {
			fmt.Fprintf(w, "  %s:%s/%s [%s] %d statement(s), %d block(s): %s\n",
				exclusion.Module, exclusion.Package, exclusion.File, exclusion.Kind,
				exclusion.Statements, exclusion.Blocks, exclusion.Reason)
		}
	}
	status := "FAIL"
	if report.RawPass {
		status = "PASS"
	}
	fmt.Fprintf(w, "\nRAW GLOBAL THRESHOLD: %s\n", status)
}

// WriteGlobalReportJSON emits the same raw totals and applied exclusions as the
// text report. Encoder output is stable enough for machine consumption because
// ReportGlobal sorts every slice before returning it.
func WriteGlobalReportJSON(w io.Writer, report GlobalReport) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(report)
}

type globalModeOptions struct {
	manifestPath   string
	exclusionsPath string
	floorsPath     string
	repoRoot       string
	minimum        float64
	check          bool
	bless          bool
	json           bool
}

// runGlobalMode keeps the command-line branch small and testable. Every global
// invocation enforces the repository-wide strict >95.0% raw threshold; --check
// turns a measured breach into a non-zero status. Blessing rewrites floors only
// after every module clears that threshold.
func runGlobalMode(options globalModeOptions, stdout, stderr io.Writer) (int, error) {
	if err := validateGlobalModeMinimum(options.minimum); err != nil {
		return 0, err
	}
	profiles, err := readGlobalProfilesFile(options.manifestPath)
	if err != nil {
		return 0, fmt.Errorf("read global profile manifest: %w", err)
	}
	profiles, err = ResolveGlobalProfiles(options.repoRoot, profiles)
	if err != nil {
		return 0, fmt.Errorf("resolve global profile ownership: %w", err)
	}
	exclusions, err := readGlobalExclusionsFile(options.repoRoot, options.exclusionsPath)
	if err != nil {
		return 0, fmt.Errorf("read global exclusions: %w", err)
	}
	report, err := ReportGlobal(profiles, exclusions, options.minimum)
	if err != nil {
		return 0, fmt.Errorf("account global coverage: %w", err)
	}
	floors, err := readGlobalFloorsFile(options.floorsPath)
	if err != nil {
		return 0, fmt.Errorf("read global floors: %w", err)
	}

	if options.json {
		if err := WriteGlobalReportJSON(stdout, report); err != nil {
			return 0, fmt.Errorf("write global JSON report: %w", err)
		}
	} else {
		PrintGlobalReport(stdout, report)
	}

	code := 0
	if options.check && !report.RawPass {
		fmt.Fprintf(stderr, "serf-fuzzcov: RAW THRESHOLD BREACH: every module must be strictly above %.4f%%\n", options.minimum)
		code = 1
	}
	if options.check || options.bless {
		for _, module := range report.Modules {
			floor, ok := floors[module.Module]
			if !ok || globalMeetsFloor(module.Covered, module.Total, floor) {
				continue
			}
			fmt.Fprintf(stderr, "serf-fuzzcov: REGRESSION %s: raw %.4f%% < floor %.4f%%\n", module.Module, module.Percent, globalFloorPercent(floor))
			code = 1
		}
	}
	if options.bless {
		if !report.RawPass {
			fmt.Fprintf(stderr, "serf-fuzzcov: refusing to bless: every module must be strictly above %.4f%%\n", options.minimum)
			return 1, nil
		}
		if err := writeGlobalFloorsFile(options.floorsPath, RaiseGlobalFloors(floors, report)); err != nil {
			return 0, fmt.Errorf("write global floors: %w", err)
		}
		if options.json {
			fmt.Fprintf(stderr, "serf-fuzzcov: raised global floors in %s\n", options.floorsPath)
		} else {
			fmt.Fprintf(stdout, "serf-fuzzcov: raised global floors in %s\n", options.floorsPath)
		}
	}
	return code, nil
}

func readGlobalCoverageProfile(filename string) ([]globalBlock, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<24)
	if !sc.Scan() {
		return nil, fmt.Errorf("empty profile")
	}
	if mode := strings.TrimSpace(sc.Text()); mode != "mode: set" {
		return nil, fmt.Errorf("profile has mode %q; only \"mode: set\" is supported", mode)
	}
	var blocks []globalBlock
	lineNo := 1
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		block, err := parseGlobalBlock(line)
		if err != nil {
			return nil, fmt.Errorf("profile line %d: %w", lineNo, err)
		}
		blocks = append(blocks, block)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return blocks, nil
}

func parseGlobalBlock(line string) (globalBlock, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return globalBlock{}, fmt.Errorf("malformed block line %q", line)
	}
	location := fields[0]
	colon := strings.LastIndex(location, ":")
	if colon <= 0 {
		return globalBlock{}, fmt.Errorf("missing source position in %q", line)
	}
	file := location[:colon]
	startEnd := location[colon+1:]
	start, end, ok := strings.Cut(startEnd, ",")
	if !ok {
		return globalBlock{}, fmt.Errorf("missing source range in %q", line)
	}
	startLine, startColumn, err := parseGlobalPosition(start)
	if err != nil {
		return globalBlock{}, fmt.Errorf("bad start position in %q: %w", line, err)
	}
	endLine, endColumn, err := parseGlobalPosition(end)
	if err != nil {
		return globalBlock{}, fmt.Errorf("bad end position in %q: %w", line, err)
	}
	if endLine < startLine || (endLine == startLine && endColumn < startColumn) {
		return globalBlock{}, fmt.Errorf("backward source range in %q", line)
	}
	statements, err := strconv.Atoi(fields[1])
	if err != nil || statements <= 0 {
		return globalBlock{}, fmt.Errorf("invalid statement count %q", fields[1])
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil || count < 0 {
		return globalBlock{}, fmt.Errorf("invalid execution count %q", fields[2])
	}
	key := fmt.Sprintf("%s:%d.%d,%d.%d %d", file, startLine, startColumn, endLine, endColumn, statements)
	return globalBlock{file: file, key: key, statements: statements, count: count}, nil
}

func parseGlobalPosition(value string) (int, int, error) {
	line, column, ok := strings.Cut(value, ".")
	if !ok || line == "" || column == "" {
		return 0, 0, fmt.Errorf("want line.column")
	}
	lineNumber, err := strconv.Atoi(line)
	if err != nil || lineNumber <= 0 {
		return 0, 0, fmt.Errorf("invalid line %q", line)
	}
	columnNumber, err := strconv.Atoi(column)
	if err != nil || columnNumber <= 0 {
		return 0, 0, fmt.Errorf("invalid column %q", column)
	}
	return lineNumber, columnNumber, nil
}

func resolveGlobalExclusion(repoRoot, module, pkg, file, kind, reason string) (Exclusion, error) {
	moduleDir, err := joinWithin(repoRoot, module)
	if err != nil {
		return Exclusion{}, err
	}
	packageDir, err := joinWithin(moduleDir, strings.TrimPrefix(pkg, "./"))
	if err != nil {
		return Exclusion{}, err
	}
	sourcePath, err := joinWithin(packageDir, file)
	if err != nil {
		return Exclusion{}, err
	}
	moduleContents, err := fuzzcovSystem.readFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return Exclusion{}, fmt.Errorf("read module go.mod: %w", err)
	}
	modulePath := modulePathFromGoMod(moduleContents)
	if modulePath == "" {
		return Exclusion{}, fmt.Errorf("module %s has no module path", module)
	}

	exclusion := Exclusion{
		Module: module, Package: pkg, File: file, Kind: kind, Reason: reason,
		SourcePath:  sourcePath,
		ProfileFile: joinImport(modulePath, path.Join(pkgSubdir(pkg), file)),
	}
	if err := validateResolvedExclusion(exclusion); err != nil {
		return Exclusion{}, err
	}
	return exclusion, nil
}

// validateResolvedExclusion is called at both the manifest boundary and the
// accounting boundary. Keeping the second check prevents a programmatic caller
// from presenting an ordinary production file as a hand-built Exclusion.
func validateResolvedExclusion(exclusion Exclusion) error {
	if exclusion.SourcePath == "" || exclusion.ProfileFile == "" {
		return fmt.Errorf("exclusion is not resolved to one production source file")
	}
	if filepath.Base(exclusion.SourcePath) != exclusion.File {
		return fmt.Errorf("resolved source %s does not match manifest file %s", exclusion.SourcePath, exclusion.File)
	}
	info, err := fuzzcovSystem.stat(exclusion.SourcePath)
	if err != nil {
		return fmt.Errorf("source file %s: %w", exclusion.SourcePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source file %s is not a regular file", exclusion.SourcePath)
	}
	modulePath, sourcePackage, err := sourceModuleAndPackage(exclusion.SourcePath)
	if err != nil {
		return err
	}
	manifestPackage, err := cleanGlobalPackage(exclusion.Package)
	if err != nil {
		return err
	}
	if sourcePackage != manifestPackage {
		return fmt.Errorf("resolved source package %s does not match manifest package %s", sourcePackage, manifestPackage)
	}
	expectedProfileFile := joinImport(modulePath, path.Join(pkgSubdir(sourcePackage), exclusion.File))
	if exclusion.ProfileFile != expectedProfileFile {
		return fmt.Errorf("resolved source maps to profile file %s, not %s", expectedProfileFile, exclusion.ProfileFile)
	}
	switch exclusion.Kind {
	case "generated":
		generated, err := hasCodeGeneratedHeader(exclusion.SourcePath)
		if err != nil {
			return err
		}
		if !generated {
			return fmt.Errorf("source file %s is not generated (missing Code generated header)", exclusion.SourcePath)
		}
	case "platform":
		coverageBuild := globalCoverageBuildContext()
		matched, err := coverageBuild.MatchFile(filepath.Dir(exclusion.SourcePath), exclusion.File)
		if err != nil {
			return fmt.Errorf("evaluate platform constraints for %s: %w", exclusion.SourcePath, err)
		}
		if matched {
			return fmt.Errorf("source file %s is available on %s/%s with serffuzz; platform exclusions require an unavailable build-constrained file", exclusion.SourcePath, coverageBuild.GOOS, coverageBuild.GOARCH)
		}
		platformDerived, err := hasPlatformDerivedUnavailability(exclusion.SourcePath, exclusion.File, coverageBuild)
		if err != nil {
			return err
		}
		if !platformDerived {
			return fmt.Errorf("source file %s is unavailable with serffuzz but not because of a GOOS/GOARCH filename suffix or platform-only build constraint", exclusion.SourcePath)
		}
	default:
		return fmt.Errorf("kind %q must be generated or platform", exclusion.Kind)
	}
	return nil
}

// globalCoverageBuildContext matches the build settings used by the deterministic
// coverage replay. Platform exclusions are valid only for production files that
// replay cannot compile.
func globalCoverageBuildContext() build.Context {
	ctx := build.Default
	ctx.BuildTags = append([]string(nil), ctx.BuildTags...)
	ctx.BuildTags = append(ctx.BuildTags, "serffuzz")
	return ctx
}

// globalPlatformBuildTags mirrors go/build's unexported historical GOOS and
// GOARCH tag lists as of Go 1.25. It deliberately excludes synthetic and
// feature tags such as unix, cgo, compiler, go1.N, serffuzz, and arbitrary
// user tags: those tags cannot justify excluding ordinary production source.
var globalPlatformBuildTags = map[string]struct{}{
	"aix": {}, "android": {}, "darwin": {}, "dragonfly": {}, "freebsd": {}, "hurd": {}, "illumos": {}, "ios": {}, "js": {}, "linux": {}, "nacl": {}, "netbsd": {}, "openbsd": {}, "plan9": {}, "solaris": {}, "wasip1": {}, "windows": {}, "zos": {},
	"386": {}, "amd64": {}, "amd64p32": {}, "arm": {}, "armbe": {}, "arm64": {}, "arm64be": {}, "loong64": {}, "mips": {}, "mipsle": {}, "mips64": {}, "mips64le": {}, "mips64p32": {}, "mips64p32le": {}, "ppc": {}, "ppc64": {}, "ppc64le": {}, "riscv": {}, "riscv64": {}, "s390": {}, "s390x": {}, "sparc": {}, "sparc64": {}, "wasm": {},
}

// hasPlatformDerivedUnavailability proves that an unavailable replay source is
// unavailable for a real platform reason. MatchFile alone is not sufficient:
// a file hidden by !serffuzz, cgo, a release tag, or an arbitrary feature tag
// is still ordinary production source and must remain in the denominator.
func hasPlatformDerivedUnavailability(sourcePath, file string, coverageBuild build.Context) (bool, error) {
	expressions, err := leadingBuildConstraintExpressions(sourcePath)
	if err != nil {
		return false, err
	}
	for _, expression := range expressions {
		tags := map[string]bool{}
		collectBuildConstraintTags(expression, tags)
		for tag := range tags {
			if !isGlobalPlatformBuildTag(tag) {
				return false, nil
			}
		}
	}

	filenameUnavailable := hasUnavailablePlatformFilenameSuffix(sourcePath, file, coverageBuild)
	// A genuine filename suffix can justify exclusion on its own. When a source
	// header is present, it must already have passed the platform-only check
	// above; otherwise an unrelated replay, cgo, or feature condition could
	// hide ordinary production code behind an unavailable filename suffix.
	return filenameUnavailable || len(expressions) > 0, nil
}

func isGlobalPlatformBuildTag(tag string) bool {
	_, ok := globalPlatformBuildTags[tag]
	return ok
}

// hasUnavailablePlatformFilenameSuffix asks go/build to evaluate the source
// filename against the replay context while replacing its contents with a
// neutral package declaration. This retains Go's exact suffix and compatibility
// rules without letting !serffuzz or another source build constraint masquerade
// as the filename's platform reason.
func hasUnavailablePlatformFilenameSuffix(sourcePath, file string, coverageBuild build.Context) bool {
	if !hasGlobalPlatformFilenameSuffix(file) {
		return false
	}
	filenameOnly := coverageBuild
	filenameOnly.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("package fuzzcov\n")), nil
	}
	matched, _ := filenameOnly.MatchFile(filepath.Dir(sourcePath), file)
	return !matched
}

// hasGlobalPlatformFilenameSuffix is a narrow precondition for the synthetic
// MatchFile check above: MatchFile can reject names beginning with '_' or '.',
// which is not a platform reason. Go treats a recognized final GOOS/GOARCH
// token after an underscore as its filename platform suffix.
func hasGlobalPlatformFilenameSuffix(file string) bool {
	name := strings.TrimSuffix(file, ".go")
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return false
	}
	firstUnderscore := strings.Index(name, "_")
	if firstUnderscore < 1 || firstUnderscore == len(name)-1 {
		return false
	}
	parts := strings.Split(name[firstUnderscore+1:], "_")
	_, ok := globalPlatformBuildTags[parts[len(parts)-1]]
	return ok
}

// leadingBuildConstraintExpressions returns directives from the leading comment
// block only, matching where Go permits build constraints. A later comment that
// happens to mention a platform tag cannot turn a non-platform exclusion into a
// valid one.
func leadingBuildConstraintExpressions(filename string) ([]constraint.Expr, error) {
	f, err := fuzzcovSystem.open(filename)
	if err != nil {
		return nil, fmt.Errorf("open source file %s: %w", filename, err)
	}
	defer func() { _ = f.Close() }()

	var expressions []constraint.Expr
	scanner := bufio.NewScanner(f)
	inBlockComment := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if inBlockComment {
			if end := strings.Index(line, "*/"); end >= 0 {
				inBlockComment = false
				if strings.TrimSpace(line[end+2:]) != "" {
					return expressions, nil
				}
			}
			continue
		}
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "//"):
			if !constraint.IsGoBuild(line) && !constraint.IsPlusBuild(line) {
				continue
			}
			expression, err := constraint.Parse(line)
			if err != nil {
				return nil, fmt.Errorf("parse build constraint in %s: %w", filename, err)
			}
			expressions = append(expressions, expression)
		case strings.HasPrefix(line, "/*"):
			if end := strings.Index(line, "*/"); end < 0 {
				inBlockComment = true
			} else if strings.TrimSpace(line[end+2:]) != "" {
				return expressions, nil
			}
		default:
			return expressions, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read source file %s: %w", filename, err)
	}
	return expressions, nil
}

func collectBuildConstraintTags(expression constraint.Expr, tags map[string]bool) {
	switch expression := expression.(type) {
	case *constraint.TagExpr:
		tags[expression.Tag] = true
	case *constraint.NotExpr:
		collectBuildConstraintTags(expression.X, tags)
	case *constraint.AndExpr:
		collectBuildConstraintTags(expression.X, tags)
		collectBuildConstraintTags(expression.Y, tags)
	case *constraint.OrExpr:
		collectBuildConstraintTags(expression.X, tags)
		collectBuildConstraintTags(expression.Y, tags)
	}
}

// sourceModuleAndPackage derives a source file's module import path and package
// relpath from the nearest enclosing go.mod. ReportGlobal uses this independently
// of manifest parsing so a constructed Exclusion cannot point a generated file at
// a different ordinary profile filename.
func sourceModuleAndPackage(sourcePath string) (string, string, error) {
	dir := filepath.Dir(sourcePath)
	for {
		goMod := filepath.Join(dir, "go.mod")
		if content, err := fuzzcovSystem.readFile(goMod); err == nil {
			modulePath := modulePathFromGoMod(content)
			if modulePath == "" {
				return "", "", fmt.Errorf("module %s has no module path", dir)
			}
			rel, err := fuzzcovSystem.rel(dir, filepath.Dir(sourcePath))
			if err != nil {
				return "", "", err
			}
			if rel == "." {
				return modulePath, ".", nil
			}
			return modulePath, "./" + filepath.ToSlash(rel), nil
		} else if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("read module go.mod %s: %w", goMod, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", fmt.Errorf("source file %s is not inside a Go module", sourcePath)
}

func hasCodeGeneratedHeader(filename string) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse source file %s: %w", filename, err)
	}
	return ast.IsGenerated(file), nil
}

func validateGlobalBlockOwnership(block globalBlock, modulePath, pkg string) error {
	expectedDir := joinImport(modulePath, pkgSubdir(pkg))
	if path.Dir(block.file) != expectedDir {
		return fmt.Errorf("profile block %s does not belong to declared module/package import directory %s", block.file, expectedDir)
	}
	if name := path.Base(block.file); name == "." || name == "/" || name == "" {
		return fmt.Errorf("profile block %s has no source filename", block.file)
	}
	return nil
}

func cleanGlobalModule(module string) (string, error) {
	if module == "" {
		return "", fmt.Errorf("module is empty")
	}
	if module == "." {
		return module, nil
	}
	if filepath.IsAbs(module) || strings.HasPrefix(module, "../") || module == ".." {
		return "", fmt.Errorf("module path %q must stay below the repository root", module)
	}
	clean := filepath.ToSlash(filepath.Clean(module))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("module path %q must stay below the repository root", module)
	}
	return clean, nil
}

func cleanGlobalModulePath(modulePath string) (string, error) {
	modulePath = strings.TrimSpace(modulePath)
	if modulePath == "" {
		return "", fmt.Errorf("module path is empty")
	}
	if strings.Contains(modulePath, `\`) || strings.HasPrefix(modulePath, "/") {
		return "", fmt.Errorf("invalid module path %q", modulePath)
	}
	clean := path.Clean(modulePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != modulePath {
		return "", fmt.Errorf("invalid module path %q", modulePath)
	}
	return clean, nil
}

func cleanGlobalPackage(pkg string) (string, error) {
	if pkg == "." {
		return pkg, nil
	}
	if !strings.HasPrefix(pkg, "./") {
		return "", fmt.Errorf("package path %q must be . or start with ./", pkg)
	}
	rel := strings.TrimPrefix(pkg, "./")
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("package path %q must stay below its module root", pkg)
	}
	return "./" + clean, nil
}

func validateExclusionFilename(file string) error {
	if file == "" {
		return fmt.Errorf("file is empty")
	}
	if strings.ContainsAny(file, `/\\`) || filepath.Base(file) != file {
		return fmt.Errorf("file %q must name one file directly in its package", file)
	}
	if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
		return fmt.Errorf("file %q must be a production .go file", file)
	}
	return nil
}

func joinWithin(root, child string) (string, error) {
	rootAbs, err := fuzzcovSystem.abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(rootAbs, filepath.FromSlash(child))
	rel, err := fuzzcovSystem.rel(rootAbs, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes %s", child, rootAbs)
	}
	return joined, nil
}

func validateGlobalMinimum(minimum float64) error {
	if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || minimum >= 100 {
		return fmt.Errorf("global minimum %.4f must be in [0, 100)", minimum)
	}
	return nil
}

// validateGlobalModeMinimum keeps ReportGlobal usable for focused accounting
// tests while preventing any command-line global-coverage mode from weakening
// the repository-wide >95.0% contract.
func validateGlobalModeMinimum(minimum float64) error {
	if err := validateGlobalMinimum(minimum); err != nil {
		return err
	}
	if minimum < 95.0 {
		return fmt.Errorf("global minimum %.4f must be at least 95.0 for global coverage", minimum)
	}
	return nil
}

func globalStrictlyExceeds(covered, total int, minimum float64) bool {
	if total <= 0 {
		return false
	}
	minimumRat, ok := new(big.Rat).SetString(strconv.FormatFloat(minimum, 'f', -1, 64))
	if !ok {
		return false
	}
	left := new(big.Int).Mul(big.NewInt(int64(covered)), big.NewInt(100))
	left.Mul(left, minimumRat.Denom())
	right := new(big.Int).Mul(big.NewInt(int64(total)), minimumRat.Num())
	return left.Cmp(right) > 0
}

func globalMeetsFloor(covered, total int, floor globalFloor) bool {
	if total <= 0 || floor.ratio == nil {
		return false
	}
	left := new(big.Int).Mul(big.NewInt(int64(covered)), floor.ratio.Denom())
	right := new(big.Int).Mul(big.NewInt(int64(total)), floor.ratio.Num())
	return left.Cmp(right) >= 0
}

func globalPercent(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(covered) / float64(total)
}
