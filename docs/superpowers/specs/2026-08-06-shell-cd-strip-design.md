# Display-strip of redundant `cd <cwd> && ` shell prefixes

Date: 2026-08-06
Status: approved design, pre-implementation

## Problem

Models habitually prefix shell commands with `cd <session cwd> && ` even
though serf already runs every command in the session's working directory
(execenv sets the process dir; the prefix is a trained-in habit, not a
serf need). The prefix is pure noise in transcript displays: it repeats a
long absolute path on nearly every shell row.

## Rule (both surfaces, identical)

When a displayed shell command starts with the literal string
`cd ` + session working directory + ` && `, render the command with that
prefix removed.

- Literal match only: no quote handling, no path normalization, no
  trailing-slash tolerance, no `;`/`&` variants. `cd "/same/dir" && x`
  and `cd /same/dir; x` display unchanged.
- A `cd` to any other directory always displays — that one is
  informative.
- Display-only: the recorded tool arguments are untouched; copy/raw
  views see the original command.
- After a mid-session worktree switch, older commands cd'ing to the old
  cwd no longer match the current cwd and display in full. Accepted:
  the strip is conservative, worst case is the status quo.

## Implementation

- **Web** (`cmd/serf-hub/frontend`): pure helper
  `stripRedundantCd(command, cwd)` beside `shellTool.tsx`; applied at
  the row summary and the expanded `ShellCommandBlock`. The session
  pane already holds `Thread.cwd`; thread it to the shell tool
  renderer.
- **TUI** (`cmd/serf-tui`): `toolsummary.SummarizeTool` learns the
  session cwd (parameter; callers plumb it from thread state) and
  applies the same rule to both desc and detail.

## Testing

Table-driven tests on the helper in both languages: exact match strips;
non-matching dir, quoted path, trailing slash, `cd` mid-command,
`cd X ; cmd`, and empty cwd all leave the command unchanged. One
renderer-level assertion per surface that the stripped form is what
displays.

## Out of scope

Protocol changes; normalizing or resolving paths; stripping any other
boilerplate.
