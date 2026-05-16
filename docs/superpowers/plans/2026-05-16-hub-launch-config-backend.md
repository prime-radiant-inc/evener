# Hub Launch Config — Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the hub-side layered launch-config resolver, credentials store, RPC surface, and spawn integration so that hub-launched `serf serve` daemons pick up the merged config from disk and per-launch overrides. UI work is in separate plans.

**Architecture:** New `internal/launchconfig` package owns layer types, TOML I/O, merging, ToArgs/ToEnv, and TOFU. New `internal/credentials` package owns `~/.serf/credentials.toml`. The hub wires `serf/launch/*` and extends `serf/auth/*` to surface both via JSON-RPC. `cmd/serf-hub/spawn.go` is rewritten to consume a `Resolved` value instead of the current ad-hoc scalar list. No back-compat for the old `[serf_launch]` shape.

**Tech Stack:** Go 1.21+, `github.com/BurntSushi/toml`, existing `internal/appwire` JSON-RPC framework, existing `cmd/serf-hub/appserver.HandleTyped` registration pattern.

**Spec:** `docs/superpowers/specs/2026-05-16-hub-serf-launch-config-design.md`

---

## File Structure

**New packages**

- `internal/launchconfig/types.go` — `Layer`, `Resolved`, `RepoStatus`, `TrustState`, `Diagnostic`, `MCPServerSpec`, `Meta`
- `internal/launchconfig/io.go` — `LoadLayer`, `SaveLayer` (atomic), `LoadMeta`, `SaveMeta`
- `internal/launchconfig/merge.go` — `mergeLayers`, scalar/list/map rules, credential blocklist
- `internal/launchconfig/paths.go` — `ProjectID`, `ProjectPaths`, in-repo path validation
- `internal/launchconfig/trust.go` — `Hash` (canonical TOML), TOFU state computation
- `internal/launchconfig/resolver.go` — top-level `Resolve(cwd, overrides) (Resolved, error)`
- `internal/launchconfig/args.go` — `ToArgs(Resolved) []string`
- `internal/launchconfig/env.go` — `ToEnv(Resolved, credentials.Resolver) []string`
- `internal/launchconfig/launchconfig_test.go` and per-file `*_test.go`

- `internal/credentials/store.go` — `Store`, `LoadStore`, `Get`, `Set`, `Clear`, `List`
- `internal/credentials/types.go` — `Provider`, `Source` constants
- `internal/credentials/store_test.go`

**Modified files**

- `internal/appwire/types.go` — add `LaunchConfigLayer`, `LaunchConfigResolved`, `RepoLaunchConfigStatus`, `LaunchConfigDiagnostic`, `MCPServerSpec`, `AuthListResponse`, `AuthApiKeySetParams`; extend `AuthStatusResponse` with `AuthModes`; add `Method*` and `Notify*` constants
- `cmd/serf-hub/spawn.go` — `SpawnRequest`/`ResumeRequest` carry `Resolved`; `buildSpawnArgs` delegates to `launchconfig.ToArgs`; env via `launchconfig.ToEnv`; credential validation via `credentials.Resolver`
- `cmd/serf-hub/app_rpc.go` — register `serf/launch/*`, `serf/auth/list`, `serf/auth/apiKey/set`; thread `LaunchOverrides` through `ThreadStart`
- `cmd/serf-hub/app_auth.go` — `hubAuthController` gains `List`, `ApiKeySet`, gains `credentials.Store` dependency, `Status`/`Logout` become provider-generic
- `cmd/serf-hub/config.go` — drop `SerfLaunchConfig` and `Env` map; introduce `LaunchConfigGlobalPath` etc. accessors
- `cmd/serf-hub/main.go` — construct `credentials.Store` + thread into hub config
- `cmd/serf-hub/e2e_test.go` — exercise the resolved config end-to-end

---

## Task 1 — Set up `internal/launchconfig` package with the Layer type

**Files:**
- Create: `internal/launchconfig/types.go`
- Create: `internal/launchconfig/types_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/types_test.go`:

```go
package launchconfig

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLayerTOMLRoundTrip(t *testing.T) {
	input := `
schema = 1
model = "openai/gpt-5"
agent = "default"
reasoning_effort = "medium"
context_strategy = "compact"
max_rounds = 200
max_subagent_depth = 1
no_project_prompts = false
sse_ring_size = 4096
skills_dirs = ["/a", "/b"]
plugin_dirs = ["/p"]
mcp_configs = ["/c"]
system_prompt_append = ["/s"]

[[mcps]]
name = "github"
command = "gh-mcp"
args = ["--token-from-env", "GITHUB_TOKEN"]

[env]
FOO = "bar"
`
	var got Layer
	if _, err := toml.Decode(input, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Schema != 1 {
		t.Errorf("Schema = %d, want 1", got.Schema)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", got.Model)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 200 {
		t.Errorf("MaxRounds = %v, want 200", got.MaxRounds)
	}
	if got.NoProjectPrompts == nil || *got.NoProjectPrompts != false {
		t.Errorf("NoProjectPrompts = %v, want false set", got.NoProjectPrompts)
	}
	if len(got.SkillsDirs) != 2 || got.SkillsDirs[0] != "/a" {
		t.Errorf("SkillsDirs = %v, want [/a /b]", got.SkillsDirs)
	}
	if len(got.MCPs) != 1 || got.MCPs[0].Name != "github" {
		t.Errorf("MCPs = %v, want one github entry", got.MCPs)
	}
	if got.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want bar", got.Env["FOO"])
	}
}

func TestLayerOmitEmptyOnEncode(t *testing.T) {
	l := Layer{Model: "openai/gpt-5"}
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "max_rounds") {
		t.Errorf("encoded output should omit max_rounds when nil:\n%s", out)
	}
	if !strings.Contains(out, `model = "openai/gpt-5"`) {
		t.Errorf("encoded output missing model:\n%s", out)
	}
}
```

(add `"strings"` to imports too)

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/launchconfig/ -run TestLayer -v
```

Expected: package or type undefined.

- [ ] **Step 3: Write the types**

`internal/launchconfig/types.go`:

```go
// Package launchconfig owns the layered configuration that hub-serf
// passes when launching a serf serve subprocess. Layers (global, in-repo,
// hub-side-per-project, per-launch) are merged into a single Resolved
// value which is then turned into argv + env via ToArgs/ToEnv.
package launchconfig

import "time"

// Layer is one writable or in-memory layer of launch configuration. All
// scalar value fields are pointer-typed so the merge logic can
// distinguish "not set at this layer" from "explicitly zero."
type Layer struct {
	Schema             int               `toml:"schema,omitempty"`
	Model              string            `toml:"model,omitempty"`
	Agent              string            `toml:"agent,omitempty"`
	ReasoningEffort    string            `toml:"reasoning_effort,omitempty"`
	ContextStrategy    string            `toml:"context_strategy,omitempty"`
	MaxRounds          *int              `toml:"max_rounds,omitempty"`
	MaxSubagentDepth   *int              `toml:"max_subagent_depth,omitempty"`
	NoProjectPrompts   *bool             `toml:"no_project_prompts,omitempty"`
	SSERingSize        *int              `toml:"sse_ring_size,omitempty"`
	SkillsDirs         []string          `toml:"skills_dirs,omitempty"`
	PluginDirs         []string          `toml:"plugin_dirs,omitempty"`
	MCPConfigs         []string          `toml:"mcp_configs,omitempty"`
	SystemPromptAppend []string          `toml:"system_prompt_append,omitempty"`
	MCPs               []MCPServerSpec   `toml:"mcps,omitempty"`
	Env                map[string]string `toml:"env,omitempty"`
}

// MCPServerSpec describes one MCP server entry. Matches the shape passed
// to `serf serve --mcp name:command args...`.
type MCPServerSpec struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args,omitempty"`
}

// LayerName identifies which layer a value came from.
type LayerName string

const (
	LayerGlobal  LayerName = "global"
	LayerRepo    LayerName = "repo"
	LayerProject LayerName = "project"
	LayerLaunch  LayerName = "launch"
)

// Resolved is the output of merging every layer. Provenance maps each
// effective field name to the topmost contributing LayerName.
type Resolved struct {
	Effective   Layer
	Layers      map[LayerName]Layer
	Provenance  map[string]LayerName
	Repo        *RepoStatus
	Diagnostics []Diagnostic
}

// TrustState describes the in-repo .serf/launch.toml trust outcome.
type TrustState string

const (
	TrustAbsent    TrustState = "absent"
	TrustUntrusted TrustState = "untrusted"
	TrustTrusted   TrustState = "trusted"
	TrustChanged   TrustState = "changed"
	TrustRejected  TrustState = "rejected"
)

// RepoStatus describes the in-repo launch.toml that resolver found, if any.
type RepoStatus struct {
	Path    string
	Hash    string
	Trust   TrustState
	Preview string
}

// Diagnostic is a non-fatal note from the resolver. Surfaced on the wire.
type Diagnostic struct {
	Layer   LayerName
	Field   string
	Message string
}

// Meta is the contents of ~/.serf/projects/<id>/meta.toml.
type Meta struct {
	Schema    int       `toml:"schema"`
	CWD       string    `toml:"cwd"`
	CreatedAt time.Time `toml:"created_at"`
	Trust     MetaTrust `toml:"trust,omitempty"`
}

// MetaTrust records the TOFU decision for the in-repo file.
type MetaTrust struct {
	Hash       string    `toml:"hash,omitempty"`
	Decision   string    `toml:"decision,omitempty"`   // "trusted" | "rejected"
	DecidedAt  time.Time `toml:"decided_at,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/launchconfig/ -run TestLayer -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/types.go internal/launchconfig/types_test.go
git commit -m "launchconfig: add Layer and Resolved types"
```

---

## Task 2 — Layer file I/O with atomic writes

**Files:**
- Create: `internal/launchconfig/io.go`
- Create: `internal/launchconfig/io_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/io_test.go`:

```go
package launchconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLayer_Missing(t *testing.T) {
	got, err := LoadLayer(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("LoadLayer missing = %v, want nil", err)
	}
	if got.Schema != 0 || got.Model != "" {
		t.Errorf("LoadLayer missing returned non-zero Layer: %+v", got)
	}
}

func TestLoadLayer_Parses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := os.WriteFile(path, []byte("schema = 1\nmodel = \"openai/gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatalf("LoadLayer: %v", err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model = %q", got.Model)
	}
}

func TestSaveLayer_AtomicAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.toml")
	if err := SaveLayer(path, Layer{Schema: 1, Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("SaveLayer: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	// Temp file must not linger.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file still present")
	}
	// Round-trip.
	got, err := LoadLayer(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("round-trip Model = %q", got.Model)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/launchconfig/ -run TestLoadLayer -v
go test ./internal/launchconfig/ -run TestSaveLayer -v
```

Expected: undefined `LoadLayer` / `SaveLayer`.

- [ ] **Step 3: Implement `io.go`**

`internal/launchconfig/io.go`:

```go
package launchconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// LoadLayer reads a Layer from path. A missing file is not an error —
// it returns a zero Layer.
func LoadLayer(path string) (Layer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Layer{}, nil
		}
		return Layer{}, fmt.Errorf("launchconfig: read %s: %w", path, err)
	}
	var out Layer
	if _, err := toml.Decode(string(data), &out); err != nil {
		return Layer{}, fmt.Errorf("launchconfig: parse %s: %w", path, err)
	}
	return out, nil
}

// SaveLayer writes a Layer to path atomically: it writes to path.tmp,
// fsync's it, then renames over the target. Mode 0600.
func SaveLayer(path string, layer Layer) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("launchconfig: open %s: %w", tmp, err)
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(layer); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("launchconfig: encode %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("launchconfig: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("launchconfig: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("launchconfig: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// LoadMeta reads a Meta from path. Missing returns a zero value.
func LoadMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Meta{}, nil
		}
		return Meta{}, fmt.Errorf("launchconfig: read %s: %w", path, err)
	}
	var out Meta
	if _, err := toml.Decode(string(data), &out); err != nil {
		return Meta{}, fmt.Errorf("launchconfig: parse %s: %w", path, err)
	}
	return out, nil
}

// SaveMeta writes a Meta to path atomically.
func SaveMeta(path string, meta Meta) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("launchconfig: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("launchconfig: open %s: %w", tmp, err)
	}
	if err := toml.NewEncoder(f).Encode(meta); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("launchconfig: encode %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS (Task 1 + Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/io.go internal/launchconfig/io_test.go
git commit -m "launchconfig: atomic layer file I/O"
```

---

## Task 3 — Project IDs and on-disk paths

**Files:**
- Create: `internal/launchconfig/paths.go`
- Create: `internal/launchconfig/paths_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/paths_test.go`:

```go
package launchconfig

import (
	"path/filepath"
	"testing"
)

func TestProjectID_Stable(t *testing.T) {
	a := ProjectID("/home/jesse/git/prime-radiant/serf")
	b := ProjectID("/home/jesse/git/prime-radiant/serf")
	if a != b {
		t.Errorf("ProjectID not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("ProjectID length = %d, want 16", len(a))
	}
}

func TestProjectID_Differs(t *testing.T) {
	a := ProjectID("/a")
	b := ProjectID("/b")
	if a == b {
		t.Errorf("ProjectID collision for /a and /b: %q", a)
	}
}

func TestPathsFor(t *testing.T) {
	root := "/var/serf"
	cwd := "/proj"
	p := PathsFor(root, cwd)
	wantGlobal := filepath.Join(root, "launch.toml")
	wantProject := filepath.Join(root, "projects", ProjectID(cwd), "launch.toml")
	wantMeta := filepath.Join(root, "projects", ProjectID(cwd), "meta.toml")
	wantRepo := filepath.Join(cwd, ".serf", "launch.toml")
	if p.Global != wantGlobal {
		t.Errorf("Global = %q, want %q", p.Global, wantGlobal)
	}
	if p.Project != wantProject {
		t.Errorf("Project = %q, want %q", p.Project, wantProject)
	}
	if p.Meta != wantMeta {
		t.Errorf("Meta = %q, want %q", p.Meta, wantMeta)
	}
	if p.Repo != wantRepo {
		t.Errorf("Repo = %q, want %q", p.Repo, wantRepo)
	}
}

func TestValidateRepoPath(t *testing.T) {
	cases := []struct {
		repo string
		path string
		want bool
	}{
		{"/repo", "sub/skills", true},
		{"/repo", "./sub/skills", true},
		{"/repo", "../escape", false},
		{"/repo", "/absolute", false},
		{"/repo", "sub/../../escape", false},
	}
	for _, tc := range cases {
		err := ValidateRepoRelativePath(tc.repo, tc.path)
		got := err == nil
		if got != tc.want {
			t.Errorf("ValidateRepoRelativePath(%q, %q) = %v (err=%v), want %v", tc.repo, tc.path, got, err, tc.want)
		}
	}
}

func TestValidateAbsolutePath(t *testing.T) {
	if err := ValidateAbsolutePath("/abs/path"); err != nil {
		t.Errorf("/abs/path: %v", err)
	}
	if err := ValidateAbsolutePath("rel/path"); err == nil {
		t.Errorf("rel/path: want error")
	}
}
```

- [ ] **Step 2: Run test (fails — undefined)**

```bash
go test ./internal/launchconfig/ -run TestProjectID -v
```

- [ ] **Step 3: Implement `paths.go`**

`internal/launchconfig/paths.go`:

```go
package launchconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// ProjectID returns the 16-hex-char stable identifier used for the
// hub-side per-project state directory.
func ProjectID(cwd string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:])[:16]
}

// Paths bundles the canonical layer-file paths for a given hub state root
// and cwd.
type Paths struct {
	Global  string // <root>/launch.toml
	Repo    string // <cwd>/.serf/launch.toml
	Project string // <root>/projects/<id>/launch.toml
	Meta    string // <root>/projects/<id>/meta.toml
}

// PathsFor computes layer paths given the hub state root (typically
// ~/.serf) and the working directory.
func PathsFor(stateRoot, cwd string) Paths {
	id := ProjectID(cwd)
	projectDir := filepath.Join(stateRoot, "projects", id)
	return Paths{
		Global:  filepath.Join(stateRoot, "launch.toml"),
		Repo:    filepath.Join(cwd, ".serf", "launch.toml"),
		Project: filepath.Join(projectDir, "launch.toml"),
		Meta:    filepath.Join(projectDir, "meta.toml"),
	}
}

// ValidateRepoRelativePath ensures `path` (when resolved against repoRoot)
// stays inside repoRoot. Absolute paths and `..` escapes are rejected.
func ValidateRepoRelativePath(repoRoot, path string) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute path not allowed in repo layer: %q", path)
	}
	clean := filepath.Clean(filepath.Join(repoRoot, path))
	rel, err := filepath.Rel(repoRoot, clean)
	if err != nil {
		return fmt.Errorf("path resolution: %w", err)
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("path escapes repo: %q", path)
	}
	return nil
}

// ValidateAbsolutePath errors when path is not absolute.
func ValidateAbsolutePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute path required: %q", path)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/paths.go internal/launchconfig/paths_test.go
git commit -m "launchconfig: project IDs and path validation"
```

---

## Task 4 — Layer merging

**Files:**
- Create: `internal/launchconfig/merge.go`
- Create: `internal/launchconfig/merge_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/merge_test.go`:

```go
package launchconfig

import (
	"reflect"
	"testing"
)

func ptrInt(v int) *int       { return &v }
func ptrBool(v bool) *bool    { return &v }

func TestMerge_ScalarPrecedence(t *testing.T) {
	g := Layer{Model: "g-model", ReasoningEffort: "low"}
	r := Layer{Model: "r-model"}
	p := Layer{}
	l := Layer{Model: "l-model"}
	got, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal: g, LayerRepo: r, LayerProject: p, LayerLaunch: l,
	})
	if got.Effective.Model != "l-model" {
		t.Errorf("Model = %q, want l-model (per-launch wins)", got.Effective.Model)
	}
	if got.Effective.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q, want low (only global set)", got.Effective.ReasoningEffort)
	}
	if got.Provenance["model"] != LayerLaunch {
		t.Errorf("Provenance[model] = %q, want launch", got.Provenance["model"])
	}
	if got.Provenance["reasoning_effort"] != LayerGlobal {
		t.Errorf("Provenance[reasoning_effort] = %q, want global", got.Provenance["reasoning_effort"])
	}
}

func TestMerge_ScalarPointerSemantics(t *testing.T) {
	g := Layer{MaxRounds: ptrInt(200)}
	l := Layer{}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerLaunch: l})
	if got.Effective.MaxRounds == nil || *got.Effective.MaxRounds != 200 {
		t.Errorf("MaxRounds = %v, want 200 (launch did not override)", got.Effective.MaxRounds)
	}
}

func TestMerge_ListAppendInLayerOrder(t *testing.T) {
	g := Layer{SkillsDirs: []string{"/g1", "/g2"}}
	r := Layer{SkillsDirs: []string{"/r1"}}
	p := Layer{SkillsDirs: []string{"/p1"}}
	l := Layer{SkillsDirs: []string{"/l1"}}
	got, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal: g, LayerRepo: r, LayerProject: p, LayerLaunch: l,
	})
	want := []string{"/g1", "/g2", "/r1", "/p1", "/l1"}
	if !reflect.DeepEqual(got.Effective.SkillsDirs, want) {
		t.Errorf("SkillsDirs = %v, want %v", got.Effective.SkillsDirs, want)
	}
}

func TestMerge_EnvMapLastWriteWins(t *testing.T) {
	g := Layer{Env: map[string]string{"A": "g", "B": "g"}}
	p := Layer{Env: map[string]string{"A": "p"}}
	got, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerProject: p})
	if got.Effective.Env["A"] != "p" {
		t.Errorf("Env[A] = %q, want p", got.Effective.Env["A"])
	}
	if got.Effective.Env["B"] != "g" {
		t.Errorf("Env[B] = %q, want g", got.Effective.Env["B"])
	}
}

func TestMerge_MCPsAppendWithDuplicateDiagnostic(t *testing.T) {
	g := Layer{MCPs: []MCPServerSpec{{Name: "x", Command: "x1"}}}
	p := Layer{MCPs: []MCPServerSpec{{Name: "x", Command: "x2"}}}
	got, diags := mergeLayers(map[LayerName]Layer{LayerGlobal: g, LayerProject: p})
	if len(got.Effective.MCPs) != 2 {
		t.Errorf("len(MCPs) = %d, want 2 (append, no dedup)", len(got.Effective.MCPs))
	}
	var seen bool
	for _, d := range diags {
		if d.Field == "mcps" && d.Layer == LayerProject {
			seen = true
		}
	}
	if !seen {
		t.Errorf("expected diagnostic for duplicate mcp name, got %v", diags)
	}
}

func TestMerge_BlockedCredentialEnvKeys(t *testing.T) {
	g := Layer{Env: map[string]string{"OPENAI_API_KEY": "leak"}}
	_, diags := mergeLayers(map[LayerName]Layer{LayerGlobal: g})
	if len(diags) == 0 || diags[0].Field != "env.OPENAI_API_KEY" {
		t.Errorf("expected blocklist diagnostic, got %v", diags)
	}
}
```

- [ ] **Step 2: Run test (fails)**

```bash
go test ./internal/launchconfig/ -run TestMerge -v
```

- [ ] **Step 3: Implement `merge.go`**

`internal/launchconfig/merge.go`:

```go
package launchconfig

import (
	"fmt"
	"strings"
)

// layerOrder is the application order (least → most specific).
var layerOrder = []LayerName{LayerGlobal, LayerRepo, LayerProject, LayerLaunch}

// credentialBlocklistSuffixes are the substring patterns refused inside
// env maps at every layer. Credentials must flow through the credentials
// store, never through a launch layer that might get committed.
var credentialBlocklistSuffixes = []string{
	"API_KEY", "_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL",
}

func isCredentialEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, s := range credentialBlocklistSuffixes {
		if strings.Contains(upper, s) {
			return true
		}
	}
	return false
}

// mergeLayers composes the supplied layers in canonical order. Layers not
// present in the map are treated as empty. Returns the resolved effective
// view plus any non-fatal diagnostics found while merging (duplicate MCP
// names, blocked credential keys, etc.).
func mergeLayers(layers map[LayerName]Layer) (Resolved, []Diagnostic) {
	var diags []Diagnostic
	eff := Layer{Env: map[string]string{}}
	prov := map[string]LayerName{}
	contributing := map[LayerName]Layer{}

	mcpNames := map[string]LayerName{}

	for _, name := range layerOrder {
		l, ok := layers[name]
		if !ok {
			continue
		}
		nonEmpty := false

		if l.Model != "" {
			eff.Model = l.Model
			prov["model"] = name
			nonEmpty = true
		}
		if l.Agent != "" {
			eff.Agent = l.Agent
			prov["agent"] = name
			nonEmpty = true
		}
		if l.ReasoningEffort != "" {
			eff.ReasoningEffort = l.ReasoningEffort
			prov["reasoning_effort"] = name
			nonEmpty = true
		}
		if l.ContextStrategy != "" {
			eff.ContextStrategy = l.ContextStrategy
			prov["context_strategy"] = name
			nonEmpty = true
		}
		if l.MaxRounds != nil {
			v := *l.MaxRounds
			eff.MaxRounds = &v
			prov["max_rounds"] = name
			nonEmpty = true
		}
		if l.MaxSubagentDepth != nil {
			v := *l.MaxSubagentDepth
			eff.MaxSubagentDepth = &v
			prov["max_subagent_depth"] = name
			nonEmpty = true
		}
		if l.NoProjectPrompts != nil {
			v := *l.NoProjectPrompts
			eff.NoProjectPrompts = &v
			prov["no_project_prompts"] = name
			nonEmpty = true
		}
		if l.SSERingSize != nil {
			if name != LayerGlobal {
				diags = append(diags, Diagnostic{
					Layer: name, Field: "sse_ring_size",
					Message: "sse_ring_size is only honored at the global layer",
				})
			} else {
				v := *l.SSERingSize
				eff.SSERingSize = &v
				prov["sse_ring_size"] = name
				nonEmpty = true
			}
		}

		// Lists: append in layer order.
		if len(l.SkillsDirs) > 0 {
			eff.SkillsDirs = append(eff.SkillsDirs, l.SkillsDirs...)
			prov["skills_dirs"] = name
			nonEmpty = true
		}
		if len(l.PluginDirs) > 0 {
			eff.PluginDirs = append(eff.PluginDirs, l.PluginDirs...)
			prov["plugin_dirs"] = name
			nonEmpty = true
		}
		if len(l.MCPConfigs) > 0 {
			eff.MCPConfigs = append(eff.MCPConfigs, l.MCPConfigs...)
			prov["mcp_configs"] = name
			nonEmpty = true
		}
		if len(l.SystemPromptAppend) > 0 {
			eff.SystemPromptAppend = append(eff.SystemPromptAppend, l.SystemPromptAppend...)
			prov["system_prompt_append"] = name
			nonEmpty = true
		}
		if len(l.MCPs) > 0 {
			for _, m := range l.MCPs {
				if prev, ok := mcpNames[m.Name]; ok {
					diags = append(diags, Diagnostic{
						Layer: name, Field: "mcps",
						Message: fmt.Sprintf("duplicate mcp name %q (previously seen at layer %q); serf launch-check will reject this", m.Name, prev),
					})
				} else {
					mcpNames[m.Name] = name
				}
				eff.MCPs = append(eff.MCPs, m)
			}
			prov["mcps"] = name
			nonEmpty = true
		}
		// Env map: last-write-wins per key, with credential blocklist.
		for k, v := range l.Env {
			if isCredentialEnvKey(k) {
				diags = append(diags, Diagnostic{
					Layer: name, Field: "env." + k,
					Message: fmt.Sprintf("env key %q looks like a credential; route through credentials store", k),
				})
				continue
			}
			eff.Env[k] = v
		}
		if len(l.Env) > 0 {
			prov["env"] = name
			nonEmpty = true
		}
		if nonEmpty {
			contributing[name] = l
		}
	}

	return Resolved{
		Effective:   eff,
		Layers:      contributing,
		Provenance:  prov,
		Diagnostics: diags,
	}, diags
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/merge.go internal/launchconfig/merge_test.go
git commit -m "launchconfig: layer merge with diagnostics"
```

---

## Task 5 — Trust hashing and meta state machine

**Files:**
- Create: `internal/launchconfig/trust.go`
- Create: `internal/launchconfig/trust_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/trust_test.go`:

```go
package launchconfig

import (
	"testing"
)

func TestCanonicalHash_StableAcrossWhitespace(t *testing.T) {
	a := `model = "x"
skills_dirs = ["/a"]
`
	b := `

model = "x"


skills_dirs = ["/a"]
`
	ha, err := CanonicalHashTOML([]byte(a))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalHashTOML([]byte(b))
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("hash should be whitespace-stable: %q vs %q", ha, hb)
	}
}

func TestCanonicalHash_DetectsSemanticChange(t *testing.T) {
	a := `model = "x"`
	b := `model = "y"`
	ha, _ := CanonicalHashTOML([]byte(a))
	hb, _ := CanonicalHashTOML([]byte(b))
	if ha == hb {
		t.Errorf("hashes should differ for different content")
	}
}

func TestComputeTrustState(t *testing.T) {
	// File absent.
	if got := ComputeTrustState("", Meta{}); got != TrustAbsent {
		t.Errorf("absent: got %q, want %q", got, TrustAbsent)
	}
	// File present, no recorded decision → untrusted.
	if got := ComputeTrustState("hash-1", Meta{}); got != TrustUntrusted {
		t.Errorf("untrusted: got %q, want %q", got, TrustUntrusted)
	}
	// File present, hash matches trusted record.
	meta := Meta{Trust: MetaTrust{Hash: "hash-1", Decision: "trusted"}}
	if got := ComputeTrustState("hash-1", meta); got != TrustTrusted {
		t.Errorf("trusted: got %q, want %q", got, TrustTrusted)
	}
	// File present, hash differs → changed.
	if got := ComputeTrustState("hash-2", meta); got != TrustChanged {
		t.Errorf("changed: got %q, want %q", got, TrustChanged)
	}
	// File present, explicitly rejected.
	rejected := Meta{Trust: MetaTrust{Hash: "hash-1", Decision: "rejected"}}
	if got := ComputeTrustState("hash-1", rejected); got != TrustRejected {
		t.Errorf("rejected: got %q, want %q", got, TrustRejected)
	}
}
```

- [ ] **Step 2: Run test (fails)**

```bash
go test ./internal/launchconfig/ -run TestCanonical -v
go test ./internal/launchconfig/ -run TestComputeTrust -v
```

- [ ] **Step 3: Implement `trust.go`**

`internal/launchconfig/trust.go`:

```go
package launchconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/BurntSushi/toml"
)

// CanonicalHashTOML returns sha256-hex of the canonicalized TOML — parsed
// into a Layer, re-encoded with sorted keys via the toml library — so
// whitespace edits and key reorderings produce a stable hash but semantic
// changes break it.
func CanonicalHashTOML(data []byte) (string, error) {
	var l Layer
	if _, err := toml.Decode(string(data), &l); err != nil {
		return "", fmt.Errorf("canonical hash: parse: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		return "", fmt.Errorf("canonical hash: encode: %w", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ComputeTrustState evaluates the current TrustState from the on-disk
// hash and the recorded Meta.
//
//   - hash == ""  → file is absent
//   - hash != "", Meta.Trust.Hash == ""      → untrusted (first contact)
//   - hash != "", Meta.Trust.Decision rejected and matches → rejected
//   - hash != "", Meta.Trust.Decision trusted and matches  → trusted
//   - hash != "", Meta.Trust.Hash differs                  → changed
func ComputeTrustState(hash string, meta Meta) TrustState {
	if hash == "" {
		return TrustAbsent
	}
	if meta.Trust.Hash == "" {
		return TrustUntrusted
	}
	if meta.Trust.Hash != hash {
		return TrustChanged
	}
	switch meta.Trust.Decision {
	case "trusted":
		return TrustTrusted
	case "rejected":
		return TrustRejected
	default:
		return TrustUntrusted
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/trust.go internal/launchconfig/trust_test.go
git commit -m "launchconfig: TOFU hashing and trust state"
```

---

## Task 6 — Top-level `Resolve` function

**Files:**
- Create: `internal/launchconfig/resolver.go`
- Create: `internal/launchconfig/resolver_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/resolver_test.go`:

```go
package launchconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_LayersMerge(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(stateRoot, "launch.toml"), `model = "g"
skills_dirs = ["/g"]
`)
	// Trusted in-repo file.
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `skills_dirs = ["sub"]`)
	repoHash, _ := CanonicalHashTOML([]byte(`skills_dirs = ["sub"]`))
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "meta.toml"), `schema = 1
cwd = "`+cwd+`"
[trust]
hash = "`+repoHash+`"
decision = "trusted"
`)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "launch.toml"), `skills_dirs = ["/p"]`)

	overrides := Layer{Model: "l", SkillsDirs: []string{"/l"}}
	got, err := Resolve(stateRoot, cwd, overrides)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "l" {
		t.Errorf("Model = %q, want l (per-launch)", got.Effective.Model)
	}
	repoExpanded := filepath.Join(cwd, "sub")
	want := []string{"/g", repoExpanded, "/p", "/l"}
	if len(got.Effective.SkillsDirs) != 4 || got.Effective.SkillsDirs[1] != repoExpanded {
		t.Errorf("SkillsDirs = %v, want %v", got.Effective.SkillsDirs, want)
	}
	if got.Repo == nil || got.Repo.Trust != TrustTrusted {
		t.Errorf("repo trust = %v, want trusted", got.Repo)
	}
}

func TestResolve_UntrustedRepoSkipped(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `model = "from-repo"`)
	got, err := Resolve(stateRoot, cwd, Layer{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "" {
		t.Errorf("untrusted repo contributed Model = %q", got.Effective.Model)
	}
	if got.Repo == nil || got.Repo.Trust != TrustUntrusted {
		t.Errorf("repo state = %v, want untrusted", got.Repo)
	}
	if got.Repo.Preview == "" {
		t.Errorf("untrusted repo preview should be non-empty")
	}
}

func TestResolve_RejectedRepoSkippedSilently(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `model = "from-repo"`)
	hash, _ := CanonicalHashTOML([]byte(`model = "from-repo"`))
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "meta.toml"), `schema = 1
cwd = "`+cwd+`"
[trust]
hash = "`+hash+`"
decision = "rejected"
`)
	got, _ := Resolve(stateRoot, cwd, Layer{})
	if got.Repo == nil || got.Repo.Trust != TrustRejected {
		t.Errorf("repo state = %v, want rejected", got.Repo)
	}
	if got.Effective.Model != "" {
		t.Errorf("rejected repo contributed config: %v", got.Effective)
	}
}

func TestResolve_RepoPathsExpandedAndValidated(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".serf", "launch.toml"), `skills_dirs = ["../outside", "good/dir"]`)
	// Pre-trust whatever the file currently is.
	hash, _ := CanonicalHashTOML([]byte(`skills_dirs = ["../outside", "good/dir"]`))
	pid := ProjectID(cwd)
	writeFile(t, filepath.Join(stateRoot, "projects", pid, "meta.toml"), `schema = 1
cwd = "`+cwd+`"
[trust]
hash = "`+hash+`"
decision = "trusted"
`)
	got, _ := Resolve(stateRoot, cwd, Layer{})
	// "../outside" rejected; "good/dir" kept and expanded to absolute.
	if len(got.Effective.SkillsDirs) != 1 {
		t.Errorf("SkillsDirs = %v, want 1 entry (the escape rejected)", got.Effective.SkillsDirs)
	}
	hasDiag := false
	for _, d := range got.Diagnostics {
		if d.Layer == LayerRepo && d.Field == "skills_dirs" {
			hasDiag = true
		}
	}
	if !hasDiag {
		t.Errorf("expected diagnostic about ../outside, got %v", got.Diagnostics)
	}
}
```

- [ ] **Step 2: Run test (fails — undefined Resolve)**

```bash
go test ./internal/launchconfig/ -run TestResolve -v
```

- [ ] **Step 3: Implement `resolver.go`**

`internal/launchconfig/resolver.go`:

```go
package launchconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Resolve loads and merges every layer for the given cwd, applying the
// per-launch override on top. stateRoot is typically ~/.serf. The repo
// layer is honored only when its trust state is "trusted".
func Resolve(stateRoot, cwd string, overrides Layer) (Resolved, error) {
	paths := PathsFor(stateRoot, cwd)
	layers := map[LayerName]Layer{}

	g, err := LoadLayer(paths.Global)
	if err != nil {
		return Resolved{}, fmt.Errorf("global: %w", err)
	}
	g = validateAbsolutePaths(LayerGlobal, g, nil)
	layers[LayerGlobal] = g

	// In-repo: load + hash + trust check.
	repoStatus, repoLayer, repoDiags := loadRepoLayer(cwd, stateRoot)
	if repoStatus != nil && repoStatus.Trust == TrustTrusted {
		layers[LayerRepo] = repoLayer
	}

	p, err := LoadLayer(paths.Project)
	if err != nil {
		return Resolved{}, fmt.Errorf("project: %w", err)
	}
	p = validateAbsolutePaths(LayerProject, p, nil)
	layers[LayerProject] = p

	layers[LayerLaunch] = overrides

	resolved, _ := mergeLayers(layers)
	resolved.Repo = repoStatus
	resolved.Diagnostics = append(resolved.Diagnostics, repoDiags...)
	return resolved, nil
}

func loadRepoLayer(cwd, stateRoot string) (*RepoStatus, Layer, []Diagnostic) {
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	data, err := os.ReadFile(repoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RepoStatus{Path: repoPath, Trust: TrustAbsent}, Layer{}, nil
		}
		return &RepoStatus{Path: repoPath, Trust: TrustAbsent}, Layer{}, []Diagnostic{{
			Layer: LayerRepo, Field: ".serf/launch.toml",
			Message: fmt.Sprintf("read: %v", err),
		}}
	}
	hash, err := CanonicalHashTOML(data)
	if err != nil {
		return &RepoStatus{Path: repoPath, Trust: TrustUntrusted, Preview: string(data)}, Layer{}, []Diagnostic{{
			Layer: LayerRepo, Field: ".serf/launch.toml",
			Message: fmt.Sprintf("hash: %v", err),
		}}
	}
	meta, _ := LoadMeta(filepath.Join(stateRoot, "projects", ProjectID(cwd), "meta.toml"))
	state := ComputeTrustState(hash, meta)

	status := &RepoStatus{Path: repoPath, Hash: hash, Trust: state}
	if state != TrustTrusted {
		status.Preview = string(data)
	}

	var layer Layer
	var diags []Diagnostic
	if state == TrustTrusted {
		if _, err := decodeLayerInto(data, &layer); err != nil {
			diags = append(diags, Diagnostic{Layer: LayerRepo, Field: ".serf/launch.toml", Message: err.Error()})
			return status, Layer{}, diags
		}
		layer, diags = validateAndExpandRepoLayer(cwd, layer)
	}
	return status, layer, diags
}

func decodeLayerInto(data []byte, out *Layer) (Layer, error) {
	if _, err := tomlDecode(data, out); err != nil {
		return Layer{}, err
	}
	return *out, nil
}

// validateAndExpandRepoLayer rejects path entries that escape the repo
// root and expands every remaining path to an absolute path anchored on
// repoRoot. Returns the cleaned layer plus diagnostics for rejected
// entries.
func validateAndExpandRepoLayer(repoRoot string, in Layer) (Layer, []Diagnostic) {
	var diags []Diagnostic
	expand := func(field string, vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if err := ValidateRepoRelativePath(repoRoot, v); err != nil {
				diags = append(diags, Diagnostic{Layer: LayerRepo, Field: field, Message: err.Error()})
				continue
			}
			out = append(out, filepath.Clean(filepath.Join(repoRoot, v)))
		}
		return out
	}
	in.SkillsDirs = expand("skills_dirs", in.SkillsDirs)
	in.PluginDirs = expand("plugin_dirs", in.PluginDirs)
	in.MCPConfigs = expand("mcp_configs", in.MCPConfigs)
	in.SystemPromptAppend = expand("system_prompt_append", in.SystemPromptAppend)
	return in, diags
}

// validateAbsolutePaths rejects relative paths at the global/project
// layers, dropping rejected entries with a diagnostic. Provided as a
// closure to allow shared logic for both layers.
func validateAbsolutePaths(layer LayerName, in Layer, diags *[]Diagnostic) Layer {
	check := func(field string, vals []string) []string {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if err := ValidateAbsolutePath(v); err != nil {
				if diags != nil {
					*diags = append(*diags, Diagnostic{Layer: layer, Field: field, Message: err.Error()})
				}
				continue
			}
			out = append(out, v)
		}
		return out
	}
	in.SkillsDirs = check("skills_dirs", in.SkillsDirs)
	in.PluginDirs = check("plugin_dirs", in.PluginDirs)
	in.MCPConfigs = check("mcp_configs", in.MCPConfigs)
	in.SystemPromptAppend = check("system_prompt_append", in.SystemPromptAppend)
	return in
}
```

Also add the `tomlDecode` helper. Append to `internal/launchconfig/io.go`:

```go
// tomlDecode is the inverse of SaveLayer's encoder; exposed for use by
// callers (the resolver) that have already read raw bytes.
func tomlDecode(data []byte, out interface{}) (toml.MetaData, error) {
	return toml.Decode(string(data), out)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/resolver.go internal/launchconfig/resolver_test.go internal/launchconfig/io.go
git commit -m "launchconfig: layered Resolve with trust + path validation"
```

---

## Task 7 — `ToArgs` (Resolved → argv)

**Files:**
- Create: `internal/launchconfig/args.go`
- Create: `internal/launchconfig/args_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/args_test.go`:

```go
package launchconfig

import (
	"reflect"
	"strings"
	"testing"
)

func TestToArgs_AllFields(t *testing.T) {
	r := Resolved{Effective: Layer{
		Model:              "openai/gpt-5",
		Agent:              "default",
		ReasoningEffort:    "medium",
		ContextStrategy:    "compact",
		MaxRounds:          ptrInt(200),
		MaxSubagentDepth:   ptrInt(2),
		NoProjectPrompts:   ptrBool(true),
		SSERingSize:        ptrInt(4096),
		SkillsDirs:         []string{"/s1", "/s2"},
		PluginDirs:         []string{"/p"},
		MCPConfigs:         []string{"/m.json"},
		SystemPromptAppend: []string{"/sp"},
		MCPs: []MCPServerSpec{
			{Name: "github", Command: "gh-mcp", Args: []string{"--token-from-env", "GITHUB_TOKEN"}},
		},
	}}
	got := ToArgs(r)
	want := []string{
		"--model", "openai/gpt-5",
		"--agent", "default",
		"--reasoning-effort", "medium",
		"--context-strategy", "compact",
		"--max-rounds", "200",
		"--max-subagent-depth", "2",
		"--no-project-prompts",
		"--sse-ring-size", "4096",
		"--skills-dir", "/s1",
		"--skills-dir", "/s2",
		"--plugin-dir", "/p",
		"--mcp-config", "/m.json",
		"--system-prompt-append", "/sp",
		"--mcp", "github:gh-mcp --token-from-env GITHUB_TOKEN",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToArgs =\n%s\nwant\n%s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestToArgs_SkipsUnset(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{Model: "openai/gpt-5"}})
	want := []string{"--model", "openai/gpt-5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToArgs = %v, want %v", got, want)
	}
}

func TestToArgs_BoolFalseDoesNotEmitFlag(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{NoProjectPrompts: ptrBool(false)}})
	for _, a := range got {
		if a == "--no-project-prompts" {
			t.Errorf("ToArgs should not emit --no-project-prompts when value is false; got %v", got)
		}
	}
}
```

- [ ] **Step 2: Run test (fails)**

```bash
go test ./internal/launchconfig/ -run TestToArgs -v
```

- [ ] **Step 3: Implement `args.go`**

`internal/launchconfig/args.go`:

```go
package launchconfig

import (
	"fmt"
	"strings"
)

// ToArgs renders the Effective layer of Resolved into the argv slice
// `serf serve` understands. Order is deterministic and matches the order
// serf's flag parser sees them: scalars first, then list fields in the
// order they appear in the Layer struct.
func ToArgs(r Resolved) []string {
	var out []string
	add := func(flag, value string) {
		out = append(out, flag, value)
	}
	e := r.Effective
	if e.Model != "" {
		add("--model", e.Model)
	}
	if e.Agent != "" {
		add("--agent", e.Agent)
	}
	if e.ReasoningEffort != "" {
		add("--reasoning-effort", e.ReasoningEffort)
	}
	if e.ContextStrategy != "" {
		add("--context-strategy", e.ContextStrategy)
	}
	if e.MaxRounds != nil {
		add("--max-rounds", fmt.Sprintf("%d", *e.MaxRounds))
	}
	if e.MaxSubagentDepth != nil {
		add("--max-subagent-depth", fmt.Sprintf("%d", *e.MaxSubagentDepth))
	}
	if e.NoProjectPrompts != nil && *e.NoProjectPrompts {
		out = append(out, "--no-project-prompts")
	}
	if e.SSERingSize != nil {
		add("--sse-ring-size", fmt.Sprintf("%d", *e.SSERingSize))
	}
	for _, d := range e.SkillsDirs {
		add("--skills-dir", d)
	}
	for _, d := range e.PluginDirs {
		add("--plugin-dir", d)
	}
	for _, d := range e.MCPConfigs {
		add("--mcp-config", d)
	}
	for _, d := range e.SystemPromptAppend {
		add("--system-prompt-append", d)
	}
	for _, m := range e.MCPs {
		spec := m.Name + ":" + m.Command
		if len(m.Args) > 0 {
			spec += " " + strings.Join(m.Args, " ")
		}
		add("--mcp", spec)
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/args.go internal/launchconfig/args_test.go
git commit -m "launchconfig: ToArgs renders Resolved into serf serve flags"
```

---

## Task 8 — `internal/credentials` package

**Files:**
- Create: `internal/credentials/store.go`
- Create: `internal/credentials/store_test.go`

- [ ] **Step 1: Write the failing test**

`internal/credentials/store_test.go`:

```go
package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LoadMissingFile(t *testing.T) {
	s, err := LoadStore(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("LoadStore missing: %v", err)
	}
	if v, _ := s.Get("anthropic"); v != "" {
		t.Errorf("Get on empty store returned %q", v)
	}
}

func TestStore_SetGetClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, _ := LoadStore(path)
	if err := s.Set("anthropic", "sk-ant-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, src := s.Get("anthropic")
	if v != "sk-ant-1" {
		t.Errorf("Get value = %q, want sk-ant-1", v)
	}
	if src != SourceFile {
		t.Errorf("Get source = %q, want file", src)
	}
	// Reload from disk; persistence works.
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore reload: %v", err)
	}
	if v, _ := s2.Get("anthropic"); v != "sk-ant-1" {
		t.Errorf("reloaded value = %q", v)
	}
	if err := s2.Clear("anthropic"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if v, _ := s2.Get("anthropic"); v != "" {
		t.Errorf("after Clear, value = %q", v)
	}
}

func TestStore_PermissionsEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(path); err == nil {
		t.Errorf("LoadStore should reject 0644-mode file")
	}
}

func TestStore_GetFallsBackToEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	s, _ := LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	v, src := s.Get("anthropic")
	if v != "env-key" || src != SourceEnv {
		t.Errorf("env fallback: v=%q src=%q", v, src)
	}
}

func TestStore_List(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("GEMINI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, _ := LoadStore(path)
	_ = s.Set("openrouter", "or-key")
	list := s.List()
	bySource := map[string]Source{}
	for _, p := range list {
		bySource[p.Name] = p.Source
	}
	if bySource["anthropic"] != SourceEnv {
		t.Errorf("anthropic source = %q", bySource["anthropic"])
	}
	if bySource["openrouter"] != SourceFile {
		t.Errorf("openrouter source = %q", bySource["openrouter"])
	}
	if _, ok := bySource["ollama"]; !ok {
		t.Errorf("ollama (no creds needed) should be in List")
	}
}
```

- [ ] **Step 2: Run test (fails)**

```bash
go test ./internal/credentials/ -v
```

- [ ] **Step 3: Implement the package**

`internal/credentials/store.go`:

```go
// Package credentials owns ~/.serf/credentials.toml. Provider API keys
// are stored verbatim with chmod 600; encryption-at-rest is deliberately
// not provided (see spec §5.5 non-goals).
package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Source describes where a provider's effective value came from.
type Source string

const (
	SourceFile   Source = "file"
	SourceEnv    Source = "env"
	SourceOAuth  Source = "oauth"
	SourceAbsent Source = "absent"
	SourceNone   Source = "none"
)

// Provider is one row in List().
type Provider struct {
	Name      string
	AuthModes []string
	Source    Source
}

// providerEnvVars maps provider name -> env var(s) checked for fallback.
// Order matters: first non-empty wins.
var providerEnvVars = map[string][]string{
	"openai":               {"OPENAI_API_KEY"},
	"anthropic":            {"ANTHROPIC_API_KEY"},
	"google":               {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	"gemini":               {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	"minimax":              {"MINIMAX_API_KEY"},
	"openrouter":           {"OPENROUTER_API_KEY"},
	"openrouter-anthropic": {"OPENROUTER_API_KEY"},
	"kimi":                 {"KIMI_API_KEY"},
	"glm":                  {"GLM_API_KEY"},
	"openai-compatible":    {"OPENAI_COMPATIBLE_BASE_URL"},
	"ollama":               nil,
}

// providerAuthModes lists supported auth flows per provider.
var providerAuthModes = map[string][]string{
	"openai":               {"apiKey", "oauth"},
	"anthropic":            {"apiKey"},
	"google":               {"apiKey"},
	"gemini":               {"apiKey"},
	"minimax":              {"apiKey"},
	"openrouter":           {"apiKey"},
	"openrouter-anthropic": {"apiKey"},
	"kimi":                 {"apiKey"},
	"glm":                  {"apiKey"},
	"openai-compatible":    {"apiKey"},
	"ollama":               {"none"},
}

type fileShape struct {
	Schema    int                        `toml:"schema"`
	Providers map[string]providerSection `toml:"providers"`
}

type providerSection struct {
	APIKey string `toml:"api_key,omitempty"`
}

// Store is the in-memory + on-disk credentials.toml.
type Store struct {
	path string
	data fileShape
}

// LoadStore reads path. Missing returns an empty Store. Non-missing files
// must have mode 0600 (group/world bits unset).
func LoadStore(path string) (*Store, error) {
	s := &Store{path: path, data: fileShape{Schema: 1, Providers: map[string]providerSection{}}}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("credentials: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credentials: %s has mode %o; require 0600", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("credentials: read %s: %w", path, err)
	}
	if _, err := toml.Decode(string(raw), &s.data); err != nil {
		return nil, fmt.Errorf("credentials: parse %s: %w", path, err)
	}
	if s.data.Providers == nil {
		s.data.Providers = map[string]providerSection{}
	}
	return s, nil
}

// Get returns the effective API key for provider and its Source.
// Lookup order: file → env → empty.
func (s *Store) Get(provider string) (string, Source) {
	provider = strings.ToLower(provider)
	if p, ok := s.data.Providers[provider]; ok && strings.TrimSpace(p.APIKey) != "" {
		return p.APIKey, SourceFile
	}
	for _, env := range providerEnvVars[provider] {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, SourceEnv
		}
	}
	return "", SourceAbsent
}

// Set writes a provider API key into the in-memory store and persists.
func (s *Store) Set(provider, value string) error {
	provider = strings.ToLower(provider)
	if s.data.Providers == nil {
		s.data.Providers = map[string]providerSection{}
	}
	s.data.Providers[provider] = providerSection{APIKey: strings.TrimSpace(value)}
	return s.save()
}

// Clear removes the provider entry. No error if absent.
func (s *Store) Clear(provider string) error {
	provider = strings.ToLower(provider)
	delete(s.data.Providers, provider)
	return s.save()
}

// List returns one Provider entry per supported provider.
func (s *Store) List() []Provider {
	out := []Provider{}
	for name, modes := range providerAuthModes {
		_, src := s.Get(name)
		// Ollama needs no creds — report SourceNone.
		if len(modes) == 1 && modes[0] == "none" {
			src = SourceNone
		}
		out = append(out, Provider{Name: name, AuthModes: modes, Source: src})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("credentials: mkdir: %w", err)
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("credentials: open: %w", err)
	}
	if err := toml.NewEncoder(f).Encode(s.data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("credentials: encode: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/credentials/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/credentials/
git commit -m "credentials: hub-managed credentials.toml store"
```

---

## Task 9 — `ToEnv` (Resolved + credentials → child env)

**Files:**
- Create: `internal/launchconfig/env.go`
- Create: `internal/launchconfig/env_test.go`

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/env_test.go`:

```go
package launchconfig

import (
	"testing"
)

type stubCreds struct {
	keys map[string]string
}

func (s stubCreds) APIKeyFor(provider string) (string, string) {
	v, ok := s.keys[provider]
	if !ok {
		return "", "absent"
	}
	return v, "file"
}

func TestToEnv_BaselineSetsRunStateAndProvider(t *testing.T) {
	parent := []string{"PATH=/usr/bin"}
	r := Resolved{Effective: Layer{Model: "anthropic/claude-1", Env: map[string]string{"FOO": "bar"}}}
	creds := stubCreds{keys: map[string]string{"anthropic": "sk-ant-FROM-FILE"}}
	got := ToEnv(EnvInputs{
		Resolved:  r,
		Provider:  "anthropic",
		Creds:     creds,
		ParentEnv: parent,
		RunDir:    "/run",
		StateDir:  "/state",
		HubToken:  "tok",
	})
	want := map[string]string{
		"PATH":              "/usr/bin",
		"FOO":               "bar",
		"SERF_HUB_SPAWNED":  "1",
		"SERF_RUN_DIR":      "/run",
		"SERF_STATE_DIR":    "/state",
		"SERF_HUB_TOKEN":    "tok",
		"ANTHROPIC_API_KEY": "sk-ant-FROM-FILE",
	}
	gotMap := envSliceToMap(got)
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, gotMap[k], v)
		}
	}
}

func TestToEnv_PerLaunchEnvBeatsCredsStore(t *testing.T) {
	r := Resolved{Effective: Layer{Env: map[string]string{"ANTHROPIC_API_KEY": "from-overrides"}}}
	creds := stubCreds{keys: map[string]string{"anthropic": "from-file"}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved: r, Provider: "anthropic", Creds: creds,
	}))
	if got["ANTHROPIC_API_KEY"] != "from-overrides" {
		t.Errorf("per-launch env should win: %q", got["ANTHROPIC_API_KEY"])
	}
}

func TestToEnv_NoProviderNoInjection(t *testing.T) {
	creds := stubCreds{keys: map[string]string{"anthropic": "x"}}
	got := envSliceToMap(ToEnv(EnvInputs{Provider: "", Creds: creds}))
	if _, ok := got["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("no provider, no injection; got %v", got)
	}
}

func envSliceToMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		i := 0
		for ; i < len(kv); i++ {
			if kv[i] == '=' {
				break
			}
		}
		if i >= len(kv) {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}
```

- [ ] **Step 2: Run test (fails)**

```bash
go test ./internal/launchconfig/ -run TestToEnv -v
```

- [ ] **Step 3: Implement `env.go`**

`internal/launchconfig/env.go`:

```go
package launchconfig

import (
	"sort"
	"strings"
)

// CredentialResolver is the slice of internal/credentials.Store that
// ToEnv depends on. Decoupled to a small interface so tests don't need
// to construct a real store.
type CredentialResolver interface {
	// APIKeyFor returns the API key value plus the source label
	// ("file", "env", "oauth", "absent"). Empty value means absent.
	APIKeyFor(provider string) (string, string)
}

// EnvInputs bundles everything ToEnv needs.
type EnvInputs struct {
	Resolved  Resolved
	Provider  string
	Creds     CredentialResolver
	ParentEnv []string // typically os.Environ()
	RunDir    string
	StateDir  string
	HubToken  string
}

// providerEnvVar maps provider name → the canonical env var that serf
// reads for that provider.
var providerEnvVar = map[string]string{
	"openai":               "OPENAI_API_KEY",
	"anthropic":            "ANTHROPIC_API_KEY",
	"google":               "GEMINI_API_KEY",
	"gemini":               "GEMINI_API_KEY",
	"minimax":              "MINIMAX_API_KEY",
	"openrouter":           "OPENROUTER_API_KEY",
	"openrouter-anthropic": "OPENROUTER_API_KEY",
	"kimi":                 "KIMI_API_KEY",
	"glm":                  "GLM_API_KEY",
	"openai-compatible":    "OPENAI_COMPATIBLE_BASE_URL",
}

// ToEnv produces the env slice for the spawned `serf serve`. Order of
// precedence per the spec §4.5:
//   1. Per-launch env from Resolved.Effective.Env (last-write-wins).
//   2. The matching credential env var (from Creds).
//   3. Parent process env (typically os.Environ()).
//   4. Provider-specific on-disk OAuth state — handled by serf itself.
//
// Items earlier in the priority list are applied later in setEnv so they
// overwrite earlier writes.
func ToEnv(in EnvInputs) []string {
	out := append([]string{}, in.ParentEnv...)
	out = setEnv(out, "SERF_HUB_SPAWNED", "1")
	if in.RunDir != "" {
		out = setEnv(out, "SERF_RUN_DIR", in.RunDir)
	}
	if in.StateDir != "" {
		out = setEnv(out, "SERF_STATE_DIR", in.StateDir)
	}
	if in.HubToken != "" {
		out = setEnv(out, "SERF_HUB_TOKEN", in.HubToken)
	}

	// 2. Credentials store value.
	if envKey, ok := providerEnvVar[strings.ToLower(in.Provider)]; ok && in.Creds != nil {
		if v, _ := in.Creds.APIKeyFor(strings.ToLower(in.Provider)); v != "" {
			out = setEnv(out, envKey, v)
		}
	}

	// 1. Per-launch env: applied last so it wins, in sorted key order
	//    for determinism.
	keys := make([]string, 0, len(in.Resolved.Effective.Env))
	for k := range in.Resolved.Effective.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = setEnv(out, k, in.Resolved.Effective.Env[k])
	}
	return out
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/env.go internal/launchconfig/env_test.go
git commit -m "launchconfig: ToEnv with priority-ordered credential injection"
```

---

## Task 10 — Appwire wire types

**Files:**
- Modify: `internal/appwire/types.go`
- Modify: `internal/appwire/types_test.go` (if exists) or add a new test file

- [ ] **Step 1: Find the existing method constants block**

Run:
```bash
grep -n "MethodSerfAuthLogout\s*=\s*\"serf/auth/logout\"" internal/appwire/types.go
```

Expected output: a single line like `30:	MethodSerfAuthLogout            = "serf/auth/logout"`.

- [ ] **Step 2: Add the new method and notification constants**

Open `internal/appwire/types.go`. After the `MethodSerfAuthLogout` line, before the next group, add:

```go
	MethodSerfAuthList              = "serf/auth/list"
	MethodSerfAuthApiKeySet         = "serf/auth/apiKey/set"
	MethodSerfLaunchResolve         = "serf/launch/resolve"
	MethodSerfLaunchGetLayer        = "serf/launch/getLayer"
	MethodSerfLaunchSetLayer        = "serf/launch/setLayer"
	MethodSerfLaunchTrustRepo       = "serf/launch/trustRepo"
```

Then after `NotifySerfSubagentEnded` in the notifications block, add:

```go
	NotifySerfAuthUpdated   = "serf/auth/updated"
	NotifySerfLaunchUpdated = "serf/launch/updated"
```

- [ ] **Step 3: Extend `AuthStatusResponse` with `AuthModes`**

In the same file find the existing block (~line 443) and add `AuthModes` after `ActiveSource`:

```go
type AuthStatusResponse struct {
	Provider       string   `json:"provider"`
	Supported      bool     `json:"supported"`
	SignedIn       bool     `json:"signedIn"`
	ActiveSource   string   `json:"activeSource"`
	AuthModes      []string `json:"authModes,omitempty"`
	HasStoredOAuth bool     `json:"hasStoredOAuth"`
	Email          string   `json:"email,omitempty"`
	StoredEmail    string   `json:"storedEmail,omitempty"`
	AccountID      string   `json:"accountId,omitempty"`
	WorkspaceID    string   `json:"workspaceId,omitempty"`
	NeedsRefresh   bool     `json:"needsRefresh,omitempty"`
	NeedsLogin     bool     `json:"needsLogin,omitempty"`
	Error          string   `json:"error,omitempty"`
}
```

- [ ] **Step 4: Add the new types**

At the end of the file:

```go
// AuthListResponse is the result of serf/auth/list.
type AuthListResponse struct {
	Providers []AuthStatusResponse `json:"providers"`
}

// AuthApiKeySetParams is the params for serf/auth/apiKey/set.
type AuthApiKeySetParams struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// LaunchConfigLayer is the wire-level partial layer (every field optional;
// pointer-typed scalars so "not set" is distinguishable from zero).
type LaunchConfigLayer struct {
	Schema             *int              `json:"schema,omitempty"`
	Model              string            `json:"model,omitempty"`
	Agent              string            `json:"agent,omitempty"`
	ReasoningEffort    string            `json:"reasoningEffort,omitempty"`
	ContextStrategy    string            `json:"contextStrategy,omitempty"`
	MaxRounds          *int              `json:"maxRounds,omitempty"`
	MaxSubagentDepth   *int              `json:"maxSubagentDepth,omitempty"`
	NoProjectPrompts   *bool             `json:"noProjectPrompts,omitempty"`
	SSERingSize        *int              `json:"sseRingSize,omitempty"`
	SkillsDirs         []string          `json:"skillsDirs,omitempty"`
	PluginDirs         []string          `json:"pluginDirs,omitempty"`
	MCPConfigs         []string          `json:"mcpConfigs,omitempty"`
	SystemPromptAppend []string          `json:"systemPromptAppend,omitempty"`
	MCPs               []MCPServerSpec   `json:"mcps,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
}

// MCPServerSpec mirrors launchconfig.MCPServerSpec on the wire.
type MCPServerSpec struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// LaunchConfigResolved is the wire representation of launchconfig.Resolved.
type LaunchConfigResolved struct {
	Effective   LaunchConfigLayer            `json:"effective"`
	Layers      map[string]LaunchConfigLayer `json:"layers"`
	Provenance  map[string]string            `json:"provenance"`
	Repo        *RepoLaunchConfigStatus      `json:"repo,omitempty"`
	Diagnostics []LaunchConfigDiagnostic     `json:"diagnostics,omitempty"`
}

type RepoLaunchConfigStatus struct {
	Path    string `json:"path"`
	Hash    string `json:"hash,omitempty"`
	Trust   string `json:"trust"`
	Preview string `json:"preview,omitempty"`
}

type LaunchConfigDiagnostic struct {
	Layer   string `json:"layer"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type LaunchConfigResolveParams struct {
	CWD             string             `json:"cwd"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

type LaunchConfigGetLayerParams struct {
	CWD   string `json:"cwd"`
	Layer string `json:"layer"` // "global" | "project"
}

type LaunchConfigSetLayerParams struct {
	CWD    string            `json:"cwd"`
	Layer  string            `json:"layer"`
	Config LaunchConfigLayer `json:"config"`
}

type LaunchConfigTrustRepoParams struct {
	CWD  string `json:"cwd"`
	Hash string `json:"hash"`
}
```

- [ ] **Step 5: Add `LaunchOverrides` to `ThreadStartParams`**

Find the existing `ThreadStartParams` struct (around line 326). Add the field at the bottom:

```go
type ThreadStartParams struct {
	Harness         string             `json:"harness,omitempty"`
	CWD             string             `json:"cwd"`
	Prompt          string             `json:"prompt,omitempty"`
	Items           []InputItem        `json:"items,omitempty"`
	ModelProvider   string             `json:"modelProvider,omitempty"`
	Model           string             `json:"model,omitempty"`
	Profile         string             `json:"profile,omitempty"`
	ReasoningEffort string             `json:"reasoningEffort,omitempty"`
	LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}
```

- [ ] **Step 6: Verify the package still compiles**

```bash
go build ./internal/appwire/...
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/appwire/types.go
git commit -m "appwire: launch-config types and serf/auth extensions"
```

---

## Task 11 — Adapter glue: launchconfig ↔ appwire

**Files:**
- Create: `internal/launchconfig/wire.go`
- Create: `internal/launchconfig/wire_test.go`

The Hub will receive wire payloads as `appwire.LaunchConfigLayer` and need to feed them into `launchconfig.Resolve` (which takes `launchconfig.Layer`). This task provides the conversion in one place.

- [ ] **Step 1: Write the failing test**

`internal/launchconfig/wire_test.go`:

```go
package launchconfig

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestFromWire(t *testing.T) {
	in := appwire.LaunchConfigLayer{
		Model:    "openai/gpt-5",
		Schema:   ptrInt(1),
		MCPs:     []appwire.MCPServerSpec{{Name: "x", Command: "y", Args: []string{"z"}}},
		MaxRounds: ptrInt(50),
	}
	got := FromWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.Schema != 1 {
		t.Errorf("Schema = %d, want 1", got.Schema)
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v, want 50", got.MaxRounds)
	}
	if len(got.MCPs) != 1 || got.MCPs[0].Name != "x" {
		t.Errorf("MCPs = %v", got.MCPs)
	}
}

func TestToWire(t *testing.T) {
	in := Layer{Model: "openai/gpt-5", MaxRounds: ptrInt(50)}
	got := ToWire(in)
	if got.Model != "openai/gpt-5" {
		t.Errorf("Model")
	}
	if got.MaxRounds == nil || *got.MaxRounds != 50 {
		t.Errorf("MaxRounds = %v", got.MaxRounds)
	}
}

func TestResolvedToWire(t *testing.T) {
	r := Resolved{
		Effective:  Layer{Model: "m"},
		Layers:     map[LayerName]Layer{LayerGlobal: {Model: "m"}},
		Provenance: map[string]LayerName{"model": LayerGlobal},
		Repo:       &RepoStatus{Path: "/p", Trust: TrustTrusted, Hash: "sha256:abc"},
	}
	got := ResolvedToWire(r)
	if got.Effective.Model != "m" {
		t.Errorf("Effective.Model")
	}
	if got.Layers["global"].Model != "m" {
		t.Errorf("Layers[global]")
	}
	if got.Provenance["model"] != "global" {
		t.Errorf("Provenance[model] = %q", got.Provenance["model"])
	}
	if got.Repo == nil || got.Repo.Trust != "trusted" {
		t.Errorf("Repo = %v", got.Repo)
	}
	_ = reflect.TypeOf(got)
}
```

- [ ] **Step 2: Run test (fails)**

```bash
go test ./internal/launchconfig/ -run TestFromWire -v
```

- [ ] **Step 3: Implement `wire.go`**

`internal/launchconfig/wire.go`:

```go
package launchconfig

import "primeradiant.com/serf/internal/appwire"

// FromWire converts an appwire.LaunchConfigLayer to the internal Layer.
func FromWire(in appwire.LaunchConfigLayer) Layer {
	out := Layer{
		Model:              in.Model,
		Agent:              in.Agent,
		ReasoningEffort:    in.ReasoningEffort,
		ContextStrategy:    in.ContextStrategy,
		MaxRounds:          copyIntPtr(in.MaxRounds),
		MaxSubagentDepth:   copyIntPtr(in.MaxSubagentDepth),
		NoProjectPrompts:   copyBoolPtr(in.NoProjectPrompts),
		SSERingSize:        copyIntPtr(in.SSERingSize),
		SkillsDirs:         in.SkillsDirs,
		PluginDirs:         in.PluginDirs,
		MCPConfigs:         in.MCPConfigs,
		SystemPromptAppend: in.SystemPromptAppend,
		Env:                in.Env,
	}
	if in.Schema != nil {
		out.Schema = *in.Schema
	}
	for _, m := range in.MCPs {
		out.MCPs = append(out.MCPs, MCPServerSpec{Name: m.Name, Command: m.Command, Args: m.Args})
	}
	return out
}

// ToWire converts an internal Layer to the appwire shape.
func ToWire(in Layer) appwire.LaunchConfigLayer {
	out := appwire.LaunchConfigLayer{
		Model:              in.Model,
		Agent:              in.Agent,
		ReasoningEffort:    in.ReasoningEffort,
		ContextStrategy:    in.ContextStrategy,
		MaxRounds:          copyIntPtr(in.MaxRounds),
		MaxSubagentDepth:   copyIntPtr(in.MaxSubagentDepth),
		NoProjectPrompts:   copyBoolPtr(in.NoProjectPrompts),
		SSERingSize:        copyIntPtr(in.SSERingSize),
		SkillsDirs:         in.SkillsDirs,
		PluginDirs:         in.PluginDirs,
		MCPConfigs:         in.MCPConfigs,
		SystemPromptAppend: in.SystemPromptAppend,
		Env:                in.Env,
	}
	if in.Schema != 0 {
		s := in.Schema
		out.Schema = &s
	}
	for _, m := range in.MCPs {
		out.MCPs = append(out.MCPs, appwire.MCPServerSpec{Name: m.Name, Command: m.Command, Args: m.Args})
	}
	return out
}

// ResolvedToWire converts an internal Resolved to the appwire shape.
func ResolvedToWire(r Resolved) appwire.LaunchConfigResolved {
	out := appwire.LaunchConfigResolved{
		Effective:  ToWire(r.Effective),
		Layers:     map[string]appwire.LaunchConfigLayer{},
		Provenance: map[string]string{},
	}
	for name, l := range r.Layers {
		out.Layers[string(name)] = ToWire(l)
	}
	for field, name := range r.Provenance {
		out.Provenance[field] = string(name)
	}
	if r.Repo != nil {
		out.Repo = &appwire.RepoLaunchConfigStatus{
			Path:    r.Repo.Path,
			Hash:    r.Repo.Hash,
			Trust:   string(r.Repo.Trust),
			Preview: r.Repo.Preview,
		}
	}
	for _, d := range r.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, appwire.LaunchConfigDiagnostic{
			Layer: string(d.Layer), Field: d.Field, Message: d.Message,
		})
	}
	return out
}

func copyIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func copyBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/launchconfig/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launchconfig/wire.go internal/launchconfig/wire_test.go
git commit -m "launchconfig: wire-type adapters"
```

---

## Task 12 — Hub-side `serf/launch/*` RPC handlers

**Files:**
- Create: `cmd/serf-hub/app_launch.go`
- Create: `cmd/serf-hub/app_launch_test.go`

- [ ] **Step 1: Write the failing tests**

`cmd/serf-hub/app_launch_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/launchconfig"
)

func TestLaunchController_Resolve_Empty(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(stateRoot)
	got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "" {
		t.Errorf("empty resolved should have no model: %v", got.Effective)
	}
	if got.Repo == nil || got.Repo.Trust != string(launchconfig.TrustAbsent) {
		t.Errorf("repo = %v, want absent", got.Repo)
	}
}

func TestLaunchController_SetLayer_GlobalRoundtrip(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(stateRoot)
	model := "openai/gpt-5"
	_, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD: cwd, Layer: "global",
		Config: appwire.LaunchConfigLayer{Model: model},
	})
	if err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	got, err := c.GetLayer(context.Background(), appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "global"})
	if err != nil {
		t.Fatalf("GetLayer: %v", err)
	}
	if got.Model != model {
		t.Errorf("Got = %q, want %q", got.Model, model)
	}
}

func TestLaunchController_TrustRepo_RecordsDecision(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := []byte(`model = "from-repo"`)
	if err := os.WriteFile(repoPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, _ := launchconfig.CanonicalHashTOML(contents)

	c := newHubLaunchController(stateRoot)
	got, err := c.TrustRepo(context.Background(), appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash})
	if err != nil {
		t.Fatalf("TrustRepo: %v", err)
	}
	if got.Repo == nil || got.Repo.Trust != string(launchconfig.TrustTrusted) {
		t.Errorf("trust after TrustRepo = %v", got.Repo)
	}
	if got.Effective.Model != "from-repo" {
		t.Errorf("trusted in-repo did not contribute: %v", got.Effective)
	}
}

func TestLaunchController_TrustRepo_HashMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoPath, []byte(`model = "x"`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newHubLaunchController(stateRoot)
	if _, err := c.TrustRepo(context.Background(), appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: "sha256:nope"}); err == nil {
		t.Errorf("TrustRepo with wrong hash should error")
	}
}
```

- [ ] **Step 2: Run (fails)**

```bash
go test ./cmd/serf-hub/ -run TestLaunchController -v
```

- [ ] **Step 3: Implement the controller**

`cmd/serf-hub/app_launch.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/launchconfig"
)

// hubLaunchController owns the serf/launch/* RPC handlers.
type hubLaunchController struct {
	stateRoot string
	now       func() time.Time
}

func newHubLaunchController(stateRoot string) *hubLaunchController {
	return &hubLaunchController{stateRoot: stateRoot, now: time.Now}
}

func (c *hubLaunchController) Resolve(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	resolved, err := launchconfig.Resolve(c.stateRoot, cwd, overrides)
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	return launchconfig.ResolvedToWire(resolved), nil
}

func (c *hubLaunchController) GetLayer(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigLayer{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	paths := launchconfig.PathsFor(c.stateRoot, cwd)
	var path string
	switch params.Layer {
	case "global":
		path = paths.Global
	case "project":
		path = paths.Project
	default:
		return appwire.LaunchConfigLayer{}, appwire.InvalidParams(fmt.Sprintf("layer %q is not writable", params.Layer))
	}
	layer, err := launchconfig.LoadLayer(path)
	if err != nil {
		return appwire.LaunchConfigLayer{}, err
	}
	return launchconfig.ToWire(layer), nil
}

func (c *hubLaunchController) SetLayer(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	paths := launchconfig.PathsFor(c.stateRoot, cwd)
	var path string
	switch params.Layer {
	case "global":
		path = paths.Global
	case "project":
		path = paths.Project
	default:
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams(fmt.Sprintf("layer %q is not writable", params.Layer))
	}
	layer := launchconfig.FromWire(params.Config)
	// Refuse credential keys in env before persisting.
	for k := range layer.Env {
		if launchconfig.IsCredentialEnvKey(k) {
			return appwire.LaunchConfigResolved{}, appwire.InvalidParams(fmt.Sprintf("env key %q looks like a credential; route through serf/auth/apiKey/set", k))
		}
	}
	if err := launchconfig.SaveLayer(path, layer); err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	resolved, err := launchconfig.Resolve(c.stateRoot, cwd, launchconfig.Layer{})
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	return launchconfig.ResolvedToWire(resolved), nil
}

func (c *hubLaunchController) TrustRepo(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	resolved, err := launchconfig.Resolve(c.stateRoot, cwd, launchconfig.Layer{})
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	if resolved.Repo == nil || resolved.Repo.Trust == string(launchconfig.TrustAbsent) {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("no .serf/launch.toml in repo")
	}
	if resolved.Repo.Hash != params.Hash {
		return appwire.LaunchConfigResolved{}, appwire.WireError{Code: -32009, Message: "file changed since review"}
	}
	paths := launchconfig.PathsFor(c.stateRoot, cwd)
	meta, _ := launchconfig.LoadMeta(paths.Meta)
	if meta.Schema == 0 {
		meta = launchconfig.Meta{Schema: 1, CWD: cwd, CreatedAt: c.now()}
	}
	meta.Trust = launchconfig.MetaTrust{Hash: params.Hash, Decision: "trusted", DecidedAt: c.now()}
	if err := launchconfig.SaveMeta(paths.Meta, meta); err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	resolved, err = launchconfig.Resolve(c.stateRoot, cwd, launchconfig.Layer{})
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	return launchconfig.ResolvedToWire(resolved), nil
}
```

Then export `IsCredentialEnvKey` by adding to `internal/launchconfig/merge.go`:

```go
// IsCredentialEnvKey is the exported version of the internal blocklist
// check, used by hub RPC handlers to refuse credential keys at write time.
func IsCredentialEnvKey(key string) bool { return isCredentialEnvKey(key) }
```

- [ ] **Step 4: Run tests**

```bash
go test ./cmd/serf-hub/ -run TestLaunchController -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_launch.go cmd/serf-hub/app_launch_test.go internal/launchconfig/merge.go
git commit -m "serf-hub: launch-config RPC handlers"
```

---

## Task 13 — Extend `serf/auth/*` with list + apiKey/set

**Files:**
- Modify: `cmd/serf-hub/app_auth.go`
- Modify: `cmd/serf-hub/app_auth_test.go`

- [ ] **Step 1: Inspect the existing controller signature**

```bash
grep -n "newHubAuthController\|hubAuthController struct" cmd/serf-hub/app_auth.go
```

You'll see `newHubAuthController(launchEnv ...map[string]string)`. We'll add a `creds *credentials.Store` dependency.

- [ ] **Step 2: Write the failing tests**

Append to `cmd/serf-hub/app_auth_test.go`:

```go
import (
	"primeradiant.com/serf/internal/credentials"
)

func TestAuth_List_IncludesAllProviders(t *testing.T) {
	stateDir := t.TempDir()
	credsPath := filepath.Join(stateDir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(stateDir, store)
	got, err := c.List(appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, p := range got.Providers {
		names[p.Provider] = true
	}
	for _, want := range []string{"openai", "anthropic", "ollama"} {
		if !names[want] {
			t.Errorf("List missing %q; got %v", want, names)
		}
	}
}

func TestAuth_ApiKeySet_WritesAndReports(t *testing.T) {
	stateDir := t.TempDir()
	credsPath := filepath.Join(stateDir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(stateDir, store)
	got, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-ant-XXX"})
	if err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	if got.ActiveSource != string(credentials.SourceFile) {
		t.Errorf("ActiveSource = %q, want file", got.ActiveSource)
	}
	// Reload from disk; value should persist.
	store2, _ := credentials.LoadStore(credsPath)
	v, src := store2.Get("anthropic")
	if v != "sk-ant-XXX" || src != credentials.SourceFile {
		t.Errorf("after ApiKeySet: v=%q src=%q", v, src)
	}
}

func TestAuth_Status_AnthropicViaStore(t *testing.T) {
	stateDir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(stateDir, "credentials.toml"))
	_ = store.Set("anthropic", "key")
	c := newHubAuthControllerWithStore(stateDir, store)
	got, err := c.Status(appwire.AuthStatusParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.SignedIn || got.ActiveSource != string(credentials.SourceFile) {
		t.Errorf("Status anthropic = %+v", got)
	}
	if len(got.AuthModes) == 0 {
		t.Errorf("AuthModes empty: %+v", got)
	}
}
```

(`filepath` import probably already there)

- [ ] **Step 3: Run tests (fail)**

```bash
go test ./cmd/serf-hub/ -run "TestAuth_List|TestAuth_ApiKeySet|TestAuth_Status_Anthropic" -v
```

- [ ] **Step 4: Extend the controller**

In `cmd/serf-hub/app_auth.go`, replace the existing `hubAuthController` struct and constructor:

```go
type hubAuthController struct {
	stateDir     string
	authEnv      map[string]string
	cfg          authopenai.Config
	client       *http.Client
	now          func() time.Time
	exchangeCode func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error)
	creds        *credentials.Store

	mu    sync.Mutex
	flows map[string]hubAuthFlow
}

func newHubAuthControllerWithStore(stateDir string, store *credentials.Store, launchEnv ...map[string]string) *hubAuthController {
	cfg := authopenai.DefaultConfig()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	authEnv := effectiveHubAuthEnv(nil)
	if len(launchEnv) > 0 {
		authEnv = effectiveHubAuthEnv(launchEnv[0])
	}
	return &hubAuthController{
		stateDir:     openAIStateDirFromEnv(authEnv),
		authEnv:      authEnv,
		cfg:          cfg,
		client:       client,
		now:          time.Now,
		exchangeCode: authopenai.ExchangeCode,
		creds:        store,
		flows:        map[string]hubAuthFlow{},
	}
}

// Kept as a wrapper so existing callers using the no-store constructor
// keep compiling. main.go will switch to the With-Store form.
func newHubAuthController(launchEnv ...map[string]string) *hubAuthController {
	emptyStore, _ := credentials.LoadStore("")
	return newHubAuthControllerWithStore("", emptyStore, launchEnv...)
}
```

Add imports:
```go
import (
	"primeradiant.com/serf/internal/credentials"
)
```

Now replace `Status` to be generic:

```go
func (c *hubAuthController) Status(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider == "openai" {
		resp, err := c.openAIStatus()
		if err != nil {
			return resp, err
		}
		resp.AuthModes = []string{"apiKey", "oauth"}
		return resp, nil
	}
	modes := credentialAuthModes(provider)
	if len(modes) == 0 {
		return appwire.AuthStatusResponse{Provider: provider, Supported: false, ActiveSource: string(credentials.SourceAbsent)}, nil
	}
	v, src := c.creds.Get(provider)
	return appwire.AuthStatusResponse{
		Provider:     provider,
		Supported:    true,
		SignedIn:     v != "",
		ActiveSource: string(src),
		AuthModes:    modes,
	}, nil
}

func credentialAuthModes(provider string) []string {
	known := map[string][]string{
		"anthropic":            {"apiKey"},
		"google":               {"apiKey"},
		"gemini":               {"apiKey"},
		"minimax":              {"apiKey"},
		"openrouter":           {"apiKey"},
		"openrouter-anthropic": {"apiKey"},
		"kimi":                 {"apiKey"},
		"glm":                  {"apiKey"},
		"openai-compatible":    {"apiKey"},
		"ollama":               {"none"},
	}
	return known[provider]
}
```

Add the new methods:

```go
func (c *hubAuthController) List(_ appwire.EmptyParams) (appwire.AuthListResponse, error) {
	out := appwire.AuthListResponse{}
	// OpenAI gets its own rich shape.
	openaiResp, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err == nil {
		out.Providers = append(out.Providers, openaiResp)
	}
	for _, p := range c.creds.List() {
		if p.Name == "openai" {
			continue
		}
		out.Providers = append(out.Providers, appwire.AuthStatusResponse{
			Provider:     p.Name,
			Supported:    true,
			SignedIn:     p.Source == credentials.SourceFile || p.Source == credentials.SourceEnv,
			ActiveSource: string(p.Source),
			AuthModes:    p.AuthModes,
		})
	}
	return out, nil
}

func (c *hubAuthController) ApiKeySet(params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider == "openai" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("openai api keys must be configured via env or hub.env; use serf/auth/login/start for OAuth")
	}
	if strings.TrimSpace(params.Value) == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	if err := c.creds.Set(provider, params.Value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: provider})
}
```

Update `Logout` to delegate to the store for non-OpenAI providers:

```go
func (c *hubAuthController) Logout(params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider == "openai" {
		// existing OAuth-file delete path; leave the body as-is.
		// (no change needed here)
	}
	if provider != "openai" {
		if err := c.creds.Clear(provider); err != nil {
			return appwire.AuthLogoutResponse{}, err
		}
		status, _ := c.Status(appwire.AuthStatusParams{Provider: provider})
		return appwire.AuthLogoutResponse{Removed: true, Status: status}, nil
	}
	// fall through to existing OpenAI logout below
	// ... existing code stays ...
}
```

(Find the existing `Logout` body, and prepend the non-openai branch before the existing OpenAI logic; do not remove the OpenAI logic.)

Add `EmptyParams` to `internal/appwire/types.go` if it doesn't already exist:

```go
// EmptyParams is the typed-empty params shape used by methods that take none.
type EmptyParams struct{}
```

Run `grep -n EmptyParams internal/appwire/types.go` first; only add if missing.

- [ ] **Step 5: Run tests**

```bash
go test ./cmd/serf-hub/ -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/app_auth.go cmd/serf-hub/app_auth_test.go internal/appwire/types.go
git commit -m "serf-hub: auth controller manages credentials store"
```

---

## Task 14 — Register new RPC handlers + emit notifications

**Files:**
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/main.go`

- [ ] **Step 1: Find the dispatcher registration block**

```bash
grep -n "MethodSerfAuthLogout\|MethodSerfAuthStatus" cmd/serf-hub/app_rpc.go
```

You'll see four `appserver.HandleTyped(...)` calls registering the existing serf/auth methods.

- [ ] **Step 2: Add new handler registrations**

Immediately after `appwire.MethodSerfAuthLogout` registration, insert:

```go
appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthList, func(_ context.Context, params appwire.EmptyParams) (appwire.AuthListResponse, error) {
	return authController.List(params)
})
appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthApiKeySet, func(_ context.Context, params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	resp, err := authController.ApiKeySet(params)
	if err == nil {
		notifyAuthUpdated(server, resp.Provider, resp.ActiveSource)
	}
	return resp, err
})
```

Adjust the existing `MethodSerfAuthLogout` registration to also emit:
```go
appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthLogout, func(_ context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	resp, err := authController.Logout(params)
	if err == nil {
		notifyAuthUpdated(server, params.Provider, resp.Status.ActiveSource)
	}
	return resp, err
})
```

Similarly for `MethodSerfAuthLoginComplete`:
```go
appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthLoginComplete, func(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
	resp, err := authController.LoginComplete(ctx, params)
	if err == nil {
		notifyAuthUpdated(server, params.Provider, resp.Status.ActiveSource)
	}
	return resp, err
})
```

Add the launchController registrations (immediately after the auth block):

```go
launchController := newHubLaunchController(cfg.HubStateRoot)
appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchResolve, func(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
	return launchController.Resolve(ctx, params)
})
appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchGetLayer, func(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
	return launchController.GetLayer(ctx, params)
})
appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchSetLayer, func(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
	resp, err := launchController.SetLayer(ctx, params)
	if err == nil {
		notifyLaunchUpdated(server, params.CWD, params.Layer)
	}
	return resp, err
})
appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchTrustRepo, func(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
	resp, err := launchController.TrustRepo(ctx, params)
	if err == nil {
		notifyLaunchUpdated(server, params.CWD, "repo")
	}
	return resp, err
})
```

- [ ] **Step 3: Implement the notify helpers**

At the bottom of `cmd/serf-hub/app_rpc.go`:

```go
func notifyAuthUpdated(server appServerLike, provider, activeSource string) {
	server.Broadcast(appwire.NotifySerfAuthUpdated, map[string]string{
		"provider":     provider,
		"activeSource": activeSource,
	})
}

func notifyLaunchUpdated(server appServerLike, cwd, layer string) {
	server.Broadcast(appwire.NotifySerfLaunchUpdated, map[string]string{
		"cwd":   cwd,
		"layer": layer,
	})
}
```

`appServerLike` interface must include `Broadcast(method string, params any)` — add this near the existing dispatcher type if not present:
```bash
grep -n "type appServerLike\|interface.*Broadcast" cmd/serf-hub/app_rpc.go
```
If missing, add (or extend) the interface to surface `Broadcast`. If the existing server type already has Broadcast on it, no new interface needed — call `server.Broadcast(...)` directly with `server`'s concrete type.

- [ ] **Step 4: Wire the credentials Store into main.go**

```bash
grep -n "newHubAuthController" cmd/serf-hub/main.go cmd/serf-hub/app_rpc.go
```

At the construction site, switch to `newHubAuthControllerWithStore`:

```go
credsPath := filepath.Join(cfg.HubStateRoot, "credentials.toml")
credsStore, err := credentials.LoadStore(credsPath)
if err != nil {
	return fmt.Errorf("credentials store: %w", err)
}
authController := newHubAuthControllerWithStore(cfg.HubStateRoot, credsStore)
```

(`cfg.HubStateRoot` is a new field — add it to `Config` in `cmd/serf-hub/config.go` defaulting to `$HOME/.serf`.)

- [ ] **Step 5: Run tests**

```bash
go test ./cmd/serf-hub/ -v
```

Expected: PASS. Plus full build:
```bash
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/app_rpc.go cmd/serf-hub/main.go cmd/serf-hub/config.go
git commit -m "serf-hub: register launch + auth RPCs and notifications"
```

---

## Task 15 — Rewrite `spawn.go` to consume `launchconfig.Resolved`

**Files:**
- Modify: `cmd/serf-hub/spawn.go`
- Modify: `cmd/serf-hub/spawn_test.go`

- [ ] **Step 1: Inspect existing flow**

```bash
grep -n "buildSpawnArgs\|buildSerfChildEnv\|validateProviderCredentials" cmd/serf-hub/spawn.go
```

- [ ] **Step 2: Write the failing test**

Append to `cmd/serf-hub/spawn_test.go`:

```go
func TestBuildSpawnArgs_FromResolved(t *testing.T) {
	r := launchconfig.Resolved{Effective: launchconfig.Layer{
		Model:      "openai/gpt-5",
		Agent:      "default",
		SkillsDirs: []string{"/sk"},
	}}
	req := SpawnRequest{Resolved: r, WorkingDir: "/wd", StateDir: "/st", RunDir: "/rn"}
	got := buildSpawnArgs(req)
	wantHas := []string{"--addr", "127.0.0.1:0", "--dir", "/wd", "--state-dir", "/st", "--run-dir", "/rn", "--model", "openai/gpt-5", "--agent", "default", "--skills-dir", "/sk"}
	for _, w := range wantHas {
		found := false
		for _, a := range got {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("buildSpawnArgs missing %q in %v", w, got)
		}
	}
}
```

- [ ] **Step 3: Run (fails — SpawnRequest.Resolved doesn't exist)**

```bash
go test ./cmd/serf-hub/ -run TestBuildSpawnArgs_FromResolved -v
```

- [ ] **Step 4: Rewrite SpawnRequest, ResumeRequest, buildSpawnArgs**

Replace the existing types and functions in `cmd/serf-hub/spawn.go`:

```go
// SpawnRequest carries everything needed to spawn one `serf serve` child.
type SpawnRequest struct {
	Resolved        launchconfig.Resolved
	WorkingDir      string
	StateDir        string
	RunDir          string
	Env             []string // built by ToEnv
	// Provider is the model's provider, used for credential injection.
	Provider        string
	SSERingSize     int
}

// ResumeRequest resumes a saved session. Resolved still applies (we
// re-resolve on each spawn so config edits take effect).
type ResumeRequest struct {
	SessionID  string
	Resolved   launchconfig.Resolved
	WorkingDir string
	StateDir   string
	RunDir     string
	Env        []string
	Provider   string
	SSERingSize int
}

// buildSpawnArgs assembles argv for `serf serve` from the resolved
// launch config plus the daemon-control flags (addr, dir, state-dir,
// run-dir, sse-ring-size).
func buildSpawnArgs(req SpawnRequest) []string {
	args := []string{"--addr", "127.0.0.1:0"}
	if req.WorkingDir != "" {
		args = append(args, "--dir", req.WorkingDir)
	}
	if req.StateDir != "" {
		args = append(args, "--state-dir", req.StateDir)
	}
	if req.RunDir != "" {
		args = append(args, "--run-dir", req.RunDir)
	}
	if req.SSERingSize > 0 {
		args = append(args, "--sse-ring-size", fmt.Sprintf("%d", req.SSERingSize))
	}
	args = append(args, launchconfig.ToArgs(req.Resolved)...)
	return args
}
```

- [ ] **Step 5: Update `HubSpawner.Spawn` and `Resume`**

Replace the existing methods. The spawner now needs the credentials store; add it as a field:

```go
type HubSpawner struct {
	Cfg        Config
	SerfBinary string
	RunDir     string
	HubToken   string
	Creds      *credentials.Store
	StateRoot  string
}

func (h *HubSpawner) Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		req.StateDir = resolveSerfLaunchStateDir(req.WorkingDir, req.Resolved.Effective.Env)
	}
	req.RunDir = h.RunDir
	if req.Resolved.Effective.SSERingSize != nil {
		req.SSERingSize = *req.Resolved.Effective.SSERingSize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:  req.Resolved,
		Provider:  req.Provider,
		Creds:     h.Creds,
		ParentEnv: os.Environ(),
		RunDir:    h.RunDir,
		StateDir:  req.StateDir,
		HubToken:  h.HubToken,
	})
	if err := validateProviderCredentials(req.Provider, h.Creds); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}
```

Similar changes for `Resume`. Drop the now-unused `buildSerfChildEnv`, `buildDefaultSerfChildEnv`, and old `validateProviderCredentials` helpers.

Replace `validateProviderCredentials`:
```go
func validateProviderCredentials(provider string, store *credentials.Store) error {
	if provider == "" {
		return nil
	}
	v, src := store.Get(provider)
	if src == credentials.SourceNone {
		return nil
	}
	if v == "" {
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set via serf/auth/apiKey/set or set the matching env var", provider))
	}
	return nil
}
```

- [ ] **Step 6: Run tests + build**

```bash
go test ./cmd/serf-hub/ -v
go build ./...
```

Expected: PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/spawn.go cmd/serf-hub/spawn_test.go
git commit -m "serf-hub: spawn.go consumes launchconfig.Resolved"
```

---

## Task 16 — Thread `LaunchOverrides` through ThreadStart

**Files:**
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/app_rpc_test.go`

- [ ] **Step 1: Find the ThreadStart handler**

```bash
grep -n "Spawn(ctx, SpawnRequest{" cmd/serf-hub/app_rpc.go
```

Around line 1268 you'll see the existing call.

- [ ] **Step 2: Write the failing test**

Append to `cmd/serf-hub/app_rpc_test.go`:

```go
func TestThreadStart_LaunchOverridesApplied(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	// Spy spawner that records the SpawnRequest it received.
	var got SpawnRequest
	spawner := &spySpawner{onSpawn: func(req SpawnRequest) { got = req }}
	cfg := newTestAppConfig(t, stateRoot, spawner)
	srv := newTestAppServer(t, cfg)

	prov := "anthropic"
	params := appwire.ThreadStartParams{
		CWD:           cwd,
		Model:         "anthropic/claude-sonnet-4-6",
		ModelProvider: prov,
		LaunchOverrides: &appwire.LaunchConfigLayer{
			SkillsDirs: []string{"/per-launch"},
			MaxRounds:  ptrInt(7),
		},
	}
	if _, err := srv.ThreadStart(context.Background(), params); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.MaxRounds == nil || *got.Resolved.Effective.MaxRounds != 7 {
		t.Errorf("MaxRounds = %v, want 7", got.Resolved.Effective.MaxRounds)
	}
	if len(got.Resolved.Effective.SkillsDirs) == 0 || got.Resolved.Effective.SkillsDirs[0] != "/per-launch" {
		t.Errorf("SkillsDirs = %v", got.Resolved.Effective.SkillsDirs)
	}
}
```

(`spySpawner` and `newTestAppConfig`/`newTestAppServer` are existing testing helpers; if not, add minimal ones following the patterns already in `app_rpc_test.go`.)

- [ ] **Step 3: Update the ThreadStart handler**

Find the existing block. Replace:

```go
entry, err := cfg.Spawner.Spawn(ctx, SpawnRequest{
	Model:           modelRef.Qualified(),
	WorkingDir:      workingDir,
	Agent:           params.Profile,
	ReasoningEffort: params.ReasoningEffort,
})
```

with:

```go
var overrides launchconfig.Layer
if params.LaunchOverrides != nil {
	overrides = launchconfig.FromWire(*params.LaunchOverrides)
}
// Legacy scalar fields win over launchOverrides (per spec §5.4).
if params.Model != "" {
	overrides.Model = modelRef.Qualified()
}
if params.Profile != "" {
	overrides.Agent = params.Profile
}
if params.ReasoningEffort != "" {
	overrides.ReasoningEffort = params.ReasoningEffort
}
resolved, err := launchconfig.Resolve(cfg.HubStateRoot, workingDir, overrides)
if err != nil {
	return appwire.ThreadStartResponse{}, err
}
entry, err := cfg.Spawner.Spawn(ctx, SpawnRequest{
	Resolved:   resolved,
	WorkingDir: workingDir,
	Provider:   modelRef.Provider,
})
```

- [ ] **Step 4: Run tests**

```bash
go test ./cmd/serf-hub/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_rpc.go cmd/serf-hub/app_rpc_test.go
git commit -m "serf-hub: ThreadStart honors launchOverrides via Resolve"
```

---

## Task 17 — Drop the old `[serf_launch]` schema and clean up config

**Files:**
- Modify: `cmd/serf-hub/config.go`
- Modify: `cmd/serf-hub/config_test.go`
- Modify: `cmd/serf-hub/main.go`

- [ ] **Step 1: Find the obsolete fields**

```bash
grep -n "SerfLaunchConfig\|SerfLaunch\b\|sse_ring_size" cmd/serf-hub/config.go
```

- [ ] **Step 2: Remove `SerfLaunchConfig` from `Config`**

In `cmd/serf-hub/config.go`, remove the `SerfLaunchConfig` struct and the `SerfLaunch SerfLaunchConfig` field. Add the new `HubStateRoot` field:

```go
type Config struct {
	Addr               string                        `toml:"addr"`
	HubStateRoot       string                        `toml:"hub_state_root"` // default ~/.serf
	StateGlob          string                        `toml:"state_glob"`
	RunDir             string                        `toml:"run_dir"`
	PastIndexDB        string                        `toml:"past_index_db"`
	StatusPollInterval time.Duration                 `toml:"status_poll_interval"`
	PastIndexRebuild   time.Duration                 `toml:"past_index_rebuild_interval"`
	SpawnTimeout       time.Duration                 `toml:"spawn_timeout"`
	PastResultsPerPage int                           `toml:"past_results_per_page"`
	Providers          []ProviderConfig              `toml:"providers"`
	CodexSources       []appsource.CodexSourceConfig `toml:"codex_sources"`
	CodexLaunches      []CodexLaunchConfig           `toml:"codex_launches"`
}
```

In `DefaultConfig` and `LoadConfig`, default `HubStateRoot` to `$HOME/.serf`:

```go
if cfg.HubStateRoot == "" {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	cfg.HubStateRoot = filepath.Join(home, ".serf")
}
```

- [ ] **Step 3: Update existing config tests to use the new shape**

Run:
```bash
go test ./cmd/serf-hub/ -run TestLoadConfig -v
```

Update any test that referenced `[serf_launch]` or `cfg.SerfLaunch.Env`. If those tests asserted behavior that the new design provides via launchconfig layers instead, port them to write into `~/.serf/launch.toml` and assert via the resolver.

- [ ] **Step 4: Update main.go to use HubStateRoot for everything**

```bash
grep -n "newHubAuthController\|HubSpawner\|cfg\.SerfLaunch" cmd/serf-hub/main.go
```

Wire `HubSpawner.StateRoot = cfg.HubStateRoot` and `HubSpawner.Creds = credsStore`.

- [ ] **Step 5: Run full build + tests**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/config.go cmd/serf-hub/config_test.go cmd/serf-hub/main.go
git commit -m "serf-hub: drop legacy [serf_launch] config, add hub_state_root"
```

---

## Task 18 — End-to-end test

**Files:**
- Modify: `cmd/serf-hub/e2e_test.go`

- [ ] **Step 1: Plan the e2e scenario**

A real hub instance with: a global launch.toml, a project hub-side launch.toml, a trusted in-repo .serf/launch.toml, plus a per-launch override. Verify the final argv handed to `serf serve` is what `serf launch-check --json` reports.

- [ ] **Step 2: Write the test**

Append to `cmd/serf-hub/e2e_test.go`:

```go
func TestE2E_LayeredLaunchConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	stateRoot := t.TempDir()
	cwd := t.TempDir()

	// Global layer.
	if err := launchconfig.SaveLayer(filepath.Join(stateRoot, "launch.toml"), launchconfig.Layer{
		Model:      "openai/gpt-5-mini-2025-08-07",
		SkillsDirs: []string{"/global/skills"},
		MaxRounds:  ptrInt(50),
	}); err != nil {
		t.Fatal(err)
	}

	// Hub-side per-project layer.
	pid := launchconfig.ProjectID(cwd)
	if err := launchconfig.SaveLayer(filepath.Join(stateRoot, "projects", pid, "launch.toml"), launchconfig.Layer{
		PluginDirs: []string{"/proj/plugins"},
	}); err != nil {
		t.Fatal(err)
	}

	// Trusted in-repo layer.
	repoTOML := []byte(`skills_dirs = ["sub"]
context_strategy = "ooda"
`)
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	_ = os.MkdirAll(filepath.Dir(repoPath), 0o755)
	_ = os.WriteFile(repoPath, repoTOML, 0o600)
	hash, _ := launchconfig.CanonicalHashTOML(repoTOML)
	_ = launchconfig.SaveMeta(filepath.Join(stateRoot, "projects", pid, "meta.toml"), launchconfig.Meta{
		Schema: 1, CWD: cwd,
		Trust: launchconfig.MetaTrust{Hash: hash, Decision: "trusted"},
	})

	// Per-launch overrides.
	overrides := launchconfig.Layer{ReasoningEffort: "low"}

	resolved, err := launchconfig.Resolve(stateRoot, cwd, overrides)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.Effective.Model != "openai/gpt-5-mini-2025-08-07" {
		t.Errorf("Model = %q", resolved.Effective.Model)
	}
	if got := resolved.Effective.SkillsDirs; len(got) != 2 || got[0] != "/global/skills" || got[1] != filepath.Join(cwd, "sub") {
		t.Errorf("SkillsDirs = %v", got)
	}
	if got := resolved.Effective.PluginDirs; len(got) != 1 || got[0] != "/proj/plugins" {
		t.Errorf("PluginDirs = %v", got)
	}
	if resolved.Effective.ContextStrategy != "ooda" {
		t.Errorf("ContextStrategy = %q", resolved.Effective.ContextStrategy)
	}
	if resolved.Effective.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q", resolved.Effective.ReasoningEffort)
	}
	args := launchconfig.ToArgs(resolved)
	wantHas := []string{"--model", "openai/gpt-5-mini-2025-08-07", "--context-strategy", "ooda", "--reasoning-effort", "low", "--max-rounds", "50"}
	for _, w := range wantHas {
		found := false
		for _, a := range args {
			if a == w {
				found = true
			}
		}
		if !found {
			t.Errorf("args missing %q in %v", w, args)
		}
	}
}
```

- [ ] **Step 3: Run**

```bash
go test ./cmd/serf-hub/ -run TestE2E_LayeredLaunchConfig -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/e2e_test.go
git commit -m "serf-hub: e2e test for layered launch config"
```

---

## Task 19 — Final cleanup and CHANGELOG / docs

**Files:**
- Modify: `cmd/serf-hub/README.md`

- [ ] **Step 1: Update the README**

In `cmd/serf-hub/README.md`, remove the section describing `[serf_launch]` and `[serf_launch.env]`. Add a "Launch Configuration" section pointing readers at `~/.serf/launch.toml`, `<project>/.serf/launch.toml`, `~/.serf/projects/<id>/launch.toml`, and `~/.serf/credentials.toml`. Reference the spec for details.

Replace the existing example `hub.toml`:

```toml
addr = "127.0.0.1:9180"
hub_state_root = "$HOME/.serf"
run_dir = "$HOME/.serf/run"
state_glob = "$HOME/.local/state/serf/projects/*"
past_index_db = "$HOME/.serf/index.db"
spawn_timeout = "30s"
```

Note: `[serf_launch]` is gone. Launch configuration goes in `~/.serf/launch.toml` (form-editable from the Hub UI).

- [ ] **Step 2: Run full test suite**

```bash
go test ./... && go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/README.md
git commit -m "serf-hub: README points at new launch-config layout"
```

---

## Implementation Checklist Summary

- [ ] Task 1 — Layer type + TOML round-trip
- [ ] Task 2 — Atomic layer file I/O
- [ ] Task 3 — Project IDs and path validation
- [ ] Task 4 — Layer merging with diagnostics
- [ ] Task 5 — TOFU hashing and trust state
- [ ] Task 6 — Top-level `Resolve`
- [ ] Task 7 — `ToArgs`
- [ ] Task 8 — `internal/credentials` package
- [ ] Task 9 — `ToEnv` with credential injection
- [ ] Task 10 — Appwire wire types
- [ ] Task 11 — launchconfig ↔ appwire adapters
- [ ] Task 12 — Hub `serf/launch/*` handlers
- [ ] Task 13 — Hub `serf/auth/*` extensions
- [ ] Task 14 — Register handlers + notifications
- [ ] Task 15 — `spawn.go` rewrite
- [ ] Task 16 — ThreadStart applies overrides
- [ ] Task 17 — Drop legacy `[serf_launch]`
- [ ] Task 18 — End-to-end test
- [ ] Task 19 — README + final cleanup
