# Frontend Reopen History Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve a still-open web pane's previously loaded transcript through narrow-screen remounts and tab switches, without changing bounded initial promotion or automatic recovery.

**Architecture:** Diagnose the failed local-open decision before changing production behavior. Repair the confirmed boundary in the existing transcript repository or thread store, retaining their ownership and authority rules. Prove the same journey with production store tests and the real browser guard.

**Tech Stack:** TypeScript, Zustand, Vitest, fake-indexeddb at the storage boundary, React, native IndexedDB, Chrome/CDP, Go browser fixture and real hub/daemon AppWire.

**Spec:** `docs/superpowers/specs/2026-10-15-frontend-reopen-history-preservation-design.md`, approved by Jesse at commit `3ac86e0e285b7f85c5baec3ae8d0f1f3b3cec488`.

## Global Constraints

- First opening a background-only session keeps the existing latest-200-body promotion behavior. Older retained identities remain pageable locally.
- Preserve the existing resident and pending byte budgets, unfinished-output exceptions, and background body reclamation.
- Local body restoration does not manufacture coverage, freshness or a server cursor.
- A genuine reconnect or source change retains its existing reconciliation semantics.
- Clear, deletion, final release and superseded openings continue to fence late results.
- A genuine close followed by a new pane still lands at latest under the existing scroll contract.
- Responsive remounts preserve the semantic reading point under the existing width-reflow and height-only rules, not unconditional identical pixels. Mounted and remounted readers use the same policy.
- Placement readiness uses the existing ref-scoped local-opening state. Missing rows or source candidates do not distinguish pending restoration from settled empty content.
- Native, TUI, server protocol, relay routing and daemon behavior are outside this repair.
- Production deployment or restart requires separate authorization.
- Keep relay changes and its uncommitted two-tab guard work out of this PR.
- Follow `docs/developing-evener/testing.md`. Assert behavior and fixture payloads, never human-facing copy. Script only external boundaries.
- Use existing widgets and tokens. This plan changes no layout, widget API, cache format, dependency or compatibility behavior.

## Review Focus

Each risk below has a control in Task 2 or Task 3:

1. More than 200 loaded bodies across several items per turn: reopening retains complete payloads, not just identities.
2. A newer accepted version arrives during local restoration: the new version wins and older loaded bodies remain.
3. Local rows or metadata are absent: automatic recovery yields a usable pane with working paging and live updates.
4. Clear, deletion or source replacement races the opening: late rows cannot revive retired content or cursors.
5. Responsive remount and genuine close/reopen have different placement lifetimes: preserve the former's anchor and retain the latter's latest placement.

## Files and interfaces

| File | Responsibility |
| --- | --- |
| `cmd/evener-hub/frontend/src/stores/threads.entryProduction.test.ts` | Production-route regression, request counts and recovery controls |
| `cmd/evener-hub/frontend/src/stores/threads.ts` | Foreground acquisition, local-open fallback and recovery decision |
| `cmd/evener-hub/frontend/src/stores/transcriptRepository.ts` | Retained history, durable restoration, versions and authority |
| `cmd/evener-hub/frontend/src/stores/transcriptRepository.lifecycle.test.ts` | Restore/update/source-boundary races |
| `cmd/evener-hub/frontend/src/stores/transcriptRepository.paging.test.ts` | Local continuation and missing-row recovery |
| `cmd/evener-hub/frontend/src/stores/transcriptRepository.resources.test.ts` | Body reclamation and existing budgets |
| `cmd/evener-hub/frontend/src/stores/transcriptRepository.retirement.test.ts` | Clear and retired-opening controls |
| `cmd/evener-hub/frontend/src/dev/backgroundTranscriptGuard.tsx` | Existing read-only browser evidence surfaces, if diagnostic evidence is needed |
| `cmd/evener-hub/frontend/scripts/backgroundtranscriptguard/run.mjs` | Real browser gestures and recorded payload evidence |
| `cmd/evener-hub/background_transcripts_browser_test.go` | Independent browser-payload, transport and producer assertions, only if strengthening evidence requires a change |
| `docs/web-ui/README.md`, `docs/product/subsystems.md` | Evergreen preservation, recovery and ownership contracts |

Preserve the existing public interfaces:

- `TranscriptRepository.attachReader(): TranscriptReader`
- `TranscriptReader.openLocal(limit: number): Promise<TranscriptReaderResult>`
- `TranscriptReader.dispose(): void`
- `openEntryTranscript(ref: string): Promise<boolean>` (private store function)
- `ensureThread(ref)` / `releaseThread(ref)` and `loadOlderTurns(ref)` through `threadsStore.getState()`.

No new public interface is prescribed. The measured failure decides which
private restoration or acquisition logic changes. A needed ownership, budget,
shared-client or reconciliation-policy change requires an approved amendment.

---

### Task 1: Reproduce and repair the rejected local reopen

**Files:** Production-route test and, only after reproduction, the owning store/repository files listed above. Temporary structural diagnostics may use the existing dev guard surface and driver.

**Consumes:** Existing `cache()`, `turn()`, `response()`, `connect()` fixtures in `threads.entryProduction.test.ts`; real `SessionEntryCache`, production route and repository; external `FakeClient` transport.

**Produces:** A failing-then-passing reopen regression and the smallest confirmed source fix with unchanged public interfaces.

- [ ] **Prepare the isolated lane and establish the baseline.**

Confirm branch `fix/frontend-reopen-history-preservation`, base
`4299b82d4c8e7cab1314445f8b17bbab0bcfc72f`, and preserved relay lane. Read the
product/testing/design-system guides, `scripts/web/web-preflight.sh`, and gate
scripts. Run `make help`, check disk space, then `make web-preflight` from the
repo root. Never install through a symlink or delete shared caches. Run the
existing `threads.entryProduction.test.ts` before editing tests.

- [ ] **Write `reopening a paged background transcript preserves its loaded payloads`.**

Extend the existing 250-item promotion fixture without weakening its initial
200-body assertions. After two local older pages restore all 250 bodies,
capture the displayed items and request counts, settle persistence, keep the
background lease, release the foreground claim, and reacquire it. Use the
fixture's actual returned items, not a projection that drops tool payloads:

```ts
const before = structuredClone(items());
expect(before).toHaveLength(250);
await settleEntryTranscriptForTests("local:a");
threadsStore.getState().releaseThread("local:a");
await threadsStore.getState().ensureThread("local:a");
await settleEntryTranscriptForTests("local:a");
expect(items()).toEqual(before);
expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
expect(client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
expect(threadsStore.getState().localOpenings.has("local:a")).toBe(false);
```

- [ ] **Run the regression against unchanged production and identify its failure.**

From `cmd/evener-hub/frontend`:

```sh
./node_modules/.bin/vitest run src/stores/threads.entryProduction.test.ts -t 'reopening a paged background transcript'
```

Expected red: missing previously loaded payloads or an unnecessary source
read, not a launch, fixture or type error. If it passes, retain it as a control
and reproduce reconnect-before-paging with multi-item turns from the real
guard. A 250-to-200 contraction alone does not explain the measured 240-to-94
fresh-read path.

- [ ] **Identify why the measured local open returns false.**

Use the original real-browser command in Task 3 when the small fixture does
not reach that branch. Capture one correlated structural decision before
`beginFreshBaseline`: deadline/early-return/catch/body-rejection outcome,
currentness predicates, source revision/read/continuity/boundary values,
pending-write bit, and first failing requested key with versions,
`identityOnly`, cache-row and turn-metadata presence. Do not log payload text
or credentials. Compare the evidence with the spec's ordered hypotheses.

The check is an identified failing predicate and its originating data, plus
an automated regression exercising that failure. Do not choose a production
fix from the downstream recovery flag alone.

- [ ] **Repair the confirmed owning boundary.**

Keep the interfaces above. Restore the previously loaded range through its
existing owner; preserve first promotion's 200-body limit and authority fences.
Do not replace recovery with an unconditional success, raise fixed limits,
retain every background body, or merge obsolete source histories. Remove
temporary diagnostics once their result is recorded.

- [ ] **Verify green and commit the repair.**

Rerun the regression, the complete production-route test file and repository
restoration tests. Run `npm run typecheck` and touched-file Biome from the
frontend directory. Require exit zero and no failures. Stage only the exact
test and production files changed, inspect the staged diff and commit.

### Task 2: Prove preservation controls and recovery to useful operation

**Files:** Focused store/repository tests listed above, any further minimal fix those tests demonstrate, and the two owning evergreen guides.

**Consumes:** Task 1's repaired local-open path and unchanged repository/store interfaces.

**Produces:** Verified edge behavior and documented ownership without weaker background budgets or source fencing.

- [ ] **Add missing control assertions at the owning boundary.**

Reuse existing cases before adding tests. For each uncovered case, write its
behavior assertion, run it red if behavior is missing, then change only the
confirmed cause:

| Case/test name | Required oracle |
| --- | --- |
| `reopening a multi-item paged transcript retains complete payloads` | Above 200 bodies across multiple items per turn, keyed full items equal the captured range and no unnecessary read occurs |
| `an accepted update wins over an in-flight local reopen` | A fixture item advanced from version 1 to 2 retains version 2 and its exact new payload, unchanged earlier bodies remain |
| Existing retained body/empty/omitted-payload cases | Explicit empty strings and real tool output restore without a spurious source read |
| Existing second-foreground-claim case | The first reader's content remains and acquisition causes no second local open |
| `reopen recovery resumes paging and live updates` | Evicted/unwritten item or metadata requires a real source response, displays its payload, accepts a subsequent older page and then a versioned notification |
| Existing opening-deadline, Clear/delete and source-change cases | Opening hold clears, delayed result cannot publish, new baseline's cursor wins |
| Existing repository resource cases | Ended background bodies reclaim under unchanged limits and unfinished-output exceptions remain |

Use producer fixture payloads and exact item sets as the oracle. New race tests
use deferred barriers or controlled clocks, not sleeps. Capture and assert
expected error outcomes. Missing coverage is not permission to claim currentness.

- [ ] **Run the affected suites and update evergreen contracts.**

From the frontend directory, run:

```sh
./node_modules/.bin/vitest run src/stores/threads.entryProduction.test.ts src/stores/transcriptRepository.test.ts src/stores/transcriptRepository.lifecycle.test.ts src/stores/transcriptRepository.paging.test.ts src/stores/transcriptRepository.resources.test.ts src/stores/transcriptRepository.retirement.test.ts src/panes/session/transcript/transcriptReadView.test.ts src/panes/session/transcript/flow/useTranscriptScroll.test.ts src/panes/session/transcript/flow/useTranscriptScroll.edge.test.ts src/panes/session/transcript/flow/useTranscriptScrollKeys.test.tsx
npm run typecheck
```

Expected: all suites finish with zero failed tests, typecheck exits zero.
Update `docs/web-ui/README.md` and S02 in `docs/product/subsystems.md` with the
verified loaded-range boundary and recovery ownership. Commit exact touched
files after Biome, diff checks and staged-diff inspection.

### Task 3: Verify the whole browser journey and deliver the separate PR

**Files:** Existing background guard and its README/tests only if stronger
producer/DOM evidence is required. No relay production changes.

**Consumes:** Tasks 1 and 2, the unweakened real browser fixture.

**Produces:** Passing preservation/recovery evidence, a reviewed and verified frontend merge, then a return to the relay lane.

- [ ] **Run the original browser guard.**

From the repository root:

```sh
go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1
```

Require the entire command to exit zero. Independently compare recorded keyed
payloads before and after 420px reactivation and the daemon producer evidence.
Confirm no unnecessary healthy-reopen read from raw AppWire trace. Observe
older content in the rendered pane and inspect a screenshot with vision.
Exercise a same-pane responsive remount at an older reading anchor, then a
genuine close/new-pane open: the former retains its semantic reading point
under the existing reflow policy and the latter lands at latest. Independently
measure semantic-entry geometry rather than equating a turn-wrapper offset
with the production anchor. Keep wrong-entry, wrong-reading-point and changed
pane-identity negative controls; use no loose offset tolerance. Keep real
hub/daemons/IndexedDB and script only the external LLM boundary.

For placement readiness, use the existing store opening state through the
current registration owner. Cover temporary bodyless and partial projections
versus settled empty/anchorless content, then completion on success, recovery,
cancellation and supersession. Run `TranscriptBody.test.tsx` and
`transcriptAnchors.test.tsx` alongside the hook/read-view/registry tests before
the real browser check. Preserve their existing reflow, empty-content and
reader-intent assertions. The cascade browser return/remount journey is also
an affected placement check and must be attributed and verified before delivery.

- [ ] **Run final affected gates sequentially.**

Apply pinned Biome to each touched frontend/shared-client source file from the
frontend directory. From root, run `make test-web`, then
`make test-web-browser`. Read the scripts and full results. Do not treat a
skipped launch or a passing subset as a passing gate. Keep required failure
artifacts under their retention policy, remove disposable diagnostics, and
never weaken the narrow preservation assertion.

- [ ] **Review, simplify, shepherd and verify the merge.**

Freeze base/head and submit the spec, amendments, complete diff and red/green
evidence to two competing PAR reviewers. Resolve real findings with tests.
Run `simplify-code:simplify-code` and rerun affected checks. Use
`shepherd-pr:shepherd-pr`, one complete edit/push/review/CI round at a time.
Merge only when current-head checks and review bodies meet repository policy.
Verify GitHub's merged state, merge SHA and intended files before cleaning this
lane. Do not restart or deploy production.

- [ ] **Return to the relay repair.**

Preserve the relay lane and its uncommitted files throughout. After verified
frontend delivery, return there, integrate the frontend fix under repository
merge rules, and rerun its strict browser journey before resuming the original
relay plan. Report each PR and evidence separately.

## Execution record and handoff

Track execution and evidence in one ledger:
`.superpowers/sdd/2026-10-16-frontend-reopen-history-preservation/progress.md`.
Record the decisive false-return cause, red/green commands, review findings,
retained artifact paths, commit/PR identities and any approved amendment there.
The session task list tracks only phase status.

This plan leaves the internal fix body to the measured reproduction; it does
not authorize a speculative fix. If the measured cause requires a scope change,
stop for approval. Recommend inline execution because the reproduction, fix and
recovery checks share one causal investigation, followed by independent review.
