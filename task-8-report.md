# Task 8 report — TUI internal tests

## Status

Implemented and self-reviewed. Production code was not changed. All eleven scoped test files were re-evaluated; ten needed edits, while `cmd/evener-tui/internal/transcript/types_covtest_test.go` already had an exact seven-case table and was retained unchanged.

## Changes

- `clipboard_system_covtest_test.go`
  - Replaced the mislabeled WSL "success" fixture that only re-propagated `ErrNoClipboardImage`.
  - The test now supplies a Windows path, observes conversion to `/mnt/c/Users/clip.png` at the filesystem boundary, and asserts the complete returned `PastedImage` state.
- `hub_start_covtest_test.go`
  - Added the repository loopback capability probe to the websocket test.
  - The ordered frame handler now captures and validates the initialize response payload instead of merely being installed.
  - The StateDir process test now uses a child executable fixture that prints its real `EVENER_STATE_DIR` and `XDG_STATE_HOME`; the test asserts both the child output and log exactly.
- `credentials_panel_covtest_test.go`
  - Seeded mismatch/empty-provider cases with existing pending/result state and asserted it is preserved.
  - Asserted exact normalized credential responses and exact create/edit submit payloads.
  - Unknown-message coverage now proves the panel remains unchanged.
- `launch_overrides_covtest_test.go`
  - Asserted exact schema replacement/preservation, exact edit error, preserved unknown-message state, and meaningful rendered content.
- `launch_schema_covtest_test.go`
  - Replaced substring checks with exact multiline summaries, edit values, and stable sorted environment serialization.
- `launch_settings_panel_covtest_test.go`
  - Replaced permissive initial-command and status checks with exact message/state assertions.
  - Seeded schema/load/resolve/save/trust error fixtures so a bad transition cannot be masked by zero state.
  - Required the named pointer rows to exist before checking values.
  - Replaced length-only parser checks with exact MCP, fallback, append, environment, skills-dir, and config-path values, including nil versus non-nil empty results.
- `plugins_client_covtest_test.go`
  - Reworked both table tests to assert every appwire method and exact JSON request payload.
  - Success cases now assert the complete concrete Bubble Tea result message; error cases assert the exact method-wrapped error.
- `plugins_panel_covtest_test.go`
  - Seeded stale/error list and browse state and asserted exact preservation/transition.
  - Asserted complete plugin action payloads.
  - Gave plugins neutral names so `BROKEN`, `DISABLED`, `AUTO-UPGRADE`, and `INSTALLED` can only be satisfied by badges rather than plugin names.
- `tool_bodies_covtest_test.go`
  - Asserted the exact abbreviated job identity and quiet duration rather than generic words.
- `reducer_covtest_test.go`
  - Populated no-op/guard fixtures with sentinel transcript state and asserted preservation.
  - Strengthened delegate revision, failed-turn projection, delegate projection, index miss, and removal/reset assertions.

## Deletions

Deleted only duplicate or no-observable-contract tests:

- `TestCovSourceBadgeColor`: discarded every return value and duplicated stronger exact theme-color coverage in `credentials_panel_test.go`.
- `TestCovApplyEditSandboxInheritExplicit`: exact duplicate of `TestCovApplyEditSandboxInherit`.
- `TestCovSubagentDisplayStatusEmpty`, `TestCovSubagentDisplayStatusNonTerminal`, `TestCovSubagentDisplayStatusTerminalWithOutcome`, and `TestCovSubagentDisplayStatusTerminalNoOutcome`: duplicate subsets of the retained exact table in `types_covtest_test.go`.
- `TestCovApplyReasoningSummaryDeltaEmpty` and `TestCovApplyToolOutputDeltaEmpty`: appending an empty string has no independently observable state transition, so these only restated private early returns.
- `TestCovResetAgentMessageEmptyItemID`: duplicated the lower-level empty-ID guard without an observable behavior distinct from the retained unknown/removal cases.
- `TestCovMergeThreadItemIntoToolInfoNil` and `TestCovMergeLatestDelegateActivityNil`: private nil/no-panic probes with no external contract.

## Commands and results

- `gofmt -w <all eleven scoped test paths>` — passed; `gofmt -d` produced no output.
- `git diff --check` — passed.
- Initial plain targeted package run — incomplete: the session-private empty `GOMODCACHE` attempted to reach `proxy.golang.org`, but sandbox DNS/network is disabled. Verification was rerun offline against the existing shared module cache with `GOMODCACHE=/Users/jesse/go/pkg/mod GOPROXY=off`.
- `GOMODCACHE=/Users/jesse/go/pkg/mod GOPROXY=off go test ./cmd/evener-tui/internal/clipboard ./cmd/evener-tui/internal/launchconfig ./cmd/evener-tui/internal/msgrender ./cmd/evener-tui/internal/transcript -count=1` — passed all four packages.
- `GOMODCACHE=/Users/jesse/go/pkg/mod GOPROXY=off go test ./cmd/evener-tui/internal/hubstart -run '^TestCov(DialHubRPCWithFrameHandler|StartLocalHubWithStateDir|StartLocalHubNoLogFile|StartLocalHubBadBinary|StartLocalHubMkdirAllError)$' -count=1 -v` — package passed: four tests passed; `TestCovDialHubRPCWithFrameHandler` skipped through `e2ecap.RequireLoopbackBind` because this sandbox denies `listen tcp 127.0.0.1:0`.
- A full hubstart package attempt was made and is incomplete in this environment: existing unscoped `TestCheckHubEnvironment` directly calls `httptest.NewServer` and panics when the sandbox denies its loopback bind. The scoped loopback test uses the required capability helper and reports the limitation honestly.

## Staged paths

The commit stages these explicit paths only:

- `cmd/evener-tui/internal/clipboard/clipboard_system_covtest_test.go`
- `cmd/evener-tui/internal/hubstart/hub_start_covtest_test.go`
- `cmd/evener-tui/internal/launchconfig/credentials_panel_covtest_test.go`
- `cmd/evener-tui/internal/launchconfig/launch_overrides_covtest_test.go`
- `cmd/evener-tui/internal/launchconfig/launch_schema_covtest_test.go`
- `cmd/evener-tui/internal/launchconfig/launch_settings_panel_covtest_test.go`
- `cmd/evener-tui/internal/launchconfig/plugins_client_covtest_test.go`
- `cmd/evener-tui/internal/launchconfig/plugins_panel_covtest_test.go`
- `cmd/evener-tui/internal/msgrender/tool_bodies_covtest_test.go`
- `cmd/evener-tui/internal/transcript/reducer_covtest_test.go`
- `task-8-report.md`

`cmd/evener-tui/internal/transcript/types_covtest_test.go` was reviewed but is unchanged, so it is not staged.

## Commit

This report is part of the commit with message `test: strengthen TUI internal coverage tests`. The final commit hash is reported in the task handoff (a commit cannot contain its own hash without changing that hash).

## Concerns / limitations

- The ordered-frame assertion compiles and is guarded correctly but could not execute on this sandbox because loopback bind is prohibited. It should execute, not skip, on an unrestricted host.
- The full hubstart package remains unverified here for the same environmental reason in pre-existing unscoped tests; this is not reported as a pass.
- No production behavior was changed.
