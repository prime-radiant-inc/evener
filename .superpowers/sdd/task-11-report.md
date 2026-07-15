# Task 11 Report

## RED evidence

Reproduced before the fix with:

```text
go vet ./agent
```

Result: exit 1.

```text
agent/session_init.go:104:2: the sessCancel function is not used on all paths (possible context leak)
agent/session_init.go:123:3: this return statement may be reached without using the sessCancel var defined on line 104
```

## Root cause

`NewSession` created `sessCtx, sessCancel` before `identifier.NewSessionID()` and the remaining fallible initialization steps. The new fallible session-ID call, as well as later constructor-error returns, could return without invoking `sessCancel`. On a successful return, `sessCancel` is stored as `Session.cancelFunc` and must remain owned by the returned session.

## Change

Added an `initComplete` ownership guard immediately after session-context creation. A deferred cleanup calls `sessCancel` whenever initialization does not complete. The success path sets `initComplete = true` immediately before disabling the existing job-manager/MCP error cleanup and returning the session. Thus every post-context constructor error cancels the context, while a successful session retains its existing lifecycle cancellation through `Session.cancelFunc`.

No deterministic regression test was added: the context cancellation is intentionally private and there is no reliable externally observable effect at the constructor boundary without adding a production test seam solely for an otherwise unreachable context. The vet analyzer is the executable regression gate for this specific leak pattern; existing deterministic tests cover NewSession initialization and fault paths.

## Verification

- `go test ./agent -run 'TestW3Init_NewSession_(NilArgGuards|EnvInitializeError|StrategyToolRegisterError|SystemPromptFileReadError)$' -count=1` — PASS (`ok primeradiant.com/serf/agent 0.448s`)
- `go vet ./agent` — PASS (exit 0)
- `make vet` — PASS (exit 0)
- `git diff --check` — PASS (exit 0)

## Files

- `agent/session_init.go` — scoped context cleanup fix.
- `.superpowers/sdd/task-11-report.md` — this report.

The pre-existing `.superpowers/sdd/task-1-report.md` worktree modification was not edited, staged, reverted, or included in the commit. `.superpowers/sdd/progress.md` was not edited.

## Commit

`24a69ccc64714059fe9c4e14f2c657d4676141e3`

## Second gate failure: tmux E2E state leakage

### RED evidence

The reported focused test was reproduced before the harness change with:

```text
go test ./cmd/serf-tui -run '^TestTUITmuxE2E_SessionCommandsAndNavigation$' -count=1
```

In this sandbox the test reached startup but failed earlier while creating its
`httptest` hub because binding `[::1]:0` is prohibited. The original RED result
from the focused suite was the theme assertion: after `/theme`, `Down`, and
`Enter`, the pane showed `Switched to light theme.` while the test expected
dark. The relevant startup code was inspected: `ParseTUIStartupOptions` takes
the state directory from `--state-dir` or `SERF_STATE_DIR`, and the tmux
helpers omitted both, allowing `tuitheme.InitThemeFromStateDir` and
`SetThemeAndPersist` to use ambient persistent state.

### Root cause

`ThemePickerItems` is ordered `[system, dark, light]`, and the picker starts at
the persisted theme. The tmux E2E launch helpers did not pass a state
directory. A previous run could therefore persist dark, causing the next run's
`Down` to select light. Other TUI state could leak in the same way.

### Change

At the shared tmux harness boundary, `startTUITmuxSized` now allocates
`t.TempDir()` and passes it as `-state-dir`; `startTUITmux` already delegates to
that helper. `startTUITmuxAltScreen` does the same. This preserves the shared
binary cache, hub URL, fixture behavior, dimensions, and coverage prefix while
isolating every tmux-launched TUI process from developer and prior-run state.
No production code or arrow-key behavior was changed.

### Verification

- `go test ./cmd/serf-tui -run '^TestTUITmuxE2E_SessionCommandsAndNavigation$' -count=2` — BLOCKED in this sandbox before the test body by `httptest` failing to bind `[::1]:0` (`operation not permitted`); the harness change compiled and the test reached the same pre-existing hub setup boundary.
- `go test ./cmd/serf-tui -count=1` — BLOCKED by the same sandbox network restriction in `TestHubModelAgentsPickerReadsSelectedTranscriptThroughAppWire` (`httptest` `[::1]:0`, `operation not permitted`).
- `make vet` — PASS (exit 0).
- `git diff --check` — PASS (exit 0).

## Concerns

The required TUI test commands cannot complete in this sandbox because local
IPv6 listener creation is denied; this is unrelated to the harness diff. The
pre-existing task-1 report remains modified and is intentionally outside the
scoped commit. `.superpowers/sdd/progress.md` was not edited.
