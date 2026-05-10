# Web Slash Commands — Design

**Status:** approved
**Date:** 2026-05-10
**Scope:** `cmd/serf-hub` web UI

## Goal

Bring the TUI's slash-command surface to the web in the form of a canonical command palette: every navigable action the chrome offers is reachable by name via a single overlay, with TUI muscle memory preserved (`/` in an empty message box opens it) and a keyboard-first shortcut (`Cmd-K`) available everywhere.

## Architecture

The existing `#search-dialog` overlay (driven by `assets/search.js`) is extended into a unified palette that handles both session search (today's behavior) and commands. One overlay, one shortcut, one mental model — VS-Code-style.

Mode is determined by the query's first character:
- `query === ""` or any text not starting with `/` → **search mode** (existing behavior: fetch `/api/search`, render live/past/in-session results).
- `query[0] === "/"` → **command mode** (filter the local command registry; never hits the backend).

Pure client-side; no new HTMX swaps. No new dialog element — the existing `<dialog id="search-dialog">` is reused.

The palette is a parallel discovery surface — not a replacement for buttons and links. Existing chrome stays intact. Commands dispatch by calling the same endpoints, navigations, and slide-over toggles the buttons already use.

### Command registry

A flat array of plain JS objects in `search.js` (the extended module) is the canonical command list. Each entry:

```js
{
  id: "compact",                 // stable; used in tests
  title: "Compact transcript",
  hint: "free up token space",   // muted right-side text
  keywords: ["compress", "ctx"], // extra search terms
  scope: "session",              // "global" | "session" | "ended-ok"
  args: null,                    // or args descriptor (see below)
  run: (ctx) => ...,             // dispatch function
}
```

`ctx` is built fresh on every open from `document.getElementById("conversation")`:

```js
{
  sessionId: <string | null>,
  sessionState: "live" | "ended" | null,
  onPage: "home" | "session" | "settings" | "spawn" | "other",
}
```

No persistent JS state across opens — the palette reads scope and context from the DOM each time it opens.

### Args descriptor

```js
args: {
  kind: "enum" | "free",
  placeholder: "choose a model…",
  source: async (ctx) => [{ id, label, hint? }, ...],  // for enum
  run: (ctx, arg) => ...,                              // arg = item or free text
}
```

## Triggers and lifecycle

**Opening:**
- `Cmd-K` / `Ctrl-K` from anywhere → palette opens, empty query, search mode active (existing behavior preserved; empty query renders nothing until the user types).
- `/` typed as the first character of an empty `.message-input` textarea → palette opens with the query pre-seeded to `/`. The `/` is intercepted; nothing lands in the textarea.
- Existing `[data-search-trigger]` sidebar link → same `open()`, search mode.

**Mode transitions during a single open:**
- The mode is recomputed on every input event from the query's first character. A user can freely toggle by editing the leading `/`.
- Switching modes resets the highlight to position 0.

**Closing:**
- `Escape` from search mode or command-filter mode → closes without running.
- `Escape` from command-args mode → back to command-filter mode (not closed).
- Click outside the modal (backdrop) → closes.
- A successful command run that does not navigate → closes.
- A navigation command → page change closes the palette implicitly.
- Selecting a search result → existing behavior (navigation closes it).

**Focus management:**
- On open, the previously-focused element is captured.
- On close-without-running, focus restores.
- After a command run: argless session commands return focus to the message textarea (if present); navigation commands let the new page own focus.

**Scope filtering (command mode only):**
- `global` always shows.
- `session` shows only when `onPage === "session"` AND `sessionState === "live"`.
- `ended-ok` shows when `onPage === "session"` regardless of state.

Search mode is unaffected by scope — it always queries the backend over all live and past sessions.

## UX: three modes

The palette has three states sharing the same `#search-dialog` shell.

### Search mode (default; query is empty or doesn't start with `/`)

Unchanged from today:
- Debounced fetch to `/api/search` on input.
- Live / Past / In-session sections.
- Up/Down move highlight; Enter opens active; Cmd-Enter opens in new tab; Shift-Enter jumps to turn for in-session results.

### Command-filter mode (query starts with `/`)

- The `/` is part of the query (so the user can backspace back to search mode).
- The list below shows registered commands whose `id`, `title`, or `keywords` match the query stripped of its leading `/` (case-insensitive substring; no fuzzy in v1).
- Up/Down move the highlight; Enter runs the highlighted item; mouse hover also moves the highlight; click also runs.
- Argless command → run, close.
- Command with args → switch to command-args mode for that command.
- Cmd-Enter and Shift-Enter are no-ops in this mode (they belong to search).

### Command-args mode

- A pill above the input shows the selected command name with a small `×` to back out to command-filter mode.
- Input clears; placeholder comes from `args.placeholder`.
- For `kind: "enum"`:
  - On entry, call `args.source(ctx)` (may be async — show a "loading…" row).
  - List renders items, filterable by query.
  - Enter on the highlighted item calls `args.run(ctx, item)`.
- For `kind: "free"`:
  - No list.
  - Enter calls `args.run(ctx, queryText)` with the raw query.

### Visual treatment

Reuses the existing `#search-dialog` modal exactly as it is today: native `<dialog>` element, top-anchored, existing widths and theme tokens. The command sections borrow the existing `search-section-header` and row styles; args mode adds a single new pill element above the input. No layout reorganization.

## Command coverage

### Session actions (live only — scope: `session`)

| id | title | hint | args | dispatch |
|---|---|---|---|---|
| `compact` | Compact transcript | free up token space | none | POST `/s/<id>/compact` |
| `interrupt` | Interrupt model call | cancel in-flight turn | none | POST `/s/<id>/interrupt` |
| `clear` | Clear context | start fresh in this session | none | POST `/s/<id>/clear` *(new proxy)* |
| `shutdown` | Shut down daemon | ends this session | none | confirm → POST `/s/<id>/shutdown` |
| `model` | Switch model | … | enum from `/models` | POST `/s/<id>/model` |
| `steer` | Steer model | inject mid-turn | free text | POST `/s/<id>/steer` |
| `fork` | Fork from turn | edit and branch | enum from transcript | POST `/s/<id>/fork` |

### Session info (live or ended — scope: `ended-ok`)

| id | title | hint | dispatch |
|---|---|---|---|
| `copy-id` | Copy session ID | clipboard | `navigator.clipboard.writeText` + toast |
| `tasks` | Toggle tasks panel | — | click `[data-tasks-trigger]` |
| `status` | Toggle session details | — | click `[data-details-trigger]` |
| `project` | Open this session's project | — | navigate to project home |
| `help` | Show all commands | TUI parity reference | open in-palette help panel |

### Global (scope: `global`)

| id | title | hint | args | dispatch |
|---|---|---|---|---|
| `new` | New session | spawn page | none | navigate `/spawn` |
| `spawn` | Spawn with task | new session, prefilled | free text | navigate `/spawn?task=<encoded>` |
| `settings` | Open settings | — | none | navigate `/settings/general` |
| `theme` | Switch theme | dark/light | enum `["dark","light"]` | flip body class + persist localStorage |
| `dashboard` | Go to dashboard | — | none | navigate `/` |
| `search` | Search sessions | clear `/` and search | none | clear the input, returning to search mode |

### Dropped from TUI

- `/quit` — no web analog. Closing the tab is the user's job.
- `/agents` — TUI-specific transcript view. Web shows main and subagent transcripts inline.

## Server changes

One small exception to "purely client-side": the hub does not currently expose `/s/<id>/clear`. The TUI calls the daemon's `/clear` directly via rendezvous. To support the palette command, add `clear` to the action proxy switch in `handleSession` (parallel to `interrupt`, `compact`, `shutdown`). Five lines including the test fixture wire-up.

No other server work.

## Implementation surfaces

### New files

- `cmd/serf-hub/jstest/test-search-commands.js` — JSDOM tests for command-filter mode, command-args mode, scope filtering, `/`-trigger from textarea, mode toggling.

### Modified files

- `cmd/serf-hub/assets/search.js` — extended with:
  - command registry (top of file or split into a co-located `commands.js` if it grows past ~150 lines; default plan is keep it in `search.js`).
  - a `mode()` helper that returns `"search" | "command-filter" | "command-args"` based on query + selected command state.
  - the `input` event handler now branches on mode: search → existing `search()` fetch; command-filter → local filter + render; command-args → enum fetch + filter or free-text.
  - the `Enter` handler now branches on mode similarly.
  - a public `openWith(initialQuery)` method (additive; existing `open()` preserved).
- `cmd/serf-hub/assets/style.css` — small additions for the command-args pill and any command-row variants (~40 lines). Existing search row styles reused.
- `cmd/serf-hub/assets/renderer.js` — hook in the textarea keydown handler: if the textarea is empty and the key is `/`, prevent default and call `window.SerfSearch.openWith("/")`. ~6 lines.
- `cmd/serf-hub/web.go` — add `case "clear":` to the session action proxy switch in `handleSession`.
- `cmd/serf-hub/web_test.go` — `TestWeb_SessionClear_ForwardsToDaemon`.

No template changes — the existing `#search-dialog` markup is reused as-is.

### Module surface

`window.SerfSearch` (additive — existing surface preserved):
- `open()` — existing.
- `close()` — existing.
- `openWith(initialQuery)` — new; opens and seeds the input, triggering the appropriate mode.

### Reused patterns

- Existing search overlay markup, keyboard handlers, debounce, row rendering.
- Model enum source → reuse the model chip's `/models` fetcher.
- Theme persistence → reuse the settings page's `localStorage.theme` key.
- Textarea keydown hook → mirrors the existing `Cmd-Enter` send handler in renderer.

## Test plan

**jstest (JSDOM):**
1. `Cmd-K` opens the palette from home, settings, and session pages.
2. Empty query and non-`/` queries preserve existing search behavior (regression coverage from the existing `test-search.js` continues to pass).
3. Typing `/` switches to command-filter mode (search fetch is not called).
4. Backspacing the `/` back to empty query restores search mode.
5. Typing `/comp` filters to the `compact` command.
6. `Escape` from command-filter mode closes.
7. `Escape` from command-args mode returns to command-filter mode (not closed).
8. `/` at the start of an empty textarea opens the palette; query is seeded to `/`.
9. `/` typed mid-text in the textarea does not open the palette.
10. Session-scoped commands are absent on the home page and present on a live session page.
11. Session-scoped commands are absent when `sessionState === "ended"`; `ended-ok` commands remain.
12. Selecting `/model` switches to args mode; the model list loads; Enter on an item POSTs to `/s/<id>/model`.
13. Backdrop click closes the palette from every mode.

**Go tests:**
- `TestWeb_SessionClear_ForwardsToDaemon` — POST `/s/<id>/clear` is forwarded to the daemon's `/clear` for a live session; returns 404 for an ended session not in the roster.

## Non-goals (v1)

- Frequency-weighted ranking, recent-commands history.
- Fuzzy matching. (Substring is sufficient and predictable.)
- Custom user-defined commands.
- Per-project command sets.
- Slash commands inside the transcript (i.e. `/foo` as a message to the agent — out of scope; this is purely a UI shortcut surface).
- Mobile-specific affordances. The palette will render and function on mobile but is not designed there first.

## Open questions

None blocking. Future polish candidates:
- A `?` keybinding for `/help` from anywhere.
- A "recent commands" header at the top of filter mode.
- Per-command keyboard shortcuts (e.g. `Cmd-Shift-K` for compact).
