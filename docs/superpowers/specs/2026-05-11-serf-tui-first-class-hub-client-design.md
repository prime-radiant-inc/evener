# Serf TUI First-Class Hub Client Design

Date: 2026-05-11
Status: Draft for implementation planning

Revision note, 2026-05-14: Jesse approved a design-system pass before further
implementation. This spec now treats the view taxonomy, widget catalog, focus
contract, and fixture-backed samples as required implementation inputs, not
optional polish.

## Summary

`serf-tui` should be a first-class universal terminal client for Serf, not a thin dashboard wrapped around fragments of the old single-session TUI. The app starts at a live-session dashboard grouped by project, drills into a polished session workspace, supports spawning and resuming through the hub, integrates the main-line Serf-owned OpenAI OAuth stack as a first-class auth flow, and preserves the interaction quality users expect from Codex: clear navigation, slash commands, scrollback, fork-from-turn, model picking, task/status views, and reliable end-to-end behavior.

The implementation foundation remains Bubble Tea. The repair is not a rewrite away from Bubble Tea; it is a Bubble Tea architecture cleanup:

- One app shell owns navigation, global key handling, and layout.
- One command registry owns slash commands, palette commands, help text, key hints, capability checks, and dispatch.
- One reusable session surface owns transcript rendering, input, scrollback, tool expansion, fork selection, task/status overlays, and live/replay reduction.
- The hub remains the only backend seam for discovery, spawn, resume, fork, auth status/login/logout, model listing, task/status, REST actions, and SSE.

This is a breaking design. There is no embedded/direct TUI compatibility path.

## Problem

The current hub TUI behaves like a second app that embeds pieces of the old app:

- `cmd/serf-tui/main.go` now starts `newHubModel(...)` unconditionally.
- The old single-session `model` still exists and is embedded inside `hubModel`, but the old `Update` loop and command behavior are not the active top-level product.
- Slash command help is shared text, but hub mode implements only part of the command set.
- Model loading, spawn, resume, and command behavior are tested in pieces, but not as a coherent terminal product.
- The dashboard and the session workspace do not yet feel like one clean system.

The result is confusing: some old features still exist in code, some are partially reimplemented, and some are advertised but missing. The fix is to promote the hub TUI to the actual product architecture and port the old session strengths into shared components instead of continuing to copy behavior into `hub_model.go`.

## Product Bar

The TUI should be at least as good as Codex for daily work. That means:

- Keyboard-first, with no hidden dead ends.
- Clean visual hierarchy in a terminal: a calm dashboard, readable transcript, restrained borders, consistent color states, good spacing, and no noisy chrome.
- Fast drilldown from "what is running?" to "what happened?" to "what do I do next?"
- Slash commands and command palette are reliable and discoverable.
- Auth state is visible before it causes spawn/model failures.
- Help text is generated from actual command definitions.
- Every disabled action explains why.
- Every network/backend failure is visible, specific, and recoverable.
- End-to-end tests drive the actual terminal UI, not only unit reducers.

## Goals

- Make `serf-tui` a polished hub-backed terminal app for live, idle, ended, forked, and resumed sessions.
- Preserve and improve the best old TUI behaviors: slash commands, transcript rendering, tool expansion, scrollback, model picker, theme support, task/status views, and clear input semantics.
- Make the dashboard live-first and project-organized.
- Let users drill into a project and then into a session without losing their place.
- Let users return to the dashboard from anywhere with `ctrl+o` and `/dashboard`.
- Let `esc` in a session enter transcript browse mode, where users can scroll back, select prior turns, expand tools, and fork a selected user turn.
- Let spawn work from the dashboard with live model discovery, filtered to models usable by Serf.
- Make Serf-owned OpenAI OAuth available from CLI, hub, and TUI without reusing Codex credentials.
- Ensure hub-spawned daemons resolve the same Serf state-dir auth as manually started `serf` processes.
- Use provider-qualified models everywhere: `provider/model`.
- Keep hub APIs as the only backend contract so future remote hosts fit naturally.
- Build automated tmux E2E coverage for the full user journey.

## Non-Goals

- No embedded/direct single-session compatibility mode.
- No HTML scraping from the web hub.
- No second rendezvous or filesystem discovery path in the TUI.
- No complete remote-host implementation in this phase.
- No attempt to clone every Codex feature before the core hub TUI is correct.
- No terminal editor for provider config, plugin code, MCP definitions, or prompts.
- No reuse of Codex auth state, `~/.codex/auth.json`, browser profile cookies, or any other tool-owned credentials.
- No generic multi-provider OAuth framework before OpenAI is correct end-to-end.
- No OS keychain dependency in this phase; storage remains Serf-owned state-dir storage with owner-only permissions.
- No broad visual redesign of the web hub as part of this work.

## Brainstormed Approaches

### Approach A: Patch `hub_model.go` Until It Has Parity

Keep the current file structure and add missing slash commands, model picker behavior, spawn fixes, and tests directly in `hub_model.go`.

Pros:

- Fastest short-term path.
- Smallest immediate diff.
- Preserves current code layout.

Cons:

- Continues the architectural mistake.
- Help text, slash commands, palette entries, and key hints stay easy to desynchronize.
- Session behavior remains split between old `model` and hub wrapper.
- Hard to make the UI polished because layout, navigation, and command concerns stay tangled.

Rejected. This is why the current implementation feels incomplete.

### Approach B: Revert to the Old TUI and Bolt a Dashboard Around It

Make the old session model top-level again and add a dashboard mode around it.

Pros:

- Restores slash command behavior quickly.
- Reuses the old session event loop directly.

Cons:

- Reintroduces the wrong backend seam.
- Old TUI expects one daemon address, while the new product needs session refs through the hub.
- Resume, fork, project drilldown, and future remote hosts become awkward.

Rejected. It fights the hub control-plane direction.

### Approach C: Build a First-Class Hub TUI with Shared Components

Keep Bubble Tea, but split the app into focused components and make commands data-driven. Port the old session strengths into a hub-native session surface.

Pros:

- Correct architecture for universal sessions.
- Solves help/command/key drift.
- Makes tests straightforward because commands and modes have explicit contracts.
- Allows a deliberate visual polish pass instead of accidental layout accretion.

Cons:

- More upfront work.
- Requires touching several TUI files rather than one tactical patch.

Chosen. This is the only approach that gets to "Codex-grade or better" without carrying the current confusion forward.

## View And Widget System

The TUI has two kinds of surfaces:

- Navigation/control surfaces: dashboard and project. These show lists,
  filters, command/search palette entries, launch entry points, details, and
  diagnostics. They do not have a chat composer and never treat bare printable
  text as a prompt.
- Work surfaces: session, busy/steer, browse/fork, and spawn. These reserve
  stable space for the current input or form state. The input mode can change,
  but drafts must remain visible and recoverable.

The approved widget catalog is:

- `AppShell`: owns top bar, body region, overlay slot, footer/composer region,
  responsive layout, and global key dispatch.
- `TopBar`: shows current scope, source label, cwd/project, connection state,
  selected model, and auth summary.
- `ActionBar`: renders generated key hints and disabled reasons from the
  command registry. It never advertises unsupported actions.
- `SessionList`: renders dashboard/project rows for project headers, live
  sessions, recent sessions, and diagnostic notices.
- `CommandPalette`: combined command/search surface opened by `/` on
  dashboard/project and by `/search` from sessions.
- `PickerPanel`: shared filterable list for model, theme, auth actions, agent
  transcripts, help, and command choices.
- `Composer`: shared textarea for message, steer, fork draft, read-only draft
  preservation, and unavailable-action explanations.
- `FormField`: focused field primitive for spawn prompt, harness, model,
  project/cwd, and future spawn options.
- `TranscriptView`: one reducer-backed renderer for replay and live transcript
  events.
- `ToolGroup`: groups tool calls and results into one coherent transcript unit.
- `DetailsDrawer`: wide-terminal side panel and narrow-terminal explicit
  overlay for selected row/session diagnostics.
- `Notice`: structured error, warning, auth-required, and action-unavailable
  display. The TUI must not use browser dialogs or transient alerts.

Focus contract:

- Printable keys go to the focused widget first.
- Global keys run only when the focused widget declines them.
- A widget that accepts text input owns printable keys, cursor movement,
  history movement, multiline insertion, and draft preservation.
- Pickers own filter text, arrow navigation, enter selection, and escape close.
- The app shell owns only mode navigation, dashboard return, quit, and
  connection/window messages that are not handled by the focused widget.

Design decisions from the mockup pass:

- `/` is the combined command/search palette on dashboard and project views.
- Wide terminals show a details drawer by default. Narrow terminals open the
  same details as an explicit overlay.
- `n` is the primary new/spawn key. Do not add an `s` compatibility alias
  unless Jesse explicitly approves backward compatibility.

## User Experience

### Startup

`serf-tui` starts in the dashboard.

Startup behavior:

1. Resolve hub address from CLI flag, environment, config, then default `127.0.0.1:9180`.
2. Resolve the Serf state dir from CLI flag, environment, config, then default.
3. Probe hub health.
4. If no local hub is running, start one with the resolved state dir and current environment.
5. If a hub is already running, connect to it. The TUI must not assume new environment variables or state-dir flags apply to an existing hub process.
6. Fetch auth status and model summary before rendering spawn/model actions as ready.
7. Show a useful startup error screen if hub startup/connect fails.

The startup screen must distinguish:

- Cannot find `serf-hub` binary.
- Hub failed to bind.
- Hub is running but unhealthy.
- Hub exists but is an older incompatible API version.
- Existing hub was started with a different state dir or stale provider/auth environment.
- Hub is remote, so auto-start is not attempted.

CLI startup contract:

- `--hub-addr`: connect to an explicit hub address.
- `--hub-bin`: explicit local `serf-hub` binary for auto-start.
- `--no-auto-start-hub`: fail clearly instead of starting a local hub.
- `--state-dir`: resolved Serf state dir used for auth, history, preferences, hub auto-start, and hub-spawned daemons.
- `--log-file`: write TUI diagnostics without polluting the terminal UI.
- `--debug`: enable verbose TUI/hub diagnostics in the log file and optional debug overlay.

Precedence must be tested: CLI flags, environment, config, defaults.

### Dashboard

The default dashboard is live-only.

Layout on normal terminal widths:

```text
serf

live sessions                              status

terminal-tetris                            2 live
  > idle      fix scoring bug              openai/gpt-5.2      4m
    working   add pause menu               anthropic/claude... 1m

serf                                      1 live
    waiting   tui model picker             openai/gpt-5.2      12m

keys: enter open  p project  n new  / palette  r refresh  ? help  q quit
```

Behavior:

- The dashboard is a navigation/control surface, not a composer surface.
- Shows only live sessions at root.
- Groups by project.
- Sorts projects by most important live child, then recent activity.
- Sorts sessions by attention: waiting, errored, processing, idle.
- `enter` opens selected session.
- `p` opens project drilldown for selected project.
- `n` opens spawn.
- `/` opens the combined command/search palette.
- `r` refreshes.
- `?` opens help overlay.
- `q` quits.

Narrow layout:

- One column.
- Same row ordering.
- Truncates project/session/model with clear ellipses.
- Keeps key hints short.

Wide layout:

- Left `SessionList` plus right `DetailsDrawer`.
- Details drawer shows selected session summary, last assistant/user snippets,
  current task list summary, available actions, disabled reasons, and structured
  diagnostics.

### Project Drilldown

Project view is the bridge from live-only dashboard to history.

```text
serf / terminal-tetris

live
  > idle      fix scoring bug              4m
    working   add pause menu               1m

recent
    ended     initial terminal renderer    yesterday
    ended     collision refactor           May 9

keys: enter open  b back  n new here  / palette  r refresh  ctrl+o dashboard
```

Behavior:

- The project view is a navigation/control surface, not a composer surface.
- Shows live sessions first.
- Shows recent ended sessions for that project.
- Keeps project context for spawn defaults.
- `b` returns to dashboard.
- `ctrl+o` always returns to dashboard.
- `/project` from a session opens this view for that session's project.
- `/` opens the combined command/search palette scoped to this project.
- Wide layout uses `DetailsDrawer` for the selected session or diagnostic row.

### Session Workspace

The session workspace is the core "Codex-grade" surface.

```text
terminal-tetris / fix scoring bug
idle  openai/gpt-5.2  /Users/jesse/terminal-tetris  18 turns  ctx 42%

you
  The score increments twice when a line clears.

assistant
  I will trace the scoring path and add a regression test first.

  tool  read_file  src/game.ts  182 lines
  tool  rg "score" src  7 matches
  tool  apply_patch  src/game.test.ts  +24

input
> add a test for back-to-back line clears too

keys: enter send  alt+enter newline  esc browse  ctrl+c interrupt  ctrl+o dashboard  /help
```

Behavior:

- Header shows session title, state, model, project dir, turns, and context pressure when available.
- Transcript prioritizes user and assistant text.
- Tool calls are visually secondary but accessible.
- The bottom `Composer` region remains visible on session/workflow surfaces.
  It can be in send, steer, fork, read-only, or unavailable mode.
- If send is unsupported, the composer shows the exact read-only reason and
  preserves any draft. It must not disappear behind pickers or overlays.
- Live stream updates the transcript without duplicating replayed events.
- Errors appear inline as system messages and in the status line until cleared by next successful action.

### Browse And Fork

`esc` in session enters browse mode. It does not leave the session.

Browse mode:

- Composer focus moves to browse selection; the composer/footer region changes
  to browse key hints instead of accepting prompt text.
- The selected transcript item is highlighted.
- `j/k` or arrows move selection by message/tool.
- Page keys scroll.
- `tab` or `enter` expands/collapses selected tool call.
- `f` starts fork flow when a user message is selected.
- `esc`, `i`, or `q` returns to compose.

Fork flow:

1. Select a prior user message.
2. Press `f`.
3. The composer enters fork mode and is prefilled with that message.
4. User edits it.
5. `enter` confirms fork; `esc` cancels.
6. TUI calls hub fork API with turn index, edited message, and optional label.
7. New session opens when hub returns a new ref.

No transcript mutation happens in the TUI.

### Spawn

Spawn is reachable from dashboard, project, and command palette.

```text
serf / new session

Task
> fix the tetris line-clear scoring bug

Model       openai/gpt-5.2
Project     terminal-tetris
Directory   /Users/jesse/terminal-tetris
Agent       default

keys: enter spawn  tab next field  shift+tab previous  esc cancel  ctrl+o dashboard
```

Behavior:

- Defaults project/dir from current dashboard selection or current project view.
- Task may be empty if hub supports dormant sessions.
- Spawn uses `FormField` focus. Text fields own printable input; select/path
  fields open pickers or editors when focused.
- Serf harness models are provider-qualified.
- Codex harness models are whatever the selected Codex harness/source reports;
  do not overload `model` to mean harness/source.
- The model field opens a `PickerPanel` when focused and activated.
- Model picker uses live hub discovery or harness-specific discovery.
- Live OpenRouter discovery includes only models Serf can use with tools.
- If no models are available, the form says exactly why: no configured provider, provider listing failed, auth missing/expired, or all live models were filtered out.
- If OpenAI models are selected but neither `OPENAI_API_KEY` nor stored Serf OpenAI OAuth is usable, the form shows `OpenAI login required` and offers `/login openai` instead of timing out on spawn.
- Spawn requests include the resolved state-dir/profile identity so the hub can start daemons that see the same Serf-owned auth record as the TUI.
- `enter` spawns.
- Success opens the new session.
- Failure stays in the form and shows the error.

### Model Picker

The model picker should be useful for real work, not just a raw API dump.

Sources:

- Hub `/api/models`.
- Each entry is `provider/model`.
- The hub enriches with catalog metadata when available.
- The hub includes provider auth readiness and auth error metadata when a provider cannot list or use models.

Filtering:

- Non-chat models are excluded.
- OpenRouter models require known tool support.
- Other providers may include unknown metadata if the provider API itself is already scoped to usable chat models.
- Configured models are used as a fallback when live discovery returns none.
- OpenAI configured models remain visible when auth is missing, but disabled with a login-required reason.

Presentation:

```text
Select model
> gpt-5.2

openai
  openai/gpt-5.2        tools  reasoning  272k
  openai/gpt-5.2-codex  tools  reasoning  272k

anthropic
  anthropic/claude-opus-4-7  tools  reasoning

keys: type filter  enter choose  esc cancel
```

### OpenAI Auth And Account State

OpenAI OAuth was already implemented after the `serf-hub` branch point and landed on `main`. The hub TUI work must integrate and adapt that main-line implementation, not redesign a weaker replacement.

Branching fact:

- `serf-hub` branched at `d3114c9` on 2026-05-07.
- Core OpenAI auth landed later on `main` in `c72f4b1`, `62ade8b`, `84c92a8`, `f8a17f4`, and `48933a9` on 2026-05-08.
- Therefore the missing auth in `serf-hub` is a stale branch-base integration gap, not a later deletion.

Hard requirements:

- Serf owns the OpenAI login. Do not reuse Codex auth state, `~/.codex/auth.json`, browser cookies, or any other product's credential cache.
- The canonical CLI remains `serf openai login`, `serf openai logout`, and `serf openai status`.
- The TUI exposes the same capability through slash/palette commands: `/auth`, `/login openai`, `/logout openai`, and `/auth openai`.
- `OPENAI_API_KEY` takes precedence over stored Serf OAuth and is reported as the active source.
- Stored OAuth lives under the resolved Serf state dir at `<state-dir>/auth/openai.json` with owner-only permissions and atomic writes.
- Login uses browser PKCE with a localhost callback, always prints the authorize URL, and supports pasted final redirect URL fallback for remote sessions.
- The OpenAI adapter resolves credentials through the shared auth service: env key first, then stored OAuth, then refresh if needed.
- Hub-spawned daemons receive the same state-dir/profile context as the TUI, so OAuth works for sessions spawned from the dashboard.
- Logout deletes only Serf-owned OpenAI auth state and never mutates env credentials.

TUI status surfaces:

- Dashboard status area shows provider readiness summary, for example `openai: env`, `openai: signed in`, `openai: login required`, or `openai: refresh failed`.
- Spawn form shows provider-specific auth reasons before submit.
- Model picker keeps disabled OpenAI configured models visible when auth is missing, with `login required` instead of hiding every option.
- Session workspace status line reports auth failures from send/model/compact actions with a direct next step.

Hub auth contract:

- The TUI never reads auth files directly.
- The hub exposes typed auth status/login/logout endpoints through `internal/hubapi.Client`.
- Login begin returns the authorize URL, callback state, and whether a local callback is active.
- Login completion supports both callback-completed and manual pasteback completion.
- Auth status includes source, signed-in state, email/account metadata when known, expiry/refresh status, and a re-login reason when unusable.

### Input, Busy State, And Steering

The old TUI had important input behavior that must survive the hub rebuild:

- `enter` sends, `alt+enter` and `ctrl+j` insert newlines.
- Up/down recall persisted input history when the composer is empty.
- Failed sends preserve the draft exactly.
- If a live session rejects `/input` because it is busy but supports steering, the TUI offers steering instead of dropping input.
- `/steer` sends guidance to a busy processing session through the hub `Steer` action.
- When steering is unavailable, the composer explains why and keeps the draft.
- Input grows to a bounded max height, then scrolls internally rather than pushing the transcript off screen.

### Status, Details, And Subagents

`/status`, `/details`, `/tasks`, and `/agents` must not be placeholders.

Status/details parity:

- `/status` opens a concise overlay with session state, model, project dir, turns, context pressure, current task summary, and recent errors.
- `/details` opens the full diagnostic view.
- Full details include tools, MCP servers, skills, plugins, hooks, main/subagent agents, auth source, hub ref, daemon address when local, transcript replay/live URLs, and capabilities.
- The details view is backed by a hub `Details` endpoint, not direct daemon/filesystem reads from the TUI.

Subagent transcript behavior:

- `/agents` opens a picker for main transcript plus subagent transcripts.
- The hub owns subagent discovery and returns stable transcript identities, labels, status, and replay/follow URLs.
- Selecting a subagent switches the session surface to that transcript while preserving the session header and dashboard return behavior.
- Replay/live dedupe rules are identical for main and subagent transcripts.
- If a subagent transcript is unavailable, the picker shows the exact reason and keeps the user in the parent session.

### Slash Commands

Slash commands are first-class and generated from one registry.

Required commands:

- `/help`: show generated command and key help.
- `/auth`: show provider auth summary.
- `/auth openai`: show detailed OpenAI auth status.
- `/login openai`: start Serf-owned OpenAI login.
- `/logout openai`: remove Serf-owned OpenAI auth state.
- `/dashboard`: return to live dashboard.
- `/project`: open current session's project.
- `/projects`: open project picker.
- `/new`: open spawn form.
- `/search`: open command/search palette.
- `/steer`: send guidance to a busy session when supported.
- `/compact`: compact current live session.
- `/status`: show session details/status.
- `/details`: alias for `/status`.
- `/tasks`: show current task list.
- `/agents`: show main/subagent transcript picker when available.
- `/model`: open model picker.
- `/model provider/model`: switch model directly.
- `/clear`: start a new session from current one through hub clear.
- `/interrupt`: interrupt current live processing session.
- `/fork`: start fork flow from selected browse item or explain how to select one.
- `/theme`: open theme picker.
- `/quit`: quit with confirmation if a session is processing.

Rules:

- Help text is generated from registry entries, filtered by current mode and capabilities.
- Unknown slash commands produce a clear "unknown command" message with `/help` hint.
- Disabled commands explain the missing capability.
- Commands available through slash should also be available through the palette unless intentionally hidden.

### Command Palette

The palette is the keyboard command surface, not only search.

Open with `/` at dashboard/project, and `/search` or a future keybinding from session.

Modes:

- Command actions: new session, dashboard, project, compact, model, tasks, theme.
- Session search: live and recent sessions.
- In-session search: current transcript text.

The palette uses the same command registry as slash commands.

### Sample Corpus

The design system must be built against a small fixture and sample corpus before
the widgets are integrated into the live app. This is not a separate TUI
storybook app; it is a set of static inputs and golden terminal renders that
drive the real widgets.

Required fixtures:

- Dashboard tree: multiple projects, live sessions, recent sessions, source
  labels, attention states, and diagnostics.
- Project history: live first, recent ended sessions, empty project, and
  selected-row details.
- Session detail: Serf source, Codex source, ended session, busy session,
  capabilities, tasks, details, and unavailable actions.
- Transcript events: replay-only, live streaming, markdown deltas, tool
  call/result grouping, system notices, duplicate replay/live events, and
  missing turn identity.
- Spawn options: Serf harness with provider models, Codex harness with
  harness-reported models, no models, auth-required models, and launch failure.
- Auth states: env key, signed-out OAuth, signed-in OAuth, expired refreshable,
  refresh failed, and remote pasteback login.
- Picker states: loading, populated, filtered empty, disabled rows, and fetch
  error.

Required sample renders:

- Narrow, normal, and wide dashboard.
- Narrow, normal, and wide project view.
- Session idle, streaming, busy-with-steer, busy-read-only, browse, and fork.
- Spawn with Serf harness, Codex harness, auth-required model, and launch error.
- Model, theme, auth, agents, help, and command palette overlays.
- Structured diagnostics and action-unavailable notices.

Required interaction samples:

- Typing `m` and `h` enters the focused prompt/form field.
- Picker owns printable filter input and arrow/enter/escape handling.
- Composer draft survives opening and closing model/theme/help/details overlays.
- Busy send keeps draft and switches to steer when supported.
- Unsupported Codex actions are absent or visibly disabled with a reason.

### Theme And Visual Design

Use Bubble Tea with Lip Gloss styles. The goal is calm, dense, and readable.

Design language:

- Build from the approved widgets: `AppShell`, `TopBar`, `ActionBar`,
  `SessionList`, `CommandPalette`, `PickerPanel`, `Composer`, `FormField`,
  `TranscriptView`, `ToolGroup`, `DetailsDrawer`, and `Notice`.
- Avoid heavy boxes around every area.
- Use section labels and spacing before borders.
- Use color sparingly for state and focus.
- Prefer dim annotations for tools and metadata.
- Keep the transcript reading tier high contrast.
- Use consistent status colors in every mode.

Themes:

- `system`, `dark`, `light`.
- Theme picker works in hub mode.
- Theme choices persist using the existing preference path if one exists; otherwise use a small TUI config file under Serf config.

Status colors:

- waiting/error: high-attention red/pink.
- processing: blue.
- idle: green or neutral if stale.
- ended: gray.
- selected row: reversed or accented but not loud.

### Error Handling

Errors must be specific and actionable.

Examples:

- Model load: "Hub returned no models. Providers configured: none. Add [[providers]] to ~/.serf/hub.toml or start hub with provider API keys."
- Existing hub env mismatch: "Connected to existing hub pid 1234. It was not started with this TUI's environment."
- Spawn timeout: "Spawn started but no rendezvous file appeared within 30s. See hub log path."
- SSE 404: "Session stream is unavailable. The session may be closed; showing replay only."
- Action unavailable: "Compact is not available for ended sessions."

No action should fail silently.

## Architecture

### Package Structure

Keep package `main` for now, but split files by responsibility.

```text
cmd/serf-tui/
  main.go                    startup only
  hub_start.go               hub auto-start and health
  openai_auth.go             TUI auth commands/status/login orchestration
  app_model.go               top-level Bubble Tea app shell
  app_modes.go               mode enum and navigation helpers
  tui_widgets.go             shared widget interfaces and focus contract
  tui_samples.go             static sample fixtures for widget tests
  tui_samples_test.go        golden sample renders and focus/key samples
  command_registry.go        slash, palette, help, key hint registry
  command_registry_test.go
  command_palette.go         command/search palette widget
  picker_panel.go            shared picker widget
  composer.go                session/fork/steer composer widget
  spawn_form.go              focused spawn form widget
  transcript_view.go         reducer-backed transcript renderer
  details_drawer.go          selected-row/session diagnostic drawer
  notices.go                 structured diagnostic/action-unavailable notices
  dashboard_model.go         live dashboard state/update/view
  dashboard_view.go          dashboard rendering
  project_model.go           project drilldown state/update/view
  session_surface.go         reusable session state/update/view
  session_reducer.go         transcript replay/live reducer
  session_commands.go        command executors for session actions
  spawn_model.go             spawn form state/update/view
  spawn_model_test.go
  model_picker.go            existing picker, adjusted for provider/model
  palette_model.go           command/search palette
  theme_model.go             theme picker integration
  styles.go                  Lip Gloss theme tokens
  tmux_e2e_test.go           full terminal coverage

cmd/serf/
  openai_login.go            CLI OpenAI login
  openai_logout.go           CLI OpenAI logout
  openai_status.go           CLI OpenAI status

internal/auth/openai/
  config.go                  OAuth endpoints/client config
  pkce.go                    verifier/challenge/state generation
  server.go                  localhost callback listener
  manual.go                  pasted redirect URL parsing
  tokens.go                  code/refresh exchange
  storage.go                 state-dir auth record
  claims.go                  account metadata extraction
  service.go                 login/logout/status/runtime token service
```

Do not create a separate package yet. The first goal is clear file boundaries inside the existing binary.

### Top-Level App Model

`appModel` owns:

- Hub client.
- Hub base URL.
- Resolved Serf state dir.
- Current mode.
- Navigation stack or return target.
- Dashboard state.
- Project state.
- Session surface.
- Spawn form.
- Palette.
- Theme.
- Last error/status.

It routes Bubble Tea messages to the active component and handles global keys:

- `ctrl+o`: dashboard.
- `ctrl+c`: mode-aware interrupt/quit.
- Window size: propagate dimensions.
- Hub tree/session/model/task/auth/detail/stream messages: route to relevant component.

### Command Registry

Command registry entry:

```go
type commandScope string

const (
	commandScopeGlobal    commandScope = "global"
	commandScopeDashboard commandScope = "dashboard"
	commandScopeProject   commandScope = "project"
	commandScopeSession   commandScope = "session"
	commandScopeSpawn     commandScope = "spawn"
)

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
```

One registry powers:

- Slash command parsing.
- `/help`.
- Command palette.
- Footer key hints.
- Tests that assert advertised commands are implemented.

### Session Surface

`sessionSurface` replaces the ad hoc embedded old model use.

Responsibilities:

- Transcript state.
- Input state.
- Scrollback and browse selection.
- Tool expansion.
- Fork draft.
- Task overlay.
- Details overlay.
- Model picker overlay for `/model`.
- Theme picker overlay for `/theme`.
- SSE lifecycle state.

It should reuse existing rendering and data structures where practical, but it should not call the old single-session network helpers. All actions go through hub commands.

### Transcript Data Flow

Opening a session:

1. TUI calls hub `GET /api/sessions/{ref}`.
2. Hub returns metadata, capabilities, replay URL, and live stream URL when applicable.
3. TUI loads replay transcript.
4. TUI starts live stream if live URL exists.
5. Reducer dedupes replay/live events by stable event identity.
6. Session surface renders the combined transcript.

Stable event identity is required before claiming transcript correctness. If the current hub event contract is insufficient, the implementation plan must add the missing metadata before relying on it.

### Hub API Contract

The TUI uses typed `internal/hubapi.Client` methods only.

Needed client methods:

- `Health`
- `Tree`
- `Project`
- `Session`
- `Models`
- `Spawn`
- `Send`
- `Tasks`
- `Interrupt`
- `Compact`
- `Clear`
- `Fork`
- `SetModel`
- `Steer`
- `Details`
- `SubagentTranscripts`
- `AuthStatus`
- `AuthLoginBegin`
- `AuthLoginComplete`
- `AuthLogout`
- `ReplayTranscript`
- `FollowTranscript`

If an endpoint is missing, add it to the hub first and cover it with hub tests before wiring TUI behavior.

### Testing Strategy

Testing must make regressions hard.

Unit tests:

- Command registry completeness.
- Help generation.
- Mode-specific visible/enabled commands.
- Dashboard row grouping and sorting.
- Project drilldown ordering.
- Spawn form state transitions.
- Model filtering and default selection.
- Session reducer replay/live dedupe.
- Browse/fork state transitions.

Hub API tests:

- Auth status reports env, signed-out, signed-in, expired, and refresh-failed states.
- Auth login begin returns authorize URL and callback metadata without leaking secrets.
- Auth login complete accepts manual pasteback and rejects mismatched state.
- Auth logout deletes only Serf OpenAI auth state.
- `/api/models` filters OpenRouter live discovery to tool-capable models.
- `/api/models` falls back to configured models if live discovery returns none.
- `/api/models` returns auth readiness/disabled reasons for OpenAI when auth is missing.
- Spawn accepts provider-qualified model.
- Spawn propagates state-dir/auth context to hub-started daemons.
- Resume returns replay plus live capability.
- Details returns tools, MCP servers, skills, plugins, hooks, agents, subagents, capabilities, auth, and runtime metadata.
- Subagent transcript listing returns stable replay/follow sources.
- Actions return capability-appropriate status.

Sample/golden tests:

- Each approved widget has fixture-backed render samples for its normal, empty,
  loading, disabled, and error states where applicable.
- Golden terminal renders cover narrow, normal, and wide widths for dashboard,
  project, session, spawn, and overlays.
- Key-ownership samples prove focused text inputs receive printable keys before
  global shortcuts.
- Draft-preservation samples prove overlays do not drop composer or spawn form
  input.
- Sample fixtures are reused by tmux E2E tests where practical so design
  examples and product behavior stay aligned.

Tmux E2E tests:

- Start TUI with each startup flag and verify precedence/behavior.
- Start TUI against fake hub.
- Dashboard shows live sessions only.
- Project drilldown shows live plus recent ended.
- Auth status appears on dashboard and spawn form.
- `/login openai` drives a fake PKCE/pasteback flow.
- `/auth openai` and `/logout openai` update visible auth state.
- Open live session and see replay.
- Type `/help`; every listed command is executable or explicitly disabled.
- `/model` opens picker.
- Model picker supports grouping, filtering, active model, disabled auth-required options, and direct `/model provider/model`.
- Spawn form loads models and submits.
- Spawn form edits task, directory, agent, reasoning effort, and provider-qualified model.
- Spawn success opens new session.
- Ended session opens with history.
- Sending to ended session resumes and sends.
- Busy send preserves input and offers `/steer` when supported.
- Input history persists and multiline composition works.
- `esc` browse, select user turn, fork.
- `/status`, `/details`, `/tasks`, and `/agents` render real hub-backed data.
- Subagent transcript picker can open replay and live streams.
- Theme picker changes and persists `system`, `dark`, and `light`.
- `ctrl+o` returns to dashboard.

Parity checklist:

- Every old TUI user-visible flag, slash command, keybinding, input behavior, picker behavior, status/detail section, and transcript event type is mapped to a spec requirement and at least one unit or tmux E2E test.
- If a behavior is intentionally removed, the spec must name it as a non-goal with rationale before code is deleted.

## Rollout

Implement in small commits:

1. Tests that capture current gaps.
2. Command registry.
3. App shell and navigation cleanup.
4. Dashboard/project polish.
5. Session surface extraction.
6. Spawn/model polish.
7. Transcript replay/live correctness.
8. Full tmux E2E suite.
9. Delete obsolete embedded/direct paths and stale docs/help only after parity is proven.

## Acceptance Criteria

- `serf-tui` starts to a polished live dashboard.
- Dashboard root shows only live sessions grouped by project.
- Project drilldown shows project history.
- Session view has working slash commands, generated help, scrollback, tool expansion, fork, tasks, status, model switch, compact, clear, interrupt, and dashboard return.
- Session input supports persisted history, multiline composition, draft preservation on failure, and busy-session steering.
- `/status`, `/details`, `/tasks`, and `/agents` are backed by hub data and render parity-level detail from the old TUI.
- Spawn loads live models, filters unusable OpenRouter models, submits, and opens the new session.
- Spawn supports task, directory, agent, reasoning effort, provider-qualified model, and auth-aware disabled states.
- Serf-owned OpenAI OAuth works through CLI, hub, and TUI without Codex credential reuse.
- Hub-spawned daemons can use stored Serf OpenAI OAuth from the resolved state dir.
- Resume from ended session loads transcript history and sends through hub.
- No command advertised in `/help` is missing.
- No action fails silently.
- No old TUI user-visible behavior is dropped without an explicit spec non-goal and test adjustment.
- Approved widgets have fixture-backed render samples and focus/key tests before
  they are integrated into the live app.
- Tmux E2E tests cover the full flows above.
- `go test ./cmd/serf-tui ./cmd/serf-hub ./cmd/serf ./internal/hubapi ./internal/auth/openai ./llm/providers/openai ./llm` passes.
- `go test ./...` passes before merge.
