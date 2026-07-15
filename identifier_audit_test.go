package serf_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestIdentifierAuditRejectsInjectedDuplicateImplementation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, "fixture.go", `package fixture

func ProjectSlug(path string) string { return path }
`)

	findings, err := identifierAuditFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("identifier audit accepted an injected ProjectSlug implementation")
	}
}

func TestIdentifierAuditRejectsProjectPathHashInAllowlistedFile(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), `package agent

import "crypto/sha256"

func nonProjectHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return string(sum[:])
}
`)

	findings, err := identifierAuditFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsIdentifierFinding(findings, "project/path data") {
		t.Fatalf("project-path SHA fixture was accepted: %v", findings)
	}
}

func TestIdentifierAuditAllowsReviewedNonProjectHash(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), `package agent

import "crypto/sha256"

func nonProjectHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return string(sum[:])
}
`)

	findings, err := identifierAuditFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("reviewed non-project SHA fixture was rejected: %v", findings)
	}
}

func TestIdentifierAuditUsesSyntaxNotCommentsOrStrings(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, "fixture.go", `package fixture

// func ProjectSlug(path string) string { return path }
var text = "ulid.Make( project-hash sha256.Sum256([]byte(path)) )"
`)

	findings, err := identifierAuditFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("comments/strings triggered identifier audit: %v", findings)
	}
}

func TestIdentifierAuditDoesNotExcludeNestedIdentifierDirectory(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("cmd", "identifier", "fixture.go"), `package fixture

func ProjectID(path string) string { return path }
`)

	findings, err := identifierAuditFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("identifier audit excluded a nested cmd/identifier directory")
	}
}

func TestIdentifierAudit(t *testing.T) {
	findings, err := identifierAuditFindings(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("identifier audit found forbidden implementation(s):\n%s", strings.Join(findings, "\n"))
	}
}

// This is an exact allowlist of reviewed production files and functions. A
// new SHA-256 use must be reviewed here, and its call arguments must still be
// free of project identity/path data.
var identifierSHA256Allowlist = map[string]map[string]bool{
	"agent/internal/jobstore/output.go": {
		"outputFileHasPrefixSHA256": true, "outputFileHasSuffixSHA256": true,
		"outputFileSHA256": true, "outputBytesSHA256": true,
	},
	"agent/internal/tool/registry.go":             {"shortHash": true},
	"agent/job_watch.go":                          {"normalizedWatchConfigHash": true},
	"agent/runtime_dir.go":                        {"nonProjectHash": true},
	"auth/openai/pkce.go":                         {"GeneratePKCE": true},
	"cmd/serf-fuzz-harvest/emit.go":               {"write": true},
	"cmd/serf-hub/image_serve.go":                 {"findImageInTranscript": true, "imageSha": true},
	"cmd/serf-hub/internal/hubcore/tree.go":       {"clusterID": true},
	"cmd/serf-hub/internal/launchconfig/trust.go": {"canonicalHashTOML": true},
	"cmd/serf-hub/output_images.go":               {"outputImageSHA": true},
	"fuzz/promoter/emit_go.go":                    {"ShortHash": true},
	"llm/continuation_secret.go": {
		"deriveContinuationSubkey": true, "versionedContinuationHMAC": true,
	},
	"llm/providers/openai/responses_continuation_fingerprint.go": {
		"requestFingerprintForResponsesBody": true,
	},
}

// clusterID is the one reviewed non-project hash that receives a project label;
// it hashes a synthetic sidebar key (project + title), not a project path. Keep
// the exact expression pinned so a future path hash in this function is not
// accepted merely because the file/function is allowlisted.
const reviewedClusterHashArgument = `[]byte(project + "\x00" + title)`

func identifierAuditFindings(root string) ([]string, error) {
	var findings []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if !strings.Contains(filepath.ToSlash(rel), "/") && identifierAuditExcludedRootDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if isGeneratedGo(raw) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		file, parseErr := parser.ParseFile(fset, path, raw, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		addSyntaxIdentifierFindings(&findings, rel, file)
		checkSHA256Findings(&findings, rel, file, fset)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(findings)
	return findings, nil
}

func addSyntaxIdentifierFindings(findings *[]string, path string, file *ast.File) {
	ulidNames := map[string]bool{}
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasPrefix(importPath, "github.com/oklog/ulid") {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			*findings = append(*findings, path+": forbidden ULID dot import")
			continue
		}
		name := "ulid"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		ulidNames[name] = true
		*findings = append(*findings, path+": forbidden ULID import")
	}
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if ok && fn.Name != nil && (fn.Name.Name == "ProjectID" || fn.Name.Name == "ProjectSlug" || fn.Name.Name == "projectSlug") {
			*findings = append(*findings, fmt.Sprintf("%s: duplicate project identifier declaration %s", path, fn.Name.Name))
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Make" && selector.Sel.Name != "New" {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && ulidNames[pkg.Name] {
			*findings = append(*findings, path+": forbidden ULID generation call "+selector.Sel.Name)
		}
		return true
	})
}

func checkSHA256Findings(findings *[]string, path string, file *ast.File, fset *token.FileSet) {
	var shaNames []string
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != "crypto/sha256" {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			*findings = append(*findings, path+": crypto/sha256 dot import is not allowed")
			continue
		}
		name := "sha256"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		shaNames = append(shaNames, name)
	}
	if len(shaNames) == 0 {
		return
	}
	allowedFunctions, allowed := identifierSHA256Allowlist[path]
	if !allowed {
		*findings = append(*findings, path+": crypto/sha256 import is not in the reviewed non-project allowlist")
		return
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		functionAllowed := allowedFunctions[fn.Name.Name]
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isSHA256Call(call, shaNames) {
				return true
			}
			if !functionAllowed {
				*findings = append(*findings, fmt.Sprintf("%s: SHA-256 call is outside reviewed function %s", path, fn.Name.Name))
				return true
			}
			if fn.Name.Name == "clusterID" && formatSHAArgument(call, fset) == reviewedClusterHashArgument {
				return true
			}
			for _, arg := range call.Args {
				if expressionContainsProjectPathData(arg) {
					*findings = append(*findings, path+": SHA-256 call consumes project/path data")
				}
			}
			return true
		})
	}
}

func isSHA256Call(call *ast.CallExpr, names []string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Sum256" && selector.Sel.Name != "New" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	for _, name := range names {
		if pkg.Name == name {
			return true
		}
	}
	return false
}

func formatSHAArgument(call *ast.CallExpr, fset *token.FileSet) string {
	if len(call.Args) != 1 {
		return ""
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, call.Args[0]); err != nil {
		return ""
	}
	return buf.String()
}

func expressionContainsProjectPathData(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if found {
			return false
		}
		switch n := node.(type) {
		case *ast.Ident:
			name := strings.ToLower(n.Name)
			for _, marker := range []string{"path", "project", "working", "canonical", "root", "repo", "directory", "bucket"} {
				if strings.Contains(name, marker) {
					found = true
					return false
				}
			}
		case *ast.SelectorExpr:
			field := strings.ToLower(n.Sel.Name)
			for _, marker := range []string{"id", "path", "project", "working", "canonical", "root", "repo", "directory", "bucket"} {
				if strings.Contains(field, marker) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func containsIdentifierFinding(findings []string, needle string) bool {
	for _, finding := range findings {
		if strings.Contains(finding, needle) {
			return true
		}
	}
	return false
}

func identifierAuditExcludedRootDir(name string) bool {
	switch name {
	case ".git", ".superpowers", "docs", "identifier":
		return true
	default:
		return false
	}
}

func isGeneratedGo(raw []byte) bool {
	lines := strings.SplitN(string(raw), "\n", 12)
	for _, line := range lines {
		if strings.Contains(line, "Code generated") && strings.Contains(line, "DO NOT EDIT") {
			return true
		}
	}
	return false
}

func writeIdentifierAuditFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
