# Personal AGENTS.md Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every session loads `~/.config/evener/AGENTS.md` ahead of the repo's own project docs, and the web UI's settings pane can read and write that file.

**Architecture:** The daemon gains a user-doc loader that shares the existing 32KB project-doc budget and renders through the existing project-docs prompt section. The hub gains two additive `evener-appwire-v4` methods plus a change notification, wired like the keybindings settings handlers. The frontend gains a small zustand store and a new "AGENTS.md" settings section with a monospace textarea, Save and Revert.

**Tech Stack:** Go (agent, appwire, cmd/evener-hub), TypeScript + React + zustand + vitest (cmd/evener-hub/frontend), `make generate` for wire types.

**Spec:** `docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md` §1 (plus §5 testing, §6 delivery). This plan is slice 1 of 3.

## Global Constraints

- Repo root for every command below: the git worktree you were started in (`git rev-parse --show-toplevel`). Frontend commands run from `cmd/evener-hub/frontend`.
- Protocol version stays `evener-appwire-v4`; every wire change is additive.
- The personal file lives at `<config root>/AGENTS.md`, where the config root is `envvars/userdirs.DefaultConfigRoot()` (`$XDG_CONFIG_HOME/evener`, default `~/.config/evener`).
- `set` writes content byte-for-byte (no trimming, no forced newline), temp-file-and-rename, mode 0o644, no precondition (last write wins).
- No `git add -A`. Commit after every task with the exact message given.
- Frontend: run `npx biome check --write <touched files under src/>` before every commit that touches frontend files; the gate is `make test-web` from the repo root.
- Go: `gofmt` is enforced by `make lint`; keep files gofmt-clean.
- Tests must be deterministic and never read the developer's real config root: every test that touches a config root uses `t.TempDir()`.
- Follow the file's existing comment style: explain why, never narrate what changed.

---

### Task 1: Daemon loads the personal doc ahead of project docs

**Files:**
- Modify: `agent/project_docs.go`
- Modify: `agent/session_init.go:1443`
- Test: `agent/project_docs_test.go`

**Interfaces:**
- Produces: `agent.UserDocFile` (const `"AGENTS.md"`), `agent.LoadUserDoc(configRoot string) (ProjectDoc, bool)`, `agent.LoadInstructionDocs(env execenv.ExecutionEnvironment, configRoot string, filenames ...string) ([]ProjectDoc, bool)`. `LoadProjectDocs` keeps its signature and behavior.

- [ ] **Step 1: Write the failing tests**

Append to `agent/project_docs_test.go`:

```go
func TestLoadUserDoc_ReadsTheConfigRootFile(t *testing.T) {
	t.Parallel()
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte("PERSONAL\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	doc, ok := LoadUserDoc(configRoot)
	if !ok {
		t.Fatal("expected the personal doc to load")
	}
	if doc.Path != filepath.Join(configRoot, "AGENTS.md") {
		t.Fatalf("path: %q", doc.Path)
	}
	if doc.Content != "PERSONAL\n" {
		t.Fatalf("content: %q", doc.Content)
	}
}

func TestLoadUserDoc_MissingOrBlankFileIsAbsent(t *testing.T) {
	t.Parallel()
	if _, ok := LoadUserDoc(t.TempDir()); ok {
		t.Fatal("a missing file must not load")
	}
	blank := t.TempDir()
	if err := os.WriteFile(filepath.Join(blank, "AGENTS.md"), []byte("  \n\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, ok := LoadUserDoc(blank); ok {
		t.Fatal("a blank file must not load")
	}
	if _, ok := LoadUserDoc(""); ok {
		t.Fatal("an empty config root must not load")
	}
}

func TestLoadUserDoc_CollapsesTheHomeDirectoryToTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := filepath.Join(home, ".config", "evener")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	doc, ok := LoadUserDoc(configRoot)
	if !ok {
		t.Fatal("expected the personal doc to load")
	}
	if doc.Path != "~/.config/evener/AGENTS.md" {
		t.Fatalf("path: %q, want the tilde-collapsed display path", doc.Path)
	}
}

func TestLoadInstructionDocs_PersonalDocComesFirst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte("PERSONAL\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadInstructionDocs(env, configRoot, "AGENTS.md")
	if truncated {
		t.Fatal("did not expect truncation")
	}
	if len(docs) != 2 {
		t.Fatalf("docs: got %d want 2 (%v)", len(docs), docs)
	}
	if docs[0].Path != filepath.Join(configRoot, "AGENTS.md") || docs[0].Content != "PERSONAL\n" {
		t.Fatalf("doc0 = %+v, want the personal doc first", docs[0])
	}
	if docs[1].Path != "AGENTS.md" || docs[1].Content != "ROOT\n" {
		t.Fatalf("doc1 = %+v, want the repo doc second", docs[1])
	}
}

func TestLoadInstructionDocs_MissingPersonalDocChangesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)
	docs, _ := LoadInstructionDocs(env, t.TempDir(), "AGENTS.md")
	if len(docs) != 1 || docs[0].Path != "AGENTS.md" {
		t.Fatalf("docs = %+v, want only the repo doc", docs)
	}
}

func TestLoadInstructionDocs_PersonalDocCountsAgainstTheSharedBudget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	half := strings.Repeat("r", projectDocByteBudget/2+1024)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(half), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configRoot := t.TempDir()
	personal := strings.Repeat("p", projectDocByteBudget/2)
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte(personal), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadInstructionDocs(env, configRoot, "AGENTS.md")
	if !truncated {
		t.Fatal("expected the repo doc to be truncated")
	}
	if len(docs) != 2 {
		t.Fatalf("docs: got %d want 2", len(docs))
	}
	if docs[0].Content != personal {
		t.Fatal("the personal doc must land intact; it is loaded first")
	}
	if !strings.Contains(docs[1].Content, projectDocTruncMark) {
		t.Fatalf("the repo doc must carry the truncation marker, got:\n%s", docs[1].Content[len(docs[1].Content)-80:])
	}
	if len(docs[0].Content)+len(docs[1].Content) > projectDocByteBudget+len(projectDocTruncMark)+2 {
		t.Fatalf("the two docs exceed the shared budget: %d", len(docs[0].Content)+len(docs[1].Content))
	}
}

func TestLoadInstructionDocs_OversizedPersonalDocIsTruncatedAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configRoot := t.TempDir()
	huge := strings.Repeat("p", projectDocByteBudget+4096)
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte(huge), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadInstructionDocs(env, configRoot, "AGENTS.md")
	if !truncated || len(docs) != 1 {
		t.Fatalf("docs = %d truncated = %v; an oversized personal doc consumes the whole budget", len(docs), truncated)
	}
	if !strings.Contains(docs[0].Content, projectDocTruncMark) {
		t.Fatal("expected the truncation marker on the personal doc")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./agent -run 'TestLoadUserDoc|TestLoadInstructionDocs' 2>&1 | head -20`
Expected: compile error, `undefined: LoadUserDoc` / `undefined: LoadInstructionDocs`.

- [ ] **Step 3: Implement the loader**

Replace `agent/project_docs.go` from the `LoadProjectDocs` doc comment to the end of the file with:

```go
// UserDocFile is the personal instructions file evener loads from the user
// config root ahead of every repo's own project docs.
const UserDocFile = "AGENTS.md"

// LoadProjectDocs discovers and loads project instruction files from git root (or working directory when not
// in a git repo) down to the current working directory. Files are loaded in depth order (root first; deeper
// files have higher precedence) and filtered by the active provider profile (caller-provided list).
func LoadProjectDocs(env execenv.ExecutionEnvironment, filenames ...string) ([]ProjectDoc, bool) {
	return loadProjectDocs(env, 0, filenames...)
}

// LoadInstructionDocs is a session's whole instruction set: the personal doc
// under configRoot first, then the repo's project docs, sharing one byte
// budget. The personal doc is loaded first because it is the user's own
// standing instructions for every session; the repo docs get whatever budget
// remains, truncated exactly as LoadProjectDocs truncates them.
func LoadInstructionDocs(env execenv.ExecutionEnvironment, configRoot string, filenames ...string) ([]ProjectDoc, bool) {
	out := []ProjectDoc{}
	used := 0
	if user, ok := LoadUserDoc(configRoot); ok {
		if len(user.Content) > projectDocByteBudget {
			user.Content = truncateDoc(user.Content, projectDocByteBudget)
			return append(out, user), true
		}
		used = len(user.Content)
		out = append(out, user)
	}
	project, truncated := loadProjectDocs(env, used, filenames...)
	return append(out, project...), truncated
}

// LoadUserDoc reads <configRoot>/AGENTS.md. ok is false when the config root
// is empty or the file is missing or blank. Path is the display path the
// prompt labels the block with: the home directory collapsed to "~", so the
// model sees "~/.config/evener/AGENTS.md" rather than a machine-specific
// absolute path.
func LoadUserDoc(configRoot string) (ProjectDoc, bool) {
	configRoot = strings.TrimSpace(configRoot)
	if configRoot == "" {
		return ProjectDoc{}, false
	}
	path := filepath.Join(configRoot, UserDocFile)
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return ProjectDoc{}, false
	}
	return ProjectDoc{Path: tildeCollapse(path), Content: string(b)}, true
}

// tildeCollapse rewrites a path under the home directory as "~/...".
func tildeCollapse(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return "~/" + filepath.ToSlash(rel)
}

// truncateDoc cuts content to remain bytes and appends the truncation marker.
func truncateDoc(content string, remain int) string {
	if remain < len(content) {
		content = content[:remain]
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + projectDocTruncMark + "\n"
}

// loadProjectDocs is LoadProjectDocs with `used` bytes of the budget already
// spent by a doc loaded before the repo's own.
func loadProjectDocs(env execenv.ExecutionEnvironment, used int, filenames ...string) ([]ProjectDoc, bool) {
	if env == nil {
		return nil, false
	}

	cwd := strings.TrimSpace(env.WorkingDirectory())
	if cwd == "" {
		return nil, false
	}
	// Resolve symlinks so cwd and git root use consistent paths (macOS /var -> /private/var).
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	root := cwd
	if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
		root = gr
	}

	dirs := execenv.DirsFromRootToCwd(root, cwd)
	out := []ProjectDoc{}
	for _, dir := range dirs {
		relDir := "."
		if r, err := filepath.Rel(root, dir); err == nil {
			relDir = r
		}
		for _, name := range filenames {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			key := name
			if relDir != "." && relDir != "" {
				key = filepath.Join(relDir, name)
			}

			content := string(b)
			if used+len(content) > projectDocByteBudget {
				out = append(out, ProjectDoc{Path: key, Content: truncateDoc(content, projectDocByteBudget-used)})
				return out, true
			}
			used += len(content)
			out = append(out, ProjectDoc{Path: key, Content: content})
		}
	}
	return out, false
}
```

Keep the `ProjectDoc` type, the two constants, and the imports at the top of the file exactly as they are.

- [ ] **Step 4: Run the loader tests**

Run: `go test ./agent -run 'TestLoadUserDoc|TestLoadProjectDocs|TestLoadInstructionDocs' -v 2>&1 | tail -20`
Expected: all PASS, including the two pre-existing `TestLoadProjectDocs_*` tests.

- [ ] **Step 5: Wire the session to the new loader**

In `agent/session_init.go`, change line 1443 from

```go
	s.projectDocs, s.projectDocsTruncated = LoadProjectDocs(s.currentEnv(), s.profile.ProjectDocFiles()...)
```

to

```go
	s.projectDocs, s.projectDocsTruncated = LoadInstructionDocs(s.currentEnv(), userdirs.DefaultConfigRoot(), s.profile.ProjectDocFiles()...)
```

`userdirs` is already imported in that file (it is used for the user skills dir at line 1345).

- [ ] **Step 6: Run the agent package tests and vet**

Run: `go build ./... && go vet ./agent && go test ./agent 2>&1 | tail -5`
Expected: build OK, vet clean, `ok  primeradiant.com/evener/agent`.

- [ ] **Step 7: Commit**

```bash
git add agent/project_docs.go agent/project_docs_test.go agent/session_init.go
git commit -m "feat(agent): load the personal AGENTS.md ahead of project docs"
```

---

### Task 2: Wire types and catalog for the agentsDoc methods

**Files:**
- Create: `appwire/agents_doc.go`
- Modify: `appwire/protocol.go` (the `Methods` list after the keybindings entries at lines 201-202; the `Notifications` list after the keybindings entry at line 295)
- Test: `appwire/protocol_test.go`
- Regenerate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Produces: `appwire.MethodEvenerSettingsAgentsDocGet = "evener/settings/agentsDoc/get"`, `appwire.MethodEvenerSettingsAgentsDocSet = "evener/settings/agentsDoc/set"`, `appwire.NotifyEvenerSettingsAgentsDocChanged = "evener/settings/agentsDoc/changed"`, `appwire.AgentsDocResponse{Path string; Exists bool; Content string}`, `appwire.AgentsDocSetParams{Content string}`. The generated TS gains `AgentsDocResponse`, `AgentsDocSetParams`, and the three names in `METHOD_NAMES` / `NOTIFICATION_NAMES`.

- [ ] **Step 1: Write the failing catalog test**

Append to `appwire/protocol_test.go` (next to `TestKeybindingsCatalog`):

```go
func TestAgentsDocCatalog(t *testing.T) {
	methods := map[string]MethodSpec{}
	for _, method := range Methods {
		methods[method.Name] = method
	}
	for _, name := range []string{
		MethodEvenerSettingsAgentsDocGet,
		MethodEvenerSettingsAgentsDocSet,
	} {
		method, ok := methods[name]
		if !ok {
			t.Fatalf("method catalog missing %s", name)
		}
		if method.Scope != ScopeHub {
			t.Errorf("method %s scope = %q, want %q", name, method.Scope, ScopeHub)
		}
		if reflect.TypeOf(method.Result) != reflect.TypeFor[AgentsDocResponse]() {
			t.Errorf("method %s result type = %T, want %T", name, method.Result, AgentsDocResponse{})
		}
	}
	if reflect.TypeOf(methods[MethodEvenerSettingsAgentsDocSet].Params) != reflect.TypeFor[AgentsDocSetParams]() {
		t.Errorf("set params type = %T, want %T", methods[MethodEvenerSettingsAgentsDocSet].Params, AgentsDocSetParams{})
	}
	var changed *NotificationSpec
	for i := range Notifications {
		if Notifications[i].Name == NotifyEvenerSettingsAgentsDocChanged {
			changed = &Notifications[i]
			break
		}
	}
	if changed == nil {
		t.Fatalf("notification catalog missing %s", NotifyEvenerSettingsAgentsDocChanged)
	}
	if reflect.TypeOf(changed.Payload) != reflect.TypeFor[AgentsDocResponse]() {
		t.Fatalf("changed payload type = %T, want %T", changed.Payload, AgentsDocResponse{})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./appwire -run TestAgentsDocCatalog 2>&1 | head -5`
Expected: compile error, `undefined: MethodEvenerSettingsAgentsDocGet`.

- [ ] **Step 3: Add the types**

Create `appwire/agents_doc.go`:

```go
package appwire

// The personal AGENTS.md: a plain file at <config root>/AGENTS.md that every
// session loads ahead of the repo's own project docs
// (agent.LoadInstructionDocs). The hub reads and rewrites it whole; there is
// deliberately no revision or precondition (spec 2026-09-07 §1): the file is
// also hand-edited in editors, and the last write wins.
const (
	MethodEvenerSettingsAgentsDocGet     = "evener/settings/agentsDoc/get"
	MethodEvenerSettingsAgentsDocSet     = "evener/settings/agentsDoc/set"
	NotifyEvenerSettingsAgentsDocChanged = "evener/settings/agentsDoc/changed"
)

// AgentsDocResponse is the file as the hub sees it: the get result, the set
// result, and the changed broadcast all carry this shape. A missing file is
// Exists=false with empty Content, never an error.
type AgentsDocResponse struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
}

// AgentsDocSetParams replaces the whole file with Content, byte for byte.
type AgentsDocSetParams struct {
	Content string `json:"content"`
}
```

- [ ] **Step 4: Catalog the methods and the notification**

In `appwire/protocol.go`, after the `MethodEvenerSettingsKeybindingsPatch` line in `Methods`, add:

```go
	{MethodEvenerSettingsAgentsDocGet, EmptyParams{}, AgentsDocResponse{}, ScopeHub, "Reads the personal AGENTS.md under the user config root: its path, whether it exists, and its content."},
	{MethodEvenerSettingsAgentsDocSet, AgentsDocSetParams{}, AgentsDocResponse{}, ScopeHub, "Replaces the personal AGENTS.md whole (no precondition); broadcasts evener/settings/agentsDoc/changed."},
```

After the `NotifyEvenerSettingsKeybindingsChanged` line in `Notifications`, add:

```go
	{NotifyEvenerSettingsAgentsDocChanged, AgentsDocResponse{}, "Broadcast after the personal AGENTS.md is written; carries the new path, existence, and content."},
```

- [ ] **Step 5: Run the appwire tests**

Run: `go test ./appwire 2>&1 | tail -5`
Expected: `ok`. `TestMethodCatalogWellFormed` and `TestNotificationCatalogWellFormed` cover the new entries; a catalog-to-router parity test in `cmd/evener-hub` will fail until Task 3 registers the handlers, which is expected at this point.

- [ ] **Step 6: Regenerate the wire outputs**

Run: `make generate && git status --short`
Expected: `cmd/evener-hub/frontend/src/protocol/types.gen.ts` and `docs/appwire-protocol.md` are modified. Confirm with `grep -n "agentsDoc" cmd/evener-hub/frontend/src/protocol/types.gen.ts | head` that `AgentsDocResponse`, `AgentsDocSetParams`, the two method entries, and the notification entry are present.

- [ ] **Step 7: Commit**

```bash
git add appwire/agents_doc.go appwire/protocol.go appwire/protocol_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(appwire): catalogue the personal AGENTS.md get/set methods"
```

---

### Task 3: Hub handlers for get and set

**Files:**
- Create: `cmd/evener-hub/app_rpc_agents_doc.go`
- Modify: `cmd/evener-hub/app_rpc.go:344` (register after `registerKeybindingsHandlers`)
- Test: `cmd/evener-hub/app_rpc_agents_doc_test.go`

**Interfaces:**
- Consumes: Task 2's constants and types; `hubLaunchConfigRoot(cfg)` (already in `app_rpc.go`); test helpers `newHubRPCTestServer`, `dialHubRPC` (in `app_rpc_test.go`).
- Produces: `registerAgentsDocHandlers(server *appserver.Server, configRoot string)`, `agentsDocPath(configRoot string) string`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/app_rpc_agents_doc_test.go`:

```go
package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubRPCAgentsDocGetReportsAMissingFile(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var got appwire.AgentsDocResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md")}
	if got != want {
		t.Fatalf("get = %+v, want %+v", got, want)
	}
}

func TestHubRPCAgentsDocGetReadsAnExistingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var got appwire.AgentsDocResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md"), Exists: true, Content: "# mine\n"}
	if got != want {
		t.Fatalf("get = %+v, want %+v", got, want)
	}
}

func TestHubRPCAgentsDocSetWritesVerbatimAndBroadcasts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evener") // the config root itself may not exist yet
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
	}

	const content = "  leading space, no trailing newline"
	var result appwire.AgentsDocResponse
	if err := clientA.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocSet, appwire.AgentsDocSetParams{Content: content}, &result); err != nil {
		t.Fatalf("set: %v", err)
	}
	want := appwire.AgentsDocResponse{Path: filepath.Join(root, "AGENTS.md"), Exists: true, Content: content}
	if result != want {
		t.Fatalf("set = %+v, want %+v", result, want)
	}

	onDisk, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(onDisk) != content {
		t.Fatalf("on disk = %q, want the content byte for byte", onDisk)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %o, want 0644", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md.tmp")); !os.IsNotExist(err) {
		t.Fatal("the temp file survived the rename")
	}

	for _, client := range []*appwire.Client{clientA, clientB} {
		notification := receiveAgentsDocChanged(t, client)
		if notification != want {
			t.Fatalf("notification = %+v, want %+v", notification, want)
		}
	}
}

func TestHubRPCAgentsDocSetReplacesThePreviousContent(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first\n", "second\n", ""} {
		var result appwire.AgentsDocResponse
		if err := client.Request(context.Background(), appwire.MethodEvenerSettingsAgentsDocSet, appwire.AgentsDocSetParams{Content: content}, &result); err != nil {
			t.Fatalf("set %q: %v", content, err)
		}
		onDisk, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(onDisk) != content {
			t.Fatalf("on disk = %q after set %q", onDisk, content)
		}
		receiveAgentsDocChanged(t, client)
	}
}

func TestWriteAgentsDocFailureLeavesThePreviousFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("relies on a directory the process cannot write")
	}
	root := t.TempDir()
	path := agentsDocPath(root)
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if err := writeAgentsDoc(path, "new"); err == nil {
		t.Fatal("expected the write to fail in a read-only directory")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "keep me\n" {
		t.Fatalf("a failed write changed the file: %q", onDisk)
	}
}

func receiveAgentsDocChanged(t *testing.T, client *appwire.Client) appwire.AgentsDocResponse {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		if notification.Method != appwire.NotifyEvenerSettingsAgentsDocChanged {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerSettingsAgentsDocChanged)
		}
		var params appwire.AgentsDocResponse
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		return params
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the agentsDoc notification")
		return appwire.AgentsDocResponse{}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub -run 'AgentsDoc' 2>&1 | head -10`
Expected: compile error, `undefined: agentsDocPath` / `undefined: writeAgentsDoc`.

- [ ] **Step 3: Implement the handlers**

Create `cmd/evener-hub/app_rpc_agents_doc.go`:

```go
package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// agentsDocPath is the personal AGENTS.md under the user config root - the
// same file agent.LoadInstructionDocs reads at session init.
func agentsDocPath(configRoot string) string {
	return filepath.Join(configRoot, agent.UserDocFile)
}

// readAgentsDoc reports the file as it is on disk. A missing file is the
// empty document, not an error: the settings section shows an empty editor
// and the first save creates it.
func readAgentsDoc(path string) (appwire.AgentsDocResponse, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return appwire.AgentsDocResponse{Path: path}, nil
	}
	if err != nil {
		return appwire.AgentsDocResponse{}, fmt.Errorf("AGENTS.md: read: %w", err)
	}
	return appwire.AgentsDocResponse{Path: path, Exists: true, Content: string(b)}, nil
}

// writeAgentsDoc replaces the file atomically (temp + rename, mode 0644,
// parent created), the same way registry.WriteConfigFile lands
// providers.toml beside it. Content is written byte for byte: this is the
// user's own prose, and trimming or appending a newline would make the
// editor disagree with the file it just saved.
func writeAgentsDoc(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("AGENTS.md: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("AGENTS.md: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("AGENTS.md: rename: %w", err)
	}
	return nil
}

// registerAgentsDocHandlers serves evener/settings/agentsDoc/{get,set}. Writes
// serialize on one mutex so two clients saving at once cannot interleave on
// the shared temp path; there is no revision check by design (spec
// 2026-09-07 §1) - the last write wins, and every client hears about it.
func registerAgentsDocHandlers(server *appserver.Server, configRoot string) {
	path := agentsDocPath(configRoot)
	var mu sync.Mutex
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocGet,
		func(context.Context, appwire.EmptyParams) (appwire.AgentsDocResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return readAgentsDoc(path)
		})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocSet,
		func(_ context.Context, params appwire.AgentsDocSetParams) (appwire.AgentsDocResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			if err := writeAgentsDoc(path, params.Content); err != nil {
				return appwire.AgentsDocResponse{}, err
			}
			resp, err := readAgentsDoc(path)
			if err != nil {
				return appwire.AgentsDocResponse{}, err
			}
			server.BroadcastAll(appwire.NotifyEvenerSettingsAgentsDocChanged, resp)
			return resp, nil
		})
}
```

In `cmd/evener-hub/app_rpc.go`, directly after the line `registerKeybindingsHandlers(server, cfg.KeybindingsStore)` add:

```go
	registerAgentsDocHandlers(server, hubLaunchConfigRoot(cfg))
```

- [ ] **Step 4: Run the hub tests**

Run: `go test ./cmd/evener-hub -run 'AgentsDoc|Catalog|Parity' 2>&1 | tail -8`
Expected: PASS. Then the whole package: `go test ./cmd/evener-hub 2>&1 | tail -3` → `ok`.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/app_rpc_agents_doc.go cmd/evener-hub/app_rpc_agents_doc_test.go cmd/evener-hub/app_rpc.go
git commit -m "feat(hub): serve the personal AGENTS.md over appwire"
```

---

### Task 4: Frontend store for the personal doc

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/agentsDoc.ts`
- Test: `cmd/evener-hub/frontend/src/stores/agentsDoc.test.ts`

**Interfaces:**
- Consumes: generated `AgentsDocResponse`, `AgentsDocSetParams`, method names from `protocol/types.gen.ts` (Task 2); `connectionStore` from `stores/connection.ts`; `FakeClient` (`emitNotification`, `on`, `calls`) from `protocol/testing/fakeClient.ts`.
- Produces: `agentsDocStore`, `useAgentsDocStore`, `resetAgentsDocStoreForTests`, `AgentsDocStoreState { doc: AgentsDocResponse | null; loading: boolean; error: string | null; fetch(): Promise<void>; save(content: string): Promise<AgentsDocResponse> }`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/evener-hub/frontend/src/stores/agentsDoc.test.ts`:

```ts
import { beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { AgentsDocResponse, AnyNotification } from "../protocol/types.gen";
import { agentsDocStore, resetAgentsDocStoreForTests } from "./agentsDoc";
import { connectionStore } from "./connection";

const DOC: AgentsDocResponse = { path: "/home/u/.config/evener/AGENTS.md", exists: true, content: "# hi\n" };

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
});

describe("fetch", () => {
  test("loads the document from evener/settings/agentsDoc/get", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().doc).toEqual(DOC);
    expect(agentsDocStore.getState().loading).toBe(false);
    expect(agentsDocStore.getState().error).toBeNull();
  });

  test("records a failed fetch as error text and keeps doc null", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => {
      throw new Error("disk on fire");
    });
    await agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().doc).toBeNull();
    expect(agentsDocStore.getState().error).toContain("disk on fire");
    expect(agentsDocStore.getState().loading).toBe(false);
  });

  test("throws when no client is connected", async () => {
    await expect(agentsDocStore.getState().fetch()).rejects.toThrow(/no client connected/);
  });
});

describe("save", () => {
  test("sends the content to evener/settings/agentsDoc/set and adopts the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/set", (params) => ({ ...DOC, content: params.content }));
    const saved = await agentsDocStore.getState().save("# new\n");
    expect(saved.content).toBe("# new\n");
    expect(agentsDocStore.getState().doc?.content).toBe("# new\n");
    expect(fake.calls.map((c) => c.method)).toEqual(["evener/settings/agentsDoc/set"]);
    expect(fake.calls[0]?.params).toEqual({ content: "# new\n" });
  });

  test("a rejected save propagates and leaves doc untouched", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    fake.on("evener/settings/agentsDoc/set", () => {
      throw new Error("read-only");
    });
    await expect(agentsDocStore.getState().save("x")).rejects.toThrow("read-only");
    expect(agentsDocStore.getState().doc).toEqual(DOC);
  });
});

describe("changed notification", () => {
  test("replaces doc with the broadcast payload", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    const pushed: AgentsDocResponse = { ...DOC, content: "# elsewhere\n" };
    fake.emitNotification({ method: "evener/settings/agentsDoc/changed", params: pushed } as AnyNotification);
    expect(agentsDocStore.getState().doc).toEqual(pushed);
  });

  test("a replaced client is re-wired: the old client's notifications stop landing", async () => {
    const first = connectFakeClient();
    const second = connectFakeClient();
    second.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    first.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "stale" },
    } as AnyNotification);
    expect(agentsDocStore.getState().doc).toEqual(DOC);
    second.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "live" },
    } as AnyNotification);
    expect(agentsDocStore.getState().doc?.content).toBe("live");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `cmd/evener-hub/frontend`): `npx vitest run src/stores/agentsDoc.test.ts 2>&1 | tail -15`
Expected: FAIL, cannot resolve `./agentsDoc`.

- [ ] **Step 3: Implement the store**

Create `cmd/evener-hub/frontend/src/stores/agentsDoc.ts`:

```ts
// The personal AGENTS.md store: the hub-authoritative file behind
// Settings -> AGENTS.md (spec 2026-09-07 §1). One document, read whole and
// written whole; no revision or conflict handling by design - the file is
// also hand-edited, and the hub's own rule is last write wins. What the
// store DOES keep current is `doc`: every evener/settings/agentsDoc/changed
// broadcast (this client's own save included) replaces it, so a second tab
// or a save from the TUI lands here without a refetch.
//
// requireClient() throws outside any try/catch, matching stores/credentials.ts:
// "no client connected" is a programmer error, not a state to degrade into.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { AgentsDocResponse, AnyNotification } from "../protocol/types.gen";
import { connectionStore } from "./connection";

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("agentsDoc store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

export interface AgentsDocStoreState {
  /** The last document the hub confirmed - null until the first fetch lands. */
  doc: AgentsDocResponse | null;
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
  /** Replaces the file whole. Resolves with the hub's view of the saved file,
   * which is also what `doc` becomes. Rejections propagate: the section
   * owns the inline error and the toast. */
  save(content: string): Promise<AgentsDocResponse>;
}

export const agentsDocStore = createStore<AgentsDocStoreState>((set) => ({
  doc: null,
  loading: false,
  error: null,

  async fetch() {
    const client = requireClient();
    set({ loading: true, error: null });
    try {
      const doc = await client.request("evener/settings/agentsDoc/get", {});
      set({ doc, loading: false });
    } catch (err) {
      set({ loading: false, error: errorText(err) });
    }
  },

  async save(content) {
    const client = requireClient();
    const doc = await client.request("evener/settings/agentsDoc/set", { content });
    set({ doc });
    return doc;
  },
}));

export function useAgentsDocStore(): AgentsDocStoreState;
export function useAgentsDocStore<T>(selector: (state: AgentsDocStoreState) => T): T;
export function useAgentsDocStore<T>(selector?: (state: AgentsDocStoreState) => T): T | AgentsDocStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(agentsDocStore, selector) : useStore(agentsDocStore);
}

// --- notification wiring ----------------------------------------------------

let wiredClient: AppwireClientLike | null = null;
let unsubscribeNotifications: (() => void) | undefined;

function handleNotification(n: AnyNotification): void {
  if (n.method === "evener/settings/agentsDoc/changed") agentsDocStore.setState({ doc: n.params });
}

function attachNotifications(client: AppwireClientLike | null): void {
  if (client === wiredClient) return; // already wired to this exact client
  unsubscribeNotifications?.();
  wiredClient = client;
  unsubscribeNotifications = client?.onNotification(handleNotification);
}

// React to the connection store rather than reading it once: this module can
// be evaluated before AppShell's connect() effect runs (stores/extensions.ts
// documents the mount-order race in full).
connectionStore.subscribe((state) => attachNotifications(state.client));
const initialClient = connectionStore.getState().client;
if (initialClient) attachNotifications(initialClient);

// resetAgentsDocStoreForTests resets the singleton between tests, including
// the module-private wiring above. No production code should ever call this.
export function resetAgentsDocStoreForTests(): void {
  unsubscribeNotifications?.();
  unsubscribeNotifications = undefined;
  wiredClient = null;
  agentsDocStore.setState({ doc: null, loading: false, error: null });
}
```

- [ ] **Step 4: Run the store tests**

Run: `npx biome check --write src/stores/agentsDoc.ts src/stores/agentsDoc.test.ts && npx vitest run src/stores/agentsDoc.test.ts 2>&1 | tail -8`
Expected: 7 tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/stores/agentsDoc.ts src/stores/agentsDoc.test.ts
git commit -m "feat(web): personal AGENTS.md store"
```

---

### Task 5: The AGENTS.md settings section

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/agentsDoc.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/agentsDoc.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections.ts` (the `SETTINGS_SECTIONS` list)
- Modify: `cmd/evener-hub/frontend/src/panes/settings/Settings.tsx` (import + `SECTION_COMPONENTS`)
- Test: `cmd/evener-hub/frontend/src/panes/settings/sections/agentsDoc.test.tsx`
- Test (update): `cmd/evener-hub/frontend/src/panes/settings/sections.test.ts`

**Interfaces:**
- Consumes: Task 4's store; widgets `Button`, `Skeleton`, `Textarea`, `Toast`, `useToasts`; `Code` from `./settingsField`; `useConnectedEffect` from `./useConnectedEffect`; `friendlyErrorMessage` from `protocol/errors`.
- Produces: `AgentsDocSection({ sectionId }: { sectionId: string })`; nav id `agents-md`, label `AGENTS.md`, cluster `agents-models`, placed right after `agents`.

- [ ] **Step 1: Update the nav inventory test**

In `cmd/evener-hub/frontend/src/panes/settings/sections.test.ts`:
- `test("has exactly 18 sections"` → `19`, `toHaveLength(19)`.
- The Agent setup test: title becomes `'the "Agent setup" cluster has exactly these 5 sections, in order, right after the ungrouped ones'`; expected labels `["Providers & credentials", "Agents", "AGENTS.md", "Evener launch", "In-repo config"]`; slice `slice(6, 11)`.
- Extensions test: `slice(11, 15)`.
- Daemon test: `slice(15, 19)`.

Run: `npx vitest run src/panes/settings/sections.test.ts 2>&1 | tail -8`
Expected: 4 failures (count, three slices).

- [ ] **Step 2: Add the nav entry**

In `sections.ts`, after `{ id: "agents", label: "Agents", cluster: "agents-models" },` add:

```ts
  { id: "agents-md", label: "AGENTS.md", cluster: "agents-models" },
```

Run the same test file: all pass.

- [ ] **Step 3: Write the failing section tests**

Create `cmd/evener-hub/frontend/src/panes/settings/sections/agentsDoc.test.tsx`:

```tsx
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { AgentsDocResponse, AnyNotification } from "../../../protocol/types.gen";
import { resetAgentsDocStoreForTests } from "../../../stores/agentsDoc";
import { connectionStore } from "../../../stores/connection";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { AgentsDocSection } from "./agentsDoc";

const DOC: AgentsDocResponse = { path: "/home/u/.config/evener/AGENTS.md", exists: true, content: "# hi\n" };

function connectFakeClient(doc: AgentsDocResponse = DOC): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/settings/agentsDoc/get", () => doc);
  fake.on("evener/settings/agentsDoc/set", (params) => ({ ...doc, exists: true, content: params.content }));
  connectionStore.getState().connect(fake);
  return fake;
}

function renderSection() {
  return render(
    <>
      <Toast />
      <AgentsDocSection sectionId="agents-md" />
    </>,
  );
}

function editor(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "AGENTS.md contents" }) as HTMLTextAreaElement;
}

function saveButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("loads the file on mount and shows its path and content", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  expect(screen.getByText("/home/u/.config/evener/AGENTS.md")).toBeTruthy();
});

test("a missing file renders an empty editor, not an error", async () => {
  connectFakeClient({ path: "/home/u/.config/evener/AGENTS.md", exists: false, content: "" });
  renderSection();
  await waitFor(() => expect(editor().value).toBe(""));
  expect(screen.queryByText(/Failed to load/)).toBeNull();
});

test("a failed load shows the error", async () => {
  const fake = new FakeClient("ready");
  fake.on("evener/settings/agentsDoc/get", () => {
    throw new Error("disk on fire");
  });
  connectionStore.getState().connect(fake);
  renderSection();
  await waitFor(() => expect(screen.getByText(/Failed to load/)).toBeTruthy());
});

test("Save and Revert are disabled until the draft differs from the loaded file", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  expect(saveButton().disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Revert" }) as HTMLButtonElement).disabled).toBe(true);

  const user = userEvent.setup();
  await user.type(editor(), "more");
  expect(saveButton().disabled).toBe(false);
  expect((screen.getByRole("button", { name: "Revert" }) as HTMLButtonElement).disabled).toBe(false);
});

test("Revert restores the loaded content", async () => {
  connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(screen.getByRole("button", { name: "Revert" }));
  expect(editor().value).toBe("# hi\n");
  expect(saveButton().disabled).toBe(true);
});

test("Save sends the draft, toasts, and the editor is clean afterwards", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(getToasts().some((t) => t.text === "Saved AGENTS.md")).toBe(true));
  const set = fake.calls.find((c) => c.method === "evener/settings/agentsDoc/set");
  expect(set?.params).toEqual({ content: "# hi\nmore" });
  expect(saveButton().disabled).toBe(true);
  expect(editor().value).toBe("# hi\nmore");
});

test("a failed save shows the error inline and keeps the draft", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/agentsDoc/set", () => {
    throw new Error("read-only file system");
  });
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  await user.click(saveButton());
  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only file system"));
  expect(editor().value).toBe("# hi\nmore");
  expect(saveButton().disabled).toBe(false);
});

test("a changed broadcast while the draft is clean replaces the editor", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  act(() => {
    fake.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "# from the TUI\n" },
    } as AnyNotification);
  });
  await waitFor(() => expect(editor().value).toBe("# from the TUI\n"));
  expect(screen.queryByText(/changed on disk/)).toBeNull();
});

test("a changed broadcast while the draft is dirty keeps the draft and offers Load current", async () => {
  const fake = connectFakeClient();
  renderSection();
  await waitFor(() => expect(editor().value).toBe("# hi\n"));
  const user = userEvent.setup();
  await user.type(editor(), "more");
  act(() => {
    fake.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "# from the TUI\n" },
    } as AnyNotification);
  });
  await waitFor(() => expect(screen.getByText(/changed on disk/)).toBeTruthy());
  expect(editor().value).toBe("# hi\nmore");

  await user.click(screen.getByRole("button", { name: "Load current" }));
  expect(editor().value).toBe("# from the TUI\n");
  expect(screen.queryByText(/changed on disk/)).toBeNull();
  expect(saveButton().disabled).toBe(true);
});
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `npx vitest run src/panes/settings/sections/agentsDoc.test.tsx 2>&1 | tail -8`
Expected: FAIL, cannot resolve `./agentsDoc`.

- [ ] **Step 5: Implement the section and its stylesheet**

Create `cmd/evener-hub/frontend/src/panes/settings/sections/agentsDoc.module.css`:

```css
.root {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 720px;
}

.help {
  margin: 0;
  color: var(--ink-mid);
  font-size: var(--font-size-ui);
}

.error {
  margin: 0;
  color: var(--ink-hi);
  font-size: var(--font-size-ui);
}

/* The file is Markdown the user hand-edits elsewhere too, so it reads in
 * mono here as it does in their editor. The shared Textarea has no font
 * prop; scoping the face to this section's own control is the narrowest
 * override. */
.editor textarea {
  width: 100%;
  font-family: var(--font-mono);
}

.note {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin: 0;
  color: var(--ink-mid);
  font-size: var(--font-size-ui);
}

.actions {
  display: flex;
  gap: var(--space-2);
}
```

Create `cmd/evener-hub/frontend/src/panes/settings/sections/agentsDoc.tsx`:

```tsx
// Settings -> AGENTS.md: the personal instructions file every session loads
// ahead of the repo's own project docs (spec 2026-09-07 §1). A whole-file
// editor over stores/agentsDoc.ts: Save is dirty-gated, Revert restores the
// last loaded content, and there is no conflict precondition by design (last
// write wins) - what the section does instead is tell the user when the file
// changed under a dirty draft and let them load the current version.
//
// `baseline` is the content the draft was last synced to; `dirty` is
// draft !== baseline. A hub push (the `doc` subscription) syncs the draft
// only while it is clean. A push whose content matches neither the baseline
// nor the draft while dirty is someone else's write, and flips `stale` -
// this client's own save lands with content equal to the draft, so it never
// reads as stale.
import { useEffect, useId, useState } from "react";
import { friendlyErrorMessage } from "../../../protocol/errors";
import { agentsDocStore, useAgentsDocStore } from "../../../stores/agentsDoc";
import { Button, Skeleton, Textarea, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./agentsDoc.module.css";
import { Code } from "./settingsField";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "agentsDoc.module.css", "root"),
  help: requireClass(styles.help, "agentsDoc.module.css", "help"),
  error: requireClass(styles.error, "agentsDoc.module.css", "error"),
  editor: requireClass(styles.editor, "agentsDoc.module.css", "editor"),
  note: requireClass(styles.note, "agentsDoc.module.css", "note"),
  actions: requireClass(styles.actions, "agentsDoc.module.css", "actions"),
};

export interface AgentsDocSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

export function AgentsDocSection(_props: AgentsDocSectionProps) {
  const doc = useAgentsDocStore((s) => s.doc);
  const loading = useAgentsDocStore((s) => s.loading);
  const error = useAgentsDocStore((s) => s.error);
  const [draft, setDraft] = useState("");
  const [baseline, setBaseline] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const toast = useToasts();
  const fieldId = useId();

  useConnectedEffect(() => agentsDocStore.getState().fetch(), []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: doc is the only trigger; draft and baseline are read at that moment, not watched
  useEffect(() => {
    if (doc === null) return;
    if (baseline === null || draft === baseline) {
      setDraft(doc.content);
      setBaseline(doc.content);
      setStale(false);
      return;
    }
    if (doc.content !== baseline && doc.content !== draft) setStale(true);
  }, [doc]);

  const dirty = baseline !== null && draft !== baseline;

  async function handleSave(): Promise<void> {
    setSaving(true);
    setSaveError(null);
    try {
      const saved = await agentsDocStore.getState().save(draft);
      setBaseline(saved.content);
      setStale(false);
      toast.push("success", "Saved AGENTS.md");
    } catch (err) {
      setSaveError(friendlyErrorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  function handleRevert(): void {
    if (baseline !== null) setDraft(baseline);
  }

  function handleLoadCurrent(): void {
    if (doc === null) return;
    setDraft(doc.content);
    setBaseline(doc.content);
    setStale(false);
  }

  if (doc === null && loading) return <Skeleton />;
  if (doc === null && error) return <p className={CLASS.error}>Failed to load: {friendlyErrorMessage(error)}</p>;
  if (doc === null) return null; // not yet fetched: the connected effect hasn't run

  return (
    <div className={CLASS.root}>
      <h2>AGENTS.md</h2>
      <p className={CLASS.help}>
        Personal instructions loaded into every session, ahead of the repo's own AGENTS.md. Stored at{" "}
        <Code>{doc.path}</Code>.
      </p>
      <div className={CLASS.editor}>
        <Textarea
          id={fieldId}
          aria-label="AGENTS.md contents"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          minLines={16}
          autoGrow
          disabled={saving}
          placeholder="# Instructions for every session"
        />
      </div>
      {stale && (
        <p className={CLASS.note} role="status">
          AGENTS.md changed on disk while you were editing.
          <Button size="sm" variant="quiet" onClick={handleLoadCurrent}>
            Load current
          </Button>
        </p>
      )}
      {saveError !== null && (
        <p className={CLASS.error} role="alert">
          Save failed: {saveError}
        </p>
      )}
      <div className={CLASS.actions}>
        <Button onClick={() => void handleSave()} disabled={!dirty || saving}>
          Save
        </Button>
        <Button variant="quiet" onClick={handleRevert} disabled={!dirty || saving}>
          Revert
        </Button>
      </div>
    </div>
  );
}
```

If `Button` does not accept `size="sm"` with `variant="quiet"` together (check `widgets/button/index.tsx`'s `ButtonProps`), drop `size` rather than invent a prop.

- [ ] **Step 6: Dispatch the section**

In `cmd/evener-hub/frontend/src/panes/settings/Settings.tsx`, add the import beside `AgentsSection`:

```ts
import { AgentsDocSection } from "./sections/agentsDoc";
```

and in `SECTION_COMPONENTS` after `agents: AgentsSection,` add:

```ts
  "agents-md": AgentsDocSection,
```

- [ ] **Step 7: Run the section tests, then the full frontend gate**

Run: `npx biome check --write src/panes/settings/sections/agentsDoc.tsx src/panes/settings/sections/agentsDoc.module.css src/panes/settings/sections/agentsDoc.test.tsx src/panes/settings/sections.ts src/panes/settings/sections.test.ts src/panes/settings/Settings.tsx && npx vitest run src/panes/settings 2>&1 | tail -8`
Expected: all settings tests pass.

Then from the repo root: `make test-web 2>&1 | tail -15`
Expected: typecheck, unit tests, and Biome all green. Read the output for `FAIL` rather than trusting the exit code of a piped command.

- [ ] **Step 8: Commit**

```bash
git add src/panes/settings/sections/agentsDoc.tsx src/panes/settings/sections/agentsDoc.module.css src/panes/settings/sections/agentsDoc.test.tsx src/panes/settings/sections.ts src/panes/settings/sections.test.ts src/panes/settings/Settings.tsx
git commit -m "feat(web): AGENTS.md settings section"
```

---

### Task 6: Docs, gates, and the pull request

**Files:**
- Modify: `docs/evener-hub.md` (the paragraph around line 230 that names `providers.toml` under the config root; add one sentence that `AGENTS.md` beside it holds personal instructions loaded ahead of every repo's project docs, editable under Settings → AGENTS.md)
- Modify: `docs/llm-providers.md` is NOT touched; `docs/appwire-protocol.md` was regenerated in Task 2.

- [ ] **Step 1: Document the file**

In `docs/evener-hub.md`, find the sentence that describes the config root contents (grep `providers.toml` around line 230) and add, in the same paragraph:

```
`AGENTS.md` beside it holds your personal standing instructions; every
session loads it ahead of the repo's own AGENTS.md, and Settings → AGENTS.md
edits it in place.
```

- [ ] **Step 2: Run every gate**

From the repo root, one at a time, and read each output for failures:

```bash
make lint 2>&1 | tail -20
make vet 2>&1 | tail -5
make test 2>&1 | tail -20
```

Expected: all green. `make lint` includes the generated-output freshness check, which passes because Task 2 committed the regenerated files. If `make test` reports one of the known flaky tests from the memory file (drain-abandon cleanup race, execenv, procgroup, drain-continue), re-run that package once and report which it was.

- [ ] **Step 3: Live check against a throwaway hub**

Follow the WebUI screenshot recipe in memory (`webui-screenshot-recipe.md`): start a throwaway hub with `XDG_CONFIG_HOME` pointed at a temp dir, open Settings → AGENTS.md in the browser pane, type a line, Save, and confirm with `cat "$XDG_CONFIG_HOME/evener/AGENTS.md"` that the file holds exactly what was typed. Then start a session in a temp repo and confirm the transcript's system-prompt view (or `evener` debug prompt dump, if the recipe records one) shows a `----- BEGIN ~/.config/evener/AGENTS.md -----` block before the repo's block. Attach a desktop and a phone-width screenshot of the section to the PR.

- [ ] **Step 4: Commit and open the PR**

```bash
git add docs/evener-hub.md
git commit -m "docs(hub): describe the personal AGENTS.md"
git push -u origin HEAD
gh pr create --repo prime-radiant-inc/evener --base main --title "Personal AGENTS.md: loaded into every session, editable in settings" --body-file - <<'EOF'
Slice 1 of docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md.

- The daemon prepends `~/.config/evener/AGENTS.md` to every session's project docs, sharing the existing 32KB budget.
- New `evener/settings/agentsDoc/{get,set}` methods and a `changed` broadcast (additive, v4).
- New Settings → AGENTS.md section: monospace editor, dirty-gated Save, Revert, and a "changed on disk" notice with Load current.

Gates: make lint, make vet, make test, make test-web. Screenshots attached.
EOF
```

Then watch the PR's CI and roborev comments; a finding that recurs on every push has to be fixed in code, not declined (memory: roborev findings block merge).
