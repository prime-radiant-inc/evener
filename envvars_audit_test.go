package serf_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
)

func TestSupportedEnvVarsAreDocumented(t *testing.T) {
	raw, err := os.ReadFile("docs/environment.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	var missing []string
	for _, v := range envvars.All() {
		if !strings.Contains(doc, "`"+v.Name+"`") {
			missing = append(missing, v.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("docs/environment.md is missing env vars:\n%s", strings.Join(missing, "\n"))
	}
}

func TestSupportedEnvVarsUseRegistryRows(t *testing.T) {
	names := map[string]bool{}
	for _, v := range envvars.All() {
		names[v.Name] = true
	}

	var findings []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".claude", ".worktrees", "docs", "envvars", "inspo", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			for name := range names {
				if literalUsesEnvName(value, name) {
					pos := fset.Position(lit.Pos())
					findings = append(findings, pos.String()+": use the envvars row for "+name+" instead of a raw env var string")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("supported env vars must be sourced from envvars rows:\n%s", strings.Join(findings, "\n"))
	}
}

func literalUsesEnvName(value, name string) bool {
	if value == name || strings.HasPrefix(value, name+"=") {
		return true
	}
	if containsEnvExpansion(value, name) {
		return true
	}
	if genericInheritedEnvName(name) {
		return false
	}
	// Use a word-boundary check: the character immediately after the matched
	// name (if any) must not be an env-name character, so that e.g. "SERF_FOO"
	// does not spuriously match inside "SERF_FOO_BAR".
	for {
		idx := strings.Index(value, name)
		if idx < 0 {
			return false
		}
		end := idx + len(name)
		if end == len(value) || !envNameChar(value[end]) {
			return true
		}
		value = value[end:]
	}
}

func containsEnvExpansion(value, name string) bool {
	if strings.Contains(value, "${"+name+"}") {
		return true
	}
	needle := "$" + name
	for {
		idx := strings.Index(value, needle)
		if idx < 0 {
			return false
		}
		end := idx + len(needle)
		if end == len(value) || !envNameChar(value[end]) {
			return true
		}
		value = value[end:]
	}
}

func envNameChar(b byte) bool {
	return b == '_' || ('A' <= b && b <= 'Z') || ('a' <= b && b <= 'z') || ('0' <= b && b <= '9')
}

func genericInheritedEnvName(name string) bool {
	switch name {
	case "HOME", "LANG", "PATH", "SHELL", "TERM", "USER":
		return true
	}
	return false
}
