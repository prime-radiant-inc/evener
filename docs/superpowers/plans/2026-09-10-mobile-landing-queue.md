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
