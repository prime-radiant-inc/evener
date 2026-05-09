# Serf TUI Universal Hub Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `serf-tui`'s embedded/direct single-session flow with a hub-backed dashboard client that auto-starts a local hub, lists sessions, drills into sessions, and drives write actions through hub APIs.

**Architecture:** Add a small shared `internal/hubapi` package for JSON DTOs, refs, and a typed client. Extend `cmd/serf-hub` with JSON endpoints that wrap existing roster, past index, replay, send, spawn, and session action behavior. Replace `cmd/serf-tui/main.go` with hub startup and a new Bubble Tea app model that uses the hub client while reusing existing message rendering and reducer concepts.

**Tech Stack:** Go, Bubble Tea, existing `cmd/serf-hub` HTTP server, existing daemon REST/SSE API, TDD with `go test`.

---

## File Structure

- Create `internal/hubapi/types.go`: shared DTOs for health, tree, session detail, capabilities, spawn schema, refs, and common action responses.
- Create `internal/hubapi/refs.go`: local ref formatting, parsing, validation, and URL path escaping helpers.
- Create `internal/hubapi/client.go`: typed HTTP client for TUI.
- Modify `server/server.go`: add action capabilities to `/status`.
- Modify `cmd/serf-hub/tree.go`: normalize `AWAITING_INPUT` and expose row IDs/project keys for API DTOs without breaking web templates.
- Modify `cmd/serf-hub/spawn.go`: add resume request with `state_dir` and `working_dir`, pass explicit resume context.
- Modify `cmd/serf-hub/web.go`: add hub JSON API routes and route them to existing hub logic.
- Modify `cmd/serf-hub/web_test.go`: add red/green API coverage.
- Create `cmd/serf-tui/hub_start.go`: hub address normalization, local detection, binary resolution, detached start, health wait.
- Create `cmd/serf-tui/hub_model.go`: dashboard/session Bubble Tea model.
- Create `cmd/serf-tui/hub_commands.go`: Bubble Tea commands backed by `hubapi.Client`.
- Modify `cmd/serf-tui/main.go`: remove embedded/direct flags and launch hub model.
- Modify `cmd/serf-tui/sse_client.go`: make parser spec-compliant and support URL streams with `Last-Event-ID`.
- Modify `cmd/serf-tui/model.go`: extract or reuse event reducer behavior for session messages.
- Modify `cmd/serf-tui/*_test.go`: update tests for hub-backed behavior and remove old resume/direct expectations.

## Task 1: Shared Hub API Types And Refs

**Files:**
- Create: `internal/hubapi/types.go`
- Create: `internal/hubapi/refs.go`
- Test: `internal/hubapi/refs_test.go`

- [ ] **Step 1: Write failing ref tests**

```go
func TestLocalRefRoundTrip(t *testing.T) {
	ref := hubapi.LocalRef("01ABC")
	if ref.String() != "local:01ABC" {
		t.Fatalf("ref=%q", ref.String())
	}
	parsed, err := hubapi.ParseRef(ref.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.HostID != "local" || parsed.SessionID != "01ABC" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseRefRejectsUnsafePathText(t *testing.T) {
	for _, raw := range []string{"", "local/", "local:../x", "local:with space"} {
		if _, err := hubapi.ParseRef(raw); err == nil {
			t.Fatalf("ParseRef(%q) succeeded", raw)
		}
	}
}
```

- [ ] **Step 2: Run red**

Run: `go test ./internal/hubapi`

Expected: fail because package does not exist.

- [ ] **Step 3: Implement DTOs and ref helpers**

Add `Ref`, `HealthResponse`, `TreeResponse`, `TreeNode`, `TreeProject`, `SessionDetail`, `SessionCapabilities`, `SpawnSchema`, `SpawnRequest`, `SpawnResponse`, and action response types. Keep JSON field names exactly as in the design spec.

- [ ] **Step 4: Run green**

Run: `go test ./internal/hubapi`

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/hubapi
git commit -m "Add shared hub API DTOs"
```

## Task 2: Hub JSON API

**Files:**
- Modify: `server/server.go`
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-hub/tree.go`
- Modify: `cmd/serf-hub/spawn.go`
- Modify: `cmd/serf-hub/web_test.go`

- [ ] **Step 1: Write failing API tests**

Add tests for:

- `GET /api/health` returns version, hub addr, and capabilities.
- `GET /api/tree` returns live and project rows with refs, row IDs, and `AWAITING_INPUT` normalized to `awaiting`.
- `GET /api/sessions/local:<id>` returns live and past details with capabilities.
- `POST /api/sessions/local:<id>/clear` returns a new ref from the daemon status.
- `GET /api/spawn-schema` returns only supported spawn fields.

- [ ] **Step 2: Run red**

Run: `go test ./cmd/serf-hub -run 'TestWeb_Api(Health|Tree|Session|SpawnSchema)|TestWeb_SessionAction_ClearReturnsRef'`

Expected: fail with 404 or missing fields.

- [ ] **Step 3: Implement routes**

Add route handlers:

```go
mux.HandleFunc("/api/health", s.handleAPIHealth)
mux.HandleFunc("/api/tree", s.handleAPITree)
mux.HandleFunc("/api/spawn-schema", s.handleAPISpawnSchema)
mux.HandleFunc("/api/sessions/", s.handleAPISession)
```

Implement API handlers by wrapping existing `workspaceData`, `BuildTree`, `handleSend`, `handleFork`, `renderSessionTasks`, `renderDetailsPanel`, and `handleSessionAction` logic where practical. Return JSON errors for API routes.

- [ ] **Step 4: Add daemon capabilities**

Add `ActionCapabilities` to `server.StatusInfo` and populate default live capabilities from server function availability and processing state.

- [ ] **Step 5: Fix resume context**

Extend spawner resume to accept a resolved `PastEntry`/resume request carrying `WorkingDir` and `StateDir`. Use those flags when invoking `serf serve --resume`.

- [ ] **Step 6: Run green**

Run: `go test ./cmd/serf-hub ./server -short`

Expected: pass.

- [ ] **Step 7: Commit**

Run:

```bash
git add server/server.go cmd/serf-hub cmd/serf-hub/web_test.go
git commit -m "Add hub JSON client API"
```

## Task 3: TUI Hub Startup And Client

**Files:**
- Create: `cmd/serf-tui/hub_start.go`
- Create: `cmd/serf-tui/hub_commands.go`
- Modify: `cmd/serf-tui/main.go`
- Test: `cmd/serf-tui/hub_start_test.go`

- [ ] **Step 1: Write failing startup tests**

Add tests for:

- `127.0.0.1:9999` normalizes to `http://127.0.0.1:9999` and local bind `127.0.0.1:9999`.
- `http://localhost:9180/` normalizes to local.
- `http://example.com:9180` is not local and does not auto-start.
- binary resolution prefers explicit path, then sibling path, then `PATH`.

- [ ] **Step 2: Run red**

Run: `go test ./cmd/serf-tui -run TestHub`

Expected: fail because helpers do not exist.

- [ ] **Step 3: Implement startup helpers**

Implement address normalization, local detection, binary resolution, detached hub start, and bounded `GET /api/health` wait.

- [ ] **Step 4: Replace main flags**

Change `serf-tui` flags to `--hub-addr`, `--hub-bin`, `--no-auto-start-hub`, `--log-file`, and `--debug`. Remove direct calls to `startEmbedded`, `pickSession`, `newModel(addr, ...)`, and `streamSSE(... daemon addr ...)` from `main.go`.

- [ ] **Step 5: Run green**

Run: `go test ./cmd/serf-tui -run 'TestHub|TestMain'`

Expected: pass after updating obsolete resume-hint tests.

- [ ] **Step 6: Commit**

Run:

```bash
git add cmd/serf-tui
git commit -m "Start serf-tui from hub"
```

## Task 4: TUI Dashboard And Session Drill-in

**Files:**
- Create: `cmd/serf-tui/hub_model.go`
- Modify: `cmd/serf-tui/sse_client.go`
- Modify: `cmd/serf-tui/model.go`
- Test: `cmd/serf-tui/hub_model_test.go`
- Test: `cmd/serf-tui/sse_client_test.go`

- [ ] **Step 1: Write failing model tests**

Add tests for:

- Initial model fetches tree and renders live/project rows.
- Selecting a row opens session detail.
- `USER_INPUT` replay event renders a user message.
- `ASSISTANT_TEXT_END` with `text` renders assistant text.
- `REPLAY_DONE` is not treated as an error.

- [ ] **Step 2: Write failing SSE parser tests**

Add tests for multiline `data:`, fields without spaces after colon, final event without blank terminator, non-200 stream response, and `Last-Event-ID` header.

- [ ] **Step 3: Run red**

Run: `go test ./cmd/serf-tui -run 'TestHubModel|TestParseSSE|TestStreamSSE'`

Expected: fail on missing model/parser behavior.

- [ ] **Step 4: Implement dashboard model**

Implement dashboard mode, session mode, search/spawn placeholders, row selection, tree refresh, session detail loading, and transcript-follow stream command.

- [ ] **Step 5: Implement replay-compatible reducer**

Update event handling so replay `USER_INPUT` appends a user message and replay `ASSISTANT_TEXT_END.text` appends assistant text when no deltas were emitted.

- [ ] **Step 6: Implement SSE parser**

Make parsing spec-compliant and add URL-based stream helper with optional `Last-Event-ID`.

- [ ] **Step 7: Run green**

Run: `go test ./cmd/serf-tui -short`

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add cmd/serf-tui
git commit -m "Add serf-tui hub dashboard"
```

## Task 5: TUI Write Actions

**Files:**
- Modify: `cmd/serf-tui/hub_model.go`
- Modify: `cmd/serf-tui/hub_commands.go`
- Test: `cmd/serf-tui/hub_model_test.go`

- [ ] **Step 1: Write failing action tests**

Add tests for:

- Enter sends through `POST /api/sessions/{ref}/send`.
- Busy send preserves input.
- `/tasks` fetches hub tasks endpoint.
- `/details` fetches hub details endpoint.
- `/interrupt`, `/compact`, `/clear`, and `/model <name>` call hub endpoints.
- Clear response navigates to the returned ref.

- [ ] **Step 2: Run red**

Run: `go test ./cmd/serf-tui -run 'TestHubModel.*(Send|Tasks|Details|Interrupt|Compact|Clear|Model)'`

Expected: fail until actions are wired.

- [ ] **Step 3: Implement actions**

Implement write actions through `hubapi.Client`; gate them on `SessionCapabilities`; keep text in input on failure.

- [ ] **Step 4: Run green**

Run: `go test ./cmd/serf-tui -short`

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add cmd/serf-tui
git commit -m "Wire serf-tui hub actions"
```

## Task 6: Docs And Final Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-05-09-serf-tui-universal-hub-client-design.md` only if implementation changes a design decision.

- [ ] **Step 1: Update docs**

Replace old `serf-tui --resume`, `--addr`, and embedded-session usage with hub-dashboard usage.

- [ ] **Step 2: Run verification**

Run:

```bash
go test ./... -short
go vet ./...
make build
make build-hub
```

Expected: all pass.

- [ ] **Step 3: Commit**

Run:

```bash
git add README.md docs/superpowers/specs/2026-05-09-serf-tui-universal-hub-client-design.md
git commit -m "Document hub-backed serf-tui"
```

## Self-review Checklist

- Spec says no back-compat: plan removes main code paths for `--addr`, embedded startup, `--resume`, `--resume-last`, and `--list-sessions`.
- Spec says auto-start hub: plan includes normalization, local-only startup, detached process, and health wait.
- Spec says hub source of truth: plan routes TUI through `internal/hubapi.Client` and hub JSON endpoints.
- Spec says durable drill-in: plan implements replay-compatible stream and reducer behavior.
- Spec says capabilities: plan adds daemon/hub capabilities and gates TUI actions.
- Spec says frequent commits: plan commits after shared API, hub API, startup, dashboard, actions, docs.
