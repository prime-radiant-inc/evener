# Background Jobs and quiet activity implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Jesse must approve this written plan and choose its execution method before implementation.

**Goal:** Show only durably backgrounded commands in activity Jobs, put terminal jobs and delegates in quiet history, and remove native failed-delegate callouts while preserving genuine session attention.

**Architecture:** Record background eligibility in the existing shell start event, then apply one source-owned predicate to activity pages and counts. Preserve diagnostic history and use the existing shared activity store for both clients. Change lifecycle grouping and presentation independently of truthful outcome state.

**Tech Stack:** Go workspace, AppWire RPC, TypeScript, React web, Zustand workspace state, React Native 0.86.3 / Expo 57, Vitest, react-test-renderer, real Chrome guards, scripted provider at the LLM boundary.

**Spec:** [Approved design](../specs/2026-10-03-background-jobs-design.md), commit `00679fa5fdc73ce048adb7626ae0184beb078460`. Read the spec and this plan together.

## Global constraints

- “Keep the wire shape unchanged.” `JobActivityJob.background` remains a boolean; persist its evidence in the existing `job_started` payload.
- “The journal is the authority, with no client-inferred classification, separate journal or transcript migration.”
- “Do not backfill classifications.” Missing historical evidence means ineligible activity, with data retained.
- “Keep the existing session/subtree scope, owner identity, ordering and cursor contracts.”
- “Neither client acquires a new reader, subscription, timer or retry loop to reconstruct background status.”
- “Failed and completed outcome buckets retain their current domain meaning.” Presentation's terminal count is `failed + completed`.
- “Preserve unknown, incomplete, unavailable and recovering states rather than replacing them with zero.”
- “A terminal parent must not hide running descendants or make their existing navigation and stop controls unreachable.”
- “Existing retention policies are unchanged.” Diagnostic list/get/output and transcript access remain complete.
- “Do not combine report evidence from independently refreshed, mismatched delegate run generations.”
- “New default tests require no provider credentials, network service, live model behavior or ambient machine state.” Loopback fixtures and a scripted provider exercise real Evener below those boundaries.
- “The merged Overview work remains final.” Do not change its spec, plan, ledger, panels or guards for this task.
- Dependency advisories, review leftovers, cascade diagnosis, job-output auto-follow, bundle architecture, global FocusScope and platform qualification remain separate work.
- Follow `AGENTS.md`, `mobile-native/AGENTS.md`, `docs/developing-evener/testing.md`, and the package import contract. Read the Expo 57 versioned documentation before native code changes. Import the shared package by `@evener/appwire-client`, with testing subpaths confined to tests/dev support.
- Keep dependency versions and lockfiles unchanged. Install the pinned dependencies after approval; installation is not an advisory patch.

## Review focus

1. A command finishes immediately around background handoff. Its durable start must already prove eligibility, with no false background acknowledgement on append/forward failure. Pin in Task 1.
2. An excluded run is surrounded by hundreds of excluded records at a page boundary. Eligible rows remain reachable and excluded rows do not consume visible capacity. Pin in Task 2, including bounded empty continuation pages.
3. Reconnect or a resumed delegate delivers an older result after newer evidence. Preserve later-page demand and reject mismatched session/run generations. Pin in Task 3 and the recorded native producer cases in Task 5.
4. A terminal parent still has active descendants on another page. Closed histories leave descendants and their controls reachable, without promoting the parent's failure. Pin in Tasks 4 and 5.
5. A native row combines failed delegates with a genuine session error, question, approval or offline state. Quiet the delegates only, preserving the attention decision, priority and action. Pin in Task 6.

---

## Execution boundary and file map

This is one product contract, not six unrelated redesigns. Execute Tasks 1–3 in order. Tasks 4 and 5 consume the same established server/shared contract. Task 6 depends on Task 5's unchanged authoritative tally interface. Native execution in this session is recommended because these six slices share eligibility, identity and terminal-count contracts. A fresh whole-branch reviewer still checks the final result.

The current isolated worktree is `wip/web-activity-followups`, starting at the spec commit above. Confirm `git status --short`, branch and HEAD before execution. Ask Jesse about unexpected changes. Keep all scratch under `$EVENER_SCRATCH_DIR`. Never disable hooks or use blanket staging.

| Slice | Source of truth / responsibility | Independently checkable deliverable |
| --- | --- | --- |
| 1 | `agent/internal/jobstore/{event,fold,record,store}.go`, `agent/job_shell.go` | Actual shell handoff survives journal reconstruction and completion |
| 2 | `agent/session_activity_jobs.go`, `agent/session_activity.go` | Filtered pages and matching counts, complete diagnostic access |
| 3 | Hub routing tests, `appwire-client/typescript/sessionActivityStore.test.ts` | Real domain reads survive routing and existing shared recovery |
| 4 | Web `JobsTab.tsx`, `activityRows.tsx`, new Jobs-only browser guard | Quiet web histories, real output pane, visible later-page recovery |
| 5 | Native `activityList.ts`, `subagentModel.ts`, row/strip renderers, `SubagentsScreen.tsx` | Independent histories and filters, truthful outcomes, reachable child work |
| 6 | Native `BoardRow.tsx`, native chip wording, `sessionState.ts`, `SessionHeader.tsx` | Quiet delegate chips across all native lists, preserved session attention |

Update evergreen ownership with its owning slice. Do not rewrite broad modules or introduce another activity owner. New test files below are deliberate deliverables; other referenced files already exist at the approved spec head.

### Evidence discipline

For every changed behavior: add the regression, run it before changing production, retain the complete failure, make the smallest fix, then rerun the regression and impacted suite. A test that already passes is preservation evidence; say so and leave working code alone. A missing dependency, failed launch or timeout is not a red behavior test. Read every invoked script first, including its worker/cleanup helpers.

Record commands, exit status, decisive assertions and retained log paths in `docs/superpowers/plans/2026-10-03-background-jobs-evidence.md` during execution. This file is an execution record, not an evergreen product guide. Do not write passing claims in advance. Capture and assert intentionally triggered error output.

`make help` was read during planning. Product tests, dependency installation and browser probes have **not** run for this plan. Before a TypeScript check, use the corresponding preflight. A fresh worktree currently has no frontend install.

## Task 1: Make background handoff durable

**Files:**
- Modify: `agent/internal/jobstore/event.go:38-57` (start evidence).
- Modify: `agent/internal/jobstore/fold.go:194-216` (reconstruction).
- Modify: `agent/internal/jobstore/record.go:189-195`, `agent/internal/jobstore/store.go:940-944` (current field/clone documentation).
- Modify: `agent/job_shell.go:230-341,598-665` (promotion order and start payload).
- Test: `agent/internal/jobstore/record_test.go`, `agent/job_shell_promotion_background_test.go`, `agent/job_shell_test.go`.
- Create: `agent/job_shell_background_durability_test.go`.
- Update: `docs/job-control.md`, S12 in `docs/product/subsystems.md`.

**Source evidence:** `Background` is currently live-only. `commitDelayedShell` already appends and forwards the start before setting `durableStarted`. Explicit background sets the live flag at creation; promotion sets it after commit. Inline retained output and foreground runtime timeout also register shell records. Their registration is not background proof.

**Interfaces:**
- Consumes: `runShell(context.Context, *jobManager, execenv.StreamingExecutor, shellArgs) shellResult`, `newFakeClockShellTestRig(*testing.T)`, `waitForShellDone(*testing.T, *jobManager, string)`, `jobstore.ReadEvents(string) ([]jobstore.Event, error)`, `jobstore.Fold([]jobstore.Event) map[string]*jobstore.JobRecord`.
- Produces: `jobstore.Event.Background bool` with JSON tag `background,omitempty` in the start payload. `JobRecord.Background bool` means durably ever backgrounded after a fold; keep its existing `json:"-"` tag. Keep `commitDelayedShell(r *runningJob) error` unchanged.
- No new public RPC, journal kind, compatibility reader or lifecycle supervisor.

**Proof:** J01–J04, J05's fresh-fold/forwarded evidence, J06's detached boundary, J14; review focus 1.

- [ ] **Step 1: Add real-producer regressions.** Add `path/filepath` and `jobstore` imports to the promotion tests. Keep the existing live assertions and add a fresh journal assertion at handoff and after the existing stop/wait. Share this helper in the new durability test file:

```go
func backgroundFromJournal(t *testing.T, jm *jobManager, id string) bool {
	t.Helper()
	events, err := jobstore.ReadEvents(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil { t.Fatal(err) }
	record := jobstore.Fold(events)[id]
	if record == nil { t.Fatalf("missing durable job %s", id) }
	return record.Background
}

func TestRunShellImmediateBackgroundRetainsEligibility(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	result := runShell(t.Context(), jm, env, shellArgs{
		Command: "printf 'background evidence\\n'", Background: true,
	})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("background handoff: %+v", result)
	}
	if !backgroundFromJournal(t, jm, result.JobID) {
		t.Fatal("handoff acknowledged without durable background evidence")
	}
	waitForShellDone(t, jm, result.JobID)
	if !backgroundFromJournal(t, jm, result.JobID) {
		t.Fatal("completion lost background evidence")
	}
}
```

Use the existing fake clock with a **real** `LocalExecutionEnvironment` for promotion. Advance the wait timer as the existing `TestRunShellPromotionMarksRecordBackground` does. Assert the journal marker immediately after `runShell` returns, before stopping. Also add these concrete cases in the new file, reusing the real rig and existing timeout barriers:

| Test name | Producer / independent assertions |
| --- | --- |
| `TestRunShellForegroundRetentionIsNotBackground` | `printf` inline; call the returned `settle(true)` closure; a retained job/output handle exists, journal background is false, stored bytes match command output |
| `TestRunShellForegroundRuntimeLimitIsNotBackground` | Existing foreground runtime-timeout rig; return has `RunningInBackground == false`, durable record remains false and timeout outcome/reason stays unchanged |
| `TestRunShellForegroundCancellationIsNotBackground` | Cancel the foreground context before promotion using the existing barrier; return is stopped/cancelled with `RunningInBackground == false`, no background acknowledgement or durable start, and inline output remains intact |
| `TestRunShellBackgroundOutcomesRetainEligibility` | Real exit 0, exit 7, background runtime limit (`stopped` / `run_timeout`), and `jm.stop` cancellation (`cancelled` / `stopped_by_parent`); read fresh records and assert exact existing status, reason and exit code as well as eligibility |
| `TestRunShellBackgroundStartFailureDoesNotAcknowledge` | Existing `failAppendN` seam fails start once; real subprocess runs through existing cleanup; return is `start_failed`, never successful background handoff |
| `TestRunShellBackgroundForwardFailureDoesNotAcknowledge` | Existing `jm.forward` transport seam rejects start; assert existing local terminal failure/receipt cleanup and no background success |
| `TestRunShellBackgroundTerminalRetryPreservesEligibility` | Existing terminal append-failure seam; release retry with its existing clock/condition barrier, then assert one authoritative terminal generation and durable marker |

For forwarded success, wire the existing `jm.forward` boundary to a second actual `jobstore.Store.Append`, with the existing parent attachment fields. Compare owner/parent/origin fields and the folded marker in both journals. Reopen the store and construct a fresh manager **after** completion; do not simulate restart by killing a still-live process. Reuse existing detached-command coverage unchanged and assert no managed journal row is introduced.

Add a fold test by decoding literal journal JSON, so it initially fails behavior rather than compilation:

```go
func TestFoldBackgroundEvidenceSurvivesTerminal(t *testing.T) {
	var start Event
	if err := json.Unmarshal([]byte(`{"kind":"job_started","seq":1,"job_id":"bg","type":"shell","background":true}`), &start); err != nil {
		t.Fatal(err)
	}
	record := Fold([]Event{start, {
		Kind: EventJobFinished, Seq: 2, JobID: "bg", Status: StatusFailed,
	}})["bg"]
	if !record.Background || record.Status != StatusFailed {
		t.Fatalf("fold lost eligibility or outcome: %+v", record)
	}
}
```

Keep the existing off-wire `JobRecord` assertions. Add JSON round-trip and incremental `Apply` checks for marked start events. An unmarked literal legacy start stays false. A duplicate unmarked start must not clear a true marker; first terminal status/generation still wins.

- [ ] **Step 2: Run the new durability tests red.** From the repository root:

```bash
go test ./agent/internal/jobstore -run '^TestFoldBackgroundEvidenceSurvivesTerminal$' -count=1 -v
go test ./agent -run '^TestRunShell(ImmediateBackgroundRetainsEligibility|PromotionMarksRecordBackground|BackgroundModeStillMarksRecord)$' -count=1 -v
```

Expected: assertions report lost durable background evidence. The existing live flag must still pass. Record each command independently; do not use a preceding failure to skip the second command.

- [ ] **Step 3: Add the marker at its existing authority.** Add the event field. In `applyEvent`'s start branch preserve true evidence:

```go
Background bool `json:"background,omitempty"` // Event start payload

// In applyEvent, EventJobStarted:
r.Background = r.Background || e.Background

// In commitDelayedShell's EventJobStarted literal:
Background: rec.Background,
```

Move promotion's existing `jm.mu`-guarded flag assignment **before** `commitDelayedShell`. Keep the actual runtime-timeout check before that promotion path:

```go
jm.mu.Lock()
run.rec.Background = true
jm.mu.Unlock()
if err := jm.commitDelayedShell(run); err != nil {
	handle.Signal()
	if errors.Is(err, errDelayedShellStartForwardTerminalFailed) {
		go jm.finalizeShellUntilDurable(run.rec.JobID, jobstore.StatusFailed, "forward_failed", nil)
	} else if !errors.Is(err, errDelayedShellStartForwardFailed) {
		jm.discardDelayedShell(run)
	}
	return shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}
}
```

Use the existing failure branch's exact cleanup/result construction when moving these lines, rather than replacing that branch with a new lifecycle policy. Keep explicit background, retained foreground settlement and foreground runtime timeout at their existing call sites. Include the field in the existing forwarded event automatically, not a second write. Update field/clone comments and the promotion test helper's stale “live-only” wording.

- [ ] **Step 4: Rerun all producer cases and the jobstore suite.**

```bash
go test ./agent/internal/jobstore -count=1
go test ./agent -run '^TestRunShell|^Test.*(Terminal.*Durab|Shell.*Forward|Detached)' -count=1
```

Expected: both commands exit 0 with no failures; producer outcome, journal reconstruction, forwarding and cleanup assertions pass. Foreground cancellation remains ineligible.

Read the selected test definitions before claiming coverage. Add any newly named tests to the focused pattern explicitly if they do not match. Confirm actual terminal fields, current cleanup and error output, not only boolean values. Update `docs/job-control.md` and S12 to say that background eligibility survives completion/restart, while retained foreground output and detached commands remain separate.

- [ ] **Step 5: Format and commit only this slice.**

```bash
gofmt -w agent/internal/jobstore/event.go agent/internal/jobstore/fold.go agent/internal/jobstore/record.go agent/internal/jobstore/store.go agent/internal/jobstore/record_test.go agent/job_shell.go agent/job_shell_promotion_background_test.go agent/job_shell_background_durability_test.go
git diff --check
git add agent/internal/jobstore/event.go agent/internal/jobstore/fold.go agent/internal/jobstore/record.go agent/internal/jobstore/store.go agent/internal/jobstore/record_test.go agent/job_shell.go agent/job_shell_promotion_background_test.go agent/job_shell_background_durability_test.go docs/job-control.md docs/product/subsystems.md
git diff --cached
git commit -m "fix(jobs): persist background handoff evidence"
```

If an existing test file requires an additional assertion, stage that exact path separately after inspecting it. Keep hooks enabled.

## Task 2: Filter activity pages and their authoritative counts

**Files:**
- Modify: `agent/session_activity_jobs.go:439-455`, `agent/session_activity.go:464-477`.
- Create/test: `agent/session_activity_background_test.go`.
- Update eligible fixtures only: `agent/session_activity_test.go`, `agent/session_activity_ownership_test.go`, `agent/session_activity_partial_test.go`, `agent/session_activity_page_budget_paging_test.go`, `agent/session_activity_warm_counts_test.go`.
- Modify test helper: `agent/jobs_activity_incremental_fold_test.go:37-63`, preserving its existing diagnostic default.
- Update: `docs/product/session-activity.md`, S05/S12 in `docs/product/subsystems.md`.

**Source evidence:** `jobsPage` checks candidates before projection and page admission. `summary` counts folded shell records in `index.Jobs`. Both already share bounded source reconstruction. Live overlay adds output length/status after a record is admitted; it must not establish eligibility.

**Interfaces:**
- Consumes: Task 1's folded `JobRecord.Background` and existing `LoadSessionActivityJobs`, `LoadSessionActivitySummary`, `Session.ListActivityJobs`, `Session.ActivitySummary`.
- Produces: private `activityJobEligible(record *jobstore.JobRecord) bool`, used by **both** activity loops; existing public responses retain their exact types.
- Test helper: factor existing writer into `writeShellJobLogFast(t *testing.T, stateDir, sessID string, n int, background bool) string`; existing `writeJobLogFast` calls it with false, new `writeActivityJobLogFast` calls it with true. The emitted start event gets the explicit argument. Diagnostic/legacy fixtures keep their original classification.

**Proof:** J03–J07, server portion of J12/J13; review focus 2.

- [ ] **Step 1: Add mixed legacy/foreground/background pagination tests.** In the new file, use a real journal store and fixed times. The existing `savePastActivityMeta` establishes retained scope. This is the core assertion, using literal event JSON before the new field exists:

```go
func TestSessionActivityBackgroundEligibilityMatchesPagesAndCounts(t *testing.T) {
	t.Parallel()
	stateDir, id := t.TempDir(), "eligiblejobs"
	savePastActivityMeta(t, stateDir, id, "Root")
	store, err := jobstore.Open(filepath.Join(jobsDir(stateDir, id), "jobs.jsonl"))
	if err != nil { t.Fatal(err) }
	defer store.Close()
	for i, background := range []bool{true, false, true, false, true} {
		raw := fmt.Sprintf(`{"kind":"job_started","job_id":"job_%d","type":"shell","owner_session_id":%q,"started_at":"2026-10-03T12:00:00Z","background":%t}`, i, id, background)
		var event jobstore.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil { t.Fatal(err) }
		if err := store.Append(event); err != nil { t.Fatal(err) }
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 2}
	seen := map[string]bool{}
	for attempts := 0; attempts < 20; attempts++ {
		page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
		if err != nil { t.Fatal(err) }
		for _, row := range page.Jobs {
			if !row.Background || seen[row.JobID] { t.Fatalf("invalid or duplicate row: %+v", row) }
			seen[row.JobID] = true
		}
		if page.Page.Complete { break }
		if page.Page.NextCursor == "" { t.Fatal("incomplete page has no continuation") }
		params.Cursor = page.Page.NextCursor
	}
	if len(seen) != 3 || seen["job_1"] || seen["job_3"] { t.Fatalf("eligible membership: %v", seen) }
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 3 || summary.Jobs.Active != 3 {
		t.Fatalf("counts disagree: %+v, %v", summary.Jobs, err)
	}
}
```

Assert first-page eligible capacity separately: two rows when the small fixture fits one scan. For the long-history case, insert more than `activityMaxWorkUnits` excluded events between eligible rows. Follow valid bounded empty continuations; require eventual complete traversal, eligible counts, no duplicate or false completed empty history. Never demand an unbounded scan to fill a page.

Add `TestSessionActivityLegacyForegroundDataRemainReadable`: write an unmarked historical journal and actual output bytes; copy journal/output bytes before the read; assert activity excludes it, diagnostic `LoadSessionJobActivityTree` still includes it and existing direct output/transcript reads return the exact bytes. Compare the original files afterward. Do not upgrade it by appending a marker. Add retained inline output and timeout cases from actual Task 1 producers using a real Session's manager, not synthetic client responses.

Extend the existing 451-row, equal-ID/multiple-owner, warm summary and unavailable-source cases. Mark only fixtures that represent newly backgrounded jobs. Add a child owner with interleaved marked/unmarked records; direct scope excludes siblings, subtree includes each eligible owner once. A temporarily unavailable child leaves healthy rows usable and counts unknown; a fresh recovered read admits its eligible rows and regains known counts. Preserve all budget and incarnation assertions.

- [ ] **Step 2: Run the new domain tests red.**

```bash
go test ./agent -run '^TestSessionActivity(BackgroundEligibilityMatchesPagesAndCounts|LegacyForegroundDataRemainReadable)$' -count=1 -v
```

Expected: unmarked/foreground rows leak into activity and totals disagree with the explicit eligible set. Diagnostic preservation assertions must remain passing.

- [ ] **Step 3: Apply one eligibility predicate before admission and counting.** Add the jobstore import to `session_activity.go` if needed. Define in `session_activity_jobs.go`:

```go
func activityJobEligible(record *jobstore.JobRecord) bool {
	return record != nil && record.Type == jobstore.JobShell && record.Background
}
```

Replace `record == nil || record.Type != jobstore.JobShell` in the existing `jobsPage` candidate check with `!activityJobEligible(record)`. Keep cutoff/highwater tests and the `token.After = key; continue` path. Replace `string(job.Type) != "shell"` in `summary` with `!activityJobEligible(job)`. Leave unknown-source handling and outcome counters intact. Eligibility is decided from folded evidence before live output overlays and response limits.

Factor the test writer without duplicating serialization. Add `Background: background` to its start-event literal, retaining the unmarked diagnostic wrapper. Do not filter `jobs.go`, `jobs_activity.go`'s complete diagnostic loader, job transcript readers or RPC diagnostics.

- [ ] **Step 4: Run focused activity and diagnostic preservation suites.**

```bash
go test ./agent -run '^TestSessionActivity|^TestLoadSessionJobActivityTree|^TestRunShellForeground' -count=1
```

Expected: exit 0 with no failures; eligible pages/counts agree, bounded traversal completes, and complete diagnostics/direct output remain readable.

Check the names selected, including the budget tests, before execution. Record J03/J06 output byte equality and J07 scope/count sets explicitly. Update the session activity guide's Jobs eligibility and unknown-count promises, plus S05/S12 responsibilities. Describe one read/count authority and unchanged diagnostic history.

- [ ] **Step 5: Format the touched Go files, inspect, and commit.** Stage only the named modified files, new test file and owning guides:

```bash
git diff --check
git add agent/session_activity_jobs.go agent/session_activity.go agent/session_activity_background_test.go agent/jobs_activity_incremental_fold_test.go agent/session_activity_test.go agent/session_activity_ownership_test.go agent/session_activity_partial_test.go agent/session_activity_page_budget_paging_test.go agent/session_activity_warm_counts_test.go docs/product/session-activity.md docs/product/subsystems.md
git diff --cached
git commit -m "fix(activity): scope Jobs pages and counts to background work"
```

## Task 3: Pin routing, shared adaptation and recovery contracts

**Files:**
- Test/fixture: `cmd/evener-hub/app_session_activity_test.go`, `cmd/evener-hub/app_jobs_test.go`.
- Test: `cmd/evener-hub/internal/appsource/session_activity_test.go`.
- Test: `appwire-client/typescript/sessionActivityStore.test.ts`, `appwire-client/typescript/sessionActivityPresentation.test.ts`, `appwire-client/typescript/threadSubscription.test.ts`.
- Update: `docs/product/session-activity.md` if the proof exposes a directly conflicting recovery promise.

**Source evidence:** Root retained routing uses `dispatchHubJobsRPC` and actual domain loaders. `RemoteHubSource` translates owner refs, not opaque job IDs/cursors. `SessionActivityStore` already tracks a displayed boundary and replays fresh pages through it; `projectSessionActivity` is the shared adapter. Preserve those owners rather than introducing a client filter or recovery loop.

**Interfaces:**
- Consumes: Task 2's unchanged `SessionJobsResponse` / `SessionActivitySummary`; `SessionActivityStore.observe("jobs"): () => void`, `.loadMore("jobs"): Promise<void>`, `.refresh("jobs"): Promise<void>`, `.getSnapshot()`, `.dispose()`; `FakeClient.emitStateChange` / `.emitReady` at the transport boundary.
- Produces: contract evidence only, no new production interface. Keep store/adapter production code unchanged when the preservation tests pass. If a new case fails, record and trace it before changing its existing owner; do not invent a second reader.
- Extend root test-only `persistedJobFixture` with a Go `background bool` field, adding that boolean to its start-event serialization. Set it explicitly for newly eligible fixtures, never default every diagnostic fixture to true.

**Proof:** J05–J07, shared-store portion of J10/J12/J13; review focus 3.

- [ ] **Step 1: Exercise actual domain reads through local and remote routing.** Add `TestSessionActivityBackgroundPublicRoutes` beside the retained hierarchy test. Use `seedPastSessionWithActivity`, `writePersistedJobsLog` and `dispatchHubJobsRPC`. Give root and child marked background jobs plus unmarked foreground/legacy jobs; keep output files for all. Walk both scopes with limit 1, then read summary. Compare explicit `(ownerRef, jobId)` sets and known totals, not counts reconstructed by the implementation under test. Direct diagnostic/output RPCs must still return the excluded jobs.

Add a remote case using the existing loopback `newScriptedRemoteHub` and `activityHostRegistry`, but its handler must invoke the **actual** public retained loaders, not manufacture an eligible response:

```go
switch method {
case appwire.MethodEvenerThreadJobsList:
	var params appwire.SessionActivityListParams
	if err := json.Unmarshal(raw, &params); err != nil { t.Error(err); return nil }
	page, err := agent.LoadSessionActivityJobs(t.Context(), stateDir, root, params)
	if err != nil { t.Error(err); return nil }
	return page
case appwire.MethodEvenerThreadActivityRead:
	var params appwire.SessionActivityReadParams
	if err := json.Unmarshal(raw, &params); err != nil { t.Error(err); return nil }
	summary, err := agent.LoadSessionActivitySummary(t.Context(), stateDir, root, params)
	if err != nil { t.Error(err); return nil }
	return summary
}
```

Add the `primeradiant.com/evener/agent` import. The fixture's root request is translated from `east:<root>` to `local:<root>` by the real remote source. Check translated rows/counts and opaque cursor preservation. Keep equal-ID collisions between east/west and local unavailable-source fences in the existing AppSource tests. The real producer-to-hub/browser chain is completed in Task 4, without substituting these fixtures for producer evidence.

- [ ] **Step 2: Add a three-page shared recovery test.** In `sessionActivityStore.test.ts`, reuse its existing `owner`, `activityClient`, `activityState`, `jobFixture` and `jobsFixture` helpers exactly. This pins fresh content beyond page one:

```ts
test("background history retains a visible third-page boundary across refresh and reconnect", async () => {
  const client = activityClient();
  let version = "before";
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    const id = cursor === "third" ? "third" : cursor === "second" ? "second" : "first";
    const next = id === "first" ? "second" : id === "second" ? "third" : undefined;
    return jobsFixture([{ ...jobFixture(id, "command_exited_nonzero"), description: `${id} ${version}` }], next);
  });
  const store = owner(client);
  try {
    store.observe("jobs");
    await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
    await store.loadMore("jobs");
    await store.loadMore("jobs");
    expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["first", "second", "third"]);
    version = "refreshed";
    await store.refresh("jobs");
    expect(store.getSnapshot().jobs.rows.find((row) => row.jobId === "third")?.description).toBe("third refreshed");
    client.emitStateChange("reconnecting");
    expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["first", "second", "third"]);
    version = "reconnected";
    client.emitReady();
    await activityState(store, () => store.getSnapshot().jobs.rows.some((row) => row.jobId === "third" && row.description === "third reconnected"));
    expect(store.getSnapshot().jobs.rows.filter((row) => row.jobId === "third")).toHaveLength(1);
  } finally {
    store.dispose();
  }
});
```

Also pin old-client replies after reconnect, a changed session identity with reused job ID, a terminal transition on the third page, and a partial subtree whose owner recovers. Assert unknown summary remains unknown until authoritative counts return. Extend the existing thread-subscription tests to hold transcript + activity together and release each in both orders: one live subscription, final release unsubscribes once. The adapter must retain the marker, owner, terminal flag, outcome and delegate run generation unchanged.

- [ ] **Step 3: Run the focused evidence suites.** Tasks 1/2 carry the eligibility red-green proof; these routing assertions qualify that behavior through additional real boundaries. Shared preservation checks may already pass; record that honestly rather than claiming an unobserved pre-fix run.

```bash
go test ./cmd/evener-hub -run '^TestSessionActivity' -count=1
go test ./cmd/evener-hub/internal/appsource -run '^TestSessionActivity' -count=1
make web-preflight
cd cmd/evener-hub/frontend
npm test -- ../../../appwire-client/typescript/sessionActivityStore.test.ts ../../../appwire-client/typescript/sessionActivityPresentation.test.ts ../../../appwire-client/typescript/threadSubscription.test.ts
```

Expected: all commands exit 0; local/remote membership, scope/counts, shared adaptation, third-page recovery and subscription-lifetime assertions pass.

Preflight may install the pinned frontend tree only after plan approval. Read `scripts/web/web-preflight.sh`; never run `npm ci` through a symlink. Use the frontend's local test binary and import mapping, not a guessed shared-package `npm test` script. The package has no such script.

- [ ] **Step 4: Format, inspect and commit contract evidence.** Run the frontend's pinned Biome on the three touched shared test paths before staging, and gofmt the three Go test files.

```bash
git diff --check
git add cmd/evener-hub/app_session_activity_test.go cmd/evener-hub/app_jobs_test.go cmd/evener-hub/internal/appsource/session_activity_test.go appwire-client/typescript/sessionActivityStore.test.ts appwire-client/typescript/sessionActivityPresentation.test.ts appwire-client/typescript/threadSubscription.test.ts
git diff --cached
git commit -m "test(activity): pin background routing and loaded-page recovery"
```

## Task 4: Put every terminal web job in quiet Completed history

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/JobsTab.tsx:32-65`, `cmd/evener-hub/frontend/src/shell/activitybar/activityRows.tsx:41-57,100`.
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/JobsWatchesTabs.test.tsx`, `cmd/evener-hub/frontend/src/shell/activitybar/activityRows.test.tsx`, `cmd/evener-hub/frontend/src/shell/activitybar/activityApi.test.tsx`.
- Create: `cmd/evener-hub/background_jobs_browser_test.go`, `cmd/evener-hub/frontend/scripts/backgroundjobsguard/run.mjs`.
- Modify/test registration: `cmd/evener-dev/webbrowser.go`, `cmd/evener-dev/webbrowser_test.go`.
- Preserve: `ActivityViewport.tsx`, `activitySidebarStore.ts`, the disclosure store and the workspace action implementation. Change them only for a reproduced failure within this Jobs contract.
- Update: web presentation in `docs/product/session-activity.md`, S02 in `docs/product/subsystems.md`.

**Source evidence:** `JobsTab` currently keeps unsuccessful terminal rows outside its successful-only fold. `AgentsTab` already folds all terminal delegates. Row tones independently add danger color. Output uses the real `workspaceStore.openPane("transcript", ..., {slot:"secondary"})`; the current test replaces that action with a spy.

**Interfaces:**
- Consumes: filtered `JobActivityJob[]`, `SessionDelegate[]`, existing shared binding and `activityNodeID`, semantic viewport/disclosure identity.
- Produces: existing `JobsTab` and row props unchanged. `jobTone(job: JobActivityJob): Tone` reads the authoritative terminal flag. No additional client background filter.
- New guard: `TestBackgroundJobsBrowser` with `//go:build browserguard`; uses existing `startHubStack`, `fakellm.New`, real `appwire.Client`, real Chrome `Driver` from `cmd/evener-hub/frontend/scripts/skillguard/run.mjs`, and `evaluate`/`navigateTo` from `cmd/evener-hub/frontend/scripts/browserGuardCdp.mjs`.
- Driver JSON stdin: `{url, artifactDir, controlPath, milestonePath, rootRef, laterJobId, excludedJobId, eligibleCount}`. Do not print the authenticated URL or token. The Go fixture owns real producer controls; the driver owns browser input and evidence, not store seeding.

**Proof:** J07/J08, web J12/J13; review focus 4's late-page work.

- [ ] **Step 1: Replace the success-only assertion with terminal-history assertions.** Keep every current outcome and output/no-output case. The revised test must first find only running work, open the terminal fold, then find all terminal statuses with unchanged wording. Import the real pane descriptor and reset helper:

```ts
import "../../panes/transcript";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";

// In the existing JobsWatchesTabs test, remove the openPane spy.
resetWorkspaceStoreForTests();
render(<JobsTab scope={scope()} />);
await screen.findByText("command running");
for (const status of ["command_exited_nonzero", "killed", "cancelled", "stopped", "unknown"])
  expect(screen.queryByText(`command ${status}`)).toBeNull();
fireEvent.click(screen.getByText("7 completed jobs"));
for (const status of ["command_exited_nonzero", "killed", "cancelled", "stopped", "unknown"])
  expect(screen.getByText(`command ${status}`)).toBeTruthy();
fireEvent.click(screen.getByRole("button", { name: /successful command/ }));
const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
expect(opened).toMatchObject({
  slot: "secondary", params: { ref: "job:raw-output", parentRef: "source:owner" },
});
expect(workspaceStore.getState().focusedPaneId).toBe(opened?.id);
```

Add reset cleanup without mocking workspace decisions. In the test that currently expects a failed row immediately, open its one-job Completed fold first. Direct `JobRow`/`AgentRow` rendering must assert `styles.glyphQuiet` on terminal glyphs and unchanged failure/status text; awaiting-input/approval glyphs still use attention. Add a runtime-limited terminal job case. Retain outputless non-buttons and same-session remount disclosure tests.

Apply the same real workspace assertion to `activityApi.test.tsx`'s existing output-action case: remove its `openPane` mock, register the transcript pane and assert `{ref:"job:authoritative", parentRef:"source:owner"}` in the actual secondary pane. Extend `activityRows.test.tsx` for neutral terminal glyphs; keep Agents routing/history coverage in `activityApi.test.tsx`.

Extend closed-history paging to put active work on page three. Render through the real `ActivityViewport`; trigger its existing observer/page boundary, not a new fetcher. A terminal transition yields one qualified anchor in history, and output still opens its actual owner pane.

- [ ] **Step 2: Run the web regressions red.** After Task 3's preflight:

```bash
cd cmd/evener-hub/frontend
npm test -- src/shell/activitybar/JobsWatchesTabs.test.tsx src/shell/activitybar/activityRows.test.tsx src/shell/activitybar/activityApi.test.tsx
```

Expected: failed/stopped rows remain visible before opening the fold, terminal glyphs retain danger styling. Workspace tests exercise the real action; registration/setup failures are not behavioral evidence.

- [ ] **Step 3: Make the minimal presentation change.**

```ts
const running = collection.rows.filter((job) => !job.terminal);
const completed = collection.rows.filter((job) => job.terminal);
// Render running.map(renderJob), followed by the existing disclosure.

function jobTone(job: JobActivityJob): Tone {
  if (job.terminal) return "quiet";
  switch (jobStatusDotState(job.status, job.terminal)) {
    case "working": return "alive";
    case "needs-you": return "attention";
    case "failed": return "danger";
    default: return "quiet";
  }
}
// In agentTone:
if (sub.terminal) return "quiet";
// JobRow calls jobTone(job), retaining jobStatusDisplay(job.status, job.reason).
```

Remove the obsolete unsuccessful array and update its misleading comment. Keep disclosure ID, loaded count wording, row keys, page boundary, owner refs and output action. Do not regroup web Agents or change web session-list error marks.

- [ ] **Step 4: Add a Jobs-only real-server Chrome guard.** Follow the existing real-server browser test's fixture lifecycle without modifying `cascade_browser_test.go`. Create 151 real background shell jobs through the scripted provider's `shell` tool calls with `background:true`, interleave retained foreground commands, and hold the oldest background command on a fixture-owned filesystem barrier. `startHubStack` uses `fake/fake-test-model` on the generic surface, which keeps canonical `shell`; `exec_command` is the OpenAI surface's alias, not this fixture's name. Assert the first provider request advertises `shell` before replying. This exceeds the public 50-row page size and puts live work beyond several terminal pages. Use real nonzero exit, stop and runtime-limit outcomes; capture expected failure output.

Read `test/e2e/fakellm` and the existing `startHubStack` helper before writing the fixture. Use real `client.ThreadStart`/`ThreadJobsList`/`ThreadActivityRead` calls to verify producer IDs, expected eligible totals and complete diagnostic membership. Never inject frontend stores, fake RPC outcomes or change command timeouts to make the browser pass.

The new driver imports the existing driver rather than copying its Chrome launcher. Its assertions include:

```js
import assert from "node:assert/strict";
import { Driver } from "../skillguard/run.mjs";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

const anchor = (ownerRef, jobId) => `job:${JSON.stringify([ownerRef, jobId])}`;
const rowSelector = (ownerRef, jobId) =>
  `[data-activity-anchor=${JSON.stringify(anchor(ownerRef, jobId))}]`;
// After real clicks and scrolls have loaded and revealed the later job:
const selector = rowSelector(fixture.rootRef, fixture.laterJobId);
assert.equal(await evaluate(driver.send, `document.querySelectorAll(${JSON.stringify(selector)}).length`), 1);
```

Check the shared `activityNodeID` construction against this test literal before using it. Require the row's rectangle to intersect the actual activity viewport, with computed quiet color and truthful terminal wording. Measure that row's offset from the viewport before and after refresh, reconnect and document reload; assert the same semantic anchor with an offset tolerance of 2 CSS pixels when content/viewport is unchanged. Preserve an open Completed disclosure and existing scope. A failed row must remain hidden while closed; later live work must be found without opening history.

Use a real network interruption for reconnect, for example CDP `Network.emulateNetworkConditions` with `offline:true`, then false. Require an observed actual socket close/reconnect and updated row content; toggling a flag without transport interruption proves nothing. Use the Go-owned barrier to finish the late running job, then require exactly one row under Completed with its actual outcome and real transcript output opened. Read console/page errors at the end of **this new** journey. Capture screenshot, rendered DOM, row rectangles, cursor-bearing RPC evidence and milestones on failure, without credentials. `driver.stop` and Go fixture cleanups must run on every exit.

Register `backgroundjobsguard` in the existing `browserGuards`, `needsBuild` and `unsignalled` sets, with this launch contract:

```go
case backgroundJobsGuard:
	return guardSpec{
		name: guard,
		argv: []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestBackgroundJobsBrowser$", "-count=1"},
		dir: ".", env: []string{vite},
	}
```

Define `backgroundJobsGuard = "backgroundjobsguard"` beside the existing constants. Update guard registration tests to require build and never-signal handling. This is the gate's existing ownership, not a new detached monitor. Run the new browser journey red before the JobsTab fix if preparing it first; otherwise retain the unit red and record the browser as post-fix integration evidence.

- [ ] **Step 5: Verify and document web behavior.**

```bash
# From frontend: pinned Biome on touched src files, then return to repository root.
make test-web
go test ./cmd/evener-dev -run 'Test.*Browser' -count=1
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundJobsBrowser$' -count=1 -v
```

Expected: all commands exit 0 with no failures; the new Chrome journey observes actual producer eligibility/counts, quiet terminal history, reachable later-page live work, current recovered content and the real output pane.

Run the canonical `make test-web-browser` at branch qualification, including its registered new journey. This is impacted gate qualification, not reopening the merged Overview work. Unit action/state assertions and Chrome rendering are separate evidence. Update the activity guide and S02 with quiet terminal history and preserved live attention/output access.

- [ ] **Step 6: Inspect and commit web code, tests, guard and guides.**

```bash
git diff --check
git add cmd/evener-hub/frontend/src/shell/activitybar/JobsTab.tsx cmd/evener-hub/frontend/src/shell/activitybar/activityRows.tsx cmd/evener-hub/frontend/src/shell/activitybar/JobsWatchesTabs.test.tsx cmd/evener-hub/frontend/src/shell/activitybar/activityRows.test.tsx cmd/evener-hub/frontend/src/shell/activitybar/activityApi.test.tsx cmd/evener-hub/background_jobs_browser_test.go cmd/evener-hub/frontend/scripts/backgroundjobsguard/run.mjs cmd/evener-dev/webbrowser.go cmd/evener-dev/webbrowser_test.go docs/product/session-activity.md docs/product/subsystems.md
git diff --cached
git commit -m "fix(web): fold terminal Jobs into quiet history"
```

## Task 5: Separate quiet native histories without hiding active work

**Files:**
- Modify: `mobile-native/src/subagents/activityList.ts`, `SubagentsScreen.tsx`, `subagentModel.ts`, `SubagentStrip.tsx`, `SubagentRowView.tsx`, `ShellJobRowView.tsx` (all under `mobile-native/src/subagents/`).
- Test in that directory: `activityList.test.ts`, `subagentModel.test.ts`, `SubagentsScreen.test.tsx`, `SubagentStrip.test.tsx`, `ShellJobRowView.test.tsx`, `sessionActivityBinding.test.ts`.
- Preserve/test: `mobile-native/src/subagentRows.test.tsx`, `mobile-native/src/subagents/stopOffer.test.ts`, `stopRequests.test.ts`, `subagentTree.test.ts`.
- Create: `mobile-native/src/subagents/activityHistory.test.tsx` for recorded producer + mixed history/recovery evidence.
- Update: `mobile-native/README.md`, native presentation in `docs/product/session-activity.md`, S03 in `docs/product/subsystems.md`.

**Source evidence:** Native flattening visits descendants independently and preserves true `failed` row state. The current list couples failure-first sections and the Done fold; summary tally retains separate failure/completed outcomes. `SubagentTree` binds the shared subtree store. Existing renderer tests use platform boundary substitutes; retain actual Evener flattening, adapter, list decision and row components.

**Interfaces:**
- Consumes: `ActivityListRow = SubagentRow | ShellJobRow`, unchanged `summaryTally(...counts): SubagentTally | null`, `SubagentTree.reload/loadMore/setClient` and existing shared package projection.
- Produces: `ActivityFilter = "all" | "running" | "done"`; `activityListItems` view gains `completedJobsOpen: boolean`, keeps `doneOpen` for delegates.
- `ActivityListItem` keeps existing `doneFold`; add `{kind:"completedJobsFold"; count:number; open:boolean}`. Presentation sections use `"running" | "done" | "completed"` and `activityListKey` adds stable `"completed-jobs-fold"`. Existing row keys stay unchanged.
- Export existing `newestFirst(a: ActivityListRow, b: ActivityListRow): number` from `subagentModel.ts` for history ordering, instead of copying the comparator. Keep row outcome state, `shellJobState`, stop identity and generation assertions unchanged.
- No native background filter, domain outcome relabeling or new subscription/recovery owner.

**Proof:** J09–J13; review focus 3/4.

- [ ] **Step 1: Pin independent lifecycle histories in the pure list tests.** Extend the existing `view` with `completedJobsOpen:false`. With its recorded failed/running/done delegates, default All must omit terminal delegate rows and show `doneFold` count 2. Add failed and successful terminal jobs using the existing `withJobs` fixture. Assert separate closed folds, independent opening and explicit Done showing all terminal rows:

```ts
const items = activityListItems(all, { ...view, completedJobsOpen: false });
expect(items.filter((item) => item.kind === "row").every((item) => item.row.state === "running")).toBe(true);
expect(items.filter((item) => item.kind === "doneFold")).toEqual([
  { kind: "doneFold", count: 2, open: false },
]);
const done = activityListItems(all, { ...view, filter: "done", completedJobsOpen: false });
expect(done.some((item) => item.kind === "doneFold" || item.kind === "completedJobsFold")).toBe(false);
expect(done.filter((item) => item.kind === "row").some((item) => item.row.state === "failed")).toBe(true);
expect(done.filter((item) => item.kind === "row").every((item) => item.row.state !== "running")).toBe(true);
```

Test opening only delegates, only jobs and both; job/delegate IDs collide but qualified row keys do not. Search narrows loaded labels without altering authoritative chip totals. An unmatched search preserves its clear control. Missing-owner notices still name known titles once. Terminal history sorts newest first across **all** outcomes, keeping existing tie order.

In `SubagentsScreen.test.tsx`, update the current 55-delegate fixture expectations: 32 Running, **23 Done**, no Failed chip or failure strip. The terminal race parent is folded, but its running `Reproduce the race` child remains outside history. Render and press that child, retain its actual identity and stop-offer access. Opening parent history must retain the parent's original reason/outcome and active-subtree controls.

Pin strip behavior with these expected inputs: failure-only tally produces no active strip; `{running:2, failed:3, done:5}` draws Running 2 plus quiet Done 8, with no failure-floor segment. Direct row rendering still says Failed / Command failed / Stopped as appropriate in ordinary ink. Live question/approval treatment remains unchanged.

- [ ] **Step 2: Run native grouping regressions red.** First read `mobile-native/AGENTS.md` and Expo 57 docs. Install native pinned dependencies only after confirming `node_modules` is **not** a symlink; `native-preflight` does not install them. Then from root:

```bash
make native-preflight
cd mobile-native
npm test -- src/subagents/activityList.test.ts src/subagents/SubagentsScreen.test.tsx src/subagents/SubagentStrip.test.tsx src/subagents/ShellJobRowView.test.tsx
```

Expected: failed parent appears first, terminal count excludes failures, jobs/delegates share one fold, and failure-only strip/callouts remain. Read the native wrapper before running it; it forwards these paths to real Vitest with budgeted workers.

- [ ] **Step 3: Build histories over preserved outcome rows.** In `activityListItems`, filter search once, then group by lifecycle and kind. Use the exported existing comparator:

```ts
const matching = rows.filter((row) => matchesSearch(row, view.query));
const running = matching.filter((row) => row.state === "running").sort(newestFirst);
const delegates = matching.filter((row) => row.kind === "subagent" && row.state !== "running").sort(newestFirst);
const jobs = matching.filter((row) => row.kind === "job" && row.state !== "running").sort(newestFirst);
```

Append a Running section under All/Running. Under All, append Done and Completed folds with their own open state and rows only when open. Under Done, append both matching terminal sections and rows without extra disclosure. Keep missing notices and qualified keys. Do not change `subagentState`'s `failed` result or flattening's descendant walk.

In `SubagentsScreen`, add `completedJobsOpen` state alongside `doneOpen`, pass both into the memo, render the new fold with a generic history-fold component taking `label:"Done"|"Completed"`, count, open and toggle. Preserve the Done component's existing accessibility/tap behavior. Use lifecycle filter counts:

```ts
const filters = [
  { id: "running" as const, label: "Running", count: activityTally?.running },
  { id: "done" as const, label: "Done", count: activityTally ? activityTally.failed + activityTally.done : undefined },
];
```

Keep All's authoritative total and `…` for unknown. Do not change `summaryTally`. Remove Failed filter styling. For strip geometry combine failure/completed into the quiet terminal segment and remove failure minimum/ordering; preserve the running segment's existing minimum. Give terminal failed row glyphs/words ordinary ink, retaining reason and exit evidence. Do not remove active-descendant flags, stop requests, output navigation or genuine live attention.

- [ ] **Step 4: Exercise recorded producers through the real shared adapter and native renderer.** In `activityHistory.test.tsx`, reuse the platform boundary setup from `SubagentsScreen.test.tsx`. Feed `subagentOutcomesDelegatesResponse()` and `subagentResumedDelegatesResponse()` from `@evener/appwire-client/testing/subagentWireFixtures` into the typed delegate API boundary. Do not replace `projectSessionActivity`, flattening, activity decisions, workspace/navigation decisions or row components.

Assert reported, failed and stopped producers retain their recorded status/reason/report and settled `runGeneration`; failed/stopped rows appear in Done after opening it. Introduce a new running generation and deliver the old report afterward: it must not become the current run's report. Keep existing `subagentRows.test.tsx` generation assertions and stop tests strict.

Give the native typed Jobs boundary three cursor pages. Load them through `SubagentTree.loadMore`, render an actual row from page three, open only Completed and refresh with changed description. Disconnect/reconnect through `setClient(null)` then the new client: healthy loaded evidence remains while recovering, the same later row renders updated content once, both history states and the selected filter/query survive. Assert `FlatList` uses the same qualified key for that visible row before/after; preserve its selected/visible row intent. Trigger its real `onEndReached` callback with both folds closed and require a later running row. Move that row terminal, refresh/reconnect and require exactly one accessible output row in Completed.

Native renderer evidence checks intent, stable identity and rendered rows. It does **not** establish physical-device scroll geometry, safe areas, keyboard behavior or VoiceOver navigation. Record that limit. If an intent/identity test fails, trace the existing binding/list owner before changing it. Device geometry requires separate qualification.

- [ ] **Step 5: Run impacted native and shared tests, then update owning guides.**

```bash
cd mobile-native
npm test -- src/subagents/activityList.test.ts src/subagents/subagentModel.test.ts src/subagents/SubagentsScreen.test.tsx src/subagents/SubagentStrip.test.tsx src/subagents/ShellJobRowView.test.tsx src/subagents/activityHistory.test.tsx src/subagents/sessionActivityBinding.test.ts src/subagents/subagentTree.test.ts src/subagents/stopOffer.test.ts src/subagents/stopRequests.test.ts src/subagentRows.test.tsx
npm run check
```

Expected: both commands exit 0 with no failures; independent histories, recorded generations, later-page recovery, active descendant controls and native typecheck pass.

Run native pinned Biome on every touched native src path before `make test-native`. Update the native README, activity guide and S03 to state separate Done/Completed history, terminal-inclusive Done filter, neutral outcomes, active descendants and one shared lifetime/recovery owner. Preserve unknown counts and recorded generation truth.

- [ ] **Step 6: Inspect and commit this native slice.** Stage the exact source/test paths listed above and the three owning guides, after pinned formatting and diff review. Use:

```bash
git diff --check
git add mobile-native/src/subagents/activityList.ts mobile-native/src/subagents/SubagentsScreen.tsx mobile-native/src/subagents/subagentModel.ts mobile-native/src/subagents/SubagentStrip.tsx mobile-native/src/subagents/SubagentRowView.tsx mobile-native/src/subagents/ShellJobRowView.tsx mobile-native/src/subagents/activityList.test.ts mobile-native/src/subagents/subagentModel.test.ts mobile-native/src/subagents/SubagentsScreen.test.tsx mobile-native/src/subagents/SubagentStrip.test.tsx mobile-native/src/subagents/ShellJobRowView.test.tsx mobile-native/src/subagents/activityHistory.test.tsx mobile-native/src/subagents/sessionActivityBinding.test.ts mobile-native/README.md docs/product/session-activity.md docs/product/subsystems.md
git diff --cached
git commit -m "fix(native): separate quiet delegate and Jobs histories"
```

Remove unchanged paths from the staging command after inspecting status; stage any additional changed preservation test by its exact path. Never stage every file in the directory.

## Task 6: Remove native delegate failure chips and preserve session attention

**Files:**
- Modify: `mobile-native/src/board/BoardRow.tsx:180-183,292-345`, native `subagentChipText` in `mobile-native/src/board/attention.ts:337`.
- Modify: `mobile-native/src/session/sessionState.ts:78-127`, `SessionHeader.tsx:192-194` in that directory.
- Test: `mobile-native/src/board/BoardRow.test.tsx`, `attention.test.ts`, `BoardScreen.test.tsx` in that directory.
- Test: `mobile-native/src/ProjectsScreen.test.tsx`, `mobile-native/src/PinSectionsScreen.test.tsx`, `mobile-native/src/session/sessionState.test.ts`, `mobile-native/src/session/SessionHeader.test.tsx`.
- Preserve: shared `appwire-client/typescript/state/navigation/selectors.ts` and native own-session attention classification.
- Update: `mobile-native/README.md`, `docs/product/session-activity.md`, S03/S05 in `docs/product/subsystems.md`.

**Source evidence:** Board, project and pinned rows share `sessionSubagentChip`/label helpers. Their failure count is separate from `attention.ts`'s own-session errored classification and priority. The session header's subagent chip uses authoritative activity counts. Web navigation uses the shared selector too, so changing that selector would broaden scope.

**Interfaces:**
- Consumes: existing navigation tallies for row presentation, authoritative `SessionActivityCounts` for the header and unchanged `summaryTally` from Task 5.
- Produces: native-only `subagentChipText(tally: {running:number}): string`; `sessionSubagentChip`/label return no chip for failure-only tally, retain existing live/root gates and active counts.
- Remove the native `ContextChip.failed` suffix field and its renderer branch. `contextChips(...)` keeps its exact parameters, neutral Subagents access, authoritative total and unknown-count fallback.

**Proof:** J11; review focus 5.

- [ ] **Step 1: Add native-only quiet-chip regressions.** In the existing BoardRow test fixtures, assert failure-only rows render no `subagent-chip`, including their accessibility labels. Mixed running/failed rows show only active count:

```ts
it("does not call out settled delegates on a working session", () => {
  const tree = mount({ item: item("working", {
    state: "active", subagents: { running: 2, failed: 3, done: 0 },
  }) });
  expect(textWith(tree, "2 running")).toHaveLength(1);
  expect(textWith(tree, "3 failed")).toHaveLength(0);
  expect(pressable(tree).props.accessibilityLabel).not.toContain("3 failed");
});
```

Check the existing navigation fixture's exact tally property/type before writing the case; use its actual field rather than adding a test-only property. In `attention.test.ts`, change native chip-word tests to Running-only and add error/question/approval/offline preservation combinations. Existing own-session Failed word and danger mark tests remain passing.

In `sessionState.test.ts`, pass authoritative counts with total 5, active 2, failed 3 and completed 0. Expect label `Subagents 5`, accessibility `Subagents, 5`, neutral attention false and no failure suffix. Unknown authoritative counts plus a known delegate roster keep `Subagents` access with “count unknown”; disconnected behavior remains unchanged. Render these actual chips in `SessionHeader.test.tsx` and exercise their existing action.

Render Board, Projects and Pins using their existing real list components. Assert no failure-only visual or spoken delegate count, retained active indicators and session navigation. Feed own error + failed delegates, error + question, approval and offline cases through actual attention classification: own errors still enter Needs you and retain priority; offline still wins its established rule; genuine question/approval controls remain available. Stale input must not become current attention.

- [ ] **Step 2: Run quiet-chip tests red.**

```bash
cd mobile-native
npm test -- src/board/BoardRow.test.tsx src/board/attention.test.ts src/session/sessionState.test.ts src/session/SessionHeader.test.tsx
```

Expected: failure-only chip and failed suffix remain. Own-session attention tests should already pass; keep their evidence separate from the new failing behavior.

- [ ] **Step 3: Change native presentation helpers only.** Retain the existing shared selector, with a native running-only display gate:

```ts
const chipTally = (session: NavigationSessionSummary) => {
  const tally = isTopLevel(session) ? subagentTallyToShow(session) : null;
  return tally && tally.running > 0 ? tally : null;
};

export function subagentChipText(tally: { running: number }): string {
  return tally.running > 0 ? `${tally.running} running` : "";
}
```

Remove failed markup from `SubagentChip`; pass only running to the Board accessibility helper's object literal. `sessionSubagentChip` and `sessionSubagentChipLabel` use the same gate, so Projects/Pins inherit it. Leave `classify`, `needsYouRank`, native own-error marks, session error decisions and shared web navigation untouched.

In `contextChips`, construct the Subagents chip without failed text:

```ts
chips.push({
  kind: "subagents", label: `Subagents ${tally.total}`, attention: false,
  accessibilityLabel: `Subagents, ${tally.total}`,
});
```

Remove `ContextChip.failed` and the matching SessionHeader suffix. Keep total provenance, actions, unknown/disconnected gates, blocked goal attention and fresh document dots.

- [ ] **Step 4: Verify all native list surfaces and canonical gates.**

```bash
cd mobile-native
npm test -- src/board/BoardRow.test.tsx src/board/attention.test.ts src/board/BoardScreen.test.tsx src/ProjectsScreen.test.tsx src/PinSectionsScreen.test.tsx src/session/sessionState.test.ts src/session/SessionHeader.test.tsx
# Format touched native paths with this tree's pinned Biome.
cd ..
make test-native
make test-web
make test-api-package
make test-web-browser
```

Expected: every command exits 0 with no failures; all native list/header surfaces are quiet for settled delegates, real session attention stays intact, and canonical impacted gates pass.

Run root `make` targets from the repository root, not the frontend/native directories. Shared package qualification has its own safe preflight. It verifies the packed package, not just in-repo aliases. Keep CPU/memory room for the harness; run heavy gates as background jobs only while doing other useful review. Inspect all finished outputs and actual selected tests.

Update the native guide and activity/subsystem contracts to distinguish quiet settled delegate outcomes from a session's own actionable errors. Do not remove independent open friction cases or claim the deferred fixes are done.

- [ ] **Step 5: Inspect and commit native chip changes and their guides.**

```bash
git diff --check
git add mobile-native/src/board/BoardRow.tsx mobile-native/src/board/attention.ts mobile-native/src/session/sessionState.ts mobile-native/src/session/SessionHeader.tsx mobile-native/src/board/BoardRow.test.tsx mobile-native/src/board/attention.test.ts mobile-native/src/board/BoardScreen.test.tsx mobile-native/src/ProjectsScreen.test.tsx mobile-native/src/PinSectionsScreen.test.tsx mobile-native/src/session/sessionState.test.ts mobile-native/src/session/SessionHeader.test.tsx mobile-native/README.md docs/product/session-activity.md docs/product/subsystems.md
git diff --cached
git commit -m "fix(native): quiet settled delegate chips without losing attention"
```

## Proof and paging coverage index

| Obligation | Owning tasks / decisive evidence |
| --- | --- |
| J01 | 1: immediate real background command, fresh folded journal after handoff/end |
| J02 | 1: fake-clock wait with real subprocess, durable read before termination |
| J03 | 1/2: retained foreground and pre-promotion timeout, exact direct output bytes |
| J04 | 1: real terminal outcome matrix, eligibility and status/reason/exit code |
| J05 | 1/3: fresh manager/store and forwarded owner journal, routed scoped reads |
| J06 | 2: missing historical marker excluded, original files unchanged, full diagnostics; 1: detached unchanged |
| J07 | 2/3/4: owner/scope/count sets, actual remote loaders, shared adapter, real producer-to-hub/browser |
| J08 | 4: outcome-inclusive fold, neutral glyphs, actual workspace action and real rendered output pane |
| J09 | 5: separate fold states, All/Running/Done, terminal totals, search and neutral presentation |
| J10 | 3/5: recorded producer generations through shared adapter and native renderer; descendant/stop access |
| J11 | 6: Board/project/pin/header renderers and preserved real own-session attention decisions |
| J12 | 3/4/5: actual third-page row, refreshed content, reconnect/reload, stable keys and disclosure; Chrome geometry, native intent-level evidence |
| J13 | 4/5: closed histories discover late active rows; terminal transition retains one row/output target |
| J14 | 1: actual start/forward failure and terminal retry barriers, no false background success |

| Spec paging case | Required owning checks |
| --- | --- |
| Multiple pages, history open, refreshed terminal description | 3 shared owner; 4 web binding/viewport; 5 native binding/renderer |
| Multiple pages, disconnect/reconnect | 3 loaded boundary replay; 4 actual socket interruption and visible row geometry; 5 held evidence and later row intent |
| Terminal first page, active work later, both histories closed | 4 page-boundary/browser; 5 FlatList page demand with independent closed folds |
| Running row becomes terminal, then refresh/reconnect | 3 shared identity; 4/5 exactly one history row and original output target |
| Eligible/excluded interleaved, paging/refresh/reconnect | 2 bounded source scans/cursors/counts; 3 shared replay; 4 real producer rows |
| Subtree owner unavailable, then recovered | 2 healthy sibling and unknown counts; 3 scope/recovery adapter; 5 missing notice becomes useful rows |

## Branch review and handoff

- [ ] Reconcile every spec section and J01–J14 against observed evidence. Mark native physical geometry, Safari, real-device safe-area/software-keyboard and screen-reader behavior untested unless separately qualified. Renderer/platform substitutes establish only the claims above.
- [ ] Run the required simplify-code review and one fresh whole-branch correctness review under the execution method Jesse chooses. Keep reviews bounded to this Jobs spec. Read every result and run any missing decisive check before relying on it.
- [ ] Inspect the exact diff against `aecae6a43030f81c989f2cc63c28d3e5eadf6035`, including owning guides and the evidence file. Confirm no old Overview, advisory, cascade or auto-follow work leaked in. Inspect `git status` and hooks; preserve unrelated files.
- [ ] Run fast impacted Go tests and the canonical web/native/package/browser gates named above. Let CI own the full repository suite; do not block a push on an unsolicited long local full run. Report script warnings and skipped platform coverage honestly.
- [ ] Commit the execution evidence with exact staging and enabled hooks. Leave requested deliverables and scratch evidence in place. Do not remove historical records or data for cleanup.
- [ ] Before opening/pushing a new PR or starting a review-monitoring batch, settle Jesse's new PR scope and observation budget. The merged Overview PR's authorization and budget apply only to that finished PR. Use the installed shepherd-pr procedure for any authorized new PR, with one push, one CI run, one review and one bounded wait per round.
- [ ] Final report: background-only rows/counts and quiet histories, actual producer/routing/output/recovery proof, each deliverable path, native/platform limitations, and the other still-unfixed follow-ups. This plan's approval is not evidence of an implemented fix.
