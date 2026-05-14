# SP7 — Plugin Manifest Extensions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Claude Code A-tier plugin manifest features to serf: `userConfig` prompt/store/substitute, `bin/` PATH injection, plugin-root `settings.json` (`agent` key), additive `skills` custom paths, and a warn-once mechanism for unsupported fields.

**Architecture:** New per-plugin types (`UserConfigOption`, `ResolvedUserConfig`, `PluginWarning`) live in `agent/`. A small `agent/internal/securestore/` package handles secret persistence (FileStore now, Keychain later). `LoadedPlugin` grows four fields. `LoadPlugin` reads three new manifest fields (`userConfig`, `skills`, plugin-root `settings.json`) and stats `<root>/bin`. Helpers (`ExpandUserConfig`, `UserConfigEnvVars`, `PluginBinPATH`) are pure functions consumed by SP5/SP6/SP8. All exported symbols match the API surface in §2 of `docs/superpowers/specs/2026-05-14-claude-code-compat-sp7-manifest-extensions-design.md`.

**Tech Stack:** Go 1.22+, `encoding/json`, `os` / `os/exec`, table-driven tests, `t.TempDir()` for real filesystem fixtures. No mocks for filesystem or process IO.

**Reference style:** `agent/mcp_config.go`, `agent/mcp_config_test.go`, `agent/plugin.go`, `agent/plugin_test.go`.

---

## Pre-flight

- [ ] **Step 0.1: Confirm sub-spec sections covered**

This plan implements every numbered section of the sub-spec:
- §3 `userConfig` schema → Tasks 5–10
- §4 Prompt flow → Tasks 22–27
- §5 Storage layout → Tasks 11–17 (FileStore, ConfigJSONStore)
- §6 Substitution → Tasks 18–21 (`ResolveUserConfig`, `ExpandUserConfig`)
- §7 `CLAUDE_PLUGIN_OPTION_*` env injection → Task 21
- §8 `bin/` PATH injection → Tasks 28–32
- §9 Plugin-root `settings.json` → Tasks 33–37
- §10 `skills` custom paths → Tasks 38–41
- §11 Warn-once mechanism → Tasks 2–4 (and re-used everywhere)
- §12 Error contracts → exercised throughout
- §13 Package layout → enforced per task

Stubs/collaborator boundaries cited in code comments:
- `SerfConfig.PluginConfigs` is owned by SP1; tests here use an in-memory `PluginConfigStore`. SP1 wires the typed JSON store later.
- SP4 calls `PromptForUserConfig` from the install/enable CLI; this plan only defines and tests the function.
- SP5/SP6 consume `ExpandUserConfig` / `UserConfigEnvVars`; this plan does not modify hook or MCP spawn sites.
- SP8 wires per-surface `UserConfigPrompter` into session entry points; this plan ships the interface and the `MapPrompter` used in tests.

- [ ] **Step 0.2: Note pre-existing files that will be touched**

Existing files modified (additive only):
- `agent/plugin.go` — `PluginManifest` gains five `RawMessage` fields; `LoadedPlugin` gains four fields; `LoadPlugin` populates them; `discoverPluginSkills` gains an override parameter.

New files created:
- `agent/plugin_warnings.go` / `_test.go`
- `agent/plugin_userconfig.go` / `_test.go`
- `agent/plugin_bin.go` / `_test.go`
- `agent/plugin_config_store.go` / `_test.go`
- `agent/plugin_settings.go` / `_test.go`
- `agent/internal/securestore/securestore.go` / `_test.go`
- Fixtures under `agent/testdata/plugins/userconfig-basic/`, `agent/testdata/plugins/plugin-with-bin/`, `agent/testdata/plugins/skills-custom/`, `agent/testdata/plugins/settings-agent/`.

---

## Phase 1 — Manifest extensions (passive RawMessage fields)

### Task 1: Extend `PluginManifest` with five new RawMessage fields

**Files:**
- Modify: `agent/plugin.go:15-28` (add fields to `PluginManifest`)
- Test: `agent/plugin_test.go` (existing file)

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestParsePluginManifest_NewFields(t *testing.T) {
	data := []byte(`{
		"name": "demo",
		"skills": "./extra/skills/",
		"userConfig": {"api_token": {"type": "string", "title": "T", "description": "D"}},
		"outputStyles": {"foo": {}},
		"lspServers": {"bar": {}},
		"experimental": {"themes": {}},
		"channels": ["c1"],
		"dependencies": ["d1"]
	}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Skills) == 0 {
		t.Error("Skills not captured")
	}
	if len(m.UserConfig) == 0 {
		t.Error("UserConfig not captured")
	}
	if len(m.OutputStyles) == 0 {
		t.Error("OutputStyles not captured")
	}
	if len(m.LSPServers) == 0 {
		t.Error("LSPServers not captured")
	}
	if len(m.Experimental) == 0 {
		t.Error("Experimental not captured")
	}
	if len(m.Channels) == 0 {
		t.Error("Channels not captured")
	}
	if len(m.Dependencies) == 0 {
		t.Error("Dependencies not captured")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginManifest_NewFields -v`
Expected: FAIL — fields missing on `PluginManifest`.

- [ ] **Step 3: Add the fields**

Edit `agent/plugin.go`, replace the `PluginManifest` struct block with:

```go
type PluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version,omitempty"`
	Description string          `json:"description,omitempty"`
	Author      json.RawMessage `json:"author,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	Repository  string          `json:"repository,omitempty"`
	License     string          `json:"license,omitempty"`
	Keywords    []string        `json:"keywords,omitempty"`
	Commands    json.RawMessage `json:"commands,omitempty"`
	Agents      json.RawMessage `json:"agents,omitempty"`
	Hooks       json.RawMessage `json:"hooks,omitempty"`
	MCPServers  json.RawMessage `json:"mcpServers,omitempty"`

	// Skills is the manifest "skills" field. May be string, []string, or
	// omitted. Paths are relative to the plugin root and start with "./".
	// Loading is additive: the default "skills/" directory is always scanned.
	Skills json.RawMessage `json:"skills,omitempty"`

	// UserConfig declares the keys prompted on enable and substituted at
	// runtime. Key order is preserved through json.RawMessage decoding so
	// the prompt UX can render fields in declaration order.
	UserConfig json.RawMessage `json:"userConfig,omitempty"`

	// OutputStyles, LSPServers, Experimental, Channels, Dependencies are
	// recognized for the warn-once mechanism only (SP7 §11). They are
	// captured as RawMessage so validation can report "unsupported field"
	// without parsing.
	OutputStyles json.RawMessage `json:"outputStyles,omitempty"`
	LSPServers   json.RawMessage `json:"lspServers,omitempty"`
	Experimental json.RawMessage `json:"experimental,omitempty"`
	Channels     json.RawMessage `json:"channels,omitempty"`
	Dependencies json.RawMessage `json:"dependencies,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParsePluginManifest_NewFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): capture new manifest fields as RawMessage"
```

---

## Phase 2 — Warn-once mechanism

### Task 2: Define `PluginWarning` and per-process dedup

**Files:**
- Create: `agent/plugin_warnings.go`
- Create: `agent/plugin_warnings_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_warnings_test.go`:

```go
package agent

import "testing"

func TestRecordPluginWarning_DedupSameProcess(t *testing.T) {
	resetWarningsForTest()

	w1 := recordPluginWarning("demo", "outputStyles", "unsupported field")
	w2 := recordPluginWarning("demo", "outputStyles", "unsupported field")

	if w1 == nil {
		t.Fatal("first warning should not be nil")
	}
	if w2 != nil {
		t.Fatal("duplicate warning should be suppressed")
	}
}

func TestRecordPluginWarning_DifferentPluginsBothFire(t *testing.T) {
	resetWarningsForTest()

	w1 := recordPluginWarning("alpha", "outputStyles", "x")
	w2 := recordPluginWarning("beta", "outputStyles", "x")

	if w1 == nil || w2 == nil {
		t.Fatal("each plugin should produce its own warning")
	}
}

func TestRecordPluginWarning_ResetForTest(t *testing.T) {
	resetWarningsForTest()
	w1 := recordPluginWarning("demo", "channels", "x")
	resetWarningsForTest()
	w2 := recordPluginWarning("demo", "channels", "x")
	if w1 == nil || w2 == nil {
		t.Fatal("reset should clear seen set")
	}
}

func TestPluginWarning_Fields(t *testing.T) {
	resetWarningsForTest()
	w := recordPluginWarning("demo", "channels", "ignoring channels")
	if w.Field != "channels" {
		t.Errorf("Field = %q", w.Field)
	}
	if w.Message != "ignoring channels" {
		t.Errorf("Message = %q", w.Message)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRecordPluginWarning -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write minimal implementation**

Create `agent/plugin_warnings.go`:

```go
package agent

import "sync"

// PluginWarning is one diagnostic emitted at load time. It is *not* an error.
// Captured on LoadedPlugin so the session can print it once at startup and
// also surface it in `serf plugin list --json`.
type PluginWarning struct {
	Field   string // e.g. "outputStyles", "settings.json:subagentStatusLine"
	Message string
}

var (
	warningsSeen   sync.Map // key: "<pluginID>\x00<field>", value: struct{}{}
	warningsSeenMu sync.Mutex
)

// recordPluginWarning returns a *PluginWarning the first time (pluginID, field)
// is seen in the current process; returns nil on every subsequent call.
func recordPluginWarning(pluginID, field, message string) *PluginWarning {
	key := pluginID + "\x00" + field
	if _, loaded := warningsSeen.LoadOrStore(key, struct{}{}); loaded {
		return nil
	}
	return &PluginWarning{Field: field, Message: message}
}

// resetWarningsForTest clears the dedup map. Tests only.
func resetWarningsForTest() {
	warningsSeen = sync.Map{}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestRecordPluginWarning -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_warnings.go agent/plugin_warnings_test.go
git commit -m "feat(plugin): warn-once dedup for unsupported manifest fields"
```

---

### Task 3: `collectManifestWarnings` for top-level unsupported fields

**Files:**
- Modify: `agent/plugin_warnings.go`
- Modify: `agent/plugin_warnings_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_warnings_test.go`:

```go
func TestCollectManifestWarnings_AllFields(t *testing.T) {
	resetWarningsForTest()
	m := PluginManifest{
		Name:         "demo",
		OutputStyles: []byte(`{}`),
		LSPServers:   []byte(`{}`),
		Experimental: []byte(`{}`),
		Channels:     []byte(`[]`),
		Dependencies: []byte(`[]`),
	}
	ws := collectManifestWarnings("demo", m)
	if len(ws) != 5 {
		t.Fatalf("got %d warnings, want 5", len(ws))
	}
	gotFields := map[string]bool{}
	for _, w := range ws {
		gotFields[w.Field] = true
	}
	for _, f := range []string{"outputStyles", "lspServers", "experimental", "channels", "dependencies"} {
		if !gotFields[f] {
			t.Errorf("missing field %q", f)
		}
	}
}

func TestCollectManifestWarnings_NoneWhenAbsent(t *testing.T) {
	resetWarningsForTest()
	m := PluginManifest{Name: "demo"}
	if ws := collectManifestWarnings("demo", m); len(ws) != 0 {
		t.Errorf("want no warnings, got %d", len(ws))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestCollectManifestWarnings -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_warnings.go`:

```go
// collectManifestWarnings inspects the manifest for top-level fields SP7
// recognizes but does not support, and produces one warning per field
// (deduplicated per-process via recordPluginWarning).
func collectManifestWarnings(pluginID string, m PluginManifest) []PluginWarning {
	checks := []struct {
		field string
		raw   []byte
	}{
		{"outputStyles", m.OutputStyles},
		{"lspServers", m.LSPServers},
		{"experimental", m.Experimental},
		{"channels", m.Channels},
		{"dependencies", m.Dependencies},
	}
	var out []PluginWarning
	for _, c := range checks {
		if len(c.raw) == 0 {
			continue
		}
		msg := "ignoring unsupported field \"" + c.field + "\""
		if w := recordPluginWarning(pluginID, c.field, msg); w != nil {
			out = append(out, *w)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestCollectManifestWarnings -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_warnings.go agent/plugin_warnings_test.go
git commit -m "feat(plugin): collect warnings for unsupported manifest fields"
```

---

### Task 4: `EmitPluginWarnings` writes to stderr

**Files:**
- Modify: `agent/plugin_warnings.go`
- Modify: `agent/plugin_warnings_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_warnings_test.go`:

```go
import "bytes"

func TestEmitPluginWarnings_Format(t *testing.T) {
	var buf bytes.Buffer
	EmitPluginWarnings(&buf, "demo", []PluginWarning{
		{Field: "outputStyles", Message: "ignoring unsupported field \"outputStyles\""},
	})
	got := buf.String()
	want := "serf: plugin \"demo\": ignoring unsupported field \"outputStyles\"\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

Update the existing import block at top of `agent/plugin_warnings_test.go` to include `"bytes"`. Final import block:

```go
import (
	"bytes"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEmitPluginWarnings -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_warnings.go`:

```go
import (
	"fmt"
	"io"
)

// EmitPluginWarnings writes one line per warning to w in the canonical format
// "serf: plugin %q: %s".
func EmitPluginWarnings(w io.Writer, pluginID string, ws []PluginWarning) {
	for _, warn := range ws {
		fmt.Fprintf(w, "serf: plugin %q: %s\n", pluginID, warn.Message)
	}
}
```

NOTE: the existing imports in `agent/plugin_warnings.go` only contain `sync`. Replace the import block at the top with:

```go
import (
	"fmt"
	"io"
	"sync"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestEmitPluginWarnings -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_warnings.go agent/plugin_warnings_test.go
git commit -m "feat(plugin): EmitPluginWarnings stderr formatter"
```

---

## Phase 3 — `userConfig` parse

### Task 5: `UserConfigType` constants and `UserConfigOption` struct

**Files:**
- Create: `agent/plugin_userconfig.go`
- Create: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_userconfig_test.go`:

```go
package agent

import "testing"

func TestUserConfigType_Constants(t *testing.T) {
	cases := []struct {
		typ  UserConfigType
		want string
	}{
		{UserConfigString, "string"},
		{UserConfigNumber, "number"},
		{UserConfigBoolean, "boolean"},
		{UserConfigDirectory, "directory"},
		{UserConfigFile, "file"},
	}
	for _, c := range cases {
		if string(c.typ) != c.want {
			t.Errorf("%v string = %q, want %q", c.typ, string(c.typ), c.want)
		}
	}
}

func TestUserConfigOption_ZeroValue(t *testing.T) {
	var o UserConfigOption
	if o.Key != "" || o.Type != "" {
		t.Errorf("zero value should be empty: %+v", o)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestUserConfigType -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Create `agent/plugin_userconfig.go`:

```go
package agent

// UserConfigType is the declared type of a userConfig option.
type UserConfigType string

const (
	UserConfigString    UserConfigType = "string"
	UserConfigNumber    UserConfigType = "number"
	UserConfigBoolean   UserConfigType = "boolean"
	UserConfigDirectory UserConfigType = "directory"
	UserConfigFile      UserConfigType = "file"
)

// UserConfigOption is one declared user-config field after parsing.
type UserConfigOption struct {
	Key         string         // map key from manifest, lower_snake_case enforced
	Type        UserConfigType // see UserConfigString etc.
	Title       string         // required
	Description string         // required
	Sensitive   bool
	Required    bool
	Default     any      // type-dependent; nil when unset
	Multiple    bool     // string-type only
	Min         *float64 // number-type only
	Max         *float64 // number-type only
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestUserConfigType -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): UserConfigType and UserConfigOption types"
```

---

### Task 6: `ParseUserConfig` happy path (nil / empty / single string)

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
import "encoding/json"

func TestParseUserConfig_NilAndEmpty(t *testing.T) {
	opts, err := ParseUserConfig(nil)
	if err != nil || opts != nil {
		t.Errorf("nil: got (%v, %v)", opts, err)
	}
	opts, err = ParseUserConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(opts) != 0 {
		t.Errorf("empty: got %d opts", len(opts))
	}
}

func TestParseUserConfig_SingleString(t *testing.T) {
	raw := json.RawMessage(`{
		"api_endpoint": {
			"type": "string",
			"title": "API",
			"description": "endpoint URL",
			"required": true,
			"default": "https://api.example.com"
		}
	}`)
	opts, err := ParseUserConfig(raw)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("got %d opts", len(opts))
	}
	o := opts[0]
	if o.Key != "api_endpoint" {
		t.Errorf("Key = %q", o.Key)
	}
	if o.Type != UserConfigString {
		t.Errorf("Type = %q", o.Type)
	}
	if o.Title != "API" || o.Description != "endpoint URL" {
		t.Errorf("Title/Desc wrong: %+v", o)
	}
	if !o.Required {
		t.Error("Required should be true")
	}
	if o.Default != "https://api.example.com" {
		t.Errorf("Default = %v", o.Default)
	}
}
```

Update the imports block at top of `agent/plugin_userconfig_test.go`:

```go
import (
	"encoding/json"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseUserConfig -v`
Expected: FAIL — `ParseUserConfig` undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// userConfigKeyRe enforces lower_snake_case identifiers (stricter than
// Claude Code; see SP7 §3).
var userConfigKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ParseUserConfig decodes the manifest's userConfig blob into an ordered
// slice. Empty input returns (nil, nil). Order matches JSON declaration
// order so the prompt UX is deterministic.
func ParseUserConfig(raw json.RawMessage) ([]UserConfigOption, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return nil, fmt.Errorf("userConfig: must be an object")
	}
	keys, err := orderedJSONObjectKeys(raw)
	if err != nil {
		return nil, fmt.Errorf("userConfig: %w", err)
	}
	if len(keys) == 0 {
		return []UserConfigOption{}, nil
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return nil, fmt.Errorf("userConfig: %w", err)
	}
	opts := make([]UserConfigOption, 0, len(keys))
	for _, k := range keys {
		o, err := parseUserConfigOption(k, asMap[k])
		if err != nil {
			return nil, err
		}
		opts = append(opts, o)
	}
	return opts, nil
}

// parseUserConfigOption parses one entry. Validation per SP7 §3 lives here.
// Full per-type rules are added in later tasks; this minimal version handles
// the string-type happy path.
func parseUserConfigOption(key string, raw json.RawMessage) (UserConfigOption, error) {
	if !userConfigKeyRe.MatchString(key) {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: identifier must match [a-z][a-z0-9_]*", key)
	}
	var body struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Sensitive   bool            `json:"sensitive"`
		Required    bool            `json:"required"`
		Default     json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: %w", key, err)
	}
	if body.Type == "" {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: required", key, "type")
	}
	if body.Title == "" {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: required", key, "title")
	}
	if body.Description == "" {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: required", key, "description")
	}
	opt := UserConfigOption{
		Key:         key,
		Type:        UserConfigType(body.Type),
		Title:       body.Title,
		Description: body.Description,
		Sensitive:   body.Sensitive,
		Required:    body.Required,
	}
	if len(body.Default) > 0 {
		var v any
		if err := json.Unmarshal(body.Default, &v); err != nil {
			return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: %w", key, "default", err)
		}
		opt.Default = v
	}
	return opt, nil
}

// orderedJSONObjectKeys returns the top-level keys of a JSON object in
// declaration order. Uses a streaming decoder.
func orderedJSONObjectKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected object, got %v", tok)
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %v", t)
		}
		keys = append(keys, k)
		// Skip the value.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
```

NOTE: replace the existing first line `package agent` in `plugin_userconfig.go` with both the package and the import block. Full top of file becomes:

```go
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseUserConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): ParseUserConfig happy path and identifier rule"
```

---

### Task 7: `ParseUserConfig` per-type validation (number/boolean/directory/file)

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test (table-driven)**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestParseUserConfig_TypeValidation(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantErr   string
		wantValid bool
	}{
		{
			name: "number with min/max",
			raw: `{"timeout":{"type":"number","title":"T","description":"D","default":5000,"min":100,"max":60000}}`,
			wantValid: true,
		},
		{
			name: "boolean default",
			raw:  `{"verbose":{"type":"boolean","title":"V","description":"D","default":false}}`,
			wantValid: true,
		},
		{
			name: "directory",
			raw:  `{"ws":{"type":"directory","title":"W","description":"D"}}`,
			wantValid: true,
		},
		{
			name: "file",
			raw:  `{"creds":{"type":"file","title":"C","description":"D"}}`,
			wantValid: true,
		},
		{
			name:    "unknown type",
			raw:     `{"x":{"type":"int","title":"T","description":"D"}}`,
			wantErr: `field "type": must be one of string, number, boolean, directory, file`,
		},
		{
			name:    "uppercase key",
			raw:     `{"API_TOKEN":{"type":"string","title":"T","description":"D"}}`,
			wantErr: "identifier must match",
		},
		{
			name:    "missing type",
			raw:     `{"x":{"title":"T","description":"D"}}`,
			wantErr: `field "type": required`,
		},
		{
			name:    "missing title",
			raw:     `{"x":{"type":"string","description":"D"}}`,
			wantErr: `field "title": required`,
		},
		{
			name:    "missing description",
			raw:     `{"x":{"type":"string","title":"T"}}`,
			wantErr: `field "description": required`,
		},
		{
			name:    "multiple on number",
			raw:     `{"x":{"type":"number","title":"T","description":"D","multiple":true}}`,
			wantErr: `field "multiple": only valid when type is "string"`,
		},
		{
			name:    "min on string",
			raw:     `{"x":{"type":"string","title":"T","description":"D","min":1}}`,
			wantErr: `field "min": only valid when type is "number"`,
		},
		{
			name:    "max on string",
			raw:     `{"x":{"type":"string","title":"T","description":"D","max":1}}`,
			wantErr: `field "max": only valid when type is "number"`,
		},
		{
			name:    "default type mismatch (boolean got string)",
			raw:     `{"x":{"type":"boolean","title":"T","description":"D","default":"yes"}}`,
			wantErr: `field "default": expected boolean, got string`,
		},
		{
			name:    "default type mismatch (number got string)",
			raw:     `{"x":{"type":"number","title":"T","description":"D","default":"5"}}`,
			wantErr: `field "default": expected number, got string`,
		},
		{
			name:    "top-level not object",
			raw:     `[1,2,3]`,
			wantErr: "userConfig: must be an object",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseUserConfig(json.RawMessage(c.raw))
			if c.wantValid {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseUserConfig_TypeValidation -v`
Expected: FAIL — type validation not implemented.

- [ ] **Step 3: Update `parseUserConfigOption`**

Replace the body of `parseUserConfigOption` in `agent/plugin_userconfig.go` with:

```go
func parseUserConfigOption(key string, raw json.RawMessage) (UserConfigOption, error) {
	if !userConfigKeyRe.MatchString(key) {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: identifier must match [a-z][a-z0-9_]*", key)
	}
	var body struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Sensitive   bool            `json:"sensitive"`
		Required    bool            `json:"required"`
		Default     json.RawMessage `json:"default"`
		Multiple    *bool           `json:"multiple,omitempty"`
		Min         *float64        `json:"min,omitempty"`
		Max         *float64        `json:"max,omitempty"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: %w", key, err)
	}
	if body.Type == "" {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: required", key, "type")
	}
	switch UserConfigType(body.Type) {
	case UserConfigString, UserConfigNumber, UserConfigBoolean, UserConfigDirectory, UserConfigFile:
	default:
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: must be one of string, number, boolean, directory, file", key, "type")
	}
	if body.Title == "" {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: required", key, "title")
	}
	if body.Description == "" {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: required", key, "description")
	}
	typ := UserConfigType(body.Type)
	if body.Multiple != nil && typ != UserConfigString {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: only valid when type is \"string\"", key, "multiple")
	}
	if body.Min != nil && typ != UserConfigNumber {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: only valid when type is \"number\"", key, "min")
	}
	if body.Max != nil && typ != UserConfigNumber {
		return UserConfigOption{}, fmt.Errorf("userConfig.%s: field %q: only valid when type is \"number\"", key, "max")
	}
	opt := UserConfigOption{
		Key:         key,
		Type:        typ,
		Title:       body.Title,
		Description: body.Description,
		Sensitive:   body.Sensitive,
		Required:    body.Required,
		Min:         body.Min,
		Max:         body.Max,
	}
	if body.Multiple != nil {
		opt.Multiple = *body.Multiple
	}
	if len(body.Default) > 0 {
		v, err := decodeUserConfigDefault(key, typ, opt.Multiple, body.Default)
		if err != nil {
			return UserConfigOption{}, err
		}
		opt.Default = v
	}
	return opt, nil
}

// decodeUserConfigDefault validates and decodes a `default` value against
// the option's type. For multiple-string fields, an array is accepted.
func decodeUserConfigDefault(key string, typ UserConfigType, multiple bool, raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("userConfig.%s: field %q: %w", key, "default", err)
	}
	mismatch := func(want, got string) error {
		return fmt.Errorf("userConfig.%s: field %q: expected %s, got %s", key, "default", want, got)
	}
	switch typ {
	case UserConfigString, UserConfigDirectory, UserConfigFile:
		if multiple {
			if _, ok := v.([]any); !ok {
				return nil, mismatch("array of string", goJSONKind(v))
			}
		} else if _, ok := v.(string); !ok {
			return nil, mismatch("string", goJSONKind(v))
		}
	case UserConfigNumber:
		if _, ok := v.(float64); !ok {
			return nil, mismatch("number", goJSONKind(v))
		}
	case UserConfigBoolean:
		if _, ok := v.(bool); !ok {
			return nil, mismatch("boolean", goJSONKind(v))
		}
	}
	return v, nil
}

func goJSONKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseUserConfig_TypeValidation -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): per-type userConfig validation"
```

---

### Task 8: Declaration order preserved + `multiple` defaults

**Files:**
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestParseUserConfig_DeclarationOrderPreserved(t *testing.T) {
	raw := json.RawMessage(`{
		"zebra": {"type":"string","title":"Z","description":"D"},
		"alpha": {"type":"string","title":"A","description":"D"},
		"mango": {"type":"string","title":"M","description":"D"}
	}`)
	opts, err := ParseUserConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{opts[0].Key, opts[1].Key, opts[2].Key}
	want := []string{"zebra", "alpha", "mango"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("opt %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseUserConfig_MultipleStringDefaultArray(t *testing.T) {
	raw := json.RawMessage(`{
		"hosts": {
			"type":"string",
			"title":"H",
			"description":"D",
			"multiple": true,
			"default": ["a","b"]
		}
	}`)
	opts, err := ParseUserConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !opts[0].Multiple {
		t.Error("Multiple should be true")
	}
	arr, ok := opts[0].Default.([]any)
	if !ok {
		t.Fatalf("Default kind = %T", opts[0].Default)
	}
	if len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
		t.Errorf("Default = %v", arr)
	}
}

func TestParseUserConfig_NumberMinMax(t *testing.T) {
	raw := json.RawMessage(`{
		"timeout": {
			"type":"number","title":"T","description":"D",
			"default": 5000, "min": 100, "max": 60000
		}
	}`)
	opts, err := ParseUserConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if opts[0].Min == nil || *opts[0].Min != 100 {
		t.Errorf("Min = %v", opts[0].Min)
	}
	if opts[0].Max == nil || *opts[0].Max != 60000 {
		t.Errorf("Max = %v", opts[0].Max)
	}
	if opts[0].Default != float64(5000) {
		t.Errorf("Default = %v (%T)", opts[0].Default, opts[0].Default)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseUserConfig_DeclarationOrderPreserved -v`
Run: `go test ./agent/ -run TestParseUserConfig_MultipleStringDefaultArray -v`
Run: `go test ./agent/ -run TestParseUserConfig_NumberMinMax -v`
Expected: PASS (already handled by current implementation).

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_userconfig_test.go
git commit -m "test(plugin): userConfig declaration order and multi-defaults"
```

---

## Phase 4 — SecureStore (FileStore)

### Task 9: `SecureStore` interface and `FileStore` skeleton

**Files:**
- Create: `agent/internal/securestore/securestore.go`
- Create: `agent/internal/securestore/securestore_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/internal/securestore/securestore_test.go`:

```go
package securestore

import (
	"path/filepath"
	"testing"
)

func TestFileStore_GetMissingFile(t *testing.T) {
	dir := t.TempDir()
	s := &FileStore{Path: filepath.Join(dir, "credentials.json")}
	v, ok, err := s.Get("demo", "api_token")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if ok {
		t.Errorf("ok should be false on missing file")
	}
	if v != "" {
		t.Errorf("v = %q, want empty", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/securestore/ -run TestFileStore_GetMissingFile -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Create `agent/internal/securestore/securestore.go`:

```go
// Package securestore stores sensitive plugin userConfig values.
//
// SP7 v1 ships only the file-backed implementation. A keychain backend will
// follow under a build tag; NewSecureStore is the only switch needed.
package securestore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// SecureStore is the persistence layer for sensitive values.
type SecureStore interface {
	Get(pluginID, key string) (string, bool, error)
	Set(pluginID, key, value string) error
	Delete(pluginID, key string) error
}

// FileStore reads/writes a JSON file mode 0600.
type FileStore struct {
	Path string // absolute path to credentials.json
}

type fileSchema struct {
	Credentials map[string]map[string]string `json:"credentials"`
}

func (s *FileStore) load() (*fileSchema, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &fileSchema{Credentials: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var fs fileSchema
	if err := json.Unmarshal(data, &fs); err != nil {
		return nil, err
	}
	if fs.Credentials == nil {
		fs.Credentials = map[string]map[string]string{}
	}
	return &fs, nil
}

// Get returns the stored value or ("", false, nil) when absent.
func (s *FileStore) Get(pluginID, key string) (string, bool, error) {
	fs, err := s.load()
	if err != nil {
		return "", false, err
	}
	m, ok := fs.Credentials[pluginID]
	if !ok {
		return "", false, nil
	}
	v, ok := m[key]
	return v, ok, nil
}

// Set persists value; not yet implemented (stub for compile).
func (s *FileStore) Set(pluginID, key, value string) error {
	return errors.New("not implemented")
}

// Delete is a stub for compile.
func (s *FileStore) Delete(pluginID, key string) error {
	return errors.New("not implemented")
}

// ensureParent creates the parent directory with mode 0700 if absent.
func (s *FileStore) ensureParent() error {
	return os.MkdirAll(filepath.Dir(s.Path), 0o700)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/securestore/ -run TestFileStore_GetMissingFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/securestore/securestore.go agent/internal/securestore/securestore_test.go
git commit -m "feat(securestore): FileStore skeleton with Get on missing file"
```

---

### Task 10: `FileStore.Set` with atomic write + mode 0600

**Files:**
- Modify: `agent/internal/securestore/securestore.go`
- Modify: `agent/internal/securestore/securestore_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/internal/securestore/securestore_test.go`:

```go
import "os"

func TestFileStore_SetWritesMode0600(t *testing.T) {
	dir := t.TempDir()
	s := &FileStore{Path: filepath.Join(dir, "subdir", "credentials.json")}
	if err := s.Set("demo", "api_token", "ghp_xyz"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %v, want 0600", mode)
	}
	parent, err := os.Stat(filepath.Dir(s.Path))
	if err != nil {
		t.Fatalf("Stat parent: %v", err)
	}
	if mode := parent.Mode().Perm(); mode != 0o700 {
		t.Errorf("parent mode = %v, want 0700", mode)
	}
}

func TestFileStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &FileStore{Path: filepath.Join(dir, "credentials.json")}
	if err := s.Set("demo", "api_token", "ghp_xyz"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get("demo", "api_token")
	if err != nil || !ok || v != "ghp_xyz" {
		t.Errorf("Get = (%q, %v, %v)", v, ok, err)
	}
}

func TestFileStore_TwoPluginsIsolated(t *testing.T) {
	dir := t.TempDir()
	s := &FileStore{Path: filepath.Join(dir, "credentials.json")}
	if err := s.Set("alpha", "k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("beta", "k", "2"); err != nil {
		t.Fatal(err)
	}
	v1, _, _ := s.Get("alpha", "k")
	v2, _, _ := s.Get("beta", "k")
	if v1 != "1" || v2 != "2" {
		t.Errorf("alpha=%q beta=%q", v1, v2)
	}
}
```

Update import block:

```go
import (
	"os"
	"path/filepath"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/securestore/ -run TestFileStore_Set -v`
Expected: FAIL — `Set` returns "not implemented".

- [ ] **Step 3: Implement `Set` with atomic write**

Replace the `Set` method in `agent/internal/securestore/securestore.go`:

```go
// Set replaces (pluginID, key) and writes the credentials file atomically.
func (s *FileStore) Set(pluginID, key, value string) error {
	if err := s.ensureParent(); err != nil {
		return err
	}
	fs, err := s.load()
	if err != nil {
		return err
	}
	if fs.Credentials[pluginID] == nil {
		fs.Credentials[pluginID] = map[string]string{}
	}
	fs.Credentials[pluginID][key] = value
	return s.writeAtomic(fs)
}

// writeAtomic serializes fs to <Path>.tmp with mode 0600, then renames.
func (s *FileStore) writeAtomic(fs *fileSchema) error {
	data, err := json.MarshalIndent(fs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Ensure mode is 0600 even if pre-existing file had broader bits.
	return os.Chmod(s.Path, 0o600)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/securestore/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/securestore/securestore.go agent/internal/securestore/securestore_test.go
git commit -m "feat(securestore): FileStore.Set atomic write with mode 0600"
```

---

### Task 11: `FileStore.Delete`

**Files:**
- Modify: `agent/internal/securestore/securestore.go`
- Modify: `agent/internal/securestore/securestore_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/internal/securestore/securestore_test.go`:

```go
func TestFileStore_Delete(t *testing.T) {
	dir := t.TempDir()
	s := &FileStore{Path: filepath.Join(dir, "credentials.json")}
	if err := s.Set("demo", "api_token", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("demo", "api_token"); err != nil {
		t.Fatal(err)
	}
	_, ok, err := s.Get("demo", "api_token")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected absent after Delete")
	}
}

func TestFileStore_DeleteMissingIsNoop(t *testing.T) {
	dir := t.TempDir()
	s := &FileStore{Path: filepath.Join(dir, "credentials.json")}
	if err := s.Delete("demo", "missing"); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/securestore/ -run TestFileStore_Delete -v`
Expected: FAIL — Delete returns "not implemented".

- [ ] **Step 3: Implement**

Replace `Delete` in `agent/internal/securestore/securestore.go`:

```go
// Delete removes (pluginID, key). Missing key is a no-op.
func (s *FileStore) Delete(pluginID, key string) error {
	fs, err := s.load()
	if err != nil {
		return err
	}
	if m, ok := fs.Credentials[pluginID]; ok {
		delete(m, key)
		if len(m) == 0 {
			delete(fs.Credentials, pluginID)
		}
	}
	// If file never existed, don't create one just to record an absence.
	if _, err := os.Stat(s.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return s.writeAtomic(fs)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/securestore/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/securestore/securestore.go agent/internal/securestore/securestore_test.go
git commit -m "feat(securestore): FileStore.Delete"
```

---

### Task 12: `NewSecureStore` factory + agent-package adapter

**Files:**
- Modify: `agent/internal/securestore/securestore.go`
- Create: `agent/plugin_userconfig_secure.go`
- Create: `agent/plugin_userconfig_secure_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_userconfig_secure_test.go`:

```go
package agent

import (
	"path/filepath"
	"testing"
)

func TestNewSecureStore_FileBacked(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.json")
	t.Setenv("HOME", dir) // FileStore default path derives from $HOME
	store, err := NewSecureStore()
	if err != nil {
		t.Fatalf("NewSecureStore: %v", err)
	}
	// We don't assert the literal path the package picks; just round-trip.
	if err := store.Set("p", "k", "v"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := store.Get("p", "k")
	if err != nil || !ok || v != "v" {
		t.Fatalf("got (%q, %v, %v)", v, ok, err)
	}
	_ = credPath // path is implementation detail
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestNewSecureStore_FileBacked -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Create `agent/plugin_userconfig_secure.go`:

```go
package agent

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/prime-radiant-inc/serf/agent/internal/securestore"
)

// SecureStore re-exports the interface so SP7 callers can stay in package agent.
type SecureStore = securestore.SecureStore

// NewSecureStore returns the platform-appropriate SecureStore. SP7 v1 always
// returns a FileStore rooted at $XDG_CONFIG_HOME/serf/credentials.json (or
// $HOME/.config/serf/credentials.json). A keychain backend lands in a
// follow-up; this function is the single switch point.
func NewSecureStore() (SecureStore, error) {
	path, err := defaultCredentialsPath()
	if err != nil {
		return nil, err
	}
	return &securestore.FileStore{Path: path}, nil
}

func defaultCredentialsPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "serf", "credentials.json"), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("HOME not set: cannot locate credentials file")
	}
	return filepath.Join(home, ".config", "serf", "credentials.json"), nil
}
```

NOTE: replace `github.com/prime-radiant-inc/serf` with the real module path. Verify with `head -1 go.mod`. The import line above is illustrative; the executing engineer must check `go.mod` and adjust.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestNewSecureStore_FileBacked -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig_secure.go agent/plugin_userconfig_secure_test.go
git commit -m "feat(plugin): NewSecureStore factory (FileStore default)"
```

---

## Phase 5 — `PluginConfigStore`

### Task 13: In-memory `PluginConfigStore` for tests + interface

**Files:**
- Create: `agent/plugin_config_store.go`
- Create: `agent/plugin_config_store_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_config_store_test.go`:

```go
package agent

import (
	"reflect"
	"testing"
)

func TestMemPluginConfigStore_LoadEmpty(t *testing.T) {
	s := NewMemPluginConfigStore()
	got, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestMemPluginConfigStore_RoundTrip(t *testing.T) {
	s := NewMemPluginConfigStore()
	want := map[string]any{"api_endpoint": "https://x", "timeout": float64(5000)}
	if err := s.Save("demo", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMemPluginConfigStore_ReplaceByKey(t *testing.T) {
	s := NewMemPluginConfigStore()
	if err := s.Save("alpha", map[string]any{"x": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("alpha", map[string]any{"y": "2"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load("alpha")
	if _, ok := got["x"]; ok {
		t.Error("Save should fully replace, not merge")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestMemPluginConfigStore -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Create `agent/plugin_config_store.go`:

```go
package agent

// PluginConfigStore is the persistence layer for plain (non-sensitive)
// userConfig values. Concrete implementations:
//   - MemPluginConfigStore: in-memory (tests)
//   - ConfigJSONStore: reads/writes the pluginConfigs section of
//     ~/.config/serf/config.json (SP1 owns the file; SP7 owns this typed
//     accessor — Task 14).
type PluginConfigStore interface {
	Load(pluginID string) (map[string]any, error)
	Save(pluginID string, values map[string]any) error
}

// MemPluginConfigStore is an in-memory PluginConfigStore for tests.
type MemPluginConfigStore struct {
	data map[string]map[string]any
}

// NewMemPluginConfigStore returns an empty in-memory store.
func NewMemPluginConfigStore() *MemPluginConfigStore {
	return &MemPluginConfigStore{data: map[string]map[string]any{}}
}

// Load returns a copy of stored values for pluginID, or an empty map.
func (s *MemPluginConfigStore) Load(pluginID string) (map[string]any, error) {
	m := s.data[pluginID]
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out, nil
}

// Save replaces stored values for pluginID with a copy of values.
func (s *MemPluginConfigStore) Save(pluginID string, values map[string]any) error {
	if values == nil {
		delete(s.data, pluginID)
		return nil
	}
	cp := make(map[string]any, len(values))
	for k, v := range values {
		cp[k] = v
	}
	s.data[pluginID] = cp
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestMemPluginConfigStore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_config_store.go agent/plugin_config_store_test.go
git commit -m "feat(plugin): PluginConfigStore interface + in-memory impl"
```

---

### Task 14: `ConfigJSONStore` backed by `~/.config/serf/config.json`

**Files:**
- Modify: `agent/plugin_config_store.go`
- Modify: `agent/plugin_config_store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_config_store_test.go`:

```go
import (
	"encoding/json"
	"os"
	"path/filepath"
)

func TestConfigJSONStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s := &ConfigJSONStore{Path: path}
	if err := s.Save("demo", map[string]any{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	// Inspect on-disk shape.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	pc, ok := raw["pluginConfigs"].(map[string]any)
	if !ok {
		t.Fatalf("pluginConfigs missing: %v", raw)
	}
	demo, ok := pc["demo"].(map[string]any)
	if !ok {
		t.Fatalf("demo missing: %v", pc)
	}
	opts, ok := demo["options"].(map[string]any)
	if !ok {
		t.Fatalf("options missing: %v", demo)
	}
	if opts["a"] != "b" {
		t.Errorf("options[a] = %v", opts["a"])
	}
}

func TestConfigJSONStore_MissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := &ConfigJSONStore{Path: filepath.Join(dir, "config.json")}
	got, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestConfigJSONStore_PreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Pre-existing config with mcpServers + other key untouched by SP7.
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"x":{}},"other":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &ConfigJSONStore{Path: path}
	if err := s.Save("demo", map[string]any{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["mcpServers"]; !ok {
		t.Error("mcpServers stripped")
	}
	if raw["other"] != float64(42) {
		t.Errorf("other = %v", raw["other"])
	}
}
```

Update the imports block at top of `agent/plugin_config_store_test.go`:

```go
import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestConfigJSONStore -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_config_store.go`:

```go
import (
	"encoding/json"
	"errors"
	"os"
)

// ConfigJSONStore reads/writes the "pluginConfigs" section of a serf
// config.json file. Other fields in the file are preserved verbatim via
// a generic map[string]any decode.
//
// Per SP7 §5.1 and §13: SP1 owns the SerfConfig struct and its
// PluginConfigs field. This store is a side-door that lets SP7 land
// before SP1 wires the typed accessor. When SP1 lands, callers may switch
// to SerfConfig.PluginConfigs directly; this store remains useful for
// CLI commands that only touch the user-config slice of the file.
type ConfigJSONStore struct {
	Path string
}

type pluginConfigEntry struct {
	Options map[string]any `json:"options"`
}

func (s *ConfigJSONStore) loadRaw() (map[string]any, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

// Load returns the persisted options for pluginID or an empty map.
func (s *ConfigJSONStore) Load(pluginID string) (map[string]any, error) {
	raw, err := s.loadRaw()
	if err != nil {
		return nil, err
	}
	pc, ok := raw["pluginConfigs"].(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	entry, ok := pc[pluginID].(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	opts, ok := entry["options"].(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return opts, nil
}

// Save replaces options for pluginID. The rest of config.json is preserved.
func (s *ConfigJSONStore) Save(pluginID string, values map[string]any) error {
	raw, err := s.loadRaw()
	if err != nil {
		return err
	}
	pc, ok := raw["pluginConfigs"].(map[string]any)
	if !ok {
		pc = map[string]any{}
	}
	if values == nil {
		delete(pc, pluginID)
	} else {
		pc[pluginID] = map[string]any{"options": values}
	}
	raw["pluginConfigs"] = pc

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
```

NOTE: append to the existing imports block in `agent/plugin_config_store.go`. The file's full top should be:

```go
package agent

import (
	"encoding/json"
	"errors"
	"os"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestConfigJSONStore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_config_store.go agent/plugin_config_store_test.go
git commit -m "feat(plugin): ConfigJSONStore for pluginConfigs section"
```

---

## Phase 6 — `ResolvedUserConfig` and resolver

### Task 15: `ResolvedUserConfig` struct and `Lookup`

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestResolvedUserConfig_LookupSemantics(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "endpoint", Type: UserConfigString, Title: "T", Description: "D"},
		{Key: "empty", Type: UserConfigString, Title: "T", Description: "D"},
	}
	r := newResolvedForTest("demo", opts, map[string]string{
		"endpoint": "https://x",
		"empty":    "",
	})
	if v, ok := r.Lookup("endpoint"); !ok || v != "https://x" {
		t.Errorf("endpoint: (%q, %v)", v, ok)
	}
	if v, ok := r.Lookup("empty"); !ok || v != "" {
		t.Errorf("empty: (%q, %v); want (\"\", true)", v, ok)
	}
	if v, ok := r.Lookup("nope"); ok || v != "" {
		t.Errorf("nope: (%q, %v); want (\"\", false)", v, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestResolvedUserConfig_LookupSemantics -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
// ResolvedUserConfig holds resolved (post-default, post-substitution-source)
// values for one plugin. Constructed by ResolveUserConfig or
// PromptForUserConfig. Read via Lookup; never mutated after construction.
type ResolvedUserConfig struct {
	PluginID string
	values   map[string]string
	options  map[string]UserConfigOption
}

// Lookup returns the resolved value. (value, true) on declared key (even
// when value is empty); ("", false) when key was never declared.
func (r *ResolvedUserConfig) Lookup(key string) (string, bool) {
	if r == nil {
		return "", false
	}
	if _, declared := r.options[key]; !declared {
		return "", false
	}
	v := r.values[key]
	return v, true
}

// newResolvedForTest builds a ResolvedUserConfig from a key→value map.
// Tests only — production code goes through ResolveUserConfig.
func newResolvedForTest(pluginID string, opts []UserConfigOption, values map[string]string) *ResolvedUserConfig {
	by := make(map[string]UserConfigOption, len(opts))
	for _, o := range opts {
		by[o.Key] = o
	}
	cp := make(map[string]string, len(values))
	for k, v := range values {
		cp[k] = v
	}
	return &ResolvedUserConfig{PluginID: pluginID, values: cp, options: by}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestResolvedUserConfig_LookupSemantics -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): ResolvedUserConfig.Lookup"
```

---

### Task 16: `stringifyUserConfigValue` for each type (§3.2 table)

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestStringifyUserConfigValue(t *testing.T) {
	cases := []struct {
		name string
		opt  UserConfigOption
		v    any
		want string
	}{
		{"string", UserConfigOption{Type: UserConfigString}, "abc", "abc"},
		{"string multi joins by space", UserConfigOption{Type: UserConfigString, Multiple: true}, []any{"a", "b"}, "a b"},
		{"number int", UserConfigOption{Type: UserConfigNumber}, float64(5000), "5000"},
		{"number float", UserConfigOption{Type: UserConfigNumber}, 1.5, "1.5"},
		{"boolean true", UserConfigOption{Type: UserConfigBoolean}, true, "true"},
		{"boolean false", UserConfigOption{Type: UserConfigBoolean}, false, "false"},
		{"directory passthrough", UserConfigOption{Type: UserConfigDirectory}, "/abs/path", "/abs/path"},
		{"file passthrough", UserConfigOption{Type: UserConfigFile}, "/abs/file", "/abs/file"},
		{"nil → empty", UserConfigOption{Type: UserConfigString}, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stringifyUserConfigValue(c.opt, c.v)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestStringifyUserConfigValue -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
import "strconv"

// stringifyUserConfigValue renders v according to opt.Type (§3.2).
// Multi-value string options join with a single ASCII space. nil → "".
func stringifyUserConfigValue(opt UserConfigOption, v any) string {
	if v == nil {
		return ""
	}
	switch opt.Type {
	case UserConfigString, UserConfigDirectory, UserConfigFile:
		if opt.Multiple {
			arr, ok := v.([]any)
			if !ok {
				if s, ok := v.(string); ok {
					return s
				}
				return ""
			}
			parts := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok {
					parts = append(parts, s)
				}
			}
			return joinSpace(parts)
		}
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	case UserConfigNumber:
		if f, ok := v.(float64); ok {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return ""
	case UserConfigBoolean:
		if b, ok := v.(bool); ok {
			if b {
				return "true"
			}
			return "false"
		}
		return ""
	}
	return ""
}

func joinSpace(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}
```

NOTE: append `"strconv"` to the existing import block of `agent/plugin_userconfig.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestStringifyUserConfigValue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): stringify userConfig values per type"
```

---

### Task 17: Tilde expansion helper for `directory` / `file`

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestExpandTilde(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	cases := []struct {
		in, want string
	}{
		{"~/projects", "/Users/test/projects"},
		{"~", "/Users/test"},
		{"~/", "/Users/test/"},
		{"/abs/path", "/abs/path"},
		{"relative", "relative"},
		{"", ""},
	}
	for _, c := range cases {
		got := expandTilde(c.in)
		if got != c.want {
			t.Errorf("expandTilde(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExpandTilde -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
// expandTilde replaces a leading "~" with $HOME. Returns input unchanged
// when there is no leading "~" or $HOME is unset.
func expandTilde(s string) string {
	if s == "" || s[0] != '~' {
		return s
	}
	home := os.Getenv("HOME")
	if home == "" {
		return s
	}
	if s == "~" {
		return home
	}
	if len(s) > 1 && s[1] == '/' {
		return home + s[1:]
	}
	return s
}
```

NOTE: add `"os"` to the import block of `agent/plugin_userconfig.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestExpandTilde -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): expandTilde helper"
```

---

### Task 18: `ResolveUserConfig` with defaults + sensitive split

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
import "path/filepath"

func TestResolveUserConfig_DefaultsAndStorage(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "endpoint", Type: UserConfigString, Title: "T", Description: "D", Default: "https://default"},
		{Key: "token", Type: UserConfigString, Title: "T", Description: "D", Sensitive: true, Required: true},
		{Key: "verbose", Type: UserConfigBoolean, Title: "T", Description: "D", Default: false},
		{Key: "timeout", Type: UserConfigNumber, Title: "T", Description: "D", Default: float64(5000)},
		{Key: "workspace", Type: UserConfigDirectory, Title: "T", Description: "D", Default: "~/projects"},
	}
	t.Setenv("HOME", "/Users/test")

	plain := NewMemPluginConfigStore()
	_ = plain.Save("demo", map[string]any{"endpoint": "https://override"})
	secure := &memSecureStore{m: map[string]string{"demo/token": "ghp_x"}}

	r, missing, err := ResolveUserConfig("demo", opts, plain, secure)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	checks := map[string]string{
		"endpoint":  "https://override",
		"token":     "ghp_x",
		"verbose":   "false",
		"timeout":   "5000",
		"workspace": "/Users/test/projects",
	}
	for k, want := range checks {
		got, ok := r.Lookup(k)
		if !ok || got != want {
			t.Errorf("Lookup(%q) = (%q, %v); want (%q, true)", k, got, ok, want)
		}
	}
}

func TestResolveUserConfig_RequiredMissing(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "token", Type: UserConfigString, Title: "T", Description: "D", Required: true, Sensitive: true},
		{Key: "opt", Type: UserConfigString, Title: "T", Description: "D"},
	}
	plain := NewMemPluginConfigStore()
	secure := &memSecureStore{m: map[string]string{}}

	_, missing, err := ResolveUserConfig("demo", opts, plain, secure)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "token" {
		t.Errorf("missing = %v, want [token]", missing)
	}
}

func TestResolveUserConfig_SensitiveInPlainIgnored(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "token", Type: UserConfigString, Title: "T", Description: "D", Sensitive: true},
	}
	plain := NewMemPluginConfigStore()
	_ = plain.Save("demo", map[string]any{"token": "leak"})
	secure := &memSecureStore{m: map[string]string{"demo/token": "secret"}}
	r, _, err := ResolveUserConfig("demo", opts, plain, secure)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Lookup("token")
	if got != "secret" {
		t.Errorf("got %q, want secret (plain-store leak)", got)
	}
}

// memSecureStore is an in-memory SecureStore for tests.
type memSecureStore struct {
	m map[string]string
}

func (s *memSecureStore) Get(p, k string) (string, bool, error) {
	v, ok := s.m[p+"/"+k]
	return v, ok, nil
}
func (s *memSecureStore) Set(p, k, v string) error    { s.m[p+"/"+k] = v; return nil }
func (s *memSecureStore) Delete(p, k string) error    { delete(s.m, p+"/"+k); return nil }

var _ = filepath.Separator // keep import alive in other tests
```

Add `"path/filepath"` to the imports block of `agent/plugin_userconfig_test.go` (or remove the placeholder `var _ = filepath.Separator` line if not needed).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestResolveUserConfig -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
// ResolveUserConfig assembles a ResolvedUserConfig from persisted state
// without prompting. Used at session start. Sensitive options read from
// secureStore; non-sensitive from plainStore. Required-but-missing keys
// appear in `missing`. Defaults are applied when storage has no entry.
func ResolveUserConfig(
	pluginID string,
	opts []UserConfigOption,
	plainStore PluginConfigStore,
	secureStore SecureStore,
) (*ResolvedUserConfig, []string, error) {
	plain, err := plainStore.Load(pluginID)
	if err != nil {
		return nil, nil, err
	}
	values := make(map[string]string, len(opts))
	byKey := make(map[string]UserConfigOption, len(opts))
	var missing []string
	for _, opt := range opts {
		byKey[opt.Key] = opt
		var raw any
		var present bool
		if opt.Sensitive {
			v, ok, err := secureStore.Get(pluginID, opt.Key)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				raw, present = v, true
			}
		} else if v, ok := plain[opt.Key]; ok {
			raw, present = v, true
		}
		if !present && opt.Default != nil {
			raw, present = opt.Default, true
		}
		if !present {
			if opt.Required {
				missing = append(missing, opt.Key)
			}
			values[opt.Key] = ""
			continue
		}
		s := stringifyUserConfigValue(opt, raw)
		if opt.Type == UserConfigDirectory || opt.Type == UserConfigFile {
			s = expandTilde(s)
		}
		values[opt.Key] = s
	}
	return &ResolvedUserConfig{PluginID: pluginID, values: values, options: byKey}, missing, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestResolveUserConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): ResolveUserConfig with defaults and secure-split"
```

---

## Phase 7 — `ExpandUserConfig` and `UserConfigEnvVars`

### Task 19: `ExpandUserConfig` basic substitution

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestExpandUserConfig_SingleAndMultiple(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "a", Type: UserConfigString, Title: "T", Description: "D"},
		{Key: "b", Type: UserConfigString, Title: "T", Description: "D"},
	}
	r := newResolvedForTest("demo", opts, map[string]string{"a": "alpha", "b": "beta"})

	got := ExpandUserConfig("--a=${user_config.a}", r)
	if got != "--a=alpha" {
		t.Errorf("single: %q", got)
	}
	got = ExpandUserConfig("${user_config.a} and ${user_config.b}", r)
	if got != "alpha and beta" {
		t.Errorf("multiple: %q", got)
	}
	got = ExpandUserConfig("${user_config.a}${user_config.b}", r)
	if got != "alphabeta" {
		t.Errorf("adjacent: %q", got)
	}
}

func TestExpandUserConfig_LiteralPreservedForUppercase(t *testing.T) {
	r := newResolvedForTest("demo", nil, nil)
	got := ExpandUserConfig("${user_config.A}", r)
	if got != "${user_config.A}" {
		t.Errorf("uppercase key should be left literal, got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExpandUserConfig -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
// userConfigTokenRe matches ${user_config.<lower_snake_case>}. Identifier
// shape matches userConfigKeyRe (§3, §6.2).
var userConfigTokenRe = regexp.MustCompile(`\$\{user_config\.([a-z][a-z0-9_]*)\}`)

// ExpandUserConfig replaces every ${user_config.KEY} occurrence in s with
// the value from r. Unknown keys (never declared) leave the literal token
// in place and emit one stderr warning per (pluginID, key) over the
// process lifetime (Task 20).
func ExpandUserConfig(s string, r *ResolvedUserConfig) string {
	if r == nil || s == "" {
		return s
	}
	return userConfigTokenRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := userConfigTokenRe.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		key := sub[1]
		v, ok := r.Lookup(key)
		if !ok {
			warnUnknownUserConfigKey(r.PluginID, key)
			return match
		}
		return v
	})
}
```

- [ ] **Step 4: Add `warnUnknownUserConfigKey` stub**

Also append (it will be expanded in Task 20):

```go
// warnUnknownUserConfigKey is a stub — Task 20 wires the once-per-pair
// stderr warning. Defined here so Task 19's compile succeeds.
func warnUnknownUserConfigKey(pluginID, key string) {}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./agent/ -run TestExpandUserConfig -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): ExpandUserConfig substitution"
```

---

### Task 20: `warnUnknownUserConfigKey` once-per-(plugin,key)

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
import "bytes"

func TestExpandUserConfig_UnknownKeyWarnsOnce(t *testing.T) {
	r := newResolvedForTest("demo", nil, nil)

	var buf bytes.Buffer
	old := userConfigUnknownWriter
	userConfigUnknownWriter = &buf
	resetUserConfigUnknownWarnings()
	t.Cleanup(func() { userConfigUnknownWriter = old })

	_ = ExpandUserConfig("${user_config.absent}", r)
	_ = ExpandUserConfig("${user_config.absent} ${user_config.absent}", r)
	count := 0
	for _, c := range buf.String() {
		if c == '\n' {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 warning line, got %d (output: %q)", count, buf.String())
	}

	// New key from same plugin → second warning.
	_ = ExpandUserConfig("${user_config.other_absent}", r)
	count = 0
	for _, c := range buf.String() {
		if c == '\n' {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 warnings after new key, got %d", count)
	}
}
```

Update the imports block at top of `agent/plugin_userconfig_test.go` to include `"bytes"` (other tests use it too).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExpandUserConfig_UnknownKeyWarnsOnce -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement**

Replace the stub `warnUnknownUserConfigKey` in `agent/plugin_userconfig.go` with:

```go
import (
	"io"
	"sync"
)

var (
	userConfigUnknownWriter io.Writer = osStderr()
	userConfigUnknownSeen   sync.Map  // key: "<pluginID>\x00<key>"
)

func osStderr() io.Writer { return os.Stderr }

// warnUnknownUserConfigKey writes one stderr line per (pluginID, key) over
// the process lifetime.
func warnUnknownUserConfigKey(pluginID, key string) {
	mapKey := pluginID + "\x00" + key
	if _, loaded := userConfigUnknownSeen.LoadOrStore(mapKey, struct{}{}); loaded {
		return
	}
	fmt.Fprintf(userConfigUnknownWriter, "serf: plugin %q: unknown user_config key %q\n", pluginID, key)
}

// resetUserConfigUnknownWarnings is a test helper.
func resetUserConfigUnknownWarnings() {
	userConfigUnknownSeen = sync.Map{}
}
```

NOTE: add `"io"` and `"sync"` to the existing import block. `"fmt"` and `"os"` already imported.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestExpandUserConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): warn-once on unknown user_config key"
```

---

### Task 21: `UserConfigEnvVars`

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestUserConfigEnvVars(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "api_endpoint", Type: UserConfigString, Title: "T", Description: "D"},
		{Key: "api_token", Type: UserConfigString, Title: "T", Description: "D", Sensitive: true},
		{Key: "verbose", Type: UserConfigBoolean, Title: "T", Description: "D"},
	}
	r := newResolvedForTest("demo", opts, map[string]string{
		"api_endpoint": "https://x",
		"api_token":    "sekret",
		"verbose":      "true",
	})
	env := UserConfigEnvVars(r)
	want := map[string]string{
		"CLAUDE_PLUGIN_OPTION_API_ENDPOINT": "https://x",
		"CLAUDE_PLUGIN_OPTION_API_TOKEN":    "sekret",
		"CLAUDE_PLUGIN_OPTION_VERBOSE":      "true",
	}
	if len(env) != len(want) {
		t.Errorf("got %d entries, want %d (%v)", len(env), len(want), env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, env[k], v)
		}
	}
}

func TestUserConfigEnvVars_NilReturnsEmpty(t *testing.T) {
	if got := UserConfigEnvVars(nil); len(got) != 0 {
		t.Errorf("nil: got %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestUserConfigEnvVars -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
import "strings"

// UserConfigEnvVars produces the CLAUDE_PLUGIN_OPTION_<KEY>=<value> pairs
// the caller should merge into a subprocess env. Keys are uppercased and
// non-[A-Z0-9_] characters become '_' (SP7 §7.1).
func UserConfigEnvVars(r *ResolvedUserConfig) map[string]string {
	if r == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(r.values))
	for key, val := range r.values {
		out["CLAUDE_PLUGIN_OPTION_"+sanitizeEnvKey(key)] = val
	}
	return out
}

// sanitizeEnvKey uppercases key and replaces non-[A-Z0-9_] with '_'.
func sanitizeEnvKey(key string) string {
	up := strings.ToUpper(key)
	out := make([]byte, 0, len(up))
	for i := 0; i < len(up); i++ {
		c := up[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
```

NOTE: add `"strings"` to the existing import block.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestUserConfigEnvVars -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): UserConfigEnvVars helper"
```

---

## Phase 8 — Prompt flow

### Task 22: `UserConfigPrompter` interface + `MapPrompter` + `ErrPromptCanceled`

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
import "errors"

func TestMapPrompter_ReturnsCannedValues(t *testing.T) {
	mp := MapPrompter{Values: map[string]string{"a": "alpha", "b": "beta"}}
	got, err := mp.Prompt(UserConfigOption{Key: "a"})
	if err != nil || got != "alpha" {
		t.Errorf("a: (%q, %v)", got, err)
	}
	got, err = mp.Prompt(UserConfigOption{Key: "missing"})
	if err != nil || got != "" {
		t.Errorf("missing: (%q, %v)", got, err)
	}
}

func TestErrPromptCanceled_Identity(t *testing.T) {
	err := fmt.Errorf("user pressed escape: %w", ErrPromptCanceled)
	if !errors.Is(err, ErrPromptCanceled) {
		t.Error("errors.Is should match wrapped ErrPromptCanceled")
	}
}
```

Add `"errors"` to imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestMapPrompter -v`
Run: `go test ./agent/ -run TestErrPromptCanceled -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
// UserConfigPrompter is the surface-specific UX. CLI, serf-tui, and
// serf-hub each ship their own implementation.
type UserConfigPrompter interface {
	// Prompt is called once per option in declaration order. The raw
	// user-entered string is returned. Empty string means "user accepted
	// the default" if a default exists; otherwise treated as no value.
	Prompt(opt UserConfigOption) (string, error)
}

// ErrPromptCanceled signals user-initiated abort (Ctrl-C, modal close).
var ErrPromptCanceled = errors.New("user_config: prompt canceled")

// MapPrompter is a non-interactive UserConfigPrompter backed by a static
// map. Used by tests and by the --plugin-option flag flow.
type MapPrompter struct {
	Values map[string]string // key → raw user input
}

// Prompt returns Values[opt.Key] (empty string when absent).
func (m MapPrompter) Prompt(opt UserConfigOption) (string, error) {
	if m.Values == nil {
		return "", nil
	}
	return m.Values[opt.Key], nil
}
```

NOTE: add `"errors"` to the import block. Replace any earlier stub if introduced.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestMapPrompter -v`
Run: `go test ./agent/ -run TestErrPromptCanceled -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): UserConfigPrompter + MapPrompter + ErrPromptCanceled"
```

---

### Task 23: `PromptForUserConfig` happy path

**Files:**
- Modify: `agent/plugin_userconfig.go`
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestPromptForUserConfig_HappyPath(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "endpoint", Type: UserConfigString, Title: "T", Description: "D"},
		{Key: "token", Type: UserConfigString, Title: "T", Description: "D", Sensitive: true},
		{Key: "verbose", Type: UserConfigBoolean, Title: "T", Description: "D", Default: false},
	}
	mp := MapPrompter{Values: map[string]string{
		"endpoint": "https://x",
		"token":    "ghp_z",
		"verbose":  "true",
	}}
	plain := NewMemPluginConfigStore()
	secure := &memSecureStore{m: map[string]string{}}

	r, err := PromptForUserConfig(mp, "demo", opts, plain, secure)
	if err != nil {
		t.Fatal(err)
	}
	// plain store has non-sensitive only
	got, _ := plain.Load("demo")
	if _, ok := got["token"]; ok {
		t.Error("sensitive value leaked into plain store")
	}
	if got["endpoint"] != "https://x" || got["verbose"] != true {
		t.Errorf("plain store: %+v", got)
	}
	// secure store has sensitive only
	if v, ok, _ := secure.Get("demo", "token"); !ok || v != "ghp_z" {
		t.Errorf("secure: (%q, %v)", v, ok)
	}
	// returned resolver has all three stringified
	if v, _ := r.Lookup("endpoint"); v != "https://x" {
		t.Errorf("Lookup endpoint = %q", v)
	}
	if v, _ := r.Lookup("verbose"); v != "true" {
		t.Errorf("Lookup verbose = %q", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestPromptForUserConfig_HappyPath -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin_userconfig.go`:

```go
// PromptForUserConfig collects values for opts via prompter and persists
// them. Plain values go to plainStore; sensitive values go to secureStore.
// Returns the resolved values for immediate use. On any error after partial
// writes, secureStore writes already made are *not* automatically rolled
// back — callers calling this from `serf plugin enable` should treat any
// error as "delete the plugin" or "re-run enable". Plain-store writes use
// Save's full-replace semantics so a failed call leaves the previous
// pluginConfigs entry untouched until the final Save.
func PromptForUserConfig(
	prompter UserConfigPrompter,
	pluginID string,
	opts []UserConfigOption,
	plainStore PluginConfigStore,
	secureStore SecureStore,
) (*ResolvedUserConfig, error) {
	plainOut := make(map[string]any, len(opts))
	resolvedValues := make(map[string]string, len(opts))
	byKey := make(map[string]UserConfigOption, len(opts))

	for _, opt := range opts {
		byKey[opt.Key] = opt
		raw, err := prompter.Prompt(opt)
		if err != nil {
			return nil, fmt.Errorf("prompting %s.%s: %w", pluginID, opt.Key, err)
		}
		val, err := coerceUserConfigInput(opt, raw)
		if err != nil {
			return nil, err
		}
		if val == nil {
			if opt.Required {
				return nil, fmt.Errorf("user_config.%s.%s: required value missing", pluginID, opt.Key)
			}
			resolvedValues[opt.Key] = ""
			continue
		}
		// Store sensitive in secure, plain elsewhere.
		if opt.Sensitive {
			s, _ := val.(string)
			if err := secureStore.Set(pluginID, opt.Key, s); err != nil {
				return nil, fmt.Errorf("secure store: %w", err)
			}
		} else {
			plainOut[opt.Key] = val
		}
		// Compute stringified form for resolver.
		resolvedValues[opt.Key] = stringifyUserConfigValue(opt, val)
		if opt.Type == UserConfigDirectory || opt.Type == UserConfigFile {
			resolvedValues[opt.Key] = expandTilde(resolvedValues[opt.Key])
		}
	}

	if err := plainStore.Save(pluginID, plainOut); err != nil {
		return nil, fmt.Errorf("plain store: %w", err)
	}
	return &ResolvedUserConfig{PluginID: pluginID, values: resolvedValues, options: byKey}, nil
}

// coerceUserConfigInput parses raw user text into the option's typed value.
// Empty raw + default → default; empty raw + no default → nil (caller decides
// required behavior). Bounds checks (min/max) apply to numbers.
func coerceUserConfigInput(opt UserConfigOption, raw string) (any, error) {
	if raw == "" {
		if opt.Default != nil {
			return opt.Default, nil
		}
		return nil, nil
	}
	switch opt.Type {
	case UserConfigString, UserConfigDirectory, UserConfigFile:
		if opt.Multiple {
			parts := strings.Fields(raw)
			arr := make([]any, len(parts))
			for i, p := range parts {
				arr[i] = p
			}
			return arr, nil
		}
		return raw, nil
	case UserConfigNumber:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("user_config.%s: not a number: %w", opt.Key, err)
		}
		if opt.Min != nil && f < *opt.Min {
			return nil, fmt.Errorf("user_config.%s: value %v below min %v", opt.Key, f, *opt.Min)
		}
		if opt.Max != nil && f > *opt.Max {
			return nil, fmt.Errorf("user_config.%s: value %v above max %v", opt.Key, f, *opt.Max)
		}
		return f, nil
	case UserConfigBoolean:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "yes", "y", "1", "on":
			return true, nil
		case "false", "no", "n", "0", "off":
			return false, nil
		default:
			return nil, fmt.Errorf("user_config.%s: not a boolean: %q", opt.Key, raw)
		}
	}
	return nil, fmt.Errorf("user_config.%s: unknown type %q", opt.Key, opt.Type)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestPromptForUserConfig_HappyPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_userconfig.go agent/plugin_userconfig_test.go
git commit -m "feat(plugin): PromptForUserConfig happy path"
```

---

### Task 24: `PromptForUserConfig` error paths (required, bounds, cancel)

**Files:**
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/plugin_userconfig_test.go`:

```go
type explodingPrompter struct{}

func (explodingPrompter) Prompt(UserConfigOption) (string, error) {
	return "", ErrPromptCanceled
}

func TestPromptForUserConfig_Cancel(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "x", Type: UserConfigString, Title: "T", Description: "D"},
	}
	plain := NewMemPluginConfigStore()
	secure := &memSecureStore{m: map[string]string{}}
	_, err := PromptForUserConfig(explodingPrompter{}, "demo", opts, plain, secure)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrPromptCanceled) {
		t.Errorf("error not Is(ErrPromptCanceled): %v", err)
	}
	// nothing persisted
	got, _ := plain.Load("demo")
	if len(got) != 0 {
		t.Errorf("plain store should be empty, got %v", got)
	}
}

func TestPromptForUserConfig_RequiredEmpty(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "token", Type: UserConfigString, Title: "T", Description: "D", Required: true},
	}
	mp := MapPrompter{Values: map[string]string{"token": ""}}
	_, err := PromptForUserConfig(mp, "demo", opts, NewMemPluginConfigStore(), &memSecureStore{m: map[string]string{}})
	if err == nil {
		t.Fatal("expected required error")
	}
	if !contains(err.Error(), "required value missing") {
		t.Errorf("err = %v", err)
	}
}

func TestPromptForUserConfig_BoundsViolation(t *testing.T) {
	max := float64(60000)
	opts := []UserConfigOption{
		{Key: "timeout", Type: UserConfigNumber, Title: "T", Description: "D", Max: &max},
	}
	mp := MapPrompter{Values: map[string]string{"timeout": "99999"}}
	_, err := PromptForUserConfig(mp, "demo", opts, NewMemPluginConfigStore(), &memSecureStore{m: map[string]string{}})
	if err == nil {
		t.Fatal("expected max-violation error")
	}
	if !contains(err.Error(), "above max") {
		t.Errorf("err = %v", err)
	}
}

func TestPromptForUserConfig_DefaultAcceptedOnEmpty(t *testing.T) {
	opts := []UserConfigOption{
		{Key: "endpoint", Type: UserConfigString, Title: "T", Description: "D", Default: "https://default"},
	}
	mp := MapPrompter{Values: map[string]string{"endpoint": ""}}
	plain := NewMemPluginConfigStore()
	_, err := PromptForUserConfig(mp, "demo", opts, plain, &memSecureStore{m: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := plain.Load("demo")
	if got["endpoint"] != "https://default" {
		t.Errorf("default not used: %v", got)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./agent/ -run TestPromptForUserConfig -v`
Expected: PASS — implementation from Task 23 already handles these paths.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_userconfig_test.go
git commit -m "test(plugin): PromptForUserConfig error and default paths"
```

---

### Task 25: Re-prompt after schema change (new required key)

**Files:**
- Modify: `agent/plugin_userconfig_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_userconfig_test.go`:

```go
func TestResolveUserConfig_NewlyRequiredKeyReportedMissing(t *testing.T) {
	plain := NewMemPluginConfigStore()
	secure := &memSecureStore{m: map[string]string{}}
	// Persisted state from v1 of plugin: only endpoint.
	_ = plain.Save("demo", map[string]any{"endpoint": "https://x"})

	// v2 of plugin adds a new required key `new_key`.
	v2 := []UserConfigOption{
		{Key: "endpoint", Type: UserConfigString, Title: "T", Description: "D"},
		{Key: "new_key", Type: UserConfigString, Title: "T", Description: "D", Required: true},
	}
	_, missing, err := ResolveUserConfig("demo", v2, plain, secure)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "new_key" {
		t.Errorf("missing = %v, want [new_key]", missing)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/ -run TestResolveUserConfig_NewlyRequiredKey -v`
Expected: PASS (already supported by Task 18).

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_userconfig_test.go
git commit -m "test(plugin): newly-required key surfaces in ResolveUserConfig.missing"
```

---

## Phase 9 — `bin/` PATH

### Task 26: `discoverPluginBinDir` helper

**Files:**
- Create: `agent/plugin_bin.go`
- Create: `agent/plugin_bin_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_bin_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPluginBinDir_Present(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, warn := discoverPluginBinDir(dir)
	if got != binDir {
		t.Errorf("got %q, want %q", got, binDir)
	}
	if warn != nil {
		t.Errorf("unexpected warning: %v", warn)
	}
}

func TestDiscoverPluginBinDir_Absent(t *testing.T) {
	dir := t.TempDir()
	got, warn := discoverPluginBinDir(dir)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if warn != nil {
		t.Errorf("absent should not warn: %v", warn)
	}
}

func TestDiscoverPluginBinDir_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	if err := os.WriteFile(path, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, warn := discoverPluginBinDir(dir)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if warn == nil {
		t.Error("expected warning for non-directory bin")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestDiscoverPluginBinDir -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Create `agent/plugin_bin.go`:

```go
package agent

import (
	"os"
	"path/filepath"
)

// discoverPluginBinDir returns the absolute path of <pluginDir>/bin if it
// exists and is a directory; "" otherwise. If the path exists but is not
// a directory, returns ("", warning).
func discoverPluginBinDir(pluginDir string) (string, *PluginWarning) {
	p := filepath.Join(pluginDir, "bin")
	info, err := os.Stat(p)
	if err != nil {
		return "", nil // absent → benign
	}
	if !info.IsDir() {
		return "", &PluginWarning{Field: "bin", Message: "expected directory at \"bin\", found other file type"}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", nil
	}
	return abs, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestDiscoverPluginBinDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_bin.go agent/plugin_bin_test.go
git commit -m "feat(plugin): discoverPluginBinDir helper"
```

---

### Task 27: `PluginBinPATH` joins absolute dirs in load order

**Files:**
- Modify: `agent/plugin_bin.go`
- Modify: `agent/plugin_bin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_bin_test.go`:

```go
func TestPluginBinPATH_None(t *testing.T) {
	if got := PluginBinPATH(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
	if got := PluginBinPATH([]LoadedPlugin{}); got != "" {
		t.Errorf("empty: got %q", got)
	}
	if got := PluginBinPATH([]LoadedPlugin{{BinDir: ""}}); got != "" {
		t.Errorf("empty BinDir: got %q", got)
	}
}

func TestPluginBinPATH_OrderPreserved(t *testing.T) {
	plugins := []LoadedPlugin{
		{BinDir: "/plugins/a/bin"},
		{BinDir: ""},
		{BinDir: "/plugins/b/bin"},
	}
	got := PluginBinPATH(plugins)
	want := "/plugins/a/bin" + string(os.PathListSeparator) + "/plugins/b/bin"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestPluginBinPATH -v`
Expected: FAIL — undefined (and `LoadedPlugin.BinDir` not yet added; Task 28 adds it).

- [ ] **Step 3: Add `BinDir` to `LoadedPlugin` and implement `PluginBinPATH`**

Edit `agent/plugin.go`, replace the `LoadedPlugin` struct block (around lines 64–72) with:

```go
// LoadedPlugin represents a plugin that has been loaded from disk.
type LoadedPlugin struct {
	Manifest   PluginManifest
	Dir        string                         // absolute path = CLAUDE_PLUGIN_ROOT
	Skills     map[string]SkillMeta           // namespaced as "plugin-name:skill-name"
	Agents     map[string]PluginAgent         // namespaced as "plugin-name:agent-name"
	Hooks      map[HookEvent][]RegisteredHook // keyed by event type
	MCPConfigs []MCPServerConfig              // namespaced as "plugin_<name>_<server>"

	// SP7 additions:
	UserConfigOptions []UserConfigOption // empty if manifest omits userConfig
	DefaultAgent      string             // from plugin-root settings.json; "" if unset
	BinDir            string             // absolute path to <root>/bin if it exists as a directory; "" otherwise
	Warnings          []PluginWarning    // unsupported fields/keys (deduplicated)
}
```

Append to `agent/plugin_bin.go`:

```go
// PluginBinPATH returns a PATH-fragment with each plugin's non-empty BinDir
// joined by os.PathListSeparator in load order. Callers prepend this to the
// inherited PATH only for shell-tool ExecCommand invocations.
func PluginBinPATH(plugins []LoadedPlugin) string {
	out := ""
	for _, p := range plugins {
		if p.BinDir == "" {
			continue
		}
		if out == "" {
			out = p.BinDir
		} else {
			out += string(os.PathListSeparator) + p.BinDir
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestPluginBinPATH -v`
Expected: PASS.

Also verify the package still compiles:
Run: `go build ./agent/...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_bin.go agent/plugin_bin_test.go
git commit -m "feat(plugin): PluginBinPATH + LoadedPlugin.BinDir"
```

---

### Task 28: End-to-end Bash exec with plugin `bin/` on PATH (Unix only)

**Files:**
- Modify: `agent/plugin_bin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_bin_test.go`:

```go
import (
	"os/exec"
	"runtime"
)

func TestPluginBinPATH_EndToEndExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-style PATH only")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "plugin-with-bin", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(binDir, "my-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	plugins := []LoadedPlugin{{BinDir: binDir}}
	path := PluginBinPATH(plugins) + string(os.PathListSeparator) + os.Getenv("PATH")

	cmd := exec.Command("sh", "-c", "my-tool")
	cmd.Env = append(os.Environ(), "PATH="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exec: %v (out=%q)", err, string(out))
	}
	if string(out) != "ok\n" {
		t.Errorf("output = %q, want %q", string(out), "ok\n")
	}
}
```

Add the imports if not present.

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/ -run TestPluginBinPATH_EndToEndExec -v`
Expected: PASS — the binary exists, PATH points at it, sh resolves it.

(This test does not exercise serf's full shell-tool registration; SP8 wires that. The PATH-string helper is what SP7 owns and this test verifies it works end-to-end with a real exec.)

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_bin_test.go
git commit -m "test(plugin): end-to-end exec via PluginBinPATH"
```

---

## Phase 10 — Plugin-root `settings.json`

### Task 29: `loadPluginSettings` parses `agent` key

**Files:**
- Create: `agent/plugin_settings.go`
- Create: `agent/plugin_settings_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_settings_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPluginSettings_Absent(t *testing.T) {
	dir := t.TempDir()
	got, warns, err := loadPluginSettings(dir, "demo", map[string]PluginAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("DefaultAgent = %q, want empty", got)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v", warns)
	}
}

func TestLoadPluginSettings_AgentResolves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"agent":"reviewer"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := map[string]PluginAgent{"demo:reviewer": {}}
	got, warns, err := loadPluginSettings(dir, "demo", agents)
	if err != nil {
		t.Fatal(err)
	}
	if got != "demo:reviewer" {
		t.Errorf("DefaultAgent = %q", got)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v", warns)
	}
}

func TestLoadPluginSettings_AgentMissing(t *testing.T) {
	resetWarningsForTest()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"agent":"nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, warns, err := loadPluginSettings(dir, "demo", map[string]PluginAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("DefaultAgent should be empty when agent missing, got %q", got)
	}
	if len(warns) != 1 {
		t.Fatalf("want 1 warning, got %d", len(warns))
	}
	if warns[0].Field != "settings.json:agent" {
		t.Errorf("warning field = %q", warns[0].Field)
	}
}

func TestLoadPluginSettings_UnknownKey(t *testing.T) {
	resetWarningsForTest()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"subagentStatusLine":{"x":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warns, err := loadPluginSettings(dir, "demo", map[string]PluginAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 {
		t.Fatalf("want 1 warning, got %d", len(warns))
	}
	if warns[0].Field != "settings.json:subagentStatusLine" {
		t.Errorf("warning field = %q", warns[0].Field)
	}
}

func TestLoadPluginSettings_Malformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadPluginSettings(dir, "demo", map[string]PluginAgent{})
	if err == nil {
		t.Error("expected parse error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadPluginSettings -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Create `agent/plugin_settings.go`:

```go
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// loadPluginSettings reads <pluginDir>/settings.json and returns the
// resolved DefaultAgent name plus any warnings for unsupported keys.
//
//   - "agent" → namespaced as "<pluginName>:<value>". Missing agent →
//     empty + warning.
//   - any other key → empty + one warning per key (settings.json:<key>).
//
// Missing file is benign; malformed JSON is a hard error per SP7 §9.3.
func loadPluginSettings(pluginDir, pluginName string, agents map[string]PluginAgent) (string, []PluginWarning, error) {
	path := filepath.Join(pluginDir, "settings.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	var warnings []PluginWarning
	var defaultAgent string

	for key, val := range raw {
		switch key {
		case "agent":
			var name string
			if err := json.Unmarshal(val, &name); err != nil {
				return "", nil, fmt.Errorf("parsing %s: settings.json:agent: %w", path, err)
			}
			namespaced := pluginName + ":" + name
			if _, ok := agents[namespaced]; !ok {
				if w := recordPluginWarning(pluginName, "settings.json:agent", fmt.Sprintf("agent %q not found in plugin", name)); w != nil {
					warnings = append(warnings, *w)
				}
				continue
			}
			defaultAgent = namespaced
		default:
			field := "settings.json:" + key
			if w := recordPluginWarning(pluginName, field, "ignoring unsupported settings.json key \""+key+"\""); w != nil {
				warnings = append(warnings, *w)
			}
		}
	}
	return defaultAgent, warnings, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestLoadPluginSettings -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_settings.go agent/plugin_settings_test.go
git commit -m "feat(plugin): loadPluginSettings parses agent key"
```

---

## Phase 11 — `skills` custom paths

### Task 30: `discoverPluginSkills` accepts an override

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestDiscoverPluginSkills_AdditiveOverride(t *testing.T) {
	dir := t.TempDir()
	// default location
	if err := os.MkdirAll(filepath.Join(dir, "skills", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "foo", "SKILL.md"),
		[]byte("---\nname: foo\ndescription: Foo skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// extra location
	if err := os.MkdirAll(filepath.Join(dir, "extra", "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra", "bar", "SKILL.md"),
		[]byte("---\nname: bar\ndescription: Bar skill\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := discoverPluginSkills(dir, "demo", "./extra")
	if _, ok := got["demo:foo"]; !ok {
		t.Errorf("default skill missing: %v", got)
	}
	if _, ok := got["demo:bar"]; !ok {
		t.Errorf("override skill missing: %v", got)
	}
}
```

If the SKILL.md frontmatter shape differs from the existing skills parser, refer to `agent/skills.go` for the actual format. Adjust the SKILL.md content to whatever `scanSkillsDir` expects (consult `agent/skills_test.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestDiscoverPluginSkills_AdditiveOverride -v`
Expected: FAIL — `discoverPluginSkills` signature is `(dir, name)` not `(dir, name, override)`.

- [ ] **Step 3: Change signature**

In `agent/plugin.go`, replace the existing `discoverPluginSkills` function with:

```go
// discoverPluginSkills scans a plugin's skills directories and returns
// skills namespaced as "pluginName:skillName". override is the manifest
// "skills" field (string, []any, or nil). Override paths supplement the
// default "skills/" directory (SP7 §10.2).
func discoverPluginSkills(pluginDir, pluginName string, override any) map[string]SkillMeta {
	dirs := resolveComponentDirs(pluginDir, "skills", override)
	raw := map[string]SkillMeta{}
	for _, dir := range dirs {
		scanSkillsDir(dir, raw)
	}
	namespaced := make(map[string]SkillMeta, len(raw))
	for name, meta := range raw {
		namespaced[pluginName+":"+name] = meta
	}
	return namespaced
}
```

Update the existing call site in `LoadPlugin` (`agent/plugin.go` line ~192):

```go
lp.Skills = discoverPluginSkills(resolved, manifest.Name, nil)
```

(Task 33 below replaces `nil` with the parsed manifest override.)

- [ ] **Step 4: Run tests**

Run: `go test ./agent/ -run TestDiscoverPluginSkills -v`
Run: `go build ./agent/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): discoverPluginSkills accepts override paths"
```

---

### Task 31: Path validation — reject absolute and traversal

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestValidateSkillsOverride(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "nil", raw: ``, want: nil},
		{name: "single string", raw: `"./extra"`, want: []string{"./extra"}},
		{name: "array", raw: `["./a","./b"]`, want: []string{"./a", "./b"}},
		{name: "absolute path", raw: `"/abs/path"`, wantErr: "paths must be relative"},
		{name: "missing dot-slash", raw: `"extra"`, wantErr: `must start with "./"`},
		{name: "traversal", raw: `"./../escape"`, wantErr: "must not traverse outside"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v any
			if c.raw != "" {
				if err := json.Unmarshal([]byte(c.raw), &v); err != nil {
					t.Fatal(err)
				}
			}
			got, err := validateSkillsOverride(v, "/plugin/root")
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", c.wantErr)
				}
				if !contains(err.Error(), c.wantErr) {
					t.Errorf("err %q does not contain %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}
```

NOTE: `contains` is defined in `agent/plugin_userconfig_test.go`; reuse it. Add `"encoding/json"` to `agent/plugin_test.go` imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestValidateSkillsOverride -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Append to `agent/plugin.go`:

```go
import "strings"

// validateSkillsOverride checks a manifest "skills" value against §10.1
// rules: relative ./-prefixed paths only, no traversal outside pluginRoot.
// Returns the cleaned list of override strings (still relative).
func validateSkillsOverride(v any, pluginRoot string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	var items []string
	switch t := v.(type) {
	case string:
		items = []string{t}
	case []any:
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("skills: array elements must be strings")
			}
			items = append(items, s)
		}
	default:
		return nil, fmt.Errorf("skills: must be string or array of strings")
	}
	for _, p := range items {
		if filepath.IsAbs(p) {
			return nil, fmt.Errorf("skills: paths must be relative and start with \"./\"")
		}
		if !strings.HasPrefix(p, "./") {
			return nil, fmt.Errorf("skills: paths must start with \"./\"")
		}
		joined := filepath.Clean(filepath.Join(pluginRoot, p))
		if !strings.HasPrefix(joined+string(filepath.Separator), filepath.Clean(pluginRoot)+string(filepath.Separator)) && joined != filepath.Clean(pluginRoot) {
			return nil, fmt.Errorf("skills: paths must not traverse outside the plugin root")
		}
	}
	return items, nil
}
```

NOTE: `"strings"` may already be imported in `agent/plugin.go`. Inspect imports first; do not duplicate.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestValidateSkillsOverride -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): validate skills override paths"
```

---

## Phase 12 — `LoadPlugin` integration

### Task 32: `LoadPlugin` parses `userConfig` into `LoadedPlugin.UserConfigOptions`

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestLoadPlugin_PopulatesUserConfigOptions(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "demo",
		"userConfig": {
			"token": {"type":"string","title":"T","description":"D","sensitive":true,"required":true}
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lp.UserConfigOptions) != 1 {
		t.Fatalf("got %d options", len(lp.UserConfigOptions))
	}
	o := lp.UserConfigOptions[0]
	if o.Key != "token" || !o.Sensitive || !o.Required {
		t.Errorf("opt = %+v", o)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadPlugin_PopulatesUserConfigOptions -v`
Expected: FAIL — `LoadedPlugin.UserConfigOptions` populated nowhere.

- [ ] **Step 3: Wire `ParseUserConfig` in `LoadPlugin`**

In `agent/plugin.go`, just after `lp.MCPConfigs = mcpConfigs` (around line 210), add:

```go
	if opts, err := ParseUserConfig(manifest.UserConfig); err != nil {
		return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	} else {
		lp.UserConfigOptions = opts
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestLoadPlugin_PopulatesUserConfigOptions -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): LoadPlugin populates UserConfigOptions"
```

---

### Task 33: `LoadPlugin` wires skills override

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestLoadPlugin_HonorsSkillsOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"demo","skills":["./extra"]}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "extra", "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra", "bar", "SKILL.md"),
		[]byte("---\nname: bar\ndescription: Bar\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lp.Skills["demo:bar"]; !ok {
		t.Errorf("override skill not loaded: keys=%v", lp.Skills)
	}
}

func TestLoadPlugin_RejectsBadSkillsPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"demo","skills":"/abs/path"}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPlugin(dir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !contains(err.Error(), "paths must be relative") {
		t.Errorf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadPlugin_HonorsSkillsOverride -v`
Run: `go test ./agent/ -run TestLoadPlugin_RejectsBadSkillsPath -v`
Expected: FAIL — override is currently passed as `nil`.

- [ ] **Step 3: Wire override through `LoadPlugin`**

In `agent/plugin.go`, replace the line `lp.Skills = discoverPluginSkills(resolved, manifest.Name, nil)` with:

```go
	var skillsOverride any
	if len(manifest.Skills) > 0 {
		if err := json.Unmarshal(manifest.Skills, &skillsOverride); err != nil {
			return LoadedPlugin{}, fmt.Errorf("in plugin at %q: parsing skills: %w", resolved, err)
		}
		if _, err := validateSkillsOverride(skillsOverride, resolved); err != nil {
			return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
		}
	}
	lp.Skills = discoverPluginSkills(resolved, manifest.Name, skillsOverride)
```

- [ ] **Step 4: Run tests**

Run: `go test ./agent/ -run TestLoadPlugin_HonorsSkillsOverride -v`
Run: `go test ./agent/ -run TestLoadPlugin_RejectsBadSkillsPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): LoadPlugin honors skills override with validation"
```

---

### Task 34: `LoadPlugin` populates `BinDir`

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestLoadPlugin_PopulatesBinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lp.BinDir == "" {
		t.Error("BinDir should be set when bin/ exists")
	}
}

func TestLoadPlugin_NoBinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lp.BinDir != "" {
		t.Errorf("BinDir = %q, want empty", lp.BinDir)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadPlugin_PopulatesBinDir -v`
Expected: FAIL — not yet wired.

- [ ] **Step 3: Wire `discoverPluginBinDir` in `LoadPlugin`**

In `agent/plugin.go`, after the skills-override block, add:

```go
	if binDir, warn := discoverPluginBinDir(resolved); binDir != "" {
		lp.BinDir = binDir
	} else if warn != nil {
		if w := recordPluginWarning(manifest.Name, warn.Field, warn.Message); w != nil {
			lp.Warnings = append(lp.Warnings, *w)
		}
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./agent/ -run TestLoadPlugin_PopulatesBinDir -v`
Run: `go test ./agent/ -run TestLoadPlugin_NoBinDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): LoadPlugin populates BinDir"
```

---

### Task 35: `LoadPlugin` populates `DefaultAgent`

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestLoadPlugin_PopulatesDefaultAgent(t *testing.T) {
	resetWarningsForTest()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "reviewer.md"),
		[]byte("---\nname: reviewer\ndescription: D\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"agent":"reviewer"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lp.DefaultAgent != "demo:reviewer" {
		t.Errorf("DefaultAgent = %q", lp.DefaultAgent)
	}
}
```

If the actual agent-file frontmatter is different from the example above, refer to `agent/plugin_agents_test.go` (or wherever `discoverPluginAgents` is tested) and match it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadPlugin_PopulatesDefaultAgent -v`
Expected: FAIL — not wired.

- [ ] **Step 3: Wire `loadPluginSettings` in `LoadPlugin`**

In `agent/plugin.go`, after the `lp.Agents = agents` line, add:

```go
	defaultAgent, settingsWarns, err := loadPluginSettings(resolved, manifest.Name, lp.Agents)
	if err != nil {
		return LoadedPlugin{}, fmt.Errorf("in plugin at %q: %w", resolved, err)
	}
	lp.DefaultAgent = defaultAgent
	lp.Warnings = append(lp.Warnings, settingsWarns...)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestLoadPlugin_PopulatesDefaultAgent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): LoadPlugin populates DefaultAgent from settings.json"
```

---

### Task 36: `LoadPlugin` collects manifest-level warnings

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_test.go`:

```go
func TestLoadPlugin_CapturesUnsupportedFieldWarnings(t *testing.T) {
	resetWarningsForTest()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name":"demo",
		"outputStyles":{"foo":{}},
		"channels":["c"],
		"dependencies":["d"]
	}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, w := range lp.Warnings {
		fields[w.Field] = true
	}
	for _, want := range []string{"outputStyles", "channels", "dependencies"} {
		if !fields[want] {
			t.Errorf("missing warning for %q (warnings: %+v)", want, lp.Warnings)
		}
	}
}

func TestLoadPlugin_WarningsDedupAcrossReload(t *testing.T) {
	resetWarningsForTest()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"demo","outputStyles":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lp1, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lp1.Warnings) != 1 {
		t.Errorf("first load: want 1 warning, got %d", len(lp1.Warnings))
	}
	lp2, err := LoadPlugin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lp2.Warnings) != 0 {
		t.Errorf("second load: want 0 warnings (dedup), got %d", len(lp2.Warnings))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadPlugin_CapturesUnsupportedFieldWarnings -v`
Expected: FAIL — not wired.

- [ ] **Step 3: Wire `collectManifestWarnings` in `LoadPlugin`**

In `agent/plugin.go`, at the end of `LoadPlugin` (just before `return lp, nil`), add:

```go
	lp.Warnings = append(lp.Warnings, collectManifestWarnings(manifest.Name, manifest)...)
```

- [ ] **Step 4: Run tests**

Run: `go test ./agent/ -run TestLoadPlugin_CapturesUnsupportedFieldWarnings -v`
Run: `go test ./agent/ -run TestLoadPlugin_WarningsDedupAcrossReload -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "feat(plugin): LoadPlugin collects warnings for unsupported fields"
```

---

## Phase 13 — Integration fixture & coverage gate

### Task 37: End-to-end `userconfig-basic` fixture

**Files:**
- Create: `agent/testdata/plugins/userconfig-basic/.claude-plugin/plugin.json`
- Create: `agent/plugin_userconfig_integration_test.go`

- [ ] **Step 1: Create the fixture**

Create `agent/testdata/plugins/userconfig-basic/.claude-plugin/plugin.json`:

```json
{
  "name": "userconfig-basic",
  "version": "0.1.0",
  "description": "Smallest plugin exercising one of each userConfig type",
  "userConfig": {
    "api_endpoint": {
      "type": "string",
      "title": "API endpoint",
      "description": "URL",
      "required": true,
      "default": "https://api.example.com"
    },
    "api_token": {
      "type": "string",
      "title": "API token",
      "description": "Secret",
      "sensitive": true,
      "required": true
    },
    "request_timeout_ms": {
      "type": "number",
      "title": "Timeout",
      "description": "ms",
      "default": 5000,
      "min": 100,
      "max": 60000
    },
    "verbose": {
      "type": "boolean",
      "title": "Verbose",
      "description": "Bool",
      "default": false
    },
    "workspace": {
      "type": "directory",
      "title": "Workspace",
      "description": "Path",
      "required": true
    },
    "allowed_hosts": {
      "type": "string",
      "title": "Hosts",
      "description": "List",
      "multiple": true,
      "default": ["api.example.com"]
    }
  }
}
```

- [ ] **Step 2: Write the failing test**

Create `agent/plugin_userconfig_integration_test.go`:

```go
package agent

import (
	"testing"
)

func TestUserConfigBasicFixture_ParseAndResolve(t *testing.T) {
	lp, err := LoadPlugin("testdata/plugins/userconfig-basic")
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	keys := []string{}
	for _, o := range lp.UserConfigOptions {
		keys = append(keys, o.Key)
	}
	wantKeys := []string{"api_endpoint", "api_token", "request_timeout_ms", "verbose", "workspace", "allowed_hosts"}
	if len(keys) != len(wantKeys) {
		t.Fatalf("got %v, want %v", keys, wantKeys)
	}
	for i, want := range wantKeys {
		if keys[i] != want {
			t.Errorf("opt %d = %q, want %q (declaration order)", i, keys[i], want)
		}
	}

	mp := MapPrompter{Values: map[string]string{
		"api_endpoint":       "https://example",
		"api_token":          "ghp_z",
		"request_timeout_ms": "30000",
		"verbose":            "true",
		"workspace":          "~/work",
		"allowed_hosts":      "a b c",
	}}
	t.Setenv("HOME", "/Users/test")
	plain := NewMemPluginConfigStore()
	secure := &memSecureStore{m: map[string]string{}}
	r, err := PromptForUserConfig(mp, "userconfig-basic@local", lp.UserConfigOptions, plain, secure)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Lookup("workspace"); v != "/Users/test/work" {
		t.Errorf("workspace = %q", v)
	}
	if v, _ := r.Lookup("allowed_hosts"); v != "a b c" {
		t.Errorf("allowed_hosts = %q", v)
	}
	if v, _ := r.Lookup("verbose"); v != "true" {
		t.Errorf("verbose = %q", v)
	}
	env := UserConfigEnvVars(r)
	if env["CLAUDE_PLUGIN_OPTION_API_TOKEN"] != "ghp_z" {
		t.Errorf("env missing API_TOKEN: %v", env)
	}
}
```

- [ ] **Step 3: Run test**

Run: `go test ./agent/ -run TestUserConfigBasicFixture -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add agent/testdata/plugins/userconfig-basic agent/plugin_userconfig_integration_test.go
git commit -m "test(plugin): end-to-end userconfig-basic fixture"
```

---

### Task 38: Coverage gate — full suite green

**Files:** none (verification only)

- [ ] **Step 1: Run the SP7 test focus**

```bash
go test ./agent/... -run 'UserConfig|PluginBin|PluginWarning|SecureStore|LoadPlugin|MemPluginConfigStore|ConfigJSONStore|LoadPluginSettings|DiscoverPluginBinDir|DiscoverPluginSkills|ValidateSkillsOverride|StringifyUserConfigValue|ExpandTilde|MapPrompter|ErrPromptCanceled|ResolveUserConfig|ParseUserConfig|EmitPluginWarnings|RecordPluginWarning|CollectManifestWarnings|NewSecureStore' -v
```

Expected: all green.

- [ ] **Step 2: Run the full package suite**

```bash
go test ./agent/...
go test ./agent/internal/securestore/...
```

Expected: PASS.

- [ ] **Step 3: Run vet / formatter**

```bash
go vet ./agent/...
gofmt -l agent/
```

Expected: no output.

- [ ] **Step 4: Commit if formatting changes**

Only run if the previous step produced output:

```bash
gofmt -w agent/
git add agent/
git commit -m "style: gofmt SP7 sources"
```

---

## Self-review notes

**Spec coverage (§§3–11):**
- §3 schema → Tasks 5–8 ✅
- §4 prompt flow → Tasks 22–25 ✅ (per-surface prompters land in SP4/SP8; this plan ships the interface + MapPrompter + tests)
- §5 storage → Tasks 9–14 ✅ (FileStore + ConfigJSONStore; SP1 still owns the typed `SerfConfig.PluginConfigs` accessor — flagged in Task 14)
- §6 substitution → Tasks 15–20 ✅
- §7 env vars → Task 21 ✅
- §8 bin/PATH → Tasks 26–28, 34 ✅
- §9 settings.json → Tasks 29, 35 ✅
- §10 skills → Tasks 30, 31, 33 ✅
- §11 warn-once → Tasks 2–4, 36 ✅
- §12 errors → exercised in Tasks 11, 20, 24, 29 ✅

**Known gaps / boundaries (not bugs):**
- `LocalExecutionEnvironment` shell-tool wiring (the actual `extraEnv["PATH"]` injection at `agent/session.go:2930-2942` per spec §8.1) is intentionally left for SP8 integration. Task 28 verifies the PATH helper end-to-end via direct `exec.Command`. The hookup of `extraEnv` to the shell tool registration is one short edit and SP8's integration test exercises it; flagging here so the SP8 implementer doesn't miss it.
- CLI / TUI / Hub prompter implementations (§4.2) live in SP4 and SP8 respectively; this plan ships the interface and `MapPrompter` only.
- `SerfConfig.PluginConfigs` typed accessor is owned by SP1. SP7 ships `ConfigJSONStore` so it can land before SP1 is finished; SP1 may later collapse the two paths.
- The `cmd/serf/main.go`, `cmd/serf-tui/embedded.go`, `cmd/serf-hub/web.go` modifications listed in §13 are SP8's responsibility (per spec §15.3).

**Placeholder scan:** No "TBD" / "implement later" entries in any task. Every step shows the actual code to write.

**Type consistency:** spot-checks:
- `LoadedPlugin.BinDir` (Task 27) matches `discoverPluginBinDir` return type (string) and `PluginBinPATH` input field.
- `UserConfigOption.Multiple bool` is consistent in `ParseUserConfig` (Task 7), `stringifyUserConfigValue` (Task 16), and `coerceUserConfigInput` (Task 23).
- `ResolvedUserConfig.PluginID` is set in `newResolvedForTest`, `ResolveUserConfig`, and `PromptForUserConfig` and read by `warnUnknownUserConfigKey`.
- `MemPluginConfigStore` matches `PluginConfigStore` interface in all consumer tests.
- `memSecureStore` matches `SecureStore` interface throughout.
- `recordPluginWarning` returns `*PluginWarning` everywhere it is called.
