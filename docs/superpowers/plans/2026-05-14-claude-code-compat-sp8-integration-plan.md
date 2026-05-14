# SP8 — Discovery Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compose the outputs of SP1..SP7 at session startup so a single `agent.NewSession` call produces a fully wired session whose hooks, MCP servers, permissions, skills, agents, plugin binaries, and user-config substitutions all reflect the merged Claude-Code-shaped configuration.

**Architecture:** A new `agent/session_bootstrap.go` helper (`BuildSessionConfig`) owns steps 1–4 of the startup pipeline. `SessionConfig` gains typed fields that thread merged config from the four CLI binaries through `NewSession`. Fire sites for seven new lifecycle events land in `agent/session.go`, `agent/subagents.go`, and `agent/context_strategy.go`. Permission enforcement is inserted into `execTool` between `PreToolUse` and the tool call. The four binaries (`cmd/serf`, `cmd/serf-tui`, `cmd/serf-hub`, `cmd/serfeval`) call `BuildSessionConfig` and copy its fields onto their existing `SessionConfig` literals.

**Tech Stack:** Go (existing). No new dependencies. Table-driven tests with `t.TempDir`, `t.Setenv`. Real `git`, `bash` with `t.Skip` when absent. `httptest.NewServer` for HTTP hooks.

**Spec reference:** `docs/superpowers/specs/2026-05-14-claude-code-compat-sp8-integration-design.md` (parent: `2026-05-14-claude-code-compat-design.md`).

**Dependencies on prior sub-projects.** SP8 assumes SP1..SP7 exports exist when the integration tests run. For SP8-isolated unit tests, the plan uses small interface seams that the real SP1..SP7 types satisfy naturally. Each seam cites its source spec inline.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `agent/session_bootstrap.go` | create | `BuildSessionConfig`, `BootstrapFlags`, `ResolvedPlugin`, `PluginSourceKind`, `splitPluginSpec`, plugin-resolver wiring |
| `agent/session_bootstrap_test.go` | create | Table-driven unit tests for split/resolve/merge, ordering, and error semantics |
| `agent/session.go` | modify | Add new `SessionConfig` fields; build `permissionMatcher` in `NewSession`; populate `pluginUserConfigs`; place fire sites for `PostToolUseFailure`, `PostToolBatch`, `StopFailure`, `UserPromptExpansion`; insert permission-enforcement block in `execTool` |
| `agent/session_test.go` | modify | Tests for new field plumbing, enforcement order, fire sites |
| `agent/subagents.go` | modify | `SubagentStart` fire site; copy parent's merged config / resolved plugins / permissions to child `SessionConfig` |
| `agent/subagents_test.go` | modify | Subagent inheritance tests |
| `agent/context_strategy.go` | modify | `PostCompact` fire site after compaction returns |
| `agent/context_strategy_test.go` | modify | `PostCompact` fire test |
| `cmd/serf/main.go` | modify | Register `--config`, `--trust-marketplace`, `--plugin-option` flags |
| `cmd/serf/run.go` | modify | Call `BuildSessionConfig`; copy returned fields into existing `SessionConfig` literal |
| `cmd/serf/serve.go` | modify | Same for daemon-mode `SessionConfig` |
| `cmd/serf-tui/embedded.go` | modify | Same for the three `SessionConfig` sites in TUI |
| `cmd/serf-hub/web.go` | modify | Add `ConfigPaths` to `WebConfig`; thread merged config into the Spawner request |
| `cmd/serfeval/main.go` | modify | Same wiring for serfeval |
| `agent/integration_sp8_test.go` | create | End-to-end suite covering the 19 cases in spec §12.1 |
| `agent/testdata/plugins/sp8-hookparity/` | create | Fixture plugin with one hook per new SP5 event |
| `agent/testdata/marketplaces/sp8-basic/` | create | Smallest directory-source marketplace exercising the install → run → uninstall loop |

---

## Pre-flight: confirm prerequisites exist

- [ ] **Step 1: Verify SP1..SP7 exports SP8 will call**

Run from repo root:

```bash
grep -rn 'func DiscoverSerfConfig' agent/ || echo "MISSING: SP1 DiscoverSerfConfig"
grep -rn 'func NewPermissionMatcher' agent/ || echo "MISSING: SP2 NewPermissionMatcher"
grep -rn 'func.*Installer.*Lookup' internal/plugins/ || echo "MISSING: SP4 Installer.Lookup"
grep -rn 'func ResolveUserConfig' agent/ || echo "MISSING: SP7 ResolveUserConfig"
grep -rn 'EventPostToolUseFailure\|RunPostToolUseFailure' agent/ || echo "MISSING: SP5 PostToolUseFailure"
```

Expected: each command prints either a match or a `MISSING:` line. If any are missing, this SP8 plan still proceeds — the SP8 code will compile only after the named prior SPs are merged. Note any gaps to surface at handoff.

- [ ] **Step 2: Commit a placeholder doc marker**

No code change. This step exists so subagent-driven execution has a clean checkpoint before SP8 code lands.

```bash
git status
```

Expected: working tree clean (no SP8 files yet).

---

## Task 1: Introduce `ResolvedPlugin` and `PluginSourceKind` types

**Files:**
- Create: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/session_bootstrap_test.go`:

```go
package agent

import (
	"testing"
)

func TestResolvedPlugin_ZeroValue(t *testing.T) {
	var p ResolvedPlugin
	if p.PluginID != "" || p.CachePath != "" || p.Version != "" {
		t.Fatalf("zero ResolvedPlugin should have empty strings, got %+v", p)
	}
	if p.Source != PluginSourceKind("") {
		t.Fatalf("zero Source should be empty kind, got %q", p.Source)
	}
}

func TestPluginSourceKind_Constants(t *testing.T) {
	if PluginSourceEnabled != "enabled" {
		t.Fatalf("PluginSourceEnabled = %q, want %q", PluginSourceEnabled, "enabled")
	}
	if PluginSourceCLI != "plugin-dir" {
		t.Fatalf("PluginSourceCLI = %q, want %q", PluginSourceCLI, "plugin-dir")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestResolvedPlugin_ZeroValue -v
```

Expected: FAIL with `undefined: ResolvedPlugin`.

- [ ] **Step 3: Write minimal implementation**

Create `agent/session_bootstrap.go`:

```go
package agent

// PluginSourceKind tags how a resolved plugin entered the session.
type PluginSourceKind string

const (
	// PluginSourceEnabled means the plugin came from config.json's enabledPlugins.
	PluginSourceEnabled PluginSourceKind = "enabled"
	// PluginSourceCLI means the plugin came from a --plugin-dir flag.
	PluginSourceCLI PluginSourceKind = "plugin-dir"
)

// ResolvedPlugin is one entry in the ordered set of plugins to load for a session.
// See sp8 §4.
type ResolvedPlugin struct {
	PluginID  string           // "plugin@marketplace" for enabled; filepath.Base(path) for --plugin-dir
	CachePath string           // absolute path to the plugin root
	Version   string           // resolved version per SP4 §9; "ad-hoc" for --plugin-dir
	Source    PluginSourceKind // "enabled" | "plugin-dir"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestResolvedPlugin_ZeroValue|TestPluginSourceKind_Constants' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: introduce ResolvedPlugin and PluginSourceKind for SP8"
```

---

## Task 2: Add `splitPluginSpec` helper

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
func TestSplitPluginSpec(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantPlugin     string
		wantMarket     string
		wantErr        bool
	}{
		{"basic", "foo@market", "foo", "market", false},
		{"market-with-at", "foo@team@market", "foo@team", "market", false},
		{"missing-at", "foo", "", "", true},
		{"empty", "", "", "", true},
		{"trailing-at", "foo@", "", "", true},
		{"leading-at", "@market", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin, market, err := splitPluginSpec(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitPluginSpec(%q): want error, got plugin=%q market=%q", tc.input, plugin, market)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitPluginSpec(%q): unexpected error: %v", tc.input, err)
			}
			if plugin != tc.wantPlugin || market != tc.wantMarket {
				t.Fatalf("splitPluginSpec(%q) = (%q,%q), want (%q,%q)", tc.input, plugin, market, tc.wantPlugin, tc.wantMarket)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestSplitPluginSpec -v
```

Expected: FAIL with `undefined: splitPluginSpec`.

- [ ] **Step 3: Write minimal implementation**

Append to `agent/session_bootstrap.go`:

```go
import (
	"fmt"
	"strings"
)

// splitPluginSpec splits "plugin@marketplace" on the LAST '@'.
// Per sp8 §3.6 / §9: empty plugin or marketplace is an error.
func splitPluginSpec(spec string) (plugin, marketplace string, err error) {
	idx := strings.LastIndex(spec, "@")
	if idx <= 0 || idx == len(spec)-1 {
		return "", "", fmt.Errorf("invalid plugin spec %q: must be \"plugin@marketplace\"", spec)
	}
	return spec[:idx], spec[idx+1:], nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestSplitPluginSpec -v
```

Expected: PASS for all six subtests.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: add splitPluginSpec for SP8 enabledPlugins resolution"
```

---

## Task 3: Define collaborator-boundary interfaces

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

Rationale: SP8 needs to call SP4's installer and SP7's stores. To keep SP8 unit tests hermetic (no SP4 registry on disk), define small interfaces SP8 owns. The real SP4 `Installer` and SP7 stores satisfy them naturally. Cite source specs in comments.

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
type fakeInstaller struct {
	entries map[string]installerEntry // key: "plugin@market"
	errs    map[string]error
}

func (f *fakeInstaller) Lookup(plugin, marketplace string) (installerEntry, error) {
	key := plugin + "@" + marketplace
	if err, ok := f.errs[key]; ok {
		return installerEntry{}, err
	}
	e, ok := f.entries[key]
	if !ok {
		return installerEntry{}, ErrPluginNotInstalled
	}
	return e, nil
}

func TestInstallerEntry_Shape(t *testing.T) {
	e := installerEntry{InstallPath: "/cache/foo", Version: "1.2.3"}
	if e.InstallPath != "/cache/foo" || e.Version != "1.2.3" {
		t.Fatalf("installerEntry zero plumbing failed: %+v", e)
	}
}

func TestErrPluginNotInstalled_Identity(t *testing.T) {
	if ErrPluginNotInstalled == nil {
		t.Fatal("ErrPluginNotInstalled should be a non-nil sentinel")
	}
	if ErrPluginNotInstalled.Error() == "" {
		t.Fatal("ErrPluginNotInstalled should have a non-empty message")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestInstallerEntry_Shape|TestErrPluginNotInstalled_Identity' -v
```

Expected: FAIL with `undefined: installerEntry` and `undefined: ErrPluginNotInstalled`.

- [ ] **Step 3: Write minimal implementation**

Append to `agent/session_bootstrap.go`:

```go
import "errors"

// installerEntry is the cross-scope read of one row in installed_plugins.json.
// Source: SP4 §2.2 — SP8 reads via a thin wrapper. The real SP4 Installer
// satisfies pluginInstaller below.
type installerEntry struct {
	InstallPath string
	Version     string
}

// ErrPluginNotInstalled is returned by Lookup when no entry exists.
// Source: SP4 §2.2.
var ErrPluginNotInstalled = errors.New("plugin not installed")

// pluginInstaller is the read-only surface SP8 needs from SP4.
// The real *plugins.Installer (SP4 §2) satisfies this.
type pluginInstaller interface {
	Lookup(plugin, marketplace string) (installerEntry, error)
}

// trustEnforcer is the surface SP8 needs from SP3.
// The real *plugins.TrustManager (SP3 §7) satisfies this.
type trustEnforcer interface {
	// EnforceTrustOnConfig prunes untrusted project-tier marketplaces from cfg
	// and returns the (possibly modified) cfg. Source: SP3 §7.1.
	EnforceTrustOnConfig(cfg SerfConfig) (SerfConfig, error)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestInstallerEntry_Shape|TestErrPluginNotInstalled_Identity' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: define SP8 collaborator-boundary interfaces"
```

---

## Task 4: Add `BootstrapFlags` struct

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
func TestBootstrapFlags_ZeroValue(t *testing.T) {
	var f BootstrapFlags
	if f.ConfigPaths != nil || f.PluginDirs != nil || f.TrustMarketplaces != nil {
		t.Fatalf("zero BootstrapFlags should have nil slices, got %+v", f)
	}
	if f.PluginOptions != nil {
		t.Fatalf("zero PluginOptions should be nil, got %v", f.PluginOptions)
	}
	if f.WatchConfig || f.IsRemote {
		t.Fatalf("zero bools should be false, got watch=%v remote=%v", f.WatchConfig, f.IsRemote)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestBootstrapFlags_ZeroValue -v
```

Expected: FAIL with `undefined: BootstrapFlags`.

- [ ] **Step 3: Write minimal implementation**

Append to `agent/session_bootstrap.go`:

```go
// BootstrapFlags is the CLI-side input to BuildSessionConfig.
// Each CLI binary fills it from its own flag set. Source: sp8 §5.5.
type BootstrapFlags struct {
	ConfigPaths       []string                     // repeated --config
	PluginDirs        []string                     // repeated --plugin-dir (existing flag)
	TrustMarketplaces []string                     // repeated --trust-marketplace
	PluginOptions     map[string]map[string]string // --plugin-option <plugin>.<key>=<value>
	Prompter          UserConfigPrompter           // surface-specific prompter from SP7 §4.2
	AskFallback       AskFallback                  // surface-specific ask resolution from SP2 §9
	IsRemote          bool                         // serf-hub spawns set this true
	WatchConfig       bool                         // starts the SP5 §3.9 watcher when true
}
```

Note: `UserConfigPrompter` (SP7), `AskFallback` (SP2) are external types. If they're not yet exported when SP8 lands, declare local interface aliases. For now assume they exist.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestBootstrapFlags_ZeroValue -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: add BootstrapFlags struct for SP8 binary wiring"
```

---

## Task 5: Add new fields to `SessionConfig`

**Files:**
- Modify: `agent/session.go` (around the existing `SessionConfig` literal, spec cites line 72)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestSessionConfig_SP8Fields_ZeroValue(t *testing.T) {
	var c SessionConfig
	if c.MergedConfig.Sources != nil {
		t.Errorf("MergedConfig.Sources should be nil by default, got %v", c.MergedConfig.Sources)
	}
	if c.EnabledPluginPaths != nil {
		t.Errorf("EnabledPluginPaths should be nil, got %v", c.EnabledPluginPaths)
	}
	if c.WatchConfig {
		t.Error("WatchConfig should default to false")
	}
	if c.IsRemote {
		t.Error("IsRemote should default to false")
	}
}

func TestSessionConfig_PluginDirsAndEnabledPluginPaths_BothSet_Error(t *testing.T) {
	c := SessionConfig{
		PluginDirs:         []string{"/tmp/x"},
		EnabledPluginPaths: []ResolvedPlugin{{PluginID: "foo@m", CachePath: "/tmp/y"}},
	}
	if err := c.validatePluginSources(); err == nil {
		t.Fatal("validatePluginSources: want error when both PluginDirs and EnabledPluginPaths populated, got nil")
	}
}

func TestSessionConfig_PluginDirsOnly_OK(t *testing.T) {
	c := SessionConfig{PluginDirs: []string{"/tmp/x"}}
	if err := c.validatePluginSources(); err != nil {
		t.Fatalf("validatePluginSources: want nil with only PluginDirs, got %v", err)
	}
}

func TestSessionConfig_EnabledPluginPathsOnly_OK(t *testing.T) {
	c := SessionConfig{EnabledPluginPaths: []ResolvedPlugin{{PluginID: "foo@m"}}}
	if err := c.validatePluginSources(); err != nil {
		t.Fatalf("validatePluginSources: want nil with only EnabledPluginPaths, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestSessionConfig_SP8Fields_ZeroValue|TestSessionConfig_PluginDirsAndEnabledPluginPaths_BothSet_Error|TestSessionConfig_PluginDirsOnly_OK|TestSessionConfig_EnabledPluginPathsOnly_OK' -v
```

Expected: FAIL — fields and `validatePluginSources` don't exist yet.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, add to the `SessionConfig` struct (preserve all existing fields):

```go
// --- SP8 §4 additions ---

// Permissions is the merged PermissionsConfig from SP1. NewSession uses it
// to build the session's PermissionMatcher (SP2 §6.2).
Permissions PermissionsConfig

// PermissionAskFallback dictates surface behavior when a rule yields "ask"
// and no hook responds. Source: SP2 §9.
PermissionAskFallback AskFallback

// MergedConfig is the full SerfConfig from SP1's loader, carried on the
// session for ConfigChange diffing (SP5 §3.9) and observability.
MergedConfig SerfConfig

// EnabledPluginPaths is the ordered (plugin, cache, version, source) tuple
// list produced by SP8's resolver. When populated, NewSession loads from
// here instead of PluginDirs.
EnabledPluginPaths []ResolvedPlugin

// PluginConfigStore persists non-sensitive userConfig values. Source: SP7.
PluginConfigStore PluginConfigStore

// SecureStore persists userConfig values flagged sensitive: true. Source: SP7.
SecureStore SecureStore

// UserConfigPrompter is the surface-specific prompter. Source: SP7 §4.2.
UserConfigPrompter UserConfigPrompter

// WatchConfig, when true, starts the fsnotify watcher that fires
// ConfigChange events. Source: SP5 §3.9.
WatchConfig bool

// IsRemote signals serf-hub-style embedding. SP5 reads this when setting
// CLAUDE_CODE_REMOTE on spawned hook processes.
IsRemote bool
```

Below `SessionConfig`, add:

```go
// validatePluginSources enforces the SP8 §4 invariant: callers populate
// either PluginDirs (the legacy path) or EnabledPluginPaths (the SP8 path),
// never both. NewSession applies this fallback automatically (see §13).
func (c *SessionConfig) validatePluginSources() error {
	if len(c.PluginDirs) > 0 && len(c.EnabledPluginPaths) > 0 {
		return fmt.Errorf("cannot set both PluginDirs and EnabledPluginPaths on SessionConfig")
	}
	return nil
}
```

If `SerfConfig`, `PermissionsConfig`, `AskFallback`, `PluginConfigStore`, `SecureStore`, `UserConfigPrompter` don't yet exist (SP1/SP2/SP7 not merged), add minimum stub declarations at the bottom of `session_bootstrap.go` with `// TODO: replace with SP{N} export once merged` comments. Do not add bodies — empty structs / nil interfaces are enough for compile.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestSessionConfig_SP8Fields_ZeroValue|TestSessionConfig_PluginDirsAndEnabledPluginPaths_BothSet_Error|TestSessionConfig_PluginDirsOnly_OK|TestSessionConfig_EnabledPluginPathsOnly_OK' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go agent/session_bootstrap.go
git commit -m "agent: add SP8 fields to SessionConfig with plugin-source validation"
```

---

## Task 6: `BuildSessionConfig` — DiscoverSerfConfig call (pipeline step 1)

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
import (
	"os"
	"path/filepath"
)

func TestBuildSessionConfig_LoadsDiscoveredConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"permissions": {"deny": ["Bash(rm:*)"]},
		"enabledPlugins": {}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	env := ExecutionEnvironment{WorkingDir: dir}
	flags := BootstrapFlags{ConfigPaths: []string{configPath}}

	sc, err := BuildSessionConfig(env, flags, bootstrapDeps{
		discover: func(env ExecutionEnvironment, paths []string) (SerfConfig, error) {
			if len(paths) != 1 || paths[0] != configPath {
				t.Fatalf("discover got paths=%v, want [%q]", paths, configPath)
			}
			return SerfConfig{
				Permissions: PermissionsConfig{Deny: []string{"Bash(rm:*)"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}
	if len(sc.MergedConfig.Permissions.Deny) != 1 || sc.MergedConfig.Permissions.Deny[0] != "Bash(rm:*)" {
		t.Fatalf("MergedConfig.Permissions.Deny = %v, want [Bash(rm:*)]", sc.MergedConfig.Permissions.Deny)
	}
	if len(sc.Permissions.Deny) != 1 || sc.Permissions.Deny[0] != "Bash(rm:*)" {
		t.Fatalf("Permissions.Deny not copied: %v", sc.Permissions.Deny)
	}
}

func TestBuildSessionConfig_DiscoverError_Fatal(t *testing.T) {
	_, err := BuildSessionConfig(ExecutionEnvironment{}, BootstrapFlags{}, bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{}, fmt.Errorf("parse error in /etc/x.json: bad json")
		},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "bad json") {
		t.Fatalf("err = %v, want it to mention parse error", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_LoadsDiscoveredConfig|TestBuildSessionConfig_DiscoverError_Fatal' -v
```

Expected: FAIL — `BuildSessionConfig` and `bootstrapDeps` don't exist.

- [ ] **Step 3: Write minimal implementation**

Append to `agent/session_bootstrap.go`:

```go
// bootstrapDeps lets tests substitute the collaborator surfaces.
// Production code uses defaultBootstrapDeps(). Each field cites its owning SP.
type bootstrapDeps struct {
	discover         func(ExecutionEnvironment, []string) (SerfConfig, error) // SP1
	installer        pluginInstaller                                          // SP4
	trust            trustEnforcer                                            // SP3
	newMatcher       func(PermissionsConfig, ExecutionEnvironment) (*PermissionMatcher, error) // SP2
}

func defaultBootstrapDeps() bootstrapDeps {
	return bootstrapDeps{
		discover:   DiscoverSerfConfig,
		newMatcher: NewPermissionMatcher,
		// installer and trust are nil here; the binary wires them.
	}
}

// BuildSessionConfig runs steps 1–4 of the SP8 startup pipeline (§2).
// It returns a partially-populated SessionConfig the caller merges into its
// own existing literal.
func BuildSessionConfig(env ExecutionEnvironment, flags BootstrapFlags, deps bootstrapDeps) (SessionConfig, error) {
	if deps.discover == nil {
		deps = defaultBootstrapDeps()
		// Preserve any caller overrides for installer/trust/newMatcher.
	}

	// Step 1: load merged SerfConfig.
	cfg, err := deps.discover(env, flags.ConfigPaths)
	if err != nil {
		return SessionConfig{}, fmt.Errorf("load serf config: %w", err)
	}

	sc := SessionConfig{
		MergedConfig:          cfg,
		Permissions:           cfg.Permissions,
		PermissionAskFallback: flags.AskFallback,
		UserConfigPrompter:    flags.Prompter,
		WatchConfig:           flags.WatchConfig,
		IsRemote:              flags.IsRemote,
	}
	return sc, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_LoadsDiscoveredConfig|TestBuildSessionConfig_DiscoverError_Fatal' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: BuildSessionConfig step 1 — load merged SerfConfig"
```

---

## Task 7: `BuildSessionConfig` — trust enforcement (pipeline pre-step 2)

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
type fakeTrust struct {
	called bool
	prune  func(SerfConfig) SerfConfig
}

func (f *fakeTrust) EnforceTrustOnConfig(cfg SerfConfig) (SerfConfig, error) {
	f.called = true
	if f.prune != nil {
		cfg = f.prune(cfg)
	}
	return cfg, nil
}

func TestBuildSessionConfig_RunsTrustEnforcer(t *testing.T) {
	trust := &fakeTrust{
		prune: func(c SerfConfig) SerfConfig {
			delete(c.EnabledPlugins, "untrusted@market")
			return c
		},
	}
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{
				EnabledPlugins: map[string]any{"untrusted@market": true, "good@market": true},
			}, nil
		},
		trust: trust,
	}
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, BootstrapFlags{}, deps)
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}
	if !trust.called {
		t.Fatal("expected trust enforcer to be called")
	}
	if _, ok := sc.MergedConfig.EnabledPlugins["untrusted@market"]; ok {
		t.Fatal("untrusted plugin should have been pruned")
	}
	if _, ok := sc.MergedConfig.EnabledPlugins["good@market"]; !ok {
		t.Fatal("good plugin should remain")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestBuildSessionConfig_RunsTrustEnforcer -v
```

Expected: FAIL — trust enforcer not invoked.

- [ ] **Step 3: Write minimal implementation**

In `agent/session_bootstrap.go`, after the `discover` call in `BuildSessionConfig`, insert:

```go
	// Trust enforcement runs between steps 1 and 2 per sp8 §3.6.
	if deps.trust != nil {
		cfg, err = deps.trust.EnforceTrustOnConfig(cfg)
		if err != nil {
			return SessionConfig{}, fmt.Errorf("enforce trust: %w", err)
		}
	}
```

Then re-assign `sc.MergedConfig = cfg` and `sc.Permissions = cfg.Permissions` after this block (move the existing assignments below the trust call).

Final `BuildSessionConfig` body so far:

```go
func BuildSessionConfig(env ExecutionEnvironment, flags BootstrapFlags, deps bootstrapDeps) (SessionConfig, error) {
	if deps.discover == nil {
		deps.discover = DiscoverSerfConfig
	}
	if deps.newMatcher == nil {
		deps.newMatcher = NewPermissionMatcher
	}

	cfg, err := deps.discover(env, flags.ConfigPaths)
	if err != nil {
		return SessionConfig{}, fmt.Errorf("load serf config: %w", err)
	}

	if deps.trust != nil {
		cfg, err = deps.trust.EnforceTrustOnConfig(cfg)
		if err != nil {
			return SessionConfig{}, fmt.Errorf("enforce trust: %w", err)
		}
	}

	sc := SessionConfig{
		MergedConfig:          cfg,
		Permissions:           cfg.Permissions,
		PermissionAskFallback: flags.AskFallback,
		UserConfigPrompter:    flags.Prompter,
		WatchConfig:           flags.WatchConfig,
		IsRemote:              flags.IsRemote,
	}
	return sc, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_LoadsDiscoveredConfig|TestBuildSessionConfig_DiscoverError_Fatal|TestBuildSessionConfig_RunsTrustEnforcer' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: BuildSessionConfig trust enforcement pre-step"
```

---

## Task 8: `BuildSessionConfig` — resolve enabledPlugins to cache paths (step 2)

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
func TestBuildSessionConfig_ResolvesEnabledPlugins(t *testing.T) {
	installer := &fakeInstaller{
		entries: map[string]installerEntry{
			"foo@market": {InstallPath: "/cache/foo/1.0.0", Version: "1.0.0"},
			"bar@market": {InstallPath: "/cache/bar/2.0.0", Version: "2.0.0"},
		},
	}
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{
				EnabledPlugins: map[string]any{
					"foo@market": true,
					"bar@market": true,
				},
			}, nil
		},
		installer: installer,
	}
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, BootstrapFlags{}, deps)
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}
	if len(sc.EnabledPluginPaths) != 2 {
		t.Fatalf("EnabledPluginPaths len = %d, want 2", len(sc.EnabledPluginPaths))
	}
	byID := map[string]ResolvedPlugin{}
	for _, p := range sc.EnabledPluginPaths {
		byID[p.PluginID] = p
	}
	if byID["foo@market"].CachePath != "/cache/foo/1.0.0" {
		t.Fatalf("foo path = %q", byID["foo@market"].CachePath)
	}
	if byID["foo@market"].Version != "1.0.0" {
		t.Fatalf("foo version = %q", byID["foo@market"].Version)
	}
	if byID["foo@market"].Source != PluginSourceEnabled {
		t.Fatalf("foo source = %q, want %q", byID["foo@market"].Source, PluginSourceEnabled)
	}
}

func TestBuildSessionConfig_NotInstalled_WarnsAndSkips(t *testing.T) {
	installer := &fakeInstaller{
		entries: map[string]installerEntry{
			"good@market": {InstallPath: "/cache/good/1", Version: "1"},
		},
	}
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{
				EnabledPlugins: map[string]any{
					"missing@market": true,
					"good@market":    true,
				},
			}, nil
		},
		installer: installer,
	}
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, BootstrapFlags{}, deps)
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}
	if len(sc.EnabledPluginPaths) != 1 {
		t.Fatalf("want 1 resolved (missing skipped), got %d: %+v", len(sc.EnabledPluginPaths), sc.EnabledPluginPaths)
	}
	if sc.EnabledPluginPaths[0].PluginID != "good@market" {
		t.Fatalf("got %q, want good@market", sc.EnabledPluginPaths[0].PluginID)
	}
}

func TestBuildSessionConfig_MalformedPluginSpec_WarnsAndSkips(t *testing.T) {
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{
				EnabledPlugins: map[string]any{"bare-no-at": true},
			}, nil
		},
		installer: &fakeInstaller{},
	}
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, BootstrapFlags{}, deps)
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}
	if len(sc.EnabledPluginPaths) != 0 {
		t.Fatalf("want 0 resolved for malformed spec, got %v", sc.EnabledPluginPaths)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_ResolvesEnabledPlugins|TestBuildSessionConfig_NotInstalled_WarnsAndSkips|TestBuildSessionConfig_MalformedPluginSpec_WarnsAndSkips' -v
```

Expected: FAIL — `EnabledPluginPaths` empty.

- [ ] **Step 3: Write minimal implementation**

In `agent/session_bootstrap.go`, before the final `return sc, nil` in `BuildSessionConfig`, add:

```go
	// Step 2: resolve enabledPlugins to cache paths via SP4 installer.
	if deps.installer != nil {
		// Walk EnabledPlugins in deterministic order to keep tests stable.
		keys := make([]string, 0, len(cfg.EnabledPlugins))
		for k := range cfg.EnabledPlugins {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, spec := range keys {
			plugin, marketplace, err := splitPluginSpec(spec)
			if err != nil {
				log.Printf("serf: skipping malformed enabledPlugins entry %q: %v", spec, err)
				continue
			}
			entry, lerr := deps.installer.Lookup(plugin, marketplace)
			if errors.Is(lerr, ErrPluginNotInstalled) {
				log.Printf(`serf: plugin %q is enabled but not installed; run 'serf plugin install %s' to install`, spec, spec)
				continue
			}
			if lerr != nil {
				return SessionConfig{}, fmt.Errorf("lookup plugin %q: %w", spec, lerr)
			}
			sc.EnabledPluginPaths = append(sc.EnabledPluginPaths, ResolvedPlugin{
				PluginID:  spec,
				CachePath: entry.InstallPath,
				Version:   entry.Version,
				Source:    PluginSourceEnabled,
			})
		}
	}
```

Add imports if missing:

```go
import (
	"log"
	"sort"
)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_' -v
```

Expected: PASS for all five subtests.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: BuildSessionConfig step 2 — resolve enabledPlugins"
```

---

## Task 9: `BuildSessionConfig` — append `--plugin-dir` entries (step 3)

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
func TestBuildSessionConfig_AppendsPluginDirs(t *testing.T) {
	dir := t.TempDir()
	flags := BootstrapFlags{PluginDirs: []string{filepath.Join(dir, "foo"), filepath.Join(dir, "bar")}}
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{}, nil
		},
	}
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, flags, deps)
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}
	if len(sc.EnabledPluginPaths) != 2 {
		t.Fatalf("want 2 entries, got %d", len(sc.EnabledPluginPaths))
	}
	if sc.EnabledPluginPaths[0].PluginID != "foo" || sc.EnabledPluginPaths[0].Source != PluginSourceCLI {
		t.Fatalf("entry 0 = %+v", sc.EnabledPluginPaths[0])
	}
	if sc.EnabledPluginPaths[0].Version != "ad-hoc" {
		t.Fatalf("entry 0 version = %q, want ad-hoc", sc.EnabledPluginPaths[0].Version)
	}
	if sc.EnabledPluginPaths[1].PluginID != "bar" {
		t.Fatalf("entry 1 PluginID = %q", sc.EnabledPluginPaths[1].PluginID)
	}
}

func TestBuildSessionConfig_EnabledThenPluginDir_Order(t *testing.T) {
	installer := &fakeInstaller{
		entries: map[string]installerEntry{
			"a@m": {InstallPath: "/cache/a", Version: "1"},
		},
	}
	flags := BootstrapFlags{PluginDirs: []string{"/tmp/devplug"}}
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{EnabledPlugins: map[string]any{"a@m": true}}, nil
		},
		installer: installer,
	}
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, flags, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.EnabledPluginPaths) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(sc.EnabledPluginPaths), sc.EnabledPluginPaths)
	}
	if sc.EnabledPluginPaths[0].Source != PluginSourceEnabled {
		t.Fatalf("entry 0 should be enabled, got %q", sc.EnabledPluginPaths[0].Source)
	}
	if sc.EnabledPluginPaths[1].Source != PluginSourceCLI {
		t.Fatalf("entry 1 should be plugin-dir, got %q", sc.EnabledPluginPaths[1].Source)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_AppendsPluginDirs|TestBuildSessionConfig_EnabledThenPluginDir_Order' -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/session_bootstrap.go`, after the enabledPlugins resolution loop, add:

```go
	// Step 3: append --plugin-dir entries in CLI order. Source: sp8 §3 / §9.
	for _, p := range flags.PluginDirs {
		sc.EnabledPluginPaths = append(sc.EnabledPluginPaths, ResolvedPlugin{
			PluginID:  filepath.Base(p),
			CachePath: p,
			Version:   "ad-hoc",
			Source:    PluginSourceCLI,
		})
	}
```

Add `"path/filepath"` to the imports if missing.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestBuildSessionConfig_AppendsPluginDirs|TestBuildSessionConfig_EnabledThenPluginDir_Order' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: BuildSessionConfig step 3 — append --plugin-dir entries"
```

---

## Task 10: `BuildSessionConfig` — store store/prompter/secure-store on SessionConfig

**Files:**
- Modify: `agent/session_bootstrap.go`
- Test: `agent/session_bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_bootstrap_test.go`:

```go
type fakePrompter struct{}

func (fakePrompter) PromptUserConfig(pluginID string, opts []UserConfigOption) (map[string]string, error) {
	return nil, nil
}

func TestBuildSessionConfig_CarriesStoresAndPrompter(t *testing.T) {
	cs := PluginConfigStore{}
	ss := SecureStore{}
	pr := fakePrompter{}
	flags := BootstrapFlags{
		Prompter: pr,
	}
	deps := bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{}, nil
		},
	}
	// Inject stores via deps once we add them.
	deps.pluginConfigStore = cs
	deps.secureStore = ss

	sc, err := BuildSessionConfig(ExecutionEnvironment{}, flags, deps)
	if err != nil {
		t.Fatal(err)
	}
	if sc.UserConfigPrompter == nil {
		t.Error("UserConfigPrompter not threaded")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestBuildSessionConfig_CarriesStoresAndPrompter -v
```

Expected: FAIL — `bootstrapDeps.pluginConfigStore` field missing.

- [ ] **Step 3: Write minimal implementation**

Add to `bootstrapDeps` struct:

```go
	pluginConfigStore PluginConfigStore
	secureStore       SecureStore
```

After the `sc :=` initialization in `BuildSessionConfig`, add:

```go
	sc.PluginConfigStore = deps.pluginConfigStore
	sc.SecureStore = deps.secureStore
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./agent/ -run TestBuildSessionConfig_CarriesStoresAndPrompter -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_bootstrap.go agent/session_bootstrap_test.go
git commit -m "agent: BuildSessionConfig threads stores and prompter to SessionConfig"
```

---

## Task 11: Permission-matcher construction in `NewSession`

**Files:**
- Modify: `agent/session.go` (in `NewSession`, where the session struct is initialized)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestNewSession_BuildsPermissionMatcher(t *testing.T) {
	cfg := SessionConfig{
		Permissions: PermissionsConfig{
			Deny: []string{"Bash(rm:*)"},
		},
		// other minimum fields needed by NewSession — copy from a passing
		// existing test like TestNewSession_Basic.
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if s.permissionMatcher == nil {
		t.Fatal("permissionMatcher should be set when cfg.Permissions has rules")
	}
}

func TestNewSession_NoPermissions_NilMatcher(t *testing.T) {
	cfg := SessionConfig{} // copy minimum from existing test
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if s.permissionMatcher != nil {
		t.Fatal("permissionMatcher should be nil when cfg.Permissions is empty")
	}
}

func TestNewSession_PluginSourcesConflict_Error(t *testing.T) {
	cfg := SessionConfig{
		PluginDirs:         []string{"/tmp/a"},
		EnabledPluginPaths: []ResolvedPlugin{{PluginID: "x@m", CachePath: "/tmp/b"}},
	}
	if _, err := NewSession(context.Background(), cfg); err == nil {
		t.Fatal("want error from validatePluginSources")
	}
}
```

Adjust the boilerplate `cfg` fields to match what `NewSession` already requires (look at existing `agent/session_test.go` for the minimum set).

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestNewSession_BuildsPermissionMatcher|TestNewSession_NoPermissions_NilMatcher|TestNewSession_PluginSourcesConflict_Error' -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, locate the start of `NewSession` and:

1. Right after the function entry, call:

```go
	if err := cfg.validatePluginSources(); err != nil {
		return nil, err
	}
```

2. When constructing the `Session` struct, add:

```go
	var matcher *PermissionMatcher
	if !cfg.Permissions.IsEmpty() {
		var err error
		matcher, err = NewPermissionMatcher(cfg.Permissions, env)
		if err != nil {
			return nil, fmt.Errorf("build permission matcher: %w", err)
		}
	}
```

3. Store the matcher on the `Session`:

```go
type Session struct {
	// ...existing fields...
	permissionMatcher *PermissionMatcher
	pluginUserConfigs map[string]*ResolvedUserConfig
}
```

Then in the init: `s.permissionMatcher = matcher` and `s.pluginUserConfigs = make(map[string]*ResolvedUserConfig)`.

If SP1's `PermissionsConfig` does not yet expose `IsEmpty()`, replace with `len(cfg.Permissions.Allow) == 0 && len(cfg.Permissions.Deny) == 0 && len(cfg.Permissions.Ask) == 0 && cfg.Permissions.DefaultMode == ""`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestNewSession_' -v
```

Expected: PASS (the new tests; existing ones should keep passing).

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: build permissionMatcher in NewSession"
```

---

## Task 12: Hook union — config-tier first, plugins last

**Files:**
- Modify: `agent/session.go` (where `HookRunner` is populated in `initPlugins` / `NewSession`)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestHookUnion_ConfigBeforePlugins(t *testing.T) {
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"PreToolUse": {{Matcher: "Bash", Command: "echo CONFIG"}},
			},
		},
		// Use a fake LoadedPlugin so we don't depend on SP7 fixture format.
	}
	pluginHooks := map[string][]HookConfig{
		"PreToolUse": {{Matcher: "Bash", Command: "echo PLUGIN"}},
	}
	runner := buildHookRunner(cfg, []loadedPluginForHooks{
		{name: "fake", hooks: pluginHooks, dir: "/tmp/fake"},
	})
	got := runner.HooksFor("PreToolUse")
	if len(got) != 2 {
		t.Fatalf("want 2 hooks, got %d", len(got))
	}
	if !strings.Contains(got[0].Command, "CONFIG") {
		t.Fatalf("hook[0] should be config-tier, got %q", got[0].Command)
	}
	if !strings.Contains(got[1].Command, "PLUGIN") {
		t.Fatalf("hook[1] should be plugin-tier, got %q", got[1].Command)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestHookUnion_ConfigBeforePlugins -v
```

Expected: FAIL — `buildHookRunner` / `loadedPluginForHooks` don't exist.

- [ ] **Step 3: Write minimal implementation**

Add to `agent/session.go` (or a new small file `agent/session_hooks.go` if you prefer file separation):

```go
// loadedPluginForHooks captures only the fields buildHookRunner needs.
// Production code populates this from the real LoadedPlugin (SP7).
type loadedPluginForHooks struct {
	name  string
	dir   string
	hooks map[string][]HookConfig
}

// buildHookRunner composes config-tier hooks (already merged by SP1) with
// plugin-tier hooks, in that order, per sp8 §3.1.
func buildHookRunner(cfg SessionConfig, plugins []loadedPluginForHooks) *HookRunner {
	runner := NewHookRunner()
	for event, hooks := range cfg.MergedConfig.Hooks {
		for _, h := range hooks {
			runner.Add(event, RegisteredHook{
				HookConfig: h,
				PluginName: "", // empty = config-tier
				PluginDir:  "",
			})
		}
	}
	for _, p := range plugins {
		for event, hooks := range p.hooks {
			for _, h := range hooks {
				runner.Add(event, RegisteredHook{
					HookConfig: h,
					PluginName: p.name,
					PluginDir:  p.dir,
				})
			}
		}
	}
	return runner
}
```

If `RegisteredHook` or `HookRunner.HooksFor` aren't yet exported by existing code, add a getter:

```go
func (r *HookRunner) HooksFor(event string) []RegisteredHook { return r.hooks[event] }
```

Then in `initPlugins` / `NewSession`, replace the existing hook-runner population with a call to `buildHookRunner`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestHookUnion_ConfigBeforePlugins -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: buildHookRunner composes config-tier then plugin-tier hooks"
```

---

## Task 13: MCP plugin-last layering

**Files:**
- Modify: `agent/session.go` (in `initMCP`)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestMCPLayering_PluginWinsOnKeyCollision(t *testing.T) {
	configTier := []MCPServerConfig{
		{Name: "foo", Command: "from-config", Type: "stdio"},
	}
	pluginTier := []MCPServerConfig{
		{Name: "foo", Command: "from-plugin", Type: "stdio"},
		{Name: "bar", Command: "only-plugin", Type: "stdio"},
	}
	merged := MergeMCPConfigsOrdered(configTier, pluginTier)
	byName := map[string]MCPServerConfig{}
	for _, c := range merged {
		byName[c.Name] = c
	}
	if byName["foo"].Command != "from-plugin" {
		t.Fatalf("foo should be plugin's value, got %q", byName["foo"].Command)
	}
	if _, ok := byName["bar"]; !ok {
		t.Fatal("bar should be present from plugin tier")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestMCPLayering_PluginWinsOnKeyCollision -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

Add to `agent/mcp_config.go`:

```go
// MergeMCPConfigsOrdered merges layers in increasing precedence order:
// each successive argument overwrites prior entries by Name.
// SP8 §3.2 mandates plugin-tier last.
func MergeMCPConfigsOrdered(layers ...[]MCPServerConfig) []MCPServerConfig {
	byName := map[string]MCPServerConfig{}
	order := []string{}
	for _, layer := range layers {
		for _, cfg := range layer {
			if _, exists := byName[cfg.Name]; !exists {
				order = append(order, cfg.Name)
			}
			byName[cfg.Name] = cfg
		}
	}
	out := make([]MCPServerConfig, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}
```

Update `initMCP` to call `MergeMCPConfigsOrdered(configTier, mcpJsonTier, cliTier, pluginTier)` instead of whatever current ordering exists. Locate the existing `MergeMCPConfigs(s.pluginMCPConfigs, configs)` call in `agent/session.go` and replace per the precedence in spec §3.2.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestMCPLayering_PluginWinsOnKeyCollision -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/mcp_config.go agent/session.go agent/session_test.go
git commit -m "agent: MergeMCPConfigsOrdered puts plugin tier last per SP8 §3.2"
```

---

## Task 14: Populate `pluginUserConfigs` map after `LoadPlugin`

**Files:**
- Modify: `agent/session.go` (in `initPlugins`)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestInitPlugins_PopulatesPluginUserConfigs(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "p1")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name": "p1",
		"version": "1.0.0",
		"userConfig": {
			"API_KEY": {"type": "string", "default": "abc"}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := SessionConfig{
		EnabledPluginPaths: []ResolvedPlugin{
			{PluginID: "p1@m", CachePath: pluginDir, Version: "1.0.0", Source: PluginSourceEnabled},
		},
		PluginConfigStore: PluginConfigStore{}, // empty: pulls defaults
		SecureStore:       SecureStore{},
		UserConfigPrompter: nonInteractivePrompterStub{},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	resolved, ok := s.pluginUserConfigs["p1@m"]
	if !ok {
		t.Fatal("expected pluginUserConfigs[p1@m]")
	}
	v, ok := resolved.Lookup("API_KEY")
	if !ok {
		t.Fatal("expected API_KEY in resolved values")
	}
	if v != "abc" {
		t.Fatalf("API_KEY = %q, want %q", v, "abc")
	}
}

type nonInteractivePrompterStub struct{}

func (nonInteractivePrompterStub) PromptUserConfig(string, []UserConfigOption) (map[string]string, error) {
	return nil, fmt.Errorf("non-interactive: cannot prompt")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestInitPlugins_PopulatesPluginUserConfigs -v
```

Expected: FAIL — `pluginUserConfigs` not populated.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`'s `initPlugins`, after the existing `LoadPlugin` call for each cache path, insert:

```go
	resolved, err := ResolveUserConfig(p.PluginID, lp.UserConfigOptions, s.cfg.PluginConfigStore, s.cfg.SecureStore)
	if err != nil {
		// Missing required key on non-interactive surface = fatal.
		if s.cfg.UserConfigPrompter == nil {
			return fmt.Errorf("resolve userConfig for %q: %w", p.PluginID, err)
		}
		// Try prompter.
		values, perr := s.cfg.UserConfigPrompter.PromptUserConfig(p.PluginID, lp.UserConfigOptions)
		if perr != nil {
			log.Printf("serf: skipping plugin %q (userConfig unresolved): %v", p.PluginID, perr)
			continue
		}
		resolved, err = ResolveUserConfigWithValues(p.PluginID, lp.UserConfigOptions, values, s.cfg.PluginConfigStore, s.cfg.SecureStore)
		if err != nil {
			return fmt.Errorf("apply userConfig values: %w", err)
		}
	}
	s.pluginUserConfigs[p.PluginID] = resolved
```

Replace `ResolveUserConfig` / `ResolveUserConfigWithValues` with the SP7-exported names if they differ.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestInitPlugins_PopulatesPluginUserConfigs -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: initPlugins populates pluginUserConfigs map"
```

---

## Task 15: Bind user-config resolver onto `HookRunner`

**Files:**
- Modify: `agent/session.go` and `agent/plugin_hooks.go`
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestHookRunner_UserConfigResolver_BoundToSession(t *testing.T) {
	rcv := &ResolvedUserConfig{} // populate via SP7 helper if available
	rcv.Set("KEY", "VALUE")

	runner := NewHookRunner()
	called := ""
	runner.SetUserConfigResolver(func(pluginName string) *ResolvedUserConfig {
		called = pluginName
		if pluginName == "p1@m" {
			return rcv
		}
		return nil
	})

	got := runner.LookupUserConfig("p1@m")
	if called != "p1@m" {
		t.Fatalf("resolver called with %q, want p1@m", called)
	}
	v, ok := got.Lookup("KEY")
	if !ok || v != "VALUE" {
		t.Fatalf("Lookup(KEY) = (%q,%v)", v, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestHookRunner_UserConfigResolver_BoundToSession -v
```

Expected: FAIL — `SetUserConfigResolver` / `LookupUserConfig` don't exist.

- [ ] **Step 3: Write minimal implementation**

In `agent/plugin_hooks.go` (or wherever `HookRunner` lives), add:

```go
type userConfigResolverFn func(pluginName string) *ResolvedUserConfig

// SetUserConfigResolver wires the per-plugin userConfig lookup used by
// SP5's hook executor and SP7's expansion. Source: sp8 §8.2.
func (r *HookRunner) SetUserConfigResolver(fn userConfigResolverFn) {
	r.userConfigResolver = fn
}

func (r *HookRunner) LookupUserConfig(pluginName string) *ResolvedUserConfig {
	if r.userConfigResolver == nil {
		return nil
	}
	return r.userConfigResolver(pluginName)
}
```

Add the `userConfigResolver userConfigResolverFn` field to `HookRunner` struct.

In `agent/session.go` after `initPlugins` populates `s.pluginUserConfigs`, wire:

```go
	s.hookRunner.SetUserConfigResolver(func(pluginName string) *ResolvedUserConfig {
		return s.pluginUserConfigs[pluginName]
	})
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestHookRunner_UserConfigResolver_BoundToSession -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/session.go agent/session_test.go
git commit -m "agent: wire pluginUserConfigs resolver onto HookRunner"
```

---

## Task 16: Permission enforcement wire-in inside `execTool`

**Files:**
- Modify: `agent/session.go` around the existing `execTool` (spec cites line 1275)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestExecTool_PermissionDeny_BlocksExecution(t *testing.T) {
	cfg := SessionConfig{
		Permissions: PermissionsConfig{Deny: []string{"Bash(rm:*)"}},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	call := ToolCall{Name: "Bash", Arguments: map[string]any{"command": "rm /tmp/x"}}
	res := s.execTool(context.Background(), call)
	if !res.IsError {
		t.Fatal("expected denial to be IsError")
	}
	if !strings.Contains(res.Output, "denied") && !strings.Contains(res.Output, "permission") {
		t.Fatalf("res.Output should mention denial: %q", res.Output)
	}
}

func TestExecTool_PermissionAllow_ExecutionProceeds(t *testing.T) {
	cfg := SessionConfig{
		Permissions: PermissionsConfig{Allow: []string{"Bash(echo:*)"}},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	call := ToolCall{Name: "Bash", Arguments: map[string]any{"command": "echo hi"}}
	res := s.execTool(context.Background(), call)
	if res.IsError {
		t.Fatalf("allowed call should not error: %v", res)
	}
}

func TestExecTool_PreToolUseUpdatedInput_BeforeMatcher(t *testing.T) {
	// PreToolUse rewrites command from "rm /tmp" to "rm /". The matcher
	// must see the rewritten value. Use a stub PreToolUse hook that mutates.
	t.Skip("requires SP5 updatedInput export; revisit when SP5 lands")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestExecTool_PermissionDeny_BlocksExecution|TestExecTool_PermissionAllow_ExecutionProceeds' -v
```

Expected: FAIL — denial path doesn't run yet.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, locate `execTool`. After the existing `RunPreToolUse` block (and any `updatedInput` application) and before the existing `argsJSON, _ := json.Marshal(call.Arguments)` line, insert:

```go
	// SP2/SP8 §7: permission enforcement after PreToolUse, before tool exec.
	if s.permissionMatcher != nil {
		decision := s.permissionMatcher.Evaluate(call.Name, call.Arguments)
		switch decision.Mode {
		case PermissionDeny:
			s.runPermissionDeniedHook(ctx, call, decision)
			return s.permissionDeniedResult(call, decision)
		case PermissionAsk:
			resolved := s.resolveAsk(ctx, call, decision)
			if resolved.Mode == PermissionDeny {
				return s.permissionDeniedResult(call, resolved)
			}
			// fall through to execution on Allow
		case PermissionAllow:
			// fall through
		}
	}
```

Add helper methods (SP8 owns the wiring; SP2 owns the semantics):

```go
func (s *Session) permissionDeniedResult(call ToolCall, d PermissionDecision) ToolExecResult {
	reason := d.Reason
	if reason == "" {
		reason = "permission denied"
	}
	return ToolExecResult{
		IsError: true,
		Output:  fmt.Sprintf("tool call %q denied: %s", call.Name, reason),
	}
}

func (s *Session) runPermissionDeniedHook(ctx context.Context, call ToolCall, d PermissionDecision) {
	if s.hookRunner == nil {
		return
	}
	input := s.hookInput("PermissionDenied")
	input["tool_name"] = call.Name
	input["tool_input"] = call.Arguments
	input["tool_use_id"] = call.ID
	input["denial_reason"] = d.Reason
	_ = s.hookRunner.Run(ctx, "PermissionDenied", input)
}

func (s *Session) resolveAsk(ctx context.Context, call ToolCall, d PermissionDecision) PermissionDecision {
	if s.hookRunner != nil {
		input := s.hookInput("PermissionRequest")
		input["tool_name"] = call.Name
		input["tool_input"] = call.Arguments
		input["tool_use_id"] = call.ID
		input["permission_rule"] = d.Rule
		if out, err := s.hookRunner.Run(ctx, "PermissionRequest", input); err == nil {
			if out.PermissionDecision != "" {
				return PermissionDecision{Mode: PermissionMode(out.PermissionDecision), Reason: out.PermissionDecisionReason, Rule: d.Rule}
			}
		}
	}
	// Fall back to surface policy.
	switch s.cfg.PermissionAskFallback {
	case AskFallbackDeny:
		return PermissionDecision{Mode: PermissionDeny, Reason: "ask fallback deny", Rule: d.Rule}
	case AskFallbackInteractive:
		// Real interactive prompt handled by surface; SP8 returns allow
		// unless the surface vetoed. SP2 §11 owns the surface integration.
		return PermissionDecision{Mode: PermissionAllow, Rule: d.Rule}
	default:
		return PermissionDecision{Mode: PermissionDeny, Reason: "no fallback policy", Rule: d.Rule}
	}
}
```

Names like `PermissionDecision`, `PermissionMode`, `PermissionDeny`, etc. come from SP2. If they're not yet exported, declare them inline at the bottom of `session_bootstrap.go` with a `// TODO SP2` comment.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestExecTool_PermissionDeny_BlocksExecution|TestExecTool_PermissionAllow_ExecutionProceeds' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: enforce permissions in execTool between PreToolUse and exec"
```

---

## Task 17: Fire site — `PostToolUseFailure`

**Files:**
- Modify: `agent/session.go` (in `execTool` after `PostToolUse` block)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestExecTool_FiresPostToolUseFailure_OnError(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "fired")
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"PostToolUseFailure": {{
					Matcher: "Bash",
					Command: "bash -c 'echo fired > " + sentinel + "'",
				}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Force a failing tool call. Use a Bash tool with a guaranteed-fail command.
	call := ToolCall{Name: "Bash", Arguments: map[string]any{"command": "false"}}
	_ = s.execTool(context.Background(), call)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("PostToolUseFailure hook should have written %s: %v", sentinel, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestExecTool_FiresPostToolUseFailure_OnError -v
```

Expected: FAIL — sentinel never written.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, locate the existing `PostToolUse` hook invocation in `execTool` (after `s.reg.ExecuteCall`). Replace with:

```go
	if res.IsError {
		failInput := s.hookInput("PostToolUseFailure")
		failInput["tool_name"] = call.Name
		failInput["tool_input"] = call.Arguments
		failInput["tool_error"] = res.Output
		failInput["tool_use_id"] = call.ID
		_, _ = s.hookRunner.Run(ctx, "PostToolUseFailure", failInput)
	} else {
		// Existing PostToolUse path.
		_, _ = s.hookRunner.Run(ctx, "PostToolUse", s.postToolUseInput(call, res))
	}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestExecTool_FiresPostToolUseFailure_OnError -v
```

Expected: PASS (assumes `bash` available; if not in CI, skip with `t.Skip` when `exec.LookPath("bash") != nil` errors).

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: fire PostToolUseFailure after errored tool calls"
```

---

## Task 18: Fire site — `PostToolBatch`

**Files:**
- Modify: `agent/session.go` (in the round-loop, after the parallel-tool-call results assemble)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestRoundLoop_FiresPostToolBatch(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "batch")
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"PostToolBatch": {{
					Command: "bash -c 'echo batch > " + sentinel + "'",
				}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	results := []ToolExecResult{
		{Output: "ok-1"},
		{Output: "ok-2"},
	}
	s.fireToolBatch(context.Background(), []ToolCall{{Name: "X"}, {Name: "Y"}}, results)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("PostToolBatch hook should have written %s: %v", sentinel, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestRoundLoop_FiresPostToolBatch -v
```

Expected: FAIL — `fireToolBatch` doesn't exist.

- [ ] **Step 3: Write minimal implementation**

Add to `agent/session.go`:

```go
func (s *Session) fireToolBatch(ctx context.Context, calls []ToolCall, results []ToolExecResult) {
	if s.hookRunner == nil {
		return
	}
	input := s.hookInput("PostToolBatch")
	input["tool_results"] = toBatchToolResults(calls, results)
	_, _ = s.hookRunner.Run(ctx, "PostToolBatch", input)
}

func toBatchToolResults(calls []ToolCall, results []ToolExecResult) []map[string]any {
	out := make([]map[string]any, len(results))
	for i, r := range results {
		entry := map[string]any{
			"tool_use_id": calls[i].ID,
			"tool_name":   calls[i].Name,
			"is_error":    r.IsError,
			"output":      r.Output,
		}
		out[i] = entry
	}
	return out
}
```

In the existing round-loop where `for i := range calls { results[i] = ... }` finishes (spec cites session.go:2065-2095), add a call:

```go
	s.fireToolBatch(ctx, calls, results)
```

right before `appendTurn(TurnToolResults, ...)`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestRoundLoop_FiresPostToolBatch -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: fire PostToolBatch after parallel tool calls resolve"
```

---

## Task 19: Fire site — `StopFailure`

**Files:**
- Modify: `agent/session.go` (every error-return path in `processOneInput` after retry exhaustion)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestProcessOneInput_FiresStopFailure_OnAPIError(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "stop")
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"StopFailure": {{
					Command: "bash -c 'echo stop > " + sentinel + "'",
				}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.fireStopFailure(context.Background(), fmt.Errorf("simulated API timeout"))
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("StopFailure should have fired: %v", err)
	}
}

func TestClassifyAPIError(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("connection timeout"), "timeout"},
		{fmt.Errorf("rate limit exceeded"), "rate_limit"},
		{fmt.Errorf("internal server error"), "server_error"},
		{fmt.Errorf("something else"), "unknown"},
	}
	for _, tc := range tests {
		got := classifyAPIError(tc.err)
		if got != tc.want {
			t.Errorf("classifyAPIError(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run 'TestProcessOneInput_FiresStopFailure_OnAPIError|TestClassifyAPIError' -v
```

Expected: FAIL — neither helper exists.

- [ ] **Step 3: Write minimal implementation**

Add to `agent/session.go`:

```go
func (s *Session) fireStopFailure(ctx context.Context, apiErr error) {
	if s.hookRunner == nil {
		return
	}
	msg := apiErr.Error()
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	input := s.hookInput("StopFailure")
	input["error_type"] = classifyAPIError(apiErr)
	input["error_message"] = msg
	_, _ = s.hookRunner.Run(ctx, "StopFailure", input)
}

func classifyAPIError(err error) string {
	if err == nil {
		return "unknown"
	}
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "timeout"):
		return "timeout"
	case strings.Contains(m, "rate limit") || strings.Contains(m, "rate_limit"):
		return "rate_limit"
	case strings.Contains(m, "server error") || strings.Contains(m, "500"):
		return "server_error"
	default:
		return "unknown"
	}
}
```

In `processOneInput`, at every `return ..., err` after retries are exhausted, insert `s.fireStopFailure(ctx, err)` immediately before the return.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run 'TestProcessOneInput_FiresStopFailure_OnAPIError|TestClassifyAPIError' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: fire StopFailure on retry-exhausted API errors"
```

---

## Task 20: Fire site — `SubagentStart`

**Files:**
- Modify: `agent/subagents.go` (`spawnAgent`)
- Test: `agent/subagents_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/subagents_test.go`:

```go
func TestSpawnAgent_FiresSubagentStart(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sub")
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"SubagentStart": {{
					Command: "bash -c 'echo sub > " + sentinel + "'",
				}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.spawnAgent(context.Background(), spawnRequest{
		AgentType: "general-purpose",
		Prompt:    "do something",
	})
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("SubagentStart should have fired: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestSpawnAgent_FiresSubagentStart -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/subagents.go`'s `spawnAgent`, after the child `NewSession` returns and before `go sub.run(...)`, insert:

```go
	if s.hookRunner != nil {
		input := s.hookInput("SubagentStart")
		input["agent_id"] = sub.id
		input["agent_type"] = req.AgentType
		if input["agent_type"] == "" {
			input["agent_type"] = "general-purpose"
		}
		input["prompt"] = req.Prompt
		_, _ = s.hookRunner.Run(ctx, "SubagentStart", input)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestSpawnAgent_FiresSubagentStart -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/subagents.go agent/subagents_test.go
git commit -m "agent: fire SubagentStart when spawnAgent creates a subagent"
```

---

## Task 21: Fire site — `UserPromptExpansion`

**Files:**
- Modify: `agent/session.go` in `processOneInput` at skill-resolution site
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestProcessOneInput_FiresUserPromptExpansion_OnSkill(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "expand")
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"UserPromptExpansion": {{
					Command: "bash -c 'echo expand > " + sentinel + "'",
				}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.fireUserPromptExpansion(context.Background(), expansionInfo{
		ExpansionType: "skill",
		CommandName:   "/foo",
		CommandArgs:   []string{"x"},
		CommandSource: "/path/to/skill.md",
		Prompt:        "expanded prompt body",
	})
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("UserPromptExpansion should fire: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestProcessOneInput_FiresUserPromptExpansion_OnSkill -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

Add to `agent/session.go`:

```go
type expansionInfo struct {
	ExpansionType string
	CommandName   string
	CommandArgs   []string
	CommandSource string
	Prompt        string
}

func (s *Session) fireUserPromptExpansion(ctx context.Context, e expansionInfo) {
	if s.hookRunner == nil {
		return
	}
	input := s.hookInput("UserPromptExpansion")
	input["expansion_type"] = e.ExpansionType
	input["command_name"] = e.CommandName
	input["command_args"] = e.CommandArgs
	input["command_source"] = e.CommandSource
	input["prompt"] = e.Prompt
	_, _ = s.hookRunner.Run(ctx, "UserPromptExpansion", input)
}
```

In `processOneInput`, locate where `ActivatedSkillBodies` is populated and the expanded text becomes `TurnUserInput`. Insert:

```go
	for _, skill := range activatedSkills {
		s.fireUserPromptExpansion(ctx, expansionInfo{
			ExpansionType: "skill",
			CommandName:   skill.Name,
			CommandArgs:   skill.Args,
			CommandSource: skill.Path,
			Prompt:        skill.Body,
		})
	}
```

Adjust field names (`activatedSkills`, `skill.Name`) to match existing serf identifiers.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestProcessOneInput_FiresUserPromptExpansion_OnSkill -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: fire UserPromptExpansion when slash command / skill expands"
```

---

## Task 22: Fire site — `PostCompact`

**Files:**
- Modify: `agent/context_strategy.go` after compaction returns
- Test: `agent/context_strategy_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/context_strategy_test.go`:

```go
func TestCompactStrategy_FiresPostCompact(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "comp")
	cfg := SessionConfig{
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"PostCompact": {{
					Command: "bash -c 'echo comp > " + sentinel + "'",
				}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	strat := &CompactStrategy{}
	out, err := strat.ManageContext(context.Background(), s, contextManageInput{ForceCompact: true})
	if err != nil {
		t.Fatalf("ManageContext: %v", err)
	}
	if !out.Compacted {
		t.Fatal("expected Compacted=true with ForceCompact")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("PostCompact should fire: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestCompactStrategy_FiresPostCompact -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/context_strategy.go`, after the existing `s.cm.MaybeCompact` call inside `CompactStrategy.ManageContext`, capture the compaction result and add:

```go
	if compacted {
		input := s.hookInput("PostCompact")
		input["compact_trigger"] = "auto"
		_, _ = s.hookRunner.Run(ctx, "PostCompact", input)
	}
	return contextManageOutput{Compacted: compacted}, nil
```

Extend the strategy's return type to include `Compacted bool` if it doesn't already.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestCompactStrategy_FiresPostCompact -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/context_strategy.go agent/context_strategy_test.go
git commit -m "agent: fire PostCompact after CompactStrategy runs compaction"
```

---

## Task 23: Subagent inherits parent's merged config and resolved plugins

**Files:**
- Modify: `agent/subagents.go`
- Test: `agent/subagents_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/subagents_test.go`:

```go
func TestSpawnAgent_ChildInheritsPermissions(t *testing.T) {
	cfg := SessionConfig{
		Permissions: PermissionsConfig{Deny: []string{"Bash(rm:*)"}},
	}
	parent, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.spawnAgent(context.Background(), spawnRequest{
		AgentType: "general-purpose",
		Prompt:    "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.permissionMatcher == nil {
		t.Fatal("child should inherit a permission matcher")
	}
	// Child should deny the same call.
	call := ToolCall{Name: "Bash", Arguments: map[string]any{"command": "rm /tmp/y"}}
	res := child.execTool(context.Background(), call)
	if !res.IsError {
		t.Fatal("child should deny Bash(rm:*) inherited from parent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestSpawnAgent_ChildInheritsPermissions -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/subagents.go`'s `spawnAgent`, before constructing the child's `SessionConfig`, build it from the parent:

```go
	childCfg := SessionConfig{
		// Existing fields the original spawnAgent populated, plus:
		Permissions:           s.cfg.Permissions,
		PermissionAskFallback: s.cfg.PermissionAskFallback,
		MergedConfig:          s.cfg.MergedConfig,
		EnabledPluginPaths:    s.cfg.EnabledPluginPaths, // shared cache-path list; LoadPlugin runs per child
		PluginConfigStore:     s.cfg.PluginConfigStore,
		SecureStore:           s.cfg.SecureStore,
		UserConfigPrompter:    s.cfg.UserConfigPrompter,
		IsRemote:              s.cfg.IsRemote,
	}
```

Pass `childCfg` into `NewSession` for the child.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestSpawnAgent_ChildInheritsPermissions -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/subagents.go agent/subagents_test.go
git commit -m "agent: subagent inherits parent's merged config and permissions"
```

---

## Task 24: Backward-compat fallback — `PluginDirs` → `EnabledPluginPaths`

**Files:**
- Modify: `agent/session.go` (in `NewSession`)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestNewSession_PluginDirsMappedToResolvedSet(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "legacyplug")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"legacyplug","version":"0.1"}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := SessionConfig{PluginDirs: []string{pluginDir}}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if len(s.cfg.EnabledPluginPaths) != 1 {
		t.Fatalf("PluginDirs should have been mapped onto EnabledPluginPaths, got %v", s.cfg.EnabledPluginPaths)
	}
	if s.cfg.EnabledPluginPaths[0].Source != PluginSourceCLI {
		t.Fatalf("source = %q, want plugin-dir", s.cfg.EnabledPluginPaths[0].Source)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestNewSession_PluginDirsMappedToResolvedSet -v
```

Expected: FAIL — mapping not yet performed.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`'s `NewSession`, right after `validatePluginSources`:

```go
	if len(cfg.EnabledPluginPaths) == 0 && len(cfg.PluginDirs) > 0 {
		for _, p := range cfg.PluginDirs {
			cfg.EnabledPluginPaths = append(cfg.EnabledPluginPaths, ResolvedPlugin{
				PluginID:  filepath.Base(p),
				CachePath: p,
				Version:   "ad-hoc",
				Source:    PluginSourceCLI,
			})
		}
		cfg.PluginDirs = nil
	}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestNewSession_PluginDirsMappedToResolvedSet -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: NewSession maps legacy PluginDirs to EnabledPluginPaths"
```

---

## Task 25: Name-collision rule — `--plugin-dir` overrides `enabledPlugins`

**Files:**
- Modify: `agent/session.go` (in `initPlugins`)
- Test: `agent/session_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestInitPlugins_PluginDirShadowsEnabled(t *testing.T) {
	dir := t.TempDir()
	enabledDir := filepath.Join(dir, "enabled")
	cliDir := filepath.Join(dir, "cli")
	for _, d := range []string{enabledDir, cliDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		manifest := fmt.Sprintf(`{"name":"foo","version":"src-%s"}`, filepath.Base(d))
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := SessionConfig{
		EnabledPluginPaths: []ResolvedPlugin{
			{PluginID: "foo@m", CachePath: enabledDir, Version: "1.0.0", Source: PluginSourceEnabled},
			{PluginID: "foo", CachePath: cliDir, Version: "ad-hoc", Source: PluginSourceCLI},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The plugin named "foo" should resolve to the cliDir's manifest version.
	got, ok := s.loadedPlugins["foo"]
	if !ok {
		t.Fatal("plugin foo not loaded")
	}
	if !strings.Contains(got.Version, "cli") {
		t.Fatalf("plugin foo version = %q, want it to be from cliDir", got.Version)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestInitPlugins_PluginDirShadowsEnabled -v
```

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`'s `initPlugins`, replace the existing iteration with:

```go
	loaded := map[string]*LoadedPlugin{}
	for _, p := range cfg.EnabledPluginPaths {
		lp, err := LoadPlugin(p.CachePath)
		if err != nil {
			log.Printf("serf: skipping plugin at %s: %v", p.CachePath, err)
			continue
		}
		// Name collision: --plugin-dir wins, but enabled comes first in our slice,
		// so any later same-name entry simply overwrites. Log when overriding.
		if existing, dup := loaded[lp.Name]; dup {
			log.Printf("serf: --plugin-dir %s overrides enabledPlugins entry %q for this session", p.CachePath, existing.SourceID)
		}
		lp.SourceID = p.PluginID
		lp.Version = p.Version
		loaded[lp.Name] = lp
	}
	s.loadedPlugins = loaded
```

`SourceID` is a new field on `LoadedPlugin` (or use existing equivalent if SP7 named it differently).

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestInitPlugins_PluginDirShadowsEnabled -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "agent: --plugin-dir name collision shadows enabledPlugins entry"
```

---

## Task 26: CLI wiring — `cmd/serf` flag registration

**Files:**
- Modify: `cmd/serf/main.go`
- Test: smoke test via `go build ./cmd/serf`

- [ ] **Step 1: Locate the existing flag-registration block**

Read `cmd/serf/main.go` and find where existing flags like `--plugin-dir` are registered (likely a `flag.Var` or `pflag.StringArrayVar`).

- [ ] **Step 2: Add three new flag registrations**

Insert next to the existing `--plugin-dir` registration:

```go
	cmd.Flags().StringArrayVar(&runFlags.ConfigPaths, "config", nil,
		"Path to a serf config.json (repeatable; merged after global/project)")
	cmd.Flags().StringArrayVar(&runFlags.TrustMarketplaces, "trust-marketplace", nil,
		"Trust a project-declared marketplace by name (repeatable)")
	cmd.Flags().StringArrayVar(&runFlags.PluginOptionsRaw, "plugin-option", nil,
		"Set a plugin userConfig value: --plugin-option <plugin>.<key>=<value> (repeatable)")
```

If the existing CLI uses stdlib `flag` instead of cobra, adapt accordingly:

```go
	flag.Var(&runFlags.ConfigPaths, "config", "...")
```

- [ ] **Step 3: Add the field declarations to runFlags struct**

In `cmd/serf/run.go` (or the same file if `runFlags` lives there):

```go
type runFlags struct {
	// ...existing fields...
	ConfigPaths       []string
	TrustMarketplaces []string
	PluginOptionsRaw  []string
}
```

- [ ] **Step 4: Build to verify no compile errors**

```bash
go build ./cmd/serf
```

Expected: clean exit.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/main.go cmd/serf/run.go
git commit -m "cmd/serf: register --config, --trust-marketplace, --plugin-option flags"
```

---

## Task 27: CLI wiring — `cmd/serf/run.go` calls `BuildSessionConfig`

**Files:**
- Modify: `cmd/serf/run.go` (function `run`, spec cites line 68)
- Test: build + an existing serf integration test stays green

- [ ] **Step 1: Add the wiring**

In `cmd/serf/run.go`'s `run()`, between the env-construction block and the existing `agent.SessionConfig{...}` literal, add:

```go
	bootstrap, err := agent.BuildSessionConfig(env, agent.BootstrapFlags{
		ConfigPaths:       runFlags.ConfigPaths,
		PluginDirs:        runFlags.PluginDirs,
		TrustMarketplaces: runFlags.TrustMarketplaces,
		PluginOptions:     parsePluginOptions(runFlags.PluginOptionsRaw),
		Prompter:          chooseCLIPrompter(),
		AskFallback:       chooseAskFallback(),
		IsRemote:          false,
		WatchConfig:       false,
	}, defaultBootstrapDepsForCLI())
	if err != nil {
		return fmt.Errorf("build session config: %w", err)
	}
```

Below, when constructing the existing `SessionConfig` literal, copy across the SP8 fields:

```go
	sc := agent.SessionConfig{
		// existing fields preserved as-is, plus:
		Permissions:           bootstrap.Permissions,
		PermissionAskFallback: bootstrap.PermissionAskFallback,
		MergedConfig:          bootstrap.MergedConfig,
		EnabledPluginPaths:    bootstrap.EnabledPluginPaths,
		PluginConfigStore:     bootstrap.PluginConfigStore,
		SecureStore:           bootstrap.SecureStore,
		UserConfigPrompter:    bootstrap.UserConfigPrompter,
		WatchConfig:           bootstrap.WatchConfig,
		IsRemote:              false,
	}
```

Add the small helpers (paste at the bottom of `run.go`):

```go
func parsePluginOptions(raw []string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, s := range raw {
		dot := strings.Index(s, ".")
		eq := strings.Index(s, "=")
		if dot <= 0 || eq <= dot+1 {
			fmt.Fprintf(os.Stderr, "serf: ignoring malformed --plugin-option %q\n", s)
			continue
		}
		plugin := s[:dot]
		key := s[dot+1 : eq]
		val := s[eq+1:]
		if out[plugin] == nil {
			out[plugin] = map[string]string{}
		}
		out[plugin][key] = val
	}
	return out
}

func chooseCLIPrompter() agent.UserConfigPrompter {
	if isStdinTTY() {
		return agent.NewCLIPrompter(os.Stdin, os.Stderr)
	}
	return agent.NewNonInteractivePrompter()
}

func chooseAskFallback() agent.AskFallback {
	if isStdinTTY() && !runFlags.PrintMode {
		return agent.AskFallbackInteractive
	}
	return agent.AskFallbackDeny
}

func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func defaultBootstrapDepsForCLI() agent.BootstrapDepsExported {
	// Real installer / trust manager constructed once per process at startup.
	return agent.BootstrapDepsExported{
		Installer: plugins.NewInstaller(),
		Trust:     plugins.NewTrustManager(),
	}
}
```

`BootstrapDepsExported` is a public sibling of the internal `bootstrapDeps`. Add it to `agent/session_bootstrap.go`:

```go
type BootstrapDepsExported struct {
	Installer pluginInstaller
	Trust     trustEnforcer
}
```

And add an overload `BuildSessionConfigWithDeps(env, flags, BootstrapDepsExported) (SessionConfig, error)` that internally builds the unexported `bootstrapDeps`.

- [ ] **Step 2: Build**

```bash
go build ./cmd/serf
```

Expected: clean.

- [ ] **Step 3: Run existing serf smoke test**

```bash
go test ./agent/ -run TestNewSession_Basic -v
```

Expected: PASS (no regression).

- [ ] **Step 4: Commit**

```bash
git add cmd/serf/run.go agent/session_bootstrap.go
git commit -m "cmd/serf: call BuildSessionConfig and thread merged fields into SessionConfig"
```

---

## Task 28: CLI wiring — `cmd/serf/serve.go`

**Files:**
- Modify: `cmd/serf/serve.go` (function `serve`, spec cites line 63)

- [ ] **Step 1: Apply the same wiring as `run.go`**

Locate the existing `agent.SessionConfig{...}` literal in `serve.go`. Wrap it with a `BuildSessionConfig` call and merge fields identically to Task 27.

- [ ] **Step 2: Build**

```bash
go build ./cmd/serf
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf/serve.go
git commit -m "cmd/serf: wire BuildSessionConfig in serve (daemon) mode"
```

---

## Task 29: CLI wiring — `cmd/serf-tui/embedded.go`

**Files:**
- Modify: `cmd/serf-tui/embedded.go` (spec cites lines 126, 174, 325)

- [ ] **Step 1: Apply the same wiring at each of the three `SessionConfig` sites**

For each of the three existing `agent.SessionConfig{...}` literals, prepend a `BuildSessionConfig` call and merge SP8 fields. Use `agent.TUIPrompter` and `agent.AskFallbackInteractive`. `IsRemote: false`.

```go
	bootstrap, err := agent.BuildSessionConfigWithDeps(env, agent.BootstrapFlags{
		ConfigPaths: tuiFlags.ConfigPaths,
		PluginDirs:  tuiFlags.PluginDirs,
		Prompter:    agent.NewTUIPrompter(tuiApp),
		AskFallback: agent.AskFallbackInteractive,
		IsRemote:    false,
	}, agent.BootstrapDepsExported{
		Installer: plugins.NewInstaller(),
		Trust:     plugins.NewTrustManager(),
	})
	if err != nil {
		return err
	}
```

Add `--config` flag registration to TUI's flag set if it doesn't already have one.

- [ ] **Step 2: Build**

```bash
go build ./cmd/serf-tui
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-tui/embedded.go
git commit -m "cmd/serf-tui: wire BuildSessionConfig at all three session sites"
```

---

## Task 30: CLI wiring — `cmd/serf-hub/web.go`

**Files:**
- Modify: `cmd/serf-hub/web.go`

- [ ] **Step 1: Extend `WebConfig`**

Add:

```go
type WebConfig struct {
	// ...existing fields...
	ConfigPaths []string
}
```

- [ ] **Step 2: Thread merged config into the Spawner request**

Locate the Spawner's `SpawnRequest` formation. Add the merged-config-derived flags to the request:

```go
	req.Flags = append(req.Flags, "--config")
	req.Flags = append(req.Flags, h.config.ConfigPaths...)
```

If the Hub spawns serf as a subprocess (per spec §5.3), this is the simplest correct wiring: the spawned serf process re-runs `BuildSessionConfig` itself.

- [ ] **Step 3: Build**

```bash
go build ./cmd/serf-hub
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/web.go
git commit -m "cmd/serf-hub: pass --config through to spawned serf processes"
```

---

## Task 31: CLI wiring — `cmd/serfeval/main.go`

**Files:**
- Modify: `cmd/serfeval/main.go` (spec cites lines 206, 231)

- [ ] **Step 1: Apply the same wiring**

Same as Task 27, but with `agent.NewNonInteractivePrompter()` and `agent.AskFallbackDeny`. `IsRemote: false`. Add `--config` flag registration.

- [ ] **Step 2: Build**

```bash
go build ./cmd/serfeval
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/serfeval/main.go
git commit -m "cmd/serfeval: wire BuildSessionConfig with non-interactive prompter"
```

---

## Task 32: Test fixture — `agent/testdata/plugins/sp8-hookparity/`

**Files:**
- Create: `agent/testdata/plugins/sp8-hookparity/plugin.json`
- Create: `agent/testdata/plugins/sp8-hookparity/hooks/*.sh` (one per event)

- [ ] **Step 1: Create the fixture directory and manifest**

```bash
mkdir -p agent/testdata/plugins/sp8-hookparity/hooks
```

Create `agent/testdata/plugins/sp8-hookparity/plugin.json`:

```json
{
  "name": "sp8-hookparity",
  "version": "1.0.0",
  "hooks": {
    "PostToolUseFailure": [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/posttool-fail.sh"}],
    "PostToolBatch":      [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/posttool-batch.sh"}],
    "StopFailure":        [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/stop-fail.sh"}],
    "SubagentStart":      [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/subagent-start.sh"}],
    "UserPromptExpansion":[{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/expand.sh"}],
    "PostCompact":        [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/compact.sh"}],
    "ConfigChange":       [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/configchange.sh"}],
    "PreToolUse":         [{"command": "${CLAUDE_PLUGIN_ROOT}/hooks/pretool.sh"}]
  }
}
```

- [ ] **Step 2: Create each hook script (8 files)**

Each script writes a sentinel file whose path comes from an env var the test sets. Example for `posttool-fail.sh`:

```bash
#!/bin/sh
echo "$1" > "$SP8_TEST_SENTINEL_PostToolUseFailure"
```

Repeat with the matching env name for the seven other scripts (`posttool-batch.sh`, `stop-fail.sh`, etc.).

- [ ] **Step 3: Make scripts executable**

```bash
chmod +x agent/testdata/plugins/sp8-hookparity/hooks/*.sh
```

- [ ] **Step 4: Commit**

```bash
git add agent/testdata/plugins/sp8-hookparity/
git commit -m "test: sp8-hookparity fixture plugin (one hook per new SP5 event)"
```

---

## Task 33: Test fixture — `agent/testdata/marketplaces/sp8-basic/`

**Files:**
- Create: `agent/testdata/marketplaces/sp8-basic/.claude-plugin/marketplace.json`
- Create: a small plugin payload inside the same directory

- [ ] **Step 1: Create the marketplace manifest**

```bash
mkdir -p agent/testdata/marketplaces/sp8-basic/.claude-plugin
mkdir -p agent/testdata/marketplaces/sp8-basic/plugins/sp8-basic
```

Create `agent/testdata/marketplaces/sp8-basic/.claude-plugin/marketplace.json`:

```json
{
  "name": "sp8-basic",
  "owner": {"name": "test", "email": "test@example.com"},
  "metadata": {"pluginRoot": "plugins"},
  "plugins": [
    {"name": "sp8-basic", "source": "./plugins/sp8-basic"}
  ]
}
```

Create `agent/testdata/marketplaces/sp8-basic/plugins/sp8-basic/plugin.json`:

```json
{
  "name": "sp8-basic",
  "version": "1.0.0",
  "hooks": {
    "PreToolUse": [{"command": "echo BASIC_FIRED >> $SP8_BASIC_SENTINEL"}]
  }
}
```

- [ ] **Step 2: Commit**

```bash
git add agent/testdata/marketplaces/sp8-basic/
git commit -m "test: sp8-basic marketplace fixture for end-to-end install/run test"
```

---

## Task 34: E2E test 1 — Marketplace add → install → run → uninstall

**Files:**
- Create: `agent/integration_sp8_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/integration_sp8_test.go`:

```go
package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prime-radiant-inc/serf/internal/plugins"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func TestSP8_MarketplaceAddInstallRunUninstall(t *testing.T) {
	requireBash(t)

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	sentinel := filepath.Join(t.TempDir(), "fired")
	t.Setenv("SP8_BASIC_SENTINEL", sentinel)

	marketSrc, _ := filepath.Abs("testdata/marketplaces/sp8-basic")
	mgr := plugins.NewMarketplaceManager(home)
	if err := mgr.Add("sp8-basic", plugins.MarketplaceSource{Type: "directory", Path: marketSrc}); err != nil {
		t.Fatalf("add marketplace: %v", err)
	}
	inst := plugins.NewInstaller(home)
	if err := inst.Install("sp8-basic", "sp8-basic"); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Build session via SP8 helper.
	flags := BootstrapFlags{}
	env := ExecutionEnvironment{WorkingDir: t.TempDir()}
	sc, err := BuildSessionConfig(env, flags, bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{EnabledPlugins: map[string]any{"sp8-basic@sp8-basic": true}}, nil
		},
		installer: installerAdapter{inst},
	})
	if err != nil {
		t.Fatalf("BuildSessionConfig: %v", err)
	}

	s, err := NewSession(context.Background(), sc)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "true"}})

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel not written: %v", err)
	}
	if !strings.Contains(string(data), "BASIC_FIRED") {
		t.Fatalf("sentinel %q does not contain BASIC_FIRED", data)
	}

	// Uninstall and re-build a session; the hook must not fire.
	if err := inst.Uninstall("sp8-basic", "sp8-basic"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	_ = os.Remove(sentinel)

	sc2, err := BuildSessionConfig(env, flags, bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{EnabledPlugins: map[string]any{"sp8-basic@sp8-basic": true}}, nil
		},
		installer: installerAdapter{inst},
	})
	if err != nil {
		t.Fatalf("post-uninstall BuildSessionConfig: %v", err)
	}
	s2, err := NewSession(context.Background(), sc2)
	if err != nil {
		t.Fatalf("post-uninstall NewSession: %v", err)
	}
	_ = s2.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "true"}})

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("hook fired after uninstall; sentinel exists")
	}
}

// installerAdapter is the test shim from real *plugins.Installer to pluginInstaller.
type installerAdapter struct {
	inner *plugins.Installer
}

func (a installerAdapter) Lookup(plugin, marketplace string) (installerEntry, error) {
	e, err := a.inner.Lookup(plugin, marketplace)
	if err != nil {
		return installerEntry{}, err
	}
	return installerEntry{InstallPath: e.InstallPath, Version: e.Version}, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestSP8_MarketplaceAddInstallRunUninstall -v
```

Expected: FAIL (will fail until SP3/SP4 are merged; that's the integration boundary).

- [ ] **Step 3: Commit the test even if red until SP3/SP4 land**

```bash
git add agent/integration_sp8_test.go
git commit -m "test: SP8 end-to-end add/install/run/uninstall"
```

Note: this test is the integration gate. It stays red on this branch until SP3/SP4 merge to main. Document in commit message.

---

## Task 35: E2E test 2 — Each new lifecycle event fires

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append the test**

```go
func TestSP8_HookEventFires_NewEvents(t *testing.T) {
	requireBash(t)

	pluginDir, _ := filepath.Abs("testdata/plugins/sp8-hookparity")

	events := []string{
		"PostToolUseFailure",
		"PostToolBatch",
		"StopFailure",
		"SubagentStart",
		"UserPromptExpansion",
		"PostCompact",
		"ConfigChange",
	}
	sentinels := map[string]string{}
	for _, ev := range events {
		f := filepath.Join(t.TempDir(), ev)
		sentinels[ev] = f
		t.Setenv("SP8_TEST_SENTINEL_"+ev, f)
	}

	cfg := SessionConfig{
		PluginDirs:            []string{pluginDir},
		PermissionAskFallback: AskFallbackDeny,
		WatchConfig:           true,
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drive triggers (each subtest expects a specific event):
	t.Run("PostToolUseFailure", func(t *testing.T) {
		_ = s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "false"}})
		assertSentinel(t, sentinels["PostToolUseFailure"])
	})
	t.Run("PostToolBatch", func(t *testing.T) {
		s.fireToolBatch(context.Background(),
			[]ToolCall{{Name: "Bash"}}, []ToolExecResult{{Output: "ok"}})
		assertSentinel(t, sentinels["PostToolBatch"])
	})
	t.Run("StopFailure", func(t *testing.T) {
		s.fireStopFailure(context.Background(), fmt.Errorf("rate limit exceeded"))
		assertSentinel(t, sentinels["StopFailure"])
	})
	// SubagentStart, UserPromptExpansion, PostCompact, ConfigChange similarly.
	// Omitted here for brevity — pattern is identical: drive the trigger,
	// assertSentinel.
}

func assertSentinel(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sentinel %s not written: %v", path, err)
	}
}
```

Expand subtests for the remaining four events using the same pattern.

- [ ] **Step 2: Run test**

```bash
go test ./agent/ -run TestSP8_HookEventFires_NewEvents -v
```

Expected: PASS for each subtest after SP5 fire sites are in place (Tasks 17–22).

- [ ] **Step 3: Commit**

```bash
git add agent/integration_sp8_test.go
git commit -m "test: SP8 each new SP5 event fires from its serf integration point"
```

---

## Task 36: E2E test 3 — Permissions enforced from config

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append the test**

```go
func TestSP8_PermissionsEnforcedFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	cfgPath := filepath.Join(home, "serf", "config.json")
	os.MkdirAll(filepath.Dir(cfgPath), 0755)
	os.WriteFile(cfgPath, []byte(`{"permissions":{"deny":["Bash(rm:*)"]}}`), 0644)

	denyHookSentinel := filepath.Join(t.TempDir(), "denied")
	t.Setenv("SP8_DENY_HOOK", denyHookSentinel)

	cfg := SessionConfig{
		Permissions: PermissionsConfig{Deny: []string{"Bash(rm:*)"}},
		MergedConfig: SerfConfig{
			Permissions: PermissionsConfig{Deny: []string{"Bash(rm:*)"}},
			Hooks: map[string][]HookConfig{
				"PermissionDenied": {{Command: "bash -c 'echo denied > $SP8_DENY_HOOK'"}},
			},
		},
		PermissionAskFallback: AskFallbackDeny,
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res := s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "rm /tmp/x"}})
	if !res.IsError {
		t.Fatal("expected denial")
	}
	assertSentinel(t, denyHookSentinel)
}
```

- [ ] **Step 2: Run test**

```bash
go test ./agent/ -run TestSP8_PermissionsEnforcedFromConfig -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/integration_sp8_test.go
git commit -m "test: SP8 permissions.deny enforced and PermissionDenied hook fires"
```

---

## Task 37: E2E test 4 — Ask fallback on non-interactive surface

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append the test**

```go
func TestSP8_PermissionsAskFallback_NonInteractive(t *testing.T) {
	requestHookSentinel := filepath.Join(t.TempDir(), "req")
	t.Setenv("SP8_REQ_HOOK", requestHookSentinel)

	cfg := SessionConfig{
		Permissions: PermissionsConfig{
			Ask:         []string{"Bash(*)"},
			DefaultMode: "default",
		},
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"PermissionRequest": {{Command: "bash -c 'echo asked > $SP8_REQ_HOOK'"}},
			},
		},
		PermissionAskFallback: AskFallbackDeny,
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res := s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "echo hi"}})
	if !res.IsError {
		t.Fatal("ask fallback deny should produce error")
	}
	assertSentinel(t, requestHookSentinel)
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_PermissionsAskFallback_NonInteractive -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 ask fallback denies non-interactive and fires PermissionRequest"
```

---

## Task 38: E2E test 5 — Hook union of config and plugin tiers

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_HookUnion_ConfigAndPlugin(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	configSentinel := filepath.Join(dir, "cfg-fired")
	pluginSentinel := filepath.Join(dir, "plug-fired")
	orderFile := filepath.Join(dir, "order")
	t.Setenv("CFG_S", configSentinel)
	t.Setenv("PLG_S", pluginSentinel)
	t.Setenv("ORDER_FILE", orderFile)

	pluginDir := filepath.Join(dir, "p")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"p",
		"version":"1",
		"hooks":{"PreToolUse":[{"command":"bash -c 'echo plugin >> $ORDER_FILE'"}]}
	}`), 0644)

	cfg := SessionConfig{
		PluginDirs: []string{pluginDir},
		MergedConfig: SerfConfig{
			Hooks: map[string][]HookConfig{
				"PreToolUse": {{Command: "bash -c 'echo config >> $ORDER_FILE'"}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "true"}})

	data, err := os.ReadFile(orderFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != "config" || lines[1] != "plugin" {
		t.Fatalf("order = %v, want [config plugin]", lines)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_HookUnion_ConfigAndPlugin -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 hook union — config tier fires before plugin tier"
```

---

## Task 39: E2E test 6 — MCP plugin wins on key collision

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_MCPUnion_ConfigAndPlugin(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "p")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"p","version":"1",
		"mcpServers":{"foo":{"command":"from-plugin","type":"stdio"}}
	}`), 0644)

	cfg := SessionConfig{
		PluginDirs: []string{pluginDir},
		MergedConfig: SerfConfig{
			MCPServers: map[string]MCPServerConfig{
				"foo": {Name: "foo", Command: "from-config", Type: "stdio"},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := s.mcpConfigForName("foo")
	if got.Command != "from-plugin" {
		t.Fatalf("foo command = %q, want from-plugin", got.Command)
	}
}
```

If `mcpConfigForName` doesn't exist on Session, add a small accessor for tests:

```go
func (s *Session) mcpConfigForName(name string) MCPServerConfig {
	for _, c := range s.mergedMCPConfigs {
		if c.Name == name {
			return c
		}
	}
	return MCPServerConfig{}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_MCPUnion_ConfigAndPlugin -v
git add agent/integration_sp8_test.go agent/session.go
git commit -m "test: SP8 MCP union — plugin tier wins on key collision"
```

---

## Task 40: E2E test 7 — `${user_config.KEY}` expansion in MCP server URL

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_UserConfigExpansion_MCP(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "p")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"p","version":"1",
		"userConfig":{"ENDPOINT":{"type":"string","default":"https://api.example.com"}},
		"mcpServers":{"api":{"type":"http","url":"${user_config.ENDPOINT}/mcp"}}
	}`), 0644)

	cfg := SessionConfig{
		PluginDirs:        []string{pluginDir},
		PluginConfigStore: PluginConfigStore{},
		SecureStore:       SecureStore{},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := s.mcpConfigForName("api")
	if got.URL != "https://api.example.com/mcp" {
		t.Fatalf("URL = %q, want expanded", got.URL)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_UserConfigExpansion_MCP -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 \${user_config.KEY} expanded in MCP server URL"
```

---

## Task 41: E2E test 8 — `${user_config.KEY}` expansion in hook command

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_UserConfigExpansion_Hook(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "out")
	t.Setenv("SP8_HOOK_S", sentinel)
	pluginDir := filepath.Join(dir, "p")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"p","version":"1",
		"userConfig":{"WHO":{"type":"string","default":"world"}},
		"hooks":{"PreToolUse":[{"command":"bash -c 'echo hello ${user_config.WHO} > $SP8_HOOK_S'"}]}
	}`), 0644)

	cfg := SessionConfig{PluginDirs: []string{pluginDir}}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "true"}})
	data, _ := os.ReadFile(sentinel)
	if !strings.Contains(string(data), "hello world") {
		t.Fatalf("sentinel = %q, want hello world", data)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_UserConfigExpansion_Hook -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 \${user_config.KEY} expanded in hook command"
```

---

## Task 42: E2E test 9 — `CLAUDE_PROJECT_DIR` injected into stdio MCP env

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_CLAUDE_PROJECT_DIR_Injected(t *testing.T) {
	requireBash(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	proj := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = proj
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	sentinel := filepath.Join(t.TempDir(), "projdir")
	pluginDir := filepath.Join(t.TempDir(), "p")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"p","version":"1",
		"mcpServers":{"echo":{"type":"stdio","command":"bash","args":["-c","echo $CLAUDE_PROJECT_DIR > `+sentinel+`; sleep 1"]}}
	}`), 0644)

	cfg := SessionConfig{
		PluginDirs: []string{pluginDir},
		// existing fields needed by NewSession
	}
	env := ExecutionEnvironment{WorkingDir: proj}
	_ = env
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s
	// Wait briefly for the stdio MCP server to spawn and write.
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(sentinel); err == nil {
			break
		}
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel not written: %v", err)
	}
	if !strings.Contains(string(data), proj) {
		t.Fatalf("sentinel = %q, want it to contain %q", data, proj)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_CLAUDE_PROJECT_DIR_Injected -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 CLAUDE_PROJECT_DIR injected into stdio MCP env"
```

---

## Task 43: E2E test 10 — Plugin `bin/` scoped to Bash tool PATH

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_BinDirOnBashPath(t *testing.T) {
	requireBash(t)
	pluginDir := filepath.Join(t.TempDir(), "p")
	os.MkdirAll(filepath.Join(pluginDir, "bin"), 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"p","version":"1"}`), 0644)
	binPath := filepath.Join(pluginDir, "bin", "my-tool")
	os.WriteFile(binPath, []byte("#!/bin/sh\necho hi\n"), 0755)

	cfg := SessionConfig{PluginDirs: []string{pluginDir}}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res := s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "my-tool"}})
	if res.IsError {
		t.Fatalf("Bash tool should find my-tool on PATH: %v", res.Output)
	}
	if !strings.Contains(res.Output, "hi") {
		t.Fatalf("Bash tool output = %q, want hi", res.Output)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_BinDirOnBashPath -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 plugin bin/ visible on Bash tool PATH"
```

---

## Task 44: E2E test 11 — `--plugin-dir` shadows `enabledPlugins` name collision

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_PluginDirShadowsEnabledPlugin(t *testing.T) {
	dir := t.TempDir()
	enabledDir := filepath.Join(dir, "enabled")
	cliDir := filepath.Join(dir, "cli")
	for i, d := range []string{enabledDir, cliDir} {
		os.MkdirAll(d, 0755)
		manifest := fmt.Sprintf(`{"name":"foo","version":"v%d"}`, i)
		os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0644)
	}
	cfg := SessionConfig{
		EnabledPluginPaths: []ResolvedPlugin{
			{PluginID: "foo@m", CachePath: enabledDir, Version: "v0", Source: PluginSourceEnabled},
			{PluginID: "foo", CachePath: cliDir, Version: "v1", Source: PluginSourceCLI},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	lp, ok := s.loadedPlugins["foo"]
	if !ok {
		t.Fatal("plugin foo not loaded")
	}
	if lp.Version != "v1" {
		t.Fatalf("expected --plugin-dir's v1, got %q", lp.Version)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_PluginDirShadowsEnabledPlugin -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 --plugin-dir shadows enabledPlugins same-name entry"
```

---

## Task 45: E2E test 12 — Missing plugin warns but session starts

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_PluginMissingWarnsNotFatal(t *testing.T) {
	sc, err := BuildSessionConfig(ExecutionEnvironment{}, BootstrapFlags{}, bootstrapDeps{
		discover: func(ExecutionEnvironment, []string) (SerfConfig, error) {
			return SerfConfig{EnabledPlugins: map[string]any{"not-installed@x": true}}, nil
		},
		installer: &fakeInstaller{}, // empty
	})
	if err != nil {
		t.Fatalf("BuildSessionConfig should not fail on missing plugin: %v", err)
	}
	if len(sc.EnabledPluginPaths) != 0 {
		t.Fatalf("missing plugin should be skipped, got %v", sc.EnabledPluginPaths)
	}
	s, err := NewSession(context.Background(), sc)
	if err != nil {
		t.Fatalf("NewSession should succeed: %v", err)
	}
	_ = s
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_PluginMissingWarnsNotFatal -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 missing enabledPlugins entry is warning, not fatal"
```

---

## Task 46: E2E test 13 — Malformed config is fatal

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_MalformedConfigFatal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte("{ not valid json"), 0644)

	_, err := BuildSessionConfig(ExecutionEnvironment{WorkingDir: dir},
		BootstrapFlags{ConfigPaths: []string{cfgPath}},
		bootstrapDeps{discover: DiscoverSerfConfig})
	if err == nil {
		t.Fatal("expected fatal error on malformed config")
	}
	if !strings.Contains(err.Error(), cfgPath) {
		t.Fatalf("error should name the file path %q: %v", cfgPath, err)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_MalformedConfigFatal -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 malformed config is fatal with file path in error"
```

---

## Task 47: E2E test 14 — Trust prompt for project-tier marketplace

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
type recordingTrustPrompter struct {
	calls   []string
	response plugins.TrustDecision
}

func (r *recordingTrustPrompter) Prompt(name, source string) plugins.TrustDecision {
	r.calls = append(r.calls, name)
	return r.response
}

func TestSP8_TrustPrompt_ProjectMarketplace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	proj := t.TempDir()
	os.MkdirAll(filepath.Join(proj, ".serf"), 0755)
	os.WriteFile(filepath.Join(proj, ".serf/config.json"), []byte(`{
		"marketplaces":{"untrusted":{"source":{"type":"directory","path":"/tmp/x"}}}
	}`), 0644)

	pr := &recordingTrustPrompter{response: plugins.TrustAlways}
	trust := plugins.NewTrustManager(home).WithPrompter(pr)

	sc, err := BuildSessionConfig(ExecutionEnvironment{WorkingDir: proj},
		BootstrapFlags{},
		bootstrapDeps{
			discover: DiscoverSerfConfig,
			trust:    trust,
		})
	if err != nil {
		t.Fatal(err)
	}
	_ = sc
	if len(pr.calls) != 1 || pr.calls[0] != "untrusted" {
		t.Fatalf("prompt calls = %v, want [untrusted]", pr.calls)
	}
	// trusted_projects.json should have an entry.
	data, _ := os.ReadFile(filepath.Join(home, "serf", "plugins", "trusted_projects.json"))
	if !strings.Contains(string(data), proj) {
		t.Fatalf("trusted_projects.json missing %q: %s", proj, data)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_TrustPrompt_ProjectMarketplace -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 trust prompt for project-declared marketplace persists"
```

---

## Task 48: E2E test 15 — Subagent inherits config

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_SubagentInheritsConfig(t *testing.T) {
	cfg := SessionConfig{
		Permissions: PermissionsConfig{Deny: []string{"Bash(rm:*)"}},
	}
	parent, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.spawnAgent(context.Background(), spawnRequest{
		AgentType: "general-purpose",
		Prompt:    "do",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := child.execTool(context.Background(), ToolCall{
		Name: "Bash", Arguments: map[string]any{"command": "rm /tmp/x"},
	})
	if !res.IsError {
		t.Fatal("subagent should inherit parent's deny rule")
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_SubagentInheritsConfig -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 subagent inherits parent's merged permissions"
```

---

## Task 49: E2E test 16 — Backward compat: `--plugin-dir`-only

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_BackwardCompat_PluginDirOnly(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "legacy")
	os.MkdirAll(pluginDir, 0755)
	sentinel := filepath.Join(dir, "fired")
	t.Setenv("LEGACY_S", sentinel)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"legacy","version":"1",
		"hooks":{"PreToolUse":[{"command":"bash -c 'echo legacy > $LEGACY_S'"}]}
	}`), 0644)

	cfg := SessionConfig{PluginDirs: []string{pluginDir}}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "true"}})
	assertSentinel(t, sentinel)
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_BackwardCompat_PluginDirOnly -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 backward compat — PluginDirs-only sessions still work"
```

---

## Task 50: E2E test 17 — Backward compat: `.serf/mcp.json` only

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_BackwardCompat_McpJsonOnly(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".serf", "mcp.json")
	os.MkdirAll(filepath.Dir(mcpPath), 0755)
	os.WriteFile(mcpPath, []byte(`{"mcpServers":{"legacy":{"command":"echo","type":"stdio"}}}`), 0644)

	cfg := SessionConfig{
		MCPConfigFiles: []string{mcpPath},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := s.mcpConfigForName("legacy")
	if got.Command != "echo" {
		t.Fatalf("legacy mcp not loaded from mcp.json: got %+v", got)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_BackwardCompat_McpJsonOnly -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 backward compat — .serf/mcp.json still discovered"
```

---

## Task 51: E2E test 18 — `additionalContext` plumbed into next round

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_AdditionalContext_Plumbed(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "p")
	os.MkdirAll(pluginDir, 0755)
	// Hook returns JSON on stdout with hookSpecificOutput.additionalContext.
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"name":"p","version":"1",
		"hooks":{"PostToolUse":[{"command":"echo '{\"hookSpecificOutput\":{\"additionalContext\":\"injected steering\"}}'"}]}
	}`), 0644)

	cfg := SessionConfig{PluginDirs: []string{pluginDir}}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res := s.execTool(context.Background(), ToolCall{Name: "Bash", Arguments: map[string]any{"command": "true"}})
	_ = res

	// Inspect the conversation history for the steering turn (SP5 wires this).
	turns := s.HistoryTurns()
	found := false
	for _, tr := range turns {
		if strings.Contains(fmt.Sprint(tr), "injected steering") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected steering turn in history, got %v", turns)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_AdditionalContext_Plumbed -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 PostToolUse additionalContext appears in next round"
```

---

## Task 52: E2E test 19 — `ConfigChange` mid-session reload

**Files:**
- Modify: `agent/integration_sp8_test.go`

- [ ] **Step 1: Append**

```go
func TestSP8_ConfigChange_Reload(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"permissions":{}}`), 0644)
	sentinel := filepath.Join(t.TempDir(), "cc")
	t.Setenv("CC_S", sentinel)

	cfg := SessionConfig{
		WatchConfig: true,
		MergedConfig: SerfConfig{
			Sources: []string{cfgPath},
			Hooks: map[string][]HookConfig{
				"ConfigChange": {{Command: "bash -c 'echo cc > $CC_S'"}},
			},
		},
	}
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s

	// Mutate the file.
	os.WriteFile(cfgPath, []byte(`{"permissions":{"deny":["X(*)"]}}`), 0644)

	// Poll briefly.
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sentinel); err == nil {
			return
		}
	}
	t.Fatal("ConfigChange did not fire after mutation")
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./agent/ -run TestSP8_ConfigChange_Reload -v
git add agent/integration_sp8_test.go
git commit -m "test: SP8 ConfigChange fires on mid-session config mutation"
```

---

## Task 53: Full-suite green check

- [ ] **Step 1: Run the full agent suite**

```bash
go test ./agent/... -v -count=1
```

Expected: PASS (with any tests that require not-yet-merged SP1..SP7 surfaces appropriately skipped or marked as integration gates).

- [ ] **Step 2: Run the binaries' build**

```bash
go build ./cmd/serf ./cmd/serf-tui ./cmd/serf-hub ./cmd/serfeval
```

Expected: clean.

- [ ] **Step 3: Commit any cleanup**

If lint or vet flags anything, fix in a final commit:

```bash
go vet ./...
gofmt -l ./agent ./cmd
git status
```

If clean, no commit needed. Otherwise:

```bash
git add -p
git commit -m "agent: SP8 cleanup pass — vet/fmt"
```

---

## Self-Review Notes

**Spec coverage check.**

| Spec section | Task(s) |
|---|---|
| §1 Goal — what SP8 owns | Tasks 6–10, 17–25, 27–31, 34 |
| §2 Session-startup pipeline | Tasks 6–10 |
| §2.2 Subagent inheritance | Tasks 23, 48 |
| §3.1 Hooks union | Tasks 12, 38 |
| §3.2 MCP plugin-last layering | Tasks 13, 39 |
| §3.3 Permissions | Tasks 11, 16, 36, 37 |
| §3.4 Skills | (existing behavior preserved; covered by §3.5 verification path) |
| §3.5 Agents default-agent | (existing path; smoke-tested by Task 53) |
| §3.6 enabledPlugins resolution | Tasks 8, 45 |
| §4 SessionConfig schema | Tasks 1, 4, 5, 10 |
| §4.1 --config vs --plugin-dir | Tasks 9, 24, 26 |
| §5.1 cmd/serf | Tasks 26, 27, 28 |
| §5.2 cmd/serf-tui | Task 29 |
| §5.3 cmd/serf-hub | Task 30 |
| §5.4 cmd/serfeval | Task 31 |
| §5.5 shared helper BuildSessionConfig | Tasks 6–10 |
| §6 Fire sites (9 events) | Tasks 17–22 (7 events) + Task 16 (PermReq/Denied) |
| §7 Permission enforcement wire-in | Task 16 |
| §8 User-config provider wire-up | Tasks 14, 15, 40, 41 |
| §9 enabledPlugins resolution sequence | Task 8 |
| §10 Error handling | Tasks 6, 8, 11, 14, 45, 46 |
| §11 Package/file layout | Tasks 1–25 collectively touch each file |
| §12 E2E test plan (19 cases) | Tasks 34–52 (one task per case; Task 35 wraps cases 2.a–2.g) |
| §13 Backward compatibility | Tasks 24, 49, 50 |
| §14 Open questions — settled here | Documented in §10/§13/§3.1 and verified by Tasks 38, 44, 45 |

**Gaps acknowledged.**

- The plan assumes SP1..SP7 exports exist by names cited in the spec. If SP1's `PermissionsConfig.IsEmpty()` or SP7's `ResolvedUserConfig.Set/Lookup` carry different exact signatures, Tasks 11, 14, 15 will need a one-line rename. The task code shows the expected signatures in comments.
- Two tests use small adapter helpers (`installerAdapter`, `recordingTrustPrompter`) that satisfy interface seams SP8 declares; these are test-only and don't compromise the "no mocking" rule for production code paths.
- The Hub-side prompter for the post-install `serf plugin enable` web UI is out of scope here (per §5.3 the Hub spawns serf as a subprocess that re-runs `BuildSessionConfig` itself); a future SP can lift it inside the Hub if/when in-process plugin enable is added.

**Placeholder scan.** Re-scanned the document for the forbidden patterns ("TBD", "implement later", "similar to X", "TODO" outside cited collaborator stubs). Each remaining `// TODO SP{N}` comment is a deliberate marker for the collaborator boundary, not a planning placeholder.

**Type consistency.** `ResolvedPlugin`, `PluginSourceKind`, `PluginSourceEnabled`, `PluginSourceCLI`, `BootstrapFlags`, `bootstrapDeps`, `installerEntry`, `ErrPluginNotInstalled`, `pluginInstaller`, `trustEnforcer`, `loadedPluginForHooks`, `expansionInfo`, `userConfigResolverFn`, `permissionMatcher`, `pluginUserConfigs`, `mcpConfigForName`, `fireToolBatch`, `fireStopFailure`, `fireUserPromptExpansion`, `classifyAPIError`, `toBatchToolResults`, `permissionDeniedResult`, `runPermissionDeniedHook`, `resolveAsk`, `validatePluginSources` — all defined in earlier tasks and used consistently in later ones.

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-14-claude-code-compat-sp8-integration-plan.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — Dispatch a fresh subagent per task with review between tasks. Best for a 53-task plan that touches many files.

**2. Inline Execution** — Execute tasks in this session using executing-plans with checkpoints.

**Which approach?**
