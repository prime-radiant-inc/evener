# Tech-debt audit: hub-app-rest

Slice: `cmd/evener-hub/app_*.go`, non-test, excluding `app_host*`.
Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no tests were run.

## Overview and inventory

| Metric | Value |
| --- | --- |
| Source files in slice | 61 (24,595 lines) |
| Test files for the slice (`app_*_test.go` minus `app_host*`) | 176 files, 89,389 lines (3.6x the source) |
| Comment lines in slice | 6,687 of 24,595 (27%) |
| `// ` comments citing issue numbers / spec sections / kata ids | 46 / 46 / 11 |
| Package-level mutable test seams in slice | ~25 (list in finding 8) |
| Commits touching `app_rpc/relay/auth/instances.go` since 2026-09-01 | 131 (app_rpc 102, 29 of them `fix`) |

Largest files: `app_instances.go` 2,716, `app_rpc.go` 2,519, `app_relay.go` 2,299,
`app_auth.go` 1,787, `app_threadread.go` 1,739, `app_threadlifecycle.go` 1,706,
`app_force_stop.go` 963. These seven are 13.7k of the 24.6k lines.

Largest functions (computed with an awk brace counter):

| Lines | Function |
| --- | --- |
| 1,829 | `newHubRelayFunctions` (`app_relay.go:471`) |
| 682 | `registerThreadHandlers` (`app_rpc.go:1304`) |
| 398 | `forceStopThread` (`app_force_stop.go:53`) |
| 383 | `hubInstancesController.Remove` (`app_instances.go:1808`) |
| 335 | `hubInstancesController.edit` (`app_instances.go:1097`) |
| 272 | `newHubAppServerWithNavigationAndTrace` (`app_rpc.go:1017`) |
| 224 | `hubThreadFork` (`app_threadlifecycle.go:1076`) |
| 199 | `checkConfirmedStoppedWithoutClaim` (`app_force_stop.go:544`) |

Product context read: `AGENTS.md`, `docs/product/subsystems.md` header. The product rule that
matters most for the fix plans is "do not treat fail closed as a default design goal"
(finding 9) and "keep evergreen docs in the same change".

Overall: the slice is functionally rich and heavily guarded, but the shape is "every edge case
got its own paragraph and its own helper". The main debt is (a) three mega-functions that are
closure bags, (b) a mutation-id correlation layer that exists in 12 functions and 7 copies of
the same two-shape decode, (c) a lot of production code that only tests call, (d) hand-copied
retry/resume wrappers, and (e) coverage-padding tests.

---

## Findings

### hub-app-rest-1: Mutation-id correlation layer is ~450 lines, spread over 12 functions, with the ErrorData/map dual decode copied 7 times (high)

Locations: `app_rpc.go:487-917`, `app_restart_required.go:295-333`, `web_api.go:60-100`,
`app_retirement_resume.go:22-38`, `internal/appsource/source.go:612`.

Evidence:
- `app_rpc.go` holds `blockedUnknownMutationError`, `mutationResumeFailureError`,
  `resolvedOwnershipTarget`, `canonicalMutationID`, `mutationIDsMatch`, `errorNamesClientMutation`,
  `adoptCallerMutationID`, `adoptCallerMutationReceipt`, `adoptFailureClientMutationID`,
  `adoptCallerMutationResult`, `isShapeRefusal`, `shapeRefusalNamesOtherMutation`,
  `correlateRetryFailure`, `nameTargetDeletedFailure`. Lines 487-917, roughly 85% comment.
- The `switch data := wire.Data.(type) { case appwire.ErrorData: ... case map[string]any: maps.Clone ... }`
  stamping appears verbatim at `app_rpc.go:677`, `app_rpc.go:903`, `app_restart_required.go:301`,
  `app_restart_required.go:318`, and the read-only version at `web_api.go:62` and `web_api.go:82`,
  plus `appsource/source.go:612`. `isLifecycleRetiringError` (`app_retirement_resume.go:27`) does
  a third variant (re-marshal into the typed struct).
- `adoptCallerMutationID` (`app_rpc.go:664`) and `nameTargetDeletedFailure` (`app_rpc.go:883`) are the
  same "set `ClientMutationID` on either shape" function with a different guard in front.
- Eight handlers repeat the `clientMutationId is required` check (`app_rpc.go:1664,1784,1801,1846,1877,1897,1917,1934`).
- This is hub-side policy on top of the daemon's own `clientMutationId` trimming, so the rules are
  split across hub and `server/appwire_runtime.go`.

Impact: any new error field has to be added to two shapes in 7 places; the policy ("who owns a
failure's mutation id") can only be understood by reading 450 lines of prose. Hot file: 29 fix
commits in 8 weeks on `app_rpc.go`.

Fix plan:
1. Add `appwire.WireError.ErrorData() (ErrorData, bool)` and `WithErrorData(func(*ErrorData))` in
   `appwire` (canonical home next to `WireError`). The first normalises typed, map and embedded
   (`HostFieldErrorData`, `LifecycleErrorData`) shapes via one re-marshal path; the second
   rewrites in place and keeps the original shape. Migrate the 7 sites to it, delete
   `clientMutationIDFromData`/`evenerErrorInfoFromData` special cases.
2. Move the 14 correlation functions to `cmd/evener-hub/mutation_correlation.go` (one file, one
   doc block that states the policy once), and replace the per-function essays with that block.
3. Add one `requireMutationID(params.ClientMutationID)` helper for the 8 repeated checks.
4. Test strategy: behavior-preserving. Existing correlation tests in `app_rpc_test.go` stay and
   run unchanged; add a table test for `ErrorData()` over typed/map/embedded inputs first (red,
   then green).
5. Docs: no product doc change; the policy block moves into the new file header.

Size: net -350 lines (delete ~450 comment/dup lines, add ~100). Risk: medium; the correlation
rules are subtle but pinned by tests. Do it in two PRs (helper + migration, then file move).

### hub-app-rest-2: `newHubRelayFunctions` is a 1,829-line closure bag with test-only members and five global observation hooks (high)

Locations: `app_relay.go:471-2299`, `app_relay.go:357-378`, `app_rpc.go:1191`.

Evidence:
- One function (`app_relay.go:471` to `2299`) declares ~35 closures that share `relayMu`,
  `relayedThreads`, `pendingRelays`, `canonicalRelays`, `relayGenerations`
  (`app_relay.go:477-1066` helpers, `startRelayOnce` alone is `1629-2127`, ~500 lines).
- `hubRelayFunctions` (`:357`) exposes `stopRelay`, `stopCanonicalRelay`, `relayCommandCount`,
  `relayPublished`. `relayCommandCount` and `relayPublished` have no production caller (`grep`
  in non-test files finds only the definitions and `app_relay.go:2296-2297`); 24 test references.
- Test hooks: `observeHubRelayFunctions`, `observeHubRelayWait`, `observeHubRelayPending`,
  `observeHubRelayPendingBackpressure`, `observeHubRelayRoutePublication`,
  `observeHubRelayDeliveryRead` (`:369-378`), invoked at 9 sites inside the closures. Tests
  reference them 77 times (`app_relay_test.go` 20, `app_rpc_test.go` 36, others).
- Churn: 22 commits since Sep 1, 10 are fixes ("break relay read acknowledgement deadlock",
  "pin relay pending-delivery capacity").

Impact: nothing in the relay's state machine can be unit-tested without building the whole
server; reviewers cannot see the lock discipline because the state is closure-captured.

Fix plan:
1. Introduce `type hubRelays struct { mu sync.Mutex; threads map...; pending map...; canonical map...; generations map...; cfg; sources; server; logf; clock }`
   and turn each closure into a method. This is a mechanical transform (closure -> method on
   receiver), done one closure per commit, bottom-up from the leaf helpers.
2. Delete `relayCommandCount`/`relayPublished` from the production struct; tests read the
   fields directly from `hubRelays` (same package).
3. Replace the six `observe*` globals with one `relayTestHooks` struct field on `hubRelays`,
   set by tests through a constructor option (no package state, tests can run in parallel).
4. `hubRelayFunctions` becomes `*hubRelays` and `registerThreadHandlers` takes it directly.
5. Split into `relay_state.go` (handles, routing), `relay_fanout.go`, `relay_read.go`.

Size: ~0 net lines, ~2,300 moved; hooks removal -80. Risk: medium-high (concurrency). Gate
with the existing relay tests; run them on CI, not locally. Docs: `docs/product/subsystems.md`
S06 row only if the file names it.

### hub-app-rest-3: `registerThreadHandlers` (682 lines) repeats one handler skeleton 8 times and hand-rolls a second copy of the resume-retry for turn/start (medium)

Locations: `app_rpc.go:1304-1986`, specifically `1660-1780` (turn/start), `1783-1980`.

Evidence:
- Handlers for turn/steer, turn/interrupt, turn/queue, turn/drainAsSteer, promoteQueuedAsSteer,
  cancelQueued, thread/clear all do: validate input -> require `clientMutationId` ->
  `withDeletionTargetOwnership(...)` -> `sourceForThread` -> action -> `adoptCallerMutationResult`.
  Eight copies of `if strings.TrimSpace(params.ClientMutationID) == "" { return ...InvalidParams("clientMutationId is required") }`.
- turn/start (`1660-1775`, ~115 lines) re-implements `withSessionResume`
  (`app_session_resume.go:83`): `attemptStart`/`retryAfterResume` with `resolved` /
  `initialPreDispatch` flags is the same state as `firstAttemptPreDispatch`, with identical
  comments about not-accepted vs blocked-unknown.
- The function is a closure inside `registerThreadHandlers` rather than a handler per method.

Fix plan:
1. Add generic `mutationHandler[P, R](server, method, spec)` where spec carries `validate`,
   `receipt func(*R) *MutationReceipt`, `run`. It owns the `requireMutationID` check and the
   trailing `adoptCallerMutationResult`.
2. Express turn/start as `withSessionResume` plus a pre-step hook for its "resolve source first"
   quirk (it resumes when source resolution fails, which `withSessionResume` does not). If the
   quirk is real, extend `withSessionResume` with an optional `resolveFailureResumes` flag
   rather than keeping a second implementation; otherwise delete.
3. Split into `registerThreadReadHandlers`, `registerTurnHandlers`, `registerSessionActionHandlers`
   (one file each, `app_rpc_turn.go` etc.).
4. Tests: existing turn/start retry tests (`app_turn_start_resume_only_test.go`, `app_rpc_test.go`)
   are the guard; add one table test over the 8 handlers asserting a missing id yields
   InvalidParams (behavior only, no copy assertions).

Size: -250 lines. Risk: medium (turn/start is the hottest path). Do 1 and 3 first.

### hub-app-rest-4: Five hand-copied "resume once then retry" wrappers plus a three-layer pass-through for the predicate (medium)

Locations: `app_compact.go:11-60`, `app_model.go:11-26`, `app_vision_model.go:11-26`,
`app_session_resume.go:303-359`, `app_session_resume.go:83`.

Evidence:
- `compactThreadWithResume`, `setThreadModelWithResume`, `setThreadVisionModelWithResume` are
  byte-for-byte the same 12-line body (`hubKnowsRef` check, `shouldResumeAfterSessionUnavailable`,
  `hubThreadAutoResume(... ThreadResumeParams{Ref: params.Ref})`, second call) differing only in the
  `*Once` function. Each `*Once` is `withSessionActionOwnership` -> `sourceForThread` ->
  `ensureThreadActionAvailable(<action>)` -> `source.X` (also copied three times), and the same
  skeleton is `relayWithResume` (`app_session_resume.go:344`), which already exists.
- `setGoalWithResume` (`:303`) and `clearThreadWithResume` (`app_compact.go:62`) spell out the body
  of `relayWithResume` instead of calling it.
- Predicate chain: `shouldResumeAfterTurnStartError` -> `shouldResumeAfterSessionUnavailable` ->
  `isSessionUnavailableError` (`app_compact.go:11-26`); the first two are pure pass-throughs.
- `hostQualifiedImageRoute` (`app_rpc.go:334`) is also a one-line pass-through to
  `appsource.HostQualifiedControllerImageRoute`.

Fix plan:
1. Rewrite the five wrappers as `relayWithResume(ctx, cfg, sources, ref, "", "<action>", func(s) { return struct{}{}, s.X(ctx, params) })`;
   delete `compactThreadOnce`, `setThreadModelOnce`, `setThreadVisionModelOnce`, and files
   `app_model.go`, `app_vision_model.go`.
2. Delete `shouldResumeAfterTurnStartError` and `shouldResumeAfterSessionUnavailable`; callers use
   `isSessionUnavailableError`. Inline `hostQualifiedImageRoute`.
3. `threadActionAvailable` takes a typed enum rather than strings (`"vision-model"`, `"shared-notes"`),
   removing the `default: return false` that silently refuses a misspelled action.
4. Tests: behavior preserved; the model/compact resume tests keep passing. The one behavioral
   delta is that these now go through `withSessionResume`'s `clientMutationID == ""` path, which
   is identical to today.

Size: -110 lines. Risk: low.

### hub-app-rest-5: `preDispatchRefusalError` and the not-accepted branch are documented as unreachable forward-compatibility (medium)

Locations: `app_session_resume.go:12-81`, `app_session_resume.go:152-176`, call sites
`app_session_resume.go:309,350`, `app_compact.go:69`, `app_rpc.go:1857`.

Evidence: the type's own doc says "No current caller can satisfy that whole-operation proof, and
the not-accepted outcome it guards is therefore forward-compatibility, not a live production
result" and `firstAttemptPreDispatch` is "always false here". 30 lines of type + 25 lines of
branch + the wrapper at 4 call sites exist only to preserve it. Jesse's rules: YAGNI, no
compat without approval.

Fix plan: delete `preDispatchRefusalError`, `isPreDispatchRefusal`, `unwrapPreDispatchRefusal`,
the `firstAttemptPreDispatch` branch, and the four wraps at call sites; remove
`TestHubRPCResumeRetrySourceResolutionFailureIsNotAccepted`'s expectation or delete the test if
it only pins the dead path (confirm first). If a future source can produce session-unavailable
at resolution, reintroduce with a failing test. Note the same not-accepted reasoning lives in
turn/start (`app_rpc.go:1730`, `initialPreDispatch`) and is reachable there; keep that one and
fold it into finding 3.

Size: -90 lines. Risk: low. Needs Jesse's nod because it removes a stated invariant.

### hub-app-rest-6: Dead and no-op production code in `app_threadread.go` and others (medium)

Locations: `app_threadread.go:1635-1710`, `:73`, `:181`, `:1528`, `:778-796`; `app_force_stop.go:746`;
`app_navigation.go:51`.

Evidence (non-test, non-comment reference counts = 1, i.e. definition only):
- `reconcileDelegateThreadItems` (`:1639`, 37 lines) has no production caller. It calls
  `reconcileDelegateThreadItem` (`:1676`) whose body is `_ = rec; return item`, a no-op. Along with
  `delegateJobIDFromRaw` (`:1681`), `isTerminalHistoricalJobStatus` (`:1699`),
  `reconcileDelegateThreadItemForTest` (`:1635`, a production-file wrapper named for tests) that is
  ~75 lines of dead code, kept alive by 6+5+5 test references and a coverage fuzz file
  (`cov_threadread_images_fuzz_test.go:141`).
- `pastThreadForRead` (`:73`, 20 lines), `pastThreadReadResponse` (`:181`, a pass-through to
  `pastThreadItemReadResponse`), `decodeTranscriptTurn` (`:1528`), `forceStopOwnershipUnchanged`
  (`app_force_stop.go:746`), `navigationReadKey` (`app_navigation.go:51`): all test-only.
- `resumeRequiredActiveFlag` (`:791`): the comment says "NOTHING IN THE TREE EMITS IT". `grep`
  confirms no producer of `ActiveFlags` containing it; `hubForkRecoveryFenced` (`:793`) tests it
  on every list/read. The `ActiveFlags` plumbing through `hubcore/roster.go:32,140,241,488,1507`,
  `prober.go:174,202`, and `app_threadlifecycle.go:1350` therefore carries a field with no writer.

Fix plan: delete the delegate-reconcile chain and its tests; delete the listed test-only helpers
and rewrite their tests against the real entry points; delete `resumeRequiredActiveFlag` and the
`slices.Contains` clause (separate PR for the `ActiveFlags` roster plumbing, since it touches
`hubcore` and the wire type: confirm no daemon emits it first). Tests: deleting dead-code tests is
the point; behavior tests of the surrounding read paths are unchanged.

Size: -300 lines (source+tests). Risk: low.

### hub-app-rest-7: Test-only wrappers and constructors live in production files (low-medium)

Locations: `app_auth.go:1390,1476,1620,1707`; `app_rpc.go:1000-1006`; `app_auth.go:134`;
`app_plugins.go:45`; `app_daemons.go:60`.

Evidence:
- `verifyEndpointFingerprint` (def only in prod, 2 tests), `verifyFlowEndpoint` (def only, 4 tests),
  `instanceStatus` (def only, 5 tests), `openAIInstanceStatusByName` (just `c.openAIInstanceStatus(name)`
  to fit a method-value shape), `newHubAppServer` (129 test callers), `newHubAppServerWithNavigation`
  (17 test callers), `newHubAuthController` (variadic env, only tests), `newHubPluginsController`
  (variadic `launchConfigRoots ...string` taking `[0]`), `setRetireOwnershipCapability`
  ("production never calls it").
- The `WithKey`/`Keyed` doubling (`verifyEndpointFingerprint`/`WithKey`, `verifyFlowEndpoint`/`WithKey`,
  `endpointFingerprintFor`/`ForKey`, `instanceStatus`/`Keyed`, `openAIInstanceStatus`/`Keyed`,
  `destinationFingerprint`/`Keyed`) exists so that production can pass a pre-resolved key; the
  unkeyed halves are the test API.

Fix plan: move each to a `*_testutil_test.go` file in the same package (`export_test`-style), or
rewrite the tests to call the keyed form with a helper `testKey(t, c)`. Delete the variadics.
Pure move; size -120 prod lines, +60 test lines. Risk: low.

### hub-app-rest-8: ~25 package-level mutable function variables exist only to be swapped by tests (medium)

Locations: `app_rpc.go:192-204` (`resolveTurnStartSource`, `resumeTurnStartThread`,
`authLoginComplete`, `authDevicePoll`, `launchTrustRepo`), `app_auth.go:102,590`
(`hubAuthControllerSetup`, `credentialWriteBetween`), `app_plugins.go:191,199`, `app_models.go:19`,
`app_plugin_autoupgrade.go:25`, `app_instances.go:74`, `app_sources.go:183`, `app_threadread.go:92,1438`,
`app_threadread_repair.go:49,69,76`, `app_restart.go:44,48`, `app_update.go:27-54`,
`app_upgrade.go:11`, `app_relay.go:369-378`.

Evidence: `authLoginComplete`, `authDevicePoll`, `launchTrustRepo` are
`func(c *X, ctx, p) { return c.Method(ctx, p) }` wrappers that exist so tests can stub a method of a
type they could instead construct with a fake collaborator (`app_rpc_auth_origin_test.go` 4 refs,
`cov_exact_app_rpc_fuzz_test.go` 5 refs). Comments like "Production leaves it nil" and
"Tests replace it" mark intent. This conflicts with the repo testing rule (scripted boundary, real
code below it) and makes tests non-parallel.

Fix plan: for each seam, pick the boundary the test actually needs (HTTP client, clock, exec)
and inject it through the owning struct (`hubAuthController.now` already shows the pattern). Delete
the three `auth*/launch*` wrapper vars outright and call the methods; tests that stub them use a
controller built with a scripted `authopenai` HTTP client (the repo's boundary rule). Convert the
rest case by case; start with the three trivial ones.

Size: -60 immediately, -200 over the sequence. Risk: low-medium (test rewrites). Order after finding 2.

### hub-app-rest-9: Endpoint-fingerprint guard is ~900 lines with four near-identical verifiers and a default-fail-closed policy (high; needs Jesse's product decision)

Locations: `app_instances.go:336-780` (fingerprint, 270-line key file store), `app_instances_key_{unix,windows,other}.go`
(245 lines), `app_auth.go:1240-1530`, `app_credentials.go:145-170`, `hubcore/registry.go:425-465`.

Evidence:
- Four separate refusers with the same shape (empty assertion / unresolvable / moved) and
  hand-written message templates: `verifyEndpointFingerprintWithKey` (`app_auth.go:1403`),
  `verifyFlowEndpointWithKey` (`:1488`), `verifyConfigRevision` (`:1442`), `verifyProbeEndpoint`
  (`app_credentials.go:145`). Plus the start-time duplicate in LoginStart/DeviceStart
  (`app_auth.go:402` and `:1092` are identical: `endpoint := c.endpointFingerprintFor(provider); if endpoint == "" && c.endpointHasDestination(...) && c.hasEndpointStateRoot() { return endpointConflict(...) }`).
- An HMAC key file with O_NOFOLLOW, uid and mode checks, an inter-process lock, atomic link/rename
  publish and repair (`readEndpointFingerprintKey` 611, `repairEndpointFingerprintKey` 669,
  `publishFreshEndpointFingerprintKey` 701) plus a Windows variant. It protects a digest of a base
  URL shown to the same user who owns the file.
- Policy: `verifyEndpointFingerprintWithKey` refuses an empty assertion whenever the key cannot be
  read ("There the write fails closed"), and `fingerprintKeyDiagnostic` tells the pane the hub is
  degraded. AGENTS.md says not to treat "fail closed" as a default goal and to state the guarantee
  and who restores operation. The recovery owner here is the repair path itself, which works, but
  the guarantee ("a credential must not land on an endpoint the user did not review") has no
  product-doc entry; `grep fingerprint docs/product` finds nothing relevant.

Impact: ~900 lines guarding one race (edit-between-form-and-write). The 6 keyed/unkeyed method pairs
and the endpoint-key resolution before every lock (19 call sites of `resolveEndpointFingerprintKey`)
is a standing tax on every instance/credential change.

Fix plan:
- Phase 1 (pure consolidation, no behavior change): one `endpointGuard` value
  `{subject string; unkeyable, unresolvable, moved error}` with `Check(asserted, current, keyErr)`;
  the four verifiers become one call with different message sets (messages are not test oracles
  per AGENTS). Hoist the duplicated start-time check into `requireFlowCapturable(provider)`. Delete
  the unkeyed method halves per finding 7.
- Phase 2 (ask Jesse): write the guarantee into `docs/product` (credentials subsystem) and decide
  if an HMAC with a hardened key file is the right mechanism; a plain SHA-256 of destination
  identity plus a per-process nonce, or a monotonic config revision from the registry (it already
  has `Generation()`), would make the key store and its Windows/other variants unnecessary
  (-500 lines). Do not implement until agreed.

Size: phase 1 -250 lines; phase 2 up to -500. Risk: phase 1 low; phase 2 touches a security
boundary and the TS client (`appwire-client/typescript/state/credentials/instances.ts`).

### hub-app-rest-10: Four "load or create a 0600 secret under the state dir" implementations (medium)

Locations: `app_instances.go:557-778` (endpoint key), `llm/continuation_secret.go:104-182`,
`cmd/evener-hub/internal/hubedge/auth_token.go:80-112`, `agent/internal/installid/installation_id.go:29-43`.

Evidence: each independently does MkdirAll(0700) -> read -> if missing generate 32 random bytes ->
write 0600 temp -> link/rename or O_EXCL. They differ in strictness (O_NOFOLLOW and uid check only
in the hub copy; exact-mode check in `llm`; none in `hubedge`; fsync in `llm` and the hub, none in
`hubedge`). `llm` even carries a `continuationSecretOps` struct of injected `mkdirAll`/`randRead`/
`openFile` functions to test its failure paths.

Fix plan: create `internal/secretfile` with `LoadOrCreate(path string, opts) ([]byte, error)`
(O_NOFOLLOW + O_EXCL publish, optional lock, platform files for the Windows/other cases moved
from `app_instances_key_*`). Migrate the hub copy first (it is the most complete), then
`hubedge`, then `llm`. Tests: port the hub's failed-write and rotation tests; no mocked FS needed.

Size: -300 lines. Risk: medium (secrets); migrate one caller per PR. If finding 9 phase 2 removes the
hub copy, only two remain.

### hub-app-rest-11: Handlers are registered twice and one registration is a placeholder; registration code lives in two homes (medium)

Locations: `app_rpc.go:1224,1226` vs `web.go:250-252`; `app_archive.go:16`; `app_session_delete.go:15`;
`internal/appserver/router.go:29`.

Evidence: `Router.Handle` is `r.handlers[method] = fn` (silent overwrite). `registerArchiveHandler`
is called in `newHubAppServerWithNavigationAndTrace` (`app_rpc.go:1224`, with
`func() *NavigationService { return navigation }`) and again in `newWebServer`
(`web.go:250`, `func() *NavigationService { return web.navigation }`); `web.navigation` is the same
value that was passed to the first call, so the second registration changes nothing but gives the
`navigation` parameter an unnecessary closure shape (`registerFavoriteHandler` takes the value).
`registerSessionDeleteHandler(server, nil)` (`:1226`) installs a handler that answers Unavailable,
then `web.go:252` overwrites it with the real one. `registerProjectDeleteHandler`
(`app_project_delete.go`, 10 lines) exists only to bind one method. Registration for auth, instances,
launch, plugins and misc lives in `app_rpc.go:1989-2420` while pin, favorite, archive, daemons,
notices, rename etc. register in their own files.

Fix plan:
1. `Router.Handle` panics on duplicate method (guard against future silent shadowing); fix the two
   existing duplicates first so it does not fire.
2. Pass `web.sessionDelete` (and `web.projectDelete`) into the server constructor via the existing
   builder args, drop the placeholder and second `registerArchiveHandler`; archive takes
   `*NavigationService` like favorite does.
3. Move `registerAuthHandlers`, `registerInstanceHandlers`, `registerLaunchHandlers`,
   `registerPluginHandlers` to their controller files; `app_rpc.go` keeps only wiring.
4. Collapse `newHubAppServer` / `...WithNavigation` / `...AndTrace` into one constructor with an
   options struct (the 6-value return becomes a `hubServerParts` struct); the first two are test-only
   (finding 7).

Size: -80, ~600 moved. Risk: low. Test: router duplicate-method panic test.

### hub-app-rest-12: Instance mutations repeat an eight-step lock preamble five times, and `edit`/`Remove` are 335/383 lines (medium)

Locations: `app_instances.go:951-1089` (create), `1097-1436` (edit), `1808-2199` (Remove),
`2619`, `2680`; preamble at 952-1000, 1098-1148, 1809-1884, 2620-2636, 2681-2696.

Evidence: each does `refuseWhenBroken` -> `requireAuth` -> validate -> resolve fingerprint key
(before locks, with a 5-line essay repeated each time) -> `lockForWrite` -> `defer captureApplied(&err)`
-> `c.auth.credMu.Lock()` -> `read()`. `Remove` contains an inline rollback state machine
(`restoreFailedRemoval`, `captureOAuthFile`, `captureRefreshRejectionFile`, `removalSupply`,
`removalRemedy`) mixed with the TOML mutation, and `moveCredentials`/`removeCredentials` duplicate the
auth controller's credential-store knowledge (`app_auth.go:608-660`).

Fix plan: add `c.mutate(func(tx *instanceTx) error) error` that owns the preamble, key resolution,
locks, `captureApplied`, and the `before`/`l` double read; `tx` carries `key`, `keyErr`, `before`,
`l`. Move the credential rollback pieces into `app_instances_credentials.go`. Keep each public
method as validate + one `mutate` call. Tests: existing `app_instances_test.go` (5,442 lines) is the
guard; no behavior change.

Size: -150 lines, ~700 moved. Risk: medium (lock order must stay: key resolve, c.mu, credMu).

### hub-app-rest-13: Giant lifecycle functions in force-stop, fork, resume, list (medium)

Locations: `app_force_stop.go:53-455` (398), `:544-742` (199), `app_threadlifecycle.go:1076-1300` (224),
`:634-806` (172), `app_threadlist.go:76-237` (161), `app_retirement_resume.go:129-283` (155).

Evidence: `forceStopThread` carries 12 local booleans/slices and nested closures
(`rejectDescendants`, `fenceDescendants`, `releaseReservations`) around one linear algorithm:
validate -> cancel resumes -> verify process -> fence descendants -> kill -> clear. Comments inside
it run 10-15 lines per step. The same ownership-group alias fencing is repeated in
`forceStopThread`, `hubThreadFork`, `resumeThreadLockedLaunch` and `retireDaemon`
(`app_daemons.go:282`), each with its own copy of "validate every alias's deletion fence first,
then take reservations".

Fix plan: split `forceStopThread` into named phases returning a `forceStopPlan` (admission,
resume-cancel, verify, fence, terminate, settle); extract `lockAliasGroup(ctx, cfg, aliases)` returning
`(release, error)` for the shared reservation+fence step and use it in the four callers. Tests:
`app_force_stop_test.go`, `app_force_stop_descendant_test.go` stay as the characterization suite.

Size: -100, ~1,200 moved. Risk: medium-high (ordering invariants are the point of the code).

### hub-app-rest-14: Fork capability fences are defined twice with "a fence added here is owed there too" comments (medium)

Locations: `app_threadread.go:754-1025` (`hubCanForkThread`, `hubForkLiveDelegateFenced`,
`hubForkRecoveryFenced`, `hubForkRecoveryFencedNow`, `hubForkIdentityFenced`, `applyHubForkCapability`,
`hubForkResolvedSessionFenced`, `hubForkDeletionFenced`) vs `app_threadlifecycle.go:1076-1630`
(`hubThreadFork`, `forkFenceTargets`, `forkTargetSessionIDFor`, `forkThreadOwner`).

Evidence: comments at `app_threadread.go:754,811` say "a fence added here is owed there too, or a
stable-ref client is advertised a fork the RPC refuses". That is a manual-synchronisation contract
between a capability projection and an RPC.

Fix plan: define one `forkAdmission(cfg, ref, threadID, owner) (ok bool, refusal error)` in
`app_fork.go`; `applyHubForkCapability` uses `ok`, `hubThreadFork` returns `refusal`. Delete the 8
predicates it subsumes. Tests: add a parity table test asserting for each fence input that
`capability == (RPC refusal == nil)` (this is the real contract; today it is only prose).

Size: -150 lines. Risk: medium.

### hub-app-rest-15: 39 `cov_*_fuzz_test.go` files are coverage padding (6,031 lines, ~760 discarded results) (medium)

Locations: `cmd/evener-hub/cov_*_test.go` (e.g. `cov_exact_app_rpc_fuzz_test.go` 478 lines,
`cov_auth_instances_fuzz_test.go` 425, `cov_threadread_images_fuzz_test.go` 301).

Evidence: `f.Fuzz(func(t *testing.T, _ byte)` bodies take no fuzzed input (6 files use the
byte-ignoring form); `_ = fn(...)` / `_, _ = fn(...)` statements discard results 760 times; a comment in
`cov_auth_instances_fuzz_test.go` calls constructors "panic-net". They call dead code to keep it
"covered" (`reconcileDelegateThreadItemForTest`, `cov_threadread_images_fuzz_test.go:141`). They
assert nothing about behavior, and they pin test-only seams (`hubAuthControllerSetup`,
`observeInstancesController`). The testing guide says tests assert behavior; none of these do.

Fix plan: for each file, delete statements whose result is discarded; keep only blocks with real
assertions (move them into the matching `app_*_test.go`). Delete the file when empty. Confirm with
`make coverage` on CI that no behavior test lost coverage; where a line only the padding covers is
reachable in production, write a behavior test, and where it is not, delete the production line.

Size: -5,000 test lines. Risk: low. Needs Jesse's agreement because "reducing test coverage is
worse than failing tests": the replacement criterion is behavior assertions, not line coverage.

### hub-app-rest-16: Seven copies of pointer-clone helpers across the repo (low)

Locations: `app_threadread.go:1309,1317` (`cloneHubBool`, `cloneHubInt64`), `server/appwire_runtime.go:2858,2866`,
`cmd/evener/serve.go:2347,2355`, `agent/status.go:569`, `agent/jobs_activity.go:1293`,
`internal/appprojector/appwire_projection.go:573,581`.

Fix plan: one generic `appwire.ClonePtr[T any](*T) *T` (appwire already owns `clone.go`); replace all
10 functions; Go's builtin `new(expr)` form can replace the body. Pure mechanical; -60 lines.
Risk: low. Cross-cutting (hub, server, agent).

### hub-app-rest-17: Comments narrate history, cite issues/specs/test names, and some doc comments are orphaned (low-medium)

Locations: 27% comment density overall; orphaned docs at `app_relay.go:292-297` (the
`stampResyncTarget` comment sits above `relayGaveUpNotice`; `relayGaveUpNotice` has its own, and
`stampResyncTarget` has none), `app_archive.go:208-218` (`validateDecisionSource` prose sits on the
`errUnknownDecisionSource` var), `app_update.go:536-540` (a half-sentence "install and the send loop
has already stopped" above `scheduleHubRestartAfterResponse`), `app_threadread.go:1530` area.

Evidence: 46 comment lines cite `#NNNN` issues, 46 cite `spec §`, 11 cite kata ids; examples like
`app_rpc.go:164` "(#2589)", `app_threadread.go:1525` "(kata kq8c)"; comments name tests ("Pinned by
TestHostAdminFanOutStopsWhenServerShutdown", `app_rpc.go:1270`). The long essays (e.g.
`mutationResumeFailureError`, 35 lines for a 15-line function) restate what the code does.

Fix plan: with each file-level refactor above, replace issue/kata/spec ids and history with a
one-paragraph "why" and let `git blame` carry the history; fix the three orphaned comments now (a
5-line PR). No lint rule; do it opportunistically. Risk: none.

### hub-app-rest-18: Self-update and restart machinery is spread over three files with label-string behavior switches (low)

Locations: `app_update.go:27-612`, `app_restart.go:1-266`, `app_upgrade.go:1-52`.

Evidence: `scheduleRestartAfterResponse(..., label string, useInstallLock bool)` branches on
`label == "restart"` (`app_update.go:~575`) to pick behavior; `hubLaunchEntrypoint`,
`errHubLaunchEntrypoint`, `hubLaunchShareBinDir`, `hubLaunchBinarySHA256`, `launchDigest*`,
`hubBoundAddr`, `hubBindWasEphemeral`, `hubUpdateMu` are package globals shared across the three
files. `hubUpgrade` and `hubUpdateApply` build the same `selfupdate.Options` literal
(`app_upgrade.go:28`, `app_update.go:318`).

Fix plan: a `selfUpdater` struct (fields replace the globals; `restartKind` enum replaces the label
string) in `cmd/evener-hub/internal/hubupdate`, one `installOptions(channel)` helper. Keep the
RPC handlers in `app_update.go`. Size: ~0 net, -40 duplicated. Risk: low-medium (restart is hard
to test; cover with the existing `app_update_test.go` seams).

### hub-app-rest-19: Small repeated tails: plugin mutate-then-list x6, archive/favorite/pin refresh tail x3, model normalization x2 (low)

Locations: `app_plugins.go:366-425`; `app_archive.go:127-135`, `app_favorite.go:41-47`,
`app_pin_section.go:160-164`; `app_models.go:471-560`.

Evidence: six plugin methods are `if err := c.mgr.X(...); err != nil { return {}, err }; return c.listPlugins(ctx)`.
Three navigation-mutating handlers each do `navigation == nil` check -> `navigation.Refresh` ->
`pokeMutationAttention(cfg)`. `sanitizeModelDescriptors` and `withDisplayNames` both loop
`normalizeModelDescriptor`.

Fix plan: `c.thenList(ctx, func() error)` for plugins (-40 lines); `refreshNavigationAfterDecision(ctx, cfg, navigation, hint) (NavigationMutation, error)`
shared by the three decision handlers (-30); fold the model loops (-15). Risk: low.

---

## Recommended fix sequence for this slice

1. Quick, no-risk deletions (one PR each, no design needed): finding 6 (dead delegate-reconcile chain,
   test-only helpers, minus the `ActiveFlags` plumbing), finding 7 (move test-only wrappers),
   finding 16 (generic pointer clone), finding 17's three orphaned comments, finding 11 step 1-2
   (duplicate registrations, `Router.Handle` panics on duplicates).
2. Finding 4 (resume wrappers via `relayWithResume`) then finding 5 (delete pre-dispatch machinery,
   with Jesse's approval). These shrink the surface finding 3 works on.
3. Finding 1 (`WireError.ErrorData()` helper, then move the correlation block). Unblocks 3.
4. Finding 3 (`mutationHandler`, split `registerThreadHandlers`).
5. Finding 2 (`hubRelays` struct), after 8 removes the observe-global dependency from tests; this is
   the highest-value but riskiest change, run on CI one closure per commit.
6. Finding 8 (test seams), finding 12 (`mutate` preamble), finding 14 (`forkAdmission`), finding 13
   (force-stop phases, alias lock helper).
7. Finding 9 phase 1 (endpoint guard consolidation); raise phase 2 and finding 10 (shared secret
   file) with Jesse as a product/security discussion before any code.
8. Finding 15 (delete coverage-padding tests) after Jesse confirms the criterion; do it per file
   alongside the production change that makes its subject dead.
9. Findings 18 and 19 opportunistically.

Docs to update in the same PRs: `docs/product/subsystems.md` only where an ownership row names a
moved file (S06 routing, credentials/S15); finding 9 phase 2 adds the endpoint-review guarantee to
the credentials product doc. No dated narratives.
