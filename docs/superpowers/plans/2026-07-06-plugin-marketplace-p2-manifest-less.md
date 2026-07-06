# Plugin Marketplace Improvements — Part 2: Manifest-less Plugin Support

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a plugin's source directory has no `plugin.json` (neither `.claude-plugin/` nor
`.codex-plugin/`), install it and make its components actually work — especially a declared MCP
server — by honoring the marketplace catalog entry's embedded manifest fields (`commands`,
`agents`, `hooks`, `mcpServers`) as a fallback manifest, instead of hard-failing with a misleading
error. A plugin that ships its own `plugin.json` is completely unchanged: the entry is only ever a
fallback, never a merge. When neither a real manifest nor any usable entry field exists, fail with
one honest, plugin-named error instead of the current message (which names the last-tried
`.codex-plugin` path) and its side effect (the install path silently deletes the freshly fetched
cache dir on that misleading error).

**Architecture:** All of Part 2 lives in the root module's `internal/plugins` package — the plugin
lifecycle manager that already parses `marketplace.json` (`catalog.go`) and drives
install/upgrade (`install.go`). `agent/plugin` (the loader `Load` reads) is **not modified**: its
`Manifest` struct already declares `Commands`/`Agents`/`Hooks`/`MCPServers` as `json.RawMessage`
with the exact same JSON field names the Claude Code marketplace-entry schema uses for the same
components, so a catalog entry's fields can be dropped straight into a `plugin.Manifest` value
with no shape translation. The fallback is realized by **synthesizing a real
`.claude-plugin/plugin.json` file into the plugin's cache directory** at install/upgrade time,
before validation — not by threading the catalog entry into `Load` itself. This keeps `Load`'s
contract exactly as it is today (dir in, `Instance` out) and means every other consumer that calls
`Load`/`validatePluginDir` on that directory afterward — this same install's own validation, a
later session's `EnabledPluginDirs()` load, `serf plugin list`'s `Broken` check, `serf-doctor` —
sees an ordinary on-disk manifest and needs no special-casing. The synthesis only ever writes into
a cache directory serf materialized itself (a `staged` fetch); a directory-source plugin
referenced in place (a local-dev/test convenience) is never written into, since it is a directory
serf does not own.

**Tech Stack:** Go, root module only (`internal/plugins`); read-only import of the already-built
`agent/plugin` package (`agentplugin.Manifest`, `agentplugin.Load`) — no `agent` module changes.
Real git fixtures for install/upgrade tests (`makeGitRepo`, matching the package's existing
test style), no mocks.

## Global Constraints

- **Only `internal/plugins` changes.** `agent/plugin/plugin.go` is read but not modified — its
  `Manifest` struct (lines 22-35) already has every field this plan needs
  (`Commands`/`Agents`/`Hooks`/`MCPServers`, all `json.RawMessage`), and `Load` (lines 203-263,
  drifted from the design spec's `~203-232` estimate — the extra lines are the already-existing
  hooks/MCP discovery calls the spec's estimate predated, not anything this plan adds) stays
  dir-only per the Architecture note above.
- **No appwire/UI changes.** Part 2 is pure backend; nothing in `cmd/serf-hub` or `appwire`
  changes. (Part 1 — the browse tree — is a separate, independent plan.)
- **`strict` semantics — corrected from the design spec.** The design spec
  (`docs/superpowers/specs/2026-07-06-plugin-marketplace-improvements-design.md`) frames
  `"strict": true` as the trigger for "marketplace entry is the manifest." **This is backwards
  from Claude Code's actual documented behavior**, confirmed against
  `https://code.claude.com/docs/en/plugin-marketplaces#strict-mode` (fetched during planning,
  2026-07-06): `strict: true` (the default) means **`plugin.json` is the authority** and the entry
  only supplements it; `strict: false` means **the entry is the plugin's entire definition, and a
  plugin.json that also declares components is a conflict that fails to load.** Claude's own
  worked example for a manifest-less MCP plugin (an inline `mcpServers` entry) explicitly sets
  `"strict": false` and says so: *"since this is set to false, the plugin doesn't need its own
  plugin.json."* Cross-checked against the real motivating example: `superpowers-marketplace`'s
  live `private-journal-mcp` entry (`https://github.com/obra/superpowers-marketplace`) is
  `"strict": true` with no embedded manifest fields at all, and the `private-journal-mcp` repo
  itself has no `.claude-plugin/` — so today it has nothing for either Claude Code or serf to fall
  back to; fixing serf's parsing doesn't retroactively fix that specific entry's JSON (a separate,
  out-of-band edit to the marketplace's `marketplace.json` would be needed to actually exercise this
  end to end against the real marketplace). **Given that, and since the v1 scope is deliberately
  narrowed to the manifest-absent case only (no `strict:false`-conflicts-with-existing-plugin.json
  detection, no `strict:true`-supplement-merge — both require a plugin.json to exist, which is
  moot here), this plan's trigger condition is simply "no plugin.json exists on disk," independent
  of the entry's `Strict` value.** `Strict` is still parsed and round-tripped (schema
  completeness, future strict:false-merge work) but is not read by the fallback logic. This
  correction is called out again inline where `CatalogPlugin.Strict` is added (Task 1) and where
  `ensureManifestFallback` is implemented (Task 4).
- **`skills` is parsed but not honored.** `agent/plugin.Manifest` has no `Skills` override field —
  a plugin's `skills/` directory is always scanned by `discoverPluginSkills` regardless of manifest
  contents, with no way to point it at a custom path. So a marketplace entry's `"skills": [...]`
  (Claude Code's custom-skill-path field) is captured on `CatalogPlugin` for schema completeness
  but is **not** plumbed into the synthesized manifest: only the plugin's default `skills/`
  directory, if the source happens to ship one, is picked up. This is an accepted v1 gap (adding a
  `Manifest.Skills` override is a bigger, separate change touching `agent/plugin`, out of scope
  here) and is pinned by a falsifiable test (Task 4) so it doesn't silently regress or get assumed
  to work.
- **No `// serf:naming-ignore:` comments needed for the new fields — verified, not assumed.**
  `cmd/serf-namingcheck/main.go` (read in full during planning) enforces snake_case JSON tags, but
  `isUpstreamCamelKey` (line ~353-359) already **hardcodes `"mcpServers"` and `"enabledPlugins"`**
  as globally-exempt keys — matching `agent/plugin.Manifest.MCPServers`'s own tag, which likewise
  carries no ignore comment today. The other five new fields (`commands`, `agents`, `hooks`,
  `skills`, `strict`) are single all-lowercase words, which satisfy both the snake_case and
  camelCase regexes trivially (`checkJSONTag`'s own comment: *"Pure-lowercase single-word keys...
  pass everywhere"*). So none of Task 1's six new struct fields need an ignore marker — this
  drifts from the task briefing's assumption that camelCase interop fields would need one;
  `go run ./cmd/serf-namingcheck` (or `make lint-naming`) must still be run to confirm.
- **Directory-source (staged=false) plugins are not written into.** `Manager.stagePlugin`
  (`install.go` ~lines 39-57) returns `staged=false` only when the *marketplace itself* is a bare
  local directory and the plugin resolves to a `Rel`/`SourceDirectory` path in place — a
  local-dev/test convenience, not the git/url/github cache path the real motivating case (an
  npm/MCP package plugin) always takes. `ensureManifestFallback` never writes a file when
  `staged=false`; a manifest-less directory-source plugin keeps failing to install, but with the
  corrected error text instead of the old misleading one.
- Per-repo `GO_MODULES` (root, `agent`, `llm`, `auth`, `envvars`, `fuzz`, `invariant`): this plan
  touches only the **root** module (`internal/plugins`). Run
  `go test ./internal/plugins/... -run '<Name>' -count=1` from the repo root;
  `golangci-lint run ./internal/plugins/...` for the package; `go run ./cmd/serf-namingcheck` (no
  `-root` flag needed — it defaults to `.`, the repo root) for the full naming sweep at the end.
- TDD red-first; test output pristine (a skipped-when-no-`git` test via `gitAvailable()`, matching
  the package's existing convention, is not a pristine-output violation).
- Never `git add -A`; stage only the exact paths listed in each task's commit step (after a
  `git status`).

---

## File Structure

New/changed responsibilities, in dependency order:

- `internal/plugins/catalog.go` — `CatalogPlugin` gains `Commands`/`Agents`/`Hooks`/`MCPServers`/
  `Skills` (`json.RawMessage`) and `Strict` (`*bool`), plus a `HasManifestFields() bool` method.
  `ParseCatalog`'s existing per-entry `json.Unmarshal` (already there for `SkippedPlugins`
  fail-soft parsing, ~lines 78-84) picks the new fields up with no other change.
- `internal/plugins/manifest_fallback.go` — **new file.** `hasPluginManifest(dir string) bool`
  (mirrors `pluginManifestVersion`'s two-flavor directory list) and
  `ensureManifestFallback(dir string, staged bool, cp CatalogPlugin) error` (the fallback: no-op
  when a manifest exists, a clear error when neither a manifest nor usable entry fields exist,
  and — only for a cache dir serf owns — synthesizing `.claude-plugin/plugin.json` from
  `agentplugin.Manifest{Name: cp.Name, Description: cp.Description, Commands: cp.Commands,
  Agents: cp.Agents, Hooks: cp.Hooks, MCPServers: cp.MCPServers}` otherwise).
- `internal/plugins/install.go` — `Install` (~lines 80-136) and `upgradeLocked` (~lines 175-232)
  each call `ensureManifestFallback` right after committing the plugin into its final directory
  and before `validatePluginDir`, reusing each function's existing cache-dir-prefix cleanup-on-error
  branch (so the "delete the cache dir on a bad plugin" behavior is unchanged — only the error text
  is now honest, per the design spec's callout of this exact side effect).

---

## Task 1 — `CatalogPlugin` captures the marketplace-entry manifest fields

**Files:**
- Modify: `internal/plugins/catalog.go:17-24` (`CatalogPlugin`)
- Test: `internal/plugins/catalog_test.go`

**Interfaces:**
- Produces: `CatalogPlugin.Commands/Agents/Hooks/MCPServers/Skills json.RawMessage`,
  `CatalogPlugin.Strict *bool` — consumed by Task 2 (`HasManifestFields`) and Task 4
  (`ensureManifestFallback`'s manifest synthesis).

- [ ] **Failing test** — add to `internal/plugins/catalog_test.go` (add `"encoding/json"` to its
  import block):
```go
func TestCatalogPlugin_ParsesMarketplaceEntryManifestFields(t *testing.T) {
	root := t.TempDir()
	mj := `{
	  "name":"acme","owner":{"name":"o"},
	  "plugins":[
	    {
	      "name":"private-journal-mcp",
	      "description":"Journal MCP server",
	      "source":{"source":"url","url":"https://example.com/x.git"},
	      "strict": false,
	      "mcpServers": {
	        "private-journal": {"command":"npx","args":["-y","private-journal-mcp"]}
	      },
	      "commands": ["./commands/"],
	      "agents": ["./agents/reviewer.md"],
	      "hooks": {"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"echo hi"}]}]},
	      "skills": ["./extra-skills/"]
	    }
	  ]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if len(cat.Plugins) != 1 {
		t.Fatalf("Plugins = %+v, want 1", cat.Plugins)
	}
	p := cat.Plugins[0]
	if p.Strict == nil || *p.Strict != false {
		t.Errorf("Strict = %v, want a pointer to false", p.Strict)
	}
	var mcp map[string]json.RawMessage
	if err := json.Unmarshal(p.MCPServers, &mcp); err != nil || len(mcp) != 1 {
		t.Fatalf("MCPServers = %s, err %v", p.MCPServers, err)
	}
	if _, ok := mcp["private-journal"]; !ok {
		t.Errorf("MCPServers missing private-journal entry: %s", p.MCPServers)
	}
	var commands []string
	if err := json.Unmarshal(p.Commands, &commands); err != nil || len(commands) != 1 {
		t.Fatalf("Commands = %s, err %v", p.Commands, err)
	}
	var agents []string
	if err := json.Unmarshal(p.Agents, &agents); err != nil || len(agents) != 1 {
		t.Fatalf("Agents = %s, err %v", p.Agents, err)
	}
	if len(p.Hooks) == 0 {
		t.Errorf("Hooks not captured")
	}
	if len(p.Skills) == 0 {
		t.Errorf("Skills not captured")
	}
}

// TestCatalogPlugin_ManifestFieldsOmittedWhenAbsent guards an ordinary plugin
// entry (the common case, a plugin with its own plugin.json): none of the
// new fallback fields should be populated just because the struct has them.
func TestCatalogPlugin_ManifestFieldsOmittedWhenAbsent(t *testing.T) {
	root := t.TempDir()
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	p := cat.Plugins[0]
	if p.Strict != nil || p.Commands != nil || p.Agents != nil || p.Hooks != nil || p.MCPServers != nil || p.Skills != nil {
		t.Errorf("expected all manifest-fallback fields nil/absent, got %+v", p)
	}
}
```
Run: `go test ./internal/plugins/... -run 'TestCatalogPlugin_' -count=1` → expect FAIL (compile
error: `Strict`/`Commands`/`Agents`/`Hooks`/`MCPServers`/`Skills` undefined on `CatalogPlugin`).

- [ ] **Implement** — in `internal/plugins/catalog.go`, change:
```go
type CatalogPlugin struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Category    string       `json:"category,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Author      CatalogOwner `json:"author,omitempty"`
	Source      Source       `json:"source"`
}
```
to:
```go
type CatalogPlugin struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Category    string       `json:"category,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Author      CatalogOwner `json:"author,omitempty"`
	Source      Source       `json:"source"`

	// The following mirror Claude Code's marketplace-entry manifest fields
	// (https://code.claude.com/docs/en/plugin-marketplaces#plugin-entries):
	// same names and JSON shapes as agent/plugin.Manifest's same-named
	// fields, so an entry can be dropped straight into a Manifest value (see
	// ensureManifestFallback, manifest_fallback.go). They are used only as a
	// fallback when the plugin's own source has no plugin.json; a plugin
	// that ships its own manifest is unchanged and these are ignored.
	Commands   json.RawMessage `json:"commands,omitempty"`
	Agents     json.RawMessage `json:"agents,omitempty"`
	Hooks      json.RawMessage `json:"hooks,omitempty"`
	MCPServers json.RawMessage `json:"mcpServers,omitempty"`
	// Skills is parsed for schema completeness but NOT currently honored:
	// agent/plugin.Manifest has no Skills override field (a plugin's
	// skills/ directory is always scanned by default, manifest or not), so
	// a marketplace entry's custom skill paths are not applied by the v1
	// fallback — only the plugin's own default skills/ directory, if it has
	// one, is picked up. See the plan's Global Constraints for why.
	Skills json.RawMessage `json:"skills,omitempty"`
	// Strict mirrors Claude Code's `strict` marketplace-entry field
	// (https://code.claude.com/docs/en/plugin-marketplaces#strict-mode):
	// default true means plugin.json is the authority and the entry only
	// supplements it; false means the entry is the plugin's entire
	// definition (and a co-existing plugin.json's components would
	// conflict). v1's fallback triggers purely on "no plugin.json exists"
	// and does not read Strict — see the plan's Global Constraints for the
	// full rationale. Captured here for round-trip and future
	// strict:false-conflict/merge work.
	Strict *bool `json:"strict,omitempty"`
}
```
(No `// serf:naming-ignore:` needed on any of the six — see Global Constraints.)

- [ ] **Run** `go test ./internal/plugins/... -run 'TestCatalogPlugin_' -count=1` → PASS.
- [ ] **Run** `go test ./internal/plugins/... -count=1` (package-wide, no regression) and
  `golangci-lint run ./internal/plugins/...`.
- [ ] **Commit** — `git add internal/plugins/catalog.go internal/plugins/catalog_test.go` →
  `feat(plugins): CatalogPlugin captures marketplace-entry manifest fields`.

## Task 2 — `CatalogPlugin.HasManifestFields()`

**Files:**
- Modify: `internal/plugins/catalog.go` (add method near `CatalogPlugin`)
- Test: `internal/plugins/catalog_test.go`

**Interfaces:**
- Consumes: the six fields from Task 1.
- Produces: `CatalogPlugin.HasManifestFields() bool`, consumed by Task 4
  (`ensureManifestFallback`'s "nothing usable" branch).

- [ ] **Failing test** — add to `internal/plugins/catalog_test.go`:
```go
func TestCatalogPlugin_HasManifestFields(t *testing.T) {
	cases := []struct {
		name string
		cp   CatalogPlugin
		want bool
	}{
		{"nothing set", CatalogPlugin{Name: "x"}, false},
		{"only skills set (excluded — see plan)", CatalogPlugin{Name: "x", Skills: json.RawMessage(`["./s"]`)}, false},
		{"only strict set", CatalogPlugin{Name: "x", Strict: boolPtr(false)}, false},
		{"mcpServers set", CatalogPlugin{Name: "x", MCPServers: json.RawMessage(`{}`)}, true},
		{"commands set", CatalogPlugin{Name: "x", Commands: json.RawMessage(`[]`)}, true},
		{"agents set", CatalogPlugin{Name: "x", Agents: json.RawMessage(`[]`)}, true},
		{"hooks set", CatalogPlugin{Name: "x", Hooks: json.RawMessage(`{}`)}, true},
	}
	for _, c := range cases {
		if got := c.cp.HasManifestFields(); got != c.want {
			t.Errorf("%s: HasManifestFields() = %v, want %v", c.name, got, c.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
```
Run: `go test ./internal/plugins/... -run 'TestCatalogPlugin_HasManifestFields' -count=1` → expect
FAIL (compile error: `HasManifestFields` undefined).

- [ ] **Implement** — in `internal/plugins/catalog.go`, add after the `CatalogPlugin` struct:
```go
// HasManifestFields reports whether the marketplace entry declares at least
// one manifest-fallback component (commands/agents/hooks/mcpServers) —
// ensureManifestFallback's signal for whether a manifest-less plugin has
// anything usable to synthesize a plugin.json from. Skills is deliberately
// excluded: the fallback does not honor it (see the Skills field's doc), so
// a skills-only entry has nothing this mechanism can act on.
func (cp CatalogPlugin) HasManifestFields() bool {
	return len(cp.Commands) > 0 || len(cp.Agents) > 0 || len(cp.Hooks) > 0 || len(cp.MCPServers) > 0
}
```

- [ ] **Run** `go test ./internal/plugins/... -run 'TestCatalogPlugin_' -count=1` → PASS.
- [ ] **Run** `go test ./internal/plugins/... -count=1` and `golangci-lint run ./internal/plugins/...`.
- [ ] **Commit** — `git add internal/plugins/catalog.go internal/plugins/catalog_test.go` →
  `feat(plugins): CatalogPlugin.HasManifestFields`.

## Task 3 — `hasPluginManifest`: does a plugin dir already have a manifest?

**Files:**
- New: `internal/plugins/manifest_fallback.go`
- Test: `internal/plugins/manifest_fallback_test.go`

**Interfaces:**
- Consumes: nothing new (pure filesystem check).
- Produces: `hasPluginManifest(dir string) bool`, consumed by Task 4 (`ensureManifestFallback`'s
  no-op branch).

- [ ] **Failing test** — create `internal/plugins/manifest_fallback_test.go`:
```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasPluginManifest(t *testing.T) {
	withClaudeManifest := filepath.Join(t.TempDir(), "has")
	writePlugin(t, withClaudeManifest, "widget", nil)
	if !hasPluginManifest(withClaudeManifest) {
		t.Error("hasPluginManifest = false, want true for a dir with .claude-plugin/plugin.json")
	}

	withCodexManifest := filepath.Join(t.TempDir(), "codex")
	os.MkdirAll(filepath.Join(withCodexManifest, ".codex-plugin"), 0o755)
	os.WriteFile(filepath.Join(withCodexManifest, ".codex-plugin", "plugin.json"), []byte(`{"name":"widget"}`), 0o644)
	if !hasPluginManifest(withCodexManifest) {
		t.Error("hasPluginManifest = false, want true for a .codex-plugin manifest")
	}

	bare := t.TempDir()
	if hasPluginManifest(bare) {
		t.Error("hasPluginManifest = true, want false for a bare directory")
	}
}
```
Run: `go test ./internal/plugins/... -run 'TestHasPluginManifest' -count=1` → expect FAIL (compile
error: `hasPluginManifest` undefined; `manifest_fallback.go` doesn't exist yet).

- [ ] **Implement** — create `internal/plugins/manifest_fallback.go`:
```go
package plugins

import (
	"os"
	"path/filepath"
)

// hasPluginManifest reports whether dir already has a plugin.json under
// either recognized manifest directory — the same two paths
// agent/plugin.Load tries (.claude-plugin/ first, .codex-plugin/ as
// fallback). Mirrors pluginManifestVersion's directory list (validate.go) so
// this stays in lock-step with Load's own fallback order.
func hasPluginManifest(dir string) bool {
	for _, mf := range []string{".claude-plugin", ".codex-plugin"} {
		if _, err := os.Stat(filepath.Join(dir, mf, "plugin.json")); err == nil {
			return true
		}
	}
	return false
}
```

- [ ] **Run** `go test ./internal/plugins/... -run 'TestHasPluginManifest' -count=1` → PASS.
- [ ] **Run** `go test ./internal/plugins/... -count=1` and `golangci-lint run ./internal/plugins/...`.
- [ ] **Commit** — `git add internal/plugins/manifest_fallback.go internal/plugins/manifest_fallback_test.go` →
  `feat(plugins): hasPluginManifest dir check`.

## Task 4 — `ensureManifestFallback`: synthesize, or fail clearly

**Files:**
- Modify: `internal/plugins/manifest_fallback.go`
- Test: `internal/plugins/manifest_fallback_test.go`

**Interfaces:**
- Consumes: `hasPluginManifest` (Task 3), `CatalogPlugin.HasManifestFields` (Task 2),
  `agentplugin.Manifest` (`agent/plugin/plugin.go:22-35`, read-only).
- Produces: `ensureManifestFallback(dir string, staged bool, cp CatalogPlugin) error`, consumed by
  Task 5 (`Install`) and Task 6 (`upgradeLocked`).

- [ ] **Failing test** — add to `internal/plugins/manifest_fallback_test.go` (add
  `"encoding/json"`, `"strings"`, and `agentplugin "primeradiant.com/serf/agent/plugin"` to its
  import block):
```go
func TestEnsureManifestFallback_ExistingManifestIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil)
	// The entry ALSO declares an MCP server — it must be ignored, since dir
	// already has its own plugin.json (Part 2 is fallback-only, never merge).
	cp := CatalogPlugin{Name: "widget", MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`)}
	if err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback on a plugin with its own manifest: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none — entry must be ignored when plugin.json exists", inst.MCPConfigs)
	}
}

func TestEnsureManifestFallback_NoManifestNoFields_ClearError(t *testing.T) {
	dir := t.TempDir() // bare: no plugin.json, no components anywhere
	cp := CatalogPlugin{Name: "bare-plugin"}
	err := ensureManifestFallback(dir, true, cp)
	if err == nil {
		t.Fatal("expected an error for a manifest-less plugin with no usable entry fields")
	}
	if strings.Contains(err.Error(), ".codex-plugin") {
		t.Errorf("error must not name the misleading .codex-plugin path, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bare-plugin") {
		t.Errorf("error should name the plugin, got: %v", err)
	}
}

func TestEnsureManifestFallback_NotStaged_ClearError(t *testing.T) {
	dir := t.TempDir() // stands in for a directory-source plugin referenced in place
	cp := CatalogPlugin{Name: "dev-plugin", MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`)}
	err := ensureManifestFallback(dir, false, cp)
	if err == nil {
		t.Fatal("expected an error for a not-staged (directory-source) manifest-less plugin")
	}
	if strings.Contains(err.Error(), ".codex-plugin") {
		t.Errorf("error must not name the misleading .codex-plugin path, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); statErr == nil {
		t.Fatal("must not write a manifest into a directory-source plugin serf does not own")
	}
}

func TestEnsureManifestFallback_SynthesizesManifest_MCPServerRegisters(t *testing.T) {
	dir := t.TempDir() // a cache-dir stand-in: bare, no manifest
	cp := CatalogPlugin{
		Name:        "private-journal-mcp",
		Description: "Journal MCP server",
		MCPServers:  json.RawMessage(`{"private-journal":{"command":"npx","args":["-y","private-journal-mcp"]}}`),
	}
	if err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load after synthesis: %v", err)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_private-journal-mcp_private-journal" {
		t.Fatalf("MCPConfigs = %+v, want one entry named plugin_private-journal-mcp_private-journal", inst.MCPConfigs)
	}
}

// TestEnsureManifestFallback_SkillsFieldNotHonored pins the accepted v1 gap
// documented on CatalogPlugin.Skills: a custom skills path declared in the
// entry is NOT picked up, only the plugin's own default skills/ directory.
func TestEnsureManifestFallback_SkillsFieldNotHonored(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "default-skill"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "default-skill", "SKILL.md"),
		[]byte("---\nname: default-skill\ndescription: d\n---\nbody"), 0o644)
	os.MkdirAll(filepath.Join(dir, "extra-skills", "extra-skill"), 0o755)
	os.WriteFile(filepath.Join(dir, "extra-skills", "extra-skill", "SKILL.md"),
		[]byte("---\nname: extra-skill\ndescription: d\n---\nbody"), 0o644)

	cp := CatalogPlugin{
		Name:       "skills-plugin",
		MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`), // needs >=1 usable field to trigger synthesis
		Skills:     json.RawMessage(`["./extra-skills/"]`),
	}
	if err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := inst.Skills["skills-plugin:default-skill"]; !ok {
		t.Errorf("default skills/ dir should still load: %+v", inst.Skills)
	}
	if _, ok := inst.Skills["skills-plugin:extra-skill"]; ok {
		t.Errorf("entry's custom skills path must NOT be honored (documented v1 gap), got: %+v", inst.Skills)
	}
}
```
Run: `go test ./internal/plugins/... -run 'TestEnsureManifestFallback' -count=1` → expect FAIL
(compile error: `ensureManifestFallback` undefined).

- [ ] **Implement** — append to `internal/plugins/manifest_fallback.go` (add `"encoding/json"`,
  `"fmt"`, and `agentplugin "primeradiant.com/serf/agent/plugin"` to its import block):
```go
// ensureManifestFallback makes a manifest-less plugin installable by
// honoring its marketplace entry as a fallback manifest — the fix for
// serf's real root cause (a bare MCP/npm-package plugin with no
// .claude-plugin/plugin.json, e.g. private-journal-mcp in
// superpowers-marketplace, had its entry's manifest-shaped fields silently
// dropped and could not install). See the plan's Global Constraints for why
// this triggers on manifest-ABSENCE alone rather than gating on the entry's
// Strict field.
//
// If dir already has a plugin.json (either flavor), this is a no-op — an
// existing manifest is always authoritative; the entry's fields are only
// ever a fallback, never a merge (strict:false's
// entry-supplements/conflicts-with-an-existing-plugin.json behavior is out
// of scope for v1).
//
// If dir has no plugin.json:
//   - and the entry declares no usable component (cp.HasManifestFields()),
//     this returns a clear, honest, plugin-named error — no misleading
//     .codex-plugin path, no Load() call at all.
//   - and the entry does declare components, and dir is a cache directory
//     serf materialized (staged), it writes a synthesized
//     .claude-plugin/plugin.json built from the entry's fields, so every
//     later Load() of dir (this install's own validation, a future
//     session's EnabledPluginDirs(), `serf plugin list`'s Broken check,
//     serf-doctor) finds an ordinary on-disk manifest and needs no
//     special-casing.
//   - and the entry declares components but dir is NOT a cache directory
//     (staged=false — a directory-source plugin referenced in place, a
//     local-dev/test convenience), it returns a distinct clear error: serf
//     will not write a generated file into a directory it does not own.
func ensureManifestFallback(dir string, staged bool, cp CatalogPlugin) error {
	if hasPluginManifest(dir) {
		return nil
	}
	if !cp.HasManifestFields() {
		return fmt.Errorf("plugin %q: source has no plugin manifest (no .claude-plugin/plugin.json or .codex-plugin/plugin.json) and the marketplace entry declares no components (commands/agents/hooks/mcpServers)", cp.Name)
	}
	if !staged {
		return fmt.Errorf("plugin %q: source has no plugin manifest; the marketplace entry declares components, but serf only synthesizes a fallback manifest into a materialized cache install, not a directory source referenced in place", cp.Name)
	}

	manifest := agentplugin.Manifest{
		Name:        cp.Name,
		Description: cp.Description,
		Commands:    cp.Commands,
		Agents:      cp.Agents,
		Hooks:       cp.Hooks,
		MCPServers:  cp.MCPServers,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0o644); err != nil {
		return fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	return nil
}
```

- [ ] **Run** `go test ./internal/plugins/... -run 'TestEnsureManifestFallback' -count=1` → PASS.
- [ ] **Run** `go test ./internal/plugins/... -count=1` and `golangci-lint run ./internal/plugins/...`.
- [ ] **Commit** — `git add internal/plugins/manifest_fallback.go internal/plugins/manifest_fallback_test.go` →
  `feat(plugins): ensureManifestFallback synthesizes a plugin.json from the marketplace entry`.

## Task 5 — Wire into `Install`

**Files:**
- Modify: `internal/plugins/install.go:98-114` (`Install`)
- Test: `internal/plugins/install_test.go`

**Interfaces:**
- Consumes: `ensureManifestFallback` (Task 4).
- Produces: `Install` now succeeds for a manifest-less plugin whose catalog entry declares usable
  components, and fails with the honest error (not the old misleading one) when it declares none.

- [ ] **Failing test** — add to `internal/plugins/install_test.go` (add `"strings"` and
  `agentplugin "primeradiant.com/serf/agent/plugin"` to its import block):
```go
func TestInstall_ManifestLessPlugin_MCPServerRegisters(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	// A plugin repo with NO .claude-plugin (or .codex-plugin) manifest at all
	// — the private-journal-mcp shape: a bare package, no plugin.json.
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	makeGitRepo(t, pluginRepo, "README.md", "bare mcp server, no manifest")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"bare-mcp",
	  "source":{"source":"url","url":"` + pluginRepo + `"},
	  "mcpServers": {"bare": {"command":"echo","args":["hi"]}}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "bare-mcp", "acme")
	if err != nil {
		t.Fatalf("Install of a manifest-less plugin with an mcpServers entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing: %v", err)
	}
	inst, err := agentplugin.Load(entry.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_bare-mcp_bare" {
		t.Fatalf("MCPConfigs = %+v, want one entry named plugin_bare-mcp_bare", inst.MCPConfigs)
	}
}

func TestInstall_ManifestLessPlugin_NoUsableFields_ClearError(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	makeGitRepo(t, pluginRepo, "README.md", "bare, undeclared")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"bare-nothing",
	  "source":{"source":"url","url":"` + pluginRepo + `"}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	_, err := m.Install(context.Background(), "bare-nothing", "acme")
	if err == nil {
		t.Fatal("expected Install to fail for a manifest-less plugin with no usable entry fields")
	}
	if strings.Contains(err.Error(), ".codex-plugin") {
		t.Errorf("error must not name the misleading .codex-plugin path: %v", err)
	}
	reg, _ := LoadRegistry(m.registryPath())
	if _, ok := reg.Plugins["bare-nothing@acme"]; ok {
		t.Fatal("a failed install must not leave a registry entry")
	}
}

// TestInstall_PluginWithOwnManifest_EntryIgnored is the required regression:
// a plugin that ships its own plugin.json is completely unchanged by Part 2,
// even when its marketplace entry ALSO declares components.
func TestInstall_PluginWithOwnManifest_EntryIgnored(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "widget", nil) // has its own plugin.json, no mcpServers
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"widget",
	  "source":{"source":"url","url":"` + pluginRepo + `"},
	  "mcpServers": {"should-be-ignored": {"command":"echo"}}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	inst, err := agentplugin.Load(entry.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 0 {
		t.Fatalf("MCPConfigs = %+v, want none — entry's mcpServers must be ignored when plugin.json exists", inst.MCPConfigs)
	}
}
```
Run: `go test ./internal/plugins/... -run 'TestInstall_ManifestLessPlugin|TestInstall_PluginWithOwnManifest' -count=1`
→ expect FAIL (the no-fields case currently fails with the OLD misleading `.codex-plugin` message
instead of erroring cleanly before `Load` runs; the MCP-registers case currently fails outright
since `Load` hard-errors on a manifest-less source).

- [ ] **Implement** — in `internal/plugins/install.go`, in `Install` (~lines 98-114), change:
```go
	dir, sha, staged, err := m.stagePlugin(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if staged {
		final, cerr := m.commitStaged(marketplace, plugin, dir, sha)
		if cerr != nil {
			return InstallEntry{}, cerr
		}
		dir = final
	}
	if err := validatePluginDir(dir); err != nil {
		if strings.HasPrefix(dir, m.cacheDir()+string(os.PathSeparator)) {
			_ = os.RemoveAll(dir)
		}
		return InstallEntry{}, fmt.Errorf("installed plugin failed validation: %w", err)
	}
```
to:
```go
	dir, sha, staged, err := m.stagePlugin(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if staged {
		final, cerr := m.commitStaged(marketplace, plugin, dir, sha)
		if cerr != nil {
			return InstallEntry{}, cerr
		}
		dir = final
	}
	if err := ensureManifestFallback(dir, staged, cp); err != nil {
		if strings.HasPrefix(dir, m.cacheDir()+string(os.PathSeparator)) {
			_ = os.RemoveAll(dir)
		}
		return InstallEntry{}, err
	}
	if err := validatePluginDir(dir); err != nil {
		if strings.HasPrefix(dir, m.cacheDir()+string(os.PathSeparator)) {
			_ = os.RemoveAll(dir)
		}
		return InstallEntry{}, fmt.Errorf("installed plugin failed validation: %w", err)
	}
```
(The cache-dir-prefix delete-on-failure behavior is unchanged and intentionally kept — the design
spec's complaint was the *misleading message*, not the cleanup — see Global Constraints.)

- [ ] **Run** `go test ./internal/plugins/... -run 'TestInstall_' -count=1` → PASS (including the
  pre-existing `TestInstall_MaterializesAndRegisters`/`TestInstall_LazyFetchesSeededPointer`/
  `TestInstall_FromGitSubdirMarketplace`, which don't touch this path and must stay green).
- [ ] **Run** `go test ./internal/plugins/... -count=1` (package-wide) and
  `golangci-lint run ./internal/plugins/...`.
- [ ] **Commit** — `git add internal/plugins/install.go internal/plugins/install_test.go` →
  `feat(plugins): Install honors the marketplace entry for manifest-less plugins`.

## Task 6 — Wire into `Upgrade`/`upgradeLocked`

**Files:**
- Modify: `internal/plugins/install.go:213-220` (`upgradeLocked`)
- Test: `internal/plugins/upgrade_test.go`

**Interfaces:**
- Consumes: `ensureManifestFallback` (Task 4).
- Produces: an upgrade that materializes a new sha-dir for a manifest-less plugin re-applies the
  fallback to that new dir (the old dir already has its own synthesized manifest from a prior
  install/upgrade and is untouched, per the existing never-delete-on-upgrade rule).

- [ ] **Failing test** — add to `internal/plugins/upgrade_test.go` (add
  `agentplugin "primeradiant.com/serf/agent/plugin"` to its import block; `os/exec` is already
  imported):
```go
func TestUpgrade_ManifestLessPlugin_FallbackAppliedToNewShaDir(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{
	  "name":"bare-mcp",
	  "source":{"source":"url","url":"` + pluginRepo + `"},
	  "mcpServers": {"bare": {"command":"echo"}}
	}]}`
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m.Install(context.Background(), "bare-mcp", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Advance the plugin repo HEAD so Upgrade materializes a NEW sha-dir.
	os.WriteFile(filepath.Join(pluginRepo, "extra.txt"), []byte("v2"), 0o644)
	cmd := exec.Command("git", "-C", pluginRepo, "commit", "-aqm", "v2")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	second, err := m.Upgrade(context.Background(), "bare-mcp", "acme")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if second.InstallPath == first.InstallPath {
		t.Fatal("upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(filepath.Join(second.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing from the new sha-dir: %v", err)
	}
	inst, err := agentplugin.Load(second.InstallPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 1 {
		t.Fatalf("MCPConfigs = %+v, want one entry after upgrade", inst.MCPConfigs)
	}
}
```
Run: `go test ./internal/plugins/... -run 'TestUpgrade_ManifestLessPlugin' -count=1` → expect FAIL
(`Upgrade` currently fails outright validating the new sha-dir, since it has no manifest either).

- [ ] **Implement** — in `internal/plugins/install.go`, in `upgradeLocked` (~lines 213-220), change:
```go
	final, err := m.commitStaged(marketplace, plugin, staging, sha)
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	if err := validatePluginDir(final); err != nil {
		_ = os.RemoveAll(final)
		return InstallEntry{}, false, false, fmt.Errorf("upgraded plugin failed validation: %w", err)
	}
```
to:
```go
	final, err := m.commitStaged(marketplace, plugin, staging, sha)
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	if err := ensureManifestFallback(final, true, cp); err != nil {
		_ = os.RemoveAll(final)
		return InstallEntry{}, false, false, err
	}
	if err := validatePluginDir(final); err != nil {
		_ = os.RemoveAll(final)
		return InstallEntry{}, false, false, fmt.Errorf("upgraded plugin failed validation: %w", err)
	}
```
(`staged` is always `true` by this point — the function already returned early above when
`!staged || sha == prev.GitCommitSha` — so the literal `true` is correct, matching the guard's own
invariant.)

- [ ] **Run** `go test ./internal/plugins/... -run 'TestUpgrade_' -count=1` → PASS (including the
  pre-existing `TestUpgrade_NewShaDirOldRemains`/`TestUpgrade_NoOpKeepsLiveDir`, unaffected since
  their fixtures already ship a `plugin.json`, so `ensureManifestFallback` no-ops for them).
- [ ] **Run full package + naming + lint gates:**
  - `go test ./internal/plugins/... -count=1`
  - `golangci-lint run ./internal/plugins/...`
  - `go run ./cmd/serf-namingcheck` (confirms none of Task 1's six new fields need a
    `// serf:naming-ignore:` line, per Global Constraints)
  - `go build ./...` from repo root (confirms no other root-module package broke)
- [ ] **Commit** — `git add internal/plugins/install.go internal/plugins/upgrade_test.go` →
  `feat(plugins): Upgrade re-applies the manifest fallback to a new sha-dir`.

---

## Testing (Part 2, from the design spec, realized above)

- `Load`/install synthesizes a manifest from the catalog entry when the source has no `plugin.json`
  (with an `mcpServers` entry → the MCP server is declared/registered) — Task 4 (unit) and Task 5
  (end-to-end `Install`).
- A plugin **with** a `plugin.json` is unchanged (entry ignored) — Task 4
  (`TestEnsureManifestFallback_ExistingManifestIsNoop`) and Task 5
  (`TestInstall_PluginWithOwnManifest_EntryIgnored`).
- The clear error when neither a source manifest nor usable entry fields exist — Task 4 and Task 5,
  both asserting the message never contains `.codex-plugin`.
- `CatalogPlugin` parses the new fields (round-trip test) — Task 1.
- An install-fixture-plugin-with-a-command/agent (not just MCP) is covered implicitly: Task 4's
  synthesis path treats `Commands`/`Agents`/`Hooks`/`MCPServers` identically (all four flow through
  the same `agentplugin.Manifest` construction); only `MCPServers` gets a dedicated end-to-end
  assertion since it's the design spec's concrete motivating case, but the mechanism is the same
  for all four — not duplicated per-field to avoid redundant tests.
- Upgrade re-applies the fallback to a newly materialized sha-dir — Task 6.

## Out of scope (Part 2, from the design spec)

- `strict:false` entry-supplements-an-existing-`plugin.json` merge, and `strict:false`
  conflicts-with-an-existing-`plugin.json` detection — both require a `plugin.json` to already
  exist, which is moot for the manifest-absent case this plan implements (see Global Constraints'
  `strict` correction).
- A `Manifest.Skills` override field in `agent/plugin` (needed to honor a marketplace entry's
  custom `skills` paths) — a separate, `agent`-module-touching change; pinned as a known gap by
  Task 4's `TestEnsureManifestFallback_SkillsFieldNotHonored`.
- Writing a synthesized manifest into a directory-source plugin referenced in place (`staged=false`)
  — serf must not write generated files into a directory it does not own; such a plugin keeps
  failing to install (with the corrected message) if it lacks its own `plugin.json`.
- Retroactively fixing any specific live marketplace's `marketplace.json` (e.g.
  `superpowers-marketplace`'s `private-journal-mcp` entry, which today has `strict: true` and no
  embedded fields at all) — that is a content edit to that marketplace repo, not a serf code change.
- Everything under Part 1 (the browse-tree UI) — a separate, independent plan/PR.

## Estimate

Roughly 250-350 loc including tests (six tasks: ~40 loc struct fields + doc comments, ~15 loc
`HasManifestFields`, ~15 loc `hasPluginManifest`, ~45 loc `ensureManifestFallback`, ~10 loc each for
the two call-site wirings, remainder is test code — which dominates, as it should for six
TDD-gated tasks each asserting real installed/loaded plugin behavior against real git fixtures).
