# iPhone checkpoint before shared TypeScript extraction

Proposed landing sequence, 9 September 2026. Jesse asked whether to land a mobile checkpoint on main before extracting the web state layer into the SDK. The recommendation is yes. The checkpoint is a working, reviewed integration baseline; it does not declare iPhone v1 release acceptance complete.

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

Do not start core state extraction while checkpoint correctness or dependencies remain uncertain. The native v5 regression now passes and is committed. Finish the broader gate work and review the selected landing units before preparing the merge candidate. Preserve the pre-rebase backup and both unrelated dirty Apple files. No merge is recorded or authorized by this proposal. iPad and dedicated accessibility work remain paused.
