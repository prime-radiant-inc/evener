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
		{"iconURL", true}, // acronym suffix is fine
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

func TestIsSnakeCase(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"model", true},
		{"name", true},
		{"working_dir", true},
		{"reasoning_effort", true},
		{"max_subagent_depth", true},
		{"a_b_c", true},
		{"id", true},
		{"workingDir", false},
		{"working-dir", false},
		{"WorkingDir", false},
		{"WORKING", false},
		{"_working", false},
		{"working_", false},
		{"working__dir", false},
		{"working dir", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isSnakeCase(c.in); got != c.ok {
				t.Fatalf("isSnakeCase(%q) = %v, want %v", c.in, got, c.ok)
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
	if got := toSnakeCase("workingDir"); got != "working_dir" {
		t.Errorf("toSnakeCase(workingDir) = %q", got)
	}
	if got := toSnakeCase("working-dir"); got != "working_dir" {
		t.Errorf("toSnakeCase(working-dir) = %q", got)
	}
	if got := toSnakeCase("MCPConfigs"); got != "mcp_configs" {
		t.Errorf("toSnakeCase(MCPConfigs) = %q", got)
	}
	if got := toSnakeCase("reasoningEffort"); got != "reasoning_effort" {
		t.Errorf("toSnakeCase(reasoningEffort) = %q", got)
	}
	if got := toSnakeCase("WorkingDir"); got != "working_dir" {
		t.Errorf("toSnakeCase(WorkingDir) = %q", got)
	}
}

// --- end-to-end fixture tests ----------------------------------------------

func TestCheckGoFile_Violations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	src := `package x

type Good struct {
	Model      string ` + "`json:\"model\"`" + `
	WorkingDir string ` + "`json:\"working_dir\"`" + `
	ID         string ` + "`json:\"id\"`" + `
	// serf:naming-ignore: legacy wire format from upstream tool
	LegacyField string ` + "`json:\"legacyField\"`" + `
	Skipped     string ` + "`json:\"-\"`" + `
}

type Bad struct {
	WorkingDir      string ` + "`json:\"workingDir\"`" + `
	ReasoningEffort string ` + "`json:\"reasoning-effort\"`" + `
	Pascal          string ` + "`json:\"WorkingDir\"`" + `
}

type TomlGood struct {
	WorkingDir string ` + "`toml:\"working_dir\"`" + `
	Model      string ` + "`toml:\"model\"`" + `
}

type TomlBad struct {
	Kebab string ` + "`toml:\"working-dir\"`" + `
	Camel string ` + "`toml:\"workingDir\"`" + `
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
		`json tag "workingDir"`,
		`json tag "reasoning-effort"`,
		`json tag "WorkingDir"`,
		`toml tag "working-dir"`,
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
		`json tag "working_dir"`,
		`json tag "id"`,
		`json tag "legacyField"`, // ignored by marker
		`json tag "-"`,           // skipped
		`toml tag "working_dir"`,
		`toml tag "model"`,
	}
	for _, w := range mustNot {
		for _, m := range got {
			if strings.Contains(m, w) {
				t.Errorf("unexpected violation %q", m)
			}
		}
	}

	// And the suggested fix in the JSON message should be snake_case now.
	wantSuggest := []string{
		`suggest "working_dir"`,
		`suggest "reasoning_effort"`,
	}
	for _, w := range wantSuggest {
		found := false
		for _, m := range got {
			if strings.Contains(m, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing snake_case suggestion %q; got: %v", w, got)
		}
	}
}

// TestCheckJSONTag_AppwireCarveOut verifies that the camelCase regime is
// enforced inside every appwire-adjacent tree (codex protocol requires it)
// and that snake_case tags in those trees become violations. The carve-out
// covers appwire/ (the protocol definition), cmd/serf-hub/internal/appsource/
// and internal/appserver/ (its clients and server-side implementation), and
// server/appwire_*.go (the hub runtime glue).
func TestCheckJSONTag_AppwireCarveOut(t *testing.T) {
	cases := []struct {
		name    string
		rel     string
		tag     string
		wantMsg bool
	}{
		// appwire/ — the protocol definition itself.
		{"appwire camelCase ok", "appwire/types.go", "workingDir", false},
		{"appwire snake bad", "appwire/types.go", "working_dir", true},
		{"appwire single word ok", "appwire/types.go", "id", false},

		// cmd/serf-hub/internal/appsource/ — codex protocol clients.
		{"appsource camelCase ok", "cmd/serf-hub/internal/appsource/codex_source.go", "threadId", false},
		{"appsource snake bad", "cmd/serf-hub/internal/appsource/codex_source.go", "thread_id", true},

		// internal/appserver/ — server-side implementation.
		{"appserver camelCase ok", "internal/appserver/notifier.go", "threadId", false},
		{"appserver snake bad", "internal/appserver/notifier.go", "thread_id", true},

		// server/appwire_*.go — hub runtime glue.
		{"server appwire_runtime camelCase ok", "server/appwire_runtime.go", "turnId", false},
		{"server appwire_projection camelCase ok", "server/appwire_projection.go", "itemId", false},
		{"server appwire_runtime snake bad", "server/appwire_runtime.go", "turn_id", true},

		// internal/appprojector/ — projects codex/appwire payloads.
		{"appprojector camelCase ok", "internal/appprojector/types.go", "workingDir", false},
		{"appprojector snake bad", "internal/appprojector/types.go", "working_dir", true},

		// cmd/serf-hub/internal/launchconfig/ — launch-option schema mirrored on the wire.
		{"launchconfig camelCase ok", "cmd/serf-hub/internal/launchconfig/config.go", "configValue", false},
		{"launchconfig snake bad", "cmd/serf-hub/internal/launchconfig/config.go", "config_value", true},

		// Other files under server/ are NOT carved out; they remain on
		// the snake-default rule.
		{"server non-appwire camel bad", "server/something.go", "turnId", true},
		{"server non-appwire snake ok", "server/something.go", "turn_id", false},

		// Ordinary files are unaffected.
		{"ordinary file camel bad", "internal/runner/run.go", "workingDir", true},
		{"ordinary file snake ok", "internal/runner/run.go", "working_dir", false},
		{"ordinary file single word ok", "internal/runner/run.go", "id", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := checkJSONTag(c.tag, c.rel)
			gotMsg := msg != ""
			if gotMsg != c.wantMsg {
				t.Fatalf("checkJSONTag(%q,%q) message=%q wantMsg=%v", c.tag, c.rel, msg, c.wantMsg)
			}
		})
	}
}

// TestCheckJSONTag_ProvidersCarveOut verifies that JSON tags under
// llm/providers/*/ are exempt from naming checks regardless of style.
func TestCheckJSONTag_ProvidersCarveOut(t *testing.T) {
	cases := []struct {
		rel string
		tag string
	}{
		{"llm/providers/openai/types.go", "tool_calls"},       // OpenAI snake
		{"llm/providers/openai/types.go", "finish_reason"},    // OpenAI snake
		{"llm/providers/google/types.go", "candidateCount"},   // Google camel
		{"llm/providers/google/types.go", "generationConfig"}, // Google camel
		{"llm/providers/anthropic/types.go", "stop_reason"},   // Anthropic snake
	}
	for _, c := range cases {
		if msg := checkJSONTag(c.tag, c.rel); msg != "" {
			t.Errorf("expected providers carve-out to silence %q in %q, got %q", c.tag, c.rel, msg)
		}
	}
}

// TestCheckGoFile_AppwirePath confirms the carve-out reaches through
// checkGoFile end-to-end: a camelCase JSON tag in appwire/ produces
// no violation, while the same struct in an ordinary path does.
func TestCheckGoFile_AppwirePath(t *testing.T) {
	src := `package x
type T struct {
	WorkingDir string ` + "`json:\"workingDir\"`" + `
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Under appwire/: camelCase is required, so no violation.
	vs, err := checkGoFile(path, "appwire/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("appwire path: want 0 violations, got %d: %v", len(vs), vs)
	}

	// Under an ordinary path: camelCase is the violation.
	vs, err = checkGoFile(path, "internal/runner/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("ordinary path: want 1 violation, got %d: %v", len(vs), vs)
	}
	if !strings.Contains(vs[0].Message, `json tag "workingDir"`) {
		t.Errorf("expected workingDir violation, got %q", vs[0].Message)
	}
}

// TestCheckGoFile_ProvidersPath confirms that JSON tags under
// llm/providers/*/ are silent regardless of casing.
func TestCheckGoFile_ProvidersPath(t *testing.T) {
	src := `package x
type T struct {
	A string ` + "`json:\"tool_calls\"`" + `
	B string ` + "`json:\"candidateCount\"`" + `
	C string ` + "`json:\"finish_reason\"`" + `
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	vs, err := checkGoFile(path, "llm/providers/openai/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("providers path: want 0 violations, got %d: %v", len(vs), vs)
	}
}

func TestCheckTOMLFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	body := `# top-level config
schema = 1
working_dir = "/tmp"
working-dir = "/tmp"
camelCase = true

[good_section]
inner_key = 1

[bad-section] # trailing comment
some-key = 2
bad_section.foo-key = 3
bad-section.good_key = 4

# serf:naming-ignore: kata.toml legacy
some-other-key = 3
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
		`toml key "working-dir"`,
		`toml key "camelCase"`,
		`toml table key "bad-section"`,
		`toml key "some-key"`,
		`toml key "foo-key"`,
		`toml key "bad-section"`,
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
		`toml key "working_dir"`,
		`toml table key "good_section"`,
		`toml key "inner_key"`,
		`toml key "some-other-key"`, // covered by ignore marker
	}
	for _, w := range mustNot {
		for _, m := range got {
			if strings.Contains(m, w) {
				t.Errorf("unexpected violation %q", m)
			}
		}
	}
	// Verify suggestion text is snake_case now.
	wantSuggest := []string{
		`suggest "working_dir"`,
		`suggest "camel_case"`,
		`suggest "bad_section"`,
	}
	for _, w := range wantSuggest {
		found := false
		for _, m := range got {
			if strings.Contains(m, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing snake_case suggestion %q; got: %v", w, got)
		}
	}
}

func TestRun_SkipsExcludedPaths(t *testing.T) {
	root := t.TempDir()

	// A camelCase JSON tag is the new violation surface, so use that.
	badSrc := `package codex
type X struct { F string ` + "`json:\"badField\"`" + ` }
`

	// File under inspo should be skipped despite obvious violations.
	if err := os.MkdirAll(filepath.Join(root, "inspo", "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inspo", "codex", "x.go"), []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// A vendored file likewise.
	if err := os.MkdirAll(filepath.Join(root, "vendor", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "foo", "x.go"), []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// testdata directory.
	if err := os.MkdirAll(filepath.Join(root, "internal", "x", "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "testdata", "x.go"), []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// appwire is exempt from the camelCase-is-bad rule.
	if err := os.MkdirAll(filepath.Join(root, "appwire"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "appwire", "x.go"), []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// llm/providers is exempt from JSON checks entirely.
	if err := os.MkdirAll(filepath.Join(root, "llm", "providers", "openai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "llm", "providers", "openai", "x.go"), []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// cmd/serf-namingcheck/ is excluded so the tool never lints itself
	// (test source files contain camelCase string literals that would fire).
	if err := os.MkdirAll(filepath.Join(root, "cmd", "serf-namingcheck"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "serf-namingcheck", "x.go"), []byte(badSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// A real file in scope.
	if err := os.MkdirAll(filepath.Join(root, "internal", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "x.go"), []byte(badSrc), 0o644); err != nil {
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
