# Serf TUI Live Dashboard UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `serf-tui` open on a live-only project-grouped dashboard, support project drill-down, preserve `esc` for session transcript browse/fork focus, and provide explicit dashboard return via `ctrl+o` and `/dashboard`.

**Architecture:** Keep the existing hub-backed `hubModel`, but split its row model into dashboard/project/session concerns. Derive dashboard and project rows from `GET /api/tree`, use existing session detail and stream APIs for drill-in, and add a typed fork call to `internal/hubapi.Client`. Session browse uses the existing embedded session model's scroll/tool focus fields instead of creating a second viewport implementation.

**Tech Stack:** Go, Bubble Tea, existing `internal/hubapi` JSON client, existing `cmd/serf-tui` rendering helpers, `go test`.

---

## File Structure

- Modify `internal/hubapi/types.go` to add `ForkRequest`.
- Modify `internal/hubapi/client.go` to add `Fork(ctx, ref, req)`.
- Modify `cmd/serf-hub/web.go` only if fork replay lacks turn identity for the TUI.
- Modify `cmd/serf-tui/message.go` to carry transcript entry index on user messages.
- Modify `cmd/serf-tui/model.go` only if SSE replay needs to preserve turn index on messages.
- Modify `cmd/serf-tui/hub_commands.go` to add `sendHubFork`.
- Modify `cmd/serf-tui/hub_model.go` for dashboard/project row adapters, navigation, session browse, and fork draft flow.
- Modify `cmd/serf-tui/hub_model_test.go` for the main behavior tests.

## Task 1: Live-Only Dashboard And Project Rows

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_model_test.go`

- [ ] **Step 1: Add failing dashboard/project row tests**

Add tests named:

```go
func TestHubModelDashboardShowsOnlyLiveSessionsGroupedByProject(t *testing.T)
func TestHubModelProjectViewShowsLiveThenRecent(t *testing.T)
```

The dashboard test should construct a `hubapi.TreeResponse` with two live sessions in project `serf`, one live session in `brainstorm`, and one ended `serf` session under `Projects`. It should assert that `buildDashboardRows(tree)` returns project header and live session rows only, and that the rendered dashboard does not contain the ended title.

The project test should construct a `hubapi.TreeProject{Name: "serf", Key: "serf"}` with mixed live and ended sessions. It should assert that `buildProjectRows(project)` returns live rows before ended rows and that the rendered project view contains both `Live now` and `Recent in this project`.

- [ ] **Step 2: Run the failing tests**

Run:

```bash
go test ./cmd/serf-tui -run 'TestHubModelDashboardShowsOnlyLiveSessionsGroupedByProject|TestHubModelProjectViewShowsLiveThenRecent' -count=1
```

Expected: fail because the row builders and project mode do not exist yet.

- [ ] **Step 3: Implement dashboard/project rows**

Implement:

```go
type hubMode int

const (
    hubModeDashboard hubMode = iota
    hubModeProject
    hubModeSession
)

type hubRowKind int

const (
    hubRowProject hubRowKind = iota
    hubRowSession
)

type hubRow struct {
    kind       hubRowKind
    ref        hubapi.Ref
    title      string
    project    string
    projectKey string
    state      string
    live       bool
    model      string
    age        string
    rowID      string
}
```

Add `buildDashboardRows(tree hubapi.TreeResponse) []hubRow` that iterates `tree.Projects`, skips projects with no live sessions, adds one project header, then live session rows only. Add `buildProjectRows(project hubapi.TreeProject) []hubRow` that appends live rows first, then ended rows. Add helpers for finding projects by key/name and for rendering status text.

- [ ] **Step 4: Run the row tests**

Run the same `go test` command from Step 2.

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/hub_model_test.go
git commit -m "feat: add serf-tui live dashboard rows"
```

## Task 2: Dashboard, Project, And Session Navigation

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_model_test.go`

- [ ] **Step 1: Add failing navigation tests**

Add tests named:

```go
func TestHubModelDashboardProjectHeaderOpensProject(t *testing.T)
func TestHubModelProjectEscReturnsDashboard(t *testing.T)
func TestHubModelSessionEscEntersBrowseInsteadOfDashboard(t *testing.T)
func TestHubModelCtrlOReturnsDashboardFromSession(t *testing.T)
func TestHubModelSlashDashboardAndProjectNavigate(t *testing.T)
```

These tests should drive `hubModel.Update` with `tea.KeyMsg` values and assert `mode`, `selectedProjectKey`, `session.scrollMode`, and rendered output.

- [ ] **Step 2: Run the failing navigation tests**

Run:

```bash
go test ./cmd/serf-tui -run 'TestHubModelDashboardProjectHeaderOpensProject|TestHubModelProjectEscReturnsDashboard|TestHubModelSessionEscEntersBrowseInsteadOfDashboard|TestHubModelCtrlOReturnsDashboardFromSession|TestHubModelSlashDashboardAndProjectNavigate' -count=1
```

Expected: fail because current `esc` returns session to dashboard and project mode does not exist.

- [ ] **Step 3: Implement navigation**

Update `hubModel` with:

```go
selectedProjectKey string
projectRows        []hubRow
```

Implement:

- `ctrl+o` as a global dashboard action.
- Dashboard `enter` opens a project header or fetches a session.
- Dashboard `p` opens the selected row's project.
- Project `esc` and `backspace` return dashboard.
- Session `esc` and `pgup` enter browse focus by setting `m.session.scrollMode = true` and blurring input.
- Session browse `esc`, `i`, and `q` return to compose focus.
- `/dashboard` and `/project` in hub slash commands.

- [ ] **Step 4: Run navigation tests**

Run the same `go test` command from Step 2.

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/hub_model_test.go
git commit -m "feat: wire serf-tui dashboard navigation"
```

## Task 3: Transcript Browse Fork Draft

**Files:**
- Modify: `internal/hubapi/types.go`
- Modify: `internal/hubapi/client.go`
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-tui/message.go`
- Modify: `cmd/serf-tui/model.go`
- Modify: `cmd/serf-tui/hub_commands.go`
- Modify: `cmd/serf-tui/hub_model.go`
- Test: `cmd/serf-tui/hub_model_test.go`
- Test: `cmd/serf-hub/web_test.go`

- [ ] **Step 1: Add failing fork tests**

Add tests named:

```go
func TestHubReplayUserInputIncludesTurnIndex(t *testing.T)
func TestHubModelBrowseForkDraftPostsForkAndNavigatesToChild(t *testing.T)
func TestHubModelBrowseForkRequiresUserTurnWithTurnIndex(t *testing.T)
```

The replay test should assert a replayed `USER_INPUT` SSE payload contains `"turn":1`. The TUI fork test should build a session with two user messages where the selected message has `TurnIndex: 3`, press `f`, edit the input, press `enter`, and assert the hub receives `POST /api/sessions/local:01SEND/fork` with `{"turn":3,"edited_message":"...","label":"original before fork"}` and the model fetches the returned child ref.

- [ ] **Step 2: Run the failing fork tests**

Run:

```bash
go test ./cmd/serf-hub ./cmd/serf-tui -run 'TestHubReplayUserInputIncludesTurnIndex|TestHubModelBrowseForkDraftPostsForkAndNavigatesToChild|TestHubModelBrowseForkRequiresUserTurnWithTurnIndex' -count=1
```

Expected: fail because replay lacks turn index and TUI fork draft does not exist.

- [ ] **Step 3: Implement fork support**

Add:

```go
type ForkRequest struct {
    Turn          int    `json:"turn"`
    EditedMessage string `json:"edited_message"`
    Label         string `json:"label"`
}
```

Add `Client.Fork(ctx, ref, req) (RefResponse, error)`.

Add `TurnIndex int` to `chatMessage`. In hub replay, emit `turn` on `USER_INPUT` payload using the transcript entry's 1-based position. In `model.handleSSEEvent`, set `TurnIndex` on replayed user messages when present.

In `hubModel`, add:

```go
type hubForkDraft struct {
    Ref          hubapi.Ref
    Turn         int
    OriginalText string
    Label        string
}
forkDraft *hubForkDraft
```

When browsing and `f` is pressed on a user message with `TurnIndex > 0` and `Capabilities.Fork`, set fork draft, return to compose, prefill input with the original text, and show a system message explaining `enter` confirms and `esc` cancels. When `enter` is pressed during a fork draft, call hub fork and navigate to returned ref. When `esc` is pressed during a fork draft, clear the draft, reset input, and return to browse.

- [ ] **Step 4: Run fork tests**

Run the same `go test` command from Step 2.

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/hubapi/types.go internal/hubapi/client.go cmd/serf-hub/web.go cmd/serf-tui/message.go cmd/serf-tui/model.go cmd/serf-tui/hub_commands.go cmd/serf-tui/hub_model.go cmd/serf-tui/hub_model_test.go cmd/serf-hub/web_test.go
git commit -m "feat: support serf-tui transcript fork drafts"
```

## Task 4: UX Polish, Help Text, And Regression Sweep

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Modify: `cmd/serf-tui/input.go`
- Test: `cmd/serf-tui/hub_model_test.go`
- Test: `cmd/serf-tui/input_test.go`

- [ ] **Step 1: Add failing UX output tests**

Add tests named:

```go
func TestHubModelDashboardEmptyStateIsLiveOnly(t *testing.T)
func TestHubModelSessionFooterShowsBrowseAndDashboardKeys(t *testing.T)
func TestSlashCommandHelpMentionsDashboardProjectAndBrowse(t *testing.T)
```

Assert the no-live dashboard says `No live sessions are running` and does not render ended sessions. Assert session footer contains `esc: browse` and `ctrl+o: dashboard`, not `esc: dashboard`.

- [ ] **Step 2: Run failing UX tests**

Run:

```bash
go test ./cmd/serf-tui -run 'TestHubModelDashboardEmptyStateIsLiveOnly|TestHubModelSessionFooterShowsBrowseAndDashboardKeys|TestSlashCommandHelpMentionsDashboardProjectAndBrowse' -count=1
```

Expected: fail until footer/help text is updated.

- [ ] **Step 3: Implement UX polish**

Update views and help text so:

- Dashboard title is `serf live`.
- Dashboard no-live empty state offers `s start a session`, `/projects browse project history`, `/ search sessions`, and `q quit`.
- Project view title is `serf / project / <name>`.
- Session footer says `enter: send  esc: browse  ctrl+o: dashboard  /help`.
- Browse footer says `esc/i: compose  f: fork  ctrl+o: dashboard`.
- Help text includes `/dashboard`, `/project`, and `esc` browse behavior.

- [ ] **Step 4: Run focused tests**

Run the same `go test` command from Step 2.

Expected: pass.

- [ ] **Step 5: Run package tests**

Run:

```bash
go test ./cmd/serf-tui ./internal/hubapi
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/hub_model.go cmd/serf-tui/input.go cmd/serf-tui/hub_model_test.go cmd/serf-tui/input_test.go
git commit -m "polish: update serf-tui hub dashboard UX"
```

## Final Verification

- [ ] Run `go test ./cmd/serf-tui ./internal/hubapi ./cmd/serf-hub`.
- [ ] Run `go test ./...` if the focused package suite passes and runtime is reasonable.
- [ ] Run `git status --short` and verify only unrelated pre-existing files remain dirty.
- [ ] Summarize commits, tests, and any limitations.
