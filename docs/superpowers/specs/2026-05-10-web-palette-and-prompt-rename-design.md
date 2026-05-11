# Web Command Palette + `task` → `prompt` Rename — Consolidated Design

**Status:** palette v1 shipped (commits `d22615c`, `31a55f0`, `e510319`); rename plan scoped, work tracked under kata #46 / #47.
**Date:** 2026-05-10
**Scope:** `cmd/serf-hub` web UI, plus a cross-cutting rename touching `cmd/serf`, `agent/`, `internal/hubapi`.

This document supersedes `2026-05-10-web-slash-commands-design.md` and incorporates the post-implementation learnings, the deferred-item roadmap (kata #42–#45), and the embedded sub-spec for the `task` → `prompt` rename (kata #46 / #47).

---

## Part 1 — Command Palette

### Goal

Bring the TUI's slash-command surface to the web. Every navigable action the chrome offers should be reachable by name through a single overlay, with TUI muscle memory preserved (`/` in an empty message textarea opens it) and a keyboard-first shortcut (`Cmd-K`) available everywhere.

### Status

**Shipped (v1):**
- 18 commands across global / session-live / session-ended scopes
- Three-mode overlay (search / command-filter / command-args)
- Cmd-K from anywhere, `/` at start of empty `.message-input`
- Real-browser smoke pass on home, settings, live session

**Deferred (post-v1):** mobile layout (#42), fuzzy matching + recent commands (#43), design refactor of `Nav` test hook (#44), `/project` rename or real project route (#45).

### Architecture

The existing `#search-dialog` overlay (driven by `assets/search.js`) is extended into a unified palette. One overlay, one shortcut, one mental model — VS-Code style.

Mode is determined by query state:
- `query === ""` or any text not starting with `/` → **search mode** (existing: fetch `/api/search`, render live/past/in-session results).
- `query[0] === "/"` and no selected command → **command-filter mode** (filter the local registry; never hits the backend).
- A command with `args` has been selected → **command-args mode** (collect the argument).

Pure client-side; no new HTMX swaps. No new dialog element. The palette is a parallel discovery surface, not a replacement for buttons and links. Commands dispatch by calling the same endpoints and slide-over toggles the existing buttons use.

#### Command registry

A flat JS array in `assets/search.js`. Each entry:

```js
{
  id: "compact",                 // stable; used in tests
  title: "Compact transcript",
  hint: "free up token space",   // muted right-side text
  keywords: ["compress"],        // extra search terms
  scope: "session",              // "global" | "session" | "ended-ok"
  args: null,                    // or args descriptor (below)
  stayOpen: false,               // if true, run() doesn't close the dialog
  run: (ctx) => ...,             // dispatch for argless commands
}
```

`ctx` is built fresh on every open from `document.getElementById("conversation")`:

```js
{ sessionId, sessionState, onPage }  // onPage ∈ "home"|"session"|"settings"|"spawn"|"other"
```

No persistent JS state across opens — context is re-read each time.

#### Args descriptor

```js
args: {
  kind: "enum" | "free",
  placeholder: "choose a model…",
  source: async (ctx) => [{ id, label, hint? }, ...],   // enum only
  run: (ctx, arg) => ...,                               // arg = picked item or free text
}
```

#### The `Nav` indirection (test hook — see kata #44)

JSDOM's `Location.assign` is non-configurable, so nav-targeting tests need a seam. Production code routes navigations through a module-level `const Nav = { go: (url) => window.location.assign(url) }` exposed on `window.SerfSearch.Nav`. Tests replace `Nav.go` after init to capture targets. The seam is one line; #44 reconsiders whether it stays.

### Triggers and lifecycle

**Opening:**
- `Cmd-K` / `Ctrl-K` from anywhere → palette opens, empty query, search mode active.
- `/` typed as the first character of an empty `.message-input` textarea → palette opens with query pre-seeded to `/`. The `/` is intercepted; nothing lands in the textarea.
- Existing `[data-search-trigger]` sidebar link → same `open()`, search mode.

**Closing:**
- `Escape` from search mode or command-filter mode → closes without running.
- `Escape` from command-args mode → back to command-filter mode (not closed), restoring the pre-args filter.
- Backdrop click → closes from any mode.
- A successful command run that doesn't navigate → closes (unless `stayOpen: true`).
- Navigation commands → page change closes the palette implicitly.

**Focus management:**
- On open, the previously-focused element is captured; on close-without-running, focus restores.
- After a command run: argless session commands return focus to the message textarea; navigation commands let the new page own focus.

**Scope filtering (command mode only):**
- `global` always shows.
- `session` shows only when `onPage === "session"` AND `sessionState !== "ended"`.
- `ended-ok` shows when `onPage === "session"` regardless of state.

Search mode is unaffected by scope.

### UX: three modes

#### Search mode

Unchanged from before this feature:
- Debounced fetch to `/api/search` on input.
- Live / Past / In-session sections.
- ↑↓ move highlight; Enter opens active; Cmd-Enter opens in new tab; Shift-Enter jumps to turn.

#### Command-filter mode

- The `/` is part of the query so the user can backspace back to search mode.
- List shows registered commands whose `id`, `title`, or `keywords` match the query stripped of its leading `/` (case-insensitive substring; no fuzzy in v1 — see #43).
- ↑↓ navigate; Enter runs the highlighted command.
- Argless command → run, close (unless `stayOpen`).
- Command with args → switch to command-args mode for that command.
- Cmd-Enter and Shift-Enter are no-ops; they belong to search mode.

#### Command-args mode

- A pill above the input shows the selected command name with a small `×` to back out.
- Input clears; placeholder comes from `args.placeholder`.
- For `kind: "enum"`:
  - Call `args.source(ctx)` (may be async — show "loading…" frame).
  - List renders items, filterable by query.
  - Enter on the highlighted item calls `args.run(ctx, item)`.
- For `kind: "free"`:
  - No list; Enter calls `args.run(ctx, queryText)`.

#### Visual treatment

Reuses `#search-dialog` exactly as it was — native `<dialog>`, top-anchored modal, existing widths and theme tokens. The args pill is injected via JS at init into the dialog header.

### Command registry (18 commands)

**Session actions (live only — scope: `session`):**

| id | title | args | dispatch |
|---|---|---|---|
| `compact` | Compact transcript | — | POST `/s/<id>/compact` |
| `interrupt` | Interrupt agent | — | POST `/s/<id>/interrupt` |
| `clear` | Clear context | — | POST `/s/<id>/clear` *(new proxy)* |
| `shutdown` | Shut down daemon | — | confirm → POST `/s/<id>/shutdown` |
| `model` | Switch model | enum from `/api/models` | POST `/s/<id>/model` body `{model:"provider/name"}` |
| `steer` | Steer model | free text | POST `/s/<id>/steer` body `{text}` |

**Session info (live or ended — scope: `ended-ok`):**

| id | title | dispatch |
|---|---|---|
| `copy-id` | Copy session ID | `copyToClipboard(id)` |
| `tasks` | Toggle tasks panel | click `[data-tasks-trigger]` |
| `status` | Toggle session details | click `[data-details-trigger]` |
| `project` | Reveal session's project | sidebar scroll + uncollapse (#45 may rename or re-route) |

**Global (scope: `global`):**

| id | title | args | dispatch |
|---|---|---|---|
| `new` | New session | — | nav `/new` |
| `spawn` | Spawn with task | free text | nav `/new?task=<encoded>` |
| `settings` | Open settings | — | nav `/settings` |
| `theme` | Switch theme | enum `[dark, light]` | flip body class + persist localStorage |
| `dashboard` | Go to dashboard | — | nav `/` |
| `search` | Search sessions | — | clear input (back to search mode), `stayOpen` |
| `help` | Show keyboard shortcuts | — | render shortcuts panel into results, `stayOpen` |

**Dropped from TUI parity:**
- `/quit` — no web analog.
- `/agents` — TUI-only transcript view; web shows main and subagent transcripts inline.
- `/fork` — was in the v1 plan but dropped (#34). Fork requires an edited message; the palette has no way to gather one. Users invoke fork through the per-turn edit affordance instead.

### Helpers worth knowing about

- `copyToClipboard(text)` — prefers `navigator.clipboard.writeText`; falls back to textarea + `document.execCommand("copy")` for non-secure-context deployments. On both-fail, surfaces via `window.SerfRenderer.appendBanner("error", …)`.
- `applyTheme(name)` — toggles `body.light-theme` / `body.dark-theme` and persists `localStorage["serf-hub.theme"]`. Shared between the palette and the settings page.
- `revealProject(ctx)` — finds the sidebar link for `ctx.sessionId`, uncollapses its enclosing `[data-project-key]` section, scrolls into view.
- `renderHelpPanel()` — paints the keyboard-shortcut reference into the results pane; clears `items` so Enter is a no-op until the user types again.
- `Nav.go(url)` — production calls `window.location.assign`; tests replace this function to capture nav targets.

### Server changes

`/s/<id>/clear` is the only new server route. Added to the action proxy switch in `web.go` alongside the existing `interrupt`/`compact`/`shutdown` cases. Five lines plus one Go test.

`/new` and `/workspace/spawn` were extended to forward an optional `?task=<text>` query param so the `/spawn` palette command's pre-fill round-trip actually works. `spawnViewData.DefaultTask` carries the value into the rendered textarea. (This is the only existing field that will need renaming in #46 — wire it via `DefaultPrompt` while keeping the URL param at `?task=` until #47.)

### Implementation surfaces

**Modified files:**
- `cmd/serf-hub/assets/search.js` — registry, mode branching, args mode, helpers, `Nav` indirection, `window.SerfSearch.{open, close, openWith, Nav}`
- `cmd/serf-hub/assets/style.css` — pill, command rows, args items, help-row layout
- `cmd/serf-hub/assets/renderer.js` — `/` at start of empty textarea → `SerfSearch.openWith("/")`
- `cmd/serf-hub/templates/app.html` — ARIA on the search dialog markup
- `cmd/serf-hub/templates/partials/spawn.html` — textarea content pre-fill from `.DefaultTask`
- `cmd/serf-hub/web.go` — `clear` action proxy; `handleIndex` forwards `?task`; `spawnViewData.DefaultTask`
- `cmd/serf-hub/web_test.go` — `TestWeb_SessionAction_ClearForwards`, `TestWeb_WorkspaceSpawn_PrefillsTaskFromQuery`, `TestWeb_Index_NewRouteForwardsTaskToWorkspace`

**New test:**
- `cmd/serf-hub/jstest/test-search-commands.js` — JSDOM coverage of every mode, scope filtering, the `/` textarea trigger, the args back-out, ARIA roles, and a per-command sweep covering all 18 dispatches.

### Test plan

**JSDOM (test-search-commands.js):**
- Mode transitions (search → command-filter → command-args and back)
- Scope filtering (global on home, full set on live, ended-ok on ended)
- `/` textarea trigger seeds the palette
- ARIA `aria-selected` and `aria-activedescendant` track ArrowDown
- Per-command sweep: one fresh JSDOM per command, expected side effect asserted (POST URL, nav target, panel-trigger click, clipboard write, theme persistence, etc.)

**Go (web_test.go):**
- `TestWeb_SessionAction_ClearForwards` — clear proxy forwards to daemon
- `TestWeb_SessionAction_NotLive_404` — extended to cover clear
- `TestWeb_WorkspaceSpawn_PrefillsTaskFromQuery` — `?task=<text>` pre-fills textarea
- `TestWeb_Index_NewRouteForwardsTaskToWorkspace` — `/new?task=…` propagates to `/workspace/spawn?task=…`

**Real-browser smoke (post-fix verification):**
- Cmd-K opens on every page; scope filter correct (7 commands on home, 17 on live)
- `/help` renders the shortcuts panel
- `/theme` → args mode, Dark/Light items rendered, Esc restores pre-args filter
- `/tasks` toggles the panel
- `/`-from-empty-textarea trigger works; the `/` is intercepted
- `/api/models` returns real models and `/model` pickers them
- `?task=` pre-fill survives the full URL → form round-trip
- No console errors

### Open follow-ups (kata)

| # | Title | Scope |
|---|---|---|
| 42 | Palette mobile layout | v2 deferred |
| 43 | Fuzzy matching + recent commands | v2 deferred |
| 44 | Rethink `window.SerfSearch.Nav` indirection | design discussion |
| 45 | `/project` doesn't navigate — it scrolls; rename or build real route | polish |

---

## Part 2 — `task` → `prompt` Rename (kata #46 / #47)

### Goal

Eliminate one of two distinct meanings that share the word "task" in the codebase, leaving "task" reserved for the agent-task domain (TodoWrite-style task list) and renaming the spawn-time initial-message concept to "prompt".

### Why now

The slash-commands work added one more user-facing surface saying "task" to mean "the initial prompt" (`/spawn <task>`, `?task=`, the textarea placeholder). Cleaning this up before more surfaces accumulate.

### Scope

Two domains share the word and only one is the rename target:

| Concept | Today | After |
|---|---|---|
| TodoWrite-style task list (`agent.Task`, `/tasks` endpoint, `task_list` events, `--share-task-store`, the `/tasks` palette command) | "task" | **stays "task"** |
| The initial user message at spawn time | "task" | **becomes "prompt"** |

### Inventory (rename targets only)

**Internal Go (safe — no external observers):**
- `cmd/serf/run.go`: `runConfig.task string`, local vars
- `cmd/serf/main.go`: usage line `<task>`, local vars
- `cmd/serf-hub/web.go`: `spawnRequest.Task`, `RecentTasks []string`, schema entry `{Name:"task"}`, details-row label, `spawnViewData.DefaultTask`
- `agent/snapshot.go`: `SessionMeta.OriginalTask` (Go field name)
- `agent/session.go`, `agent/fork.go`, `agent/strategy_session_log.go`: `extractOriginalTask` helper, vars
- All Go test files referencing the above

**User-facing text (safe):**
- `templates/partials/spawn.html`: `"describe the task…"` placeholder, `"recent tasks"` header
- CLI help text: `<task>` → `<prompt>`
- README and docs

**Wire / storage (deferred to #47):**
- POST `/api/spawn` JSON field `"task"` — consumed by serf-tui via `hubapi.SpawnRequest`
- `.meta.json` field `"original_task"` — every saved session has it
- HTML form field `<textarea name="task">` — consumed by spawn.go + spawn.js (internal-scope, but observable through DOM)

**Brand-new (rename freely, no compat surface):**
- The palette's `/spawn <task>` URL param `?task=` → `?prompt=` (only this code reads it)

### Plan

#### Kata #46 — internal + UI (no wire changes)

1. Rename Go fields, vars, and helpers everywhere they semantically mean "prompt":
   - `runConfig.task` → `runConfig.prompt`
   - `spawnRequest.Task` → `spawnRequest.Prompt`
   - `spawnViewData.DefaultTask` → `DefaultPrompt`
   - `spawnViewData.RecentTasks` → `RecentPrompts`
   - `SessionMeta.OriginalTask` → `OriginalPrompt` *(Go field only; **keep the JSON tag `original_task`** — wire migration is #47)*
   - `extractOriginalTask` → `extractOriginalPrompt`
2. Template strings: `"describe the prompt…"`, `"recent prompts"`, details-row label `"prompt"`.
3. CLI help text: `<task>` → `<prompt>`.
4. Spawn form field `name="task"` → `name="prompt"` (internal scope — only the spawn page and the Go handler read it; coordinated update covers both).
5. URL param: `?task=` → `?prompt=` in `handleIndex`'s forwarder, in `handleWorkspaceSpawn`'s reader, and in the palette's `/spawn` command's `Nav.go(…)` payload. No compat needed — this is brand-new code.
6. Update all Go tests to the new Go field names.
7. Update README and docs.

**Result:** the only places the literal string `"task"` remains in the spawn/prompt domain are JSON wire tags (`json:"task"` on `spawnRequest`, `json:"original_task"` on `SessionMeta`) — deliberately deferred to #47.

#### Kata #47 — wire migration (blocked by #46)

1. `spawnRequest` accepts both `"task"` and `"prompt"` JSON keys on input; emits `"prompt"` on output. Custom `UnmarshalJSON` or paired tags.
2. `internal/hubapi/SpawnRequest`: add `Prompt string`, deprecate `Task string`. Serf-tui updated to send `prompt`.
3. `agent/snapshot.go` `SessionMeta`: accept both `original_task` and `original_prompt`; write `original_prompt`. Test coverage for reading legacy meta files.
4. After a release cycle where both names are accepted, drop `task` / `original_task`.

### Risk

Low for #46 (mechanical rename, no observable behavior change). #47 has higher risk: any external client of `/api/spawn` that hard-codes the `task` field needs a migration path, and existing `.meta.json` files must remain readable. Both are tractable with the dual-accept approach.

### Non-goals

- Renaming `agent.Task`, `--share-task-store`, `/tasks`, `task_list` events. Those are agent-task domain.
- Renaming the kata's name (no, "kata" is the issue tracker; orthogonal).

---

## Cross-references

- Implementation commits: `d22615c` (initial palette), `31a55f0` (command sweep tests), `e510319` (p1/p2 fixes including #33–#41)
- Closed kata: #33, #34, #35, #36, #37, #38, #39, #40, #41
- Open kata: #42 (mobile), #43 (fuzzy/recent), #44 (Nav rethink), #45 (project rename), #46 (rename internal), #47 (wire migration)
- Earlier spec this supersedes: `2026-05-10-web-slash-commands-design.md`
