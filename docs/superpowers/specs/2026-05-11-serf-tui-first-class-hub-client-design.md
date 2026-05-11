# Serf TUI First-Class Hub Client Design

Date: 2026-05-11
Status: Draft for implementation planning

## Summary

`serf-tui` should be a first-class universal terminal client for Serf, not a thin dashboard wrapped around fragments of the old single-session TUI. The app starts at a live-session dashboard grouped by project, drills into a polished session workspace, supports spawning and resuming through the hub, and preserves the interaction quality users expect from Codex: clear navigation, slash commands, scrollback, fork-from-turn, model picking, task/status views, and reliable end-to-end behavior.

The implementation foundation remains Bubble Tea. The repair is not a rewrite away from Bubble Tea; it is a Bubble Tea architecture cleanup:

- One app shell owns navigation, global key handling, and layout.
- One command registry owns slash commands, palette commands, help text, key hints, capability checks, and dispatch.
- One reusable session surface owns transcript rendering, input, scrollback, tool expansion, fork selection, task/status overlays, and live/replay reduction.
- The hub remains the only backend seam for discovery, spawn, resume, fork, model listing, task/status, REST actions, and SSE.

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

## User Experience

### Startup

`serf-tui` starts in the dashboard.

Startup behavior:

1. Resolve hub address from CLI flag, environment, config, then default `127.0.0.1:9180`.
2. Probe hub health.
3. If no local hub is running, start one.
4. If a hub is already running, connect to it. The TUI must not assume new environment variables apply to an existing hub process.
5. Show a useful startup error screen if hub startup/connect fails.

The startup screen must distinguish:

- Cannot find `serf-hub` binary.
- Hub failed to bind.
- Hub is running but unhealthy.
- Hub exists but is an older incompatible API version.
- Hub is remote, so auto-start is not attempted.

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

keys: enter open  p project  n new  / search  r refresh  ? help  q quit
```

Behavior:

- Shows only live sessions at root.
- Groups by project.
- Sorts projects by most important live child, then recent activity.
- Sorts sessions by attention: waiting, errored, processing, idle.
- `enter` opens selected session.
- `p` opens project drilldown for selected project.
- `n` opens spawn.
- `/` opens command/search palette.
- `r` refreshes.
- `?` opens help overlay.
- `q` quits.

Narrow layout:

- One column.
- Same row ordering.
- Truncates project/session/model with clear ellipses.
- Keeps key hints short.

Wide layout:

- Left list plus right preview.
- Preview shows selected session summary, last assistant/user snippets, current task list summary, and available actions.

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

keys: enter open  b back  n new here  / search  r refresh  ctrl+o dashboard
```

Behavior:

- Shows live sessions first.
- Shows recent ended sessions for that project.
- Keeps project context for spawn defaults.
- `b` returns to dashboard.
- `ctrl+o` always returns to dashboard.
- `/project` from a session opens this view for that session's project.

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
- Input appears only when send is supported. Otherwise a read-only reason appears.
- Live stream updates the transcript without duplicating replayed events.
- Errors appear inline as system messages and in the status line until cleared by next successful action.

### Browse And Fork

`esc` in session enters browse mode. It does not leave the session.

Browse mode:

- Input blurs.
- The selected transcript item is highlighted.
- `j/k` or arrows move selection by message/tool.
- Page keys scroll.
- `tab` or `enter` expands/collapses selected tool call.
- `f` starts fork flow when a user message is selected.
- `esc`, `i`, or `q` returns to compose.

Fork flow:

1. Select a prior user message.
2. Press `f`.
3. Input is prefilled with that message.
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

keys: enter spawn  tab next field  m model  d directory  a agent  esc cancel  ctrl+o dashboard
```

Behavior:

- Defaults project/dir from current dashboard selection or current project view.
- Task may be empty if hub supports dormant sessions.
- Model is provider-qualified.
- `m` opens model picker.
- Model picker uses live hub discovery.
- Live OpenRouter discovery includes only models Serf can use with tools.
- If no models are available, the form says exactly why: no configured provider, provider listing failed, or all live models were filtered out.
- `enter` spawns.
- Success opens the new session.
- Failure stays in the form and shows the error.

### Model Picker

The model picker should be useful for real work, not just a raw API dump.

Sources:

- Hub `/api/models`.
- Each entry is `provider/model`.
- The hub enriches with catalog metadata when available.

Filtering:

- Non-chat models are excluded.
- OpenRouter models require known tool support.
- Other providers may include unknown metadata if the provider API itself is already scoped to usable chat models.
- Configured models are used as a fallback when live discovery returns none.

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

### Slash Commands

Slash commands are first-class and generated from one registry.

Required commands:

- `/help`: show generated command and key help.
- `/dashboard`: return to live dashboard.
- `/project`: open current session's project.
- `/projects`: open project picker.
- `/new`: open spawn form.
- `/search`: open command/search palette.
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

### Theme And Visual Design

Use Bubble Tea with Lip Gloss styles. The goal is calm, dense, and readable.

Design language:

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
  app_model.go               top-level Bubble Tea app shell
  app_modes.go               mode enum and navigation helpers
  command_registry.go        slash, palette, help, key hint registry
  command_registry_test.go
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
```

Do not create a separate package yet. The first goal is clear file boundaries inside the existing binary.

### Top-Level App Model

`appModel` owns:

- Hub client.
- Hub base URL.
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
- Hub tree/session/model/task/stream messages: route to relevant component.

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

- `/api/models` filters OpenRouter live discovery to tool-capable models.
- `/api/models` falls back to configured models if live discovery returns none.
- Spawn accepts provider-qualified model.
- Resume returns replay plus live capability.
- Actions return capability-appropriate status.

Tmux E2E tests:

- Start TUI against fake hub.
- Dashboard shows live sessions only.
- Project drilldown shows live plus recent ended.
- Open live session and see replay.
- Type `/help`; every listed command is executable or explicitly disabled.
- `/model` opens picker.
- Spawn form loads models and submits.
- Spawn success opens new session.
- Ended session opens with history.
- Sending to ended session resumes and sends.
- `esc` browse, select user turn, fork.
- `ctrl+o` returns to dashboard.

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
- Spawn loads live models, filters unusable OpenRouter models, submits, and opens the new session.
- Resume from ended session loads transcript history and sends through hub.
- No command advertised in `/help` is missing.
- No action fails silently.
- Tmux E2E tests cover the full flows above.
- `go test ./cmd/serf-tui ./cmd/serf-hub ./internal/hubapi` passes.
- `go test ./...` passes before merge.
