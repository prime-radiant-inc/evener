# Plugin Marketplaces P1 — Backend Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `internal/plugins` manager package — the single source of truth for serf's plugin marketplaces and installed plugins on disk — with marketplace add/remove/list/refresh/browse and plugin install/upgrade/remove/enable/disable/list/update-all, all driven by real `git` against local fixtures.

**Architecture:** One new root-module Go package, `primeradiant.com/serf/internal/plugins`. On-disk state (Claude-Code-shaped, serf-owned) lives under `~/.config/serf/plugins/`: `known_marketplaces.json`, `installed_plugins.json`, `marketplaces/<name>/` clones, and `cache/<marketplace>/<plugin>/<sha>/` materialized plugins. A `Manager` value exposes every operation; registry mutations are serialized by a `flock` and written atomically (temp + rename). The existing `agent/plugin.Load` is reused unchanged as a dry-run validator. This plan implements the parent spec `docs/superpowers/specs/2026-07-04-plugin-marketplaces-design.md` phase **P1** (§15); P2–P7 (CLI, gating, auto-upgrade, web, TUI, slash commands, doctoring) are separate plans.

**Tech Stack:** Go 1.25 (`go.work` multi-module: this package is in the root module `primeradiant.com/serf`). Stdlib `os/exec` shelling out to the system `git`. `golang.org/x/sys/unix` (already an indirect dep — promoted to direct) for `flock`. `encoding/json`. Reuses `primeradiant.com/serf/agent/plugin` (`Load`, `Manifest`, `ParseManifest`) and `primeradiant.com/serf/envvars` (`XDGConfigHome`). Tests use `t.TempDir()` and real local `git` repositories as fixtures — **no mocks, no network**; git-dependent tests `t.Skip` when `git` is absent.

## Global Constraints

- Module: package path is `primeradiant.com/serf/internal/plugins` (root module). It MAY import `primeradiant.com/serf/agent/plugin`, `primeradiant.com/serf/frontmatter`, and `primeradiant.com/serf/envvars`. It MUST NOT import anything under `primeradiant.com/serf/agent/internal/...` (Go internal rule forbids it — verified).
- Source-type discriminator is **`url`, never `git`** (the on-disk JSON `"source"` value). `git` is accepted only as a **read-only legacy alias** for `url` on a marketplace container; serf never writes `git`.
- The install-registry (`installed_plugins.json`) JSON shape is `{"version":2,"plugins":{"<plugin>@<marketplace>":[<entry>...]}}` — the value is an array (Claude drop-in shape); v1 writes exactly one entry per plugin.
- Cache directories are keyed by resolved commit **`<sha>`**, not by version (`cache/<marketplace>/<plugin>/<sha>/`). Plugins whose **marketplace** is a `directory` source are **referenced in place** (no cache copy, empty sha).
- All registry/marketplace writes go through `atomicWriteFile` (temp + rename) under a `flock` on `<root>/.lock`.
- v1 is **user-scope only** — there is no `Scope` type, no `--scope`, no project registry.
- TDD: every task writes a failing test first, then the minimal implementation. Commit after each task. Test output must be pristine.
- Run tests with `go test ./internal/plugins/ -run <Name> -v` from the repo root (the `go.work` root).

---

## File Structure

All files under `internal/plugins/` (each `X.go` has a sibling `X_test.go` unless noted):

| File | Responsibility |
|---|---|
| `doc.go` | package doc only (no test) |
| `paths.go` | `Manager` type + store-root/path resolution via `envvars.XDGConfigHome` |
| `atomic.go` | `atomicWriteFile(path, data, perm)` — temp + fsync + rename |
| `locks.go` | `acquireLock(lockPath, timeout)` — `unix.Flock` with backoff |
| `registry.go` | `Registry`, `InstallEntry`; `LoadRegistry`/`SaveRegistry` |
| `git.go` | `gitClone`/`gitSparseClone`/`gitPull`/`gitHeadSHA` — shell out to `git` |
| `source.go` | `SourceKind`, `Source` (+ string-form & legacy-`git` `UnmarshalJSON`); `resolveAndFetch` |
| `marketplaces.go` | `MarketplaceRef`, `Marketplaces`; `Manager` marketplace verbs (Add/Remove/List/Refresh) |
| `catalog.go` | `Catalog`, `CatalogPlugin`; `ParseCatalog`; `Manager.Browse` |
| `version.go` | `computeVersion` |
| `validate.go` | `validatePluginDir` (dry-run `agent/plugin.Load`) |
| `install.go` | `Manager` plugin verbs: Install/Upgrade/Remove/Enable/Disable/List/UpdateAll |

**Design decisions resolving sp3/sp4 conflicts** (both prior specs are `DEFERRED`; this plan supersedes them):
- Two registries, two names: `Marketplaces` (`known_marketplaces.json`) and `Registry` (`installed_plugins.json`). (sp3/sp4 both called theirs `Registry`.)
- `Source` is a single **struct** used for both marketplace containers and plugin entries (sp4's `PluginSource` interface is dropped — a one-package manager needs no resolver seam).
- Registry IO is free functions with explicit paths (`LoadRegistry(path)`, `SaveRegistry(path, r)`), sp4-style.
- `Scope`/`ProjectRoot`/`GlobalConfig` are all dropped (user-scope-only v1).

---

## Task 0: Scaffold the package

**Files:**
- Create: `internal/plugins/doc.go`

- [ ] **Step 1: Create the package doc**

```go
// Package plugins is serf's manager for Claude Code-compatible plugin
// marketplaces and installed plugins. It owns the on-disk state under
// ~/.config/serf/plugins/ (known_marketplaces.json, installed_plugins.json,
// cloned marketplaces, and the materialized plugin cache) and exposes a
// Manager with marketplace and plugin lifecycle operations. It shells out to
// git for fetching and reuses agent/plugin.Load to validate materialized
// plugins. See docs/superpowers/specs/2026-07-04-plugin-marketplaces-design.md.
package plugins
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./internal/plugins/...`
Expected: succeeds, no output.

- [ ] **Step 3: Commit**

```bash
git add internal/plugins/doc.go
git commit -m "plugins: scaffold internal/plugins package"
```

---

## Task 1: Store paths (`Manager` + path helpers)

**Files:**
- Create: `internal/plugins/paths.go`
- Test: `internal/plugins/paths_test.go`

**Interfaces:**
- Produces: `type Manager struct { Root string; Now func() time.Time; Stderr io.Writer }`; `func NewManager(root string) *Manager` (empty `root` → `DefaultRoot()`); `func DefaultRoot() string`; unexported path methods `registryPath()`, `marketplacesDir()`, `cacheDir()`, `lockPath()`, `marketplaceDir(name)`, `pluginCacheDir(marketplace, plugin, sha)`.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"path/filepath"
	"testing"
)

func TestDefaultRoot_UsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	got := DefaultRoot()
	want := filepath.Join("/tmp/xdgcfg", "serf", "plugins")
	if got != want {
		t.Fatalf("DefaultRoot() = %q, want %q", got, want)
	}
}

func TestManagerPaths(t *testing.T) {
	m := NewManager("/store")
	cases := map[string]string{
		m.registryPath():                       "/store/installed_plugins.json",
		m.marketplacesDir():                     "/store/marketplaces",
		m.cacheDir():                            "/store/cache",
		m.lockPath():                            "/store/.lock",
		m.marketplaceDir("acme"):                "/store/marketplaces/acme",
		m.pluginCacheDir("acme", "widget", "ab"): "/store/cache/acme/widget/ab",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run 'TestDefaultRoot_UsesXDGConfigHome|TestManagerPaths' -v`
Expected: FAIL — `undefined: DefaultRoot`, `undefined: NewManager`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"primeradiant.com/serf/envvars"
)

// Manager owns all on-disk plugin state under Root (~/.config/serf/plugins).
type Manager struct {
	Root   string           // store root
	Now    func() time.Time // injectable clock; defaults to time.Now
	Stderr io.Writer        // warnings sink; defaults to os.Stderr
}

// NewManager returns a Manager rooted at root, or DefaultRoot() when root == "".
func NewManager(root string) *Manager {
	if root == "" {
		root = DefaultRoot()
	}
	return &Manager{Root: root, Now: time.Now, Stderr: os.Stderr}
}

// DefaultRoot is ~/.config/serf/plugins, honoring XDG_CONFIG_HOME the same way
// the rest of serf does (envvars.XDGConfigHome).
func DefaultRoot() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins")
}

func (m *Manager) registryPath() string   { return filepath.Join(m.Root, "installed_plugins.json") }
func (m *Manager) marketplacesFile() string {
	return filepath.Join(m.Root, "known_marketplaces.json")
}
func (m *Manager) marketplacesDir() string { return filepath.Join(m.Root, "marketplaces") }
func (m *Manager) cacheDir() string        { return filepath.Join(m.Root, "cache") }
func (m *Manager) lockPath() string        { return filepath.Join(m.Root, ".lock") }

func (m *Manager) marketplaceDir(name string) string {
	return filepath.Join(m.marketplacesDir(), name)
}

func (m *Manager) pluginCacheDir(marketplace, plugin, sha string) string {
	return filepath.Join(m.cacheDir(), marketplace, plugin, sha)
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run 'TestDefaultRoot_UsesXDGConfigHome|TestManagerPaths' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/paths.go internal/plugins/paths_test.go
git commit -m "plugins: Manager + store path resolution"
```

---

## Task 2: Atomic file writes

**Files:**
- Create: `internal/plugins/atomic.go`
- Test: `internal/plugins/atomic_test.go`

**Interfaces:**
- Produces: `func atomicWriteFile(path string, data []byte, perm os.FileMode) error` — writes to a temp file in the same dir, fsyncs, renames over `path`, and fsyncs the parent dir; removes the temp on any error.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile_WritesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.json")

	if err := atomicWriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Fatalf("content = %q, want %q", b, "one")
	}
	if err := atomicWriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("content = %q, want %q", b, "two")
	}

	// No leftover temp files.
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1 (leftover temp?)", len(entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestAtomicWriteFile -v`
Expected: FAIL — `undefined: atomicWriteFile`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path atomically: it creates an O_EXCL temp
// file in the same directory, fsyncs it, renames it over path, then fsyncs the
// parent directory so the rename is durable. The temp file is removed on any
// error.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", parent, err)
	}
	suf := make([]byte, 6)
	if _, err := rand.Read(suf); err != nil {
		return fmt.Errorf("entropy: %w", err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%s", path, os.Getpid(), hex.EncodeToString(suf))

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("creating temp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("syncing temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("closing temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming %s -> %s: %w", tmp, path, err)
	}
	if dir, err := os.Open(parent); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestAtomicWriteFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/atomic.go internal/plugins/atomic_test.go
git commit -m "plugins: atomic temp+rename file writes"
```

---

## Task 3: Registry file lock

**Files:**
- Create: `internal/plugins/locks.go`
- Test: `internal/plugins/locks_test.go`
- Modify: `go.mod` (promote `golang.org/x/sys` to a direct dependency)

**Interfaces:**
- Produces: `func acquireLock(lockPath string, timeout time.Duration) (release func(), err error)` — exclusive `flock`; retries with backoff until `timeout`, then returns an "operation in progress" error.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireLock_ExclusiveWithTimeout(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.lock")

	release, err := acquireLock(lp, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire must fail within the timeout while the first is held.
	_, err = acquireLock(lp, 100*time.Millisecond)
	if err == nil {
		t.Fatal("second acquire succeeded while lock held; want timeout error")
	}

	release()

	// After release, acquire must succeed again.
	release2, err := acquireLock(lp, time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestAcquireLock -v`
Expected: FAIL — `undefined: acquireLock`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// acquireLock takes an exclusive flock on lockPath, retrying with capped
// exponential backoff until timeout elapses. The returned release unlocks and
// closes the file.
func acquireLock(lockPath string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating lock parent: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
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
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another serf plugin operation is in progress (locked: %s)", lockPath)
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
}
```

- [ ] **Step 4: Promote the dependency and run tests**

Run: `go get golang.org/x/sys/unix && go test ./internal/plugins/ -run TestAcquireLock -v`
Expected: `go get` moves `golang.org/x/sys` from indirect to direct in `go.mod`; tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/locks.go internal/plugins/locks_test.go go.mod go.sum
git commit -m "plugins: flock-based registry lock with backoff"
```

---

## Task 4: Install registry (`installed_plugins.json`)

**Files:**
- Create: `internal/plugins/registry.go`
- Test: `internal/plugins/registry_test.go`

**Interfaces:**
- Consumes: `atomicWriteFile` (Task 2).
- Produces: `type Registry struct { Version int; Plugins map[string][]InstallEntry }`; `type InstallEntry struct { InstallPath, Version, GitCommitSha string; InstalledAt, LastUpdated time.Time; Enabled, AutoUpgrade bool; Source Source }`; `func LoadRegistry(path string) (Registry, error)` (absent → `{Version:2, Plugins:{}}`); `func SaveRegistry(path string, r Registry) error`. (`Source` is defined in Task 6; until then the field type will not compile — Task 6 is a prerequisite for the build but this task's *test* only needs the numeric/string/bool fields, so define a minimal `Source` stub here and replace it in Task 6. To avoid a stub, order Task 6 before Task 4 during execution; the plan lists Task 6's `Source` type in its Interfaces block so it can be written first if preferred.)

> **Execution note:** `InstallEntry.Source` refers to `Source` from Task 6. If you implement strictly in listed order, add a one-line `type Source struct{}` placeholder at the top of `registry.go`, then delete it when Task 6 creates the real `Source` in `source.go`. Simplest path: do Task 6 (Source type) before Task 4. Either way the committed tree after Task 6 has exactly one `Source` definition.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRegistry_AbsentReturnsEmptyV2(t *testing.T) {
	r, err := LoadRegistry(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("LoadRegistry absent: %v", err)
	}
	if r.Version != 2 || r.Plugins == nil || len(r.Plugins) != 0 {
		t.Fatalf("absent registry = %+v, want {Version:2, Plugins:{}}", r)
	}
}

func TestSaveLoadRegistry_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "installed_plugins.json")
	in := Registry{
		Version: 2,
		Plugins: map[string][]InstallEntry{
			"widget@acme": {{
				InstallPath:  "/store/cache/acme/widget/abc123",
				Version:      "1.0.0",
				GitCommitSha: "abc123",
				InstalledAt:  time.Unix(1000, 0).UTC(),
				LastUpdated:  time.Unix(2000, 0).UTC(),
				Enabled:      true,
			}},
		},
	}
	if err := SaveRegistry(p, in); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	out, err := LoadRegistry(p)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	got := out.Plugins["widget@acme"][0]
	if got.InstallPath != "/store/cache/acme/widget/abc123" || got.Version != "1.0.0" || !got.Enabled {
		t.Fatalf("round-trip entry = %+v", got)
	}
}

func TestLoadRegistry_RejectsUnknownVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "installed_plugins.json")
	os.WriteFile(p, []byte(`{"version":99,"plugins":{}}`), 0o644)
	if _, err := LoadRegistry(p); err == nil {
		t.Fatal("expected error for unsupported schema version 99")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestLoadRegistry -v` and `... -run TestSaveLoadRegistry -v`
Expected: FAIL — `undefined: LoadRegistry`, `undefined: Registry`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Registry is installed_plugins.json: the set of installed plugins keyed by
// "<plugin>@<marketplace>". The value is an array (Claude Code's shape); v1
// writes exactly one entry per key.
type Registry struct {
	Version int                       `json:"version"`
	Plugins map[string][]InstallEntry `json:"plugins"`
}

// InstallEntry is one installed plugin.
type InstallEntry struct {
	InstallPath  string    `json:"installPath"`
	Version      string    `json:"version"`
	GitCommitSha string    `json:"gitCommitSha,omitempty"`
	InstalledAt  time.Time `json:"installedAt"`
	LastUpdated  time.Time `json:"lastUpdated"`
	Enabled      bool      `json:"enabled"`
	AutoUpgrade  bool      `json:"autoUpgrade"`
	Source       Source    `json:"source"`
}

// LoadRegistry reads installed_plugins.json. An absent file is not an error: it
// returns an empty v2 registry.
func LoadRegistry(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Registry{Version: 2, Plugins: map[string][]InstallEntry{}}, nil
		}
		return Registry{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return Registry{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if r.Version != 1 && r.Version != 2 {
		return Registry{}, fmt.Errorf("parsing %s: unsupported installed_plugins.json schema version %d", path, r.Version)
	}
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	return r, nil
}

// SaveRegistry writes r to path atomically, always at schema version 2.
func SaveRegistry(path string, r Registry) error {
	if r.Plugins == nil {
		r.Plugins = map[string][]InstallEntry{}
	}
	r.Version = 2
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling registry: %w", err)
	}
	body = append(body, '\n')
	return atomicWriteFile(path, body, 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run 'TestLoadRegistry|TestSaveLoadRegistry' -v`
Expected: PASS. (Requires `Source` from Task 6 to compile — see the execution note above.)

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/registry.go internal/plugins/registry_test.go
git commit -m "plugins: installed_plugins.json registry load/save"
```

---

## Task 5: Git shell-out

**Files:**
- Create: `internal/plugins/git.go`
- Test: `internal/plugins/git_test.go`

**Interfaces:**
- Produces:
  - `func gitAvailable() bool`
  - `func gitClone(ctx context.Context, url, dir, ref, sha string) error` — clone `url` into `dir`; checkout `ref` then `sha` if set.
  - `func gitSparseClone(ctx context.Context, url, dir, subdir, ref, sha string) error` — blobless partial clone limited to `subdir`.
  - `func gitPull(ctx context.Context, dir string) error` — `git -C dir pull --ff-only`.
  - `func gitHeadSHA(ctx context.Context, dir string) (string, error)` — `git -C dir rev-parse HEAD`.
- Consumes: nothing serf-specific.

- [ ] **Step 1: Write a test helper that builds a real local git repo, then the failing test**

```go
package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeGitRepo initialises a git repo at dir with one file and returns its HEAD sha.
func makeGitRepo(t *testing.T, dir string, file, content string) string {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git not available")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out))
}

func TestGitClone_CopiesRepoAtSha(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	sha := makeGitRepo(t, src, "plugin.txt", "hello")

	dst := filepath.Join(t.TempDir(), "dst")
	if err := gitClone(context.Background(), src, dst, "", sha); err != nil {
		t.Fatalf("gitClone: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "plugin.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("cloned file = %q, err %v", b, err)
	}
	got, err := gitHeadSHA(context.Background(), dst)
	if err != nil || got != sha {
		t.Fatalf("HEAD = %q err %v, want %q", got, err, sha)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestGitClone -v`
Expected: FAIL — `undefined: gitAvailable`, `undefined: gitClone`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// gitClone clones url into dir, then checks out ref and/or sha when set.
func gitClone(ctx context.Context, url, dir, ref, sha string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if sha == "" && ref == "" {
		args = append(args, "--depth=1")
	}
	args = append(args, url, dir)
	if _, err := git(ctx, "", args...); err != nil {
		return err
	}
	if ref != "" {
		if _, err := git(ctx, dir, "checkout", "--quiet", ref); err != nil {
			return err
		}
	}
	if sha != "" {
		if _, err := git(ctx, dir, "checkout", "--quiet", sha); err != nil {
			return err
		}
	}
	return nil
}

// gitSparseClone does a blobless, sparse clone of url into dir limited to
// subdir, then pins ref/sha. Falls back to a full checkout of subdir contents.
func gitSparseClone(ctx context.Context, url, dir, subdir, ref, sha string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet", "--filter=blob:none", "--no-checkout", url, dir}
	if _, err := git(ctx, "", args...); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "sparse-checkout", "set", "--cone", subdir); err != nil {
		return err
	}
	target := "HEAD"
	if sha != "" {
		target = sha
	} else if ref != "" {
		target = ref
	}
	if _, err := git(ctx, dir, "checkout", "--quiet", target); err != nil {
		return err
	}
	return nil
}

func gitPull(ctx context.Context, dir string) error {
	_, err := git(ctx, dir, "pull", "--ff-only", "--quiet")
	return err
}

func gitHeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestGitClone -v`
Expected: PASS (or SKIP if git is unavailable).

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/git.go internal/plugins/git_test.go
git commit -m "plugins: git shell-out (clone, sparse clone, pull, head sha)"
```

---

## Task 6: `Source` type (kinds, string form, legacy alias)

**Files:**
- Create: `internal/plugins/source.go`
- Test: `internal/plugins/source_test.go`

**Interfaces:**
- Produces: `type SourceKind string` with consts `SourceDirectory="directory"`, `SourceGitHub="github"`, `SourceURL="url"`, `SourceGitSubdir="git-subdir"`; `type Source struct { Kind SourceKind; Repo, URL, Path, Ref, Sha string; Rel bool }` with `UnmarshalJSON`/`MarshalJSON` that (a) accept a bare JSON string `"./sub"` as `{Kind:SourceDirectory, Path:"./sub", Rel:true}`, (b) map a read `"source":"git"` to `SourceURL`, and (c) never write `"git"`.
- Consumed by: `Registry.InstallEntry.Source` (Task 4), `catalog.go` (Task 8), `marketplaces.go` (Task 7).

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"encoding/json"
	"testing"
)

func TestSource_UnmarshalObjectForms(t *testing.T) {
	cases := map[string]Source{
		`{"source":"github","repo":"o/r"}`:                          {Kind: SourceGitHub, Repo: "o/r"},
		`{"source":"url","url":"https://x/y.git"}`:                   {Kind: SourceURL, URL: "https://x/y.git"},
		`{"source":"directory","path":"/abs"}`:                       {Kind: SourceDirectory, Path: "/abs"},
		`{"source":"git-subdir","url":"https://x.git","path":"a/b"}`: {Kind: SourceGitSubdir, URL: "https://x.git", Path: "a/b"},
		`{"source":"git","url":"https://x/y.git"}`:                   {Kind: SourceURL, URL: "https://x/y.git"}, // legacy alias
	}
	for in, want := range cases {
		var s Source
		if err := json.Unmarshal([]byte(in), &s); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if s != want {
			t.Errorf("Unmarshal(%s) = %+v, want %+v", in, s, want)
		}
	}
}

func TestSource_UnmarshalStringForm(t *testing.T) {
	var s Source
	if err := json.Unmarshal([]byte(`"./plugins/widget"`), &s); err != nil {
		t.Fatalf("Unmarshal string: %v", err)
	}
	if s.Kind != SourceDirectory || s.Path != "./plugins/widget" || !s.Rel {
		t.Fatalf("string source = %+v, want directory/rel", s)
	}
}

func TestSource_MarshalNeverWritesGit(t *testing.T) {
	b, _ := json.Marshal(Source{Kind: SourceURL, URL: "https://x/y.git"})
	if got := string(b); got == `{"source":"git","url":"https://x/y.git"}` || !json.Valid(b) {
		t.Fatalf("marshalled = %s", got)
	}
	var round Source
	json.Unmarshal(b, &round)
	if round.Kind != SourceURL {
		t.Fatalf("round-trip kind = %q", round.Kind)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestSource_ -v`
Expected: FAIL — `undefined: Source`, `undefined: SourceGitHub`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type SourceKind string

const (
	SourceDirectory SourceKind = "directory"
	SourceGitHub    SourceKind = "github"
	SourceURL       SourceKind = "url"
	SourceGitSubdir SourceKind = "git-subdir"
)

// Source is a marketplace-container or plugin source. Field meaning depends on
// Kind: Repo (github), URL (url/git-subdir), Path (directory local path, or
// git-subdir subdirectory, or the bare-string relative path). Rel marks the
// Claude "./subdir" bare-string plugin-source form (relative to marketplace root).
type Source struct {
	Kind SourceKind
	Repo string
	URL  string
	Path string
	Ref  string
	Sha  string
	Rel  bool
}

// sourceJSON is the on-disk object shape.
type sourceJSON struct {
	Source string `json:"source"`
	Repo   string `json:"repo,omitempty"`
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Sha    string `json:"sha,omitempty"`
}

func (s *Source) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' { // bare string form: "./subdir"
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = Source{Kind: SourceDirectory, Path: str, Rel: true}
		return nil
	}
	var j sourceJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	kind := SourceKind(j.Source)
	if kind == "git" { // read-only legacy alias
		kind = SourceURL
	}
	switch kind {
	case SourceDirectory, SourceGitHub, SourceURL, SourceGitSubdir:
	default:
		return fmt.Errorf("unknown plugin source type %q", j.Source)
	}
	*s = Source{Kind: kind, Repo: j.Repo, URL: j.URL, Path: j.Path, Ref: j.Ref, Sha: j.Sha}
	return nil
}

func (s Source) MarshalJSON() ([]byte, error) {
	return json.Marshal(sourceJSON{
		Source: string(s.Kind), Repo: s.Repo, URL: s.URL, Path: s.Path, Ref: s.Ref, Sha: s.Sha,
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestSource_ -v`
Expected: PASS. Also run `go build ./internal/plugins/...` to confirm `registry.go`'s `Source` field now resolves.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/source.go internal/plugins/source_test.go
git commit -m "plugins: Source type with string-form + legacy-git-alias parsing"
```

---

## Task 7: Marketplace registry & verbs (add/list/remove/refresh)

**Files:**
- Create: `internal/plugins/marketplaces.go`
- Test: `internal/plugins/marketplaces_test.go`

**Interfaces:**
- Consumes: `Source` (Task 6), `gitClone`/`gitPull` (Task 5), `atomicWriteFile` (Task 2), `acquireLock` (Task 3), `Manager` paths (Task 1).
- Produces: `type MarketplaceRef struct { Source Source; InstallLocation string; LastUpdated time.Time }`; `type Marketplaces map[string]MarketplaceRef`; `func (m *Manager) loadMarketplaces() (Marketplaces, error)`; `func (m *Manager) AddMarketplace(ctx, name string, src Source) (MarketplaceRef, error)` (name `""` → derived from `marketplace.json`); `func (m *Manager) ListMarketplaces() (Marketplaces, error)`; `func (m *Manager) RemoveMarketplace(name string) error`; `func (m *Manager) RefreshMarketplace(ctx, name string) error`.

- [ ] **Step 1: Write the failing test** (uses `makeGitRepo` from Task 5 and a `marketplace.json` fixture)

```go
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeMarketplaceRepo builds a git repo containing a .claude-plugin/marketplace.json
// naming one plugin, and returns its path.
func makeMarketplaceRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mkt-"+name)
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[` +
		`{"name":"widget","description":"a widget","source":"./plugins/widget"}]}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, dir, "README.md", "mkt") // also commits marketplace.json via `git add .`
	return dir
}

func TestAddListRemoveMarketplace(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())

	ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if ref.InstallLocation == "" {
		t.Fatal("empty InstallLocation")
	}

	list, err := m.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := list["acme"]; !ok {
		t.Fatalf("marketplace 'acme' not listed: %v", list)
	}

	if err := m.RemoveMarketplace("acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	list, _ = m.ListMarketplaces()
	if _, ok := list["acme"]; ok {
		t.Fatal("marketplace still present after remove")
	}
	if _, err := os.Stat(m.marketplaceDir("acme")); !os.IsNotExist(err) {
		t.Fatal("clone dir not deleted after remove")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestAddListRemoveMarketplace -v`
Expected: FAIL — `undefined: (*Manager).AddMarketplace`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type MarketplaceRef struct {
	Source          Source    `json:"source"`
	InstallLocation string    `json:"installLocation"`
	LastUpdated     time.Time `json:"lastUpdated"`
}

type Marketplaces map[string]MarketplaceRef

func (m *Manager) loadMarketplaces() (Marketplaces, error) {
	data, err := os.ReadFile(m.marketplacesFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Marketplaces{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", m.marketplacesFile(), err)
	}
	var mk Marketplaces
	if err := json.Unmarshal(data, &mk); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", m.marketplacesFile(), err)
	}
	if mk == nil {
		mk = Marketplaces{}
	}
	return mk, nil
}

func (m *Manager) saveMarketplaces(mk Marketplaces) error {
	body, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling marketplaces: %w", err)
	}
	return atomicWriteFile(m.marketplacesFile(), append(body, '\n'), 0o644)
}

// fetchMarketplaceContainer clones/references src into destDir and returns the
// directory that contains .claude-plugin/marketplace.json.
func (m *Manager) fetchMarketplaceContainer(ctx context.Context, src Source, destDir string) (string, error) {
	switch src.Kind {
	case SourceDirectory:
		return src.Path, nil // referenced in place
	case SourceGitHub:
		url := "https://github.com/" + src.Repo + ".git"
		if err := gitClone(ctx, url, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return destDir, nil
	case SourceURL:
		if err := gitClone(ctx, src.URL, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return destDir, nil
	case SourceGitSubdir:
		if err := gitSparseClone(ctx, src.URL, destDir, src.Path, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return filepath.Join(destDir, src.Path), nil
	default:
		return "", fmt.Errorf("unsupported marketplace source %q", src.Kind)
	}
}

// AddMarketplace fetches src, reads its marketplace.json for the name (unless
// name is given), and records it. Returns the stored ref.
func (m *Manager) AddMarketplace(ctx context.Context, name string, src Source) (MarketplaceRef, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return MarketplaceRef{}, err
	}
	defer release()

	// Fetch into a staging dir first so a bad marketplace never half-registers.
	staging := m.marketplaceDir(".staging")
	os.RemoveAll(staging)
	root, err := m.fetchMarketplaceContainer(ctx, src, staging)
	if err != nil {
		os.RemoveAll(staging)
		return MarketplaceRef{}, err
	}
	cat, err := ParseCatalog(root)
	if err != nil {
		os.RemoveAll(staging)
		return MarketplaceRef{}, fmt.Errorf("reading marketplace.json: %w", err)
	}
	if name == "" {
		name = cat.Name
	}
	if name == "" {
		os.RemoveAll(staging)
		return MarketplaceRef{}, errors.New("marketplace has no name and none was given")
	}

	installLoc := src.Path // directory source: in place
	if src.Kind != SourceDirectory {
		installLoc = m.marketplaceDir(name)
		os.RemoveAll(installLoc)
		if err := os.Rename(staging, installLoc); err != nil {
			os.RemoveAll(staging)
			return MarketplaceRef{}, fmt.Errorf("installing marketplace clone: %w", err)
		}
	} else {
		os.RemoveAll(staging)
	}

	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}
	ref := MarketplaceRef{Source: src, InstallLocation: installLoc, LastUpdated: m.now().UTC()}
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		return MarketplaceRef{}, err
	}
	return ref, nil
}

func (m *Manager) ListMarketplaces() (Marketplaces, error) { return m.loadMarketplaces() }

func (m *Manager) RemoveMarketplace(name string) error {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	ref, ok := mk[name]
	if !ok {
		return fmt.Errorf("marketplace %q not found", name)
	}
	if ref.Source.Kind != SourceDirectory {
		os.RemoveAll(m.marketplaceDir(name)) // never delete a directory-source's own contents
	}
	delete(mk, name)
	return m.saveMarketplaces(mk)
}

func (m *Manager) RefreshMarketplace(ctx context.Context, name string) error {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	ref, ok := mk[name]
	if !ok {
		return fmt.Errorf("marketplace %q not found", name)
	}
	if ref.Source.Kind != SourceDirectory {
		if err := gitPull(ctx, ref.InstallLocation); err != nil {
			return err
		}
	}
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	return m.saveMarketplaces(mk)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestAddListRemoveMarketplace -v`
Expected: PASS (needs `ParseCatalog` from Task 8 to compile — implement Task 8 first, or add a temporary stub `func ParseCatalog(dir string) (Catalog, error)` and complete it in Task 8; the listed order builds Task 8 before running this test).

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/marketplaces.go internal/plugins/marketplaces_test.go
git commit -m "plugins: known_marketplaces.json + add/list/remove/refresh"
```

---

## Task 8: Catalog parsing (`marketplace.json`) and Browse

**Files:**
- Create: `internal/plugins/catalog.go`
- Test: `internal/plugins/catalog_test.go`

**Interfaces:**
- Consumes: `Source` (Task 6).
- Produces: `type Catalog struct { Name, Description string; Owner CatalogOwner; PluginRoot string; Plugins []CatalogPlugin }`; `type CatalogOwner struct { Name, Email string }`; `type CatalogPlugin struct { Name, Description, Category, Homepage string; Author CatalogOwner; Source Source }`; `func ParseCatalog(marketplaceRoot string) (Catalog, error)` (reads `<root>/.claude-plugin/marketplace.json`); `func (m *Manager) Browse(name string) (Catalog, error)`.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	root := t.TempDir()
	mj := `{
	  "name":"acme","description":"d","owner":{"name":"o","email":"o@e"},
	  "metadata":{"pluginRoot":"plugins"},
	  "plugins":[
	    {"name":"widget","description":"w","category":"dev","source":"./plugins/widget"},
	    {"name":"gadget","source":{"source":"git-subdir","url":"https://x.git","path":"g"}}
	  ]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if cat.Name != "acme" || len(cat.Plugins) != 2 {
		t.Fatalf("catalog = %+v", cat)
	}
	if cat.Plugins[0].Source.Kind != SourceDirectory || !cat.Plugins[0].Source.Rel {
		t.Errorf("widget source = %+v, want rel directory", cat.Plugins[0].Source)
	}
	if cat.Plugins[1].Source.Kind != SourceGitSubdir {
		t.Errorf("gadget source = %+v, want git-subdir", cat.Plugins[1].Source)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestParseCatalog -v`
Expected: FAIL — `undefined: ParseCatalog`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type CatalogOwner struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type CatalogPlugin struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Category    string       `json:"category,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Author      CatalogOwner `json:"author,omitempty"`
	Source      Source       `json:"source"`
}

type Catalog struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Owner       CatalogOwner    `json:"owner,omitempty"`
	Metadata    catalogMetadata `json:"metadata,omitempty"`
	Plugins     []CatalogPlugin `json:"plugins"`
}

type catalogMetadata struct {
	PluginRoot string `json:"pluginRoot,omitempty"`
}

// ParseCatalog reads <marketplaceRoot>/.claude-plugin/marketplace.json.
func ParseCatalog(marketplaceRoot string) (Catalog, error) {
	p := filepath.Join(marketplaceRoot, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return Catalog{}, fmt.Errorf("reading %s: %w", p, err)
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}, fmt.Errorf("parsing %s: %w", p, err)
	}
	return c, nil
}

// Browse returns the parsed catalog of a registered marketplace.
func (m *Manager) Browse(name string) (Catalog, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return Catalog{}, err
	}
	ref, ok := mk[name]
	if !ok {
		return Catalog{}, fmt.Errorf("marketplace %q not found", name)
	}
	return ParseCatalog(ref.InstallLocation)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run 'TestParseCatalog|TestAddListRemoveMarketplace' -v`
Expected: PASS (Task 7's test now compiles and passes too).

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/catalog.go internal/plugins/catalog_test.go
git commit -m "plugins: marketplace.json catalog parsing + Browse"
```

---

## Task 9: `computeVersion`

**Files:**
- Create: `internal/plugins/version.go`
- Test: `internal/plugins/version_test.go`

**Interfaces:**
- Produces: `func computeVersion(pluginJSONVer, declaredVer, commitSHA string) string` — precedence: plugin.json version → marketplace-declared version → 12-char commit sha → `"unknown"`.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import "testing"

func TestComputeVersion(t *testing.T) {
	cases := []struct{ pj, decl, sha, want string }{
		{"1.2.3", "9.9", "abcdef0123456789", "1.2.3"},
		{"", "2.0", "abcdef0123456789", "2.0"},
		{"", "", "abcdef0123456789ff", "abcdef012345"},
		{"", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := computeVersion(c.pj, c.decl, c.sha); got != c.want {
			t.Errorf("computeVersion(%q,%q,%q) = %q, want %q", c.pj, c.decl, c.sha, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestComputeVersion -v`
Expected: FAIL — `undefined: computeVersion`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

// computeVersion picks a display version: plugin.json version, else the
// marketplace-declared version, else a 12-char commit sha, else "unknown".
func computeVersion(pluginJSONVer, declaredVer, commitSHA string) string {
	if pluginJSONVer != "" {
		return pluginJSONVer
	}
	if declaredVer != "" {
		return declaredVer
	}
	if commitSHA != "" {
		if len(commitSHA) > 12 {
			return commitSHA[:12]
		}
		return commitSHA
	}
	return "unknown"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestComputeVersion -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/version.go internal/plugins/version_test.go
git commit -m "plugins: computeVersion precedence"
```

---

## Task 10: Plugin validation (dry-run `agent/plugin.Load`)

**Files:**
- Create: `internal/plugins/validate.go`
- Test: `internal/plugins/validate_test.go`

**Interfaces:**
- Consumes: `primeradiant.com/serf/agent/plugin` (`Load`).
- Produces: `func validatePluginDir(dir string) error` — returns nil iff `agent/plugin.Load(dir)` succeeds (manifest + all components parse); `func pluginManifestVersion(dir string) string` — best-effort plugin.json version for `computeVersion` (empty on any error).

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlugin(t *testing.T, dir, name string, extra map[string]string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","version":"1.0.0"}`), 0o644)
	for rel, content := range extra {
		full := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(content), 0o644)
	}
}

func TestValidatePluginDir(t *testing.T) {
	good := filepath.Join(t.TempDir(), "good")
	writePlugin(t, good, "widget", nil)
	if err := validatePluginDir(good); err != nil {
		t.Fatalf("valid plugin rejected: %v", err)
	}
	if v := pluginManifestVersion(good); v != "1.0.0" {
		t.Fatalf("manifest version = %q, want 1.0.0", v)
	}

	// A broken agents/*.md (missing frontmatter name) must fail validation,
	// because agent/plugin.Load parses every component.
	bad := filepath.Join(t.TempDir(), "bad")
	writePlugin(t, bad, "widget", map[string]string{
		"agents/broken.md": "no frontmatter here",
	})
	if err := validatePluginDir(bad); err == nil {
		t.Fatal("broken plugin passed validation")
	}
}
```

> **Execution note:** confirm the exact failure mode of a malformed `agents/*.md` against the real `agent/plugin/agents.go` parser before finalizing the "bad" fixture — if a body-less `.md` is tolerated, switch the fixture to an invalid `plugin.json` (e.g. non-kebab-case name `"Widget"`) which `agent/plugin.Load` definitively rejects (`validatePluginName`). The test's intent — "a plugin that `Load` rejects fails `validatePluginDir`" — is what matters.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestValidatePluginDir -v`
Expected: FAIL — `undefined: validatePluginDir`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"

	agentplugin "primeradiant.com/serf/agent/plugin"
)

// validatePluginDir returns nil iff the plugin at dir loads cleanly — manifest
// plus every component (agents, hooks, mcp, skills) parses. It reuses the exact
// loader the session uses, so "installs" and "loads" agree.
func validatePluginDir(dir string) error {
	_, err := agentplugin.Load(dir)
	return err
}

// pluginManifestVersion returns the plugin.json "version" (best effort; "" on
// any error or absence), for computeVersion.
func pluginManifestVersion(dir string) string {
	for _, mf := range []string{".claude-plugin", ".codex-plugin"} {
		data, err := os.ReadFile(filepath.Join(dir, mf, "plugin.json"))
		if err != nil {
			continue
		}
		var m struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &m) == nil {
			return m.Version
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestValidatePluginDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/validate.go internal/plugins/validate_test.go
git commit -m "plugins: validate materialized plugin via agent/plugin.Load"
```

---

## Task 11: Plugin source resolution & fetch

**Files:**
- Modify: `internal/plugins/source.go`
- Test: `internal/plugins/source_fetch_test.go`

**Interfaces:**
- Consumes: `gitClone`/`gitSparseClone`/`gitHeadSHA` (Task 5).
- Produces: `func fetchPluginSource(ctx context.Context, src Source, marketplaceRoot, destDir string) (sha string, err error)` — materializes a plugin's source into `destDir`, returning the resolved commit sha (empty for `directory`/relative sources). For `Rel`/directory sources it copies from `marketplaceRoot` (or the absolute `Path`); for git sources it clones (pinned) then reports HEAD.
- Produces helper: `func copyTree(src, dst string) error`.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchPluginSource_RelativeCopiesFromMarketplace(t *testing.T) {
	mktRoot := t.TempDir()
	// a plugin living at <mktRoot>/plugins/widget
	writePlugin(t, filepath.Join(mktRoot, "plugins", "widget"), "widget", nil)

	dst := filepath.Join(t.TempDir(), "out")
	sha, err := fetchPluginSource(context.Background(),
		Source{Kind: SourceDirectory, Path: "./plugins/widget", Rel: true}, mktRoot, dst)
	if err != nil {
		t.Fatalf("fetchPluginSource: %v", err)
	}
	if sha != "" {
		t.Errorf("relative source sha = %q, want empty", sha)
	}
	if _, err := os.Stat(filepath.Join(dst, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin.json not copied: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestFetchPluginSource_ -v`
Expected: FAIL — `undefined: fetchPluginSource`.

- [ ] **Step 3: Write the implementation (append to `source.go`)**

```go
import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// fetchPluginSource materializes a plugin's source into destDir. It returns the
// resolved commit sha (empty for directory/relative sources).
func fetchPluginSource(ctx context.Context, src Source, marketplaceRoot, destDir string) (string, error) {
	switch {
	case src.Rel || src.Kind == SourceDirectory:
		from := src.Path
		if src.Rel {
			from = filepath.Join(marketplaceRoot, src.Path)
		}
		if err := copyTree(from, destDir); err != nil {
			return "", err
		}
		return "", nil
	case src.Kind == SourceGitHub:
		url := "https://github.com/" + src.Repo + ".git"
		if err := gitClone(ctx, url, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return gitHeadSHA(ctx, destDir)
	case src.Kind == SourceURL:
		if err := gitClone(ctx, src.URL, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return gitHeadSHA(ctx, destDir)
	case src.Kind == SourceGitSubdir:
		clone := destDir + ".clone"
		defer os.RemoveAll(clone)
		if err := gitSparseClone(ctx, src.URL, clone, src.Path, src.Ref, src.Sha); err != nil {
			return "", err
		}
		if err := copyTree(filepath.Join(clone, src.Path), destDir); err != nil {
			return "", err
		}
		return gitHeadSHA(ctx, clone)
	default:
		return "", fmt.Errorf("unsupported plugin source %q", src.Kind)
	}
}

// copyTree recursively copies src to dst (files, dirs, and symlink targets as
// regular files); dst is created fresh.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", src)
	}
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
```

> **Execution note:** `source.go` already imports `bytes`, `encoding/json`, `fmt` (Task 6). Merge the new imports (`context`, `io`, `os`, `path/filepath`) into the existing import block rather than adding a second one.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestFetchPluginSource_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/source.go internal/plugins/source_fetch_test.go
git commit -m "plugins: resolve + fetch plugin sources (relative/github/url/git-subdir)"
```

---

## Task 12: `Install`

**Files:**
- Create: `internal/plugins/install.go`
- Test: `internal/plugins/install_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `func (m *Manager) Install(ctx context.Context, plugin, marketplace string) (InstallEntry, error)` — resolves the plugin's source from the marketplace catalog, materializes it (into `cache/<mkt>/<plugin>/<sha>/`, or in place for a directory marketplace), validates, and records an enabled registry entry keyed `"<plugin>@<marketplace>"`.
- Produces helper: `registryKey(plugin, marketplace string) string`.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeInstallableMarketplace builds a git marketplace whose one plugin's source
// is a bare-string "./plugins/widget" living in the same repo.
func makeInstallableMarketplace(t *testing.T) (mktRepo, name string) {
	t.Helper()
	name = "acme"
	dir := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`), 0o644)
	writePlugin(t, filepath.Join(dir, "plugins", "widget"), "widget", nil)
	makeGitRepo(t, dir, "README.md", "x")
	return dir, name
}

func TestInstall_MaterializesAndRegisters(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !entry.Enabled {
		t.Error("installed entry not enabled")
	}
	if _, err := os.Stat(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("materialized plugin.json missing: %v", err)
	}

	reg, _ := LoadRegistry(m.registryPath())
	if _, ok := reg.Plugins["widget@acme"]; !ok {
		t.Fatalf("registry missing widget@acme: %+v", reg.Plugins)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestInstall_ -v`
Expected: FAIL — `undefined: (*Manager).Install`.

- [ ] **Step 3: Write the implementation**

```go
package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func registryKey(plugin, marketplace string) string { return plugin + "@" + marketplace }

// catalogPlugin finds a named plugin's entry + its marketplace ref.
func (m *Manager) catalogPlugin(marketplace, plugin string) (MarketplaceRef, CatalogPlugin, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, CatalogPlugin{}, err
	}
	ref, ok := mk[marketplace]
	if !ok {
		return MarketplaceRef{}, CatalogPlugin{}, fmt.Errorf("marketplace %q not found", marketplace)
	}
	cat, err := ParseCatalog(ref.InstallLocation)
	if err != nil {
		return MarketplaceRef{}, CatalogPlugin{}, err
	}
	for _, p := range cat.Plugins {
		if p.Name == plugin {
			return ref, p, nil
		}
	}
	return MarketplaceRef{}, CatalogPlugin{}, fmt.Errorf("plugin %q not found in marketplace %q", plugin, marketplace)
}

// materialize fetches a plugin's source into the cache (or references it in
// place for a directory marketplace) and returns its dir + resolved sha.
func (m *Manager) materialize(ctx context.Context, marketplace, plugin string, ref MarketplaceRef, cp CatalogPlugin) (dir, sha string, err error) {
	if ref.Source.Kind == SourceDirectory {
		// referenced in place: resolve relative to the marketplace root, no copy.
		root := ref.InstallLocation
		if cp.Source.Rel {
			return filepath.Join(root, cp.Source.Path), "", nil
		}
		if cp.Source.Kind == SourceDirectory {
			return cp.Source.Path, "", nil
		}
	}
	// staging → sha → move to cache/<mkt>/<plugin>/<sha>/
	staging := m.pluginCacheDir(marketplace, plugin, ".staging")
	os.RemoveAll(staging)
	sha, err = fetchPluginSource(ctx, cp.Source, ref.InstallLocation, staging)
	if err != nil {
		os.RemoveAll(staging)
		return "", "", err
	}
	key := sha
	if key == "" {
		key = "unknown"
	}
	final := m.pluginCacheDir(marketplace, plugin, key)
	os.RemoveAll(final)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		os.RemoveAll(staging)
		return "", "", err
	}
	if err := os.Rename(staging, final); err != nil {
		os.RemoveAll(staging)
		return "", "", err
	}
	return final, sha, nil
}

// Install installs plugin from marketplace, enabled.
func (m *Manager) Install(ctx context.Context, plugin, marketplace string) (InstallEntry, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return InstallEntry{}, err
	}
	defer release()

	ref, cp, err := m.catalogPlugin(marketplace, plugin)
	if err != nil {
		return InstallEntry{}, err
	}
	dir, sha, err := m.materialize(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if err := validatePluginDir(dir); err != nil {
		return InstallEntry{}, fmt.Errorf("installed plugin failed validation: %w", err)
	}

	now := m.now().UTC()
	entry := InstallEntry{
		InstallPath:  dir,
		Version:      computeVersion(pluginManifestVersion(dir), cp.Source.Ref, sha),
		GitCommitSha: sha,
		InstalledAt:  now,
		LastUpdated:  now,
		Enabled:      true,
		AutoUpgrade:  false,
		Source:       cp.Source,
	}
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return InstallEntry{}, err
	}
	reg.Plugins[registryKey(plugin, marketplace)] = []InstallEntry{entry}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		return InstallEntry{}, err
	}
	return entry, nil
}
```

> **Execution note:** `install.go`'s import block is `context`, `fmt`, `os`, `path/filepath`, `time` (Tasks 13–15 fold in `strings` and `sort`). Keep one import block as later tasks append.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestInstall_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/install_test.go
git commit -m "plugins: Install — materialize, validate, register (enabled)"
```

---

## Task 13: `Upgrade` (new sha-dir, repoint, old dir remains)

**Files:**
- Modify: `internal/plugins/install.go`
- Test: `internal/plugins/upgrade_test.go`

**Interfaces:**
- Produces: `func (m *Manager) Upgrade(ctx context.Context, plugin, marketplace string) (InstallEntry, error)` — re-materialize from the current catalog, validate, and repoint the registry to the new sha-dir. **Never deletes** the old dir (§12 sweep reclaims it). Returns the updated entry (unchanged if the sha is identical).

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpgrade_NewShaDirOldRemains(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	// A marketplace whose plugin source is a github/url git repo (so it has a sha).
	pluginRepo := filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "widget", nil)
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"}}]}`), 0o644)
	makeGitRepo(t, mktRepo, "README.md", "x")

	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	first, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Advance the plugin repo HEAD.
	os.WriteFile(filepath.Join(pluginRepo, "extra.txt"), []byte("v2"), 0o644)
	cmd := exec.Command("git", "-C", pluginRepo, "commit", "-aqm", "v2")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cmd.Run()

	second, err := m.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if second.InstallPath == first.InstallPath {
		t.Fatal("upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(first.InstallPath); err != nil {
		t.Fatal("old sha-dir was deleted; upgrade must not GC")
	}
	reg, _ := LoadRegistry(m.registryPath())
	if reg.Plugins["widget@acme"][0].InstallPath != second.InstallPath {
		t.Fatal("registry not repointed to new dir")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestUpgrade_ -v`
Expected: FAIL — `undefined: (*Manager).Upgrade`.

- [ ] **Step 3: Write the implementation (append to `install.go`)**

```go
// Upgrade re-materializes plugin from its marketplace and repoints the registry
// to the new sha-dir. It never deletes the previous dir (the gc sweep, §12,
// reclaims it) so live sessions keep working. A no-op if the sha is unchanged.
func (m *Manager) Upgrade(ctx context.Context, plugin, marketplace string) (InstallEntry, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return InstallEntry{}, err
	}
	defer release()

	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return InstallEntry{}, err
	}
	entries, ok := reg.Plugins[key]
	if !ok || len(entries) == 0 {
		return InstallEntry{}, fmt.Errorf("%s is not installed", key)
	}
	prev := entries[0]

	ref, cp, err := m.catalogPlugin(marketplace, plugin)
	if err != nil {
		return InstallEntry{}, err
	}
	dir, sha, err := m.materialize(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if dir == prev.InstallPath { // same sha → nothing changed
		return prev, nil
	}
	if err := validatePluginDir(dir); err != nil {
		return InstallEntry{}, fmt.Errorf("upgraded plugin failed validation: %w", err)
	}

	prev.InstallPath = dir
	prev.GitCommitSha = sha
	prev.Version = computeVersion(pluginManifestVersion(dir), cp.Source.Ref, sha)
	prev.LastUpdated = m.now().UTC()
	prev.Source = cp.Source
	reg.Plugins[key] = []InstallEntry{prev}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		return InstallEntry{}, err
	}
	return prev, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestUpgrade_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/upgrade_test.go
git commit -m "plugins: Upgrade — new sha-dir, repoint, never GC"
```

---

## Task 14: `Remove`, `Enable`, `Disable`

**Files:**
- Modify: `internal/plugins/install.go`
- Test: `internal/plugins/lifecycle_test.go`

**Interfaces:**
- Produces:
  - `func (m *Manager) Remove(plugin, marketplace string) error` — delete the registry entry and its cache dir (in-place directory sources are not deleted).
  - `func (m *Manager) SetEnabled(plugin, marketplace string, enabled bool) error` — flip the `Enabled` flag.
  - `func (m *Manager) SetAutoUpgrade(plugin, marketplace string, on bool) error` — flip `AutoUpgrade`.

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRemoveEnableDisable(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	if err := m.SetEnabled("widget", name, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	reg, _ := LoadRegistry(m.registryPath())
	if reg.Plugins["widget@acme"][0].Enabled {
		t.Fatal("entry still enabled after disable")
	}

	if err := m.SetAutoUpgrade("widget", name, true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	reg, _ = LoadRegistry(m.registryPath())
	if !reg.Plugins["widget@acme"][0].AutoUpgrade {
		t.Fatal("autoUpgrade not set")
	}

	if err := m.Remove("widget", name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	reg, _ = LoadRegistry(m.registryPath())
	if _, ok := reg.Plugins["widget@acme"]; ok {
		t.Fatal("entry still present after remove")
	}
	// cache dir gone (installPath was under the cache root)
	if strings.HasPrefix(entry.InstallPath, m.cacheDir()) {
		if _, err := os.Stat(entry.InstallPath); !os.IsNotExist(err) {
			t.Fatal("cache dir not deleted on remove")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestRemoveEnableDisable -v`
Expected: FAIL — `undefined: (*Manager).Remove`.

- [ ] **Step 3: Write the implementation (append to `install.go`)**

```go
import "strings"

func (m *Manager) mutateEntry(plugin, marketplace string, fn func(*InstallEntry)) error {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return err
	}
	entries, ok := reg.Plugins[key]
	if !ok || len(entries) == 0 {
		return fmt.Errorf("%s is not installed", key)
	}
	e := entries[0]
	fn(&e)
	reg.Plugins[key] = []InstallEntry{e}
	return SaveRegistry(m.registryPath(), reg)
}

func (m *Manager) SetEnabled(plugin, marketplace string, enabled bool) error {
	return m.mutateEntry(plugin, marketplace, func(e *InstallEntry) { e.Enabled = enabled })
}

func (m *Manager) SetAutoUpgrade(plugin, marketplace string, on bool) error {
	return m.mutateEntry(plugin, marketplace, func(e *InstallEntry) { e.AutoUpgrade = on })
}

// Remove deletes the registry entry and its cache dir. A plugin referenced in
// place (directory-source marketplace) leaves the source untouched.
func (m *Manager) Remove(plugin, marketplace string) error {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return err
	}
	entries, ok := reg.Plugins[key]
	if !ok {
		return fmt.Errorf("%s is not installed", key)
	}
	if len(entries) > 0 {
		p := entries[0].InstallPath
		if strings.HasPrefix(p, m.cacheDir()+string(os.PathSeparator)) {
			os.RemoveAll(p)
		}
	}
	delete(reg.Plugins, key)
	return SaveRegistry(m.registryPath(), reg)
}
```

> **Execution note:** `install.go` already imports `fmt`, `os`, `time`, `context`, `path/filepath` (Tasks 12–13). Fold `strings` into that block; do not add a second import statement.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestRemoveEnableDisable -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/lifecycle_test.go
git commit -m "plugins: Remove, SetEnabled, SetAutoUpgrade"
```

---

## Task 15: `List` (with broken detection) and `UpdateAll`

**Files:**
- Modify: `internal/plugins/install.go`
- Test: `internal/plugins/list_test.go`

**Interfaces:**
- Produces:
  - `type ListItem struct { Plugin, Marketplace, Version string; Enabled, AutoUpgrade, Broken bool; InstallPath, GitCommitSha string; InstalledAt, LastUpdated time.Time }`
  - `func (m *Manager) List() ([]ListItem, error)` — every registry entry; `Broken=true` when its `InstallPath` no longer validates.
  - `func (m *Manager) UpdateAll(ctx context.Context) ([]InstallEntry, error)` — `Upgrade` every installed, git-backed plugin (skips directory-source/`Rel` entries).

- [ ] **Step 1: Write the failing test**

```go
package plugins

import (
	"context"
	"os"
	"testing"
)

func TestList_FlagsBroken(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	items, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Plugin != "widget" || items[0].Broken {
		t.Fatalf("List = %+v, want one healthy widget", items)
	}

	// Corrupt the installed plugin on disk → List must flag it broken.
	os.RemoveAll(entry.InstallPath)
	items, _ = m.List()
	if !items[0].Broken {
		t.Fatal("List did not flag a missing install dir as broken")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugins/ -run TestList_ -v`
Expected: FAIL — `undefined: (*Manager).List`.

- [ ] **Step 3: Write the implementation (append to `install.go`)**

```go
import "sort"

type ListItem struct {
	Plugin       string
	Marketplace  string
	Version      string
	Enabled      bool
	AutoUpgrade  bool
	Broken       bool
	InstallPath  string
	GitCommitSha string
	InstalledAt  time.Time
	LastUpdated  time.Time
}

func splitKey(key string) (plugin, marketplace string) {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

func (m *Manager) List() ([]ListItem, error) {
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	var out []ListItem
	for key, entries := range reg.Plugins {
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		plugin, marketplace := splitKey(key)
		out = append(out, ListItem{
			Plugin:       plugin,
			Marketplace:  marketplace,
			Version:      e.Version,
			Enabled:      e.Enabled,
			AutoUpgrade:  e.AutoUpgrade,
			Broken:       validatePluginDir(e.InstallPath) != nil,
			InstallPath:  e.InstallPath,
			GitCommitSha: e.GitCommitSha,
			InstalledAt:  e.InstalledAt,
			LastUpdated:  e.LastUpdated,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out, nil
}

// UpdateAll upgrades every installed, git-backed plugin (directory/relative
// sources are inherently current and skipped). Failures are collected but do
// not stop the others.
func (m *Manager) UpdateAll(ctx context.Context) ([]InstallEntry, error) {
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	var keys []string
	for key := range reg.Plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var updated []InstallEntry
	var errs []string
	for _, key := range keys {
		e := reg.Plugins[key][0]
		if e.Source.Rel || e.Source.Kind == SourceDirectory {
			continue
		}
		plugin, marketplace := splitKey(key)
		entry, err := m.Upgrade(ctx, plugin, marketplace)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		updated = append(updated, entry)
	}
	if len(errs) > 0 {
		return updated, fmt.Errorf("some upgrades failed:\n%s", strings.Join(errs, "\n"))
	}
	return updated, nil
}
```

> **Execution note:** fold `sort` into `install.go`'s existing import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugins/ -run TestList_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/install.go internal/plugins/list_test.go
git commit -m "plugins: List (broken detection) + UpdateAll"
```

---

## Task 16: Full-package gate

**Files:**
- None (verification only).

- [ ] **Step 1: Run the whole package with the race detector**

Run: `go test ./internal/plugins/ -race -count=1`
Expected: `ok  primeradiant.com/serf/internal/plugins`. No failures. (Git-dependent tests run if `git` is present; otherwise they SKIP — a SKIP is acceptable, a FAIL is not.)

- [ ] **Step 2: Vet and build the whole module**

Run: `go vet ./internal/plugins/... && go build ./...`
Expected: both succeed, no output.

- [ ] **Step 3: Confirm lint is clean (repo uses golangci-lint)**

Run: `golangci-lint run ./internal/plugins/... --max-issues-per-linter 0 --max-same-issues 0`
Expected: no issues. (If the repo's `make lint` is the canonical entrypoint, run that instead and confirm `internal/plugins` is clean.)

- [ ] **Step 4: Commit any lint/vet fixups**

```bash
git add -u internal/plugins/ && git add internal/plugins/
git commit -m "plugins: lint/vet cleanup for internal/plugins"
```
(If Steps 1–3 needed no changes — the expected outcome — there is nothing to stage or commit; skip this step.)

---

## Self-Review

Checked against spec `2026-07-04-plugin-marketplaces-design.md`:

- §4 (manager package, `agent/plugin` stays consumer): Tasks 0–15 build `internal/plugins`; validation reuses `agent/plugin.Load` (Task 10). ✓
- §5 (store layout, registry shape, sha-keyed cache, single user scope): Tasks 1, 4, 12. `installed_plugins.json` array shape, `enabled`/`autoUpgrade` folded in. ✓
- §6 (four `url`-not-`git` source kinds, sparse clone, add/remove/list/refresh, browse): Tasks 5–8. ✓
- §7 (install/upgrade/remove/enable/disable/list/update-all; full-validate; directory-in-place; never-GC-on-upgrade): Tasks 12–15. ✓
- §12 (upgrade never deletes): Task 13 asserts the old dir remains. ✓ (The `gc` sweep verb itself is P4 — noted, not in P1.)
- §16 testing (real git fixtures, no network, `t.Skip` without git, broken-plugin detection): every git task skips without git; Task 15 tests broken detection. ✓

**Deferred to later phases (correctly out of P1):** `EnabledPluginDirs`/dedup (P2), first-run seeding (P2), the `serf plugin` CLI (P2), the `gc` sweep + auto-upgrade daemon (P4), slash commands (P3), web/TUI (P5/P6), doctor (P7). The parent spec's §15 assigns these; P1 is the manager core only.

**Known execution-ordering notes folded into tasks:** `Source` (Task 6) must exist before `registry.go` (Task 4) compiles; `ParseCatalog` (Task 8) before Task 7's test runs. Both are called out inline. An implementer doing strict numeric order should build 6 before 4 and 8 before 7 (or use the one-line stubs noted).

**Type consistency:** `Source`, `InstallEntry`, `Registry`, `MarketplaceRef`, `Catalog`/`CatalogPlugin`, `ListItem`, and the `Manager` method set are named identically across all tasks. `Manager.Install/Upgrade/Remove/SetEnabled/SetAutoUpgrade/List/UpdateAll/AddMarketplace/ListMarketplaces/RemoveMarketplace/RefreshMarketplace/Browse` is the complete public verb surface P2 will drive.
