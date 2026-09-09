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

Do not start core state extraction while checkpoint correctness or dependencies remain uncertain. The native v5 regression now passes and is committed. Finish the broader gate work and review the selected landing units before preparing the merge candidate. Preserve the pre-rebase backup and both unrelated dirty Apple files. Jesse subsequently authorized starting small reviewable PRs and landing them after checks and required review. The first four prerequisite PRs are now merged; the native checkpoint itself remains in progress. iPad and dedicated accessibility work remain paused.


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
| [#1066: provider-bound device authorization](https://github.com/prime-radiant-inc/evener/pull/1066) | `df7c8ba67` | Regression red/green, all auth tests, package vet, independent review | Merged; verified on GitHub |
| [#1067: accurate logout result](https://github.com/prime-radiant-inc/evener/pull/1067) | `5eaebfe6e` | Removal and concurrent-write regressions red/green, all auth tests with race detection, package vet, independent review | Merged; verified on GitHub |
| [#1068: empty session array](https://github.com/prime-radiant-inc/evener/pull/1068) | `1c18e6866` | Regression red/green, thread-list tests, package vet, independent review | Merged; verified on GitHub |
| [#1069: update entrypoint aliases](https://github.com/prime-radiant-inc/evener/pull/1069) | `3705a9098` | Portable regression, full hub package, focused race tests, package vet, independent review | Merged; verified on GitHub |

The navigation candidate was omitted after its behavioral regression passed on current main: semantic snapshot comparison already handles nil and empty entities. Changing that representation would add no needed fix. The isolated candidate and audit are retained for traceability. PR #1069 addresses the macOS path-alias failures exposed by the full hub package run, including a portable regression using an explicit parent-directory symlink.

GitHub requires seven named checks, an up-to-date base and one approving review. Auto-merge is disabled. The first four prerequisite PRs have merged. That does not mean the native checkpoint has merged.

The first web check on #1066 failed because Chrome missed its 30-second startup deadline before layout cases ran. The remaining browser guards passed, as did the same frontend on #1067. After the original workflow completed, the targeted retry was accepted as attempt 2 on unchanged head `df7c8ba67`; no timeout or test coverage was changed. The web retry and dependent artifact build passed; all CI checks on #1066 are green. The audit isolated the original failure to DevTools HTTP readiness after Chrome announced its endpoint. The log does not capture enough readiness/runner diagnostics to establish the underlying cause, so this is a passing retry, not a root-cause-fix claim.

RoboRev found a concurrency issue on #1067 at `f0cb0297d`: the existence check and credential removal are separate store operations, and the surrounding credential lock allows concurrent writers. The correction is published at `5eaebfe6e`: logout holds the existing controller lock exclusively across layer selection and removal, while other credential writers retain the shared side. The regression exercises real API-key set and logout handlers and verifies the removal result and final store. All auth tests pass with race detection in both the candidate (9.680 s) and authoritative integration source (10.012 s), and independent Luna review is clean. That correction is included in the merged PR. A successful RoboRev status does not imply a clean review; its actual findings are the review evidence. The exact-head RoboRev comments for #1066, #1068 and #1069 report no issues.

## Next SDK slices

The SDK audit compares the original branch against its actual common base before checking applicability to newer main. Generated protocol types already exist on main and have no original branch delta. The next review boundary is the runtime initialize decoder and handshake-result observer in the client and focused tests; retain main's existing recovery transport and tests. Portable activity/job projections and question-answer formatting can follow in separate slices, with affected web imports updated in each. Package exports/build metadata and outside-checkout qualification follow those dependencies. Keep the larger recipe catalog in capability batches with installed-consumer contract tests. None of these slices begins the proposed higher-level shared-state extraction.

## Second prerequisite batch

Jesse requested a larger batch after verifying the first four merges. These candidates use fresh main `50565eb465981d4b5a8fbbc939ad0bfb245aec68`; the complete native source remains preserved at `394203d56` with subsequent status commits. Each candidate has an isolated sibling worktree and independent local review. The handshake PR is stacked on the separate Linux probe correction; the remaining published candidates target main.

| PR | Scope | Head | Local evidence / current status |
| --- | --- | --- | --- |
| [#1071](https://github.com/prime-radiant-inc/evener/pull/1071) | Daemon initialize build identity | `f539bde96` | Regression red/green, server AppWire tests, vet; CI green and RoboRev clean; approving review required |
| [#1072](https://github.com/prime-radiant-inc/evener/pull/1072) | Empty subagent preview array | `0f7224899` | Wire regression red/green, preview race tests, vet; CI green and RoboRev clean; approving review required |
| [#1073](https://github.com/prime-radiant-inc/evener/pull/1073) | Terminal reasoning lifecycle and turn readback | `928993b99` | Reservation snapshot regression red/green; old turn closes before new start; timing/usage/cost retained; full projector and server AppWire/turn race tests, fuzz seeds, vet, lint and independent review pass; fresh CI/RoboRev running |
| [#1074](https://github.com/prime-radiant-inc/evener/pull/1074) | Omit absent steering images | `e6128ae08` | Nil/empty regression red/green, full projector race suite, vet; CI green and RoboRev clean; approving review required |
| [#1075](https://github.com/prime-radiant-inc/evener/pull/1075) | Persisted goal continuation identity and grouping | `b23d2fa3a` | Three red/green regressions, full transcript race suite (119.842 s), vet; CI green and RoboRev clean; approving review required |
| [#1076](https://github.com/prime-radiant-inc/evener/pull/1076) | Initialize decoder and successful-handshake observer | `4ce87876f` | 81 client/reconnect tests and independent review; full web gate passes with #1079; stacked on #1081, also land after #1071; fresh CI/RoboRev running |
| [#1077](https://github.com/prime-radiant-inc/evener/pull/1077) | Portable question-answer formatter | `49a442845` | 21 formatter tests, canonical web gate; scenario citations corrected and race-tested; CI green and RoboRev clean; approving review required |
| [#1078](https://github.com/prime-radiant-inc/evener/pull/1078) | Skip persisted task/delegate detail in session lists | `b733c5eda` | Task-hydration and project-identity regressions red/green, focused race tests, full hub package (164.389 s), vet and independent review; CI green and RoboRev clean; approving review required |
| [#1079](https://github.com/prime-radiant-inc/evener/pull/1079) | Await provider-dialog lazy import in UI test | `b18b73c5b` | 91 Spawn tests pass in isolated candidate; canonical web gate passes with this correction on the handshake candidate; CI green and RoboRev clean; approving review required |
| [#1081](https://github.com/prime-radiant-inc/evener/pull/1081) | Retry interrupted Linux process-exit probes | `73fe59e55` | Four behavioral red cases; full Linux daemonprocess race suite, vet, Linux-targeted lint and native Darwin tests pass; test lint correction pushed; fresh CI/RoboRev running |

The goal change includes its typed agent persistence metadata and producer hunk because those are required by the reader/index behavior. It excludes the unrelated shutdown changes. Independent review initially questioned whether steering leaves a group open; the reviewer retracted that finding after checking `continuesLogicalTurn` and the indexed/sequential regressions. No redundant representation field was added.

The older roster fast-path proposal dropped cheap goal/usage/capability/recovery metadata along with expensive history reads. PR #1078 shares the existing projection and preserves those fields while omitting task aggregate and delegate history hydration from list rows. Recovery/ownership checks remain intentional v5 correctness guards; this is not an I/O-free projector or a measured latency claim. Review rejected new live-row fallbacks that could replace intentional nil/zero values with stale persisted data. A detailed-read project annotation defect also has a red/green regression and correction. The broader source-fanout proposal and its working files remain preserved in `mobile-land-roster-loading`; it is not published or counted as complete.

The first #1073 fuzz failure was in the fuzz harness's live item fold, which replaced accumulated reasoning text with the status-only completion frame. The source correction preserves live/reload equality. A bounded fuzz run before the final reservation correction completed 7,767 executions; both replay seed corpora pass with race detection on the final correction. Review reproduced reasoning misattribution, missing tool-only completion and an old turn remaining in progress after a durable reservation. The real server readback regression fails on the previous published head and passes after closing the old turn before the new start. It also exposed the snapshot reducer dropping completion usage/cost; those optional fields now survive readback. Final local evidence includes the full projector race suite, server AppWire/turn race suite, vet, scoped lint and independent review.

PR #1077's first root/race CI failures came from stale scenario source citations after the formatter move. Both references and the dock's shared-helper comment are corrected, and the citation test passes with race detection. Current CI and RoboRev are clean.

PR #1076 rejects malformed initialize responses as terminal protocol failures. Its full local web gate exposed a provider-dialog test that did not await the lazy import; #1079 awaits that actual completion without widening a deadline. The 91-test Spawn suite passes in the isolated test candidate, and the canonical web gate passes with the same correction applied to the handshake candidate. That temporary test overlay was removed after publishing #1079; the SDK diff remains limited to the client boundary and its tests.

The handshake CI also observed an interrupted Linux process-exit check. #1081 retries EINTR from the nonblocking Linux poll boundary, preserving live/exited/closed/error outcomes. Four deterministic injected-interruption cases fail without the retry; the full Linux package passes with race detection in an offline container. The original CI error does not identify its exact syscall, so this establishes the tested Linux mechanism rather than a traced incident attribution. Linux-targeted lint found error identity comparisons in the regression; explicit test control flags correct them without changing production behavior. #1076 is stacked on #1081 and must be retargeted to main after that prerequisite merges. #1071 must also land before #1076 to supply direct-daemon build identity.

A separate #1081 browser check failed after all web unit/type/lint gates passed: Vite could not bind the port released by a temporary availability probe. A focused browser-guard candidate is in progress to let the serving process own port allocation. No timeout increase or weakened browser assertion is part of that work.

Ten further PRs are published; seven have green CI and clean RoboRev comments at the recorded heads. GitHub still requires approving review and current checks on main-targeted PRs. The review corrections are committed in their isolated candidate branches; reconciling those reviewed commits with the preserved native integration source is part of checkpoint integration after this batch lands. No second-batch merge or fresh native artifact is recorded.
