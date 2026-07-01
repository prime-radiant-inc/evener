package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViolationString(t *testing.T) {
	v := Violation{File: "a/b.go", Line: 42, Message: "boom"}
	if got := v.String(); got != "a/b.go:42: boom" {
		t.Fatalf("String()=%q", got)
	}
}

func TestIsUpstreamCamelKey(t *testing.T) {
	for _, key := range []string{"mcpServers", "enabledPlugins"} {
		if !isUpstreamCamelKey(key) {
			t.Errorf("isUpstreamCamelKey(%q)=false, want true", key)
		}
	}
	for _, key := range []string{"workingDir", "model", "servers"} {
		if isUpstreamCamelKey(key) {
			t.Errorf("isUpstreamCamelKey(%q)=true, want false", key)
		}
	}
	// The upstream camel keys must be silent even in ordinary (snake-default) paths.
	if msg := checkJSONTag("mcpServers", "internal/runner/run.go"); msg != "" {
		t.Fatalf("mcpServers should be exempt, got %q", msg)
	}
}

func TestCheckTOMLTag(t *testing.T) {
	if msg := checkTOMLTag("working_dir"); msg != "" {
		t.Fatalf("snake_case toml tag should pass, got %q", msg)
	}
	if msg := checkTOMLTag("-"); msg != "" {
		t.Fatalf("skipped tag should pass, got %q", msg)
	}
	if msg := checkTOMLTag("workingDir"); msg == "" || !strings.Contains(msg, "snake_case") {
		t.Fatalf("camelCase toml tag should fail, got %q", msg)
	}
}

func TestToCamelCaseEmpty(t *testing.T) {
	if got := toCamelCase(""); got != "" {
		t.Fatalf("toCamelCase(empty)=%q, want empty", got)
	}
	// Leading underscore yields an empty first part that must be skipped.
	if got := toCamelCase("_working_dir"); got != "workingDir" {
		t.Fatalf("toCamelCase(_working_dir)=%q, want workingDir", got)
	}
}

func TestCheckGoFileParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.go")
	if err := os.WriteFile(path, []byte("package x\nthis is not go\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	vs, err := checkGoFile(path, "broken.go")
	if err != nil {
		t.Fatalf("checkGoFile should report parse errors as violations, not fail: %v", err)
	}
	if len(vs) != 1 || !strings.Contains(vs[0].Message, "parse error") {
		t.Fatalf("expected a parse-error violation, got %+v", vs)
	}
}

// A triple-quoted TOML string must not be linted line-by-line, and quoted
// table keys are accepted verbatim.
func TestCheckTOMLFileMultilineAndQuotedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	body := "prompt = \"\"\"\n" +
		"badKey = should-not-be-linted\n" +
		"\"\"\"\n" +
		"\n" +
		"[\"quoted.key.with.dots\"]\n" +
		"inner_key = 1\n" +
		"\n" +
		"real_key = 2\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	vs, err := checkTOMLFile(path, "x.toml")
	if err != nil {
		t.Fatalf("checkTOMLFile: %v", err)
	}
	for _, v := range vs {
		if strings.Contains(v.Message, "badKey") {
			t.Fatalf("content inside a triple-quoted string was linted: %+v", vs)
		}
		if strings.Contains(v.Message, "quoted.key") {
			t.Fatalf("quoted table key should be accepted verbatim: %+v", vs)
		}
	}
}

func TestCheckTOMLFileMissingFile(t *testing.T) {
	if _, err := checkTOMLFile(filepath.Join(t.TempDir(), "nope.toml"), "nope.toml"); err == nil {
		t.Fatal("expected error reading a missing toml file")
	}
}

// Run in verbose mode over a tree containing both a .go and a .toml violation
// exercises the verbose logging path and the TOML dispatch branch.
func TestRunVerboseCoversGoAndTOML(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "x"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	goSrc := "package x\ntype T struct { F string `json:\"badField\"` }\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "x.go"), []byte(goSrc), 0o644); err != nil {
		t.Fatalf("seed go: %v", err)
	}
	tomlSrc := "bad-key = 1\ngood_key = 2\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "x.toml"), []byte(tomlSrc), 0o644); err != nil {
		t.Fatalf("seed toml: %v", err)
	}

	vs, err := Run(root, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawGo, sawTOML bool
	for _, v := range vs {
		if strings.Contains(v.Message, "badField") {
			sawGo = true
		}
		if strings.Contains(v.Message, "bad-key") {
			sawTOML = true
		}
	}
	if !sawGo || !sawTOML {
		t.Fatalf("expected both go and toml violations, got %+v", vs)
	}
}

// Hidden directories below the top level are skipped by the walker.
func TestIsExcludedHiddenSegment(t *testing.T) {
	if !isExcluded("internal/.hidden/x.go") {
		t.Fatal("a nested hidden dir segment should be excluded")
	}
	// .github is the documented exception.
	if isExcluded(".github/workflows/ci.go") {
		t.Fatal(".github must not be excluded")
	}
	if isExcluded("internal/x/x.go") {
		t.Fatal("ordinary path should not be excluded")
	}
}
