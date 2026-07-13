// Command serf-fuzzregistry audits scripts/run-fuzz.sh's target manifest against
// the native and explicitly marked Rapid fuzz surfaces declared in the workspace.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/modfile"
)

const (
	rapidMarker  = "serf:fuzz rapid"
	fuzzBuildTag = "serffuzz"
)

// Target is a coverage-replay identity. Module and Package are paths relative to
// go.work and its module directory, respectively. Focus carries the optional
// ";"-separated "file[#func]" focus specs from the manifest; it is metadata for
// spec validation, never part of target identity.
type Target struct {
	Kind    string
	Module  string
	Package string
	Name    string
	Focus   string
}

// targetIdentity preserves the four target fields as an exact, comparable key.
// Its string rendering is only for diagnostics and must not be used as identity.
type targetIdentity struct {
	Kind    string
	Module  string
	Package string
	Name    string
}

type workspaceModule struct {
	label string
	dir   string
}

func main() {
	osExit(runRegistry(os.Args[1:], os.Stdout, os.Stderr))
}

var osExit = os.Exit
var discoverWorkspace = DiscoverWorkspace
var registryAbs = filepath.Abs
var registryEvalSymlinks = filepath.EvalSymlinks
var registryRel = filepath.Rel
var registryWalkDir = filepath.WalkDir
var registryPackagePath = packagePath
var registryParseWork = modfile.ParseWork

func runRegistry(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serf-fuzzregistry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoRoot := fs.String("repo-root", ".", "repository root containing go.work")
	registryPath := fs.String("registry", "", "path to scripts/run-fuzz.sh --list output")
	check := fs.Bool("check", false, "fail when registered and discovered coverage targets differ")
	emitPlan := fs.Bool("emit-plan", false, "write validated native/Rapid targets as TSV")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *registryPath == "" {
		return registryError(stderr, "--registry is required")
	}
	if fs.NArg() != 0 {
		return registryError(stderr, "unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	file, err := os.Open(*registryPath)
	if err != nil {
		return registryError(stderr, "open registry: %v", err)
	}
	defer func() { _ = file.Close() }()

	registered, err := ParseRegistry(file)
	if err != nil {
		return registryError(stderr, "parse registry: %v", err)
	}

	if *check || *emitPlan {
		discovered, err := discoverWorkspace(*repoRoot)
		if err != nil {
			return registryError(stderr, "discover workspace: %v", err)
		}
		if err := CheckTargets(registered, discovered); err != nil {
			return registryError(stderr, "%v", err)
		}
		if err := CheckFocusSpecs(*repoRoot, registered); err != nil {
			return registryError(stderr, "%v", err)
		}
	}
	if *emitPlan {
		if err := EmitPlan(stdout, registered); err != nil {
			return registryError(stderr, "emit replay plan: %v", err)
		}
	}
	return 0
}

func registryError(w io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(w, "serf-fuzzregistry: "+format+"\n", args...)
	return 1
}

// ParseRegistry reads the colon-delimited TARGETS rows emitted by run-fuzz.sh.
// Trailing coverage metadata is intentionally ignored here: target identity has
// exactly four fields, and support-only test rows are preserved for callers that
// need the raw registry but excluded from coverage validation and replay plans.
func ParseRegistry(r io.Reader) ([]Target, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var targets []Target
	for line := 1; scanner.Scan(); line++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		fields := strings.Split(raw, ":")
		if len(fields) < 4 {
			return nil, fmt.Errorf("line %d: expected at least four colon-separated fields", line)
		}
		focus := ""
		if len(fields) >= 6 {
			// Mirror fuzz-coverage.sh's `IFS=: read tag module pkg name cover
			// focus`: everything after the fifth colon is the focus field.
			focus = strings.Join(fields[5:], ":")
		}
		target, err := canonicalTarget(Target{
			Kind:    strings.TrimSpace(fields[0]),
			Module:  strings.TrimSpace(fields[1]),
			Package: strings.TrimSpace(fields[2]),
			Name:    strings.TrimSpace(fields[3]),
			Focus:   focus,
		})
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		targets = append(targets, target)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}
	return targets, nil
}

// DiscoverWorkspace finds native Go fuzz targets and stateful/property tests
// explicitly opted into the coverage program with a marker directly above the
// Test declaration.
func DiscoverWorkspace(root string) ([]Target, error) {
	modules, err := readWorkspaceModules(root)
	if err != nil {
		return nil, err
	}
	buildContext := fuzzBuildContext()
	moduleRoots := make(map[string]struct{}, len(modules))
	for _, module := range modules {
		moduleRoots[filepath.Clean(module.dir)] = struct{}{}
	}

	var targets []Target
	var issues []string
	for _, module := range modules {
		err := registryWalkDir(module.dir, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if filePath != module.dir {
					if _, nestedModule := moduleRoots[filepath.Clean(filePath)]; nestedModule {
						return fs.SkipDir
					}
					if skipDirectory(entry.Name()) {
						return fs.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			active, err := buildContext.MatchFile(filepath.Dir(filePath), entry.Name())
			if err != nil {
				return fmt.Errorf("match build constraints for %s: %w", filePath, err)
			}
			if !active {
				return nil
			}

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
			if err != nil {
				return fmt.Errorf("parse %s: %w", filePath, err)
			}
			pkg, err := registryPackagePath(module.dir, filepath.Dir(filePath))
			if err != nil {
				return err
			}
			rapidNames := rapidImportNames(file)
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				if isNativeFuzzer(fn) {
					targets = append(targets, Target{Kind: "native", Module: module.label, Package: pkg, Name: fn.Name.Name})
				}

				marked := hasRapidMarker(fset, fn)
				if marked {
					if !isTestFunction(fn) {
						issues = append(issues, fmt.Sprintf("%s: %s marker must annotate a Test function with *testing.T", displayPath(root, filePath), rapidMarker))
						continue
					}
					targets = append(targets, Target{Kind: "rapid", Module: module.label, Package: pkg, Name: fn.Name.Name})
					continue
				}
				if isTestFunction(fn) && callsRapidCheck(fn, rapidNames) {
					issues = append(issues, fmt.Sprintf("%s: %s calls rapid.Check without // %s marker", displayPath(root, filePath), fn.Name.Name, rapidMarker))
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(issues) > 0 {
		sort.Strings(issues)
		return nil, errors.New(strings.Join(issues, "\n"))
	}
	sortTargets(targets)
	return targets, nil
}

// CheckTargets validates only coverage targets. Support-only test rows remain in
// the manifest for their existing consumers and are deliberately not compared.
func CheckTargets(registered, discovered []Target) error {
	registeredSet, issues := targetSet("registered", registered)
	discoveredSet, discoveredIssues := targetSet("discovered", discovered)
	issues = append(issues, discoveredIssues...)

	for key, target := range discoveredSet {
		if _, ok := registeredSet[key]; !ok {
			issues = append(issues, "missing registration: "+targetString(target))
		}
	}
	for key, target := range registeredSet {
		if _, ok := discoveredSet[key]; !ok {
			issues = append(issues, "stale registration: "+targetString(target))
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.Strings(issues)
	return errors.New("fuzz target registry drift:\n  " + strings.Join(issues, "\n  "))
}

// EmitPlan writes the validated coverage replay plan as headerless UTF-8 TSV.
// The caller is responsible for calling CheckTargets first when validation is
// required; duplicate coverage rows are rejected here so a plan is never vague.
func EmitPlan(w io.Writer, targets []Target) error {
	coverage := make([]Target, 0, len(targets))
	seen := make(map[targetIdentity]struct{})
	for _, raw := range targets {
		target, err := canonicalTarget(raw)
		if err != nil {
			return err
		}
		if !isCoverageKind(target.Kind) {
			continue
		}
		key := identityOf(target)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate coverage target: %s", targetString(target))
		}
		seen[key] = struct{}{}
		coverage = append(coverage, target)
	}
	sortTargets(coverage)
	for _, target := range coverage {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", target.Kind, target.Module, target.Package, target.Name); err != nil {
			return err
		}
	}
	return nil
}

func readWorkspaceModules(root string) ([]workspaceModule, error) {
	absRoot, err := registryAbs(root)
	if err != nil {
		return nil, fmt.Errorf("absolute repository root: %w", err)
	}
	physicalRoot, err := registryEvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	physicalRoot = filepath.Clean(physicalRoot)
	workPath := filepath.Join(absRoot, "go.work")
	data, err := os.ReadFile(workPath)
	if err != nil {
		return nil, fmt.Errorf("read go.work: %w", err)
	}
	work, err := registryParseWork(workPath, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse go.work: %w", err)
	}

	seenLabels := make(map[string]struct{}, len(work.Use))
	seenDirs := make(map[string]struct{}, len(work.Use))
	modules := make([]workspaceModule, 0, len(work.Use))
	for _, use := range work.Use {
		if use.Path == "" {
			continue
		}
		logicalDir := use.Path
		if !filepath.IsAbs(logicalDir) {
			logicalDir = filepath.Join(absRoot, logicalDir)
		}
		logicalDir = filepath.Clean(logicalDir)
		rel, err := registryRel(absRoot, logicalDir)
		if err != nil {
			return nil, fmt.Errorf("resolve go.work use %q: %w", use.Path, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("go.work module %q is outside repository root", use.Path)
		}
		label := filepath.ToSlash(rel)

		physicalDir, err := registryEvalSymlinks(logicalDir)
		if err != nil {
			return nil, fmt.Errorf("resolve go.work module %q: %w", use.Path, err)
		}
		physicalDir = filepath.Clean(physicalDir)
		if !pathWithinDir(physicalRoot, physicalDir) {
			return nil, fmt.Errorf("go.work module %q is outside repository root", use.Path)
		}
		if _, err := os.Stat(filepath.Join(physicalDir, "go.mod")); err != nil {
			return nil, fmt.Errorf("go.work module %q has no go.mod: %w", use.Path, err)
		}
		if _, ok := seenLabels[label]; ok {
			return nil, fmt.Errorf("go.work lists module %q more than once", label)
		}
		if _, ok := seenDirs[physicalDir]; ok {
			return nil, fmt.Errorf("go.work lists duplicate module directory %q", use.Path)
		}
		seenLabels[label] = struct{}{}
		seenDirs[physicalDir] = struct{}{}
		modules = append(modules, workspaceModule{label: label, dir: physicalDir})
	}
	if len(modules) == 0 {
		return nil, errors.New("go.work contains no modules")
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].label < modules[j].label })
	return modules, nil
}

func pathWithinDir(root, candidate string) bool {
	rel, err := registryRel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func targetSet(label string, targets []Target) (map[targetIdentity]Target, []string) {
	set := make(map[targetIdentity]Target, len(targets))
	var issues []string
	for _, raw := range targets {
		target, err := canonicalTarget(raw)
		if err != nil {
			issues = append(issues, fmt.Sprintf("invalid %s target %q: %v", label, targetString(raw), err))
			continue
		}
		if !isCoverageKind(target.Kind) {
			continue
		}
		key := identityOf(target)
		if _, ok := set[key]; ok {
			issues = append(issues, fmt.Sprintf("duplicate %s target: %s", label, targetString(target)))
			continue
		}
		set[key] = target
	}
	return set, issues
}

func canonicalTarget(target Target) (Target, error) {
	kind := strings.TrimSpace(target.Kind)
	if kind != "native" && kind != "rapid" && kind != "test" {
		return Target{}, fmt.Errorf("unknown target kind %q", target.Kind)
	}
	module, err := canonicalModule(target.Module)
	if err != nil {
		return Target{}, err
	}
	pkg, err := canonicalPackage(target.Package)
	if err != nil {
		return Target{}, err
	}
	name := strings.TrimSpace(target.Name)
	if name == "" || strings.ContainsAny(name, "\t\r\n:") {
		return Target{}, fmt.Errorf("invalid target name %q", target.Name)
	}
	return Target{Kind: kind, Module: module, Package: pkg, Name: name, Focus: strings.TrimSpace(target.Focus)}, nil
}

// CheckFocusSpecs verifies every native target's focus specs resolve the way
// cmd/serf-fuzzcov will resolve them at report time: the file exists under the
// target's package directory and, when a "#func" suffix is present, a top-level
// function or method of that name is declared there. This catches specs naming
// a type or a renamed function at registry-check time instead of at the end of
// a full fuzz-coverage replay.
func CheckFocusSpecs(root string, targets []Target) error {
	var issues []string
	for _, target := range targets {
		if target.Kind != "native" || target.Focus == "" {
			continue
		}
		pkgSub := strings.TrimPrefix(target.Package, "./")
		if pkgSub == "." {
			pkgSub = ""
		}
		for _, spec := range strings.Split(target.Focus, ";") {
			relpath, fn, _ := strings.Cut(spec, "#")
			display := path.Join(target.Module, pkgSub, relpath)
			srcPath := filepath.Join(root, target.Module, pkgSub, relpath)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, srcPath, nil, 0)
			if err != nil {
				issues = append(issues, fmt.Sprintf("%s: %s (focus spec %q): %v", target.Name, display, spec, err))
				continue
			}
			if fn == "" {
				continue
			}
			found := false
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == fn {
					found = true
					break
				}
			}
			if !found {
				issues = append(issues, fmt.Sprintf("%s: function %s not found in %s (focus spec %q)", target.Name, fn, display, spec))
			}
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.Strings(issues)
	return errors.New("fuzz focus spec drift:\n  " + strings.Join(issues, "\n  "))
}

func canonicalModule(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", errors.New("module is empty")
	}
	clean := path.Clean(value)
	if clean == "." {
		return ".", nil
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("module must be repository-relative: %q", value)
	}
	return strings.TrimPrefix(clean, "./"), nil
}

func canonicalPackage(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", errors.New("package is empty")
	}
	clean := path.Clean(value)
	if clean == "." {
		return ".", nil
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("package must be module-relative: %q", value)
	}
	return "./" + strings.TrimPrefix(clean, "./"), nil
}

func packagePath(moduleDir, dir string) (string, error) {
	rel, err := registryRel(moduleDir, dir)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return ".", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("package %q lies outside module %q", dir, moduleDir)
	}
	return "./" + filepath.ToSlash(rel), nil
}

func skipDirectory(name string) bool {
	switch name {
	case "testdata", "vendor", "node_modules":
		return true
	}
	// Go package loading ignores both dot- and underscore-prefixed directories.
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// fuzzBuildContext matches the explicit build configuration used by run-fuzz
// and coverage replay. build.Default supplies the current target platform,
// architecture, cgo state, and toolchain tags; this adds only serffuzz.
func fuzzBuildContext() build.Context {
	context := build.Default
	context.BuildTags = append(append([]string(nil), context.BuildTags...), fuzzBuildTag)
	return context
}

func isNativeFuzzer(fn *ast.FuncDecl) bool {
	return isGoTestName(fn.Name.Name, "Fuzz") && hasSingleTestingParameter(fn, "F")
}

func isTestFunction(fn *ast.FuncDecl) bool {
	return isGoTestName(fn.Name.Name, "Test") && hasSingleTestingParameter(fn, "T")
}

// isGoTestName and hasSingleTestingParameter mirror Go 1.25 cmd/go's test
// target checks. In particular, cmd/go accepts *F and *anything.F because an
// AST-only scan cannot reliably resolve how testing was imported.
func isGoTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	runeAfterPrefix, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(runeAfterPrefix)
}

func hasSingleTestingParameter(fn *ast.FuncDecl, typeName string) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return false
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) != 0 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	if ident, ok := star.X.(*ast.Ident); ok {
		return ident.Name == typeName
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == typeName
}

func rapidImportNames(file *ast.File) map[string]struct{} {
	names := make(map[string]struct{})
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || importPath != "pgregory.net/rapid" {
			continue
		}
		name := "rapid"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" && name != "." {
			names[name] = struct{}{}
		}
	}
	return names
}

func hasRapidMarker(fset *token.FileSet, fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	functionLine := fset.Position(fn.Pos()).Line
	for _, comment := range fn.Doc.List {
		if fset.Position(comment.End()).Line != functionLine-1 {
			continue
		}
		text := strings.TrimSpace(comment.Text)
		text = strings.TrimPrefix(text, "//")
		text = strings.TrimPrefix(text, "/*")
		text = strings.TrimSuffix(text, "*/")
		if strings.TrimSpace(text) == rapidMarker {
			return true
		}
	}
	return false
}

func callsRapidCheck(fn *ast.FuncDecl, rapidNames map[string]struct{}) bool {
	if len(rapidNames) == 0 || fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Check" {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		_, found = rapidNames[ident.Name]
		return !found
	})
	return found
}

func displayPath(root, filePath string) string {
	absRoot, err := registryAbs(root)
	if err != nil {
		return filepath.ToSlash(filePath)
	}
	rel, err := registryRel(absRoot, filePath)
	if err != nil {
		return filepath.ToSlash(filePath)
	}
	return filepath.ToSlash(rel)
}

func isCoverageKind(kind string) bool {
	return kind == "native" || kind == "rapid"
}

func targetString(target Target) string {
	return target.Kind + ":" + target.Module + ":" + target.Package + ":" + target.Name
}

func identityOf(target Target) targetIdentity {
	return targetIdentity{
		Kind:    target.Kind,
		Module:  target.Module,
		Package: target.Package,
		Name:    target.Name,
	}
}

func sortTargets(targets []Target) {
	sort.Slice(targets, func(i, j int) bool {
		left, right := targets[i], targets[j]
		if left.Module != right.Module {
			return left.Module < right.Module
		}
		if left.Package != right.Package {
			return left.Package < right.Package
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})
}
