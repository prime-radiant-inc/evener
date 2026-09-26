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

var execsupportPackagesCache struct {
	sync.Once
	packages []goListPackage
	err      error
}

func TestExecsupportPackagesStayInLibraryScope(t *testing.T) {
	packages := listExecsupportPackages(t)
	got := make([]string, 0, len(packages))
	for _, pkg := range packages {
		got = append(got, pkg.ImportPath)
	}
	sort.Strings(got)

	wantPackages := []string{
		"primeradiant.com/evener/execsupport/orphanpipe",
		"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest",
		"primeradiant.com/evener/execsupport/procgroup",
		"primeradiant.com/evener/execsupport/shellquote",
		"primeradiant.com/evener/execsupport/valueexpr",
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
	}, got...)
	sort.Strings(wantLibraries)
	actualLibraries := append([]string(nil), libraryPackages...)
	sort.Strings(actualLibraries)
	if !reflect.DeepEqual(actualLibraries, wantLibraries) {
		t.Fatalf("libraryPackages = %v, want %v", actualLibraries, wantLibraries)
	}
}

func TestReviveExportedRuleCoversExecsupportFiles(t *testing.T) {
	root := repositoryRoot(t)
	configBytes, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	var config golangciConfig
	if err := yaml.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
	}

	var pathExcept string
	for _, rule := range config.Linters.Exclusions.Rules {
		if rule.PathExcept == "" || rule.Text == "" || !slices.Contains(rule.Linters, "revive") {
			continue
		}
		if rule.Text == `^exported: (exported (method|function|type|const|var)|comment on exported)` {
			pathExcept = rule.PathExcept
			break
		}
	}
	if pathExcept == "" {
		t.Fatal(".golangci.yml has no revive exported path-except rule")
	}
	re, err := regexp.Compile(pathExcept)
	if err != nil {
		t.Fatalf("compile revive path-except %q: %v", pathExcept, err)
	}

	for _, pkg := range listExecsupportPackages(t) {
		for _, file := range packageGoFiles(pkg) {
			path, err := filepath.Rel(root, filepath.Join(pkg.Dir, file))
			if err != nil {
				t.Fatalf("relativize %s/%s: %v", pkg.Dir, file, err)
			}
			path = filepath.ToSlash(path)
			if !re.MatchString(path) {
				t.Errorf("revive exported path-except %q does not cover %s", pathExcept, path)
			}
		}
	}
}

func listExecsupportPackages(t *testing.T) []goListPackage {
	t.Helper()
	root := repositoryRoot(t)
	execsupportPackagesCache.Do(func() {
		execsupportPackagesCache.packages, execsupportPackagesCache.err = loadExecsupportPackages(root)
	})
	if execsupportPackagesCache.err != nil {
		t.Fatal(execsupportPackagesCache.err)
	}
	return execsupportPackagesCache.packages
}

func loadExecsupportPackages(root string) ([]goListPackage, error) {
	cmd := exec.Command("go", "list", "-json", "./execsupport/...")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("go list ./execsupport/...: %w\n%s", err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("go list ./execsupport/...: %w", err)
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
		return nil, errors.New("go list ./execsupport/... returned no packages")
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
