package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	internalcheck "primeradiant.com/evener/cmd/evener-internalcheck"
)

type goListPackage struct {
	Dir          string
	ImportPath   string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
}

type golangciConfig struct {
	Linters struct {
		Exclusions struct {
			Rules []struct {
				PathExcept string   `yaml:"path-except"`
				Text       string   `yaml:"text"`
				Linters    []string `yaml:"linters"`
			} `yaml:"rules"`
		} `yaml:"exclusions"`
	} `yaml:"linters"`
}

var allExecsupportPackages = []string{
	"primeradiant.com/evener/execsupport/componentlog",
	"primeradiant.com/evener/execsupport/fsatomic",
	"primeradiant.com/evener/execsupport/orphanpipe",
	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest",
	"primeradiant.com/evener/execsupport/procgroup",
	"primeradiant.com/evener/execsupport/shellquote",
	"primeradiant.com/evener/execsupport/syncio",
	"primeradiant.com/evener/execsupport/valueexpr",
}

var execsupportPackagesByGOOS = map[string][]string{
	"darwin": allExecsupportPackages,
	"linux":  allExecsupportPackages,
	"windows": {
		"primeradiant.com/evener/execsupport/componentlog",
		"primeradiant.com/evener/execsupport/fsatomic",
		"primeradiant.com/evener/execsupport/orphanpipe",
		"primeradiant.com/evener/execsupport/procgroup",
		"primeradiant.com/evener/execsupport/shellquote",
		"primeradiant.com/evener/execsupport/syncio",
		"primeradiant.com/evener/execsupport/valueexpr",
	},
}

var supportedGOOS = []string{"linux", "darwin", "windows"}

type execsupportInventory struct {
	packages []goListPackage
	paths    []string
}

var supportedExecsupportInventoryCache struct {
	sync.Once
	inventories map[string]execsupportInventory
	err         error
}

// libraryPackages are the packages evener-internalcheck holds to an
// internal-free exported surface. Every execsupport package must be one.
var libraryPackages = internalcheck.LibraryPackages()

// execsupportStaysInLibraryScope keeps every execsupport package the host
// GOOS builds in libraryPackages, and pins that list.
func execsupportStaysInLibraryScope(a *audit) {
	inventories := listSupportedExecsupportInventories(a)
	inventory, ok := inventories[runtime.GOOS]
	if !ok {
		a.Fatalf("missing execsupport inventory for GOOS=%s", runtime.GOOS)
	}
	assertExecsupportPackagesStayInLibraryScope(a, runtime.GOOS, inventory.packages)

	wantLibraries := append([]string{
		"primeradiant.com/evener/agent",
		"primeradiant.com/evener/agent/diagnostic",
		"primeradiant.com/evener/agent/execenv",
		"primeradiant.com/evener/agent/mcpconfig",
		"primeradiant.com/evener/agent/plugin",
		"primeradiant.com/evener/agent/provider",
		"primeradiant.com/evener/agent/schema",
		"primeradiant.com/evener/agent/skill",
		"primeradiant.com/evener/agent/task",
		"primeradiant.com/evener/agent/transcript",
		"primeradiant.com/evener/llm",
		"primeradiant.com/evener/llm/registry",
	}, allExecsupportPackages...)
	sort.Strings(wantLibraries)
	actualLibraries := append([]string(nil), libraryPackages...)
	sort.Strings(actualLibraries)
	if !reflect.DeepEqual(actualLibraries, wantLibraries) {
		a.Fatalf("libraryPackages = %v, want %v", actualLibraries, wantLibraries)
	}
}

// execsupportStaysInLibraryScopeForUnlistedGOOS holds the same rule for a
// GOOS no inventory lists.
func execsupportStaysInLibraryScopeForUnlistedGOOS(a *audit) {
	root := repositoryRoot(a)
	packages, err := loadExecsupportPackagesForGOOS(root, "freebsd")
	if err != nil {
		a.Fatal(err)
	}
	assertExecsupportPackagesStayInLibraryScope(a, "freebsd", packages)
}

func assertExecsupportPackagesStayInLibraryScope(a *audit, goos string, packages []goListPackage) {
	for _, pkg := range packages {
		if !slices.Contains(libraryPackages, pkg.ImportPath) {
			a.Errorf("GOOS=%s package %q is not in libraryPackages", goos, pkg.ImportPath)
		}
	}
}

// execsupportCoversSupportedGOOS pins which execsupport packages each GOOS
// builds, and that together they are every execsupport package.
func execsupportCoversSupportedGOOS(a *audit) {
	inventories := listSupportedExecsupportInventories(a)
	var union []string
	for _, goos := range supportedGOOS {
		got := inventories[goos].paths
		want := execsupportPackagesByGOOS[goos]
		if !reflect.DeepEqual(got, want) {
			a.Fatalf("GOOS=%s go list ./execsupport/... = %v, want %v", goos, got, want)
		}
		union = append(union, got...)
	}
	slices.Sort(union)
	union = slices.Compact(union)
	if !reflect.DeepEqual(union, allExecsupportPackages) {
		a.Fatalf("supported GOOS package union = %v, want %v", union, allExecsupportPackages)
	}
}

// reviveExportedRuleCoversLibraryFiles keeps .golangci.yml's revive
// exported-comment rule applying to every file of the published execsupport
// and llm/registry packages.
func reviveExportedRuleCoversLibraryFiles(a *audit) {
	root := repositoryRoot(a)
	pathExcept, re := reviveExportedPathExcept(a, root)
	inventories := listSupportedExecsupportInventories(a)
	for _, goos := range sortedInventoryGOOSes(inventories) {
		assertReviveExportedRuleCoversPackages(a, root, pathExcept, re, goos, inventories[goos].packages)
	}

	registryPackages, err := loadGoListPackages(root, "linux", "primeradiant.com/evener/llm/registry")
	if err != nil {
		a.Fatal(err)
	}
	assertReviveExportedRuleCoversPackages(a, root, pathExcept, re, "linux registry", registryPackages)
}

func sortedInventoryGOOSes(inventories map[string]execsupportInventory) []string {
	gooses := make([]string, 0, len(inventories))
	for goos := range inventories {
		gooses = append(gooses, goos)
	}
	sort.Strings(gooses)
	return gooses
}

func assertReviveExportedRuleCoversPackages(a *audit, root, pathExcept string, re *regexp.Regexp, goos string, packages []goListPackage) {
	for _, pkg := range packages {
		for _, file := range packageGoFiles(pkg) {
			path, err := filepath.Rel(root, filepath.Join(pkg.Dir, file))
			if err != nil {
				a.Fatalf("relativize %s/%s: %v", pkg.Dir, file, err)
			}
			path = filepath.ToSlash(path)
			if !re.MatchString(path) {
				a.Errorf("revive exported path-except %q does not cover %s (GOOS=%s)", pathExcept, path, goos)
			}
		}
	}
}

func reviveExportedPathExcept(a *audit, root string) (string, *regexp.Regexp) {
	configBytes, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		a.Fatalf("read .golangci.yml: %v", err)
	}
	var config golangciConfig
	if err := yaml.Unmarshal(configBytes, &config); err != nil {
		a.Fatalf("parse .golangci.yml: %v", err)
	}
	for _, rule := range config.Linters.Exclusions.Rules {
		if rule.PathExcept == "" || rule.Text == "" || !slices.Contains(rule.Linters, "revive") {
			continue
		}
		if rule.Text != `^exported: (exported (method|function|type|const|var)|comment on exported)` {
			continue
		}
		re, err := regexp.Compile(rule.PathExcept)
		if err != nil {
			a.Fatalf("compile revive path-except %q: %v", rule.PathExcept, err)
		}
		return rule.PathExcept, re
	}
	a.Fatal(".golangci.yml has no revive exported path-except rule")
	return "", nil
}

func listSupportedExecsupportInventories(a *audit) map[string]execsupportInventory {
	root := repositoryRoot(a)
	supportedExecsupportInventoryCache.Do(func() {
		inventories := make(map[string]execsupportInventory, len(supportedGOOS))
		hostPackages, err := loadExecsupportPackages(root)
		if err != nil {
			supportedExecsupportInventoryCache.err = err
			return
		}
		inventories[runtime.GOOS] = newExecsupportInventory(hostPackages)
		for _, goos := range supportedGOOS {
			if goos == runtime.GOOS {
				continue
			}
			packages, err := loadExecsupportPackagesForGOOS(root, goos)
			if err != nil {
				supportedExecsupportInventoryCache.err = err
				return
			}
			inventories[goos] = newExecsupportInventory(packages)
		}
		supportedExecsupportInventoryCache.inventories = inventories
	})
	if supportedExecsupportInventoryCache.err != nil {
		a.Fatal(supportedExecsupportInventoryCache.err)
	}
	return supportedExecsupportInventoryCache.inventories
}

func newExecsupportInventory(packages []goListPackage) execsupportInventory {
	paths := make([]string, 0, len(packages))
	for _, pkg := range packages {
		paths = append(paths, pkg.ImportPath)
	}
	sort.Strings(paths)
	return execsupportInventory{packages: packages, paths: paths}
}

func loadExecsupportPackages(root string) ([]goListPackage, error) {
	return loadExecsupportPackagesForGOOS(root, "")
}

func loadExecsupportPackagesForGOOS(root, goos string) ([]goListPackage, error) {
	return loadGoListPackages(root, goos, "./execsupport/...")
}

func loadGoListPackages(root, goos string, patterns ...string) ([]goListPackage, error) {
	args := append([]string{"list", "-json"}, patterns...)
	cmd := exec.CommandContext(context.Background(), "go", args...)
	cmd.Dir = root
	if goos != "" {
		cmd.Env = append(os.Environ(), "GOOS="+goos)
	}
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("go list %s: %w\n%s", strings.Join(patterns, " "), err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("go list %s: %w", strings.Join(patterns, " "), err)
	}

	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var packages []goListPackage
	for {
		var pkg goListPackage
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list package: %w", err)
		}
		packages = append(packages, pkg)
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("go list %s returned no packages", strings.Join(patterns, " "))
	}
	return packages, nil
}

func packageGoFiles(pkg goListPackage) []string {
	files := make([]string, 0, len(pkg.GoFiles)+len(pkg.CgoFiles)+len(pkg.TestGoFiles)+len(pkg.XTestGoFiles))
	files = append(files, pkg.GoFiles...)
	files = append(files, pkg.CgoFiles...)
	files = append(files, pkg.TestGoFiles...)
	files = append(files, pkg.XTestGoFiles...)
	sort.Strings(files)
	return files
}

// repositoryRoot is the absolute repository root the audit runs from. go list
// reports each package's Dir as an absolute path, so the files it names are
// made repository-relative against this.
func repositoryRoot(a *audit) string {
	root, err := os.Getwd()
	if err != nil {
		a.Fatalf("get working directory: %v", err)
	}
	return root
}
