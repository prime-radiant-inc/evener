package internalcheck

import (
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
	"testing"

	"gopkg.in/yaml.v3"
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
	"primeradiant.com/evener/execsupport/orphanpipe",
	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest",
	"primeradiant.com/evener/execsupport/procgroup",
	"primeradiant.com/evener/execsupport/shellquote",
	"primeradiant.com/evener/execsupport/valueexpr",
}

var execsupportPackagesByGOOS = map[string][]string{
	"darwin": allExecsupportPackages,
	"linux":  allExecsupportPackages,
	"windows": {
		"primeradiant.com/evener/execsupport/orphanpipe",
		"primeradiant.com/evener/execsupport/procgroup",
		"primeradiant.com/evener/execsupport/shellquote",
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

func TestExecsupportPackagesStayInLibraryScope(t *testing.T) {
	inventories := listSupportedExecsupportInventories(t)
	inventory, ok := inventories[runtime.GOOS]
	if !ok {
		t.Fatalf("unsupported test host GOOS %q", runtime.GOOS)
	}
	got := inventory.paths
	wantPackages, ok := execsupportPackagesByGOOS[runtime.GOOS]
	if !ok {
		t.Fatalf("unsupported test host GOOS %q", runtime.GOOS)
	}
	if !reflect.DeepEqual(got, wantPackages) {
		t.Fatalf("go list ./execsupport/... = %v, want %v", got, wantPackages)
	}

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
		t.Fatalf("libraryPackages = %v, want %v", actualLibraries, wantLibraries)
	}
}

func TestExecsupportPackagesCoverSupportedGOOS(t *testing.T) {
	inventories := listSupportedExecsupportInventories(t)
	var union []string
	for _, goos := range supportedGOOS {
		got := inventories[goos].paths
		want := execsupportPackagesByGOOS[goos]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("GOOS=%s go list ./execsupport/... = %v, want %v", goos, got, want)
		}
		union = append(union, got...)
	}
	slices.Sort(union)
	union = slices.Compact(union)
	if !reflect.DeepEqual(union, allExecsupportPackages) {
		t.Fatalf("supported GOOS package union = %v, want %v", union, allExecsupportPackages)
	}
}

func TestReviveExportedRuleCoversPublishedLibraryFiles(t *testing.T) {
	root := repositoryRoot(t)
	pathExcept, re := reviveExportedPathExcept(t, root)
	inventories := listSupportedExecsupportInventories(t)
	for _, goos := range supportedGOOS {
		for _, pkg := range inventories[goos].packages {
			for _, file := range packageGoFiles(pkg) {
				path, err := filepath.Rel(root, filepath.Join(pkg.Dir, file))
				if err != nil {
					t.Fatalf("relativize %s/%s: %v", pkg.Dir, file, err)
				}
				path = filepath.ToSlash(path)
				if !re.MatchString(path) {
					t.Errorf("revive exported path-except %q does not cover %s (GOOS=%s)", pathExcept, path, goos)
				}
			}
		}
	}

	registryPackages, err := loadGoListPackages(root, "linux", "primeradiant.com/evener/llm/registry")
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range registryPackages {
		for _, file := range packageGoFiles(pkg) {
			path, err := filepath.Rel(root, filepath.Join(pkg.Dir, file))
			if err != nil {
				t.Fatalf("relativize %s/%s: %v", pkg.Dir, file, err)
			}
			path = filepath.ToSlash(path)
			if !re.MatchString(path) {
				t.Errorf("revive exported path-except %q does not cover %s (GOOS=linux registry)", pathExcept, path)
			}
		}
	}
}

func reviveExportedPathExcept(t *testing.T, root string) (string, *regexp.Regexp) {
	t.Helper()
	configBytes, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	var config golangciConfig
	if err := yaml.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
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
			t.Fatalf("compile revive path-except %q: %v", rule.PathExcept, err)
		}
		return rule.PathExcept, re
	}
	t.Fatal(".golangci.yml has no revive exported path-except rule")
	return "", nil
}

func listSupportedExecsupportInventories(t *testing.T) map[string]execsupportInventory {
	t.Helper()
	root := repositoryRoot(t)
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
		t.Fatal(supportedExecsupportInventoryCache.err)
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
	cmd := exec.Command("go", args...)
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

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	if filepath.IsAbs(file) {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		if isRepositoryRoot(candidate) {
			return candidate
		}
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for candidate := filepath.Clean(workingDir); ; candidate = filepath.Dir(candidate) {
		if isRepositoryRoot(candidate) {
			return candidate
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	t.Fatalf("repository root not found from %q or %q", file, workingDir)
	return ""
}

func isRepositoryRoot(path string) bool {
	for _, name := range []string{".golangci.yml", "go.work"} {
		if _, err := os.Stat(filepath.Join(path, name)); err != nil {
			return false
		}
	}
	return true
}
