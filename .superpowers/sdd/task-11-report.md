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

## Third gate failure: six hub lint issues

### RED evidence

`make lint` was run before the fixes and failed with exactly 6 issues: three
gofmt findings in `app_launch_test.go`,
`cov_launch_models_plugins_fuzz_test.go`, and `e2e_test.go`; QF1012 in
`cov_small_faults_pass5_fuzz_test.go:152`; SA9003 in
`web_api_archive.go:68`; and SA9003 in `web_test.go:6647`.

### Root cause

Three branch-introduced one-line `if` statements were not gofmt-formatted. The
pass5 fuzz helper used `WriteString(fmt.Sprintf(...))`, triggering QF1012. The
archive handler contained a dead empty project branch. The observer grant
workspace test had an empty failure body, so its intended assertion did not
report failures.

### Change

Formatted only the three touched test files, changed the pass5 helper to
`_, _ = fmt.Fprintf(&many, "x%d.png ", i)`, removed the dead archive branch,
and restored a `t.Fatalf` reporting `wd.ObserverRouteIDs` in the observer test.

### Verification

- `gofmt -w cmd/serf-hub/app_launch_test.go cmd/serf-hub/cov_launch_models_plugins_fuzz_test.go cmd/serf-hub/e2e_test.go cmd/serf-hub/cov_small_faults_pass5_fuzz_test.go cmd/serf-hub/web_api_archive.go cmd/serf-hub/web_test.go` — PASS.
- Focused hub tests for launch config, archive API, observer grant workspace, and `FuzzSmallFaultsPass5` — PASS (`ok primeradiant.com/serf/cmd/serf-hub 0.473s`; pass5 `0.387s`).
- `go test ./cmd/serf-hub -count=1` — BLOCKED by sandbox IPv6 listener permission in `TestHubRPCAuthStatusUsesUserScopedOpenAIAuth`: `httptest` bind `[::1]:0`, `operation not permitted`.
- `make lint` — the six reported issues are resolved; the command reaches unrelated pre-existing repository findings (20 issues: errcheck 1, gocritic 4, govet 2, ineffassign 1, nilerr 1, staticcheck 9, unused 2), so exits 2. It also emitted a non-fatal golangci cache permission warning.
- `make vet` — PASS (exit 0).
- `git diff --check` — PASS (exit 0).

No changes were made to `.superpowers/sdd/task-1-report.md` or
`.superpowers/sdd/progress.md`.

## Full remaining lint wave

### RED evidence

Started by reproducing `make lint` after commit `78b1ce30c`. It reported the
20 findings listed in the prior section. The wave was then run to completion;
each newly exposed mechanical finding was fixed and `make lint` was rerun until
it reached exit 0.

### Root causes and changes

- `agent/execenv/securepath_darwin.go`: replaced deprecated raw
  `unix.Syscall(SYS_FCNTL, ...)` with x/sys's supported `unix.FcntlInt` wrapper
  for Darwin `F_GETPATH`, preserving the canonical open-fd path query. The
  root fd close is now explicitly discarded in a deferred closure. x/sys
  inspection confirmed there is no F_GETPATH-specific wrapper; `FcntlInt` is
  the available Darwin fcntl wrapper and carries the pointer argument through
  the supported entry point.
- `agent/execenv/project_resolver.go` and its test: changed `reflect.Ptr` to
  `reflect.Pointer` and lowercased all six newly linted Git error strings.
- `agent/internal/installid/installation_id.go`: made the temporary-file
  persistence failure path return the existing winner reread after any write,
  sync, close, chmod, or rename failure, preserving empty-on-failure behavior
  and avoiding the ineffectual error assignment. Removed the unused contention
  seam field and simplified promoted filesystem selectors in its tests.
- `agent/session_worktree_resume.go`: introduced a boolean existence helper for
  the documented missing-worktree recovery path, preserving warning-and-fallback
  behavior without nilerr confusion.
- `agent/workspace_info.go`: changed `WriteString(fmt.Sprintf(...))` to direct
  `fmt.Fprintf`.
- `agent/execenv/gitpath.go`: removed the unused duplicate helper; the fuzz
  test now calls identifier's single implementation directly.
- Replaced string-to-string byte comparisons with `bytes.Equal` in doctor,
  schema, and transcript tests; simplified the installid path join; used direct
  `fmt.Fprint`; simplified promoted `fs.Fs` selectors; and removed the unused
  UUID helper.
- Added explicit fatal-path returns in newly surfaced SA5011 tests in hubcore
  and apptranscript. Lowercased identifier Git errors, used `reflect.Pointer`,
  simplified identifier test paths, removed the unused `gitInit` helper, and
  applied golangci's exact UUID composite-literal formatter rewrite.

### Verification

- `make lint` — PASS, `0 issues`, exit 0. The command also completed generation;
  the environment emitted only non-fatal golangci cache-permission warnings
  and the existing warning that gitleaks is not installed.
- `make vet` — PASS, exit 0.
- `git diff --check` — PASS, exit 0.
- `go test ./agent/internal/installid -count=1` — PASS.
- `go test ./agent/execenv -run '^(Test(Secure|Path|GitPath|ProjectResolver)|Test.*Canonical|Test.*Root)' -count=1` — PASS.
- `go test ./agent/execenv -count=1` — the touched tests ran, but the package
  was blocked by unrelated sandbox process restrictions in
  `TestStreamCommandSignalKillsWholeProcessGroup`: `/proc/.../cmdline` was
  absent and `/bin/ps` failed with `operation not permitted`.
- `go test ./agent -run '(Resume|Worktree)' -count=1` — PASS.
- `go test ./agent/internal/contextmgr -count=1` — PASS.
- `go test ./agent/schema -count=1` — PASS.
- `go test ./agent/doctor -count=1` — PASS.
- `go test ./internal/apptranscript -count=1` — PASS.
- `go test ./identifier -count=1` — PASS.
- `GOOS=darwin GOARCH=arm64 go test -c ./agent/execenv -o .task11-execenv-darwin.test` — PASS; the temporary binary was removed.

No suppressions were added. No changes were made to
`.superpowers/sdd/task-1-report.md` or `.superpowers/sdd/progress.md`.

## Fourth gate failure: SA5011 fatal-return control flow

### RED evidence

After commit `90d301a25`, `make lint` first reported the six requested SA5011
findings in `cmd/serf-hub/app_instances_test.go` at lines 85, 107, and 220
(each nil check followed by a pointer dereference). The checks called
`t.Fatal/Fatalf`, but this lint configuration does not infer that those calls
terminate the test function.

### Root cause and change

Added an explicit `return` immediately after each of those three fatal calls.
The same lint run then exposed four identical SA5011 patterns: three in
`internal/appprojector/appwire_projection_test.go` (lines 272, 1589, and 1757)
and one additional instance test in `cmd/serf-hub/app_instances_test.go:295`,
plus one observer check in `cmd/serf-hub/web_test.go:873`. Added immediate
returns to all five additional fatal paths as the same minimal test-only
control-flow clarification. Ran gofmt on the affected files.

### Verification

- `make lint` before changes — RED: 6 SA5011 findings as described above.
- Focused instance tests
  `go test ./cmd/serf-hub -run '^TestInstances_(Create_ListIncludesEntry|Edit_ChangesBaseURLAndAPIStyle)$' -count=1` — PASS (`0.498s`).
- Additional SA5011 regression tests
  `go test ./internal/appprojector -run '^(TestAppEventProjectorProjectsReasoningDelta|TestProjector_ForwardsProviderCause|TestProjector_AssistantTextResetDiscardsInProgressItem)$' -count=1` — PASS (`0.179s`).
- `go test ./cmd/serf-hub -count=1` — BLOCKED by the sandbox listener restriction in `TestHubRPCAuthStatusUsesUserScopedOpenAIAuth`: `httptest` could not bind `[::1]:0` (`operation not permitted`).
- `make vet` — PASS (exit 0).
- `git diff --check` — PASS (exit 0).
- `gofmt -w cmd/serf-hub/app_instances_test.go internal/appprojector/appwire_projection_test.go` — PASS.

### Further lint layer / NEEDS_CONTEXT

The post-fix `make lint` no longer reported SA5011, but exposed 20 findings in
unrelated files. They are recorded exactly here rather than broadening this
scoped wave without direction:

```text
agent/execenv/securepath_darwin.go:52:18: Error return value of `unix.Close` is not checked (errcheck)
agent/doctor/locate_test.go:233:5: stringXbytes: suggestion: !bytes.Equal(after, before) (gocritic)
agent/internal/installid/installation_id_test.go:77:37: filepathJoin: "/state" contains a path separator (gocritic)
agent/schema/cov_s5_snapshot_test.go:147:6: stringXbytes: suggestion: !bytes.Equal(got, want) (gocritic)
agent/transcript_lookup_test.go:352:5: stringXbytes: suggestion: !bytes.Equal(gotBytes, legacyBytes) (gocritic)
agent/execenv/project_resolver.go:33:67: inline: Constant reflect.Ptr should be inlined (govet)
agent/execenv/project_resolver_test.go:30:55: inline: Constant reflect.Ptr should be inlined (govet)
agent/internal/installid/installation_id.go:110:4: ineffectual assignment to err (ineffassign)
agent/session_worktree_resume.go:61:3: error is not nil (line 59) but it returns nil (nilerr)
agent/execenv/project_resolver.go:173:10: ST1005: error strings should not be capitalized (staticcheck)
agent/execenv/project_resolver.go:176:10: ST1005: error strings should not be capitalized (staticcheck)
agent/execenv/project_resolver.go:179:10: ST1005: error strings should not be capitalized (staticcheck)
agent/execenv/securepath_darwin.go:23:33: SA1019: unix.SYS_FCNTL is deprecated: Use libSystem wrappers instead of direct syscalls. (staticcheck)
agent/internal/contextmgr/task8_strategy_fuzz_test.go:294:5: QF1012: Use fmt.Fprint(...) instead of WriteString(fmt.Sprint(...)) (staticcheck)
agent/internal/installid/installation_id_test.go:252:20: QF1008: could remove embedded field "Fs" from selector (staticcheck)
agent/internal/installid/installation_id_test.go:266:20: QF1008: could remove embedded field "Fs" from selector (staticcheck)
agent/internal/installid/installation_id_test.go:284:20: QF1008: could remove embedded field "Fs" from selector (staticcheck)
agent/workspace_info.go:180:3: QF1012: Use fmt.Fprintf(...) instead of WriteString(fmt.Sprintf(...)) (staticcheck)
agent/execenv/gitpath.go:189:6: func gitEntryResolvesToCommon is unused (unused)
agent/internal/installid/installation_id_test.go:220:2: field renameCount is unused (unused)
```

`make lint` therefore still exits 2 on these 20 unrelated findings. The
required next lint wave needs parent direction/context before changing those
files. No changes were made to `.superpowers/sdd/task-1-report.md` or
`.superpowers/sdd/progress.md`.

## Fifth gate failure: stale tool-fluency transcript IDs

### RED evidence

Reproduced the clean-break failure with:

```text
go test ./tools/tool-fluency/cmd/serf-fluency -run '^TestAllTranscriptToolCountsIncludesChildSessions$' -count=1
```

Result: FAIL — `allTranscriptToolCounts: invalid session id "child_session"`.

### Root cause and change

The transcript reader now validates local transcript filename/header session
IDs as strict 22-character UUIDv7/base62 IDs. The regression fixture used
`root_session` and `child_session`, so the child transcript was rejected before
tool counts could be accumulated. Replaced them with distinct deterministic
valid IDs `02wMz5Txv1C3Hut0M8GCeB` and `02wMz5Txv2enqVTitaig6F`; the test still
asserts `read_file == 2`, preserving the cross-session accumulation contract.

Audited all local transcript/meta fixtures in this package. Migrated the
synchronized offline and coverage-program metadata IDs (`child`, `new`, `old`,
`parented`, and `root` parent linkage) to distinct valid IDs and updated the
root-session expectation. Deliberately invalid fixtures remain unchanged:
`invalid.meta.json`, malformed JSON, `unreadable.meta.json`, empty transcript
name, and `bad.transcript.jsonl` continue to exercise error paths. Generic
non-fixture labels were not indiscriminately rewritten.

### Verification

- Focused transcript test — PASS.
- `go test ./tools/tool-fluency/cmd/serf-fluency -count=1` — PASS.
- `MODULES=. scripts/run-module-tests.sh -short -count=1` — tool-fluency passed; the root-module runner was blocked by the sandbox listener restriction in `server/TestWSTransportRecordsFrames` (`httptest` bind `[::1]:0`, `operation not permitted`).
- `make lint` — PASS, 0 issues, exit 0.
- `make vet` — PASS, exit 0.
- `git diff --check` — PASS, exit 0.

No changes were made to `.superpowers/sdd/task-1-report.md` or
`.superpowers/sdd/progress.md`.

## Final full-branch verification

### Focused, static, and repository gates

The exact focused commands from Task 11 passed in dependency order:

- `go test ./identifier/... -count=1` — PASS (`0.775s`).
- `(cd agent && go test ./execenv ./internal/installid ./internal/jobstore ./internal/worktree . -count=1)` — PASS (`7.211s`, `1.004s`, `4.057s`, `3.775s`, and `47.705s`).
- `(cd llm && go test ./providers/google/... -count=1)` — PASS (`0.533s`).
- `go test ./cmdutil ./cmd/serf ./cmd/serf-hub/internal/launchconfig ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub ./cmd/serf-tui -count=1` — PASS (`1.128s`, `5.096s`, `0.985s`, `1.663s`, `48.832s`, and `3.798s`).

The exact static and repository gates also passed:

- `make vet` — PASS, exit 0.
- `make lint` — PASS, exit 0; all six lint phases reported `0 issues`, and generation completed. The environment reported only the existing non-fatal warning that `gitleaks` was not installed.
- `make test` — PASS: root `31.48s`, agent `62.38s`, llm `10.06s`, auth `2.33s`, envvars `1.01s`, invariant `0.81s`, identifier `2.31s`. The runner reported that `systemd-run` was unavailable and therefore ran uncapped.

### Race gates and Darwin toolchain diagnosis

The ambient `go1.26.1 darwin/arm64` toolchain could not provide valid full-agent race evidence. The exact agent race command twice left a fork child spinning before `exec`, with the child sampled in ThreadSanitizer's `TraceSwitchPartImpl` and the parent waiting in `syscall.forkExec`. The initially named worktree tests passed independently, passed together for 10 runs, and passed together for 100 runs. The complete agent race suite also passed when serialized with `-parallel=1` (`182.659s`), showing that concurrent fork/exec pressure was required.

This matches Go issue [#79804](https://github.com/golang/go/issues/79804), a Darwin/arm64 `-race` fork/exec regression introduced in Go 1.26. The issue explicitly records both child-side crashes and runaway pre-exec children consuming a core. The approved Go 1.26 backport is [#79806](https://github.com/golang/go/issues/79806), released in Go 1.26.5. Its fix commit, `2055b1a15decdb874718dad06bfe573ae74e10dd`, adds `//go:norace` to Darwin `rawSyscall`, `rawSyscall6`, and `rawSyscall9` because their invalid child-side TSan state caused the failure. Source inspection confirmed those annotations in the downloaded Go 1.26.5 toolchain and their absence in the ambient Go 1.26.1 toolchain.

No Serf code or tests were changed to work around the toolchain defect, and no race coverage was reduced. Under the fixed released Go 1.26.5 toolchain, both exact Task 11 race commands passed:

- `(cd agent && GOTOOLCHAIN=go1.26.5 go test -race ./internal/installid ./internal/jobstore . -count=1)` — PASS: installid `1.492s`, jobstore `9.375s`, agent `92.354s`.
- `GOTOOLCHAIN=go1.26.5 go test -race ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1` — PASS: hubcore `6.276s`, hub `115.388s`.

### Final searches and repository state

- The exact production search for `ulid.(Make|New)`, `oklog/ulid`, and duplicate `ProjectID`/`ProjectSlug` definitions returned only the intended shared `identifier/project.go:118` `ProjectID` definition.
- The exact stale-term search returned only immutable historical plans/specifications, including the approved identifier design and plan where the terms describe the old format, rejected compatibility, clean-break fixtures, or the audit command itself. No current operational documentation or production code uses the stale contracts.
- `git diff --check` — PASS.
- Task 11 fix commits before this evidence update are `24a69ccc6`, `abfea7ff9`, `6014c193e`, `90d301a25`, `78b1ce30c`, `9ffc4dd7b`, and `fca76fd51`.

The pre-existing `.superpowers/sdd/task-1-report.md` worktree modification remains the sole protected unrelated exception. It was not edited, staged, reverted, or committed during Task 11.

## Final review fix wave

### Finding 1: canonical project deletion membership mismatch

**Validation:** Accepted. `handleAPIProjectDelete` selected sessions with raw
`EffectiveWorkingDir(e.Meta) == project.CanonicalPath`, while tree ingestion
resolved each distinct effective working directory with `identifier.ResolveProject`
and grouped by `Project.ID`. Linked worktrees, nested repository directories, and
symlink aliases therefore appeared in one tree project but were omitted by deletion.

**RED:**

```text
go test ./cmd/serf-hub -run '^TestProjectDelete(RemovesCanonicalProjectMembers|ResolutionFailureDoesNotPartiallyDelete)$' -count=1 -v
```

Exit 1. `TestProjectDeleteRemovesCanonicalProjectMembers` returned `deleted=[]`
instead of deleting the main-checkout, linked-worktree, nested-directory, and
symlink-alias sessions. `TestProjectDeleteResolutionFailureDoesNotPartiallyDelete`
returned HTTP 200 with no deletions instead of failing membership resolution.

**Implementation:** Added `hubcore.ResolveProjectMapStrict`, factored over the
same resolver loop used by presentation's `ResolveProjectMap`. The delete handler
now collects all uncapped past entries, resolves every distinct effective working
directory before any deletion or decision-row mutation, fails closed with HTTP 500
on any resolution error, and selects members by resolved `Project.ID == body.Key`.
Existing request key/path resolution, current-tree revalidation, live-session
safety checks, and per-file removal behavior remain intact.

**GREEN:**

- Focused new regressions: PASS (`ok primeradiant.com/serf/cmd/serf-hub 0.733s`).
- `go test ./cmd/serf-hub -run '^TestProjectDelete' -count=1` — PASS (`0.683s`).
- `go test ./cmd/serf-hub/internal/hubcore -run '^(TestBuildTreeCanonicalProjectAggregation|TestBuildTree|TestResolveProjectMap)' -count=1` — PASS (`0.419s`).
- Full `go test ./cmd/serf-hub -count=1` — BLOCKED by the sandbox's pre-existing IPv6 listener restriction: `TestHubRPCAuthStatusUsesUserScopedOpenAIAuth` panicked when `httptest` could not bind `[::1]:0` (`operation not permitted`).
- Full `go test ./cmd/serf-hub/internal/hubcore -count=1` — BLOCKED by the same restriction in `FuzzHubcoreScenarios/seed#54` at `httptest.NewServer`.

**Files:** `cmd/serf-hub/web_api_project_delete.go`,
`cmd/serf-hub/web_api_project_delete_test.go`, and
`cmd/serf-hub/internal/hubcore/tree.go`.

### Finding 2: crash-stale installation-ID lock

**Validation:** Accepted. Production used an `O_CREATE|O_EXCL` sentinel and only
removed it on the live owner's normal path. A killed owner permanently left the
sentinel, causing every later invocation to exhaust the bounded retries and return
an empty ID.

**RED:**

```text
go test ./agent/internal/installid -run '^TestLoadOrCreateInstallationID_StaleLockPathDoesNotBlockRecovery$' -count=1 -v
```

Exit 1: `stale lock path suppressed installation ID recovery: got "": invalid UUID payload`.

**Implementation:** Production now opens a persistent lock file without following
Unix symlinks or Windows reparse points, validates that it is a regular file,
secures its mode on Unix, and holds a real advisory lock for the complete
read/generate/temp-write/sync/close/chmod/atomic-replace operation. Supported
Unix targets use nonblocking `flock`; Windows uses `LockFileEx`. All release and
close errors are joined and surfaced as an empty
result rather than reporting success. Advisory locks release on process death, so
stale lock-file contents no longer block recovery. The existing 100-attempt/5ms
bounded contention policy and winner reread remain. The injectable afero entry
point retains its deterministic sentinel seam rather than pretending an in-memory
filesystem provides host advisory locking.

**GREEN:**

- Focused stale, convergence, live-winner, bounded-wait, and held-owner tests — PASS (`0.473s`).
- `go test ./agent/internal/installid -count=1` — PASS (`0.260s`).
- `GOTOOLCHAIN=go1.26.5 go test -race ./agent/internal/installid -count=1` — PASS (`1.382s`).
- Additional compile checks for Linux/amd64 and FreeBSD/amd64 passed; temporary binaries were removed.

**Files:** `agent/internal/installid/installation_id.go`,
`agent/internal/installid/installation_id_test.go`,
`agent/internal/installid/lock_unix.go`,
`agent/internal/installid/lock_windows.go`.

### Finding 3: Windows atomic replacement guarantee

**Validation:** Accepted. The production and injectable paths both called generic
`afero.Fs.Rename`; that does not provide the approved spec's documented atomic
replacement guarantee for an existing invalid singleton on Windows.

**RED:**

```text
go test ./agent/internal/installid -run '^TestLoadOrCreateInstallationID_InvalidValueUsesAtomicReplacement$' -count=1 -v
```

Exit 1 at compile time: `undefined: installationIDAtomicReplace` (three references),
proving the production path had no platform atomic-replacement seam.

**Implementation:** The production OS entry point now routes persistence through
`atomicReplaceInstallationID`; the injectable afero entry point retains generic
rename for non-OS deterministic tests. Supported Unix targets use same-directory
`os.Rename`, whose underlying rename operation atomically replaces the destination.
Windows uses `kernel32!ReplaceFileW` whenever an invalid destination already
exists; it does not use delete-then-rename or `MoveFileEx`. First creation, for
which `ReplaceFileW` is inapplicable, remains a same-directory rename while the
cross-process lock is held. Replace failure is returned and winner-reread semantics
remain; temp cleanup, mode, write, sync, and close ordering are unchanged.

**GREEN:**

- Focused atomic replacement, legacy/invalid replacement, temp cleanup, and stale-lock tests — PASS (`0.399s`).
- Full and race installid results are listed under finding 2.
- `GOOS=windows GOARCH=amd64 go test -c ./agent/internal/installid -o .task11-installid-windows.test.exe` — PASS; binary removed.

**Files:** `agent/internal/installid/installation_id.go`,
`agent/internal/installid/installation_id_test.go`,
`agent/internal/installid/replace_unix.go`,
`agent/internal/installid/replace_windows.go`.

### Final checks and concerns

- `gofmt` over all changed Go files — PASS.
- `go vet ./agent/internal/installid ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub` — PASS.
- `git diff --check` — PASS.
- The required full hub and hubcore suites cannot complete in this sandbox solely
  because local IPv6 listener creation is prohibited; focused affected tests pass.
- The pre-existing `.superpowers/sdd/task-1-report.md` modification was not edited,
  staged, reverted, or committed. `.superpowers/sdd/progress.md` was not edited.

## Re-review platform-scope correction

The fix-wave re-review found that the newly added IBM AIX implementation used
process-associated `fcntl` record locks, which do not serialize two callers in
the same process. Investigation then established that AIX support itself was
unjustified scope: Serf's release matrix contains only Linux/amd64 and
Darwin/arm64, self-update rejects every other release target, the repository
does not document AIX support, and the two AIX-specific files first appeared in
the fix wave. Go still exposes `aix/ppc64`, but that does not make it a declared
Serf platform.

**RED:** a repository assertion requiring
`agent/internal/installid/lock_aix.go` and
`agent/internal/installid/replace_aix.go` to be absent failed because both files
existed. `git diff --name-status 4249041be..HEAD -- '*aix*'` showed both as new
files.

**Implementation:** Removed the unclaimed AIX-specific lock and replacement
implementations instead of expanding this identifier refactor with an AIX-only
in-process lock registry. The retained Windows lock's static regular-file error
now uses `errors.New`, matching the Unix copy and clearing the cross-platform
`perfsprint` finding. No Linux, Darwin, other supported Unix, or Windows lock or
replacement behavior changed.

**GREEN:**

- The repository assertion confirms both AIX-specific files are absent.
- `go test ./agent/internal/installid -count=1` — PASS (`0.437s`).
- `GOTOOLCHAIN=go1.26.5 go test -race ./agent/internal/installid -count=1` — PASS (`1.345s`).
- `(cd agent && golangci-lint run ./internal/installid/...)` — PASS (`0 issues`).
- `go vet ./agent/internal/installid` — PASS.
- Windows/amd64 and FreeBSD/amd64 installid test-binary cross-compiles — PASS;
  temporary binaries were removed.
- `gofmt -d agent/internal/installid/lock_windows.go` and `git diff --check` — PASS.
