# Serf TUI First-Class Hub Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild `serf-tui` as a polished Bubble Tea hub client with dashboard, project drilldown, session workspace, command parity, Serf-owned OpenAI OAuth, spawn/model discovery, transcript browse/fork, and end-to-end tmux coverage.

**Architecture:** Keep `serf-tui` hub-backed only. Split the current hub model into a top-level app shell plus focused Dashboard, Project, Session, Auth, Spawn, Palette, Command Registry, and Styles modules. All backend work goes through `internal/hubapi.Client`; no direct daemon or filesystem discovery from the TUI. The `serf-hub` branch predates the main-line OpenAI auth commits, so integrate those commits into this branch and expose auth through the hub rather than having the TUI read auth files directly.

**Tech Stack:** Go, Bubble Tea, Bubbles, Lip Gloss, `httptest`, `tmux`, Serf hub JSON/SSE APIs.

---

## Pre-Execution Rules

- Start from a git branch/worktree where unrelated dirty work is either committed, stashed, or explicitly left alone by file path.
- Do not use `git add -A`.
- Do not remove old embedded/direct code until parity tests prove the hub TUI covers its user-facing behavior.
- Do not redesign OpenAI OAuth from scratch. Bring forward the main-line implementation and adapt it unless a specific piece is proven technically obsolete.
- Do not implement backward compatibility for old `--provider` or old `[[spawn_template]]` unless Jesse explicitly asks.
- Do not reuse Codex auth state, `~/.codex/auth.json`, browser cookies, or any product-owned credentials outside Serf.
- Every task below starts with tests, watches them fail, implements the smallest fix, runs targeted tests, and commits.

## Branch And Auth Integration Facts

- `serf-hub` branched from `main` at `d3114c9` on 2026-05-07.
- The Serf-owned OpenAI auth stack landed on `main` after that branch point on 2026-05-08.
- Source commits to integrate:
  - `c72f4b1 feat: add OpenAI auth foundations`
  - `9772717 fix: validate persisted OpenAI auth records`
  - `33f5efd feat: add OpenAI auth token and callback flows`
  - `377b7d4 feat: route OpenAI OAuth traffic via Codex transport`
  - `2c2712a feat: add openai auth service and claims`
  - `62ade8b Add explicit OpenAI auth CLI commands`
  - `08ca76c fix: harden OpenAI OAuth refresh handling`
  - `24d6111 fix: support immediate OpenAI pasteback login`
  - `7795345 fix: align OpenAI authorize URL with Codex flow`
  - `8c5089e fix: match Pi OpenAI login flow`
  - `6885106 fix: stream OAuth-backed OpenAI responses`
  - `0bf4745 fix: stabilize OpenAI OAuth streamed tool calls`
  - `01e93ba Add TUI-local OpenAI auth helper`
  - `84c92a8 feat: add OpenAI login flow to serf-tui`
  - `f8a17f4 fix: refresh TUI OpenAI auth status on demand`
  - `48933a9 fix: harden TUI OpenAI auth lifecycle`
  - `018a67a refactor: route TUI auth through provider context`
  - `a4aa58a fix: wait for embedded TUI server readiness`
  - `715f06a fix: keep TUI async SSE consumption armed`
- Treat missing auth in this branch as a stale-branch integration gap, not as a deleted feature.
- Prefer a merge from current local `main` if conflicts are manageable. If a full merge drags unrelated risk into the TUI branch, cherry-pick the auth commits in order and document any skipped commits with exact rationale in the parity checklist.

## File Structure

Create or reshape these TUI files:

- Create: `cmd/serf-tui/app_model.go` - top-level Bubble Tea shell, mode routing, global keys, shared status/error.
- Create: `cmd/serf-tui/app_modes.go` - mode enum, navigation helpers, return targets.
- Integrate/adapt from main: `cmd/serf-tui/openai_auth.go` - TUI auth status/login/logout orchestration over hub APIs.
- Create: `cmd/serf-tui/command_registry.go` - command definitions, slash parser integration, help generator, palette source.
- Create: `cmd/serf-tui/command_registry_test.go` - command/help/parity tests.
- Create: `cmd/serf-tui/dashboard_model.go` - dashboard state, selection, refresh handling.
- Create: `cmd/serf-tui/dashboard_view.go` - dashboard rendering.
- Create: `cmd/serf-tui/dashboard_model_test.go` - live-only grouping/sorting tests.
- Create: `cmd/serf-tui/project_model.go` - project drilldown state and rendering.
- Create: `cmd/serf-tui/project_model_test.go` - project live/recent ordering tests.
- Create: `cmd/serf-tui/session_surface.go` - hub-native session workspace state/update/view.
- Create: `cmd/serf-tui/session_surface_test.go` - slash command, browse, fork, action tests.
- Create: `cmd/serf-tui/session_reducer.go` - transcript replay/live event reducer.
- Create: `cmd/serf-tui/session_reducer_test.go` - replay/live dedupe and ordering tests.
- Create: `cmd/serf-tui/spawn_model.go` - spawn form state/update/view.
- Create: `cmd/serf-tui/spawn_model_test.go` - spawn form model load, model picker, submit tests.
- Create: `cmd/serf-tui/palette_model.go` - command/search palette.
- Create: `cmd/serf-tui/palette_model_test.go` - palette source and dispatch tests.
- Create: `cmd/serf-tui/styles.go` - Lip Gloss tokens, theme status styles, layout helpers.

Modify these existing files:

- Modify: `cmd/serf-tui/main.go` - boot `appModel`, not the old `hubModel` directly.
- Modify: `cmd/serf-tui/hub_commands.go` - keep typed async commands; add the replay/follow/model/task helpers named in Tasks 8 and 9.
- Modify: `cmd/serf-tui/hub_model.go` - shrink or delete after replacement; do not keep duplicated command logic.
- Modify: `cmd/serf-tui/model.go` - preserve reusable rendering/data structures only; remove direct daemon ownership after parity.
- Modify: `cmd/serf-tui/input.go` - keep slash parsing if useful, but move help generation to command registry.
- Modify: `cmd/serf-tui/history.go` - preserve persisted input history semantics in the new session composer.
- Modify: `cmd/serf-tui/theme_picker.go` and `cmd/serf-tui/theme_test.go` - keep real theme picker behavior and persistence.
- Modify: `cmd/serf-tui/transcript_view.go` - preserve main/subagent transcript picker behavior behind hub-backed data.
- Modify: `cmd/serf-tui/tmux_e2e_test.go` - expand full terminal journey coverage.
- Modify: `internal/hubapi/client.go` and `internal/hubapi/types.go` - add the typed endpoints listed in the spec: project, replay transcript, transcript follow, tasks, details, subagent transcripts, auth, steer, fork, clear, and model change.
- Modify: `cmd/serf-hub/web.go` and tests - only for hub API gaps required by TUI.
- Integrate/adapt from main: `internal/auth/openai/config.go`, `pkce.go`, `server.go`, `manual.go`, `tokens.go`, `storage.go`, `claims.go`, `service.go` - Serf-owned OpenAI OAuth lifecycle.
- Integrate/adapt from main: `internal/auth/openai/*_test.go` - auth storage, PKCE, manual pasteback, callback, token exchange, refresh, and status tests.
- Integrate/adapt from main: `cmd/serf/openai_login.go`, `cmd/serf/openai_logout.go`, `cmd/serf/openai_status.go` - canonical CLI auth commands.
- Modify: `cmd/serf/main.go` - dispatch the OpenAI command family.
- Modify: `llm/providers/openai/adapter.go` and tests - resolve env key first, then Serf OAuth, with refresh.

## Task 0: Integrate Main-Line OpenAI Auth And Build The Parity Inventory

**Files:**
- Create: `docs/superpowers/notes/2026-05-11-serf-tui-parity-checklist.md`
- Integrate/modify: `internal/auth/openai/*`
- Integrate/modify: `cmd/serf/openai_*.go`
- Integrate/modify: `cmd/serf-tui/openai_auth.go`
- Modify: `cmd/serf/main.go`
- Modify: `cmd/serf-tui/main.go`
- Modify: `cmd/serf-tui/model.go`
- Modify: `cmd/serf-tui/input.go`
- Modify: `cmd/serf-tui/sse_client.go`
- Modify: `cmd/serf-tui/statusbar.go`
- Modify: `llm/providers/openai/adapter.go`
- Modify: `cmd/serf-tui/tmux_e2e_test.go`

- [ ] **Step 1: Prove the auth gap is branch-base drift**

Run:

```bash
mb=$(git merge-base HEAD main)
git show -s --format='%h %ci %s' "$mb"
for c in c72f4b1 62ade8b 84c92a8 f8a17f4 48933a9; do
	git merge-base --is-ancestor "$c" "$mb"
	echo "$c ancestor_of_merge_base=$?"
	git merge-base --is-ancestor "$c" main
	echo "$c ancestor_of_main=$?"
	git merge-base --is-ancestor "$c" HEAD
	echo "$c ancestor_of_head=$?"
done
```

Expected:

```text
d3114c9 ... fix(agent,cmdutil): symmetric WithModel dispatch and provider case normalization
c72f4b1 ancestor_of_merge_base=1
c72f4b1 ancestor_of_main=0
c72f4b1 ancestor_of_head=1
...
48933a9 ancestor_of_merge_base=1
48933a9 ancestor_of_main=0
48933a9 ancestor_of_head=1
```

Interpretation:

- exit `0` means the commit is present in that target.
- exit `1` means it is absent.
- The auth commits are present in local `main`, absent from the `serf-hub` merge base, and absent from current `HEAD`.
- This means the branch predated auth; do not describe this as a dropped/deleted feature.

- [ ] **Step 2: Create the parity checklist**

Create a parity checklist that maps each old user-visible behavior to one of: `restored`, `replaced by hub-native equivalent`, or `explicitly removed with rationale`.

Required rows:

- Startup flags: `--hub-addr`, `--hub-bin`, `--no-auto-start-hub`, `--log-file`, `--debug`, and `--state-dir`.
- Slash commands: `/help`, `/dashboard`, `/project`, `/projects`, `/new`, `/search`, `/steer`, `/compact`, `/status`, `/details`, `/tasks`, `/agents`, `/model`, `/clear`, `/interrupt`, `/fork`, `/theme`, `/auth`, `/login openai`, `/logout openai`, `/quit`.
- Input behavior: send, multiline, persisted history, send-failure draft preservation, busy `/input` handling, steering.
- Pickers: model picker, theme picker, agent/subagent transcript picker, command palette.
- Details/status sections: tools, MCP servers, skills, plugins, hooks, agents, subagents, tasks, context/tokens, auth, capabilities.
- Transcript events: session start/end, user input, steering, assistant text deltas, communicate events, tool lifecycle, subagent start/end, context updates, token updates, task updates, errors.
- Spawn fields: task, model, directory, project, agent, reasoning effort.
- Auth source commits: `c72f4b1`, `62ade8b`, `84c92a8`, `f8a17f4`, `48933a9`, plus follow-up auth commits listed in "Branch And Auth Integration Facts".

- [ ] **Step 3: Verify the current branch lacks the auth stack**

Run:

```bash
test ! -d internal/auth/openai
rg -n "openai login|OpenAIAuth|oauth|auth/openai" cmd/serf cmd/serf-tui llm/providers/openai
```

Expected:

```text
test ! -d internal/auth/openai
```

exits `0`, and `rg` shows only env-key OpenAI adapter references. If `internal/auth/openai` already exists because a prior task integrated it, replace this step with:

```bash
go test ./internal/auth/openai -count=1
```

- [ ] **Step 4: Integrate auth source commits from local `main`**

Preferred path: merge local `main` into the implementation branch, because `main` already contains the auth stack plus later hardening fixes.

Run:

```bash
git status --short
git merge --no-ff main
```

Expected:

- If conflicts are small and mostly in `cmd/serf-tui`, resolve them by preserving hub-backed TUI architecture and main-line auth behavior.
- If conflicts are broad outside auth/TUI/provider files, abort only the merge operation with `git merge --abort`, then use the cherry-pick path below. Do not reset the worktree.

Cherry-pick fallback:

```bash
git cherry-pick -n \
	c72f4b1 9772717 33f5efd 377b7d4 2c2712a 62ade8b \
	08ca76c 24d6111 7795345 8c5089e 6885106 0bf4745 \
	01e93ba 84c92a8 f8a17f4 48933a9 018a67a a4aa58a 715f06a
```

Expected:

- Auth package, CLI commands, OpenAI adapter auth resolution, and old TUI auth support appear in the worktree.
- Conflicts in `cmd/serf-tui/*` are expected because hub TUI work also changed those files.
- Resolve conflicts in favor of: hub-backed navigation and sessions from `serf-hub`, auth lifecycle and provider-context behavior from `main`.

- [ ] **Step 5: Validate auth foundation after integration**

The integrated auth implementation should provide:

- `internal/auth/openai/config.go`
- `internal/auth/openai/pkce.go`
- `internal/auth/openai/server.go`
- `internal/auth/openai/manual.go`
- `internal/auth/openai/tokens.go`
- `internal/auth/openai/storage.go`
- `internal/auth/openai/claims.go`
- `internal/auth/openai/service.go`

Run:

```bash
go test ./internal/auth/openai -count=1
```

- [ ] **Step 6: Validate canonical CLI commands**

The integrated CLI must provide:

- `serf openai login`
- `serf openai logout`
- `serf openai status`

Tests must cover signed-out, env-auth, stored-auth, logout, callback login, and manual pasteback login.

Run:

```bash
go test ./cmd/serf -run 'OpenAI|Auth|Login|Logout|Status' -count=1
```

- [ ] **Step 7: Validate OpenAI adapter credential resolution**

The OpenAI adapter must resolve credentials in this order:

1. `OPENAI_API_KEY`
2. stored Serf OAuth from resolved state dir
3. refresh stored OAuth when expired or near expiry
4. return a clear re-login error when refresh is permanently invalid

Run:

```bash
go test ./llm/providers/openai -run 'Auth|Credential|Refresh|Authorization' -count=1
```

- [ ] **Step 8: Adapt auth to hub APIs**

Add typed hub API methods and server endpoints:

- `AuthStatus(provider string)`
- `AuthLoginBegin(provider string)`
- `AuthLoginComplete(provider string, redirectURL string)`
- `AuthLogout(provider string)`

Hub tests must prove the hub never returns token secrets and never reads Codex credential state.

Run:

```bash
go test ./internal/hubapi ./cmd/serf-hub -run 'Auth|OpenAI' -count=1
```

- [ ] **Step 9: Adapt TUI auth UX over hub APIs**

`serf-tui` must support:

- `/auth`
- `/auth openai`
- `/login openai`
- `/logout openai`
- dashboard auth summary
- spawn/model auth-required disabled reasons
- clear errors when login is required or refresh failed

Run:

```bash
go test ./cmd/serf-tui -run 'Auth|OpenAI|CommandRegistry' -count=1
```

- [ ] **Step 10: Add auth E2E coverage**

Extend the fake hub/tmux harness to cover:

- dashboard shows OpenAI signed-out state
- `/login openai` shows authorize URL and accepts pasteback
- auth status updates to signed-in
- spawn with OpenAI model is disabled before auth and enabled after auth
- `/logout openai` returns to signed-out state

Run:

```bash
go test ./cmd/serf-tui -run TestTUIE2E_OpenAIAuth -count=1
```

- [ ] **Step 11: Commit**

```bash
git add docs/superpowers/notes/2026-05-11-serf-tui-parity-checklist.md internal/auth/openai cmd/serf/openai_login.go cmd/serf/openai_logout.go cmd/serf/openai_status.go cmd/serf/main.go cmd/serf-tui/openai_auth.go cmd/serf-tui/tmux_e2e_test.go llm/providers/openai/adapter.go
git commit -m "feat(serf): integrate Serf-owned OpenAI auth"
```

## Task 1: Lock The Current Gaps With Command Registry Tests

**Files:**
- Create: `cmd/serf-tui/command_registry.go`
- Create: `cmd/serf-tui/command_registry_test.go`
- Modify: `cmd/serf-tui/input.go`

- [ ] **Step 1: Write failing tests for command/help parity**

Create `cmd/serf-tui/command_registry_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestCommandRegistryHelpOnlyListsImplementedCommands(t *testing.T) {
	reg := defaultCommandRegistry()
	help := reg.Help(commandContext{Scope: commandScopeSession})

	for _, name := range []string{
		"help", "auth", "login", "logout", "dashboard", "project",
		"projects", "new", "search", "steer", "compact", "status",
		"details", "tasks", "agents", "model", "clear", "interrupt",
		"fork", "theme", "quit",
	} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("registry missing /%s", name)
		}
		if !strings.Contains(help, "/"+name) {
			t.Fatalf("help missing /%s:\n%s", name, help)
		}
	}
}

func TestCommandRegistryRejectsAdvertisedButUnimplementedCommands(t *testing.T) {
	reg := defaultCommandRegistry()
	help := reg.Help(commandContext{Scope: commandScopeSession})
	for _, line := range strings.Split(help, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "/") {
			continue
		}
		name := strings.Fields(strings.TrimPrefix(line, "/"))[0]
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("help advertises unimplemented command /%s", name)
		}
	}
}

func TestParseCommandInvocation(t *testing.T) {
	got := parseCommandInvocation("/model openai/gpt-5.2")
	if got.Name != "model" || got.Args != "openai/gpt-5.2" {
		t.Fatalf("parse: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./cmd/serf-tui -run 'TestCommandRegistry' -count=1
```

Expected: fail because `defaultCommandRegistry`, `commandContext`, and related types do not exist.

- [ ] **Step 3: Add minimal registry types**

Create `cmd/serf-tui/command_registry.go`:

```go
package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type commandScope string

const (
	commandScopeGlobal    commandScope = "global"
	commandScopeDashboard commandScope = "dashboard"
	commandScopeProject   commandScope = "project"
	commandScopeSession   commandScope = "session"
	commandScopeSpawn     commandScope = "spawn"
)

type commandContext struct {
	Scope        commandScope
	CanSend      bool
	CanInterrupt bool
	CanCompact   bool
	CanClear     bool
	CanModel     bool
	CanFork      bool
	CanSteer     bool
	CanAuth      bool
}

type commandInvocation struct {
	Name string
	Args string
}

type commandEntry struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Scopes      []commandScope
	KeyHint     string
	Visible     func(commandContext) bool
	Enabled     func(commandContext) (bool, string)
	Run         func(*appModel, commandInvocation) tea.Cmd
}

type commandRegistry struct {
	entries []commandEntry
	byName  map[string]commandEntry
}

func defaultCommandRegistry() commandRegistry {
	entries := []commandEntry{
		{Name: "help", Description: "Show command help", Usage: "/help", Scopes: allCommandScopes()},
		{Name: "auth", Description: "Show provider auth status", Usage: "/auth [provider]", Scopes: allCommandScopes()},
		{Name: "login", Description: "Log in to a provider", Usage: "/login openai", Scopes: allCommandScopes()},
		{Name: "logout", Description: "Log out from a provider", Usage: "/logout openai", Scopes: allCommandScopes()},
		{Name: "dashboard", Description: "Go to live dashboard", Usage: "/dashboard", Scopes: allCommandScopes(), KeyHint: "ctrl+o"},
		{Name: "project", Description: "Open this session's project", Usage: "/project", Scopes: []commandScope{commandScopeSession}},
		{Name: "projects", Description: "Open project picker", Usage: "/projects", Scopes: allCommandScopes()},
		{Name: "new", Aliases: []string{"spawn"}, Description: "Create a new session", Usage: "/new", Scopes: allCommandScopes(), KeyHint: "n"},
		{Name: "search", Description: "Search commands and sessions", Usage: "/search", Scopes: allCommandScopes(), KeyHint: "/"},
		{Name: "steer", Description: "Send guidance to a busy session", Usage: "/steer <message>", Scopes: []commandScope{commandScopeSession}},
		{Name: "compact", Description: "Compact current session", Usage: "/compact", Scopes: []commandScope{commandScopeSession}},
		{Name: "status", Description: "Show session status", Usage: "/status", Scopes: []commandScope{commandScopeSession}},
		{Name: "details", Description: "Show session details", Usage: "/details", Scopes: []commandScope{commandScopeSession}},
		{Name: "tasks", Description: "Show task list", Usage: "/tasks", Scopes: []commandScope{commandScopeSession}},
		{Name: "agents", Description: "Show main and subagent transcripts", Usage: "/agents", Scopes: []commandScope{commandScopeSession}},
		{Name: "model", Description: "Pick or switch model", Usage: "/model [provider/model]", Scopes: []commandScope{commandScopeSession, commandScopeSpawn}},
		{Name: "clear", Description: "Start a new session from here", Usage: "/clear", Scopes: []commandScope{commandScopeSession}},
		{Name: "interrupt", Description: "Interrupt current session", Usage: "/interrupt", Scopes: []commandScope{commandScopeSession}, KeyHint: "ctrl+c"},
		{Name: "fork", Description: "Fork from selected user turn", Usage: "/fork", Scopes: []commandScope{commandScopeSession}},
		{Name: "theme", Description: "Pick theme", Usage: "/theme", Scopes: allCommandScopes()},
		{Name: "quit", Description: "Exit the TUI", Usage: "/quit", Scopes: allCommandScopes(), KeyHint: "q"},
	}
	return newCommandRegistry(entries)
}

func allCommandScopes() []commandScope {
	return []commandScope{commandScopeGlobal, commandScopeDashboard, commandScopeProject, commandScopeSession, commandScopeSpawn}
}

func newCommandRegistry(entries []commandEntry) commandRegistry {
	reg := commandRegistry{entries: entries, byName: map[string]commandEntry{}}
	for _, entry := range entries {
		reg.byName[entry.Name] = entry
		for _, alias := range entry.Aliases {
			reg.byName[alias] = entry
		}
	}
	return reg
}

func (r commandRegistry) Lookup(name string) (commandEntry, bool) {
	entry, ok := r.byName[strings.TrimSpace(name)]
	return entry, ok
}

func (r commandRegistry) Help(ctx commandContext) string {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, entry := range r.entries {
		if !entry.visibleIn(ctx) {
			continue
		}
		b.WriteString("  ")
		b.WriteString(entry.Usage)
		if entry.Description != "" {
			b.WriteString("  ")
			b.WriteString(entry.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (e commandEntry) visibleIn(ctx commandContext) bool {
	if e.Visible != nil && !e.Visible(ctx) {
		return false
	}
	if len(e.Scopes) == 0 {
		return true
	}
	for _, scope := range e.Scopes {
		if scope == commandScopeGlobal || scope == ctx.Scope {
			return true
		}
	}
	return false
}

func parseCommandInvocation(input string) commandInvocation {
	cmd, args := parseSlashCommand(input)
	return commandInvocation{Name: cmd, Args: args}
}
```

- [ ] **Step 4: Run tests and verify pass**

Run:

```bash
go test ./cmd/serf-tui -run 'TestCommandRegistry|TestParseCommandInvocation' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/command_registry.go cmd/serf-tui/command_registry_test.go
git commit -m "test(serf-tui): define hub command registry contract"
```

## Task 2: Add The App Shell And Mode Routing

**Files:**
- Create: `cmd/serf-tui/app_modes.go`
- Create: `cmd/serf-tui/app_model.go`
- Modify: `cmd/serf-tui/main.go`
- Test: `cmd/serf-tui/app_model_test.go`

- [ ] **Step 1: Write failing app shell tests**

Create `cmd/serf-tui/app_model_test.go`:

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAppModelStartsOnDashboard(t *testing.T) {
	m := newAppModel(nil, "http://127.0.0.1:9180")
	if m.mode != appModeDashboard {
		t.Fatalf("mode=%v, want dashboard", m.mode)
	}
}

func TestAppModelCtrlOReturnsDashboard(t *testing.T) {
	m := newAppModel(nil, "http://127.0.0.1:9180")
	m.mode = appModeSession
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	got := updated.(appModel)
	if got.mode != appModeDashboard {
		t.Fatalf("mode=%v, want dashboard", got.mode)
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestAppModel -count=1
```

Expected: fail because `appModel` does not exist.

- [ ] **Step 3: Implement minimal app modes**

Create `cmd/serf-tui/app_modes.go`:

```go
package main

type appMode int

const (
	appModeDashboard appMode = iota
	appModeProject
	appModeSession
	appModeSpawn
	appModePalette
)
```

- [ ] **Step 4: Implement minimal app model**

Create `cmd/serf-tui/app_model.go`:

```go
package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/hubapi"
)

type appModel struct {
	client   *hubapi.Client
	hubURL   string
	mode     appMode
	commands commandRegistry
	width    int
	height   int
	err      error
}

func newAppModel(client *hubapi.Client, hubURL string) appModel {
	return appModel{
		client:   client,
		hubURL:   hubURL,
		mode:     appModeDashboard,
		commands: defaultCommandRegistry(),
	}
}

func (m appModel) Init() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return fetchHubTree(m.client)
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlO {
			m.mode = appModeDashboard
			return m, fetchHubTree(m.client)
		}
	}
	return m, nil
}

func (m appModel) View() string {
	return "serf\n\nloading dashboard..."
}
```

- [ ] **Step 5: Wire main to app model**

Modify `cmd/serf-tui/main.go`:

```go
m := newAppModel(runtime.Client, runtime.Address.BaseURL)
```

This replaces:

```go
m := newHubModel(runtime.Client, runtime.Address.BaseURL)
```

- [ ] **Step 6: Verify targeted tests**

Run:

```bash
go test ./cmd/serf-tui -run TestAppModel -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/app_modes.go cmd/serf-tui/app_model.go cmd/serf-tui/app_model_test.go cmd/serf-tui/main.go
git commit -m "feat(serf-tui): add hub app shell"
```

## Task 3: Build The Live Dashboard Component

**Files:**
- Create: `cmd/serf-tui/dashboard_model.go`
- Create: `cmd/serf-tui/dashboard_view.go`
- Create: `cmd/serf-tui/dashboard_model_test.go`
- Modify: `cmd/serf-tui/app_model.go`

- [ ] **Step 1: Write failing live-only dashboard tests**

Create `cmd/serf-tui/dashboard_model_test.go`:

```go
package main

import (
	"testing"

	"primeradiant.com/serf/internal/hubapi"
)

func TestDashboardRowsShowsOnlyLiveSessionsGroupedByProject(t *testing.T) {
	tree := hubapi.TreeResponse{Projects: []hubapi.ProjectNode{{
		Name: "terminal-tetris",
		Path: "/Users/jesse/terminal-tetris",
		Sessions: []hubapi.SessionSummary{
			{Ref: "local:live1", Title: "live", State: "idle", Live: true, Project: "terminal-tetris"},
			{Ref: "local:old1", Title: "ended", State: "ended", Live: false, Project: "terminal-tetris"},
		},
	}}}
	rows := buildDashboardRowsV2(tree)
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want project header + one live session: %+v", len(rows), rows)
	}
	if rows[0].Kind != dashboardRowProject || rows[1].Title != "live" {
		t.Fatalf("rows=%+v", rows)
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestDashboardRowsShowsOnlyLiveSessionsGroupedByProject -count=1
```

Expected: fail because dashboard V2 types do not exist.

- [ ] **Step 3: Implement dashboard row builder**

Create `cmd/serf-tui/dashboard_model.go`:

```go
package main

import (
	"sort"

	"primeradiant.com/serf/internal/hubapi"
)

type dashboardRowKind int

const (
	dashboardRowProject dashboardRowKind = iota
	dashboardRowSession
)

type dashboardRow struct {
	Kind       dashboardRowKind
	Ref        string
	Title      string
	Project    string
	ProjectKey string
	State      string
	Model      string
	Age        string
	Live       bool
}

type dashboardModel struct {
	tree     hubapi.TreeResponse
	rows     []dashboardRow
	selected int
}

func buildDashboardRowsV2(tree hubapi.TreeResponse) []dashboardRow {
	var rows []dashboardRow
	for _, project := range tree.Projects {
		var live []hubapi.SessionSummary
		for _, session := range project.Sessions {
			if session.Live {
				live = append(live, session)
			}
		}
		if len(live) == 0 {
			continue
		}
		sort.SliceStable(live, func(i, j int) bool {
			return sessionAttentionRank(live[i].State) < sessionAttentionRank(live[j].State)
		})
		rows = append(rows, dashboardRow{
			Kind:       dashboardRowProject,
			Title:      project.Name,
			Project:    project.Name,
			ProjectKey: project.Path,
		})
		for _, session := range live {
			rows = append(rows, dashboardRow{
				Kind:       dashboardRowSession,
				Ref:        session.Ref,
				Title:      session.Title,
				Project:    project.Name,
				ProjectKey: project.Path,
				State:      session.State,
				Model:      session.Model,
				Age:        session.Age,
				Live:       true,
			})
		}
	}
	return rows
}

func sessionAttentionRank(state string) int {
	switch state {
	case "waiting", "awaiting", "error":
		return 0
	case "processing", "working":
		return 1
	case "idle":
		return 2
	default:
		return 3
	}
}
```

- [ ] **Step 4: Add dashboard view**

Create `cmd/serf-tui/dashboard_view.go`:

```go
package main

import (
	"fmt"
	"strings"
)

func (m dashboardModel) View(width int) string {
	var b strings.Builder
	b.WriteString("serf\n\n")
	if len(m.rows) == 0 {
		b.WriteString("No live sessions.\n\n")
		b.WriteString("keys: n new  / search  r refresh  q quit\n")
		return b.String()
	}
	b.WriteString("live sessions\n\n")
	for i, row := range m.rows {
		selected := "  "
		if i == m.selected {
			selected = "> "
		}
		if row.Kind == dashboardRowProject {
			fmt.Fprintf(&b, "%s%s\n", selected, row.Title)
			continue
		}
		fmt.Fprintf(&b, "%s%-10s %-32s %s\n", selected, row.State, row.Title, row.Model)
	}
	b.WriteString("\nkeys: enter open  p project  n new  / search  r refresh  ? help  q quit\n")
	return b.String()
}
```

- [ ] **Step 5: Wire dashboard into app model**

Add field to `appModel`:

```go
dashboard dashboardModel
```

Handle `hubTreeMsg` in `app_model.go`:

```go
case hubTreeMsg:
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.dashboard.tree = msg.tree
	m.dashboard.rows = buildDashboardRowsV2(msg.tree)
	if m.dashboard.selected >= len(m.dashboard.rows) {
		m.dashboard.selected = len(m.dashboard.rows) - 1
	}
	if m.dashboard.selected < 0 {
		m.dashboard.selected = 0
	}
	return m, nil
```

Update `View`:

```go
if m.mode == appModeDashboard {
	return m.dashboard.View(m.width)
}
```

- [ ] **Step 6: Verify**

Run:

```bash
go test ./cmd/serf-tui -run 'TestDashboard|TestAppModel' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/dashboard_model.go cmd/serf-tui/dashboard_view.go cmd/serf-tui/dashboard_model_test.go cmd/serf-tui/app_model.go
git commit -m "feat(serf-tui): render live dashboard"
```

## Task 4: Add Project Drilldown

**Files:**
- Create: `cmd/serf-tui/project_model.go`
- Create: `cmd/serf-tui/project_model_test.go`
- Modify: `cmd/serf-tui/app_model.go`
- Modify: `cmd/serf-tui/dashboard_model.go`

- [ ] **Step 1: Write failing project drilldown tests**

Create `cmd/serf-tui/project_model_test.go`:

```go
package main

import (
	"testing"

	"primeradiant.com/serf/internal/hubapi"
)

func TestProjectRowsShowsLiveThenRecentEnded(t *testing.T) {
	project := hubapi.ProjectNode{
		Name: "terminal-tetris",
		Path: "/tmp/terminal-tetris",
		Sessions: []hubapi.SessionSummary{
			{Ref: "local:old", Title: "old", State: "ended", Live: false},
			{Ref: "local:live", Title: "live", State: "idle", Live: true},
		},
	}
	rows := buildProjectRows(project)
	if len(rows) != 4 {
		t.Fatalf("rows=%d, want live header/session + recent header/session: %+v", len(rows), rows)
	}
	if rows[1].Title != "live" || rows[3].Title != "old" {
		t.Fatalf("rows=%+v", rows)
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestProjectRowsShowsLiveThenRecentEnded -count=1
```

Expected: fail because project model does not exist.

- [ ] **Step 3: Implement project row model**

Create `cmd/serf-tui/project_model.go`:

```go
package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/internal/hubapi"
)

type projectRowKind int

const (
	projectRowSection projectRowKind = iota
	projectRowSession
)

type projectRow struct {
	Kind  projectRowKind
	Ref   string
	Title string
	State string
	Live  bool
	Model string
}

type projectModel struct {
	project  hubapi.ProjectNode
	rows     []projectRow
	selected int
}

func buildProjectRows(project hubapi.ProjectNode) []projectRow {
	var liveRows []projectRow
	var recentRows []projectRow
	for _, session := range project.Sessions {
		row := projectRow{Kind: projectRowSession, Ref: session.Ref, Title: session.Title, State: session.State, Live: session.Live, Model: session.Model}
		if session.Live {
			liveRows = append(liveRows, row)
		} else {
			recentRows = append(recentRows, row)
		}
	}
	var rows []projectRow
	if len(liveRows) > 0 {
		rows = append(rows, projectRow{Kind: projectRowSection, Title: "live"})
		rows = append(rows, liveRows...)
	}
	if len(recentRows) > 0 {
		rows = append(rows, projectRow{Kind: projectRowSection, Title: "recent"})
		rows = append(rows, recentRows...)
	}
	return rows
}

func (m projectModel) View(width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "serf / %s\n\n", m.project.Name)
	for i, row := range m.rows {
		selected := "  "
		if i == m.selected {
			selected = "> "
		}
		if row.Kind == projectRowSection {
			fmt.Fprintf(&b, "%s%s\n", selected, row.Title)
			continue
		}
		fmt.Fprintf(&b, "%s%-10s %s\n", selected, row.State, row.Title)
	}
	b.WriteString("\nkeys: enter open  b back  n new here  / search  r refresh  ctrl+o dashboard\n")
	return b.String()
}
```

- [ ] **Step 4: Wire dashboard `p` to project mode**

In `app_model.go`, when dashboard is active and key is `p`, find selected project by row `ProjectKey`, populate `projectModel`, set `appModeProject`.

Use helper:

```go
func projectByKey(tree hubapi.TreeResponse, key string) (hubapi.ProjectNode, bool) {
	for _, project := range tree.Projects {
		if project.Path == key || project.Name == key {
			return project, true
		}
	}
	return hubapi.ProjectNode{}, false
}
```

- [ ] **Step 5: Verify**

Run:

```bash
go test ./cmd/serf-tui -run 'TestProject|TestDashboard' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/project_model.go cmd/serf-tui/project_model_test.go cmd/serf-tui/app_model.go cmd/serf-tui/dashboard_model.go
git commit -m "feat(serf-tui): add project drilldown"
```

## Task 5: Make Hub Model Discovery Correct For Spawn

**Files:**
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-hub/web_test.go`
- Modify: `cmd/serf-tui/hub_commands.go`
- Test: `cmd/serf-hub/web_test.go`

- [ ] **Step 1: Write failing hub test for OpenRouter filtering**

Add to `cmd/serf-hub/web_test.go`:

```go
func TestWeb_ApiModels_FiltersOpenRouterLiveModelsToToolCapable(t *testing.T) {
	liveModelsCache.mu.Lock()
	liveModelsCache.expires = time.Time{}
	liveModelsCache.models = nil
	liveModelsCache.mu.Unlock()
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("OLLAMA_BASE_URL", "")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OLLAMA_API_KEY", "")

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"deepseek/deepseek-chat"},{"id":"morph/morph-v3-fast"},{"id":"unknown/no-tools"}]}`)) //nolint:errcheck
	}))
	defer live.Close()
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("OPENROUTER_BASE_URL", live.URL+"/v1")

	web := NewWebServer(WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	var models []hubapi.ModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models: got %d, want 1; body=%s", len(models), rec.Body.String())
	}
	if models[0].Provider != "openrouter" || models[0].Model != "deepseek/deepseek-chat" || !models[0].SupportsTools {
		t.Fatalf("model mismatch: %+v", models[0])
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-hub -run TestWeb_ApiModels_FiltersOpenRouterLiveModelsToToolCapable -count=1
```

Expected: fail because OpenRouter returns all three models.

- [ ] **Step 3: Filter OpenRouter using catalog tool metadata**

In `cmd/serf-hub/web.go`, inside `fetchLiveModels`, compute catalog metadata before appending each model:

```go
mi := catalogModelInfo(cat, m.ID)
if prov == "openrouter" && (mi == nil || !mi.SupportsTools) {
	continue
}
```

Add helper near `fetchLiveModels`:

```go
func catalogModelInfo(cat *llm.ModelCatalog, modelID string) *llm.ModelInfo {
	if cat == nil {
		return nil
	}
	return cat.GetModelInfo(modelID)
}
```

Reuse `mi` for metadata enrichment instead of calling `cat.GetModelInfo(m.ID)` again.

- [ ] **Step 4: Verify hub model tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestWeb_ApiModels' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/web.go cmd/serf-hub/web_test.go
git commit -m "fix(serf-hub): filter live OpenRouter models for tools"
```

## Task 6: Build Spawn Form Component

**Files:**
- Create: `cmd/serf-tui/spawn_model.go`
- Create: `cmd/serf-tui/spawn_model_test.go`
- Modify: `cmd/serf-tui/app_model.go`
- Modify: `cmd/serf-tui/hub_commands.go`

- [ ] **Step 1: Write failing spawn form tests**

Create `cmd/serf-tui/spawn_model_test.go`:

```go
package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSpawnModelLoadsFirstModel(t *testing.T) {
	m := newSpawnModel("/tmp", "tmp")
	updated, _ := m.Update(hubModelsMsg{models: []modelPickerItem{{id: "openai/gpt-5.2", display: "openai/gpt-5.2"}}})
	got := updated.(spawnModel)
	if got.model != "openai/gpt-5.2" {
		t.Fatalf("model=%q", got.model)
	}
}

func TestSpawnModelShowsNoModelsError(t *testing.T) {
	m := newSpawnModel("/tmp", "tmp")
	updated, _ := m.Update(hubModelsMsg{})
	if !strings.Contains(updated.(spawnModel).View(80), "no models available") {
		t.Fatalf("view missing no models message:\n%s", updated.(spawnModel).View(80))
	}
}

func TestSpawnModelMOpensPicker(t *testing.T) {
	m := newSpawnModel("/tmp", "tmp")
	m.models = []modelPickerItem{{id: "openai/gpt-5.2", display: "openai/gpt-5.2"}}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if updated.(spawnModel).picker == nil {
		t.Fatal("expected model picker")
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestSpawnModel -count=1
```

Expected: fail because `spawnModel` does not exist.

- [ ] **Step 3: Implement spawn component**

Create `cmd/serf-tui/spawn_model.go`:

```go
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type spawnModel struct {
	task    string
	dir     string
	project string
	model   string
	models  []modelPickerItem
	picker  *modelPicker
	err     string
}

func newSpawnModel(dir, project string) spawnModel {
	return spawnModel{dir: dir, project: project}
}

func (m spawnModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hubModelsMsg:
		if msg.err != nil {
			m.err = "model load failed: " + msg.err.Error()
			return m, nil
		}
		m.models = msg.models
		if len(m.models) == 0 {
			m.err = "no models available"
			return m, nil
		}
		if m.model == "" {
			m.model = m.models[0].id
		}
	case tea.KeyMsg:
		if m.picker != nil {
			updated, cmd := m.picker.Update(msg)
			picker := updated.(modelPicker)
			m.picker = &picker
			if picker.done {
				m.picker = nil
				if picker.selected != "" {
					m.model = picker.selected
				}
			}
			return m, cmd
		}
		if msg.String() == "m" {
			if len(m.models) == 0 {
				m.err = "no models available"
				return m, nil
			}
			picker := newModelPicker(m.models, m.model, 80)
			picker.title = "Select model"
			m.picker = &picker
			return m, nil
		}
	}
	return m, nil
}

func (m spawnModel) View(width int) string {
	var b strings.Builder
	b.WriteString("serf / new session\n\n")
	if m.picker != nil {
		b.WriteString(m.picker.View())
		return b.String()
	}
	fmt.Fprintf(&b, "Task\n> %s\n\n", m.task)
	model := m.model
	if model == "" && m.err == "" {
		model = "(loading models...)"
	}
	fmt.Fprintf(&b, "Model       %s\n", model)
	fmt.Fprintf(&b, "Project     %s\n", m.project)
	fmt.Fprintf(&b, "Directory   %s\n", m.dir)
	if m.err != "" {
		fmt.Fprintf(&b, "\nerror: %s\n", m.err)
	}
	b.WriteString("\nkeys: enter spawn  m model  esc cancel  ctrl+o dashboard\n")
	return b.String()
}
```

- [ ] **Step 4: Wire app `n` to spawn**

In `app_model.go`, when dashboard or project mode receives `n`:

```go
m.spawn = newSpawnModel(defaultDirFromSelection(m), defaultProjectFromSelection(m))
m.mode = appModeSpawn
return m, fetchHubModels(m.client)
```

Add field:

```go
spawn spawnModel
```

Route `hubModelsMsg` and spawn key messages to `spawn.Update`.

- [ ] **Step 5: Verify**

Run:

```bash
go test ./cmd/serf-tui -run 'TestSpawnModel|TestAppModel' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/spawn_model.go cmd/serf-tui/spawn_model_test.go cmd/serf-tui/app_model.go cmd/serf-tui/hub_commands.go
git commit -m "feat(serf-tui): add first-class spawn form"
```

## Task 7: Build Hub-Native Session Surface

**Files:**
- Create: `cmd/serf-tui/session_surface.go`
- Create: `cmd/serf-tui/session_surface_test.go`
- Modify: `cmd/serf-tui/app_model.go`
- Modify: `cmd/serf-tui/hub_commands.go`

- [ ] **Step 1: Write failing session slash command tests**

Create `cmd/serf-tui/session_surface_test.go`:

```go
package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/hubapi"
)

func TestSessionSurfaceHelpUsesRegistry(t *testing.T) {
	s := newSessionSurface(defaultCommandRegistry())
	s.detail = hubapi.SessionDetail{Ref: "local:01", SessionID: "01", Capabilities: hubapi.SessionCapabilities{Send: true, Compact: true, Clear: true, ChangeModel: true}}
	s.input.SetValue("/help")
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := updated.(sessionSurface).View(100)
	if !strings.Contains(view, "/dashboard") || !strings.Contains(view, "/model [provider/model]") {
		t.Fatalf("help missing commands:\n%s", view)
	}
}

func TestSessionSurfaceModelWithoutArgsOpensPicker(t *testing.T) {
	s := newSessionSurface(defaultCommandRegistry())
	s.detail = hubapi.SessionDetail{Ref: "local:01", SessionID: "01", Capabilities: hubapi.SessionCapabilities{ChangeModel: true}}
	s.models = []modelPickerItem{{id: "openai/gpt-5.2", display: "openai/gpt-5.2"}}
	s.input.SetValue("/model")
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(sessionSurface).modelPicker == nil {
		t.Fatal("expected model picker")
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestSessionSurface -count=1
```

Expected: fail because `sessionSurface` does not exist.

- [ ] **Step 3: Implement minimal session surface**

Create `cmd/serf-tui/session_surface.go`:

```go
package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textarea"
	"primeradiant.com/serf/internal/hubapi"
)

type sessionSurface struct {
	commands    commandRegistry
	detail      hubapi.SessionDetail
	messages    []chatMessage
	input       textarea.Model
	models      []modelPickerItem
	modelPicker *modelPicker
	scrollMode  bool
}

func newSessionSurface(commands commandRegistry) sessionSurface {
	input := textarea.New()
	input.Focus()
	input.Prompt = "> "
	return sessionSurface{commands: commands, input: input}
}

func (s sessionSurface) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			text := strings.TrimSpace(s.input.Value())
			if text == "" {
				return s, nil
			}
			inv := parseCommandInvocation(text)
			if inv.Name != "" {
				s.input.SetValue("")
				return s.runCommand(inv)
			}
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s sessionSurface) runCommand(inv commandInvocation) (tea.Model, tea.Cmd) {
	switch inv.Name {
	case "help":
		s.messages = append(s.messages, chatMessage{Kind: msgSystem, Text: s.commands.Help(s.commandContext())})
	case "model":
		if strings.TrimSpace(inv.Args) == "" {
			if len(s.models) == 0 {
				s.messages = append(s.messages, chatMessage{Kind: msgSystem, Text: "No models available."})
				return s, nil
			}
			picker := newModelPicker(s.models, s.detail.Model, 80)
			picker.title = "Select model"
			s.modelPicker = &picker
			return s, nil
		}
	default:
		s.messages = append(s.messages, chatMessage{Kind: msgSystem, Text: "Command requires app shell dispatch: /" + inv.Name})
	}
	return s, nil
}

func (s sessionSurface) commandContext() commandContext {
	return commandContext{
		Scope:        commandScopeSession,
		CanSend:      s.detail.Capabilities.Send,
		CanInterrupt: s.detail.Capabilities.Interrupt,
		CanCompact:   s.detail.Capabilities.Compact,
		CanClear:     s.detail.Capabilities.Clear,
		CanModel:     s.detail.Capabilities.ChangeModel,
		CanFork:      s.detail.Capabilities.Fork,
	}
}

func (s sessionSurface) View(width int) string {
	var b strings.Builder
	b.WriteString(s.detail.Title)
	b.WriteString("\n\n")
	for _, msg := range s.messages {
		b.WriteString(msg.Text)
		b.WriteString("\n")
	}
	if s.modelPicker != nil {
		b.WriteString(s.modelPicker.View())
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(s.input.View())
	b.WriteString("\n\nkeys: enter send  esc browse  ctrl+o dashboard  /help\n")
	return b.String()
}
```

- [ ] **Step 4: Verify**

Run:

```bash
go test ./cmd/serf-tui -run TestSessionSurface -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/session_surface.go cmd/serf-tui/session_surface_test.go cmd/serf-tui/app_model.go cmd/serf-tui/hub_commands.go
git commit -m "feat(serf-tui): add hub-native session surface"
```

## Task 8: Move All Session Slash Commands To Registry Dispatch

**Files:**
- Modify: `cmd/serf-tui/command_registry.go`
- Modify: `cmd/serf-tui/session_surface.go`
- Modify: `cmd/serf-tui/session_surface_test.go`
- Modify: `cmd/serf-tui/hub_commands.go`

- [ ] **Step 1: Write failing tests for required command dispatch**

Add to `cmd/serf-tui/session_surface_test.go`:

```go
func TestSessionSurfaceRequiredCommandsHaveExecutors(t *testing.T) {
	reg := defaultCommandRegistry()
	for _, name := range []string{"dashboard", "project", "projects", "new", "search", "compact", "status", "details", "tasks", "agents", "model", "clear", "interrupt", "fork", "theme", "quit"} {
		entry, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("missing /%s", name)
		}
		if entry.Run == nil {
			t.Fatalf("/%s has nil executor", name)
		}
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestSessionSurfaceRequiredCommandsHaveExecutors -count=1
```

Expected: fail because registry entries have nil `Run`.

- [ ] **Step 3: Add executor functions to command registry**

In `command_registry.go`, wire each entry with `Run`.

Example:

```go
func commandDashboard(m *appModel, inv commandInvocation) tea.Cmd {
	m.mode = appModeDashboard
	return fetchHubTree(m.client)
}

func commandCompact(m *appModel, inv commandInvocation) tea.Cmd {
	ref, ok := m.currentSessionRef()
	if !ok {
		m.setStatus("No current session.")
		return nil
	}
	return sendHubAction(m.client, ref, "compact")
}
```

Do this for every required command. If a command has no full implementation yet, its executor must open an explicit overlay or add a visible status message. It must not be nil.

- [ ] **Step 4: Route session slash through registry**

In `session_surface.go`, replace hard-coded command switch with:

```go
entry, ok := s.commands.Lookup(inv.Name)
if !ok {
	s.messages = append(s.messages, chatMessage{Kind: msgSystem, Text: "Unknown command: /" + inv.Name + ". Use /help."})
	return s, nil
}
enabled, reason := entry.enabledIn(s.commandContext())
if !enabled {
	s.messages = append(s.messages, chatMessage{Kind: msgSystem, Text: reason})
	return s, nil
}
```

The executor runs at `appModel` level, so session surface should return a `commandInvocationMsg` to the app shell.

Define:

```go
type commandInvocationMsg struct {
	Invocation commandInvocation
}
```

- [ ] **Step 5: Verify**

Run:

```bash
go test ./cmd/serf-tui -run 'TestCommandRegistry|TestSessionSurface' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/command_registry.go cmd/serf-tui/session_surface.go cmd/serf-tui/session_surface_test.go cmd/serf-tui/hub_commands.go cmd/serf-tui/app_model.go
git commit -m "feat(serf-tui): unify slash command dispatch"
```

## Task 9: Add Transcript Reducer With Replay/Live Dedupe

**Files:**
- Create: `cmd/serf-tui/session_reducer.go`
- Create: `cmd/serf-tui/session_reducer_test.go`
- Modify: `cmd/serf-tui/session_surface.go`
- Modify: `internal/hubapi/types.go` when the existing replay/follow payload type cannot carry a stable event ID; add `EventID string`, `Turn int`, and `Source string` to that payload.

- [ ] **Step 1: Write failing reducer tests**

Create `cmd/serf-tui/session_reducer_test.go`:

```go
package main

import "testing"

func TestSessionReducerDedupesReplayAndLiveByEventID(t *testing.T) {
	r := newSessionReducer()
	r.Apply(transcriptEvent{ID: "1", Kind: "user", Text: "hello"})
	r.Apply(transcriptEvent{ID: "1", Kind: "user", Text: "hello"})
	if len(r.Messages()) != 1 {
		t.Fatalf("messages=%d, want 1", len(r.Messages()))
	}
}

func TestSessionReducerKeepsOrder(t *testing.T) {
	r := newSessionReducer()
	r.Apply(transcriptEvent{ID: "1", Kind: "user", Text: "hello"})
	r.Apply(transcriptEvent{ID: "2", Kind: "assistant", Text: "world"})
	got := r.Messages()
	if got[0].Text != "hello" || got[1].Text != "world" {
		t.Fatalf("messages=%+v", got)
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestSessionReducer -count=1
```

Expected: fail because reducer does not exist.

- [ ] **Step 3: Implement reducer**

Create `cmd/serf-tui/session_reducer.go`:

```go
package main

type transcriptEvent struct {
	ID   string
	Kind string
	Text string
}

type sessionReducer struct {
	seen     map[string]struct{}
	messages []chatMessage
}

func newSessionReducer() sessionReducer {
	return sessionReducer{seen: map[string]struct{}{}}
}

func (r *sessionReducer) Apply(ev transcriptEvent) {
	if ev.ID != "" {
		if _, ok := r.seen[ev.ID]; ok {
			return
		}
		r.seen[ev.ID] = struct{}{}
	}
	kind := msgAssistant
	switch ev.Kind {
	case "user":
		kind = msgUser
	case "system":
		kind = msgSystem
	case "tool":
		kind = msgTool
	}
	r.messages = append(r.messages, chatMessage{Kind: kind, Text: ev.Text})
}

func (r *sessionReducer) Messages() []chatMessage {
	return append([]chatMessage(nil), r.messages...)
}
```

- [ ] **Step 4: Verify**

Run:

```bash
go test ./cmd/serf-tui -run TestSessionReducer -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/session_reducer.go cmd/serf-tui/session_reducer_test.go cmd/serf-tui/session_surface.go internal/hubapi/types.go
git commit -m "feat(serf-tui): add transcript reducer"
```

## Task 10: Add Bubble Tea Styling And Theme Tokens

**Files:**
- Create: `cmd/serf-tui/styles.go`
- Modify: `cmd/serf-tui/dashboard_view.go`
- Modify: `cmd/serf-tui/project_model.go`
- Modify: `cmd/serf-tui/session_surface.go`
- Modify: `cmd/serf-tui/spawn_model.go`
- Test: `cmd/serf-tui/theme_test.go`

- [ ] **Step 1: Write failing style smoke test**

Add to `cmd/serf-tui/theme_test.go`:

```go
func TestTUIStylesRenderSelectedRow(t *testing.T) {
	styles := defaultTUIStyles()
	got := styles.Selected.Render("selected")
	if got == "selected" {
		t.Fatal("selected style should add terminal styling")
	}
}
```

- [ ] **Step 2: Verify fail**

Run:

```bash
go test ./cmd/serf-tui -run TestTUIStylesRenderSelectedRow -count=1
```

Expected: fail because style tokens do not exist.

- [ ] **Step 3: Implement style tokens**

Create `cmd/serf-tui/styles.go`:

```go
package main

import "github.com/charmbracelet/lipgloss"

type tuiStyles struct {
	Title      lipgloss.Style
	Section    lipgloss.Style
	Muted      lipgloss.Style
	Selected   lipgloss.Style
	Error      lipgloss.Style
	Idle       lipgloss.Style
	Processing lipgloss.Style
	Waiting    lipgloss.Style
	Ended      lipgloss.Style
}

func defaultTUIStyles() tuiStyles {
	return tuiStyles{
		Title:      lipgloss.NewStyle().Bold(true),
		Section:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true),
		Muted:      lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		Selected:   lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("238")),
		Error:      lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		Idle:       lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		Processing: lipgloss.NewStyle().Foreground(lipgloss.Color("111")),
		Waiting:    lipgloss.NewStyle().Foreground(lipgloss.Color("210")),
		Ended:      lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}
```

- [ ] **Step 4: Apply styles gradually**

Use `defaultTUIStyles()` in each view. Keep styling restrained:

```go
styles := defaultTUIStyles()
b.WriteString(styles.Title.Render("serf"))
```

Do not add heavy boxes. Use color and spacing for hierarchy.

- [ ] **Step 5: Verify**

Run:

```bash
go test ./cmd/serf-tui -run 'TestTUIStyles|TestDashboard|TestProject|TestSpawn|TestSessionSurface' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/styles.go cmd/serf-tui/dashboard_view.go cmd/serf-tui/project_model.go cmd/serf-tui/session_surface.go cmd/serf-tui/spawn_model.go cmd/serf-tui/theme_test.go
git commit -m "style(serf-tui): add polished terminal styles"
```

## Task 11: Add Full Tmux E2E Coverage

**Files:**
- Modify: `cmd/serf-tui/tmux_e2e_test.go`

- [ ] **Step 1: Add fake hub scenarios**

Extend the fake hub in `cmd/serf-tui/tmux_e2e_test.go` to serve:

- `GET /api/tree`
- `GET /api/models`
- `GET /api/auth`
- `GET /api/auth/openai`
- `POST /api/auth/openai/login`
- `POST /api/auth/openai/complete`
- `POST /api/auth/openai/logout`
- `POST /api/spawn`
- `GET /api/sessions/{ref}`
- `POST /api/sessions/{ref}/send`
- `POST /api/sessions/{ref}/steer`
- `POST /api/sessions/{ref}/compact`
- `POST /api/sessions/{ref}/model`
- `POST /api/sessions/{ref}/clear`
- `POST /api/sessions/{ref}/fork`
- `GET /api/sessions/{ref}/tasks`
- `GET /api/sessions/{ref}/details`
- `GET /api/sessions/{ref}/agents`
- `GET /api/sessions/{ref}/events?mode=replay`
- `GET /api/sessions/{ref}/events?mode=transcript-follow`

- [ ] **Step 2: Write E2E dashboard and spawn test**

Add:

```go
func TestTUITmuxE2E_DashboardSpawnAndOpenSession(t *testing.T) {
	app := startTUITmuxWithFakeHub(t)
	defer app.Close()

	app.WaitFor("live sessions", "terminal-tetris")
	app.TypeKey("n")
	app.WaitFor("serf / new session", "Model")
	app.TypeLine("fix scoring bug")
	app.TypeKey("Enter")
	app.WaitFor("fix scoring bug", "enter send")
}
```

Adapt helper names to the existing `tmux_e2e_test.go` harness.

- [ ] **Step 3: Write E2E command parity test**

Add:

```go
func TestTUITmuxE2E_SessionSlashCommands(t *testing.T) {
	app := startTUITmuxWithFakeHub(t)
	defer app.Close()

	app.OpenFirstSession()
	app.TypeLine("/help")
	app.WaitFor("Available commands:", "/dashboard", "/model [provider/model]", "/tasks", "/auth")
	app.TypeLine("/tasks")
	app.WaitFor("task list")
	app.TypeLine("/details")
	app.WaitFor("MCP", "skills", "plugins")
	app.TypeLine("/agents")
	app.WaitFor("main", "subagent")
	app.TypeLine("/model")
	app.WaitFor("Select model")
	app.TypeKey("Esc")
	app.TypeLine("/dashboard")
	app.WaitFor("live sessions")
}
```

- [ ] **Step 4: Write E2E browse and fork test**

Add:

```go
func TestTUITmuxE2E_BrowseAndFork(t *testing.T) {
	app := startTUITmuxWithFakeHub(t)
	defer app.Close()

	app.OpenFirstSession()
	app.TypeKey("Esc")
	app.WaitFor("browse")
	app.TypeKey("f")
	app.WaitFor("Fork")
	app.TypeLine("edited user message")
	app.WaitFor("forked session")
}
```

- [ ] **Step 5: Write E2E auth and busy-input test**

Add:

```go
func TestTUITmuxE2E_OpenAIAuthAndSteer(t *testing.T) {
	app := startTUITmuxWithFakeHub(t)
	defer app.Close()

	app.WaitFor("openai: login required")
	app.TypeLine("/login openai")
	app.WaitFor("https://", "paste redirect")
	app.TypeLine("http://127.0.0.1/callback?code=test-code&state=test-state")
	app.WaitFor("openai: signed in")

	app.OpenBusySession()
	app.TypeLine("try this next")
	app.WaitFor("session is busy", "/steer")
	app.TypeLine("/steer try this next")
	app.WaitFor("steered")

	app.TypeLine("/logout openai")
	app.WaitFor("openai: login required")
}
```

- [ ] **Step 6: Verify E2E**

Run:

```bash
go test ./cmd/serf-tui -run TestTUITmuxE2E -count=1
```

Expected: pass. If tmux is missing, tests should skip with a clear message.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-tui/tmux_e2e_test.go
git commit -m "test(serf-tui): cover hub tui end to end"
```

## Task 12: Delete Dead Embedded/Direct Paths After Parity

**Files:**
- Modify: `cmd/serf-tui/main.go`
- Modify: `cmd/serf-tui/embedded.go`
- Modify: `cmd/serf-tui/model.go`
- Modify: `cmd/serf-tui/input.go`
- Modify: tests under `cmd/serf-tui`

- [ ] **Step 1: Search for direct daemon-only paths**

Run:

```bash
rg -n "embedded|--addr|provider|sendInput\\(|fetchModels\\(|http://%s/(input|models|compact|clear|model)" cmd/serf-tui -g'*.go'
```

Expected: find old direct helpers and tests.

- [ ] **Step 2: Remove only code proven unused by tests**

Delete direct daemon helpers only after all hub E2E tests pass. Keep reusable transcript rendering structs if the new session surface uses them.

- [ ] **Step 3: Verify no stale help or direct network calls**

Run:

```bash
rg -n "slashCommandHelp\\(|sendInput\\(|fetchModels\\(|/models\"|/input\"" cmd/serf-tui -g'*.go'
```

Expected: no stale handwritten help; no direct daemon `/models` or `/input` calls from the TUI.

- [ ] **Step 4: Run full TUI tests**

Run:

```bash
go test ./cmd/serf-tui -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui
git commit -m "refactor(serf-tui): remove direct session mode"
```

## Task 13: Final Verification And Build

**Files:**
- No source changes unless failures require fixes.

- [ ] **Step 1: Run targeted suites**

Run:

```bash
go test ./cmd/serf-tui ./cmd/serf-hub ./cmd/serf ./internal/hubapi ./internal/auth/openai ./llm/providers/openai ./llm -count=1
```

Expected: pass.

- [ ] **Step 2: Run full suite**

Run:

```bash
go test ./... -count=1
```

Expected: pass.

- [ ] **Step 3: Build local binaries for manual testing**

Run:

```bash
go build -o ./serf ./cmd/serf
go build -o ./serf-hub ./cmd/serf-hub
go build -o ./serf-tui ./cmd/serf-tui
```

Expected: all builds exit 0.

- [ ] **Step 4: Manual smoke with real hub**

Run:

```bash
tmux new-session -d -s serf-tui-smoke -x 120 -y 40 './serf-tui'
tmux capture-pane -p -t serf-tui-smoke -S -40
```

Expected: dashboard renders. If an old hub is already running, restart it before validating model discovery.

- [ ] **Step 5: Commit any final fixes**

```bash
git status --short
git add <only files changed by this task>
git commit -m "fix(serf-tui): finalize hub tui verification"
```

Only commit if there are actual fixes.

## Completion Checklist

- [ ] Root dashboard is live-only and project-grouped.
- [ ] Project drilldown shows live and recent ended sessions.
- [ ] Session view supports generated `/help`.
- [ ] Every advertised slash command is implemented or visibly disabled with a reason.
- [ ] `serf openai login`, `serf openai status`, and `serf openai logout` work from the main-line auth stack.
- [ ] TUI `/auth`, `/auth openai`, `/login openai`, and `/logout openai` work through hub APIs.
- [ ] OpenAI adapter uses `OPENAI_API_KEY` first and Serf-owned OAuth second.
- [ ] Hub-spawned daemons receive the same state-dir/auth context as the TUI.
- [ ] `/model` opens picker; `/model provider/model` switches directly.
- [ ] Spawn loads models and submits successfully.
- [ ] OpenRouter live model list is filtered to tool-capable entries.
- [ ] Busy input preserves the draft and `/steer` works when the hub advertises steering.
- [ ] Input history and multiline composition match old TUI behavior.
- [ ] `/status`, `/details`, `/tasks`, and `/agents` render hub-backed real data.
- [ ] Ended session opens with replay history.
- [ ] Sending to ended session resumes through hub.
- [ ] `esc` enters browse mode.
- [ ] Browse mode supports fork from selected user turn.
- [ ] `ctrl+o` returns to dashboard from every mode.
- [ ] No stale direct daemon TUI path remains.
- [ ] `go test ./... -count=1` passes.
