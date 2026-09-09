# iPhone checkpoint before shared TypeScript extraction

Landing sequence authorized by Jesse, 9 September 2026. Jesse asked whether to land a mobile checkpoint on main before extracting the web state layer into the SDK. The recommendation is yes. The checkpoint is a working, reviewed integration baseline; it does not declare iPhone v1 release acceptance complete.

## Scope observed

The authoritative worktree is `live-concepts-plan2-integrate`. The completed rebase target is main `48dcab480272cf4d48b5b62fd2c0da6e1f5fc720`; the scope audit used integration head `7055a66d961c9bd38c996e20ef8defcb8fc66085`. Native recovery subsequently landed locally as `55bba80de`, followed by the evidence-digest scan correction `16ae60ac5`. The audited exact-base branch comparison contains 1,776 changed files, 321,459 additions and 1,727 deletions. It includes two older mobile implementations and extensive historical evidence in addition to the native app.

The shared remote-tracking main ref subsequently advanced to `88dff840510e42fbab1d449a75c7a1cba08aa3de`. Missing changes from that newer main are not branch deletions. A landing candidate must be refreshed against the then-current main and verified at its own exact head.

A read-only import audit identified eight shared `mobile/src` runtime modules consumed by native: `conversation/model.ts`, `conversation/project.ts`, `services/activity.ts`, `services/conversation.ts`, `services/newSession.ts`, `services/roster.ts`, `state/activity.ts`, and `state/conversation.ts`. Their behavioral tests and fixtures are additional dependencies. Native also imports the existing web protocol/client and selected portable helpers. Copying only `mobile-native/` would produce an incomplete checkout.

## Landing units

1. **Required server and SDK foundations.** Review the protocol package, its public exports, examples and outside-checkout qualification together with the server fixes required by native loading, live subscription, recovery and transcript behavior. Keep current web imports working. Audit each agent/server change for a concrete native dependency; unrelated changes remain separate. If the backend corrections are independently substantial, split them into their own prerequisite PR. This is packaging and correctness work already implemented on the branch, not the proposed extraction of shared state.
2. **The native iPhone checkpoint.** Include the Expo app, its eight shared runtime modules at their current paths, the tests and fixtures that validate them, native dependencies/configuration, build controls, CI gates and TestFlight automation needed to reproduce it. Include a concise status and acceptance index with the evidence it references. Keeping the shared modules at their existing paths avoids turning checkpoint preparation into the core refactor.
3. **A reproducible checkpoint build.** Build the reviewed source, exercise the core iPhone journey against the current protocol, record source/build identities, and produce a new TestFlight build from that source. The existing 0.1.0 (3) artifact predates this v5 rebase and cannot stand in for this gate.
4. **Shared state extraction.** Start from the landed checkpoint. Review and implement the separate [shared-state design](../../superpowers/specs/2026-09-09-shared-appwire-state-design.md), adopting the extracted web behavior in web first and then native. Use separate commits/PRs for the public state entry point, mutation/persistence boundary, session controller, native adoption and navigation consolidation.

The old `mobile-concepts` application, the unused Tauri UI/runtime, its generated Apple project and unrelated historical artifacts do not belong in the native runtime landing unit. Preserve them on the existing branch and backup; exclusion from a new reviewed candidate must not delete or reset their work. Historical evidence remains immutable. Any documentation chosen for the candidate must have its referenced evidence retained or an explicit historical branch reference.

## Checkpoint acceptance

- The candidate builds from a clean checkout with its declared dependencies. Native checking must not rely on an installed older mobile app or undeclared sibling dependencies.
- The canonical merge gate passes on the candidate head. Run web browser guards for affected web behavior and a current-artifact native smoke journey separately.
- The iPhone journey covers connect, project-organized session browsing, automatic paging, open/read, send/stop, question or approval handling, and return after ordinary disconnection. Retain existing text/image drafts and reader state.
- AppWire v5 recovery is explicitly checked: incompatible saved history stays readable, dedicated force-stop reaches the server, and resume refreshes the current destination after reconnect without refreshing a destination the user has left.
- Status distinguishes implemented functionality, deterministic verification, simulator evidence, TestFlight availability and physical-device acceptance. Outstanding performance, full SDK outcome coverage and iPhone v1 acceptance remain visible.
- Independent review covers the actual proposed diff and artifact. Passing checks on the larger development branch do not automatically qualify a newly selected subset.

## Preservation and sequencing

Do not start core state extraction while checkpoint correctness or dependencies remain uncertain. The native v5 regression now passes and is committed. Finish the broader gate work and review the selected landing units before preparing the merge candidate. Preserve the pre-rebase backup and both unrelated dirty Apple files. Jesse subsequently authorized starting small reviewable PRs and landing them after checks and required review. No merge is recorded yet. iPad and dedicated accessibility work remain paused.


## First prerequisite batch

The first candidates are based on main `ab02d1cbbfd9f33b4e318105574caaa47c398e9a`, with source preserved at `b9312580a11a454e304d0f59ac045f4ca7e438dc`. Each has an isolated sibling landing worktree; the authoritative native branch and its Apple edits remain unchanged.

| Candidate | Scope | Branch |
| --- | --- | --- |
| Empty session list | Return an empty JSON array rather than null for an empty hub | `codex/mobile-empty-session-list` |
| Device authorization binding | Reject a device flow polled for another provider instance without consuming the original flow | `codex/mobile-device-auth-binding` |
| Logout result | Report whether a stored credential actually existed while preserving environment credentials | `codex/mobile-logout-result` |

These changes already existed in the mobile integration source. Extracting them into focused candidates does not add new product scope. Roster performance, lifecycle/transcript corrections, SDK packaging, shared native dependencies and the app itself remain subsequent batches.


## Published prerequisite PRs

| PR | Reviewed head | Local validation | Landing status |
| --- | --- | --- | --- |
| [#1066: provider-bound device authorization](https://github.com/prime-radiant-inc/evener/pull/1066) | `df7c8ba67` | Regression red/green, all auth tests, package vet, independent review | Open; all CI checks and RoboRev pass; approving GitHub review required |
| [#1067: accurate logout result](https://github.com/prime-radiant-inc/evener/pull/1067) | `5eaebfe6e` | Removal and concurrent-write regressions red/green, all auth tests with race detection, package vet, independent review | Open; corrected head published; CI, RoboRev and approving GitHub review required |
| [#1068: empty session array](https://github.com/prime-radiant-inc/evener/pull/1068) | `1c18e6866` | Regression red/green, thread-list tests, package vet, independent review | Open; all CI checks and RoboRev pass; approving GitHub review required |
| [#1069: update entrypoint aliases](https://github.com/prime-radiant-inc/evener/pull/1069) | `3705a9098` | Portable regression, full hub package, focused race tests, package vet, independent review | Open; all CI checks and RoboRev pass; approving GitHub review required |

The navigation candidate was omitted after its behavioral regression passed on current main: semantic snapshot comparison already handles nil and empty entities. Changing that representation would add no needed fix. The isolated candidate and audit are retained for traceability. PR #1069 addresses the macOS path-alias failures exposed by the full hub package run, including a portable regression using an explicit parent-directory symlink.

GitHub requires seven named checks, an up-to-date base and one approving review. Auto-merge is disabled. These are open prerequisite PRs, not evidence that the native checkpoint has merged.

The first web check on #1066 failed because Chrome missed its 30-second startup deadline before layout cases ran. The remaining browser guards passed, as did the same frontend on #1067. After the original workflow completed, the targeted retry was accepted as attempt 2 on unchanged head `df7c8ba67`; no timeout or test coverage was changed. The web retry and dependent artifact build passed; all CI checks on #1066 are green. The audit isolated the original failure to DevTools HTTP readiness after Chrome announced its endpoint. The log does not capture enough readiness/runner diagnostics to establish the underlying cause, so this is a passing retry, not a root-cause-fix claim.

RoboRev found a concurrency issue on #1067 at `f0cb0297d`: the existence check and credential removal are separate store operations, and the surrounding credential lock allows concurrent writers. The correction is published at `5eaebfe6e`: logout holds the existing controller lock exclusively across layer selection and removal, while other credential writers retain the shared side. The regression exercises real API-key set and logout handlers and verifies the removal result and final store. All auth tests pass with race detection in both the candidate (9.680 s) and authoritative integration source (10.012 s), and independent Luna review is clean. CI and RoboRev must rerun on this corrected head. A successful RoboRev status does not imply a clean review; its actual findings are the review evidence. The exact-head RoboRev comments for #1066, #1068 and #1069 report no issues.

## Next SDK slices

The SDK audit compares the original branch against its actual common base before checking applicability to newer main. Generated protocol types already exist on main and have no original branch delta. The next review boundary is the runtime initialize decoder and handshake-result observer in the client and focused tests; retain main's existing recovery transport and tests. Portable activity/job projections and question-answer formatting can follow in separate slices, with affected web imports updated in each. Package exports/build metadata and outside-checkout qualification follow those dependencies. Keep the larger recipe catalog in capability batches with installed-consumer contract tests. None of these slices begins the proposed higher-level shared-state extraction.
