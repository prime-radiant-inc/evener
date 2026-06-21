# Serf Hub UX Redesign

Date: 2026-05-08
Status: Design

## Goal

Replace the current `cmd/serf-hub` UI with a quieter, denser, project-organized interface designed for 10–50 concurrent live sessions plus an open-ended history. Optimize for a mix of synchronous (drive an agent) and asynchronous (check in on running work) use. Remove ceremony around sessions: forks, subagents, and resume should feel like a single coherent surface.

## Non-goals

- Mobile UI. Deferred to a follow-up after desktop ships. Implementation should not be hostile to mobile (semantic CSS, no fixed-pixel widths in critical layouts), but designing the mobile view is its own pass.
- Bulk multi-session selection.
- Full-text search across past transcripts. (Already filed as kata #15 v2 — covered by metadata-only search now.)
- Spawn templates (`[[spawn_template]]` in `hub.toml`). YAGNI — a model picker covers the use case.
- Editing system prompts, agent definitions, plugin code, or MCP server commands inside the hub. The hub *shows* these and links "open in editor"; the source files own the truth.

## Architecture

### Hub side

The hub continues to be a sibling binary at `cmd/serf-hub` that proxies REST + SSE to discovered `serf serve` daemons. Subsystems mostly stay:

- **Roster** (rendezvous discovery + status prober) — unchanged.
- **PastIndex** (in-memory metadata search across `<state-dir>/projects/<sha>/sessions/*.meta.json`) — unchanged.
- **Proxies** (REST and SSE) — unchanged.
- **Spawner** (forks `serf serve` subprocesses) — simplified: drops `SpawnTemplate`, accepts a `(provider, model, agent, working_dir, reasoning_effort)` tuple directly.
- **Web** (routes + templates + assets) — substantially rewritten around the new IA: sidebar, workspace pane, spawn surface, settings surface, ⌘K palette, fork affordance.

New hub responsibilities:

- **Fork mapping** — when the user edits a prior message and confirms the fork, the hub tells the daemon to fork at turn N. The hub stores a `parent_session_id` + `divergence_turn` on the new session's metadata.
- **Theme** — preference is per-browser (localStorage). CSS uses `:root[data-theme=light]` / `:root[data-theme=dark]` with both palettes shipped. `prefers-color-scheme` is the default; user can override.
- **Notification preferences** — per-browser (localStorage). All four channels (title-count, favicon dot, OS Notification API, sound) default off.

### Daemon side

Two daemon-side changes are needed for the design to work end-to-end:

1. **Resume preserves `session_id`.** Today `serf serve --resume <id>` mints a new session_id. The hub's "click + type wakes a closed session" model requires the resumed daemon to advertise the same session_id to the rendezvous file and on `/status`. *Open question: implement on the daemon vs maintain a public→internal mapping in the hub.* Recommend daemon-side because it's cleaner and benefits TUI/CLI users too.
2. **Fork-at-turn endpoint.** New `POST /sessions/<id>/fork` on the daemon that takes `{turn: N, edited_message: "…", label: "…"}` and:
   - Creates a new session whose transcript is the parent's first N-1 turns + the edited turn, with `parent_session_id` and `divergence_turn` metadata.
   - Returns the new session_id.
   - The original session is unchanged on disk.
   - The new daemon does NOT start a turn yet — it sits idle until the user types again. (This matches the "resume is invisible" rule.)

## Information architecture

### Two-pane layout

- **Sidebar** (left, fixed-width, scrollable): the canonical multi-session view. Top: `+ new session`, `🔍 search` (or ⌘K). Then sections in this order:
  - **Live** — every running session across every project, sorted by attention (awaiting → processing → warning → idle). Each row shows a status dot, the session title, the project name as a sub-tag, and an age timestamp.
  - **Projects** — every known project (working_dir cluster), each project a small uppercase header with a count. Within a project: top-level sessions by recency, with their subagents indented below them, and their forks at the same indent as the session but dimmed and immediately following the subagents. Collapsed projects show a roll-up dot reflecting the most-attention-needing live child.
- **Workspace** (right, fluid): the open session's view. When nothing is selected, shows the spawn surface.

Live sessions appear in BOTH the Live section and inside their project. They are not duplicated state — the Live section is a filter view.

### Status taxonomy

Five visual states, distinguished by dot color:

| State | Color | Meaning |
|---|---|---|
| awaiting | `#f7768e` (pink) | turn ended with an agent-visible need for the user or an error — needs you |
| processing | `#7aa2f7` (blue) | agent currently working: streaming, tool-calling, compacting |
| warning | `#e0af68` (amber) | context pressure high, near turn limit, or other warn |
| idle | `#9ece6a` (green, or muted gray when not freshly idle) | ready for input |
| ended | `#3a3a44` (gray) | daemon shut down; session archived |

Closed sessions and fork rows that aren't running render the gray dot. Fork rows additionally use a `⎇` glyph and dim text.

### Subagents and forks in the tree

- **Subagent**: `●` colored dot, indented one level under origin, listed only under the originating session.
- **Fork**: `⎇` glyph, same indent as top-level, dim text, immediately follows the parent's subagents (before the next top-level session).
- **Sort**: top-level sessions by recency. Each top-level session's children follow it (subagents indented, forks at same indent and dim). A fork's age does not move it away from its parent.
- **Fork subagents**: do NOT appear in the project tree. They appear inside the fork's workspace pane via a title-bar "↳ subagents (N)" affordance. This avoids the "is that indented row a subagent of the fork above or the parent above the fork?" ambiguity.

## Workspace pane

### Title bar

- Line 1: session title (left, weight 500), `details` link (right).
- Line 2 (small, muted): `branch@sha · ● status · N turns`. For the row that's the dim fork (i.e., the *preserved original*): prefixed with `↳ original of <new-branch-title>, divergence at turn N`. For the new branch (top-level): no prefix — just looks like a regular session.

### Details panel

The `details` link in the title bar opens a slide-over panel (or modal — TBD during build) with rare-glance metadata that doesn't earn body chrome:

- Working directory (full path)
- Files touched this session (count, list, click to filter the transcript)
- Tool histogram (counts per tool)
- Recent commits in the working tree
- System prompt + system-prompt appends (preview)
- Environment variables passed to the daemon
- Daemon PID, listening address, uptime
- Token usage detail (per-turn input/output, cumulative)
- For forks: a small fork-tree visual showing siblings + divergence turns

The panel is one column, scrollable, with section headers. Closes on `esc` or click-outside.

### Conversation body

Two reading tiers:

**Message tier** — user pill and assistant body. 14.5–15 px, full text contrast, generous line height (~1.7), generous vertical breathing room between turns.

- **User**: right-aligned rounded pill (`max-width: 62%`). On hover, two affordances appear top-right: `copy` and `✎ edit`. Clicking edit makes the pill content editable in place.
- **Assistant**: full-width markdown body, no border, no role label, no role icon. Code spans inline.

**Annotation tier** — tool calls, diffs, subagent references. 12.5 px, muted color, indented 28 px from the message column. Reads like margin notes underneath the message that triggered them.

- **Cheap reads** (`read_file`, `grep`, `list_dir`) — single line of prose: `verb · target · result`, separators are middle dots.
- **Mutating tools** (`edit_file`, `write_file`, `shell` w/ side effects) — same one-liner, with a result emphasis (`+82` green, `exit 1` pink). Diff bodies indent one more step (44 px from the message column) under a 1 px hairline left rule, monospace, dimmed.
- **Subagent reference** — same one-liner with `subagent` in purple as the verb: `subagent · <task> · ● done · N turns · time`. Click to navigate to the subagent's session.
- **`communicate` tool** — elided. Renders as a plain assistant message. No tool chrome.

### Bottom strip

- Input box (textarea, generous min-height).
- Below the input, a single hairline-divided row: `model · context-bar · context-numbers · cost · send ⌘↵`. Working directory and branch live in the title bar / details panel; do not repeat them here.

## Spawn surface

Reached via `+ new session` (or by clicking the workspace area when no session is selected). Prompt-first:

- Big task input (textarea). Empty input is allowed — spawns a dormant session.
- Above the input, four chips for things you may change per-spawn:
  - **model** (lists configured models from your providers — provider is implicit in the choice)
  - **working_dir** (recent dirs + free path; validated as absolute + existing)
  - **branch** (auto-suggests worktrees of the project; "create new worktree" option)
  - **access mode** (full access / read-only)
- "advanced" reveals: max-turns, env vars, system-prompt-append, max cost.
- Below: "recent tasks · same project" — clickable rewinds.
- Spawn button (`spawn ⌘↵`).

Sticky defaults are remembered per-project in the browser's `localStorage` (key: `serf-hub.spawn-defaults.<project-sha>`). Fields stored: model, agent, branch, access mode. Across projects, model is sticky to the most recent globally (`serf-hub.spawn-defaults.global.model`).

## Forking

Forks are created by editing a prior user message and confirming. Transcripts are immutable, so a fork is implemented as creating a new session with the edited prefix and preserving the original session unchanged.

### Identity model

After fork, the user's mental "thread" continues forward — same title, same working dir, same model. Implementation:

- The new session inherits the original session's **title** and metadata (working_dir, model, agent, project).
- The original session keeps its session_id and full transcript, gets a `ForkLabel` set from the dialog's input (e.g., `"before TDD approach"`), and is rendered in the sidebar as `<original title> · <ForkLabel>`.
- The new session has `parent_session_id = <original>` and `divergence_turn = N` so the sidebar can group them.

Sidebar effect (immediately after fork):

- The new session is the top-level row carrying the original's name. The user is in its workspace.
- The original session is dim with `⎇` glyph and the user's fork label, sitting below the new session's subagents.

Dim is a permanent property of fork rows, not a state — it conveys "this is a side branch of the row above," not "this is closed."

### User flow

1. Hover any prior user pill: `copy` and `✎ edit` appear top-right.
2. Click `✎ edit` — the pill becomes editable in place.
3. Commit with `⌘↵`. A small confirmation appears in the would-be assistant-response slot:
   > "Editing this message will fork the conversation. The current branch continues with your edited message; the original is preserved as a sibling fork. Label the original: <input, prefilled with auto-suggestion>"
   Cancel keeps the in-place edit.
4. On confirm: hub calls `POST /sessions/<id>/fork` on the daemon with `{turn: N, edited_message, label}`, gets back `{new_session_id}`, navigates the workspace pane to the new session. The original session is re-rendered as the dim fork row.

The new session is dormant after fork — its transcript ends at the edited turn N. When the user types their next message, the daemon spawns/resumes that session and produces turn N+1.

## Resume

Invisible. There is no "resume" button. Clicking any session — live, idle, ended, fork, closed — opens its workspace pane. Status dot reflects current state.

When the user types and sends in a closed session, the hub:
1. Calls the daemon spawner to start `serf serve --resume <session_id>`.
2. Waits for rendezvous (with `started_after` filter, per kata #6).
3. Forwards the user's input to the freshly-spawned daemon.
4. The status dot transitions ended → processing.

For the user, the experience is "click, type, send." No banners, no minted-new-id surprise, no extra clicks.

This requires the daemon-side change to preserve `session_id` on `--resume` (see Architecture §Daemon side).

## Search palette

Triggered by `⌘K` (or clicking `search` in the sidebar). Modal centered overlay. One search box, three scopes:

- **Live** (currently running sessions matching the query)
- **Past** (closed sessions matching by title, ID, or working_dir — metadata only, NOT full transcript text)
- **Inside open session** (string match in the active conversation's turns)

Results ordered most-recent-first within each scope. Keyboard nav: ↑↓ move, ↵ open, ⌘↵ open in new tab, ⇧↵ jump to matched turn (in-session results only).

## Settings

A single Settings page with a left rail and per-section content panes. Read-only panes show truth from on-disk config and link "open in editor"; editable panes provide row UIs.

Sections:

- **General** — startup defaults, hub address, default model.
- **Theme** — light / dark / system.
- **Notifications** — title count, favicon dot, OS notification, sound. All toggles, all default off.
- **Providers** (read-only) — list configured providers, API key presence, default model per provider, models available.
- **Agents** (read-only) — list discovered agent profiles (name, where it's defined, system-prompt preview, tool allowlist preview).
- **Plugins** (read-only) — list plugin directories, versions, contributions (skills/agents/MCPs/hooks).
- **Skills** (read-only) — list skills per plugin, enabled/disabled state, brief description. Toggle enable/disable per agent.
- **MCP servers** (read-only inspection) — list servers, command, status (running/stopped/error), tools count, agents allowed. Live status. View log on error.
- **Hub** — bind address, spawn timeout, results-per-page.
- **Storage** — paths and sizes for state-dir, run-dir, hub.toml location.

## Empty states

- **First run** (no live, no past) — sidebar shows `+ new session · search` only. Workspace shows the spawn surface centered, no recent tasks.
- **No live** (have past) — Live section is hidden entirely. Projects render normally.
- **No past in a project** — project folder still renders. No past rows below the live ones.
- **Search palette, no matches** — single line "no matches in live, past, or this session." No scope sub-headers when all empty.
- **Settings pane, nothing configured** (e.g., no MCP servers) — short text: "no MCP servers configured. Add one in `~/.config/serf/.mcp.json`." with the path code-formatted.
- **New session form, no recent tasks** — section omitted.

## Notifications

All four channels default off. User opts in via Settings → Notifications.

- **Title count** — `(N) serf — <session>` where N is the count of sessions awaiting reply.
- **Favicon dot** — small overlay on the favicon, color is the highest-attention state across all sessions (red > amber > blue > green).
- **OS notification** — fired only on transitions: `idle→awaiting` and `processing→errored`. Never while the tab is focused. Notification text: `serf · <session>` + the agent's most recent message excerpt. Actions: open, snooze 10m, dismiss.
- **Sound** — short tone on the same transitions as OS notification.

## Theme

Both light and dark palettes ship in v1, defined as CSS custom properties on `:root[data-theme=...]`. Default follows `prefers-color-scheme`; user can override.

Color tokens (sketch):

| Token | Dark | Light |
|---|---|---|
| `--bg` | `#0a0a0e` | `#fafafa` |
| `--bg-raised` | `#16161e` | `#f0f0f1` |
| `--text` | `#ececf0` | `#16161e` |
| `--text-muted` | `#7a7a86` | `#6a6a76` |
| `--text-dim` | `#5a5a64` | `#a0a0a8` |
| `--rule` | `#1a1a20` | `#e0e0e3` |
| `--accent` | `#7aa2f7` | `#3b6fc9` |
| `--state-awaiting` | `#f7768e` | `#c43755` |
| `--state-processing` | `#7aa2f7` | `#3b6fc9` |
| `--state-warning` | `#e0af68` | `#a06f1e` |
| `--state-idle` | `#9ece6a` | `#3f7a1e` |
| `--state-subagent` | `#bb9af7` | `#7449c7` |

CSS uses semantic class names tied to the design vocabulary: `.message`, `.user-message`, `.assistant-message`, `.tool-call`, `.tool-call-mutating`, `.diff-body`, `.subagent-reference`, `.sidebar`, `.sidebar-section`, `.live-row`, `.project-row`, `.fork-row`, `.subagent-row`, `.status-dot`, `.bottom-strip`, `.context-bar`, etc. No utility classes.

## Data model changes

Hub side, on `agent.SessionMeta`:

- Add `ParentSessionID` (string, optional) — the session this one was forked from.
- Add `DivergenceTurn` (int, optional) — the turn at which the fork branched.
- Add `ForkLabel` (string, optional) — the user-supplied display name for this branch.

Daemon side:

- `--resume <id>` MUST preserve session_id and write the same id to rendezvous.
- New `POST /sessions/<id>/fork` endpoint, body `{turn, edited_message, label}`, returns `{new_session_id}`. Behavior: write a new session meta + transcript with the prefix copied through turn-1, the edited message as turn N, then idle.

## Implementation notes

- Single-page app or htmx? Today the hub is htmx-driven with vanilla JS for SSE coalescing. The redesign keeps that shape — htmx for partial swaps (sidebar refresh, search, settings panes), `renderer.js` for SSE in the workspace. No SPA framework.
- Per-page Go template sets stay (today's pattern works; CSP-friendly).
- All inline scripts are now in vendored asset files (kata #11 work).
- Roster + PastIndex stay as the data sources for the sidebar; new endpoints serve the combined Live + Projects tree as a single render.
- The `+ new session` form replaces today's `/live/new`. Spawn templates removed.

## Out of scope (reaffirmed)

- Mobile.
- Bulk selection / multi-session actions.
- Cross-transcript full-text search.
- Spawn templates.
- Editing source-of-truth config files in the hub UI.

## Open questions

1. **Daemon `--resume` preserving session_id** — change daemon, or keep public→internal mapping in the hub? Recommend daemon-side. Decision before plan-writing.
2. **Agent allowlist preview in Settings → Agents** — show system-prompt previews and tool allowlists, or just names + locations? The latter is YAGNI-friendly. Recommend names + locations + "open in editor."
3. **Where does fork metadata live** — in `agent.SessionMeta` (reused for live + past), or a hub-side index? Recommend `SessionMeta` because it survives daemon restarts and is already indexed.
4. **Fork dialog timing** — show before the user types more, or after they hit send on a different message? Drawn as: edit → ⌘↵ → fork dialog appears. Confirm and the new branch is active before the user types another message.
