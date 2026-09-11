# Mobile landing queue: fix current-head RoboRev findings on the six candidate PRs

Spec: GitHub issue prime-radiant-inc/evener#1116 ("Native iPhone v1: full remaining
work and laptop repair handoff"), section "Active work update — 10 September 2026".

Coordinator: the Claude session in
`/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/mobile-app-integration-6d4885`.
The coordinator owns pushes, merges, CI/RoboRev requalification and merge order
(#1098 before #1100; #1091 before #1096; #1105 and #1109 independent).

Each task below is one PR. The coordinator has ALREADY merged `origin/main`
(`ff0ec6474`) into every PR branch as a `--no-ff` merge commit; that merge is the
HEAD of each worktree when the implementer starts. Implementers fix the findings
listed in their task, in their own worktree, and commit. They do not push.

## Global Constraints (bind every task)

1. Work only inside your assigned worktree. Never touch another worktree, the main
   checkout at `/Users/jesse/git/prime-radiant-inc/evener`, or any other branch.
2. NEVER run `go clean -cache`, `go clean -testcache`, `go clean -modcache`, or
   delete anything under the Go build cache or any `node_modules`. Other lanes are
   building concurrently and share the Go cache. Never bypass git hooks
   (`--no-verify` is forbidden).
3. Do NOT push. Commit on the branch already checked out in your worktree and stop.
   Do not create new branches.
4. Before adding or changing any test, read `AGENTS.md` and
   `docs/developing-evener/testing.md` in your worktree.
5. TDD for every finding: write the failing regression test first, run it and record
   the RED output, then implement the fix and record the GREEN output. A test that
   passes before the fix is not a regression test.
6. Do not weaken, delete, or re-point existing tests or expected values to make
   something pass. If an existing test contradicts a finding, report it.
7. If, after reading the code, you conclude a RoboRev finding is factually wrong
   (the described defect cannot occur), do NOT implement a change for it. Explain
   why with file:line evidence in your report and use status DONE_WITH_CONCERNS.
   The coordinator will rule and reply on the PR.
8. Do not invent new AppWire protocol methods or notification types. If you must
   touch `appwire/protocol.go` or another `go:generate` source, run `make generate`
   and commit the regenerated output in the same commit.
9. Frontend TypeScript (`cmd/evener-hub/frontend/src/**`): run
   `npx biome check --write <touched files>` before committing and make sure
   `make test-web` passes from the worktree root. Avoid `noNonNullAssertion` and
   array-index-key violations.
10. Go: run `gofmt -l` on touched files (must print nothing), `go vet ./...` in each
    touched module, and `go test -count=1 ./<touched package>/...` for every touched
    package. For concurrency-sensitive changes also run the focused tests with
    `-race`. Do NOT run `make merge-approval-gate` or `make test-race`; the
    coordinator and CI own the full gates.
11. Smallest change that fixes the finding. Match the surrounding code style. Name
    things by what they do in the domain. No comments about what used to be there.
12. Commit messages follow the repo's conventional style (e.g. `fix(hub): …`,
    `fix(agent): …`, `fix(webui): …`). No attribution lines or trailers.
13. Test output must be pristine: no stray warnings, no leftover debug output.
14. Do not modify historical documentation or receipts under `docs/` unless the
    task says so.

## Task 1: PR #1091 portable activity — three RoboRev findings (frontend TypeScript)

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity` (PR https://github.com/prime-radiant-inc/evener/pull/1091)
Frontend dir: `cmd/evener-hub/frontend` (deps already installed by the coordinator via
`npm ci`; if `node_modules` is missing run `NODE_DISABLE_COMPILE_CACHE=1 npm ci` there).
Focused tests: `npx vitest run <file>` from `cmd/evener-hub/frontend`; full frontend gate:
`make test-web` from the worktree root.

Context from the spec: this PR shares activity and job-output helpers between the web
frontend and the future native client. Existing fixes on the branch preserve
continuation freshness, align backend/frontend failure counts, and retain the max
activity timestamp. Sparse omitted usage counts are allowed by Go `omitempty`; do not
reintroduce the old invalid requirement that every count be present. The backend truth
for failure classification is `agent/jobs_activity.go`.

RoboRev findings at head `320b90c` (verbatim):

- **Queued root refresh can supersede an active continuation** —
  `cmd/evener-hub/frontend/src/stores/activitySummary.ts:190-197`,
  `cmd/evener-hub/frontend/src/stores/activityPanel.ts:124-146`: If a newer jobs bump
  queues `pendingBump` and the user starts "Load more" before the original root request
  settles, `issuePendingBump()` starts another root fetch whose `beginFetch()` replaces
  the continuation's request ID. The continuation result is then discarded by
  `publishFetch()`'s request-ID guard, silently losing the successfully requested page.
  Fix: defer queued root refreshes while a continuation is active, or coordinate so the
  continuation completes and merges before the queued root refresh replaces the panel
  tree.

- **Row failure vs. badge failure counting diverge from backend truth** —
  `cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.ts` (`entryIsFailed`,
  `activityDelegateState`) vs `cmd/evener-hub/frontend/src/protocol/activityMerge.ts`
  (`summarizeSession`): Rows and fold `failedCount` use status-inclusive
  `isActivityFailure(outcome, status)`, while merged badge counts use outcome-only
  checks (`outcome === "failure"` for shells/turns, `outcome === "failed" || "exhausted"`
  for stable delegates, matching `agent/jobs_activity.go`). A terminal entry with status
  `failed`/`error`/`exhausted` but no failure outcome renders as failed in the tree yet
  counts as completed in the badge and aggregate. Fix: align one side with backend
  truth — make terminal row failure outcome-only, or make `summarizeSession` include
  status failures.

- **Turn-container clone overwrites newer turns for off-target delegates** —
  `cmd/evener-hub/frontend/src/protocol/activityMerge.ts` (`revisionFencedDelegate`,
  `mergeDelegate`): Stable delegates are fenced by `projectionRevision`, but turn
  containers unconditionally `cloneDelegate(patch)`, replacing `turns` wholesale. A
  nested child continuation therefore overwrites current turns with the patch snapshot
  even when the page only targeted the child session, regressing newer turns. Fix:
  fence or merge turns when `!withinTarget` — retain current turns unless the target is
  the delegate itself, keeping only the existing `maxActivity` timestamp merge for
  off-target delegates.

Requirements:
1. For finding 2, decide which side to align by reading `agent/jobs_activity.go` (the
   backend is the truth). State the decision and the evidence in your report.
2. One regression test per finding, in the existing vitest suites next to the code
   (`*.test.ts`), written RED first.
3. Run `npx biome check --write` on touched files, then `make test-web` from the
   worktree root; it must pass with pristine output.
4. One commit per finding is preferred; a single commit is acceptable if the changes
   are entangled. Report commits and evidence.

## Task 2: PR #1096 native checkpoint — open task count ignores cancelled/remaining

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR https://github.com/prime-radiant-inc/evener/pull/1096)
Native app dir: `mobile-native/`; shared session core: `mobile/src/`. Deps are installed
by the coordinator via `npm ci --prefix mobile-native` (its `postinstall` applies the
checked-in `patches/`; if `mobile-native/node_modules` is missing run
`NODE_DISABLE_COMPILE_CACHE=1 npm ci --prefix mobile-native` from the worktree root).
Tests: `make test-native` from the worktree root runs `npm test`, `npm run test:shared`
and `npm run check` (TypeScript) in `mobile-native`. Focused: from `mobile-native`,
`npx vitest run --root .. --config mobile-native/vitest.config.mts <path under mobile/src>`.
Do not delete or weaken the keybinding v-flag assertion in the native tests.

RoboRev finding at head `0a1330f` (verbatim):

- **Severity**: Medium
- **Location**: `mobile/src/services/activity.ts:412-421`; `mobile/src/state/activity.ts:430-443`
- **Problem**: Both the initial activity projection and live task-update handler
  calculate open tasks as `total - done`, ignoring the protocol's `cancelled` and
  authoritative `remaining` fields. A session with three cancelled tasks and no
  completed tasks is therefore reported as having three open tasks, making terminal
  work appear actionable.
- **Fix**: Preserve the task outcome fields and derive the open count from `remaining`,
  falling back to `total - done - cancelled` when the field is omitted; cover
  cancelled-only and zero-remaining updates with behavioral tests.

Requirements:
1. Confirm the field names and optionality against the generated protocol types the
   shared core consumes (search `mobile/src` and `cmd/evener-hub/frontend/src/protocol`
   for the task summary / task update types; the Go source of truth is
   `appwire/protocol.go`). Cite the exact type in your report. Do not invent fields.
2. Regression tests RED first: cancelled-only update, zero-remaining update, and the
   omitted-`remaining` fallback, for both the initial projection and the live handler.
3. `make test-native` passes with pristine output (all three parts).
4. Single commit, e.g. `fix(mobile): derive open task count from remaining and cancelled`.

## Task 3: PR #1100 round timings — compaction replay copies leak into public transcript

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay` (PR https://github.com/prime-radiant-inc/evener/pull/1100)
Module: `agent/` (its own Go module; run `go` commands from inside `agent/`).

Context from the spec: this PR makes compaction interleaved with round timings replay
with stable live/cold coordinates. Recovery-tail copies use `context_replay` metadata
(`entry.Turn.ContextReplay`). They must remain available to model resume history but
must not duplicate visible transcript items, usage, ATIF steps or logical boundaries.
Full projection, indexed grouping and the one-item paging path already skip those
copies. The public transcript (markdown/outline) projection does not.

RoboRev finding at head `5da490c` (verbatim):

- **Compaction replay entries leak into public transcript projection**
  - **Location:** `agent/session_compaction.go:220-221`; `agent/session_tools_transcript.go:1026-1033`
  - **Problem:** Compaction now appends `ContextReplay` copies to the durable
    transcript, but the public transcript projection does not exclude them. Markdown
    and outline reads therefore expose duplicate conversation entries, distort turn
    ranges and counts, and may cause duplicate tool-result call IDs to be paired
    unpredictably.
  - **Fix:** Filter `entry.Turn.ContextReplay` from `publicTranscriptEntry`/
    `publicTranscriptData` before range calculation and rendering, while retaining
    the copies only for resume/model-context reconstruction.

Requirements:
1. Filter `ContextReplay` entries out of the public transcript projection BEFORE range
   and count calculation, in the helper(s) the finding names (or the single shared
   helper they both call, if one exists — do not duplicate the filter).
2. Model resume history must still include the copies; add or extend a test proving
   that is unchanged.
3. Regression test RED first: a transcript containing a compaction with replay copies
   must render markdown/outline with no duplicate entries, correct turn ranges/counts,
   and each tool-call ID paired exactly once. Follow the existing scripted-provider /
   fixture patterns in the `agent` tests (read `docs/developing-evener/testing.md` and
   `docs/developing-evener/agent-test-serial-prefix.md` first; the agent test suite has
   a deadline audit that rejects bare wall-clock bounds).
4. Run `go test -count=1 ./...` inside `agent/` plus `go vet ./...`; run the focused
   new tests with `-race`.
5. Single commit, e.g. `fix(agent): exclude context replay copies from public transcript`.

## Task 4: PR #1105 local fork capability — fork paths discard resolved state directory

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability` (PR https://github.com/prime-radiant-inc/evener/pull/1105)
Package: `cmd/evener-hub` (root module).

Context from the spec: the PR advertises and implements fork capability for local
sessions with ownership admission. Stopped saved delegates remain forkable by the
established main contract; live aliases remain blocked. Do not change those contracts.

RoboRev finding at head `f0a0c97` (verbatim):

- `cmd/evener-hub/app_threadlifecycle.go:798-803` & `cmd/evener-hub/app_threadlifecycle.go:835-839`
  — `ownershipEntry` can locate a session in `cfg.StateDir/projects/<project>` when the
  past index is unavailable or stale, but both fork paths discard the returned
  `entry.StateDir` and fall back to `cfg.StateDir`. In production `cfg.StateDir` is the
  parent of `projects`, so `agent.AsideSession` and `agent.ForkSession*` read the wrong
  directory and fail even though ownership admission and the advertised fork
  capability succeeded. Initialize `stateDir` from `entry.StateDir` and only fall back
  to `cfg.StateDir` when the admitted entry has no state directory, in both the aside
  and normal fork paths.

Requirements:
1. Regression tests RED first, for BOTH fork modes (aside and normal), with the source
   session's files nested under `cfg.StateDir/projects/<project>` and the past index
   absent or stale so `ownershipEntry` resolves via the project scan. Follow the existing
   hub test fixtures in `cmd/evener-hub/*_test.go` (read `docs/developing-evener/testing.md`
   first). Before the fix the fork must fail; after it must succeed.
2. Fix both handlers as the finding describes; keep the `cfg.StateDir` fallback only
   when the entry carries no state directory.
3. Run `go test -count=1 ./cmd/evener-hub/...` and `go vet ./cmd/evener-hub/...` from the
   worktree root; run the new tests with `-race`.
4. Single commit, e.g. `fix(hub): fork from the admitted entry's state directory`.

## Task 5: PR #1109 shutdown boundary — CI deadline audit failure plus RoboRev findings

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1109`
Branch: `codex/mobile-shutdown-boundary` (PR https://github.com/prime-radiant-inc/evener/pull/1109)
Modules: `agent/` (own module) and `cmd/evener` (root module).

Context from the spec: this PR makes daemon shutdown publish a terminal session
boundary (SESSION_END) through a bounded path even when a bridge is wedged or a
sender is blocked behind a full buffer, serializes session identity replacement
against old close/event drain, and gives cleanup Notification hooks the close context.
Preserve the retryable rendezvous removal and the identity-swap/drain-order fixes
already on the branch.

### Part A — CI failure (required checks `tests` and `race-modules / agent` fail)

`TestNoBareWallClockDeadlineInAgentTests` (`agent/deadline_audit_test.go:56`) fails:
- `agent/session_lossless_events_test.go:140`: bare wall-clock bound passed to `time.After(...)`
- `agent/session_lossless_events_test.go:158`: bare wall-clock bound passed to `awaitWithin(...)`

Read `agent/deadline_audit_test.go` and `docs/developing-evener/testing.md` to learn the
accepted deadline helpers, then rewrite those two waits the way the rest of the agent
tests do. Do not relax the audit.

### Part B — RoboRev findings at head `22f3602` (verbatim)

Medium:

- **`agent/session_lifecycle.go:573`** — Terminal `SESSION_END` only calls
  `jobManager.onSessionEvent` when `delivered` is true, but `emitWithProvenance` always
  fans out even when the channel drops. With a full buffer and no consumer, or a wedged
  bridge that hits the close deadline, the job manager misses the terminal event.
  - Fix: Notify `jobManager` whenever `emitEnd` fires regardless of channel delivery,
    keeping channel backpressure separate from job-tree projection.

- **`agent/session_lifecycle.go:331`, `cmd/evener/serve.go:1373`** —
  `closeSupersededSession` claims a shutdown close still publishes the terminal
  boundary after an ordinary close consumed the path, but both go through
  `closeOnce.Do`, so the second call is a no-op and cannot emit anything.
  - Fix: Make the terminal emission bypass `closeOnce` or document that double-close is
    unsupported and add a test covering `Close` followed by `CloseForShutdown`.

- **`cmd/evener/serve.go:1345`** — Shutdown calls `rvRegistration.Remove()` only once
  and discards its error. Since `Registration.Remove` now deliberately leaves
  `registered` set after a failed removal so it can be retried, a transient filesystem
  failure can leave a stale rendezvous PID artifact after the daemon exits, potentially
  causing later discovery or PID-reuse problems.
  - Fix: Retry rendezvous removal with bounded backoff during shutdown, and log or
    otherwise surface a persistent failure after the retry budget is exhausted.

Low:

- **`cmd/evener/internal/rvreg/rvreg.go:18`** — `Register` now holds `mu` across
  `rendezvous.Write` filesystem I/O, widening the critical section that previously did
  the write before locking and can stall `UpdateSessionID`/`Remove` behind disk work.
  - Fix: Restore write-before-lock ordering, checking `removed` before and after the
    write under lock.

- **`agent/session_events.go:348`** — Every `sendEvent` takes exclusive
  `closeCtxMu.Lock` just to lazily create `closeSignal`, serializing the event hot path
  on an exclusive lock.
  - Fix: Use a read-fast-path with upgrade only when `closeSignal` is nil, or initialize
    the signal once at session construction.

Requirements:
1. Part A first, as its own commit.
2. For each Medium finding: verify it against the code. If real, regression test RED
   first (a deterministic test, no bare wall-clock deadlines), then fix. If the code
   already handles it (for example the double-close finding may be a misread of what
   `closeSupersededSession` does), do not change behaviour; instead add the test the
   finding asks for that proves the existing behaviour and explain in the report.
3. For the two Low findings: implement the write-before-lock ordering in `rvreg` and
   the once-at-construction (or read-fast-path) close signal, each with the existing
   tests still passing. If either would weaken a guarantee the branch's tests rely on,
   explain and skip it with DONE_WITH_CONCERNS.
4. Run inside `agent/`: `go test -count=1 ./...` and `go vet ./...`; from the root:
   `go test -count=1 ./cmd/evener/...` and `go vet ./cmd/evener/...`; and the focused
   serve/bridge/lifecycle/rvreg tests with `-race`. Also run
   `go test -count=1 -run TestNoBareWallClockDeadlineInAgentTests ./...` inside `agent/`.
5. Commits: one for Part A, then one per finding (or one per closely related pair).

## Task 6: PR #1098 environment replay — ambiguous transcript-write failure can duplicate environment entries

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`
Branch: `codex/mobile-transcript-identity` (PR https://github.com/prime-radiant-inc/evener/pull/1098)
Module: `agent/` (its own Go module; run `go` commands from inside it).
Current head `83efe2731` is the merge of origin/main; CI is fully green there.

Context from the spec: this PR preserves environment transcript identity across hub
restarts. It resets environment state on missing/header-only restore and after history
compaction even when marker persistence fails, and retains main's environment, hook and
diagnostic exclusions. Never blindly replay a write after a failure; uncertain mutation
state must be reconciled, not retried on faith.

RoboRev finding at head `83efe27` (verbatim; one of two reviewers raised it):

- **`agent/session.go:1630-1641`** — Ambiguous transcript-write failure can duplicate
  environment entries. If `AppendDurable` hits a sync/write error and its rollback
  *also* fails, the environment entry may remain in the transcript while the tracker is
  reset and retry proceeds. The retry then writes a second environment block with a new
  ID, producing duplicate model-visible and UI-visible context.
  **Suggested fix:** Treat rollback-failure errors as an indeterminate append outcome —
  reconcile the transcript by the generated stable ID (or establish durability) before
  deciding whether to retry, and only reset/retry after confirming the original entry
  is absent.

Requirements:
1. Verify the finding against `agent/session.go` and the `AppendDurable` rollback path.
   If the double-failure path really can leave the entry durable while the tracker
   resets, it is real.
2. If real: regression test RED first that injects a write failure whose rollback also
   fails (follow the existing fault-injection/scripted patterns in the `agent` tests;
   read `docs/developing-evener/testing.md` and `docs/developing-evener/agent-test-serial-prefix.md`
   first — the agent suite has a deadline audit that rejects bare wall-clock bounds),
   asserting that a subsequent turn does not produce a second environment block. Then
   the smallest fix: on rollback failure, do not reset the tracker / do not retry until
   the transcript has been reconciled by the stable ID.
3. If the finding is a misread, do not change behaviour; add the test that proves the
   double-failure path cannot duplicate, and explain with file:line evidence
   (DONE_WITH_CONCERNS).
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, and the focused new
   test with `-race`.
5. Single commit, e.g. `fix(agent): reconcile environment entry after failed rollback`.

## Task 7: PR #1105 local fork capability — round 2: capability/admission signal mismatches

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability` (PR https://github.com/prime-radiant-inc/evener/pull/1105)
Package: `cmd/evener-hub` (root module). Current head `8687b2737` (CI fully green; the
earlier state-directory finding is fixed there by `TestHubForkBranchesInAdmittedEntryStateDir`).

Context from the spec: the PR advertises fork capability for local sessions and enforces
ownership admission in the fork RPC. Contracts that must not change: stopped saved
delegates remain forkable (`TestHubUpgradeBlocksForkWritesUntilParentStop` and the
main-branch contract), live aliases remain blocked, remote capabilities preserved,
state-directory / subagent / recovery / restart-required fences retained. The finding
theme is that the capability projection (`applyHubForkCapability` /
`hubCanForkThread` in `cmd/evener-hub/app_threadread.go`) and the RPC admission
(`hubThreadFork` in `cmd/evener-hub/app_threadlifecycle.go`) use different predicates,
so the UI and the RPC can disagree.

RoboRev findings at head `8687b27` (verbatim):

## roborev: Combined Review (`8687b27`)

## Verdict: 3 medium and 1 low issue — all stem from capability/admission signal mismatches in the hub fork projection.

---

### Medium

- **Resume response reports fork unavailable until later refresh** — `cmd/evener-hub/app_threadlifecycle.go:487`, `cmd/evener-hub/app_threadlifecycle.go:718`
  A successful `thread/resume` projects `ForkFromTurn` while the recovery lock still has `ResumeRequired=true`. The deferred `ExplicitResumeCompleted` call clears that fence only after the response has already been built, so the resume response incorrectly reports fork as unavailable until a later read or status notification refreshes it. Fix: reapply `applyHubForkCapability` after `ExplicitResumeCompleted` succeeds, or otherwise delay capability projection until the recovery fence has been cleared.

- **Subagent predicate diverges between capability and admission** — `cmd/evener-hub/app_threadlifecycle.go` (`hubThreadFork`) vs `cmd/evener-hub/app_threadread.go` (`hubCanForkThread`)
  Fork admission gates subagents on `entry.Meta.IsSubagent`, while capability projection gates on `thread.Evener.Kind == "subagent"`. If persisted meta and wire kind diverge, capability and RPC disagree — a live alias can be advertised as unfforkable yet forked, or vice versa. Fix: use one predicate in both places, e.g. block on `cfg.Roster.IsSubagentActive(ref.ThreadID)` alone, or on `IsSubagentActive && (Meta.IsSubagent || Kind == "subagent")`.

- **Recovery-signal set mismatch between capability fence and admission enforcement** — `cmd/evener-hub/app_threadlifecycle.go` (`hubThreadFork`)
  Capability fences fork on `Evener.ResumeRequired`, `Status.RestartRequired`, and `ActiveFlags` containing `resumeRequired`, but admission only enforces `sessionActionRecoveryError` plus the restart-required probe and never inspects the resolved thread status flags. An RPC can therefore succeed where the UI says fork is unavailable. (Related to the resume-timing issue above, but this is a broader signal-set mismatch, not just a deferred-clear race.) Fix: resolve the target thread status in admission and reject when `hubForkRecoveryFenced` is true, or narrow the capability fence to only the signals admission actually enforces.

### Low

- **Ownership resolvability checked at admission but not in capability projection** — `cmd/evener-hub/app_threadread.go` (`applyHubForkCapability`)
  Capability advertises fork based on `cfg.StateDir` non-empty plus roster/recovery checks without verifying `ownershipEntry` succeeds, while `hubThreadFork` requires `ownershipEntry` ok. Ambiguous, unreadable, or missing ownership advertises fork but the fork RPC then returns unavailable. Fix: gate the advertised capability on the same ownership resolvability admission requires, or explicitly accept and document the advertise-then-reject mismatch.

---
*Reviewers: 2 done | Synthesis: codex, 18s | Total: 6m20s*


Requirements:
1. Verify each finding against the code. For each real one: regression test RED first
   that drives the real hub handlers (follow the fixtures in
   `cmd/evener-hub/app_fork_capabilities_test.go` and neighbours; read
   `docs/developing-evener/testing.md` first), then the smallest fix. Prefer ONE shared
   predicate/helper that both the capability projection and the admission path call, so
   they cannot diverge again, rather than patching each site separately.
2. Medium 1 (resume response): after `ExplicitResumeCompleted` succeeds, the resume
   response must project the same fork capability a subsequent read would.
3. Medium 2 (subagent predicate) and Medium 3 (recovery-signal set): make capability and
   admission agree on the same signals; do not loosen admission to match a permissive
   projection — when in doubt the RPC must be at least as strict as the advertised
   capability, and the advertised capability must not promise what the RPC refuses.
4. Low (ownership resolvability): either gate the advertised capability on the same
   ownership resolvability admission requires, or, if that is too expensive in the read
   path, document the advertise-then-reject mismatch in a code comment at the projection
   site and explain in the report why. State which you chose.
5. If a finding is a misread, do not change behaviour; add the test that proves the
   existing behaviour and explain with file:line evidence (DONE_WITH_CONCERNS).
6. Run `go test -count=1 ./cmd/evener-hub/...`, `go vet ./cmd/evener-hub/...`, and the
   new tests with `-race`.
7. One commit per finding, or one per closely related pair.

## Task 8: PR #1096 native checkpoint — round 2: failed command projection and stale roster rows

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR https://github.com/prime-radiant-inc/evener/pull/1096)
Current head `0709bf519` (CI fully green; the earlier open-task-count finding is fixed there).
Native app dir: `mobile-native/`; shared session core: `mobile/src/`. Deps are installed
(`mobile-native/node_modules`; if missing run `NODE_DISABLE_COMPILE_CACHE=1 npm ci --prefix mobile-native`
from the worktree root). Tests: `make test-native` from the worktree root (vitest, test:shared,
tsc). Focused shared-core tests: from `mobile-native`,
`npx vitest run --root .. --config mobile-native/vitest.config.mts <path under mobile/src>`;
focused native tests: from `mobile-native`, `npx vitest run <path under src>`.
Do not delete or weaken the keybinding v-flag assertion.

RoboRev findings at head `0709bf5` (verbatim):

### Medium
**Failed command executions projected as completed**
- `mobile/src/conversation/project.ts:105-115`, `mobile/src/state/conversation.ts:615-619`
- `toolCallFailed()` determines failure solely from a non-empty `item.error`. A settled
  command with a nonzero `exitCode` but no error text is projected as completed, so native
  activity status and failure-based clustering report failed commands as successful.
- **Fix:** Treat any defined nonzero `exitCode` as a failure in both projection paths, and
  add a behavioral test covering a nonzero exit code without an error message.

### Low
**Stale roster results retained on query change**
- `mobile-native/src/rosterSearch.ts:118-127`
- `load()` publishes the new normalized query before checking whether the query changed,
  so `normalized !== this.state.query` is always false. Searches retain rows from the
  previous query while the new request loads, and the UI can display and open stale results.
- **Fix:** Compare against the previous query before publishing the new state — clear rows
  when the query changes, preserve them for same-query refreshes.

Requirements:
1. Medium: confirm the wire shape first — where does `exitCode` live on the settled command
   item the shared core consumes (cite the generated protocol type or `appwire/protocol.go`
   / `appwire/types.go` line), and how does the web frontend classify a nonzero exit code
   (search `cmd/evener-hub/frontend/src` for the equivalent predicate) so native matches
   web. If the two projection paths can share one predicate, share it rather than
   duplicating the check. RED-first tests: nonzero exit code with no error text is failed
   in both paths; zero exit code with no error stays completed; error text with no exit
   code stays failed.
2. Low: RED-first test that a query change clears the previous rows while loading and a
   same-query refresh preserves them; then fix the ordering.
3. `make test-native` passes with pristine output (all three parts).
4. One commit per finding.

## Task 9: PR #1100 round timings — round 2: compaction ownership across overlapping mutations

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay` (PR https://github.com/prime-radiant-inc/evener/pull/1100)
Module: `agent/` (its own Go module; run `go` commands from inside it).
Current head `21b3c6415` (CI green except a known unrelated `race-root` flake in `cmd/evener`
`serve_state_test.go`; the earlier public-transcript finding is fixed by the head commit).

Design context from the spec (issue #1116, "Compaction/timing correction"): this PR captures
and persists logical-turn OWNERSHIP for round timing, context/summary compaction, steering
and hook completions. **An event that arrives after turn completion remains with its owner
and must not move the current turn's lifecycle.** Durable `TurnContextCompaction`
presentation records precede dependent checkpoint/summary/steering events. The public
regression `TestCompactionOwnerDuringOverlappingMutations` (three cases: later turn active;
prior turn finished/idle; PreCompact hook steering) drives a real Session through a
scripted provider and compares full and one-item live/cold payloads, keys, positions and
owners. Live and cold (durable) projections MUST agree on the owner of every compaction
item; which owner they agree on is a design choice already made (the owner at staging).

RoboRev finding at head `21b3c64` (verbatim):

- **`agent/session_compaction.go:403`** — Compaction ownership is captured when staging
  begins, but compaction can remain in flight while the active mutation changes. In the
  overlapping-mutation case, metadata and injected steering are published after mutation
  B becomes active but retain mutation A's owner, causing live and durable projections to
  attribute the compaction sequence to different logical turns. **Fix:** Resolve the owner
  at the publication boundary, or otherwise rebind the staged compaction markers, events,
  and steering records to the owner active when the fold is committed.

Requirements:
1. Decide, with evidence, whether the finding describes a real live/durable DISAGREEMENT
   or merely restates the intentional retain-the-staging-owner design. Read
   `agent/session_compaction.go` around the staging and publication boundaries and the
   overlapping-mutation regression. The question that matters: for a compaction whose
   staging began under mutation A and whose fold commits after mutation B is active, do the
   live notifications (metadata, injected steering, item-completed) and the durable
   records (`TurnContextCompaction`, steering records) carry the SAME owner? If every
   publication path reads the staged owner, they agree and the finding is a misread of the
   design; if any path resolves the owner from the current mutation at publish time, they
   disagree and the finding is real.
2. If real: RED-first regression that stages under A, activates B before the fold commits,
   and asserts live and cold owners are equal (and equal to the staged owner, per the
   design), then the smallest fix that makes every publication read the staged owner. Do
   not switch the design to "owner at commit" — that would move a prior turn's compaction
   into the current turn's lifecycle, which the spec forbids.
3. If a misread: do not change behaviour. If the existing regression does not already
   pin the "fold commits after B is active" ordering explicitly, add that case (or an
   assertion) so the property is proven rather than assumed, and explain with file:line
   evidence (DONE_WITH_CONCERNS).
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, and the focused
   compaction tests with `-race`. Read `docs/developing-evener/testing.md` and
   `docs/developing-evener/agent-test-serial-prefix.md` first; no bare wall-clock deadlines.
5. Single commit.

## Task 10: root-cause the intermittent `TestRunServeRetrySafeTurnPublishesControllableStableIdentity/stop` failure on main

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-serve`
Branch: `claude/fix-serve-stable-identity-flake` (based on origin/main `ff0ec6474`; new PR against main)
Package: `cmd/evener` (root module). Test: `cmd/evener/serve_state_test.go:335`
`TestRunServeRetrySafeTurnPublishesControllableStableIdentity`, subtest `stop`.

Why this matters: the failure is intermittent and has now broken required CI (`tests` and/or
`race-root`) on three unrelated mobile landing PRs (#1098, #1109 and #1100) and at least one
main push. It is a broken window that blocks the landing queue. Reruns pass; the handoff
records 20 local repeats and a focused private-cache run all passing, so it is timing- or
scheduling-dependent and shows up under CI's `-race` load.

Observed failure (CI run 34565291374, job race-root, `make test-race RACE_SCOPE=root`):

```
--- FAIL: TestRunServeRetrySafeTurnPublishesControllableStableIdentity (0.61s)
    --- FAIL: TestRunServeRetrySafeTurnPublishesControllableStableIdentity/stop (0.21s)
        serve_state_test.go:397: thread/read published no active turn while processing
```

Earlier occurrence: CI run 34520439714 job 103016314609 (on #1109's head) with the same
assertion. Full failed-job log of the latest occurrence is saved at
`/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-1100-race-root.log`.

Requirements:
1. Use the systematic-debugging discipline: form a hypothesis about the ordering the test
   assumes (the daemon publishing an active turn via `thread/read` while the scripted
   provider is still processing, before `stop` lands), find the code path that can violate
   it, and REPRODUCE it deterministically before changing anything. Useful levers:
   `go test -race -count=50 -run 'TestRunServeRetrySafeTurnPublishesControllableStableIdentity' ./cmd/evener/`
   under CPU pressure (e.g. `GOMAXPROCS=2`, or run alongside a busy loop), `-cpu 1,2,4`,
   and `GODEBUG` scheduler settings. Read `docs/developing-evener/testing.md`,
   `docs/developing-evener/agent-test-serial-prefix.md` and the test's own fixture helpers
   first. Record how many iterations it took to reproduce and under what settings.
2. Determine whether the defect is in the test (an assumption about ordering the daemon
   does not guarantee) or in the daemon (a real window where `thread/read` can observe a
   processing session with no active turn published). Report which, with file:line
   evidence. A daemon bug gets a fix plus a RED-first regression that fails
   deterministically without the fix; a test bug gets the test corrected to wait on the
   real signal it needs (no sleeps, no bare wall-clock deadlines, no loosened assertion).
3. Prove the fix: the reproduction settings that failed before must pass for at least
   200 iterations after (`-count=200`) under `-race`, and the package's ordinary
   `go test -count=1 ./cmd/evener/...` and `go vet` stay green.
4. Do not weaken the assertion at `serve_state_test.go:397` or mark the test flaky/skipped.
5. Commit on the branch (conventional message, e.g. `fix(serve): …` or `test(serve): …`),
   push it, and open a PR against main with `gh pr create --base main` (Jesse's explicit
   instruction for the flake lanes: "have a subagent fix each flake and pr it"). Do not merge.

## Task 11: root-cause the intermittent `TestServeWebSocketUnsubscribeWaitsThroughSubscriptionRegistration` failure on main

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-ws`
Branch: `claude/fix-ws-unsubscribe-registration-flake` (based on origin/main `ff0ec6474`; new PR against main)
Package: `internal/appserver` (root module). Test: `internal/appserver/websocket_dispatch_test.go:601`.

Why this matters: intermittent required-CI failure (`race-root`, `make test-race RACE_SCOPE=root`)
seen on two unrelated branches within a day (run 34541625455 on `fix/web-relative-file-links`
b430f67b4, and run 34529896590 on `codex/mobile-repair-handoff-20260910` 0d30ea35f); reruns pass.

Observed failure:

```
--- FAIL: TestServeWebSocketUnsubscribeWaitsThroughSubscriptionRegistration (0.01s)
    websocket_dispatch_test.go:663: unsubscribe left a stale registered subscription
```

Both failed-job excerpts (with the goroutine dump that precedes the FAIL line) are saved at
`/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-flake-ws-unsubscribe.log`.

Requirements:
1. Systematic-debugging discipline: read the test and the dispatch code it exercises (the
   subscribe/unsubscribe ordering across the WebSocket dispatcher's registration path),
   form a hypothesis for how an unsubscribe can complete while the subscription is still
   (or becomes) registered, and REPRODUCE it deterministically before changing anything.
   Levers: `go test -race -count=100 -run 'TestServeWebSocketUnsubscribeWaitsThroughSubscriptionRegistration' ./internal/appserver/`,
   `-cpu 1,2,4`, `GOMAXPROCS`, CPU load. Read `docs/developing-evener/testing.md` first.
   Record iterations and settings.
2. Determine test assumption vs server defect (a real window where unsubscribe returns
   before registration lands, leaving a stale subscription that would leak events to a
   closed client) with file:line evidence. Server defect: fix plus RED-first regression that
   fails deterministically without the fix. Test defect: wait on the real signal; no sleeps,
   no bare wall-clock deadlines, no loosened assertion, no skip.
3. Prove: failing settings pass ≥200 iterations under `-race` after the fix;
   `go test -count=1 ./internal/appserver/...` and `go vet ./internal/appserver/...` green;
   `gofmt -l` silent on touched files.
4. Commit with a conventional message stating root cause and reproduction evidence, push,
   and open a PR against main with `gh pr create --base main`. Do not merge.

## Task 12: root-cause the intermittent `TestProvidersListShowsInstancesCredentialSourcesAndStrayEntries` failure

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-providers`
Branch: `claude/fix-providers-list-flake` (based on origin/main `ff0ec6474`; new PR against main)
Package: `cmd/evener` (root module). Test: `cmd/evener/providers_test.go:115`, assertion at `:134`.

Why this matters: one observed intermittent required-CI failure (`race-root`, run 34524792182
on `codex/mobile-roster-loading-qualified` 290c6281e, a PR that did not touch providers and
later merged green). Lower confidence than the other flake lanes: reproduction is REQUIRED
before any change; if it will not reproduce after a serious attempt, report BLOCKED with the
evidence and your best hypothesis rather than guessing.

Observed failure (the printed table shows groq/ollama/work rows; the assertion text is
"list prints credential sources, never values (spec §11.2)"; the full excerpt is saved at
`/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-flake-providers.log`). Note the test name promises a "stray entries" row; check whether
the missing/extra content is the stray entry, ordering, or the user-layer path line, by
diffing the expected output in the test against the printed output in the log.

Requirements:
1. Systematic-debugging discipline: read the test and the `providers list` command path it
   drives; identify anything environment- or timing-dependent (temp dir layout under
   `/tmp/evener-module-tests.*`, `/var` vs `/private/var` normalization, directory
   listing order, parallel tests sharing an env var or HOME, map iteration order).
   Reproduce with `go test -race -count=200 -run 'TestProvidersListShowsInstancesCredentialSourcesAndStrayEntries' ./cmd/evener/`,
   `-cpu 1,2,4`, and with the package's other tests running in parallel (`-run 'TestProviders'`
   and the whole package) since the failure appeared in a full race run.
2. Fix the root cause (production ordering/determinism bug → fix + RED-first regression;
   test isolation bug → fix the isolation, never loosen the assertion).
3. Prove ≥200 clean iterations under the failing settings; package tests, vet and gofmt green.
4. Commit, push, and open a PR against main with `gh pr create --base main`. Do not merge.

## Task 13: make the web layout-guard browser startup robust to the intermittent 30 s startup deadline

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-layoutguard`
Branch: `claude/fix-layoutguard-browser-startup-flake` (based on origin/main `ff0ec6474`; new PR against main)
Area: `cmd/evener-hub/frontend/scripts/layoutguard/` (`run.mjs` and its browser launch), invoked
by `make test-web-browser` (`make/testing.mk:35`) in the required `web` CI job.

Why this matters: main push 91d1d6356 (run 34257184696, 2026-09-08) failed the required `web`
job with an environment-class error even though Chrome did start:

```
FAIL  web-layoutguard (exit 2)
browser guard startup failed (environment problem, not a test case failure): browser startup deadline exceeded after 30000ms
Chrome binary: /usr/bin/google-chrome
Chrome argv: --headless=new --disable-gpu --disable-crash-reporter --remote-debugging-port=0 --user-data-dir=... --no-first-run --disable-extensions about:blank
chrome stderr: [...] ERROR:dbus/bus.cc:405] Failed to connect to the bus: ...
DevTools listening on ws://127.0.0.1:44767/devtools/browser/7d93ae34-...
```

The full failed-job log is saved at `/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-flake-layoutguard.log`. The "DevTools listening"
line was printed, so the deadline elapsed AFTER Chrome exposed its endpoint: the readiness
detection or the first CDP connection is what stalled, not the binary.

Requirements:
1. Read `run.mjs` (and whatever it delegates browser launch/readiness to) and the harness docs
   under `docs/developing-evener/` (testing.md, the web-ui docs) before changing anything.
   Establish exactly how readiness is detected (stderr scrape for the DevTools line, port
   probe, CDP handshake, first target/page creation) and where a 30 s deadline applies.
2. Reproduce locally if possible (Chrome is on this Mac; simulate the stall by delaying or
   racing the readiness signal, e.g. a wrapper binary that delays stderr, or by starting the
   guard under CPU load). If the stall cannot be reproduced, reason from the log's ordering
   and say so explicitly.
3. Fix the root cause in the harness: for example make readiness wait on the CDP endpoint
   actually accepting a connection rather than on a stderr line, retry the initial CDP
   connect within the existing deadline, or launch once more if the first launch exposes an
   endpoint that never answers. Do NOT simply raise the deadline, and do NOT let a real
   test-case failure be retried or hidden. Keep the "environment problem, not a test case
   failure" classification accurate.
4. Prove with the harness's own tests (`*.test.mjs` next to the scripts) plus a passing local
   `make test-web-browser`; add a unit test for the readiness/retry logic where the code
   structure allows it.
5. Commit (`fix(web): …` or `test(web): …`), push, and open a PR against main with
   `gh pr create --base main`. Do not merge.

## Task 14: PR #1091 portable activity — round 2: continuation started during an in-flight root refresh

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity` (PR https://github.com/prime-radiant-inc/evener/pull/1091)
Current head `0aa507278` (CI fully green). Frontend dir `cmd/evener-hub/frontend`; focused tests
`npx vitest run <file>`; full gate `make test-web` from the worktree root.

Context: round 1 (Task 1) closed the direction "root refresh supersedes an active continuation"
by deferring root refreshes behind `continuationPending(ref)` and draining `pendingBump` after
the continuation settles. This finding is the opposite direction.

RoboRev finding at head `0aa5072` (verbatim):

- **Location**: `cmd/evener-hub/frontend/src/stores/activityPanel.ts:124-148`,
  `cmd/evener-hub/frontend/src/stores/activitySummary.ts:203-206`
- **Problem**: A continuation can start while a root refresh is in flight because `beginFetch`
  does not guard against `pending.kind === "root"`. The continuation replaces the panel request
  ID, causing the root response to be discarded. If the continuation succeeds,
  `publishContinuationCounts` then marks the newer root bump as fresh even though only the old
  tree plus one branch page was merged, leaving sibling activity stale until another bump.
- **Fix**: Serialize root and continuation requests by queuing or disabling continuations during
  root refreshes, or retain/reapply the superseded root refresh after the continuation and
  avoid marking the bump fresh until the full root snapshot is applied.

Requirements:
1. RED-first regression for exactly this path: root fetch in flight, user triggers "Load more"
   for a branch, root response arrives, continuation response arrives → the root snapshot must
   not be lost and `lastFetchedBump` must not be marked fresh for a bump whose full root
   snapshot was never applied.
2. Smallest fix consistent with round 1's design. Preferred: do not start a continuation while a
   root fetch is pending for that ref — either queue the continuation to run after the root
   settles (reusing the settle/drain pattern) or make the "Load more" trigger a no-op with the
   button disabled while the root is loading. State which and why. Do not discard either
   response silently; do not mark the bump fresh from a continuation result.
3. Re-run the store tests (`activitySummary*.test.ts`, `activityPanel*` tests, `ActivityPanel`
   component tests if they cover Load more), `npx biome check --write` on touched files, then
   `make test-web` once. Single commit unless the change is naturally two.

## Task 15: PR #1109 shutdown boundary — round 2: in-flight notification hooks not cancelled at shutdown start

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1109`
Branch: `codex/mobile-shutdown-boundary` (PR https://github.com/prime-radiant-inc/evener/pull/1109)
Current head `a0ddad58b` (CI fully green). Modules: `agent/` (own module) and `cmd/evener` (root).

Context: this PR gives cleanup Notification hooks the close context and skips them once it has
expired, bounds shutdown event delivery, and serializes identity replacement against the old
close/event drain. Round 1 (Task 5) fixed the deadline-audit CI failure, added the rendezvous
removal retry, the shared-lock close signal, and pinned the double-close and terminal-boundary
ordering. Preserve all of that.

RoboRev finding at head `a0ddad5` (verbatim; one of two reviewers raised it):

- **Notification hooks not cancelled on shutdown start**
  **Location:** `agent/session_events.go:475-486`, `cmd/evener/serve.go:1381-1384`
  A notification hook already running when shutdown begins retains `context.Background()`
  because `fireNotificationHook` only reads `closeCtx` when the hook starts. The serve shutdown
  path waits for `inputLoopDone` before calling `CloseForShutdown`, while notification hooks run
  synchronously on that loop, so a long-running hook can delay session cleanup and rendezvous
  removal beyond the intended shutdown budget.
  **Suggested fix:** Track active notification-hook contexts and cancel them as soon as shutdown
  starts, or propagate the turn/shutdown context into `fireNotificationHook` so in-flight hooks
  are interrupted before waiting for `inputLoopDone`.

Requirements:
1. Verify against the code: trace what context a hook that is already executing holds when
   `CloseForShutdown` / the serve shutdown path begins, and whether the serve path's wait on
   `inputLoopDone` can indeed block behind a synchronous hook. Cite file:line.
2. If real: RED-first deterministic regression (no bare wall-clock deadlines; use the sanctioned
   helpers/TRIPWIRE form per `agent/deadline_audit_test.go`) in which a notification hook that
   blocks until cancelled is running when shutdown starts, asserting the hook is interrupted and
   shutdown completes within the close budget. Then the smallest fix: derive the hook's context
   from something shutdown cancels (a session-level shutdown context, or the existing close
   context made available before the hook starts) so in-flight hooks observe cancellation.
   Do not make hooks asynchronous and do not change hook semantics outside shutdown.
3. If a misread (for example the hook already runs under a context shutdown cancels): pin the
   behaviour with the test the finding asks for and explain with file:line (DONE_WITH_CONCERNS).
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, the deadline audit, and the
   focused hook/lifecycle/serve tests with `-race`; from the root `go test -count=1 ./cmd/evener/...`.
5. Single commit.

## Task 16: PR #1105 local fork capability — round 3: relay-layer and roster-layer fencing gaps

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability` (PR https://github.com/prime-radiant-inc/evener/pull/1105)
Current head `4d71f9467` (CI fully green). Package: `cmd/evener-hub` (root module) and
`cmd/evener-hub/internal/hubcore`.

Context: rounds 1 and 2 (Tasks 4 and 7) fixed the fork state directory, the resume-response
projection, and unified the live-delegate predicate (`hubForkLiveDelegateFenced` →
`Roster.IsSubagentActive`) across capability and admission; the ownership-resolvability
mismatch is documented at the projection site. Preserve all of that. Contracts unchanged:
stopped saved delegates remain forkable, live aliases remain blocked, remote capabilities
preserved, the RPC at least as strict as the advertised capability.

RoboRev findings at head `4d71f94` (verbatim):

## roborev: Combined Review (`4d71f94`)

## PR Review Summary

**Verdict: 3 medium findings — fork capability recovery fencing has gaps across relay and roster layers.**

---

### Medium

- **`cmd/evener-hub/app_relay.go:246`** — `stampForkCapability` derives recovery fencing from only `params.Status`, ignoring a top-level `resumeRequired` field in relayed payloads. A notification can incorrectly advertise `forkFromTurn: true` while the daemon is explicitly requiring resume. **Fix**: Preserve and check the raw top-level `resumeRequired` field before enabling the fork capability.

- **`cmd/evener-hub/app_relay.go:616`** — `ownsFork` is derived from `target.thread`, the subscription-time snapshot. `applyHubForkCapability` fences on that snapshot's static recovery signals (`Evener.ResumeRequired`, `Status.Type == restartRequired`, `ActiveFlags resumeRequired`). After recovery clears, the snapshot stays fenced, so later idle/active notifications are still stamped `forkFromTurn=false` until resubscribe. **Fix**: Derive the allowance from live signals for the notification's thread instead of the stale status — compute `allowFork` from storage/roster/`ResumeLocks` only, or re-project with the notification's `Status` merged onto `target.thread`'s `Ref`.

- **`cmd/evener-hub/app_threadread.go:436-437` and `cmd/evener-hub/internal/hubcore/roster.go:650-658`** — The live-delegate fence relies on `Roster.IsSubagentActive`, but `SubagentState` scans `byPID` without excluding `LiveEntry.Crashed`. Crash-retained parent entries preserve their last `RunningSubagentIDs`, so a stopped persisted child can remain falsely marked daemon-owned and be rejected from forking until crash retention expires. **Fix**: Make subagent activity ignore crashed roster entries before applying the live-delegate fork fence.

---

### Summary

The change projects hub-owned fork capability and admission rules across reads, relays, lifecycle responses, and persisted local sessions. Two relay-layer gaps allow incorrect fork-capability stamping under recovery conditions, and one roster-layer gap causes false daemon-ownership of stopped children during crash retention.

---
*Reviewers: 2 done | Synthesis: codex, 14s | Total: 14m7s*


Requirements:
1. Verify each finding against the code with file:line. For each real one: RED-first
   regression through the real handlers/relay path (existing fixtures in
   `cmd/evener-hub/app_relay*_test.go`, `app_fork_capabilities_test.go`, and the hubcore
   roster tests), then the smallest fix.
2. Finding 1 (relay `stampForkCapability` ignores a top-level `resumeRequired`): confirm
   whether relayed payloads actually carry such a field (cite the producer) before treating
   it as real; if nothing produces it, that is a misread — pin and explain.
3. Finding 2 (relay `ownsFork` from the subscription-time snapshot stays fenced after
   recovery clears): if real, derive the fork allowance from the live recovery signals for the
   notification's thread rather than the snapshot; do not loosen the fence.
4. Finding 3 (roster `SubagentState` counts crash-retained parents): if real, make subagent
   activity ignore crashed roster entries so a stopped persisted child is not falsely
   daemon-owned during crash retention, with a hubcore test; check that no existing test
   depends on crashed parents keeping their children "active".
5. Run `go test -count=1 ./cmd/evener-hub/...`, `go vet ./cmd/evener-hub/...`, and the new
   tests with `-race`. One commit per finding.
6. Any finding you judge pre-existing behaviour outside this PR's change set: still fix it if
   it is small and provably correct; otherwise report it as out of scope with evidence so the
   coordinator can file it separately (DONE_WITH_CONCERNS).

## Task 17: PR #1098 environment replay — round 2: ambiguous-append live state and sequence reuse

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`
Branch: `codex/mobile-transcript-identity` (PR https://github.com/prime-radiant-inc/evener/pull/1098)
Current head `2a12149cd` (CI fully green). Module: `agent/` (own module; `agent/transcript` is a
package inside it).

Context: round 1 (Task 6) made a failed durable append whose rollback also fails return a
wrapped `transcript.ErrRollbackFailed`, and `environmentEntryAbsentAfterFailedWriteLocked`
reconciles by stable ID: it rewinds the tracker (so the next turn re-emits) only when the entry
is confirmed absent, and keeps the advanced tracker when the entry is present or the outcome is
unknown. The two findings below are consequences of the "entry present, tracker kept" branch.

RoboRev findings at head `2a12149` (verbatim):

## roborev: Combined Review (`2a12149`)

## Verdict: Two medium-severity state-consistency issues and one stale comment — recommend addressing the ambiguous-append recovery before merge.

### Critical
None.

### High
None.

### Medium

- **`agent/session.go:1633-1645`** — Ambiguous environment append leaves live state inconsistent. When reconciliation returns `false` after an ambiguous entry remains in the transcript, the advanced tracker is kept but the confirmed turn is never appended to `s.history`, `persistedAppendLog`, metadata, or the live `EventEnvironment` stream. A subsequent retry then omits the environment from the model and live projection even though cold restore sees it in the durable transcript. **Fix**: Treat a reconciled entry as committed — synchronize history, persistence bookkeeping, tracker metadata, and the live event (or otherwise ensure the next accepted input replays the durable context without creating a duplicate).

- **`agent/transcript/transcript.go:462-476`** — Sequence number reuse after failed rollback. If `Sync` fails and rollback fails after the entry remains in the file, `AppendDurable` returns before incrementing `w.seq`. Since the session avoids rewriting that entry, the next successful append reuses the same sequence number, violating the transcript's monotonic sequence invariant and producing duplicate `seq` values. **Fix**: Reconcile or rescan the writer's sequence state after confirming an ambiguous entry remains, ensuring subsequent appends use a sequence greater than every durable entry.

### Low

- **`agent/session.go:177`** — Stale comment reference. The comment references `setEnvContextState`, which was removed in this change, leaving a dangling pointer for readers checking `envContextState` locking. **Fix**: Update the comment to reference the current helpers (`appendEnvironmentContext` / `resetEnvContextTrackerLocked`).

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 14m38s*


Requirements:
1. Medium 1 (live state inconsistent when the ambiguous entry remains): verify with file:line
   what the success path appends after a durable environment write (`s.history`,
   `persistedAppendLog`, metadata, the live `EventEnvironment` emission) and confirm the
   reconciled-present path skips them. If real: RED-first regression in the existing
   `session_environment_rollback_regression_test.go` harness asserting that after an
   ambiguous append whose entry is confirmed present, (a) `ResumeHistory` / model history
   includes the environment turn, (b) the live event stream carried the `EventEnvironment`
   for it, and (c) a subsequent turn does not duplicate it; then the smallest fix: when
   reconciliation confirms the entry is present, complete the in-memory side effects the
   success path would have done (treat it as a success with a late confirmation), while an
   unknown outcome keeps today's conservative behaviour. State exactly which side effects you
   replay and why each is safe to run after the fact.
2. Medium 2 (sequence reuse after failed rollback): in `agent/transcript/transcript.go`, when
   `Sync` fails and rollback fails with the entry still in the file, advance the writer's
   sequence (and the failure observation, if the transcript's failure count is defined as
   "entries a later reader would see") so the next append does not reuse the seq. RED-first
   test in the transcript package: after such a double failure, the next successful append
   carries a strictly greater seq. Then check the session-side reconciliation still agrees
   (an unknown outcome must not advance twice on retry). Prior reviewers noted the comment at
   `transcript.go:476-478` describes the failure count as "a statement about the transcript";
   make the code and that comment agree.
3. Low: fix the stale `setEnvContextState` reference at `agent/session.go:177`.
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, the deadline audit, and the
   focused tests with `-race`. One commit per finding.

## Task 18: PR #1091 portable activity — round 3: retain loaded continuation pages across a bounded root refresh

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity` (PR https://github.com/prime-radiant-inc/evener/pull/1091)
Current head `5e576b28d` (merge of main 65093ee69 on top of `bd64695d1`; CI in progress). Frontend
dir `cmd/evener-hub/frontend`; focused tests `npx vitest run <file>`; full gate `make test-web`.

Context: the shared merge helpers in `src/protocol/activityMerge.ts` are consumed by the web
stores (`activityList.ts`, `activityPanel.ts`) and by the native checkpoint PR #1096, which carries
this code. Rounds 1 and 2 (Tasks 1 and 14) serialized root refreshes and continuations in both
directions. Task 14's review recorded, as pre-existing behaviour, that `fenceRootSession` maps over
incoming entries only, so entries that exist only in the previous page-merged tree are dropped on
every accepted root refresh. RoboRev has now raised that as a medium on #1096 (the same code):

- **`fenceRootSession` can drop already-loaded continuation entries on root refresh**
- **Location**: `cmd/evener-hub/frontend/src/protocol/activityMerge.ts:80-100` (used by
  `activityList.ts:146-151` and `activityPanel.ts:298-307`)
- **Problem**: `fenceRootSession` rebuilds the tree exclusively from `incoming.entries`. After a
  continuation page has been grafted, a subsequent root refresh can still be bounded and omit
  those previously loaded entries; the refresh then discards them and restores the earlier
  continuation state. Live activity updates can therefore make already-loaded activity disappear.
- **Fix**: Merge accepted root refreshes with retained entries and nested continuation coverage,
  applying newer incoming projections without dropping entries loaded from prior pages. Add a
  behavioral regression covering a grafted continuation followed by an accepted bounded root
  refresh.

Requirements:
1. First establish the backend's paging contract with file:line from `agent/jobs_activity.go`
   (and the AppWire types): in what order does a bounded root page list entries, what does the
   continuation token cover, and can an entry legitimately disappear from the root (pruned,
   superseded) so that retaining it would be wrong? State the retention rule you derive from
   that contract before writing code. A safe shape: when the incoming root is bounded (carries
   a continuation), entries loaded from prior pages that lie beyond the incoming window are
   retained and the continuation coverage is kept; when the incoming root is complete, or an
   entry's identity is present in the incoming page, the incoming projection wins. Do not retain
   entries the backend says are gone.
2. RED-first regression in `activityMerge.test.ts`: graft a continuation page, then apply an
   accepted bounded root refresh that omits those entries → they are retained with the newer
   incoming projections applied to overlapping entries, and the continuation state is not reset
   to the pre-page state. Add the complete-root counterpart (entries not in a complete root are
   dropped). Cover the nested (delegate child) continuation case if the helper handles it.
3. Re-examine the branch test re-pointed in Task 14 ("keeps a continuation merge when a late
   root refresh resolves after closing"): if this change makes its original assertions valid
   again through the retain-and-re-graft path, restore them; otherwise leave it.
4. `npx biome check --write` on touched files, then `make test-web` once. Single commit unless
   naturally two. Do not push.

## Task 19: PR #1091 portable activity — round 3b: RoboRev findings at 5e576b2 (do together with Task 18)

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity`. Current head `5e576b28d` (CI fully green).

RoboRev findings at head `5e576b2` (verbatim):

## roborev: Combined Review (`5e576b2`)

**Summary verdict:** Two medium-impact correctness gaps around activity failure predicates and continuation merging, plus three lower-severity consistency/freshness issues; no critical defects.

## Medium

- **Continuation requests are not serialized across branches** — `cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTree.tsx:491`, `cmd/evener-hub/frontend/src/stores/activityPanel.ts:164` (`beginContinuationFetch`)
  - Only the continuation button for the currently loading branch is disabled, but `beginContinuationFetch` lets a second continuation replace the single `pending` request. Clicking "Load more" on a second branch silently discards the first response.
  - Fix: reject `beginContinuationFetch` when any continuation is pending and disable *all* continuation buttons while `loadingContinuationID` is set.

- **Row/fold failure predicates diverge from badge counts** — `cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.ts` (`jobIsFailed`, `activityDelegateState`) vs `cmd/evener-hub/frontend/src/protocol/activityMerge.ts` (`summarizeSession`)
  - Row/fold uses broad `isActivityFailureOutcome` for both jobs and stable delegates, while the merged badge counts jobs/turns only on `outcome === "failure"` and stable delegates only on `"failed"`/`"exhausted"`. A terminal shell with `outcome: "failed"` or a stable delegate with `outcome: "failure"` renders failed in one place and completed in the other, breaking the single-number invariant.
  - Fix: narrow predicates to match the backend — terminal jobs/turns fail only on `"failure"`; terminal stable delegates fail only on `"failed"`/`"exhausted"`.

- **Descendant turn merge can drop unseen turns** — `cmd/evener-hub/frontend/src/protocol/activityMerge.ts` (`mergeDelegate` turn retention)
  - When a descendant page merges and the on-screen container already has turns, current turns unconditionally win. A later descendant cut may carry newer turns the client hasn't seen, which are discarded while `latestActivityAt` takes the max — pairing fresh status/timestamp with stale turns.
  - Fix: union turns by `jobId` instead of wholesale retain/replace, preserving unseen patch turns.

## Low

- **Failed glyph disagrees with outcome-based fold** — `cmd/evener-hub/frontend/src/panes/session/chrome/ActivityTree.tsx:415-417`
  - Terminal entries with `status` of `"failed"`, `"error"`, or `"exhausted"` but absent `outcome` are intentionally treated as non-failures by `jobIsFailed`, yet `statusState` still derives from the status and renders the failed glyph. The visual row contradicts the outcome-based fold and aggregate counts.
  - Fix: for non-live rows, derive the glyph solely from the terminal outcome (`failed` vs `ended`) instead of falling back to the displayed status.

- **Missing summary entry silently skips freshness invalidation** — `cmd/evener-hub/frontend/src/stores/activityPanel.ts` (`beginFetch` `summaryRequestID`)
  - A missing summary entry defaults to `0`, which never matches a real summary `requestID`, so a later `publishContinuationFailure` silently skips freshness invalidation.
  - Fix: capture the absence explicitly and skip publishing, or ensure the summary entry exists before starting a continuation.

---
*Reviewers: 2 done | Synthesis: codex, 22s | Total: 14m10s*


Requirements:
1. Medium "continuation requests not serialized across branches": RED-first regression (two
   branches, Load more on the second while the first page is pending → the first response is
   not discarded); fix per the finding: `beginContinuationFetch` refuses while any continuation
   is pending and every continuation control is disabled while `loadingContinuationID` is set.
2. Medium "row/fold failure predicates diverge from badge counts": this is the deferred Task 1
   minor. Narrow the predicates by kind to match `agent/jobs_activity.go` exactly: terminal
   jobs/turns fail only on `"failure"`; terminal stable delegates fail only on
   `"failed"`/`"exhausted"`. Share the per-kind predicates between `activityRows.ts` and
   `summarizeSession` so there is one definition per kind. RED-first with the two cross-vocabulary
   shapes the finding names.
3. Medium "descendant turn merge can drop unseen turns": union turns by `jobId` (incoming patch
   turns not present in the current container are appended in the patch's order; overlapping
   turns take the newer projection subject to the existing revision fence). RED-first. This
   interacts with Task 18's retention rule; design them together so a bounded root refresh and
   a descendant page use one merge policy.
4. Low findings: fix each that is a one-line-class change in code this PR owns (failed glyph
   derived from terminal outcome for non-live rows; freshness invalidation when the summary entry
   is missing; any others in the verbatim list). If one requires a design decision or touches
   code outside the PR, report it with evidence instead.
5. `npx biome check --write` on touched files; `make test-web` once. One commit per finding
   (Task 18's change may share a commit with item 3 if they are one policy).

## Task 20: PR #1100 round timings — round 3: direct `ProcessInput` turns and live/cold identity

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay`. Current head `e0fbcc673` (merge of main 65093ee69;
CI fully green). Modules: `agent/` (own module), `internal/appprojector`, `internal/apptranscript`
(root module).

Context: rounds 1 and 2 (Tasks 3 and 9) excluded context-replay copies from the public
transcript and pinned the staged compaction owner across a mutation boundary for
client-mutation turns. Both findings below concern the DIRECT `ProcessInput` path (a turn
started without a client-mutation record, so with no `StableTurnID` and no active turn owner),
where live and cold projections must still agree on turn identity and grouping. The PR's whole
purpose is live/cold coordinate parity, so these are in scope.

RoboRev findings at head `e0fbcc6` (verbatim; one of two reviewers):

## roborev: Combined Review (`e0fbcc6`)

## Summary Verdict

Two medium-severity turn-identity and compaction-ownership inconsistencies between live and cold projections need resolution.

## Findings

### Medium

- **Direct `ProcessInput` turns lack stable turn IDs, causing live/cold projection divergence** — `agent/session.go:1627`, `internal/appprojector/appwire_projection.go:273`
  Direct `ProcessInput` user turns have no `StableTurnID`, so cold projection falls back to their 1-based transcript entry index. The new environment entry receives a stable ID but does not advance the live projector's `nextTurn` counter, causing the first live user turn to use `turn_1` while the same cold-projected entry uses `turn_2`. Subsequent environment updates compound the divergence and can break live/reloaded item identity.
  *Fix:* Advance the fallback turn sequence for environment groups while preserving any reserved runnable ID, or assign a stable ID to direct user turns and use it consistently in live and cold projections.

- **Ownerless compaction records break cold turn grouping** — `agent/session_compaction.go:403`, `internal/appprojector/appwire_projection.go:1060`, `internal/apptranscript/logical_turn.go:60`
  `compactionOwner` is derived only from `activeTurnOwner()`, which is empty for direct `ProcessInput` turns. Live ownerless compaction events fall back to the projector's active turn, but cold grouping treats unowned `TurnContextCompaction` (and the newly persisted checkpoint/summary markers) as standalone groups, closing the user turn and assigning later assistant/timing items different turn IDs.
  *Fix:* Persist the actual direct logical-turn owner for compaction records, or make ownerless cold grouping follow the live projector's active-turn fallback semantics.

## Note

One reviewer found no issues. The findings above come from a single reviewer; the other reviewer assessed the change as clean.

---
*Reviewers: 2 done | Synthesis: codex, 9s | Total: 22m6s*


Requirements:
1. Establish with file:line which entry points still produce direct `ProcessInput` turns in
   production (CLI `run`, tests, hub?) and how the live projector numbers turns for them
   (`nextTurn` fallback) versus how cold projection numbers them (1-based entry index). Then
   reproduce each finding with a RED-first regression that compares full live and cold item
   identities for a direct-input session containing (a) an environment entry followed by a user
   turn, and (b) a compaction fold during a direct-input turn; the existing overlap/parity
   fixtures in `server/appwire_*_test.go` and the apptranscript/appprojector tests are the
   models. If a scenario cannot occur (for example direct input is test-only and the projector
   never sees environment entries there), prove it and report a misread with evidence.
2. Fix with one consistent rule for both findings: either give direct turns a stable identity
   that both projections use, or make the cold fallback follow the live projector's semantics
   for environment groups and ownerless compaction records. Prefer the option that does not
   change persisted transcript formats or the index version unless unavoidable; if the index
   version must change, say so explicitly. Do not regress the client-mutation path pinned by
   `TestCompactionOwnerDuringOverlappingMutations` and the Task 9 overlap case.
3. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, the deadline audit; from the
   root: `go test -count=1 ./internal/appprojector/... ./internal/apptranscript/... ./server/...`
   and `go vet` on those; focused new tests with `-race`. One commit per finding, or one if they
   are one rule.

## Task 21: root-cause the intermittent transcript scroll guard failure "pill is visible at mount"

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (based on current origin/main; new PR against main)
Area: the transcript scroll guard under `cmd/evener-hub/frontend/scripts/` (its runner) and its
entry `cmd/evener-hub/frontend/src/dev/transcriptscrollguard-entry.tsx`, part of `make test-web-browser`
(required `web` CI job).

Why this matters: discovered by the Task 13 lane while running `make test-web-browser` on
current main: the case `pill is visible at mount` fails intermittently as a real case failure
(exit 1), reproducing on unmodified `origin/main` about 1 run in 8 and 1 in 5 on the #1135 branch.
The Task 13 implementer's suspect: `src/dev/transcriptscrollguard-entry.tsx:307`
`waitForTranscriptSettled` reports settled while geometry is still moving (one failing base run
measured a scroll height of 17395px vs 17827px in a passing run). Treat that as a hypothesis to
verify, not a conclusion. Their full notes are in
`/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/mobile-app-integration-6d4885/.superpowers/sdd/2026-09-10-mobile-landing-queue/task-13-report.md`
(search for "pill is visible at mount").

Requirements:
1. Systematic-debugging discipline. Reproduce first: run the transcript scroll guard alone in a
   loop (find the runner's single-guard invocation; `make test-web-browser` runs all five) until
   the failure appears, capturing the guard's measurements on failure. Frontend deps: run
   `NODE_DISABLE_COMPILE_CACHE=1 npm ci` in `cmd/evener-hub/frontend` of your worktree first.
2. Root-cause with file:line: is the settle detector returning early (layout/fonts/images still
   changing), is the case's assertion racing the pill's own visibility logic, or is the fixture
   itself nondeterministic? A harness bug gets the harness fixed to wait on the real signal; a
   product bug (the pill genuinely not visible at mount under some layout timing) gets a fix plus
   a regression. No sleeps, no widened tolerances, no skipped or loosened assertion.
3. Prove ≥30 consecutive clean runs of the single guard under the settings that reproduced the
   failure, plus one full `make test-web-browser`, plus the harness's own `*.test.*` files and
   `make test-web` if you touched `src/`.
4. Commit (`fix(web): …` or `test(web): …`) with cause and evidence; push; open a PR against
   main with `gh pr create --base main`; do not merge.

## Task 22: PR #1109 shutdown boundary — round 3: authoritative event sends must observe shutdown

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1109`
Branch: `codex/mobile-shutdown-boundary`. Current head `a6e0fca53` (merge of main bf67c454f on
top of `1ab21ef58`; CI fully green). Modules: `agent/` (own module) and `cmd/evener` (root).

Context: Task 15 made notification hooks observe the session lifetime context (which `serve` now
supplies as its shutdown context via `LifetimeContext`), because hooks run synchronously on the
input loop and the serve shutdown path waits for `inputLoopDone` before `closeLiveSession`
(`cmd/evener/serve.go:1389-1392`), whose `CloseForShutdown` is the only publisher of `closeCtx`.
This round's finding is the same structural gap on the event-send path.

RoboRev finding at head `a6e0fca` (verbatim; High):

## roborev: Combined Review (`a6e0fca`)

## Summary Verdict

One High severity finding identified regarding event send context handling during shutdown.

---

### High

- **Location**: `agent/session_events.go:348,421-430`; `cmd/evener/serve.go:1389-1392`
- **Problem**: Normal event sends use `context.Background()` and only become cancellable after `Session.Close()` publishes `closeCtx`. During serve shutdown, the daemon waits for `inputLoopDone` before calling `closeLiveSession`; if a wedged authoritative bridge fills the event buffer while the input loop is emitting, that loop remains blocked holding `eventsMu.RLock`, shutdown waits forever, and the close deadline is never published.
- **Fix**: Make authoritative sends observe the session lifetime/shutdown context, or initiate session close before waiting for the input loop; add an end-to-end regression with a full buffer, wedged bridge, and shutdown.

---
*Reviewers: 2 done | Synthesis: codex, 6s | Total: 8m8s*


Requirements:
1. Verify with file:line: where a normal (authoritative) event send blocks on a full buffer
   (`agent/session_events.go` around the blocked-send path and `closeSignal`), which lock it
   holds while blocked (`eventsMu.RLock`?), whether it observes anything before `closeCtx` is
   published, and whether the serve shutdown goroutine's `<-inputLoopDone` wait precedes the
   only call that would publish `closeCtx`. If every link holds, the finding is real.
2. If real: RED-first deterministic end-to-end regression (no bare wall-clock deadlines; the
   sanctioned TRIPWIRE form only where a bound is unavoidable): a full event buffer, a wedged
   authoritative bridge, the input loop emitting, then serve shutdown begins → shutdown must
   complete within the close budget (the terminal boundary path from round 1 still applies).
   Then the smallest fix consistent with Task 15's mechanism: make the blocked authoritative send
   observe the session lifetime context (the same context hooks now observe) in addition to
   `closeCtx`, keeping close-budget precedence and the bounded-delivery semantics of round 1;
   OR, if that cannot be made safe, have serve initiate the session close before waiting for the
   input loop, with the identity-swap/drain-order fixes preserved. State which and why. Do not
   drop authoritative events on the healthy path; the change must only affect sends that are
   already blocked when shutdown starts.
3. If a misread (for example the send already selects on a lifetime-derived signal), pin with
   the test the finding asks for and explain (DONE_WITH_CONCERNS).
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, the deadline audit, focused
   `-race` on the events/lifecycle tests; from the root `go test -count=1 ./cmd/evener/...`,
   `go vet ./cmd/evener/...`, focused `-race` on the serve shutdown tests; and the pinned
   `golangci-lint run ./agent/... ./cmd/evener/...` (2.13.1 is installed; it enforces
   `modernize`). Single commit unless naturally two.

## Task 23: PR #1091 portable activity — round 4: retain a loaded child subtree across an incomplete refresh

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity`. Current head `f9d2c7cd1` (merge of main bf67c454f on top
of `4a7829e61`; CI green except the tail still running). Frontend dir `cmd/evener-hub/frontend`.

Context: round 3 (Tasks 18+19) established the retention rule "identity-keyed union, authority
follows coverage" for entries: a non-complete page says nothing past its cutoff, so prior-only
entries beyond the window are retained; a complete page's omissions are real. The internal review
of that round recorded, as a pre-existing gap on a different axis, that `fenceSession` drops the
whole loaded child subtree when the incoming delegate carries no `child` even though the branch is
not complete (depth truncation at `activityMaxNewDepth = 32`, and the child-unavailable /
link-mismatch error paths in `agent/jobs_activity.go:1056-1085`). RoboRev has now raised it:

## roborev: Combined Review (`f9d2c7c`)

**Verdict: One medium-severity issue found — a loaded child subtree can be erased during a bounded root refresh.**

## Medium

- **Location**: `cmd/evener-hub/frontend/src/protocol/activityMerge.ts:129-130`
- **Problem**: `fenceSession` drops the previously loaded `prior.delegate.child` whenever a refreshed delegate omits `entry.delegate.child`, even when the incoming branch is incomplete or depth-truncated. Bounded root activity responses intentionally omit depth-truncated children, so a later refresh can erase a child subtree loaded through continuation, along with its counts.
- **Fix**: Preserve and recursively fence the prior child when the incoming branch is incomplete and no replacement child is provided; only discard it when the incoming branch is complete. Add a regression covering a truncated refresh after loading a child continuation.

---

One reviewer flagged this regression risk; the other found no issues. The finding stands as the sole actionable item.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 10m42s*


Requirements:
1. Apply the same retention rule on the child axis: when the incoming delegate omits `child`
   and the incoming branch is NOT complete, keep the prior child subtree and fence it recursively
   (its own entries/child follow the same rule, counts recomputed bottom-up); when the incoming
   branch is complete, or the incoming delegate carries a replacement `child`, the incoming wins.
   Cite the backend shapes that omit `child` (`agent/jobs_activity.go:1056-1085`) so the rule is
   grounded, and confirm whether the error-path omissions (child unavailable, link mismatch) should
   also retain or should drop — decide from what the backend says about the branch's completeness
   in those shapes and state it.
2. RED-first regression in `activityMerge.test.ts`: load a child continuation, then apply a
   bounded/depth-truncated root refresh whose delegate omits `child` → the loaded child subtree
   survives with counts intact; and the complete counterpart drops it. Nested case if applicable.
3. `npx biome check --write` on touched files; `make test-web` once; single commit.

## Task 24: PR #1105 local fork capability — round 4: stable-ref routing and fence-before-refresh ordering

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `9e9e0efe7` (CI fully green).
Package: `cmd/evener-hub` (root module).

Context: rounds 1-3 (Tasks 4, 7, 16) fixed the fork state directory, the resume-response
projection, unified the live-delegate predicate (`hubForkLiveDelegateFenced` → `Roster.IsSubagentActive`,
now crash-aware in roster and sidebar), and documented the advertise-then-reject ownership
mismatch. Contracts unchanged: stopped saved delegates forkable, live aliases blocked, remote
capabilities preserved, RPC at least as strict as the capability.

RoboRev findings at head `9e9e0ef` (verbatim; one of two reviewers):

## roborev: Combined Review (`9e9e0ef`)

**Summary:** Two reviewers split on this change — one flagged medium-severity session routing and roster-staleness issues in fork lifecycle fencing, the other found no issues.

## Review Findings

### Medium

- **`cmd/evener-hub/app_threadlifecycle.go:800-863`** — Stable workspace refs can route forks to retired sessions. `fork` admission and mutation use `ref.ThreadID` directly. Live daemon threads can expose a stable `WorkspaceRef` while their current `SessionID` changes after `thread/clear`; the capability projection advertises fork for that stable ref, but the mutation then looks up and forks the retired session metadata instead of the current session. **Fix:** Resolve stable workspace refs to the live daemon's current session ID before calling `ownershipEntry`, `hubAsideSession`, or `hubForkSession*`, while retaining the stable ref for recovery/deletion fencing.

- **`cmd/evener-hub/app_threadlifecycle.go:807-856`** — Stale roster can permit forks of live delegates. `hubForkLiveDelegateFenced` is checked before `refreshDaemonRestartRequiredError` refreshes the roster. If the roster is stale, or a child becomes live before/during that refresh, the refresh only checks protocol compatibility and the fork proceeds despite the delegate still being daemon-owned. **Fix:** Refresh and capture the ownership snapshot before the live-delegate admission check, then revalidate ownership immediately before the fork mutation under an admission lock that prevents the parent daemon's ownership state from changing concurrently.

## Reviewer Consensus

- **Reviewer 1** identified the two medium findings above.
- **Reviewer 2** found no issues, stating hub fork authority is consistently projected and enforced across reads, lists, resumes, relays, and crash-retained roster state.

The two medium findings are not contradicted by Review 2's clean verdict — that review focused on authority projection and enforcement consistency, whereas the flagged issues concern session-routing precision and roster-staleness race conditions in the fork lifecycle path specifically.

---
*Reviewers: 2 done | Synthesis: codex, 12s | Total: 10m41s*


Requirements:
1. Finding 1 (stable workspace ref routes to a retired session): verify with file:line how a
   fork request's `ref` is resolved in `hubThreadFork` versus how the capability projection and
   the read path resolve a stable `WorkspaceRef` to the current `SessionID` after `thread/clear`
   (there is an existing resolution helper on the read path; find it). If the fork path really
   uses `ref.ThreadID` for a ref that is a stable workspace ref, RED-first regression: clear a
   live thread so its session id changes, fork by the stable ref → the fork must branch from the
   CURRENT session (assert the child's parent id), then fix by resolving the ref through the same
   helper the read path uses before admission and mutation. If stable refs cannot reach the fork
   handler unresolved, pin and explain (misread).
2. Finding 2 (fence checked before the roster refresh): verify the ordering in `hubThreadFork`
   (`hubForkLiveDelegateFenced` at ~807 vs `refreshDaemonRestartRequiredError` at ~820/856). If
   real, RED-first regression with a roster that is stale at admission time (child becomes live
   between the last refresh and the fork) → the fork must be refused; then fix by refreshing and
   capturing the ownership snapshot BEFORE the live-delegate check, and evaluating the fence on
   that snapshot. Do not loosen the fence; do not double-refresh on the healthy path if one
   refresh can serve both checks.
3. Run `go test -count=1 ./cmd/evener-hub/...`, `go vet ./cmd/evener-hub/...`, focused `-race`,
   and the pinned `golangci-lint run ./cmd/evener-hub/...`. One commit per finding.

## Task 25: PR #1091 portable activity — round 5: off-target container state, deferred-queue eviction, updater purity

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity`. Current head `a756cff96` (merge of main 35eb09465 on top
of `a2ee9b033`; CI fully green). Frontend dir `cmd/evener-hub/frontend`.

Context: rounds 1-4 established serialization of root refreshes and continuations in both
directions and across branches, per-kind failure predicates, and the retention rule
"identity-keyed union, authority follows coverage" on the entry, turn and child axes.

RoboRev findings at head `a756cff` (verbatim; one of two reviewers):

## roborev: Combined Review (`a756cff`)

## Summary Verdict: 3 issues found (2 Medium, 1 Low) — no critical or high-severity issues.

---

### Medium

- **`cmd/evener-hub/frontend/src/protocol/activityMerge.ts`** (`revisionFencedDelegate`, `mergeDelegate`, `fenceSession`) — Turn containers have no projection revision, so `revisionFencedDelegate` unconditionally clones `patch`. For an off-target descendant page this replaces the container's `status`/`outcome`/`terminal` with the stale snapshot from when the page was cut; only `turns` and `latestActivityAt` are repaired. `fenceSession` has the same gap: it clones the incoming delegate without retaining prior `turns`, so a bounded refresh can drop turns previously loaded by a continuation.
  - **Fix**: Base off-target turn-container state on `current` and only union unseen `patch` turns, and make `fenceSession` retain/union prior turns when the incoming branch is bounded.

- **`cmd/evener-hub/frontend/src/stores/activitySummary.ts`** (`refreshRoot` queue path) — When deferred behind a continuation, queuing bails with `if (!entry ...) return state`. If the summary entry was evicted while the panel still holds a continuation pending, the bump is dropped with no `pendingBump` and freshness is lost until the next bump.
  - **Fix**: Create the entry when deferred instead of returning early, preserving the queued `fetch`/`onFailure` with newest-bump-wins semantics.

### Low

- **`cmd/evener-hub/frontend/src/stores/activityPanel.ts`** (`publishFetch`) — `publishContinuationFailure` and `publishContinuationCounts` are called inside the `set((state) => ...)` updater, making the updater impure and coupling panel commit ordering to nested summary-store updates.
  - **Fix**: Compute the continuation outcome inside the updater, then publish to `activitySummaryStore` after the panel `set` completes.

---

**Note:** One reviewer found no issues; the findings above are from the second reviewer. The change overall centralizes activity protocol parsing with turn-aware counting and fenced refresh/continuation coordination.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 9m12s*


Requirements:
1. Medium 1: for an off-target turn container (and the equivalent path in `fenceSession` when the
   incoming branch is bounded), the container's `status`/`outcome`/`terminal` (and any other
   projection fields) must come from `current`, with only unseen `patch` turns unioned in and
   `latestActivityAt` taking the max — the same "authority follows targeting/coverage" rule the
   turns already follow. Cite which fields a turn container carries (`appwire/types.go`) so the
   list is complete. RED-first: an off-target descendant page whose snapshot is older than the
   on-screen container must not regress the container's status/outcome/terminal.
2. Medium 2: in `refreshRoot`'s deferred path, when the summary entry is missing while the panel
   still has a continuation pending, create the entry and queue the bump (newest-bump-wins,
   preserving `fetch`/`onFailure`) instead of returning early. RED-first through the store. Check
   the eviction pass (`panelStoreEviction.ts`) to state whether the shape is reachable in
   production; fix regardless if the store contract allows it, and say which.
3. Low: move `publishContinuationFailure`/`publishContinuationCounts` out of the `set((state)
   => …)` updater — compute the outcome inside, publish to the summary store after the panel
   `set` completes — without changing the ordering guarantees the earlier rounds rely on
   (the badge must still be published from the fenced tree, and the deferred-bump drain must
   still run after the continuation merges). Existing tests must pass unchanged.
4. `npx biome check --write` on touched files; `make test-web` once; one commit per finding.

## Task 26: PR #1098 environment replay — round 3: unknown-outcome model context, writer poisoning, test error contract

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`
Branch: `codex/mobile-transcript-identity`. Current head `a949ff1f7` (merge of main 35eb09465 on top
of `ce6ffc088`; CI fully green). Module: `agent/` (own module; `agent/transcript` inside it).

Context: rounds 1-2 (Tasks 6, 17) made a failed durable append whose rollback also fails
reconcile by stable ID: confirmed present → in-memory side effects completed and tracker kept;
confirmed absent → tracker rewound and the next turn re-emits; unknown (durability or read
failed) → tracker kept and nothing claimed. The writer spends the seq only for a retained
entry. Two hazards were recorded as deferred: the unknown branch leaves a tracker advanced past
what the model saw; and a failed WRITE whose rollback fails leaves a partial line the next
append concatenates onto. RoboRev has now raised both, plus a test-contract gap:

## roborev: Combined Review (`a949ff1`)

## Code Review Summary

Two medium-severity durability bugs and one test gap found — no critical or high issues.

---

## Medium

- **`agent/session.go:1660`** — `environmentEntryUnknown` leaves `envTracker` advanced even though the environment turn was never added to `s.history` or the model context. A subsequent retry with the same environment emits no block, and a later change emits only a diff against a state the model never saw, silently omitting environment context. Fix: retain and reconcile the unresolved turn before accepting another input, or reset the tracker to a known durable baseline and force a complete re-emission rather than continuing with an uncommitted state.

- **`agent/transcript/transcript.go:507-516`** — When a durable write fails after writing part of a line and rollback also fails, `appendFailureLocked` returns `ErrRollbackFailed` but leaves the writer usable. A later append is written after the partial tail, producing a malformed JSONL record that makes transcript reads fail; if the entire line was written before returning the error, the writer also fails to advance its sequence and failure counters. Fix: poison the writer after an unsuccessful rollback and require reopening/reconciling the transcript before further appends, or repair the tail and update bookkeeping based on the records actually retained.

## Low

- **`agent/session_envctx_test.go:173`, `:198`, `:233`, `:495`** — `maybeAppendEnvironmentContext` now returns `error` but these calls discard it, so the transcript-failure test never asserts the failure-mode contract and the race hammer silently drops results. Fix: assert non-nil error on the injected failure and nil error on retry/success; use explicit `_ =` only for the concurrent hammer where results are intentionally ignored.

---

**Overall:** The change adds durable environment-context replay, failure reconciliation, compaction handling, and live AppWire projection. The main risk is unresolved append failures that can desynchronize model context or corrupt subsequent transcript writes; the test gap means the new error contract isn't being exercised.

---
*Reviewers: 2 done | Synthesis: codex, 10s | Total: 18m1s*


Requirements:
1. Medium 1 (unknown outcome): choose and implement one of the finding's two options with a
   stated reason: (a) retain the unresolved turn and reconcile it (re-run
   `environmentEntryAbsentAfterFailedWriteLocked`) before accepting the next input, committing
   or rewinding once the outcome is known; or (b) on unknown, reset the tracker to the last
   durable baseline and force a complete environment re-emission on the next turn, accepting a
   possible duplicate block over a silently missing one. Prefer the option that never lets the
   model run with environment context it did not see; state the trade explicitly. RED-first:
   unknown outcome, then a subsequent turn → the model context must contain the environment
   (either the reconciled entry or a complete re-emission), never a diff against unseen state.
2. Medium 2 (writer poisoning): after a failed write whose rollback also fails (partial line
   possibly in the file), the writer must refuse further appends with a distinct error (poisoned)
   so a later append cannot produce a malformed record; if the entire line was written before the
   error, spend seq/failure counters as the retained-entry path does. RED-first in the transcript
   package: partial-line write failure + rollback failure → next append refused; and the
   whole-line variant advances seq. Make the session-side handling of a poisoned writer explicit
   (what the session does when its transcript writer is poisoned — surface as the session's
   transcript failure path, do not silently continue).
3. Low: in `agent/session_envctx_test.go:173, :198, :233, :495`, assert the returned error
   (non-nil on the injected failure, nil on retry/success); keep `_ =` only where results are
   intentionally ignored in the concurrent hammer, with a comment.
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, deadline audit, focused
   `-race` on the transcript and environment tests, pinned `golangci-lint run ./...`. One commit
   per finding.

## Task 27: PR #1137 transcript bottom-hold — round 2: close the coalesced scroll-up window

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `448e8a489`
(comment tidy + merge of main 35eb09465; CI fully green). Frontend dir `cmd/evener-hub/frontend`.

Context: round 1 (Task 21) fixed the mount under-follow by classifying a scroll event as a
measurement correction when the transcript was at the bottom, the port is unchanged, content
grew, and `scrollTop` did not decrease, then re-pinning. The internal review recorded one narrow
misfire: a reader's upward gesture coalesced in the same frame with a larger forward correction
nets `scrollTop >= previous` and gets pinned for one frame. RoboRev now raises it:

## roborev: Combined Review (`448e8a4`)

**Verdict: One medium finding — a scroll re-pin race condition that can yank a reader who scrolled up.**

---

## Medium

- **`cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:1069`** — The handler infers that a scroll event is virtualizer correction whenever `scrollHeight` grows and `scrollTop` has not decreased. A native scroll event can coalesce a reader's upward gesture with a late measurement correction; if the correction exceeds the gesture delta, this condition forcibly pins the reader back to the bottom and leaves the pill hidden. The code's own comment acknowledges this race, violating the transcript's "never yank a reader who scrolled up" behavior.
  - **Fix**: Track virtualizer/programmatic correction state or explicit user-scroll intent instead of inferring the event source solely from net geometry, and add a browser-level regression covering an upward gesture during late measurement.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 3m50s*


Requirements:
1. Replace net-geometry inference with an explicit signal, as the finding asks. Two candidate
   designs; pick one with a stated reason: (a) user-intent tracking — record a pending user
   gesture from `wheel`, `touchmove`/`pointerdown`+move, and scroll-key `keydown` on the scroll
   port (passive listeners), and never classify a scroll event as a correction while a gesture is
   pending in the same frame; or (b) correction-intent tracking — observe content growth from the
   virtualizer's measurement path (`VirtualList` `onChange`/measurement seam or a
   `ResizeObserver` on the inner element) and only classify as a correction an event that follows
   an observed growth within the same frame. (a) protects the reader; (b) identifies the
   correction; either must keep the round-1 fix working for the mount case and must never yank a
   reader who gestured. Do not widen thresholds.
2. Unit regression RED-first through the real hook: an upward gesture coalesced with a larger
   forward correction in one event must leave the reader where they scrolled (pill visible),
   while the pure-correction event still re-pins.
3. Browser-level regression in the transcript scroll guard (a new case driven through CDP
   input): dispatch an upward wheel gesture while a late measurement grows the transcript, assert
   the reader is not returned to the bottom; plus keep the existing pill-at-mount case green.
   Prove with ≥20 consecutive clean single-guard runs of both cases and one full
   `make test-web-browser`; `make test-web`; biome.
4. If the browser case cannot be made deterministic (measurement timing not controllable from
   the harness), say so with evidence and pin the behaviour at the unit level only; do not add a
   flaky case.
5. Commit (conventional, cause + evidence), fetch/merge main if it moved, push, append the
   report, do not change PR state.

## Task 28: PR #1137 transcript bottom-hold — round 3: gesture-veto edge cases

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `f9985829f`
(CI green so far). Frontend dir `cmd/evener-hub/frontend`.

Context: round 2 (Task 27) added user-intent tracking — passive `wheel`/`touchmove`/scroll-key
`keydown`/pointer-drag listeners on the scroll port set a pending-gesture flag, cleared on the
next rAF, that vetoes the round-1 re-pin. RoboRev's review of that head found real edge cases:

## roborev: Combined Review (`f998582`)

**Verdict: Code is functional but the gesture-veto input tracking and its tests have real edge cases and false positives that should be addressed before merge.**

## Critical
None.

## High
None.

## Medium

- **Keyboard gesture detection misses existing `window`-level scroll bindings**
  `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:1183`; `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScrollKeys.ts:60-85`
  The gesture listener is attached only to the VirtualList scroll element, but `useTranscriptScrollKeys` dispatches from `window` and directly mutates `scrollTop`. When focus is on the transcript wrapper or another non-editable element, the keydown never reaches this listener, so an upward keyboard scroll can be coalesced with a larger virtualizer correction and be incorrectly re-pinned to the bottom.
  *Fix:* Integrate the gesture marker with `useTranscriptScrollKeys` before its direct scroll writes, or add a correctly scoped window/document-level notification for transcript scroll actions.

- **Keydown false positives for non-scroll targets**
  `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:1061-1063`
  Every descendant `keydown` for `" "`, `Home`, `End`, or an arrow/page key is treated as a transcript scroll gesture without checking the target or whether the default action actually scrolls the transcript. For example, pressing Space on a focused transcript button activates that button rather than scrolling; during a late measurement correction this vetoes the re-pin and can leave a reader at the bottom with a false jump pill.
  *Fix:* Restrict this path to events whose target/default behavior can scroll the transcript, and route custom transcript actions through an explicit gesture signal instead of inferring from key names alone.

- **Veto tests decouple fake `measure` from `el.scrollTop`, overstating restoration**
  `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.test.ts:599`, `:635`
  The coalesced-gesture and parameterized veto tests leave `el.scrollTop` at 16374 while the fake `measure` reports 16432, then assert `el.scrollTop` stays at 16374. In production, `measure` reads the live DOM, so the DOM is already at the net 16432 when `handleScroll` runs and the veto path performs no restoration — the reader stays at the net position, not the pre-event position. The tests pass only because the injectable seam ignores the element and do not exercise the real DOM contract.
  *Fix:* Keep the fake and DOM in sync to mirror the browser (set both to the net geometry before dispatch) and assert no re-pin to bottom plus pill offer, not exact restoration to the pre-event offset.

## Low

- **`pointerDragging` can get permanently stuck true**
  `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:1064-1071`, `:1184-1187`
  `pointerDragging` is set on `pointerdown` on `el` but cleared only by `pointerup`/`pointercancel` on `el`. A mouse drag that starts inside the transcript and ends outside misses the release, leaving the flag permanently set; later ordinary pointer movement over the transcript is then classified as a gesture and repeatedly suppresses legitimate bottom corrections.
  *Fix:* Use pointer capture on `pointerdown`, or listen for matching `pointerup`/`pointercancel` at `window`/`document` (tracking pointer ID/buttons), or clear on `pointerleave`.

- **`gesturePending` rAF clear stalls in background tabs**
  `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:1053`
  `gesturePending` clears only via `requestAnimationFrame`, which pauses in hidden/background tabs, extending the same-frame veto indefinitely and letting a stale gesture suppress a later correction.
  *Fix:* Add a timeout fallback or consume-and-clear the veto on the next handled scroll event.

---
*Reviewers: 2 done | Synthesis: codex, 17s | Total: 5m2s*


Requirements:
1. Medium 1: `useTranscriptScrollKeys` (`useTranscriptScrollKeys.ts:60-85`) dispatches from
   `window` and writes `scrollTop` directly, bypassing the port-level listener. Integrate the
   gesture marker at the source: expose a `markGesture()` (or equivalent) from the scroll hook
   and call it in `useTranscriptScrollKeys` immediately before each direct scroll write, instead
   of listening for keys at the port. RED-first: a window-dispatched transcript scroll key during
   a late measurement must not re-pin.
2. Medium 2: remove the port-level keydown inference (space/Home/End/arrows/page keys on any
   descendant), since it produces false positives for non-scroll targets (a focused button,
   an editable field); route transcript scroll actions through the explicit marker from item 1
   only. RED-first: Space on a focused descendant control must not veto a legitimate correction.
3. Medium 3: the veto tests decouple the fake `measure` from `el.scrollTop`. Make the fixture
   mirror the browser (set both to the net geometry before dispatch) and assert "no re-pin to
   the bottom, pill offered" rather than exact restoration to the pre-event offset.
4. Lows: `pointerDragging` cleared only by `pointerup`/`pointercancel` on the port can stick
   when a drag ends outside — use pointer capture on `pointerdown` or listen at
   `window`/`document`; `gesturePending`'s rAF clear stalls in background tabs — add a
   consume-and-clear on the next handled scroll event (preferred) or a timeout fallback.
5. `npx biome check --write`; `make test-web`; the transcript scroll guard ≥20 consecutive clean
   runs and one full `make test-web-browser`. One commit per finding or per closely related
   pair. Fetch/merge main if it moved, push, do not change PR state.

## Task 29: PR #1105 local fork capability — round 5: activeFlags fence parity and validation ordering

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `1c73cc873` (CI fully green).

Context: round 2 (Task 7) established with evidence, confirmed by two reviewers, that
`Status.ActiveFlags` containing `"resumeRequired"` has NO producer anywhere in the repo (the only
references are the consumer in `app_threadread.go`, the field in `appwire/types.go`, and the
clone). The projection still fences fork on it; admission never has. Round 4 (Task 24) hoisted
the roster refresh above ownership discovery, which moved parameter validation after them (the
deferred minor). RoboRev now raises both:

## roborev: Combined Review (`1c73cc8`)

## Verdict: 2 findings (1 Medium, 1 Low) — hub fork capability projection and admission fencing has an unenforced fence and a validation-ordering issue.

### Medium

- **`cmd/evener-hub/app_threadlifecycle.go:806-820`** — `applyHubForkCapability` disables fork when a live thread reports `status.activeFlags` containing `"resumeRequired"`, but `hubThreadFork` never checks that predicate. It only checks resume locks and incompatible daemon protocol state, so a live daemon can advertise a recovery-fenced status while the fork RPC still branches its persisted session.
  - **Fix**: Resolve the admitted live thread status during fork admission and apply the same recovery-fence predicate used by capability projection, or remove this fence from projection until the RPC can enforce it.

### Low

- **`cmd/evener-hub/app_threadlifecycle.go:801-825` and `:849-857`** — Fork parameter validation now occurs after roster refresh and ownership discovery. Malformed requests for an unknown or unreadable local target return `CodeUnavailable` (and may scan all project directories) instead of the expected `CodeInvalidParams`.
  - **Fix**: Validate aside-specific fields, `sourceTurnId`, and `editedInput`/`deferInput` combinations before ownership discovery and daemon refresh, while retaining recovery and mutation fences before branching.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 12m13s*


Requirements:
1. Medium: make capability and admission agree on the `activeFlags` signal. Two options; pick
   with a stated reason: (a) enforce in admission — resolve the admitted live thread's status
   during fork admission (reuse whatever status the handler already has in hand or the roster
   snapshot; do not add a second daemon round-trip if one is avoidable) and apply the same
   predicate `applyHubForkCapability` uses, RED-first with a status carrying the flag → fork
   refused; or (b) remove the fence from projection since nothing produces the signal. Note
   constraint 6: the pre-existing `TestHubForkCapabilityKeepsDaemonPermissionsAndUnknownFields`
   asserts the projection fence for `activeFlags:["resumeRequired"]`; option (b) would re-point
   that expectation, so it is only acceptable if that test's assertion is demonstrably pinning a
   dead signal and you say so explicitly in the report — otherwise take (a). Either way, extend
   `TestHubForkAdmissionRefusesEveryProjectedRecoveryFence` so its name is true: every signal the
   projection fences on is exercised through admission.
2. Low: move aside-specific field validation, `sourceTurnId`, and `editedInput`/`deferInput`
   combination checks ahead of the roster refresh and ownership discovery, keeping every
   recovery and mutation fence before branching. RED-first: a malformed fork against an unknown
   local target returns `CodeInvalidParams` (not `CodeUnavailable`) and triggers no project scan.
3. `go test -count=1 ./cmd/evener-hub/...`, `go vet`, focused `-race`, pinned
   `golangci-lint run ./cmd/evener-hub/...`. One commit per finding.

## Task 30: PR #1091 portable activity — round 6: omitted delegate `type` must mean stable delegate

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity`. Current head `b0d96f946` (CI green; behind main
`e812703fc` after #1109 merged — merge main before pushing). Frontend dir `cmd/evener-hub/frontend`.

RoboRev finding at head `b0d96f9` (verbatim):

## roborev: Combined Review (`b0d96f9`)

## Verdict: One medium-severity issue found — optional `type` field mishandled in delegate classification.

### Medium

- **`ActivityDelegate.type` omission misclassified as turn container**
  - **Files**: `cmd/evener-hub/frontend/src/protocol/activityData.ts:583-587`, `cmd/evener-hub/frontend/src/panes/session/chrome/activityRows.ts:82-92`, `cmd/evener-hub/frontend/src/protocol/activityMerge.ts:80-83`
  - **Problem**: `ActivityDelegate.type` is optional on the wire, but the new logic treats every value other than the literal `"delegate"` — including an omitted `type` — as a turn container. A stable delegate payload without `type` therefore ignores its `terminal` state, contributes no count when `turns` is absent, and can disappear from active/failure summaries after a refresh or continuation merge.
  - **Fix**: Centralize delegate-kind detection and treat an omitted `type` as the stable-delegate form (or only classify explicitly recognized turn-container types), then use that predicate consistently for activity state, merging, and summary counting.

### Low

None reported.

### Summary

The change centralizes activity protocol data and adds bounded refresh/continuation merging with turn-aware states and serialized root versus continuation fetching. The only concern is the delegate classification logic mishandling valid delegates with an omitted `type` field.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 6m25s*


Requirements:
1. Confirm the wire contract: `JobActivityDelegate.Type` in `appwire/types.go` (is it `omitempty`;
   what values does the backend emit — `agent/jobs_activity.go` sets `"delegate"` at the stable
   construction site; does anything emit a turn-container type today; what does the generated
   TypeScript declare). Cite file:line.
2. Centralize delegate-kind detection in one exported predicate in `activityData.ts` (for example
   `isTurnContainer(delegate)` true only for explicitly recognized turn-container type values;
   omitted or `"delegate"` → stable delegate), and use it in `activityData.ts:583-587`,
   `activityRows.ts:82-92`, and `activityMerge.ts:80-83` (and any other `type === "delegate"`
   comparison — grep). RED-first: a stable delegate payload with `type` omitted must classify,
   merge and count as a stable delegate (revision fence applies; terminal failure vocabulary
   `failed`/`exhausted`; not the turn-container union path).
3. `npx biome check --write`; `make test-web`; single commit; then `git fetch origin && git merge
   --no-ff --no-edit origin/main` (do not push; the coordinator pushes).

## Task 31: PR #1137 transcript bottom-hold — round 4: no gesture on a no-op scroll write

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `fb073213f`
(CI green so far). Frontend dir `cmd/evener-hub/frontend`.

Context: round 3 (Task 28) moved gesture marking to the source: `useTranscriptScrollKeys`
calls `markGesture()` before each direct `scrollTop` write, port-level key inference was
removed, drags end on the button/window, and the pending flag is consumed on the next handled
scroll event. A false veto still disarms the re-pin until the reader returns to the bottom, so
every false marker matters.

RoboRev finding at head `fb07321` (verbatim):

## roborev: Combined Review (`fb07321`)

**Verdict:** One medium-severity edge case found; otherwise the change is solid.

### Medium

- **`cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScrollKeys.ts:79`** — `markGestureRef.current()` fires even when the clamped `scrollTop` assignment is a no-op (e.g., `Alt+ArrowDown` or `Alt+Shift+ArrowDown` while already at the bottom). If a late measurement increases `scrollHeight` before the gesture flag clears, the correction is vetoed, leaving the transcript short of the true bottom and incorrectly showing the new-content pill. **Fix:** Only mark the gesture when the requested scroll would actually change the offset (calculate the clamped target or compare the resulting `scrollTop`), and add a regression test for a no-op downward key at the bottom during late measurement.

---
*Reviewers: 2 done | Synthesis: codex, 6s | Total: 5m29s*


Requirements:
1. Mark a gesture only when the direct write actually moves the port: compare `scrollTop`
   before and after the clamped assignment in `useTranscriptScrollKeys` (or compute the clamped
   target first and skip the marker when it equals the current offset). RED-first: `Alt+ArrowDown`
   / `Alt+Shift+ArrowDown` while already at the bottom during a late measurement must not veto
   the re-pin; a real page-up must still mark.
2. Check the same no-op hazard for every other `markGesture` caller and for the drag/wheel paths
   (a wheel event with zero delta; a `touchmove` that does not scroll) — state which are exact
   and which can still mark without motion, and fix the ones that can, or explain why marking is
   harmless there.
3. `npx biome check --write`; `make test-web`; guard ≥20 clean runs and one full
   `make test-web-browser`; commit; fetch/merge main if it moved; push; do not change PR state.

## Task 32: PR #1105 local fork capability — round 6: propagate ActiveFlags, fence both identities, validate first

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `46a8ba832` (CI green so far).

Context: round 5 (Task 29) made the roster carry the daemon's `ActiveFlags` so admission could
apply the projection's recovery predicate; round 4 resolved stable refs to the current session
for the mutation while keeping recovery/deletion reservations on the requested ref (a deferred
minor); round 5 placed parameter validation after the deletion/recovery fences deliberately.
RoboRev's review of that head raises the consequences:

## roborev: Combined Review (`46a8ba8`)

## Summary Verdict

Three medium-severity issues around fork capability projection and fencing, plus one low-severity roster fingerprint gap — none are clean.

---

### Medium

- **`ActiveFlags` dropped on local thread path** — `cmd/evener-hub/app_rpc.go:34`, `cmd/evener-hub/app_threadread.go:490`
  `Roster` records daemon `ActiveFlags`, but the local `ThreadList` path copies only `Status` into `LocalDaemonEntry`, so `threadFromEntry` drops `resumeRequired`. `applyHubForkCapability` then advertises `forkFromTurn`, and the subsequent fork RPC refreshes the roster and rejects the same request.
  *Fix*: Propagate `ActiveFlags` through `LocalDaemonEntry` into `Thread.Status`, or have `applyHubForkCapability` consult the current roster owner when projecting live local threads.

- **Fence identity mismatch in `hubThreadFork`** — `cmd/evener-hub/app_threadlifecycle.go` (`hubThreadFork`)
  Recovery and deletion fences are checked on the requested alias (`ref.ThreadID`) while the fork branches on the resolved session (`forkTargetSessionID`, the current session after `thread/clear`). A stable-ref fork therefore misses `ResumeRequired`/`Stopping` or deletion state held on the current session.
  *Fix*: Resolve `forkTargetSessionID` before fencing and check recovery/deletion for both the requested alias and the resolved target, matching the dual delegate check already done.

- **Validation ordering allows wrong error precedence** — `cmd/evener-hub/app_threadlifecycle.go` (`hubThreadFork`)
  `validateThreadForkParams` runs after `deletionFenceError` and `sessionActionRecoveryError`. The latter can return `Unavailable` and, via `sessionConnectionRecoveryError`, call `ownershipEntry` when unconfirmed recovery exists. A malformed fork on a fenced session returns the wrong code and can hit the project scan the comment claims validation avoids.
  *Fix*: Move `validateThreadForkParams` to the top after `ParseRef`/source check, before epoch capture, deletion, and recovery fences.

---

### Low

- **`rosterFingerprint` omits `ActiveFlags`** — `cmd/evener-hub/internal/hubcore/roster.go:242`
  If a daemon changes only `resumeRequired` while remaining `idle`, the roster reports no observable change, so `onChange` does not invalidate navigation and clients can retain a stale fork capability.
  *Fix*: Include a deterministically ordered copy of `ActiveFlags` in the roster fingerprint and trigger the relevant status/navigation update when those flags change.

---
*Reviewers: 2 done | Synthesis: codex, 14s | Total: 8m28s*


Requirements:
1. Medium 1: propagate `ActiveFlags` from the roster's live entry through `LocalDaemonEntry`
   into `Thread.Status` on the local `ThreadList`/read path (or have `applyHubForkCapability`
   consult the roster owner for live local threads — pick the one that keeps a single source and
   say why). RED-first: a live local thread whose daemon reports `resumeRequired` in
   `activeFlags` must not advertise `forkFromTurn` on the list/read path.
2. Medium 2: check the recovery and deletion fences for BOTH the requested alias and the
   resolved target session (matching the dual live-delegate check), resolving
   `forkTargetSessionID` before fencing. RED-first: a stable-ref fork whose resolved current
   session is resume-required (or deletion-fenced) must be refused.
3. Medium 3: move `validateThreadForkParams` to the top after `ParseRef`/source check, before
   epoch capture, deletion and recovery fences (RoboRev's ordering supersedes round 5's
   conservative placement; state that the precedence change is intended). RED-first: a malformed
   fork against a fenced session returns `CodeInvalidParams` and calls neither `ownershipEntry`
   nor the roster refresh.
4. Low: include a deterministically ordered copy of `ActiveFlags` in `rosterFingerprint` so a
   flags-only change invalidates navigation/status; RED-first through the fingerprint.
5. `go test -count=1 ./cmd/evener-hub/...`, vet, focused `-race`, pinned lint. One commit per
   finding.

## Task 33: PR #1100 round timings — round 4: preseed input, idle folds, and continuation attribution

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay`. Current head `2321d93f5` (CI fully green). Modules:
`agent/` (own module), `internal/appprojector`, `internal/apptranscript`, `server`.

Context: round 3 (Task 20) gave every turn that no client mutation reserved an in-memory
`turn_direct_<ulid>` via `nameTurnItself` (direct input, goal continuation, unserved wake),
carried on the persisted entry and the events, reported by `activeTurnOwner` while it runs, with
`ActiveTurnID` from a client mutation taking precedence. The implementer flagged twice that a
continuation which could not claim the durable slot (`turnNameHeld`) leaves its entry id
(`turn_direct_*`) disagreeing with `activeTurnOwner` (`turn_m*`). RoboRev's identity reviewer now
raises that plus two more paths:

## roborev: Combined Review (`2321d93`)

**Mixed verdict — one reviewer flagged three Medium turn-identity divergence issues; the other found the code clean.**

## Medium

- **Preseeded delegate input has empty `StableTurnID`** — `agent/session_lifecycle.go:2011-2022`, `agent/delegate_runtime.go:2187`
  A preseeded delegate input bypasses normal user-turn creation, so the persisted `TurnUserInput` and emitted `EventUserInput` both keep an empty `StableTurnID`. When an environment entry precedes it, the live projector opens `turn_1` while transcript replay falls back to the physical entry index, changing item identities after reload.
  **Fix**: Assign one stable turn ID while pre-seeding the input and propagate that same ID through the later `EventUserInput`.

- **Idle compaction records persist without an owner** — `agent/session_compaction.go:405`, `agent/session_compaction.go:541-544`
  Idle compaction captures an empty `compactionOwner` and persists context-compaction/checkpoint records without an owner. Live announcements reuse one synthetic no-active-turn gap ID, but cold replay treats each unowned compaction record as a separate entry-index group, causing reload grouping and transcript-key changes.
  **Fix**: Persist a single stable owner for an idle fold and use it for both emitted events and all persisted compaction records, or make both projections coalesce the same unowned gap.

- **Continuation `directTurnID` loses attribution to `activeTurnOwner`** — `agent/session_queue.go:1053-1061`, `agent/session_lifecycle.go:1568-1570`
  A continuation that cannot claim the durable slot self-mints `directTurnID`, but `activeTurnOwner` prefers the other turn's `ActiveTurnID`. Its timing, steering, and compaction records are therefore attributed to the other turn even though the continuation opened under its self-minted ID.
  **Fix**: Prefer the current `directTurnID` when present, or carry the continuation's owner explicitly through all publication paths.

## Reviewer Disagreement

- Review 2 found no issues and described the change as replay-safe with proper environment-tracker recovery.
- The three findings above all stem from Review 1's analysis of fallback turn-identity paths where live and replayed transcripts can diverge — a class of issue Review 2 did not flag. The file/line references from Review 1 are preserved for follow-up.

---
*Reviewers: 2 done | Synthesis: codex, 12s | Total: 30m37s*


Requirements:
1. Medium 1 (preseeded delegate input): the child preseed path (`delegate_runtime.go:2187` →
   `session_lifecycle.go:2011-2022`) bypasses `acceptUserInput`'s naming. Route it through the
   same rule (mint a direct id at preseed time, carry it on the persisted `TurnUserInput` and the
   later `EventUserInput`). RED-first live/cold parity for a delegate whose preseeded input follows
   an environment entry.
2. Medium 2 (idle compaction records ownerless): when a fold runs with no active turn,
   `compactionOwner` is empty; live coalesces on one synthetic gap id while cold makes each
   unowned record its own group. Choose one rule with a stated reason: persist a single stable
   owner for the idle fold (mint one id per fold and stamp every emitted event and persisted
   record with it), or make cold grouping coalesce consecutive unowned compaction records into
   one gap the same way live does. Prefer the option that needs no index-version bump; say so if
   one is unavoidable. RED-first parity for an idle fold followed by reload.
3. Medium 3 (continuation attribution): coordinator ruling — the turn that is actually
   executing owns its records. When `directTurnID` is set (a direct/continuation/wake turn is
   running), `activeTurnOwner` returns it even if a client mutation holds the reserved slot;
   `ActiveTurnID` wins only when no direct turn is executing. Verify against the client-mutation
   path pinned by `TestCompactionOwnerDuringOverlappingMutations` and the Task 9 overlap case
   (they must still pass; if one fails, report why before changing it — do not re-point). RED-first:
   a continuation under `turnNameHeld` has its timing, steering and compaction records attributed
   to its own id in both projections.
4. Run inside `agent/`: `go test -count=1 ./...`, `go vet ./...`, deadline audit; from the root:
   `./internal/appprojector/... ./internal/apptranscript/... ./server/... ./cmd/evener/...
   ./cmd/evener-tui/...`; focused `-race`; pinned `golangci-lint run ./agent/... ./server/...`.
   One commit per finding.

## Task 34: PR #1137 transcript bottom-hold — round 5: no marker for a gesture that cannot move the port

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `e3ea4a92e`
(Task 31 fix round 1, not yet pushed; RoboRev reviewed `45a8f15`). Frontend dir
`cmd/evener-hub/frontend`. File under change:
`src/panes/session/transcript/flow/useTranscriptScroll.ts` (gesture block near lines 700-800,
listeners near 1244-1262) and its test `useTranscriptScroll.test.ts`.

Context: Task 31 made the wheel and touch markers exact for sideways input and named two residual
over-marking cases in the comment above `startPointerDrag` (a vertical wheel/drag AT a scroll
limit, and one over a NESTED vertical scroller - `tools/sandboxescalation.module.css:28` is
`overflow-y: auto` inside the transcript). Over-marking is the harmful direction: one false veto
of a measurement correction records the reader as away from the bottom and disarms the re-pin
until they return, which is the pill flake this PR exists to fix. RoboRev's round on `45a8f15`
raises those residuals plus two more (verbatim):

## roborev: Combined Review (`45a8f15`)

## Code Review Summary

Three medium-severity issues around false gesture vetoes in the scroll correction logic.

## Findings

### Medium

- **`useTranscriptScroll.ts:752-769` — Pointer handlers don't filter `pointerType`, causing touch to double-fire gestures**
  Both reviewers flagged this. The pointer handlers track all pointer types, so a touch swipe fires both `touchmove` and `pointermove`. The touch path correctly ignores sideways movement, but the pointer path marks on any move with `buttons !== 0`, re-introducing a false veto for sideways swipes in nested scrollers and defeating the touch exactness. The separate touch Y-axis check doesn't prevent this because both event streams fire for the same touch.
  **Fix:** Ignore touch in the pointer path and only track primary-button mouse/pen drags — return early when `event.pointerType === "touch"` and only set dragging for `button === 0` / `isPrimary`. Add coverage using both pointer and touch events for a horizontal swipe.

- **`useTranscriptScroll.ts:774-777` — Wheel events mark a gesture before confirming the transcript scroll port actually moved**
  Any wheel event with nonzero `deltaY` marks a gesture before confirming that the transcript scroll port moved. Wheel events bubble from nested scroll containers (the transcript's internal rails), and an outward wheel at the transcript's own scroll limit is also a no-op. If a measurement correction arrives while that pending marker is set, the correction is vetoed and the normal path records the reader as away from the bottom, preventing subsequent automatic re-pinning until the reader returns to the bottom.
  **Fix:** Associate the marker with the actual transcript scroll owner and movement, or defer/validate the marker against the port's resulting geometry; at minimum suppress wheel directions already at the relevant scroll boundary and ignore events from independently scrollable descendants.

- **`useTranscriptScroll.ts:781-793` — `lastTouchYRef` never cleared on `touchend`/`touchcancel`**
  `lastTouchYRef` is never cleared on `touchend`/`touchcancel`, so the `last === null` guard for a touch that began outside the port only works once. After any normal touch sequence the stale Y makes the next outside-start first move compare against the old value and falsely veto.
  **Fix:** Add `touchend`/`touchcancel` listeners that reset `lastTouchYRef.current` to `null`.

## Summary

The change adds bottom-hold correction and gesture tracking, but pointer/touch overlap, bubbled/no-op wheel events, and a stale touch-Y ref can each create false gesture vetoes. All three are in the same gesture-tracking block and should be addressed together.

---
*Reviewers: 2 done | Synthesis: codex, 12s | Total: 3m48s*

Coordinator verification and rulings:
- Medium 1 is real: `startPointerDrag` sets the drag flag on every `pointerdown` and
  `continuePointerDrag` marks on any move with a button held, and a finger produces pointer events
  with `buttons === 1`, so a sideways swipe marks through the pointer path even though the touch
  path deliberately does not.
- Medium 2 is the residual Task 31 documented. Ruling: close it, not just document it. The
  "at a limit" case is decidable BEFORE the scroll from the port's current geometry (reading
  `scrollTop`/`scrollHeight`/`clientHeight` before the browser scrolls is a plain read of present
  state, not the after-the-fact geometry inference the design replaced), and the nested-scroller
  case is decidable from `event.target`. Cost if wrong: a predicate that wrongly says "cannot
  move" under-marks one real gesture scroll, the safe direction per the existing comment.
- Medium 3: under the touch event model a touch is delivered to the element it started on, so a
  `touchmove` reaching the port normally implies its `touchstart` bubbled through too and reseeded
  the ref; the stale value only bites when a descendant stopped `touchstart`'s propagation or the
  listeners attached mid-touch. Ruling: fix it anyway (two listeners, one ref reset) because the
  guard's stated contract ("a touch that began outside the port is not marked on its first move")
  is otherwise false after the first touch.

Requirements:
1. Medium 1: `startPointerDrag` begins a drag only for a non-touch pointer with the primary
   button (`event.pointerType !== "touch"` and `event.button === 0`); `continuePointerDrag`
   returns early for `pointerType === "touch"` before touching the drag flag (a finger must not
   end or mark a mouse drag). RED-first: a horizontal swipe dispatched as BOTH a touch sequence
   and the matching pointer events (`pointerType: "touch"`) does not veto the correction; a
   right-button drag (`button: 2`) does not mark.
2. Medium 2: extract one predicate that answers "can this vertical input move the port?" and use
   it from both `markWheel` and `continueTouch` (touch has the same two residuals; fixing wheel
   alone leaves the next round obvious). Direction: wheel `deltaY > 0` and a finger moving UP
   both push content down (`scrollTop` increases). The predicate says no when (a) the port is
   already at that limit (reuse `isAtBottom` from `./scrollMetrics` for the bottom; `scrollTop <= 0`
   for the top), or (b) some element between `event.target` and the port (exclusive) is an
   independent vertical scroller that can still move in that direction (`scrollHeight >
   clientHeight`, computed `overflow-y` of `auto` or `scroll`, and not at its own limit in that
   direction). A nested scroller that is at its own limit lets the input reach the port, so that
   case still marks. RED-first for: wheel down at the bottom; wheel up at the top; wheel over a
   nested scroller with room in that direction; wheel over a nested scroller at its limit (marks);
   and the touch equivalents of the first and third. jsdom hardcodes layout to 0 - the test file
   already fakes metrics through `makeMeasure`/a defined `scrollTop`; fake the nested element the
   same way (define `scrollHeight`/`clientHeight`/`scrollTop` on it and set `style.overflowY`).
   Rewrite the marker-exactness comment above `startPointerDrag` so it describes what now holds;
   drop the two residual cases from it and from the report's audit table.
3. Medium 3: `touchend` and `touchcancel` listeners (added and removed with the others) reset
   `lastTouchYRef` to `null`. RED-first: touchstart/touchmove/touchend, then a bare `touchmove`
   with no `touchstart`, does not mark.
4. Listener deps: the effect's dependency list names every handler it attaches (Task 31
   convention); add the new ones.
5. Verification before reporting: both test files green, `make test-web` green (typecheck, test,
   lint), biome clean on touched files. Commit(s) on the branch, do NOT push, do not amend.
6. Report contract: append a "Task 34" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-31-report.md` (same lane) with: the
   predicate's exact rule, each finding's verification and fix, commit SHAs, test summary, and
   any residual you could not close with a stated reason.

## Task 35: PR #1091 portable activity — round 7: a bounded refresh speaks for the turns it lists

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1091`
Branch: `codex/mobile-portable-activity`. Current head `0703ac29a` (CI green, contains main
`e812703fc`). Frontend dir `cmd/evener-hub/frontend`. File under change:
`src/protocol/activityMerge.ts` (`fenceSession`, incomplete-branch turn union near line 134-139;
`unionTurns` at line 51) and its test `src/protocol/activityMerge.test.ts`.

Context: Task 25 made `revisionFencedDelegate` take `patchIsAuthority`; a root refresh passes
`true`, so the refresh's projection wins outright and only coverage governs what it left out.
For an incomplete branch `fenceSession` then overrides `delegate.turns` with
`unionTurns(prior.delegate.turns, entry.delegate.turns)`, and `unionTurns` keeps its FIRST
argument's object for every duplicate id. So a listed turn that changed state between snapshots
keeps its stale prior object even though the refresh is authoritative for it. RoboRev finding at
head `0703ac2` (verbatim):

## roborev: Combined Review (`0703ac2`)

## Verdict: One medium finding — bounded root refresh can leave turn rows and aggregate badges inconsistent.

### Medium

- **`cmd/evener-hub/frontend/src/protocol/activityMerge.ts:134-139`** — An incomplete root refresh treats every duplicate turn ID as stale and retains the previous turn object, even though the refresh is authoritative for the turns it lists. If a listed turn changes from running to completed or failed, the UI continues rendering the old state while the incoming counts and aggregate describe the new state; because the list length is unchanged, `retainedBelow` remains false and the tree is not re-summarized. Fix: for authoritative root refreshes, replace duplicate turn records with the incoming versions and retain only prior turns omitted past the cutoff. Recompute session summaries whenever retained or replaced turns affect the displayed tree.

---
*Reviewers: 2 done | Synthesis: codex, 6s | Total: 13m12s*

Coordinator verification and ruling: real. `unionTurns` at line 51 builds
`[...current, ...patch-not-in-current]`, so in `fenceSession` the prior object wins for every
listed id, contradicting the comment two lines above it ("its projection wins outright").
Ruling: in `fenceSession`'s incomplete-branch path the INCOMING turn list is the statement and
prior turns are kept only for ids it omits, appended after it (a bounded page is a prefix of
the turn list, so incoming-first is also the right order). The non-authority union inside
`revisionFencedDelegate` (a continuation aimed at a descendant) stays current-first - that patch
does not speak for the container. Once incoming wins for listed ids, the server's counts (which
describe the page it sent) agree with the displayed turns when nothing is retained, and
`summarizeSession` already runs when something is; no extra recompute path is needed. Cost if
wrong: a retained turn's ordering shifts from prior-first to incoming-first.

Requirements:
1. RED-first: a bounded root refresh (branch truncated with a continuation) whose incoming turn
   list carries `turn-1` as terminal/completed while the current tree has `turn-1` running and
   `turn-2` past the cutoff. Expect the merged container to show `turn-1` completed (`terminal:
   true`, its outcome) and `turn-2` retained, in that order, and expect `counts` to agree with the
   displayed turns (the retained turn counted, the replaced turn counted in its NEW state). Add a
   second assertion for the not-retained case (incoming lists both turns, one changed state):
   the displayed turns are the incoming objects and `counts` are the server's.
2. Smallest fix: make the incomplete-branch union incoming-first (the arguments of `unionTurns`,
   or an equivalent one-line change) so listed ids take the incoming object and only omitted
   prior turns are appended. Do not change `revisionFencedDelegate`'s non-authority union.
   Keep the `retainedBelow` length comparison semantics intact.
3. Fix the comment at the override so it states the rule that now holds (incoming speaks for
   listed turns; prior turns survive only past the cutoff). No comment about what used to be.
4. Existing tests "a bounded refresh keeps the turns a continuation loaded into a container" and
   "a bounded refresh still updates the container state it does list" must still pass unchanged;
   if either fails, report why before touching it - do not re-point.
5. Verification before reporting: `npx biome check --write` on touched files, the activityMerge
   test file green, `make test-web` green. Commit on the branch; do NOT push; do not amend; do
   not merge main (the branch already contains `e812703fc`).
6. Report contract: append a "Task 35" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-30-report.md` (same lane) with RED and
   GREEN evidence, commit SHA, and a one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 36: PR #1096 native checkpoint — refresh onto main after #1091's squash merge

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096, base main). Tip `fa44d5180`. Main is now
`bc1ed2aef` = the squash merge of #1091 ("refactor(client): share activity and job output
helpers"). A merge of `origin/main` into the branch is IN PROGRESS in the worktree
(`git merge --no-ff --no-edit origin/main` was run by the coordinator; `git status` shows 13
conflicted files, all under `cmd/evener-hub/frontend/src/{protocol,stores,panes/session/chrome}`,
24 conflict hunks in total; rerere preimages were recorded).

Why it conflicts: #1096 repeatedly merged EARLIER states of the #1091 branch (the second parents
of its first-parent merge commits: `51fc01d0c`, `ee614fd61`, `77fcabca9`, `be05e2563`, `2664cc881`,
`279f32e7a`, `ab5c7006a`, `28ca7f6d3`, `cfb63fb68`, …), and #1091 was later rewritten and went
through seven more review rounds before landing, so those commits are not ancestors of what main
now holds. `git diff --stat origin/main fa44d5180 -- <conflicted files>` is 94 insertions /
955 deletions: the branch mostly holds an OLDER copy of #1091's code. #1096's own first-parent
commit in these files is `d44f080c2 refactor(web): extract portable activity helpers` (the origin
of #1091 itself). Everything under `mobile/` and `mobile-native/` merged cleanly.

Requirements:
1. Resolve every conflict so that main's #1091 code is the result for the 13 files
   (`git checkout --theirs` per file is the right starting point: in this merge "ours" is the
   branch and "theirs" is `origin/main`), THEN check each hunk of `git diff origin/main fa44d5180`
   for the same files: for every line the branch ADDS relative to main, decide whether it is (a)
   already present in main in another form, (b) an older draft of something #1091 superseded, or
   (c) #1096's own change that main lacks. Re-apply only (c). List every hunk with its
   classification and the commit it came from (`git log -S` or `git blame fa44d5180 -- file`)
   in the report; "nothing in category (c)" is an acceptable and likely answer, but it has to be
   shown, not assumed.
2. The whole tree must build and pass after resolution: from the worktree root run
   `make test-web` (typecheck, test, lint) and `make test-native` (`npm ci --prefix mobile-native`
   first if `mobile-native/node_modules` is missing or stale; postinstall runs patch-package),
   and `npx biome check` on the resolved files from `cmd/evener-hub/frontend`. If an existing
   test on the branch fails against main's #1091 code, do NOT weaken or delete it: report the
   failure with the test name and the assertion, and stop with DONE_WITH_CONCERNS.
3. Commit the merge with git's default merge message (`git commit --no-edit`). One merge commit,
   nothing else. Do not amend, do not push, do not rebase, do not create branches.
4. Do not touch anything outside the 13 conflicted files except what `npm ci` regenerates under
   `node_modules` (untracked). If `git status` shows any other modified tracked file after your
   work, explain it.
5. Report contract: write `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-36-report.md`
   with the hunk classification table, the exact commands and their tail output for the gates,
   and the merge commit SHA; return only status, the merge commit SHA, a one-line test summary,
   and concerns.

## Task 37: PR #1137 transcript bottom-hold — round 6: a session switch clears the gesture marker

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `0e6960631` (= Task 34's
`b812e7f93` plus a coordinator merge of main `bc1ed2aef`; CI on `b812e7f` was fully green).
Frontend dir `cmd/evener-hub/frontend`. File under change:
`src/panes/session/transcript/flow/useTranscriptScroll.ts` (gesture refs and their comment near
lines 780-792; the per-ref reset block near line 1201) and its test `useTranscriptScroll.test.ts`.

RoboRev finding at head `b812e7f` (verbatim, one reviewer; the other found no issues):

## roborev: Combined Review (`b812e7f`)

## Code Review Summary

**Verdict: Mostly clean — one medium-severity state-retention concern flagged by one reviewer.**

---

### Medium

- **Gesture state persists across session/ref transitions** — `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:784-792`
  Gesture state (pending marker, pointer drag, touch position) is intentionally retained across session and scroll-element transitions. A pending gesture from the previous transcript can be consumed by the first scroll event of a newly selected session, marking it as away-from-bottom and preventing the late-measurement re-pin — leaving a fresh session in an incorrect jump-to-latest state.
  **Fix**: Reset/cancel gesture and input-tracking refs when the session or scroll element changes, or associate pending gestures with a session/element generation and ignore markers from prior generations.

---

### Reviewer Consensus

- **Reviewer 1**: Flagged the medium issue above.
- **Reviewer 2**: No issues found; noted the bottom-hold correction, same-frame gesture veto, nested-scroller guards, and keyboard scroll marking as working correctly.

The disagreement centers on whether retained gesture state across session switches is a real risk. The fix is defensive and low-cost (generation-tagging or ref reset on transition), so it's worth considering even though the happy path appears sound.

---
*Reviewers: 2 done | Synthesis: codex, 20s | Total: 6m55s*

Coordinator verification and ruling: real, narrow, cheap. The per-ref reset block (the
`refForInitRef.current !== ref || hasContentRemounted` branch) resets every other piece of
per-session state but not `gesturePendingRef`, `pointerDraggingRef` or `lastTouchYRef`, and the
comment above them says so by design ("at most one event of the new session could ever see a
gesture aimed at the old one"). That one event is the mount block's own `scrollToIndex`, the most
consequential scroll event the new session gets: handleScroll consumes the stale marker, the
correction is vetoed, and the ordinary path records the reader as away from the bottom - the
exact mount strand this PR exists to fix. Reachable when the clearing frame has not run (a wheel
and a sidebar click in one frame, or a wheel just as the tab is hidden, since requestAnimationFrame
does not run while hidden). Ruling: a gesture marker exists to veto the event it caused, and no
gesture can be responsible for a different session's mount scroll, so the switch clears it.
Cost if wrong: one real gesture straddling a session switch goes unmarked (under-marking, the
safe direction).

Requirements:
1. RED-first: mark a gesture (a real vertical `WheelEvent` that the predicate accepts) on
   session A with no scroll event in between, then switch `ref` to session B so the reset block
   runs and the mount lands short with a later measurement correction (the same shape the
   existing correction tests use). Pre-fix the correction is vetoed (`expected 16577 to be
   16664` or the equivalent for the harness you use); post-fix it re-pins. Add the pointer-drag
   and touch variants only if the harness makes them one-liners; otherwise cover the marker.
2. In the per-ref reset block, clear the gesture state: `gesturePendingRef` false, the pending
   clearing frame cancelled and its handle nulled, `pointerDraggingRef` false, `lastTouchYRef`
   null. Keep the per-ref block the single place this happens (do not sprinkle resets into the
   listeners).
3. Rewrite the comment above the gesture refs so it states the rule that now holds (a marker
   vetoes only the event it caused; a session switch cannot be that event, so it clears) and
   drops the "NOT reset" claim. No comment about what used to be there.
4. Existing tests must pass untouched. Both gesture test files green, `make test-web` green,
   biome clean. One commit on top of `0e6960631`; do NOT push, do not amend, do not merge main
   again.
5. Report contract: append a "Task 37" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-31-report.md` (same lane) with RED and
   GREEN evidence, commit SHA, one-line test summary; return only status, commit SHA, test summary,
   concerns.

## Task 38: PR #1105 local fork capability — round 7: recheck against the rendezvous, flags on the spawned-read path

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `566806bde` (Task 32 commits plus a merge of main
`bc1ed2aef`; CI fully green). Module: root (`cmd/evener-hub`, `cmd/evener-hub/internal/hubcore`).

Context: round 6 (Task 32) resolved the fork target before the locks, took both fences sorted, and
added a post-lock recheck that re-runs `forkTargetSessionID`. RoboRev's round on `566806b`
(verbatim):

## roborev: Combined Review (`566806b`)

**Verdict: 2 findings (1 Medium, 1 Low) — hub-owned fork capability projection is solid, with minor robustness gaps in the confirmation/recheck paths.**

## Medium

- **`cmd/evener-hub/app_threadlifecycle.go:845`** — The post-lock target recheck reads the existing roster snapshot without refreshing it. A concurrent `thread/clear` can acquire the same alias lock, update the rendezvous session ID, release the lock, and leave the roster stale until its asynchronous watcher runs. The fork can then resolve the old session, pass the recheck, and branch the retired transcript. **Fix**: Refresh the roster after acquiring the locks and before resolving the target again, or read the current rendezvous ownership directly while holding the locks. Add coverage for a delayed roster watcher.

## Low

- **`cmd/evener-hub/internal/hubcore/roster.go:914`** — `ReadSpawnedThread` constructs a `ProbeResult` from the daemon's thread response but drops `root.Status.ActiveFlags`. The new recovery fence relies on these flags in the roster, so this confirmation path can temporarily advertise fork capability for a daemon reporting `resumeRequired`. **Fix**: Copy `root.Status.ActiveFlags` into the `ProbeResult`, and add a confirmation-path test asserting the flags survive publication.

---
*Reviewers: 2 done | Synthesis: codex, 18s | Total: 8m40s*

Coordinator verification and rulings:
- Medium is real. `forkTargetSessionID` (app_threadlifecycle.go:1007-1019) resolves through
  `liveDaemonForThread`, i.e. the roster's last scan, and the roster learns a rendezvous session
  id change asynchronously (its watcher re-lists `rendezvous.ListStrict(runDir)`, roster.go:337-342).
  `resumeThread`'s recheck (app_threadlifecycle.go:387-393 via `resumeOwnership` →
  `resumeOwnershipStep`, :536-560) reads `ResumeLocks` state and the rendezvous entries in
  `cfg.RunDir` directly, which is why it is not fooled. The fork's recheck compares a stale
  snapshot to the same stale snapshot and passes. Ruling: do NOT refresh the roster under the
  locks (that probes daemons while holding two per-session mutexes); make the post-lock recheck
  read the current ownership from the same sources `resumeOwnershipStep` reads (rendezvous
  entries in `cfg.RunDir` for the thread, plus `ResumeLocks` resolved/recovery state), compare
  the session that names to the pre-lock `sessionID`, and refuse with the same retryable
  `Unavailable` when they differ. The pre-lock resolution stays roster-based (it also feeds the
  live-delegate fence). Cost if wrong: one extra rendezvous directory read per fork under lock.
- Low is real. `ReadSpawnedThread` (roster.go:911-914) builds its `ProbeResult` without
  `ActiveFlags` while the struct carries them (roster.go:56) and the normal prober path fills them;
  a spawned daemon confirmed through this path advertises fork capability until the next probe
  even if it reported `resumeRequired`. Ruling: copy `root.Status.ActiveFlags` (cloned) into the
  result.

Requirements:
1. Medium: RED-first with a delayed roster watcher: keep the roster's answer stale through the
   existing `hubRosterList` seam (the shape Task 32's re-check test uses) while the rendezvous
   entry for the thread in a real `cfg.RunDir` is rewritten to a new session id
   (`rvreg.UpdateSessionID` or writing the entry the way the rendezvous tests do) after the
   pre-lock resolution and before the recheck; the fork must refuse without branching (pin with the
   `ListSessionMetas` before/after count), and a control row where the rendezvous still names the
   same session must proceed. Then implement the rendezvous-backed recheck. Do not widen the
   pre-lock path.
2. Low: RED-first confirmation-path test asserting that `ActiveFlags` reported by the spawned
   daemon's thread response survive publication into the roster (the flag then drives
   `hubForkLiveStatusFenced` — assert through whichever surface the existing `ReadSpawnedThread`
   tests observe); then the one-line copy.
3. Existing fork/resume/fence tests must pass untouched; if one contradicts the new recheck,
   report why before changing anything.
4. Gates: `gofmt -l` clean, `go vet ./cmd/evener-hub/...`, `go test -count=1 ./cmd/evener-hub/...`,
   `-race` over the fork fence tests and `ReadSpawnedThread` tests, pinned golangci-lint 2.13.1
   0 issues. One commit per finding; do NOT push, do not amend, do not merge main.
5. Report contract: append a "Task 38" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` (same lane) with RED and
   GREEN evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 39: PR #1096 native checkpoint — round 2: wire turn status in projection, no per-turn usage merge

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096). Current head `0c19ec0bc` (merge of main
`bc1ed2aef`; CI fully green). Shared mobile core `mobile/src` (vitest via `make test-native`, which
runs `mobile-native`'s vitest, `test:shared`, and `tsc`; `npm ci --prefix mobile-native` only if its
`node_modules` is missing). Files under change: `mobile/src/conversation/project.ts` (line 309) and
its test; `mobile/src/state/conversation.ts` (lines 2653-2655) and its test.

RoboRev finding at head `0c19ec0` (verbatim):

## roborev: Combined Review (`0c19ec0`)

**Verdict:** Two medium-severity projection bugs in the mobile conversation/activity state; otherwise structurally sound.

## Medium

- **`mobile/src/conversation/project.ts:309`** — `projectThread` checks `turn.status === "running"` for active turns, but the wire protocol uses `"inProgress"`. Initial snapshots and rehydrated state with an active turn render assistant messages with `streaming: false`, making the transcript appear settled while output is still arriving. Use the wire `"inProgress"` status (ideally via a shared active-status predicate) and add a projection test for an active snapshot.
- **`mobile/src/state/conversation.ts:2653`** — The `turn/completed` handler merges the individual turn's usage into `conversation.usage`, which is the cumulative session aggregate. After multiple turns, displayed token counters can temporarily—or permanently on the plain `open` path—reset to the latest turn's totals. Do not merge `params.turn.usage` into cumulative conversation usage; publish the completion state and refresh the authoritative conversation projection instead.

---
*Reviewers: 2 done | Synthesis: codex, 6s | Total: 13m56s*

Coordinator verification and rulings:
- Medium 1 is real. `projectItem` (project.ts:284-286) receives a wire `Turn` from
  `cmd/evener-hub/frontend/src/protocol/types.gen` whose status on the wire is `"inProgress"`
  (`appwire.TurnStatusInProgress`); project.ts:676 already uses `turn.status === "inProgress"` to
  find the active turn, and project.ts:122 / state/conversation.ts:618,650,685 map item status
  the same way. Line 309 compares the turn against `"running"`, which the wire never sends, so a
  snapshot's assistant message is never `streaming`. Ruling: compare against the wire value
  through one shared predicate (there may already be one for items; reuse or extract it so the
  turn and item checks cannot drift again). If the existing test "projects an agentMessage with
  a delta as streaming while incomplete" (project.test.ts:311) passes today, its fixture must be
  setting a status the wire never sends; correcting that fixture to the wire value is permitted
  and must be called out in the report as a fixture correction with the before/after value.
- Medium 2 is real. `conversation.usage` is projected from `EvenerThread.usage`, the session's
  cumulative counters (model.ts:171, project.ts:690,742-747). The `turn/completed` handler
  (state/conversation.ts:2653-2655) spreads the single turn's `usage` over it, so after the first
  turn the displayed counters become that turn's totals. Ruling: remove the merge; the handler
  publishes completion state only, and usage stays whatever the last authoritative projection
  said. Check whether a conversation refresh (`thread/read` re-projection) already follows
  completion on every path (the finding says the plain `open` path lacks one); if it does not,
  do NOT add a new refresh in this task - say so in the report and leave usage untouched until the
  next projection. Cost if wrong: counters lag until the next projection instead of being wrong.

Requirements:
1. Medium 1: RED-first projection test: a `Thread` snapshot whose turn has wire status
   `"inProgress"` and an `agentMessage` with a delta projects the assistant item with
   `streaming: true`; a completed turn projects `streaming: false`. Then the fix via the shared
   predicate.
2. Medium 2: RED-first store test: after two `turn/completed` notifications carrying different
   per-turn `usage`, `conversation.usage` still equals what the projection set (pin totalTokens
   and inputTokens at least); then delete the merge. Keep the `activeTurnId`/`status` handling
   unchanged.
3. No existing expectation weakened; the one permitted fixture correction is the wire-value one
   above, reported explicitly.
4. Gates: the two test files green, `make test-native` green from the worktree root, biome (or
   the repo's configured formatter for `mobile/src`) clean on touched files. One commit per
   finding, `fix(mobile): …` style; do NOT push, do not amend, do not merge main.
5. Report contract: write `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-39-report.md`
   with RED and GREEN evidence, the predicate you reused or extracted, the refresh-path answer for
   Medium 2, commit SHAs, one-line test summary; return only status, commit SHAs, test summary,
   concerns.

## Task 40: PR #1137 transcript bottom-hold — round 7: measure residual scroll chaining before deciding the nested band

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `a112dfad9` (contains
main `bc1ed2aef`; CI fully green). Frontend dir `cmd/evener-hub/frontend`. File under question:
`src/panes/session/transcript/flow/useTranscriptScroll.ts` (`hasRoomToScroll` ~line 675,
`verticalInputCanMovePort` ~line 685-730) and its test. Browser tooling available in the repo:
the browser-guard harness (`cmd/evener-hub/frontend/scripts/browserGuardCdp.mjs`,
`browserGuardProcess.mjs`, the `transcriptscrollguard` guard, `make test-web-browser`) drives
headless Chrome over CDP.

RoboRev finding at head `a112dfa` (verbatim):

## roborev: Combined Review (`a112dfa`)

## Verdict: One medium-severity issue found around nested scroll-chaining edge cases; otherwise the change is clean.

### Medium

- **Nested scroll chaining can misclassify reader gestures** — `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:675`
  - `hasRoomToScroll` treats any positive space in a nested scroller as if the nested element will consume the entire wheel/touch gesture. When the gesture delta exceeds the remaining space (e.g., `deltaY: 120` with only 3px left), the browser scrolls the nested element to its boundary and chains the residual delta to the transcript port. Since `markGesture` is suppressed, a simultaneous content-growth correction can re-pin the transcript and undo the reader's scroll.
  - Fix: Account for the gesture's remaining displacement when deciding whether the nested scroller can consume it, or defer marking until the port's actual scroll movement is observed. Add a case for a near-boundary nested scroller that chains its residual delta to the transcript port.

### Summary

The PR adds late-measurement bottom re-pinning and source-level gesture tracking for transcript scrolling with reader-gesture vetoing (keyboard, wheel, touch, and pointer markers) and extensive regression coverage. The one open issue is the nested scroll-chaining edge case above; no other problems were identified.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 7m25s*

Coordinator verification and ruling: the finding rests on a browser-behaviour claim I cannot
verify by reading - that when one wheel event's delta exceeds a nested scroller's remaining room,
the browser scrolls the nested element to its limit AND applies the residual to the ancestor port
within that same event. Whether Chrome does that (versus latching the event to the nested
scroller and chaining only on later events) decides which error the current code makes, and the
two errors are not symmetric: not marking when the port does move costs one frame's re-pin over
the reader's residual scroll; marking when the port does not move is a false veto that disarms
the bottom-hold correction until the reader returns to the bottom. So this round MEASURES first
and changes code only on evidence. Cost if the measurement is wrong for another browser: the
comment names Chrome as the measured engine and the band as browser-defined.

Requirements:
1. Spike (throwaway, NOT committed; keep it under the scratchpad directory
   `/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad` or under `/tmp` - never in the worktree): using the same headless Chrome the browser
   guards use (reuse `browserGuardProcess.mjs`/`browserGuardCdp.mjs` helpers or a minimal CDP
   script with the same launch flags), load a page with an `overflow-y:auto` port containing an
   `overflow-y:auto` nested scroller that has exactly 3px of room downward, the port itself with
   plenty of room. Dispatch ONE real wheel event over the nested scroller via CDP
   (`Input.dispatchMouseEvent` type `mouseWheel`, deltaY 120, deltaMode pixels), wait a frame, and
   read both `scrollTop`s. Repeat with the nested scroller having 0px room (control: the port must
   move) and with the nested scroller having 300px room (control: only the nested moves). Record
   the Chrome version and the three results verbatim in the report.
2. If Chrome does NOT move the port within the same event when the nested scroller had room
   (latches to the nested scroller): no code change to marking. Refute the finding in the report
   with the measurement, and extend the comment above `verticalInputCanMovePort` with one
   sentence naming this band: a nested scroller with less room than the delta consumes the event
   in Chrome and chains only on later events, which the next event marks. Commit that comment
   change only.
3. If Chrome DOES move the port within the same event: mark when the input's displacement
   exceeds the nested room - for wheel with `deltaMode === 0` compare `|deltaY|` to the room
   exactly; for line/page delta modes and for touch, treat "some room but less than the movement"
   as "the port may move" and mark; RED-first with a nested scroller 3px from its limit and a
   120px wheel (pre-fix: no marker, correction re-pins over the reader's scroll; post-fix:
   marked). Keep every existing case green; the 3px-room-with-small-delta case from Task 34 must
   still NOT mark when the delta fits in the room. Rewrite the predicate comment so its "a mark
   is a scroll the port will really feel" claim is stated at its real strength.
4. Either way: both gesture test files green, `make test-web` green, biome clean; do NOT push, do
   not amend, do not merge main. One commit.
5. Report contract: append a "Task 40" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-31-report.md` with the spike script
   path, Chrome version, the three measurements verbatim, which branch of the ruling applied and
   why, RED/GREEN if code changed, commit SHA, one-line test summary; return only status, commit
   SHA, the measurement in one line, concerns.

## Task 41: PR #1105 local fork capability — round 8: alias-claim ambiguity, one resolution rule, deletion precedence

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `4922e2b55` (contains main `bc1ed2aef`; CI fully
green). Module: root (`cmd/evener-hub`, `cmd/evener-hub/internal/hubcore`).

RoboRev finding at head `4922e2b` (verbatim; one reviewer, the other found no issues):

## roborev: Combined Review (`4922e2b`)

## Code Review Summary: Three medium-severity gaps in stable-alias fork resolution

### Medium

- **`cmd/evener-hub/app_threadlifecycle.go:1028-1037`** — `forkTargetSessionIDUnderLock` returns the first rendezvous entry matching the requested alias. If multiple daemons claim the same stable workspace ref, selection depends on directory order; the subsequent `ownershipEntry` lookup validates only the selected session, so the fork can silently branch the wrong transcript. Collect all matching claims and reject conflicting current session IDs, or reuse the existing ownership conflict-resolution logic before mutating the transcript.

- **`cmd/evener-hub/app_threadlifecycle.go:811-859, 1040-1045`** — The pre-lock resolver consults only the roster and falls back to the requested alias, while the under-lock resolver also follows `ResumeLocks` redirects. After an explicit resume through a stable alias, the daemon can shut down while `ResolvedSessionID` still maps the alias to the current session; a fork then sees different IDs and is rejected as "session ownership changed" even though nothing changed during the request. Resolve the initial target with the same `ResumeLocks` fallback used by the under-lock resolver, while retaining the recheck to detect changes that occur while waiting.

- **`cmd/evener-hub/app_threadlifecycle.go:808-810`** — The handler refreshes daemon ownership and reports restart/discovery failures before checking the durable deletion fence. A deleted or deletion-in-progress target can therefore return a generic unavailable or restart-required error instead of the terminal `MutationOutcomeTargetDeleted` response. Preserve deletion-fence precedence by checking known deletion state before reporting refresh/restart errors, while still refreshing when needed for non-deleted targets.

### Notes

Review 2 found no issues. The changes align fork capability projection with hub ownership and recovery state, but stable-alias resolution still has ambiguity, redirect, and deletion-precedence gaps worth addressing.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 23m27s*

Coordinator verification and rulings:
- Medium 1 is real, and Task 38's comment on `forkTargetSessionIDUnderLock` was wrong about the
  downstream guard: `ownershipEntry` (app_restart_required.go:160-190) refuses a SESSION ID that
  appears in more than one project dir; it never sees the other daemon that claimed the same
  workspace ALIAS, so first-match-in-directory-order can branch the wrong transcript when two
  local daemons claim one alias. The codebase already treats that shape as real - `resumeClaimTarget`'s
  conflict check (app_threadlifecycle.go:571-580) exists for it. Ruling: collect every local
  rendezvous claim on the alias; if they name more than one distinct current session id, refuse
  with a retryable `Unavailable` (reuse `resumeClaimTarget`'s logic or its error if it fits;
  otherwise the smallest equivalent), and fix the comment so it no longer cites
  `TestHubForkCapabilityAdvertisesAheadOfOwnershipResolution` as covering this.
- Medium 2 is real. `ResumeLocks.resolvedSessionID` is set at resumelocks.go:227 and never
  cleared; after an explicit resume through a stable alias and a graceful daemon shutdown (its
  rendezvous entry removed), the pre-lock resolver (roster miss → the alias itself) and the
  under-lock resolver (no entry → `cmp.Or(ResumeSessionID, ResolvedSessionID)` → the old
  session) disagree in steady state, so every cold fork of that alias is refused with "session
  ownership changed" until the hub restarts. Ruling: one resolution rule. The pre-lock
  resolution keeps the roster first (it also feeds the live-delegate fence) and, when no live
  daemon owns the thread, falls back to the same `ResumeLocks` redirect the under-lock resolver
  uses before answering the alias itself. The under-lock recheck stays as it is.
- Medium 3 is real in principle: the refresh at :808 runs before any deletion check, so a
  deleted target whose refresh fails (restart-required or discovery error) gets a generic error
  instead of the terminal `MutationOutcomeTargetDeleted`. Ruling: read the durable deletion
  state for the requested alias before the refresh (the unlocked read `deletionFenceErrorNaming`
  performs; no lock at that point), return the deletion refusal if it is already known, then
  refresh as today; the locked deletion pass after the locks stays.

Requirements:
1. Medium 1: RED-first with two local rendezvous entries (real files in `cfg.RunDir`) both
   claiming the alias with different current session ids; the fork must refuse without branching
   (`ListSessionMetas` before/after) regardless of directory order (name the files so both
   orders are exercised, as Task 32's check-order test did); a control with one claim proceeds.
2. Medium 2: RED-first: resume-through-alias state (set the redirect the way the resume path
   does, or through `ResumeLocks`' API if a test hook exists), no roster entry, no rendezvous
   entry; a fork through the alias must NOT be refused as "session ownership changed" and must
   branch the session the redirect names. Keep Task 32's and Task 38's recheck tests green
   untouched.
3. Medium 3: RED-first: target durably deleted AND the refresh returning a restart-required or
   discovery error (fault the refresh the way existing tests do); the client must get the
   deletion refusal with `MutationOutcomeTargetDeleted`. A non-deleted target with a failing
   refresh still gets the refresh error.
4. Existing fork/resume/fence tests must pass untouched; if one contradicts a ruling, report why
   before changing anything.
5. Gates: `gofmt -l` clean, `go vet ./cmd/evener-hub/...`, `go test -count=1 ./cmd/evener-hub/...`,
   `-race` over the fork fence tests, pinned golangci-lint 2.13.1 0 issues. One commit per
   finding; do NOT push, do not amend, do not merge main.
6. Report contract: append a "Task 41" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` (same lane) with RED and
   GREEN evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 42: PR #1137 transcript bottom-hold — round 8: middle-button autoscroll marks, zoom wheels do not

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `87f4944e1` (contains
main `bc1ed2aef`). CI on this head: the `tests` job failed on `TestRunServe_StreamErrorPublishesIdleStatus`
in `cmd/evener` (a Go test this frontend-only PR cannot touch; handled as its own flake lane, Task 43).
Frontend dir `cmd/evener-hub/frontend`. File under change:
`src/panes/session/transcript/flow/useTranscriptScroll.ts` (`startPointerDrag` ~line 851,
`markWheel` ~lines 889-895) and its test.

RoboRev finding at head `87f4944` (verbatim):

## roborev: Combined Review (`87f4944`)

## Summary Verdict

2 issues found (1 Medium, 1 Low) in gesture-tracking edge cases; core bottom-hold correction logic is sound.

---

### Medium

- **`cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:851`** — Pointer tracking only accepts `button === 0`, so native middle-button autoscroll (`button === 1`) is never marked as a reader gesture. A same-frame measurement correction can therefore satisfy the bottom-hold predicate and snap the transcript back to the bottom while the user is autoscrolling. Track auxiliary/middle-button drags as well, while continuing to ignore secondary/context-menu buttons.

### Low

- **`cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:889-895`** — Every wheel event with nonzero `deltaY` is marked before its default action. `Ctrl`-wheel gestures used for browser/page zoom do not scroll the transcript but can still veto a same-frame correction and incorrectly mark the reader away from the bottom. Ignore non-scrolling modified wheel events (at minimum `event.ctrlKey`) and honor events already marked `defaultPrevented`.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 7m27s*

Coordinator verification and rulings:
- Medium is real. `startPointerDrag` accepts only `button === 0`; native middle-button autoscroll
  (Windows/Linux Chrome: press the middle button, move, the port scrolls continuously) delivers
  pointer events with the middle button held, and its scroll events are real reader scrolling.
  Unmarked, every same-frame correction during an autoscroll re-pins over the reader - not a
  one-frame cost, because autoscroll produces a stream of scroll events. Ruling: a drag begins for
  the primary OR the middle button (`button === 0 || button === 1`); the secondary button and
  touch stay excluded. Cost if wrong: on a platform where the middle button does nothing, a
  middle-button drag over the transcript marks a gesture that moves nothing (a false veto only if
  a correction lands in that same frame) - the same trade the pointer path already makes for a
  selection drag, and stated in the comment.
- Low is real. A wheel with `ctrlKey` is browser zoom (and a macOS trackpad pinch arrives as a
  ctrl-wheel); it never scrolls the port, so marking it is over-marking. Ruling: `markWheel`
  returns early for `event.ctrlKey` and for `event.defaultPrevented` (a descendant handler that
  already claimed the wheel). Other modifiers stay as they are (shift-wheel is a horizontal
  scroll on most platforms and already has `deltaY === 0`).

Requirements:
1. Medium: RED-first with a `PointerEvent` drag using `button: 1`, `buttons: 4` that moves the
   port while a correction lands: pre-fix the correction re-pins (reader's scroll undone),
   post-fix it is vetoed. Keep the right-button case (`button: 2`) not marking.
2. Low: RED-first with a `WheelEvent` carrying `ctrlKey: true` and a nonzero `deltaY` over a port
   with room: pre-fix a same-frame correction is vetoed, post-fix it re-pins. Same for a wheel
   dispatched with `cancelable: true` and `preventDefault()` called by a descendant listener
   before it reaches the port.
3. Update the marker-exactness comment above `startPointerDrag` (which buttons count and why) and
   the wheel line in it (zoom wheels and claimed wheels do not count). No comment about what used
   to be there.
4. Existing tests untouched and green; both gesture test files green; `make test-web` green;
   biome clean. One commit; do NOT push, do not amend, do not merge main (main has not moved).
5. Report contract: append a "Task 42" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-31-report.md` with RED and GREEN
   evidence, commit SHA, one-line test summary; return only status, commit SHA, test summary,
   concerns.

## Task 43: CI flake — `TestRunServe_StreamErrorPublishesIdleStatus` reads "active" after a stream error

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-serve-idle`
Branch: `claude/fix-serve-stream-error-idle-flake`, created from `origin/main` (`bc1ed2aef`). Module:
root (`cmd/evener`). This is a flake lane: per Jesse's standing instruction, the implementer pushes
its own branch and opens a PR against `main` when the fix is verified (the one exception to the
"coordinator pushes" rule).

Signature (CI `tests` job on PR #1137 head `87f4944`, a frontend-only change that cannot touch this
code; full job log at
`/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-1137-87f4944-tests.log`):

```
2026-09-11T18:38:12.3242272Z --- FAIL: TestRunServe_StreamErrorPublishesIdleStatus (0.22s)
2026-09-11T18:38:12.3244105Z     serve_state_test.go:601: thread/read = status "active", capabilities {Send:false Steer:true Interrupt:true Compact:true Clear:false ForkFromTurn:false Shutdown:true ChangeModel:true ChangeVisionModel:true Queue:true Goal:true Rename:true}; want idle, send enabled, queue and interrupt disabled
```

The test is at `cmd/evener/serve_state_test.go:511` (assertion at :601); the file was last touched
by #1133 (`bf67c454f`, the serve stable-identity flake fix) and by #936. The serve log around the
failure shows the injected `provider error: openai error: stream ended without finish event` for
the test's session, then the `thread/read` observed `status "active"` where the test expects the
idle status the stream error should have published.

Requirements:
1. Root-cause first, per the repo's rule that a sighted flake gets root-caused, not retried. Read
   the test, the serve status publication path the stream error takes, and the fix #1133 made to
   this file for how it gated on the daemon's turn claim. Decide whether the race is in the test
   (reading before the publication it waits for, or waiting on the wrong signal) or in serve (the
   idle status published after `thread/read` can already observe the post-error state). State
   which, with file:line evidence, in the report.
2. Reproduce before fixing: run the test under `-race -count=N` and/or with CPU pressure
   (`GOMAXPROCS`, `-cpu`) until the failure shows at least once, or explain why it cannot be
   provoked locally and what evidence supports the root cause instead. Never use
   `go clean -testcache`/`-cache`.
3. Fix the root cause with the smallest change: if the test waits on the wrong signal, make it
   wait on the publication it asserts (the way #1133 did), without weakening what it proves; if
   serve publishes idle late, fix the ordering in serve, RED-first. No bare wall-clock deadlines
   in agent tests without a `// TRIPWIRE:` comment; the deadline audit
   (`TestNoBareWallClockDeadlineInAgentTests`) must pass. Do not weaken, delete, or re-point the
   test's assertions.
4. Verify: the focused test under `-race -count=50` green; `go test -count=1 ./cmd/evener/...`
   green; `gofmt -l` clean; `go vet ./cmd/evener/...`; pinned golangci-lint 2.13.1 0 issues.
5. Commit in the repo's conventional style (`fix(serve): …` or `test(serve): …`), no attribution
   trailers; push the branch to origin; open a PR against `main` with `gh pr create` whose body
   states the signature, the root cause with file:line, the fix, and the reproduction evidence.
   No attribution lines in the PR body. Do not merge; do not touch any other branch or worktree.
6. Report contract: write `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-43-report.md`
   with the root cause, reproduction evidence, the fix, gate output tails, commit SHA, and the PR
   number/URL; return only status, PR number, commit SHA, one-line test summary, concerns.

## Task 44: PR #1096 native checkpoint — round 3: one identity for truncation ownership, web-parity failure predicate

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096). Current head `ca3df883c` (contains main
`bc1ed2aef`; CI fully green). Shared mobile core `mobile/src` (`make test-native`; no formatter
config applies to `mobile/src`, match surrounding style by inspection). Files under change:
`mobile/src/state/conversation.ts` (truncation ownership sites the finding lists) and its test;
`mobile/src/conversation/project.ts` (`toolCallFailed`, ~line 113) and its test.

RoboRev finding at head `ca3df88` (verbatim):

## roborev: Combined Review (`ca3df88`)

## Verdict: Two findings — one medium truncation-ownership inconsistency, one low failure-classification parity gap.

---

### Medium

- **Truncation ownership uses inconsistent item identity** — `mobile/src/state/conversation.ts:68-69, 1151-1156, 1183-1184, 2889-2895, 2959-2965`
  Truncation ownership is stored under `transcriptKey`, but live delta guards and reset/lifecycle cleanup use the wire `itemId`. Since the protocol permits these IDs to differ, a truncated tool item with oversized arguments or error text can still accept a subsequent tool-output delta; rehydrate and replacement paths can also retain stale truncation ownership.
  **Fix:** Resolve incoming wire IDs to the canonical item identity, or consistently track both `id` and `transcriptKey` for every `has`, `add`, and `delete` operation. Add a behavioral test combining differing IDs, truncation of a non-delta field, and a later live delta.

---

### Low

- **Failure classification diverges from web** — `mobile/src/conversation/project.ts:113`
  `toolCallFailed()` claims parity with web `hasItemFailure()` but omits `status === "failed" || status === "interrupted"` and uses untrimmed `error !== ""` while web uses `error.trim() !== ""` (`cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts:125`). A `commandExecution` with `status: "failed"` and no `error`/`exitCode` projects as `completed` on mobile but `failed` on web.
  **Fix:** Mirror web predicate: check trimmed error plus failed/interrupted status in addition to nonzero `exitCode`.

---
*Reviewers: 2 done | Synthesis: codex, 21s | Total: 16m46s*

Coordinator verification and rulings:
- Medium is real. `timelineIdentity(item)` (state/conversation.ts:68-70) is `transcriptKey ?? id`,
  and the projection and rehydrate paths add THAT to `truncatedItemIds` (:1151-1156, :1183-1184),
  while both live delta handlers guard and add with the wire `params.itemId` (:2889-2895,
  :2959-2965). When an item's `transcriptKey` differs from its `id`, ownership recorded under the
  key never blocks a delta keyed by the id, and the reset/lifecycle cleanup keyed by id never
  clears ownership recorded under the key. Ruling: one canonical identity - the timeline
  identity - for every `has`/`add`/`delete` on `truncatedItemIds`. The delta handlers already
  look up `existing` by wire id; use `timelineIdentity(existing)` there. Audit every other
  `truncatedItemIds` access (grep) and convert the same way; list each site with before/after
  in the report. Cost if wrong: a truncation guard keyed one way and cleared another, which is
  the bug being fixed.
- Low is real. `toolCallFailed` (project.ts:113) claims parity with web `hasItemFailure`
  (cmd/evener-hub/frontend/src/transcriptDisplay/projector.ts:118-130) but omits
  `status === "failed" || status === "interrupted"` and does not trim the error. Ruling: mirror
  the web predicate exactly (trimmed error, failure status, nonzero exit code) and update the
  comment so the parity claim is true.

Requirements:
1. Medium: RED-first behavioural store test: an item whose `transcriptKey` differs from its
   `id`, truncated on a NON-delta field (oversized arguments or error text) at projection, then
   a live tool-output delta for that wire id; pre-fix the delta is accepted, post-fix it is
   refused by the frozen guard. Add a second case for the cleanup path: after the reset/lifecycle
   event that is supposed to release ownership, a fresh item reusing the identity is not
   frozen. Then the identity change at every site.
2. Low: RED-first projection test: a `commandExecution` with `status: "failed"` and no
   `error`/`exitCode` projects as failed; a whitespace-only `error` does not count as a failure
   (trim parity); `status: "interrupted"` counts. Then the predicate.
3. No existing expectation weakened or re-pointed; if an existing test contradicts a ruling,
   report why before changing anything.
4. Gates: touched test files green; `make test-native` green from the worktree root. One commit
   per finding, `fix(mobile): …`; do NOT push, do not amend, do not merge main.
5. Report contract: append a "Task 44" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-39-report.md` (same lane) with RED
   and GREEN evidence, the site-by-site identity table, commit SHAs, one-line test summary;
   return only status, commit SHAs, test summary, concerns.

## Task 45: PR #1100 round timings — round 5: replay-tail copies without a durable compaction anchor

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay`. Current head `7be31b7ed` (contains main `bc1ed2aef`; main
has since moved to `b10be62a0` - the coordinator merges it before the push, do not merge it
yourself). Module: `agent/` (own module).

RoboRev finding at head `7be31b7` (verbatim):

## roborev: Combined Review (`7be31b7`)

## Summary Verdict: One medium-severity issue found regarding replay-tail persistence when no durable compaction anchor exists.

### Review Findings

**Medium**

- **`agent/session_compaction.go:219-224`**, **`agent/transcript_read.go:157-165`** — Replay-tail entries are appended even when the fold produces no durable `CHECKPOINT` or `SUMMARY` marker (e.g., short/no-op compaction or when marker writes fail). `ResumeHistory` only anchors on those markers; without one, it clears `ContextReplay` but retains both the original entries and the replay copies, duplicating conversation and tool history after restart or delegate fork.
  - **Suggested fix**: Only append replay-tail copies after at least one replacement marker has been written successfully, or make `ResumeHistory` discard `ContextReplay` entries when no durable compaction anchor exists.

### Clean Areas

- Durable round timing, environment context, compaction layers, and turn-ownership metadata persistence (no issues flagged by either reviewer).
- Live and cold turn grouping around self-minted and gap identities (no issues flagged).
- Environment context replay across compaction and restore (no issues flagged).

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 37m17s*

Coordinator verification and ruling: the shape is real on the read side. `publishFoldedHistory`
writes every `rewriteTail` turn durably with `ContextReplay = true` after
`commit.commitTranscriptsLocked()` (session_compaction.go:217-224; the loop predates this PR, the
`ContextReplay = true` stamp is this PR's), and `ResumeHistory` (transcript_read.go:145-183)
anchors only on the last `CHECKPOINT`/`SUMMARY`; in the no-anchor branch it returns EVERY entry
with `ContextReplay` cleared, so if a transcript ever holds replay copies without a later marker
the originals and the copies both come back. `TestCompactionReplay_ResumeHistoryRetainsReplayCopies`
pins the anchored case only. Ruling: fix the read side - in the no-anchor branch `ResumeHistory`
drops entries whose `ContextReplay` is set (they are copies by construction; their originals are
in the same list) - and determine on the write side whether any fold path can write a tail
without having landed a marker: if one exists (a no-op fold with a non-empty tail, or marker
writes failing while tail writes succeed), guard the tail writes on a landed marker as well,
RED-first; if none exists, say so with file:line evidence and leave the write side alone. Cost if
wrong: a replay copy that was legitimately the only surviving record of a turn (which the design
does not produce - copies are always duplicates of retained originals) would be dropped on a
resume with no anchor.

Requirements:
1. Read side, RED-first: a transcript with original turns, replay copies of some of them
   (`ContextReplay: true`), and NO compaction marker; `ResumeHistory` must return the originals
   once, with no duplicated conversation or tool history. Keep
   `TestCompactionReplay_ResumeHistoryRetainsReplayCopies` and the three
   `TestResumeHistoryFromTranscript_*` tests green untouched; the anchored branch's behaviour must
   not change.
2. Write side: trace `commitTranscriptsLocked` and every caller of `publishFoldedHistory` for a
   path that reaches the tail loop with no marker written (`rewriteTail` non-empty while the fold
   produced no `CHECKPOINT`/`SUMMARY`, or marker write errors). State the answer with file:line in
   the report; add the guard RED-first only if such a path exists.
3. Cold projection parity check: say (with file:line) whether `internal/apptranscript`'s replay
   anchors the same way and would duplicate too; do NOT change the root module in this task - if
   it would, report it for a follow-up.
4. Gates: `gofmt -l` clean; `go vet ./...` in `agent/`; `go test -count=1 ./...` in `agent/`;
   deadline audit; `-race` on the focused tests; pinned golangci-lint 0 issues. One commit per
   side changed; do NOT push, do not amend, do not merge main.
5. Report contract: append a "Task 45" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-33-report.md` (same lane) with RED and
   GREEN evidence, the write-side trace, the projection parity answer, commit SHAs, one-line test
   summary; return only status, commit SHAs, test summary, concerns.

## Task 46: PR #1137 transcript bottom-hold — round 9: shift-wheel is horizontal, a held middle button is a standing gesture

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `39e293b5f` (contains
main `bc1ed2aef`; main has since moved to `b10be62a0` - the coordinator merges it before the push, do
not merge it yourself). Frontend dir `cmd/evener-hub/frontend`. File under change:
`src/panes/session/transcript/flow/useTranscriptScroll.ts` (`startPointerDrag`/`continuePointerDrag`/
`endPointerDrag` ~lines 870-899, `markWheel` ~line 918, `handleScroll`'s consume of
`gesturePendingRef`) and its test.

RoboRev finding at head `39e293b` (verbatim; Review 2 found no issues):

## roborev: Combined Review (`39e293b`)

**Verdict: Mostly clean with two minor edge cases in gesture classification.**

Reviewers 1 and 2 assessed the same change. Review 2 found no issues; Review 1 found two edge cases. No overlapping findings to deduplicate.

---

### Medium

- **`useTranscriptScroll.ts:918`** — `markWheel` treats every non-zero `deltaY` as vertical transcript movement. Browsers commonly report a non-zero `deltaY` for `Shift`+wheel while performing horizontal scrolling; this port clips horizontal overflow, so no transcript scroll occurs, yet the gesture marker can veto a subsequent measurement correction and permanently show the reader as away from the bottom. Fix: detect and ignore wheel input whose effective axis is horizontal, including `Shift`+wheel on supported platforms, and add a behavioral regression test.

### Low

- **`useTranscriptScroll.ts:870-899`** — Middle-button autoscroll is marked only when the pointer moves. Native autoscroll can continue while the pointer is stationary, leaving `gesturePendingRef` clear; if live content grows during that active autoscroll, the resulting scroll event can be misclassified as a bottom-hold correction and snap the reader back. Fix: track middle-button autoscroll separately and recognize its ongoing scroll activity, including the stationary-pointer phase, without making ordinary selection drags permanent vetoes.

---
*Reviewers: 2 done | Synthesis: codex, 6s | Total: 7m32s*

Coordinator verification and rulings:
- Medium: the port is `overflow-x: clip` (widgets/virtuallist/virtuallist.module.css:11), so no
  horizontal input ever moves it, and Firefox is known to deliver Shift+wheel as a non-zero
  `deltaY` with `shiftKey` set while scrolling horizontally. Task 42's brief assumed shift-wheel
  always arrives with `deltaY === 0`; that assumption is not safe across engines. Ruling: treat a
  wheel with `shiftKey` as horizontal and do not mark it. This is the under-marking direction in
  any engine that applies shift-wheel vertically (one frame's re-pin) and closes a permanent false
  veto in the engines that apply it horizontally, so no measurement is needed to pick the safe side.
- Low: this is the stationary-autoscroll residual Task 42 disclosed, and the ruling changes,
  scoped: last round rejected a standing veto because it lumped the primary and middle buttons
  together. Scoped to the MIDDLE button only, the trade inverts - on Windows/Linux a held middle
  button IS continuous reader scrolling, and leaving it unmarked snaps the reader back on every
  correction during streaming, the fights-the-user failure in the exact common use; on macOS a
  middle-button hold does nothing, and a false veto needs the reader to press and hold the middle
  button over the transcript while content grows, which is rare. Ruling: track a held middle
  button as an autoscroll in progress (set on `pointerdown` with `button === 1`, non-touch; cleared
  on `pointerup`/`pointercancel`/`pointerleave` and on a move with `buttons` lacking the middle
  bit), and let `handleScroll` treat any scroll event while it is set as a reader gesture, in
  addition to the consumed marker. The PRIMARY button keeps the moving-only marking; a selection
  drag must not become a standing veto. Cost if wrong: the macOS shape above.

Requirements:
1. Medium, RED-first: a `WheelEvent` with `shiftKey: true` and a nonzero `deltaY` over a port
   with room; pre-fix a same-frame correction is vetoed, post-fix it re-pins. Keep the horizontal
   (`deltaX` only) case and the ctrl/defaultPrevented cases green.
2. Low, RED-first: middle button down (`button: 1, buttons: 4`), one move, then NO further
   pointer events while content grows and a correction lands; pre-fix the correction re-pins
   (reader snapped back), post-fix it is vetoed and the pill is offered. Control: the same
   sequence with the primary button (`button: 0, buttons: 1`) still re-pins (a held primary button
   is not a standing veto). Also: after `pointerup` the standing state is clear, and a later
   correction re-pins.
3. Update the comment above `startPointerDrag` and the marker-exactness summary so the middle
   button's rule (standing while held) and the primary button's (moving only) are both stated with
   their trades; the wheel line names shift-wheel as horizontal. No comment about what used to be.
4. Existing tests untouched and green; both gesture test files green; `make test-web` green;
   biome clean. One commit per finding; do NOT push, do not amend, do not merge main.
5. Report contract: append a "Task 46" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-31-report.md` with RED and GREEN
   evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test summary,
   concerns.

## Task 47: PR #1098 environment replay — round 4: a poisoned writer fails closed at turn admission; dirty after a landed line

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`
Branch: `codex/mobile-transcript-identity`. Current head `184dee1f7` (Task 26 commits plus a merge of
main `b10be62a0`; CI fully green). Module: `agent/` (own module; `agent/transcript` inside it).

RoboRev finding at head `184dee1` (verbatim; the previous head `7d3a1d4`, identical code plus a
merge-only commit, was reviewed "No issues found" - one reviewer reported clean this time too):

## roborev: Combined Review (`184dee1`)

**Verdict:** Two High and one Medium issue found; one reviewer reported clean.

## High

- **Missing module dependency for `ulid/v2`** — `agent/session.go:14`, `go.mod`
  The new `github.com/oklog/ulid/v2` import is not declared in `go.mod` or vendored, so the package cannot be resolved at compile time. Add it to `go.mod` and update `go.sum`, or use an existing in-repo ID generator.

- **Poisoned writer silently drops transcript turns** — `agent/transcript/transcript.go:454-496`, `agent/session.go:1849-1859`
  After a failed write transfers a complete JSONL entry, the writer is poisoned. `recordTurn` then ignores `ErrWriterPoisoned` and only logs a warning, so subsequent user and assistant turns continue in memory but are silently omitted from the durable transcript. Reopen or reconcile the poisoned writer before continuing, or propagate the error from `recordTurn` to abort processing so turns cannot proceed without durable persistence.

## Medium

- **Dirty flag not set after failed buffered write** — `agent/transcript/transcript.go:489-496`
  When the buffered write path transfers the entire line and then returns an error, `poisonLandedBytesLocked` records the entry but never sets `w.dirty`. `Close` therefore skips its sync, leaving the entry counted and readable in-process without the close-time durability guarantee. Mark the writer dirty whenever any bytes (especially a complete line) remain after a failed buffered write so `Close` attempts the required sync.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 13m4s*

Coordinator verification and rulings:
- High 1 (missing `ulid/v2` dependency) is REFUTED: `agent/` is its own Go module and
  `agent/go.mod:9` declares `github.com/oklog/ulid/v2 v2.1.1`; the import at `agent/session.go:14`
  resolves, and CI's `tests`/`race-modules` jobs compiled and passed on this head. Do not change
  anything for it; the coordinator posts the refutation.
- High 2 is a real policy gap this PR sharpened. `recordTurn` (session.go:1848-1859) has no error
  return; it warns and continues, which was tolerable when a write failure lost one record. With
  poisoning, EVERY later record is lost for the rest of the session while turns keep running, and
  the environment path already fails every turn loudly under the same condition
  (`TestEnvironmentPoisonedWriterFailsEveryTurnLoudly`) - but only when there is an environment
  block to append, so a turn with an unchanged environment proceeds with only warnings. Ruling:
  fail closed at turn admission. When the transcript writer is poisoned, the next input is
  refused with `ErrWriterPoisoned` (wrapped) before the turn runs, the same visible failure the
  environment path produces, instead of running a turn whose records cannot be persisted. Keep
  `recordTurn`'s warn-and-continue for transient errors (not this PR's policy to change) and do
  not change its signature. Choose the smallest admission point (the place every input passes
  through before a turn starts; read `processInputKindWithProvenance` / `acceptUserInput`) and
  expose the writer's poisoned state through a nil-safe accessor rather than reaching into the
  struct. Cost if wrong: a session with a dead transcript stops accepting input until restarted,
  which is the safe failure - the alternative is silent history loss on the next restart.
- Medium is real: the buffered door's failure path (transcript.go:489-496 →
  `poisonLandedBytesLocked`) counts a landed whole line but never sets `w.dirty`, so `Close` skips
  its sync for a record the writer counted. Ruling: mark the writer dirty whenever bytes landed
  (`written > 0`) on that path so `Close` attempts the sync.

Requirements:
1. High 2, RED-first: poison the writer (the fault-plan shape the Task 26 tests use), keep the
   environment unchanged so no environment block is due, submit an input; pre-fix the turn runs
   and only a warning is emitted, post-fix the input is refused with an error wrapping
   `ErrWriterPoisoned` and no model request is made. The existing loud-failure test stays
   untouched; if any existing test expects a turn to proceed with a poisoned writer, report it
   before changing anything.
2. Medium, RED-first: a buffered `Append` whose write lands the whole line then fails; `Close`
   must attempt `Sync` (observe through the fault plan's op sequence); pre-fix it does not.
3. Gates: `gofmt -l`; `go vet ./...` in `agent/`; `go test -count=1 ./...` in `agent/`; deadline
   audit; `-race` on the focused tests; pinned golangci-lint 0 issues. One commit per finding;
   do NOT push, do not amend, do not merge main.
4. Report contract: append a "Task 47" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-26-report.md` (same lane) with RED and
   GREEN evidence, the admission point chosen and why, commit SHAs, one-line test summary; return
   only status, commit SHAs, test summary, concerns.

## Task 48: PR #1105 local fork capability — round 9: deletion fence in the projection, one liveness rule for both resolvers

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `0ccc197a2` (Task 41 commits plus a merge of main
`b10be62a0`; CI fully green). Module: root (`cmd/evener-hub`).

RoboRev finding at head `0ccc197` (verbatim):

## roborev: Combined Review (`0ccc197`)

## Summary Verdict: Two medium-severity findings on fork capability consistency; no critical or high issues.

---

### Medium

- **`cmd/evener-hub/app_threadread.go:482-503`** — `applyHubForkCapability` never consults `DeletionStore`, so a thread that is already durably fenced for deletion can still advertise `ForkFromTurn: true` in list/read/status responses. `hubThreadFork` rejects the same request via `deletionFenceError`, leaving the UI with an enabled action that can never succeed. **Fix:** Include deletion-fence checks for the requested ref and any resolved session identity before advertising the fork capability.

- **`cmd/evener-hub/app_threadlifecycle.go:1036-1058` and `1101-1114`** — The pre-lock resolver ignores retained rendezvous entries unless the roster still considers them live, while the under-lock resolver considers all rendezvous claims, including crash-retained markers. After a daemon clears to a new session and then crashes, the first resolution can select the stable alias while the second selects the marker's current session, causing every fork through that alias to fail with "session ownership changed" even though the capability projection treats the stopped session as forkable. **Fix:** Use the same authority and filtering rules for both resolutions—either resolve retained crash markers before locking or exclude them consistently—so a stable ref cannot disagree with itself without an actual ownership change.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 10m1s*

Coordinator verification and rulings:
- Medium 1 is real: `applyHubForkCapability` (app_threadread.go:482-503) consults ownership
  errors, unconfirmed daemons, storage and `hubCanForkThread`, never the `DeletionStore`, so a
  thread durably fenced for deletion advertises `ForkFromTurn: true` while `hubThreadFork` refuses
  it. Ruling: the projection consults the same unlocked deletion read the fork's pre-refresh check
  uses (Task 41's `deletionFenceError` read) for the requested ref and, when it resolves one, the
  resolved session; a fenced thread advertises `ForkFromTurn: false`. Keep it a read, no lock.
- Medium 2 is real and is the divergence Task 41's "one rule" missed: the pre-lock resolver
  reaches the rendezvous through the roster, which excludes `Crashed` entries
  (`liveDaemonForThread`), while `forkTargetSessionIDUnderLock` (:1036-1058) collects every local
  claim on the alias including crash-retained markers. After a daemon clears to a new session and
  crashes, the two disagree and every fork through the alias fails "session ownership changed".
  Ruling: one liveness rule - the under-lock collection skips claims the roster would not treat
  as live (the crash-retained marker shape `resumeClaimTarget` already recognises as "not a live
  owner"); if it cannot decide liveness from the entry alone, prefer refusing with the existing
  ambiguity error over disagreeing. State the rule in one place both resolvers cite.

Requirements:
1. Medium 1, RED-first: a thread durably fenced for deletion in the `DeletionStore`; `thread/list`
   and `thread/read` must project `ForkFromTurn: false` (pre-fix true); a control thread without a
   fence still advertises it.
2. Medium 2, RED-first: daemon clears to a new session, then a crash-retained rendezvous marker
   remains while the roster shows nothing live for the alias; a fork through the alias must not be
   refused as "session ownership changed" (pre-fix it is); the fork resolves to the same session
   both before and under the locks. Task 32/38/41 recheck tests stay green untouched.
3. Gates: `gofmt -l`; `go vet ./cmd/evener-hub/...`; `go test -count=1 ./cmd/evener-hub/...`;
   `-race` over the fork fence tests; pinned golangci-lint 0 issues. One commit per finding; do
   NOT push, do not amend, do not merge main.
4. Report contract: append a "Task 48" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` (same lane) with RED and
   GREEN evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 49: PR #1137 transcript bottom-hold — round 10: a hidden document clears the pending marker too

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-scrollguard`
Branch: `claude/fix-transcriptscrollguard-pill-flake` (PR #1137). Current head `16406565c` (contains
main `b10be62a0`; main has since moved to `f21e1c595` - the coordinator merges it before the push, do
not merge it yourself). Frontend dir `cmd/evener-hub/frontend`. File under change:
`src/panes/session/transcript/flow/useTranscriptScroll.ts` (`markGesture` ~832-839, the
`endAutoscrollWhenHidden` handler ~947-948) and its test.

RoboRev finding at head `1640656` (verbatim; Review 2 found no issues):

## roborev: Combined Review (`1640656`)

## Verdict: One medium-severity issue found — one reviewer flagged a gesture-state leak on tab visibility change; the other reviewer found no issues.

### Medium

- **`cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts:832-839, 947-948`**
  - A pointer drag or wheel can set `gesturePendingRef` even when no transcript scroll event is produced. If the document becomes hidden before the scheduled `requestAnimationFrame` runs, `endAutoscrollWhenHidden` clears only `middleButtonHeldRef`; the pending gesture and frame handle remain. A later content-measurement scroll — including the first frame after returning to the tab — can therefore be misclassified as reader input, skipping the bottom correction and permanently marking the reader as away from the bottom.
  - **Fix**: Clear `gesturePendingRef` and cancel/reset `gestureClearFrameRef` when the document becomes hidden. Add a regression test for a non-scrolling gesture followed by hidden-tab content growth.

### Reviewer Agreement

| Reviewer | Verdict |
|----------|---------|
| Review 1 | 1 Medium issue found |
| Review 2 | Clean — no issues found |

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 5m42s*

Coordinator verification and ruling: real, narrow, and the same shape Task 46 just closed for the
middle-button state. The pointer-drag path marks without producing a scroll (the least exact marker,
deliberately kept), the clearing frame does not run while the document is hidden (the markGesture
comment already says so), and `endAutoscrollWhenHidden` clears only `middleButtonHeldRef`, so a
marker set by a non-scrolling gesture just before the tab goes hidden survives until the first
scroll event after return - which, after content grew meanwhile, is the measurement correction it
then vetoes. Ruling: when the document becomes hidden, clear `gesturePendingRef` and cancel and
null the clearing frame handle alongside the middle-button state (one handler, the same rule Task
37 applies on a session switch). Do not touch `pointerDraggingRef` or `lastTouchYRef` for this:
they act only on a later input event that carries its own state. Cost if wrong: a real gesture
whose scroll event lands after the document is hidden goes unmarked - under-marking, the safe
direction, and the frame boundary already made that trade.

Requirements:
1. RED-first: a primary-button pointer drag over the port (marks, no scroll event), then
   `visibilitychange` with `visibilityState` hidden, then content growth and a measurement
   correction; pre-fix the correction is vetoed (`expected 16577 to be 16664`), post-fix it
   re-pins. Keep the visible-edge control from Task 46 (a change back to visible clears nothing)
   and add its counterpart for the marker if the harness makes it one more case.
2. Fold the clear into the existing hidden-document handler (rename it if its name now
   under-describes what it clears); the frame handle is cancelled and nulled with the same
   `!== null` guard the per-ref reset uses. Update the markGesture comment's "that frame may never
   arrive" paragraph so the hidden-document clear is named as one of the marker's bounds.
3. Existing tests untouched and green; both gesture test files green; `make test-web` green;
   biome clean. One commit; do NOT push, do not amend, do not merge main.
4. Report contract: append a "Task 49" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-31-report.md` with RED and GREEN
   evidence, commit SHA, one-line test summary; return only status, commit SHA, test summary,
   concerns.

## Task 50: PR #1100 round timings — round 6: mint the preseed id without adopting it

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay`. Current head `dfef0a3d3` (contains main `b10be62a0`; main
has since moved to `f21e1c595` - the coordinator merges it before the push, do not merge it
yourself). Module: `agent/` (own module).

RoboRev finding at head `dfef0a3` (verbatim):

## roborev: Combined Review (`dfef0a3`)

**Summary:** One medium-severity issue found — failed preseed setup can leave stale turn ownership on retained delegate sessions.

---

## Review Findings

**Medium**

- `agent/delegate_runtime.go:2212` — `nameTurnItself()` mutates `child.directTurnID` before the durable append and transcript readback succeed. Every failure after that point returns without clearing the ID, while the failed delegate runtime is retained without launching a run. Later idle compaction or recovery records can therefore use an abandoned `turn_direct_*` owner, producing orphaned or incorrectly grouped live and cold transcript items.
  - **Fix:** Mint the ID without adopting it until preseed validation succeeds, or conditionally clear `directTurnID` on every failure path when it still equals the preseeded ID.

---

**Otherwise:** Reviewers agree the change correctly adds durable round-timing, compaction presentation turns with live/cold parity, durable environment context, and self-minted turn identity across delegates, continuations, and compaction folds. No other issues found.

---
*Reviewers: 2 done | Synthesis: codex, 5s | Total: 24m9s*

Coordinator verification and ruling: real. `preseedInput` (delegate_runtime.go:2203-2231) calls
`child.nameTurnItself()`, which mints AND adopts (session_active_turn.go:170-174 →
`adoptSelfMintedTurnID` sets `s.directTurnID`) before the durable append and the strict readback;
every failure return after that line leaves `child.directTurnID` set on a runtime that
`retainAdoptedWithoutLaunch` keeps without a run, and `activeTurnOwner` (session_queue.go:1063)
prefers `directTurnID`, so any later record that session publishes is attributed to a turn that
never ran. The adoption in `preseedInput` is also redundant: Task 33 made the executing run adopt
the preseeded id itself when it starts (`acceptUserInput` → `adoptSelfMintedTurnID` via
`delegatePreseededTurnID`). Ruling: `preseedInput` mints only. Split the mint from the adoption
(e.g. a `mintDirectTurnID()` that `nameTurnItself` also uses), stamp the minted id on the entry and
return it, and leave adoption to the run start that already performs it. Do not add clears on the
failure paths - with no adoption there is nothing to clear. Cost if wrong: if any code between
preseed and run start read `directTurnID` expecting the preseeded id, it would now see empty;
verify there is none (`maybeAppendEnvironmentContext` runs before the mint).

Requirements:
1. RED-first: a preseed whose durable append (or readback) fails must leave the child's
   `directTurnID` empty (pre-fix it holds the minted id); drive it through the delegate path the
   existing preseed tests use, or a focused test of `preseedInput` with a faulted transcript.
2. The success path is unchanged for observers: `TestDelegatePreseededTurnID*` and Task 33's
   agent-layer parity pin (`delegate_preseed_turn_identity_test.go`) stay green untouched, and
   the run still adopts the id at start (assert it in the new test's success control if not
   already pinned).
3. Trace every reader of `directTurnID` between preseed and run start and state with file:line
   that none expects it set; if one does, report before changing anything.
4. Gates: `gofmt -l`; `go vet ./...` in `agent/`; `go test -count=1 ./...` in `agent/`; deadline
   audit; `-race` on the focused tests; pinned golangci-lint 0 issues. One commit; do NOT push,
   do not amend, do not merge main.
5. Report contract: append a "Task 50" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-33-report.md` (same lane) with RED and
   GREEN evidence, the reader trace, commit SHA, one-line test summary; return only status,
   commit SHA, test summary, concerns.

## Task 51: PR #1105 local fork capability — round 10: recovery and delegate fences for both identities in the projection; deterministic test ids

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `ee0d6f794` (contains main `f21e1c595`; CI fully
green after a runner-side rerun). Module: root (`cmd/evener-hub`).

RoboRev finding at head `ee0d6f7` (verbatim):

## roborev: Combined Review (`ee0d6f7`)

**Verdict:** Fork capability projection advertises forks the RPC can reject due to a missing recovery/delegate fence on the resolved session, and one test relies on nondeterministic ID ordering.

## Critical
None.

## High
None.

## Medium

- **`cmd/evener-hub/app_threadread.go:446-467`** — Fork capability projection checks recovery and live-delegate fences only for the requested alias, but `hubThreadFork` resolves stable workspace aliases to a current session and fences both identities. Likewise `hubForkDeletionFenced` checks both, while `hubForkRecoveryFencedNow` only checks `ResumeLocks.RecoveryState` for the alias. A stable-ref client can therefore be advertised `ForkFromTurn=true` when the session the fork would branch is `ResumeRequired` or `Stopping > 0`, only to have the RPC reject it. **Fix:** Resolve `forkTargetSessionID` during capability projection (as the deletion projection does) and apply recovery and delegate checks to both the requested alias and the resolved session, fencing if either identity is `ResumeRequired` or `Stopping > 0`.

## Low

- **`cmd/evener-hub/app_fork_capabilities_test.go:1279-1288`** — The test assumes two sequential `identifier.NewSessionID()` calls produce lexicographically increasing IDs. These are UUIDv7-based with random low-order bits, so the ordering assertion can fail nondeterministically before the test exercises its intended behavior. **Fix:** Use deterministic IDs or sort the generated IDs by value instead of asserting generation order.

---
*Reviewers: 2 done | Synthesis: codex, 13s | Total: 14m52s*

Coordinator verification and rulings:
- Medium is real. `hubForkRecoveryFencedNow` (app_threadread.go:446-467) checks
  `hubForkLiveStatusFenced` and `ResumeLocks.RecoveryState` for `ref.ThreadID` only, while
  `hubThreadFork` fences both the requested alias and the session it resolves to, and Task 48's
  `hubForkDeletionFenced` already resolves both. Ruling: the projection resolves the target the
  way the deletion projection does (`forkTargetSessionID`) and applies the recovery-state and
  live-status checks to both identities, fencing if either is `ResumeRequired` or `Stopping > 0`;
  reuse the resolution the deletion check already performs rather than resolving twice (one
  resolution per projection, shared by both fences).
- Low is real and is a flake in a test this queue added (Task 32's
  `TestHubForkReportsDeletionBeforeRecoveryWhicheverIdentitySortsFirst`, :1279-1288): two
  sequential `identifier.NewSessionID()` calls are asserted lexicographically increasing, which
  UUIDv7-style ids with random low bits do not guarantee within one millisecond. Ruling: generate
  the two ids, sort them, and assign the "sorts first"/"sorts second" roles from the sorted pair
  (the test's purpose is exercising both orders, which the sorted assignment still does); no
  ordering assertion on generation.

Requirements:
1. Medium, RED-first: a stable-ref thread whose RESOLVED session is `ResumeRequired` (and a second
   row with `Stopping > 0`) while the alias itself is clear; `thread/list`/`thread/read` must
   project `ForkFromTurn: false` (pre-fix true) and the fork RPC must refuse; a control with both
   identities clear still advertises and forks. Then the shared-resolution fence.
2. Low: the sorted-assignment change; run the test with `-count=200` to show no ordering
   failure, and state in the report how the two-orders property is still exercised.
3. Existing fork/resume/fence tests untouched (beyond the Low's own test); if one contradicts the
   ruling, report before changing anything.
4. Gates: `gofmt -l`; `go vet ./cmd/evener-hub/...`; `go test -count=1 ./cmd/evener-hub/...`;
   `-race` over the fork fence tests; pinned golangci-lint 0 issues. One commit per finding; do
   NOT push, do not amend, do not merge main (main has not moved since `f21e1c595`).
5. Report contract: append a "Task 51" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` (same lane) with RED and
   GREEN evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 52: PR #1096 native checkpoint — round 4: cluster members in paging dedup and truncation

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096). Current head `f1158ab21` (contains main
`b10be62a0`; main has since moved to `f21e1c595` - the coordinator merges it before the push, do not
merge it yourself). Shared mobile core `mobile/src` (`make test-native`; no formatter config applies,
match surrounding style). File under change: `mobile/src/state/conversation.ts` (paging dedup
~:1994-2006; `truncateItem` ~:465-490) and its test.

RoboRev finding at head `f1158ab` (verbatim):

## roborev: Combined Review (`f1158ab`)

**Verdict:** Two medium-severity gaps around clustered activity member handling in paging deduplication and output truncation.

### Medium

- **`mobile/src/state/conversation.ts:1996-2005`** — Paging deduplication seeds `currentIds` from only top-level `timelineIdentity` values, so activity cluster members are omitted. If an older page replays an attachment for such a member with a different wire ID but the same `transcriptKey`, the source isn't recognized and duplicate attachment rows are retained. Fix: build the source-deduplication set from `timelineIdentities` for every current row (including cluster members and attachment source identities), and add a regression covering changed wire IDs inside a clustered activity.

- **`mobile/src/state/conversation.ts:465-487`** — `truncateItem` truncates only the cluster's top-level `detail`, not `members[].detail`. Native transcript projection later expands clustered members directly, so oversized arguments/output/error from any member bypass the `MAX_ITEM_BYTES` limit and can be rendered in full. Fix: apply truncation and ownership tracking to each activity member, or unroll clusters before enforcing the byte limit so every displayed activity detail is bounded.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 33m7s*

Coordinator verification and rulings:
- Medium 1 is real. `timelineIdentities(item)` (:72-82) already yields the top-level identity plus
  every activity member's `transcriptKey ?? id` plus the attachment source, but the loadOlder
  paging dedup seeds `existingIds`/`currentIds` from `timelineIdentity` alone (:1996-1999), so an
  older page replaying an attachment for a cluster member under a different wire id with the same
  `transcriptKey` is not recognised. Ruling: seed both sets from the union of `timelineIdentities`
  over every current row, and add each accepted incoming row's identities the same way as it is
  admitted (the loop already updates the seen set; keep its first-occurrence semantics).
- Medium 2 is real. `truncateItem` bounds only `item.detail`; an activity cluster's
  `members[].detail` is never truncated and native expands members directly, so a member's
  oversized arguments/output/error bypasses `MAX_ITEM_BYTES`. Ruling: truncate each member's
  detail with the same rule as the top-level detail, and record truncation ownership for a member
  under the member's own identity (`member.transcriptKey ?? member.id`) so the frozen guard applies
  to a later delta aimed at that member; keep the top-level ownership as it is.

Requirements:
1. Medium 1, RED-first: a current clustered activity whose member has `transcriptKey` K and wire
   id A; an older page carrying an attachment row for that member with wire id B and the same
   source key K; pre-fix the attachment row is retained (duplicate), post-fix it is deduplicated.
   A control: a genuinely new attachment for an unrelated source is still admitted.
2. Medium 2, RED-first: a clustered activity whose member carries output over `MAX_ITEM_BYTES`;
   pre-fix the projected member output exceeds the limit, post-fix it is truncated and the
   member's identity is in the truncation ownership set (use the store's existing
   `getTruncatedItemIds()` oracle as Task 44 did); then a delta aimed at that member's wire id is
   refused by the frozen guard. Top-level truncation behaviour unchanged.
3. No existing expectation weakened; if a test contradicts a ruling, report before changing it.
4. Gates: touched test files green; `make test-native` green from the worktree root. One commit
   per finding, `fix(mobile): …`; do NOT push, do not amend, do not merge main.
5. Report contract: append a "Task 52" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-39-report.md` (same lane) with RED and
   GREEN evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 53: PR #1105 local fork capability — round 11: one live-status rule for the RPC and the projection

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `73f06d3ea` (contains main `c7e6c0968`; CI fully
green). Module: root (`cmd/evener-hub`).

RoboRev finding at head `73f06d3` (verbatim):

## roborev: Combined Review (`73f06d3`)

**Verdict:** One medium-severity gap found — fork RPC admission doesn't fence the resolved session identity the way the capability projection does.

---

## Medium

- **Location**: `cmd/evener-hub/app_threadlifecycle.go:887` (`hubThreadFork`)
- **Problem**: `hubThreadFork` checks the daemon's live recovery status only for `ref.ThreadID` via `hubForkLiveStatusFenced(cfg, ref.ThreadID)`. However, when `forkTargetSessionID` resolves a stable workspace alias, the resolved `sessionID` can differ from `ref.ThreadID`. The capability projection correctly fences that resolved session through `hubForkResolvedSessionFenced` (which also calls `hubForkLiveStatusFenced(cfg, sessionID)`), but the RPC admission path does not. As a result, a daemon reporting `resumeRequired` or `restartRequired` can still accept a fork through its stable alias, even though the advertised capability is disabled for that resolved session.
- **Fix**: Apply the live-status fence to every resolved fork target — mirror `hubForkResolvedSessionFenced` by also refusing when `sessionID != ref.ThreadID && hubForkLiveStatusFenced(cfg, sessionID)` (or iterate over `forkFenceTargets`) — and return the same recovery admission error before branching.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 13m52s*

Coordinator verification and ruling: the described divergence is not reachable, and this round
closes the class rather than the instance. `hubForkLiveStatusFenced(cfg, id)` answers from
`liveDaemonForThread(roster, id)`: for the alias that is the workspace-ref scan, for the resolved
session it is `roster.Find`, and both land on the SAME roster entry when a daemon is live (the
resolved session IS that entry's session id), so the flags are identical; when no daemon is live
the resolution comes from the `ResumeLocks` redirect (Task 41) and neither identity has a roster
entry, so both answer false. Task 51's review said the same ("both ids resolve through the same
roster entry, so this is a no-op in practice"). But two paths encoding the same rule differently
is exactly what has produced the last three rounds on this lane. Ruling: (1) refute the defect
in the report with that two-shape argument and pin it - a test that, in the stable-alias shape
with a live daemon carrying `resumeRequired`, asserts `hubForkLiveStatusFenced` answers the same
for the alias and for the resolved session, and the fork RPC refuses either way; (2) make the
RPC's live-status fence textually the projection's rule - iterate `forkFenceTargets` for
`hubForkLiveStatusFenced` (the loop the recovery and deletion passes already use), so the RPC
and `hubForkResolvedSessionFenced` cite one rule; this is a parity refactor with no behaviour
change, so no RED is claimed for it and every existing test must stay green untouched. Cost if
wrong: one extra roster lookup per fork for the resolved id, which hits `Find` directly.

Requirements:
1. The pinning test from ruling (1): assert agreement of the two calls and the RPC refusal, with
   a control where neither identity is fenced and the fork proceeds.
2. The parity refactor from ruling (2), with the comment at the RPC's fence naming the projection
   counterpart (and vice versa if the projection's comment does not already point back).
3. Existing fork/resume/fence tests untouched and green.
4. Gates: `gofmt -l`; `go vet ./cmd/evener-hub/...`; `go test -count=1 ./cmd/evener-hub/...`;
   `-race` over the fork fence tests; pinned golangci-lint 0 issues. One commit; do NOT push, do
   not amend, do not merge main (main has not moved since `c7e6c0968`).
5. Report contract: append a "Task 53" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` (same lane) with the
   refutation argument, the pinning test's evidence, commit SHA, one-line test summary; return
   only status, commit SHA, test summary, concerns.

## Task 54: PR #1096 native checkpoint — round 5: clustered members as first-class targets

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096). Current head `0dac80b2d` (contains main
`c7e6c0968`; CI fully green). Shared mobile core `mobile/src` (`make test-native` from the worktree
root; no formatter config applies to `mobile/src`, match surrounding style). Lane history for
context (read the Task 44 and Task 52 sections first; they explain the identity model and the
truncation-ownership set): `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-39-report.md`.

RoboRev finding at head `0dac80b` (verbatim):

## roborev: Combined Review (`0dac80b`)

## Summary Verdict

Medium-severity issues in clustered-member handling across activity projection, delta updates, and truncation reconciliation; one low-severity dedupe gap. No critical or high findings.

---

### Medium

- **Activity and incremental projection ignore turn-level status** — `mobile/src/conversation/project.ts:130-134`, `mobile/src/state/conversation.ts:649-720`
  Sparse live items can omit `item.status` while their containing turn is `inProgress`, causing running tool/reasoning rows (and incremental assistant rows) to appear completed. Include the containing turn's active status in the shared predicate, and pass or derive that context in incremental projection.

- **Sparse reasoning completion drops clustered-member output** — `mobile/src/state/conversation.ts:2709-2725`
  Output is preserved only for top-level rows. For a later member of a clustered activity, `existing` is not found because the member is nested in `members[]`; the replacement sets accumulated `detail.output` to `undefined`. Resolve the existing member by canonical transcript identity and preserve its output when completion omits `text`.

- **Delta handlers miss clustered members** — `mobile/src/state/conversation.ts:2906-2946` and `2952-3016`
  Reasoning and tool-output delta handlers search only top-level activity rows. Deltas for later clustered members trigger a reread instead of updating live, while deltas for the first member update only the cluster's top-level detail and leave `members[0]` stale. Locate the target within clustered members and rebuild affected activity segments while preserving each member's identity, output, and truncation state.

- **Member truncation freezes not preserved across reconciliation** — `mobile/src/state/conversation.ts:1159`
  `reconcileTruncationFrom` preserves top-level freezes via `priorFrozenIds`/`supersededFrozenIds` but member freezes are only re-added when `exceedsActivityDetailLimit` is true. An already-truncated member (now short) that remains in the final set loses its freeze on the next `loadOlder`/rehydrate, re-allowing deltas against truncated content. Carry member identities through the same preservation check as top-level, e.g. retain entries for `member.transcriptKey ?? member.id` when still in `retainedIds`.

### Low

- **`loadOlder` dedupe misses partial cluster overlaps** — `mobile/src/state/conversation.ts:2039`
  `existingIds` is seeded from the full `timelineIdentities` union but incoming rows are skipped only on top-level `timelineIdentity`. An incoming cluster with a new top-level id but an overlapping member identity slips through as a duplicate. Skip when any incoming identity overlaps, e.g. check `[...timelineIdentities(item)].some((id) => existingIds.has(id))` before accepting the row.

---
*Reviewers: 2 done | Synthesis: codex, 14s | Total: 24m24s*

Coordinator verification and rulings (all five verified against the code):
- M1 (turn-level status): `activityState(item)` (conversation/project.ts:130-134) and the store's
  incremental projection read only `item.status`; a sparse live item can omit it while its turn
  is `inProgress`, so a running row projects as completed. Ruling: an item with NO status inherits
  its containing turn's active state - extend the shared predicate (or add a sibling that takes
  the turn's status) and use it in both the canonical projector (which has the turn) and the
  incremental path (derive the turn's status from the store's active turn). An item WITH a status
  keeps its own. RED-first: a status-less tool item in an in-progress turn projects running in
  both paths; the same item in a completed turn projects completed.
- M2 (sparse reasoning completion drops member output): the completion handler finds `existing`
  among top-level rows only (state/conversation.ts:2709-2725), so a later clustered member's
  accumulated output is replaced by `undefined`. Ruling: resolve the existing row by canonical
  identity INCLUDING cluster members, and preserve the member's output when completion omits text.
- M3 (delta handlers miss clustered members): the reasoning and tool-output delta handlers
  (:2906-2946, :2952-3016) search top-level rows only; a delta for a later member falls back to a
  reread and a delta for the first member updates the cluster's top-level detail but leaves
  `members[0]` stale. Ruling: introduce ONE lookup that resolves a wire item id to
  `{ rowIndex, memberIndex | null }` across top-level rows and members, and one rebuild that
  replaces a member in place preserving every other member's identity, output and truncation
  state; use it from every delta and completion handler (M2 too), so the frozen guard for a
  member's identity (Task 52) is actually exercised by a real delta. Keep the reread fallback for
  ids found nowhere.
- M4 (member freezes not preserved across reconciliation): `reconcileTruncationFrom` (:1150-1200)
  preserves top-level freezes through `priorFrozenIds`/`supersededFrozenIds` but re-adds member
  freezes only when the member still exceeds the limit, so an already-truncated (now short)
  member loses its freeze on the next loadOlder/rehydrate. Ruling: carry member identities through
  the same preservation check as top-level (a retained member whose identity is in either prior
  set stays frozen).
- Low (dedup on any overlapping identity): the loadOlder skip checks only the top-level identity
  (:2039); an incoming cluster with a new top-level id but an overlapping member identity is
  admitted. Ruling: skip when ANY of the incoming row's `timelineIdentities` is already present,
  keeping first-occurrence semantics.

Requirements:
1. RED-first for each of the five, with the shapes named above; for M3 the RED is a real delta
   round-trip aimed at a non-first member's wire id (pre-fix: reread fallback or stale member;
   post-fix: the member updates in place, the cluster's other members untouched) and one aimed at
   the first member (pre-fix: `members[0]` stale).
2. No existing expectation weakened or re-pointed; if a test contradicts a ruling, report before
   changing it. Task 44's and Task 52's tests stay green.
3. Gates: touched test files green; `make test-native` green. One commit per finding (M2 and M3
   may share the lookup/rebuild commit if the split is artificial - say so), `fix(mobile): …`;
   do NOT push, do not amend, do not merge main (main has not moved since `c7e6c0968`).
4. Report contract: write `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-54-report.md`
   with RED and GREEN evidence per finding, the lookup/rebuild design in a paragraph, commit SHAs,
   one-line test summary; return only status, commit SHAs, test summary, concerns.

## Task 55: CI tooling flakes — golangci-lint installer with no retry; gate package-list timeout

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-ci-tools`
Branch: `claude/fix-ci-tooling-download-flakes`, created from `origin/main` (`53b263eed`).
Flake lane: per Jesse's standing instruction the implementer pushes its own branch and opens a PR
against `main` when the fix is verified.

Two CI-infrastructure failures today, both on PR #1105 heads whose code was clean:

1. `lint-golangci` job, head `9ee4eb4`, run 34644540860 (log:
   `/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-1105-9ee4eb4-103412048047.log`):
   `make tools-golangci` (make/repo.mk:13) runs the golangci-lint installer, which failed with
   `golangci/golangci-lint err http_download_curl received HTTP status 500` and no retry; the
   `static` aggregate job then failed with it.
2. `tests` job, head `ee0d6f7`, run 34639098143 (log: `/private/tmp/claude-501/-Users-jesse-git-prime-radiant-inc-evener--claude-worktrees-mobile-app-integration-6d4885/4bf3d0c3-48ad-4045-8100-dc324dbc5174/scratchpad/ci-1105-ee0d6f7-tests.log`):
   `scripts/gate/run-module-tests.sh: go list ./... timed out after 30s` for the root module on
   the runner, before any test ran; the other modules passed. The script's own advice was to
   clean the caches, which is not the fix for a slow runner.

Requirements:
1. Root-cause first, in the tooling: read `make/repo.mk`'s `tools-golangci` target and the
   installer invocation it uses (script or curl), and `scripts/gate/run-module-tests.sh`'s
   package-list step and its 30s bound. State with file:line why each failure has no retry or
   headroom today.
2. Installer: make the download retry on transient HTTP failures (5xx, connection resets) with a
   small bounded backoff, and prefer the pinned version already declared in `.tool-versions` /
   the Makefile (do not change the pinned version). If a Go-toolchain install path
   (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1`) is the repo's
   sanctioned fallback, say so; do not invent one. Verify locally: the target succeeds against a
   simulated transient failure (e.g. a wrapper that fails the first attempt) and the pinned
   binary version is what ends up on PATH.
3. Package list: give `go list ./...` bounded retries with a longer per-attempt budget rather
   than a single 30s attempt, keeping the total bounded and the existing failure diagnostics
   (retained stderr log path) intact; keep every documented behaviour of the script
   (`docs/developing-evener/testing.md` and the script's help text) accurate - update the help
   text if the bound changes. Verify locally that a forced timeout on the first attempt succeeds
   on the retry and that a persistent failure still fails with the same diagnostics.
4. No test weakened; tests for shell/make behaviour follow whatever pattern the repo already
   uses for gate scripts (look for existing tests of `scripts/gate/`; if none, a small
   `bats`-free shell check invoked by an existing make target is acceptable only if the repo
   already has that pattern - otherwise document the manual verification in the PR body).
5. Gates: `make lint` (the repository lint that covers Makefiles/scripts), `shellcheck` on the
   touched script if the repo runs it, and a local `make tools-golangci` + `ROOT_FULL=1 make test`
   smoke to prove nothing regressed. Never `go clean -cache`/`-testcache`.
6. Commit in conventional style (`ci: …` or `build: …`), no attribution trailers; push the
   branch; open a PR against `main` with `gh pr create` (draft is fine) whose body states both
   signatures with run links, the root causes with file:line, the fixes, and the verification.
   Do not merge.
7. Report contract: write `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-55-report.md`
   with the root causes, verification output tails, commit SHA, and PR number/URL; return only
   status, PR number, commit SHA, one-line verification summary, concerns.

## Task 56: PR #1105 local fork capability — round 12: resolved-session deletion precedence; relay staleness documented

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `9ee4eb443` (contains main `c7e6c0968`; main has
since moved to `53b263eed` - the coordinator merges it before the push, do not merge it yourself).
Module: root (`cmd/evener-hub`).

RoboRev finding at head `9ee4eb4` (verbatim; one reviewer done, one SKIPPED):

## roborev: Combined Review (`9ee4eb4`)

## Review Findings
- **Severity**: Medium
  **Location**: `cmd/evener-hub/app_relay.go:616-618`
  **Problem**: Relayed status notifications calculate `forkFromTurn` from global recovery state and broadcast it to every connection. A connection established before a recovery completes can receive `forkFromTurn: true` after recovery clears, but `hubThreadFork` still rejects that same connection via `sessionConnectionRecoveryError` until it reconnects.
  **Fix**: Make the relayed capability projection account for each connection’s recovery sequence, or expose the stale-connection fence so a connection never receives an actionable fork capability that its requests cannot use.
---
- **Severity**: Medium
  **Location**: `cmd/evener-hub/app_threadlifecycle.go:818-820`
  **Problem**: The preflight deletion check only examines the requested alias. For a stable alias that resolves to a different current session, a deletion fence on that resolved session is checked only after `refreshDaemonRestartRequiredError` returns. If roster/discovery refresh fails first, the terminal `MutationOutcomeTargetDeleted` response is masked by a retryable unavailable/restart error.
  **Fix**: Resolve the current session using the last trusted ownership information before returning refresh errors, and preserve the resolved session’s deletion-fence precedence.
## Summary
The change adds hub-owned fork capability projection and extensive fencing across live, persisted, delegated, recovery, and deletion states.

---
*Reviewers: 2 total (1 done, 1 skipped) | Synthesis: codex | Total: 11m10s*

Coordinator verification and rulings:
- Medium 2 (deletion precedence for the resolved session) is real and is Task 41's M3 extended to
  the second identity: the pre-refresh deletion read at app_threadlifecycle.go:818-820 covers the
  requested alias only, and the resolved session is fenced only after
  `refreshDaemonRestartRequiredError`, so a refresh failure masks the resolved session's terminal
  `MutationOutcomeTargetDeleted`. Ruling: before the refresh, resolve the target from the last
  trusted ownership information (the roster's current snapshot plus the redirect, i.e.
  `forkTargetSessionID` as it stands before the refresh) and apply the unlocked deletion read to
  both identities; keep the post-refresh resolution and every locked pass as they are (the
  pre-refresh resolution may be stale, which is fine for a terminal deletion answer and is
  re-resolved after the refresh anyway). RED-first: resolved session durably fenced for deletion
  and the refresh returning a restart/discovery error → `MutationOutcomeTargetDeleted`; a live
  resolved session with a failing refresh still gets the refresh error.
- Medium 1 (relay broadcasts one capability to every connection; a connection established before
  a recovery completed still gets refused per-connection by `sessionConnectionRecoveryError`
  until it reconnects) is a per-connection condition the shared projection cannot know without
  per-connection state; making the relay project per connection is a design change beyond this
  PR. Ruling: do NOT implement; document it as a named gap in `applyHubForkCapability`'s comment
  (app_threadread.go:475-481 already lists deliberate gaps): the refusal that connection meets is
  the retryable resume-required error, which is actionable (reconnect), and the window closes on
  reconnect. Record it in the report as a follow-up with the relay site (app_relay.go:616-618).

Requirements:
1. Medium 2 RED-first as above, then the fix; existing fork/resume/fence tests untouched.
2. Medium 1 comment + report entry only.
3. Gates: `gofmt -l`; `go vet ./cmd/evener-hub/...`; `go test -count=1 ./cmd/evener-hub/...`;
   `-race` over the fork fence tests; pinned golangci-lint 0 issues. One commit for the fix (the
   comment may ride with it); do NOT push, do not amend, do not merge main.
4. Report contract: append a "Task 56" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` with RED and GREEN
   evidence, commit SHA, one-line test summary; return only status, commit SHA, test summary,
   concerns.

## Task 57: PR #1100 round timings — round 7: synthetic compaction-owner turns complete live too

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay`. Current head `215a4182a` (contains main `c7e6c0968`;
main has since moved to `53b263eed` - the coordinator merges it before the push, do not merge it
yourself). Modules: `agent/` (own module), root (`internal/appprojector`, `internal/apptranscript`,
`server`).

RoboRev finding at head `215a418` (verbatim; Review 2 found no issues):

## roborev: Combined Review (`215a418`)

## Verdict: One medium-severity live/cold projection divergence in synthetic idle compaction turns; otherwise clean.

---

### Medium

- **Idle compaction synthetic turns stay `InProgress` live but `Completed` in cold transcript**
  - `agent/session_compaction.go:420-423`, `internal/appprojector/appwire_projection.go:1599-1635`, `server/appwire_turns.go:331-335`
  - Idle compaction now receives a synthetic `turn_compaction_...` owner whose events emit only `NotifyItemCompleted`. The live reducer creates unknown owner turns as `InProgress` and no later notification completes them, while the cold transcript projection always marks the grouped turn `Completed` at `internal/apptranscript/logical_turn.go:203`. Idle compaction and its owned steering can therefore remain permanently `InProgress` live and disagree with the reloaded transcript.
  - **Fix**: Emit an explicit completed lifecycle for synthetic compaction-owner turns, or teach the live reducer to finalize those owner-only groups. Extend the idle-compaction regression to assert live and cold turn statuses, not only item identity.

---

**Note:** Review 2 found no issues and confirmed that the change persists round timings, compaction layers, environment context, and self-minted turn identities while keeping live and transcript projections grouped identically.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 40m47s*

Coordinator verification and ruling: plausible and specific - Task 33's idle-fold owner
(`turn_compaction_<ulid>`) is a synthetic owner whose events only ever carry item completions,
the live reducer opens an unknown owner as `InProgress` (appwire_projection.go:1599-1635) and
nothing closes it, while cold marks the grouped turn `Completed` (logical_turn.go:203). Verify
it first with a parity test that asserts turn STATUS live vs cold for an idle fold (Task 33's
regression asserts item identity only). Ruling: prefer teaching the live reducer to finalize an
owner-only compaction group when its compaction record completes (no new wire notification, no
protocol change) over emitting a new lifecycle event; if the reducer cannot know the group is
complete without a signal, use the existing turn-lifecycle notification the direct-input path
already emits, and say why. Cost if wrong: an idle fold's group reads Completed live one event
earlier or later than cold; the parity test decides.

Requirements:
1. RED-first: extend the idle-compaction regression to assert live and cold turn status for the
   synthetic owner (and its owned steering); pre-fix live is `InProgress`, cold `Completed`.
2. The fix per the ruling; no index-version bump unless unavoidable (say so if it is); no new
   AppWire method or notification type; if a `go:generate` source is touched, commit the
   regenerated output in the same commit.
3. Existing parity tests (`TestIdleCompactionKeepsLiveAndColdGrouping`, the held-continuation
   test, `TestCompactionOwnerDuringOverlappingMutations`) stay green untouched.
4. Gates: `gofmt -l`; `go vet` in each touched module; `go test -count=1` for the touched
   packages; deadline audit; `-race` on the focused tests; pinned golangci-lint 0 issues. One
   commit; do NOT push, do not amend, do not merge main.
5. Report contract: append a "Task 57" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-33-report.md` with RED and GREEN
   evidence, which option the ruling's preference resolved to and why, commit SHA, one-line test
   summary; return only status, commit SHA, test summary, concerns.

## Task 58: PR #1098 environment replay — round 5: return a claimed turn exactly once

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`
Branch: `codex/mobile-transcript-identity`. Current head `f913e3190` (contains main `c7e6c0968`; main
has since moved to `3f8c9cb25` - the coordinator merges it before the push, do not merge it
yourself). Module: `agent/` (own module).

RoboRev finding at head `f913e31` (verbatim; one reviewer done, one SKIPPED):

## roborev: Combined Review (`f913e31`)

## Review Findings
- **Severity**: Medium
- **Location**: `agent/session_client_mutation_queue.go:377-383`
- **Problem**: `returnClaimedDirectClientMutationTurn` releases a claim using only the caller’s `acceptedTurnsFloor`, so concurrent failed inputs can release the wrong claim. For example, claims at floors `0` and `1` can raise `AcceptedTurns` to `2`; if the first releases first, the counter becomes `1`, and the second sees `AcceptedTurns == floor` and does not decrement. The actual turns are rolled back, but `AcceptedTurns` remains inflated, potentially causing premature max-turn exhaustion.
- **Fix**: Associate each claim with a unique reservation/token and release that exact reservation, or serialize direct input admission and rollback so claim ownership cannot interleave.

## Summary
The change adds durable environment-context events and replay while hardening transcript append failure handling.

---
*Reviewers: 2 total (1 done, 1 skipped) | Synthesis: codex | Total: 24m53s*

Coordinator verification and ruling: in scope and real. `returnClaimedDirectClientMutationTurn`
is NEW in this PR (added with the environment-append failure path; the PR diff adds it and calls
it from the durable-environment-append failure branch), and it releases by comparing
`AcceptedTurns` to the caller's floor, so two inputs whose claims interleaved (floors 0 and 1,
counter at 2) release wrongly when the first returns first (2→1, then 1 > 1 is false and the
second never decrements), leaving the counter inflated by one and max-turn exhaustion early.
Ruling: a claim is a unit, and a release returns exactly that unit - release decrements by one,
unconditionally except for a zero guard, and the call site guarantees one release per failed
claim (the failure path runs once per claim). Do not add a reservation token unless the
one-decrement rule cannot be made safe at the single call site; if you find a second caller or a
path that can release twice for one claim, report it and use a token. Cost if wrong: a double
release under-counts by one, the opposite error, equally bounded.

Requirements:
1. RED-first: two direct inputs whose claims interleave (claim A at floor 0, claim B at floor 1,
   counter 2), both failing their durable environment append; release in the order A then B and
   in the order B then A; `AcceptedTurns` must return to its starting value in both orders
   (pre-fix one order leaves it inflated). Drive it through the real claim/mutate path with the
   fault-plan fixtures the Task 26/47 tests use; if true concurrency is needed to interleave,
   use the session's own test hooks (the `testOnly` hooks the compaction tests use) rather than
   sleeps, and no bare wall-clock deadline without `// TRIPWIRE:`.
2. Verify with file:line that the failure path calls the release exactly once per claim and that
   no other path releases the same claim; state it in the report.
3. Gates: `gofmt -l`; `go vet ./...` in `agent/`; `go test -count=1 ./...` in `agent/`; deadline
   audit; `-race` on the focused tests; pinned golangci-lint 0 issues. One commit; do NOT push,
   do not amend, do not merge main.
4. Report contract: append a "Task 58" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-26-report.md` (same lane) with RED and
   GREEN evidence for both orders, the single-release trace, commit SHA, one-line test summary;
   return only status, commit SHA, test summary, concerns.

## Task 59: PR #1105 local fork capability — round 13: an unverifiable claim refuses, it does not admit

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `8205599de` (contains main `3f8c9cb25`; CI fully
green). Module: root (`cmd/evener-hub`).

RoboRev finding at head `8205599` (verbatim; one reviewer):

## roborev: Combined Review (`8205599`)

## Verdict: One Medium issue found; fork capability projection is otherwise solid.

### Medium

- **`cmd/evener-hub/app_threadlifecycle.go:1176-1180`** — `forkClaimIsLiveOwner` treats every `daemonprocess.Controller.Open` error except `ErrExited` as proof that the claim is live. When there's only one claim or multiple claims name the same session, `resumeClaimTarget` returns early without re-verifying any process, so a malformed, stale, or PID-reused rendezvous entry can authorize a fork without ownership being established. Fail closed by propagating verification errors, or make `resumeClaimTarget` verify every retained claim before accepting a non-conflicting target — only `ErrExited` should be treated as a non-live claim.

### Summary

The change adds hub-side fork capability projection with dual-identity recovery, deletion, and live-delegate fencing across reads, lists, relays, and fork admission. One ownership verification path can still admit an unverified rendezvous claim; all other paths reviewed clean.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 24m2s*

Coordinator verification and ruling: real, and it corrects Task 48's rule for the single-claim
case. `forkClaimIsLiveOwner` (app_threadlifecycle.go:1169-1186) returns true for every `Open`
error other than `ErrExited` (:1179-1181); Task 48 chose that so an ambiguous alias would meet
`resumeClaimTarget`'s conflict refusal rather than a resolver disagreement, but when there is
ONE claim (or several naming one session) `resumeClaimTarget` returns it without any
verification, so a malformed, stale or PID-reused rendezvous entry becomes the fork's target with
ownership never established. Ruling: three outcomes, not two. A claim whose process verifies is
live; a claim whose process has exited (`ErrExited`) is dropped as before; a claim whose
verification FAILS for any other reason is unverifiable, and a fork that would resolve through an
unverifiable claim is refused with the retryable `Unavailable` the ambiguity path already uses
("cannot verify session ownership: …", carrying the verification error), never admitted and never
silently dropped. The ambiguity behaviour is unchanged (two verified claims naming different
sessions still refuse through `resumeClaimTarget`). Cost if wrong: a hub whose process handles
cannot be opened for a legitimate reason (permissions) refuses forks retryably where it used to
admit them - the safe direction for a mutation.

Consequence for fixtures: the Task 32 and Task 38 recheck tests keep their claims "live" only
because `Open` refuses a self-PID with a non-`ErrExited` error (Task 48's review noted this).
Under the new rule those forks would be refused as unverifiable. Correct those fixtures to live
process stubs the way Task 48 corrected the two-daemons test (`forceStopProcess`), keeping their
names, rows and assertions unchanged, and list each in the report as a fixture correction with
the reason; do not weaken any assertion.

Requirements:
1. RED-first: a single rendezvous claim whose process verification fails with a non-`ErrExited`
   error (a PID-reused / start-time-mismatch shape, or a controller stub returning a distinct
   error); pre-fix the fork is admitted and branches, post-fix it is refused retryably without
   branching (`ListSessionMetas` before/after) and the refusal carries the verification error. A
   control with a verifying claim still forks; an `ErrExited` claim still drops through to the
   redirect/alias rule as before.
2. The fixture corrections above, each named.
3. Gates: `gofmt -l`; `go vet ./cmd/evener-hub/...`; `go test -count=1 ./cmd/evener-hub/...`;
   `-race` over the fork fence tests; pinned golangci-lint 0 issues. One commit; do NOT push, do
   not amend, do not merge main (main has not moved since `3f8c9cb25`).
4. Report contract: append a "Task 59" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` with RED and GREEN
   evidence, the fixture-correction list, commit SHA, one-line test summary; return only status,
   commit SHA, test summary, concerns.

## Task 60: PR #1096 native checkpoint — round 6: identity-first lifecycle lookup, cursor/flag agreement, orphan rendering, clamped truncation

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096). Current head `b899447a3` (contains main
`3f8c9cb25`; CI fully green). Shared mobile core `mobile/src` (`make test-native`). Lane context:
Task 44/52/54 sections of `task-39-report.md` and `task-54-report.md` (identity model, member-aware
lookup/rebuild).

RoboRev finding at head `b899447` (verbatim):

## roborev: Combined Review (`b899447`)

**Verdict:** Code adds valuable native activity projection and checkpoint features, but has paging, hierarchy, and reasoning-output edge cases to address.

## Review Findings

### Medium

- **`mobile/src/state/conversation.ts:2802`** — Lifecycle events replace activity members using canonical `transcriptKey` identity, but the existing reasoning output is looked up only by the incoming wire `id`. If the wire ID changes while `transcriptKey` remains stable, a sparse reasoning completion cannot find the accumulated output and replaces it with `undefined`, causing streamed reasoning text to disappear. **Fix:** Resolve the existing activity using `params.item.transcriptKey ?? params.item.id` (including clustered-member lookup) before preserving sparse reasoning output.

- **`mobile/src/state/conversation.ts:2166`** — `loadOlder` nulls `olderCursor` whenever the merged list is at `RETAINED_ITEM_CAP`, but preserves `hasEarlierItems` from the page result. This leaves `hasEarlierItems=true` with a null cursor, where subsequent `loadOlder` calls early-return `ignored`. **Fix:** Clear `hasEarlierItems` when forcing the cursor to null at cap, or preserve the server cursor instead of nulling it.

- **`mobile/src/services/activity.ts:283`** — `validateHierarchy` throws `missing-parent` for any delegate or job whose parent is absent, while `projectWork` below retains fallback logic to render orphans at top level as never-dropped. The fallback is unreachable, so one orphaned delegate or job fails the entire `ActivityView` projection instead of degrading gracefully. **Fix:** Align the two paths: either allow missing parents to render top-level and remove the throw, or remove the dead fallback and document fail-closed behavior.

### Low

- **`mobile/src/state/conversation.ts:488`** — `truncateText` computes `targetBytes` as `maxBytes` minus marker length without clamping. When `maxBytes` is smaller than the marker, it returns only the marker, which already exceeds `maxBytes`. **Fix:** Clamp `targetBytes` at zero and handle the marker-larger-than-limit case explicitly.

## Summary

The change strengthens native activity projection, clustering, rehydration, and canonical identity handling, and adds the native checkpoint app with shared session projection. Four edge cases remain: reasoning-output loss on wire ID changes, paging cursor/flag desync at the retained-item cap, an unreachable orphan-hierarchy fallback, and an unclamped truncation calculation.

---
*Reviewers: 2 done | Synthesis: codex, 13s | Total: 22m10s*

Coordinator verification and rulings (all four in scope - both files are new in this PR):
- M1 (lifecycle lookup by wire id): the residual Task 54 named at the call site, now a finding.
  Ruling: resolve the existing activity by canonical identity (`transcriptKey ?? id`) first, then by
  wire id, in the lifecycle completion path (extend `findActivityTarget` or add the identity form
  beside it - one lookup family, no second seam), so a sparse reasoning completion whose wire id
  changed keeps the accumulated output. RED-first: a reasoning member with stable transcriptKey
  and a changed wire id completes sparsely; pre-fix its output becomes undefined.
- M2 (cursor/flag desync at cap): `loadOlder` nulls `olderCursor` when the merged list is at
  `RETAINED_ITEM_CAP` but keeps `hasEarlierItems` from the page (state/conversation.ts:~2166),
  so later loads early-return ignored while the UI still offers them. Ruling: the two must agree.
  Determine which the design intends - if the store already evicts on load (the eviction/prune
  machinery the cap uses elsewhere), preserve the server cursor and let `loadOlder` evict; if the
  cap is meant to stop paging, clear `hasEarlierItems` when the cursor is nulled and say so in the
  comment. State the choice and why in the report. RED-first: at cap, a subsequent `loadOlder` is
  either honoured (cursor kept) or not offered (flag cleared); pre-fix it is offered and ignored.
- M3 (orphan hierarchy): `validateHierarchy` throws `missing-parent` (services/activity.ts:283,
  :291) while `projectWork` documents rendering orphans at top level "never dropped" (:357) - the
  fallback is unreachable and one orphan fails the whole `ActivityView`. Ruling: graceful
  degradation wins - orphans render at top level; remove the missing-parent throw for that case
  only, keep the other validations, and make the doc and the code agree. RED-first: a delegate
  whose parent is absent projects at top level instead of throwing.
- Low (unclamped truncation): `truncateText` computes `targetBytes = maxBytes - marker` without a
  clamp (:488), so a limit smaller than the marker returns the marker alone, exceeding the limit.
  Ruling: clamp at zero and handle the marker-larger-than-limit case explicitly (return a prefix
  that fits, or the empty string with the marker only if the caller's contract says the marker is
  mandatory - read the callers and say which). RED-first with maxBytes below the marker length.

Requirements:
1. RED-first for each; existing tests untouched (Task 44/52/54 tests green).
2. Gates: touched test files green; `make test-native` green. One commit per finding,
   `fix(mobile): …`; do NOT push, do not amend, do not merge main (main has not moved since
   `3f8c9cb25`).
3. Report contract: append a "Task 60" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-54-report.md` (same lane) with RED and
   GREEN evidence, the M2 design determination, the truncation contract determination, commit
   SHAs, one-line test summary; return only status, commit SHAs, test summary, concerns.

## Task 61: PR #1098 environment replay — round 6: no claim before the poisoned check; environment event inside the ordering transaction

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1098`
Branch: `codex/mobile-transcript-identity`. Current head `a4a6a20bb` (contains main `3f8c9cb25`; CI fully
green). Module: `agent/` (own module).

RoboRev finding at head `a4a6a20` (verbatim; two reviewers done):

## roborev: Combined Review (`a4a6a20`)

## Summary Verdict: Two medium issues found — durable environment-context replay and live projection are solid, but mutation cleanup and event ordering need attention.

### Medium

- **Mutation claim/release is not rolled back on poisoned-transcript early returns** — `agent/session_lifecycle.go:1015-1028`, `agent/session_queue.go:664-715`, `agent/session_client_mutation.go:442-456`: `ProcessPendingUserInput` and `ProcessClientMutationStart` claim or remove a durable mutation before calling `ProcessInputKind`. If the new poisoned-transcript guard returns early, `acceptUserInput` is never reached and its existing claim-rollback path does not run. The queue entry can remain removed with a `"claimed"` pending execution, while a start mutation can remain claimed with its turn budget consumed; the session may then report idle and the accepted input is unavailable until restart recovery. Fix: check for a poisoned writer before claiming work, and also roll back/release the mutation whenever `ProcessInputKind` returns before transcript incorporation, including races where poisoning occurs after the pre-check.

- **Environment event emitted outside the transcript ordering transaction** — `agent/session.go:1679-1684`, `agent/session_compaction.go:224-231`: Environment history is committed under `attentionMu`, but `EventEnvironment` is emitted only after releasing that ordering boundary and after autosave. A concurrent `Session.Compact` can therefore publish and emit compaction events before the environment event even though the environment entry precedes the compaction in the transcript. The AppWire projector treats environment events as standalone turn boundaries, so live projection can close or split an unrelated turn and diverge from cold transcript replay. Fix: tie the environment event to the same ordered publication mechanism as the transcript append and compaction side effects, rather than emitting it after the transaction has released its ordering lock.

---
*Reviewers: 2 done | Synthesis: codex, 13s | Total: 31m1s*

Coordinator verification and rulings:
- Medium 1 is the residual recorded in Tasks 47 and 58, now in scope: the poisoned-transcript gate
  made a refusal reachable in a live session, and `ProcessPendingUserInput`
  (session_client_mutation_queue.go:208 → :214) and `ProcessClientMutationStart`
  (session_client_mutation.go:451 → :455) claim or durably pop BEFORE calling into the loop, so the
  gate's early return leaves the queue entry removed with a "claimed" pending execution, or a start
  mutation claimed with its turn budget consumed, until restart recovery. Ruling, in two halves:
  (a) check the writer's poisoned state (the same nil-safe read the gate uses) before claiming or
  popping in both callers, refusing with the same wrapped `ErrWriterPoisoned`; (b) because
  poisoning can happen between that pre-check and the gate, also release or roll back the claim
  when `ProcessInputKind` returns before transcript incorporation - reuse the existing rollback
  helpers (`returnClaimedDirectClientMutationTurn` for the direct budget; the queue's own restore
  for the popped entry, the way the in-loop drain now leaves a message queued) rather than adding
  a new mechanism. RED-first for each caller: a claimed start mutation and a popped queued
  message with a poisoned writer; pre-fix the claim stays consumed / the entry stays removed after
  the refusal; post-fix the budget is returned and the entry is back in the durable queue.
- Medium 2 is real by reading: environment history is committed under `attentionMu` but
  `EventEnvironment` is emitted after the unlock and after autosave (session.go:~1679-1684), while
  compaction side effects publish under the same ordering boundary (session_compaction.go:224-231),
  so a concurrent `Compact` can emit its events before the environment event although the
  environment entry precedes the compaction in the transcript; the projector treats environment
  events as standalone turn boundaries, so live can split or close a turn cold does not. Ruling:
  emit `EventEnvironment` inside the same ordered publication the transcript append and the
  compaction side effects use (before the ordering boundary is released), so live event order
  equals transcript order; keep the warning emission where it is. RED-first: a live/cold parity
  test that interleaves an environment append with a fold using the session's `testOnly` hooks
  (`beforeFoldTranscriptCommit` / `beforeFoldSideEffectsFlush` already exist) so the fold publishes
  between the environment commit and its emission; pre-fix the live projection orders the
  compaction before the environment block, cold the reverse.

Requirements:
1. Both findings RED-first as above; no bare wall-clock deadline without `// TRIPWIRE:`; the
   existing poisoned-writer tests (Tasks 47/58) and the environment parity tests stay green
   untouched.
2. Gates: `gofmt -l`; `go vet ./...` in `agent/`; `go test -count=1 ./...` in `agent/`; deadline
   audit; `-race` on the focused tests; pinned golangci-lint 0 issues. One commit per finding; do
   NOT push, do not amend, do not merge main.
3. Report contract: append a "Task 61" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-26-report.md` with RED and GREEN
   evidence, the release/rollback trace for each caller, commit SHAs, one-line test summary;
   return only status, commit SHAs, test summary, concerns.

## Task 62: PR #1105 local fork capability — round 14: the two resolvers fall back to each other's source

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105`
Branch: `codex/mobile-fork-capability`. Current head `049f26b98` (contains main `3f8c9cb25`; CI fully
green). Module: root (`cmd/evener-hub`).

RoboRev finding at head `049f26b` (verbatim; one Low, the other reviewer clean):

## roborev: Combined Review (`049f26b`)

**Verdict:** 1 minor consistency issue found across 2 reviews.

## Review Findings

### Low

- **`cmd/evener-hub/app_threadlifecycle.go:1061-1099`, `1142-1158`** — The pre-lock resolver uses `cfg.Roster` to resolve a stable workspace alias, while the under-lock resolver only consults `cfg.RunDir`. In configurations with a roster but no run directory, a valid alias resolves to the current session before locking but back to the alias under the lock, causing `hubThreadFork` to reject the fork as "session ownership changed." The inverse occurs when `RunDir` is set but `Roster` is nil. **Fix:** Use a shared resolution path, or make each resolver fall back to the other configured ownership source, and add stable-alias coverage for configurations where only one source is available.

## Summary

The change projects hub-owned fork capability across reads, lists, relays, recovery fences, ownership checks, and crashed-subagent navigation. Reviewer 2 found no issues and confirmed consistent recovery, deletion, and live-delegate fencing.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 22m49s*

Coordinator verification and ruling: real as stated, in configurations production does not wire
(`cmd/evener-hub/main.go` builds the roster from the same run dir that becomes `WebConfig.RunDir`,
as Task 51's review recorded), but it is the last remaining way the two resolvers can disagree
without an ownership change, and closing it is two fallback lines. Ruling: each resolver falls
back to the other's source when its own is absent - the pre-lock resolver keeps roster-first and,
when `cfg.Roster == nil` and `cfg.RunDir != ""`, resolves through the rendezvous the way the
under-lock resolver does; the under-lock resolver keeps rendezvous-first and, when
`cfg.RunDir == ""` and the roster is present, resolves through the roster the way the pre-lock
resolver does; both then end in the shared redirect/alias tail as today. No behaviour change in
the production configuration (both sources present).

Requirements:
1. RED-first, two rows: roster present with no run dir, and run dir present with no roster; a fork
   through a stable alias must not be refused as "session ownership changed" (pre-fix it is in
   each row) and must branch the same session both resolvers name; the production-config tests
   stay green untouched.
2. State the fallback rule once, in the comment both resolvers already share (Task 53's), and
   note in the report that this closes the last configuration-dependent disagreement.
3. Gates: `gofmt -l`; `go vet ./cmd/evener-hub/...`; `go test -count=1 ./cmd/evener-hub/...`;
   `-race` over the fork fence tests; pinned golangci-lint 0 issues. One commit; do NOT push, do
   not amend, do not merge main.
4. Report contract: append a "Task 62" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md` with RED and GREEN
   evidence, commit SHA, one-line test summary; return only status, commit SHA, test summary,
   concerns.

## Task 63: PR #1100 round timings — round 8: a fold's replay tail must survive a crash before the marker anchors

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`
Branch: `codex/mobile-round-timing-replay`. Current head `6f7f61540` (contains main `3f8c9cb25`; CI fully
green). Modules: `agent/` (own module; `agent/schema`, `agent/transcript` inside it); root
(`internal/apptranscript`, `internal/appprojector`) only to confirm cold behaviour.

RoboRev finding at head `6f7f615` (verbatim; two reviewers done):

## roborev: Combined Review (`6f7f615`)

**Verdict: One high-severity issue found — compaction marker durability gap risks permanent turn loss on crash.**

### High

- **`agent/session_compaction.go:217-231`** — Compaction marker is appended before the replay tail. If the marker becomes durable but the process crashes before the subsequent `ContextReplay` entries are durably appended, `ResumeHistory` anchors on that marker and discards the original entries before it, permanently losing turns recorded during the fold. Make marker and replay-tail publication crash-atomic, or persist a recovery journal/completion state that lets startup detect and finish an incomplete tail before applying the compaction anchor.

---
*Reviewers: 2 done | Synthesis: codex, 7s | Total: 53m16s*

Coordinator verification and ruling: real. `publishFoldedHistory` writes the fold's markers
durably in `commitTranscriptsLocked` and only then the `rewriteTail` copies (session_compaction.go
:217-231, the Task 45 guard); `ResumeHistory` anchors on the last marker and discards everything
before it. A crash after the marker is durable and before the tail is durable therefore loses the
turns recorded during the fold from every later resume. The window predates this PR, but this PR
owns the replay-tail design now (Task 45), so it closes it. Ruling: prefer the ordering fix over a
new record kind - write the replay tail BEFORE the marker, with each copy tagged as belonging to
the fold it replays, and make `ResumeHistory`'s anchored branch include the tagged `ContextReplay`
copies that immediately precede the marker they name (the originals before the marker stay
discarded as today). Crash before the marker → no anchor → the no-anchor branch drops the copies
and keeps the originals (Task 45); crash mid-tail → same; crash after the marker → the tail is
already durable. The tag is a new optional field on `schema.Turn` (an `omitempty` addition, no
format-version bump unless the transcript reader rejects unknown fields - verify and say). Cold
projection drops `ContextReplay` unconditionally (Task 45 verified), so it is unaffected; confirm.
If you find the tail-first order impossible (the tail must reference the marker's identity before
the marker exists, and the marker's id is minted at stage time so it should be available - check),
fall back to RoboRev's other option: a durable completion record after the tail, with the anchored
branch honouring only a completed marker; say why.

Requirements:
1. RED-first: a fold whose transcript ends after the marker with the tail absent (simulate the
   crash with the fault plan the transcript tests use, or by truncating the file after the marker)
   must, after the fix, resume with the fold's turns present - pre-fix they are gone. A second case:
   transcript ends mid-tail before any marker → originals present, no duplicates.
2. `TestCompactionReplay_ResumeHistoryRetainsReplayCopies`, the four `TestResumeHistoryFromTranscript_*`
   cases and Task 45's tests stay green; if one contradicts the ordering, report before changing.
3. Gates: `gofmt -l`; `go vet ./...` in `agent/`; `go test -count=1 ./...` in `agent/`; deadline
   audit; `-race` on the focused tests; pinned golangci-lint 0 issues; if a `go:generate` source is
   touched, commit the regenerated output. One commit (two if the schema field is separated); do
   NOT push, do not amend, do not merge main.
4. Report contract: append a "Task 63" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-33-report.md` with RED and GREEN
   evidence, the ordering/tag design and the format-version determination, commit SHAs, one-line
   test summary; return only status, commit SHAs, test summary, concerns.

## Task 64: PR #1096 native checkpoint — round 7: tolerate corrupt saved hubs; bound the sign-in poll interval

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096`
Branch: `codex/mobile-native-checkpoint` (PR #1096). Current head `c563a5d29` (contains main
`3f8c9cb25`; CI fully green). Native app `mobile-native/src` (`make test-native`).

RoboRev finding at head `c563a5d` (verbatim; two Low, one reviewer clean):

## roborev: Combined Review (`c563a5d`)

**Verdict: 2 low-severity robustness issues found (no critical/high concerns).**

### Low

- **`mobile-native/src/connection.ts:113`, `mobile-native/src/connection.ts:126`** — `JSON.parse` on SecureStore data throws a raw `SyntaxError` when the index or a hub record is corrupt, bypassing the intended `Saved hub index could not be read` / `Saved hub could not be read` errors. A corrupt index makes `list()` fail entirely instead of degrading gracefully. Fix: wrap both parses in try/catch and throw the domain errors, and treat a corrupt index as empty or filtered rather than propagating `SyntaxError`.
- **`mobile-native/src/providerSignIn.ts:75`** — Server-controlled `intervalSeconds` is validated only as a non-negative safe integer with no upper bound; `delay * 1000` can exceed the `setTimeout` maximum and fire immediately, turning a huge interval into a tight device-poll loop. Fix: clamp the delay to a sane maximum such as `Math.min(delay, 60)` before scheduling.

> One reviewer found no issues; the findings above come from a second reviewer. No critical, high, or medium-severity issues were reported.

---
*Reviewers: 2 done | Synthesis: codex, 8s | Total: 22m44s*

Coordinator verification and rulings: both in scope (the native app is this PR) and small.
- Low 1: `JSON.parse` on SecureStore data throws a raw `SyntaxError` past the intended domain errors
  (connection.ts:113, :126). Ruling: wrap both parses; a corrupt hub record throws the "Saved hub
  could not be read" domain error, and a corrupt index is treated as empty so `list()` degrades
  rather than failing; say which and why in the code.
- Low 2: server-controlled `intervalSeconds` (providerSignIn.ts:75) is unbounded; a huge value
  overflows `setTimeout` and fires immediately, turning the device-code poll into a tight loop.
  Ruling: clamp the delay to a sane maximum (60 seconds) before scheduling.

Requirements:
1. RED-first for each: a corrupt index and a corrupt record in the SecureStore stub; an
   `intervalSeconds` above the clamp.
2. Gates: touched test files green; `make test-native` green. One commit per finding,
   `fix(mobile-native): …`; do NOT push, do not amend, do not merge main.
3. Report contract: append a "Task 64" section to
   `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-54-report.md` (same lane) with RED and
   GREEN evidence, commit SHAs, one-line test summary; return only status, commit SHAs, test
   summary, concerns.

## Task 65: PR #1145 round 3 (head 5496dcc) — CI tooling flake fix, RoboRev findings

Worktree: /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/flake-ci-tools (branch claude/fix-ci-tooling-download-flakes). This is the flake lane: unlike the other lanes you DO push your branch and update the PR (#1145); you do not merge it.

### RoboRev verdict (verbatim, 3 reviewers)

## roborev: Combined Review (`5496dcc`)

**Verdict: One blocker (version verification rejects a correct install) plus several medium-severity gaps in process-group cleanup and post-failure diagnostics.**

## Critical

None.

## High

### Version verification always rejects a correct golangci-lint install
- **Location**: `scripts/ops/install-golangci-lint.sh:75-91`
- **Problem**: `.tool-versions` pins the linter as `golangci-lint 2.13.1` (no leading `v`), and `version` is read verbatim. `golangci-lint version` prints its release with a leading `v` (`v2.13.1`). The guard builds `reported` and matches `*" version $version "*` — i.e. `version 2.13.1` — but the printed token is `v2.13.1`, which never contains `version 2.13.1`. The case always falls through to the mismatch branch and exits 1. This is exactly the failure mode the script exists to prevent: the installer succeeds, the guard rejects a correctly installed binary, and `lint-golangci` (and the aggregate `static` job CI gates on) fails unconditionally. The bare `$version` in the match is suspect because the version is *only* used with the `v` prefix elsewhere (`sh -s -- -b "$bindir" "v$version"`).
- **Fix**: Match the tag form the tool actually prints, e.g. `*" version v$version "*`, or normalize the leading `v` out of `version` when reading the pin. Verify against a real `golangci-lint version` invocation rather than assuming — the exact wording is what the match depends on.

## Medium

### `ps` error suppression defeats process-group liveness guarantee
- **Location**: `scripts/gate/run-module-tests.sh:303-305`, used by `stop_root_package_list_group` at `:331-343`
- **Problem**: `root_package_list_group_survivors` suppresses `ps` errors and returns only the `awk` output. If `ps` fails or produces no parseable output, callers interpret the empty result as proof the process group is gone and may start a retry while the timed-out `go list` is still running — defeating the cache-lock and concurrent-writer safety the cleanup is meant to provide.
- **Fix**: Propagate the process-listing failure separately from the survivor list and fail closed when liveness cannot be determined; only retry after a successful probe confirms no live members remain.

### Gate proceeds to unbounded Go work after root discovery fails
- **Location**: `scripts/gate/run-module-tests.sh:573`, `:575`, `:487`
- **Problem**: A failed bounded root `go list` still falls through to `run_wave $WAVE2`, whose `go test` invocations and the agent module's bare `go list ./...` have no timeout on the same stalled `GOCACHE`/`GOMODCACHE`. The gate can hang indefinitely after the ~212s root bound is reported.
- **Fix**: Distinguish root-discovery failure from test failure and exit/skip `WAVE2` when discovery failed, or route the remaining `go list`/`go test` invocations through the same bound.

### `set -m` toggled on the shared shell
- **Location**: `scripts/gate/run-module-tests.sh:325-345`
- **Problem**: Enabling monitor mode in a non-interactive shell changes job-notification and process-group behavior for the *entire* script, not just the one job. The script relies on job semantics: `run_wave` backgrounds every module/stream with `( ... ) &`, tracks pids in `active_pids`, and `stop_children`/`cleanup` signal those pids with `kill -TERM` then `wait`. With `set -m` active, each background job becomes its own process group and job, and `kill -TERM "$pid"` no longer reaches the job's children the way tree-snapshotting assumes. The window is short and precedes the waves, so it may be benign — but the change asserts safety without evidence.
- **Fix**: Scope monitor mode to the single job (e.g. run the attempt inside a subshell that turns monitor mode on for its own `&`, capturing the pgid there and passing it back). If the shared-shell toggle is kept, document and pin with a test that wave scheduling and `stop_children` still behave identically.

### Hard-failure branches lack artifacts and have reversed diagnostic ordering
- **Location**: `scripts/gate/run-module-tests.sh:361-364`, `:379-381`
- **Problem**: Both new hard-failure branches return 1 without the artifacts a reader needs. In the `list_pgid != list_pid` branch the attempt is killed and `wait "$list_pid"` is deliberately skipped, but `$attempt_list` is left behind with partial content and `root.packages.attempt*` is never removed. On failure the log directory is kept and only per-module logs are dumped — the attempt files and their stderr are separate paths named only in the timeout diagnostic. Additionally, the pid-mismatch branch prints its message *before* calling `root_package_list_timeout_diagnostic`, so the reader sees "cannot be stopped as one" before knowing where the log is.
- **Fix**: Print the pid-mismatch explanation before `root_package_list_timeout_diagnostic`, and state in the failure output exactly which files were retained (`$package_list_stderr`, `$attempt_list`) so a stalled host's partial package list is discoverable rather than inferred.

## Low

### Retry replay appears after the verdict summary
- **Location**: `scripts/gate/run-module-tests.sh:525-529`
- **Problem**: The replay reads `$root_package_list_retry_log` and prefixes lines with `run-module-tests.sh: `, but it runs after `run_wave $WAVE1`/`run_wave $WAVE2` and `finish_stream web`. If the root list times out on attempt 1 and succeeds on attempt 2, that fact is reported *after* the PASS lines — output no longer matches event order, and a reader grepping for the retry notice before the verdicts finds nothing. The stated purpose (making the notice visible) suffers when it trails a passing report.
- **Fix**: Move the replay before the verdict summary and mark it as a warning, or include a line in the final summary block when the retry log is non-empty.

### `~212s` ceiling arithmetic describes an unreachable state
- **Location**: `docs/developing-evener/testing.md:328-332`
- **Problem**: The doc states a run "fails in about three and a half minutes rather than hanging" and the script comment claims a "~212s ceiling." The 212s figure is 3 × (60s timeout + up to 10s stop grace) + 2s backoff, but the stop grace only applies when the group fails to die cleanly — and the branch that gives up retrying (`stop_root_package_list_group` returning non-zero) ends the run on the first attempt. The ~212s state is unreachable: the run either retries cheaply (60s + 1s per attempt) or stops at attempt 1. Quoting both as the same quantity will mislead future debugging.
- **Fix**: State the two cases separately: a clean-stop timeout costs ~61s per attempt and fails at ~183s over three attempts; a group that will not stop fails on the attempt where that happens, after at most 10s of grace, and is not retried.

### `pipefail` retry loop makes non-network failures slow and indistinguishable
- **Location**: `scripts/ops/install-golangci-lint.sh:52-56`
- **Problem**: `curl -sSfL "$installer_url" | sh -s -- -b "$bindir" "v$version"` under `set -o pipefail` makes a failed fetch count as a failed attempt (the stated rationale), but a successful fetch piped into a failing `sh` is indistinguishable from a network failure. The retry loop then re-downloads up to three times with 5s/10s backoff — for a broken pin or bindir permission problem the operator waits 15s+ and gets the same failure three times. The comment deliberately avoids classifying failures (defensible), but the backoff makes misdiagnosis slow.
- **Fix**: Capture the installer's output and include the last non-empty stderr line in the retry notice, or reduce the backoff; at minimum note that a non-network cause will fail identically each time.

### Retry notice not surfaced in the failing-module-output section
- **Location**: `scripts/gate/run-module-tests.sh:440-441`
- **Problem**: The retry log is appended from inside `run_root_package_list` (running in the `run_wave` subshell), and the replay's output goes to stderr. On a *failing* run the retry notice appears only in the stderr stream, not in the `=== failing module output ===` section a CI reader sees first. Every other diagnostic in this script is surfaced in that section; this one is the exception.
- **Fix**: Also append the retry notice to the root module's log path (or emit it into the failing-module-output section) so CI diagnostics stay in one place.

---
*Reviewers: 3 done | Synthesis: codex, 38s | Total: 17m40s*


### Coordinator rulings

- **High (version verification always rejects): REFUTED, do not implement.** Evidence: on this machine `golangci-lint version` prints `golangci-lint has version 2.13.1 built with go1.27.0 from 6d2288e0 on 2026-08-20T14:28:34Z` — the token after `version` is `2.13.1`, no leading `v`, so `*" version 2.13.1 "*` matches. CI's `static` job at this very head ran `make tools-golangci` (ci.yml:85) through this script and passed. Only change: extend the existing comment above the `case` to quote the exact line the tool prints (`golangci-lint has version X built with …`) so the next reader does not assume a `v`. The coordinator posts the refutation on the PR.
- **Medium 1 (`ps` failure read as "group gone"): real, fix.** `root_package_list_group_survivors` must distinguish "no live members" from "could not list processes". Fail closed: when `ps` itself fails, `stop_root_package_list_group` returns non-zero with a message that liveness could not be determined, and the caller does not retry. Verify the discriminating case the way round 2 verified the SIGKILL-survivor branch (a failing `ps` stub on PATH or an equivalent mutation), and record the run in the report and PR body.
- **Medium 2 (unbounded Go work after root discovery fails): real, fix the smallest way.** Do NOT restructure the waves or skip WAVE2 — the other modules are independent and their failure is already the gate's failure. Route the agent module's bare `go list ./...` (the `subpkgs` loop, ~:487) and any other bare `go list` in the script through the same bounded package-list helper the root uses, so nothing in the gate can hang on a stalled `GOCACHE`/`GOMODCACHE` after the root bound has already been reported. If the helper is root-specific, generalise its name and arguments rather than copying it.
- **Medium 3 (`set -m` on the shared shell): real, fix.** The monitor-mode toggle must never be visible to `run_wave`, `stop_children` or `cleanup`. Scope it to the subshell that spawns the one attempt (turn it on inside a subshell, spawn there, capture the pgid, wait there and return the status), or use `perl -e 'setpgrp(0,0); exec @ARGV' -- …` as the portable group spawn (perl is present on macOS and the CI image; `setsid` is not on macOS — keep that reason in the comment). Pick the smaller diff. State in the report, with the line numbers, why no `&` outside that scope can observe monitor mode.
- **Medium 4 (hard-failure branches: artifacts and ordering): real, fix.** In both hard-failure branches print `root_package_list_timeout_diagnostic` (which names the retained log) FIRST, then the one-line explanation of why the run will not retry. The failure output must name exactly which files were retained: `$package_list_stderr` and the partial `$attempt_list` (retain it; do not delete `root.packages.attempt*` on a hard failure). Make sure those lines also land in the root module's log so they appear in the `=== failing module output ===` section.
- **Low 1 (retry replay after the verdict summary): fix.** Emit the replay before the verdict summary, prefixed as a warning.
- **Low 2 (~212s arithmetic): fix the doc and the script comment.** State the two cases separately: clean-stop timeouts cost ~61s per attempt and fail after three (~183s); a group that will not stop fails on that attempt after at most the stop grace and is not retried. Do not invent other numbers — derive them from the constants in the script and cite them.
- **Low 3 (`pipefail` retry loop indistinguishable): fix minimally.** Capture the attempt's stderr and include its last non-empty line in the retry notice; keep the attempt count and backoff as they are; note in the comment that a non-network cause fails identically each attempt.
- **Low 4 (retry notice not in failing-module-output): fix.** Also append the retry notice to the root module's log path.

### Requirements

1. Every fix RED-first where the script has a test harness (look under `scripts/gate/` and the Makefile for how the gate script is tested; if there is none, the round-2 style recorded mutation runs are the evidence — say which you used). No existing test weakened.
2. One commit per finding or per tightly coupled pair, conventional `ci:`/`docs:` subjects, no trailers.
3. Before pushing: merge `origin/main` (`git fetch origin main` on its own line — a bare `git fetch origin` currently exits non-zero on a tag clobber and will silently stop an `&&` chain — then `git merge --no-ff --no-edit origin/main`), run `bash -n` on both scripts and `shellcheck` if installed, and run the gate script once locally in the bounded-discovery path if it completes in reasonable time (report the elapsed time; if it does not, say so and run the smallest module).
4. Push the branch; extend the PR body with a "Review round 3" section carrying the High refutation evidence (the exact `golangci-lint version` line and the green `static` job at 5496dcc), and one line per Medium/Low naming the commit.

### Report

Append "Round 3" to your existing report file. Reply with: status, commit SHAs in order, the pushed head, the mutation/verification output lines for Medium 1 and Medium 3, one-line test summary, concerns.

## Task 66: PR #1096 round 8 (head 2d4c7a4) — native checkpoint, RoboRev findings

Worktree: /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1096 (branch codex/mobile-native-checkpoint). Files: `mobile/src/**`, `mobile-native/src/**`, and one web-frontend file (`cmd/evener-hub/frontend/src/protocol/activityList.ts`, which is under the web constraints: biome check --write on touched files, `make test-web` must pass).

### RoboRev verdict (verbatim, 3 reviewers)

## roborev: Combined Review (`2d4c7a4`)

## Code Review Summary

The changeset adds native/mobile conversation projection, activity reconciliation, and portable pagination; reviewers found four medium issues and one low issue—no critical or high findings.

### Medium

- **Rehydrate race loses frozen-member ownership** — `mobile/src/state/conversation.ts:1859-1865` and `:2003-2005`
  `mergeLiveActivityMembers` can preserve a live-updated clustered member, but `supersededIds` records only the cluster's top-level identity. A later member truncated and frozen by a live delta is excluded from `rehydratePriorFrozen` (snapshot contains it, `supersededFrozen` doesn't), so reconciliation clears its freeze and lets later deltas append to already-truncated output. Track supersession for every live-updated member identity and preserve only those that remain frozen; add a regression for a frozen later member during an in-flight rehydrate with a stale snapshot.

- **Stale error survives a successful queued page** — `cmd/evener-hub/frontend/src/protocol/activityList.ts:146`
  Paged success publishes only `tree` without clearing `error`, so a failed queued page leaves a stale error even after a later queued page succeeds. Include `error: null` on the success publish or clear the error when a subsequent page succeeds.

- **`loadEarlier` silently dropped during refresh** — `mobile-native/src/jobOutput.ts:52`
  `run()` coalesces any concurrent call into the same `inFlight` promise and drops its `beforeBytes`, so a `loadEarlier` requested during a `refresh` silently never loads earlier output. Queue the `loadEarlier` cursor separately (like `ActivityList` does) or reject concurrent differing requests instead of returning the unrelated in-flight promise.

- **Duplicated tone classification and redaction logic** — `mobile/src/state/activity.ts:114-171` (duplicating `mobile/src/services/activity.ts:121-172`) and `:177-215`
  The store re-declares `classifyTone`, `formatOutputBytes`, `RUNNING_STATUSES`, `IDLE_STATUSES`, `TERMINAL_STATUSES`, and `FAILED_STATUSES` verbatim from the pure projection service, and adds `FAILED_OUTCOMES` (an exact copy of `FAILED_STATUSES`) used for the outcome check while the service uses `FAILED_STATUSES` for the same check. The two classifiers are byte-for-byte equivalent today, so a future edit to one set silently leaves store and service disagreeing on tone for the same wire status—row colour depends on whether it was last rendered from a full read or a live notification. The store also re-implements `projectJobEntry`/`projectDelegateEntry` inline, giving the diagnostic redaction allowlist two owners. Export `classifyTone`, `formatOutputBytes`, the status sets, and `projectJobEntry`/`projectDelegateEntry` (or a single `projectDiagnostics` helper) from the service and have the store import them, per the repo's own `CLAUDE.md` duplication prohibition.

### Low

- **Missing `uncertain` flag on failed sign-in completion** — `mobile-native/src/providerSignIn.ts:284-302`
  `complete()`'s catch publishes `busy: false` and the "could not be confirmed" error but never sets `this.uncertain = true`, unlike every other uncertain-terminal path (`poll()` catch at `:232`, unrecognized-status branch at `:241`, `setConnection` interrupted-poll path at `:103`). With `uncertain` staying false, a subsequent `checkStatus()` (`:324`) publishes `error: null` and a non-conservative `credentialState`, and `schedule()` is no longer blocked by the guard—the user sees a silently clean status line instead of the explicit re-check prompt the surrounding code maintains. Set `this.uncertain = true` in `complete()`'s catch before publishing, matching the other paths.

---
*Reviewers: 3 done | Synthesis: codex, 16s | Total: 35m46s*


### Coordinator rulings

Verify each finding against the code before touching it; a finding that cannot occur is refuted with file:line evidence under DONE_WITH_CONCERNS (Global Constraint 7). Rulings assuming they verify:

- **Medium 1 (rehydrate race loses a frozen member's ownership, `conversation.ts` ~1859-1865 and ~2003-2005): real if `supersededIds` is keyed on the cluster's top-level identity only.** Task 54/60 made truncation and freezing per member and rehydration member-inclusive (`rereadIdentities`); supersession must be member-inclusive the same way, or the two encodings of "which identities did a live delta own" disagree — the exact class we keep meeting. RED-first with the shape the finding names: a later member truncated and frozen by a live delta while a rehydrate with a stale snapshot is in flight; the member must stay frozen after reconciliation and a later delta must not append to it. Fix by tracking supersession for every live-updated member identity through the same identity helpers (`timelineIdentities`/`ownTimelineIdentities`), not a parallel set.
- **Medium 2 (stale `error` survives a successful queued page, web `activityList.ts` ~146): real if the paged-success publish omits `error`.** RED-first: a failed queued page followed by a successful one must publish `error: null`. Smallest fix: include `error: null` on the success publish. Web constraints apply (biome, `make test-web`).
- **Medium 3 (`loadEarlier` silently dropped during a `refresh`, `mobile-native/src/jobOutput.ts` ~52): real if `run()` returns the unrelated in-flight promise and discards `beforeBytes`.** RED-first: `loadEarlier` called while a `refresh` is in flight must still load the earlier page after the refresh settles. Fix in the shape `ActivityList` already uses in this branch (queue the cursor and run it after the in-flight request), so the two paginators share one rule; if `ActivityList`'s queue logic can be lifted into a shared helper in a few lines, do that rather than copying it.
- **Medium 4 (duplicated tone classification and redaction logic, `mobile/src/state/activity.ts` ~114-215 vs `mobile/src/services/activity.ts` ~121-172): real by inspection and a repo-rule violation (CLAUDE.md: never introduce duplication; extract and share).** Fix: export `classifyTone`, `formatOutputBytes`, the four status sets and `projectJobEntry`/`projectDelegateEntry` (or one `projectDiagnostics` helper) from the service and import them in the store; delete the store's copies and `FAILED_OUTCOMES` (use `FAILED_STATUSES`). RED-first is a characterization pin: a test that the store and the service classify the same wire status identically for every status in the sets (table over the union), which stays green through the consolidation and fails if anyone re-forks them. Do not change behaviour; if the two copies actually differ anywhere, STOP and report the difference before choosing which one wins.
- **Low (missing `uncertain = true` in `complete()`'s catch, `providerSignIn.ts` ~284-302): real if the other three uncertain-terminal paths set it and this one does not.** RED-first: after a failed completion, `checkStatus()` must keep the explicit re-check prompt (not publish `error: null`) and `schedule()` must stay blocked by the guard. Fix: set `this.uncertain = true` before publishing, matching the other paths.

### Requirements

1. One commit per finding, RED output recorded before each fix, no existing test weakened or re-pointed (if one contradicts a finding, report it).
2. `make test-native` green with counts; for Medium 2 additionally `npx biome check --write` on the touched web file and `make test-web` from the worktree root.
3. Do not push. Do not merge main. Match each file's house style (tabs vs spaces per file).
4. Append a "Task 66" section to `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-54-report.md` with RED/GREEN evidence per finding and the gate outputs; reply with status, commit SHAs, one-line test summary, concerns.

## Task 67: PR #1105 round 15 (head 0707f7c) — local fork capability, RoboRev findings

Worktree: /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1105 (branch codex/mobile-fork-capability), package `cmd/evener-hub`.

### RoboRev verdict (verbatim, 3 reviewers; review 2 found no issues)

## roborev: Combined Review (`0707f7c`)

## Verdict: Logic is correct and well-reasoned, but the fork projection is computed on hot paths more often than it can change, with minor capability-admission and redirect-traversal gaps.

**Medium**

- **`cmd/evener-hub/app_threadlifecycle.go:1131`** — `forkRedirectSessionID` follows only one completed recovery redirect. If aliases resolve transitively (`A → B → C`), forking through `A` branches `B` instead of the current `C`; redirect cycles are also not detected. The under-lock resolver inherits the same one-hop behavior.
  - *Fix:* Traverse completed redirects to a fixed point with cycle detection, while preserving pending-recovery fences and using the same resolver before and after locking.

- **`cmd/evener-hub/web_workspace.go:120-124` and `cmd/evener-hub/web_api_tree.go:1113-1115`** — Workspace capability projection still derives `Fork` from `pastExists` or direct daemon capabilities, while the new hub fork admission rejects live delegates, recovery-fenced sessions, deletion-fenced sessions, and some stable-ref targets. REST/workspace clients can therefore hide a valid hub fork or advertise one that the RPC rejects.
  - *Fix:* Route workspace/API capability generation through the same hub-owned fork projection, including stable-ref resolution and all recovery, ownership, and deletion fences.

- **`cmd/evener-hub/app_relay.go:614-618`** — `ownsFork` is computed for every relayed notification on a local relay key, but its only consumers (`stampClosedThreadCapabilities`, `stampForkCapability`) both return the notification untouched unless `notification.Method == appwire.NotifyThreadStatusChanged`. `applyHubForkCapability` is not free: `hubCanForkThread` scans the roster's byPID map, `hubForkRecoveryFencedNow` calls `hubForkLiveStatusFenced` → `liveDaemonForThread`, then the projection resolves the target session a second time, then `hubForkDeletionFenced` performs up to two `DeletionStore.TargetState` lookups. The publish path is per-notification, so every `item/started` and `item/completed` frame of a subscribed session (i.e. every tool call, per subscribed client) now pays several roster scans and lock acquisitions to compute an answer it then discards. `enrichOutputImageNotification` on the line above already avoids this shape with a cheap method check before doing any work.
  - *Fix:* Gate the computation on the method, mirroring the stampers' own precondition:
    ```go
    if strings.HasPrefix(target.relayKey, "local:") {
        notification = enrichOutputImageNotification(...)
        if notification.Method == appwire.NotifyThreadStatusChanged {
            ownsFork := applyHubForkCapability(cfg, target.thread).Evener.Capabilities.ForkFromTurn
            notification = stampClosedThreadCapabilities(notification, ownsFork)
            notification = stampForkCapability(notification, ownsFork)
        }
    }
    ```

**Low**

- **`cmd/evener-hub/app_threadread.go:553` (via `app_restart_required.go:216`, `internal/hubcore/roster.go:625`)** — `applyHubForkCapability` runs once per entry on both thread/list sweeps (`app_threadlist.go:106` and the past-entry sweep at `app_threadread.go:722`). For any thread that is not a live daemon's current session — which is every saved/past session, the bulk of a list response — `hubForkRecoveryFencedNow` and then `forkTargetSessionID` each call `liveDaemonForThread`, whose `roster.Find` misses and falls through to `hubRosterList(roster)` → `Roster.List()`. `List` is not memoized: it allocates a map, deep-clones every entry (`cloneLiveEntry`, including the new `ActiveFlags`), and sorts. So one list request performs a full roster snapshot clone-and-sort per listed thread, twice over, for a question the fork RPC re-derives from scratch anyway.
  - *Fix:* Resolve liveness once per projection pass (a `map[workspaceRef]LiveEntry` built from one `List()` call and threaded through the sweep), or hoist the resolution out of the per-entry builder and pass the already-resolved session id in. `hubRosterList` already exists as the seam for this.

- **`cmd/evener-hub/app_threadread.go:444-448`, `internal/hubcore/roster.go:29,56,92,258,848,915`, `prober.go:141`, `navigation_projection.go:270`** — The `ActiveFlags` fence is unreachable in production. Nothing on the producing side writes `ThreadStatus.ActiveFlags` — the daemon builds `appwire.ThreadStatus{Type: appCapabilities(...)}` (`server/appwire_runtime.go:2267`), the projector does the same (`internal/appprojector/appwire_projection.go:1600`), and the only non-test assignments in the tree are the ones this diff added on the consuming side. The repo's own docs confirm this (`test/scenarios/web-model-switch-mid-session.md:157`, `docs/superpowers/specs/2026-06-06-evener-goal-design.md:361`: "zero non-test consumers"). The recovery fence therefore rests on the string literal `"resumeRequired"` in `hubForkRecoveryFenced`, which no producer emits and no constant in the repo defines, while the plumbing around it (roster field, defensive clone, fingerprint hashing on every refresh, prober copies, navigation clones, and the tests that script a prober to populate it) is carried for it. The real hub-side recovery signals this fence needs are already covered by `cfg.ResumeLocks.RecoveryState`, which the same functions consult.
  - *Fix:* Either drop the active-flag branch and its roster plumbing until a producer exists, or make the flag name a shared constant used by both the daemon's status egress and this predicate, so the two cannot be spelled apart.

*Review 2 found no issues. The fence and projection logic is correct and unusually well-reasoned — the RPC is stricter than the projection in every enumerated gap — but the projection is computed on two hot paths far more often than it can change and carries `ActiveFlags` machinery that no producer in the tree populates.*

---
*Reviewers: 3 done | Synthesis: codex, 29s | Total: 45m25s*


### Coordinator rulings

Verify each finding against the code before touching it; refute with file:line under DONE_WITH_CONCERNS where the described defect cannot occur (Global Constraint 7).

- **Medium 1 (`forkRedirectSessionID` follows one redirect hop): verify first.** Read how `ResumeLocks` writes `RecoveryState(...).ResumeSessionID` and `ResolvedSessionID(...)` (internal/hubcore or wherever the store lives). If a recovered session can itself be recovered so that A→B and B→C both exist as separate records, the one-hop read is real: traverse completed redirects to a fixed point in `forkRedirectSessionID` (already the shared tail of both resolvers) with a visited set that stops on a cycle, RED-first with a two-hop fixture and a cycle fixture; pending-recovery fences are unchanged. If the store collapses chains (recovering B rewrites A's redirect to C, or a redirect target can never itself be an alias), refute with the writing site's file:line and add a characterization test that pins the collapse so the refutation stays true.
- **Medium 2 (workspace/REST capability projection derives `Fork` from `pastExists`): verify what it gates, then decide by that.** `apiSessionCapabilities` (web_api_tree.go ~1108) and `liveWorkspaceSnapshot` (web_workspace.go ~120-124) feed the REST/workspace clients. Find the endpoint a web client calls when `Fork` is true. If that path ends in the same hub fork admission this PR built (`hubThreadFork` or the shared fences), the REST projection must answer from the same hub-owned projection (`applyHubForkCapability`) — same class as rounds 9–15, fix now, RED-first with a recovery-fenced and a deletion-fenced session that the REST capability must not advertise. If the REST fork path has its own admission that agrees with its own capability, refute with the endpoint's file:line, and leave a one-paragraph note in the report on whether the two should converge (the coordinator files the issue). Size valve: if routing the REST projection through the hub one needs more than a thin call (the WebServer lacks the thread record the projection wants), stop at NEEDS_CONTEXT with the assessment rather than restructuring.
- **Medium 3 (`ownsFork` computed for every relayed notification): real, fix.** Gate the computation on `notification.Method == appwire.NotifyThreadStatusChanged`, mirroring the stampers' precondition. RED-first: a relayed non-status notification (e.g. an item/started frame) must not invoke the projection — count calls through the existing `hubRosterList` seam or an equivalent hook the tests already use; a status notification still gets stamped.
- **Low 1 (liveness resolved twice per listed thread, one roster clone-and-sort each): fix the smallest half now.** Inside `applyHubForkCapability`, resolve `liveDaemonForThread` once and pass the result into both consumers (`hubForkRecoveryFencedNow`/`hubForkLiveStatusFenced` and `forkTargetSessionID`), removing the second lookup. The once-per-sweep memoisation (a map built from one `List()` and threaded through the sweep) is the restructure already tracked in #1146 — do not do it here; the coordinator adds this finding to that issue.
- **Low 2 (`ActiveFlags` fence has no producer): document, do not remove.** The consumer-side plumbing was added at RoboRev's own request in rounds 5, 6 and 12 (Tasks 7, 29, 32, 38); removing it now re-opens those rounds. Fix: introduce one named constant in the hub package for the `resumeRequired` flag, used by `hubForkRecoveryFenced` (and any other spelling of the literal in `cmd/evener-hub`), with a comment stating that no producer emits it yet and that the hub-side recovery signal it complements is `cfg.ResumeLocks.RecoveryState`. Do NOT add anything to `appwire` (Global Constraint 8). The coordinator files the producer-side issue.

### Requirements

1. One commit per finding; RED-first for every code change; no existing test weakened or re-pointed (report contradictions instead).
2. Gates: gofmt on touched files, `go vet ./...`, pinned golangci-lint 2.13.1 with 0 issues, `go test -count=1 ./cmd/evener-hub/...`, `-race` on the fork fence suite plus the new tests with the `-run` selector recorded verbatim.
3. Do not push. Do not merge main.
4. Append a "Task 67" section to `.superpowers/sdd/2026-09-10-mobile-landing-queue/task-32-report.md`; reply with status, commit SHAs, one-line test summary, concerns, and for Medium 1 and Medium 2 the verification outcome (real or refuted) with file:line.
