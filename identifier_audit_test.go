package serf_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestIdentifierAuditRejectsInjectedDuplicateImplementation(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, "fixture.go", `package fixture

func ProjectSlug(path string) string { return path }
`)
	assertIdentifierAuditFinds(t, root, "duplicate project identifier declaration")
}

func TestIdentifierAuditRejectsProjectPathHashInAllowlistedFile(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), `package agent

import "crypto/sha256"

func nonProjectHash(x string) string {
	sum := sha256.Sum256([]byte(x))
	return string(sum[:])
}
`)
	assertIdentifierAuditFinds(t, root, "unreviewed SHA-256 operation")
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

func TestIdentifierAuditRejectsAdditionalSHAOperationInReviewedFunction(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), `package agent

import "crypto/sha256"

func nonProjectHash(input string) string {
	first := sha256.Sum256([]byte(input))
	x := input
	second := sha256.Sum256([]byte(x))
	return string(first[:]) + string(second[:])
}
`)
	assertIdentifierAuditFinds(t, root, "unreviewed SHA-256 operation")
}

func TestIdentifierAuditRejectsPackageInitializerSHA(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), `package agent

import "crypto/sha256"

var packageHash = sha256.Sum256([]byte("package initializer"))

func nonProjectHash(input string) string { return input }
`)
	assertIdentifierAuditFinds(t, root, "unreviewed SHA-256 operation")
}

func TestIdentifierAuditRejectsSHA256NewWriteFlow(t *testing.T) {
	root := t.TempDir()
	writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), `package agent

import "crypto/sha256"

func nonProjectHash(input string) string {
	_, _ = sha256.New().Write([]byte(input))
	return input
}
`)
	assertIdentifierAuditFinds(t, root, "unreviewed SHA-256 operation")
}

func TestIdentifierAuditRejectsSHA256AliasAndDotImports(t *testing.T) {
	for name, source := range map[string]string{
		"alias": `package agent

import cryptohash "crypto/sha256"

func nonProjectHash(input string) string {
	sum := cryptohash.Sum256([]byte(input))
	return string(sum[:])
}
`,
		"dot": `package agent

import . "crypto/sha256"

func nonProjectHash(input string) string {
	sum := Sum256([]byte(input))
	return string(sum[:])
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeIdentifierAuditFixture(t, root, filepath.Join("agent", "runtime_dir.go"), source)
			needle := "crypto/sha256 alias"
			if name == "dot" {
				needle = "crypto/sha256 dot import"
			}
			assertIdentifierAuditFinds(t, root, needle)
		})
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
	assertIdentifierAuditFinds(t, root, "duplicate project identifier declaration")
}

func TestIdentifierAuditTrackedScopeExcludesIgnoredNestedCheckout(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	writeIdentifierAuditFixture(t, root, ".gitignore", "ignored/\n")
	writeIdentifierAuditFixture(t, root, filepath.Join("active", "fixture.go"), `package fixture

func ProjectID(path string) string { return path }
`)
	writeIdentifierAuditFixture(t, root, filepath.Join("ignored", "nested-checkout", "legacy.go"), `package legacy

func ProjectSlug(path string) string { return path }
`)
	if out, err := exec.Command("git", "-C", root, "add", ".gitignore", "active/fixture.go").CombinedOutput(); err != nil {
		t.Fatalf("git add audit fixtures: %v: %s", err, out)
	}

	findings, err := identifierAuditTrackedFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsIdentifierFinding(findings, "active/fixture.go: duplicate project identifier declaration") {
		t.Fatalf("active checkout finding missing: %v", findings)
	}
	for _, finding := range findings {
		if strings.Contains(finding, "nested-checkout") {
			t.Fatalf("nested checkout was audited: %v", findings)
		}
	}
}

func TestIdentifierAudit(t *testing.T) {
	findings, err := identifierAuditTrackedFindings(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("identifier audit found forbidden implementation(s):\n%s", strings.Join(findings, "\n"))
	}
}

// This closed-world inventory records every currently reviewed crypto/sha256
// package operation. A new package use, selector, method shape, alias, or
// package initializer fails until its exact AST fingerprint is reviewed here.
var identifierSHA256Inventory = map[string]map[string]map[string]bool{
	"agent/internal/jobstore/output.go": {
		"outputFileHasPrefixSHA256": {"New()": true},
		"outputFileHasSuffixSHA256": {"New()": true},
		"outputFileSHA256":          {"New()": true},
		"outputBytesSHA256":         {"Sum256(b)": true},
	},
	"agent/internal/tool/registry.go": {"shortHash": {"Sum256(b)": true}},
	"agent/job_watch.go":              {"normalizedWatchConfigHash": {"Sum256(b)": true}},
	"agent/runtime_dir.go":            {"nonProjectHash": {"Sum256([]byte(input))": true}},
	"auth/openai/pkce.go":             {"GeneratePKCE": {"Sum256([]byte(verifier))": true}},
	"cmd/serf-fuzz-harvest/emit.go":   {"write": {"Sum256(encoded)": true}},
	"cmd/serf-hub/image_serve.go": {"findImageInTranscript": {
		"Sum256(p.Image.Data)": true, "Sum256(p.ToolResult.ImageData)": true,
	}, "imageSha": {"Sum256(data)": true}},
	"cmd/serf-hub/internal/hubcore/tree.go": {
		"clusterID": {"Sum256([]byte(project + \"\\x00\" + title))": true},
	},
	"cmd/serf-hub/internal/launchconfig/trust.go": {"canonicalHashTOML": {"Sum256(buf.Bytes())": true}},
	"cmd/serf-hub/output_images.go":               {"outputImageSHA": {"Sum256(data)": true}},
	"fuzz/promoter/emit_go.go":                    {"ShortHash": {"New()": true}},
	"internal/apptranscript/turn_index.go": {
		"anchorAt":                        {"Sum256(data[:n])": true},
		"anchorsMatchObserved":            {"Sum256(data[:n])": true},
		"extendPrefixStamp":               {"New()": true, "Size": true},
		"initialPrefixStamp":              {"Sum256([]byte(\"serf-apptranscript-prefix-v1\"))": true},
		"prefixStamp":                     {"New()": true, "Sum256([]byte(\"serf-apptranscript-prefix-v1\"))": true},
		"turnIndexIntegrityStampObserved": {"Sum256(data)": true},
		"turnIndexJournalStampObserved":   {"Sum256(data)": true},
	},
	"llm/providers/openai/responses_continuation_fingerprint.go": {
		"requestFingerprintForResponsesBody": {"Sum256(b)": true},
	},
	"llm/continuation_secret.go": {
		"deriveContinuationSubkey":  {"New": true},
		"versionedContinuationHMAC": {"New": true},
	},
}

func identifierAuditFindings(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !strings.Contains(filepath.ToSlash(rel), "/") && identifierAuditExcludedRootDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return identifierAuditFindingsForFiles(root, paths)
}

func identifierAuditTrackedFindings(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--", "*.go")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list tracked Go files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	paths := make([]string, 0, bytes.Count(out, []byte{0}))
	for _, rawPath := range bytes.Split(out, []byte{0}) {
		if len(rawPath) == 0 {
			continue
		}
		rel := filepath.ToSlash(string(rawPath))
		rootDir := rel
		if slash := strings.IndexByte(rel, '/'); slash >= 0 {
			rootDir = rel[:slash]
		}
		if identifierAuditExcludedRootDir(rootDir) {
			continue
		}
		paths = append(paths, rel)
	}
	return identifierAuditFindingsForFiles(root, paths)
}

func identifierAuditFindingsForFiles(root string, paths []string) ([]string, error) {
	var findings []string
	fset := token.NewFileSet()
	for _, rel := range paths {
		if filepath.Ext(rel) != ".go" || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if isGeneratedGo(raw) {
			continue
		}
		file, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		addSyntaxIdentifierFindings(&findings, rel, file)
		checkSHA256Findings(&findings, rel, file, fset)
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
		if fn, ok := node.(*ast.FuncDecl); ok && fn.Name != nil {
			switch fn.Name.Name {
			case "ProjectID", "ProjectSlug", "projectSlug":
				*findings = append(*findings, fmt.Sprintf("%s: duplicate project identifier declaration %s", path, fn.Name.Name))
			}
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
	shaNames := map[string]bool{}
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != "crypto/sha256" {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			*findings = append(*findings, path+": crypto/sha256 dot import is not allowed")
			continue
		}
		if imp.Name != nil {
			*findings = append(*findings, path+": crypto/sha256 alias is not in the closed-world inventory")
			continue
		}
		name := "sha256"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		shaNames[name] = true
	}
	if len(shaNames) == 0 {
		return
	}
	inventory, ok := identifierSHA256Inventory[path]
	if !ok {
		*findings = append(*findings, path+": crypto/sha256 import is not in the closed-world inventory")
		return
	}
	parents := astParentMap(file)
	seen := map[string]bool{}
	checkNode := func(node ast.Node, function string) {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || !shaNames[pkg.Name] {
			return
		}
		call, isCall := parents[selector].(*ast.CallExpr)
		fingerprint := selector.Sel.Name
		if isCall && call.Fun == selector {
			fingerprint = shaOperationFingerprint(call, parents, fset)
		}
		key := function + "\x00" + fingerprint
		if !seen[key] {
			seen[key] = true
			if inventory[function] == nil || !inventory[function][fingerprint] {
				*findings = append(*findings, fmt.Sprintf("%s: unreviewed SHA-256 operation %s in %s", path, fingerprint, function))
			}
		}
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				checkNode(node, fn.Name.Name)
				return true
			})
			continue
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			checkNode(node, "<package>")
			return true
		})
	}
}

func astParentMap(file *ast.File) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	ast.Walk(astParentVisitor{parents: parents}, file)
	return parents
}

type astParentVisitor struct {
	parents map[ast.Node]ast.Node
	parent  ast.Node
}

func (v astParentVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	v.parents[node] = v.parent
	return astParentVisitor{parents: v.parents, parent: node}
}

func shaOperationFingerprint(call *ast.CallExpr, parents map[ast.Node]ast.Node, fset *token.FileSet) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, call); err != nil {
		return "<unformattable>"
	}
	text := buf.String()
	for _, prefix := range []string{"sha256.", "cryptohash."} {
		text = strings.TrimPrefix(text, prefix)
	}
	if selector, ok := parents[call].(*ast.SelectorExpr); ok && selector.X == call {
		if outer, ok := parents[selector].(*ast.CallExpr); ok && outer.Fun == selector {
			var outerBuf bytes.Buffer
			if err := format.Node(&outerBuf, fset, outer); err == nil {
				text = strings.TrimPrefix(outerBuf.String(), "sha256.")
			}
		}
	}
	return text
}

func assertIdentifierAuditFinds(t *testing.T, root, needle string) {
	t.Helper()
	findings, err := identifierAuditFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsIdentifierFinding(findings, needle) {
		t.Fatalf("audit did not report %q: %v", needle, findings)
	}
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
	for _, line := range strings.SplitN(string(raw), "\n", 12) {
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
