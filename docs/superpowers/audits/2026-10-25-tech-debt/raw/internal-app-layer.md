# Tech-debt audit: internal-app-layer

Slice: `internal/appserver`, `internal/apptranscript`, `internal/appoverlay`, `internal/transcriptindex`, `internal/appprojector`, `internal/apphistory`, `internal/appitempaging`, `internal/appwirets`, `internal/appwiredoc` (non-test code).
Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no tests were run.

## Overview and inventory

| Package | non-test files | non-test LOC | test files | test LOC |
|---|---|---|---|---|
| appserver | 8 | 3600 | 35 | 10406 |
| apptranscript | 12 | 3350 | 31 | 6135 |
| transcriptindex | 10 | 4807 | 27 | 7725 |
| appprojector | 1 | 652 | 7 | 1864 |
| appoverlay | 4 | 1036 | 3 | 1127 |
| apphistory | 2 | 467 | 3 | 534 |
| appitempaging | 3 | 391 | 4 | 739 |
| appwirets | 2 | 717 | 2 | 1183 |
| appwiredoc | 1 | 260 | 2 | 543 |
| total | 43 | 15280 | 114 | 30256 |

Largest files: `appserver/server.go` 2327, `transcriptindex/index.go` 1420, `apptranscript/apptranscript.go` 1380, `transcriptindex/build.go` 1006, `transcriptindex/window.go` 1000.
Longest functions (>=100 lines): `projectTurn` 414 (apptranscript.go:484), `AppEventProjector.Project` 356 (appwire_projection.go:81), `captureSubscription` 132 (server.go:1406), `fill` 105 (apphistory/paging.go:188).

Overall health: the code is carefully reasoned and heavily commented, and most of the packages hold up (appoverlay, apphistory, appitempaging are small and coherent). The debt concentrates in four places:

1. A legacy-format transcript projection path (communicate deferral, registry, flush, index builder mirror) that survives next to the identity-format path introduced on 2026-09-26.
2. `appserver/server.go`, a 2.3k-line file that mixes connection lifecycle, dispatch policy, force-stop recovery, hydration capture, and a subscription admission API that carries two resolver generations and some dead entry points.
3. Drifted copies of small helpers: delegate-info mapping and clones, tool-intent extraction, failure counting, `cloneThreadItem`, pointer clones.
4. Comments that cite symbols and files that no longer exist, and coverage-driven seams in production code.

I did not read `transcriptindex/index.go`/`records.go`/`cache.go` line by line (persisted-format internals); findings there are limited to what I opened.

Product-model notes used for fix plans: recovery over lockdown; the shared TS client owns shared derivations (nothing here touches it); evergreen docs must change with the code (`docs/product/subsystems.md` rows for the transcript read model and AppWire server).

---

## Finding 1 (high, decision needed): legacy-format transcript projection runs beside the identity format

Locations:
- `internal/apptranscript/apptranscript.go:444-482` (`ProjectEntryParts` routes `turn.Format != schema.TurnFormatIdentity` to the legacy path), `:484-904` (`projectTurn` assistant/tool-result cases with communicate deferral via `reg.CommRawArgs`), `:410-420` (`communicateRawFallbackText`), `:142-209` (`CommunicateMessageFromArguments`, `NormalizeCommunicateArguments`)
- `internal/apptranscript/flush_communicate.go:1-102` (whole file), `internal/apptranscript/logical_turn.go:80-319` (accumulator, `TurnGrouper`, `mergeGroupedItems`, `MergeThreadItems`)
- `internal/transcriptindex/build.go:105` ("state applies to legacy entries only"), `:195-262` (`applyLegacy`), `:264-393` (`registryCommArgs`, `communicateResultCallID`, `communicateContext`, `recordCommState`), `:806-847` (`lookup`/`recordNames`/`setName`), `transcriptindex/window.go:560-690` (`remapFlushPositions`, `pendingFlush`, `pendingRegistry`)
- `internal/transcriptindex/index.go:256` (`persistedCommCall`), `build.go:931-953` (`turnKindLegacy` branches in `stampTurn`)

Evidence:
- `agent/schema` introduced `TurnFormatIdentity` in commit `1268e1282e` (2026-09-26, "phase 2a"); `agent/transcript/placement.go:81` stamps `turn.Format = schema.TurnFormatIdentity` on every entry written now. So every legacy entry is at least one month old; every new entry takes the identity path (`projectIdentityAssistant`, apptranscript.go:906, plus `CommunicateItem`/`NoticeItem`).
- The legacy path needs its own machinery: `ToolCallRegistry{Names, CommRawArgs, LastAssistantText, LastAssistantTurnID}` (apptranscript.go:32), `argrepair.RepairJSON` replay (apptranscript.go:817, flush_communicate.go:34), an echo-suppression state machine, 31 `communicate` mentions in build.go alone, and a persisted `commCalls` snapshot in the index meta.
- The index builder re-implements ProjectTurn's tool-name tracking (`recordNames`, build.go:818: "the ones apptranscript.ProjectTurn makes") and `communicateResultCallID` (build.go:286: "matching the name resolution apptranscript.ProjectTurn itself applies"). Parity is held by tests (`transcriptindex/reference_test.go`, `derived_totals_parity_test.go`), not by shared code.
- Rough size of the legacy-only code, by my count of the ranges above: about 1,000 non-test lines across the three packages, plus the tests that pin it.

Impact: this is the largest single source of complexity in the slice, and every change to communicate/tool-result projection must be made in two or three places with parity tests. It is also existing backward compatibility, so removing it needs Jesse's decision about whether pre-2026-09-26 transcripts must keep rendering on reload.

Fix plan (only after Jesse decides):
- Option A (keep legacy reads, make them cheaper): convert legacy transcripts once on first open into identity format (the daemon already knows how to write COMMUNICATE/NOTICE entries), then delete the read-side legacy path. One-shot migration with a backup copy, tested against the recorded fixtures in `agent/testdata`.
- Option B (drop legacy reads): legacy sessions open as a stub "transcript predates this version" thread with recovery guidance. This conflicts with "preserve the user's work" in AGENTS.md, so I recommend A over B.
- Either way: delete `flush_communicate.go`, `CommRawArgs`, `LastAssistant*` on the registry, `applyLegacy`, `commCalls` persistence, `pendingFlush`, and `NormalizeCommunicateArguments`; bump the index `projectionID`; update `docs/product/subsystems.md` transcript-read-model row.
- Test strategy: before deleting, snapshot reload output for each legacy fixture; after migration, assert the identity-format projection matches (behavior parity, not copy).

Size: roughly -1,000 / +150 (migration). Risk: high; touches persisted index format and reload fidelity. Must be a decided, staged change.

---

## Finding 2 (medium): two subscription-admission resolver generations coexist

Locations: `internal/appserver/server.go:48-55` (config fields), `:77-90` (V2 types), `:1989-2010` (`executeOrdered` merges both), `:2066-2085` (`safeResolveAdmission`, `safeResolveAdmissionV2`); users `server/server.go:506-516` (V1, local runtime) and `cmd/evener-hub/app_rpc.go:1060` (V2, hub).

Evidence:
- `ServerConfig.SubscriptionAdmissionResolver func(Message) (string, bool)` and `SubscriptionAdmissionResolverV2 func(Message) SubscriptionAdmissionResolution` do the same job. `executeOrdered` has two near-identical 130-character conditions (server.go:1995 and :1999), then synthesizes a V2 `intent` from a V1 result (`:2001-2006`) using `threadReadAdmissionEligible`, which re-decodes the params.
- `safeResolveAdmission` and `safeResolveAdmissionV2` are the same recover-and-log wrapper with different result shapes.
- The method/param decoding is also duplicated outside: `server/server.go:556-577` (`decodeSubscriptionAdmissionParams`, with its own fuzz target) and the inline decode in `app_rpc.go:1061-1072` decode the same two methods' params.
- The "V2" suffix is a history name, not a domain name.

Impact: any change to admission semantics has to be made in two resolvers and three code paths; the local runtime silently gets weaker behavior (no `Invalid` intent, so a malformed `thread/read` is routed differently).

Fix plan:
1. Port `server/server.go`'s resolver to return `SubscriptionAdmissionResolution` (map `(target,true)` to `Resolved`, `("",false)` on an eligible read to `Unresolved`, undecodable to `Invalid`).
2. Delete the V1 field, `safeResolveAdmission`, the V1 branch in `executeOrdered`, and rename V2 to `SubscriptionAdmissionResolver`.
3. Move the shared "decode thread/read or thread/unsubscribe params" step into `appserver` (exported helper), delete the hub's inline copy and `decodeSubscriptionAdmissionParams`; keep one fuzz target on the shared helper.
- Tests: the existing `admission_resolution_test.go` and `subscription_admission_ordering_test.go` become the guard; add a behavior test that the local runtime now reports invalid-target errors the way the hub does (TDD, this is a small behavior change).
- Docs: AppWire server row in `docs/product/subsystems.md` if it names the resolver.

Size: about -90 / +40. Risk: medium (subscription ordering is subtle; the existing ordering tests are good).

---

## Finding 3 (medium): dead and test-only API on the subscription and notifier surface

Locations:
- `internal/appserver/server.go:692` (`Connection.Subscribe`), `:698` (`Connection.ReplaceSubscriptions`), `:1157` (package `Subscribe(ctx, threadID)`), `:1324` (package `ReplaceSubscriptions(ctx, threadID)`), `:548` (`Server.SetBeforeSubscriptionGate`)
- `internal/appserver/subscriptions.go:58` (`Subscriptions.Subscribe`), `:132` (`ReplaceConnectionSubscriptions`), `:261` (`IsSubscribed`), `:283` (`Connections`)
- `internal/appserver/notifier.go:81` (`RetainedWindow`), `:102` (`RetainedWindowCurrent`), `:14-20` (`RetainedNotificationWindow`)

Evidence (repo-wide grep excluding `_test.go`):
- `appserver.Subscribe(` has no caller; package-level `ReplaceSubscriptions` is only called from `appstack_seqfuzz_test.go:44-51`, `server_gaps_test.go:220`, `server_test.go:1691`; `Connection.ReplaceSubscriptions` only from tests (`hub_multisession_seqfuzz_test.go:215`, `server_test.go:570`).
- `IsSubscribed` has one non-test mention, its own definition, against 49 test uses. `Subscriptions.Connections` has no non-test caller.
- `RetainedWindow` and `RetainedWindowCurrent` have no non-test caller anywhere (only `server/appwire_rejoin_test.go:23` reads `RetainedWindow(...).UpperSeq`).
- `SetBeforeSubscriptionGate` has no non-test caller; three server tests use it as a hook.
- Real subscriptions go through `CaptureSubscription*`, `SubscribeWithAdmission`, and `Unsubscribe*`.

Impact: roughly 130 lines of production code exist to support tests and fuzzers, and they bypass the gates that the real paths take (`Subscribe` takes `projectionMu` but not `hydrationMu`/`admissionMu`), so tests exercise a path production never uses. The seqfuzz tests (`appstack_seqfuzz_test.go`, `hub_multisession_seqfuzz_test.go`) therefore model an API shape that does not exist in production.

Fix plan:
- Delete the non-test-used methods. Move the few helpers tests need (`IsSubscribed`, `RetainedWindow().UpperSeq`) into `_test.go` files in their packages, or have `server/appwire_rejoin_test.go` use `CurrentSequence`.
- Re-point the seqfuzz tests at `SubscribeWithAdmission`/`CaptureSubscription` so they cover the real gates.
- Keep `SetBeforeSubscriptionGate` only if the three server tests need it; otherwise replace with a package-internal hook (see Finding 6).
- No product docs affected.

Size: about -150 prod, test edits about +/-120. Risk: low.

---

## Finding 4 (medium): `appserver/server.go` is a 2.3k-line file with several unrelated subsystems and a 14-hook test seam on the `Server` struct

Locations: `internal/appserver/server.go:142-198` (struct), `:336-380` (admission bookkeeping), `:599-1055` (Connection, enqueue, hydration finalizers, force-stop recovery flag), `:1056-1590` (context keys, subscribe/capture API), `:1606-1670` (dispatch policy), `:1672-2327` (receive loop, worker, slot pools, panic barriers).

Evidence:
- 2327 lines in one file; `captureSubscription` is 132 lines with 8 parameters and a variadic `resolveTarget ...func() SubscriptionTarget` used as an optional argument (`server.go:1406-1420`, and `len(resolveTarget) != 0` tests at :1430, :1462, :1480).
- The generic JSON-RPC server contains product knowledge: force-stop admission (`recoveryRunning`, `recoveryClearID`, `markRecoveryClear`, `clearRecoveryOnPanic`, `beforeSend`, `afterSend`; server.go:834-945, 1685-1752) keyed to `appwire.MethodEvenerThreadForceStop`.
- The `Server` struct has 14 test-only hook/override fields (`keepaliveTickerFactory`, `keepaliveDecision`, `requestQueueCapacity`, `blockedEnqueue`, `afterWorkerDequeue`, `afterSlowReadAcquire`, `slowReadStallThreshold`, `wrapWebSocketTransport`, `sendWriteTimeout`, `afterUnregisterDelete`, `beforeSubscriptionRegistration`, `beforeSubscriptionBeginBuffered`, `beforeHydrationCommit`, `afterBroadcastConnectionLookup`) with 26 nil-guarded call sites in non-test code.
- appserver tests are 10,406 lines (2.9x the production code) across 35 files, largely pinning these interleavings.

Impact: high navigation cost and merge-conflict surface; reviewers cannot see which locks (`mu`, `projectionMu`, `deliveryMu`, `hydrationMu`, `admissionMu`, `responseMu`, `sendMu`) a given function may take, because the lock-order rules live in prose scattered across the file.

Fix plan (mechanical, behavior-preserving):
1. Split `server.go` by concern within the package: `server.go` (Server, registry, Broadcast), `connection.go` (Connection, send/receive, worker, slot pools), `hydration.go` (finalizers, `captureSubscription`, `SubscribeWithAdmission`, release/withdraw), `recovery.go` (force-stop busy slot), `dispatch.go` (policy; see Finding 5). Pure moves, no edits, one commit per file move, `go build` plus the existing package tests in CI.
2. Replace the optional variadic params with explicit option structs (`captureOptions{target func() SubscriptionTarget, handoff, releaseOnError}`), removing `CaptureSubscription`/`CaptureSubscriptionWithHandoff` as two thin wrappers over one `captureSubscription`.
3. Collect the 14 hooks into one `testHooks` struct field (nil in production) so the production struct reads cleanly; do not remove them (the tests pin real races).
- Docs: add a short lock-order table to the package doc comment (single canonical home), reference from `docs/product/subsystems.md`.

Size: about +0/-0 net for the split (about 2,300 lines moved), -60 for wrapper removal. Risk: low for moves, medium for option-struct change.

---

## Finding 5 (medium): two mechanisms decide which requests leave the serial worker

Locations: `internal/appserver/server.go:1606-1670` (`concurrentDispatchMethod`, hard-coded method list with about 55 comment lines), `:60-76` (`ServerConfig.ConcurrentRequest` and its doc), `:1989-2064` (`executeOrdered` consults both), `:2160-2235` (pool slots), `:2115` (slow-read slots).

Evidence:
- The config doc itself says `ConcurrentRequest` names requests "beyond the built-in slow reads (concurrentDispatchMethod)". The built-in list names product methods such as `MethodEvenerUpdateApply`, `MethodEvenerUpgrade`, `MethodEvenerPluginCheckUpdates`, `MethodEvenerJobsList`, so the "generic" server owns product scheduling policy, while the hub supplies the other half through a callback (`cmd/evener-hub/app_rpc.go:1055`, `concurrentForwardedRequest`).
- Two slot systems with separate caps and separate failure modes: slow reads block the worker when full (`slowReadDispatchCap = 16`), pooled requests are refused Unavailable (`concurrentRequestCap = 32`, `concurrentRequestTotalCap = 128`). `executeOrdered` has two separate `go func()` dispatch blocks (:2038-2050, :2052-2063).

Impact: adding a slow method requires knowing which of two places to edit and which backpressure behavior it will get; the list's rationale is buried in comments inside a `case`.

Fix plan:
- Move the method table into the embedder config: a `map[string]DispatchClass` (or the `ConcurrentRequest` callback) supplied by whoever builds the server, so `appserver` holds mechanism only and the policy lives beside the handlers it describes. `server/server.go` and the hub each pass their list. One `DispatchClass` type with `{Serial, SlowRead, Pooled(pool)}`.
- Keep the two backpressure behaviors, but unify the dispatch code to one `dispatch(class)` function.
- Tests: `websocket_dispatch_test.go` (16 tests) is the guard; add one test that a method unknown to the table stays serial.
- Docs: product subsystems row for the hub RPC server.

Size: about -80 / +60. Risk: medium (scheduling behavior), but the table move can be done first as a pure refactor.

---

## Finding 6 (low): `ServerConfig` has accreted redundant knobs

Locations: `internal/appserver/server.go:29-41` and `:564-567` and `:591-597`.

Evidence:
- `Navigation *NavigationCapability` and `NavigationCapability func() *NavigationCapability`: the hub sets exactly one of them (`app_rpc.go:1028-1051`: `capability = nil` whenever the provider is set), and `initialize` prefers the func when present.
- `AdapterNativeInitialize` ("keeps the shared JSON-RPC server usable in tests for adapters") has no non-test setter in the repo (grep of `AdapterNativeInitialize` outside `server.go` and tests returns nothing).
- `olderProtocolVersion` (server.go:587) is a one-line wrapper around `appwire.OlderProtocolVersion`; `navigationCapability` (:591) is a nil-safe copy.
- `Server.Logf` and `Server.logf` (:381-393) are the same function under two names.

Fix plan: keep only the func field (the static case is `func() ... { return fixed }`), delete `Navigation`; delete `AdapterNativeInitialize` and the test that needs it (or move to a test-local config wrapper); inline the two wrappers; keep one `Logf`. Tests: existing initialize tests (`upgrade_required_test.go`). Size about -35. Risk: low.

---

## Finding 7 (medium): subscription lifecycle carried through six context keys and an unsubscribe function with three code paths

Locations: `internal/appserver/server.go:1056-1059`, `:1205-1262` (`UnsubscribeLifecycle`), `:1265-1320` (`SubscribeWithAdmission`), `:1998-2030` (`executeOrdered` stuffing the keys).

Evidence:
- Context keys: `connectionContextKey`, `requestIDContextKey`, `subscriptionAdmissionFailureContextKey`, `subscriptionAdmissionResolverPanicContextKey`, `subscriptionLifecycleContextKey`, `subscriptionLifecycleContextKeysKey`, `subscriptionUnresolvedContextKey`. Of these, three (`...LifecycleContextKey`, `...KeysKey`, `...UnresolvedContextKey`) encode one value: a `(key, secondaryKey, unresolved)` triple, which `UnsubscribeLifecycle` unpacks with a nested branch (`len(keys) >= 2 && keys[0] != "" && keys[1] != ""`, a loop, and a single-key fallback).
- `Unsubscribe` (:1187) also reads `subscriptionLifecycleContextKey`, so there are two public unsubscribe entry points with overlapping fallbacks; the hub calls `UnsubscribeLifecycle` three times (`app_rpc.go:1529,1532,1539`) and the local runtime calls `Unsubscribe` three times (`server/appwire_runtime.go:1430,1433,1437`).
- `SubscribeWithAdmission` and `captureSubscription` repeat the same gate sequence (`beforeSubscriptionRegistration`, `hydrationMu`, `projectionMu`, `deliveryMu`, `admissionMu`, `claimSubscriptionAdmission`, history defer, `subscribeOwned`/`beginBuffered`, delete pending admission), with the `history` branch of `SubscribeWithAdmission` re-entering `CaptureSubscriptionWithHandoff` with a closure that returns `server.routedSeq` unlocked (server.go:1283).

Impact: any change to unsubscribe semantics touches `executeOrdered`, three context keys, and two public functions; the unlocked read of `server.routedSeq` inside a closure is a data-race smell worth a second look (it is written under `projectionMu`; here the closure runs after `captureSubscription` takes the gates, so it is probably safe, but it is not locally evident).

Fix plan:
1. Replace the three lifecycle keys with one `unsubscribeTarget{primary, secondary string, unresolved bool}` value under a single key, built once in `executeOrdered` and consumed once.
2. Collapse `Unsubscribe` and `UnsubscribeLifecycle` into one function that takes the resolved target when present and the raw key otherwise; update the 6 callers.
3. Extract the shared registration-gate prologue of `SubscribeWithAdmission` and `captureSubscription` into one `lockRegistration(conn)` returning an unlock func.
- Tests: `subscription_rollback_lifecycle_test.go`, `subscription_admission_ordering_test.go`, `hydration_commit_race_test.go` guard behavior; run race lane in CI only.

Size: about -110 / +50. Risk: medium-high (concurrency); do after Findings 2 and 3 shrink the surface.

---

## Finding 8 (medium): `projectTurn` is a 414-line switch with the same ThreadItem scaffolding pasted per kind

Locations: `internal/apptranscript/apptranscript.go:484-904`; helper already exists at `internal/apptranscript/notice.go:44-52` (`entryStartedAt`).

Evidence:
- Seven `case` arms each build an `appwire.ThreadItem{Type:"systemMessage", ID: fmt.Sprintf("item_..._%d", turnIndex), TurnID, TranscriptEntryIndex, Description, Text, Status: Completed, EventKind}` literal (checkpoint/summary :527, model switch :545, environment/notes :563, turn failure :601, hook :624, goal continuation :655), differing only in id prefix, description, event kind.
- `ms := turn.Timestamp.UnixMilli(); item.X = &ms` is written out 5 times in this file (lines 492, 804, 879, 922, 1062) plus `logical_turn.go:223`, `notice.go:50`, and `appoverlay/overlay.go:466`, although `entryStartedAt` (notice.go:44) already implements it for `StartedAt`.
- The function mixes the tool-result/communicate state machine (lines 767-880) with plain marker kinds; two deferred closures (timestamp stamping at :487, invariant check at :515) wrap it.
- `projectIdentityAssistant` (:906) repeats the reasoning/web-search/tool-call arms of the legacy assistant case; `redactedThinkingText` handling exists in two forms (:722 vs :984-1011).

Impact: every new system-message kind pays about 15 lines of scaffolding and risks a field omitted in one arm (TurnID/TranscriptEntryIndex are set in some arms and not in the steering/goal arms, :655-664 has no `TranscriptEntryIndex`).

Fix plan:
- Add `systemMessageItem(turnID string, idx int, idPrefix string, kind EventKind, description, text string) ThreadItem` and a `millisOf(time.Time) *int64`; replace the 7 literals and 8 timestamp sites. Pure refactor, guarded by `new_format_projection_test.go`, `hook_turn_test.go`, `failed_turn_test.go`, `goal_continuation_test.go`, `project_turn_parts_test.go`, and the fuzz `projectturn_fuzz_test.go`.
- Then split `projectTurn` into `projectMarkerTurn`, `projectAssistantTurn`, `projectToolResults`. If Finding 1 is approved, most of the assistant/tool-result arms disappear, so sequence this after the decision for those arms and do the marker-kind extraction now.
- A latent inconsistency to pin with a test while extracting: the goal-continuation systemMessage lacks `TranscriptEntryIndex` that its siblings set.

Size: about -120 / +40. Risk: low.

---

## Finding 9 (medium): three scans compute the same session totals, plus a second failure-counter implementation

Locations:
- `internal/apptranscript/usage_total.go:36-60` (`UsageTotalFromFile`/`scanUsageTotal`), `failed_tool_calls.go:46-80` (`FailedToolCallsFromFile`/`scanFailedToolCalls`), `derived_totals.go:44-90` (`DerivedTotalsFromFile`/`scanDerivedTotals`), `turn_cache.go:19-130` (three memo fields, `memoizeScan`), `read_stats.go` (observer for these scans)
- `internal/apptranscript/failed_tool_calls.go:100-135` (`failureCounter`) versus `agent/transcript/failures.go:96-190` (`transcript.FailureCounter`, used by `transcriptindex/build.go:78-87,162`)
- callers: `agent/jobs_activity_past.go:325` (usage only), `cmd/evener-hub/app_threadread.go:1439,1481` (both scans, only as the no-index fallback), `transcriptindex/window.go:66` (`Index.DerivedTotals`, the primary path)

Evidence:
- `scanDerivedTotals` is documented as "exactly the union of the two functions it replaces" yet both singles remain: `FailedToolCallsFromFile` is called only from `stampDerivedFailureCount` (app_threadread.go:1481), a branch taken when meta already carries usage; `UsageTotalFromFile` only from `historicalActivityUsage` (jobs_activity_past.go:325). Each could call the derived scan and discard one half.
- The failure rule has one shared predicate (`transcript.FailedToolResult`) but two stateful counters with the same name-learning logic: apptranscript's narrow-decode `failureCounter.observe` (failed_tool_calls.go:129) and `transcript.FailureCounter.observe` (failures.go:127). The comment on `FailedToolResult` ("two copies of the rule are two rules") describes exactly this risk one level up.
- Three nearly identical `...Entry` narrow-decode structs (`usageOnlyEntry`, `failedToolCallEntry`, `derivedTotalsEntry`) and three `memo` fields on `turnCacheEntry`.

Impact: three fallbacks, three memo slots, three test files (`usage_total_test.go`, `failed_tool_calls_test.go`, `derived_totals_test.go`) to keep in parity; the index (`Index.DerivedTotals`) is a fourth implementation held to parity by tests.

Fix plan:
1. Delete `UsageTotalFromFile`/`scanUsageTotal` and `FailedToolCallsFromFile`/`scanFailedToolCalls`, `usageOnlyEntry`, `failedToolCallEntry`; point the two callers at `DerivedTotalsFromFile` (ignore the unused half).
2. Reduce `turnCacheEntry` to one scan memo, and `memoizeScan` generics to one concrete use (or keep generic if simpler to read).
3. Make `scanDerivedTotals` feed `transcript.FailureCounter` (it takes full `schema.Turn`; the narrow decode exists for speed, so decode `schema.Turn` only for entries containing tool parts, or add `FailureCounter.ObserveParts` that takes the narrow struct). Delete `failureCounter`.
- Tests: `derived_totals_test.go` and `transcriptindex/derived_totals_parity_test.go` stay as the guard; fold the single-scan tests into derived tests.

Size: about -220 / +30. Risk: low-medium (perf of narrow decode must be preserved; check the existing `derived_totals_fuzz_test.go` and benchmark in CI).

---

## Finding 10 (low): `TurnCache.invalidate` deletes sidecar files that no longer exist

Locations: `internal/apptranscript/turn_cache.go:235-250`; `internal/transcriptindex/dir.go:6`.

Evidence: `invalidate` does `os.Remove(path + ".appwire-index.json")` and `os.Remove(path + ".appwire-index.json.journal")`. The only place the string `appwire-index` appears in Go code is that function; the current sidecar is a directory at `transcriptindex.DirFor(path) == path + ".index"`. `git log -S'appwire-index.json'` shows the old index was replaced in `792c379eca` ("transcript is the only history read model (activation)").

Impact: dead remnant of the pre-activation index. It also means `apptranscript` holds knowledge about a sidecar format it should not (transcriptindex imports apptranscript, not the reverse), and a parse failure leaves the current sidecar untouched, which may be intended but is now accidental.

Fix plan: delete the two `os.Remove` lines and, with them, the `_ =` ignore. If invalidating the real sidecar on a failed parse is desired, that is a product decision; discuss with Jesse (recovery owner is the index's own rebuild path via `RebuildRequiredError`). Test: existing `turn_cache_test.go`. Size -3. Risk: very low.

---

## Finding 11 (low): exported API in apptranscript/appitempaging with no production caller, and pass-through wrappers

Locations:
- `internal/apptranscript/apptranscript.go:92` (`ScanPrelude`: only tests), `:1224` (`ItemTurnProjectionFromEntries`) and `:1236` (`ItemTurnsFromEntries`): non-test callers none; test callers in `agent/notification_test.go:326,330`, `agent/system_event_wire_fixture_test.go:231`, and 7 apptranscript tests
- `apptranscript.go:257` (`ExitCodeFromToolState`) is a one-line pass-through to `transcript.ExitCodeFromToolState`; one external caller (`appoverlay/overlay.go:524`) could call the original
- `apptranscript.go:340,351` and `:1145` (`DefaultImageProjector`, `ImagesFromContent`, `StringifyToolContent`, `WebSearchProjection`): exported, zero external callers
- `internal/appitempaging/page.go:78` (`HasTranscriptKeysPrefix`: only `key_test.go`), `:107-110` (`ValidateCandidates` forwards to `validateCandidates`), `:177-179` (`comparePosition` forwards to `ComparePosition`)
- `apptranscript.go:1211-1220`: `ItemTurnsFromFile` and `itemTurnsFromFileContext` wrap `ItemTurnProjectionFromFile` just to drop the projection

Fix plan: delete `HasTranscriptKeysPrefix`, `ScanPrelude`, `ItemTurnProjectionFromEntries` (keep `ItemTurnsFromEntries` only if the agent tests move to the file path; otherwise convert those 2 agent tests and delete both); collapse the three pass-through pairs; unexport the four zero-caller exports; call `transcript.ExitCodeFromToolState` directly. Tests: compile plus existing suites. Size about -90 prod, -50 test. Risk: low.

---

## Finding 12 (medium): stale comments point at symbols and files that no longer exist, including a dead rule in `EchoesAssistantText`

Locations (each verified to have no matching symbol/file in the worktree):
- `internal/apptranscript/apptranscript.go:207` and `:38`, `:761`, `:824`: `matchesLastAssistantMessage` ("in internal/appprojector"), `recordAssistantMessage`; the projector now "holds no turn or item state" (`appwire_projection.go:23-28`) and neither name exists
- `logical_turn.go:14` (`appTurnSnapshot`), `logical_turn.go:266` ("mirrors server mergeAppThreadItem"), `flush_communicate.go:77` (`appThreadItemIdentityMatches`)
- `cmd/evener-hub/app_threadread.go:1506` and `internal/transcriptindex/window.go:557,573`: `server/appwire_turns.go` (file does not exist)
- `apptranscript.go:216-231` (`ToolIntentFromArguments` doc): "PR #709, 2026-08-29 rename (7512a736e)" history narration inside a doc comment
- `transcriptindex/index.go:25-31`: comment begins "v8: ..." above `projectionID = "transcript-read-model-v8"` with `formatVersion = 6` and no v1-v7 context

Impact: `EchoesAssistantText`'s doc claims to be "the single definition ... for all three surfaces" including a projector function that was deleted; readers will hunt for it. Per Jesse's rules, comments about history and renames belong in commit messages.

Fix plan: rewrite those comments to describe present behavior (EchoesAssistantText: callers are the reload path, the index builder, `cmd/evener/run.go:592`, and `tools/tool-fluency`); drop the history clauses; delete the orphaned "v8" paragraph. Docs: none needed beyond comments. Size about +/-60 comment lines. Risk: none. (A cheap guard: add a lint that fails on comment-cited identifiers in `internal/app*` that do not exist is over-engineering; skip.)

---

## Finding 13 (medium): `ToolIntentFromArguments` and other small helpers copied across modules "independently", with a documented drift risk

Locations: `internal/apptranscript/apptranscript.go:227-250`, `agent/transcript_render.go:1749-1770`, `agent/doctor/transcript.go:331-350`; also `communicateRawFallbackText` (apptranscript.go:410) mirroring `agent/transcript_render.go` `oneLine(truncRunes(...))` (:1966,1976).

Evidence: the doc comment on `ToolIntentFromArguments` says every other reader "applies the same rule independently rather than sharing this function ... since each lives in a different Go module or is a standalone binary that cannot import this one." The three Go copies each implement the same `intent`-then-`purpose` fallback. But `agent/transcript` (module `agent`) is already imported by `internal/apptranscript`, `agent/transcript_render.go` and `agent/doctor`, and already hosts the sibling `ExitCodeFromToolState` and `FailedToolResult` for exactly this reason. The "cannot import" premise does not hold for the Go copies.

Fix plan: add `transcript.ToolIntent(raw json.RawMessage) string` in `agent/transcript` (next to `failures.go`), delete the three copies, and move the `communicateRawFallbackText` truncate-and-collapse helper to the same package if `transcript_render.go` can export `truncRunes`/`oneLine` there. TUI copies (`toolsummary`, `msgrender`) are outside this slice; check if they can import the same package and, if not, leave them with a cross-reference. TDD: table test in `agent/transcript` covering intent, purpose fallback, whitespace, non-string values (port the existing cases). Size about -45 / +25. Risk: low.

---

## Finding 14 (medium): delegate-info mapping and cloning duplicated four times, plus six pointer-clone helpers

Locations: `internal/appprojector/appwire_projection.go:466-522` (`appwireDelegateInfo`, 55 lines), `:552-571` (`cloneAppwireDelegateInfo`), `:573-587` (`cloneBoolPointer`, `cloneInt64Pointer`), `:643-653` (`cloneSkillNames`); `server/appwire_runtime.go:2827-2864` (`appDelegateFromDetailedStatus`) with `cloneServerBool/cloneServerInt64` at :2866-2880; `cmd/evener-hub/app_threadread.go:1278-1315` (`appwireDelegateFromAgentStatus`) with `cloneHubBool/cloneHubInt64` at :1317-1331; `appwire/clone.go:284-296` (`cloneDelegateInfo`), `:125` (`clonePointer`); queue clone `appwire/clone.go:298-322` versus projector queue literal at `appwire_projection.go:213-224`.

Evidence:
- The three mapping functions each list about 45 fields positionally. They differ in details (the projector truncates `Reason`, `Error`, `NotResumableReason`, `Task`, `Description`, `Model` with `activitybound.Truncate`, sets `LogicalOwnerSessionID`, `ReportPreview`; the server and hub versions do none of that). Whether the read path truncates elsewhere is not visible from these files, so this may already be a drift between live push and saved read.
- Pointer clones: `clonePointer[T]` exists generically in `appwire/clone.go:125`, `llm/registry/load.go:622`, `cmd/evener-hub/navigation_projection.go:1887`, `agent/internal/jobstore/event_clone.go:32`, `cmd/evener-hub/internal/appsource/remote_hub_probe.go:278`; the non-generic `bool`/`int64` variants in appprojector, server and hub are 6 more copies.
- `cloneAppwireDelegateInfo` in the projector duplicates `appwire.cloneDelegateInfo` field for field (message, structured result, bool/int pointers, usage, worktree, warnings, diagnostics).
- `cloneSkillNames` plus `append([]string(nil), ...)` chain re-implements `appwire.cloneQueueState`; the projector could assign `appwire.QueueState{...}` and pass it through an exported clone.

Impact: a new field on `EvenerDelegateInfo` must be added in at least four mapping/clone sites; omitting one drops data on one surface only (live vs saved vs local).

Fix plan:
1. Export `appwire.CloneDelegateInfo` and `appwire.CloneQueueState`; delete the projector's clone functions and pointer helpers (use `appwire` clones or `slices.Clone` and `new`-style copies).
2. For mapping, define the canonical `appwire.DelegateInfoFromStatus(...)` once if `events.DelegateUpdatedData`, `agent.DelegateStatusInfo` and `server.DelegateStatusInfo` can share a field-compatible struct (check: they appear field-identical; if so, make the event data embed or alias the status struct so a single conversion remains). If not unifiable, at minimum make server and hub call one shared function and keep the projector's bounded variant on top of it.
3. Resolve the truncation question with a test: same delegate through live push and saved read must produce equal bounded rows.
- Docs: `docs/product/session-activity.md` already states live and read agree on bounds; add the shared conversion as the owner. TDD for step 3 since it may change behavior.

Size: about -190 / +70. Risk: medium (cross-module, three call sites, behavior parity).

---

## Finding 15 (medium): `AppEventProjector.Project` is a 356-line switch with duplicated job and task branches

Locations: `internal/appprojector/appwire_projection.go:81-437`.

Evidence:
- `EventJobStarted` (:253-292) and `EventJobFinished` (:293-338) build the same `EvenerJobInfo` (13 shared fields listed twice) and the same trailing `JobsTreeUpdatedParams` block.
- `EventSessionStart` and `EventTaskUpdated` both copy three routing fields (`TaskStoreOwnerSessionID`, `TaskPublicationEpoch`, `TaskPublicationRevision`) from event data to the notification and to the projector (:105-110, :210-218, :193-199); `observeTaskPublication` exists but callers still repeat the assignments.
- A 20-line comment on `EventSandboxEscalationRequested` (:225-231) and one on `EventSandboxEscalationResolved` (:242-250) narrate history ("M7", "v1", "reserved and empty") inside the switch.
- `thread/closed` params stay `map[string]any` with a comment that a typed struct is "not provably byte-identical" (:411-417); unresolved since `kcb5`.

Fix plan: split into `projectJob`, `projectTask`, `projectSession` helpers returning `[]AppNotification`; add `jobInfo(common, finished)` and `jobsTreeNotification(rootID, revision)`; add `withTaskRouting(n AppNotification, data)` once. Replace the `map[string]any` with the typed struct plus a golden-bytes test that proves the wire bytes are unchanged (TDD, the comment's own concern); trim narrating comments to current behavior. Guard: `appwire_projection_test.go`, `delegate_projection_test.go`, `execution_status_test.go`, `project_fuzz_test.go`. Size about -90 / +40. Risk: low.

---

## Finding 16 (medium): `appitempaging.cloneThreadItem` has drifted from `appwire`'s clone, and two cursor codecs duplicate each other

Locations: `internal/appitempaging/page.go:209-224` versus `appwire/clone.go:58-67`; `internal/apphistory/paging.go:23-33,278-290,300-310` versus `internal/appitempaging/cursor.go:24-110`.

Evidence:
- `appwire.cloneThreadItem` deep-copies `Images`, `OutputImages`, `Position`, `StartedAt`, `CompletedAt`, `DurationMS`, `ExitCode`, `Raw`. The appitempaging copy clones only `Position`, `Images` (shallow element copy), `OutputImages`, `Raw`. `StartedAt`, `CompletedAt`, `DurationMS`, `ExitCode` pointers stay shared between the source candidate and the regrouped turn that `RegroupTurnFragments` returns (page.go:131). It is exercised on every selected-history page (`apphistory.fill` -> `RegroupTurnFragments`).
- `appwire.cloneThreadItem` is unexported, which is why the second copy exists. `appwire.CloneOverlayItem` is the only exported route.
- Two opaque-cursor implementations: `appitempaging.EncodeCursor/DecodeCursor` (base64url JSON, version, strict `DisallowUnknownFields`, trailing-data check, 1 MiB bound) and `apphistory.contentCursor` with `cursorPrefix = "content1."`, its own base64url JSON encode/decode, a separate `1<<20` bound (`decodeCursor`, paging.go:300), and no strict unknown-field check.

Impact: the shared-pointer aliasing is a real correctness smell (a consumer that mutates `StartedAt` on a regrouped item changes the cached source), and two cursor formats mean two sets of staleness rules.

Fix plan: export `appwire.CloneThreadItem` and delete `appitempaging.cloneThreadItem` (and `cloneTurnWithoutItems`, a one-line helper used once). Add a failing test first: regroup a candidate, mutate `*item.StartedAt` on the result, assert the source is unchanged (TDD, behavior fix). For cursors, extract a small `opaque.Encode(prefix, v)/Decode(prefix, &v, maxBytes)` helper in `appitempaging` that both use, keeping each cursor's own payload struct and fences; keep the formats byte-compatible so held cursors do not go stale (the stale path already exists and recovers by re-read). Also delete `ValidateCandidates -> validateCandidates` and `comparePosition -> ComparePosition` pass-throughs (see Finding 11). Size about -70 / +30. Risk: low-medium.

---

## Finding 17 (low): production code carries seams that exist only for coverage or one test

Locations:
- `internal/apptranscript/live_notice.go:15-17`: `var marshalContextCompaction = json.Marshal` ("a seam so a test can reach the marshal-failure branch no real ContextCompactionData can")
- `internal/apptranscript/read_stats.go` (`InstallReadObserverForTesting`, `ReadStats`, global mutex) used only to count scans in tests
- `internal/transcriptindex/index.go:52-60,107-137,1403-1415`: four `testKill*` function globals, `SetUpdateLogRecordsForTest`, `HoldSidecarLockForTest`
- `internal/appwirets/main.go` and `appwiredoc/main.go`: `exitProcess`, `executeTemplate`, injected `writeFile` params used so tests can force write/render failures
- `internal/appserver/*`: the 14 Server hooks (Finding 4)

Evidence: the marshal-failure branch is unreachable with real data by the comment's own admission (`contextCompactionRaw` marshals a map of ints and strings). The `testKill*` seams are legitimate crash-recovery hooks (kill_test.go), but they are package-level mutable globals that the index tests cannot run in parallel.

Fix plan: delete `marshalContextCompaction` and the branch test (ignore the impossible error with a documented `// cannot fail: plain ints and strings`); keep `testKill*` but gather them into one `testHooks` struct and make `ReadStats` counters atomic package counters read through a `_test.go` accessor (no installable observer in production). Leave the generator `run(args, stderr, writeFile)` shape, which is a conventional testable `main`; drop only `executeTemplate`/`exitProcess` globals if tests can drive failures with a bad path instead. Size about -50. Risk: low (but tests lose a few coverage-only cases; that is deliberate).

---

## Finding 18 (low): the two protocol generators each carry a private reflective type walker, and the doc generator silently accepts name collisions

Locations: `internal/appwirets/emit.go:404-500` (`registry`, `addNamed`, `walkFields`, `discover`) versus `internal/appwiredoc/main.go:139-217` (`registerReflectType`, `registerNestedTypes`, `fieldsOf`).

Evidence:
- Both walk the same `appwire.Methods`/`Notifications` catalog and discover nested named types. appwirets panics on two distinct types claiming one name unless they emit identically (emit.go:424-440, citing "the #1016 trap"). appwiredoc does `if _, seen := typeNames[name]; !seen` and keeps the first claimant (main.go:152), exactly the behavior #1016 describes, so the doc can describe a different type than the TypeScript generated from the same catalog.
- Different follow rules: doc follows only types whose `PkgPath` equals appwire's; TS skips opaque structs (`isOpaqueStruct`) and panics on anonymous structs.

Fix plan: lift the walk (catalog roots, optional supplemental roots, union arms from `appwire.MethodResultArms`, collision rule) into one `internal/appwirecatalog` package returning an ordered `[]NamedType`; both generators render from it. Doc generator inherits the collision panic. Guard: `TestGeneratedFileCurrent` and `make lint-generated` (generated outputs must be byte-identical or the diff is reviewed). Add a unit test that two same-named differing types panic in the shared walker. Size about -90 / +60. Risk: low; outputs are checked in.

---

## Finding 19 (low): `apphistory.Read` and `fill` are long functions with inlined bookkeeping

Locations: `internal/apphistory/paging.go:62-131` (`Read`), `:188-293` (`fill`).

Evidence: `Read` computes `Changes` item/turn survivors with two maps (`kept`, `keptTurns`) inline over 50 lines; `fill` interleaves page walk, candidate selection, a byte-budget trim loop that re-marshals the whole page per dropped item (`for { ... json.Marshal(result.Data) ... selected = selected[1:] }`, :263-276; O(n) re-encodes of a 1 MiB page), and cursor minting. The size loop measures per-item sizes once (:249-259) then does a second, whole-page encode loop anyway.

Fix plan: extract `reconcileChanges(changes, response, sourceWindow, selection)` and `trimToPageBudget(selected)` as named functions; drop the second encode loop if the per-item sum is within a constant of the full encoding (verify with the existing paging tests and one size-boundary test), otherwise bound the loop with binary search on the prefix. Behavior must not change; guard with the existing `Paging cases must cover refresh and reconnect after loading multiple pages` tests required by AGENTS.md. Size about +/-60. Risk: low.

---

## Recommended fix sequence

1. Quick deletions with no behavior change (one PR): Finding 10 (stale sidecar remove), Finding 3 (dead subscription/notifier API, tests re-pointed), Finding 11 (dead exports and pass-throughs), Finding 6 (config knobs), Finding 12 (stale comments). About -400 lines net, all low risk.
2. Small correctness-flavored fixes with TDD (one PR each): Finding 16 (`cloneThreadItem` aliasing, export `appwire.CloneThreadItem`), then Finding 13 (shared `transcript.ToolIntent`).
3. Consolidation PRs: Finding 9 (single derived-totals scan and shared `FailureCounter`), Finding 14 (delegate mapping and clones in `appwire`; confirms/closes the truncation question), Finding 15 (`Project` split), Finding 8 (marker-kind helpers; defer the assistant/tool-result split until Finding 1 is decided), Finding 18 (shared catalog walker).
4. appserver restructuring, in order: Finding 2 (single resolver), Finding 5 (dispatch class table into embedder config), Finding 7 (lifecycle key and unsubscribe unification), then Finding 4 (file split, option structs, hook struct) last so the moves apply to the already-shrunk code. Concurrency changes get a CI race run, not a local one.
5. Discuss with Jesse before starting: Finding 1 (legacy-format transcripts: migrate-on-open vs drop). It is the biggest win in the slice and it is backward compatibility, so it needs an explicit decision; Findings 8 and the legacy halves of 9 and 17 shrink further once it lands.
6. Low-priority tidy: Findings 17 and 19 whenever those files are next touched.
