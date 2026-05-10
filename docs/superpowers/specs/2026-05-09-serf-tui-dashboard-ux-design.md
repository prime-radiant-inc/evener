# Serf TUI Live Dashboard UX - Design Spec

Date: 2026-05-09
Status: Draft
Linear: PRI-1542

## Summary

`serf-tui` should open as a live operations console, not as a history browser. The home dashboard shows only live sessions, grouped by project. From there, a user can drill into a project to see that project's live sessions plus recent ended sessions, or open any session into a chat-first workspace.

The session workspace is not a modal child of the dashboard. It is a peer workspace with its own focus stack. `esc` belongs to session focus: it moves from composing into transcript browse/fork mode and back again. Returning to the dashboard is explicit through a global overview action (`ctrl+o`) or `/dashboard`.

This spec is the authoritative UX design for the TUI dashboard, project drill-down, session navigation, and transcript browse/fork behavior. It supersedes the earlier generic dashboard/drill-in wording in the universal hub client design where the two conflict.

## Goals

- Start on a dashboard that shows live work only.
- Make projects the primary grouping and drill-down path.
- Keep history out of the home dashboard while still making project history reachable.
- Let users open a session and stay in a chat-first flow without accidental dashboard exits.
- Make `esc` useful for transcript review, selecting previous turns, and forking.
- Provide a reliable way back to the dashboard from any view.
- Keep the TUI aligned with the hub API and remote-ready session refs.

## Non-goals

- No root-level list of ended sessions.
- No root-level "all projects with history" tree.
- No modal-style `g d` navigation scheme.
- No direct daemon discovery or local state scanning in the TUI.
- No terminal-only feature that cannot be represented through the hub API.

## UX Principles

### Home is live-only

The home dashboard answers one question: "What is running right now?" It should not show ended sessions, past-only projects, or historical fork trees. Those belong under project drill-down, search, or explicit project switching.

### Projects are containers, not filters

A project row is a navigable place. Selecting a project opens a project workspace with live sessions first and recent sessions below. This avoids turning the root into a large mixed tree while keeping history one deliberate step away.

### Session view is a workspace

Opening a session is not entering a modal. The session view has a breadcrumb and global overview actions, but its local keyboard model optimizes for driving and reviewing that session. In particular, `esc` never means "leave this session for the dashboard."

### Focus, not modes

The TUI has view targets and focus layers:

- View target: dashboard, project, or session.
- Session focus: compose, transcript browse, or overlay.

This makes behavior predictable. `ctrl+o` changes the view target to dashboard. `esc` changes focus inside the current session or dismisses a local overlay.

## Information Architecture

### Dashboard

The dashboard shows live sessions grouped by project.

Example:

```text
serf live                                      local hub · 7 live · 2 need input

> ● serf                                      4 live · 1 awaiting · /Users/jesse/.../serf
    ● awaiting   finish TUI dashboard UX       gpt-5.2   3m
    ● working    run hub smoke tests           gpt-5.2   11m
    ● idle       inspect resume behavior       gpt-5.2   41m
    ● idle       web hub css pass              gpt-5.2   2h

  ● brainstorm                                2 live · 1 working · /Users/jesse/.../brainstorm
    ● working    mobile polish follow-up       gpt-5.2   8m
    ● idle       ticket triage                 gpt-5.2   1h

  ● ghost-pepper                              1 live · awaiting
    ● awaiting   investigate signing failure   gpt-5.2   5m

↑/↓ select  enter open  p project  s spawn  / filter  ctrl+o dashboard  q quit
```

Rows:

- Project header rows are selectable.
- Session rows are selectable.
- Past-only projects do not appear.
- Ended sessions do not appear.
- Subagents can appear under their live parent only when they are live or need attention; ended child history stays in project view.

Project header content:

- Highest-attention status dot across live children.
- Project display name.
- Live count.
- Highest-attention summary.
- Working directory when width allows.

Session row content:

- Status dot.
- Attention state.
- Title.
- Model.
- Age since last update.
- Host label when remote hosts exist.

Sorting:

1. Projects with awaiting sessions.
2. Projects with processing sessions.
3. Projects with warning sessions.
4. Projects with idle sessions.
5. Within the same status rank, most recently updated project first.
6. Sessions inside a project use the same attention-rank then recency sort.

### Project Drill-Down

Opening a project shows project-scoped work. It is the first place where ended sessions appear.

Example:

```text
serf / project / serf                         4 live · 18 recent · local

Live now
> ● awaiting   finish TUI dashboard UX         gpt-5.2   3m
  ● working    run hub smoke tests             gpt-5.2   11m
  ● idle       inspect resume behavior         gpt-5.2   41m
  ● idle       web hub css pass                gpt-5.2   2h

Recent in this project
  ○ ended      hub tree API ref cleanup         gpt-5.2   yesterday
  ⎇ ended      original before fork             gpt-5.2   Fri
  ○ ended      oauth state-dir propagation      gpt-5.2   Thu

↑/↓ select  enter open  r resume  s spawn here  / filter  esc dashboard
```

Sections:

- `Live now` appears first and is omitted only if there are no live sessions in the project.
- `Recent in this project` shows ended sessions, fork originals, and ended subagents by recency.
- A filter narrows both sections but preserves the section split.
- Project view can show a fork/subagent tree when the project has enough vertical space; otherwise it uses compact rows with glyphs.

Actions:

- `enter` opens the selected session.
- `r` on an ended session resumes it through the hub and opens the session.
- `s` starts a new session with the project working directory prefilled.
- `esc` or `backspace` returns to the dashboard because project view has no local transcript focus to preserve.
- `ctrl+o` also returns to the dashboard.

Past-only projects:

- They are not shown on the root dashboard.
- They are reachable through `/projects`, search, or opening an ended session from another route.
- If the dashboard has no live sessions, the empty state offers `s start session` and `/projects browse project history`; it does not render history inline.

### Session Workspace

Opening a session fills the TUI with a chat-first session workspace.

Example:

```text
serf / session / finish TUI dashboard UX       ● awaiting · serf · gpt-5.2 · 41 turns

assistant
  I found the current hub model returns to dashboard on esc. That conflicts with
  transcript browse and fork behavior.

tool · read_file · cmd/serf-tui/hub_model.go:180 · 24 lines

user
  a is closest. but... g d doesn't make sense. this thing isn't modal like that?

assistant
  The right model is a focus stack, not modal back-navigation...

──────────────────────────────────────────────────────────────────────────────
compose: type message                                                     ctx 42%
enter send  alt+enter newline  esc browse turns  ctrl+o dashboard  /help
```

The session header includes:

- Breadcrumb: `serf / session / <title>`.
- State dot and state label.
- Project name.
- Model/profile.
- Turn count and context pressure when available.
- Host label when remote hosts exist.

The body includes:

- Full transcript replay from persisted events.
- Live tail for running sessions.
- Existing TUI message/tool rendering, adapted to the hub stream.
- Compact annotations for tools, tasks, subagents, and fork lineage.

The footer is focus-specific:

- Compose focus shows send/newline/browse/dashboard commands.
- Browse focus shows scroll/select/fork/compose/dashboard commands.
- Overlay focus shows confirmation or cancel commands.

## Navigation Model

### Global navigation

`ctrl+o` is the global dashboard action. It should work from dashboard, project view, session compose focus, session browse focus, and local overlays. On the dashboard it is a no-op.

Slash command fallbacks:

- `/dashboard` goes to the dashboard.
- `/project` goes to the current session's project drill-down when the current session has project metadata.
- `/projects` opens a project switcher that includes live and past-only projects.

The design intentionally does not use `ctrl+p` for project navigation because terminal/readline environments commonly treat it as previous-history/up. The reliable project path is `/project`; the fast dashboard path is `ctrl+o`.

### Dashboard keys

```text
up/down, j/k      Move selection.
enter             Open selected session, or open selected project header.
p                 Open the selected row's project.
s                 Spawn a new session; if selection is in a project, prefill that project.
/                 Filter live sessions and live projects.
r                 Refresh hub tree immediately.
?                 Show dashboard help.
ctrl+o            Dashboard no-op.
q                 Quit.
```

### Project keys

```text
up/down, j/k      Move selection.
enter             Open selected session.
r                 Resume selected ended session.
s                 Spawn a new session in this project.
/                 Filter within this project.
esc, backspace    Return to dashboard.
ctrl+o            Return to dashboard.
?                 Show project help.
q                 Quit.
```

### Session compose keys

```text
enter             Send message when input is non-empty.
alt+enter         Insert newline.
ctrl+j            Insert newline fallback.
esc               Enter transcript browse focus.
pgup              Enter transcript browse focus and scroll one page up.
tab               Expand/collapse the latest completed tool call, preserving current behavior.
ctrl+c            Interrupt when allowed; second ctrl+c within the confirmation window quits.
ctrl+o            Go to dashboard.
/dashboard        Go to dashboard.
/project          Go to current project drill-down.
/tasks            Show tasks overlay.
/details          Show details overlay.
/clear            Clear through hub and navigate to returned new ref.
/model            Change model through hub when capability allows.
/quit             Quit.
```

### Session browse keys

```text
up/down, j/k      Move selected turn or tool annotation.
pgup/pgdn         Scroll transcript.
home/end          Jump to transcript start/end.
enter             Expand/collapse selected tool annotation or select a message.
f                 Fork at the selected user turn.
c                 Copy selected message or tool summary.
i                 Return to compose focus.
esc               Return to compose focus.
ctrl+o            Go to dashboard.
?                 Show browse help.
```

`q` should not quit the TUI from session browse. It may return to compose focus for users accustomed to the current scroll-mode behavior, but quitting a whole universal client from inside transcript browse is too easy to trigger accidentally.

## Transcript Browse And Fork

`esc` from compose enters transcript browse. Browse makes previous turns selectable and exposes fork as a first-class action.

Selection behavior:

- The initial browse selection is the most recent user turn.
- Up/down moves across user and assistant messages.
- Tool annotations are selectable below their parent message.
- The selected item is visually marked with a left rail and a short footer summary.

Fork flow:

1. User selects a prior user turn.
2. User presses `f`.
3. TUI opens a fork editor overlay with the selected message prefilled.
4. User edits the message and optionally labels the original branch.
5. Confirm calls the hub fork API with parent ref, selected turn, edited message, and label.
6. Hub creates the new session using the same fork semantics as the web UI.
7. TUI navigates to the returned child ref, refreshes the project/dashboard cache, and lands in compose focus.

If the selected row is not forkable, `f` shows a short inline reason:

- Assistant messages: "select a user turn to fork."
- Tool annotations: "select the parent user turn to fork."
- Sessions without persisted transcript: "fork requires persisted transcript."
- Hub or daemon capability disabled: "fork is not available for this session."

Overlay behavior:

- `esc` closes the fork overlay and returns to transcript browse.
- `ctrl+o` closes the overlay and goes to dashboard.
- Confirming with unchanged text is allowed only if the user supplied a label; otherwise it is a no-op with an explanation.

## Empty States

### Dashboard, no live sessions

```text
serf live                                      local hub · no live sessions

No live sessions are running.

s start a session
/projects browse project history
/ search sessions
q quit
```

The dashboard does not fill the empty state with past sessions. That would violate the live-only root rule.

### Project, no live sessions

The project view omits `Live now` and starts with `Recent in this project`. This is acceptable because the user intentionally entered project history.

### Project, no history

The project view shows `Live now` and a short footer line: `No ended sessions in this project yet.`

### Session, ended and read-only

The session opens with full transcript replay and a read-only footer:

```text
ended session · enter a message to resume if supported · /project · ctrl+o dashboard
```

If resume is unsupported, the footer states the exact reason from capabilities.

## Capability Rules

The UI must enable or disable actions from hub-reported capabilities:

- `can_send`: show compose input and allow sends.
- `can_resume`: allow sending or `r` resume for ended sessions.
- `can_interrupt`: allow interrupt only while processing.
- `can_fork`: enable browse `f`.
- `can_clear`: enable `/clear`.
- `can_set_model`: enable `/model`.
- `can_show_tasks`: enable `/tasks`.

Disabled actions should remain visible in help only when useful, with a reason. The user should not have to infer that a key failed.

## Data And API Implications

The TUI should use hub APIs only:

- Build the root dashboard from live entries returned by the hub tree API.
- Build project drill-down from project entries returned by the hub tree API or a project-scoped endpoint.
- Open sessions through hub detail and transcript APIs.
- Follow live sessions through hub SSE.
- Send, interrupt, resume, fork, clear, and model changes through hub actions.

Useful API shape:

```text
GET  /api/tree
GET  /api/projects
GET  /api/projects/{project_key}
GET  /api/sessions/{ref}
GET  /api/sessions/{ref}/transcript
GET  /api/sessions/{ref}/events?after=<event_id>
POST /api/sessions/{ref}/input
POST /api/sessions/{ref}/resume
POST /api/sessions/{ref}/fork
POST /api/sessions/{ref}/clear
POST /api/sessions/{ref}/interrupt
POST /api/sessions/{ref}/model
```

If the existing hub tree response is enough for v1, the TUI can derive dashboard and project rows client-side. The implementation should still keep that derivation behind a small adapter so a future project-scoped endpoint does not require rewriting view code.

## Rendering Requirements

- Root dashboard should fit useful information in an 80-column terminal.
- Project and session views should degrade cleanly below 100 columns.
- Status dots must not be the only state indicator; include text labels.
- Project names should be stable and human-readable, usually `filepath.Base(working_dir)`.
- Duplicate project basenames should disambiguate with a parent segment or host label.
- Host labels are hidden for local-only v1 unless needed to disambiguate.
- Footer help must update with focus. Stale help is a UX bug.

## Implementation Notes

Expected TUI model structure:

- `hubModeDashboard`
- `hubModeProject`
- `hubModeSession`
- Session focus enum: `compose`, `browse`, `overlay`
- Row adapters for dashboard rows and project rows.
- A session controller that owns transcript replay, live follow, input state, browse selection, and overlays.

Important current-code changes implied by this UX:

- Remove `esc` and `backspace` as session-to-dashboard shortcuts in `hubModel.updateKey`.
- Route `esc` to session focus handling before any global view navigation.
- Stop treating `q` as a global quit while inside session browse.
- Replace the flat `buildHubRows` dashboard with grouped live-only dashboard rows.
- Add project view state and row construction.
- Add explicit `ctrl+o` and slash-command navigation to dashboard/project.

## Test Plan

Unit tests:

- Dashboard rows include only live sessions.
- Dashboard rows group live sessions by project.
- Past-only projects do not appear on dashboard.
- Project view includes live sessions and recent ended sessions.
- Project sort uses attention rank, then recency.
- `enter` on a dashboard project header opens project view.
- `enter` on a dashboard session opens session view.
- `p` on any dashboard row opens that project.
- `esc` in project view returns to dashboard.
- `esc` in session compose enters browse focus.
- `esc` in session browse returns to compose focus.
- `ctrl+o` from session returns to dashboard.
- `/project` from session opens the current project.
- Browse `f` opens fork overlay only on forkable user turns.
- Fork success navigates to the returned child ref.
- Disabled capabilities produce visible reasons instead of silent no-ops.

Integration tests:

- Start `serf-tui` with a hub tree containing live and ended sessions; root renders live only.
- Open a live session, replay transcript, and tail a live event without duplicate messages.
- Open an ended project session and resume it through hub.
- Clear a session and verify the TUI navigates to the new returned ref.

Manual UX checks:

- 80x24 terminal with three projects and seven live sessions.
- No-live dashboard empty state.
- Project with many recent sessions.
- Session browse/fork flow using only keyboard.
- Accidental `esc` in session does not lose session context.
- `ctrl+o` works from compose, browse, details overlay, and fork overlay.

## Acceptance Criteria

- `serf-tui` starts on a live-only project-grouped dashboard.
- A user can drill from dashboard project header to project view.
- Project view shows live sessions first and recent project history second.
- Opening a session gives a chat-first workspace.
- `esc` in a session never returns to dashboard.
- `esc` in a session enables transcript browse and fork flow.
- A reliable dashboard return exists through `ctrl+o` and `/dashboard`.
- The implementation is hub-backed, not a second local discovery path.
