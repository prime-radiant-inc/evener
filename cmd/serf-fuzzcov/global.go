package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
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
	Module  string
	Package string
	Path    string
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
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
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

// ReadGlobalExclusions resolves and validates the reviewed whole-file manifest:
//
//	module<TAB>package-relative-path<TAB>file<TAB>generated|platform<TAB>reason
//
// Generated entries require the standard Code generated header. Platform entries
// require a production .go file which the active Go build context cannot select.
func ReadGlobalExclusions(repoRoot string, r io.Reader) ([]Exclusion, error) {
	root, err := filepath.Abs(repoRoot)
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
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
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
	f, err := os.Open(filename)
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
		absolutePath, err := filepath.Abs(profile.Path)
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
		profileFile, err := findExclusionProfileFile(exclusion, union)
		if err != nil {
			return GlobalReport{}, fmt.Errorf("exclusion %s:%s:%s: %w", module, pkg, exclusion.File, err)
		}
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
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].module != keys[j].module {
			return keys[i].module < keys[j].module
		}
		return keys[i].packagePath < keys[j].packagePath
	})

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
	sort.Slice(applied, func(i, j int) bool {
		if applied[i].Module != applied[j].Module {
			return applied[i].Module < applied[j].Module
		}
		if applied[i].Package != applied[j].Package {
			return applied[i].Package < applied[j].Package
		}
		return applied[i].File < applied[j].File
	})
	report.AppliedExclusions = applied
	return report, nil
}

// ReadGlobalFloors reads a module->percentage ratchet. It intentionally shares
// no state with the older per-target focus floors.
func ReadGlobalFloors(r io.Reader) (map[string]float64, error) {
	floors := map[string]float64{}
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
			return nil, fmt.Errorf("global floors line %d: want \"module percent\", got %q", lineNo, line)
		}
		module, err := cleanGlobalModule(fields[0])
		if err != nil {
			return nil, fmt.Errorf("global floors line %d: %w", lineNo, err)
		}
		if _, exists := floors[module]; exists {
			return nil, fmt.Errorf("global floors line %d: duplicate floor for %s", lineNo, module)
		}
		floor, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 || floor > 100 {
			return nil, fmt.Errorf("global floors line %d: invalid percentage %q", lineNo, fields[1])
		}
		floors[module] = floor
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return floors, nil
}

func readGlobalFloorsFile(filename string) (map[string]float64, error) {
	f, err := os.Open(filename)
	if os.IsNotExist(err) {
		return map[string]float64{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadGlobalFloors(f)
}

// RaiseGlobalFloors returns a copy of old with only passing modules raised to
// their measured ratio. A failed raw threshold can never create or lower a floor.
func RaiseGlobalFloors(old map[string]float64, report GlobalReport) map[string]float64 {
	raised := make(map[string]float64, len(old)+len(report.Modules))
	for module, floor := range old {
		raised[module] = floor
	}
	for _, module := range report.Modules {
		if !module.Pass || module.Total == 0 {
			continue
		}
		current := globalPercent(module.Covered, module.Total)
		if current > raised[module.Module] {
			raised[module.Module] = current
		}
	}
	return raised
}

func writeGlobalFloorsFile(filename string, floors map[string]float64) error {
	modules := make([]string, 0, len(floors))
	for module := range floors {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	var out strings.Builder
	out.WriteString("# Global fuzz-reachable coverage floors (whole-module statement %).\n")
	out.WriteString("# Managed by serf-fuzzcov global mode. Floors rise only after raw coverage is strictly above 95.0%.\n")
	out.WriteString("# A blessing never lowers an existing floor.\n")
	for _, module := range modules {
		fmt.Fprintf(&out, "%s %s\n", module, strconv.FormatFloat(floors[module], 'f', -1, 64))
	}
	return os.WriteFile(filename, []byte(out.String()), 0o644)
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

// runGlobalMode keeps the command-line branch small and testable. Advisory runs
// still print an honest failing raw result; --check and --bless turn that result
// into a non-zero status. Blessing rewrites floors only after every module clears
// the strict raw threshold.
func runGlobalMode(options globalModeOptions, stdout, stderr io.Writer) (int, error) {
	profiles, err := readGlobalProfilesFile(options.manifestPath)
	if err != nil {
		return 0, fmt.Errorf("read global profile manifest: %w", err)
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
			fmt.Fprintf(stderr, "serf-fuzzcov: REGRESSION %s: raw %.4f%% < floor %.4f%%\n", module.Module, module.Percent, floor)
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
	moduleContents, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
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
	info, err := os.Stat(exclusion.SourcePath)
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
		matched, err := build.Default.MatchFile(filepath.Dir(exclusion.SourcePath), exclusion.File)
		if err != nil {
			return fmt.Errorf("evaluate platform constraints for %s: %w", exclusion.SourcePath, err)
		}
		if matched {
			return fmt.Errorf("source file %s is available on %s/%s; platform exclusions require an unavailable build-constrained file", exclusion.SourcePath, build.Default.GOOS, build.Default.GOARCH)
		}
	default:
		return fmt.Errorf("kind %q must be generated or platform", exclusion.Kind)
	}
	return nil
}

// sourceModuleAndPackage derives a source file's module import path and package
// relpath from the nearest enclosing go.mod. ReportGlobal uses this independently
// of manifest parsing so a constructed Exclusion cannot point a generated file at
// a different ordinary profile filename.
func sourceModuleAndPackage(sourcePath string) (string, string, error) {
	dir := filepath.Dir(sourcePath)
	for {
		goMod := filepath.Join(dir, "go.mod")
		if content, err := os.ReadFile(goMod); err == nil {
			modulePath := modulePathFromGoMod(content)
			if modulePath == "" {
				return "", "", fmt.Errorf("module %s has no module path", dir)
			}
			rel, err := filepath.Rel(dir, filepath.Dir(sourcePath))
			if err != nil {
				return "", "", err
			}
			if rel == "." {
				return modulePath, ".", nil
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", "", fmt.Errorf("source file %s escapes module %s", sourcePath, dir)
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

func findExclusionProfileFile(exclusion Exclusion, union map[string]globalBlock) (string, error) {
	if exclusion.ProfileFile == "" {
		return "", fmt.Errorf("exclusion is not resolved to a profile filename")
	}
	return exclusion.ProfileFile, nil
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
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(rootAbs, filepath.FromSlash(child))
	rel, err := filepath.Rel(rootAbs, joined)
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

func globalMeetsFloor(covered, total int, floor float64) bool {
	if total <= 0 {
		return false
	}
	floorRat, ok := new(big.Rat).SetString(strconv.FormatFloat(floor, 'f', -1, 64))
	if !ok {
		return false
	}
	left := new(big.Int).Mul(big.NewInt(int64(covered)), big.NewInt(100))
	left.Mul(left, floorRat.Denom())
	right := new(big.Int).Mul(big.NewInt(int64(total)), floorRat.Num())
	return left.Cmp(right) >= 0
}

func globalPercent(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(covered) / float64(total)
}
