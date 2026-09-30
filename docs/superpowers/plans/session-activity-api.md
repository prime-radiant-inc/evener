# Session Activity API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement the assigned task. Steps use checkbox syntax for tracking. The controller owns delegation and reviews; implementers do not spawn agents.

**Goal:** Make session-owned delegates, shell jobs, and watches accessible through good typed APIs and shared client recovery, independent of global navigation.

**Architecture:** Agent-domain projections provide bounded, scoped reads from existing authorities. Daemon and hub AppWire handlers route those reads. A shared TypeScript owner supplies browser and native activity surfaces while navigation keeps small session summaries.

**Tech Stack:** Go, AppWire, generated TypeScript, React, React Native, existing Go/Vitest/browser test infrastructure.

**Spec:** [Session activity APIs](../../design/session-activity-api.md).

## Global Constraints

- Preserve stable delegate identities and logical receiver ownership of watches.
- Navigation stays roots-only and does not enumerate descendant activity.
- Remove shipped navigation detail fields through a coordinated representation version 3 cutover, with compact counts and both clients updated together.
- No permission, approval, diagnostic-authority, provider, or runtime-restart changes.
- Default tests are deterministic; no live provider calls or credential changes.
- No new compatibility fallback or dual-read path for new consumers.
- Page default 50, maximum 200 rows, encoded response at most 256 KiB.
- Cold scans yield after 2,000 work units or 4 MiB of journal input and honor cancellation.
- Retry transient failures after 1, 2, 4, 8, 16, then 30 seconds, capped at 30 seconds without an attempt limit while observed.
- Use existing domain classifiers, journals, source routing, and thread subscriptions.
- Run generation after protocol edits; import the shared package by name.
- Read testing instructions before test edits. Never run npm ci through a symlink.
- Keep evergreen product docs truthful; implementation progress belongs in the plan ledger.
- Implementers use isolated worktrees and GPT-6.1 Sol with precise task briefs. Prefer low reasoning effort where available; the coordination ledger records the authorized worker settings. The controller integrates commits and owns architectural decisions and the PR review/merge process.

## Review Focus

- A root-only navigation tree and real running descendants must produce complete session activity: Tasks 2, 3, and 5 cover the real producer/router boundary.
- A workspace alias clears or a remote shares a local session ID: Task 3 pins resolved identities and all translated reference fields; Task 4 fences late results.
- A child physically holds a parent's watch: Task 2 checks receiver ownership, delivery/clear invalidation, retained unknown state, and deduplication.
- A journal exceeds one read budget or a live page changes while paging: Tasks 2 and 4 pin advancing cursors, status changes, cancellation, retained data, and stale-cursor recovery.
- Multiple views share a connection: Tasks 4 through 6 pin subscription ownership, request coalescing, scope disposal, and absence of manual repair controls.

## Execution and integration

Preserve PR #3493's history in the integration branch and merge current main
before edits. Do not discard its useful UI changes. Publish only complete,
reviewed rounds. The controller may split independent deliverables into small
PRs when they can pass and be reviewed independently; #3493 remains the activity
surface integration target.

Task 1 establishes the generated contract. Tasks 2 and 3 may start their
independent implementation/test work from the written interface in parallel,
then consume Task 1's commit before qualification. Task 4 starts after Task 1.
Task 5's navigation contract/projection/codec slice (5a) may run after Task 1,
independently of the session read backend and client owner. Its browser consumer
slice (5b) and Task 6 start after Task 4 and consume the version 3 navigation
contract. Shared presentation work (4p) supplies their pure loaded-data adapter
and ref-qualified entity lookup before either consumer migrates. Task 7 runs
after the integrated functional tasks. No two workers edit
the same ownership area concurrently. There are at most three active workers;
a reviewer takes a freed worker slot.

## File ownership and interface map

| Task | Owner and primary files | Dependency |
| --- | --- | --- |
| 1 | AppWire contract, Go client, generation inputs/outputs | Written spec |
| 2 | Agent read projections, bounded derived indexes, domain invalidations | Task 1 types |
| 3 | Server hooks, serve wiring, hub/appsource routing and notification relay | Tasks 1 and 2 interfaces |
| 4 | Framework-free SessionActivity client, public exports and tests | Task 1 types |
| 4p | Pure shared presentation, watch formatting and qualified entity lookup | Task 4 snapshot and Task 1 rows |
| 5a | Navigation contract/projection/shared codec and resource cleanup | Task 1; coordinated generation ownership |
| 5b | Browser activity surfaces, transcript leases and browser guards | Tasks 3, 4, 4p and 5a |
| 6 | Native activity adapter, transcript leases and consumer lifecycle | Tasks 3, 4, 4p and 5a |
| 7 | Integrated qualification, evergreen docs and AGENTS ownership guidance | Tasks 1–6 |

### Task 1: Typed session activity contract

**Files:** Create `appwire/session_activity.go` and `appwire/session_activity_test.go`; modify `appwire/types.go`, `appwire/protocol.go`, `appwire/client.go`, generator inputs if required, and generated protocol/TypeScript artifacts.

**Interfaces:** Produce every named wire type and method from the spec. Add `MethodEvenerThreadActivityRead`, `MethodEvenerThreadDelegatesList`, `MethodEvenerThreadJobsList`, `MethodEvenerThreadWatchesList`, and `NotifyEvenerThreadActivityChanged`. Go client methods are `ThreadActivityRead(ctx, SessionActivityReadParams)`, `ThreadDelegatesList(ctx, SessionActivityListParams)`, `ThreadJobsList(ctx, SessionActivityListParams)`, and `ThreadWatchesList(ctx, SessionActivityListParams)`, returning their concrete response and error. Export `SessionActivityResource` and constants for summary, delegates, jobs, and watches. This task owns generated files throughout integration.

- [x] Add wire-contract tests for typed rows, known-empty arrays, unknown counts, scoped params, and concrete method discovery. Use structured decoding, not large string snapshots.
- [x] Run `go test ./appwire -run 'TestSessionActivity|TestProtocol' -count=1`; record the intended failure before implementation.
- [x] Implement the contract and Go client methods. Use compact domain resource structs, with no nested delegate tree or `any` payload.
- [x] Run `make generate`, then the focused AppWire tests. Record generated file changes and any registration tests that await Task 3 rather than weakening them.
- [x] Commit the contract and send its SHA to the controller immediately so other workers can consume it. Continue only contract/generation corrections assigned by the controller.

### Task 2: Authoritative, bounded session activity reads

**Files:** Create focused files `agent/session_activity.go`, `agent/session_activity_delegates.go`, `agent/session_activity_jobs.go`, `agent/session_activity_watches.go`, `agent/session_activity_cursor.go`, and corresponding tests. Modify existing domain files only for narrow reusable projections, derived indexes, and invalidation publication. This task owns `agent/` changes including event payloads.

**Interfaces:** Produce these methods on `*agent.Session`:

```go
ActivitySummary(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error)
ListActivityDelegates(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error)
ListActivityJobs(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error)
ListActivityWatches(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error)
```

Produce corresponding retained-read functions `LoadSessionActivitySummary`,
`LoadSessionActivityDelegates`, `LoadSessionActivityJobs`, and
`LoadSessionActivityWatches`, taking `(ctx, stateDir, sessionID, params)` and
returning the same typed response. Params refs reaching these functions name the
resolved local session. A live root method may resolve a genuine descendant ref
within its controller tree; unrelated refs are rejected. Publish domain changes
through the existing session event bridge using a typed activity-change event
carrying logical session ID and affected resources. Inform Task 3 of the exact
event type/payload after its first commit.

- [x] Add `TestSessionActivityRealDelegateTree` with a scripted provider creating child and grandchild delegates. Assert stable identities, direct versus subtree ownership, and survival of an unavailable child runtime.
- [x] Add `TestSessionActivityWatchReceiverOwnership`: install a parent-owned child-source watch, assert it appears exactly once for the receiver, assert delivery/clear/end produces the correct invalidation, and distinguish retained unknown state from armed/ended.
- [x] Add cursor tests with 451 rows, large prose, creation and status updates between pages, unrelated-session/cross-resource tokens, journal replacement, deletion, and canceled cold reconstruction. Assert every retained identity is reachable once and continuation advances when no rows can yet be safely emitted.
- [x] Run `go test ./agent -run '^TestSessionActivity' -count=1`; record the intended red results.
- [x] Implement the four domain queries, narrow reusable row projections, and bounded disposable index/cursor state. Reuse the existing controller, shell store, watch receiver projection, bounded scanners, and incremental fold. Do not call the old recursive `JobActivityTree` to implement each new page.
- [x] Prove a summary does not load descendant journals and a warm page does not rescan its entire journal using counted fixture readers or existing scanner seams. Use real source data and no fake lifecycle owner.
- [x] Run focused tests and a targeted race run over the new shared read/index state. Commit in coherent increments, report public interface readiness, and retain red/green output in the report.

### Task 3: Daemon, hub, remote routing, and invalidations

**Files:** Create `server/appwire_session_activity.go`, `server/appwire_session_activity_test.go`, `cmd/evener-hub/app_session_activity.go`, and tests. Modify narrow registration/hook sections in `server/server.go`, `server/appwire_runtime.go`, `cmd/evener/serve.go`, `cmd/evener-hub/app_rpc.go`, and the implementations of `cmd/evener-hub/internal/appsource/Source`. Do not edit agent projection internals or generated files.

**Interfaces:** Consume Task 1's typed methods and Task 2's agent functions. Add typed context-bearing server hooks for all four reads. Add source methods matching the Go client names and responses. All four methods route through the existing deletion/source fences and descendant read resolution; retained fallback is only for known local ended sessions. Map Task 2's activity event to `SessionActivityChangedParams` and route logical receiver/session references correctly.

- [x] Add `TestSessionActivityPublicRoutes` with real registered daemon and hub routers and scripted session state, including a root with descendants absent from navigation.
- [x] Add alias/clear, live-child, ended-local, unavailable-remote, and same-ID-on-two-sources cases. Assert the response's resolved session/context and every nested reference, not only the top-level ref.
- [x] Add cancellation propagation and watch-change notification tests. Verify following activity does not replace or unsubscribe a transcript's thread subscription.
- [x] Run `go test ./server ./cmd/evener-hub ./cmd/evener-hub/internal/appsource -run 'TestSessionActivity' -count=1`; record the intended red results.
- [x] Wire context-bearing handlers and source methods, exact reference translation, retained reads, and notification forwarding. Resolve live session methods per call so thread/clear does not retain the old session. Never answer a child request with the root's rows.
- [x] Run focused tests plus existing source reference-translation and method-registration tests. Report any generated changes needed to Task 1 through the controller, then commit.

### Task 4: Shared recovering activity client

**Files:** Create `appwire-client/typescript/sessionActivityStore.ts` and `sessionActivityStore.test.ts`; modify package exports/build configuration and README as required. Preserve the existing `sessionActivity.ts` metrics decoder. Do not edit browser or native components or generated types.

**Interfaces:** Implement and export `SessionActivityStore` exactly as the spec describes, including `observe(resource): () => void` for view-owned collection demand. `getSnapshot()` returns typed `context`, `summary`, and collection states keyed by delegates/jobs/watches, each exposing rows, loading, complete, error/unavailable state, and whether additional pages exist. Export the snapshot/state types and a narrow `SessionActivityClient` interface. Use shared subscription ownership if already available; isolate any new lease helper in this package. Coordinate the final names of snapshot properties in the report before Tasks 5/6 start.

- [x] Write fake-transport/fake-clock behavior tests for start-summary-only, opening one collection, coalescing notifications, resource-specific refresh, pagination, and truthful counts before page completion.
- [x] Write recovery tests for pre-ready mounts, transient retries through the 30-second cap, disposal, reconnect, stale cursor, scope replacement, and late old-client results. Assert requests and retained state transitions instead of timer implementation details.
- [x] Write tests with two owners on the same connection proving activity does not replace/unsubscribe a mounted transcript and duplicate collection demand coalesces.
- [x] Run the new Vitest file from the frontend test environment and capture red evidence.
- [x] Implement the shared owner using generated types and existing AppWire error classification. A stale cursor restarts only that collection; partial source issues retain successful rows and retry while observed. No UI copy or framework code belongs here.
- [x] Run focused client tests, package checks/typecheck, and Biome on touched package files. Commit and report the exact exported interface for consumers.

### Task 4p: Shared activity presentation

**Files:** Add `appwire-client/typescript/sessionActivityPresentation.ts` and its
tests. Adapt `activityData.ts`, `activityRows.ts`, `entityView.ts`, `watchText.ts`,
their tests, and package exports/build/qualification surfaces. Browser and native
networking and actions remain owned by Tasks 5b and 6.

**Interfaces:** `projectSessionActivity(snapshot)` is a pure adapter over loaded
domain rows, returning the rendering tree, original context and summary, domain
watches, completeness, pending state and issues. Rendering identities combine
authoritative refs with logical IDs. Entity lookup uses kind, logical ID and
owning ref; raw action targets remain separate from rendering keys. Partial
usage and missing logical session IDs remain unknown rather than fabricated.

- [x] Pin reversed parent/child pages, missing parents, equal logical IDs on separate sources, retained/live reconciliation, original action targets, optional facts, unknown counts and explicit watch states with structured behavior tests.
- [x] Project loaded records without networking, retry state, lifecycle folding or a new ordering authority. Keep unresolved descendants visible with incomplete relationship evidence.
- [x] Move shared watch presentation to `SessionWatch` and generated domain cadence types. Preserve armed, ended and unknown distinctly.
- [x] Run focused shared tests, installed package qualification and the pinned formatter. Enumerate consumer migrations and obtain independent spec and quality review.

### Task 5: Browser activity surfaces and navigation boundary

**Files:** Modify `cmd/evener-hub/frontend/src/shell/focusedSession.ts`, `shell/statusbar/statusScope.ts`, `shell/statusbar/StatusBar.tsx`, `shell/activitybar/` and their tests; add a focused browser binding for the shared owner. Modify navigation protocol/projection/service/store files solely to remove the unmerged subagents resource and activity detail dependence after consumers migrate. Repair `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs` around its Tasks fixture. Coordinate generated changes with Task 1.

**Interfaces:** Consume Task 4's shared owner and Task 1's domain rows. Preserve existing workspace focus/routing and the Tasks tab's task model. Context supplies ancestry; navigation locations still supply rail placement. The browser binding shares owners by connection/ref/scope and releases view demand on disposal.

- [x] Add UI tests that get counts and ancestry without fetching collection pages; opening Agents/Jobs/Watches acquires only that collection; deep child drill-in does not require sibling pages.
- [x] Add tests for loading versus known-empty, retained data through disconnect, automatic pagination recovery, and scope switching during a request. Use the shared owner rather than a fabricated navigation tree.
- [x] Run focused sidebar/status/focus tests, capture intended failures, and migrate the views to typed domain data. Preserve row actions and embedded Tasks behavior.
- [x] Remove the unmerged navigation subagents API, child-derived attention count, and obsolete fixtures. Inventory all consumers of navigation jobs/watch arrays before removing them; migrate a dependent view to the shared owner rather than silently dropping behavior. Keep root-only tree/attention tests intact.
- [x] Cut navigation over to representation version 3: advertised read versions, validation, shared client requests/codec, browser fixtures and native callers must agree. Keep compact running-job/watch counts and a bounded command summary. Do not add v2 fallback emission or change the AppWire connection version for this navigation-only shape change.
- [x] Update the browser layout guard to exercise Tasks in the activity sidebar and retain its actual overflow/collapse assertions. Run targeted navigation tests, `make test-web`, and the affected real-browser guard.
- [x] Commit, with source/behavior evidence that global navigation no longer carries activity detail or builds per-session child resources.

### Task 6: Native consumers use the shared owner

**Files:** Modify `mobile-native/src/subagents/subagentTree.ts`, `useSubagentTree.ts`, related adapters/tests, and only the native row/detail consumers necessary for the new data shape. Preserve existing shell-job output and stop control code. Read native AGENTS instructions and the pinned Expo documentation before changing native code.

**Interfaces:** Consume Task 4's shared owner with subtree scope. A pure adapter may provide the existing rendering model from flat delegate/job rows; it must not add networking, recovery loops, lifecycle interpretation, or another source of truth.

- [x] Add native owner/adapter tests for child/grandchild identity, shell-job ownership, missing runtime, retained pages, reconnect, and route disposal while another consumer remains mounted.
- [x] Run the focused native tests and capture red evidence before replacing the duplicate fetch/reload loop with the shared owner.
- [x] Preserve scope, counts, model/usage fields when available, nested drill-in, delegate stop reconciliation, and shell-job detail/output behavior. No new native visual redesign is included.
- [x] Run affected native tests, native typecheck, and touched-path formatter. Report simulator/device qualification separately from these automated checks.
- [x] Commit and document any genuinely unavailable optional fields rather than substituting misleading zero values.

### Task 7: Integrated qualification and evergreen ownership

**Files:** Update `docs/product/subsystems.md`, `docs/product/README.md`, `docs/job-control.md`, `appwire-client/typescript/README.md`, and `AGENTS.md`; add `docs/product/session-activity.md`. Update source-linked architecture/API docs and remove contradictory activity-surface design assertions. Integration fixes stay owned by their original implementers.

**Interfaces:** Consume the integrated branch and every task's test/review report. Product docs describe only behavior verified in that branch. Add concise AGENTS guidance that session activity comes from domain APIs and changes update the owning guide/subsystem map and real producer/router tests.

- [x] Run the source-backed acceptance matrix from the spec across real producers, public routers, shared client, and both consumers. Resolve missing evidence through the owning task before claiming completion.
- [x] Keep the existing root-only navigation invariants and reproduce the original missing-agent failure against the new public route. Verify every page can be consumed under changes and reconnect without a repair click.
- [x] Update evergreen ownership, scope, pagination, recovery, and retention semantics with verified source references. Keep deferred friction cases R08, T04, T05, and D01 deferred.
- [x] Apply simplify-code to the completed diff and run the tests covering simplifications. Complete generated freshness, focused race checks, web/native/API checks, and browser guards appropriate to the final diff.
- [x] Obtain independent task and whole-branch reviews. Resolve valid important findings at the root and retain red/green or conclusive refutation evidence.
- [ ] Update PR #3493's title/body to describe the resulting behavior, push one complete round, and inspect all current-head CI checks, the actual combined RoboRev comment, and open per-commit reviews. Repeat only for actionable findings. Merge under Jesse's existing green-CI/Low-or-refuted authorization and complete the repository's post-merge qualification. Handle actionable Low findings in a small follow-up.

## Completion evidence

The controller ledger records each worker's worktree, base/head commits, red/green
commands, task review, remaining findings, PR head, CI/review evidence, merge
identity, and post-merge qualification. The final report links the design,
implementation, owning evergreen documentation, and actual shipped evidence.
