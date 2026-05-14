# SP1 — Config Loader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the unified Claude Code-style `config.json` loader in package `agent`: parse one file, merge three tiers (global → project → `--config`), and expose the merged result with structured errors and warnings.

**Architecture:** One new pair of files, `agent/config.go` plus `agent/config_test.go`, modeled on the existing `agent/mcp_config.go` triad (`LoadMCPConfigFile` / `MergeMCPConfigs` / `DiscoverMCPConfigs`). All five top-level fields stay as `json.RawMessage` (or maps of it) except `permissions`, which SP1 destructures because it owns its merge rule. Validation runs inline at load time and fails fast.

**Tech Stack:** Go 1.x, `encoding/json`, `os`, `path/filepath`, standard Go `testing` with table-driven tests, `t.TempDir()`, `t.Setenv`, real `git` binary (skip if absent) for discovery tests.

**Parent spec:** `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
**Sub-spec (source of truth):** `docs/superpowers/specs/2026-05-14-claude-code-compat-sp1-config-loader-design.md`

**File map:**
- Create `agent/config.go` — types, `LoadSerfConfigFile`, `MergeSerfConfigs`, `DiscoverSerfConfig`, `globalSerfConfigPath`, `serfConfigWarnWriter`.
- Create `agent/config_test.go` — table-driven unit tests for parse, merge, discovery.
- Create `agent/testdata/config/full.json`, `agent/testdata/config/hooks_only.json`, `agent/testdata/config/permissions_only.json`, `agent/testdata/config/malformed.json`.

**Conventions:**
- No mocked filesystem. Every test that touches disk uses `t.TempDir()`.
- Tests live in `package agent` (same package, not `agent_test`) so unexported helpers are reachable, matching `mcp_config_test.go`.
- Test names follow `TestLoadSerfConfigFile_*`, `TestMergeSerfConfigs_*`, `TestDiscoverSerfConfig_*` so `go test ./agent/... -run SerfConfig` matches them all.
- Inside table-driven tests, sub-rows use `t.Run(tc.name, …)` per the `mcp_config_test.go` style.
- Commit per task. Conventional-commit prefix `feat:` for code+test commits, `test:` for test-only commits, `docs:` for fixture/docs commits.

---

## Task 1: Type scaffolding and stub functions

**Files:**
- Create: `agent/config.go`
- Test: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/config_test.go`:

```go
package agent

import (
	"encoding/json"
	"testing"
)

// TestSerfConfig_ZeroValue verifies the SerfConfig zero value is what the
// rest of the loader contract assumes (nil maps and slices).
func TestSerfConfig_ZeroValue(t *testing.T) {
	var c SerfConfig
	if c.Marketplaces != nil {
		t.Errorf("Marketplaces = %v, want nil", c.Marketplaces)
	}
	if c.EnabledPlugins != nil {
		t.Errorf("EnabledPlugins = %v, want nil", c.EnabledPlugins)
	}
	if c.Hooks != nil {
		t.Errorf("Hooks = %v, want nil", c.Hooks)
	}
	if c.MCPServers != nil {
		t.Errorf("MCPServers = %v, want nil", c.MCPServers)
	}
	if c.Permissions.Allow != nil || c.Permissions.Deny != nil || c.Permissions.DefaultMode != "" {
		t.Errorf("Permissions zero = %+v, want all empty", c.Permissions)
	}
	if c.Sources != nil {
		t.Errorf("Sources = %v, want nil", c.Sources)
	}

	// Compile-check: SerfConfig.Hooks values are []json.RawMessage so SP5
	// can unmarshal per-event entries individually.
	var _ map[string][]json.RawMessage = c.Hooks
}

// TestConfigTier_Constants verifies the three tier constants exist with the
// documented ordering (global < project < CLI).
func TestConfigTier_Constants(t *testing.T) {
	if TierGlobal >= TierProject {
		t.Errorf("TierGlobal (%d) must be < TierProject (%d)", TierGlobal, TierProject)
	}
	if TierProject >= TierCLI {
		t.Errorf("TierProject (%d) must be < TierCLI (%d)", TierProject, TierCLI)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/... -run SerfConfig`
Expected: FAIL — `undefined: SerfConfig`, `undefined: TierGlobal`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `agent/config.go`:

```go
package agent

import (
	"encoding/json"
	"io"
	"os"
)

// ConfigTier identifies which precedence tier a config file came from.
type ConfigTier int

const (
	// TierGlobal is ~/.config/serf/config.json (lowest precedence).
	TierGlobal ConfigTier = iota
	// TierProject is .serf/config.json at the git root.
	TierProject
	// TierCLI is a --config <path> argument (highest precedence).
	TierCLI
)

// ConfigSource records one file that contributed to a SerfConfig.
type ConfigSource struct {
	Path     string     // absolute path
	Tier     ConfigTier // Global, Project, or CLI
	CLIIndex int        // position in the --config list (0-based) for TierCLI; -1 otherwise
}

// PermissionsConfig is the only top-level field SP1 destructures, because SP1
// owns its merge rule (allow/deny concatenate; defaultMode scalar-overwrites).
type PermissionsConfig struct {
	Allow       []string
	Deny        []string
	DefaultMode string // "" means unset; SP2 picks a default.
}

// SerfConfig is the parsed contents of one config.json file or the merged
// result of multiple files. Each field is raw JSON; SP1 does not interpret
// inner shapes except for Permissions.
type SerfConfig struct {
	Marketplaces   map[string]json.RawMessage
	EnabledPlugins map[string]json.RawMessage
	Hooks          map[string][]json.RawMessage
	MCPServers     map[string]json.RawMessage
	Permissions    PermissionsConfig
	Sources        []ConfigSource
}

// serfConfigWarnWriter is where non-fatal config warnings go. Tests swap it
// to capture output without touching os.Stderr.
var serfConfigWarnWriter io.Writer = os.Stderr

// LoadSerfConfigFile parses one config.json. Missing file is not an error
// (returns zero SerfConfig and nil). Malformed JSON or a structurally
// invalid field is an error annotated with path + offending field.
func LoadSerfConfigFile(path string) (SerfConfig, error) {
	return SerfConfig{}, nil
}

// MergeSerfConfigs merges layers low-precedence-first.
func MergeSerfConfigs(layers ...SerfConfig) SerfConfig {
	return SerfConfig{}
}

// DiscoverSerfConfig loads global → project → each --config in order and
// returns the merged result.
func DiscoverSerfConfig(env ExecutionEnvironment, cliPaths []string) (SerfConfig, error) {
	return SerfConfig{}, nil
}

// globalSerfConfigPath returns the global config.json path. Uses
// XDG_CONFIG_HOME if set, otherwise ~/.config.
func globalSerfConfigPath() string {
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/... -run SerfConfig`
Expected: PASS for `TestSerfConfig_ZeroValue` and `TestConfigTier_Constants`.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): scaffold SerfConfig types and stub loaders"
```

---

## Task 2: LoadSerfConfigFile — absent file returns zero (§8.1 row 1)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
import (
	"path/filepath"
)

func TestLoadSerfConfigFile_AbsentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json") // not written

	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("absent file should not error, got: %v", err)
	}
	if cfg.Marketplaces != nil || cfg.EnabledPlugins != nil || cfg.Hooks != nil ||
		cfg.MCPServers != nil || cfg.Sources != nil ||
		cfg.Permissions.Allow != nil || cfg.Permissions.Deny != nil ||
		cfg.Permissions.DefaultMode != "" {
		t.Fatalf("absent file should return zero SerfConfig, got: %+v", cfg)
	}
}
```

(If `path/filepath` is not already imported in the test file, add it to the import block; otherwise merge.)

- [ ] **Step 2: Run test to verify it fails or passes trivially**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_AbsentFile -v`
Expected: PASS (the stub returns zero SerfConfig and nil error). This is the documented behavior for an absent file. The next task will introduce real parsing, which is when this contract is meaningful to protect.

- [ ] **Step 3: Implement the absent-file path explicitly**

Replace `LoadSerfConfigFile` in `agent/config.go`:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

func LoadSerfConfigFile(path string) (SerfConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SerfConfig{}, nil
		}
		return SerfConfig{}, fmt.Errorf("reading serf config %s: %w", path, err)
	}
	_ = data
	// Parsing comes in the next task.
	return SerfConfig{}, nil
}
```

- [ ] **Step 4: Run test to verify it still passes**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_AbsentFile -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): LoadSerfConfigFile returns zero on absent file"
```

---

## Task 3: LoadSerfConfigFile — empty object & top-level-not-object (§8.1 rows 2 and 7)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
import (
	"os"
	"strings"
)

func TestLoadSerfConfigFile_EmptyObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("empty object should parse cleanly, got: %v", err)
	}
	// Every field is the zero value EXCEPT Sources, which records the file.
	if cfg.Marketplaces != nil || cfg.EnabledPlugins != nil || cfg.Hooks != nil ||
		cfg.MCPServers != nil ||
		cfg.Permissions.Allow != nil || cfg.Permissions.Deny != nil ||
		cfg.Permissions.DefaultMode != "" {
		t.Fatalf("empty object should leave fields zero, got: %+v", cfg)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].Path != path {
		t.Fatalf("Sources = %+v, want one entry with path %q", cfg.Sources, path)
	}
	if cfg.Sources[0].CLIIndex != -1 {
		t.Errorf("Sources[0].CLIIndex = %d, want -1", cfg.Sources[0].CLIIndex)
	}
}

func TestLoadSerfConfigFile_TopLevelNotObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`[]`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadSerfConfigFile(path)
	if err == nil {
		t.Fatal("top-level array should error")
	}
	if !strings.Contains(err.Error(), "top-level must be a JSON object") {
		t.Errorf("error = %q, want substring %q", err.Error(), "top-level must be a JSON object")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, missing file path %q", err.Error(), path)
	}
}
```

(Add `os` and `strings` to the test file imports if not already present.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_(EmptyObject|TopLevelNotObject)" -v`
Expected: FAIL — empty-object test fails on Sources length; top-level-array test fails on missing error.

- [ ] **Step 3: Implement parse skeleton**

Replace `LoadSerfConfigFile` in `agent/config.go`. Also add a `bytes` import:

```go
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

func LoadSerfConfigFile(path string) (SerfConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SerfConfig{}, nil
		}
		return SerfConfig{}, fmt.Errorf("reading serf config %s: %w", path, err)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return SerfConfig{}, fmt.Errorf("serf config %s: top-level must be a JSON object", path)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return SerfConfig{}, fmt.Errorf("parsing serf config %s: %w", path, err)
	}

	cfg := SerfConfig{
		Sources: []ConfigSource{{Path: path, Tier: TierGlobal, CLIIndex: -1}},
	}
	// Field parsers slot in here in the next tasks.
	_ = raw
	return cfg, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_(EmptyObject|TopLevelNotObject|AbsentFile)" -v`
Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): parse top-level object and record Sources"
```

---

## Task 4: LoadSerfConfigFile — malformed JSON (§8.1 row 8)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestLoadSerfConfigFile_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// A lone '{' is invalid JSON but does start with '{' so it passes the
	// top-level check and exercises json.Unmarshal's error path.
	if err := os.WriteFile(path, []byte(`{`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadSerfConfigFile(path)
	if err == nil {
		t.Fatal("malformed JSON should error")
	}
	if !strings.Contains(err.Error(), "parsing serf config") {
		t.Errorf("error = %q, want substring %q", err.Error(), "parsing serf config")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, missing file path %q", err.Error(), path)
	}
}
```

- [ ] **Step 2: Run test to verify it passes already**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_MalformedJSON -v`
Expected: PASS — `json.Unmarshal` rejects `{` and the error already wraps with `parsing serf config %s`. This test pins the contract.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin malformed-JSON error contract"
```

---

## Task 5: LoadSerfConfigFile — parse Marketplaces / EnabledPlugins / MCPServers (§8.1 rows 11, 12, 13 + happy paths)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestLoadSerfConfigFile_RawObjectFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		// check is run against the parsed config.
		check func(t *testing.T, cfg SerfConfig)
	}{
		{
			name: "marketplaces populated",
			body: `{"marketplaces":{"anthropics":{"source":{"type":"github"}}}}`,
			check: func(t *testing.T, cfg SerfConfig) {
				if len(cfg.Marketplaces) != 1 {
					t.Fatalf("Marketplaces len = %d, want 1", len(cfg.Marketplaces))
				}
				if _, ok := cfg.Marketplaces["anthropics"]; !ok {
					t.Errorf("Marketplaces missing key anthropics: %v", cfg.Marketplaces)
				}
			},
		},
		{
			name: "enabledPlugins populated",
			body: `{"enabledPlugins":{"a@m":true,"b@m":{"version":"1.0"}}}`,
			check: func(t *testing.T, cfg SerfConfig) {
				if len(cfg.EnabledPlugins) != 2 {
					t.Fatalf("EnabledPlugins len = %d, want 2", len(cfg.EnabledPlugins))
				}
			},
		},
		{
			name: "mcpServers populated",
			body: `{"mcpServers":{"github":{"command":"gh-mcp"}}}`,
			check: func(t *testing.T, cfg SerfConfig) {
				if _, ok := cfg.MCPServers["github"]; !ok {
					t.Errorf("MCPServers missing github: %v", cfg.MCPServers)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadSerfConfigFile(path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, cfg)
		})
	}
}

func TestLoadSerfConfigFile_RawObjectFields_TypeErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{"marketplaces not object", `{"marketplaces":[]}`, `"marketplaces"`},
		{"enabledPlugins not object", `{"enabledPlugins":[]}`, `"enabledPlugins"`},
		{"mcpServers not object", `{"mcpServers":[]}`, `"mcpServers"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSerfConfigFile(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error = %q, want substring %s", err.Error(), tc.wantField)
			}
			if !strings.Contains(err.Error(), "must be an object") {
				t.Errorf("error = %q, want substring 'must be an object'", err.Error())
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, missing file path %q", err.Error(), path)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_RawObjectFields" -v`
Expected: FAIL — fields not yet populated; type errors not yet raised.

- [ ] **Step 3: Implement raw-object-field parsing**

Replace `LoadSerfConfigFile` in `agent/config.go`:

```go
func LoadSerfConfigFile(path string) (SerfConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SerfConfig{}, nil
		}
		return SerfConfig{}, fmt.Errorf("reading serf config %s: %w", path, err)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return SerfConfig{}, fmt.Errorf("serf config %s: top-level must be a JSON object", path)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return SerfConfig{}, fmt.Errorf("parsing serf config %s: %w", path, err)
	}

	cfg := SerfConfig{
		Sources: []ConfigSource{{Path: path, Tier: TierGlobal, CLIIndex: -1}},
	}

	for _, field := range []string{"marketplaces", "enabledPlugins", "mcpServers"} {
		v, ok := raw[field]
		if !ok {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err != nil {
			return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be an object", path, field)
		}
		switch field {
		case "marketplaces":
			cfg.Marketplaces = m
		case "enabledPlugins":
			cfg.EnabledPlugins = m
		case "mcpServers":
			cfg.MCPServers = m
		}
	}

	return cfg, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_RawObjectFields" -v`
Expected: PASS for both the happy-path table and the type-error table.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): parse marketplaces, enabledPlugins, mcpServers"
```

---

## Task 6: LoadSerfConfigFile — parse Hooks (§8.1 rows 4, 6, 9, 10)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestLoadSerfConfigFile_HooksPopulated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"hooks": {
			"PreToolUse": [
				{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh"}]}
			],
			"Stop": []
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MCPServers != nil {
		t.Errorf("MCPServers should remain nil, got %v", cfg.MCPServers)
	}
	if cfg.Hooks == nil {
		t.Fatalf("Hooks should be populated, got nil")
	}
	if got := len(cfg.Hooks["PreToolUse"]); got != 1 {
		t.Errorf("Hooks[PreToolUse] len = %d, want 1", got)
	}
	if _, ok := cfg.Hooks["Stop"]; !ok {
		t.Errorf("Hooks should include Stop key with empty array")
	}
	if got := len(cfg.Hooks["Stop"]); got != 0 {
		t.Errorf("Hooks[Stop] len = %d, want 0", got)
	}
}

func TestLoadSerfConfigFile_HooksTypeErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{"hooks not an object", `{"hooks":[]}`, `"hooks"`},
		{"hooks event value not array", `{"hooks":{"PreToolUse":{}}}`, `"hooks.PreToolUse"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSerfConfigFile(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error = %q, want substring %s", err.Error(), tc.wantField)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, missing file path %q", err.Error(), path)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_Hooks" -v`
Expected: FAIL — `Hooks` is still nil; type errors not raised.

- [ ] **Step 3: Implement Hooks parsing**

Add this block in `LoadSerfConfigFile` after the loop over raw-object fields (before `return cfg, nil`):

```go
	if v, ok := raw["hooks"]; ok {
		var hooksRaw map[string]json.RawMessage
		if err := json.Unmarshal(v, &hooksRaw); err != nil {
			return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be an object of event-name to array", path, "hooks")
		}
		cfg.Hooks = make(map[string][]json.RawMessage, len(hooksRaw))
		for event, ev := range hooksRaw {
			var arr []json.RawMessage
			if err := json.Unmarshal(ev, &arr); err != nil {
				return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be an array", path, "hooks."+event)
			}
			cfg.Hooks[event] = arr
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_Hooks" -v`
Expected: PASS for both tests.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): parse hooks event-to-array map"
```

---

## Task 7: LoadSerfConfigFile — parse Permissions happy path (§8.1 rows 3 and 5)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestLoadSerfConfigFile_PermissionsFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"permissions": {
			"allow": ["Bash(ls:*)", "Skill(*)"],
			"deny":  ["Bash(rm:-rf*)"],
			"defaultMode": "default"
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Permissions.Allow; len(got) != 2 || got[0] != "Bash(ls:*)" || got[1] != "Skill(*)" {
		t.Errorf("Allow = %v", got)
	}
	if got := cfg.Permissions.Deny; len(got) != 1 || got[0] != "Bash(rm:-rf*)" {
		t.Errorf("Deny = %v", got)
	}
	if cfg.Permissions.DefaultMode != "default" {
		t.Errorf("DefaultMode = %q, want %q", cfg.Permissions.DefaultMode, "default")
	}
}

func TestLoadSerfConfigFile_PermissionsPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"permissions":{"allow":["Bash(ls:*)"]}}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Permissions.Allow; len(got) != 1 || got[0] != "Bash(ls:*)" {
		t.Errorf("Allow = %v", got)
	}
	if cfg.Permissions.Deny != nil {
		t.Errorf("Deny = %v, want nil", cfg.Permissions.Deny)
	}
	if cfg.Permissions.DefaultMode != "" {
		t.Errorf("DefaultMode = %q, want empty string", cfg.Permissions.DefaultMode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_Permissions -v`
Expected: FAIL — Permissions parsing not yet implemented.

- [ ] **Step 3: Implement Permissions parsing**

Add this block in `LoadSerfConfigFile` after the Hooks block (before `return cfg, nil`):

```go
	if v, ok := raw["permissions"]; ok {
		var permRaw map[string]json.RawMessage
		if err := json.Unmarshal(v, &permRaw); err != nil {
			return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be an object", path, "permissions")
		}
		for k, vv := range permRaw {
			switch k {
			case "allow":
				var allow []string
				if err := json.Unmarshal(vv, &allow); err != nil {
					return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be an array of strings", path, "permissions.allow")
				}
				cfg.Permissions.Allow = allow
			case "deny":
				var deny []string
				if err := json.Unmarshal(vv, &deny); err != nil {
					return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be an array of strings", path, "permissions.deny")
				}
				cfg.Permissions.Deny = deny
			case "defaultMode":
				var mode string
				if err := json.Unmarshal(vv, &mode); err != nil {
					return SerfConfig{}, fmt.Errorf("serf config %s: field %q: must be a string", path, "permissions.defaultMode")
				}
				cfg.Permissions.DefaultMode = mode
			default:
				fmt.Fprintf(serfConfigWarnWriter, "serf config %s: ignoring unknown field %q\n", path, "permissions."+k)
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_Permissions -v`
Expected: PASS for both `_PermissionsFull` and `_PermissionsPartial`.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): parse permissions allow/deny/defaultMode"
```

---

## Task 8: LoadSerfConfigFile — permissions type errors (§8.1 rows 14–18)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestLoadSerfConfigFile_PermissionsTypeErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantField string
		wantMsg   string
	}{
		{"permissions not object", `{"permissions":[]}`, `"permissions"`, "must be an object"},
		{"allow not array", `{"permissions":{"allow":"x"}}`, `"permissions.allow"`, "must be an array of strings"},
		{"allow non-strings", `{"permissions":{"allow":[1]}}`, `"permissions.allow"`, "must be an array of strings"},
		{"deny not array", `{"permissions":{"deny":"x"}}`, `"permissions.deny"`, "must be an array of strings"},
		{"defaultMode not string", `{"permissions":{"defaultMode":42}}`, `"permissions.defaultMode"`, "must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSerfConfigFile(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error = %q, want substring %s", err.Error(), tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantMsg)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, missing file path %q", err.Error(), path)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they pass already**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_PermissionsTypeErrors -v`
Expected: PASS — the parser implementation in Task 7 already covers each branch. This test pins them.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin permissions type-error contract"
```

---

## Task 9: LoadSerfConfigFile — unknown-field warnings (§8.1 rows 19 and 20)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
import (
	"bytes"
)

func TestLoadSerfConfigFile_UnknownTopLevelFieldWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"themes":{}}`), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := serfConfigWarnWriter
	serfConfigWarnWriter = &buf
	t.Cleanup(func() { serfConfigWarnWriter = prev })

	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("unknown top-level field should not error, got: %v", err)
	}
	if cfg.Marketplaces != nil || cfg.Hooks != nil {
		t.Errorf("config should be otherwise zero, got: %+v", cfg)
	}
	got := buf.String()
	if !strings.Contains(got, `unknown field "themes"`) {
		t.Errorf("warning = %q, want substring %q", got, `unknown field "themes"`)
	}
	if !strings.Contains(got, path) {
		t.Errorf("warning = %q, missing file path %q", got, path)
	}
	if n := strings.Count(got, "themes"); n != 1 {
		t.Errorf("themes mentioned %d times, want 1", n)
	}
}

func TestLoadSerfConfigFile_UnknownPermissionsSubfieldWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"permissions":{"foo":1}}`), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := serfConfigWarnWriter
	serfConfigWarnWriter = &buf
	t.Cleanup(func() { serfConfigWarnWriter = prev })

	cfg, err := LoadSerfConfigFile(path)
	if err != nil {
		t.Fatalf("unknown permissions subfield should not error, got: %v", err)
	}
	if cfg.Permissions.Allow != nil || cfg.Permissions.DefaultMode != "" {
		t.Errorf("permissions should be empty, got: %+v", cfg.Permissions)
	}
	got := buf.String()
	if !strings.Contains(got, `unknown field "permissions.foo"`) {
		t.Errorf("warning = %q, want substring %q", got, `unknown field "permissions.foo"`)
	}
}
```

- [ ] **Step 2: Run tests to verify the top-level test fails**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_Unknown" -v`
Expected: FAIL for `_UnknownTopLevelFieldWarns` (warning not emitted). The `_UnknownPermissionsSubfieldWarns` test passes already because Task 7 wired the permissions-subfield warning.

- [ ] **Step 3: Implement the top-level unknown-field warning**

Modify `LoadSerfConfigFile` to scan unknown top-level keys after the `raw` unmarshal succeeds. Insert this block immediately after `var raw map[string]json.RawMessage; if err := json.Unmarshal(...)` (before the `cfg := SerfConfig{...}` line):

```go
	known := map[string]struct{}{
		"marketplaces":   {},
		"enabledPlugins": {},
		"hooks":          {},
		"mcpServers":     {},
		"permissions":    {},
	}
	for k := range raw {
		if _, ok := known[k]; !ok {
			fmt.Fprintf(serfConfigWarnWriter, "serf config %s: ignoring unknown field %q\n", path, k)
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_Unknown" -v`
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): warn once on unknown top-level and permissions fields"
```

---

## Task 10: LoadSerfConfigFile — I/O error path (§8.1 row 21)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
import (
	"runtime"
)

func TestLoadSerfConfigFile_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics required")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode checks")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })

	_, err := LoadSerfConfigFile(path)
	if err == nil {
		t.Fatal("expected permission-denied error")
	}
	if !strings.Contains(err.Error(), "reading serf config") {
		t.Errorf("error = %q, want substring %q", err.Error(), "reading serf config")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, missing file path %q", err.Error(), path)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/... -run TestLoadSerfConfigFile_PermissionDenied -v`
Expected: PASS — the existing `LoadSerfConfigFile` already wraps non-`ErrNotExist` errors with `reading serf config %s`. This test pins the contract.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin permission-denied error contract"
```

---

## Task 11: MergeSerfConfigs — zero and one layer (§8.2 rows 1, 2)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestMergeSerfConfigs_ZeroLayers(t *testing.T) {
	got := MergeSerfConfigs()
	if got.Marketplaces != nil || got.EnabledPlugins != nil || got.Hooks != nil ||
		got.MCPServers != nil || got.Sources != nil ||
		got.Permissions.Allow != nil || got.Permissions.Deny != nil ||
		got.Permissions.DefaultMode != "" {
		t.Fatalf("zero layers = %+v, want zero SerfConfig", got)
	}
}

func TestMergeSerfConfigs_OneLayer(t *testing.T) {
	in := SerfConfig{
		Marketplaces:   map[string]json.RawMessage{"m": json.RawMessage(`{}`)},
		EnabledPlugins: map[string]json.RawMessage{"p@m": json.RawMessage(`true`)},
		Hooks:          map[string][]json.RawMessage{"PreToolUse": {json.RawMessage(`{}`)}},
		MCPServers:     map[string]json.RawMessage{"s": json.RawMessage(`{}`)},
		Permissions: PermissionsConfig{
			Allow:       []string{"Bash(ls:*)"},
			Deny:        []string{"Bash(rm:*)"},
			DefaultMode: "default",
		},
		Sources: []ConfigSource{{Path: "/x", Tier: TierGlobal, CLIIndex: -1}},
	}
	got := MergeSerfConfigs(in)
	if len(got.Marketplaces) != 1 || len(got.EnabledPlugins) != 1 ||
		len(got.Hooks["PreToolUse"]) != 1 || len(got.MCPServers) != 1 {
		t.Errorf("one-layer copy lost data: %+v", got)
	}
	if len(got.Permissions.Allow) != 1 || got.Permissions.Allow[0] != "Bash(ls:*)" {
		t.Errorf("Allow = %v", got.Permissions.Allow)
	}
	if got.Permissions.DefaultMode != "default" {
		t.Errorf("DefaultMode = %q", got.Permissions.DefaultMode)
	}
	if len(got.Sources) != 1 || got.Sources[0].Path != "/x" {
		t.Errorf("Sources = %+v", got.Sources)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/... -run TestMergeSerfConfigs -v`
Expected: FAIL — stub merge returns zero SerfConfig regardless of input.

- [ ] **Step 3: Implement MergeSerfConfigs**

Replace `MergeSerfConfigs` in `agent/config.go`:

```go
func MergeSerfConfigs(layers ...SerfConfig) SerfConfig {
	var out SerfConfig
	for _, layer := range layers {
		for k, v := range layer.Marketplaces {
			if out.Marketplaces == nil {
				out.Marketplaces = map[string]json.RawMessage{}
			}
			out.Marketplaces[k] = v
		}
		for k, v := range layer.EnabledPlugins {
			if out.EnabledPlugins == nil {
				out.EnabledPlugins = map[string]json.RawMessage{}
			}
			out.EnabledPlugins[k] = v
		}
		for k, v := range layer.MCPServers {
			if out.MCPServers == nil {
				out.MCPServers = map[string]json.RawMessage{}
			}
			out.MCPServers[k] = v
		}
		for event, arr := range layer.Hooks {
			if out.Hooks == nil {
				out.Hooks = map[string][]json.RawMessage{}
			}
			out.Hooks[event] = append(out.Hooks[event], arr...)
		}
		out.Permissions.Allow = append(out.Permissions.Allow, layer.Permissions.Allow...)
		out.Permissions.Deny = append(out.Permissions.Deny, layer.Permissions.Deny...)
		if layer.Permissions.DefaultMode != "" {
			out.Permissions.DefaultMode = layer.Permissions.DefaultMode
		}
		out.Sources = append(out.Sources, layer.Sources...)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run TestMergeSerfConfigs -v`
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): MergeSerfConfigs base cases (zero and one layer)"
```

---

## Task 12: MergeSerfConfigs — hooks concat and per-event independence (§8.2 rows 3, 4)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestMergeSerfConfigs_HooksConcatInTierOrder(t *testing.T) {
	g := SerfConfig{Hooks: map[string][]json.RawMessage{"PreToolUse": {json.RawMessage(`"A"`)}}}
	p := SerfConfig{Hooks: map[string][]json.RawMessage{"PreToolUse": {json.RawMessage(`"B"`)}}}
	c := SerfConfig{Hooks: map[string][]json.RawMessage{"PreToolUse": {json.RawMessage(`"C"`)}}}

	got := MergeSerfConfigs(g, p, c).Hooks["PreToolUse"]
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (concat)", len(got))
	}
	if string(got[0]) != `"A"` || string(got[1]) != `"B"` || string(got[2]) != `"C"` {
		t.Errorf("order wrong: %v", got)
	}
}

func TestMergeSerfConfigs_HooksPerEventIndependence(t *testing.T) {
	g := SerfConfig{Hooks: map[string][]json.RawMessage{"PreToolUse": {json.RawMessage(`"A"`)}}}
	p := SerfConfig{Hooks: map[string][]json.RawMessage{"Stop": {json.RawMessage(`"B"`)}}}

	merged := MergeSerfConfigs(g, p)
	if len(merged.Hooks["PreToolUse"]) != 1 || string(merged.Hooks["PreToolUse"][0]) != `"A"` {
		t.Errorf("PreToolUse = %v", merged.Hooks["PreToolUse"])
	}
	if len(merged.Hooks["Stop"]) != 1 || string(merged.Hooks["Stop"][0]) != `"B"` {
		t.Errorf("Stop = %v", merged.Hooks["Stop"])
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestMergeSerfConfigs_Hooks" -v`
Expected: PASS — Task 11 already implemented per-event append. This pins the contract.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin hooks concat and per-event independence"
```

---

## Task 13: MergeSerfConfigs — map replace-by-key for mcpServers / marketplaces / enabledPlugins (§8.2 rows 5–8)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestMergeSerfConfigs_MCPServersReplaceAndAdd(t *testing.T) {
	g := SerfConfig{MCPServers: map[string]json.RawMessage{"github": json.RawMessage(`{"v":"g"}`)}}
	p := SerfConfig{MCPServers: map[string]json.RawMessage{
		"github": json.RawMessage(`{"v":"p"}`),
		"jira":   json.RawMessage(`{"v":"p"}`),
	}}
	got := MergeSerfConfigs(g, p).MCPServers
	if string(got["github"]) != `{"v":"p"}` {
		t.Errorf("github = %s, want project value", got["github"])
	}
	if _, ok := got["jira"]; !ok {
		t.Errorf("jira not added: %v", got)
	}
}

func TestMergeSerfConfigs_MarketplacesAndEnabledPluginsReplace(t *testing.T) {
	g := SerfConfig{
		Marketplaces:   map[string]json.RawMessage{"m": json.RawMessage(`{"v":"g"}`)},
		EnabledPlugins: map[string]json.RawMessage{"a@m": json.RawMessage(`true`)},
	}
	p := SerfConfig{
		Marketplaces:   map[string]json.RawMessage{"m": json.RawMessage(`{"v":"p"}`)},
		EnabledPlugins: map[string]json.RawMessage{"a@m": json.RawMessage(`{"version":"1.0"}`)},
	}
	got := MergeSerfConfigs(g, p)
	if string(got.Marketplaces["m"]) != `{"v":"p"}` {
		t.Errorf("Marketplaces[m] = %s", got.Marketplaces["m"])
	}
	if string(got.EnabledPlugins["a@m"]) != `{"version":"1.0"}` {
		t.Errorf("EnabledPlugins[a@m] = %s", got.EnabledPlugins["a@m"])
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestMergeSerfConfigs_(MCPServers|MarketplacesAndEnabled)" -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin map replace-by-key for the three RawMessage maps"
```

---

## Task 14: MergeSerfConfigs — permissions allow/deny concat and duplicate preservation (§8.2 rows 9, 10, 14)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestMergeSerfConfigs_PermissionsAllowDenyConcat(t *testing.T) {
	g := SerfConfig{Permissions: PermissionsConfig{Allow: []string{"a"}, Deny: []string{"x"}}}
	p := SerfConfig{Permissions: PermissionsConfig{Allow: []string{"b"}, Deny: []string{"y"}}}
	c := SerfConfig{Permissions: PermissionsConfig{Allow: []string{"c"}, Deny: []string{"z"}}}

	got := MergeSerfConfigs(g, p, c).Permissions
	wantAllow := []string{"a", "b", "c"}
	wantDeny := []string{"x", "y", "z"}
	if len(got.Allow) != 3 || got.Allow[0] != wantAllow[0] || got.Allow[1] != wantAllow[1] || got.Allow[2] != wantAllow[2] {
		t.Errorf("Allow = %v, want %v", got.Allow, wantAllow)
	}
	if len(got.Deny) != 3 || got.Deny[0] != wantDeny[0] || got.Deny[1] != wantDeny[1] || got.Deny[2] != wantDeny[2] {
		t.Errorf("Deny = %v, want %v", got.Deny, wantDeny)
	}
}

func TestMergeSerfConfigs_DuplicateAllowPreserved(t *testing.T) {
	g := SerfConfig{Permissions: PermissionsConfig{Allow: []string{"x"}}}
	p := SerfConfig{Permissions: PermissionsConfig{Allow: []string{"x"}}}
	got := MergeSerfConfigs(g, p).Permissions.Allow
	if len(got) != 2 || got[0] != "x" || got[1] != "x" {
		t.Errorf("Allow = %v, want [x x] (SP2 dedupes, not SP1)", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestMergeSerfConfigs_(PermissionsAllowDeny|DuplicateAllow)" -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin permissions allow/deny concat semantics"
```

---

## Task 15: MergeSerfConfigs — defaultMode scalar overwrite and empty-preserves-lower (§8.2 rows 11, 12)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestMergeSerfConfigs_DefaultModeScalarOverwrite(t *testing.T) {
	g := SerfConfig{Permissions: PermissionsConfig{DefaultMode: "default"}}
	p := SerfConfig{Permissions: PermissionsConfig{DefaultMode: ""}}
	c := SerfConfig{Permissions: PermissionsConfig{DefaultMode: "acceptEdits"}}
	got := MergeSerfConfigs(g, p, c).Permissions.DefaultMode
	if got != "acceptEdits" {
		t.Errorf("DefaultMode = %q, want %q", got, "acceptEdits")
	}
}

func TestMergeSerfConfigs_DefaultModeEmptyPreservesLower(t *testing.T) {
	g := SerfConfig{Permissions: PermissionsConfig{DefaultMode: "default"}}
	p := SerfConfig{Permissions: PermissionsConfig{DefaultMode: ""}}
	c := SerfConfig{Permissions: PermissionsConfig{DefaultMode: ""}}
	got := MergeSerfConfigs(g, p, c).Permissions.DefaultMode
	if got != "default" {
		t.Errorf("DefaultMode = %q, want %q (empty higher tiers do not clobber)", got, "default")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestMergeSerfConfigs_DefaultMode" -v`
Expected: PASS — the merge impl already only overwrites when the layer value is non-empty.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin defaultMode overwrite vs empty-preserves rule"
```

---

## Task 16: MergeSerfConfigs — Sources concatenate in layer order (§8.2 row 13)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestMergeSerfConfigs_SourcesConcatInOrder(t *testing.T) {
	g := SerfConfig{Sources: []ConfigSource{{Path: "/g", Tier: TierGlobal, CLIIndex: -1}}}
	p := SerfConfig{Sources: []ConfigSource{{Path: "/p", Tier: TierProject, CLIIndex: -1}}}
	c0 := SerfConfig{Sources: []ConfigSource{{Path: "/c0", Tier: TierCLI, CLIIndex: 0}}}
	c1 := SerfConfig{Sources: []ConfigSource{{Path: "/c1", Tier: TierCLI, CLIIndex: 1}}}

	got := MergeSerfConfigs(g, p, c0, c1).Sources
	if len(got) != 4 {
		t.Fatalf("len(Sources) = %d, want 4", len(got))
	}
	wantPaths := []string{"/g", "/p", "/c0", "/c1"}
	for i, w := range wantPaths {
		if got[i].Path != w {
			t.Errorf("Sources[%d].Path = %q, want %q", i, got[i].Path, w)
		}
	}
	if got[0].Tier != TierGlobal || got[1].Tier != TierProject || got[2].Tier != TierCLI || got[3].Tier != TierCLI {
		t.Errorf("tier ordering wrong: %+v", got)
	}
	if got[2].CLIIndex != 0 || got[3].CLIIndex != 1 {
		t.Errorf("CLIIndex preservation wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/... -run TestMergeSerfConfigs_SourcesConcatInOrder -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin Sources concatenation order"
```

---

## Task 17: globalSerfConfigPath — XDG_CONFIG_HOME and fallback

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestGlobalSerfConfigPath_HonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	got := globalSerfConfigPath()
	want := filepath.Join("/custom/xdg", "serf", "config.json")
	if got != want {
		t.Errorf("globalSerfConfigPath = %q, want %q", got, want)
	}
}

func TestGlobalSerfConfigPath_FallbackHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/fakehome")
	got := globalSerfConfigPath()
	want := filepath.Join("/tmp/fakehome", ".config", "serf", "config.json")
	if got != want {
		t.Errorf("globalSerfConfigPath = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/... -run TestGlobalSerfConfigPath -v`
Expected: FAIL — stub returns `""`.

- [ ] **Step 3: Implement globalSerfConfigPath**

Replace `globalSerfConfigPath` in `agent/config.go` (mirror `globalMCPConfigPath` exactly except for the final filename):

```go
import (
	"path/filepath"
)

func globalSerfConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "config.json")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/... -run TestGlobalSerfConfigPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): globalSerfConfigPath honors XDG_CONFIG_HOME"
```

---

## Task 18: Test fixture helpers — `serfConfigTestRepo` (git repo + tier paths)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the helper as a regular function**

Append to `agent/config_test.go`:

```go
import (
	"context"
	"os/exec"
	"time"
)

// serfConfigTestRepo builds a temporary directory layout suitable for
// DiscoverSerfConfig tests:
//
//   <tmp>/xdg/serf/config.json        (global, when global!="")
//   <tmp>/repo/.git/...               (initialized git repo)
//   <tmp>/repo/.serf/config.json      (project, when project!="")
//   <tmp>/cli/<name>.json             (one CLI file per cliFiles entry)
//
// It also calls t.Setenv to point XDG_CONFIG_HOME at <tmp>/xdg and returns
// an ExecutionEnvironment rooted at <tmp>/repo so DiscoverSerfConfig sees
// the project tier through git-root detection.
//
// Empty string for global or project means "do not create that file".
// cliFiles maps relative name -> file body; the returned cliPaths slice
// preserves cliFiles iteration order via the caller-supplied cliOrder.
func serfConfigTestRepo(t *testing.T, global, project string, cliOrder []string, cliFiles map[string]string) (env ExecutionEnvironment, repo string, cliPaths []string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping discovery test")
	}

	root := t.TempDir()

	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "serf"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if global != "" {
		if err := os.WriteFile(filepath.Join(xdg, "serf", "config.json"), []byte(global), 0644); err != nil {
			t.Fatal(err)
		}
	}

	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}
	if project != "" {
		if err := os.MkdirAll(filepath.Join(repo, ".serf"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".serf", "config.json"), []byte(project), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cliDir := filepath.Join(root, "cli")
	if err := os.MkdirAll(cliDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range cliOrder {
		body, ok := cliFiles[name]
		if !ok {
			t.Fatalf("cliOrder names %q but cliFiles has no entry", name)
		}
		p := filepath.Join(cliDir, name)
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		cliPaths = append(cliPaths, p)
	}

	env = NewLocalExecutionEnvironment(repo)
	return env, repo, cliPaths
}
```

- [ ] **Step 2: Confirm it compiles**

Run: `go test ./agent/... -run TestSerfConfig_ZeroValue -v`
Expected: PASS — this exercises compilation. The helper has no test of its own yet; subsequent tasks will exercise it.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): add serfConfigTestRepo helper for discovery tests"
```

---

## Task 19: DiscoverSerfConfig — all three tiers absent (§8.3 row 1)

**Files:**
- Modify: `agent/config.go`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_AllAbsent(t *testing.T) {
	env, _, cliPaths := serfConfigTestRepo(t, "", "", nil, nil)
	cfg, err := DiscoverSerfConfig(env, cliPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Marketplaces != nil || cfg.Hooks != nil || cfg.MCPServers != nil ||
		cfg.EnabledPlugins != nil || cfg.Sources != nil ||
		cfg.Permissions.Allow != nil {
		t.Fatalf("all absent should yield zero SerfConfig, got: %+v", cfg)
	}
}
```

- [ ] **Step 2: Run test to verify it passes against the stub**

Run: `go test ./agent/... -run TestDiscoverSerfConfig_AllAbsent -v`
Expected: PASS — the stub returns zero SerfConfig. This is the documented zero case. The next tasks will demand real behavior.

- [ ] **Step 3: Implement DiscoverSerfConfig minimally to satisfy this case**

Replace `DiscoverSerfConfig` in `agent/config.go`:

```go
func DiscoverSerfConfig(env ExecutionEnvironment, cliPaths []string) (SerfConfig, error) {
	var layers []SerfConfig

	// Tier 1: global.
	if gp := globalSerfConfigPath(); gp != "" {
		layer, err := LoadSerfConfigFile(gp)
		if err != nil {
			return SerfConfig{}, err
		}
		if len(layer.Sources) > 0 {
			for i := range layer.Sources {
				layer.Sources[i].Tier = TierGlobal
				layer.Sources[i].CLIIndex = -1
			}
			layers = append(layers, layer)
		}
	}

	// Tier 2: project.
	if env != nil {
		cwd := env.WorkingDirectory()
		root := gitRootOrEmpty(env, cwd)
		if root != "" {
			pp := filepath.Join(root, ".serf", "config.json")
			layer, err := LoadSerfConfigFile(pp)
			if err != nil {
				return SerfConfig{}, err
			}
			if len(layer.Sources) > 0 {
				for i := range layer.Sources {
					layer.Sources[i].Tier = TierProject
					layer.Sources[i].CLIIndex = -1
				}
				layers = append(layers, layer)
			}
		}
	}

	// Tier 3: --config files in CLI order. Missing CLI files ARE errors;
	// stat first so the error surfaces with the --config breadcrumb.
	for i, p := range cliPaths {
		if _, statErr := os.Stat(p); statErr != nil {
			return SerfConfig{}, fmt.Errorf("--config %s: %w", p, statErr)
		}
		layer, err := LoadSerfConfigFile(p)
		if err != nil {
			return SerfConfig{}, fmt.Errorf("--config %s: %w", p, err)
		}
		for j := range layer.Sources {
			layer.Sources[j].Tier = TierCLI
			layer.Sources[j].CLIIndex = i
		}
		layers = append(layers, layer)
	}

	return MergeSerfConfigs(layers...), nil
}
```

- [ ] **Step 4: Run test to verify it still passes**

Run: `go test ./agent/... -run TestDiscoverSerfConfig_AllAbsent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/config.go agent/config_test.go
git commit -m "feat(config): DiscoverSerfConfig three-tier skeleton"
```

---

## Task 20: DiscoverSerfConfig — only global / only project / only CLI (§8.3 rows 2, 3, 4)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_OnlyGlobal(t *testing.T) {
	body := `{"permissions":{"defaultMode":"default","allow":["G"]}}`
	env, _, cliPaths := serfConfigTestRepo(t, body, "", nil, nil)
	cfg, err := DiscoverSerfConfig(env, cliPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Permissions.DefaultMode != "default" || len(cfg.Permissions.Allow) != 1 || cfg.Permissions.Allow[0] != "G" {
		t.Errorf("Permissions = %+v", cfg.Permissions)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].Tier != TierGlobal {
		t.Errorf("Sources = %+v", cfg.Sources)
	}
}

func TestDiscoverSerfConfig_OnlyProject(t *testing.T) {
	body := `{"permissions":{"allow":["P"]}}`
	env, _, cliPaths := serfConfigTestRepo(t, "", body, nil, nil)
	cfg, err := DiscoverSerfConfig(env, cliPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Permissions.Allow) != 1 || cfg.Permissions.Allow[0] != "P" {
		t.Errorf("Allow = %v", cfg.Permissions.Allow)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].Tier != TierProject {
		t.Errorf("Sources = %+v", cfg.Sources)
	}
}

func TestDiscoverSerfConfig_OnlyCLI(t *testing.T) {
	body := `{"permissions":{"allow":["C"]}}`
	env, _, cliPaths := serfConfigTestRepo(t, "", "", []string{"one.json"}, map[string]string{"one.json": body})
	cfg, err := DiscoverSerfConfig(env, cliPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Permissions.Allow) != 1 || cfg.Permissions.Allow[0] != "C" {
		t.Errorf("Allow = %v", cfg.Permissions.Allow)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].Tier != TierCLI || cfg.Sources[0].CLIIndex != 0 {
		t.Errorf("Sources = %+v", cfg.Sources)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestDiscoverSerfConfig_Only" -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin discovery with each single tier present"
```

---

## Task 21: DiscoverSerfConfig — all three tiers concat (§8.3 rows 5, 6, 12)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_AllTiersConcat(t *testing.T) {
	global := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"g.sh"}]}]}}`
	project := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"p.sh"}]}]}}`
	cli0 := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"c0.sh"}]}]}}`
	cli1 := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"c1.sh"}]}]}}`

	env, _, cliPaths := serfConfigTestRepo(
		t, global, project,
		[]string{"a.json", "b.json"},
		map[string]string{"a.json": cli0, "b.json": cli1},
	)
	cfg, err := DiscoverSerfConfig(env, cliPaths)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(cfg.Hooks["PreToolUse"]); got != 4 {
		t.Fatalf("Hooks[PreToolUse] len = %d, want 4 (g, p, cli0, cli1)", got)
	}
	wantInOrder := []string{"g.sh", "p.sh", "c0.sh", "c1.sh"}
	for i, sub := range wantInOrder {
		if !strings.Contains(string(cfg.Hooks["PreToolUse"][i]), sub) {
			t.Errorf("Hooks[PreToolUse][%d] = %s, want substring %q", i, cfg.Hooks["PreToolUse"][i], sub)
		}
	}

	// Sources order: global, project, cli0, cli1.
	if len(cfg.Sources) != 4 {
		t.Fatalf("Sources len = %d, want 4: %+v", len(cfg.Sources), cfg.Sources)
	}
	wantTiers := []ConfigTier{TierGlobal, TierProject, TierCLI, TierCLI}
	for i, w := range wantTiers {
		if cfg.Sources[i].Tier != w {
			t.Errorf("Sources[%d].Tier = %d, want %d", i, cfg.Sources[i].Tier, w)
		}
	}
	if cfg.Sources[2].CLIIndex != 0 || cfg.Sources[3].CLIIndex != 1 {
		t.Errorf("CLIIndex = %d / %d, want 0 / 1", cfg.Sources[2].CLIIndex, cfg.Sources[3].CLIIndex)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/... -run TestDiscoverSerfConfig_AllTiersConcat -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin three-tier hook concat and Sources order"
```

---

## Task 22: DiscoverSerfConfig — project ignored when cwd not in a git repo (§8.3 row 7)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_ProjectSkippedOutsideGit(t *testing.T) {
	// Build a layout that has a .serf/config.json present but no git repo
	// around it. DiscoverSerfConfig must not pick it up via the gitRootOrEmpty
	// path.
	root := t.TempDir()

	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "serf"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	nonRepo := filepath.Join(root, "nonrepo")
	if err := os.MkdirAll(filepath.Join(nonRepo, ".serf"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonRepo, ".serf", "config.json"), []byte(`{"permissions":{"allow":["P"]}}`), 0644); err != nil {
		t.Fatal(err)
	}

	env := NewLocalExecutionEnvironment(nonRepo)
	cfg, err := DiscoverSerfConfig(env, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Permissions.Allow != nil {
		t.Errorf("Allow = %v, want nil (project tier should not load outside a git repo)", cfg.Permissions.Allow)
	}
	if len(cfg.Sources) != 0 {
		t.Errorf("Sources = %+v, want empty", cfg.Sources)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/... -run TestDiscoverSerfConfig_ProjectSkippedOutsideGit -v`
Expected: PASS — `gitRootOrEmpty` returns `""` for a non-repo dir, and DiscoverSerfConfig short-circuits.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin project-tier skip outside git repo"
```

---

## Task 23: DiscoverSerfConfig — malformed file at each tier aborts (§8.3 rows 8, 9, 10)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_MalformedGlobalAborts(t *testing.T) {
	env, _, cliPaths := serfConfigTestRepo(t, `{`, "", nil, nil)
	_, err := DiscoverSerfConfig(env, cliPaths)
	if err == nil {
		t.Fatal("expected error for malformed global config")
	}
	if !strings.Contains(err.Error(), "parsing serf config") || !strings.Contains(err.Error(), "/xdg/serf/config.json") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestDiscoverSerfConfig_MalformedProjectAborts(t *testing.T) {
	env, repo, cliPaths := serfConfigTestRepo(t, "", `{`, nil, nil)
	_, err := DiscoverSerfConfig(env, cliPaths)
	if err == nil {
		t.Fatal("expected error for malformed project config")
	}
	wantPath := filepath.Join(repo, ".serf", "config.json")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error = %q, want substring %q", err.Error(), wantPath)
	}
}

func TestDiscoverSerfConfig_MalformedCLIAbortsWithTierPrefix(t *testing.T) {
	env, _, cliPaths := serfConfigTestRepo(t, "", "",
		[]string{"bad.json"}, map[string]string{"bad.json": `{`},
	)
	_, err := DiscoverSerfConfig(env, cliPaths)
	if err == nil {
		t.Fatal("expected error for malformed CLI config")
	}
	if !strings.Contains(err.Error(), "--config "+cliPaths[0]) {
		t.Errorf("error = %q, want --config <path> prefix with %q", err.Error(), cliPaths[0])
	}
	if !strings.Contains(err.Error(), "parsing serf config") {
		t.Errorf("error = %q, want inner 'parsing serf config' message", err.Error())
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestDiscoverSerfConfig_Malformed" -v`
Expected: PASS for all three. Global and project surface the inner error untouched; CLI is wrapped with `--config <path>: …`.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin malformed-config errors at each tier"
```

---

## Task 24: DiscoverSerfConfig — missing --config file is an error (§8.3 row 11)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_MissingCLIIsError(t *testing.T) {
	env, _, _ := serfConfigTestRepo(t, "", "", nil, nil)
	missing := filepath.Join(t.TempDir(), "absent-config.json")
	_, err := DiscoverSerfConfig(env, []string{missing})
	if err == nil {
		t.Fatal("expected error for missing --config file")
	}
	if !strings.Contains(err.Error(), "--config "+missing) {
		t.Errorf("error = %q, want '--config %s'", err.Error(), missing)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/... -run TestDiscoverSerfConfig_MissingCLIIsError -v`
Expected: PASS — `os.Stat` returns `*PathError` for the absent path, which Task 19 wraps as `--config <path>: %w` before `LoadSerfConfigFile` is even called.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin missing-CLI-file as an explicit error"
```

---

## Task 25: DiscoverSerfConfig — project discovered via git root from subdir (§8.3 row 13)

**Files:**
- Modify: `agent/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/config_test.go`:

```go
func TestDiscoverSerfConfig_ProjectViaGitRootFromSubdir(t *testing.T) {
	body := `{"permissions":{"allow":["P"]}}`
	env, repo, _ := serfConfigTestRepo(t, "", body, nil, nil)

	// Reroot the env at <repo>/sub/dir, two levels below the repo root.
	subdir := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	local, ok := env.(*LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("env is %T, want *LocalExecutionEnvironment", env)
	}
	env = local.WithWorkingDirectory(subdir)

	cfg, err := DiscoverSerfConfig(env, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Permissions.Allow) != 1 || cfg.Permissions.Allow[0] != "P" {
		t.Errorf("Allow = %v, want [P] (project tier should resolve via git root)", cfg.Permissions.Allow)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].Tier != TierProject {
		t.Errorf("Sources = %+v", cfg.Sources)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/... -run TestDiscoverSerfConfig_ProjectViaGitRootFromSubdir -v`
Expected: PASS — `gitRootOrEmpty` walks up to the git root from any subdir.

- [ ] **Step 3: Commit**

```bash
git add agent/config_test.go
git commit -m "test(config): pin project tier resolution from a subdir"
```

---

## Task 26: Shared fixtures — `agent/testdata/config/`

**Files:**
- Create: `agent/testdata/config/full.json`
- Create: `agent/testdata/config/hooks_only.json`
- Create: `agent/testdata/config/permissions_only.json`
- Create: `agent/testdata/config/malformed.json`
- Modify: `agent/config_test.go`

- [ ] **Step 1: Create the fixture files**

`agent/testdata/config/full.json`:

```json
{
  "marketplaces": {
    "anthropics": { "source": { "type": "github", "repo": "anthropics/claude-code-plugins" }, "autoUpdate": false }
  },
  "enabledPlugins": {
    "code-review@anthropics": true,
    "linter@anthropics":      { "version": "1.2.3" }
  },
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "guard.sh" } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "summarize.sh" } ] }
    ]
  },
  "mcpServers": {
    "github": { "command": "gh-mcp", "args": ["--token", "abc"] }
  },
  "permissions": {
    "allow":       [ "Bash(ls:*)", "Skill(*)" ],
    "deny":        [ "Bash(rm:-rf*)" ],
    "defaultMode": "default"
  }
}
```

`agent/testdata/config/hooks_only.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "guard.sh" } ] }
    ]
  }
}
```

`agent/testdata/config/permissions_only.json`:

```json
{
  "permissions": {
    "allow":       [ "Bash(ls:*)" ],
    "defaultMode": "default"
  }
}
```

`agent/testdata/config/malformed.json`:

```
{
```

(Literal single `{` byte, no closing brace — used to trigger the parse-error path.)

- [ ] **Step 2: Write a test that exercises the §3 full example via the fixture**

Append to `agent/config_test.go`:

```go
func TestLoadSerfConfigFile_FullExampleFixture(t *testing.T) {
	cfg, err := LoadSerfConfigFile("testdata/config/full.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Marketplaces) != 1 {
		t.Errorf("Marketplaces len = %d", len(cfg.Marketplaces))
	}
	if len(cfg.EnabledPlugins) != 2 {
		t.Errorf("EnabledPlugins len = %d", len(cfg.EnabledPlugins))
	}
	if len(cfg.Hooks["PreToolUse"]) != 1 || len(cfg.Hooks["Stop"]) != 1 {
		t.Errorf("Hooks = %+v", cfg.Hooks)
	}
	if _, ok := cfg.MCPServers["github"]; !ok {
		t.Errorf("MCPServers missing github: %v", cfg.MCPServers)
	}
	if len(cfg.Permissions.Allow) != 2 || cfg.Permissions.DefaultMode != "default" {
		t.Errorf("Permissions = %+v", cfg.Permissions)
	}
	if len(cfg.Sources) != 1 || !strings.HasSuffix(cfg.Sources[0].Path, "testdata/config/full.json") {
		t.Errorf("Sources = %+v", cfg.Sources)
	}
}

func TestLoadSerfConfigFile_MalformedFixture(t *testing.T) {
	_, err := LoadSerfConfigFile("testdata/config/malformed.json")
	if err == nil {
		t.Fatal("malformed fixture should error")
	}
	if !strings.Contains(err.Error(), "parsing serf config") {
		t.Errorf("error = %q", err.Error())
	}
}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./agent/... -run "TestLoadSerfConfigFile_(FullExample|Malformed)Fixture" -v`
Expected: PASS for both.

- [ ] **Step 4: Commit**

```bash
git add agent/testdata/config/ agent/config_test.go
git commit -m "test(config): add shared JSON fixtures and full-example test"
```

---

## Task 27: Coverage sweep — ensure `go test ./agent/... -run SerfConfig` is green and inspect for gaps

**Files:** (no edits expected; this is a verification task)

- [ ] **Step 1: Run the full SerfConfig suite**

Run: `go test ./agent/... -run "SerfConfig|MergeSerfConfigs|DiscoverSerfConfig|LoadSerfConfigFile|GlobalSerfConfigPath" -v`
Expected: PASS for every test added in Tasks 1–26.

- [ ] **Step 2: Run the full agent test suite to catch regressions**

Run: `go test ./agent/...`
Expected: PASS (no other agent tests should be affected by additive changes).

- [ ] **Step 3: Tally exported-function coverage**

Verify every exported symbol from §2 has at least one direct test:

| Symbol | Test(s) |
|---|---|
| `SerfConfig` | `TestSerfConfig_ZeroValue`, every `TestLoadSerfConfigFile_*` |
| `PermissionsConfig` | `TestLoadSerfConfigFile_Permissions*`, `TestMergeSerfConfigs_Permissions*` |
| `ConfigSource` | `TestLoadSerfConfigFile_EmptyObject`, `TestMergeSerfConfigs_SourcesConcatInOrder` |
| `ConfigTier` constants | `TestConfigTier_Constants` |
| `LoadSerfConfigFile` | every `TestLoadSerfConfigFile_*` |
| `MergeSerfConfigs` | every `TestMergeSerfConfigs_*` |
| `DiscoverSerfConfig` | every `TestDiscoverSerfConfig_*` |
| `globalSerfConfigPath` (unexported but spec-listed) | `TestGlobalSerfConfigPath_*` |

If any row is missing, add a one-line test before continuing.

- [ ] **Step 4: Commit any small additions made under Step 3**

```bash
git status
# If clean: skip commit. Otherwise:
git add agent/config_test.go
git commit -m "test(config): close coverage gaps in SP1 suite"
```

---

## Task 28: Final verification

**Files:** (no edits)

- [ ] **Step 1: Run vet and tests one last time**

Run: `go vet ./agent/... && go test ./agent/...`
Expected: PASS.

- [ ] **Step 2: Verify no other package was touched**

Run: `git diff --name-only main..HEAD -- ':!docs'`
Expected: only `agent/config.go`, `agent/config_test.go`, and files under `agent/testdata/config/` appear. SP1 must not modify any existing serf file.

- [ ] **Step 3: Confirm**

If both checks pass, SP1 is complete and ready for SP2/SP5/SP6/SP7 to start in parallel and SP8 to wire `DiscoverSerfConfig` into the CLI entry points.

---

## Self-Review Notes

**Spec coverage (against `2026-05-14-claude-code-compat-sp1-config-loader-design.md`):**

| Spec section | Covered by |
|---|---|
| §2 Public API surface | Tasks 1, 5–7, 11, 17, 19 |
| §3 File-Format Details (happy paths) | Tasks 3, 5, 6, 7, 26 |
| §3 Validation table (object types, array types, etc.) | Tasks 3, 5, 6, 8, 9 |
| §4 Hooks concat | Tasks 11, 12, 21 |
| §4 mcpServers / marketplaces / enabledPlugins replace-by-key | Tasks 11, 13 |
| §4 permissions.allow / deny concat | Tasks 11, 14 |
| §4 permissions.defaultMode scalar overwrite (with empty-preserves) | Tasks 11, 15 |
| §4 Sources concatenate in order | Tasks 11, 16, 21 |
| §5 Validation behavior (error texts) | Tasks 3–10 |
| §6 Error Contracts (absent vs missing-CLI vs malformed) | Tasks 2, 4, 10, 23, 24 |
| §7 Package and File Layout | Task 1 (creates files), Task 26 (fixtures) |
| §8.1 LoadSerfConfigFile (22 rows) | Tasks 2, 3, 5, 6, 7, 8, 9, 10, 26 |
| §8.2 MergeSerfConfigs (14 rows) | Tasks 11, 12, 13, 14, 15, 16 |
| §8.3 DiscoverSerfConfig (13 rows) | Tasks 19, 20, 21, 22, 23, 24, 25 |
| §8.4 Fixtures | Task 26 |
| §8.5 Coverage gate | Task 27 |
| §9.1 Hook ordering (low→high) | Task 21 (asserts global, project, cli0, cli1 order) |

**Placeholder scan:** No "TBD", "implement similar to X", or "handle edge cases" steps. Every code step shows the full updated function or block.

**Type consistency check:**
- `SerfConfig.Hooks` is `map[string][]json.RawMessage` everywhere it appears.
- `PermissionsConfig.DefaultMode` is `string` everywhere.
- `ConfigSource.CLIIndex` is `int` with `-1` for non-CLI tiers, consistent in Tasks 3, 11, 16, 19, 21.
- `LoadSerfConfigFile(path string) (SerfConfig, error)` signature is stable from Task 1 onward.
- `MergeSerfConfigs(layers ...SerfConfig) SerfConfig` signature is stable from Task 1 onward.
- `DiscoverSerfConfig(env ExecutionEnvironment, cliPaths []string) (SerfConfig, error)` signature is stable from Task 1 onward.

**Known gaps / deferred items (documented intentionally):**
- Validation row 22 ("All five top-level fields error message names the path") is satisfied by every existing error test asserting `strings.Contains(err.Error(), path)`; no separate task is needed.
- Hook ordering across plugin-provided hooks (§9.1 last paragraph: "Plugin-provided hooks append at the very end") is SP5's responsibility. SP1 only provides the merged config-tier ordering; the plan stops at that boundary, as the sub-spec requires.
- All other open questions in §9.2 are explicitly out of SP1 scope and remain `json.RawMessage` until their owning sub-projects parse them.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-14-claude-code-compat-sp1-config-loader-plan.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

Which approach?
