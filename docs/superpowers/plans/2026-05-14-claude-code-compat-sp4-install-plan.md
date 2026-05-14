# SP4 — Plugin Install, Uninstall, Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the plugin lifecycle (install / uninstall / update / list / enable / disable) for Claude Code-compatible plugins, including the `installed_plugins.json` registry, atomic IO, version resolution, file lock, and the `serf plugin` CLI subcommand tree.

**Architecture:** All install logic lives in a new `internal/plugins` package; the CLI lives in a new `cmd/serf/plugin` package. SP4 calls SP3 only through the `MarketplaceResolver` / `PluginSource` interfaces declared locally — production SP3 satisfies them later. Each registry mutation is serialized by a file lock and persisted atomically (tmp + rename). The `enabledPlugins` field of `config.json` is rewritten via a JSON round-trip that preserves unknown top-level keys, so SP4 never strands SP1's hooks/mcpServers/permissions sections.

**Tech Stack:** Go 1.25, `encoding/json`, `golang.org/x/sys/unix` (flock), `github.com/spf13/cobra` (already used by `cmd/serf`), `testing` with `t.TempDir()` and real files. No mocked filesystem. Optional `git` binary used only for directory-source SHA tests (`t.Skip` when absent).

Spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-sp4-install-design.md`.

---

## File Map

New package `internal/plugins/`:

- `registry.go` / `registry_test.go` — `Registry`, `InstallEntry`, `Scope`, `LoadRegistry`, `SaveRegistry`, `DefaultRegistryPath`, invariants.
- `version.go` / `version_test.go` — pure `computeVersion` per §9.
- `config_rewrite.go` / `config_rewrite_test.go` — `loadRawConfig` / `writeRawConfig` preserving unknown keys.
- `locks.go` / `locks_test.go` — `acquireRegistryLock` flock wrapper with timeout.
- `resolver.go` — local `MarketplaceResolver` / `PluginSource` interfaces (SP3 collaborator boundary; SP3 will satisfy).
- `install.go` / `install_test.go` — `Installer` struct + `Install` / `Uninstall` / `Update` / `UpdateAll` / `Enable` / `Disable` / `List`.
- `testdata/installed_plugins_v2.json`, `installed_plugins_malformed.json`, `plugin_with_version.json`, `plugin_no_version.json`, `plugin_invalid_version.json`.

New package `cmd/serf/plugin/`:

- `install.go` — Cobra commands + flag wiring.
- `install_test.go` — flag parsing, exit codes, rendering against a stub `Installer`-shaped interface.
- `render.go` — human + `--json` output.

No existing file is modified by SP4 itself. SP8 wires `cmd/serf/plugin.NewCommand()` into the root `serf` command.

---

## Phase 0 — Package scaffolding

### Task 0.1: Create `internal/plugins` package directory and doc.go

**Files:**
- Create: `internal/plugins/doc.go`

- [ ] **Step 1: Write the package doc**

```go
// Package plugins implements the on-disk plugin install state for serf:
// the installed_plugins.json registry, atomic IO, version resolution, a
// file lock, and the Installer that exposes Install/Uninstall/Update/
// Enable/Disable/List. The CLI in cmd/serf/plugin is a thin wrapper.
//
// SP4 calls SP3 only through MarketplaceResolver / PluginSource declared
// in resolver.go.
package plugins
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/plugins/...`
Expected: succeeds with no output.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/doc.go
git commit -m "plugins: scaffold internal/plugins package"
```

---

## Phase 1 — Registry types

### Task 1.1: Declare `Scope`, `InstallEntry`, `Registry` types

**Files:**
- Create: `internal/plugins/registry.go`

- [ ] **Step 1: Write the type declarations**

```go
package plugins

import "time"

// Scope is the install scope for a plugin entry. SP4 v1 writes only
// ScopeUser or ScopeProject; ScopeLocal / ScopeManaged are accepted on
// read for forward compatibility but rejected as inputs.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeLocal   Scope = "local"
	ScopeManaged Scope = "managed"
)

// InstallEntry is one installation of one plugin at one scope.
type InstallEntry struct {
	Scope        Scope     `json:"scope"`
	InstallPath  string    `json:"installPath"`
	Version      string    `json:"version"`
	InstalledAt  time.Time `json:"installedAt"`
	LastUpdated  time.Time `json:"lastUpdated"`
	GitCommitSha string    `json:"gitCommitSha,omitempty"`
}

// Registry is the parsed contents of installed_plugins.json. It is the
// single source of truth for which plugins are installed on this machine.
type Registry struct {
	Version int                       `json:"version"`
	Plugins map[string][]InstallEntry `json:"plugins"`
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/plugins/...`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/registry.go
git commit -m "plugins: declare Registry, InstallEntry, Scope types"
```

### Task 1.2: `LoadRegistry` — absent-file returns empty

**Files:**
- Modify: `internal/plugins/registry.go`
- Test: `internal/plugins/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"path/filepath"
	"testing"
)

func TestLoadRegistry_AbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed_plugins.json")
	r, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Version != 2 {
		t.Errorf("Version = %d, want 2", r.Version)
	}
	if r.Plugins == nil {
		t.Errorf("Plugins must be a non-nil empty map")
	}
	if len(r.Plugins) != 0 {
		t.Errorf("Plugins len = %d, want 0", len(r.Plugins))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestLoadRegistry_AbsentFile -v`
Expected: FAIL — undefined `LoadRegistry`.

- [ ] **Step 3: Implement minimal `LoadRegistry`**

Add to `internal/plugins/registry.go`:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// LoadRegistry reads installed_plugins.json from registryPath. Missing
// file returns an empty Registry{Version: 2} and a nil error. Malformed
// file is a hard error annotated with the file path.
func LoadRegistry(registryPath string) (Registry, error) {
	data, err := os.ReadFile(registryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{Version: 2, Plugins: map[string][]InstallEntry{}}, nil
		}
		return Registry{}, fmt.Errorf("reading %s: %w", registryPath, err)
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return Registry{}, fmt.Errorf("parsing %s: %w", registryPath, err)
	}
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	return r, nil
}
```

(Imports go into a single `import (...)` block in `registry.go`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestLoadRegistry_AbsentFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/registry.go internal/plugins/registry_test.go
git commit -m "plugins: LoadRegistry returns empty on absent file"
```

### Task 1.3: `LoadRegistry` — schema version checks + malformed

**Files:**
- Modify: `internal/plugins/registry.go`, `internal/plugins/registry_test.go`

- [ ] **Step 1: Add failing table-driven tests**

```go
func TestLoadRegistry_VersionsAndMalformed(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantErr   string // substring; "" means success
		wantPlugs int
	}{
		{"empty plugins map", `{"version":2,"plugins":{}}`, "", 0},
		{"version 1 accepted", `{"version":1,"plugins":{}}`, "", 0},
		{"version 99 rejected", `{"version":99,"plugins":{}}`, "unsupported installed_plugins.json schema version 99", 0},
		{"malformed JSON", `{`, "parsing", 0},
		{"trailing whitespace tolerated", `{"version":2,"plugins":{}}` + "\n\n", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "installed_plugins.json")
			if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}
			r, err := LoadRegistry(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(r.Plugins) != tc.wantPlugs {
					t.Errorf("Plugins len = %d, want %d", len(r.Plugins), tc.wantPlugs)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}
```

Add `"os"` and `"strings"` to test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugins/ -run TestLoadRegistry_VersionsAndMalformed -v`
Expected: FAIL — version 99 currently accepted.

- [ ] **Step 3: Add version gate to `LoadRegistry`**

Insert after the `json.Unmarshal` success, before the `if r.Plugins == nil` line:

```go
	if r.Version != 1 && r.Version != 2 {
		return Registry{}, fmt.Errorf("parsing %s: unsupported installed_plugins.json schema version %d", registryPath, r.Version)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestLoadRegistry -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/registry.go internal/plugins/registry_test.go
git commit -m "plugins: validate registry schema version and surface parse errors"
```

### Task 1.4: `LoadRegistry` — invariant checks (installPath inside cache root, duplicate scopes, required fields)

**Files:**
- Modify: `internal/plugins/registry.go`, `internal/plugins/registry_test.go`

- [ ] **Step 1: Extend `LoadRegistry` signature with cache-root validation**

Replace the `LoadRegistry` signature with two functions — keep the existing one as a thin wrapper:

```go
// LoadRegistryWithCacheRoot is LoadRegistry with the cache-root invariant
// check enabled. cacheRoot must be absolute. Pass "" to skip the check.
func LoadRegistryWithCacheRoot(registryPath, cacheRoot string) (Registry, error) {
	data, err := os.ReadFile(registryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{Version: 2, Plugins: map[string][]InstallEntry{}}, nil
		}
		return Registry{}, fmt.Errorf("reading %s: %w", registryPath, err)
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return Registry{}, fmt.Errorf("parsing %s: %w", registryPath, err)
	}
	if r.Version != 1 && r.Version != 2 {
		return Registry{}, fmt.Errorf("parsing %s: unsupported installed_plugins.json schema version %d", registryPath, r.Version)
	}
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	if err := validateEntries(r, cacheRoot, registryPath); err != nil {
		return Registry{}, err
	}
	return r, nil
}

// LoadRegistry is LoadRegistryWithCacheRoot with no cache-root check.
func LoadRegistry(registryPath string) (Registry, error) {
	return LoadRegistryWithCacheRoot(registryPath, "")
}

func validateEntries(r Registry, cacheRoot, registryPath string) error {
	for key, entries := range r.Plugins {
		seenScopes := map[Scope]bool{}
		for i, e := range entries {
			if e.InstallPath == "" {
				return fmt.Errorf("parsing %s: plugin %q entry %d: installPath is required", registryPath, key, i)
			}
			if seenScopes[e.Scope] {
				return fmt.Errorf("parsing %s: plugin %q has duplicate scope %q", registryPath, key, string(e.Scope))
			}
			seenScopes[e.Scope] = true
			if cacheRoot != "" {
				rel, err := filepath.Rel(cacheRoot, e.InstallPath)
				if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
					return fmt.Errorf("parsing %s: plugin %q entry %d: installPath %q outside cache root %q", registryPath, key, i, e.InstallPath, cacheRoot)
				}
			}
		}
	}
	return nil
}
```

Add `"path/filepath"` and `"strings"` to imports.

- [ ] **Step 2: Write the failing tests**

```go
func TestLoadRegistry_Invariants(t *testing.T) {
	cacheRoot := "/cache"
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "installPath outside cache root",
			content: `{"version":2,"plugins":{"x@y":[{"scope":"user","installPath":"/elsewhere/x/1","version":"1","installedAt":"2026-01-01T00:00:00Z","lastUpdated":"2026-01-01T00:00:00Z"}]}}`,
			want:    "outside cache root",
		},
		{
			name:    "duplicate scope under one key",
			content: `{"version":2,"plugins":{"x@y":[{"scope":"user","installPath":"/cache/y/x/1","version":"1","installedAt":"2026-01-01T00:00:00Z","lastUpdated":"2026-01-01T00:00:00Z"},{"scope":"user","installPath":"/cache/y/x/2","version":"2","installedAt":"2026-01-01T00:00:00Z","lastUpdated":"2026-01-01T00:00:00Z"}]}}`,
			want:    `duplicate scope "user"`,
		},
		{
			name:    "missing installPath",
			content: `{"version":2,"plugins":{"x@y":[{"scope":"user","installPath":"","version":"1","installedAt":"2026-01-01T00:00:00Z","lastUpdated":"2026-01-01T00:00:00Z"}]}}`,
			want:    "installPath is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "installed_plugins.json")
			if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadRegistryWithCacheRoot(path, cacheRoot)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLoadRegistry_OmittedGitCommitShaParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed_plugins.json")
	content := `{"version":2,"plugins":{"x@y":[{"scope":"user","installPath":"/cache/y/x/1","version":"1","installedAt":"2026-01-01T00:00:00Z","lastUpdated":"2026-01-01T00:00:00Z"}]}}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistryWithCacheRoot(path, "/cache")
	if err != nil {
		t.Fatal(err)
	}
	if r.Plugins["x@y"][0].GitCommitSha != "" {
		t.Errorf("GitCommitSha = %q, want empty", r.Plugins["x@y"][0].GitCommitSha)
	}
}
```

Add `"path/filepath"` to test imports if not already present.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/plugins/ -run "TestLoadRegistry_Invariants|TestLoadRegistry_OmittedGitCommitShaParses" -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/registry.go internal/plugins/registry_test.go
git commit -m "plugins: enforce registry invariants on load"
```

### Task 1.5: `SaveRegistry` — atomic write with sorted keys

**Files:**
- Modify: `internal/plugins/registry.go`, `internal/plugins/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSaveRegistry_RoundTripAndSortedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "installed_plugins.json")
	r := Registry{
		Version: 2,
		Plugins: map[string][]InstallEntry{
			"zeta@m":  {{Scope: ScopeUser, InstallPath: "/c/m/zeta/1", Version: "1", InstalledAt: time.Unix(0, 0).UTC(), LastUpdated: time.Unix(0, 0).UTC()}},
			"alpha@m": {{Scope: ScopeUser, InstallPath: "/c/m/alpha/1", Version: "1", InstalledAt: time.Unix(0, 0).UTC(), LastUpdated: time.Unix(0, 0).UTC()}},
		},
	}
	if err := SaveRegistry(path, r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("output must end with a newline")
	}
	if strings.Index(s, `"alpha@m"`) > strings.Index(s, `"zeta@m"`) {
		t.Errorf("plugins keys must be sorted alphabetically; got: %s", s)
	}
	if !strings.Contains(s, "  ") {
		t.Errorf("output must be indented with 2 spaces; got: %s", s)
	}
	r2, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Plugins) != 2 {
		t.Errorf("round-trip lost plugins")
	}
}
```

Add `"time"` to test imports.

- [ ] **Step 2: Run test — expect FAIL (SaveRegistry undefined)**

Run: `go test ./internal/plugins/ -run TestSaveRegistry_RoundTripAndSortedKeys -v`
Expected: FAIL.

- [ ] **Step 3: Implement `SaveRegistry`**

Append to `internal/plugins/registry.go`:

```go
import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"sort"
)

// SaveRegistry writes r to registryPath atomically: marshal to JSON, write
// to <path>.tmp.<pid>.<rand>, fsync, rename over <path>, fsync parent.
// The directory is created if missing (mode 0755). The caller holds the
// registry file lock.
func SaveRegistry(registryPath string, r Registry) error {
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	body, err := marshalRegistry(r)
	if err != nil {
		return fmt.Errorf("marshalling registry: %w", err)
	}
	parent := filepath.Dir(registryPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", parent, err)
	}
	suf := make([]byte, 4)
	if _, err := rand.Read(suf); err != nil {
		return fmt.Errorf("entropy: %w", err)
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d.%s", registryPath, os.Getpid(), hex.EncodeToString(suf))
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", tmpPath, err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("fsync %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, registryPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, registryPath, err)
	}
	if dir, err := os.Open(parent); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// marshalRegistry emits JSON with sorted plugin keys, 2-space indent, and
// a trailing newline. Determinism matters for diffability.
func marshalRegistry(r Registry) ([]byte, error) {
	keys := make([]string, 0, len(r.Plugins))
	for k := range r.Plugins {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteString("{\n  \"version\": ")
	if _, err := fmt.Fprintf(&buf, "%d", r.Version); err != nil {
		return nil, err
	}
	buf.WriteString(",\n  \"plugins\": {")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n    ")
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteString(": ")
		eb, err := marshalEntriesIndented(r.Plugins[k])
		if err != nil {
			return nil, err
		}
		buf.Write(eb)
	}
	if len(keys) > 0 {
		buf.WriteString("\n  ")
	}
	buf.WriteString("}\n}\n")
	return buf.Bytes(), nil
}

func marshalEntriesIndented(entries []InstallEntry) ([]byte, error) {
	if len(entries) == 0 {
		return []byte("[]"), nil
	}
	var buf bytes.Buffer
	buf.WriteString("[")
	for i, e := range entries {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n      ")
		b, err := json.MarshalIndent(e, "      ", "  ")
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteString("\n    ]")
	return buf.Bytes(), nil
}
```

Merge the new imports into the existing single `import (...)` block.

- [ ] **Step 4: Run test**

Run: `go test ./internal/plugins/ -run TestSaveRegistry_RoundTripAndSortedKeys -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/registry.go internal/plugins/registry_test.go
git commit -m "plugins: atomic SaveRegistry with sorted keys"
```

### Task 1.6: `DefaultRegistryPath`

**Files:**
- Modify: `internal/plugins/registry.go`, `internal/plugins/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDefaultRegistryPath_HonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/y")
	got := DefaultRegistryPath()
	want := "/x/y/serf/plugins/installed_plugins.json"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDefaultRegistryPath_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/joe")
	got := DefaultRegistryPath()
	want := "/home/joe/.config/serf/plugins/installed_plugins.json"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/plugins/ -run TestDefaultRegistryPath -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `registry.go`:

```go
// DefaultRegistryPath returns ~/.config/serf/plugins/installed_plugins.json
// for the current user, honoring XDG_CONFIG_HOME.
func DefaultRegistryPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins", "installed_plugins.json")
}

// DefaultCacheRoot returns ~/.config/serf/plugins/cache for the current user.
func DefaultCacheRoot() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins", "cache")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/plugins/ -run TestDefaultRegistryPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/registry.go internal/plugins/registry_test.go
git commit -m "plugins: DefaultRegistryPath and DefaultCacheRoot helpers"
```

### Task 1.7: Add Claude Code example fixture and round-trip test

**Files:**
- Create: `internal/plugins/testdata/installed_plugins_v2.json`
- Modify: `internal/plugins/registry_test.go`

- [ ] **Step 1: Add the fixture**

```json
{
  "version": 2,
  "plugins": {
    "agent-sdk-dev@claude-plugins-official": [
      {
        "scope": "user",
        "installPath": "/tmp/cache/claude-plugins-official/agent-sdk-dev/unknown",
        "version": "unknown",
        "installedAt": "2026-05-14T17:33:11.512Z",
        "lastUpdated": "2026-05-14T17:33:11.512Z",
        "gitCommitSha": "6d3752c000e2b3d0e6137bd7adb04895d6f40f14"
      }
    ],
    "formatter@my-marketplace": [
      {
        "scope": "user",
        "installPath": "/tmp/cache/my-marketplace/formatter/1.2.0",
        "version": "1.2.0",
        "installedAt": "2026-05-14T17:33:11.512Z",
        "lastUpdated": "2026-05-14T17:33:11.512Z",
        "gitCommitSha": "6d3752c000e2b3d0e6137bd7adb04895d6f40f14"
      },
      {
        "scope": "project",
        "installPath": "/tmp/cache/my-marketplace/formatter/1.2.0",
        "version": "1.2.0",
        "installedAt": "2026-05-14T17:33:11.512Z",
        "lastUpdated": "2026-05-14T17:33:11.512Z",
        "gitCommitSha": "6d3752c000e2b3d0e6137bd7adb04895d6f40f14"
      }
    ]
  }
}
```

- [ ] **Step 2: Write the failing test**

```go
func TestLoadRegistry_ClaudeCodeExampleRoundTrip(t *testing.T) {
	src, err := os.ReadFile("testdata/installed_plugins_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "installed_plugins.json")
	if err := os.WriteFile(path, src, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(r.Plugins) != 2 {
		t.Fatalf("Plugins len = %d, want 2", len(r.Plugins))
	}
	if len(r.Plugins["formatter@my-marketplace"]) != 2 {
		t.Errorf("formatter entries = %d, want 2", len(r.Plugins["formatter@my-marketplace"]))
	}
	// Round-trip
	out := filepath.Join(dir, "out.json")
	if err := SaveRegistry(out, r); err != nil {
		t.Fatalf("save: %v", err)
	}
	r2, err := LoadRegistry(out)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if len(r2.Plugins) != len(r.Plugins) {
		t.Errorf("round-trip plugin count drift")
	}
	if r2.Plugins["agent-sdk-dev@claude-plugins-official"][0].Version != "unknown" {
		t.Errorf("round-trip version drift")
	}
}
```

- [ ] **Step 3: Run**

Run: `go test ./internal/plugins/ -run TestLoadRegistry_ClaudeCodeExampleRoundTrip -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/testdata/installed_plugins_v2.json internal/plugins/registry_test.go
git commit -m "plugins: round-trip Claude Code example registry fixture"
```

---

## Phase 2 — Version resolution

### Task 2.1: `computeVersion` table

**Files:**
- Create: `internal/plugins/version.go`, `internal/plugins/version_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"strings"
	"testing"
)

func TestComputeVersion(t *testing.T) {
	cases := []struct {
		name          string
		reqVersion    string
		declaredVer   string
		commitSHA     string
		hasPluginJSON bool
		pluginJSONVer any // string, nil, or non-string for the invalid-type case
		want          string
		wantErr       string
	}{
		{"req matches plugin.json", "1.0.0", "", "abc", true, "1.0.0", "1.0.0", ""},
		{"req mismatches plugin.json", "1.0.0", "", "abc", true, "1.0.1", "", "does not match"},
		{"req only, no plugin.json", "1.0.0", "", "abc", false, nil, "1.0.0", ""},
		{"plugin.json wins over declared", "", "9.9.9", "abc", true, "2.1.0", "2.1.0", ""},
		{"declared used when no plugin.json", "", "1.2.3", "abc", false, nil, "1.2.3", ""},
		{"commit sha truncated to 12 chars", "", "", "abcdef1234567890aaa", false, nil, "abcdef123456", ""},
		{"unknown when no source info", "", "", "", false, nil, "unknown", ""},
		{"plugin.json no version field falls through", "", "1.2.3", "abc", true, nil, "1.2.3", ""},
		{"plugin.json version not a string", "", "", "abc", true, 42, "", "must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := computeVersion(tc.reqVersion, tc.declaredVer, tc.commitSHA, tc.hasPluginJSON, tc.pluginJSONVer)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestComputeVersion -v`
Expected: FAIL — `computeVersion` undefined.

- [ ] **Step 3: Implement**

Write `internal/plugins/version.go`:

```go
package plugins

import "fmt"

// computeVersion applies §9 of the SP4 design.
//
//   - reqVersion: --version flag (may be "")
//   - declaredVer: PluginSource.DeclaredVersion() (may be "")
//   - commitSHA: PluginSource.CommitSHA() (may be "")
//   - hasPluginJSON: true if the fetched cache dir contains a plugin.json
//   - pluginJSONVer: the raw value of plugin.json.version. nil means absent.
//     Must be a string when set, else returns an error.
func computeVersion(reqVersion, declaredVer, commitSHA string, hasPluginJSON bool, pluginJSONVer any) (string, error) {
	pjv := ""
	if hasPluginJSON && pluginJSONVer != nil {
		s, ok := pluginJSONVer.(string)
		if !ok {
			return "", fmt.Errorf(`plugin.json: "version" must be a string, got %T`, pluginJSONVer)
		}
		pjv = s
	}

	if reqVersion != "" {
		if pjv != "" && pjv != reqVersion {
			return "", fmt.Errorf(`--version %q does not match plugin.json version %q`, reqVersion, pjv)
		}
		return reqVersion, nil
	}
	if pjv != "" {
		return pjv, nil
	}
	if declaredVer != "" {
		return declaredVer, nil
	}
	if commitSHA != "" {
		if len(commitSHA) > 12 {
			return commitSHA[:12], nil
		}
		return commitSHA, nil
	}
	return "unknown", nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestComputeVersion -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/version.go internal/plugins/version_test.go
git commit -m "plugins: computeVersion implements §9 resolution rules"
```

---

## Phase 3 — Config rewrite (preserve unknown keys)

### Task 3.1: `loadRawConfig` returns a map with `enabledPlugins` destructured

**Files:**
- Create: `internal/plugins/config_rewrite.go`, `internal/plugins/config_rewrite_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRawConfig_AbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := loadRawConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.enabledPlugins == nil {
		t.Errorf("enabledPlugins must be a non-nil empty map")
	}
	if len(cfg.other) != 0 {
		t.Errorf("other must be empty, got %v", cfg.other)
	}
}

func TestLoadRawConfig_PreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"hooks": {"PreToolUse": [{"matcher":"Bash"}]},
		"mcpServers": {"x": {"command": "y"}},
		"permissions": {"allow": ["Bash(ls:*)"]},
		"enabledPlugins": {"foo@bar": true}
	}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRawConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.other["hooks"]; !ok {
		t.Errorf("hooks key not preserved")
	}
	var enabled bool
	if err := json.Unmarshal(cfg.enabledPlugins["foo@bar"], &enabled); err != nil || !enabled {
		t.Errorf("enabledPlugins[foo@bar] = %v %v, want true", string(cfg.enabledPlugins["foo@bar"]), err)
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestLoadRawConfig -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Write `internal/plugins/config_rewrite.go`:

```go
package plugins

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// rawConfig is the partial-parse shape of config.json used by enable/disable.
// "enabledPlugins" is destructured; every other top-level key is preserved
// verbatim in "other" so SP4 can round-trip the file without losing SP1's
// hooks / mcpServers / permissions sections.
type rawConfig struct {
	enabledPlugins map[string]json.RawMessage
	other          map[string]json.RawMessage
}

func loadRawConfig(path string) (rawConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rawConfig{
				enabledPlugins: map[string]json.RawMessage{},
				other:          map[string]json.RawMessage{},
			}, nil
		}
		return rawConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return rawConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	cfg := rawConfig{
		enabledPlugins: map[string]json.RawMessage{},
		other:          map[string]json.RawMessage{},
	}
	for k, v := range top {
		if k == "enabledPlugins" {
			if err := json.Unmarshal(v, &cfg.enabledPlugins); err != nil {
				return rawConfig{}, fmt.Errorf("parsing %s: enabledPlugins: %w", path, err)
			}
			if cfg.enabledPlugins == nil {
				cfg.enabledPlugins = map[string]json.RawMessage{}
			}
			continue
		}
		cfg.other[k] = v
	}
	return cfg, nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestLoadRawConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/config_rewrite.go internal/plugins/config_rewrite_test.go
git commit -m "plugins: loadRawConfig destructures enabledPlugins and preserves unknown keys"
```

### Task 3.2: `writeRawConfig` — atomic write preserving keys

**Files:**
- Modify: `internal/plugins/config_rewrite.go`, `internal/plugins/config_rewrite_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestWriteRawConfig_RoundTripPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.json")
	cfg := rawConfig{
		enabledPlugins: map[string]json.RawMessage{
			"foo@bar": json.RawMessage("true"),
		},
		other: map[string]json.RawMessage{
			"hooks":       json.RawMessage(`{"PreToolUse":[{"matcher":"Bash"}]}`),
			"permissions": json.RawMessage(`{"allow":["Bash(ls:*)"]}`),
		},
	}
	if err := writeRawConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg2, err := loadRawConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg2.other["hooks"]; !ok {
		t.Errorf("hooks dropped on round-trip")
	}
	if _, ok := cfg2.other["permissions"]; !ok {
		t.Errorf("permissions dropped on round-trip")
	}
	if string(cfg2.enabledPlugins["foo@bar"]) != "true" {
		t.Errorf("enabledPlugins drift: %s", string(cfg2.enabledPlugins["foo@bar"]))
	}
}

func TestWriteRawConfig_CreatesEmptyShapeWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := rawConfig{
		enabledPlugins: map[string]json.RawMessage{"x@y": json.RawMessage("true")},
		other:          map[string]json.RawMessage{},
	}
	if err := writeRawConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"enabledPlugins"`)) {
		t.Errorf("output missing enabledPlugins: %s", string(b))
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestWriteRawConfig -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `internal/plugins/config_rewrite.go`:

```go
// writeRawConfig serializes cfg back to path atomically. Top-level keys are
// emitted in sorted order with "enabledPlugins" placed alphabetically among
// them. Unknown keys (cfg.other) round-trip byte-for-byte.
func writeRawConfig(path string, cfg rawConfig) error {
	top := map[string]json.RawMessage{}
	for k, v := range cfg.other {
		top[k] = v
	}
	epBytes, err := marshalEnabledPlugins(cfg.enabledPlugins)
	if err != nil {
		return fmt.Errorf("marshalling enabledPlugins: %w", err)
	}
	top["enabledPlugins"] = epBytes

	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  ")
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(indentRaw(top[k], "  "))
	}
	if len(keys) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", parent, err)
	}
	suf := make([]byte, 4)
	if _, err := rand.Read(suf); err != nil {
		return fmt.Errorf("entropy: %w", err)
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d.%s", path, os.Getpid(), hex.EncodeToString(suf))
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

func marshalEnabledPlugins(m map[string]json.RawMessage) (json.RawMessage, error) {
	if len(m) == 0 {
		return json.RawMessage("{}"), nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n    ")
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(m[k])
	}
	buf.WriteString("\n  }")
	return buf.Bytes(), nil
}

// indentRaw rewraps a raw JSON value so embedded newlines align to prefix.
// For raw messages that are single-line, returns them unchanged.
func indentRaw(raw json.RawMessage, prefix string) []byte {
	if !bytes.Contains(raw, []byte("\n")) {
		return raw
	}
	lines := bytes.Split(raw, []byte("\n"))
	for i := 1; i < len(lines); i++ {
		lines[i] = append([]byte(prefix), lines[i]...)
	}
	return bytes.Join(lines, []byte("\n"))
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestWriteRawConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/config_rewrite.go internal/plugins/config_rewrite_test.go
git commit -m "plugins: writeRawConfig round-trips unknown top-level keys"
```


---

## Phase 4 — File lock

### Task 4.1: `acquireRegistryLock` with timeout

**Files:**
- Create: `internal/plugins/locks.go`, `internal/plugins/locks_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAcquireRegistryLock_Serializes(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "installed_plugins.json.lock")

	var ord []string
	var mu sync.Mutex
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		ord = append(ord, s)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})

	go func() {
		defer wg.Done()
		<-start
		release, err := acquireRegistryLock(lockPath, 2*time.Second)
		if err != nil {
			t.Errorf("A: %v", err)
			return
		}
		record("A-acquired")
		time.Sleep(150 * time.Millisecond)
		record("A-release")
		release()
	}()
	go func() {
		defer wg.Done()
		<-start
		time.Sleep(20 * time.Millisecond) // ensure A goes first
		release, err := acquireRegistryLock(lockPath, 2*time.Second)
		if err != nil {
			t.Errorf("B: %v", err)
			return
		}
		record("B-acquired")
		release()
	}()
	close(start)
	wg.Wait()
	want := []string{"A-acquired", "A-release", "B-acquired"}
	if strings.Join(ord, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ord, want)
	}
}

func TestAcquireRegistryLock_Timeout(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "installed_plugins.json.lock")
	release, err := acquireRegistryLock(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = acquireRegistryLock(lockPath, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "another serf plugin operation is in progress") {
		t.Errorf("error wording: %v", err)
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestAcquireRegistryLock -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Write `internal/plugins/locks.go`:

```go
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// acquireRegistryLock opens (creating if needed) lockPath and takes an
// advisory exclusive flock on it. Returns a release func and a nil error
// on success. On contention, retries with exponential backoff until
// timeout elapses; on timeout returns a user-facing error.
func acquireRegistryLock(lockPath string, timeout time.Duration) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("creating lock parent: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}

	deadline := time.Now().Add(timeout)
	backoff := 10 * time.Millisecond
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errIsWouldBlock(err) {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another serf plugin operation is in progress (locked: %s)", lockPath)
		}
		time.Sleep(backoff)
		if backoff < 200*time.Millisecond {
			backoff *= 2
		}
	}
}

func errIsWouldBlock(err error) bool {
	return err == unix.EWOULDBLOCK || err == unix.EAGAIN
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestAcquireRegistryLock -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/locks.go internal/plugins/locks_test.go
git commit -m "plugins: acquireRegistryLock with flock + exponential backoff"
```

---

## Phase 5 — Resolver interfaces (SP3 collaborator boundary)

### Task 5.1: Declare `MarketplaceResolver`, `PluginSource`

**Files:**
- Create: `internal/plugins/resolver.go`

- [ ] **Step 1: Write the type declarations**

```go
package plugins

import "context"

// MarketplaceResolver locates a marketplace and resolves a plugin entry to
// a fetchable source. SP3 owns the production implementation; SP4 owns
// this interface so install tests can stub it.
//
// Source: docs/superpowers/specs/2026-05-14-claude-code-compat-sp4-install-design.md §2.4
type MarketplaceResolver interface {
	Resolve(ctx context.Context, pluginName, marketplaceName string) (PluginSource, error)
}

// PluginSource is the source-type-erased view of a plugin's bytes. SP3
// produces it; SP4 calls Fetch and validates the result.
type PluginSource interface {
	// Fetch copies the plugin payload into destDir. destDir must exist, be
	// empty, and live under CacheRoot. On error, Fetch must leave destDir
	// empty so SP4's rollback can rmdir it cleanly.
	Fetch(ctx context.Context, destDir string) error
	// DeclaredVersion returns the marketplace-declared version (may be "").
	DeclaredVersion() string
	// CommitSHA returns the git commit SHA at the source's current ref,
	// or "" for non-git sources.
	CommitSHA() string
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/plugins/...`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/resolver.go
git commit -m "plugins: declare MarketplaceResolver / PluginSource interfaces"
```

### Task 5.2: In-test stub resolver

**Files:**
- Create: `internal/plugins/stub_resolver_test.go`

- [ ] **Step 1: Write the stub (test-only file)**

```go
package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// stubSource is a test PluginSource that copies a fixture directory into
// destDir. Configure declared/sha fields per test.
type stubSource struct {
	fixtureDir string
	declared   string
	sha        string
	fetchErr   error
}

func (s *stubSource) Fetch(_ context.Context, destDir string) error {
	if s.fetchErr != nil {
		return s.fetchErr
	}
	return copyTree(s.fixtureDir, destDir)
}
func (s *stubSource) DeclaredVersion() string { return s.declared }
func (s *stubSource) CommitSHA() string       { return s.sha }

// stubResolver maps "plugin@marketplace" to a stubSource.
type stubResolver struct {
	byKey   map[string]*stubSource
	missing map[string]bool
}

func newStubResolver() *stubResolver {
	return &stubResolver{byKey: map[string]*stubSource{}, missing: map[string]bool{}}
}

func (r *stubResolver) Resolve(_ context.Context, plugin, market string) (PluginSource, error) {
	key := plugin + "@" + market
	if r.missing[key] {
		return nil, fmt.Errorf("not found")
	}
	src, ok := r.byKey[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return src, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644)
	})
}
```

- [ ] **Step 2: Verify it builds**

Run: `go test ./internal/plugins/ -run XXXNoMatch -v`
Expected: no failures, no panics, just "no tests to run."

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/stub_resolver_test.go
git commit -m "plugins: test-only stub MarketplaceResolver / PluginSource"
```

---

## Phase 6 — Installer struct + Install

### Task 6.1: Declare `Installer`, `InstallRequest`, `InstallResult`

**Files:**
- Create: `internal/plugins/install.go`

- [ ] **Step 1: Write the type declarations**

```go
package plugins

import (
	"context"
	"io"
	"time"
)

// Installer is the entry point for every mutating operation. Construct
// once per CLI invocation; it owns the registry file lock and cache root.
type Installer struct {
	RegistryPath string
	CacheRoot    string
	GlobalConfig string
	ProjectRoot  string
	Marketplaces MarketplaceResolver
	Now          func() time.Time
	Stderr       io.Writer
	LockTimeout  time.Duration
}

// InstallRequest names one plugin to install at one scope.
type InstallRequest struct {
	Plugin      string
	Marketplace string
	Scope       Scope
	Version     string
	Pin         bool
	Force       bool
	NoEnable    bool
}

// InstallResult describes one completed install.
type InstallResult struct {
	Plugin      string
	Marketplace string
	Scope       Scope
	Version     string
	InstallPath string
	Enabled     bool
	AlreadyAt   bool
}

// UninstallRequest, UpdateRequest, EnableRequest, DisableRequest, ListEntry
// are declared here so the whole public surface is visible in one place.

type UninstallRequest struct {
	Plugin      string
	Marketplace string
	Scope       Scope
	KeepData    bool
}

type UpdateRequest struct {
	Plugin      string
	Marketplace string
	Scope       Scope
	Force       bool
}

type EnableRequest struct {
	Plugin      string
	Marketplace string
	Scope       Scope
	Pin         bool
}

type DisableRequest struct {
	Plugin      string
	Marketplace string
	Scope       Scope
}

type ListEntry struct {
	Plugin       string
	Marketplace  string
	Scope        Scope
	Version      string
	InstallPath  string
	InstalledAt  time.Time
	LastUpdated  time.Time
	GitCommitSha string
	Enabled      bool
}

// nowOr returns i.Now() if set, else time.Now().UTC().
func (i *Installer) nowOr() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now().UTC()
}

// stub bodies — implemented in later tasks.
func (i *Installer) Install(ctx context.Context, req InstallRequest) (InstallResult, error) {
	return InstallResult{}, errNotImplemented("Install")
}
func (i *Installer) Uninstall(ctx context.Context, req UninstallRequest) error {
	return errNotImplemented("Uninstall")
}
func (i *Installer) Update(ctx context.Context, req UpdateRequest) (InstallResult, error) {
	return InstallResult{}, errNotImplemented("Update")
}
func (i *Installer) UpdateAll(ctx context.Context, scope Scope) ([]InstallResult, error) {
	return nil, errNotImplemented("UpdateAll")
}
func (i *Installer) Enable(ctx context.Context, req EnableRequest) error {
	return errNotImplemented("Enable")
}
func (i *Installer) Disable(ctx context.Context, req DisableRequest) error {
	return errNotImplemented("Disable")
}
func (i *Installer) List(ctx context.Context, scope Scope) ([]ListEntry, error) {
	return nil, errNotImplemented("List")
}

func errNotImplemented(op string) error {
	return &notImplementedError{op: op}
}

type notImplementedError struct{ op string }

func (e *notImplementedError) Error() string { return e.op + " not implemented" }
```

- [ ] **Step 2: Build**

Run: `go build ./internal/plugins/...`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install.go
git commit -m "plugins: declare Installer struct and public request/result types"
```

### Task 6.2: `parsePluginSpec` helper

**Files:**
- Modify: `internal/plugins/install.go`
- Create test additions in `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"strings"
	"testing"
)

func TestParsePluginSpec(t *testing.T) {
	cases := []struct {
		name        string
		spec        string
		flag        string
		wantPlugin  string
		wantMarket  string
		wantErrSub  string
	}{
		{"bare name + flag", "foo", "bar", "foo", "bar", ""},
		{"suffix only", "foo@bar", "", "foo", "bar", ""},
		{"suffix + matching flag", "foo@bar", "bar", "foo", "bar", ""},
		{"suffix mismatches flag", "foo@bar", "baz", "", "", `"foo@bar" but --marketplace "baz"`},
		{"no marketplace anywhere", "foo", "", "", "", "requires a marketplace"},
		{"empty spec", "", "", "", "", "requires"},
		{"plugin with @ in name keeps last @", "foo@bar@mkt", "", "foo@bar", "mkt", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, m, err := parsePluginSpec(tc.spec, tc.flag)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("err = %v, want sub %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if p != tc.wantPlugin || m != tc.wantMarket {
				t.Errorf("got (%q,%q), want (%q,%q)", p, m, tc.wantPlugin, tc.wantMarket)
			}
		})
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestParsePluginSpec -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `install.go`:

```go
import "strings"

// parsePluginSpec splits "name@marketplace" on the last @. flagMarket, if
// non-empty, overrides the suffix; a mismatch is a hard error.
func parsePluginSpec(spec, flagMarket string) (plugin, marketplace string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("plugin spec is empty; requires a name and marketplace")
	}
	at := strings.LastIndex(spec, "@")
	if at >= 0 {
		plugin = spec[:at]
		marketplace = spec[at+1:]
	} else {
		plugin = spec
	}
	if flagMarket != "" {
		if marketplace != "" && marketplace != flagMarket {
			return "", "", fmt.Errorf(`plugin spec "%s" but --marketplace "%s"`, spec, flagMarket)
		}
		marketplace = flagMarket
	}
	if plugin == "" {
		return "", "", fmt.Errorf(`plugin spec "%s" has an empty name`, spec)
	}
	if marketplace == "" {
		return "", "", fmt.Errorf(`plugin "%s" requires a marketplace; pass plugin@marketplace or --marketplace <name>`, plugin)
	}
	return plugin, marketplace, nil
}
```

Add `"fmt"` and `"strings"` to imports (merged into existing block).

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestParsePluginSpec -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: parsePluginSpec handles suffix and flag forms"
```

### Task 6.3: Sanitize cache path components

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSanitizePathComponent(t *testing.T) {
	cases := []struct {
		in      string
		wantErr string
	}{
		{"plugin-name", ""},
		{"plug.name_2", ""},
		{"../escape", "invalid"},
		{"..foo", "invalid"},
		{"a/b", "invalid"},
		{"", "invalid"},
		{".", "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := sanitizePathComponent(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want sub %q", err, tc.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestSanitizePathComponent -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `install.go`:

```go
// sanitizePathComponent rejects names that would escape the cache root.
func sanitizePathComponent(s string) error {
	if s == "" || s == "." || s == ".." {
		return fmt.Errorf("invalid name %q", s)
	}
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("invalid name %q (no path separators)", s)
	}
	if strings.HasPrefix(s, "..") {
		return fmt.Errorf("invalid name %q (leading ..)", s)
	}
	return nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestSanitizePathComponent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: sanitizePathComponent for cache path safety"
```

### Task 6.4: Helper — read `plugin.json.version` from cache dir

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`
- Create: `internal/plugins/testdata/plugin_with_version.json`, `internal/plugins/testdata/plugin_no_version.json`, `internal/plugins/testdata/plugin_invalid_version.json`

- [ ] **Step 1: Add three fixtures**

`testdata/plugin_with_version.json`:

```json
{"name": "x", "version": "1.2.0"}
```

`testdata/plugin_no_version.json`:

```json
{"name": "x"}
```

`testdata/plugin_invalid_version.json`:

```json
{"name": "x", "version": 42}
```

- [ ] **Step 2: Write the failing test**

```go
func TestReadPluginJSONVersion(t *testing.T) {
	cases := []struct {
		name        string
		fixture     string
		exists      bool
		wantHas     bool
		wantVal     any
		wantErrSub  string
	}{
		{"no plugin.json", "", false, false, nil, ""},
		{"plugin.json with version", "testdata/plugin_with_version.json", true, true, "1.2.0", ""},
		{"plugin.json without version", "testdata/plugin_no_version.json", true, true, nil, ""},
		{"plugin.json with non-string version", "testdata/plugin_invalid_version.json", true, true, float64(42), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.exists {
				b, err := os.ReadFile(tc.fixture)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0644); err != nil {
					t.Fatal(err)
				}
			}
			has, val, err := readPluginJSONVersion(dir)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("err = %v, want sub %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if has != tc.wantHas {
				t.Errorf("has = %v, want %v", has, tc.wantHas)
			}
			if val != tc.wantVal {
				t.Errorf("val = %v (%T), want %v (%T)", val, val, tc.wantVal, tc.wantVal)
			}
		})
	}
}
```

Add `"os"`, `"path/filepath"` to test imports.

- [ ] **Step 3: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestReadPluginJSONVersion -v`
Expected: FAIL.

- [ ] **Step 4: Implement**

Append to `install.go`:

```go
import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// readPluginJSONVersion reads <cacheDir>/plugin.json and returns:
//   - has: true if the file exists and parses as a JSON object
//   - val: the raw value of plugin.json.version (nil if absent)
//   - err: parse error (not a missing-file error)
func readPluginJSONVersion(cacheDir string) (has bool, val any, err error) {
	path := filepath.Join(cacheDir, "plugin.json")
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("reading %s: %w", path, rerr)
	}
	var top map[string]any
	if jerr := json.Unmarshal(data, &top); jerr != nil {
		return false, nil, fmt.Errorf("parsing %s: %w", path, jerr)
	}
	return true, top["version"], nil
}
```

(Merge imports.)

- [ ] **Step 5: Run — PASS**

Run: `go test ./internal/plugins/ -run TestReadPluginJSONVersion -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go internal/plugins/testdata/plugin_with_version.json internal/plugins/testdata/plugin_no_version.json internal/plugins/testdata/plugin_invalid_version.json
git commit -m "plugins: readPluginJSONVersion with fixtures"
```

### Task 6.5: `pathForScope` helper

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPathForScope(t *testing.T) {
	i := &Installer{GlobalConfig: "/home/u/.config/serf/config.json", ProjectRoot: "/repo"}
	cases := []struct {
		scope    Scope
		want     string
		wantErr  string
	}{
		{ScopeUser, "/home/u/.config/serf/config.json", ""},
		{ScopeProject, "/repo/.serf/config.json", ""},
		{ScopeLocal, "", "not yet supported"},
		{ScopeManaged, "", "not yet supported"},
		{Scope(""), "", "not yet supported"},
	}
	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			got, err := i.pathForScope(tc.scope)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want sub %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestPathForScope_ProjectRequiresGitRepo(t *testing.T) {
	i := &Installer{GlobalConfig: "/g", ProjectRoot: ""}
	_, err := i.pathForScope(ScopeProject)
	if err == nil || !strings.Contains(err.Error(), "requires a git repository") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestPathForScope -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `install.go`:

```go
func (i *Installer) pathForScope(scope Scope) (string, error) {
	switch scope {
	case ScopeUser:
		return i.GlobalConfig, nil
	case ScopeProject:
		if i.ProjectRoot == "" {
			return "", fmt.Errorf("--scope project requires a git repository")
		}
		return filepath.Join(i.ProjectRoot, ".serf", "config.json"), nil
	default:
		return "", fmt.Errorf(`scope %q is not yet supported in serf`, string(scope))
	}
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestPathForScope -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: pathForScope resolves user/project config paths"
```

### Task 6.6: Helper — upsert registry entry

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpsertEntry_PreservesInstalledAt(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	r := Registry{Version: 2, Plugins: map[string][]InstallEntry{
		"x@y": {{Scope: ScopeUser, InstallPath: "/c/y/x/1", Version: "1", InstalledAt: old, LastUpdated: old}},
	}}
	updated := upsertEntry(r, "x@y", InstallEntry{
		Scope:       ScopeUser,
		InstallPath: "/c/y/x/2",
		Version:     "2",
		InstalledAt: now,
		LastUpdated: now,
	})
	got := updated.Plugins["x@y"][0]
	if !got.InstalledAt.Equal(old) {
		t.Errorf("InstalledAt = %v, want preserved %v", got.InstalledAt, old)
	}
	if !got.LastUpdated.Equal(now) {
		t.Errorf("LastUpdated = %v, want %v", got.LastUpdated, now)
	}
	if got.Version != "2" {
		t.Errorf("Version not updated")
	}
}

func TestUpsertEntry_AppendsNewScope(t *testing.T) {
	now := time.Now()
	r := Registry{Version: 2, Plugins: map[string][]InstallEntry{
		"x@y": {{Scope: ScopeUser, InstallPath: "/c/y/x/1", Version: "1", InstalledAt: now, LastUpdated: now}},
	}}
	updated := upsertEntry(r, "x@y", InstallEntry{
		Scope: ScopeProject, InstallPath: "/c/y/x/1", Version: "1", InstalledAt: now, LastUpdated: now,
	})
	if len(updated.Plugins["x@y"]) != 2 {
		t.Errorf("entries = %d, want 2", len(updated.Plugins["x@y"]))
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestUpsertEntry -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `install.go`:

```go
// upsertEntry inserts or replaces the entry for (key, e.Scope) in r. When
// replacing, the existing InstalledAt is preserved (lifecycle: only a first
// install advances it).
func upsertEntry(r Registry, key string, e InstallEntry) Registry {
	entries := r.Plugins[key]
	for i, existing := range entries {
		if existing.Scope == e.Scope {
			e.InstalledAt = existing.InstalledAt
			entries[i] = e
			r.Plugins[key] = entries
			return r
		}
	}
	r.Plugins[key] = append(entries, e)
	return r
}

// removeEntry deletes the (key, scope) entry. Returns the new registry and
// true if a removal happened.
func removeEntry(r Registry, key string, scope Scope) (Registry, bool) {
	entries := r.Plugins[key]
	for i, e := range entries {
		if e.Scope == scope {
			r.Plugins[key] = append(entries[:i], entries[i+1:]...)
			if len(r.Plugins[key]) == 0 {
				delete(r.Plugins, key)
			}
			return r, true
		}
	}
	return r, false
}

// findEntry returns the entry for (key, scope) and whether it exists.
func findEntry(r Registry, key string, scope Scope) (InstallEntry, bool) {
	for _, e := range r.Plugins[key] {
		if e.Scope == scope {
			return e, true
		}
	}
	return InstallEntry{}, false
}
```

Add `"time"` to imports.

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestUpsertEntry -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: upsertEntry/removeEntry/findEntry helpers"
```

### Task 6.7: Helper — apply enable/disable to a `rawConfig`

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestApplyEnable(t *testing.T) {
	cfg := rawConfig{
		enabledPlugins: map[string]json.RawMessage{},
		other:          map[string]json.RawMessage{},
	}
	applyEnable(&cfg, "x@y", "1.0.0", false)
	if string(cfg.enabledPlugins["x@y"]) != "true" {
		t.Errorf("got %s, want true", string(cfg.enabledPlugins["x@y"]))
	}
	applyEnable(&cfg, "x@y", "1.0.0", true)
	if string(cfg.enabledPlugins["x@y"]) != `{"version": "1.0.0"}` {
		t.Errorf("got %s, want pinned object", string(cfg.enabledPlugins["x@y"]))
	}
}

func TestApplyDisable(t *testing.T) {
	cfg := rawConfig{
		enabledPlugins: map[string]json.RawMessage{"x@y": json.RawMessage("true")},
		other:          map[string]json.RawMessage{},
	}
	applyDisable(&cfg, "x@y")
	if _, ok := cfg.enabledPlugins["x@y"]; ok {
		t.Errorf("key should be deleted")
	}
	applyDisable(&cfg, "x@y") // idempotent
}
```

Add `"encoding/json"` to imports.

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run "TestApplyEnable|TestApplyDisable" -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `install.go`:

```go
func applyEnable(cfg *rawConfig, key, version string, pin bool) {
	if pin {
		cfg.enabledPlugins[key] = json.RawMessage(fmt.Sprintf(`{"version": %q}`, version))
		return
	}
	cfg.enabledPlugins[key] = json.RawMessage("true")
}

func applyDisable(cfg *rawConfig, key string) {
	delete(cfg.enabledPlugins, key)
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run "TestApplyEnable|TestApplyDisable" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: applyEnable/applyDisable mutate rawConfig in place"
```

### Task 6.8: `Install` — happy path (fresh install at user scope, enable, commit)

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func newTestInstaller(t *testing.T) *Installer {
	t.Helper()
	root := t.TempDir()
	return &Installer{
		RegistryPath: filepath.Join(root, "installed_plugins.json"),
		CacheRoot:    filepath.Join(root, "cache"),
		GlobalConfig: filepath.Join(root, "config.json"),
		ProjectRoot:  filepath.Join(root, "repo"),
		Marketplaces: newStubResolver(),
		Now:          func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) },
		Stderr:       io.Discard,
		LockTimeout:  2 * time.Second,
	}
}

// fixture creates a minimal plugin payload dir for stubSource.Fetch to copy.
func newFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInstall_HappyPath_UserScope(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{
		"plugin.json": `{"name":"formatter","version":"1.0.0"}`,
	})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["formatter@mkt"] = &stubSource{fixtureDir: fix, declared: "1.0.0", sha: "abc1234567890abc"}

	res, err := i.Install(context.Background(), InstallRequest{Plugin: "formatter@mkt", Scope: ScopeUser})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", res.Version)
	}
	if !res.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if res.AlreadyAt {
		t.Errorf("AlreadyAt = true, want false")
	}
	want := filepath.Join(i.CacheRoot, "mkt", "formatter", "1.0.0")
	if res.InstallPath != want {
		t.Errorf("InstallPath = %q, want %q", res.InstallPath, want)
	}
	if _, err := os.Stat(filepath.Join(want, "plugin.json")); err != nil {
		t.Errorf("plugin.json not copied: %v", err)
	}

	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := findEntry(reg, "formatter@mkt", ScopeUser)
	if !ok {
		t.Fatal("registry missing entry")
	}
	if e.Version != "1.0.0" || e.GitCommitSha != "abc1234567890abc" {
		t.Errorf("entry = %+v", e)
	}

	cfg, err := loadRawConfig(i.GlobalConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.enabledPlugins["formatter@mkt"]) != "true" {
		t.Errorf("enabledPlugins missing: %v", cfg.enabledPlugins)
	}
}
```

Add `"context"`, `"io"` to test imports.

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestInstall_HappyPath_UserScope -v`
Expected: FAIL — `Install not implemented`.

- [ ] **Step 3: Implement `Install`**

Replace the stub `Install` body in `install.go`:

```go
func (i *Installer) Install(ctx context.Context, req InstallRequest) (InstallResult, error) {
	if req.Scope == "" {
		req.Scope = ScopeUser
	}
	if req.Scope != ScopeUser && req.Scope != ScopeProject {
		return InstallResult{}, fmt.Errorf(`serf plugin install: scope %q is not yet supported in serf`, string(req.Scope))
	}
	plugin, market, err := parsePluginSpec(req.Plugin, req.Marketplace)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}
	if err := sanitizePathComponent(plugin); err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}
	if err := sanitizePathComponent(market); err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}
	configPath, err := i.pathForScope(req.Scope)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}

	release, err := acquireRegistryLock(i.RegistryPath+".lock", i.lockTimeoutOr())
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}
	defer release()

	src, err := i.Marketplaces.Resolve(ctx, plugin, market)
	if err != nil {
		return InstallResult{}, fmt.Errorf(`serf plugin install: plugin "%s@%s" is not in any known marketplace; run 'serf plugin marketplace add ...': %w`, plugin, market, err)
	}

	// Pre-fetch version guess (without plugin.json): may shift to plugin.json
	// version after fetch. We need a directory name to fetch into. Resolve to
	// a temporary cache dir, then rename to the final version dir afterwards.
	tmpVersion, err := computeVersion(req.Version, src.DeclaredVersion(), src.CommitSHA(), false, nil)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}

	stagingDir := filepath.Join(i.CacheRoot, market, plugin, tmpVersion)
	key := plugin + "@" + market

	// Check if (key, scope, version) is already registered and not Force.
	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}
	if reg.Plugins == nil {
		reg.Plugins = map[string][]InstallEntry{}
	}
	if existing, ok := findEntry(reg, key, req.Scope); ok && existing.Version == tmpVersion && !req.Force {
		// short-circuit; still ensure enable if requested
		res := InstallResult{
			Plugin:      plugin,
			Marketplace: market,
			Scope:       req.Scope,
			Version:     existing.Version,
			InstallPath: existing.InstallPath,
			AlreadyAt:   true,
		}
		if !req.NoEnable {
			if err := i.writeEnable(configPath, key, existing.Version, req.Pin); err != nil {
				fmt.Fprintf(i.stderrOr(), "warning: enable failed: %v\n", err)
			} else {
				res.Enabled = true
			}
		}
		return res, nil
	}

	if err := prepareCacheDir(stagingDir, req.Force, i.stderrOr()); err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
	}

	if err := src.Fetch(ctx, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin install: fetching "%s@%s": %w`, plugin, market, err)
	}

	// Post-fetch validation.
	entries, rerr := os.ReadDir(stagingDir)
	if rerr != nil || len(entries) == 0 {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin install: validating "%s@%s": fetched cache dir is empty`, plugin, market)
	}
	hasPJ, pjVer, err := readPluginJSONVersion(stagingDir)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin install: validating "%s@%s": %w`, plugin, market, err)
	}
	finalVersion, err := computeVersion(req.Version, src.DeclaredVersion(), src.CommitSHA(), hasPJ, pjVer)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin install: validating "%s@%s": %w`, plugin, market, err)
	}

	// If the resolved version differs from the staging dir's name, move.
	finalDir := filepath.Join(i.CacheRoot, market, plugin, finalVersion)
	if finalDir != stagingDir {
		if err := os.MkdirAll(filepath.Dir(finalDir), 0755); err != nil {
			_ = os.RemoveAll(stagingDir)
			return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
		}
		if _, err := os.Stat(finalDir); err == nil {
			if !req.Force {
				_ = os.RemoveAll(stagingDir)
				existing, ok := findEntry(reg, key, req.Scope)
				if ok && existing.Version == finalVersion {
					res := InstallResult{Plugin: plugin, Marketplace: market, Scope: req.Scope, Version: finalVersion, InstallPath: finalDir, AlreadyAt: true}
					if !req.NoEnable {
						if err := i.writeEnable(configPath, key, finalVersion, req.Pin); err != nil {
							fmt.Fprintf(i.stderrOr(), "warning: enable failed: %v\n", err)
						} else {
							res.Enabled = true
						}
					}
					return res, nil
				}
			}
			_ = os.RemoveAll(finalDir)
		}
		if err := os.Rename(stagingDir, finalDir); err != nil {
			_ = os.RemoveAll(stagingDir)
			return InstallResult{}, fmt.Errorf("serf plugin install: %w", err)
		}
	}

	now := i.nowOr()
	entry := InstallEntry{
		Scope:        req.Scope,
		InstallPath:  finalDir,
		Version:      finalVersion,
		InstalledAt:  now,
		LastUpdated:  now,
		GitCommitSha: src.CommitSHA(),
	}
	reg = upsertEntry(reg, key, entry)
	if err := SaveRegistry(i.RegistryPath, reg); err != nil {
		_ = os.RemoveAll(finalDir)
		return InstallResult{}, fmt.Errorf("serf plugin install: writing installed_plugins.json: %w", err)
	}

	res := InstallResult{
		Plugin:      plugin,
		Marketplace: market,
		Scope:       req.Scope,
		Version:     finalVersion,
		InstallPath: finalDir,
	}
	if !req.NoEnable {
		if err := i.writeEnable(configPath, key, finalVersion, req.Pin); err != nil {
			return res, fmt.Errorf("serf plugin install: writing %s: %w", configPath, err)
		}
		res.Enabled = true
	}
	return res, nil
}

func (i *Installer) lockTimeoutOr() time.Duration {
	if i.LockTimeout > 0 {
		return i.LockTimeout
	}
	return 30 * time.Second
}

func (i *Installer) stderrOr() io.Writer {
	if i.Stderr != nil {
		return i.Stderr
	}
	return os.Stderr
}

// writeEnable mutates one scope's config.json: setting enabledPlugins[key].
func (i *Installer) writeEnable(configPath, key, version string, pin bool) error {
	cfg, err := loadRawConfig(configPath)
	if err != nil {
		return err
	}
	applyEnable(&cfg, key, version, pin)
	return writeRawConfig(configPath, cfg)
}

// prepareCacheDir ensures dir exists and is empty. If non-empty and force is
// false, this is treated as an orphan and wiped (logged).
func prepareCacheDir(dir string, force bool, stderr io.Writer) error {
	if entries, err := os.ReadDir(dir); err == nil {
		if len(entries) == 0 {
			return nil
		}
		if !force {
			fmt.Fprintf(stderr, "removing orphan cache dir %s\n", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("removing existing cache dir %s: %w", dir, err)
		}
	}
	return os.MkdirAll(dir, 0755)
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestInstall_HappyPath_UserScope -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: Install happy path with cache fetch and enable"
```

### Task 6.9: `Install` — re-install short-circuits (`AlreadyAt`)

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_AlreadyAt_NoForce(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	first, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	if first.AlreadyAt {
		t.Fatal("first install should not be AlreadyAt")
	}
	mtimeBefore, _ := os.Stat(first.InstallPath)

	second, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyAt {
		t.Errorf("second install should be AlreadyAt, got %+v", second)
	}
	mtimeAfter, _ := os.Stat(first.InstallPath)
	if mtimeAfter.ModTime() != mtimeBefore.ModTime() {
		t.Errorf("cache dir mtime advanced on AlreadyAt re-install")
	}
}

func TestInstall_Force_RefetchesAndAdvancesLastUpdated(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	i.Now = func() time.Time { return t1 }
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	i.Now = func() time.Time { return t2 }
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Force: true}); err != nil {
		t.Fatal(err)
	}
	reg, _ := LoadRegistry(i.RegistryPath)
	e, _ := findEntry(reg, "x@m", ScopeUser)
	if !e.InstalledAt.Equal(t1) {
		t.Errorf("InstalledAt = %v, want preserved %v", e.InstalledAt, t1)
	}
	if !e.LastUpdated.Equal(t2) {
		t.Errorf("LastUpdated = %v, want %v", e.LastUpdated, t2)
	}
}
```

- [ ] **Step 2: Run**

Run: `go test ./internal/plugins/ -run "TestInstall_AlreadyAt_NoForce|TestInstall_Force_RefetchesAndAdvancesLastUpdated" -v`
Expected: PASS (the prior Install implementation already handles both).

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install AlreadyAt and Force re-install paths"
```

### Task 6.10: `Install` — project scope writes `.serf/config.json`

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_ProjectScope_WritesProjectConfig(t *testing.T) {
	i := newTestInstaller(t)
	// ensure project root exists
	if err := os.MkdirAll(i.ProjectRoot, 0755); err != nil {
		t.Fatal(err)
	}
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeProject}); err != nil {
		t.Fatal(err)
	}
	projCfg := filepath.Join(i.ProjectRoot, ".serf", "config.json")
	cfg, err := loadRawConfig(projCfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.enabledPlugins["x@m"]) != "true" {
		t.Errorf("project config missing entry: %v", cfg.enabledPlugins)
	}
	if _, err := os.Stat(i.GlobalConfig); err == nil {
		gcfg, _ := loadRawConfig(i.GlobalConfig)
		if _, ok := gcfg.enabledPlugins["x@m"]; ok {
			t.Errorf("global config should not have entry")
		}
	}
}

func TestInstall_ProjectScope_RequiresGitRoot(t *testing.T) {
	i := newTestInstaller(t)
	i.ProjectRoot = ""
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	_, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeProject})
	if err == nil || !strings.Contains(err.Error(), "requires a git repository") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstall_ProjectScope -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install project-scope path"
```

### Task 6.11: `Install` — `--pin` writes `{version}`; `--no-enable` skips enable

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_Pin_WritesVersionObject(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Pin: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadRawConfig(i.GlobalConfig)
	got := string(cfg.enabledPlugins["x@m"])
	if got != `{"version": "1.0.0"}` {
		t.Errorf("got %s, want pinned object", got)
	}
}

func TestInstall_NoEnable_LeavesConfigAlone(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	res, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", NoEnable: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if _, err := os.Stat(i.GlobalConfig); err == nil {
		cfg, _ := loadRawConfig(i.GlobalConfig)
		if _, ok := cfg.enabledPlugins["x@m"]; ok {
			t.Errorf("enabledPlugins should be empty after --no-enable")
		}
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run "TestInstall_Pin|TestInstall_NoEnable" -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install --pin and --no-enable flags"
```

### Task 6.12: `Install` — explicit `--version` matching / mismatching plugin.json

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_ExplicitVersionMatching(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix}

	res, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.0.0" {
		t.Errorf("Version = %q", res.Version)
	}
}

func TestInstall_ExplicitVersionMismatch_RollsBack(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.1"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix}

	_, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Version: "1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	// cache dir gone
	if _, statErr := os.Stat(filepath.Join(i.CacheRoot, "m", "x")); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(filepath.Join(i.CacheRoot, "m", "x"))
		if len(entries) > 0 {
			t.Errorf("cache dir survived rollback: %v", entries)
		}
	}
	// registry unchanged
	reg, _ := LoadRegistry(i.RegistryPath)
	if _, ok := findEntry(reg, "x@m", ScopeUser); ok {
		t.Errorf("registry should have no entry")
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run "TestInstall_ExplicitVersion" -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install --version match and mismatch"
```

### Task 6.13: `Install` — rollback on `Fetch` error

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_RollbackOnFetchError(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, fetchErr: fmt.Errorf("network down")}

	_, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("err = %v", err)
	}
	// cache dir for staged version must not survive
	matches, _ := filepath.Glob(filepath.Join(i.CacheRoot, "m", "x", "*"))
	if len(matches) != 0 {
		t.Errorf("cache subdirs survived: %v", matches)
	}
	reg, _ := LoadRegistry(i.RegistryPath)
	if _, ok := findEntry(reg, "x@m", ScopeUser); ok {
		t.Errorf("registry should be empty")
	}
}
```

Add `"fmt"` to test imports if not present.

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstall_RollbackOnFetchError -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install rollback on Fetch error"
```

### Task 6.14: `Install` — rollback when validation reports invalid `version` type

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_RollbackOnInvalidVersionType(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":42}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: ""}

	_, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("err = %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(i.CacheRoot, "m", "x", "*"))
	if len(matches) != 0 {
		t.Errorf("cache survived rollback: %v", matches)
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstall_RollbackOnInvalidVersionType -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install rollback when plugin.json.version is non-string"
```

### Task 6.15: `Install` — fetch produces empty cache dir (SP3 misbehaved)

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_RollbackOnEmptyFetch(t *testing.T) {
	i := newTestInstaller(t)
	// fixture dir is empty -> stubSource.Fetch produces an empty cache dir
	emptyFix := t.TempDir()
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: emptyFix, declared: "1.0.0"}

	_, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), "fetched cache dir is empty") {
		t.Fatalf("err = %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(i.CacheRoot, "m", "x", "*"))
	if len(matches) != 0 {
		t.Errorf("cache survived rollback: %v", matches)
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstall_RollbackOnEmptyFetch -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install rollback when fetch produces empty dir"
```

### Task 6.16: `Install` — no `plugin.json` → version is short SHA

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_NoPluginJSON_UsesCommitSHA(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"README.md": "hi"})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, sha: "abcdef1234567890aaa"}

	res, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "abcdef123456" {
		t.Errorf("Version = %q, want short SHA", res.Version)
	}
	reg, _ := LoadRegistry(i.RegistryPath)
	e, _ := findEntry(reg, "x@m", ScopeUser)
	if e.GitCommitSha != "abcdef1234567890aaa" {
		t.Errorf("GitCommitSha = %q, want full", e.GitCommitSha)
	}
}

func TestInstall_DirectoryNotInGit_VersionUnknown(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"README.md": "hi"})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix} // sha empty

	res, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "unknown" {
		t.Errorf("Version = %q, want unknown", res.Version)
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run "TestInstall_NoPluginJSON|TestInstall_DirectoryNotInGit" -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install version fallback to SHA and unknown"
```

### Task 6.17: `Install` — two scopes share cache dir at same version

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_TwoScopesShareCacheDir(t *testing.T) {
	i := newTestInstaller(t)
	if err := os.MkdirAll(i.ProjectRoot, 0755); err != nil {
		t.Fatal(err)
	}
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	a, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeUser})
	if err != nil {
		t.Fatal(err)
	}
	b, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if a.InstallPath != b.InstallPath {
		t.Errorf("scopes have different paths: %q vs %q", a.InstallPath, b.InstallPath)
	}
	reg, _ := LoadRegistry(i.RegistryPath)
	if len(reg.Plugins["x@m"]) != 2 {
		t.Errorf("entries = %d, want 2", len(reg.Plugins["x@m"]))
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstall_TwoScopesShareCacheDir -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover two-scope install sharing cache dir"
```

### Task 6.18: `Install` — marketplace miss returns clear error

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstall_UnknownMarketplace(t *testing.T) {
	i := newTestInstaller(t)
	// no entries in stub resolver
	_, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), "not in any known marketplace") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstall_UnknownMarketplace -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/install_test.go
git commit -m "plugins: cover Install error on unknown marketplace"
```


---

## Phase 7 — Uninstall

### Task 7.1: `Uninstall` happy path

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUninstall_HappyPath(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	res, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}

	if err := i.Uninstall(context.Background(), UninstallRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	reg, _ := LoadRegistry(i.RegistryPath)
	if _, ok := findEntry(reg, "x@m", ScopeUser); ok {
		t.Errorf("registry entry remains")
	}
	cfg, _ := loadRawConfig(i.GlobalConfig)
	if _, ok := cfg.enabledPlugins["x@m"]; ok {
		t.Errorf("enabledPlugins not cleared")
	}
	if _, err := os.Stat(res.InstallPath); !os.IsNotExist(err) {
		t.Errorf("cache dir survived: %v", err)
	}
}

func TestUninstall_KeepsCacheWhenOtherScopeShares(t *testing.T) {
	i := newTestInstaller(t)
	if err := os.MkdirAll(i.ProjectRoot, 0755); err != nil {
		t.Fatal(err)
	}
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeUser}); err != nil {
		t.Fatal(err)
	}
	res, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}

	if err := i.Uninstall(context.Background(), UninstallRequest{Plugin: "x@m", Scope: ScopeUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.InstallPath); err != nil {
		t.Errorf("cache should remain (project scope still references it): %v", err)
	}
	reg, _ := LoadRegistry(i.RegistryPath)
	if len(reg.Plugins["x@m"]) != 1 {
		t.Errorf("entries = %d, want 1", len(reg.Plugins["x@m"]))
	}
}

func TestUninstall_NotInstalledIsError(t *testing.T) {
	i := newTestInstaller(t)
	err := i.Uninstall(context.Background(), UninstallRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), `not installed at scope "user"`) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run — FAIL (not implemented)**

Run: `go test ./internal/plugins/ -run TestUninstall -v`
Expected: FAIL.

- [ ] **Step 3: Implement `Uninstall`**

Replace the stub body in `install.go`:

```go
func (i *Installer) Uninstall(ctx context.Context, req UninstallRequest) error {
	if req.Scope == "" {
		req.Scope = ScopeUser
	}
	if req.Scope != ScopeUser && req.Scope != ScopeProject {
		return fmt.Errorf(`serf plugin uninstall: scope %q is not yet supported in serf`, string(req.Scope))
	}
	plugin, market, err := parsePluginSpec(req.Plugin, req.Marketplace)
	if err != nil {
		return fmt.Errorf("serf plugin uninstall: %w", err)
	}
	configPath, err := i.pathForScope(req.Scope)
	if err != nil {
		return fmt.Errorf("serf plugin uninstall: %w", err)
	}
	if req.KeepData {
		fmt.Fprintf(i.stderrOr(), "--keep-data is reserved; serf does not maintain plugin data directories yet\n")
	}

	release, err := acquireRegistryLock(i.RegistryPath+".lock", i.lockTimeoutOr())
	if err != nil {
		return fmt.Errorf("serf plugin uninstall: %w", err)
	}
	defer release()

	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		return fmt.Errorf("serf plugin uninstall: %w", err)
	}
	key := plugin + "@" + market
	entry, ok := findEntry(reg, key, req.Scope)
	if !ok {
		return fmt.Errorf(`serf plugin uninstall: plugin "%s" is not installed at scope %q`, key, string(req.Scope))
	}

	// Disable in config.json (best effort; do not block uninstall).
	cfg, lerr := loadRawConfig(configPath)
	if lerr == nil {
		applyDisable(&cfg, key)
		if werr := writeRawConfig(configPath, cfg); werr != nil {
			fmt.Fprintf(i.stderrOr(), "warning: writing %s: %v\n", configPath, werr)
		}
	} else {
		fmt.Fprintf(i.stderrOr(), "warning: reading %s: %v\n", configPath, lerr)
	}

	reg, _ = removeEntry(reg, key, req.Scope)
	if err := SaveRegistry(i.RegistryPath, reg); err != nil {
		return fmt.Errorf("serf plugin uninstall: writing installed_plugins.json: %w", err)
	}

	// GC cache dir if no other scope references it.
	stillReferenced := false
	for _, e := range reg.Plugins[key] {
		if e.InstallPath == entry.InstallPath {
			stillReferenced = true
			break
		}
	}
	if !stillReferenced {
		if err := os.RemoveAll(entry.InstallPath); err != nil {
			fmt.Fprintf(i.stderrOr(), "warning: removing cache %s: %v\n", entry.InstallPath, err)
		}
		// Walk up to but not including CacheRoot.
		dir := filepath.Dir(entry.InstallPath)
		for strings.HasPrefix(dir, i.CacheRoot) && dir != i.CacheRoot {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(dir); err != nil {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestUninstall -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: Uninstall with shared-scope GC and config.json disable"
```

---

## Phase 8 — Update / UpdateAll

### Task 8.1: `Update` — happy path advances version + GCs old dir

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpdate_AdvancesVersionAndGCsOldDir(t *testing.T) {
	i := newTestInstaller(t)
	fixV1 := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	fixV2 := newFixture(t, map[string]string{"plugin.json": `{"version":"2.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV1, declared: "1.0.0"}

	first, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV2, declared: "2.0.0"}

	res, err := i.Update(context.Background(), UpdateRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "2.0.0" {
		t.Errorf("Version = %q want 2.0.0", res.Version)
	}
	if _, err := os.Stat(first.InstallPath); !os.IsNotExist(err) {
		t.Errorf("old cache dir survived GC: %v", err)
	}
}

func TestUpdate_NoOpWhenSourceUnchanged(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	res, err := i.Update(context.Background(), UpdateRequest{Plugin: "x@m"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyAt {
		t.Errorf("AlreadyAt = false, want true on unchanged source")
	}
}

func TestUpdate_NotInstalledIsError(t *testing.T) {
	i := newTestInstaller(t)
	err := struct{ E error }{}
	_, e := i.Update(context.Background(), UpdateRequest{Plugin: "x@m"})
	err.E = e
	if err.E == nil || !strings.Contains(err.E.Error(), "not installed") {
		t.Fatalf("err = %v", err.E)
	}
}

func TestUpdate_PinnedEnabledPlugins_Rewritten(t *testing.T) {
	i := newTestInstaller(t)
	fixV1 := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	fixV2 := newFixture(t, map[string]string{"plugin.json": `{"version":"2.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV1, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Pin: true}); err != nil {
		t.Fatal(err)
	}
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV2, declared: "2.0.0"}
	if _, err := i.Update(context.Background(), UpdateRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadRawConfig(i.GlobalConfig)
	if string(cfg.enabledPlugins["x@m"]) != `{"version": "2.0.0"}` {
		t.Errorf("got %s, want pinned to 2.0.0", string(cfg.enabledPlugins["x@m"]))
	}
}

func TestUpdate_BareTrueEnabledPlugins_LeftAlone(t *testing.T) {
	i := newTestInstaller(t)
	fixV1 := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	fixV2 := newFixture(t, map[string]string{"plugin.json": `{"version":"2.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV1, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV2, declared: "2.0.0"}
	if _, err := i.Update(context.Background(), UpdateRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadRawConfig(i.GlobalConfig)
	if string(cfg.enabledPlugins["x@m"]) != "true" {
		t.Errorf("got %s, want bare true", string(cfg.enabledPlugins["x@m"]))
	}
}

func TestUpdate_OtherScopeStillReferencesOldVersion(t *testing.T) {
	i := newTestInstaller(t)
	if err := os.MkdirAll(i.ProjectRoot, 0755); err != nil {
		t.Fatal(err)
	}
	fixV1 := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	fixV2 := newFixture(t, map[string]string{"plugin.json": `{"version":"2.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV1, declared: "1.0.0"}
	a, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeUser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", Scope: ScopeProject}); err != nil {
		t.Fatal(err)
	}
	r.byKey["x@m"] = &stubSource{fixtureDir: fixV2, declared: "2.0.0"}
	if _, err := i.Update(context.Background(), UpdateRequest{Plugin: "x@m", Scope: ScopeUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.InstallPath); err != nil {
		t.Errorf("old cache should remain (project still references): %v", err)
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestUpdate -v`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement `Update`**

Replace the stub body in `install.go`:

```go
func (i *Installer) Update(ctx context.Context, req UpdateRequest) (InstallResult, error) {
	if req.Scope == "" {
		req.Scope = ScopeUser
	}
	if req.Scope != ScopeUser && req.Scope != ScopeProject {
		return InstallResult{}, fmt.Errorf(`serf plugin update: scope %q is not yet supported in serf`, string(req.Scope))
	}
	plugin, market, err := parsePluginSpec(req.Plugin, req.Marketplace)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin update: %w", err)
	}
	configPath, err := i.pathForScope(req.Scope)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin update: %w", err)
	}

	release, err := acquireRegistryLock(i.RegistryPath+".lock", i.lockTimeoutOr())
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin update: %w", err)
	}
	defer release()

	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin update: %w", err)
	}
	key := plugin + "@" + market
	existing, ok := findEntry(reg, key, req.Scope)
	if !ok {
		return InstallResult{}, fmt.Errorf(`serf plugin update: plugin "%s" is not installed at scope %q`, key, string(req.Scope))
	}

	src, err := i.Marketplaces.Resolve(ctx, plugin, market)
	if err != nil {
		return InstallResult{}, fmt.Errorf(`serf plugin update: resolving "%s@%s": %w`, plugin, market, err)
	}

	stagingDir := filepath.Join(i.CacheRoot, market, plugin, ".staging")
	if err := prepareCacheDir(stagingDir, true, i.stderrOr()); err != nil {
		return InstallResult{}, fmt.Errorf("serf plugin update: %w", err)
	}
	if err := src.Fetch(ctx, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin update: fetching "%s@%s": %w`, plugin, market, err)
	}
	hasPJ, pjVer, err := readPluginJSONVersion(stagingDir)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin update: validating "%s@%s": %w`, plugin, market, err)
	}
	newVersion, err := computeVersion("", src.DeclaredVersion(), src.CommitSHA(), hasPJ, pjVer)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf(`serf plugin update: validating "%s@%s": %w`, plugin, market, err)
	}

	if newVersion == existing.Version && !req.Force {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{
			Plugin: plugin, Marketplace: market, Scope: req.Scope,
			Version: existing.Version, InstallPath: existing.InstallPath, AlreadyAt: true,
		}, nil
	}

	newDir := filepath.Join(i.CacheRoot, market, plugin, newVersion)
	if _, err := os.Stat(newDir); err == nil {
		_ = os.RemoveAll(newDir)
	}
	if err := os.Rename(stagingDir, newDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return InstallResult{}, fmt.Errorf("serf plugin update: %w", err)
	}

	now := i.nowOr()
	entry := InstallEntry{
		Scope:        req.Scope,
		InstallPath:  newDir,
		Version:      newVersion,
		InstalledAt:  existing.InstalledAt,
		LastUpdated:  now,
		GitCommitSha: src.CommitSHA(),
	}
	reg = upsertEntry(reg, key, entry)
	if err := SaveRegistry(i.RegistryPath, reg); err != nil {
		_ = os.RemoveAll(newDir)
		return InstallResult{}, fmt.Errorf("serf plugin update: writing installed_plugins.json: %w", err)
	}

	// updateEnabledIfPinned: rewrite {version: oldVer} → {version: newVer}; leave true alone.
	if cfg, lerr := loadRawConfig(configPath); lerr == nil {
		if raw, ok := cfg.enabledPlugins[key]; ok && len(raw) > 0 && raw[0] == '{' {
			cfg.enabledPlugins[key] = json.RawMessage(fmt.Sprintf(`{"version": %q}`, newVersion))
			if werr := writeRawConfig(configPath, cfg); werr != nil {
				fmt.Fprintf(i.stderrOr(), "warning: writing %s: %v\n", configPath, werr)
			}
		}
	}

	// GC old cache dir if no other scope still references it.
	stillReferenced := false
	for _, e := range reg.Plugins[key] {
		if e.InstallPath == existing.InstallPath {
			stillReferenced = true
			break
		}
	}
	if !stillReferenced && existing.InstallPath != newDir {
		if err := os.RemoveAll(existing.InstallPath); err != nil {
			fmt.Fprintf(i.stderrOr(), "warning: removing old cache %s: %v\n", existing.InstallPath, err)
		}
	}
	return InstallResult{
		Plugin: plugin, Marketplace: market, Scope: req.Scope,
		Version: newVersion, InstallPath: newDir,
	}, nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestUpdate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: Update advances version, GCs old dir, walks pinned config"
```

### Task 8.2: `UpdateAll` continues past failures

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpdateAll_ContinuesPastFailures(t *testing.T) {
	i := newTestInstaller(t)
	r := i.Marketplaces.(*stubResolver)

	fixA1 := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	fixA2 := newFixture(t, map[string]string{"plugin.json": `{"version":"2.0.0"}`})
	fixB1 := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r.byKey["a@m"] = &stubSource{fixtureDir: fixA1, declared: "1.0.0"}
	r.byKey["b@m"] = &stubSource{fixtureDir: fixB1, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "a@m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "b@m"}); err != nil {
		t.Fatal(err)
	}

	// Advance a; make b's resolver fail.
	r.byKey["a@m"] = &stubSource{fixtureDir: fixA2, declared: "2.0.0"}
	r.missing["b@m"] = true

	results, err := i.UpdateAll(context.Background(), ScopeUser)
	if err == nil {
		t.Fatalf("expected aggregate error, got nil")
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	// Results are sorted alphabetically by key.
	if results[0].Plugin != "a" || results[0].Version != "2.0.0" {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[1].Plugin != "b" {
		t.Errorf("results[1] = %+v", results[1])
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestUpdateAll -v`
Expected: FAIL.

- [ ] **Step 3: Implement `UpdateAll`**

Replace stub:

```go
func (i *Installer) UpdateAll(ctx context.Context, scope Scope) ([]InstallResult, error) {
	if scope == "" {
		scope = ScopeUser
	}
	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("serf plugin update: %w", err)
	}
	keys := make([]string, 0, len(reg.Plugins))
	for k, entries := range reg.Plugins {
		for _, e := range entries {
			if e.Scope == scope {
				keys = append(keys, k)
				break
			}
		}
	}
	sort.Strings(keys)

	results := make([]InstallResult, 0, len(keys))
	var errs []error
	for _, key := range keys {
		at := strings.LastIndex(key, "@")
		plugin, market := key[:at], key[at+1:]
		res, uerr := i.Update(ctx, UpdateRequest{Plugin: plugin, Marketplace: market, Scope: scope})
		if uerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, uerr))
			results = append(results, InstallResult{Plugin: plugin, Marketplace: market, Scope: scope})
			continue
		}
		results = append(results, res)
	}
	if len(errs) > 0 {
		return results, joinErrors(errs)
	}
	return results, nil
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return fmt.Errorf("multiple errors: %s", strings.Join(parts, "; "))
}
```

Add `"sort"` to imports.

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestUpdateAll -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: UpdateAll continues past per-plugin failures"
```

---

## Phase 9 — Enable / Disable

### Task 9.1: `Enable` / `Disable`

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestEnable_PinAndPlainAndDisable(t *testing.T) {
	i := newTestInstaller(t)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", NoEnable: true}); err != nil {
		t.Fatal(err)
	}

	if err := i.Enable(context.Background(), EnableRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadRawConfig(i.GlobalConfig)
	if string(cfg.enabledPlugins["x@m"]) != "true" {
		t.Errorf("got %s, want true", string(cfg.enabledPlugins["x@m"]))
	}

	if err := i.Enable(context.Background(), EnableRequest{Plugin: "x@m", Pin: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadRawConfig(i.GlobalConfig)
	if string(cfg.enabledPlugins["x@m"]) != `{"version": "1.0.0"}` {
		t.Errorf("pinned object missing: %s", string(cfg.enabledPlugins["x@m"]))
	}

	if err := i.Disable(context.Background(), DisableRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadRawConfig(i.GlobalConfig)
	if _, ok := cfg.enabledPlugins["x@m"]; ok {
		t.Errorf("key should be deleted")
	}
	// disable idempotent
	if err := i.Disable(context.Background(), DisableRequest{Plugin: "x@m"}); err != nil {
		t.Errorf("disable should be idempotent, got %v", err)
	}
}

func TestEnable_NotInstalledIsError(t *testing.T) {
	i := newTestInstaller(t)
	err := i.Enable(context.Background(), EnableRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), "not installed at scope") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnable_PreservesUnknownConfigKeys(t *testing.T) {
	i := newTestInstaller(t)
	// pre-populate config.json with hooks / mcpServers
	pre := `{
		"hooks": {"PreToolUse":[{"matcher":"Bash"}]},
		"mcpServers": {"a":{"command":"b"}}
	}`
	if err := os.MkdirAll(filepath.Dir(i.GlobalConfig), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.GlobalConfig, []byte(pre), 0644); err != nil {
		t.Fatal(err)
	}
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "x@m", NoEnable: true}); err != nil {
		t.Fatal(err)
	}
	if err := i.Enable(context.Background(), EnableRequest{Plugin: "x@m"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadRawConfig(i.GlobalConfig)
	if _, ok := cfg.other["hooks"]; !ok {
		t.Errorf("hooks dropped")
	}
	if _, ok := cfg.other["mcpServers"]; !ok {
		t.Errorf("mcpServers dropped")
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestEnable -v`
Expected: FAIL.

- [ ] **Step 3: Implement `Enable` / `Disable`**

Replace stubs:

```go
func (i *Installer) Enable(ctx context.Context, req EnableRequest) error {
	if req.Scope == "" {
		req.Scope = ScopeUser
	}
	if req.Scope != ScopeUser && req.Scope != ScopeProject {
		return fmt.Errorf(`serf plugin enable: scope %q is not yet supported in serf`, string(req.Scope))
	}
	plugin, market, err := parsePluginSpec(req.Plugin, req.Marketplace)
	if err != nil {
		return fmt.Errorf("serf plugin enable: %w", err)
	}
	configPath, err := i.pathForScope(req.Scope)
	if err != nil {
		return fmt.Errorf("serf plugin enable: %w", err)
	}
	release, err := acquireRegistryLock(i.RegistryPath+".lock", i.lockTimeoutOr())
	if err != nil {
		return fmt.Errorf("serf plugin enable: %w", err)
	}
	defer release()

	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		return fmt.Errorf("serf plugin enable: %w", err)
	}
	key := plugin + "@" + market
	e, ok := findEntry(reg, key, req.Scope)
	if !ok {
		return fmt.Errorf(`serf plugin enable: plugin "%s" is not installed at scope %q`, key, string(req.Scope))
	}
	cfg, err := loadRawConfig(configPath)
	if err != nil {
		return fmt.Errorf("serf plugin enable: %w", err)
	}
	applyEnable(&cfg, key, e.Version, req.Pin)
	if err := writeRawConfig(configPath, cfg); err != nil {
		return fmt.Errorf("serf plugin enable: writing %s: %w", configPath, err)
	}
	return nil
}

func (i *Installer) Disable(ctx context.Context, req DisableRequest) error {
	if req.Scope == "" {
		req.Scope = ScopeUser
	}
	if req.Scope != ScopeUser && req.Scope != ScopeProject {
		return fmt.Errorf(`serf plugin disable: scope %q is not yet supported in serf`, string(req.Scope))
	}
	plugin, market, err := parsePluginSpec(req.Plugin, req.Marketplace)
	if err != nil {
		return fmt.Errorf("serf plugin disable: %w", err)
	}
	configPath, err := i.pathForScope(req.Scope)
	if err != nil {
		return fmt.Errorf("serf plugin disable: %w", err)
	}
	release, err := acquireRegistryLock(i.RegistryPath+".lock", i.lockTimeoutOr())
	if err != nil {
		return fmt.Errorf("serf plugin disable: %w", err)
	}
	defer release()

	cfg, err := loadRawConfig(configPath)
	if err != nil {
		return fmt.Errorf("serf plugin disable: %w", err)
	}
	key := plugin + "@" + market
	applyDisable(&cfg, key)
	if err := writeRawConfig(configPath, cfg); err != nil {
		return fmt.Errorf("serf plugin disable: writing %s: %w", configPath, err)
	}
	return nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestEnable -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: Enable and Disable mutate scoped config.json"
```

---

## Phase 10 — List

### Task 10.1: `List` returns registry × enabledPlugins join

**Files:**
- Modify: `internal/plugins/install.go`, `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestList_EmptyRegistry(t *testing.T) {
	i := newTestInstaller(t)
	out, err := i.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("got %d entries, want 0", len(out))
	}
}

func TestList_TwoPluginsTwoScopes(t *testing.T) {
	i := newTestInstaller(t)
	if err := os.MkdirAll(i.ProjectRoot, 0755); err != nil {
		t.Fatal(err)
	}
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r := i.Marketplaces.(*stubResolver)
	r.byKey["a@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	r.byKey["b@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "a@m", Scope: ScopeUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Install(context.Background(), InstallRequest{Plugin: "b@m", Scope: ScopeProject}); err != nil {
		t.Fatal(err)
	}

	all, err := i.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d, want 2", len(all))
	}
	// Should be sorted by key for deterministic output.
	if all[0].Plugin != "a" || all[1].Plugin != "b" {
		t.Errorf("not sorted: %+v", all)
	}
	if !all[0].Enabled {
		t.Errorf("a@m at user scope should be enabled")
	}
	if !all[1].Enabled {
		t.Errorf("b@m at project scope should be enabled")
	}

	onlyUser, err := i.List(context.Background(), ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyUser) != 1 || onlyUser[0].Plugin != "a" {
		t.Errorf("onlyUser = %+v", onlyUser)
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./internal/plugins/ -run TestList -v`
Expected: FAIL.

- [ ] **Step 3: Implement `List`**

Replace stub:

```go
func (i *Installer) List(ctx context.Context, scope Scope) ([]ListEntry, error) {
	reg, err := LoadRegistry(i.RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("serf plugin list: %w", err)
	}
	// Pre-load per-scope enabledPlugins maps.
	enabled := map[Scope]map[string]bool{}
	loadEnabled := func(s Scope) {
		path, perr := i.pathForScope(s)
		if perr != nil {
			enabled[s] = map[string]bool{}
			return
		}
		cfg, cerr := loadRawConfig(path)
		if cerr != nil {
			enabled[s] = map[string]bool{}
			return
		}
		m := map[string]bool{}
		for k := range cfg.enabledPlugins {
			m[k] = true
		}
		enabled[s] = m
	}
	loadEnabled(ScopeUser)
	if i.ProjectRoot != "" {
		loadEnabled(ScopeProject)
	}

	keys := make([]string, 0, len(reg.Plugins))
	for k := range reg.Plugins {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []ListEntry
	for _, key := range keys {
		at := strings.LastIndex(key, "@")
		plugin, market := key[:at], key[at+1:]
		for _, e := range reg.Plugins[key] {
			if scope != "" && e.Scope != scope {
				continue
			}
			isEnabled := false
			if m, ok := enabled[e.Scope]; ok {
				isEnabled = m[key]
			}
			out = append(out, ListEntry{
				Plugin: plugin, Marketplace: market, Scope: e.Scope,
				Version: e.Version, InstallPath: e.InstallPath,
				InstalledAt: e.InstalledAt, LastUpdated: e.LastUpdated,
				GitCommitSha: e.GitCommitSha, Enabled: isEnabled,
			})
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./internal/plugins/ -run TestList -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: List joins registry with enabledPlugins per scope"
```

---

## Phase 11 — Concurrency

### Task 11.1: Install lock serializes concurrent goroutines

**Files:**
- Modify: `internal/plugins/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstaller_LockSerializes(t *testing.T) {
	i := newTestInstaller(t)
	r := i.Marketplaces.(*stubResolver)
	for _, name := range []string{"a", "b"} {
		fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
		r.byKey[name+"@m"] = &slowStubSource{stubSource: stubSource{fixtureDir: fix, declared: "1.0.0"}, delay: 100 * time.Millisecond}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := i.Install(context.Background(), InstallRequest{Plugin: "a@m"})
		errs <- err
	}()
	go func() {
		<-start
		_, err := i.Install(context.Background(), InstallRequest{Plugin: "b@m"})
		errs <- err
	}()
	t0 := time.Now()
	close(start)
	for k := 0; k < 2; k++ {
		if err := <-errs; err != nil {
			t.Errorf("install: %v", err)
		}
	}
	elapsed := time.Since(t0)
	if elapsed < 200*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 200ms (lock should have serialized)", elapsed)
	}
}

func TestInstaller_LockTimeout(t *testing.T) {
	i := newTestInstaller(t)
	i.LockTimeout = 100 * time.Millisecond
	// Hold the lock from a separate goroutine for longer than the timeout.
	release, err := acquireRegistryLock(i.RegistryPath+".lock", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r := i.Marketplaces.(*stubResolver)
	fix := newFixture(t, map[string]string{"plugin.json": `{"version":"1.0.0"}`})
	r.byKey["x@m"] = &stubSource{fixtureDir: fix, declared: "1.0.0"}

	_, err = i.Install(context.Background(), InstallRequest{Plugin: "x@m"})
	if err == nil || !strings.Contains(err.Error(), "another serf plugin operation is in progress") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Add `slowStubSource` adapter to `stub_resolver_test.go`**

Append to that file:

```go
type slowStubSource struct {
	stubSource
	delay time.Duration
}

func (s *slowStubSource) Fetch(ctx context.Context, destDir string) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.stubSource.Fetch(ctx, destDir)
}
```

Add `"time"` to imports.

- [ ] **Step 3: Run — should PASS**

Run: `go test ./internal/plugins/ -run TestInstaller_Lock -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/install_test.go internal/plugins/stub_resolver_test.go
git commit -m "plugins: cover lock serialization and lock timeout"
```


---

## Phase 12 — CLI surface

The CLI in `cmd/serf/plugin/` wraps `Installer` with a Cobra command tree. Existing serf binaries use Cobra; verify by reading `cmd/serf/main.go` before adding the import.

### Task 12.1: Define `pluginInstaller` interface for testable CLI

**Files:**
- Create: `cmd/serf/plugin/install.go`

- [ ] **Step 1: Write the scaffolding**

```go
// Package plugin implements the `serf plugin` subcommand tree for installing,
// uninstalling, updating, listing, enabling, and disabling Claude Code
// compatible plugins.
package plugin

import (
	"context"

	"primeradiant.com/serf/internal/plugins"
)

// pluginInstaller is the subset of *plugins.Installer the CLI consumes. It
// exists so install_test.go can drop in a fake without spinning up the real
// filesystem-backed installer.
type pluginInstaller interface {
	Install(ctx context.Context, req plugins.InstallRequest) (plugins.InstallResult, error)
	Uninstall(ctx context.Context, req plugins.UninstallRequest) error
	Update(ctx context.Context, req plugins.UpdateRequest) (plugins.InstallResult, error)
	UpdateAll(ctx context.Context, scope plugins.Scope) ([]plugins.InstallResult, error)
	Enable(ctx context.Context, req plugins.EnableRequest) error
	Disable(ctx context.Context, req plugins.DisableRequest) error
	List(ctx context.Context, scope plugins.Scope) ([]plugins.ListEntry, error)
}
```

- [ ] **Step 2: Build**

Run: `go build ./cmd/serf/plugin/...`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf/plugin/install.go
git commit -m "plugin cli: scaffold pluginInstaller interface"
```

### Task 12.2: `NewCommand` factory + install subcommand wiring

**Files:**
- Modify: `cmd/serf/plugin/install.go`
- Create: `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugin

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/plugins"
)

type fakeInstaller struct {
	installCalls []plugins.InstallRequest
	installRes   plugins.InstallResult
	installErr   error
}

func (f *fakeInstaller) Install(_ context.Context, req plugins.InstallRequest) (plugins.InstallResult, error) {
	f.installCalls = append(f.installCalls, req)
	return f.installRes, f.installErr
}
func (f *fakeInstaller) Uninstall(_ context.Context, _ plugins.UninstallRequest) error { return nil }
func (f *fakeInstaller) Update(_ context.Context, _ plugins.UpdateRequest) (plugins.InstallResult, error) {
	return plugins.InstallResult{}, nil
}
func (f *fakeInstaller) UpdateAll(_ context.Context, _ plugins.Scope) ([]plugins.InstallResult, error) {
	return nil, nil
}
func (f *fakeInstaller) Enable(_ context.Context, _ plugins.EnableRequest) error   { return nil }
func (f *fakeInstaller) Disable(_ context.Context, _ plugins.DisableRequest) error { return nil }
func (f *fakeInstaller) List(_ context.Context, _ plugins.Scope) ([]plugins.ListEntry, error) {
	return nil, nil
}

func TestInstallCommand_HappyPath(t *testing.T) {
	f := &fakeInstaller{installRes: plugins.InstallResult{
		Plugin: "x", Marketplace: "m", Scope: plugins.ScopeUser, Version: "1.0.0",
		InstallPath: "/c/m/x/1.0.0", Enabled: true,
	}}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"install", "x@m"}, f, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if len(f.installCalls) != 1 || f.installCalls[0].Plugin != "x@m" {
		t.Errorf("installCalls = %+v", f.installCalls)
	}
	if !strings.Contains(stdout.String(), "Installed") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestInstallCommand_MissingMarketplace(t *testing.T) {
	f := &fakeInstaller{installErr: errors.New(`serf plugin install: plugin "x" requires a marketplace; pass plugin@marketplace or --marketplace <name>`)}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"install", "x"}, f, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "requires a marketplace") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./cmd/serf/plugin/ -run TestInstallCommand -v`
Expected: FAIL — undefined `Run`.

- [ ] **Step 3: Implement `Run` and the install subcommand**

Replace `cmd/serf/plugin/install.go` content with:

```go
// Package plugin implements the `serf plugin` subcommand tree for installing,
// uninstalling, updating, listing, enabling, and disabling Claude Code
// compatible plugins.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"primeradiant.com/serf/internal/plugins"
)

type pluginInstaller interface {
	Install(ctx context.Context, req plugins.InstallRequest) (plugins.InstallResult, error)
	Uninstall(ctx context.Context, req plugins.UninstallRequest) error
	Update(ctx context.Context, req plugins.UpdateRequest) (plugins.InstallResult, error)
	UpdateAll(ctx context.Context, scope plugins.Scope) ([]plugins.InstallResult, error)
	Enable(ctx context.Context, req plugins.EnableRequest) error
	Disable(ctx context.Context, req plugins.DisableRequest) error
	List(ctx context.Context, scope plugins.Scope) ([]plugins.ListEntry, error)
}

// Run dispatches one CLI invocation against inst. stdout receives normal
// output (human or --json); stderr receives errors and warnings. Returns
// the exit code per the design's §8.3 table.
func Run(args []string, inst pluginInstaller, stdout, stderr io.Writer) int {
	root := NewCommand(inst)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return classifyExitCode(err)
	}
	return 0
}

// NewCommand builds the `serf plugin` cobra root. SP8 wires this into the
// root serf command.
func NewCommand(inst pluginInstaller) *cobra.Command {
	root := &cobra.Command{
		Use:   "plugin",
		Short: "Manage Claude Code-compatible plugins",
	}
	root.AddCommand(installCmd(inst))
	return root
}

func installCmd(inst pluginInstaller) *cobra.Command {
	var (
		scope       string
		marketplace string
		version     string
		pin         bool
		noEnable    bool
		force       bool
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "install <plugin>[@<marketplace>]",
		Short: "Install a plugin from a known marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseScope(scope)
			if err != nil {
				return usageError{err: err}
			}
			res, err := inst.Install(cmd.Context(), plugins.InstallRequest{
				Plugin:      args[0],
				Marketplace: marketplace,
				Scope:       s,
				Version:     version,
				Pin:         pin,
				NoEnable:    noEnable,
				Force:       force,
			})
			if err != nil {
				if jsonOut {
					_ = renderJSONError(cmd.OutOrStdout(), err)
				}
				return err
			}
			if jsonOut {
				return renderInstallJSON(cmd.OutOrStdout(), []plugins.InstallResult{res})
			}
			return renderInstallHuman(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVarP(&scope, "scope", "s", "user", "user or project")
	cmd.Flags().StringVar(&marketplace, "marketplace", "", "marketplace name (when not encoded in plugin spec)")
	cmd.Flags().StringVar(&version, "version", "", "pin install to a specific version")
	cmd.Flags().BoolVar(&pin, "pin", false, `write {"version":"..."} to enabledPlugins instead of true`)
	cmd.Flags().BoolVar(&noEnable, "no-enable", false, "install and register only; do not flip enabledPlugins")
	cmd.Flags().BoolVar(&force, "force", false, "re-fetch even if target version is already cached")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// parseScope rejects unsupported scopes at the CLI layer so the user sees a
// usage-class exit code rather than I/O class.
func parseScope(s string) (plugins.Scope, error) {
	switch s {
	case "user":
		return plugins.ScopeUser, nil
	case "project":
		return plugins.ScopeProject, nil
	case "local", "managed":
		return "", fmt.Errorf(`scope %q is not yet supported in serf`, s)
	default:
		return "", fmt.Errorf(`unknown scope %q (want user or project)`, s)
	}
}

// usageError is sentinel-wrapped so classifyExitCode returns 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// classifyExitCode maps an error to one of §8.3's exit codes.
func classifyExitCode(err error) int {
	if err == nil {
		return 0
	}
	var uerr usageError
	if errors.As(err, &uerr) {
		return 2
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not yet supported"),
		strings.Contains(msg, "requires a marketplace"),
		strings.Contains(msg, "but --marketplace"),
		strings.Contains(msg, "requires a git repository"),
		strings.Contains(msg, "--prune is not supported"),
		strings.Contains(msg, "--available requires --json"),
		strings.Contains(msg, "mutually exclusive"):
		return 2
	case strings.Contains(msg, "not in any known marketplace"),
		strings.Contains(msg, "fetching "),
		strings.Contains(msg, "resolving "):
		return 4
	case strings.Contains(msg, "installed_plugins.json"),
		strings.Contains(msg, "another serf plugin operation"):
		return 3
	}
	return 1
}

// renderInstallHuman writes a human-readable summary.
func renderInstallHuman(w io.Writer, res plugins.InstallResult) error {
	verb := "Installed"
	if res.AlreadyAt {
		verb = "Already installed"
	}
	fmt.Fprintf(w, "%s %s@%s %s to %s scope.\n", verb, res.Plugin, res.Marketplace, res.Version, res.Scope)
	if res.Enabled {
		fmt.Fprintln(w, "Enabled.")
	}
	return nil
}

// renderInstallJSON emits {"ok": true, "results": [...]}.
func renderInstallJSON(w io.Writer, results []plugins.InstallResult) error {
	type entry struct {
		Plugin      string         `json:"plugin"`
		Marketplace string         `json:"marketplace"`
		Scope       plugins.Scope  `json:"scope"`
		Version     string         `json:"version"`
		InstallPath string         `json:"installPath"`
		Enabled     bool           `json:"enabled"`
		AlreadyAt   bool           `json:"alreadyAt"`
	}
	out := struct {
		OK      bool    `json:"ok"`
		Results []entry `json:"results"`
	}{OK: true, Results: make([]entry, 0, len(results))}
	for _, r := range results {
		out.Results = append(out.Results, entry{
			Plugin: r.Plugin, Marketplace: r.Marketplace, Scope: r.Scope,
			Version: r.Version, InstallPath: r.InstallPath,
			Enabled: r.Enabled, AlreadyAt: r.AlreadyAt,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func renderJSONError(w io.Writer, err error) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"ok": false, "error": err.Error()})
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./cmd/serf/plugin/ -run TestInstallCommand -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugin/install.go cmd/serf/plugin/install_test.go
git commit -m "plugin cli: install subcommand with human and --json output"
```

### Task 12.3: CLI rejects `--scope managed` with exit 2

**Files:**
- Modify: `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstallCommand_RejectsManagedScope(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"install", "x@m", "--scope", "managed"}, f, &stdout, &stderr)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "not yet supported") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if len(f.installCalls) != 0 {
		t.Errorf("install should not have been called")
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./cmd/serf/plugin/ -run TestInstallCommand_RejectsManagedScope -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf/plugin/install_test.go
git commit -m "plugin cli: cover --scope managed rejection"
```

### Task 12.4: CLI `--json` install output is valid JSON with `ok:true`

**Files:**
- Modify: `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstallCommand_JSON(t *testing.T) {
	f := &fakeInstaller{installRes: plugins.InstallResult{
		Plugin: "x", Marketplace: "m", Scope: plugins.ScopeUser, Version: "1.0.0",
		InstallPath: "/c/m/x/1.0.0", Enabled: true,
	}}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"install", "x@m", "--json"}, f, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr=%s", code, stderr.String())
	}
	var got struct {
		OK      bool             `json:"ok"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not valid JSON: %v\n%s", err, stdout.String())
	}
	if !got.OK || len(got.Results) != 1 {
		t.Errorf("got = %+v", got)
	}
}
```

Add `"encoding/json"` to test imports.

- [ ] **Step 2: Run — should PASS**

Run: `go test ./cmd/serf/plugin/ -run TestInstallCommand_JSON -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf/plugin/install_test.go
git commit -m "plugin cli: cover install --json output"
```

### Task 12.5: CLI install resolver miss → exit 4

**Files:**
- Modify: `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestInstallCommand_ResolverMissExit4(t *testing.T) {
	f := &fakeInstaller{installErr: errors.New(`serf plugin install: plugin "x@m" is not in any known marketplace; run 'serf plugin marketplace add ...'`)}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"install", "x@m"}, f, &stdout, &stderr)
	if code != 4 {
		t.Errorf("code = %d, want 4; stderr=%s", code, stderr.String())
	}
}
```

- [ ] **Step 2: Run — should PASS**

Run: `go test ./cmd/serf/plugin/ -run TestInstallCommand_ResolverMissExit4 -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf/plugin/install_test.go
git commit -m "plugin cli: cover resolver-miss exit 4"
```

### Task 12.6: Uninstall subcommand + `--keep-data` warning + `--prune` rejection

**Files:**
- Modify: `cmd/serf/plugin/install.go`, `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestUninstallCommand_HappyPath(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"uninstall", "x@m"}, f, &stdout, &stderr)
	if code != 0 {
		t.Errorf("code = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Uninstalled") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestUninstallCommand_KeepDataWarns(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"uninstall", "x@m", "--keep-data"}, f, &stdout, &stderr)
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), "--keep-data is reserved") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestUninstallCommand_PruneRejected(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"uninstall", "x@m", "--prune"}, f, &stdout, &stderr)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--prune is not supported") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./cmd/serf/plugin/ -run TestUninstallCommand -v`
Expected: FAIL.

- [ ] **Step 3: Implement `uninstallCmd`**

Insert in `install.go` after `installCmd` and register in `NewCommand`:

```go
func uninstallCmd(inst pluginInstaller) *cobra.Command {
	var (
		scope    string
		market   string
		keepData bool
		prune    bool
	)
	cmd := &cobra.Command{
		Use:     "uninstall <plugin>[@<marketplace>]",
		Aliases: []string{"remove", "rm"},
		Short:   "Uninstall a plugin",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prune {
				return usageError{err: fmt.Errorf("--prune is not supported; serf v1 does not auto-install plugin dependencies")}
			}
			if keepData {
				fmt.Fprintln(cmd.ErrOrStderr(), "--keep-data is reserved; serf does not maintain plugin data directories yet")
			}
			s, err := parseScope(scope)
			if err != nil {
				return usageError{err: err}
			}
			if err := inst.Uninstall(cmd.Context(), plugins.UninstallRequest{
				Plugin: args[0], Marketplace: market, Scope: s, KeepData: keepData,
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Uninstalled %s from %s scope.\n", args[0], s)
			return nil
		},
	}
	cmd.Flags().StringVarP(&scope, "scope", "s", "user", "user or project")
	cmd.Flags().StringVar(&market, "marketplace", "", "marketplace name")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "reserved; no-op in v1")
	cmd.Flags().BoolVar(&prune, "prune", false, "rejected in v1")
	return cmd
}
```

Update `NewCommand`:

```go
func NewCommand(inst pluginInstaller) *cobra.Command {
	root := &cobra.Command{
		Use:   "plugin",
		Short: "Manage Claude Code-compatible plugins",
	}
	root.AddCommand(installCmd(inst))
	root.AddCommand(uninstallCmd(inst))
	return root
}
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./cmd/serf/plugin/ -run TestUninstallCommand -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugin/install.go cmd/serf/plugin/install_test.go
git commit -m "plugin cli: uninstall subcommand with --keep-data warn and --prune reject"
```

### Task 12.7: Update + update --all subcommand

**Files:**
- Modify: `cmd/serf/plugin/install.go`, `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestUpdateAllCommand_JSONMixedResults(t *testing.T) {
	results := []plugins.InstallResult{
		{Plugin: "a", Marketplace: "m", Scope: plugins.ScopeUser, Version: "2.0.0", InstallPath: "/c/m/a/2"},
		{Plugin: "b", Marketplace: "m", Scope: plugins.ScopeUser},
	}
	f := &fakeUpdateAll{results: results, err: errors.New("multiple errors: b@m: nope")}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"update", "--all", "--json"}, f, &stdout, &stderr)
	if code != 1 {
		t.Errorf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	if got["ok"] != false {
		t.Errorf("ok = %v, want false", got["ok"])
	}
}

func TestUpdateCommand_MutuallyExclusive(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"update", "x@m", "--all"}, f, &stdout, &stderr)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// fakeUpdateAll is fakeInstaller with custom UpdateAll behavior.
type fakeUpdateAll struct {
	fakeInstaller
	results []plugins.InstallResult
	err     error
}

func (f *fakeUpdateAll) UpdateAll(_ context.Context, _ plugins.Scope) ([]plugins.InstallResult, error) {
	return f.results, f.err
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./cmd/serf/plugin/ -run TestUpdate -v`
Expected: FAIL.

- [ ] **Step 3: Implement `updateCmd`**

Insert and register:

```go
func updateCmd(inst pluginInstaller) *cobra.Command {
	var (
		scope   string
		market  string
		all     bool
		force   bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "update [<plugin>[@<marketplace>]]",
		Short: "Update one or all installed plugins",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseScope(scope)
			if err != nil {
				return usageError{err: err}
			}
			if all && len(args) > 0 {
				return usageError{err: fmt.Errorf("--all and <plugin> are mutually exclusive")}
			}
			if all {
				results, uerr := inst.UpdateAll(cmd.Context(), s)
				if jsonOut {
					return renderUpdateAllJSON(cmd.OutOrStdout(), results, uerr)
				}
				renderUpdateAllHuman(cmd.OutOrStdout(), cmd.ErrOrStderr(), results, uerr)
				return uerr
			}
			if len(args) == 0 {
				return usageError{err: fmt.Errorf("update requires <plugin> or --all")}
			}
			res, uerr := inst.Update(cmd.Context(), plugins.UpdateRequest{
				Plugin: args[0], Marketplace: market, Scope: s, Force: force,
			})
			if uerr != nil {
				if jsonOut {
					_ = renderJSONError(cmd.OutOrStdout(), uerr)
				}
				return uerr
			}
			if jsonOut {
				return renderInstallJSON(cmd.OutOrStdout(), []plugins.InstallResult{res})
			}
			return renderInstallHuman(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVarP(&scope, "scope", "s", "user", "user or project")
	cmd.Flags().StringVar(&market, "marketplace", "", "marketplace name")
	cmd.Flags().BoolVar(&all, "all", false, "update every installed plugin at the scope")
	cmd.Flags().BoolVar(&force, "force", false, "re-fetch even if unchanged")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func renderUpdateAllJSON(w io.Writer, results []plugins.InstallResult, err error) error {
	type entry struct {
		Plugin      string        `json:"plugin"`
		Marketplace string        `json:"marketplace"`
		Scope       plugins.Scope `json:"scope"`
		Version     string        `json:"version,omitempty"`
		Error       string        `json:"error,omitempty"`
	}
	out := struct {
		OK      bool    `json:"ok"`
		Results []entry `json:"results"`
	}{OK: err == nil}
	for _, r := range results {
		out.Results = append(out.Results, entry{
			Plugin: r.Plugin, Marketplace: r.Marketplace, Scope: r.Scope, Version: r.Version,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func renderUpdateAllHuman(stdout, stderr io.Writer, results []plugins.InstallResult, err error) {
	for _, r := range results {
		if r.Version == "" {
			fmt.Fprintf(stderr, "✗ %s@%s\n", r.Plugin, r.Marketplace)
			continue
		}
		fmt.Fprintf(stdout, "✓ %s@%s: %s\n", r.Plugin, r.Marketplace, r.Version)
	}
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
	}
}
```

Update `NewCommand`:

```go
	root.AddCommand(updateCmd(inst))
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./cmd/serf/plugin/ -run TestUpdate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugin/install.go cmd/serf/plugin/install_test.go
git commit -m "plugin cli: update and update --all with JSON output"
```

### Task 12.8: Enable / disable subcommands

**Files:**
- Modify: `cmd/serf/plugin/install.go`, `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestEnableDisableCommands(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"enable", "x@m"}, f, &stdout, &stderr); code != 0 {
		t.Errorf("enable: code = %d; stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"disable", "x@m"}, f, &stdout, &stderr); code != 0 {
		t.Errorf("disable: code = %d; stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"enable", "x@m", "--pin"}, f, &stdout, &stderr); code != 0 {
		t.Errorf("enable --pin: code = %d", code)
	}
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./cmd/serf/plugin/ -run TestEnableDisableCommands -v`
Expected: FAIL.

- [ ] **Step 3: Implement and register**

Add to `install.go`:

```go
func enableCmd(inst pluginInstaller) *cobra.Command {
	var scope, market string
	var pin bool
	cmd := &cobra.Command{
		Use:   "enable <plugin>[@<marketplace>]",
		Short: "Enable an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseScope(scope)
			if err != nil {
				return usageError{err: err}
			}
			if err := inst.Enable(cmd.Context(), plugins.EnableRequest{
				Plugin: args[0], Marketplace: market, Scope: s, Pin: pin,
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Enabled %s at %s scope.\n", args[0], s)
			return nil
		},
	}
	cmd.Flags().StringVarP(&scope, "scope", "s", "user", "user or project")
	cmd.Flags().StringVar(&market, "marketplace", "", "marketplace name")
	cmd.Flags().BoolVar(&pin, "pin", false, `write {"version":"..."} to enabledPlugins instead of true`)
	return cmd
}

func disableCmd(inst pluginInstaller) *cobra.Command {
	var scope, market string
	cmd := &cobra.Command{
		Use:   "disable <plugin>[@<marketplace>]",
		Short: "Disable an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseScope(scope)
			if err != nil {
				return usageError{err: err}
			}
			if err := inst.Disable(cmd.Context(), plugins.DisableRequest{
				Plugin: args[0], Marketplace: market, Scope: s,
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Disabled %s at %s scope.\n", args[0], s)
			return nil
		},
	}
	cmd.Flags().StringVarP(&scope, "scope", "s", "user", "user or project")
	cmd.Flags().StringVar(&market, "marketplace", "", "marketplace name")
	return cmd
}
```

Register:

```go
	root.AddCommand(enableCmd(inst))
	root.AddCommand(disableCmd(inst))
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./cmd/serf/plugin/ -run TestEnableDisableCommands -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugin/install.go cmd/serf/plugin/install_test.go
git commit -m "plugin cli: enable and disable subcommands"
```

### Task 12.9: List subcommand + `--available` requires `--json`

**Files:**
- Modify: `cmd/serf/plugin/install.go`, `cmd/serf/plugin/install_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestListCommand_JSON(t *testing.T) {
	f := &fakeInstaller{}
	f2 := &fakeList{fakeInstaller: *f, entries: []plugins.ListEntry{
		{Plugin: "a", Marketplace: "m", Scope: plugins.ScopeUser, Version: "1.0.0", Enabled: true},
	}}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"list", "--json"}, f2, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr=%s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	arr, _ := got["plugins"].([]any)
	if len(arr) != 1 {
		t.Errorf("got %d entries, want 1", len(arr))
	}
}

func TestListCommand_AvailableRequiresJSON(t *testing.T) {
	f := &fakeInstaller{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"list", "--available"}, f, &stdout, &stderr)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--available requires --json") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

type fakeList struct {
	fakeInstaller
	entries []plugins.ListEntry
}

func (f *fakeList) List(_ context.Context, _ plugins.Scope) ([]plugins.ListEntry, error) {
	return f.entries, nil
}
```

- [ ] **Step 2: Run — FAIL**

Run: `go test ./cmd/serf/plugin/ -run TestListCommand -v`
Expected: FAIL.

- [ ] **Step 3: Implement and register `listCmd`**

```go
func listCmd(inst pluginInstaller) *cobra.Command {
	var scope string
	var jsonOut, available bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			if available && !jsonOut {
				return usageError{err: fmt.Errorf("--available requires --json")}
			}
			var s plugins.Scope
			if scope != "" {
				parsed, err := parseScope(scope)
				if err != nil {
					return usageError{err: err}
				}
				s = parsed
			}
			entries, err := inst.List(cmd.Context(), s)
			if err != nil {
				return err
			}
			if jsonOut {
				return renderListJSON(cmd.OutOrStdout(), entries)
			}
			return renderListHuman(cmd.OutOrStdout(), entries)
		},
	}
	cmd.Flags().StringVarP(&scope, "scope", "s", "", "filter to user or project")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&available, "available", false, "include available-but-not-installed plugins (requires --json)")
	return cmd
}

func renderListJSON(w io.Writer, entries []plugins.ListEntry) error {
	out := struct {
		Plugins []plugins.ListEntry `json:"plugins"`
	}{Plugins: entries}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func renderListHuman(w io.Writer, entries []plugins.ListEntry) error {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No plugins installed.")
		return nil
	}
	for _, e := range entries {
		enabled := "disabled"
		if e.Enabled {
			enabled = "enabled"
		}
		fmt.Fprintf(w, "%s@%s\t%s\t%s\t%s\n", e.Plugin, e.Marketplace, e.Version, e.Scope, enabled)
	}
	return nil
}
```

Register:

```go
	root.AddCommand(listCmd(inst))
```

- [ ] **Step 4: Run — PASS**

Run: `go test ./cmd/serf/plugin/ -run TestListCommand -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/plugin/install.go cmd/serf/plugin/install_test.go
git commit -m "plugin cli: list subcommand with --json and --available gating"
```

### Task 12.10: Full-suite green

**Files:** none

- [ ] **Step 1: Run the full SP4 test suite**

Run: `go test ./internal/plugins/... ./cmd/serf/plugin/...`
Expected: ok, all green.

- [ ] **Step 2: Commit (no-op if green; otherwise fix and commit each fix as its own change)**

If green, no commit needed. If a test reveals a real bug, write a failing test reproducing it (a new task added inline here) before fixing.

---

## Coverage matrix (cross-check against spec §13.6)

- Every exported `Installer` method has at least one happy-path and one error-path test:
  - `Install`: 6.8 (happy), 6.13 / 6.14 / 6.15 / 6.18 (errors).
  - `Uninstall`: 7.1 (happy + shared scope + not-installed).
  - `Update`: 8.1 (happy + no-op + pinned + bare-true + other-scope).
  - `UpdateAll`: 8.2.
  - `Enable` / `Disable`: 9.1.
  - `List`: 10.1.
- Every error in §11 has a triggering test row:
  - Bad spec: 6.2.
  - Unsupported scope: 6.2 / 6.5 / 12.3.
  - Missing project: 6.10.
  - Marketplace miss: 6.18 / 12.5.
  - Source fetch failure: 6.13.
  - Validation failure: 6.14 / 6.15.
  - Version mismatch: 6.12.
  - Not installed: 7.1 / 8.1 / 9.1.
  - Registry write failure: covered by atomic-write tests (Task 1.5 round-trip).
  - Lock timeout: 11.1.
  - Multi-error: 8.2 / 12.7.
- Every rule in §9 has a row in `TestComputeVersion` (Task 2.1, 8 rows).
- Every CLI flag in §8.2 has a test row in Phase 12:
  - `--scope`: 12.2 / 12.3.
  - `--marketplace`: surfaced through fakeInstaller request inspection.
  - `--version`: 6.12 (Installer level).
  - `--pin`: 12.8.
  - `--no-enable`: 6.11.
  - `--force`: 6.9 / 8.1.
  - `--all`: 12.7.
  - `--keep-data`: 12.6.
  - `--prune`: 12.6.
  - `--json`: 12.4 / 12.7 / 12.9.
  - `--available`: 12.9.

---

## Notes & deferred items

- **SP3 type names.** If SP3's resolver / source interfaces land with names other than `MarketplaceResolver` / `PluginSource`, rename in `internal/plugins/resolver.go` before wiring SP3's production implementation. SP4 references are confined to that file plus the `Installer.Marketplaces` field.
- **`--keep-data`.** Reserved; the CLI prints a warning and the installer ignores it. SP7 owns plugin data directories.
- **`local` / `managed` scopes.** Rejected at the CLI and at the Installer with a clear error; round-tripped on registry reads via the existing `Scope` const.
- **Bare-name resolution (no `@mkt`).** Out of scope; `parsePluginSpec` rejects bare names with the user-facing wording from §11.
- **Cross-machine concurrency.** Out of scope per §10.4.
- **End-to-end test for `marketplace add → install → session triggers plugin hook → uninstall`.** Owned by SP8; SP4 ships the install half only.

