# Background Jobs execution evidence

## Scope and authority

This record covers the approved [Jobs design](../specs/2026-10-03-background-jobs-design.md)
and [implementation plan](2026-10-03-background-jobs.md). Jesse approved the
spec at `00679fa5fd` and the plan at `1e97dd0d05`, selecting inline execution.
The branch starts at `aecae6a43030f81c989f2cc63c28d3e5eadf6035`.
Jesse separately approved the shared-store retained-snapshot fix during native
client-replacement verification.

The durable shell start event now records background handoff. The domain reader
uses one predicate for Jobs pages and counts. Complete diagnostics and direct
output remain available. Web and phone histories retain true terminal outcomes
with neutral presentation. Native session lists show running delegate chips;
header totals stay authoritative and a session's own attention stays intact.

All six implementation slices are committed and their completion checks pass.
All four simplify reports were read and six quality-only cleanups were applied.
Their individual checks and full post-cleanup gates passed. The separate fresh
whole-branch correctness review found one Important native discovery defect.
The single author fix has observed red-green proof and passing full post-fix gates.
This file does not authorize a push, a pull request, monitoring or deployment.

## Behavior proof

Passing preservation cases are distinguished from observed red-green fixes.
Missing dependencies, test setup failures and browser-driver syntax failures
are recorded in the ledger; they are not counted as product regressions.

| Obligation | Decisive evidence and observed result |
| --- | --- |
| J01 | `agent/job_shell_background_durability_test.go`, `TestRunShellImmediateBackgroundRetainsEligibility`: real subprocess loses the marker on a fresh journal read before the change, retains it after settlement afterward. |
| J02 | `agent/job_shell_promotion_background_test.go`: actual foreground promotion proves the durable start before handoff and after termination, observed red-green. |
| J03 | Real retained foreground, pre-promotion runtime-limit and cancellation producers remain ineligible. `TestSessionActivityRealForegroundProducersStayDiagnosticOnly` failed before filtering, then passed; direct output remains exact. |
| J04 | `TestRunShellBackgroundOutcomesRetainEligibility` checks success, nonzero exit, runtime limit and parent stop with actual subprocesses. Parent stop invokes the real job manager and records `cancelled` / `stopped_by_parent`; status, reason and exit code assertions remain strict. The browser also exercises a real stop. |
| J05 | `TestRunShellBackgroundForwardedAndReopenedEvidence` exercises a second real forwarded store, reopened store and fresh manager. Marker preservation failed before the producer/fold fix, then passed. |
| J06 | `TestSessionActivityLegacyForegroundDataRemainReadable` pins exact saved output/transcript bytes and original journal/output equality while excluding unmarked history. Real detached execution leaves no managed journal record. |
| J07 | `TestSessionActivityBackgroundEligibilityMatchesPagesAndCounts` checks explicit eligible membership, bounded excluded gaps and counts. Actual local/remote `TestSessionActivityBackgroundPublicRoutes`, mixed-owner, partial-owner and warm-count tests retain scope, identities and unknown counts. |
| J08 | Web `JobsWatchesTabs.test.tsx` and `activityRows.test.tsx` observed red-green for all terminal grouping and neutral glyphs. `activityApi.test.tsx` uses the real workspace action. `TestBackgroundJobsBrowser` opens and renders actual saved output in a secondary pane beside its originating session. |
| J09 | Native activity tests observed 27 failures before grouping/presentation changes. Independent Done delegates and Completed jobs, All/Running/Done, search, truthful outcome words and authoritative unknown/count labels pass afterward. |
| J10 | `mobile-native/src/subagents/activityHistory.test.tsx` uses recorded producers from `agent/testdata/subagentwire` through the shared adapter and phone renderer. Reported/failed/stopped generations, resumed running generations and delayed old replies retain coherent evidence. Existing descendant navigation and stop decisions remain covered. |
| J11 | Board/project/location/pin/header regressions use the actual `subagents` navigation field and authoritative total 5, active 2, failed 3. Thirteen quiet-chip behavior assertions failed before the native-only fix. Own errors remain in Needs you ahead of questions/approvals; offline and stale-input decisions stay intact. Eight focused files, 428 tests and native typecheck pass after fixture repairs. |
| J12 | Shared third-page recovery tests, real Chrome four-page history and native three-page renderer/binding tests retain current content and qualified identity. Native client replacement initially lost later rows; independent binding and shared retained-input regressions failed, then passed after the approved existing-owner fix. Chrome semantic offsets differ by at most 1 px against a 2 px bound through refresh/reconnect/reload. Phone proof covers view intent, not device geometry. |
| J13 | Real Chrome discovers the oldest active producer on page four with Completed closed, then settles it once with its original output target. Phone `closed histories discover later live work, then retain one terminal output target after recovery` now executes the installed native content-length latch rather than injecting two end callbacks. It and the failed-parent/active-descendant page-three case failed on the original view, then pass with one native callback and continued shared-owner demand. Separate closed folds, later terminal recovery and original output access remain asserted. |
| J14 | Real start/forward failure and terminal-append retry tests preserve lifecycle results and prevent false background acknowledgement. These retain the existing failure/retry owner. |

## Paging and recovery matrix

| Case | Evidence |
| --- | --- |
| Open multiple-page history, refreshed terminal description | Shared third-page test, Chrome job016 on page three, native later qualified row. |
| Disconnect/reconnect after multiple pages | Shared generation fence and fresh cursors, actual Chrome socket replacement, native replacement client with retained membership. |
| Terminal first page, active work later, histories closed | Chrome fourth-page producer and native FlatList `onEndReached` with independent closed folds. |
| Running becomes terminal, refresh/reconnect | Shared stable identity, Chrome actual terminal lifecycle and saved output pane, native single terminal output target. |
| Eligible/excluded interleaved | Domain bounded scans including more than 2,000 excluded events, real local/remote pages and Chrome 151 background versus four retained foreground producers. |
| Required subtree owner unavailable, then recovered | Domain/shared/phone healthy rows survive, counts remain unknown until authoritative recovery. |

## Verified task checkpoints

| Slice | Commit | Completion command and observed result |
| --- | --- | --- |
| Durable handoff | `e1f1f65917` | Full jobstore suite plus selected RunShell/terminal durability/forward/detached tests, exit 0. |
| Eligible pages/counts | `3c3b687f54` | `go test ./agent -run '^TestSessionActivity\|^TestLoadSessionJobActivityTree\|^TestRunShellForeground' -count=1`, exit 0. |
| Routing and shared recovery | `f0bfd8b715` | Hub and appsource activity suites, three shared Vitest files with 115 tests and 214 Node tooling tests, exit 0. Canonical web gate also passed. |
| Web quiet history | `d4f29f0336` | Canonical web gate, dev browser launcher tests, web build and actual `TestBackgroundJobsBrowser`, exit 0. |
| Native separate histories | `b001cfc017` | Eleven native focused files, 186 tests and native typecheck, exit 0. Shared retained-input suite: 122 Vitest and 214 Node tooling tests, exit 0. Canonical native/web gates passed. |
| Native quiet chips | `c74143beed` | Fresh task-done: eight focused files, 428 tests and native typecheck, exit 0. All four canonical native/web/package/browser gates also exit 0. |

All root `make` gates run from the repository root. Scripts were inspected before
relying on their output. Task-done logs contain the real commands, not printed
stand-ins. `make test-native && make test-web && make test-api-package && make
test-web-browser` exited 0 as `job_034YjykTGJS8qGFnkg7GkI_XCa2Xhr1KoPA`.
All 31,405 output bytes were read. The run produced an iOS Metro bundle from
2,463 modules, ran 5,536 tests across 365 native files and 785 tests across six
shared-mobile files, checked native types/lint/script imports, passed all three
web gate lanes, qualified the installed packed package and passed all eleven
real browser guards including Background Jobs.

Fresh committed-tree Go qualification exited 0 as
`job_034YjykTGJS8qGFnkg7GkI_lOlDdtRUWJo2`; its complete 275-byte output reports
five actual package runs:

```sh
GOMAXPROCS=8 go test ./agent/internal/jobstore -count=1
GOMAXPROCS=8 go test ./agent -run '^TestSessionActivity|^TestLoadSessionJobActivityTree|^TestRunShell|^Test.*(Terminal.*Durab|Shell.*Forward|Detached)' -count=1
GOMAXPROCS=8 go test ./cmd/evener-hub -run '^TestSessionActivity' -count=1
GOMAXPROCS=8 go test ./cmd/evener-hub/internal/appsource -run '^TestSessionActivity' -count=1
GOMAXPROCS=8 go test ./cmd/evener-dev -run 'Test.*Browser' -count=1
```

## Simplification review

All four read-only seats reviewed the exact full 66-file branch range through
`c74143beed`, both authorities and the evidence/rulings. Each used
`codex-jesse-at-pr/gpt-6.1-sol-1m` with high reasoning effort. Initial private
scratch access failed, so the same seats resumed with an inline brief. All four
reports were received and read before applying any recommendation.

| Angle and seat | Findings and disposition |
| --- | --- |
| Reuse, `dlg_034ZpIjDXvO32tlXY0ogCG` | Replaced duplicate promise gates with the existing test-only `deferred` helper. |
| Simplification, `dlg_034ZpJFxxnKRVKlmQDOvpt` | Decoded browser milestones once, combined duplicate fold markup with independent state setters, and identified the consumed snapshot also found by Efficiency. |
| Efficiency, `dlg_034ZpJl3d0Qtdr7BqxLyvR` | Released the consumed native snapshot, classified matching rows once and sorted visible groups only, consolidated four browser capture evaluations into one. Skipped scoped/coalesced notification wakeups because they would change helper timing/error/return behavior. |
| Altitude, `dlg_034ZpKEXNhyKQOsnRMoUJn` | Deferred unifying launcher command/build/interrupt traits because it reaches the unchanged shared gate and its other callers. No current policy omission established. |

Nine findings reduced to eight unique mechanisms. Six cleanups were applied
across six files. No function or constant was removed, renamed or given a new
signature. No test or assertion was removed or weakened. No cleanup was reverted.
The seats did not run behavior gates or independently revalidate historical
execution/platform evidence. Altitude ran whitespace and driver syntax checks.

Individual post-cleanup checks exited 0: recorded phone history, five tests;
retained binding/tree/history, 21 tests; list/model/history, 63 tests; independent
fold screen/history, 33 tests plus native typecheck; actual Chrome after milestone
decoding, 17.116 s; actual Chrome after capture consolidation, 15.916 s.
The last real run still has four pages and 151 eligible jobs, quiet truthful
nonzero/stopped history, exact originating output and no page/console errors.
Its later-row offsets are -25.3125 px before refresh and after refresh/reconnect,
then -24.3125 px after reload, within the unchanged 2 px bound.

The capture artifacts were copied before analysis to supplied scratch
`background-jobs-browser-evidence/simplify-capture`. An independent audit checks
all seven state records retain the exact seven-field schema, empty errors and
saved HTML. This audit supplements the real journey rather than replacing it.

Full post-cleanup qualification exited 0 as
`job_034YjykTGJS8qGFnkg7GkI_cQIPWsg8bM8R`: `make test-native && make test-web &&
make test-web-browser`. All 31,160 output bytes were read. The run bundled 2,463
iOS Metro modules, passed 5,536 native tests in 365 files and 785 shared-mobile
tests in six files, native types/lint/script imports, all three web gate lanes,
the production web build and all eleven real browser guards. Test counts did not
decrease. Existing SQLite/cache/npm/generated-size/large-chunk warnings remain
unsuppressed. The separate whole-branch correctness result follows.

## Final correctness review and author fix

The sole actual fresh-context reviewer, `dlg_034ZqDdM222b7UQRJ6Xp6G`, reviewed
all nine commits and 67 paths from the branch base through
`109fd8ec6ee20738baeaa110980e426e2c2237a6`, with the explicit most-capable
`codex-jesse-at-pr/gpt-6.1-sol-1m` model and high reasoning effort. The initial
explorer dispatch, `dlg_034Zq9QwUu2E3cnIrRZr5p`, returned an inventory and
explicitly performed no correctness analysis. It is not a review or verdict.

The actual review found no Critical issues, one Important issue and no new
Minors. Its technical verdict was With fixes, with no integration authorization.
The author read the complete report, graded the Important finding by its effect
and recorded every declined behavior in the chronological rulings below.

Closed phone history can consume a second page without changing its height.
The installed native `VirtualizedList` emits its end callback once per content
length, so page-three live jobs or a failed parent's active descendants could
remain unloaded despite authoritative active counts. The author independently
executed both real-binding/platform-latch reproductions: one callback, two pages,
100 folded terminal rows, `hasMore: true` and no reachable live row. Explicit
page-three admission proved that shared cursors and projection preserved identity.

The single fix retains observed visible-edge demand when only folded membership
changes. The native view gates each resource with shared loading/error state;
new visible keys, changed geometry, scrolling away, focus and disposal bound that
demand. The existing store still owns admission, cursors, backoff and recovery.
No new reader, subscription, retry timer or shared-store implementation was added.

Both final synchronized acceptance tests failed on the original view with only
root/page-two requests, then passed after restoring the fix. All twelve real
phone history tests and native typecheck exited 0. Preserved cases include recorded
reports, later-row client replacement, terminal/output identity and missing-owner
recovery. New cases cover changed rows/geometry, scrolling away, focus pause and
return, shared 1-second backoff followed by useful live recovery, and disposal with
a delayed reply. No assertion was weakened. A React effect synchronization error
and an `act` callback return-type error were fixture repairs, not extra product fixes.

The first full post-fix gate, `job_034YjykTGJS8qGFnkg7GkI_0ZKns0uIZplG`,
exited 2 after native tests: 5,542 passed and one existing screen paging test timed
out. All 7,699 output bytes were read; later gates did not run. The exact case
also timed out alone. Its asynchronous `act` awaited page admission before the
event's state update could commit and trigger the effect. Committing the native
event synchronously before awaiting admission fixed the fixture. All forty
screen/history cases and native typecheck passed, with every assertion and
deadline unchanged. No production change was needed for this timeout.

Full post-fix `make test-native && make test-web && make test-api-package && make
test-web-browser` exited 0 as `job_034YjykTGJS8qGFnkg7GkI_DwBPl6BqAM9N`.
All 31,338 output bytes were read. The run bundled 2,463 iOS Metro modules,
passed all 5,543 native tests in 365 files and all 785 shared-mobile tests in six
files, native typecheck/lint/script imports, all three frontend gate lanes,
installed packed-package qualification, the production web build and all eleven
real browser guards. Existing warnings remain disclosed and unsuppressed.
No second correctness review is scheduled. The report and reproduction were copied
before reading into supplied scratch `sdd/2026-10-03-background-jobs/final-review/`.
Historical author-observed gate logs and Chrome offsets were not independently
replayed by this source reviewer. Full-repository CI and physical platform limits
remain as stated below. The separate recursive Activity Sheet retains its existing
failure-first presentation; this sidebar/native lane did not quiet that renderer.

### Deferred minors

- Browser launcher execution policy remains split across named command/build/
  interrupt registrations. Consolidating traits is broader shared-gate work.
- The browser test helper can reread all Jobs pages on unrelated notifications.
  Narrowing/coalescing wakeups changes behavior outside the quality-only pass.

## Retained audit evidence

The execution ledger and complete task logs remain in the supplied scratch:

`/tmp/evener-sandbox-2966472067/tmp/evener-sandbox-934126790/sdd/2026-10-03-background-jobs/`

The ledger records observed failures, disproved hypotheses, test-fixture repairs,
source fixes and every ruling in chronological order. The real Chrome qualified
run was copied before analysis into the supplied scratch under
`background-jobs-browser-evidence/qualified-run`. Earlier failed browser probes
are retained separately. The original negative-offset browser predicate had a
fixture syntax error; repairing it did not change product scroll recovery or its
2 px assertion. Refresh was driven by a real shell terminal lifecycle, because
ordinary output appends do not invalidate Jobs metadata.

## Limits and independent follow-ups

Chrome proves the tested browser geometry and real-stack journey. Native
renderers, bindings, typecheck and iOS Metro bundle prove source/resolver and view
intent coverage. Safari, physical phone safe areas/software keyboard, VoiceOver
and installed-app geometry are unqualified. The full repository lint/vet/test
and race/fuzz CI lanes were not run locally for this follow-up.

Existing warnings include Node experimental SQLite, cold Metro cache rebuild,
Biome's generated `mermaidPage.ts` exceeding its size limit, npm notices and the
web build's large-chunk warning. No check or warning was suppressed. Dependency
advisories, Overview review leftovers, cascade reload diagnosis, job-output
auto-follow and bundle-size architecture remain separate, unfixed follow-ups.
No historical job files, output or transcripts were removed.

## Chronological rulings

- Ruling: Place this plan's workspace in $EVENER_SCRATCH_DIR/sdd/2026-10-03-background-jobs and retain it at finish, rather than creating/deleting repository-local skill scratch — higher-priority scratch instructions and approved plan require this placement/retention — cost if wrong: execution evidence needs an explicit path rather than the skill's default path.
- Task 1: Ruling: Keep filepath/jobstore imports with the shared journal helper in the new durability file, not in the promotion file — that file calls the helper and would otherwise have unused imports — cost if wrong: move helper/imports together; behavior is unchanged.
- Task 2: Ruling: Create jobsDir explicitly before opening the new literal journal fixture — savePastActivityMeta writes metadata but does not create the sessions/<id> journal directory, so the plan's setup first failed with ENOENT — cost if wrong: test setup only, no production behavior.
- Task 2: Ruling: Do not assert a partial numeric Total while Jobs.Known is false — the existing summary loop clears the whole count struct on an unavailable required owner, and unknown counts are not partial totals — cost if wrong: adjust the assertion to a changed count contract; healthy row preservation and recovered exact counts remain required.
- Task 2: Ruling: Require unknown summary counts for the cold unavailable child, keeping its collection issue assertions, without demanding the summary repeat that issue — the guide defines summary issues as current bounded-pass findings, not a complete census, and the brief requires preserving cold no-scan behavior — cost if wrong: revisit summary issue ownership separately; eligible recovered counts remain exact.
- Task 3: Ruling: Accept zero-to-limit rows per routed page, requiring advancing opaque continuations and the exact complete eligible owner/job set — filtered tail candidates may validly yield an empty final page, and existing bounded paging contract permits empty continuations — cost if wrong: tighten page completeness behavior at its server owner, preserving membership checks. No production change.
- Task 3: Ruling: Test stale third-page rejection at the reconnect generation fence without demanding overlapping same-resource requests — the shared store intentionally serializes requests, so old reply must settle before its queued fresh root runs — cost if wrong: add an overlap case only if the existing serialization contract changes. Store production code unchanged.
- Task 3: Ruling: Include remote_hub_refs.go and the owning session-activity guide in this contract slice — the actual remote loader exposed a production rejection of valid opaque output anchors, and Task3 explicitly permits fixing the traced existing owner — cost if wrong: isolate this small existing-contract fix into its own commit; no new reader, interface or compatibility policy added.
- Task 4: Ruling: The brief uses obsolete background:true, but current DefShell advertises mode only and parseShellMode consumes mode. Use canonical shell with mode:background, preserving the approved real handoff and adding no compatibility. Cost if wrong: fixture misses an undocumented public argument, current schema disallows it.
- Task 4: Ruling: DefShell does not advertise max_runtime_ms and its additionalProperties:false rejects that field; the real browser producer cannot request a runtime limit through this public surface. Keep real runtime-limit coverage from Tasks 1/2 and direct web terminal runtime wording/quiet tone, with actual nonzero and stop outcomes in Chrome. Do not add a tool field or change command timeouts to force a browser pass. Cost if wrong: browser-specific runtime-limit routing remains unqualified.
- Task 4: Ruling: Browser geometry measures the first visible terminal row on page three, with the real nonzero and cancelled rows visible in the same viewport; the oldest held live job is independently discovered on page four while Completed is closed, then settles into that fold. Measuring the live job outside the fold would test a top-of-list row rather than physical later-page history. Cost if wrong: late live and later-history recovery are composed rather than a single unchanged-layout row across its terminal relocation.
- Task 4: Refresh cause confirmed at agent/session.go61-62: only actual shell start/finish lifecycle events publish Jobs invalidations. agent/jobs.go1439-1453 appends output and feeds watches, not Jobs; ordinary output is not a page-refresh producer. Copied browser failure has only initial four page calls despite the refresh control record. Ruling: Hold the newest of the existing151 backgrounds too, release its actual successful terminal lifecycle for refresh after the Go client confirms the oldest producer's refresh output, and retain the oldest for reconnect/final output. This uses the existing domain producer rather than adding invalidation behavior or synthetic notifications. Initial active count becomes2 and closed terminal fold149; membership151 and late visible semantic anchor remain required. Cost if wrong: terminal relocation adds a real layout transition to refresh qualification; the same completed target row and unchanged viewport still must preserve offset within2px.
- Task 5: Ruling: Add retained same-ref/same-scope snapshot input to the existing SessionActivityStore, and pass native held membership before old-owner disposal — client replacement discards the rows from which the shared owner derives its displayed boundary; retaining them lets the existing paced replay and session fences recover without a native paging loop — cost if wrong: retained evidence could cross connection/identity boundaries, so test ref/scope isolation, runtime/read reset, resolved-session retirement, partial membership and late old replies before completion.
- Task 6: Demo RED observed before assertion migration: frame7 expected deleted failed suffix but received undefined; focused runtime exit1. Fixture-only repairs then native tsc and eight real Vitest files428tests exit0, full outputs recovered/read at transcript6171-6184. Existing failed delegate outcome checks remain. Ruling: Include src/dev/demoSessions.test.ts in this task and final focused gate because its real frame7 assertion consumed removed ContextChip.failed; replace that presentation assertion with exact neutral labels/accessibility, retaining true outcome checks. Cost if wrong: demo chip expectations need adjustment, no product scope expansion.
- Final: Ruling: Keep historical RED/GREEN logs, gate counts and Chrome offsets qualified by the author-observed retained artifacts, not the reviewer's inaccessible private logs — the reviewer inspected source/test construction but did not independently reproduce those historical runs, and completed full reads remain completed — cost if wrong: replay a disputed qualification without representing this review as independent execution evidence.
- Final: Ruling: Leave full-repository lint/vet/test/race/fuzz and CI readiness unqualified — bounded local impacted gates and source review are the actual evidence, with no authorized push or CI run — cost if wrong: an integration or untargeted regression can remain until the future CI gate; no CI-green claim.
- Final: Ruling: Leave Safari, installed-phone geometry/safe areas/keyboard and VoiceOver unqualified, while accepting the executed native source-level latch reproduction as an Important discovery defect — installed platform logic and real native decisions establish the missing demand without pretending to be a device journey — cost if wrong: device-specific layout/accessibility behavior still requires its own platform run.
- Final: Ruling: Keep Chrome-specific runtime-limit routing expressly unqualified under the approved public-tool schema ruling — actual domain runtime-limit and web terminal-wording cases pass, but forcing the real browser producer would add an unapproved tool field or timeout policy — cost if wrong: the browser-specific runtime-limit path needs a separately approved producer surface.
- Final: Ruling: Keep the unchanged recursive Activity Sheet failure-first/danger presentation outside this approved sidebar/native execution lane — the approved web slice changes JobsTab and shared sidebar rows, the recursive Activity Sheet retains its documented separate owner and was not changed — cost if wrong: opening that Sheet still emphasizes failed activity; broaden its presentation only with Jesse's scope decision rather than claim it quieted here.
- Final: Ruling: Leave dependency advisories, Overview leftovers, cascade reload diagnosis, job-output auto-follow and bundle architecture as the independent unfinished follow-ups already recorded — this Jobs lane neither repairs nor qualifies those owners — cost if wrong: those recorded bugs/advisories remain until separately approved work; no completion claim for them.
- Final: Ruling: Re-arm native visible-end demand after a successful same-height folded page through the existing shared owner, and replace twice-injected callback proof with the installed platform latch — the plan's original renderer test bypassed React Native's per-content-length latch and therefore missed closed-history live discovery; the native facade exposes existing per-resource read state without new cursors, subscriptions or retry timers — cost if wrong: stale visible demand could drain unseen pages or override backoff, so pin new visible rows, changed geometry, scrolling away, focus pause/resume, errors/backoff recovery and route disposal before completion.
