package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- predicate tests -------------------------------------------------------

func TestIsCamelCase(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"model", true},
		{"name", true},
		{"id", true},
		{"url", true},
		{"workingDir", true},
		{"reasoningEffort", true},
		{"iconURL", true},  // acronym suffix is fine
		{"toolCallId", true},
		{"working_dir", false},
		{"WorkingDir", false},
		{"working-dir", false},
		{"URL", false}, // all-caps is not lower-led
		{"working dir", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isCamelCase(c.in); got != c.ok {
				t.Fatalf("isCamelCase(%q) = %v, want %v", c.in, got, c.ok)
			}
		})
	}
}

func TestIsKebabCase(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"model", true},
		{"name", true},
		{"working-dir", true},
		{"reasoning-effort", true},
		{"max-rounds", true},
		{"a-b-c", true},
		{"working_dir", false},
		{"workingDir", false},
		{"working-Dir", false},
		{"-working", false},
		{"working-", false},
		{"working--dir", false},
		{"WORKING", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isKebabCase(c.in); got != c.ok {
				t.Fatalf("isKebabCase(%q) = %v, want %v", c.in, got, c.ok)
			}
		})
	}
}

// --- tagKey + suggestion ---------------------------------------------------

func TestTagKey(t *testing.T) {
	cases := []struct {
		in   string
		key  string
		skip bool
	}{
		{"name", "name", false},
		{"name,omitempty", "name", false},
		{"-", "", true},
		{"-,", "", true},
		{",omitempty", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		k, skip := tagKey(c.in)
		if k != c.key || skip != c.skip {
			t.Fatalf("tagKey(%q) = (%q,%v), want (%q,%v)", c.in, k, skip, c.key, c.skip)
		}
	}
}

func TestSuggestions(t *testing.T) {
	if got := toCamelCase("working_dir"); got != "workingDir" {
		t.Errorf("toCamelCase(working_dir) = %q", got)
	}
	if got := toCamelCase("max_subagent_depth"); got != "maxSubagentDepth" {
		t.Errorf("toCamelCase(max_subagent_depth) = %q", got)
	}
	if got := toKebabCase("workingDir"); got != "working-dir" {
		t.Errorf("toKebabCase(workingDir) = %q", got)
	}
	if got := toKebabCase("max_subagent_depth"); got != "max-subagent-depth" {
		t.Errorf("toKebabCase(max_subagent_depth) = %q", got)
	}
	if got := toKebabCase("MCPConfigs"); got != "mcp-configs" {
		t.Errorf("toKebabCase(MCPConfigs) = %q", got)
	}
}

// --- end-to-end fixture tests ----------------------------------------------

func TestCheckGoFile_Violations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	src := `package x

type Good struct {
	Model      string ` + "`json:\"model\"`" + `
	WorkingDir string ` + "`json:\"workingDir\"`" + `
	ID         string ` + "`json:\"id\"`" + `
	// serf:naming-ignore: legacy wire format from upstream codex
	LegacyField string ` + "`json:\"legacy_field\"`" + `
	Skipped     string ` + "`json:\"-\"`" + `
}

type Bad struct {
	WorkingDir      string ` + "`json:\"working_dir\"`" + `
	ReasoningEffort string ` + "`json:\"reasoning-effort\"`" + `
	Pascal          string ` + "`json:\"WorkingDir\"`" + `
}

type TomlGood struct {
	WorkingDir string ` + "`toml:\"working-dir\"`" + `
	Model      string ` + "`toml:\"model\"`" + `
}

type TomlBad struct {
	WorkingDir string ` + "`toml:\"working_dir\"`" + `
	Camel      string ` + "`toml:\"workingDir\"`" + `
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	violations, err := checkGoFile(path, "x.go")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(violations))
	for _, v := range violations {
		got = append(got, v.Message)
	}

	wantContains := []string{
		`json tag "working_dir"`,
		`json tag "reasoning-effort"`,
		`json tag "WorkingDir"`,
		`toml tag "working_dir"`,
		`toml tag "workingDir"`,
	}
	for _, w := range wantContains {
		found := false
		for _, m := range got {
			if strings.Contains(m, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing violation %q; got: %v", w, got)
		}
	}

	// Things that must NOT trigger:
	mustNot := []string{
		`json tag "model"`,
		`json tag "workingDir"`,
		`json tag "id"`,
		`json tag "legacy_field"`, // ignored by marker
		`json tag "-"`,             // skipped
		`toml tag "working-dir"`,
		`toml tag "model"`,
	}
	for _, w := range mustNot {
		for _, m := range got {
			if strings.Contains(m, w) {
				t.Errorf("unexpected violation %q", m)
			}
		}
	}
}

func TestCheckTOMLFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	body := `# top-level config
schema = 1
working-dir = "/tmp"
working_dir = "/tmp"
camelCase = true

[good-section]
inner-key = 1

[bad_section]
some_key = 2

# serf:naming-ignore: kata.toml legacy
some_other_key = 3
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	violations, err := checkTOMLFile(path, "x.toml")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(violations))
	for _, v := range violations {
		got = append(got, v.Message)
	}
	expectContains := []string{
		`toml key "working_dir"`,
		`toml key "camelCase"`,
		`toml table key "bad_section"`,
		`toml key "some_key"`,
	}
	for _, w := range expectContains {
		found := false
		for _, m := range got {
			if strings.Contains(m, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q; got: %v", w, got)
		}
	}
	mustNot := []string{
		`toml key "schema"`,
		`toml key "working-dir"`,
		`toml table key "good-section"`,
		`toml key "inner-key"`,
		`toml key "some_other_key"`, // covered by ignore marker
	}
	for _, w := range mustNot {
		for _, m := range got {
			if strings.Contains(m, w) {
				t.Errorf("unexpected violation %q", m)
			}
		}
	}
}

func TestRun_SkipsExcludedPaths(t *testing.T) {
	root := t.TempDir()

	// File under inspo should be skipped despite obvious violations.
	if err := os.MkdirAll(filepath.Join(root, "inspo", "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	skipSrc := `package codex
type X struct { F string ` + "`json:\"bad_field\"`" + ` }
`
	if err := os.WriteFile(filepath.Join(root, "inspo", "codex", "x.go"), []byte(skipSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// A vendored file likewise.
	if err := os.MkdirAll(filepath.Join(root, "vendor", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "foo", "x.go"), []byte(skipSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// testdata directory.
	if err := os.MkdirAll(filepath.Join(root, "internal", "x", "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "testdata", "x.go"), []byte(skipSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// A real file in scope.
	if err := os.MkdirAll(filepath.Join(root, "internal", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "x.go"), []byte(skipSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	vs, err := Run(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(vs), vs)
	}
	if !strings.HasPrefix(vs[0].File, "internal/x/x.go") {
		t.Errorf("expected violation from internal/x/x.go, got %s", vs[0].File)
	}
}
