# iPhone redesign, Phase 7: activity pulse, seen marker and subagent tallies (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every task carries its code, so a Sonnet implementer transcribes it; the reviewer checks it against this plan and the code as it is on main.

**Goal:** The hub gives the phone and the web three more facts the Board needs: a live pulse meter with honest Quiet and May be stuck labels (S5), a seen-through marker the phone and the web share so Finished and Idle agree across devices (S4), and each live session's subagent tally over its whole tree (S3).

**Architecture:**
- **S5 (PRs 14-15).** The daemon counts its whole tree's transcript motion in a ring of ten-second slots, fed from the AppWire methods its projection commits publish, and stamps the sample on its `thread/list` root row. The hub's probe keeps the sample off the roster fingerprint, and a new polled method `evener/activity/read` serves minutes, running subagents and a quiet time the hub withholds while any subagent runs. It never touches navigation, so a working session never moves a navigation revision.
- **S4 (PRs 16-17).** The daemon stamps when each turn ends (`SessionMeta.LastTurnEndedAt`, carried on the thread envelope). The hub keeps a source-qualified `session_seen` table in `index.db`, a method `evener/session/seen/set`, and two summary fields, `turn_ended_at` and `unseen`, computed against the marker and a one-time epoch. The web marks a session seen when its pane opens.
- **S3 (PRs 18-19).** The daemon counts every delegate in its tree's folded journal (running, failed, done) at `thread/list` time through a seam, the way it already attaches live watches. The hub carries the tally from the probe to a `subagents` summary field on live rows.

**Tech Stack:** Go 1.27 workspace (root module and `agent/`), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v5"`), SQLite through `modernc.org/sqlite` in the hub's shared `index.db`, TypeScript 6 in `appwire-client/typescript` tested by vitest from `cmd/evener-hub/frontend`, the React web frontend.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: 7.1 (Live bands, Finished and its blue dot), 7.2 (row anatomy: why line, last line), 7.3 (Mark as read and unread), 9 (subagent states), 13.1 (Quiet, May be stuck, Finished, Idle), 13.2, 16.4 (the pulse meter), 17 and 18 (S3, S4, S5). The server plan `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md` holds the design-level sections this plan replaces, the PR map (these are PRs 14 to 19) and the lane's landing rules. Written against main at `328685d4f`, after the tolerant navigation codec (#2473), the approval flag (PR 3, #2508), task progress (PR 12, #2502) and failed turns settling to errored (PR 2, #2514) merged.

## Global Constraints

- **Wire.** Every change is additive and stays on `ProtocolVersion = "evener-appwire-v5"` (`appwire/types.go:25`); the navigation read stays `representationVersion: 2`. A new field is optional and `omitempty`, so its key is absent unless it carries a fact, and it lands in the same PR as its codec entry, its hub schema bound and its `make generate` output (Jesse's ruling).
- **Codec first.** The tolerant codec (#2473) is on main. A PR that adds a navigation summary field (PRs 17 and 19) also waits for a TestFlight build containing #2473 (the server plan's TestFlight checkpoint, which PR 3 already had to meet before #2508 merged).
- **Never add a `FeatureSet` key.** The TypeScript `initialize` decoder refuses unknown feature keys (`appwire-client/typescript/client.ts:146-193`), so a new one breaks every older app's connection. A client learns a new method exists from the method answering (an older hub answers method not found, `-32601`, `appwire/errors.go:6`) and a new row field from its presence.
- **Casing.** `hubapi` navigation JSON is snake_case (`turn_ended_at`); `appwire` JSON is camelCase (`lastTurnEndedAt`). The tagliatelle lint enforces both (`.golangci.yml`, and `.golangci-appwire.yml` for `server/`).
- **A new summary field** goes in the `hubapi` struct, `projectShallow`, the hub schema (`navigationSessionValueValid`, `cmd/evener-hub/navigation_schema.go:419`), `cloneNavigationSummary`, the codec's `SESSION_KEYS` and `sessionValue` (`appwire-client/typescript/state/navigation/codec.ts:163-325`), and the shared fixture `cmd/evener-hub/testdata/navigation/value-records.json`. A nested record gets one validity predicate the schema and the projector share, so the projector drops a value the schema would refuse instead of failing the whole resource (`navigationTaskProgress` and `navigationTaskProgressValid`, PR 12).
- **Clones and fingerprints.** A new pointer or slice on `ProbeResult`, `LiveEntry`, `TreeNode` or a summary is deep-copied in `CloneLiveEntry` (`hubcore/roster.go:165`, which `cloneNavigationLiveEntries` now calls), `liveEntryFromProbe` (`roster.go:1309`), `cloneTreeNodesContext` (`hubcore/tree.go:114`) and `cloneNavigationSummary` (`navigation_projection.go:2030`) as it applies. Value fields need no copy. A `LiveEntry` field that changes a row is hashed in `rosterFingerprint` (`roster.go:387`); a field that must never move navigation is left out on purpose, with a test.
- **hubcore tests** are `fuzzScenario*` functions registered in `FuzzHubcoreScenarios` (`hubcore/scenarios_fuzz_test.go`), in alphabetical order. Run them with `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`. An unregistered one never runs; `golangci-lint run ./cmd/evener-hub/internal/hubcore/` reports it unused.
- **Methods.** A new hub method gets a catalog row (`appwire/protocol.go`), a handler registered beside `registerFavoriteHandler` (`cmd/evener-hub/app_rpc.go:1097`), and a line in `TestHubRPCRegistersExpectedHandlerSet` (`cmd/evener-hub/app_rpc_test.go:12665`); `TestHubRouterMatchesCatalog` then passes on its own. No change here alters what `internal/appprojector` emits and no notification is added, so no `cmd/evener-tui` case is needed.
- **Generated files.** After any `appwire` or `hubapi` type change: `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1` and `make lint-generated`. Commit `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md`.
- **Go floors,** per module touched (root, and `agent/` for PRs 16 and 18): `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`. Format with `$(go env GOROOT)/bin/gofmt`. Run the pinned `golangci-lint run ./<package>/` on each touched Go package, and for `server/` also `golangci-lint run --config .golangci-appwire.yml ./server/...` (`make/linting.mk:124-125`).
- **TypeScript floors.** Biome only from `cmd/evener-hub/frontend`: `npx biome check --write <paths>` on touched files under `src/` and `../../../appwire-client/typescript`. Never run Biome in `mobile-native` or from the repo root, and never `npm ci` through a symlinked `node_modules`.
- **Targeted tests only.** Run each task's own tests and the gates it names. CI runs the full matrix; never run `make test-web`, `make test-native` or `make lint` locally.
- **Tests are deterministic.** Clocks are injected, no sleeps, and every new test is shown failing before its code lands. Fakes only at real boundaries.
- **Size.** Each PR stays under about 800 changed lines of production code; the estimates below are well under. Landing follows the roadmap: a regular PR, CI green at the head, the RoboRev comment read, /simplify run, then an admin squash merge.
- **Copy.** Wire names may use the codebase's words (delegate, escalation). User-facing copy ("Quiet 4m", "May be stuck · no updates for 12m", "2 subagents failed") belongs to the phone and web lanes and follows spec section 5.

## Rulings

Decisions the spec leaves open, with the reason for each.

**S5, activity pulse**

1. **Activity rides a polled hub method, never navigation rows.** Bars change every minute a session works. On the revisioned navigation resource they would move the fingerprint on every probe and broadcast an invalidation about once a minute per working session (`cmd/evener-hub/navigation_service.go:957-1064`), and `NextBoundary` schedules only the 24-hour and 14-day tier boundaries (`navigationSnapshotBoundary`, `navigation_service.go:1370-1399`). A notification pushed each probe tick would go to every client, web tabs that draw no meter included. A read the phone polls while a Board or session is on screen keeps the cost with the viewer and needs no subscription state.
2. **What moves the meter is defined on the published AppWire methods.** An `item/completed` and an `item/toolOutput/delta` each count toward a bar (spec 16.4: "transcript items and tool output events"). `turn/started`, `item/started`, `item/agentMessage/delta` and `item/reasoning/summaryTextDelta` advance only the quiet clock: a model streaming its answer is not quiet, but a flood of tokens is not a flood of work. Status, queue, usage, task, goal, job and retry notifications move neither: a model retry loop that makes no progress for ten minutes should read May be stuck. Counting the methods a commit publishes, inside that commit (`RecordAppEvent`, `server/appwire_runtime.go:413`, and `RecordDescendantAppEvent`, `:692`), counts only events the daemon accepted for the current identity.
3. **The meter counts the whole tree.** Every in-process descendant's events reach the root's server through `RecordDescendantAppEvent` (`cmd/evener/serve.go:1283-1288`, inherited at every depth by `agent/subagents.go:841`), so a coordinator's meter shows its subagents' work (Jesse's ruling). The meter is not part of `threadEnvelope`: the envelope samples a fixed set of events and never a delta (`server/thread_envelope.go:166-176`), and the meter has to see every delta. It is a plain counter under `Server.mu`, which both commits already hold.
4. **Bars are sliding minutes built from ten-second slots.** A clock-aligned minute restarts empty every sixty seconds, so the newest bar would flicker low at the top of each minute. Forty-two slots give seven one-minute bars that end at the moment the row is listed.
5. **The quiet clock starts when the daemon starts serving the session.** A meter that has seen no motion reports the time its identity was installed, so a daemon that restarted into a stuck turn reads May be stuck ten minutes later instead of never.
6. **The hub sends a quiet duration, withheld while any subagent runs.** Jesse ruled that an agent waiting on subagents is never stuck, and counting descendant events cannot keep that promise alone: a subagent inside one long model call emits nothing for minutes. So `evener/activity/read` carries `runningSubagents` (the #2493 shape) and, in place of #2493's `lastActivityAt`, a server-computed `quietForMs` that is present only while the session is working and no subagent runs. A duration computed on the hub from its own clock is immune to phone clock skew (phase 2's Review Focus 4 compares hub timestamps only), and withholding it puts Jesse's rule in one place that no client can get wrong. The shared `quietState` helper applies the spec's 3- and 10-minute thresholds and refuses both labels when `runningSubagents` is above zero, so the rule holds even against an older hub. The hub needs no S3 data for this: each live root's `RunningSubagentStates` (`hubcore/tree.go:942-973`) already names its running descendants.
7. **`runningSubagents` counts listed descendants whose own status is active,** by the tree's rule (`runningSubagentState`, `tree.go:989-994`): a descendant with no reported state is not counted.
8. **Remote hosts answer through a fan-out,** one `evener/activity/read` per attached host in parallel, each bounded at three seconds; a host that fails or is not attached contributes nothing to that read. Carrying activity on the remote hub's list rows instead would make every 30-second remote walk a content change (`hubcore/remotecache.go:114-131`) and so a navigation invalidation.
9. **Activity is keyed by the Live row's ref.** A daemon advertises a workspace ref that outlives `thread/clear` (`cmd/evener/serve.go:767`), and the Live row carries it (`tree.go:951-952`), so the read uses the same rule (`hubcore.LiveRowRef`) and a client joins activity to rows by ref.

**S4, seen-through marker**

10. **Finished is measured from a daemon-stamped turn-end time.** `SessionMeta.UpdatedAt` moves on every meta write, renames and each model round included (`agent/session_model_call.go:1493`, `agent/session_namer.go:637`), so it cannot say when a turn ended. The daemon stamps `lastTurnEndedAt` at the one boundary every turn end passes (`transitionProcessingAtBoundaryLocked`, `agent/session_state.go:321-328`), keeps it in `SessionMeta` so a restored session still knows it, and carries it on the thread envelope under `facetMeta`, which `TURN_ENDED` already re-samples (`facetAll`, `thread_envelope.go:198-200`). No new `EnvelopeSampling` method is needed: the envelope already reads `Meta()`.
11. **No new meta write at the boundary.** The stamp persists with the next meta save: processInput's exit save, retirement and `Close` (`agent/session_lifecycle.go:1916-1932`, `agent/session_retirement_prepare.go:245`, and the `Close` autosave that `TestWorkMillis_InterruptThenCloseFlushesToDisk` pins). A daemon crash in between loses only that stamp, the same exposure `WorkMillis` has.
12. **The hub computes `unseen` and sends it beside `turn_ended_at`;** clients never see the marker, the epoch or the unread flag. One rule in one place, and the phone's attention model keeps only its state precedence (Question and Failed outrank Finished). The design section sent `seen_through` on the summary and left the comparison to clients; that would put the epoch and the unread flag in every client too.
13. **The store's epoch is the time it first opened.** A turn that ended before the epoch counts as seen, so the first Board after the upgrade does not flood Finished with every live session (phase 2 Review Focus 4 avoided the same flood with a device epoch).
14. **`seen/set` takes the row's own `turn_ended_at` as `seenThrough`,** never the phone's clock or the hub's "now": a device showing an older Board marks only the turn it showed, so a newer turn stays unseen. Seen-through only moves forward. "Mark as unread" is an explicit flag, because clearing a mark would leave a session whose turn ended before the epoch impossible to mark unread. So each mark is `{ref, seenThrough}` or `{ref, unread: true}` rather than the design section's `{refs, seenThrough?}`: one `seenThrough` cannot be right for several rows that ended at different times.
15. **Per user means per hub.** The hub has no user identity (`initialize` carries only `clientInfo.name`); on a personal hub the two are the same.
16. **Live rows never fold into a cluster.** `clusterable` (`tree.go:1838-1848`) let a live idle session fold, contradicting its caller's own rule that live signal is never hidden (`clusterRepeatedTitles`, `tree.go:1768-1777`), and an unseen Finished session is live by definition. After this change only ended rows fold; the existing cluster tests use ended rows and keep passing. This is simpler than the design section's route of carrying the marker into `BuildTree` so `clusterable` could skip unseen rows, and it keeps a live row findable whether or not it is unseen.
17. **The phone switches per row, by the presence of `turn_ended_at`.** A row carrying it is hub-authoritative. A row without it (an older hub, or a live session whose daemon predates PR 16 and has not ended a turn since) keeps the phone's local `SeenMarkers` fallback unchanged. The phone does not upload its local marks: they compare `updated_at` values, and the hub's epoch already covers the upgrade. The handoff section below has the details.
18. **The web marks a session seen when its pane opens,** and when the page becomes visible again while the pane is open, so a session read on the web loses its blue dot on the phone (spec 18's "Finished-and-unseen vs Idle agrees across phone and web"). A turn that ends while the pane stays open is not "opened since", exactly as on the phone. Whether the web also draws blue dots is a later web design question.

**S3, subagent tallies**

19. **The tally comes from the delegate controller's folded journal, not from `aggregateActivity`.** `aggregateActivity` (`agent/jobs_activity.go:1519-1571`) runs over the bounded `JobActivityTree` projection, which loads child journals from disk, counts shell jobs beside delegates, and marks itself incomplete when a budget truncates it, so a 500-node tree could undercount. The controller already holds every delegate of the tree at every depth in memory (`delegatestore.State`, folded from the root's shared journal at open, `agent/delegate_tree_controller.go:275-282`). Walking it is a few microseconds and needs no memo, and because it never truncates, the row needs no `complete` flag (the design section's `NavigationSubagentCounts.complete`).
20. **It is read at list time through a seam, not sampled into the envelope.** A nested delegate's lifecycle change is emitted on its owner's stream, which reaches the root's server through `RecordDescendantAppEvent` and never samples the root's envelope (`server/appwire_runtime.go:683-690`), so an envelope facet would lag until the root's next event. `thread/list` already reads live watches through `SetDescendantLiveWatchesFunc` after releasing `s.mu` (`appwire_runtime.go:1311-1323`); the tally uses the same shape and the same lock order, and the controller lock it takes is the one `DetailedStatus` already takes (`agent/status.go:303-310`).
21. **Classification matches the Subagents list.** A delegate is running until a run has ended with no run open after it, failed when that run ended failed or exhausted, and done otherwise (completed, cancelled, stopped, or idle between runs), which is `projectStableActivityDelegate`'s `Terminal` and `activityDelegateOutcome` (`jobs_activity.go:1231-1234`, `:1573-1584`). The terminal rule moves into one shared function so the two cannot drift.
22. **Live roots only.** Ended sessions carry no tally: it would cost a delegate journal fold per row, and an ended session's row is a quiet one-line row. Rows carry `subagents` only when the tree has at least one subagent.
23. **Remote parity for S3 and S4 rides the remote hub's list rows,** the pattern PR 5 sets for approvals: the remote hub's `thread/list` root rows carry `subagents` and `lastTurnEndedAt`, and the controller reads them. Both change only on real events, so the 30-second remote walk does not churn.

## Questions for Jesse

1. **Should a failed subagent count as failed on its coordinator's row for the rest of the session?** Spec 9 defines "failed" by how a subagent's latest run ended, so a subagent that failed and was never resumed keeps the row's red "2 subagents failed" for the session's whole life, even after the coordinator spawned a replacement that succeeded. I recommend yes, as the spec defines it: the row, the Subagents chip and the Subagents list must agree, and a coordinator that resumes a failed subagent moves it back to running and then done. The alternative (count a failure only until the coordinator has read its report) would need the phone to explain why the Subagents list shows failures the row does not.
2. **Should a Finished session nobody has opened hold off the daemon's idle retirement?** The hub retires a daemon after an hour of proven inactivity (`daemon_idle_timeout`, default `1h`, `cmd/evener-hub/config.go:105`). A session whose turn finished while you were away then leaves the Live band, and its blue dot with it, although its marker still says unseen. I recommend leaving retirement as it is in this lane: the phone's fallback behaves the same today, the session stays one tap away in its project and resumes with its marker intact, and holding daemons for unread results trades memory on the host for a signal the Board could show another way. If testers miss finished work, the follow-up is a Board band for recently retired unseen sessions, read from the past index, not a longer-lived daemon.

## Review Focus

1. **A coordinator waiting on subagents reads Quiet or May be stuck.** A subagent inside one long model call emits nothing for minutes, so a quiet clock alone fails Jesse's ruling. Pinned by `TestActivityReadWithholdsQuietWhileASubagentRuns` (Task 15.3: a running subagent and 15 minutes of silence carry no quiet time) and the TypeScript test "a session waiting on subagents is never quiet or stuck" (Task 15.4).
2. **The pulse meter churns navigation.** If activity moved the roster fingerprint, every probe of a working session would bump navigation revisions and broadcast an invalidation. Pinned by `fuzzScenarioRoster_FingerprintIgnoresActivity` (Task 15.1).
3. **A quick turn between two probes never shows a blue dot.** A turn that starts and ends between two probes leaves the status `awaiting` on both. Pinned by `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheTurnEndMoves` (Task 17.2) and `TestNavigationRowsCarryTurnEndedAtAndUnseen` (Task 17.3).
4. **A stale or skewed mark hides a newer turn, or the first run floods Finished.** A device showing an older Board marks only the turn it showed, seen-through never moves back, the millisecond round trip through `turn_ended_at` is exact, and turns before the epoch read seen. Pinned by `fuzzScenarioSessionSeenStore_MarkSeenMovesForwardOnly` and `fuzzScenarioSessionSeenSnapshot_Unseen` (Task 17.1), and `TestNavigationRowsCarryTurnEndedAtAndUnseen` (Task 17.3).
5. **A large tree undercounts.** Nested subagents, subagents past the row's children cap, and failures must all count. Pinned by `TestSubagentTallyCountsEveryDelegateInTheTreeByState` (Task 18.1) and `TestNavigationRowsCarryTheWholeTreesSubagentTally` (Task 19.2, a root with 60 subagents whose row omits some children).

---

## PRs and lanes

| PR | Item | Tasks | Production lines (est.) | Depends on | Lane |
|---|---|---|---|---|---|
| 14 | S5a: the daemon's pulse meter | 14.1-14.2 | 140 | none | daemon |
| 15 | S5b: `evener/activity/read` | 15.1-15.5 | 300 | PR 14 | hub |
| 16 | S4a: the daemon stamps turn ends | 16.1-16.2 | 45 | none | daemon |
| 17 | S4b: the seen marker, rows and the web | 17.1-17.6 | 480 | PR 16; a TestFlight build containing #2473 | hub |
| 18 | S3a: the daemon's subagent tally | 18.1-18.2 | 110 | none | daemon |
| 19 | S3b: tallies on rows | 19.1-19.3 | 80 | PR 18; a TestFlight build containing #2473 | hub |

- **The three daemon PRs (14, 16, 18) can run as parallel lanes.** Each appends one field to the end of `EvenerThread` (`appwire/types.go:822`) and regenerates `types.gen.ts` and `docs/appwire-protocol.md`; PRs 14 and 18 both touch `handleAppThreadList` and `appwire/clone.go`. The second and third to merge rebase, keep both sides of those adjacent-line conflicts, and re-run `make generate`.
- **The three hub PRs (15, 17, 19) can be built in parallel but merge one at a time,** in the order PR 15, PR 17, PR 19. They touch the same hub files (`prober.go`, `roster.go`, `tree.go`, `app_rpc.go`, `appwire/protocol.go`, the handler-set test; 17 and 19 also the summary, the codec and the fixture), so each rebases on the one before it and re-runs `make generate`.
- The server plan's PRs 2, 3 and 12 are on main and this plan is written against them. PRs 5 and 6 edit the same hub files (`roster.go`, `tree.go`, `local_daemon.go`, `web_api_tree.go`, the summary and the codec); the coordinator sequences them with these, and whichever merges second rebases.
- S5 does not depend on S3 (ruling 6), so the roadmap's value order (S5, S4, S3) holds.

## Phone lane handoff

This lane changes `mobile-native/` only through the shared package. When each hub PR is on the hub the phone talks to, the phone lane switches off its fallback as follows.

**S5 (after PR 15).**
- While a Board or session screen is on screen and the connection is live, call `evener/activity/read` every 10 seconds: no `refs` from the Board, `refs: [ref]` from a session screen. Decode with `decodeActivityRead` (Task 15.4). A method-not-found answer (`-32601`) means an older hub: stop polling on that connection and keep the fallbacks.
- A Working row's `PulseMeter` gets `perMinute={activity.minutes}`; the fleet meter sums every session's minutes, bar by bar. A row with no entry keeps the one-bar fallback (`WORKING_WITHOUT_ACTIVITY`).
- Labels come from `quietState(activity, msSinceTheRead)`: "Quiet 4m" for `quiet`, "May be stuck · no updates for 12m" for `stuck`, and the stuck rows float to the top of Working (spec 7.1). The helper never returns a label while `runningSubagents` is above zero.
- "Waiting on N subagents" takes N from `runningSubagents`, which counts every depth; the loaded-children count is the fallback.

**S4 (after PR 17).**
- For a row that carries `turn_ended_at`, Finished versus Idle is `row.unseen === true`, and the local marker is ignored for that row. A row without it keeps the local `SeenMarkers` path from phase 2, unchanged.
- Opening such a row calls `evener/session/seen/set` with `{sessions: [{ref: row.ref, seenThrough: Date.parse(row.turn_ended_at)}]}`. So the dot clears at once, keep an in-memory pending map of `ref` to `seenThrough`, and treat the row as seen while `Date.parse(row.turn_ended_at) <= pending[ref]`; drop the entry once the hub's row reads `unseen` false or carries a newer `turn_ended_at`.
- Mark as read (long-press and select mode) sends the same call, one entry per row with `turn_ended_at`; Mark as unread sends `{ref, unread: true}`. Rows without `turn_ended_at` keep using `SeenMarkers.markSeen` and `markUnread`.
- The call is idempotent, so resend it after a reconnect rather than tracking it in the navigation recovery checkpoint.
- Order Finished and Idle newest first by `turn_ended_at`, falling back to `updated_at`.

**S3 (after PR 19).**
- When a row carries `subagents`, the last line's "2 subagents failed" comes from `subagents.failed`, and the Subagents chip's strip and counts come from `running`, `failed` and `done`. Without it, the children fallback stays.

**Known limits.**
- A NeedsYou row carries no workspace ref (the NeedsYou builder in `hubcore/tree.go:1563-1574` sets none), so after a `thread/clear` its ref is the new session's ID while the session's Live row keeps the workspace ref. Its `unseen` is keyed by that ref, so after a clear it can disagree with the Live row's. The Board reads Finished from the Live band and Question and Approval outrank it, so the Board is not affected; a client that joins the two bands by ref sees two refs for one session, as it does today.

---

## PR 14: the daemon's pulse meter, S5a (Tasks 14.1-14.2)

**Branch:** `git fetch origin && git switch -c claude/s5a-activity-meter origin/main`

**What it adds.** A daemon counts its root tree's transcript motion and stamps the sample on its `thread/list` root row as `EvenerThread.activity`. Nothing reads it yet; PR 15 makes the hub serve it.

### Task 14.1: The activity meter

**Files:**
- Create: `server/activity_meter.go`
- Create: `server/activity_meter_test.go`
- Modify: `appwire/types.go` (new type `ThreadActivity`; `EvenerThread.Activity` after `VisionModel`, `:822`)
- Modify: `appwire/clone.go` (`CloneThreadActivity`; one line in `cloneEvenerThread`, `:104-117`)
- Modify: `appwire/clone_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: the `appwire.Notify*` method constants (`appwire/types.go:208-215`).
- Produces:
  - `appwire.ThreadActivity{Minutes []int; LastActivityAt int64}` with JSON `minutes`, `lastActivityAt`; `EvenerThread.Activity *ThreadActivity` with JSON `activity,omitempty`; TS `ThreadActivity` and `EvenerThread.activity?: ThreadActivity`.
  - `appwire.CloneThreadActivity(*ThreadActivity) *ThreadActivity` (PR 15 uses it).
  - In package `server`: `type activityMeter struct { now func() time.Time; ... }` with `restart()`, `observe(method string)`, `snapshot() *appwire.ThreadActivity`.

- [ ] **Step 1: Write the failing tests**

`server/activity_meter_test.go`:

```go
package server

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// activityTestClock is a hand-advanced clock for the meter.
type activityTestClock struct{ now time.Time }

func (c *activityTestClock) Now() time.Time { return c.now }

// activityTestStart is a ten-second slot boundary (1_800_000_000 is a multiple
// of ten), so each test knows which slot an event lands in.
var activityTestStart = time.Unix(1_800_000_000, 0).UTC()

func startedMeter() (*activityMeter, *activityTestClock) {
	clock := &activityTestClock{now: activityTestStart}
	meter := &activityMeter{now: clock.Now}
	meter.restart()
	return meter, clock
}

// An item finishing and each tool output event count toward the newest bar;
// streaming and bookkeeping notifications do not (spec 16.4: "transcript items
// and tool output events").
func TestActivityMeterCountsItemsAndToolOutputInTheNewestMinute(t *testing.T) {
	meter, _ := startedMeter()
	for _, method := range []string{
		appwire.NotifyItemCompleted,
		appwire.NotifyToolOutputDelta,
		appwire.NotifyToolOutputDelta,
		appwire.NotifyAgentMessageDelta,
		appwire.NotifyItemStarted,
		appwire.NotifyThreadStatusChanged,
		appwire.NotifyEvenerTaskUpdated,
		appwire.NotifyEvenerThreadModelRetry,
	} {
		meter.observe(method)
	}
	if got, want := meter.snapshot().Minutes, []int{0, 0, 0, 0, 0, 0, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("minutes = %v, want %v", got, want)
	}
}

// Each bar is a sliding minute ending at the read: an event moves to the next
// older bar sixty seconds after it happened and leaves the meter after seven
// minutes. A quiet minute is zero.
func TestActivityMeterSlidesAnEventThroughOlderBarsAndDropsItAfterSevenMinutes(t *testing.T) {
	meter, clock := startedMeter()
	meter.observe(appwire.NotifyItemCompleted)
	for _, step := range []struct {
		after time.Duration
		want  []int
	}{
		{59 * time.Second, []int{0, 0, 0, 0, 0, 0, 1}},
		{60 * time.Second, []int{0, 0, 0, 0, 0, 1, 0}},
		{3*time.Minute + 30*time.Second, []int{0, 0, 0, 1, 0, 0, 0}},
		{6*time.Minute + 59*time.Second, []int{1, 0, 0, 0, 0, 0, 0}},
		{7 * time.Minute, []int{0, 0, 0, 0, 0, 0, 0}},
	} {
		clock.now = activityTestStart.Add(step.after)
		if got := meter.snapshot().Minutes; !reflect.DeepEqual(got, step.want) {
			t.Fatalf("after %s: minutes = %v, want %v", step.after, got, step.want)
		}
	}
}

// A slot reused on a later lap of the ring counts only its own events.
func TestActivityMeterReusedSlotForgetsItsEarlierLap(t *testing.T) {
	meter, clock := startedMeter()
	meter.observe(appwire.NotifyItemCompleted)
	meter.observe(appwire.NotifyItemCompleted)
	clock.now = activityTestStart.Add(7 * time.Minute) // the same ring position, one lap later
	meter.observe(appwire.NotifyToolOutputDelta)
	if got, want := meter.snapshot().Minutes, []int{0, 0, 0, 0, 0, 0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("minutes = %v, want %v", got, want)
	}
}

// The quiet clock starts when the meter starts, and any transcript motion
// (streaming included) moves it; bookkeeping does not.
func TestActivityMeterQuietClockMovesOnTranscriptMotionOnly(t *testing.T) {
	meter, clock := startedMeter()
	if got, want := meter.snapshot().LastActivityAt, activityTestStart.UnixMilli(); got != want {
		t.Fatalf("a meter nothing has moved reports %d, want its start %d", got, want)
	}
	clock.now = activityTestStart.Add(90 * time.Second)
	meter.observe(appwire.NotifyThreadStatusChanged)
	if got, want := meter.snapshot().LastActivityAt, activityTestStart.UnixMilli(); got != want {
		t.Fatalf("a status change moved the quiet clock to %d, want %d", got, want)
	}
	meter.observe(appwire.NotifyReasoningSummaryDelta)
	if got, want := meter.snapshot().LastActivityAt, clock.now.UnixMilli(); got != want {
		t.Fatalf("a model thinking left the quiet clock at %d, want %d", got, want)
	}
	if got, want := meter.snapshot().Minutes, []int{0, 0, 0, 0, 0, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("streaming counted toward a bar: minutes = %v", got)
	}
}

func TestActivityMeterReadsNothingBeforeItStarts(t *testing.T) {
	if got := (&activityMeter{}).snapshot(); got != nil {
		t.Fatalf("an unstarted meter reported %+v, want nil", got)
	}
}
```

Append to `appwire/clone_test.go`:

```go
func TestCloneThreadOwnsActivity(t *testing.T) {
	original := Thread{Evener: EvenerThread{Activity: &ThreadActivity{Minutes: []int{0, 1, 2, 3, 4, 5, 6}, LastActivityAt: 7}}}
	clone := CloneThread(original)
	if !reflect.DeepEqual(clone, original) {
		t.Fatal("clone changed values while copying")
	}
	clone.Evener.Activity.Minutes[6] = 99
	clone.Evener.Activity.LastActivityAt = 8
	if original.Evener.Activity.Minutes[6] != 6 || original.Evener.Activity.LastActivityAt != 7 {
		t.Fatalf("activity was changed through its clone: %+v", original.Evener.Activity)
	}
	if CloneThreadActivity(nil) != nil {
		t.Fatal("a nil sample cloned to a non-nil one")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestActivityMeter' -count=1` and `go test ./appwire -run 'TestCloneThreadOwnsActivity' -count=1`
Expected: FAIL to compile (`activityMeter`, `ThreadActivity`, `CloneThreadActivity` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, after `EvenerThread.VisionModel`:

```go
	// Activity is the pulse meter of a live root session's whole tree (spec
	// 16.4, S5): the root and every in-process descendant. It rides thread/list
	// rows only, never a thread/read snapshot, because nothing announces its
	// changes to a subscriber; the hub serves it through evener/activity/read,
	// never navigation. Absent on descendant rows and from an older daemon.
	Activity *ThreadActivity `json:"activity,omitempty"`
```

and, after the `EvenerThread` type:

```go
// ThreadActivity is one pulse meter sample. Minutes holds seven one-minute
// counts, oldest first, the last ending when the row was listed; each counts
// the transcript items that finished and the tool output events in that minute.
// LastActivityAt is the Unix-millisecond time of the tree's newest transcript
// motion (a turn or item starting, a message or reasoning summary streaming, an
// item finishing, a tool writing output), or the time the daemon began serving
// this session when nothing has moved since.
type ThreadActivity struct {
	Minutes        []int `json:"minutes"`
	LastActivityAt int64 `json:"lastActivityAt"`
}
```

`appwire/clone.go`, in `cloneEvenerThread` after `e.FailedToolCalls = cloneInt(e.FailedToolCalls)`: `e.Activity = CloneThreadActivity(e.Activity)`; and after `cloneInt64`:

```go
// CloneThreadActivity deep-copies a pulse meter sample; nil stays nil.
func CloneThreadActivity(value *ThreadActivity) *ThreadActivity {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Minutes = append([]int(nil), value.Minutes...)
	return &clone
}
```

`server/activity_meter.go`:

```go
package server

import (
	"time"

	"primeradiant.com/evener/appwire"
)

// The pulse meter (spec 16.4) draws seven one-minute bars. The meter keeps
// ten-second slots so each bar is a minute that ends when the row is listed,
// rather than a clock minute that starts empty every sixty seconds.
const (
	activitySlotSeconds = 10
	activitySlotsPerBar = 6
	activityBars        = 7
	activitySlotCount   = activityBars * activitySlotsPerBar
)

// activitySlot is one ten-second slot of the ring. index is the slot's absolute
// number (Unix seconds divided by activitySlotSeconds), so a slot left over from
// an earlier lap of the ring reads as empty.
type activitySlot struct {
	index int64
	count int
}

// activityMeter counts one root session tree's transcript motion for the pulse
// meter and the Quiet and May be stuck labels (spec 13.1, 16.4). The root and
// every in-process descendant feed it, so a coordinator's meter shows its whole
// tree (Jesse's ruling for S5).
//
// RecordAppEvent and RecordDescendantAppEvent feed it the AppWire methods they
// publish, inside their projection commits, and the thread list reads it; all
// three hold Server.mu, which guards it. It is not part of threadEnvelope: the
// envelope samples a fixed set of events and never a delta (facetsByEvent in
// thread_envelope.go), and the meter has to see every delta.
type activityMeter struct {
	now        func() time.Time
	slots      [activitySlotCount]activitySlot
	lastMotion time.Time
}

func (m *activityMeter) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// restart empties the meter and starts its quiet clock now. It runs when an
// identity is installed: a replaced identity is a different session, and a
// session that has not moved since its daemon began serving it has been quiet
// since then.
func (m *activityMeter) restart() {
	m.slots = [activitySlotCount]activitySlot{}
	m.lastMotion = m.clock()
}

// observe records one published AppWire notification. An item finishing and a
// tool writing output each count toward the current bar ("transcript items and
// tool output events", spec 16.4). A turn or item starting and a message or
// reasoning summary streaming prove the tree is moving without finishing
// anything, so they only advance the quiet clock. Everything else (status,
// queue, usage, task, goal, job and retry notifications) is bookkeeping and
// moves neither: a retry loop that makes no progress should read May be stuck.
func (m *activityMeter) observe(method string) {
	switch method {
	case appwire.NotifyItemCompleted, appwire.NotifyToolOutputDelta:
		at := m.clock()
		m.count(at)
		m.touch(at)
	case appwire.NotifyTurnStarted, appwire.NotifyItemStarted, appwire.NotifyAgentMessageDelta, appwire.NotifyReasoningSummaryDelta:
		m.touch(m.clock())
	}
}

func (m *activityMeter) count(at time.Time) {
	index := at.Unix() / activitySlotSeconds
	slot := &m.slots[index%activitySlotCount]
	if slot.index != index {
		*slot = activitySlot{index: index}
	}
	slot.count++
}

func (m *activityMeter) touch(at time.Time) {
	if at.After(m.lastMotion) {
		m.lastMotion = at
	}
}

// snapshot reads the meter for a thread list row: seven bars, oldest first, the
// last ending in the current slot, and the time of the newest motion. It is nil
// until an identity has started the meter.
func (m *activityMeter) snapshot() *appwire.ThreadActivity {
	if m.lastMotion.IsZero() {
		return nil
	}
	current := m.clock().Unix() / activitySlotSeconds
	minutes := make([]int, activityBars)
	for bar := range activityBars {
		newest := current - int64((activityBars-1-bar)*activitySlotsPerBar)
		for index := newest - activitySlotsPerBar + 1; index <= newest; index++ {
			if slot := m.slots[index%activitySlotCount]; slot.index == index {
				minutes[bar] += slot.count
			}
		}
	}
	return &appwire.ThreadActivity{Minutes: minutes, LastActivityAt: m.lastMotion.UnixMilli()}
}
```

Run `$(go env GOROOT)/bin/gofmt -w server/activity_meter.go server/activity_meter_test.go appwire/types.go appwire/clone.go appwire/clone_test.go`, then `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./server -run 'TestActivityMeter' -count=1`, `go test ./appwire -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `ThreadActivity` and `activity?: ThreadActivity` on `EvenerThread`.

- [ ] **Step 5: Commit**

```bash
git add server/activity_meter.go server/activity_meter_test.go appwire/types.go appwire/clone.go appwire/clone_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(server): a pulse meter counts a session tree's transcript motion"
```

### Task 14.2: The root row carries the whole tree's meter

**Files:**
- Modify: `server/server.go` (`Server`: an `appActivity activityMeter` field after `appEnvelopeSource`, `:366`)
- Modify: `server/appwire_runtime.go` (`RecordAppEvent` `:413`; `RecordDescendantAppEvent` `:692`; `ReplaceAppIdentity` `:261`; `handleAppThreadList` `:1293`)
- Create: `server/appwire_activity_test.go`

**Interfaces:**
- Consumes: Task 14.1's `activityMeter`.
- Produces: the `thread/list` root row's `Evener.Activity`, which PR 15's probe reads.

- [ ] **Step 1: Write the failing tests**

`server/appwire_activity_test.go`:

```go
package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func activityTestServer(t *testing.T, threadID string) (*Server, *activityTestClock) {
	t.Helper()
	clock := &activityTestClock{now: activityTestStart}
	srv := NewServer(ServerConfig{})
	srv.appActivity.now = clock.Now
	srv.SetAppIdentity("local", threadID)
	return srv, clock
}

func listedRootActivity(t *testing.T, srv *Server) (*appwire.ThreadActivity, []appwire.Thread) {
	t.Helper()
	list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{IncludeSubagents: true, StatusOnly: true})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	return list.Data[0].Evener.Activity, list.Data[1:]
}

// The root row's meter counts the root's own transcript items and tool output
// and every in-process descendant's (Jesse's ruling for S5: a coordinator's
// meter shows its whole tree). Descendant rows and thread/read carry none.
func TestThreadListRootRowCarriesTheWholeTreesActivity(t *testing.T) {
	srv, clock := activityTestServer(t, "root")
	// The root: a user message finishes (1), a tool streams two output
	// deltas (2) and finishes (1).
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "root", Data: events.UserInputData{Text: "run the tests"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "root", Data: events.ToolCallStartData{ToolName: "shell", CallID: "call-1"}})
	for range 2 {
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventToolCallOutputDelta, SessionID: "root", Data: events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call-1", Delta: "ok\n"}})
	}
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "root", Data: events.ToolCallEndData{ToolName: "shell", CallID: "call-1", Output: "ok"}})
	// A subagent's message finishes a minute later (1).
	clock.now = activityTestStart.Add(time.Minute)
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventUserInput, SessionID: "child-1", Data: events.UserInputData{Text: "fix the race"}})

	activity, descendants := listedRootActivity(t, srv)
	if activity == nil {
		t.Fatal("the root row carries no activity")
	}
	if want := []int{0, 0, 0, 0, 0, 4, 1}; !reflect.DeepEqual(activity.Minutes, want) {
		t.Fatalf("root minutes = %v, want %v", activity.Minutes, want)
	}
	if activity.LastActivityAt != clock.now.UnixMilli() {
		t.Fatalf("last activity = %d, want the subagent's %d", activity.LastActivityAt, clock.now.UnixMilli())
	}
	if len(descendants) == 0 {
		t.Fatal("the list carries no descendant row")
	}
	for _, row := range descendants {
		if row.Evener.Activity != nil {
			t.Fatalf("descendant row %s carries activity %+v", row.ID, row.Evener.Activity)
		}
	}
	if read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{}); read.Thread.Evener.Activity != nil {
		t.Fatalf("thread/read carries activity %+v; nothing announces its changes to a subscriber", read.Thread.Evener.Activity)
	}
}

// A replaced identity is a different session: its meter starts empty, its
// quiet clock starts when it is installed, and a late event from the replaced
// tree counts for nothing.
func TestReplacedIdentityStartsAFreshMeter(t *testing.T) {
	srv, clock := activityTestServer(t, "old")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "old", Data: events.UserInputData{Text: "work"}})
	clock.now = activityTestStart.Add(30 * time.Second)
	srv.SetAppIdentity("local", "new")
	srv.RecordDescendantAppEvent("old", events.SessionEvent{Kind: events.EventUserInput, SessionID: "old-child", Data: events.UserInputData{Text: "late"}})

	activity, _ := listedRootActivity(t, srv)
	if want := []int{0, 0, 0, 0, 0, 0, 0}; activity == nil || !reflect.DeepEqual(activity.Minutes, want) {
		t.Fatalf("new identity's activity = %+v, want empty minutes", activity)
	}
	if activity.LastActivityAt != clock.now.UnixMilli() {
		t.Fatalf("new identity's quiet clock starts at %d, want its install time %d", activity.LastActivityAt, clock.now.UnixMilli())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestThreadListRootRowCarriesTheWholeTreesActivity|TestReplacedIdentityStartsAFreshMeter' -count=1`
Expected: FAIL to compile (`srv.appActivity` undefined).

- [ ] **Step 3: Implement**

`server/server.go`, after `appEnvelopeSource ThreadEnvelopeSource`:

```go
	// appActivity is the root tree's pulse meter (S5). The projection commits
	// feed it and the thread list reads it, all under mu; see activityMeter.
	appActivity activityMeter
```

`server/appwire_runtime.go`:
- `ReplaceAppIdentity`: right after `s.appEnvelope = threadEnvelope{}`, add `s.appActivity.restart()`.
- `RecordAppEvent`: right after `projected := s.appProjector.Project(event)`, add:

```go
		for _, item := range projected {
			s.appActivity.observe(item.Method)
		}
```

- `RecordDescendantAppEvent`: right after `projected := projection.projector.Project(event)`, add the same loop, with this comment above it: `// A descendant's motion is its root's too: the meter shows the whole tree.`
- `handleAppThreadList`: right after `data := []appwire.Thread{s.appThreadWithDiagnosticsLocked(diagnostics)}`, add:

```go
	// Only the list carries the meter: a thread/read snapshot would hand a
	// subscriber a value no notification ever updates.
	data[0].Evener.Activity = s.appActivity.snapshot()
```

Run `$(go env GOROOT)/bin/gofmt -w server/server.go server/appwire_runtime.go server/appwire_activity_test.go`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./server -count=1`
Expected: PASS, including every existing thread list and projection test unchanged.

Gates: `go vet ./server/... ./appwire/...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`, `golangci-lint run ./appwire/`, `golangci-lint run --config .golangci-appwire.yml ./server/...`, `make lint-generated`.

- [ ] **Step 5: Commit and open PR 14**

```bash
git add server/server.go server/appwire_runtime.go server/appwire_activity_test.go
git commit -m "feat(server): thread/list root rows carry the whole tree's pulse meter"
```

Title "feat(server): the daemon's pulse meter (S5a, phase 7 PR 14)". The body says the meter counts `item/completed` and `item/toolOutput/delta` per bar, the quiet clock moves on any transcript motion, it rides `thread/list` rows only, and nothing consumes it until PR 15.

---

## PR 15: `evener/activity/read`, S5b (Tasks 15.1-15.5)

**Branch:** `git fetch origin && git switch -c claude/s5b-activity-read origin/main`. PR 14 is on main.

**What it adds.** The hub keeps each live root's pulse meter sample from its probe and serves it, with the root's running subagents and a quiet time, through a new polled method. Its fallback today is `updated_at` and a single still bar (phase 2 ruling 3); the phone switches as the handoff section says.

### Task 15.1: The probe keeps the tree's sample, off the fingerprint

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (the result literal at the end of `Probe`, `:148-167`)
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry` `:28-87`; `ProbeResult` `:90-122`; `CloneLiveEntry` `:165`; `liveEntryFromProbe` `:1309`)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`LiveRowRef` beside `liveWorkspaceIdentity`, `:856`)
- Create: `cmd/evener-hub/internal/hubcore/activity_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register the four scenarios)

**Interfaces:**
- Consumes: `appwire.ThreadActivity`, `appwire.CloneThreadActivity` (PR 14).
- Produces: `ProbeResult.Activity` and `LiveEntry.Activity *appwire.ThreadActivity`; `hubcore.LiveRowRef(LiveEntry) string`.

- [ ] **Step 1: Write the failing scenarios**

`cmd/evener-hub/internal/hubcore/activity_test.go`:

```go
package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheTreesActivity: the probe keeps the listed
// root's pulse meter sample, which evener/activity/read serves (S5).
func fuzzScenarioStatusProber_KeepsTheTreesActivity(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_activity",
		state:     appwire.ThreadStatusActive,
		setup: func(srv *server.Server) {
			srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_activity", Data: events.UserInputData{Text: "go"}})
		},
	})
	got := prober.Probe(entry)
	if !got.OK || got.Activity == nil {
		t.Fatalf("probe = %+v, want the root's activity", got)
	}
	if len(got.Activity.Minutes) != 7 || got.Activity.Minutes[6] < 1 || got.Activity.LastActivityAt == 0 {
		t.Fatalf("activity = %+v, want seven bars with the user message in the newest", got.Activity)
	}
}

// fuzzScenarioRoster_FingerprintIgnoresActivity: the pulse meter moves every
// minute a session works, and hashing it would bump navigation revisions and
// broadcast an invalidation on every probe (S5 ruling 1).
func fuzzScenarioRoster_FingerprintIgnoresActivity(t *testing.T) {
	entry := func(activity *appwire.ThreadActivity) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusActive, Activity: activity}}
	}
	quiet := entry(&appwire.ThreadActivity{Minutes: []int{0, 0, 0, 0, 0, 0, 1}, LastActivityAt: 1})
	busy := entry(&appwire.ThreadActivity{Minutes: []int{0, 0, 0, 0, 0, 9, 40}, LastActivityAt: 2})
	if rosterFingerprint(quiet) != rosterFingerprint(busy) || rosterFingerprint(quiet) != rosterFingerprint(entry(nil)) {
		t.Fatal("the roster fingerprint moved when only the pulse meter did")
	}
}

// fuzzScenarioRoster_EntriesOwnTheirActivity: an entry never aliases the
// probe's sample, and a clone never aliases the entry's.
func fuzzScenarioRoster_EntriesOwnTheirActivity(t *testing.T) {
	sample := &appwire.ThreadActivity{Minutes: []int{0, 0, 0, 0, 0, 0, 1}, LastActivityAt: 1}
	fromProbe := liveEntryFromProbe(rendezvous.Entry{PID: 1, SessionID: "01A"}, ProbeResult{SessionID: "01A", OK: true, Activity: sample})
	sample.Minutes[6] = 99
	if fromProbe.Activity.Minutes[6] != 1 {
		t.Fatal("liveEntryFromProbe aliased the probe's minutes")
	}
	clone := CloneLiveEntry(fromProbe)
	clone.Activity.Minutes[6] = 42
	if fromProbe.Activity.Minutes[6] != 1 {
		t.Fatal("CloneLiveEntry aliased the entry's minutes")
	}
}

// fuzzScenarioLiveRowRef_IsTheRefTheLiveRowCarries: a client joins activity
// to Board rows by ref, so the read spells a session's ref exactly as its Live
// row does, including the workspace ref a daemon keeps across thread/clear.
func fuzzScenarioLiveRowRef_IsTheRefTheLiveRowCarries(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cleared := LiveEntry{PID: 1, SourceID: "local", WorkspaceRef: "local:01WORKSPACE", SessionID: "01CURRENT", Status: appwire.ThreadStatusActive}
	plain := LiveEntry{PID: 2, SessionID: "01PLAIN", Status: appwire.ThreadStatusActive}
	tree := BuildTreeAt(nil, []LiveEntry{cleared, plain}, map[ArchiveKey]bool{}, now)
	rowRefs := map[string]string{}
	for _, row := range tree.Live {
		rowRefs[row.ID] = row.Ref
	}
	if got := LiveRowRef(cleared); got != "local:01WORKSPACE" || rowRefs["01CURRENT"] != got {
		t.Fatalf("LiveRowRef = %q, Live row ref = %q, want both local:01WORKSPACE", got, rowRefs["01CURRENT"])
	}
	// A row with no workspace ref carries none, and navigation spells its ID
	// as a local ref.
	if got := LiveRowRef(plain); got != "local:01PLAIN" || rowRefs["01PLAIN"] != "" {
		t.Fatalf("LiveRowRef = %q, Live row ref = %q, want local:01PLAIN and an empty row ref", got, rowRefs["01PLAIN"])
	}
}
```

Register the four in `FuzzHubcoreScenarios`, in alphabetical order.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`Activity` and `LiveRowRef` undefined).

- [ ] **Step 3: Implement**

`roster.go`, at the end of `ProbeResult`, after `Tasks`:

```go
	// Activity is the root tree's pulse meter sample (S5), nil from a daemon
	// that predates it. See LiveEntry.Activity.
	Activity *appwire.ThreadActivity
```

and in `Probe`'s result literal (`prober.go`), after `Tasks: root.Evener.Tasks,`: `Activity: appwire.CloneThreadActivity(root.Evener.Activity),`.

`roster.go`, at the end of `LiveEntry`, after `Tasks`:

```go
	// Activity is the daemon's pulse meter sample for the root's whole tree,
	// from the probe that produced this entry (S5). evener/activity/read serves
	// it and navigation never does. rosterFingerprint leaves it out on purpose:
	// its bars move every minute a session works, and hashing them would bump
	// navigation revisions and broadcast an invalidation on every probe.
	Activity *appwire.ThreadActivity
```

`CloneLiveEntry`, after its `Tasks` line: `out.Activity = appwire.CloneThreadActivity(in.Activity)`; `cloneNavigationLiveEntries` calls it, so navigation's copy needs nothing more. `liveEntryFromProbe`, after `Tasks:`: `Activity: appwire.CloneThreadActivity(result.Activity),`.

`tree.go`, after `liveWorkspaceIdentity`:

```go
// LiveRowRef is the ref a live root's Live row carries (liveRefMap in
// buildTreeAtWithProjects): the workspace ref its daemon advertises when that
// ref is valid for the daemon's own source, else the session's local ref. A
// client joining per-session data to Board rows by ref, as it does with
// evener/activity/read, needs this exact spelling.
func LiveRowRef(entry LiveEntry) string {
	if _, ref, ok := liveWorkspaceIdentity(entry); ok {
		return ref.String()
	}
	return hubapi.LocalRef(entry.SessionID).String()
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS, and no unused scenario.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/activity_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): the probe keeps each live root's pulse meter, off the fingerprint"
```

### Task 15.2: The wire shape of `evener/activity/read`

**Files:**
- Modify: `appwire/types.go` (`MethodEvenerActivityRead` beside `MethodEvenerSearch`, `:87`; three new types)
- Create: `appwire/activity_read_test.go`

**Interfaces:**
- Produces: `appwire.MethodEvenerActivityRead = "evener/activity/read"`; `ActivityReadParams{Refs []string}` (`refs,omitempty`); `ActivityReadResponse{Sessions []SessionActivity}` (`sessions`); `SessionActivity{Ref string; Minutes []int; RunningSubagents int; QuietForMS *int64}` (`ref`, `minutes`, `runningSubagents`, `quietForMs,omitempty`). TypeScript gains the same names once Task 15.3's catalog row makes them reachable.

- [ ] **Step 1: Write the failing test**

`appwire/activity_read_test.go`:

```go
package appwire

import (
	"encoding/json"
	"testing"
)

// The quiet time is absent when the hub withholds it (a subagent running, or
// the session not working) and present when it is zero: "quiet for 0 ms" and
// "cannot be quiet" are different claims (S5).
func TestSessionActivityOmitsOnlyAWithheldQuietTime(t *testing.T) {
	zero := int64(0)
	for _, tc := range []struct {
		activity SessionActivity
		want     string
	}{
		{SessionActivity{Ref: "local:a", Minutes: []int{0, 1}, RunningSubagents: 2}, `{"ref":"local:a","minutes":[0,1],"runningSubagents":2}`},
		{SessionActivity{Ref: "local:a", Minutes: []int{0, 1}, QuietForMS: &zero}, `{"ref":"local:a","minutes":[0,1],"runningSubagents":0,"quietForMs":0}`},
	} {
		raw, err := json.Marshal(tc.activity)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != tc.want {
			t.Fatalf("marshal = %s, want %s", raw, tc.want)
		}
	}
	raw, err := json.Marshal(ActivityReadParams{})
	if err != nil || string(raw) != `{}` {
		t.Fatalf("empty params = %s (%v), want {}: no refs reads every session", raw, err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./appwire -run 'TestSessionActivityOmitsOnlyAWithheldQuietTime' -count=1`
Expected: FAIL to compile (`SessionActivity` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, in the method constants beside `MethodEvenerSearch`:

```go
	// MethodEvenerActivityRead reads the pulse meter, running subagents and
	// quiet time of the hub's live top-level sessions (S5).
	MethodEvenerActivityRead = "evener/activity/read"
```

and near `SearchParams`:

```go
// ActivityReadParams selects the sessions evener/activity/read reports. Refs
// names sessions by the refs their Live rows carry; empty reads every live
// top-level session of the hub and of its attached hosts.
type ActivityReadParams struct {
	Refs []string `json:"refs,omitempty"`
}

// ActivityReadResponse is one read of the pulse meters. A session whose daemon
// predates the meter, or whose host did not answer in time, is absent, and a
// client keeps its fallback for it until the next read.
type ActivityReadResponse struct {
	Sessions []SessionActivity `json:"sessions"`
}

// SessionActivity is one live top-level session's activity (spec 13.1, 16.4).
type SessionActivity struct {
	// Ref is the session's navigation ref, the one its Live row carries.
	Ref string `json:"ref"`
	// Minutes holds seven one-minute counts over the session's whole tree,
	// oldest first, the last ending when the hub last probed its daemon: the
	// transcript items that finished and the tool output events in each.
	Minutes []int `json:"minutes"`
	// RunningSubagents counts the session's subagents, at every depth, whose
	// own turn is running.
	RunningSubagents int `json:"runningSubagents"`
	// QuietForMS is how long the session's whole tree has gone without
	// transcript motion, as of this read. It is present only while the session
	// is working and none of its subagents runs: an agent waiting on subagents
	// is never quiet or stuck (Jesse's ruling for S5), and a subagent inside
	// one long model call emits nothing for minutes.
	QuietForMS *int64 `json:"quietForMs,omitempty"`
}
```

Run `$(go env GOROOT)/bin/gofmt -w appwire/types.go appwire/activity_read_test.go`. The catalog row lands with its handler in Task 15.3, so every commit keeps `TestHubRouterMatchesCatalog` green; until then `make generate` has nothing new to emit, because the generator reaches types only through the catalog (`internal/appwirets/emit.go:551-609`).

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./appwire -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go appwire/activity_read_test.go
git commit -m "feat(appwire): the evener/activity/read wire shape"
```

### Task 15.3: The hub reads its own sessions, and withholds quiet while a subagent runs

**Files:**
- Modify: `appwire/protocol.go` (a catalog row after `MethodEvenerSearch`'s, `:165`)
- Create: `cmd/evener-hub/app_activity.go`
- Create: `cmd/evener-hub/app_activity_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (register beside `registerFavoriteHandler`, `:1097`)
- Modify: `cmd/evener-hub/app_rpc_test.go` (`TestHubRPCRegistersExpectedHandlerSet`, `:12665`: add `appwire.MethodEvenerActivityRead` after `appwire.MethodEvenerSearch`)
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `LiveEntry.Activity`, `hubcore.LiveRowRef` (Task 15.1); the types of Task 15.2.
- Produces: the catalog row; `hubActivityRead(ctx, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ActivityReadParams, now time.Time) (appwire.ActivityReadResponse, error)`; `registerActivityReadHandler(server, cfg, sources)`. Task 15.5 adds the remote half.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_activity_test.go`:

```go
package hub

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

var activityReadNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

var activityReadMinutes = []int{0, 0, 3, 1, 0, 0, 0}

func silentFor(d time.Duration) *appwire.ThreadActivity {
	return &appwire.ThreadActivity{Minutes: append([]int(nil), activityReadMinutes...), LastActivityAt: activityReadNow.Add(-d).UnixMilli()}
}

func liveActivityEntry(pid int, id, status string, activity *appwire.ThreadActivity) hubcore.LiveEntry {
	return hubcore.LiveEntry{Entry: rendezvous.Entry{PID: pid, SessionID: id}, SessionID: id, Status: status, Activity: activity}
}

func quietMillis(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

// Jesse's ruling: an agent waiting on subagents is never stuck. A subagent
// inside one long model call emits nothing for minutes, so the read withholds
// the quiet time outright while one runs, and while the session is not
// working at all. A session with nothing running reports how long its tree
// has been silent.
func TestActivityReadWithholdsQuietWhileASubagentRuns(t *testing.T) {
	waiting := liveActivityEntry(2, "01WAITING", appwire.ThreadStatusActive, silentFor(15*time.Minute))
	waiting.RunningSubagentIDs = []string{"child-running", "child-settled", "child-unknown"}
	waiting.RunningSubagentStates = map[string]string{"child-running": appwire.ThreadStatusActive, "child-settled": appwire.ThreadStatusIdle}
	crashed := liveActivityEntry(5, "01CRASHED", "errored", silentFor(time.Minute))
	crashed.Crashed = true
	roster := hubcore.NewRosterWithEntries(
		liveActivityEntry(1, "01ALONE", appwire.ThreadStatusActive, silentFor(15*time.Minute)),
		waiting,
		liveActivityEntry(3, "01FINISHED", appwire.ThreadStatusAwaiting, silentFor(15*time.Minute)),
		liveActivityEntry(4, "01OLDDAEMON", appwire.ThreadStatusActive, nil),
		crashed,
	)
	got, err := hubActivityRead(t.Context(), hubcore.WebConfig{Roster: roster}, nil, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	want := []appwire.SessionActivity{
		{Ref: "local:01ALONE", Minutes: activityReadMinutes, QuietForMS: quietMillis(15 * time.Minute)},
		{Ref: "local:01FINISHED", Minutes: activityReadMinutes},
		{Ref: "local:01WAITING", Minutes: activityReadMinutes, RunningSubagents: 1},
	}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", got.Sessions, want)
	}
}

// A refs filter reads only the sessions it names, spelled as their Live rows
// spell them; a malformed or oversized filter is refused before anything is read.
func TestActivityReadFiltersToTheRefsAsked(t *testing.T) {
	cleared := liveActivityEntry(1, "01CURRENT", appwire.ThreadStatusActive, silentFor(time.Minute))
	cleared.SourceID, cleared.WorkspaceRef = "local", "local:01WORKSPACE"
	roster := hubcore.NewRosterWithEntries(cleared, liveActivityEntry(2, "01OTHER", appwire.ThreadStatusActive, silentFor(time.Minute)))
	cfg := hubcore.WebConfig{Roster: roster}

	got, err := hubActivityRead(t.Context(), cfg, nil, appwire.ActivityReadParams{Refs: []string{"local:01WORKSPACE"}}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Ref != "local:01WORKSPACE" {
		t.Fatalf("sessions = %+v, want only the session whose Live row is local:01WORKSPACE", got.Sessions)
	}
	if _, err := hubActivityRead(t.Context(), cfg, nil, appwire.ActivityReadParams{Refs: []string{"not a ref"}}, activityReadNow); err == nil || !strings.Contains(err.Error(), "refs must be session refs") {
		t.Fatalf("malformed ref error = %v, want an invalid-params refusal", err)
	}
	many := make([]string, maxActivityReadRefs+1)
	for i := range many {
		many[i] = "local:01OTHER"
	}
	if _, err := hubActivityRead(t.Context(), cfg, nil, appwire.ActivityReadParams{Refs: many}, activityReadNow); err == nil {
		t.Fatal("an oversized refs filter was accepted")
	}
}

// No roster means no local sessions, and the response still carries an empty
// list, never null.
func TestActivityReadWithoutARosterIsAnEmptyList(t *testing.T) {
	got, err := hubActivityRead(t.Context(), hubcore.WebConfig{}, nil, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil || got.Sessions == nil || len(got.Sessions) != 0 {
		t.Fatalf("read = %+v (%v), want an empty, non-nil list", got, err)
	}
}
```

Add `appwire.MethodEvenerActivityRead,` after `appwire.MethodEvenerSearch,` in `TestHubRPCRegistersExpectedHandlerSet`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestActivityRead|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog' -count=1`
Expected: FAIL to compile (`hubActivityRead` undefined).

- [ ] **Step 3: Implement**

`cmd/evener-hub/app_activity.go`:

```go
package hub

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

// maxActivityReadRefs bounds an explicit refs filter: a Board reads with no
// filter, and a session screen with one ref.
const maxActivityReadRefs = 500

func registerActivityReadHandler(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerActivityRead, func(ctx context.Context, params appwire.ActivityReadParams) (appwire.ActivityReadResponse, error) {
		return hubActivityRead(ctx, cfg, sources, params, time.Now())
	})
}

// hubActivityRead answers evener/activity/read (S5): the live top-level
// sessions of this hub's roster, filtered to params.Refs when it names any.
// It never reads navigation and never changes it.
func hubActivityRead(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ActivityReadParams, now time.Time) (appwire.ActivityReadResponse, error) {
	if len(params.Refs) > maxActivityReadRefs {
		return appwire.ActivityReadResponse{}, appwire.InvalidParams(fmt.Sprintf("refs names at most %d sessions", maxActivityReadRefs))
	}
	wanted := make(map[string]struct{}, len(params.Refs))
	for _, raw := range params.Refs {
		ref, err := hubapi.ParseRef(raw)
		if err != nil {
			return appwire.ActivityReadResponse{}, appwire.InvalidParams("refs must be session refs: " + err.Error())
		}
		wanted[ref.String()] = struct{}{}
	}
	keep := func(ref string) bool {
		_, ok := wanted[ref]
		return len(wanted) == 0 || ok
	}
	sessions := []appwire.SessionActivity{}
	if cfg.Roster != nil {
		for _, entry := range cfg.Roster.List() {
			if activity, ok := localSessionActivity(entry, now); ok && keep(activity.Ref) {
				sessions = append(sessions, activity)
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Ref < sessions[j].Ref })
	return appwire.ActivityReadResponse{Sessions: sessions}, nil
}

// localSessionActivity is one live root's activity as the hub reads it now.
// Only a live, uncrashed root whose daemon reported a sample has one.
func localSessionActivity(entry hubcore.LiveEntry, now time.Time) (appwire.SessionActivity, bool) {
	if entry.Crashed || entry.SessionID == "" || entry.Activity == nil {
		return appwire.SessionActivity{}, false
	}
	activity := appwire.SessionActivity{
		Ref:              hubcore.LiveRowRef(entry),
		Minutes:          slices.Clone(entry.Activity.Minutes),
		RunningSubagents: runningSubagents(entry),
	}
	// Jesse's ruling: an agent waiting on subagents is never stuck. A subagent
	// inside one long model call emits nothing for minutes, so the tree's quiet
	// clock cannot keep that promise alone; the quiet time is withheld outright
	// while any subagent runs, and while the session is not working.
	if hubcore.NormalizeState(entry.Status) == "active" && activity.RunningSubagents == 0 {
		quiet := max(now.Sub(time.UnixMilli(entry.Activity.LastActivityAt)).Milliseconds(), 0)
		activity.QuietForMS = &quiet
	}
	return activity, true
}

// runningSubagents counts a root's listed descendants whose own status is
// active, by the rule its tree rows use (runningSubagentState in
// hubcore/tree.go): a descendant with no reported state does not count.
func runningSubagents(entry hubcore.LiveEntry) int {
	count := 0
	for _, id := range entry.RunningSubagentIDs {
		if hubcore.NormalizeState(entry.RunningSubagentStates[id]) == "active" {
			count++
		}
	}
	return count
}
```

`sources` stays unused until Task 15.5 adds the fan-out; no enabled linter flags an unused parameter (`.golangci.yml` lists revive's rules explicitly, and `unused-parameter` is not among them).

`app_rpc.go`, after `registerFavoriteHandler(server, cfg, navigation)`: `registerActivityReadHandler(server, cfg, sources)`.

`appwire/protocol.go`, after the `MethodEvenerSearch` row:

```go
	{MethodEvenerActivityRead, ActivityReadParams{}, ActivityReadResponse{}, ScopeHub, "Reads the pulse meter (seven one-minute activity counts over the whole tree), running subagents and quiet time of the hub's live top-level sessions and its attached hosts' (S5). A client polls it while a Board or session is on screen; it is never part of navigation."},
```

Run `$(go env GOROOT)/bin/gofmt -w cmd/evener-hub/app_activity.go cmd/evener-hub/app_activity_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go appwire/protocol.go`, then `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestActivityRead|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `ActivityReadParams`, `ActivityReadResponse`, `SessionActivity` and the method's entry.

- [ ] **Step 5: Commit**

```bash
git add appwire/protocol.go cmd/evener-hub/app_activity.go cmd/evener-hub/app_activity_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): evener/activity/read serves live roots' pulse meters and withholds quiet while subagents run"
```

### Task 15.4: The shared decoder and quiet rule

**Files:**
- Create: `appwire-client/typescript/sessionActivity.ts`
- Create: `appwire-client/typescript/sessionActivity.test.ts`
- Modify: `appwire-client/typescript/index.ts` (after the `./sendQueueAvailability` exports, `:359-360`)
- Modify: `appwire-client/typescript/tsconfig.build.json` (add `"sessionActivity.ts"` after `"railSessionState.ts"`, `:61`)
- Modify: `appwire-client/typescript/scripts/qualify-package.mjs` (two smoke calls after the `humanizeState` ones, `:196-198`)
- Modify: `appwire-client/typescript/README.md` (the module list)

**Interfaces:**
- Consumes: TS `SessionActivity` from `types.gen.ts` (generated in Task 15.3).
- Produces (root exports of `@evener/appwire-client`): `decodeActivityRead(value: unknown): SessionActivity[]`; `quietState(activity: SessionActivity, sinceReadMs: number): { state: QuietState; forMs: number } | null`; `type QuietState = "quiet" | "stuck"`; `QUIET_AFTER_MS = 180_000`; `STUCK_AFTER_MS = 600_000`.

- [ ] **Step 1: Write the failing tests**

`appwire-client/typescript/sessionActivity.test.ts`:

```ts
import { expect, test } from "vitest";
import { decodeActivityRead, QUIET_AFTER_MS, quietState, STUCK_AFTER_MS } from "./sessionActivity";

const minutes = [0, 0, 1, 4, 9, 2, 0];

test("decodeActivityRead keeps well-formed sessions and drops keys it does not know", () => {
  expect(
    decodeActivityRead({
      sessions: [
        { ref: "local:a", minutes, runningSubagents: 0, quietForMs: 1_000, futureKey: { nested: true } },
        { ref: "paradise-park:b", minutes, runningSubagents: 2 },
      ],
      futureTopLevel: 1,
    }),
  ).toEqual([
    { ref: "local:a", minutes, runningSubagents: 0, quietForMs: 1_000 },
    { ref: "paradise-park:b", minutes, runningSubagents: 2 },
  ]);
});

test("decodeActivityRead drops a malformed session, never the whole read", () => {
  const good = { ref: "local:a", minutes, runningSubagents: 0 };
  expect(
    decodeActivityRead({
      sessions: [
        good,
        { ref: "", minutes, runningSubagents: 0 },
        { ref: "local:b", minutes: [1, -1], runningSubagents: 0 },
        { ref: "local:c", minutes: [], runningSubagents: 0 },
        { ref: "local:d", minutes, runningSubagents: 1.5 },
        { ref: "local:e", minutes, runningSubagents: 0, quietForMs: -5 },
        "not a session",
      ],
    }),
  ).toEqual([good]);
});

test("decodeActivityRead throws on a result that is not a session list", () => {
  for (const value of [null, [], {}, { sessions: "none" }]) {
    expect(() => decodeActivityRead(value)).toThrow("activity read: invalid response");
  }
});

test("a working session reads Quiet from three minutes and May be stuck from ten", () => {
  const quietFor = (ms: number) => quietState({ ref: "local:a", minutes, runningSubagents: 0, quietForMs: ms }, 0);
  expect(quietFor(QUIET_AFTER_MS - 1)).toBeNull();
  expect(quietFor(QUIET_AFTER_MS)).toEqual({ state: "quiet", forMs: QUIET_AFTER_MS });
  expect(quietFor(STUCK_AFTER_MS - 1)).toEqual({ state: "quiet", forMs: STUCK_AFTER_MS - 1 });
  expect(quietFor(STUCK_AFTER_MS)).toEqual({ state: "stuck", forMs: STUCK_AFTER_MS });
});

test("the time since the read counts toward the quiet time", () => {
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 0, quietForMs: 2 * 60_000 }, 60_000)).toEqual({
    state: "quiet",
    forMs: QUIET_AFTER_MS,
  });
});

// Jesse's ruling: an agent waiting on subagents is never stuck. The hub sends
// no quiet time then; a session reporting a running subagent reads neither
// label even if one arrived, and a session that is not working has none.
test("a session waiting on subagents is never quiet or stuck", () => {
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 1 }, 15 * 60_000)).toBeNull();
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 1, quietForMs: 15 * 60_000 }, 0)).toBeNull();
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 0 }, 15 * 60_000)).toBeNull();
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/sessionActivity.test.ts`
Expected: FAIL: `Cannot find module './sessionActivity'`.

- [ ] **Step 3: Implement**

`appwire-client/typescript/sessionActivity.ts`:

```ts
// The pulse meter's data (spec 16.4) and the Quiet and May be stuck labels
// (spec 13.1), read from evener/activity/read (server addition S5). Both apps
// decode the read here and ask quietState which label a working session shows.
import type { SessionActivity } from "./types.gen";

/** A working session reads Quiet after three minutes without transcript motion. */
export const QUIET_AFTER_MS = 3 * 60_000;
/** A working session reads May be stuck after ten. */
export const STUCK_AFTER_MS = 10 * 60_000;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;

// One session entry, key by key as the navigation codec treats a value record:
// the keys this client knows are validated and kept, and any other key is
// dropped. A malformed entry is dropped alone, so one bad row never blanks
// every meter.
function sessionActivity(value: unknown): SessionActivity | null {
  if (!isRecord(value)) return null;
  const { ref, minutes, runningSubagents, quietForMs } = value;
  if (typeof ref !== "string" || ref === "" || ref.length > 1024) return null;
  if (!Array.isArray(minutes) || minutes.length === 0 || minutes.length > 60 || !minutes.every(count)) return null;
  if (!count(runningSubagents)) return null;
  if (quietForMs !== undefined && !count(quietForMs)) return null;
  return { ref, minutes: [...minutes], runningSubagents, ...(quietForMs === undefined ? {} : { quietForMs }) };
}

/** Decodes an evener/activity/read result. A result that is not a session
 * list throws, so a caller keeps the meters it has rather than drawing them
 * flat. */
export function decodeActivityRead(value: unknown): SessionActivity[] {
  if (!isRecord(value) || !Array.isArray(value.sessions)) throw new Error("activity read: invalid response");
  const sessions: SessionActivity[] = [];
  for (const entry of value.sessions) {
    const session = sessionActivity(entry);
    if (session) sessions.push(session);
  }
  return sessions;
}

export type QuietState = "quiet" | "stuck";

/** Whether a working session reads Quiet or May be stuck `sinceReadMs` after
 * the read that returned `activity`, and how long it has been quiet; null when
 * it reads neither. The hub withholds the quiet time while the session is not
 * working or any of its subagents runs, and so does this: an agent waiting on
 * subagents is never quiet or stuck (Jesse's ruling for S5). */
export function quietState(
  activity: SessionActivity,
  sinceReadMs: number,
): { state: QuietState; forMs: number } | null {
  if (activity.quietForMs === undefined || activity.runningSubagents > 0) return null;
  const forMs = activity.quietForMs + Math.max(0, sinceReadMs);
  if (forMs >= STUCK_AFTER_MS) return { state: "stuck", forMs };
  if (forMs >= QUIET_AFTER_MS) return { state: "quiet", forMs };
  return null;
}
```

`index.ts`, after the two `./sendQueueAvailability` lines:

```ts
export type { QuietState } from "./sessionActivity";
export { decodeActivityRead, QUIET_AFTER_MS, quietState, STUCK_AFTER_MS } from "./sessionActivity";
```

`tsconfig.build.json`: add `"sessionActivity.ts",` after `"railSessionState.ts",`.

`scripts/qualify-package.mjs`, after the three `humanizeState` asserts:

```js
assert.deepEqual(client.decodeActivityRead({ sessions: [{ ref: "local:a", minutes: [0, 1], runningSubagents: 0, quietForMs: 200000 }] }).map((session) => session.ref), ["local:a"]);
assert.equal(client.quietState({ ref: "local:a", minutes: [0], runningSubagents: 0, quietForMs: 200000 }, 0).state, "quiet");
```

`README.md`, in the module list after "the short lowercase session-state gloss a session row's second line leads with,", add: "the pulse meter's activity read decoder and the Quiet and May be stuck rule both apps' Boards apply (`decodeActivityRead`, `quietState`),".

- [ ] **Step 4: Run them to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/sessionActivity.test.ts && npx biome check --write ../../../appwire-client/typescript/sessionActivity.ts ../../../appwire-client/typescript/sessionActivity.test.ts ../../../appwire-client/typescript/index.ts && npm run typecheck`, then `make test-api-package`.
Expected: PASS; the package qualification runs the two smoke calls.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/sessionActivity.ts appwire-client/typescript/sessionActivity.test.ts appwire-client/typescript/index.ts appwire-client/typescript/tsconfig.build.json appwire-client/typescript/scripts/qualify-package.mjs appwire-client/typescript/README.md
git commit -m "feat(appwire-client): decode activity reads and apply the Quiet and May be stuck rule"
```

### Task 15.5: Attached hosts answer through a fan-out

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_source.go` (`ReadSessionActivity` after `FetchSessionImage`, `:595-601`)
- Modify: `cmd/evener-hub/app_activity.go`
- Modify: `cmd/evener-hub/app_activity_test.go`

**Interfaces:**
- Consumes: `RemoteHubSource.call`, `toRemoteRef`, `fromRemoteRefString` (`remote_hub_refs.go:27`, `:73`); `remoteHostNames` and `remoteSourceAttached` (`app_threadlist.go:343`, `:369`).
- Produces: `(*appsource.RemoteHubSource).ReadSessionActivity(ctx, appwire.ActivityReadParams) (appwire.ActivityReadResponse, error)`; `hubActivityRead` fans out.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/evener-hub/app_activity_test.go` (add `context`, `encoding/json`, `net`, `sync` and `appsource` to its imports):

```go
// newScriptedActivityHost wires an initialized AppWire client to an in-memory
// host that answers evener/activity/read with handle's result (or fails when it
// returns an error), recording every request's params. No SSH, no network.
func newScriptedActivityHost(t *testing.T, handle func(appwire.ActivityReadParams) (appwire.ActivityReadResponse, *appwire.WireError)) (*appwire.Client, func() []appwire.ActivityReadParams) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	host := appwire.NewStreamTransport(serverConn)
	var mu sync.Mutex
	var seen []appwire.ActivityReadParams
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := host.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			var result any
			var wireErr *appwire.WireError
			switch msg.Request.Method {
			case appwire.MethodInitialize:
				result = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
			case appwire.MethodEvenerActivityRead:
				var params appwire.ActivityReadParams
				if err := json.Unmarshal(msg.Request.Params, &params); err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, params)
				mu.Unlock()
				result, wireErr = handle(params)
			default:
				return
			}
			if wireErr != nil {
				if err := host.Send(ctx, appwire.ErrorMessage(msg.Request.ID, *wireErr)); err != nil {
					return
				}
				continue
			}
			data, err := json.Marshal(result)
			if err != nil {
				return
			}
			if err := host.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()
	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize scripted host: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})
	return client, func() []appwire.ActivityReadParams {
		mu.Lock()
		defer mu.Unlock()
		return append([]appwire.ActivityReadParams(nil), seen...)
	}
}

func activityHostRegistry(name string, client *appwire.Client, attached bool) *appsource.Registry {
	source := appsource.NewRemoteHubSource(name, nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) { return client, attached && host == name })
	registry := appsource.NewRegistry()
	registry.Add(source)
	return registry
}

// An attached host answers for its own sessions: its refs come back in the
// controller's namespace, a row the controller cannot address (the host's own
// host) is dropped, and a filter reaches the host in the host's namespace.
func TestActivityReadFansOutToAttachedHosts(t *testing.T) {
	quiet := int64(1_000)
	client, seen := newScriptedActivityHost(t, func(appwire.ActivityReadParams) (appwire.ActivityReadResponse, *appwire.WireError) {
		return appwire.ActivityReadResponse{Sessions: []appwire.SessionActivity{
			{Ref: "local:r1", Minutes: activityReadMinutes, QuietForMS: &quiet},
			{Ref: "nested-host:x", Minutes: activityReadMinutes},
		}}, nil
	})
	cfg := hubcore.WebConfig{
		Roster:      hubcore.NewRosterWithEntries(liveActivityEntry(1, "01LOCAL", appwire.ThreadStatusAwaiting, silentFor(time.Minute))),
		RemoteHosts: []hostreg.Host{{Name: "h1"}},
	}
	registry := activityHostRegistry("h1", client, true)
	cfg.RemoteHostClientIfAttached = func(host string) (*appwire.Client, bool) { return client, host == "h1" }

	got, err := hubActivityRead(t.Context(), cfg, registry, appwire.ActivityReadParams{}, activityReadNow)
	if err != nil {
		t.Fatalf("activity read: %v", err)
	}
	want := []appwire.SessionActivity{
		{Ref: "h1:r1", Minutes: activityReadMinutes, QuietForMS: &quiet},
		{Ref: "local:01LOCAL", Minutes: activityReadMinutes},
	}
	if !reflect.DeepEqual(got.Sessions, want) {
		t.Fatalf("sessions = %+v, want %+v", got.Sessions, want)
	}

	if _, err := hubActivityRead(t.Context(), cfg, registry, appwire.ActivityReadParams{Refs: []string{"h1:r1"}}, activityReadNow); err != nil {
		t.Fatalf("filtered read: %v", err)
	}
	if calls := seen(); len(calls) != 2 || calls[0].Refs != nil || !reflect.DeepEqual(calls[1].Refs, []string{"local:r1"}) {
		t.Fatalf("host requests = %+v, want an unfiltered read, then local:r1", calls)
	}
	// A filter naming only local sessions never calls the host.
	if _, err := hubActivityRead(t.Context(), cfg, registry, appwire.ActivityReadParams{Refs: []string{"local:01LOCAL"}}, activityReadNow); err != nil {
		t.Fatalf("local-only read: %v", err)
	}
	if calls := seen(); len(calls) != 2 {
		t.Fatalf("host requests = %d, want still 2", len(calls))
	}
}

// A host that fails contributes nothing, and a host that is not attached is
// never called; the local sessions are still read.
func TestActivityReadSkipsAFailingOrUnattachedHost(t *testing.T) {
	failing, failed := newScriptedActivityHost(t, func(appwire.ActivityReadParams) (appwire.ActivityReadResponse, *appwire.WireError) {
		wireErr := appwire.InternalError("host failed")
		return appwire.ActivityReadResponse{}, &wireErr
	})
	cfg := hubcore.WebConfig{
		Roster:      hubcore.NewRosterWithEntries(liveActivityEntry(1, "01LOCAL", appwire.ThreadStatusAwaiting, silentFor(time.Minute))),
		RemoteHosts: []hostreg.Host{{Name: "h1"}},
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			return failing, host == "h1"
		},
	}
	got, err := hubActivityRead(t.Context(), cfg, activityHostRegistry("h1", failing, true), appwire.ActivityReadParams{}, activityReadNow)
	if err != nil || len(got.Sessions) != 1 || got.Sessions[0].Ref != "local:01LOCAL" {
		t.Fatalf("read with a failing host = %+v (%v), want only the local session", got, err)
	}
	if len(failed()) != 1 {
		t.Fatalf("failing host calls = %d, want 1", len(failed()))
	}

	idle, idleCalls := newScriptedActivityHost(t, func(appwire.ActivityReadParams) (appwire.ActivityReadResponse, *appwire.WireError) {
		return appwire.ActivityReadResponse{Sessions: []appwire.SessionActivity{{Ref: "local:r1", Minutes: activityReadMinutes}}}, nil
	})
	cfg.RemoteHostClientIfAttached = func(string) (*appwire.Client, bool) { return nil, false }
	got, err = hubActivityRead(t.Context(), cfg, activityHostRegistry("h1", idle, false), appwire.ActivityReadParams{}, activityReadNow)
	if err != nil || len(got.Sessions) != 1 || len(idleCalls()) != 0 {
		t.Fatalf("read with an unattached host = %+v (%v), %d host calls; want only the local session and no call", got, err, len(idleCalls()))
	}
}
```

Add `"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"` to the imports.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestActivityRead' -count=1`
Expected: the two new tests FAIL: the read returns only the local session and never calls the host.

- [ ] **Step 3: Implement**

`remote_hub_source.go`, after `FetchSessionImage`:

```go
// ReadSessionActivity reads the host's own live sessions' pulse meters for the
// controller's evener/activity/read (S5). Refs are rewritten into the host's
// namespace on the way out and back into the controller's on the way in; a
// session the controller cannot address (one of the host's own hosts) is
// dropped rather than failing the read.
func (s *RemoteHubSource) ReadSessionActivity(ctx context.Context, params appwire.ActivityReadParams) (appwire.ActivityReadResponse, error) {
	remote := appwire.ActivityReadParams{}
	for _, raw := range params.Refs {
		ref, err := s.toRemoteRef(raw, "")
		if err != nil {
			return appwire.ActivityReadResponse{}, err
		}
		remote.Refs = append(remote.Refs, ref.String())
	}
	var out appwire.ActivityReadResponse
	if err := s.call(ctx, appwire.MethodEvenerActivityRead, remote, &out); err != nil {
		return appwire.ActivityReadResponse{}, err
	}
	sessions := make([]appwire.SessionActivity, 0, len(out.Sessions))
	for _, session := range out.Sessions {
		ref, err := s.fromRemoteRefString(session.Ref)
		if err != nil || ref == "" {
			continue
		}
		session.Ref = ref
		sessions = append(sessions, session)
	}
	return appwire.ActivityReadResponse{Sessions: sessions}, nil
}
```

`app_activity.go`: add `"sync"` to the imports, and:

```go
// remoteActivityReadBudget bounds one host's answer. A host answers from its
// own roster in memory, so anything longer is a stalled channel: that host's
// sessions are left out of this read, and a client keeps its fallback for them
// until the next poll.
const remoteActivityReadBudget = 3 * time.Second

// remoteSessionActivityReader is the source capability the read fans out to:
// an attached host hub answers for its own live sessions.
type remoteSessionActivityReader interface {
	ReadSessionActivity(ctx context.Context, params appwire.ActivityReadParams) (appwire.ActivityReadResponse, error)
}

// remoteSessionActivity asks every attached host the filter names (every host
// when it names none), in parallel, each bounded by remoteActivityReadBudget.
// A host that fails or times out contributes nothing; an unattached host is
// skipped without a call, never dialed.
func remoteSessionActivity(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, refs []string) []appwire.SessionActivity {
	if sources == nil {
		return nil
	}
	byHost := make(map[string][]string)
	for _, raw := range refs {
		if ref, err := hubapi.ParseRef(raw); err == nil {
			byHost[ref.HostID] = append(byHost[ref.HostID], ref.String())
		}
	}
	remoteHosts := remoteHostNames(cfg)
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sessions []appwire.SessionActivity
	)
	for _, source := range sources.All() {
		id := source.ID()
		reader, ok := source.(remoteSessionActivityReader)
		hostRefs, named := byHost[id]
		if !ok || (len(refs) > 0 && !named) || !remoteSourceAttached(cfg, remoteHosts, id) {
			continue
		}
		wg.Go(func() {
			hostCtx, cancel := context.WithTimeout(ctx, remoteActivityReadBudget)
			defer cancel()
			response, err := reader.ReadSessionActivity(hostCtx, appwire.ActivityReadParams{Refs: hostRefs})
			if err != nil {
				return
			}
			mu.Lock()
			sessions = append(sessions, response.Sessions...)
			mu.Unlock()
		})
	}
	wg.Wait()
	return sessions
}
```

In `hubActivityRead`, replace the roster loop with one that reads the roster only when the filter is empty or names a `local` ref, then append the remote sessions before sorting:

```go
	askedLocal := len(wanted) == 0
	for ref := range wanted {
		if strings.HasPrefix(ref, "local:") {
			askedLocal = true
		}
	}
	sessions := []appwire.SessionActivity{}
	if cfg.Roster != nil && askedLocal {
		for _, entry := range cfg.Roster.List() {
			if activity, ok := localSessionActivity(entry, now); ok && keep(activity.Ref) {
				sessions = append(sessions, activity)
			}
		}
	}
	for _, activity := range remoteSessionActivity(ctx, cfg, sources, params.Refs) {
		if keep(activity.Ref) {
			sessions = append(sessions, activity)
		}
	}
```

(add `"strings"` to the imports). Run `$(go env GOROOT)/bin/gofmt -w` on the three touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub -run 'TestActivityRead|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog' -count=1`, `go test ./cmd/evener-hub/internal/appsource -count=1`
Expected: PASS.

Gates: `go vet ./cmd/evener-hub/... ./appwire/...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`, `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/hubcore/ ./cmd/evener-hub/internal/appsource/ ./appwire/`, `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `make lint-generated`.

- [ ] **Step 5: Commit and open PR 15**

```bash
git add cmd/evener-hub/internal/appsource/remote_hub_source.go cmd/evener-hub/app_activity.go cmd/evener-hub/app_activity_test.go
git commit -m "feat(hub): activity reads fan out to attached hosts"
```

Title "feat(hub): evener/activity/read, the pulse meter and quiet time (S5b, phase 7 PR 15)". The body names the fallback it retires (a single still bar, no Quiet or May be stuck), says the quiet time is withheld while any subagent runs (Jesse's ruling, and why a quiet clock alone cannot keep it), that the read never touches navigation, and points the phone lane at the handoff section of this plan.

---

## PR 16: the daemon stamps turn ends, S4a (Tasks 16.1-16.2)

**Branch:** `git fetch origin && git switch -c claude/s4a-turn-ended-at origin/main`

**What it adds.** Every session knows when its last turn ended, keeps it in its meta, and its thread snapshot carries it as `EvenerThread.lastTurnEndedAt`. PR 17 turns it into the Board's Finished band.

### Task 16.1: The session stamps and keeps the turn end

**Files:**
- Modify: `agent/schema/snapshot.go` (`SessionMeta`: `LastTurnEndedAt` after `WorkMillis`, `:243`)
- Modify: `agent/session.go` (`Session`: `lastTurnEndedAt` after `turnStartedAt`, `:411`)
- Modify: `agent/session_state.go` (`transitionProcessingAtBoundaryLocked`, `:321-328`; the `metaWithNotes` literal, `:215-253`)
- Modify: `agent/session_init.go` (the restore literal, `:1123`)
- Create: `agent/session_turn_ended_test.go`
- Create: `agent/schema/snapshot_turn_ended_test.go`

**Interfaces:**
- Produces: `schema.SessionMeta.LastTurnEndedAt time.Time` (JSON `last_turn_ended_at,omitzero`); `Session.Meta().LastTurnEndedAt`, which Task 16.2's envelope samples through the existing `SessionMeta()` seam.

- [ ] **Step 1: Write the failing tests**

`agent/session_turn_ended_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A turn's end is stamped at the processing boundary with the session's own
// clock. The Board's Finished band measures from it (S4): unlike UpdatedAt it
// never moves on a rename or a mid-turn meta write.
func TestLastTurnEndedAt_StampedWhenATurnEnds(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}), withSteps(
		func(req llm.Request) llm.Response {
			clk.Advance(2 * time.Second)
			return finalResponse("ok")
		},
	))
	if got := sess.Meta().LastTurnEndedAt; !got.IsZero() {
		t.Fatalf("a session that has not run a turn reports %v", got)
	}
	// TRIPWIRE: scripted in-process adapter with a fake clock, no real I/O;
	// only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got, want := sess.Meta().LastTurnEndedAt, clk.Now().UTC(); !got.Equal(want) {
		t.Fatalf("LastTurnEndedAt = %v, want the boundary's %v", got, want)
	}
}

// A restored session still knows when its last turn ended, so a daemon restart
// neither turns a seen session unseen nor the reverse (S4).
func TestRestoreSeedsLastTurnEndedAt(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	meta := schema.SessionMeta{
		ID:              "01RESTOREENDEDTIME000001",
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		CreatedAt:       ended.Add(-time.Hour),
		LastTurnEndedAt: ended,
	}
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, "")
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()
	if got := sess.Meta().LastTurnEndedAt; !got.Equal(ended) {
		t.Fatalf("restored LastTurnEndedAt = %v, want %v", got, ended)
	}
}
```

`agent/schema/snapshot_turn_ended_test.go`:

```go
package schema

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// LastTurnEndedAt persists as last_turn_ended_at at millisecond precision and
// is absent until a turn has ended, so an older meta and a session that never
// ran read the same.
func TestSessionMeta_LastTurnEndedAtRoundTrip(t *testing.T) {
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	raw, err := json.Marshal(SessionMeta{ID: "01X", LastTurnEndedAt: ended})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"last_turn_ended_at":"2026-09-26T12:00:00.123Z"`) {
		t.Fatalf("meta = %s, want last_turn_ended_at", raw)
	}
	var back SessionMeta
	if err := json.Unmarshal(raw, &back); err != nil || !back.LastTurnEndedAt.Equal(ended) {
		t.Fatalf("round trip = %v (%v), want %v", back.LastTurnEndedAt, err, ended)
	}
	empty, err := json.Marshal(SessionMeta{ID: "01X"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "last_turn_ended_at") {
		t.Fatalf("a meta with no turn end carries the key: %s", empty)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd agent && go test . -run 'TestLastTurnEndedAt_StampedWhenATurnEnds|TestRestoreSeedsLastTurnEndedAt' -count=1 && go test ./schema -run 'TestSessionMeta_LastTurnEndedAtRoundTrip' -count=1`
Expected: FAIL to compile (`LastTurnEndedAt` undefined).

- [ ] **Step 3: Implement**

`agent/schema/snapshot.go`, after `WorkMillis`:

```go
	// LastTurnEndedAt is when the session's last turn ended, stamped at the
	// processing boundary every turn end passes (S4). The hub's Finished band
	// measures from it: unlike UpdatedAt it never moves on a rename or a
	// mid-turn meta write. Zero until a turn has ended, and on metas written
	// before the field existed.
	LastTurnEndedAt time.Time `json:"last_turn_ended_at,omitzero"`
```

`agent/session.go`, after `turnStartedAt`:

```go
	lastTurnEndedAt time.Time // when the last turn ended (stamped at the processing boundary); seeded from SessionMeta on restore, mapped out via Meta(). Guarded by mu, like workMillis.
```

`agent/session_state.go`, in `transitionProcessingAtBoundaryLocked`:

```go
	if s.state == SessionProcessing && !s.closingOrClosedLocked() {
		s.state = state
		turnMS = s.accumulateWorkLocked()
		s.lastTurnEndedAt = s.sclock().Now().UTC()
		transitioned = true
	}
```

and in `metaWithNotes`' literal, after `WorkMillis: s.workMillis,`: `LastTurnEndedAt: s.lastTurnEndedAt,`.

`agent/session_init.go`, in the restore literal after `workMillis: meta.WorkMillis,`: `lastTurnEndedAt: meta.LastTurnEndedAt,`.

The stamp persists with the next meta save (ruling 11); no write is added at the boundary.

Run `$(go env GOROOT)/bin/gofmt -w` on the six files.

- [ ] **Step 4: Run them to verify they pass**

Run: `cd agent && go test . -run 'TestLastTurnEndedAt|TestRestoreSeeds|TestWorkMillis|TestSessionMeta' -count=1 && go test ./schema -count=1`
Expected: PASS, the WorkMillis and golden meta tests unchanged (the golden meta leaves the new field zero, so its JSON is unchanged).

- [ ] **Step 5: Commit**

```bash
git add agent/schema/snapshot.go agent/session.go agent/session_state.go agent/session_init.go agent/session_turn_ended_test.go agent/schema/snapshot_turn_ended_test.go
git commit -m "feat(agent): a session stamps when its last turn ended and keeps it in its meta"
```

### Task 16.2: The thread snapshot carries the turn end

**Files:**
- Modify: `server/thread_envelope.go` (`threadEnvelope`'s `Name`/`Preview` comment and a new field, `:91-98`; `refreshFacets`' `facetMeta` branch, `:395-398`; `assign`'s `facetMeta` branch, `:518-521`)
- Modify: `server/appwire_runtime.go` (`appThreadWithDiagnosticsLocked`, its envelope reads `:2477-2503` and its `EvenerThread` literal `:2515-2545`)
- Modify: `appwire/types.go` (`EvenerThread.LastTurnEndedAt` after `VisionModel`)
- Create: `server/thread_envelope_turn_ended_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `SessionMeta.LastTurnEndedAt` (Task 16.1) through `ThreadEnvelopeSource.SessionMeta()`; no new `EnvelopeSampling` method.
- Produces: `appwire.EvenerThread.LastTurnEndedAt int64` (Unix ms, JSON `lastTurnEndedAt,omitempty`), on thread/read and thread/list alike; PR 17's probe reads it.

- [ ] **Step 1: Write the failing tests**

`server/thread_envelope_turn_ended_test.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// The thread snapshot carries when the last turn ended, sampled from the
// session's meta. TURN_ENDED re-samples it, because it re-samples every facet
// (S4).
func TestThreadSnapshotsCarryTheLastTurnEndedTime(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root", LastTurnEndedAt: ended}})
	listed := func() int64 {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener.LastTurnEndedAt
	}
	if got := listed(); got != ended.UnixMilli() {
		t.Fatalf("listed lastTurnEndedAt = %d, want %d", got, ended.UnixMilli())
	}
	if got := srv.appThreadReadSnapshot(appwire.ThreadReadParams{}).Thread.Evener.LastTurnEndedAt; got != ended.UnixMilli() {
		t.Fatalf("read lastTurnEndedAt = %d, want %d", got, ended.UnixMilli())
	}
	later := ended.Add(time.Minute)
	src.meta.LastTurnEndedAt = later
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root", Data: events.TurnEndedData{TurnDurationMS: 60_000}}, nil)
	if got := listed(); got != later.UnixMilli() {
		t.Fatalf("after TURN_ENDED lastTurnEndedAt = %d, want %d", got, later.UnixMilli())
	}
}

// A session that has not ended a turn carries no key at all.
func TestThreadSnapshotsOmitTheTurnEndBeforeAnyTurnEnds(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	raw, err := json.Marshal(srv.appThreadReadSnapshot(appwire.ThreadReadParams{}).Thread.Evener)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lastTurnEndedAt") {
		t.Fatalf("a session with no turn end carries lastTurnEndedAt: %s", raw)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestThreadSnapshotsCarryTheLastTurnEndedTime|TestThreadSnapshotsOmitTheTurnEndBeforeAnyTurnEnds' -count=1`
Expected: FAIL to compile (`LastTurnEndedAt` undefined on `EvenerThread`).

- [ ] **Step 3: Implement**

`appwire/types.go`, after `EvenerThread.VisionModel`:

```go
	// LastTurnEndedAt is when the session's last turn ended, in Unix
	// milliseconds (S4); absent before any turn has ended and from an older
	// daemon. The hub compares it with its seen-through marker to tell a
	// Finished session from an Idle one. Snapshot-only: no notification
	// carries it.
	LastTurnEndedAt int64 `json:"lastTurnEndedAt,omitempty"`
```

`server/thread_envelope.go`, replace the comment and fields

```go
	// Name and Preview are the only two things appThread reads out of
	// schema.SessionMeta. Storing the two strings rather than the whole struct is
	// deliberate: SessionMeta has roughly a dozen other fields (turn counts,
	// pinned notes, worktree paths) that change constantly and silently, and
	// storing them would create a dozen values this envelope claims to keep
	// current and does not.
	Name    string
	Preview string
```

with

```go
	// Name, Preview and LastTurnEndedAt are the only things appThread reads out
	// of schema.SessionMeta. Storing them rather than the whole struct is
	// deliberate: SessionMeta has roughly a dozen other fields (turn counts,
	// pinned notes, worktree paths) that change constantly and silently, and
	// storing them would create a dozen values this envelope claims to keep
	// current and does not. LastTurnEndedAt (Unix ms, 0 before any turn has
	// ended) is current by construction: only a turn ending moves it, and
	// TURN_ENDED re-samples every facet, facetMeta included.
	Name            string
	Preview         string
	LastTurnEndedAt int64
```

In `refreshFacets`' `facets&facetMeta != 0` branch, after `next.Preview = ...`:

```go
			if !meta.LastTurnEndedAt.IsZero() {
				next.LastTurnEndedAt = meta.LastTurnEndedAt.UnixMilli()
			}
```

In `assign`'s `facetMeta` branch, after `e.Preview = next.Preview`: `e.LastTurnEndedAt = next.LastTurnEndedAt`.

`server/appwire_runtime.go`, in `appThreadWithDiagnosticsLocked`: `lastTurnEndedAt := envelope.LastTurnEndedAt` beside the other envelope reads, and `LastTurnEndedAt: lastTurnEndedAt,` after `VisionModel: visionModel,` in the `EvenerThread` literal.

Run `$(go env GOROOT)/bin/gofmt -w server/thread_envelope.go server/appwire_runtime.go appwire/types.go server/thread_envelope_turn_ended_test.go`, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./server -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `lastTurnEndedAt?: number` on `EvenerThread`.

Gates, in the root module and in `agent/`: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`; `golangci-lint run ./agent/ ./agent/schema/ ./appwire/`, `golangci-lint run --config .golangci-appwire.yml ./server/...`; `make lint-generated`.

- [ ] **Step 5: Commit and open PR 16**

```bash
git add server/thread_envelope.go server/appwire_runtime.go appwire/types.go server/thread_envelope_turn_ended_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(server): thread snapshots carry when the last turn ended"
```

Title "feat(agent): the daemon stamps when each turn ends (S4a, phase 7 PR 16)". The body says why `updated_at` cannot serve (it moves on renames and every model round), that the stamp persists with the next meta save, and that PR 17 consumes it.

---

## PR 17: the seen marker, on rows and on the web, S4b (Tasks 17.1-17.6)

**Branch:** `git fetch origin && git switch -c claude/s4b-seen-marker origin/main`. PR 16 is on main, and a TestFlight build containing #2473 exists.

**What it adds.** The hub stores a seen-through marker per session, serves `evener/session/seen/set`, and puts `turn_ended_at` and `unseen` on live rows; live rows stop folding into clusters; a remote host's rows carry their turn end; the web marks a session seen when its pane opens. The phone's fallback (a device-local marker compared against `updated_at`, phase 2 ruling 4) retires row by row as the handoff section says.

### Task 17.1: The seen-marker store

**Files:**
- Create: `cmd/evener-hub/internal/hubcore/session_seen.go`
- Create: `cmd/evener-hub/internal/hubcore/session_seen_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/session_order.go` (`UnixMilliTime` and `UnixMilliseconds` after `UnixSeconds`, `:114-120`)
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register the nine scenarios)

**Interfaces:**
- Consumes: `sqliteDSN` (`sqlite_dsn.go:20`), `ensureIndexSchema` (`index_schema.go:45`), `SessionPinKey` and `NormalizeDecisionSource` (`pin_section.go:56`, `archive.go:33`).
- Produces:
  - `NewSessionSeenStore(dbPath string) *SessionSeenStore` with `SetOnChange(func())`, `Snapshot() (SessionSeenSnapshot, error)`, `MarkSeen(source, sessionID string, through time.Time) (bool, error)`, `MarkUnread(source, sessionID string) (bool, error)`, `Delete(source, sessionID string) (bool, error)`.
  - `SessionSeenSnapshot{Epoch time.Time; Records map[ArchiveKey]SessionSeenRecord}` with `Unseen(key ArchiveKey, turnEndedAt time.Time) bool` and `Clone() SessionSeenSnapshot`; `SessionSeenRecord{SeenThrough time.Time; Unread bool}`.
  - `UnixMilliTime(ms int64) time.Time` and `UnixMilliseconds(t time.Time) int64`.

- [ ] **Step 1: Write the failing scenarios**

`cmd/evener-hub/internal/hubcore/session_seen_test.go`:

```go
package hubcore

import (
	"path/filepath"
	"testing"
	"time"
)

var seenTestNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// newSeenTestStore opens a store at path whose clock reads *now.
func newSeenTestStore(path string, now *time.Time) *SessionSeenStore {
	store := NewSessionSeenStore(path)
	store.now = func() time.Time { return *now }
	return store
}

// fuzzScenarioSessionSeenStore_EpochIsSetOnceAndSurvivesReopen: the epoch is
// the store's first read and never moves after, so the first Board after an
// upgrade counts every earlier turn as seen (S4 ruling 13).
func fuzzScenarioSessionSeenStore_EpochIsSetOnceAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	now := seenTestNow
	first, err := newSeenTestStore(path, &now).Snapshot()
	if err != nil || !first.Epoch.Equal(seenTestNow) {
		t.Fatalf("first epoch = %v (%v), want %v", first.Epoch, err, seenTestNow)
	}
	now = seenTestNow.Add(time.Hour)
	again, err := newSeenTestStore(path, &now).Snapshot()
	if err != nil || !again.Epoch.Equal(seenTestNow) {
		t.Fatalf("reopened epoch = %v (%v), want still %v", again.Epoch, err, seenTestNow)
	}
}

// fuzzScenarioSessionSeenStore_MarkSeenMovesForwardOnly: a mark records the
// turn-ended time a client showed; an older one, from a device showing a stale
// Board, changes nothing, and "local" names the controller's own sessions.
func fuzzScenarioSessionSeenStore_MarkSeenMovesForwardOnly(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	turn := seenTestNow.Add(10 * time.Minute)
	for _, step := range []struct {
		source  string
		through time.Time
		changed bool
		want    time.Time
	}{
		{"", turn, true, turn},
		{"local", turn, false, turn},
		{"", turn.Add(-time.Minute), false, turn},
		{"", turn.Add(time.Millisecond), true, turn.Add(time.Millisecond)},
	} {
		changed, err := store.MarkSeen(step.source, "01A", step.through)
		if err != nil || changed != step.changed {
			t.Fatalf("MarkSeen(%q, %v) = %v, %v; want changed %v", step.source, step.through, changed, err, step.changed)
		}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if got := snapshot.Records[SessionPinKey("", "01A")].SeenThrough; !got.Equal(step.want) {
			t.Fatalf("after MarkSeen(%v): seen through %v, want %v", step.through, got, step.want)
		}
	}
}

// fuzzScenarioSessionSeenStore_UnreadOutranksSeenUntilTheNextMark: "Mark as
// unread" makes a seen session unseen and keeps its seen-through; the next
// mark (opening it again) clears it.
func fuzzScenarioSessionSeenStore_UnreadOutranksSeenUntilTheNextMark(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	key := SessionPinKey("", "01A")
	turn := seenTestNow.Add(10 * time.Minute)
	if _, err := store.MarkSeen("", "01A", turn); err != nil {
		t.Fatal(err)
	}
	for i, wantChanged := range []bool{true, false} {
		changed, err := store.MarkUnread("", "01A")
		if err != nil || changed != wantChanged {
			t.Fatalf("MarkUnread #%d = %v, %v; want changed %v", i+1, changed, err, wantChanged)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if record := snapshot.Records[key]; !record.Unread || !record.SeenThrough.Equal(turn) || !snapshot.Unseen(key, turn) {
		t.Fatalf("after MarkUnread: record %+v, unseen %v; want unread and seen through %v", record, snapshot.Unseen(key, turn), turn)
	}
	if changed, err := store.MarkSeen("", "01A", turn); err != nil || !changed {
		t.Fatalf("marking the same turn seen again = %v, %v; want it to clear unread", changed, err)
	}
	if snapshot, err = store.Snapshot(); err != nil || snapshot.Unseen(key, turn) {
		t.Fatalf("after MarkSeen the session still reads unseen (%v)", err)
	}
}

// fuzzScenarioSessionSeenStore_SourcesKeepSeparateMarks: two hosts' sessions
// that share a bare ID keep separate markers.
func fuzzScenarioSessionSeenStore_SourcesKeepSeparateMarks(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	local, remote := seenTestNow.Add(time.Minute), seenTestNow.Add(2*time.Minute)
	if _, err := store.MarkSeen("", "01A", local); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkSeen("paradise-park", "01A", remote); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Records[SessionPinKey("", "01A")].SeenThrough; !got.Equal(local) {
		t.Fatalf("local mark = %v, want %v", got, local)
	}
	if got := snapshot.Records[SessionPinKey("paradise-park", "01A")].SeenThrough; !got.Equal(remote) {
		t.Fatalf("remote mark = %v, want %v", got, remote)
	}
}

// fuzzScenarioSessionSeenStore_OnChangeFiresOnlyWhenAMarkChanges: an unchanged
// mark must not invalidate navigation.
func fuzzScenarioSessionSeenStore_OnChangeFiresOnlyWhenAMarkChanges(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	fired := 0
	store.SetOnChange(func() { fired++ })
	for range 2 {
		if _, err := store.MarkSeen("", "01A", seenTestNow.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := store.Delete("", "01A"); err != nil {
			t.Fatal(err)
		}
	}
	if fired != 2 {
		t.Fatalf("onChange fired %d times, want 2: one mark, one delete", fired)
	}
	if snapshot, err := store.Snapshot(); err != nil || len(snapshot.Records) != 0 {
		t.Fatalf("after Delete: records %+v (%v), want none", snapshot.Records, err)
	}
}

// fuzzScenarioSessionSeenStore_WithoutADatabaseIsNoStore: an unconfigured
// store writes nothing and reads as no store, so nothing reads unseen.
func fuzzScenarioSessionSeenStore_WithoutADatabaseIsNoStore(t *testing.T) {
	store := NewSessionSeenStore("")
	if changed, err := store.MarkSeen("", "01A", seenTestNow); changed || err != nil {
		t.Fatalf("MarkSeen with no database = %v, %v", changed, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil || !snapshot.Epoch.IsZero() || snapshot.Unseen(SessionPinKey("", "01A"), seenTestNow) {
		t.Fatalf("snapshot with no database = %+v (%v), want no store", snapshot, err)
	}
}

// fuzzScenarioSessionSeenSnapshot_Unseen: the rule the Board's Finished band
// reads (S4).
func fuzzScenarioSessionSeenSnapshot_Unseen(t *testing.T) {
	epoch := seenTestNow
	mark := epoch.Add(10 * time.Minute)
	snapshot := SessionSeenSnapshot{Epoch: epoch, Records: map[ArchiveKey]SessionSeenRecord{
		SessionPinKey("", "seen"):   {SeenThrough: mark},
		SessionPinKey("", "unread"): {SeenThrough: mark, Unread: true},
	}}
	for _, tc := range []struct {
		name  string
		id    string
		ended time.Time
		want  bool
	}{
		{"a turn that ended exactly at its mark", "seen", mark, false},
		{"a turn that ended a millisecond after its mark", "seen", mark.Add(time.Millisecond), true},
		{"a turn that ended before its mark", "seen", mark.Add(-time.Minute), false},
		{"an unmarked turn that ended before the epoch", "unmarked", epoch.Add(-time.Minute), false},
		{"an unmarked turn that ended after the epoch", "unmarked", epoch.Add(time.Minute), true},
		{"an unread session", "unread", mark.Add(-time.Minute), true},
		{"a row with no turn-ended time", "unread", time.Time{}, false},
	} {
		if got := snapshot.Unseen(SessionPinKey("", tc.id), tc.ended); got != tc.want {
			t.Errorf("%s: unseen = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (SessionSeenSnapshot{}).Unseen(SessionPinKey("", "unmarked"), epoch.Add(time.Hour)) {
		t.Error("with no store, a session read unseen")
	}
}

// fuzzScenarioSessionSeenSnapshot_CloneOwnsItsRecords: a navigation build owns
// the records it captured.
func fuzzScenarioSessionSeenSnapshot_CloneOwnsItsRecords(t *testing.T) {
	snapshot := SessionSeenSnapshot{Epoch: seenTestNow, Records: map[ArchiveKey]SessionSeenRecord{SessionPinKey("", "01A"): {Unread: true}}}
	clone := snapshot.Clone()
	delete(clone.Records, SessionPinKey("", "01A"))
	if len(snapshot.Records) != 1 {
		t.Fatal("Clone aliased the records")
	}
}

// fuzzScenarioUnixMilliTime_RoundTripsTheWireStamp: the daemon sends turn ends
// in milliseconds, and a time built from one round-trips exactly.
func fuzzScenarioUnixMilliTime_RoundTripsTheWireStamp(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	if got := UnixMilliTime(at.UnixMilli()); !got.Equal(at) || got.Location() != time.UTC {
		t.Fatalf("UnixMilliTime = %v, want %v in UTC", got, at)
	}
	if !UnixMilliTime(0).IsZero() || !UnixMilliTime(-1).IsZero() {
		t.Fatal("a zero or negative stamp is not the zero time")
	}
	if UnixMilliseconds(time.Time{}) != 0 || UnixMilliseconds(at) != at.UnixMilli() {
		t.Fatal("UnixMilliseconds does not invert UnixMilliTime")
	}
}
```

Register the nine in `FuzzHubcoreScenarios`, in alphabetical order.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`SessionSeenStore` undefined).

- [ ] **Step 3: Implement**

`session_order.go`, after `UnixSeconds`:

```go
// UnixMilliTime is UnixTime for a Unix-millisecond wire value, such as a
// daemon's lastTurnEndedAt.
func UnixMilliTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// UnixMilliseconds is UnixSeconds in milliseconds.
func UnixMilliseconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
```

`cmd/evener-hub/internal/hubcore/session_seen.go`:

```go
package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// SessionSeenStore persists the hub's per-session "seen through" marker (spec
// 18, S4) in index.db beside the archive, favorite and pin stores, so the phone
// and the web agree on which finished turns are still unseen (spec 7.1: "A
// blue dot marks the ones you haven't opened since"). The hub has no user
// identity, so the marker is per hub, which on a personal hub is per person.
// Rows are source-qualified like session pins (SessionPinKey): two hosts'
// sessions that share a bare ID keep separate markers.
type SessionSeenStore struct {
	dbPath   string
	fs       afero.Fs
	openDB   func(driverName, dataSourceName string) (*sql.DB, error)
	now      func() time.Time
	onChange func()
}

// NewSessionSeenStore returns a store backed by the SQLite file at dbPath. An
// empty path is a store that reads as no store and writes nothing.
func NewSessionSeenStore(dbPath string) *SessionSeenStore {
	return &SessionSeenStore{dbPath: dbPath, fs: afero.NewOsFs(), openDB: sql.Open, now: time.Now}
}

// SetOnChange registers a callback fired after a write that changed a marker.
func (s *SessionSeenStore) SetOnChange(fn func()) { s.onChange = fn }

// SessionSeenRecord is one session's stored marker. SeenThrough is the newest
// turn-ended time a client showed when it marked the session seen; Unread is
// an explicit "Mark as unread", which outranks it until the next mark.
type SessionSeenRecord struct {
	SeenThrough time.Time
	Unread      bool
}

// SessionSeenSnapshot is the store's contents for one navigation build.
type SessionSeenSnapshot struct {
	// Epoch is when the store first opened. A turn that ended before it counts
	// as seen, so the first build after an upgrade does not report every live
	// session unseen. Zero means no store, and then nothing is unseen.
	Epoch time.Time
	// Records are keyed by SessionPinKey(source, session ID).
	Records map[ArchiveKey]SessionSeenRecord
}

// Unseen reports whether a live session finished a turn nobody has seen since:
// it was marked unread, or its last turn ended after both its seen-through
// mark and the store's epoch. A session with no turn-ended time is never
// unseen here: its row carries no turn_ended_at, and a client keeps its own
// fallback for such a row.
func (s SessionSeenSnapshot) Unseen(key ArchiveKey, turnEndedAt time.Time) bool {
	if s.Epoch.IsZero() || turnEndedAt.IsZero() {
		return false
	}
	record := s.Records[key]
	if record.Unread {
		return true
	}
	through := s.Epoch
	if record.SeenThrough.After(through) {
		through = record.SeenThrough
	}
	return turnEndedAt.After(through)
}

// Clone returns a snapshot whose records the caller owns.
func (s SessionSeenSnapshot) Clone() SessionSeenSnapshot {
	s.Records = maps.Clone(s.Records)
	return s
}

const createSessionSeenTable = `
CREATE TABLE IF NOT EXISTS session_seen (
  source       TEXT    NOT NULL DEFAULT '',
  session_id   TEXT    NOT NULL,
  seen_through INTEGER NOT NULL DEFAULT 0,
  unread       INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (source, session_id)
)`

const createSessionSeenEpochTable = `
CREATE TABLE IF NOT EXISTS session_seen_epoch (
  id    INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
  epoch INTEGER NOT NULL
)`

func (s *SessionSeenStore) open() (*sql.DB, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := s.openDB("sqlite", sqliteDSN(s.dbPath))
	if err != nil {
		return nil, err
	}
	for _, stmt := range []string{createSessionSeenTable, createSessionSeenEpochTable} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err := ensureIndexSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Snapshot reads the epoch, recording it on the store's first read, and every
// record.
func (s *SessionSeenStore) Snapshot() (SessionSeenSnapshot, error) {
	if s == nil || s.dbPath == "" {
		return SessionSeenSnapshot{}, nil
	}
	db, err := s.open()
	if err != nil {
		return SessionSeenSnapshot{}, err
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	epoch, err := s.epoch(ctx, db)
	if err != nil {
		return SessionSeenSnapshot{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT source, session_id, seen_through, unread FROM session_seen`)
	if err != nil {
		return SessionSeenSnapshot{}, err
	}
	defer func() { _ = rows.Close() }()
	records := make(map[ArchiveKey]SessionSeenRecord)
	for rows.Next() {
		var source, sessionID string
		var through int64
		var unread int
		if err := rows.Scan(&source, &sessionID, &through, &unread); err != nil {
			return SessionSeenSnapshot{}, err
		}
		records[SessionPinKey(source, sessionID)] = SessionSeenRecord{SeenThrough: UnixMilliTime(through), Unread: unread != 0}
	}
	return SessionSeenSnapshot{Epoch: epoch, Records: records}, rows.Err()
}

// epoch reads the store's epoch, recording now as the epoch on the first read.
// INSERT OR IGNORE settles two first reads racing: the loser's value is
// ignored, and both read back the winner's.
func (s *SessionSeenStore) epoch(ctx context.Context, db *sql.DB) (time.Time, error) {
	const read = `SELECT epoch FROM session_seen_epoch WHERE id = 1`
	var epoch int64
	err := db.QueryRowContext(ctx, read).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO session_seen_epoch (id, epoch) VALUES (1, ?)`, s.now().UnixMilli()); err != nil {
			return time.Time{}, err
		}
		err = db.QueryRowContext(ctx, read).Scan(&epoch)
	}
	if err != nil {
		return time.Time{}, err
	}
	return UnixMilliTime(epoch), nil
}

// MarkSeen records that a client showed the session's last turn ending at
// through: the row's turn_ended_at, a hub timestamp, never the client's clock.
// The mark only moves forward, so a device showing an older Board cannot
// un-see a turn another device already marked, and it clears an explicit
// unread. It reports whether anything changed, and fires onChange only then.
func (s *SessionSeenStore) MarkSeen(source, sessionID string, through time.Time) (bool, error) {
	return s.write(`
INSERT INTO session_seen (source, session_id, seen_through, unread, updated_at) VALUES (?, ?, ?, 0, ?)
ON CONFLICT(source, session_id) DO UPDATE SET
  seen_through = max(session_seen.seen_through, excluded.seen_through),
  unread = 0,
  updated_at = excluded.updated_at
WHERE session_seen.seen_through < excluded.seen_through OR session_seen.unread != 0`,
		NormalizeDecisionSource(source), sessionID, through.UnixMilli(), s.now().Unix())
}

// MarkUnread records an explicit "Mark as unread" (spec 7.3): the session reads
// unseen until it is next marked seen.
func (s *SessionSeenStore) MarkUnread(source, sessionID string) (bool, error) {
	return s.write(`
INSERT INTO session_seen (source, session_id, seen_through, unread, updated_at) VALUES (?, ?, 0, 1, ?)
ON CONFLICT(source, session_id) DO UPDATE SET unread = 1, updated_at = excluded.updated_at
WHERE session_seen.unread = 0`,
		NormalizeDecisionSource(source), sessionID, s.now().Unix())
}

// Delete forgets one session's marker, for session deletion.
func (s *SessionSeenStore) Delete(source, sessionID string) (bool, error) {
	return s.write(`DELETE FROM session_seen WHERE source = ? AND session_id = ?`, NormalizeDecisionSource(source), sessionID)
}

// write runs one statement and reports whether it changed a row. A single
// statement is atomic, and sqliteDSN's busy_timeout waits out a concurrent
// writer, so no retry loop is needed.
func (s *SessionSeenStore) write(query string, args ...any) (bool, error) {
	if s == nil || s.dbPath == "" {
		return false, nil
	}
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	result, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if s.onChange != nil {
		s.onChange()
	}
	return true, nil
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the three touched Go files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS, no unused scenario. If an upsert with a `WHERE` that matches nothing reported a changed row, `OnChangeFiresOnlyWhenAMarkChanges` would fail here: SQLite counts only rows the update actually changed.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/session_seen.go cmd/evener-hub/internal/hubcore/session_seen_test.go cmd/evener-hub/internal/hubcore/session_order.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): a store for per-session seen-through markers"
```

### Task 17.2: Live entries and tree rows carry the turn end; live rows never fold

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal, `:148-167`)
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry` `:28-87`; `ProbeResult` `:90-122`; `liveEntryFromProbe` `:1309`; the end of `rosterFingerprint`'s loop, after `writeTaskFingerprint` `:532`)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode` `:425-473`; a `turnEndedAtFor` closure after `tasksFor` `:1035-1037`; `buildNode`'s `parentDead` clamp `:1198-1205` and literal `:1213-1231`; the live-only leaf `:1458-1474`; the NeedsYou node `:1563-1574`; `clusterable` `:1838-1848`)
- Modify: `cmd/evener-hub/internal/hubcore/prober_wire_test.go` (`wireProbeEnvelopeSource` gains a `meta` field)
- Create: `cmd/evener-hub/internal/hubcore/turn_ended_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go`

**Interfaces:**
- Consumes: `EvenerThread.LastTurnEndedAt` (PR 16); `UnixMilliTime`, `UnixMilliseconds` (Task 17.1).
- Produces: `ProbeResult.LastTurnEndedAt`, `LiveEntry.LastTurnEndedAt` and `TreeNode.TurnEndedAt`, all `time.Time` values.

- [ ] **Step 1: Write the failing scenarios**

In `prober_wire_test.go`, add `meta schema.SessionMeta` to `wireProbeEnvelopeSource` and change its `SessionMeta()` to `return s.meta`.

`cmd/evener-hub/internal/hubcore/turn_ended_test.go`:

```go
package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// fuzzScenarioStatusProber_KeepsTheLastTurnEndedTime: the probe keeps the
// listed root's turn end, which decides Finished versus Idle (S4).
func fuzzScenarioStatusProber_KeepsTheLastTurnEndedTime(t *testing.T) {
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_turn_ended",
		state:     appwire.ThreadStatusAwaiting,
		source:    wireProbeEnvelopeSource{meta: schema.SessionMeta{ID: "th_turn_ended", LastTurnEndedAt: ended}},
	})
	if got := prober.Probe(entry); !got.OK || !got.LastTurnEndedAt.Equal(ended) {
		t.Fatalf("probe = %+v, want LastTurnEndedAt %v", got, ended)
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheTurnEndMoves: a turn that
// starts and ends between two probes leaves Status awaiting on both and moves
// only the turn end; navigation must still invalidate, or the row never turns
// Finished (S4).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheTurnEndMoves(t *testing.T) {
	ended := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	entry := func(at time.Time) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusAwaiting, LastTurnEndedAt: at}}
	}
	if rosterFingerprint(entry(ended)) == rosterFingerprint(entry(ended.Add(4*time.Second))) {
		t.Fatal("the roster fingerprint held when a turn ended between two probes")
	}
}

// fuzzScenarioBuildTree_EveryRowCarriesTheTurnEndedTime: a live session's
// Live, project and NeedsYou rows and a meta-less live leaf carry its turn end
// from one closure; an ended session carries none.
func fuzzScenarioBuildTree_EveryRowCarriesTheTurnEndedTime(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting, LastTurnEndedAt: ended},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusAwaiting, LastTurnEndedAt: ended},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || !liveRow.TurnEndedAt.Equal(ended) || !projectRow.TurnEndedAt.Equal(ended) {
		t.Fatalf("Live row %v (%v), project row %v (%v): both must carry %v", liveRow.TurnEndedAt, inLive, projectRow.TurnEndedAt, inProject, ended)
	}
	needsYou := false
	for _, row := range tree.NeedsYou {
		if row.ID == "01LIVE" {
			needsYou = row.TurnEndedAt.Equal(ended)
		}
	}
	if !needsYou {
		t.Fatalf("NeedsYou = %+v, want the awaiting session carrying %v", tree.NeedsYou, ended)
	}
	leaf := false
	for _, row := range tree.Live {
		if row.ID == "01NOMETA" {
			leaf = row.TurnEndedAt.Equal(ended)
		}
	}
	if !leaf {
		t.Fatalf("Live = %+v, want the meta-less leaf carrying %v", tree.Live, ended)
	}
	_, _, endedRow, found := liveAndProjectRowsFor(tree, "01ENDED")
	if !found || !endedRow.TurnEndedAt.IsZero() {
		t.Fatalf("ended session's row = %+v (found %v), want no turn end", endedRow, found)
	}
}

// fuzzScenarioBuildTree_DoesNotClusterLiveIdleRepeatedTitles: live sessions
// never fold, idle ones included, so a live session that finished a turn you
// have not seen keeps its own row (S4 ruling 16).
func fuzzScenarioBuildTree_DoesNotClusterLiveIdleRepeatedTitles(t *testing.T) {
	now := time.Now()
	var metas []schema.SessionMeta
	var live []LiveEntry
	for i := range 3 {
		id := "01IDLE" + string(rune('A'+i))
		metas = append(metas, schema.SessionMeta{ID: id, Name: "describe this image", UpdatedAt: now.Add(-time.Duration(i) * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener-docs"}})
		live = append(live, LiveEntry{PID: i + 1, SessionID: id, Status: appwire.ThreadStatusIdle})
	}
	sessions := allSessions(projectByName(t, buildTree(metas, live), "evener-docs"))
	for _, s := range sessions {
		if s.Kind == "cluster" {
			t.Fatalf("live idle sessions folded into cluster %q", s.Title)
		}
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3 unfolded rows", len(sessions))
	}
}
```

Register the four in `FuzzHubcoreScenarios`, in alphabetical order.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`LastTurnEndedAt` undefined on `ProbeResult`); once the fields exist, the cluster scenario fails until `clusterable` changes.

- [ ] **Step 3: Implement**

`roster.go`, at the end of `ProbeResult` (after `Tasks`, and after `Activity` if PR 15 is on main):

```go
	// LastTurnEndedAt is when the listed root's last turn ended (S4); zero from
	// a daemon that predates it, or before any turn has ended.
	LastTurnEndedAt time.Time
```

and in `Probe`'s literal (`prober.go`), after `Tasks: root.Evener.Tasks,` (and `Activity`): `LastTurnEndedAt: UnixMilliTime(root.Evener.LastTurnEndedAt),`.

`roster.go`, at the end of `LiveEntry` (after `Tasks`, and after `Activity` if PR 15 is on main):

```go
	// LastTurnEndedAt is when the session's last turn ended, stamped by its
	// daemon at the turn boundary (S4). It decides Finished versus Idle against
	// the hub's seen marker, so rosterFingerprint hashes it: a turn that starts
	// and ends between two probes leaves Status unchanged and moves only this.
	LastTurnEndedAt time.Time
```

`liveEntryFromProbe`: `LastTurnEndedAt: result.LastTurnEndedAt,`. In `rosterFingerprint`, at the end of the loop body, after `writeTaskFingerprint(h, bySess[id].Tasks)`:

```go
		_, _ = h.Write([]byte{0})
		// A turn that starts and ends between two probes leaves Status where it
		// was and moves only this, and it turns the row Finished (S4).
		_, _ = h.Write([]byte(strconv.FormatInt(UnixMilliseconds(bySess[id].LastTurnEndedAt), 10)))
```

`tree.go`, in `TreeNode` after `Tasks`:

```go
	// TurnEndedAt is when a live session's last turn ended (LiveEntry.
	// LastTurnEndedAt, S4). Every builder sets it from one closure so a
	// session's rows agree; an ended session has none.
	TurnEndedAt time.Time
```

After `tasksFor`:

```go
	// turnEndedAtFor resolves a live session's last turn end from the same live
	// map stateFor reads, so its Live, project and NeedsYou rows agree (S4).
	turnEndedAtFor := func(id string) time.Time {
		return liveMap[id].LastTurnEndedAt
	}
```

`buildNode`: after `approvalPending := approvalPendingFor(m.ID)` add `turnEndedAt := turnEndedAtFor(m.ID)`, clear it in the `parentDead` branch with `turnEndedAt = time.Time{}` (a row clamped to ended carries no turn end, exactly as it carries no ask or approval), and set `TurnEndedAt: turnEndedAt,` after `Tasks: tasksFor(m.ID),` in the literal. The live-only leaf: `TurnEndedAt: turnEndedAtFor(le.SessionID),` after its `Tasks`. The NeedsYou node: `TurnEndedAt: le.LastTurnEndedAt,` after its `Tasks`.

`clusterable`, with its comment:

```go
// clusterable reports whether a session row may be folded into a repeated-title
// cluster: a plain ended session with no children, jobs or watches of its own.
// A live session never folds, idle included: clusterRepeatedTitles keeps live
// signal out of a fold, and a live session that finished a turn nobody has
// seen carries the Board's unseen mark (S4).
func clusterable(n TreeNode) bool {
	if n.Kind != "session" {
		return false
	}
	if len(n.Children) > 0 || len(n.RunningJobs) > 0 || len(n.CompletedJobs) > 0 || len(n.Watches) > 0 {
		return false
	}
	return n.State == "ended"
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$|^TestBuildTreeDoesNotClusterSessionWithJobs$' -count=1`, `go test ./cmd/evener-hub -run 'Cluster' -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS, the existing cluster scenarios and `TestNavigationCatalogGraphMergesCollidingClusterRows` unchanged (their repeated runs are ended).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/prober_wire_test.go cmd/evener-hub/internal/hubcore/turn_ended_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries and rows carry when the last turn ended; live rows never fold"
```

### Task 17.3: Rows carry `turn_ended_at` and `unseen`

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationSessionSummary` `:239-294`, after `UpdatedAt` `:268`)
- Modify: `cmd/evener-hub/internal/hubcore/config.go` (`WebConfig`, after `PinSections` `:85`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`navigationBuildInputs` `:94-114`; `cloneNavigationInputsContext` `:213`; `cloneNavigationInputs` `:258`; `projectShallow` `:1701`; an `unseen` method after `pinSectionIDFor` `:2022-2028`; `cloneNavigationSummary` `:2030`)
- Modify: `cmd/evener-hub/navigation_service.go` (`Capture` `:1328-1368`)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`SESSION_KEYS` `:163-185`; `sessionValue` `:293-325`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json` (the session record, after `updated_at`)
- Create: `cmd/evener-hub/navigation_seen_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `hubcore.SessionSeenStore`, `SessionSeenSnapshot`, `SessionSeenRecord`, `UnixMilliTime` (Task 17.1); `TreeNode.TurnEndedAt` (Task 17.2).
- Produces: `hubapi.NavigationSessionSummary.TurnEndedAt *time.Time` (`turn_ended_at,omitempty`) and `.Unseen bool` (`unseen,omitempty`); `navigationBuildInputs.SessionSeen hubcore.SessionSeenSnapshot`; `hubcore.WebConfig.SessionSeen *hubcore.SessionSeenStore`; TypeScript `NavigationSessionSummary.turn_ended_at?: string` and `.unseen?: boolean`.

No hub schema bound is needed: a time pointer and a bool have no size, exactly like `updated_at` and `favorite`, and the codec validates `turn_ended_at` as a strict RFC3339 timestamp.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_seen_test.go`:

```go
package hub

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// seenLiveRows projects the Live section for one idle live session per entry of
// turnEnds against seen, keyed by session ID.
func seenLiveRows(t *testing.T, now time.Time, turnEnds map[string]time.Time, seen hubcore.SessionSeenSnapshot) map[string]hubapi.NavigationSessionSummary {
	t.Helper()
	var metas []schema.SessionMeta
	var live []hubcore.LiveEntry
	for id, ended := range turnEnds {
		metas = append(metas, schema.SessionMeta{ID: id, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}})
		live = append(live, hubcore.LiveEntry{PID: len(live) + 1, SessionID: id, Status: appwire.ThreadStatusIdle, LastTurnEndedAt: ended})
	}
	tree := hubcore.BuildTreeAt(metas, live, nil, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree, SessionSeen: seen})
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]hubapi.NavigationSessionSummary)
	for _, row := range projection.LivePage(0, 0).Sessions {
		rows[row.SessionID] = row
	}
	return rows
}

// A live row carries when its last turn ended and, against the hub's seen
// marker and epoch, whether that turn is unseen (S4). The phone reads unseen
// as Finished and its absence as Idle, and echoes turn_ended_at back as
// seenThrough, so the millisecond round trip through the wire must be exact.
func TestNavigationRowsCarryTurnEndedAtAndUnseen(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	epoch := now.Add(-time.Hour)
	mark := now.Add(-10 * time.Minute)
	turnEnds := map[string]time.Time{
		"01SEEN":       mark,
		"01NEWER":      mark.Add(time.Millisecond),
		"01PREEPOCH":   epoch.Add(-time.Minute),
		"01POSTEPOCH":  epoch.Add(time.Minute),
		"01UNREAD":     mark.Add(-time.Minute),
		"01NEVERENDED": {},
	}
	seen := hubcore.SessionSeenSnapshot{Epoch: epoch, Records: map[hubcore.ArchiveKey]hubcore.SessionSeenRecord{
		hubcore.SessionPinKey("", "01SEEN"):   {SeenThrough: mark},
		hubcore.SessionPinKey("", "01NEWER"):  {SeenThrough: mark},
		hubcore.SessionPinKey("", "01UNREAD"): {SeenThrough: mark, Unread: true},
	}}
	rows := seenLiveRows(t, now, turnEnds, seen)
	for id, wantUnseen := range map[string]bool{
		"01SEEN": false, "01NEWER": true, "01PREEPOCH": false, "01POSTEPOCH": true, "01UNREAD": true, "01NEVERENDED": false,
	} {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("no Live row for %s in %v", id, rows)
		}
		fields := navigationSummaryJSONFields(t, row)
		if _, present := fields["unseen"]; row.Unseen != wantUnseen || present != wantUnseen {
			t.Errorf("%s: unseen = %v (key present %v), want %v: the key is absent unless it is true", id, row.Unseen, present, wantUnseen)
		}
		ended := turnEnds[id]
		raw, present := fields["turn_ended_at"]
		if present == ended.IsZero() {
			t.Fatalf("%s: turn_ended_at present = %v for a turn end of %v", id, present, ended)
		}
		if want := `"` + ended.Format(time.RFC3339Nano) + `"`; present && string(raw) != want {
			t.Errorf("%s: turn_ended_at = %s, want %s", id, raw, want)
		}
	}

	// The phone marks a row seen with Date.parse(turn_ended_at): milliseconds
	// read back off the wire. That mark must clear exactly the turn it showed.
	var shownText string
	if err := json.Unmarshal(navigationSummaryJSONFields(t, rows["01NEWER"])["turn_ended_at"], &shownText); err != nil {
		t.Fatal(err)
	}
	shown, err := time.Parse(time.RFC3339Nano, shownText)
	if err != nil {
		t.Fatal(err)
	}
	seen.Records[hubcore.SessionPinKey("", "01NEWER")] = hubcore.SessionSeenRecord{SeenThrough: hubcore.UnixMilliTime(shown.UnixMilli())}
	if seenLiveRows(t, now, turnEnds, seen)["01NEWER"].Unseen {
		t.Fatal("marking the shown turn_ended_at seen left the row unseen: the millisecond round trip is not exact")
	}
}

// Capture reads the hub's seen markers beside its pins and favorites, so a
// navigation build decorates rows from the store evener/session/seen/set
// writes.
func TestNavigationCaptureReadsTheSeenMarkers(t *testing.T) {
	store := hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "index.db"))
	through := time.Date(2026, 9, 26, 11, 58, 0, 123_000_000, time.UTC)
	if _, err := store.MarkSeen("", "01A", through); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), SessionSeen: store})
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	seen := snapshot.Inputs.SessionSeen
	if seen.Epoch.IsZero() || !seen.Records[hubcore.SessionPinKey("", "01A")].SeenThrough.Equal(through) {
		t.Fatalf("captured seen markers = %+v, want the store's epoch and the mark through %v", seen, through)
	}
}

// A navigation build owns the seen records it captured: both clone paths copy
// them.
func TestNavigationInputsCloneOwnsTheSeenRecords(t *testing.T) {
	key := hubcore.SessionPinKey("", "01A")
	inputs := navigationBuildInputs{SessionSeen: hubcore.SessionSeenSnapshot{Epoch: time.Now(), Records: map[hubcore.ArchiveKey]hubcore.SessionSeenRecord{key: {Unread: true}}}}
	contextClone, err := cloneNavigationInputsContext(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, clone := range []navigationBuildInputs{cloneNavigationInputs(inputs), contextClone} {
		delete(clone.SessionSeen.Records, key)
	}
	if !inputs.SessionSeen.Records[key].Unread {
		t.Fatal("a clone of the navigation inputs aliased the seen records")
	}
}
```

In `codec.test.ts`, after the test "codec rejects an armed omitted count above the omitted watch total":

```ts
// S4: a live row carries when its last turn ended and whether that turn is
// unseen. The codec accepts both and refuses a malformed value, which the hub
// never sends.
test("codec accepts a row's turn end and unseen mark and refuses malformed ones", () => {
  const session = entityKey(key, "1");
  const statusFor = (extra: Record<string, unknown>) =>
    decodeNavigationResponse(
      key,
      undefined,
      snapshotResponse(key, {
        ...liveSnapshot(),
        entities: [{ key: session, kind: "session", value: { ...sessionValue("local:session"), ...extra } }],
      }),
    ).status;

  expect(statusFor({ turn_ended_at: "2026-09-26T11:58:00.123Z", unseen: true })).toBe("snapshot");
  expect(() => statusFor({ turn_ended_at: "yesterday" })).toThrow();
  expect(() => statusFor({ unseen: "yes" })).toThrow();
});
```

In `value-records.json`, after `"updated_at": "2026-09-26T12:00:00Z",` in the session record:

```json
    "turn_ended_at": "2026-09-26T11:58:00.123Z",
    "unseen": true,
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryTurnEndedAtAndUnseen|TestNavigationCaptureReadsTheSeenMarkers|TestNavigationInputsCloneOwnsTheSeenRecords|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`SessionSeen` is not a field of `navigationBuildInputs`).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: FAIL: "codec keeps every field the hub's value records carry" (the codec drops `turn_ended_at` and `unseen`) and the new test's malformed cases (unknown keys are dropped, not refused).

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, in `NavigationSessionSummary` after `UpdatedAt`:

```go
	// TurnEndedAt is when a live session's last turn ended, stamped by its
	// daemon (S4). It is present only on a live row whose daemon reported one.
	// A client that marks the row seen echoes it back as seenThrough.
	TurnEndedAt *time.Time `json:"turn_ended_at,omitempty"`
	// Unseen marks a live row whose last turn ended after the hub's
	// seen-through marker for it, or that was marked unread (S4): Finished on
	// the Board, and Idle when absent. It is only ever set on a row that
	// carries TurnEndedAt.
	Unseen bool `json:"unseen,omitempty"`
```

`hubcore/config.go`, in `WebConfig` after `PinSections`:

```go
	SessionSeen *SessionSeenStore // per-session seen-through markers (S4); nil when not configured
```

`navigation_projection.go`, in `navigationBuildInputs` after `PinAssignments`:

```go
	// SessionSeen is the hub's seen-through markers and their epoch, captured
	// with Tree. A live row's unseen flag is computed against it (S4).
	SessionSeen hubcore.SessionSeenSnapshot
```

In `cloneNavigationInputsContext`, before `out.Tree, err = in.Tree.SnapshotContext(ctx)`, and in `cloneNavigationInputs`, before `return out`:

```go
	out.SessionSeen = in.SessionSeen.Clone()
```

In `projectShallow`, after the `updatedAt` block:

```go
	var turnEndedAt *time.Time
	if !node.TurnEndedAt.IsZero() {
		ended := node.TurnEndedAt
		turnEndedAt = &ended
	}
```

and in its literal, after `UpdatedAt: updatedAt,`:

```go
		TurnEndedAt:         turnEndedAt,
		Unseen:              p.projection.unseen(ref, node.TurnEndedAt),
```

After `pinSectionIDFor`:

```go
// unseen reports whether a row's last turn ended after the hub's seen-through
// marker for it (S4). The marker is keyed by the row's own ref, the one a
// client marks it by, so a live session's Live, project and pin rows, which
// share that ref, agree.
func (p navigationProjection) unseen(ref hubapi.Ref, turnEndedAt time.Time) bool {
	return p.inputs.SessionSeen.Unseen(hubcore.SessionPinKey(ref.HostID, ref.SessionID), turnEndedAt)
}
```

In `cloneNavigationSummary`, after the `UpdatedAt` copy:

```go
	if summary.TurnEndedAt != nil {
		ended := *summary.TurnEndedAt
		clone.TurnEndedAt = &ended
	}
```

`navigation_service.go`, in `Capture` after the `sections, err := s.web.pinSections()` check:

```go
	// The seen store is nil-safe: an unconfigured hub reads as no store, and
	// no row is unseen.
	seen, err := s.web.cfg.SessionSeen.Snapshot()
	if err != nil {
		return navigationSourceSnapshot{}, err
	}
```

and after `inputs := navigationBuildInputsFromTreeSnapshot(...)`: `inputs.SessionSeen = seen`.

`codec.ts`: add `"turn_ended_at"` and `"unseen"` to `SESSION_KEYS`' optional list after `"updated_at"`, and to `sessionValue` after its `updated_at` line:

```ts
    optional(value.turn_ended_at, rfc3339Timestamp) &&
    optional(value.unseen, bool) &&
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, `make generate`, and `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNavigation|TestCloneNavigationSummary' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation && npm run typecheck`
Expected: PASS; `types.gen.ts` gains `turn_ended_at?: string` and `unseen?: boolean` on `NavigationSessionSummary`.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_service.go cmd/evener-hub/navigation_seen_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): live rows carry turn_ended_at and unseen against the seen marker"
```

### Task 17.4: `evener/session/seen/set`

**Files:**
- Modify: `appwire/types.go` (`MethodEvenerSessionSeenSet` after `MethodEvenerSessionPinUnpin` `:86`; three types after `SessionPinUnpinResponse` `:579-584`)
- Modify: `appwire/protocol.go` (a catalog row after `MethodEvenerSessionPinUnpin`'s, `:164`)
- Create: `cmd/evener-hub/app_session_seen.go`
- Create: `cmd/evener-hub/app_session_seen_test.go`
- Modify: `cmd/evener-hub/app_pin_section.go` (rename `commitPinNavigation` to `commitNavigationChange`: its definition `:135` and its four calls `:44`, `:59`, `:88`, `:110`)
- Modify: `cmd/evener-hub/app_rpc.go` (register after `registerPinSectionHandlers`, `:1101`)
- Modify: `cmd/evener-hub/app_rpc_test.go` (`TestHubRPCRegistersExpectedHandlerSet`: `appwire.MethodEvenerSessionSeenSet` after `appwire.MethodEvenerSessionPinUnpin`)
- Modify: `cmd/evener-hub/main.go` (construct the store after `pinSections` `:291`; `SessionSeen: sessionSeen,` after `PinSections: pinSections,` `:537`; its change hook after the pin hook `:645-647`)
- Modify: `cmd/evener-hub/project_delete.go` (`scrubSessionDecisions` `:487-510`)
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `SessionSeenStore.MarkSeen`, `MarkUnread`, `Delete`, `SetOnChange` (Task 17.1); `WebConfig.SessionSeen` (Task 17.3); `validateDecisionSource` (`app_archive.go:221`); `hubapi.ParseRef`.
- Produces: `appwire.MethodEvenerSessionSeenSet = "evener/session/seen/set"`; `SessionSeenSetParams{Sessions []SessionSeenMark}`; `SessionSeenMark{Ref string; SeenThrough int64; Unread bool}` (`ref`, `seenThrough,omitempty`, `unread,omitempty`); `SessionSeenSetResponse{OK, Changed bool; Navigation NavigationMutation}`; `maxSessionSeenMarks = 500`; `commitNavigationChange(ctx, cfg, navigation, changed)`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_session_seen_test.go`:

```go
package hub

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func newSessionSeenWeb(t *testing.T) (*WebServer, *hubcore.SessionSeenStore) {
	t.Helper()
	store := hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "index.db"))
	return NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), SessionSeen: store}), store
}

func dispatchSessionSeenSet(t *testing.T, web *WebServer, marks ...appwire.SessionSeenMark) (appwire.SessionSeenSetResponse, error) {
	t.Helper()
	return dispatchPinning[appwire.SessionSeenSetResponse](t, web, appwire.MethodEvenerSessionSeenSet, appwire.SessionSeenSetParams{Sessions: marks})
}

// A mark records the turn end a client showed and reports whether it changed
// anything; the same mark again changes nothing and commits no navigation
// targets. Unread is its own mark and keeps the seen-through.
func TestSessionSeenSetRecordsMarksAndReportsChange(t *testing.T) {
	web, store := newSessionSeenWeb(t)
	through := time.Date(2026, 9, 26, 11, 58, 0, 123_000_000, time.UTC)
	mark := appwire.SessionSeenMark{Ref: "local:01A", SeenThrough: through.UnixMilli()}

	if first, err := dispatchSessionSeenSet(t, web, mark); err != nil || !first.OK || !first.Changed {
		t.Fatalf("first mark = %+v (%v), want a committed change", first, err)
	}
	if again, err := dispatchSessionSeenSet(t, web, mark); err != nil || !again.OK || again.Changed || len(again.Navigation.Targets) != 0 {
		t.Fatalf("repeated mark = %+v (%v), want no change and no targets", again, err)
	}
	if unread, err := dispatchSessionSeenSet(t, web, appwire.SessionSeenMark{Ref: "local:01A", Unread: true}); err != nil || !unread.Changed {
		t.Fatalf("unread mark = %+v (%v), want a change", unread, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if record := snapshot.Records[hubcore.SessionPinKey("", "01A")]; !record.SeenThrough.Equal(through) || !record.Unread {
		t.Fatalf("stored record = %+v, want seen through %v and unread", record, through)
	}
}

// A malformed call is refused before anything is written: one bad mark fails
// the whole call.
func TestSessionSeenSetRefusesAMalformedCallWithoutWriting(t *testing.T) {
	web, store := newSessionSeenWeb(t)
	good := appwire.SessionSeenMark{Ref: "local:01GOOD", SeenThrough: 1_790_000_000_000}
	tooMany := make([]appwire.SessionSeenMark, maxSessionSeenMarks+1)
	for i := range tooMany {
		tooMany[i] = good
	}
	for name, marks := range map[string][]appwire.SessionSeenMark{
		"no sessions":                    nil,
		"too many sessions":              tooMany,
		"a malformed ref":                {good, {Ref: "not a ref", SeenThrough: 1}},
		"both seenThrough and unread":    {good, {Ref: "local:01B", SeenThrough: 1, Unread: true}},
		"neither seenThrough nor unread": {good, {Ref: "local:01B"}},
		"a negative seenThrough":         {good, {Ref: "local:01B", SeenThrough: -1, Unread: true}},
		"an unknown source":              {good, {Ref: "ghost:01B", SeenThrough: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := dispatchSessionSeenSet(t, web, marks...)
			assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
		})
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 0 {
		t.Fatalf("records after refused calls = %+v, want none", snapshot.Records)
	}
}

// Deleting a controller session forgets its marker and leaves a remote host's
// marker on the same bare ID alone.
func TestSessionScrubClearsOnlyTheControllerSeenMarker(t *testing.T) {
	web, store := newSessionSeenWeb(t)
	through := time.Date(2026, 9, 26, 11, 58, 0, 0, time.UTC)
	for _, source := range []string{"", "host-a"} {
		if _, err := store.MarkSeen(source, "th_1", through); err != nil {
			t.Fatal(err)
		}
	}
	if failures := web.scrubSessionDecisions("th_1"); len(failures) != 0 {
		t.Fatalf("scrub failures = %v", failures)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, local := snapshot.Records[hubcore.SessionPinKey("", "th_1")]; local {
		t.Fatal("the controller's marker survived its session's deletion")
	}
	if _, remote := snapshot.Records[hubcore.SessionPinKey("host-a", "th_1")]; !remote {
		t.Fatal("scrubbing the controller session dropped a remote host's marker")
	}
}

// The hub opens its seen store in index.db and wires its change hook, so a
// mark from any writer invalidates navigation.
func TestRunMainWiresTheSeenMarkerStore(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	var web *WebServer
	deps.afterWeb = func(created *WebServer) { web = created }
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if web == nil || web.cfg.SessionSeen == nil {
		t.Fatal("the hub started without a seen-marker store")
	}
	_, before, _ := web.navigation.snapshotPendingHint()
	if changed, err := web.cfg.SessionSeen.MarkSeen("", "01A", time.Now()); err != nil || !changed {
		t.Fatalf("MarkSeen = %v, %v; want a change", changed, err)
	}
	if _, after, _ := web.navigation.snapshotPendingHint(); after <= before {
		t.Fatal("a seen mark did not invalidate navigation")
	}
}
```

Add `appwire.MethodEvenerSessionSeenSet,` after `appwire.MethodEvenerSessionPinUnpin,` in `TestHubRPCRegistersExpectedHandlerSet`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestSessionSeenSet|TestSessionScrubClearsOnlyTheControllerSeenMarker|TestRunMainWiresTheSeenMarkerStore|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog' -count=1`
Expected: FAIL to compile (`appwire.SessionSeenMark` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, in the method constants after `MethodEvenerSessionPinUnpin`:

```go
	MethodEvenerSessionSeenSet           = "evener/session/seen/set"
```

and after `SessionPinUnpinResponse`:

```go
// SessionSeenSetParams marks sessions seen or unread on the hub (S4), so the
// Board's Finished and Idle agree on every device.
type SessionSeenSetParams struct {
	Sessions []SessionSeenMark `json:"sessions"`
}

// SessionSeenMark is one session's mark, addressed by the ref its row carries.
// It sets exactly one of SeenThrough and Unread. SeenThrough is the row's own
// turn_ended_at in Unix milliseconds, the turn the client showed, never a
// client clock; the hub keeps the newest it has been sent. Unread is "Mark as
// unread", which lasts until the next SeenThrough mark.
type SessionSeenMark struct {
	Ref         string `json:"ref"`
	SeenThrough int64  `json:"seenThrough,omitempty"`
	Unread      bool   `json:"unread,omitempty"`
}

// SessionSeenSetResponse acknowledges the marks. Changed reports whether any
// mark moved a stored marker; Navigation carries the committed invalidation
// targets, empty when nothing changed.
type SessionSeenSetResponse struct {
	OK         bool               `json:"ok"`
	Changed    bool               `json:"changed"`
	Navigation NavigationMutation `json:"navigation"`
}
```

`appwire/protocol.go`, after the `MethodEvenerSessionPinUnpin` row:

```go
	{MethodEvenerSessionSeenSet, SessionSeenSetParams{}, SessionSeenSetResponse{}, ScopeHub, "Marks sessions seen through a turn end, or unread, on the hub (S4), and returns the committed navigation receipt. Live rows then carry unseen from the hub's marker."},
```

`app_pin_section.go`: rename `commitPinNavigation` to `commitNavigationChange` at its definition and its four calls, and give it this comment:

```go
// commitNavigationChange returns the navigation receipt for a hub-owned
// mutation: the committed targets when the mutation changed anything, and an
// empty mutation when it did not.
```

`cmd/evener-hub/app_session_seen.go`:

```go
package hub

import (
	"context"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

// maxSessionSeenMarks bounds one evener/session/seen/set call: opening a row
// sends one mark, and Mark as read on a selection sends one per row.
const maxSessionSeenMarks = 500

func registerSessionSeenHandler(server *appserver.Server, cfg hubcore.WebConfig, navigation *NavigationService) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionSeenSet, func(ctx context.Context, params appwire.SessionSeenSetParams) (appwire.SessionSeenSetResponse, error) {
		return sessionSeenSet(ctx, cfg, navigation, params)
	})
}

// sessionSeenWrite is one validated mark: the store key it writes and what it
// writes there.
type sessionSeenWrite struct {
	source, sessionID string
	seenThrough       int64
	unread            bool
}

// sessionSeenSet answers evener/session/seen/set (S4). Every mark is checked
// before any is written, so a malformed call changes nothing. A mark names its
// session by the row's ref as sent; it is not resolved to a current session,
// because the projection reads markers by that same ref.
func sessionSeenSet(ctx context.Context, cfg hubcore.WebConfig, navigation *NavigationService, params appwire.SessionSeenSetParams) (appwire.SessionSeenSetResponse, error) {
	if len(params.Sessions) == 0 || len(params.Sessions) > maxSessionSeenMarks {
		return appwire.SessionSeenSetResponse{}, appwire.InvalidParams(fmt.Sprintf("sessions must name 1 to %d sessions", maxSessionSeenMarks))
	}
	if cfg.SessionSeen == nil {
		return appwire.SessionSeenSetResponse{}, appwire.InternalError("seen marker store not configured")
	}
	writes := make([]sessionSeenWrite, 0, len(params.Sessions))
	for _, mark := range params.Sessions {
		ref, err := hubapi.ParseRef(mark.Ref)
		if err != nil {
			return appwire.SessionSeenSetResponse{}, appwire.InvalidParams("sessions[].ref must be a session ref: " + err.Error())
		}
		if mark.SeenThrough < 0 || (mark.SeenThrough > 0) == mark.Unread {
			return appwire.SessionSeenSetResponse{}, appwire.InvalidParams("each session sets exactly one of seenThrough or unread")
		}
		source := hubcore.NormalizeDecisionSource(ref.HostID)
		if err := validateDecisionSource(cfg, source); err != nil {
			return appwire.SessionSeenSetResponse{}, err
		}
		writes = append(writes, sessionSeenWrite{source: source, sessionID: ref.SessionID, seenThrough: mark.SeenThrough, unread: mark.Unread})
	}
	changed := false
	for _, write := range writes {
		var wrote bool
		var err error
		if write.unread {
			wrote, err = cfg.SessionSeen.MarkUnread(write.source, write.sessionID)
		} else {
			wrote, err = cfg.SessionSeen.MarkSeen(write.source, write.sessionID, hubcore.UnixMilliTime(write.seenThrough))
		}
		if err != nil {
			return appwire.SessionSeenSetResponse{}, appwire.InternalError("seen marker store error: " + err.Error())
		}
		changed = changed || wrote
	}
	mutation, err := commitNavigationChange(ctx, cfg, navigation, changed)
	if err != nil {
		return appwire.SessionSeenSetResponse{}, err
	}
	return appwire.SessionSeenSetResponse{OK: true, Changed: changed, Navigation: mutation}, nil
}
```

`app_rpc.go`, after `registerPinSectionHandlers(server, cfg, navigation, resolve)`: `registerSessionSeenHandler(server, cfg, navigation)`.

`main.go`: after `pinSections := hubcore.NewPinSectionStore(pastIndexDB)`, add `sessionSeen := hubcore.NewSessionSeenStore(pastIndexDB)`; in the `WebConfig` literal after `PinSections: pinSections,`, add `SessionSeen: sessionSeen,`; after the `pinSections.SetOnChange` block:

```go
	// A seen mark changes live rows' unseen flag; a mark committed by any
	// writer, the RPC or a session deletion's scrub, invalidates navigation.
	sessionSeen.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })
```

`project_delete.go`, at the end of `scrubSessionDecisions` before `return decisionErrors`:

```go
	if s.cfg.SessionSeen != nil {
		if _, err := s.cfg.SessionSeen.Delete("", threadID); err != nil {
			decisionErrors = append(decisionErrors, fmt.Sprintf("seen marker store error: %v", err))
		}
	}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, then `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestSessionSeenSet|TestSessionScrub|TestRunMainWiresTheSeenMarkerStore|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog|PinSection|SessionPin' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `golangci-lint run ./cmd/evener-hub/ ./appwire/`
Expected: PASS; `types.gen.ts` gains the three types and the method's entry.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go appwire/protocol.go cmd/evener-hub/app_session_seen.go cmd/evener-hub/app_session_seen_test.go cmd/evener-hub/app_pin_section.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go cmd/evener-hub/main.go cmd/evener-hub/project_delete.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): evener/session/seen/set records seen-through marks and unread"
```

### Task 17.5: A remote host's rows carry their turn end

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry` `:47-92`; `threadFromEntry`'s `Evener` literal `:1138-1143`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_turn_ended_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster` `:116-160`)
- Modify: `cmd/evener-hub/web_api_tree.go` (`appThreadTreeEntries` `:824-877`)
- Create: `cmd/evener-hub/remote_turn_ended_test.go`

**Interfaces:**
- Consumes: `LiveEntry.LastTurnEndedAt` (Task 17.2); `EvenerThread.LastTurnEndedAt` (PR 16); the summary fields (Task 17.3).
- Produces: `appsource.LocalDaemonEntry.LastTurnEndedAt int64` (Unix milliseconds).

A controller reads a remote host's sessions from that hub's `thread/list`, whose local rows `threadFromEntry` builds from the remote hub's roster. The turn end rides that row, the way PR 5 carries approvals: it changes only when a turn ends, so the 30-second remote walk does not churn navigation (ruling 23).

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_turn_ended_test.go`:

```go
package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's turn end
// on the row, so the controller can tell Finished from Idle for a remote
// session (S4).
func TestLocalDaemonSourceListCarriesTheRootsTurnEnd(t *testing.T) {
	const ended = int64(1_790_000_000_123)
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/ended", ThreadID: "th_ended", SessionID: "sess_ended"}, Status: appwire.ThreadStatusIdle, LastTurnEndedAt: ended},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/fresh", ThreadID: "th_fresh", SessionID: "sess_fresh"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	endedByID := map[string]int64{}
	for _, thread := range resp.Data {
		endedByID[thread.ID] = thread.Evener.LastTurnEndedAt
	}
	if endedByID["th_ended"] != ended || endedByID["th_fresh"] != 0 {
		t.Fatalf("turn ends = %v, want th_ended at %d and none on th_fresh", endedByID, ended)
	}
}
```

`cmd/evener-hub/remote_turn_ended_test.go`:

```go
package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's turn end and never lend it to the
// root's in-process subagent aliases: a child's row is not the root's turn.
func TestLocalDaemonEntriesFromRosterCarryTheTurnEndOnlyOnTheRoot(t *testing.T) {
	ended := time.Date(2026, 9, 26, 11, 58, 0, 123_000_000, time.UTC)
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{"sess_child"}, LastTurnEndedAt: ended,
	}})
	if len(entries) != 2 || entries[0].LastTurnEndedAt != ended.UnixMilli() || entries[1].LastTurnEndedAt != 0 {
		t.Fatalf("entries = %+v, want the root at %d and its alias with none", entries, ended.UnixMilli())
	}
}

// A controller reads a remote host's turn end off its list row, so the remote
// live row carries turn_ended_at and unseen exactly like a local one (S4).
func TestNavigationRemoteLiveRowCarriesTurnEndAndUnseen(t *testing.T) {
	ended := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "th_1", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener: appwire.EvenerThread{Ref: "host-a:th_1", LastTurnEndedAt: ended.UnixMilli()},
	}})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "host-a"}, online: true})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", RemoteThreadCache: cache, Past: hubcore.NewPastIndex("")})
	web.sources = registry

	snapshot := web.navigationSnapshotInputs(t.Context())
	tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, map[hubcore.ArchiveKey]bool{}, snapshot.projects)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, web.apiTreeSources(), hubapi.AttentionSummary{}, snapshot.live, nil, nil, nil, nil)
	inputs.SessionSeen = hubcore.SessionSeenSnapshot{Epoch: ended.Add(-time.Hour)}
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	row := navigationProjectedSummary(t, projection, "host-a:th_1")
	if row.TurnEndedAt == nil || !row.TurnEndedAt.Equal(ended) || !row.Unseen {
		t.Fatalf("remote row = %+v, want turn end %v and unseen", row, ended)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListCarriesTheRootsTurnEnd' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterCarryTheTurnEndOnlyOnTheRoot|TestNavigationRemoteLiveRowCarriesTurnEndAndUnseen' -count=1`
Expected: FAIL to compile (`LastTurnEndedAt` is not a field of `LocalDaemonEntry`); once it exists, the remote row test fails with no turn end.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `CapabilitiesKnown`:

```go
	// LastTurnEndedAt mirrors hubcore.LiveEntry.LastTurnEndedAt in Unix
	// milliseconds: when the root's last turn ended (S4). threadFromEntry
	// carries it into appwire.EvenerThread.LastTurnEndedAt so a controller
	// reading this hub can tell Finished from Idle. A read-only alias has none.
	LastTurnEndedAt int64
```

and in `threadFromEntry`'s `Evener` literal after `AskPending: item.PendingAsk,`: `LastTurnEndedAt: item.LastTurnEndedAt,`.

`app_rpc.go`, in `localDaemonEntriesFromRoster`: add `LastTurnEndedAt: hubcore.UnixMilliseconds(item.LastTurnEndedAt),` to the root entry's literal, and in the child loop after `child.ReadOnlyAlias = true`: `child.LastTurnEndedAt = 0 // the root's turn is not the child's`.

`web_api_tree.go`, in `appThreadTreeEntries` after `entry.Watches = diagnosticsWatches(thread.Evener.Diagnostics)`:

```go
	entry.LastTurnEndedAt = hubcore.UnixMilliTime(thread.Evener.LastTurnEndedAt)
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemoteLiveRowCarriesTurnEndAndUnseen|TestNavigationOffline' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_turn_ended_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/remote_turn_ended_test.go
git commit -m "feat(hub): remote hosts' rows carry their turn end"
```

### Task 17.6: The web marks a session seen when its pane opens

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/markSeen.ts`
- Create: `cmd/evener-hub/frontend/src/panes/session/markSeen.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx` (import; one call after the `ensureThread` effect, which ends at `:268`)
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx` (`setNavigationTitle` `:545-586` gains an optional `fields` argument; one wiring test)

**Interfaces:**
- Consumes: `turn_ended_at` and `unseen` (Task 17.3); `evener/session/seen/set` (Task 17.4); `selectSessionSummary` (`stores/navigation/selectors.ts:43`); `connectionStore` (`stores/connection.ts`); `navigationStore` (`stores/navigation/store.ts`).
- Produces: `seenThroughToMark(summary: Pick<NavigationSessionSummary, "unseen" | "turn_ended_at"> | undefined): number | undefined`; `useMarkSessionSeenOnOpen(ref: string): void`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/frontend/src/panes/session/markSeen.test.ts`:

```ts
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { seenThroughToMark, useMarkSessionSeenOnOpen } from "./markSeen";

const REF = "local:01SEEN";
const FIRST_TURN = "2026-09-26T11:58:00.123Z";
const NEXT_TURN = "2026-09-26T12:04:00.456Z";
const SEEN_SET = "evener/session/seen/set";

let visibility: DocumentVisibilityState = "visible";

// showRow installs the pane's row as a loaded navigation location, the way the
// hub's location read would.
function showRow(fields: Partial<NavigationSessionSummary>): void {
  const key = { kind: "location", ref: REF } as const;
  const data = {
    generation_id: "generation_test",
    revision: 1,
    ref: REF,
    top_level_ref: REF,
    top_level: true,
    session: {
      ref: REF,
      host_id: "local",
      session_id: "01SEEN",
      title: "Finished work",
      project: "test-project",
      state: "idle",
      kind: "session",
      live: true,
      children: [],
      ...fields,
    },
  };
  navigationStore.setState({
    mode: "v2",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data,
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: "etag",
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
}

function connectFake(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on(SEEN_SET, () => ({ ok: true, changed: true, navigation: { generation_id: "generation_test", targets: [] } }));
  connectionStore.getState().connect(fake);
  return fake;
}

const marks = (fake: FakeClient) => fake.calls.filter((call) => call.method === SEEN_SET).map((call) => call.params);
const markFor = (turn: string) => ({ sessions: [{ ref: REF, seenThrough: Date.parse(turn) }] });

function setVisibility(next: DocumentVisibilityState): void {
  visibility = next;
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
});

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(document, "visibilityState");
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
});

test("only an unseen row that carries its turn end has a turn to mark", () => {
  expect(seenThroughToMark({ unseen: true, turn_ended_at: FIRST_TURN })).toBe(Date.parse(FIRST_TURN));
  expect(seenThroughToMark({ unseen: false, turn_ended_at: FIRST_TURN })).toBeUndefined();
  expect(seenThroughToMark({ turn_ended_at: FIRST_TURN })).toBeUndefined();
  expect(seenThroughToMark({ unseen: true })).toBeUndefined();
  expect(seenThroughToMark({ unseen: true, turn_ended_at: "not a time" })).toBeUndefined();
  expect(seenThroughToMark(undefined)).toBeUndefined();
});

test("opening the pane marks the turn its row shows, once", () => {
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
  // The hub's answer arrives as a navigation update and must not mark again.
  act(() => showRow({ turn_ended_at: FIRST_TURN }));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});

test("a row that loads after the pane opens is marked when it arrives, and a later turn is not", () => {
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([]);
  act(() => showRow({ unseen: true, turn_ended_at: FIRST_TURN }));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
  // A turn that ends while the pane stays open is not "opened since" (S4 ruling 18).
  act(() => showRow({ unseen: true, turn_ended_at: NEXT_TURN }));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});

test("a seen row, or one without a turn end, sends nothing", () => {
  const fake = connectFake();
  showRow({ unseen: false, turn_ended_at: FIRST_TURN });
  renderHook(() => useMarkSessionSeenOnOpen(REF)).unmount();
  showRow({});
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([]);
});

test("coming back to the page marks the turn the row shows now", () => {
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  act(() => showRow({ unseen: true, turn_ended_at: NEXT_TURN }));
  setVisibility("hidden");
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
  setVisibility("visible");
  expect(marks(fake)).toEqual([markFor(FIRST_TURN), markFor(NEXT_TURN)]);
});

test("the mark waits for a ready connection", () => {
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  const fake = connectFake();
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});
```

In `Session.test.tsx`, give `setNavigationTitle` a fourth parameter `fields: Record<string, unknown> = {}` and spread `...fields` at the end of its `session` object, then add:

```tsx
// S4: opening a session pane marks the turn its row shows as seen on the hub,
// so the session's blue dot clears on the phone too.
test("opening the pane marks the session's unseen turn seen", async () => {
  setNavigationTitle("ref_a", "Finished work", true, { turn_ended_at: "2026-09-26T11:58:00.123Z", unseen: true });
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("evener/session/seen/set", () => ({
    ok: true,
    changed: true,
    navigation: { generation_id: "generation_test", targets: [] },
  }));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/session/seen/set").map((call) => call.params)).toEqual([
      { sessions: [{ ref: "ref_a", seenThrough: Date.parse("2026-09-26T11:58:00.123Z") }] },
    ]),
  );
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/markSeen.test.ts src/panes/session/Session.test.tsx`
Expected: FAIL: `./markSeen` does not resolve, and the Session test sees no mark.

- [ ] **Step 3: Implement**

`cmd/evener-hub/frontend/src/panes/session/markSeen.ts`:

```ts
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { useEffect } from "react";
import { connectionStore } from "../../stores/connection";
import { selectSessionSummary } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";

/** The seenThrough a pane opening on this row sends: the row's own
 * turn_ended_at in milliseconds, when the hub reports that turn unseen. A seen
 * row, and a row with no turn end (an older hub, or a session that has not
 * ended a turn), has nothing to mark. */
export function seenThroughToMark(
  summary: Pick<NavigationSessionSummary, "unseen" | "turn_ended_at"> | undefined,
): number | undefined {
  if (summary?.unseen !== true || summary.turn_ended_at === undefined) return undefined;
  const seenThrough = Date.parse(summary.turn_ended_at);
  return Number.isFinite(seenThrough) ? seenThrough : undefined;
}

/** Marks a session seen on the hub when its pane opens, and again when the
 * page becomes visible with the pane still open, so its blue dot clears on
 * every device (spec 18, S4). Each open marks the row as the pane first finds
 * it, once: a turn that ends while the pane stays open is not "opened since".
 * A row still loading is marked when it arrives. The hub's mark is idempotent,
 * so a failed one is left for the next open. */
export function useMarkSessionSeenOnOpen(ref: string): void {
  useEffect(() => {
    let awaitingRow = true;
    const markOnce = () => {
      if (!awaitingRow) return;
      const { client, state } = connectionStore.getState();
      if (state !== "ready" || !client) return;
      const summary = selectSessionSummary(ref, navigationStore.getState());
      if (!summary) return;
      awaitingRow = false;
      const seenThrough = seenThroughToMark(summary);
      if (seenThrough === undefined) return;
      client.request("evener/session/seen/set", { sessions: [{ ref, seenThrough }] }).catch(() => {});
    };
    const onVisibilityChange = () => {
      if (document.visibilityState !== "visible") return;
      awaitingRow = true;
      markOnce();
    };
    markOnce();
    const unsubscribeConnection = connectionStore.subscribe(markOnce);
    const unsubscribeNavigation = navigationStore.subscribe(markOnce);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      unsubscribeConnection();
      unsubscribeNavigation();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [ref]);
}
```

`Session.tsx`: import `useMarkSessionSeenOnOpen` from `./markSeen`, and after the `ensureThread` effect:

```tsx
  // S4: opening the pane marks its session seen on the hub, so the session's
  // blue dot clears on the phone too.
  useMarkSessionSeenOnOpen(ref);
```

Run `cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/markSeen.ts src/panes/session/markSeen.test.ts src/panes/session/Session.tsx src/panes/session/Session.test.tsx`.

- [ ] **Step 4: Run them to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/markSeen.test.ts src/panes/session/Session.test.tsx && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/markSeen.ts cmd/evener-hub/frontend/src/panes/session/markSeen.test.ts cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/session/Session.test.tsx
git commit -m "feat(web): opening a session pane marks it seen on the hub"
```

Title "feat(hub): the seen-through marker (S4b, phase 7 PR 17)". The body says the hub keeps a source-qualified marker in `index.db`, live rows carry `turn_ended_at` and `unseen`, `evener/session/seen/set` writes marks, live rows no longer fold into clusters, remote hosts' rows carry their turn end, and the web marks a session seen when its pane opens; it names the TestFlight build containing #2473 that it waited for.

---

## PR 18: the daemon's subagent tally, S3a (Tasks 18.1-18.2)

**Branch:** `git fetch origin && git switch -c claude/s3a-subagent-tally origin/main`

**What it adds.** A root session counts every delegate in its tree by how its latest run stands, and the daemon stamps that tally on its `thread/list` root row as `EvenerThread.subagents`. Nothing reads it yet; PR 19 carries it to rows.

### Task 18.1: The root session tallies its whole delegate tree

**Files:**
- Modify: `appwire/types.go` (the `SubagentTally` type, after the `EvenerThread` type)
- Create: `agent/subagent_tally.go`
- Create: `agent/subagent_tally_test.go`
- Modify: `agent/jobs_activity.go` (`projectStableActivityDelegate`'s terminal line, `:1233`)

**Interfaces:**
- Consumes: `delegateTreeController.durable` (`delegatestore.State`, every delegate of the tree at every depth, `agent/delegate_tree_controller.go:46-72`) and its `mu`; `activityDelegateOutcome` (`jobs_activity.go:1573-1584`); `Session.delegateController` and `ownsDelegateController` (`agent/session.go:127-130`).
- Produces: `appwire.SubagentTally{Running, Failed, Done int}` (JSON `running`, `failed`, `done`); `delegateRunTerminal(outcome *delegatestore.Outcome, currentRunOpen bool) bool`; `(c *delegateTreeController) tally() appwire.SubagentTally`; `(s *Session) SubagentTally() (appwire.SubagentTally, bool)`.

The agent package already builds `appwire` activity types (`jobs_activity.go`), so the tally is the wire type itself and no conversion layer is needed. The new type is not reachable from the catalog until Task 18.2, so `make generate` has nothing to emit here.

- [ ] **Step 1: Write the failing tests**

`agent/subagent_tally_test.go`:

```go
package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// tallyAggregate is one folded delegate: its latest outcome ("" before any run
// ended) and whether a run is open after it.
func tallyAggregate(id string, status delegatestore.OutcomeStatus, runOpen bool) *delegatestore.Aggregate {
	aggregate := &delegatestore.Aggregate{DelegateID: id, Phase: delegatestore.PhaseIdle, Resumable: true, CurrentRunOpen: runOpen}
	if runOpen {
		aggregate.Phase = delegatestore.PhaseRunning
	}
	if status != "" {
		aggregate.LatestOutcome = &delegatestore.Outcome{Status: status}
	}
	return aggregate
}

// The row's tally counts every delegate in the tree, nested ones and ones past
// the row's children cap included, by how its latest run stands: running
// until a run has ended with none open after it, failed when that run ended
// failed or exhausted, done otherwise (spec 9, S3 ruling 21).
func TestSubagentTallyCountsEveryDelegateInTheTreeByState(t *testing.T) {
	controller := &delegateTreeController{durable: delegatestore.State{}}
	for _, aggregate := range []*delegatestore.Aggregate{
		tallyAggregate("d-first-run", "", true),
		tallyAggregate("d-created", "", false),
		tallyAggregate("d-resumed", delegatestore.OutcomeFailed, true),
		tallyAggregate("d-failed", delegatestore.OutcomeFailed, false),
		tallyAggregate("d-exhausted", delegatestore.OutcomeExhausted, false),
		tallyAggregate("d-nested-failed", delegatestore.OutcomeFailed, false),
		tallyAggregate("d-completed", delegatestore.OutcomeCompleted, false),
		tallyAggregate("d-cancelled", delegatestore.OutcomeCancelled, false),
		tallyAggregate("d-stopped", delegatestore.OutcomeStopped, false),
	} {
		controller.durable[aggregate.DelegateID] = aggregate
	}
	controller.durable["d-nested-failed"].Descriptor.ParentDelegateID = "d-completed"
	if got, want := controller.tally(), (appwire.SubagentTally{Running: 3, Failed: 3, Done: 3}); got != want {
		t.Fatalf("tally = %+v, want %+v", got, want)
	}
}

// Only the tree's root session owns the controller. A child session shares
// its root's controller and must not report the root's tree as its own; a
// session with no delegate tree reports none.
func TestSubagentTallyIsTheRootsAlone(t *testing.T) {
	controller := &delegateTreeController{durable: delegatestore.State{"d": tallyAggregate("d", "", true)}}
	for _, tc := range []struct {
		name    string
		session *Session
		want    bool
	}{
		{"the root", &Session{delegateController: controller, ownsDelegateController: true}, true},
		{"a child session", &Session{delegateController: controller}, false},
		{"no delegate tree", &Session{}, false},
	} {
		tally, ok := tc.session.SubagentTally()
		if ok != tc.want || (ok && tally.Running != 1) {
			t.Errorf("%s: SubagentTally = %+v, %v; want ok %v", tc.name, tally, ok, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd agent && go test . -run 'TestSubagentTally' -count=1`
Expected: FAIL to compile (`appwire.SubagentTally` and `controller.tally` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, after the `EvenerThread` type:

```go
// SubagentTally counts a live root session's subagents, at every depth, by how
// each one's latest run stands (spec 9, S3). Running: no run has ended yet, or
// a run is open again after the last one ended. Failed: the latest run ended
// failed or exhausted. Done: it ended any other way (completed, cancelled or
// stopped), including a subagent idle between runs.
type SubagentTally struct {
	Running int `json:"running"`
	Failed  int `json:"failed"`
	Done    int `json:"done"`
}
```

`agent/subagent_tally.go`:

```go
package agent

import (
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// delegateRunTerminal reports whether a delegate's latest run has ended with
// no run open after it: the Subagents list's terminal rule
// (projectStableActivityDelegate) and the row tally's, kept in one place so
// the row and the list cannot disagree.
func delegateRunTerminal(outcome *delegatestore.Outcome, currentRunOpen bool) bool {
	return outcome != nil && !currentRunOpen
}

// tally counts every delegate the controller has folded, at every depth and
// over the tree's whole history, by how its latest run stands (S3).
func (c *delegateTreeController) tally() appwire.SubagentTally {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tally appwire.SubagentTally
	for _, aggregate := range c.durable {
		switch {
		case !delegateRunTerminal(aggregate.LatestOutcome, aggregate.CurrentRunOpen):
			tally.Running++
		case activityDelegateOutcome(string(aggregate.LatestOutcome.Status)) == "failure":
			tally.Failed++
		default:
			tally.Done++
		}
	}
	return tally
}

// SubagentTally is this session's whole delegate tree counted by state, for its
// thread list row (S3). Only the tree's root owns the controller: a child
// session shares its root's, and it and a session with no delegate tree report
// false. It takes the controller's lock, as DetailedStatus does, and never
// the session's.
func (s *Session) SubagentTally() (appwire.SubagentTally, bool) {
	if s == nil || !s.ownsDelegateController || s.delegateController == nil {
		return appwire.SubagentTally{}, false
	}
	return s.delegateController.tally(), true
}
```

`jobs_activity.go:1233`: `delegate.Terminal = delegateRunTerminal(row.lastOutcome, row.currentRunOpen)`.

Run `$(go env GOROOT)/bin/gofmt -w appwire/types.go agent/subagent_tally.go agent/subagent_tally_test.go agent/jobs_activity.go`.

- [ ] **Step 4: Run them to verify they pass**

Run: `cd agent && go test . -run 'TestSubagentTally|Activity' -count=1`, `go test ./appwire -count=1`
Expected: PASS, the activity tree's tests unchanged.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go agent/subagent_tally.go agent/subagent_tally_test.go agent/jobs_activity.go
git commit -m "feat(agent): a root session tallies its whole delegate tree by state"
```

### Task 18.2: The root row carries the tally

**Files:**
- Modify: `appwire/types.go` (`EvenerThread.Subagents`, at the end of `EvenerThread`, `:822`)
- Modify: `appwire/clone.go` (`cloneSubagentTally`; one line in `cloneEvenerThread`, `:104-117`)
- Modify: `appwire/clone_test.go`
- Modify: `server/server.go` (`Server`: `appSubagentTallyFunc` after `appDescendantLiveWatchesFunc`, `:393-400`)
- Modify: `server/appwire_runtime.go` (`SetSubagentTallyFunc` after `SetDescendantLiveWatchesFunc`, `:383-387`; `attachSubagentTally` after `attachLiveWatches`, `:2712-2731`; one call in `handleAppThreadList`, `:1293-1325`)
- Create: `server/appwire_subagent_tally_test.go`
- Modify: `cmd/evener/serve.go` (`serveServer`: `SetSubagentTallyFunc` after `SetDescendantLiveWatchesFunc`, `:146`; one line in `bridgeSession` after the live-watches seam, `:1308-1310`)
- Modify: `cmd/evener/serve_state_test.go` (`clearIdentityServer` `:767-778` records the installed tally)
- Create: `cmd/evener/serve_subagent_tally_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `appwire.SubagentTally`, `Session.SubagentTally` (Task 18.1).
- Produces: `EvenerThread.Subagents *SubagentTally` (`subagents,omitempty`); `(*server.Server).SetSubagentTallyFunc(func() (appwire.SubagentTally, bool))`; TS `SubagentTally` and `EvenerThread.subagents?: SubagentTally`.

- [ ] **Step 1: Write the failing tests**

`server/appwire_subagent_tally_test.go`:

```go
package server

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// The root row carries its tree's tally, read through the seam when the row is
// listed; a descendant row never does, and a tree with no subagents, or a
// session with no delegate tree, carries none (S3 ruling 22).
func TestThreadListRootRowCarriesItsTreesSubagentTally(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tally appwire.SubagentTally
		ok    bool
		want  *appwire.SubagentTally
	}{
		{"a tree with subagents", appwire.SubagentTally{Running: 2, Failed: 1, Done: 5}, true, &appwire.SubagentTally{Running: 2, Failed: 1, Done: 5}},
		{"a tree with none", appwire.SubagentTally{}, true, nil},
		{"no delegate tree", appwire.SubagentTally{Running: 1}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_root")
			srv.RecordDescendantAppEvent("th_root", events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_child", Data: events.UserInputData{Text: "go"}})
			srv.SetSubagentTallyFunc(func() (appwire.SubagentTally, bool) { return tc.tally, tc.ok })
			list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{IncludeSubagents: true, StatusOnly: true})
			if err != nil {
				t.Fatalf("thread/list: %v", err)
			}
			if len(list.Data) != 2 {
				t.Fatalf("rows = %+v, want the root and its child", list.Data)
			}
			if got := list.Data[0].Evener.Subagents; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("root tally = %+v, want %+v", got, tc.want)
			}
			if child := list.Data[1].Evener.Subagents; child != nil {
				t.Fatalf("the child row carries a tally: %+v", child)
			}
			if read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{}); read.Thread.Evener.Subagents != nil {
				t.Fatalf("thread/read carries a tally %+v; nothing announces its changes to a subscriber", read.Thread.Evener.Subagents)
			}
		})
	}
}
```

Append to `appwire/clone_test.go`:

```go
func TestCloneThreadOwnsTheSubagentTally(t *testing.T) {
	original := Thread{Evener: EvenerThread{Subagents: &SubagentTally{Running: 1, Failed: 2, Done: 3}}}
	clone := CloneThread(original)
	if !reflect.DeepEqual(clone, original) {
		t.Fatal("clone changed values while copying")
	}
	clone.Evener.Subagents.Failed = 9
	if original.Evener.Subagents.Failed != 2 {
		t.Fatalf("the tally was changed through its clone: %+v", original.Evener.Subagents)
	}
}
```

In `cmd/evener/serve_state_test.go`, give `clearIdentityServer` two fields and a forwarding method:

```go
	// subagentTally and subagentTallyInstalls record the tally seam serve
	// installs for each session it bridges.
	subagentTally         func() (appwire.SubagentTally, bool)
	subagentTallyInstalls int
```

```go
func (s *clearIdentityServer) SetSubagentTallyFunc(fn func() (appwire.SubagentTally, bool)) {
	s.subagentTally = fn
	s.subagentTallyInstalls++
	s.Server.SetSubagentTallyFunc(fn)
}
```

`cmd/evener/serve_subagent_tally_test.go`:

```go
package main

import "testing"

// Serve installs the root session's subagent tally when it bridges the session,
// and again for the replacement a thread/clear installs (S3): like the live
// watches beside it, the tally has to follow the current session.
func TestServeInstallsTheRootsSubagentTallyAndFollowsClear(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var installs int
	var answered bool
	obs := runClearAttempt(t, deps, state, args, func(*clearObservation) {
		installs = state.srv.subagentTallyInstalls
		if state.srv.subagentTally != nil {
			_, answered = state.srv.subagentTally()
		}
	})
	if obs.clearErr != nil {
		t.Fatalf("thread/clear: %v", obs.clearErr)
	}
	if installs != 2 {
		t.Fatalf("tally installs = %d, want one at startup and one for the clear's replacement", installs)
	}
	if !answered {
		t.Fatal("the installed tally does not answer for the current root session")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestThreadListRootRowCarriesItsTreesSubagentTally' -count=1`, `go test ./appwire -run 'TestCloneThreadOwnsTheSubagentTally' -count=1`, `go test ./cmd/evener -run 'TestServeInstallsTheRootsSubagentTallyAndFollowsClear' -count=1`
Expected: FAIL to compile (`SetSubagentTallyFunc` and `EvenerThread.Subagents` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, at the end of `EvenerThread`:

```go
	// Subagents tallies a live root session's whole delegate tree (S3), read
	// from the root's delegate controller when the row is listed. It rides
	// thread/list root rows only, when the tree has at least one subagent, and
	// never a thread/read snapshot: no notification announces its changes.
	Subagents *SubagentTally `json:"subagents,omitempty"`
```

`appwire/clone.go`: in `cloneEvenerThread` after `e.FailedToolCalls = cloneInt(e.FailedToolCalls)`: `e.Subagents = cloneSubagentTally(e.Subagents)`; and after `cloneEvenerUsage`:

```go
func cloneSubagentTally(value *SubagentTally) *SubagentTally {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
```

`server/server.go`, after `appDescendantLiveWatchesFunc`:

```go
	// appSubagentTallyFunc reads the root session's whole-tree subagent tally
	// for the thread list's root row (S3); nil disables it.
	appSubagentTallyFunc func() (appwire.SubagentTally, bool)
```

`server/appwire_runtime.go`, after `SetDescendantLiveWatchesFunc`:

```go
// SetSubagentTallyFunc installs the seam the thread LIST path reads the root
// session's subagent tally through (S3). Like SetDescendantLiveWatchesFunc it
// reaches across the delegate-controller boundary, so the list calls it after
// releasing s.mu. fn reports false for a session with no delegate tree of its
// own; nil disables the tally.
func (s *Server) SetSubagentTallyFunc(fn func() (appwire.SubagentTally, bool)) {
	s.mu.Lock()
	s.appSubagentTallyFunc = fn
	s.mu.Unlock()
}
```

after `attachLiveWatches`:

```go
// attachSubagentTally stamps the root row (data[0]) with its tree's subagent
// tally. A nested delegate's lifecycle change is emitted on its owner's stream
// and never samples the root's envelope, so the tally is read when the row is
// listed rather than cached. A tree with no subagent carries none.
func (s *Server) attachSubagentTally(data []appwire.Thread) {
	s.mu.RLock()
	fn := s.appSubagentTallyFunc
	s.mu.RUnlock()
	if fn == nil || len(data) == 0 {
		return
	}
	tally, ok := fn()
	if !ok || tally.Running+tally.Failed+tally.Done == 0 {
		return
	}
	data[0].Evener.Subagents = &tally
}
```

and in `handleAppThreadList`, after `s.attachLiveWatches(data)`: `s.attachSubagentTally(data)`.

`cmd/evener/serve.go`: in `serveServer` after `SetDescendantLiveWatchesFunc(...)`, add `SetSubagentTallyFunc(func() (appwire.SubagentTally, bool))`; in `bridgeSession`, after the `srv.SetDescendantLiveWatchesFunc(...)` call:

```go
		// The root's row carries its whole tree's subagent tally (S3), read from
		// the delegate controller when the list is served. Set per session so it
		// follows the replacement a thread/clear installs.
		srv.SetSubagentTallyFunc(s.SubagentTally)
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./server -count=1`, `go test ./appwire -count=1`, `go test ./cmd/evener -run 'TestServeInstallsTheRootsSubagentTallyAndFollowsClear|TestRunServeClear' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `SubagentTally` and `subagents?: SubagentTally` on `EvenerThread`.

Gates, per module (root and `agent/`): `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`; `golangci-lint run ./appwire/ ./cmd/evener/`, `cd agent && golangci-lint run .`, `golangci-lint run --config .golangci-appwire.yml ./server/...`, `make lint-generated`.

- [ ] **Step 5: Commit and open PR 18**

```bash
git add appwire/types.go appwire/clone.go appwire/clone_test.go server/server.go server/appwire_runtime.go server/appwire_subagent_tally_test.go cmd/evener/serve.go cmd/evener/serve_state_test.go cmd/evener/serve_subagent_tally_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(server): thread/list root rows carry the tree's subagent tally"
```

Title "feat(server): the daemon's subagent tally (S3a, phase 7 PR 18)". The body says the tally counts every delegate in the controller's folded journal by the Subagents list's own terminal rule, rides `thread/list` root rows only, and nothing consumes it until PR 19.

---

## PR 19: tallies on rows, S3b (Tasks 19.1-19.3)

**Branch:** `git fetch origin && git switch -c claude/s3b-subagent-tallies origin/main`. PR 18 and PR 17 are on main, and a TestFlight build containing #2473 exists.

**What it adds.** The hub carries each live root's tally from its probe to a `subagents` field on the root's rows, and a remote host's rows carry theirs. The phone's fallback (counting loaded children, which misses nested and capped subagents) retires as the handoff section says.

### Task 19.1: Live entries and tree rows carry the tally

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal, `:148-167`)
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry` `:28-87`; `ProbeResult` `:90-122`; `liveEntryFromProbe` `:1309`; the end of `rosterFingerprint`'s loop)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode` `:425-473`; a `subagentsFor` closure beside `turnEndedAtFor`; `buildNode`; the live-only leaf; the NeedsYou node)
- Create: `cmd/evener-hub/internal/hubcore/subagent_tally_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go`

**Interfaces:**
- Consumes: `EvenerThread.Subagents` (PR 18).
- Produces: `ProbeResult.Subagents`, `LiveEntry.Subagents` and `TreeNode.Subagents`, all `appwire.SubagentTally` values (zero when unknown or empty).

- [ ] **Step 1: Write the failing scenarios**

`cmd/evener-hub/internal/hubcore/subagent_tally_test.go`:

```go
package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheTreesSubagentTally: the probe keeps the
// listed root's whole-tree tally (S3).
func fuzzScenarioStatusProber_KeepsTheTreesSubagentTally(t *testing.T) {
	want := appwire.SubagentTally{Running: 2, Failed: 1, Done: 57}
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_tally",
		state:     appwire.ThreadStatusActive,
		setup: func(srv *server.Server) {
			srv.SetSubagentTallyFunc(func() (appwire.SubagentTally, bool) { return want, true })
		},
	})
	if got := prober.Probe(entry); !got.OK || got.Subagents != want {
		t.Fatalf("probe = %+v, want the tally %+v", got, want)
	}
}

// fuzzScenarioRoster_FingerprintMovesWithTheSubagentTally: a subagent failing
// changes the row's last line, so navigation must invalidate. The counts move
// only on a delegate's lifecycle, never on a probe tick.
func fuzzScenarioRoster_FingerprintMovesWithTheSubagentTally(t *testing.T) {
	entry := func(tally appwire.SubagentTally) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusActive, Subagents: tally}}
	}
	running := entry(appwire.SubagentTally{Running: 2})
	oneFailed := entry(appwire.SubagentTally{Running: 1, Failed: 1})
	oneDone := entry(appwire.SubagentTally{Running: 1, Done: 1})
	if rosterFingerprint(running) == rosterFingerprint(oneFailed) || rosterFingerprint(oneFailed) == rosterFingerprint(oneDone) {
		t.Fatal("the roster fingerprint held when a subagent ended")
	}
}

// fuzzScenarioBuildTree_RootRowsCarryTheTreesSubagentTally: a live root's
// Live, project and NeedsYou rows carry its tally from one closure; its
// subagent rows and an ended session carry none.
func fuzzScenarioBuildTree_RootRowsCarryTheTreesSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tally := appwire.SubagentTally{Running: 1, Failed: 1, Done: 2}
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01CHILD"}, Subagents: tally}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ROOT")
	if !inLive || !inProject || liveRow.Subagents != tally || projectRow.Subagents != tally {
		t.Fatalf("Live row %+v (%v), project row %+v (%v): both must carry %+v", liveRow.Subagents, inLive, projectRow.Subagents, inProject, tally)
	}
	needsYou := false
	for _, row := range tree.NeedsYou {
		if row.ID == "01ROOT" {
			needsYou = row.Subagents == tally
		}
	}
	if !needsYou {
		t.Fatalf("NeedsYou = %+v, want the awaiting root carrying %+v", tree.NeedsYou, tally)
	}
	if len(liveRow.Children) != 1 || liveRow.Children[0].Subagents != (appwire.SubagentTally{}) {
		t.Fatalf("children = %+v, want one subagent row with no tally", liveRow.Children)
	}
	if _, _, ended, found := liveAndProjectRowsFor(tree, "01ENDED"); !found || ended.Subagents != (appwire.SubagentTally{}) {
		t.Fatalf("ended session's row = %+v (found %v), want no tally", ended, found)
	}
}
```

Register the three in `FuzzHubcoreScenarios`, in alphabetical order.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`Subagents` undefined on `ProbeResult`).

- [ ] **Step 3: Implement**

`roster.go`, in `ProbeResult` after `LastTurnEndedAt`:

```go
	// Subagents is the listed root's whole-tree subagent tally (S3); zero from
	// a daemon that predates it and for a tree with no subagent.
	Subagents appwire.SubagentTally
```

`prober.go`, before `Probe`'s `return ProbeResult{`:

```go
	var subagents appwire.SubagentTally
	if root.Evener.Subagents != nil {
		subagents = *root.Evener.Subagents
	}
```

and in the literal: `Subagents: subagents,`.

`roster.go`, in `LiveEntry` after `LastTurnEndedAt`:

```go
	// Subagents is the root's whole-tree subagent tally from its probe (S3). It
	// renders on the row's last line and Subagents chip, so rosterFingerprint
	// hashes it; the counts move only when a subagent's run starts or ends.
	Subagents appwire.SubagentTally
```

`liveEntryFromProbe`: `Subagents: result.Subagents,`. In `rosterFingerprint`, after the turn-end lines from Task 17.2, at the end of the loop body:

```go
		// A subagent failing or finishing changes the row's last line (S3).
		tally := bySess[id].Subagents
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.Itoa(tally.Running) + "/" + strconv.Itoa(tally.Failed) + "/" + strconv.Itoa(tally.Done)))
```

`tree.go`, in `TreeNode` after `TurnEndedAt`:

```go
	// Subagents is a live root's whole-tree subagent tally (LiveEntry.Subagents,
	// S3). Every builder sets it from one closure; subagent rows and ended
	// sessions have none.
	Subagents appwire.SubagentTally
```

Beside `turnEndedAtFor`:

```go
	// subagentsFor resolves a live root's subagent tally from the same live map,
	// so its Live, project and NeedsYou rows agree (S3).
	subagentsFor := func(id string) appwire.SubagentTally {
		return liveMap[id].Subagents
	}
```

`buildNode`: after `turnEndedAt := turnEndedAtFor(m.ID)` add `subagentTally := subagentsFor(m.ID)` (not `subagents`: `buildNode` already declares a `subagents` slice of child metas further down), clear it in the `parentDead` branch with `subagentTally = appwire.SubagentTally{}`, and set `Subagents: subagentTally,` after `TurnEndedAt: turnEndedAt,`. The live-only leaf: `Subagents: subagentsFor(le.SessionID),` after its `TurnEndedAt`. The NeedsYou node: `Subagents: le.Subagents,` after its `TurnEndedAt`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/subagent_tally_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries and root rows carry the tree's subagent tally"
```

### Task 19.2: Rows carry `subagents`

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationSubagentTally`; `NavigationSessionSummary.Subagents` after `MoreSubagents`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`projectShallow`; `cloneNavigationSummary`)
- Modify: `cmd/evener-hub/navigation_schema.go` (`navigationSessionValueValid`'s final return `:445`; a `navigationSubagentTallyValid` predicate after `navigationTaskProgressValid` `:448-457`)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`SUBAGENT_TALLY_KEYS`; `SESSION_KEYS`; `sessionValue`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json`
- Create: `cmd/evener-hub/navigation_subagent_tally_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `TreeNode.Subagents` (Task 19.1).
- Produces: `hubapi.NavigationSubagentTally{Running, Failed, Done int}` (JSON `running`, `failed`, `done`); `NavigationSessionSummary.Subagents *NavigationSubagentTally` (`subagents,omitempty`); TS `NavigationSubagentTally` and `NavigationSessionSummary.subagents?: NavigationSubagentTally`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_subagent_tally_test.go`:

```go
package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A root's row carries its whole tree's tally even when the row cannot carry
// every child: 60 subagents are more than the row's children cap, and nested
// ones never appear as its children at all. The tally is the daemon's count,
// not the rows' (S3, Review Focus 5). A root with no subagents carries no key.
func TestNavigationRowsCarryTheWholeTreesSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01QUIET", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	for i := range 60 {
		metas = append(metas, schema.SessionMeta{
			ID: fmt.Sprintf("01CHILD%02d", i), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Duration(i) * time.Second),
			ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"},
		})
	}
	tally := appwire.SubagentTally{Running: 2, Failed: 1, Done: 57}
	live := []hubcore.LiveEntry{
		{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive, Subagents: tally},
		{PID: 2, SessionID: "01QUIET", Status: appwire.ThreadStatusIdle},
	}
	tree := hubcore.BuildTreeAt(metas, live, nil, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]hubapi.NavigationSessionSummary{}
	for _, row := range projection.LivePage(0, 0).Sessions {
		rows[row.SessionID] = row
	}
	root := rows["01ROOT"]
	if len(root.Children) >= 60 {
		t.Fatalf("root row carries %d children; the fixture must exceed the row's cap", len(root.Children))
	}
	if want := (hubapi.NavigationSubagentTally{Running: 2, Failed: 1, Done: 57}); root.Subagents == nil || *root.Subagents != want {
		t.Fatalf("root tally = %+v, want %+v", root.Subagents, want)
	}
	for _, child := range root.Children {
		if child.Subagents != nil {
			t.Fatalf("subagent row %s carries a tally %+v", child.SessionID, child.Subagents)
		}
	}
	if _, present := navigationSummaryJSONFields(t, rows["01QUIET"])["subagents"]; present {
		t.Fatal("a root with no subagents carries the subagents key")
	}
}

// A tally the schema would refuse, which only a malformed daemon answer can
// carry, is dropped from the row instead of failing the whole resource.
func TestNavigationRowsDropAMalformedSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []hubcore.LiveEntry{{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive, Subagents: appwire.SubagentTally{Running: 2, Failed: -1}}}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.BuildTreeAt(metas, live, nil, now)})
	if err != nil {
		t.Fatalf("a malformed tally failed the build: %v", err)
	}
	if rows := projection.LivePage(0, 0).Sessions; len(rows) != 1 || rows[0].Subagents != nil {
		t.Fatalf("rows = %+v, want the root with no tally", rows)
	}
}

// The hub schema refuses a tally the codec would refuse.
func TestNavigationSchemaRefusesANegativeSubagentCount(t *testing.T) {
	valid := hubapi.NavigationSessionSummary{Ref: "local:01A", HostID: "local", SessionID: "01A", State: "active", Kind: "session", Subagents: &hubapi.NavigationSubagentTally{Running: 1}}
	if !navigationSessionValueValid(valid) {
		t.Fatal("a valid tally was refused")
	}
	valid.Subagents = &hubapi.NavigationSubagentTally{Failed: -1}
	if navigationSessionValueValid(valid) {
		t.Fatal("a negative subagent count was accepted")
	}
}
```

In `codec.test.ts`, after the test from Task 17.3:

```ts
// S3: a live root's row carries its whole tree's subagent tally.
test("codec accepts a row's subagent tally and refuses a malformed one", () => {
  const session = entityKey(key, "1");
  const statusFor = (subagents: unknown) =>
    decodeNavigationResponse(
      key,
      undefined,
      snapshotResponse(key, {
        ...liveSnapshot(),
        entities: [{ key: session, kind: "session", value: { ...sessionValue("local:session"), subagents } }],
      }),
    ).status;

  expect(statusFor({ running: 2, failed: 1, done: 57 })).toBe("snapshot");
  expect(() => statusFor({ running: 2, failed: -1, done: 57 })).toThrow();
  expect(() => statusFor({ running: 2, failed: 1 })).toThrow();
  expect(() => statusFor(3)).toThrow();
});
```

In `value-records.json`, after `"more_subagents": 3,` in the session record:

```json
    "subagents": { "running": 2, "failed": 1, "done": 5 },
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryTheWholeTreesSubagentTally|TestNavigationRowsDropAMalformedSubagentTally|TestNavigationSchemaRefusesANegativeSubagentCount|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`hubapi.NavigationSubagentTally` undefined).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: FAIL: the value-records test drops `subagents`, and the malformed tallies are dropped rather than refused.

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, before `NavigationSessionSummary`:

```go
// NavigationSubagentTally is a live root's whole-tree subagent tally (S3):
// every subagent at every depth, running, failed (its latest run ended failed
// or exhausted) or done.
type NavigationSubagentTally struct {
	Running int `json:"running"`
	Failed  int `json:"failed"`
	Done    int `json:"done"`
}
```

and in `NavigationSessionSummary` after `MoreSubagents`:

```go
	// Subagents is a live root's whole-tree subagent tally, counted by its
	// daemon (S3). Present only on a live root row whose tree has a subagent;
	// it counts subagents the row's children never show (nested, or past the
	// children cap).
	Subagents *NavigationSubagentTally `json:"subagents,omitempty"`
```

`navigation_projection.go`, in `projectShallow`'s literal after `MoreSubagents: node.MoreSubagents,`: `Subagents: navigationSubagentTally(node.Subagents),`; after `projectShallow`:

```go
// navigationSubagentTally is a root row's tally on the wire: absent when the
// tree has no subagent, and dropped when the schema would refuse it (a
// negative count, which only a malformed daemon answer can carry), the way
// navigationTaskProgress drops bad progress rather than fail the resource.
func navigationSubagentTally(tally appwire.SubagentTally) *hubapi.NavigationSubagentTally {
	wire := hubapi.NavigationSubagentTally{Running: tally.Running, Failed: tally.Failed, Done: tally.Done}
	if wire.Running+wire.Failed+wire.Done == 0 || !navigationSubagentTallyValid(wire) {
		return nil
	}
	return &wire
}
```

In `cloneNavigationSummary`, after the `TurnEndedAt` copy:

```go
	if summary.Subagents != nil {
		tally := *summary.Subagents
		clone.Subagents = &tally
	}
```

`navigation_schema.go`: end `navigationSessionValueValid` with

```go
	return (value.Tasks == nil || navigationTaskProgressValid(*value.Tasks)) &&
		(value.Subagents == nil || navigationSubagentTallyValid(*value.Subagents))
```

and add after `navigationTaskProgressValid`:

```go
// navigationSubagentTallyValid mirrors the web codec's subagentTallyValue: every
// count is a safe non-negative integer. The projector drops a tally this
// refuses rather than failing the whole resource over it.
func navigationSubagentTallyValid(tally hubapi.NavigationSubagentTally) bool {
	return navigationIntCount(tally.Running) && navigationIntCount(tally.Failed) && navigationIntCount(tally.Done)
}
```

`codec.ts`, before `SESSION_KEYS`:

```ts
const SUBAGENT_TALLY_KEYS = valueRecordKeys(["running", "failed", "done"]);
```

add `"subagents"` to `SESSION_KEYS`' optional list after `"more_subagents"` and `subagents: SUBAGENT_TALLY_KEYS` to its nested map; after `tasksValue`:

```ts
const subagentTallyValue = (value: unknown): boolean =>
  knownKeys(value, SUBAGENT_TALLY_KEYS) && count(value.running) && count(value.failed) && count(value.done);
```

and in `sessionValue` after its `more_subagents` line: `optional(value.subagents, subagentTallyValue) &&`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, `make generate`, and `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNavigation|TestCloneNavigationSummary' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_schema.go cmd/evener-hub/navigation_subagent_tally_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): live root rows carry their whole tree's subagent tally"
```

### Task 19.3: A remote host's rows carry their tally

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry`; `threadFromEntry`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_subagent_tally_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster`)
- Modify: `cmd/evener-hub/web_api_tree.go` (`appThreadTreeEntries`)
- Create: `cmd/evener-hub/remote_subagent_tally_test.go`

**Interfaces:**
- Consumes: `LiveEntry.Subagents` (Task 19.1); `EvenerThread.Subagents` (PR 18); the summary field (Task 19.2).
- Produces: `appsource.LocalDaemonEntry.Subagents appwire.SubagentTally`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_subagent_tally_test.go`:

```go
package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's tally on
// the row when its tree has a subagent, and no key otherwise (S3).
func TestLocalDaemonSourceListCarriesTheRootsSubagentTally(t *testing.T) {
	tally := appwire.SubagentTally{Running: 2, Failed: 1, Done: 5}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/tree", ThreadID: "th_tree", SessionID: "sess_tree"}, Status: appwire.ThreadStatusActive, Subagents: tally},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/alone", ThreadID: "th_alone", SessionID: "sess_alone"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.SubagentTally{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.Subagents
	}
	if !reflect.DeepEqual(byID["th_tree"], &tally) || byID["th_alone"] != nil {
		t.Fatalf("tallies = %+v, want th_tree's %+v and none on th_alone", byID, tally)
	}
}
```

`cmd/evener-hub/remote_subagent_tally_test.go`:

```go
package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's tally and never lend it to the root's
// in-process subagent aliases.
func TestLocalDaemonEntriesFromRosterCarryTheTallyOnlyOnTheRoot(t *testing.T) {
	tally := appwire.SubagentTally{Running: 1, Done: 2}
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"sess_child"}, Subagents: tally,
	}})
	if len(entries) != 2 || entries[0].Subagents != tally || entries[1].Subagents != (appwire.SubagentTally{}) {
		t.Fatalf("entries = %+v, want the root's tally %+v and none on its alias", entries, tally)
	}
}

// A controller reads a remote host's tally off its list row, so the remote
// live row carries subagents exactly like a local one (S3).
func TestNavigationRemoteLiveRowCarriesItsSubagentTally(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "th_1", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Ref: "host-a:th_1", Subagents: &appwire.SubagentTally{Running: 3, Failed: 1}},
	}})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "host-a"}, online: true})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", RemoteThreadCache: cache, Past: hubcore.NewPastIndex("")})
	web.sources = registry

	snapshot := web.navigationSnapshotInputs(t.Context())
	tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, map[hubcore.ArchiveKey]bool{}, snapshot.projects)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, web.apiTreeSources(), hubapi.AttentionSummary{}, snapshot.live, nil, nil, nil, nil)
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	row := navigationProjectedSummary(t, projection, "host-a:th_1")
	if want := (hubapi.NavigationSubagentTally{Running: 3, Failed: 1}); row.Subagents == nil || *row.Subagents != want {
		t.Fatalf("remote row tally = %+v, want %+v", row.Subagents, want)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListCarriesTheRootsSubagentTally' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterCarryTheTallyOnlyOnTheRoot|TestNavigationRemoteLiveRowCarriesItsSubagentTally' -count=1`
Expected: FAIL to compile (`Subagents` is not a field of `LocalDaemonEntry`); once it exists, the remote row test fails with no tally.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `LastTurnEndedAt`:

```go
	// Subagents mirrors hubcore.LiveEntry.Subagents: the root's whole-tree
	// subagent tally (S3). threadFromEntry carries it into
	// appwire.EvenerThread.Subagents when the tree has a subagent. A read-only
	// alias has none.
	Subagents appwire.SubagentTally
```

and in `threadFromEntry`, after the `thread := appwire.Thread{...}` literal:

```go
	if tally := item.Subagents; !item.ReadOnlyAlias && tally.Running+tally.Failed+tally.Done > 0 {
		thread.Evener.Subagents = &tally
	}
```

`app_rpc.go`, in `localDaemonEntriesFromRoster`: add `Subagents: item.Subagents,` to the root entry's literal, and in the child loop beside `child.LastTurnEndedAt = 0`: `child.Subagents = appwire.SubagentTally{} // the root's tree is not the child's`.

`web_api_tree.go`, in `appThreadTreeEntries` after the `LastTurnEndedAt` line from Task 17.5:

```go
	if thread.Evener.Subagents != nil {
		entry.Subagents = *thread.Evener.Subagents
	}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemoteLiveRow|TestNavigationOffline' -count=1`
Expected: PASS.

Gates: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`, `golangci-lint run ./cmd/evener-hub/... ./hubapi/`, `make lint-generated`, `make secret-scan` (the fixture changed).

- [ ] **Step 5: Commit and open PR 19**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_subagent_tally_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/remote_subagent_tally_test.go
git commit -m "feat(hub): remote hosts' rows carry their subagent tally"
```

Title "feat(hub): subagent tallies on rows (S3b, phase 7 PR 19)". The body says live root rows carry `subagents` (running, failed, done over the whole tree, from the daemon's controller), remote rows too, and names the TestFlight build containing #2473 that it waited for.

---

## Self-review

- **Spec coverage.** Spec 18's S5 row (activity buckets per live session over the last 7 to 10 minutes, and the last activity time): PRs 14 and 15, seven one-minute bars and a quiet time the hub withholds while subagents run (ruling 6). The S4 row (a per-user seen-through marker, a method to set it, included in summaries): PRs 16 and 17, the `session_seen` store, `evener/session/seen/set`, and `turn_ended_at` and `unseen` on rows. The S3 row (running, failed and done per top-level session, omitted descendants included): PRs 18 and 19. Spec 13.1's Quiet (3 minutes) and May be stuck (10 minutes): `quietState` (Task 15.4). Spec 7.3's Mark as read and Mark as unread: `seenThrough` and `unread` marks (Task 17.4). Spec 9's failed subagent: ruling 21 and Question 1. Jesse's S5 ruling: rulings 3 and 6 and Review Focus 1.
- **Checked by running it.** Every task's code was applied in order onto main `328685d4f`, PRs 14 to 19 together, and each task's named tests passed, with `make generate` and `TestGeneratedFileCurrent`, the pinned golangci-lint (default and `.golangci-appwire.yml`, root module and `agent/`), `go vet -tags evenerfuzz` for the host and `GOOS=windows`, vitest, `npm run typecheck` and Biome. Not exercised: the package qualification smoke calls (Task 15.4, `make test-api-package`) and the README line. On macOS three existing tests fail on unmodified main and have nothing to do with this plan: `agent`'s `TestDelegateResourceBootstrap_MetadataEACCESPreservesResumability` and `TestJobActivityTree_LiveRootChildContinuationSurvivesHistoricalEpochs`, and `cmd/evener`'s `TestRunServeClearRetainsReferencedScratchWhenConstructionFailsAfterRetention` (a `/var` against `/private/var` temp path mismatch). An implementer who meets them should not chase them in these PRs.
- **Placeholders.** None: every task carries its code, and the phone lane's changes are a handoff, not tasks.
- **Names.** `ThreadActivity`, `CloneThreadActivity`, `activityMeter`; `ActivityReadParams`, `ActivityReadResponse`, `SessionActivity` (`quietForMs`), `LiveRowRef`, `maxActivityReadRefs`, `decodeActivityRead`, `quietState`; `SessionMeta.LastTurnEndedAt`, `EvenerThread.LastTurnEndedAt`, `LiveEntry.LastTurnEndedAt`, `TreeNode.TurnEndedAt`, `turn_ended_at`, `unseen`; `SessionSeenStore`, `SessionSeenSnapshot`, `SessionSeenRecord`, `UnixMilliTime`, `UnixMilliseconds`, `SessionSeenSetParams`, `SessionSeenMark`, `SessionSeenSetResponse`, `maxSessionSeenMarks`, `commitNavigationChange`, `seenThroughToMark`, `useMarkSessionSeenOnOpen`; `appwire.SubagentTally`, `delegateRunTerminal`, `Session.SubagentTally`, `SetSubagentTallyFunc`, `NavigationSubagentTally`, `navigationSubagentTally`, `navigationSubagentTallyValid`. Each is used the same way wherever it appears.
- **Review Focus.** Item 1: Tasks 15.3 and 15.4. Item 2: Task 15.1. Item 3: Tasks 17.2 and 17.3. Item 4: Tasks 17.1 and 17.3. Item 5: Tasks 18.1 and 19.2.
- **Against the server plan's sketches.** The server plan's S5, S4 and S3 sections now point here. This plan departs from their sketches where the code argued for it: a quiet time instead of `lastActivityAt` (ruling 6), ten-second slots (ruling 4), `unseen` computed on the hub instead of `seen_through` on rows (ruling 12), one mark per row with an explicit unread (ruling 14), live rows never cluster (ruling 16), and the controller's journal instead of `aggregateActivity`, with no `complete` flag (ruling 19).
