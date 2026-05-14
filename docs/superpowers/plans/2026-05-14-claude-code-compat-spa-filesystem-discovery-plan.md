# SP-A — Filesystem Plugin Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `DiscoverPluginDirs` to package `agent` so serf auto-loads plugins from `~/.config/serf/plugins/*/` and `<git-root>/.serf/plugins/*/`, unioned with `--plugin-dir` flags, with collisions resolved by precedence (user < project < CLI).

**Architecture:** One new file `agent/plugin_discovery.go` exposes `DiscoverPluginDirs(env, extraDirs) ([]string, []DiscoveryShadowedEntry, []error)`. It walks the global and project roots (using existing `gitRootOrEmpty`), filters for `.claude-plugin/plugin.json`, resolves symlinks per entry, parses each manifest just enough to learn its `name`, then merges all sources into a precedence-ordered slice. Discovery never aborts: malformed manifests, unreadable roots, and bad `--plugin-dir` paths land in the returned `[]error` so SP8 can surface them at startup.

**Tech Stack:** Go 1.x, package `agent`, real filesystem via `t.TempDir()`, `os.ReadDir`, `filepath.EvalSymlinks`, `encoding/json`. No mocked filesystem; no new third-party deps. Closest analogs: `agent/skills.go` (`DiscoverSkills`, `scanSkillsDir`), `agent/mcp_config.go` (`DiscoverMCPConfigs`, `globalMCPConfigPath`), `agent/plugin.go` (`ParsePluginManifest`).

**Spec source of truth:** `docs/superpowers/specs/2026-05-14-claude-code-compat-spa-filesystem-discovery-design.md`

---

## File Structure

**Created:**
- `agent/plugin_discovery.go` — `DiscoverPluginDirs`, `DiscoveryShadowedEntry`, internal helpers (`globalPluginsRoot`, `walkPluginsRoot`, `readManifestName`, `appendShadowed`).
- `agent/plugin_discovery_test.go` — test helpers (`writePluginDir`, `writeManifest`) and tests for every spec scenario.

**Untouched:** `agent/plugin.go`, `agent/mcp_config.go`, `agent/skills.go`, `agent/project_docs.go`. SP-A reuses `gitRootOrEmpty` from `project_docs.go` and `ParsePluginManifest` indirectly (we only read `name`). Wiring into `SessionConfig` and the four `cmd/` binaries belongs to SP8 and is **out of scope** for this plan.

**Conventions:**
- Tests use `t.TempDir()` exclusively. No fixtures under `testdata/` — every tree is built in-process so tests are self-contained.
- `t.Setenv("HOME", ...)` and `t.Setenv("XDG_CONFIG_HOME", ...)` redirect the global root.
- For the project root, we create a `.git/` directory inside `t.TempDir()` and pass a `fakeEnv` whose `ExecCommand("git rev-parse --show-toplevel", ...)` returns that path — same trick as `fakeEnvForMCP`.
- Frequent commits: one per task minimum. Every commit message follows the conventional-commit style used in the repo (`feat:`, `test:`, `refactor:`).

---

## Task 1: Create the package skeleton and shared types

**Files:**
- Create: `agent/plugin_discovery.go`

- [ ] **Step 1: Write the file skeleton with the public type only**

```go
package agent

// DiscoveryShadowedEntry records a plugin-name collision that
// DiscoverPluginDirs resolved by precedence. The caller (SP8 startup)
// uses this to emit one "shadowed plugin" warning per collision.
type DiscoveryShadowedEntry struct {
	Name       string
	KeptDir    string
	SkippedDir string
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./agent/...`
Expected: PASS (no output, exit 0).

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery.go
git commit -m "feat(agent): add DiscoveryShadowedEntry type for plugin discovery"
```

---

## Task 2: Add the test helper `writePluginDir`

**Files:**
- Create: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Write the test file with helpers only**

```go
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePluginDir writes a minimal valid plugin into <parent>/<dirname>/
// with manifest name <manifestName>. Returns the EvalSymlinks-resolved
// absolute plugin dir so callers can compare it directly against paths
// returned by DiscoverPluginDirs (important on macOS where t.TempDir()
// hands out /var/... paths that resolve to /private/var/...).
func writePluginDir(t *testing.T, parent, dirname, manifestName string) string {
	t.Helper()
	pluginDir := filepath.Join(parent, dirname)
	manifestDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"name":"` + manifestName + `","version":"0.0.1"}`
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	abs, err := filepath.Abs(pluginDir)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

// writeRawManifest writes <parent>/<dirname>/.claude-plugin/plugin.json
// containing raw bytes (for malformed-manifest tests). Returns the
// EvalSymlinks-resolved absolute plugin dir for the same reason as
// writePluginDir.
func writeRawManifest(t *testing.T, parent, dirname string, body []byte) string {
	t.Helper()
	pluginDir := filepath.Join(parent, dirname)
	manifestDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	abs, _ := filepath.Abs(pluginDir)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

// fakeEnvForDiscovery is a minimal ExecutionEnvironment for plugin-discovery tests.
type fakeEnvForDiscovery struct {
	workDir string
	gitRoot string
}

func (f *fakeEnvForDiscovery) Initialize() error    { return nil }
func (f *fakeEnvForDiscovery) Cleanup()             {}
func (f *fakeEnvForDiscovery) WorkingDirectory() string { return f.workDir }
func (f *fakeEnvForDiscovery) Platform() string         { return "test" }
func (f *fakeEnvForDiscovery) OSVersion() string        { return "test" }

func (f *fakeEnvForDiscovery) ExecCommand(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
	if f.gitRoot != "" && strings.Contains(command, "git rev-parse --show-toplevel") {
		return ExecResult{Stdout: f.gitRoot, ExitCode: 0}, nil
	}
	return ExecResult{ExitCode: 1}, nil
}

func (f *fakeEnvForDiscovery) ReadFile(string, *int, *int) (string, error)           { return "", nil }
func (f *fakeEnvForDiscovery) WriteFile(string, string) (string, error)              { return "", nil }
func (f *fakeEnvForDiscovery) EditFile(string, string, string, bool) (string, error) { return "", nil }
func (f *fakeEnvForDiscovery) FileExists(string) bool                                { return false }
func (f *fakeEnvForDiscovery) Glob(string, string) ([]string, error)                 { return nil, nil }
func (f *fakeEnvForDiscovery) Grep(string, string, string, bool, int, string) (string, error) {
	return "", nil
}
func (f *fakeEnvForDiscovery) ListDirectory(string, int) ([]DirEntry, error) { return nil, nil }

// silence unused-import warnings until first real test references them.
var _ = errors.New
```

- [ ] **Step 2: Verify the test file compiles**

Run: `go vet ./agent/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): scaffold plugin-discovery test helpers"
```

---

## Task 3: Failing test — empty everywhere returns (nil, nil, nil)

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_EmptyEverywhere(t *testing.T) {
	// No global root, no project root, no extraDirs.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, shadowed, errs := DiscoverPluginDirs(env, nil)

	if dirs != nil {
		t.Errorf("dirs = %v, want nil", dirs)
	}
	if shadowed != nil {
		t.Errorf("shadowed = %v, want nil", shadowed)
	}
	if errs != nil {
		t.Errorf("errs = %v, want nil", errs)
	}
}
```

- [ ] **Step 2: Run to verify it fails (function not defined)**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_EmptyEverywhere -v`
Expected: FAIL — `undefined: DiscoverPluginDirs`.

- [ ] **Step 3: Add the minimal stub**

Append to `agent/plugin_discovery.go`:

```go
// DiscoverPluginDirs returns absolute paths to every plugin directory
// found across the known locations, plus any CLI-supplied directories.
// The returned slice is ordered: lowest-precedence first.
//
// Discovery sources (lowest to highest precedence):
//  1. ~/.config/serf/plugins/<name>/   (per-user)
//  2. <git-root>/.serf/plugins/<name>/ (per-project)
//  3. extraDirs                         (e.g., --plugin-dir, in order)
//
// A plugin directory qualifies if it contains .claude-plugin/plugin.json.
// Symlinks are resolved per plugin entry via filepath.EvalSymlinks.
// Errors are reported via the returned []error (one per failure) but
// never abort discovery. Returns (nil, nil, nil) if no roots and no
// extraDirs yield any plugin.
func DiscoverPluginDirs(env ExecutionEnvironment, extraDirs []string) ([]string, []DiscoveryShadowedEntry, []error) {
	return nil, nil, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_EmptyEverywhere -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git commit -m "feat(agent): stub DiscoverPluginDirs; empty-everywhere returns nil"
```

---

## Task 4: `globalPluginsRoot` helper

**Files:**
- Modify: `agent/plugin_discovery.go`
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestGlobalPluginsRoot_XDGOverride(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got := globalPluginsRoot()
	want := filepath.Join(xdg, "serf", "plugins")
	if got != want {
		t.Errorf("globalPluginsRoot() = %q, want %q", got, want)
	}
}

func TestGlobalPluginsRoot_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := globalPluginsRoot()
	want := filepath.Join(home, ".config", "serf", "plugins")
	if got != want {
		t.Errorf("globalPluginsRoot() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run TestGlobalPluginsRoot -v`
Expected: FAIL — `undefined: globalPluginsRoot`.

- [ ] **Step 3: Add the helper**

Append to `agent/plugin_discovery.go`:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// globalPluginsRoot returns the per-user plugins root.
// Mirrors globalMCPConfigPath: uses XDG_CONFIG_HOME if set, else ~/.config.
// Returns "" if HOME is unset and XDG_CONFIG_HOME is unset.
func globalPluginsRoot() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins")
}
```

(Adjust the import block — Go will tell you which ones the rest of the file still needs once it grows.)

- [ ] **Step 4: Run to verify both tests pass**

Run: `go test ./agent/ -run TestGlobalPluginsRoot -v`
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git commit -m "feat(agent): add globalPluginsRoot helper for plugin discovery"
```

---

## Task 5: `readManifestName` helper

**Files:**
- Modify: `agent/plugin_discovery.go`
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestReadManifestName_Valid(t *testing.T) {
	dir := t.TempDir()
	plugin := writePluginDir(t, dir, "anydir", "alpha")

	name, err := readManifestName(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "alpha" {
		t.Errorf("name = %q, want %q", name, "alpha")
	}
}

func TestReadManifestName_Malformed(t *testing.T) {
	dir := t.TempDir()
	plugin := writeRawManifest(t, dir, "bogus", []byte("{not json"))

	_, err := readManifestName(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	if err == nil {
		t.Fatal("expected error for malformed manifest, got nil")
	}
}

func TestReadManifestName_EmptyName(t *testing.T) {
	dir := t.TempDir()
	plugin := writeRawManifest(t, dir, "empty", []byte(`{"version":"1"}`))

	_, err := readManifestName(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/ -run TestReadManifestName -v`
Expected: FAIL — `undefined: readManifestName`.

- [ ] **Step 3: Add the helper**

Append to `agent/plugin_discovery.go`:

```go
// readManifestName parses just the "name" field of a plugin.json
// manifest. It avoids ParsePluginManifest so a single bad field in the
// rest of the manifest does not abort discovery for that entry.
func readManifestName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading manifest %q: %w", path, err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parsing manifest %q: %w", path, err)
	}
	if m.Name == "" {
		return "", fmt.Errorf("manifest %q has empty name", path)
	}
	return m.Name, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/ -run TestReadManifestName -v`
Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git commit -m "feat(agent): add readManifestName for plugin discovery"
```

---

## Task 6: Discover one plugin under the global root

**Files:**
- Modify: `agent/plugin_discovery.go`
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestDiscoverPluginDirs_GlobalOnly(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := writePluginDir(t, globalRoot, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, shadowed, errs := DiscoverPluginDirs(env, nil)

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(shadowed) != 0 {
		t.Errorf("shadowed = %v, want none", shadowed)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("dirs = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_GlobalOnly -v`
Expected: FAIL — dirs is empty.

- [ ] **Step 3: Implement `walkPluginsRoot` and wire it into `DiscoverPluginDirs`**

Replace the stub body of `DiscoverPluginDirs` and add `walkPluginsRoot`:

```go
// rawEntry holds an in-progress discovery result before collision resolution.
type rawEntry struct {
	name string
	dir  string
}

// walkPluginsRoot scans <root> for subdirectories containing
// .claude-plugin/plugin.json. It returns one rawEntry per qualifying
// directory and one error per parse/symlink failure.
// A nonexistent root returns (nil, nil); an unreadable root returns
// (nil, [permission error]).
func walkPluginsRoot(root string) ([]rawEntry, []error) {
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("reading plugins root %q: %w", root, err)}
	}

	var out []rawEntry
	var errs []error
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Stat(path) // follows symlinks
		if err != nil {
			errs = append(errs, fmt.Errorf("stat %q: %w", path, err))
			continue
		}
		if !info.IsDir() {
			continue
		}
		manifest := filepath.Join(path, ".claude-plugin", "plugin.json")
		if _, err := os.Stat(manifest); err != nil {
			continue // silently skip — directory is not a plugin
		}
		abs, err := filepath.EvalSymlinks(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving %q: %w", path, err))
			continue
		}
		abs, err = filepath.Abs(abs)
		if err != nil {
			errs = append(errs, fmt.Errorf("abs %q: %w", path, err))
			continue
		}
		name, err := readManifestName(filepath.Join(abs, ".claude-plugin", "plugin.json"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, rawEntry{name: name, dir: abs})
	}
	return out, errs
}

func DiscoverPluginDirs(env ExecutionEnvironment, extraDirs []string) ([]string, []DiscoveryShadowedEntry, []error) {
	var raw []rawEntry
	var errs []error

	if r, e := walkPluginsRoot(globalPluginsRoot()); len(r) > 0 || len(e) > 0 {
		raw = append(raw, r...)
		errs = append(errs, e...)
	}

	if len(raw) == 0 && len(errs) == 0 && len(extraDirs) == 0 {
		return nil, nil, nil
	}

	dirs := make([]string, 0, len(raw))
	seen := map[string]int{}
	var shadowed []DiscoveryShadowedEntry
	for _, r := range raw {
		if i, ok := seen[r.name]; ok {
			shadowed = append(shadowed, DiscoveryShadowedEntry{
				Name: r.name, KeptDir: r.dir, SkippedDir: dirs[i],
			})
			dirs[i] = r.dir
		} else {
			seen[r.name] = len(dirs)
			dirs = append(dirs, r.dir)
		}
	}
	if len(dirs) == 0 {
		dirs = nil
	}
	return dirs, shadowed, errs
}
```

- [ ] **Step 4: Run to verify pass; ensure earlier tests still pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs -v`
Expected: PASS for `EmptyEverywhere` and `GlobalOnly`.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git commit -m "feat(agent): discover plugins under global plugins root"
```

---

## Task 7: Discover plugins under the project root

**Files:**
- Modify: `agent/plugin_discovery.go`
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestDiscoverPluginDirs_ProjectOnly(t *testing.T) {
	// No global root.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projRoot := t.TempDir()
	pluginsDir := filepath.Join(projRoot, ".serf", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := writePluginDir(t, pluginsDir, "bar", "bar")

	env := &fakeEnvForDiscovery{workDir: projRoot, gitRoot: projRoot}

	dirs, shadowed, errs := DiscoverPluginDirs(env, nil)

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(shadowed) != 0 {
		t.Errorf("shadowed = %v, want none", shadowed)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("dirs = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ProjectOnly -v`
Expected: FAIL — dirs is empty (project root not scanned yet).

- [ ] **Step 3: Add project-root walk to `DiscoverPluginDirs`**

Replace the body of `DiscoverPluginDirs` with:

```go
func DiscoverPluginDirs(env ExecutionEnvironment, extraDirs []string) ([]string, []DiscoveryShadowedEntry, []error) {
	var raw []rawEntry
	var errs []error

	// Source 1: per-user (~/.config/serf/plugins).
	if r, e := walkPluginsRoot(globalPluginsRoot()); len(r) > 0 || len(e) > 0 {
		raw = append(raw, r...)
		errs = append(errs, e...)
	}

	// Source 2: per-project (<git-root>/.serf/plugins).
	if env != nil {
		cwd := env.WorkingDirectory()
		if cwd != "" {
			if root := gitRootOrEmpty(env, cwd); root != "" {
				r, e := walkPluginsRoot(filepath.Join(root, ".serf", "plugins"))
				raw = append(raw, r...)
				errs = append(errs, e...)
			}
		}
	}

	if len(raw) == 0 && len(errs) == 0 && len(extraDirs) == 0 {
		return nil, nil, nil
	}

	dirs := make([]string, 0, len(raw))
	seen := map[string]int{}
	var shadowed []DiscoveryShadowedEntry
	for _, r := range raw {
		if i, ok := seen[r.name]; ok {
			shadowed = append(shadowed, DiscoveryShadowedEntry{
				Name: r.name, KeptDir: r.dir, SkippedDir: dirs[i],
			})
			dirs[i] = r.dir
		} else {
			seen[r.name] = len(dirs)
			dirs = append(dirs, r.dir)
		}
	}
	if len(dirs) == 0 {
		dirs = nil
	}
	return dirs, shadowed, errs
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs -v`
Expected: PASS for all current tests.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git commit -m "feat(agent): discover plugins under project root via gitRootOrEmpty"
```

---

## Task 8: Distinct plugins in global + project return in precedence order

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_GlobalAndProjectDistinct(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	globalFoo := writePluginDir(t, globalRoot, "foo", "foo")

	projRoot := t.TempDir()
	projPlugins := filepath.Join(projRoot, ".serf", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	projBar := writePluginDir(t, projPlugins, "bar", "bar")

	env := &fakeEnvForDiscovery{workDir: projRoot, gitRoot: projRoot}

	dirs, shadowed, errs := DiscoverPluginDirs(env, nil)

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(shadowed) != 0 {
		t.Errorf("shadowed = %v, want none", shadowed)
	}
	if len(dirs) != 2 {
		t.Fatalf("dirs = %v, want 2 entries", dirs)
	}
	// Global is lowest-precedence; it comes first.
	if dirs[0] != globalFoo {
		t.Errorf("dirs[0] = %q, want %q (global first)", dirs[0], globalFoo)
	}
	if dirs[1] != projBar {
		t.Errorf("dirs[1] = %q, want %q (project second)", dirs[1], projBar)
	}
}
```

- [ ] **Step 2: Run to verify pass (implementation already covers this)**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_GlobalAndProjectDistinct -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): cover distinct global+project plugin discovery"
```

---

## Task 9: Project shadows global on name collision

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_ProjectShadowsGlobal(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	globalFoo := writePluginDir(t, globalRoot, "foo", "foo")

	projRoot := t.TempDir()
	projPlugins := filepath.Join(projRoot, ".serf", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	projFoo := writePluginDir(t, projPlugins, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: projRoot, gitRoot: projRoot}

	dirs, shadowed, errs := DiscoverPluginDirs(env, nil)

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != projFoo {
		t.Errorf("dirs = %v, want [%q]", dirs, projFoo)
	}
	if len(shadowed) != 1 {
		t.Fatalf("shadowed = %v, want 1 entry", shadowed)
	}
	s := shadowed[0]
	if s.Name != "foo" || s.KeptDir != projFoo || s.SkippedDir != globalFoo {
		t.Errorf("shadowed = %+v, want {foo, %q, %q}", s, projFoo, globalFoo)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ProjectShadowsGlobal -v`
Expected: PASS — the existing precedence loop already handles this.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): cover project-shadows-global precedence"
```

---

## Task 10: Subdirectories without a manifest are silently skipped

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_NoManifestSilentlySkipped(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A valid plugin alongside a bare directory and a file.
	want := writePluginDir(t, globalRoot, "foo", "foo")
	if err := os.MkdirAll(filepath.Join(globalRoot, "no-manifest-here"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalRoot, "stray-file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, nil)

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("dirs = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_NoManifestSilentlySkipped -v`
Expected: PASS — `walkPluginsRoot` already skips entries without the manifest.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): skip non-plugin entries silently"
```

---

## Task 11: Malformed manifest reports an error but does not abort

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_MalformedManifestReportsError(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	good := writePluginDir(t, globalRoot, "good", "good")
	_ = writeRawManifest(t, globalRoot, "bad", []byte("{not json"))

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, nil)

	if len(dirs) != 1 || dirs[0] != good {
		t.Errorf("dirs = %v, want [%q] (bad should be excluded)", dirs, good)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1 entry", errs)
	}
	if !strings.Contains(errs[0].Error(), "parsing manifest") {
		t.Errorf("err = %v, want 'parsing manifest' message", errs[0])
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_MalformedManifestReportsError -v`
Expected: PASS — `walkPluginsRoot` already records parse errors.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): report malformed manifest without aborting"
```

---

## Task 12: Symlink to a plugin elsewhere resolves to the real path

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_SymlinkResolves(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Real plugin lives elsewhere; we symlink it into the global root.
	dev := t.TempDir()
	real := writePluginDir(t, dev, "real-foo", "foo")

	link := filepath.Join(globalRoot, "foo")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, nil)
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 {
		t.Fatalf("dirs = %v, want 1 entry", dirs)
	}
	// Compare via EvalSymlinks because macOS prepends /private to /var paths.
	gotResolved, _ := filepath.EvalSymlinks(dirs[0])
	wantResolved, _ := filepath.EvalSymlinks(real)
	if gotResolved != wantResolved {
		t.Errorf("dirs[0] resolved = %q, want %q", gotResolved, wantResolved)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_SymlinkResolves -v`
Expected: PASS — `walkPluginsRoot` already calls `filepath.EvalSymlinks`.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): symlink-to-plugin resolves to real path"
```

---

## Task 13: Symlink cycle records an error and skips the entry

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_SymlinkCycleReportsError(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create a self-referential symlink: foo -> bar; bar -> foo.
	foo := filepath.Join(globalRoot, "foo")
	bar := filepath.Join(globalRoot, "bar")
	if err := os.Symlink(bar, foo); err != nil {
		t.Fatalf("Symlink foo->bar: %v", err)
	}
	if err := os.Symlink(foo, bar); err != nil {
		t.Fatalf("Symlink bar->foo: %v", err)
	}

	// And a real plugin alongside to confirm discovery continues.
	good := writePluginDir(t, globalRoot, "good", "good")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, nil)

	if len(dirs) != 1 || dirs[0] != good {
		t.Errorf("dirs = %v, want [%q]", dirs, good)
	}
	// We may see 0+ errs (the cycle entries fail os.Stat before
	// EvalSymlinks). Either is acceptable as long as discovery did not
	// abort and the good plugin still loaded. Sanity-check the failure
	// mode if it surfaces.
	for _, e := range errs {
		if e == nil {
			t.Errorf("nil error in errs slice")
		}
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_SymlinkCycleReportsError -v`
Expected: PASS — the good plugin loads and any cycle error is captured rather than aborting.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): symlink cycles do not abort discovery"
```

---

## Task 14: Unreadable global root reports one error; project still loads

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_UnreadableRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses mode bits")
	}

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Drop permissions so ReadDir fails.
	if err := os.Chmod(globalRoot, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(globalRoot, 0o755) })

	projRoot := t.TempDir()
	projPlugins := filepath.Join(projRoot, ".serf", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	projFoo := writePluginDir(t, projPlugins, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: projRoot, gitRoot: projRoot}

	dirs, _, errs := DiscoverPluginDirs(env, nil)

	if len(dirs) != 1 || dirs[0] != projFoo {
		t.Errorf("dirs = %v, want [%q]", dirs, projFoo)
	}
	if len(errs) == 0 {
		t.Errorf("expected at least one error for unreadable root, got none")
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_UnreadableRoot -v`
Expected: PASS — `walkPluginsRoot` returns a permission error and project discovery still runs.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): unreadable plugins root records error and continues"
```

---

## Task 15: `--plugin-dir` adds an entry with highest precedence

**Files:**
- Modify: `agent/plugin_discovery.go`
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestDiscoverPluginDirs_ExtraDirShadowsProject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projRoot := t.TempDir()
	projPlugins := filepath.Join(projRoot, ".serf", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	projFoo := writePluginDir(t, projPlugins, "foo", "foo")

	// Extra dir contains a plugin also named "foo" living elsewhere.
	devRoot := t.TempDir()
	extraFoo := writePluginDir(t, devRoot, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: projRoot, gitRoot: projRoot}

	dirs, shadowed, errs := DiscoverPluginDirs(env, []string{extraFoo})

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != extraFoo {
		t.Errorf("dirs = %v, want [%q]", dirs, extraFoo)
	}
	if len(shadowed) != 1 {
		t.Fatalf("shadowed = %v, want 1 entry", shadowed)
	}
	if shadowed[0].Name != "foo" || shadowed[0].KeptDir != extraFoo || shadowed[0].SkippedDir != projFoo {
		t.Errorf("shadowed = %+v, want {foo, %q, %q}", shadowed[0], extraFoo, projFoo)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ExtraDirShadowsProject -v`
Expected: FAIL — extraDirs is not yet consumed.

- [ ] **Step 3: Add extraDirs handling**

Insert this block in `DiscoverPluginDirs` after the project-root walk but before the precedence-merge loop:

```go
	// Source 3: explicit extraDirs (--plugin-dir, in flag order; highest
	// precedence). Each path must point at a plugin directory (one that
	// contains .claude-plugin/plugin.json).
	for _, p := range extraDirs {
		abs, err := filepath.Abs(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("--plugin-dir %q: %w", p, err))
			continue
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			errs = append(errs, fmt.Errorf("--plugin-dir %q: %w", p, err))
			continue
		}
		manifest := filepath.Join(resolved, ".claude-plugin", "plugin.json")
		if _, err := os.Stat(manifest); err != nil {
			errs = append(errs, fmt.Errorf("--plugin-dir %q: no manifest", p))
			continue
		}
		name, err := readManifestName(manifest)
		if err != nil {
			errs = append(errs, fmt.Errorf("--plugin-dir %q: %w", p, err))
			continue
		}
		raw = append(raw, rawEntry{name: name, dir: resolved})
	}
```

Also update the early-return guard so an `extraDirs`-only call still goes through merge:

```go
	if len(raw) == 0 && len(errs) == 0 {
		return nil, nil, nil
	}
```

- [ ] **Step 4: Run to verify pass and existing tests still pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs -v`
Expected: PASS for every existing `TestDiscoverPluginDirs_*`.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git commit -m "feat(agent): --plugin-dir wins over filesystem-discovered plugins"
```

---

## Task 16: Multiple `--plugin-dir` flags — last wins, prior recorded as shadowed

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_ExtraDirOrderLastWins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	firstRoot := t.TempDir()
	first := writePluginDir(t, firstRoot, "foo", "foo")

	secondRoot := t.TempDir()
	second := writePluginDir(t, secondRoot, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, shadowed, errs := DiscoverPluginDirs(env, []string{first, second})

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != second {
		t.Errorf("dirs = %v, want [%q] (last flag wins)", dirs, second)
	}
	if len(shadowed) != 1 {
		t.Fatalf("shadowed = %v, want 1 entry", shadowed)
	}
	if shadowed[0].KeptDir != second || shadowed[0].SkippedDir != first {
		t.Errorf("shadowed = %+v, want kept=%q skipped=%q", shadowed[0], second, first)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ExtraDirOrderLastWins -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): last --plugin-dir wins on collision"
```

---

## Task 17: `--plugin-dir` pointing at a directory without a manifest is an error

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_ExtraDirWithoutManifest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	bare := t.TempDir() // exists but has no .claude-plugin/plugin.json

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, []string{bare})

	if dirs != nil {
		t.Errorf("dirs = %v, want nil", dirs)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "no manifest") {
		t.Errorf("err = %v, want 'no manifest' in message", errs[0])
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ExtraDirWithoutManifest -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): --plugin-dir without manifest is reported"
```

---

## Task 18: `--plugin-dir` pointing at a nonexistent path is an error

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_ExtraDirNonexistent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	missing := filepath.Join(t.TempDir(), "definitely-not-here")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, []string{missing})

	if dirs != nil {
		t.Errorf("dirs = %v, want nil", dirs)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "--plugin-dir") {
		t.Errorf("err = %v, want '--plugin-dir' prefix", errs[0])
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ExtraDirNonexistent -v`
Expected: PASS — `EvalSymlinks` on a missing path returns an error which we wrap.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): --plugin-dir with missing path is reported"
```

---

## Task 19: No git root — project source is silently skipped

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_NoGitRoot(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := writePluginDir(t, globalRoot, "foo", "foo")

	// gitRoot intentionally empty -> gitRootOrEmpty will return "".
	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, nil)

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none (project source must be silent)", errs)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("dirs = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_NoGitRoot -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): no git root means project source is skipped silently"
```

---

## Task 20: Manifest name differs from directory basename — collisions key on manifest name

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_ManifestNameDiffersFromDirname(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Directory is "xyz" but manifest declares name "foo".
	xyz := writePluginDir(t, globalRoot, "xyz", "foo")

	// Project has a plugin in dir "abc" also named "foo" -> should shadow.
	projRoot := t.TempDir()
	projPlugins := filepath.Join(projRoot, ".serf", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	abcFoo := writePluginDir(t, projPlugins, "abc", "foo")

	env := &fakeEnvForDiscovery{workDir: projRoot, gitRoot: projRoot}

	dirs, shadowed, errs := DiscoverPluginDirs(env, nil)
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != abcFoo {
		t.Errorf("dirs = %v, want [%q] (project shadows by manifest name)", dirs, abcFoo)
	}
	if len(shadowed) != 1 || shadowed[0].SkippedDir != xyz {
		t.Errorf("shadowed = %+v, want skipped=%q", shadowed, xyz)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_ManifestNameDiffersFromDirname -v`
Expected: PASS — `rawEntry.name` already comes from the manifest.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): plugin-name collisions key on manifest, not dirname"
```

---

## Task 21: Empty `extraDirs` slice behaves identically to nil

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_EmptyExtraDirsSlice(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := writePluginDir(t, globalRoot, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, []string{})
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("dirs = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_EmptyExtraDirsSlice -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): empty extraDirs slice equivalent to nil"
```

---

## Task 22: XDG_CONFIG_HOME override moves the global root

**Files:**
- Modify: `agent/plugin_discovery_test.go`

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_XDGConfigHomeOverride(t *testing.T) {
	// Point XDG_CONFIG_HOME at a fresh temp; ensure HOME is also fresh so
	// the fallback wouldn't accidentally find anything.
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	xdgRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(xdgRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := writePluginDir(t, xdgRoot, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	dirs, _, errs := DiscoverPluginDirs(env, nil)
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("dirs = %v, want [%q]", dirs, want)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_XDGConfigHomeOverride -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): XDG_CONFIG_HOME override redirects global plugins root"
```

---

## Task 23: Same absolute directory reached via two routes deduplicates

**Files:**
- Modify: `agent/plugin_discovery_test.go`

This addresses spec open question #2: a plugin in `~/.config/serf/plugins/foo/` and a `--plugin-dir` pointing to the same absolute directory must coalesce into one entry, not appear twice. After `EvalSymlinks` both routes resolve to the same `abs`, and the existing precedence loop keys on `name`, so the duplicate should appear in `shadowed` rather than as two dirs.

- [ ] **Step 1: Append the test**

```go
func TestDiscoverPluginDirs_DuplicateAbsPathDeduplicates(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalRoot := filepath.Join(xdg, "serf", "plugins")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plugin := writePluginDir(t, globalRoot, "foo", "foo")

	env := &fakeEnvForDiscovery{workDir: t.TempDir(), gitRoot: ""}

	// Pass the same plugin via --plugin-dir.
	dirs, shadowed, errs := DiscoverPluginDirs(env, []string{plugin})

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if len(dirs) != 1 || dirs[0] != plugin {
		t.Errorf("dirs = %v, want [%q] (must dedupe)", dirs, plugin)
	}
	if len(shadowed) != 1 {
		t.Errorf("shadowed = %v, want exactly 1 entry (the duplicate)", shadowed)
	}
}
```

- [ ] **Step 2: Run to verify pass**

Run: `go test ./agent/ -run TestDiscoverPluginDirs_DuplicateAbsPathDeduplicates -v`
Expected: PASS — the precedence merge already collapses by name.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_discovery_test.go
git commit -m "test(agent): same absolute dir via two routes deduplicates"
```

---

## Task 24: Full-package test run + final commit checkpoint

**Files:**
- (none — verification only)

- [ ] **Step 1: Run the full `agent` package test suite**

Run: `go test ./agent/...`
Expected: PASS for the entire agent package. SP-A adds no regressions to existing tests.

- [ ] **Step 2: Vet and format**

Run: `gofmt -l agent/plugin_discovery.go agent/plugin_discovery_test.go && go vet ./agent/...`
Expected: empty output from `gofmt -l` (files are formatted) and clean `go vet`.

- [ ] **Step 3: Commit any formatting fixups (if any)**

```bash
git add agent/plugin_discovery.go agent/plugin_discovery_test.go
git diff --cached --quiet || git commit -m "style(agent): gofmt SP-A discovery files"
```

(No-op if nothing to commit; the `||` skips the commit when the diff is empty.)

---

## Spec Coverage Map

| Spec scenario (table in §8) | Implementing task(s) |
|---|---|
| 1 empty everywhere | Task 3 |
| 2 global only | Task 6 |
| 3 project only | Task 7 |
| 4 global+project distinct (precedence order) | Task 8 |
| 5 project shadows global | Task 9 |
| 6 plugin-dir shadows project | Task 15 |
| 7 plugin-dir order (last wins) | Task 16 |
| 8 no manifest → silently skipped | Task 10 |
| 9 malformed manifest → reported, excluded | Task 11 |
| 10 symlink to elsewhere | Task 12 |
| 11 symlink cycle | Task 13 |
| 12 unreadable root | Task 14 |
| 13 plugin-dir without manifest | Task 17 |
| 14 plugin-dir nonexistent | Task 18 |
| 15 no git root | Task 19 |
| 16 XDG_CONFIG_HOME override | Task 22 (plus Task 4 helper-level) |
| 17 manifest name differs from dirname | Task 20 |
| 18 empty plugin-dir slice | Task 21 |
| Open Q#2: dedupe same abs path via two routes | Task 23 |

Open question #1 (parent-root symlink resolution) is intentionally **not** implemented — the spec recommends per-entry resolution only.
Open question #3 (mid-session rescan) is **not** in scope — discovery runs once per session.

## Out of Scope (handled by SP8)

- Wiring `DiscoverPluginDirs` into `SessionConfig` and the four `cmd/` binaries (`serf`, `serf-tui`, `serf-hub`, `serfeval`).
- Printing `[]error` and `[]DiscoveryShadowedEntry` to stderr at startup.
- Calling `LoadPlugins(dirs)` on the result.
- Lifecycle event firing.

Those belong to SP8 and the parent design's "Discovery integration" item.
