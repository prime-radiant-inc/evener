# Plugin Marketplaces P2 — CLI + Enable-Gating + Bundling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the P1 `internal/plugins` manager drivable from a `serf plugin` CLI, have serf sessions actually LOAD the installed+enabled plugins (enable-gating), and seed a couple of standard marketplaces on first run.

**Architecture:** Three pieces on branch `cc-plugin-marketplaces` (P1's `internal/plugins` is already present + gated green). (1) `internal/plugins/enabled.go` — `EnabledPluginDirs(explicit)` merges explicit `--plugin-dir` values with installed+enabled registry entries, pre-validates each via the existing dry-run `Load`, dedups by plugin name (explicit wins), and skips broken ones, so the fail-hard `agent/plugin.LoadAll` never chokes. (2) `internal/plugins/seed.go` — first-run seeding of `known_marketplaces.json`. (3) A stdlib-`flag` `serf plugin` command tree (`cmd/serf/plugincmd.go`, package `main`, modeled on the existing `runOpenAI` nested switch) plus the two-line gating injection at the only two session-construction sites (`cmd/serf/run.go:183`, `cmd/serf/serve.go:212`). The hub needs NO change: it spawns `serf serve`, which self-computes enabled dirs.

**Tech Stack:** Go 1.25, root module `primeradiant.com/serf`. Stdlib `flag` (NOT cobra — cmd/serf is hand-rolled). Reuses P1 `internal/plugins` (`Manager`, `NewManager`, `DefaultRoot`, `List`, `AddMarketplace`/`ListMarketplaces`/`RemoveMarketplace`/`RefreshMarketplace`/`Browse`, `Install`/`Upgrade`/`Remove`/`SetEnabled`/`SetAutoUpgrade`/`UpdateAll`, `Source`/`SourceKind`, unexported `loadMarketplaces`/`saveMarketplaces`, `validatePluginDir`). Tests use `t.TempDir()` + real local `git` fixtures (the P1 helpers `makeGitRepo`/`makeInstallableMarketplace`/`writePlugin` live in `internal/plugins` test files — the CLI tests build their own small fixtures or drive a `Manager` rooted at a temp dir); `t.Skip` when `git` is absent.

## Global Constraints

- Discriminator is `url`, never `git` (P1 invariant — unchanged).
- v1 is **user-scope only**: `EnabledPluginDirs` and seeding operate on the single `DefaultRoot()` store. No project scope, no `--scope`.
- Gating injects at the **serf process** layer (`run.go` for bare `serf`, `serve.go` for hub-spawned `serf serve`). Do NOT modify `cmd/serf-hub/spawn.go` — that would double-inject.
- `EnabledPluginDirs(explicit []string) []string` must return a set the fail-hard `agent/plugin.LoadAll` accepts: every dir validates (dry-run `Load`), no two dirs share a plugin `Manifest.Name` (explicit `--plugin-dir` wins over a registry entry on collision). Broken/duplicate entries are dropped with a warning to `m.Stderr`.
- Seeding is **first-run only**, gated by `known_marketplaces.json` not existing; **user scope only**; opt-out via `--no-default-marketplaces` (bare `serf`) / a hub config flag. Seeded marketplaces are pointers (github refs), cloned lazily on first browse — do NOT clone at seed time.
- Default seeded marketplaces: `claude-plugins-official` → `github: anthropics/claude-plugins-official`; `superpowers-marketplace` → `github: obra/superpowers-marketplace`.
- CLI: stdlib `flag`; a new `case "plugin"` in `dispatchCLICommand` (`cmd/serf/main.go:269`); a nested switch modeled on `runOpenAI` (`cmd/serf/openai_login.go:46`); every leaf supports `--json`; `install`/`marketplace add` support `--yes` (non-interactive confirm). Add a `plugin` row to `printRunCommands` (`cmd/serf/main.go:218`).
- TDD; commit per task; pristine test output; `go test ./internal/plugins/... ./cmd/serf/...` from repo root.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/plugins/enabled.go` (+ `_test.go`) | `EnabledPluginDirs(explicit)` — merge/validate/dedup |
| `internal/plugins/seed.go` (+ `_test.go`) | `SeedDefaultMarketplaces` (first-run) + the embedded default list |
| `cmd/serf/plugincmd.go` (+ `_test.go`) | `runPlugin` command tree (marketplace + lifecycle subcommands, `--json`/`--yes`, rendering) |
| `cmd/serf/main.go` (modify) | `case "plugin"` in dispatch; `plugin` help row |
| `cmd/serf/run.go` (modify, ~:183) | inject `EnabledPluginDirs` into `PluginDirs`; first-run seed hook |
| `cmd/serf/serve.go` (modify, ~:212) | inject `EnabledPluginDirs` into `PluginDirs` |

---

## Task 1: `EnabledPluginDirs` — merge, validate, dedup

**Files:**
- Create: `internal/plugins/enabled.go`
- Test: `internal/plugins/enabled_test.go`

**Interfaces:**
- Consumes: `Manager`, `LoadRegistry`, `m.registryPath()`, `validatePluginDir`, `agent/plugin.Load` (for the plugin name of an explicit dir).
- Produces: `func (m *Manager) EnabledPluginDirs(explicit []string) []string` — the final, load-safe plugin-dir list (explicit dirs first, then enabled+valid registry dirs, deduped by plugin `Manifest.Name`, explicit winning; broken/dup dropped with a warning to `m.stderr()`).

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"context"
	"testing"
)

func TestEnabledPluginDirs_FiltersDisabledAndBroken(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t) // installs "widget" (relative source) — from install_test.go
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// enabled → included
	dirs := m.EnabledPluginDirs(nil)
	if len(dirs) != 1 || dirs[0] != entry.InstallPath {
		t.Fatalf("EnabledPluginDirs = %v, want [%s]", dirs, entry.InstallPath)
	}

	// disabled → excluded
	if err := m.SetEnabled("widget", name, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if dirs := m.EnabledPluginDirs(nil); len(dirs) != 0 {
		t.Fatalf("disabled plugin still returned: %v", dirs)
	}
}

func TestEnabledPluginDirs_ExplicitFirstAndDeduped(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	// An explicit --plugin-dir pointing at a DIFFERENT dir with the SAME plugin name
	// ("widget") must win: the registry dir is deduped out.
	explicitDir := t.TempDir()
	writePlugin(t, explicitDir, "widget", nil) // from validate_test.go

	dirs := m.EnabledPluginDirs([]string{explicitDir})
	if len(dirs) != 1 || dirs[0] != explicitDir {
		t.Fatalf("EnabledPluginDirs=%v, want [%s] (explicit wins over registry %s)", dirs, explicitDir, entry.InstallPath)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/plugins/ -run TestEnabledPluginDirs -v`
Expected: FAIL — `m.EnabledPluginDirs undefined`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"fmt"

	agentplugin "primeradiant.com/serf/agent/plugin"
)

// EnabledPluginDirs returns the plugin directories a session should load:
// the explicit --plugin-dir values first, then every installed+enabled+valid
// registry entry. Each dir is dry-run validated (agent/plugin.Load), and the
// set is deduped by plugin Manifest.Name — an explicit dir wins over a registry
// entry with the same plugin name — so the fail-hard LoadAll never sees a broken
// or duplicate-named plugin. Dropped dirs are warned to m.stderr().
func (m *Manager) EnabledPluginDirs(explicit []string) []string {
	seen := map[string]bool{} // plugin name → already chosen
	var out []string

	add := func(dir string, fromRegistry bool) {
		inst, err := agentplugin.Load(dir)
		if err != nil {
			if fromRegistry {
				fmt.Fprintf(m.stderr(), "warning: skipping broken plugin %s: %v\n", dir, err)
			} else {
				fmt.Fprintf(m.stderr(), "warning: skipping invalid --plugin-dir %s: %v\n", dir, err)
			}
			return
		}
		name := inst.Manifest.Name
		if seen[name] {
			if fromRegistry {
				return // explicit already won; silently drop the registry dup
			}
			fmt.Fprintf(m.stderr(), "warning: duplicate plugin name %q at %s; keeping the first\n", name, dir)
			return
		}
		seen[name] = true
		out = append(out, dir)
	}

	for _, d := range explicit {
		add(d, false)
	}

	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		fmt.Fprintf(m.stderr(), "warning: reading plugin registry: %v\n", err)
		return out
	}
	// Deterministic order across the enabled registry entries.
	for _, item := range mustList(m) {
		if item.Enabled && !item.Broken {
			add(item.InstallPath, true)
		}
	}
	_ = reg
	return out
}

// mustList returns m.List() results or nil on error (already warned by caller).
func mustList(m *Manager) []ListItem {
	items, err := m.List()
	if err != nil {
		fmt.Fprintf(m.stderr(), "warning: listing plugins: %v\n", err)
		return nil
	}
	return items
}
```

> **Execution note:** `m.List()` already computes `Broken` (via `validatePluginDir`) and returns entries sorted deterministically (P1 Task 15 fix). Since `List()` covers reading the registry + broken detection, the `LoadRegistry`/`reg` lines above are redundant — drop them and just iterate `mustList(m)`. Keep the double-`Load` (List's `validatePluginDir` + this `agentplugin.Load`) — it's the price of getting the plugin name for dedup, and matches the spec's "pre-validate each dir" contract. Confirm `stderr()` exists on `Manager` (added in P1); it does (`paths.go`).

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/plugins/ -run TestEnabledPluginDirs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/enabled.go internal/plugins/enabled_test.go
git commit -m "plugins: EnabledPluginDirs — merge explicit + enabled, validate, dedup by name"
```

---

## Task 2: First-run marketplace seeding

**Files:**
- Create: `internal/plugins/seed.go`
- Test: `internal/plugins/seed_test.go`

**Interfaces:**
- Consumes: `m.marketplacesFile()`, unexported `loadMarketplaces`/`saveMarketplaces`, `Source`, `MarketplaceRef`.
- Produces: `func DefaultMarketplaceSeeds() map[string]Source` (the embedded default list); `func (m *Manager) SeedDefaultMarketplaces() (seeded bool, err error)` — if `known_marketplaces.json` does NOT exist, write the default entries (as unfetched pointers: `InstallLocation` empty, cloned lazily on first Browse/Install) and return `true`; if it exists, no-op and return `false`.

> **Design note:** seeded entries have an empty `InstallLocation` (not yet cloned). `Browse`/`Install` must lazily clone a registered-but-unfetched marketplace. Check whether P1's `Browse`/`catalogPlugin` handle an empty `InstallLocation` — if they assume a cloned dir, add a small `ensureFetched(name)` in this task that clones on demand and backfills `InstallLocation` (reusing `fetchMarketplaceContainer`), and call it at the top of `Browse` and `catalogPlugin`. If P1 already requires a clone at add-time, an alternative is to seed by calling `AddMarketplace` lazily on first use instead of writing empty pointers — pick whichever is smaller after reading `marketplaces.go`, and note the choice in the report.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"os"
	"testing"
)

func TestSeedDefaultMarketplaces_FirstRunOnly(t *testing.T) {
	m := NewManager(t.TempDir())

	seeded, err := m.SeedDefaultMarketplaces()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !seeded {
		t.Fatal("first run should seed")
	}
	mk, _ := m.ListMarketplaces()
	if _, ok := mk["claude-plugins-official"]; !ok {
		t.Fatalf("official marketplace not seeded: %v", mk)
	}
	if _, ok := mk["superpowers-marketplace"]; !ok {
		t.Fatalf("superpowers marketplace not seeded: %v", mk)
	}

	// second run: no-op (respects user removals)
	seeded, err = m.SeedDefaultMarketplaces()
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if seeded {
		t.Fatal("second run should NOT re-seed")
	}

	// a user who removes a seeded marketplace and re-runs must not get it back
	if err := m.RemoveMarketplace("superpowers-marketplace"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m.SeedDefaultMarketplaces()
	mk, _ = m.ListMarketplaces()
	if _, ok := mk["superpowers-marketplace"]; ok {
		t.Fatal("removed seed was re-added")
	}
	_ = os.Stat
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/plugins/ -run TestSeedDefaultMarketplaces -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"errors"
	"io/fs"
	"os"
)

// DefaultMarketplaceSeeds is the set of marketplaces seeded on first run
// (pointers only — cloned lazily on first use).
func DefaultMarketplaceSeeds() map[string]Source {
	return map[string]Source{
		"claude-plugins-official": {Kind: SourceGitHub, Repo: "anthropics/claude-plugins-official"},
		"superpowers-marketplace": {Kind: SourceGitHub, Repo: "obra/superpowers-marketplace"},
	}
}

// SeedDefaultMarketplaces writes the default marketplaces IFF known_marketplaces.json
// does not yet exist. It is a no-op once the file exists, so a user who removes a
// seeded marketplace never gets it back. Seeded entries are unfetched pointers
// (empty InstallLocation), cloned lazily on first Browse/Install.
func (m *Manager) SeedDefaultMarketplaces() (bool, error) {
	if _, err := os.Stat(m.marketplacesFile()); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	mk := Marketplaces{}
	for name, src := range DefaultMarketplaceSeeds() {
		mk[name] = MarketplaceRef{Source: src, LastUpdated: m.now().UTC()}
	}
	if err := m.saveMarketplaces(mk); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run + verify**

Run: `go test ./internal/plugins/ -run TestSeedDefaultMarketplaces -v` → PASS. If a seeded (unfetched) marketplace breaks `Browse`/`Install`, implement the `ensureFetched` lazy-clone per the Design note and add a test that `Browse("claude-plugins-official")` clones-then-parses. Run `go test ./internal/plugins/ -race -count=1` to confirm no P1 regression.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/seed.go internal/plugins/seed_test.go internal/plugins/marketplaces.go internal/plugins/catalog.go
git commit -m "plugins: first-run default-marketplace seeding (lazy-fetch pointers)"
```

---

## Task 3: `serf plugin marketplace ...` CLI subtree

**Files:**
- Create: `cmd/serf/plugincmd.go`
- Modify: `cmd/serf/main.go` (dispatch case + help row)
- Test: `cmd/serf/plugincmd_test.go`

**Interfaces:**
- Consumes: `internal/plugins` (`NewManager`, `AddMarketplace`, `ListMarketplaces`, `RemoveMarketplace`, `RefreshMarketplace`, `Source`/`SourceKind`).
- Produces: `func runPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) error` — nested switch (`marketplace`, and in Task 4 the lifecycle verbs); `func runPluginMarketplace(args []string, stdout, stderr io.Writer) error` — `add|remove|list|refresh`. Marketplace `add <url-or-owner/repo> [--yes]` parses the source (owner/repo → `github`; `https://`/`git@` → `url`; local path → `directory`).

- [ ] **Step 1: Write the failing test** (drive the command with a temp store via an env override or a `--root` hidden flag; here use `SERF`-style: the CLI builds a `NewManager("")` = `DefaultRoot()`, so the test sets `XDG_CONFIG_HOME` to a temp dir)

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPluginMarketplaceList_Empty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"marketplace", "list"}, nil, &out, &errb); err != nil {
		t.Fatalf("runPlugin marketplace list: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "No marketplaces") && strings.TrimSpace(out.String()) != "" {
		// empty store: either a friendly "No marketplaces" line or empty output
		t.Logf("marketplace list output: %q", out.String())
	}
}

func TestPluginUnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	err := runPlugin([]string{"bogus"}, nil, &out, &errb)
	if err == nil {
		t.Fatal("unknown plugin subcommand should error")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/serf/ -run 'TestPluginMarketplaceList_Empty|TestPluginUnknownSubcommand' -v`
Expected: FAIL — `runPlugin undefined`.

- [ ] **Step 3: Write the implementation** — model `runPlugin` on `runOpenAI` (`openai_login.go:46`), a leaf on `runUpgrade` (`upgrade.go:15`). Full source in the sub-spec; the shape:

```go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"primeradiant.com/serf/internal/plugins"
)

func runPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPluginUsage(stderr)
		return nil
	}
	switch args[0] {
	case "marketplace":
		return runPluginMarketplace(args[1:], stdout, stderr)
	// lifecycle verbs added in Task 4:
	case "install", "remove", "enable", "disable", "list", "upgrade", "gc":
		return runPluginLifecycle(args[0], args[1:], stdin, stdout, stderr)
	case "help", "-h", "--help":
		printPluginUsage(stderr)
		return nil
	default:
		printPluginUsage(stderr)
		return fmt.Errorf("unknown plugin command %q", args[0])
	}
}

func runPluginMarketplace(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: serf plugin marketplace add|remove|list|refresh")
	}
	m := plugins.NewManager("")
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("marketplace list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		mk, err := m.ListMarketplaces()
		if err != nil {
			return err
		}
		return renderMarketplaces(stdout, mk, *asJSON)
	case "add":
		fs := flag.NewFlagSet("marketplace add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		yes := fs.Bool("yes", false, "skip the trust confirmation")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: serf plugin marketplace add <url|owner/repo|path> [--yes]")
		}
		src, err := parseMarketplaceSourceArg(fs.Arg(0))
		if err != nil {
			return err
		}
		if !*yes {
			fmt.Fprintf(stderr, "Add marketplace from %s? Marketplaces are arbitrary code repositories.\nPass --yes to confirm.\n", fs.Arg(0))
			return fmt.Errorf("confirmation required")
		}
		ref, err := m.AddMarketplace(context.Background(), "", src)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Added marketplace at %s\n", ref.InstallLocation)
		return nil
	case "remove":
		// flag set, NArg name, m.RemoveMarketplace(name)
		...
	case "refresh":
		// m.RefreshMarketplace(ctx, name)
		...
	default:
		return fmt.Errorf("unknown marketplace command %q", args[0])
	}
	return nil
}

// parseMarketplaceSourceArg maps a CLI arg to a Source: "owner/repo" → github,
// "https://…"/"git@…" → url, an existing local path → directory.
func parseMarketplaceSourceArg(arg string) (plugins.Source, error) {
	switch {
	case strings.HasPrefix(arg, "https://"), strings.HasPrefix(arg, "http://"), strings.HasPrefix(arg, "git@"):
		return plugins.Source{Kind: plugins.SourceURL, URL: arg}, nil
	case strings.Count(arg, "/") == 1 && !strings.Contains(arg, ":") && !strings.HasPrefix(arg, "."):
		return plugins.Source{Kind: plugins.SourceGitHub, Repo: arg}, nil
	default:
		// treat as a local directory path
		return plugins.Source{Kind: plugins.SourceDirectory, Path: arg}, nil
	}
}
```

Provide `printPluginUsage`, `renderMarketplaces(w, mk, asJSON)` (human table + `json.NewEncoder`), and the `remove`/`refresh` cases (mirror `add`/`list`). Then wire dispatch in `cmd/serf/main.go`:
- In `dispatchCLICommand` (main.go:264-281) add `case "plugin": return true, "serf plugin", runPlugin(args[1:], stdin, stdout, stderr)`.
- In `printRunCommands` (main.go:218-225) add a `plugin   manage plugin marketplaces and plugins` row.

- [ ] **Step 4: Run + verify**

Run: `go test ./cmd/serf/ -run 'TestPluginMarketplace|TestPluginUnknown' -v` → PASS. `go build ./...` → ok.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugincmd.go cmd/serf/plugincmd_test.go cmd/serf/main.go
git commit -m "serf: plugin marketplace add|remove|list|refresh CLI subtree"
```

---

## Task 4: `serf plugin install|remove|enable|disable|list|upgrade|gc` CLI

**Files:**
- Modify: `cmd/serf/plugincmd.go`
- Test: `cmd/serf/plugincmd_test.go`

**Interfaces:**
- Produces: `func runPluginLifecycle(verb string, args []string, stdin io.Reader, stdout, stderr io.Writer) error` — dispatches each verb over `plugins.NewManager("")`. `install <plugin>@<marketplace> [--yes]`; `remove/enable/disable/upgrade <plugin>@<marketplace>`; `upgrade --all`; `list [--json]`; `gc`.

- [ ] **Step 1: Write the failing test** (drive `list` against a temp store; drive `install` from a fixture marketplace by pre-seeding a `Manager` at the same `XDG_CONFIG_HOME` root, then invoking the CLI)

```go
func TestPluginList_JSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if err := runPlugin([]string{"list", "--json"}, nil, &out, &errb); err != nil {
		t.Fatalf("plugin list --json: %v\n%s", err, errb.String())
	}
	// empty store → valid JSON array/object
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("expected JSON output")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `runPluginLifecycle undefined`.

- [ ] **Step 3: Implement `runPluginLifecycle`** — for each verb, a `flag.FlagSet` (`--json` on `list`, `--all` on `upgrade`, `--yes` on `install`), parse `<plugin>@<marketplace>` from `fs.Arg(0)` (split on the last `@`), call the matching `Manager` method, render (`--json` via `json.NewEncoder`, else a human line/table). `install` without `--yes` prints the source + "pass --yes" and errors, mirroring `marketplace add`. Provide `splitPluginRef(arg) (plugin, marketplace string, err error)` and `renderPluginList(w, items, asJSON)`.

- [ ] **Step 4: Run + verify** — `go test ./cmd/serf/ -run TestPlugin -v` PASS; `go build ./...` ok.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugincmd.go cmd/serf/plugincmd_test.go
git commit -m "serf: plugin install/remove/enable/disable/list/upgrade/gc CLI"
```

---

## Task 5: Enable-gating injection + first-run seed hook

**Files:**
- Modify: `cmd/serf/run.go` (~:183 PluginDirs; ~:88 seed hook)
- Modify: `cmd/serf/serve.go` (~:212 PluginDirs)
- Test: `cmd/serf/plugincmd_test.go` (or a new `gating_test.go`) + an `internal/plugins` integration assertion

**Interfaces:**
- Consumes: `plugins.NewManager("").EnabledPluginDirs(explicit)`.
- The two session-construction sites set `PluginDirs` to the merged/gated list instead of the raw `--plugin-dir` slice.

- [ ] **Step 1: Write the failing test** — an integration test at the `internal/plugins` level already covers `EnabledPluginDirs` (Task 1). For the wiring, add a `cmd/serf` test that constructs the run/serve `SessionConfig` path is impractical to unit-test end-to-end; instead assert the seam directly: a small test that, given a temp `XDG_CONFIG_HOME` with an installed+enabled plugin, `plugins.NewManager("").EnabledPluginDirs(nil)` returns it — and a doc/comment check that `run.go`/`serve.go` call it. (The real end-to-end proof is the P-level e2e scenario in a later phase.) Keep this task's automated test at the `EnabledPluginDirs` seam; verify the wiring by `go build` + a manual `serf plugin list` smoke.

- [ ] **Step 2: Wire the injection.** In `cmd/serf/run.go` at the `PluginDirs: cfg.pluginDirs` line (~:183):
```go
	PluginDirs:                  plugins.NewManager("").EnabledPluginDirs(cfg.pluginDirs),
```
In `cmd/serf/serve.go` at the `PluginDirs: []string(pluginDirs)` line (~:212):
```go
	PluginDirs:                  plugins.NewManager("").EnabledPluginDirs([]string(pluginDirs)),
```
Add the `primeradiant.com/serf/internal/plugins` import to both files.

- [ ] **Step 3: First-run seed hook.** In `cmd/serf/run.go` near the existing `cmdutil.EnsureUserConfigDirs()` call (~:88), after ensuring config dirs, seed (respecting a new `--no-default-marketplaces` flag threaded through `runConfig`; default false = seed):
```go
	if !cfg.noDefaultMarketplaces {
		if _, err := plugins.NewManager("").SeedDefaultMarketplaces(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: seeding default marketplaces: %v\n", err)
		}
	}
```
Register the flag in `newRunFlagSet` (main.go, near the `--plugin-dir` registration at main.go:190) as `fs.BoolVar(&flags.noDefaultMarketplaces, "no-default-marketplaces", false, "do not seed the default plugin marketplaces on first run")`, add the field to the flags struct + `runConfig` (run.go:58 area), and thread it (main.go:144 area).

- [ ] **Step 4: Verify.** `go build ./...` ok; `go test ./internal/plugins/... ./cmd/serf/... -count=1` PASS; `go vet ./cmd/serf/... ./internal/plugins/...` clean; `golangci-lint run ./cmd/serf/... ./internal/plugins/... --max-issues-per-linter 0 --max-same-issues 0` clean (fix any errcheck per P1 convention). Manual smoke: build `serf`, run `serf plugin marketplace list` and `serf plugin list` against a temp `XDG_CONFIG_HOME`.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/run.go cmd/serf/serve.go cmd/serf/main.go
git commit -m "serf: gate sessions to enabled plugins; seed default marketplaces on first run"
```

---

## Task 6: Full gate

**Files:** none (verification).

- [ ] `go build ./...` ok.
- [ ] `go test ./internal/plugins/... ./cmd/serf/... -race -count=1` → ok.
- [ ] `go vet ./internal/plugins/... ./cmd/serf/...` clean.
- [ ] `golangci-lint run ./internal/plugins/... ./cmd/serf/... --max-issues-per-linter 0 --max-same-issues 0` → 0 issues (fix errcheck: best-effort `_ =`, real handling where a write/durability error).
- [ ] Manual smoke against a temp `XDG_CONFIG_HOME`: `serf plugin marketplace add anthropics/claude-plugins-official --yes` (real network+git — or skip if offline), `serf plugin marketplace list`, `serf plugin list`.
- [ ] Commit any fixups.

---

## Self-Review

Checked against spec §5.3 (user-scope), §6 (source parsing), §7 (verbs), §8 (gating seam), §11 (seeding):
- CLI verbs (§7 / §9.5): marketplace add/remove/list/refresh (T3) + install/remove/enable/disable/list/upgrade[--all]/gc (T4), `--json` + `--yes`. ✓ (`doctor` is P7.)
- Gating (§8): `EnabledPluginDirs` (T1) injected at the two serf-process session sites (T5); hub untouched (spawns `serf serve`, which self-gates). ✓
- Bundling (§11): user-scope, first-run-only, pointer seeds, `--no-default-marketplaces` opt-out. ✓
- No cobra; stdlib `flag` + `dispatchCLICommand` case, per the real codebase. ✓

**Type consistency:** `EnabledPluginDirs(explicit []string) []string`, `SeedDefaultMarketplaces() (bool, error)`, `DefaultMarketplaceSeeds() map[string]Source`, `runPlugin`/`runPluginMarketplace`/`runPluginLifecycle`, `parseMarketplaceSourceArg`/`splitPluginRef` — used consistently across tasks.

**Deferred (correctly not P2):** `serf plugin doctor` (P7); auto-upgrade daemon (P4); slash commands (P3); web/TUI (P5/P6).
