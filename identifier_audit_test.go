package serf_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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

func TestIdentifierAudit(t *testing.T) {
	findings, err := identifierAuditFindings(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("identifier audit found forbidden implementation(s):\n%s", strings.Join(findings, "\n"))
	}
}

var identifierDuplicatePattern = regexp.MustCompile(`(?m)^\s*func\s+(ProjectID|ProjectSlug|projectSlug)\s*\(`)

// This is intentionally an exact allowlist. These files use SHA-256 for
// non-project concerns (cache keys, image/content fingerprints, PKCE, and
// request signatures); a new SHA-256 import must be reviewed here before it
// can enter production.
var identifierSHA256Allowlist = map[string]string{
	"agent/internal/jobstore/output.go":                          "durable output integrity and content keys",
	"agent/internal/tool/registry.go":                            "tool definition fingerprints",
	"agent/job_watch.go":                                         "watch payload fingerprints",
	"agent/runtime_dir.go":                                       "non-project cache/tool-call signatures",
	"auth/openai/pkce.go":                                        "OAuth PKCE challenge",
	"cmd/serf-fuzz-harvest/emit.go":                              "fuzz artifact names",
	"cmd/serf-hub/image_serve.go":                                "image content lookup",
	"cmd/serf-hub/internal/hubcore/tree.go":                      "synthetic repeated-title cluster IDs",
	"cmd/serf-hub/internal/launchconfig/trust.go":                "canonical launch configuration digest",
	"cmd/serf-hub/output_images.go":                              "image content lookup",
	"fuzz/promoter/emit_go.go":                                   "fuzz corpus fingerprints",
	"llm/continuation_secret.go":                                 "continuation secret HMAC",
	"llm/providers/openai/responses_continuation_fingerprint.go": "request fingerprints",
}

// These files previously contained the removed local project hash/slug
// implementations. Keep a path-specific guard as well as the generic scan so
// a later reintroduction is reported at the migration boundary.
var removedProjectHashImplementations = map[string][]string{
	"agent/internal/worktree/name.go": {
		"func ProjectID(",
		"sha256.Sum256([]byte(canonicalAbsRoot))",
	},
	"cmd/serf-hub/internal/hubcore/tree.go": {
		"func projectSlug(",
		"sha256.Sum256([]byte(path))",
	},
}

func identifierAuditFindings(root string) ([]string, error) {
	var findings []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && identifierAuditExcludedDir(entry.Name()) {
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
		if isGeneratedGo(raw) || identifierAuditInExcludedTree(path) {
			return nil
		}
		source := string(raw)
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		addIdentifierFindings(&findings, rel, source)

		file, parseErr := parser.ParseFile(fset, path, raw, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, imp := range file.Imports {
			if imp.Path.Value != `"crypto/sha256"` {
				continue
			}
			if _, allowed := identifierSHA256Allowlist[rel]; !allowed {
				findings = append(findings, rel+": crypto/sha256 import is not in the reviewed non-project allowlist")
			}
		}
		for _, snippet := range removedProjectHashImplementations[rel] {
			if strings.Contains(source, snippet) {
				findings = append(findings, rel+": removed project-hash implementation contains "+snippet)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(findings)
	return findings, nil
}

func addIdentifierFindings(findings *[]string, path, source string) {
	for _, forbidden := range []string{
		"ulid.Make(",
		"ulid.New(",
		"github.com/oklog/ulid",
	} {
		if strings.Contains(source, forbidden) {
			*findings = append(*findings, path+": forbidden identifier implementation: "+forbidden)
		}
	}
	if identifierDuplicatePattern.MatchString(source) {
		*findings = append(*findings, path+": duplicate project identifier implementation")
	}
}

func identifierAuditExcludedDir(name string) bool {
	switch name {
	case ".git", ".superpowers", "docs", "identifier":
		return true
	default:
		return false
	}
}

func identifierAuditInExcludedTree(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "identifier" || part == ".superpowers" || part == "docs" {
			return true
		}
	}
	return false
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
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
