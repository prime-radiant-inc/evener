# Tech-debt audit: hub-app-host

Slice: `cmd/evener-hub/app_host*.go` (non-test). Worktree: `.worktrees/debt-audit` (origin/main).
Method: read the central files, `wc`/`grep` counts, `git log`, plus cheap static analysis on the one
package (`golangci-lint` with `unparam`, then `dupl`/`gocognit`/`gocyclo`, `--tests=false`, ~12s). No test suites run.

## Overview and inventory

| File | Lines | Comment lines | Funcs | Types |
| --- | ---: | ---: | ---: | ---: |
| app_host_manage.go | 3854 | 1626 | 120 | 17 |
| app_host_deploy.go | 1367 | 394 | 38 | 2 |
| app_host_mutations.go | 1196 | 307 | 21 | 0 |
| app_host_record_pruning.go | 999 | 220 | 16 | 1 |
| app_host_teardown_ops.go | 961 | 332 | 24 | 2 |
| app_host_ops.go | 839 | 279 | 25 | 2 |
| app_host_remnants.go | 783 | 311 | 28 | 7 |
| app_host_commit.go | 686 | 271 | 20 | 1 |
| app_host_receipts.go | 673 | 223 | 19 | 7 |
| app_host_admin.go | 658 | 330 | 20 | 2 |
| app_host_records.go | 559 | 196 | 22 | 2 |
| app_host_tombstones.go | 515 | 186 | 15 | 6 |
| app_host_credentials.go | 369 | 198 | 5 | 1 |
| app_host_running.go | 358 | 129 | 13 | 1 |
| app_host_attach.go | 309 | 154 | 5 | 0 |
| app_host_probe.go | 116 | 40 | 4 | 0 |
| **Total** | **14242** | **5196 (36%)** | **395** | **51** |

Tests: 45 `app_host*_test.go` files, 32,682 lines (2.3x the source).

What the slice is. Two unrelated things share the `app_host_` prefix:

1. The **host registry write path** (manage, mutations, commit, receipts, records, tombstones, record_pruning,
   remnants, teardown_ops, plus ops/deploy/running/probe for the deploy pipeline). This is a crash-safe,
   idempotent, two-phase-commit mutation engine over `hub.toml`: staged markers, receipts, tombstones, pruned
   markers, teardown remnants, attempts, cross-file store-sync intents, compensation records, a boot reconcile
   pipeline. About 12k of the 14k lines.
2. **Remote-host proxying** (admin: forward allow-listed admin RPCs and re-emit notifications; credentials:
   push API keys; attach: Connect). About 1.3k lines. These have no dependency on the registry write path.

Overall health. The engine is carefully built and heavily tested, but it shows classic LLM-era accretion:
every record kind, mutation kind, and review round got its own copy of the same skeleton; "slice" boundary
comments from the build-out were never updated after the later slices landed; compat decoders for formats from
the last month remain; and 36% of all lines are comments, 406 of them citing `spec 08 §N`. Maintenance drag is
concentrated in three places: the per-record-kind plumbing (finding 1, 2), the three 200-290 line mutation
functions (finding 3), and `app_host_manage.go`, which at 3854 lines holds the store, the TOML writer, the
legacy migration, the boot pipeline, the compensation resume machine, and the row builder.

Product-model notes. AGENTS.md wants automatic recovery over lockdown, and this engine does that (forward repair
through remnants, boot finalization). The fix plans below preserve every recovery path; they change structure,
not behavior. `docs/product/subsystems.md` S07 does not list any `app_host_*.go` file (it names sshconn,
hostreg, hostops only), and `docs/product/friction.md` holds line-anchored links into these files that have
drifted (finding 13).

Static analysis results used below:

- `unparam`: 7 hits in the slice (finding 12).
- `dupl` (threshold 90): `EnsureDeploy`/`EnsureRestart`; `hubTOMLStoreSyncTables`/`hubTOMLStagedReceiptTables`;
  `retryArm` switch halves.
- `gocognit` > 60: `runOperationWorker` (66). `gocyclo` > 40: `RemoveResult` (43), `deriveHostTOMLRecords` (46).
- Functions >= 100 lines: 16 (largest `RemoveResult` 284, `UpdateResult` 254, `runOperationWorker` 234,
  `deriveHostTOMLRecords` 230, `startOperation` 220, `newHubHostManager` 218).

---

## Findings

### hub-app-host-1: Nine near-identical "preserve file records for names this write does not own" functions (severity: high)

Locations:
- `app_host_records.go:354` `hubTOMLReceiptTables`, `:411` `hubTOMLTombstoneTables`, `:436` `hubTOMLPrunedReceiptTables`,
  `:469` `hubTOMLStagedReceiptTables`, `:501` `hubTOMLTeardownRemnantTables`, `:530` `hubTOMLTeardownAttemptTables`
- `app_host_manage.go:2737` `hubTOMLStoreSyncTables`, `:2767` `hubTOMLMirrorCommitTables`
- `app_host_records.go:265` `hubTOMLRecordTables` (the ninth, for host records and generations)
- helper `ownedRecordNames` at `app_host_records.go:391`

Evidence. Each function is the same shape: `out := make(map...); maps.Copy(out, carried); owned :=
ownedRecordNames(entries, known); if owned == nil { return out }; for k, v := range cfg.<Section> { skip if in
out / owned / dropped; out[k] = v }`. They differ only in the section, the key-to-owner-name rule, and whether
the "carried set wins" guard is present. That last difference is drift, not design:
`hubTOMLStagedReceiptTables`, `...RemnantTables`, `...AttemptTables`, `...StoreSyncTables`,
`...MirrorCommitTables` check `if _, carried := out[key]` first, while `hubTOMLReceiptTables` (`:354-385`) and
`hubTOMLPrunedReceiptTables` (`:436-466`) do not. The ownership set is also re-inlined three times instead of
calling `ownedRecordNames`: `app_host_records.go:267` and `:360`, `app_host_manage.go:2591`. `dupl` flags
`hubTOMLStoreSyncTables` against `hubTOMLStagedReceiptTables` as 22-25 line clones. Two of the copies carry the
identical "Unreachable: decodeHubTOMLForRewrite validates the section" comment (`:370`, `:446`).

Impact. Adding a record kind (the codebase added 5 in one month) means copying a ninth variant and choosing
which guards to include. The receipt/pruned variants can resurrect a record the derivation just moved if the
carried-set guard is missing for them.

Fix plan.
1. In `app_host_records.go` add one generic `preserveUnowned[V any](out map[string]V, fileSection map[string]V,
   owner func(key string, v V) (name string, ok bool), owned map[string]struct{}, dropped map[string]struct{})`
   containing the three skips (carried set wins, owned name, dropped).
2. Rewrite each `hubTOML*Tables` as: clone carried set, compute `owned` once via `ownedRecordNames`, call
   `preserveUnowned` with its owner rule (key itself, remnant.Host, parsed scoped key name, or via-remnant for
   attempts). Delete the three inlined owner-set loops. Move the two in `app_host_manage.go` next to the others.
3. Tests first: a table test that drives each section through the preserve helper with the cases
   {file-only unowned record kept, owned record not resurrected, dropped record not resurrected, carried record
   wins over file copy}; run it against the current functions to expose which sections lack the carried-wins case
   (red for receipts/pruned), then fix by delegation.
4. No doc change beyond comments.

Size: about -180 net lines (nine ~25-30 line bodies become ~8-10 lines each plus one ~25 line helper).
Risk: medium. These govern what survives a rewrite of `hub.toml`; the table test above and the existing
`app_host_toml_store_test.go`/`app_host_tombstones_test.go` preservation tests are the guard. Land after finding 2
or together with it.

---

### hub-app-host-2: Adding a record kind touches ~38 sites in 6 files (parallel per-kind maps) (severity: high)

Locations:
- `app_host_tombstones.go:116-172` `hostTOMLRecords`: 8 record maps + 8 `dropped*` sets + `raisedHighWater`
- `app_host_manage.go:~380-450` `hostStore` (8 maps), `:455-600` the `setX`/`xSnapshot` pairs, `:582` `installRecords`,
  `:3091` `installRestoredRecords`
- `app_host_record_pruning.go:27` `recordsSnapshot`, `:44` `sameHostTOMLRecords`, `:50` `nonNilRecords` (16 nil checks)
- `config.go:110` (`Config.TeardownAttempts` etc., outside the slice but part of the same fan-out)

Evidence. For one kind, `TeardownAttempts`, a grep for `TeardownAttempts|.attempts|droppedAttempts|attemptsSnapshot|
hubTOMLTeardownAttemptTables` outside tests yields 38 hits: manage 4, record_pruning 18, records 4, remnants 7,
tombstones 2, config 3. `hostStore` has a `set*` and a `*Snapshot` method per kind (`setReceipts`/`receiptsSnapshot`,
`setRecordMaps`/`tombstoneSnapshot`/`prunedReceiptSnapshot`, `setRemnantMaps`/`remnantSnapshot`/`stagedSnapshot`/
`attemptsSnapshot`, `setStoreSync`/`storeSyncSnapshot`, `setHighWaterMap`/`highWaterSnapshot`) plus `installRecords`
and `recordsSnapshot`, which re-list the same eight fields. `nonNilRecords` exists only to make `reflect.DeepEqual`
treat nil and empty maps alike.

Impact. High cost to extend, high chance of forgetting one site (a kind missing from `nonNilRecords` or
`installRecords` silently misbehaves at boot reconcile or after a write).

Fix plan.
1. Introduce `type recordSet[V any] struct { live map[string]V; dropped map[string]struct{} }` with
   `clone()`, `drop(key)`, and a nil-safe constructor so nil-vs-empty never needs normalizing.
2. Make `hostTOMLRecords` a struct of `recordSet`s (one field per kind); make `hostStore` hold one
   `records hostTOMLRecords` guarded by its mutex, with `snapshotRecords()`/`installRecords()` as the only
   accessors (keep the few targeted readers like `markedRemnantFor`, `remnantByID`, `openAttemptFor` as thin
   methods over the snapshot).
3. Delete `set*`, per-kind `*Snapshot`, `nonNilRecords`, `nonNilMap`, and collapse `installRestoredRecords` to
   one call. `sameHostTOMLRecords` becomes `reflect.DeepEqual` over the already-non-nil sets.
4. Behavior-preserving, guarded by the existing `app_host_toml_store_test.go`, `app_host_record_pruning` tests and
   the boot-reconcile tests. Add one test that round-trips every kind through snapshot/install so a future kind
   cannot be omitted.
5. Do it in two PRs: first the store accessors (hostStore side), then the derivation (`deriveHostTOMLRecords`).

Size: about -350 net lines across 5 files. Risk: medium-high (touches every write); mitigated by being mechanical
and by the round-trip test. Do together with finding 1.

---

### hub-app-host-3: AddResult / UpdateResult / RemoveResult repeat a ~60-line preamble and unlock by hand 57 times (severity: high)

Locations: `app_host_mutations.go:215-400` (AddResult, 182 lines), `:609-863` (UpdateResult, 254), `:884-1166`
(RemoveResult, 284; `gocyclo` 43).

Evidence. Each starts with the same sequence: `guardControllerLocalHosts` -> shape/guard refusal ->
`currentHostIdentity` -> `lookupHostMutationReceipt` (receipt arm) -> `finalizeOrphanMarkerIfAny` ->
`currentHostIdentity` again -> second `lookupHostMutationReceipt` -> `acquireHostGate` -> `cfg.mu.Lock` ->
`isMutating` -> `remnantRefusal` -> `hosts.Get`/stale-identity check. The two-lookup dance is spelled out three
times with differently named locals (`current`, `addCurrent`, `afterFinalize`, `removeCurrent`). Across the
slice there are 57 `m.cfg.mu.Unlock()` calls against 19 `Lock()` calls, because every early return unlocks by
hand; one missed unlock is a deadlock. The closing sequence (finalize, collision-dropped check, `committedArm`) is
also repeated three times, and `UpdateResult` and `RemoveResult` share the teardown-failure arm (remnant create,
`finalizeReceipt`, row stamp with `OpenRemnantID`/`EscalationAgeSec`). Two defensive dead spots sit inside:
`_ = stored` at `:793` and a branch commented "Unreachable: the vanished-entry case was folded into liveErr above"
at `:828`.

Impact. The most security- and durability-sensitive code in the slice is also the least reviewable; a fix to the
dedup order has to be made in three places (the code comments already say "exactly as AddResult and RemoveResult
read it after their own finalization").

Fix plan.
1. Extract `m.admitMutation(ctx, kind, name, mutationID, expected *hostMutationIdentity) (*admission, *appwire.HostMutationResult, error)`
   that performs guard, dedup lookup, orphan-marker finalization, re-lookup, and returns either a replay arm or
   an admission carrying the gate release.
2. Extract `m.lockedMutation(name, fn func() (T, error))` or at least `m.lockName(name) (unlock func(), err error)`
   covering `mu.Lock`, `isMutating`, `remnantRefusal` so the body uses `defer unlock()` where it can. The
   post-commit unlock/relock window in Update/Remove stays explicit but is the only manual one.
3. Extract `m.finishMutation(plan, receipt, remnant *HostTeardownRemnant, kind, ...)` for the finalize + collision
   + arm tail, and `m.teardownFailureArm(...)` for the shared update/remove failure branch.
4. Delete the `_ = stored` and unreachable branch.
5. TDD note: no behavior change; guard with the existing e2e/pipeline/round tests and add one lock-balance test
   (a table of early-refusal paths for each kind asserting the mutation lock is free afterwards via `TryLock`).

Size: about -250 net lines. Risk: medium-high (ordering and lock windows are subtle); do it after finding 1/2 and
before touching anything else in this file. Keep the commit/compensation core (`stageCommit`, `flipRuntimeSwapped`,
`finalizeReceipt`) untouched.

---

### hub-app-host-4: Stale "slice boundary" seams and comments describe a product that no longer exists (severity: medium)

Locations:
- `app_host_manage.go:229-240` (`remnantFence`, `lastKnownPublish` field comments), `:1360-1370`
  (`testOnlyRemnantGated`), `:1468` (`m.cfg.remnantFence = m.openRemnantID`), `:1662-1666` (BOUNDARY S12 comment)
- `app_host_record_pruning.go:196-209` (`remnantGated`), `app_host_deploy.go:11-20, 199-210` (`remnantOpen`, "Boundary
  seams this slice deliberately leaves"), `:1035-1050` (`lastKnownPublish`)
- `app_host_receipts.go:22-27` ("DEFERRED (S12): the two-write staged-marker protocol is the later slice's seam")
- `app_host_remnants.go:37-59` ("Boundaries this slice deliberately does not cross"), `:563`
- `app_host_commit.go:354` (BOUNDARY S13/S17), `app_host_tombstones.go:18`

Evidence.
- `newHubHostManager` assigns `m.cfg.remnantFence = m.openRemnantID` unconditionally (`:1468`), with the comment
  "A nil seam would answer 'no remnant'... so it is never left nil". Yet `remnantOpen` (`app_host_deploy.go:202-211`)
  and `remnantGated` (`app_host_record_pruning.go:203-210`) keep `if m.cfg.remnantFence == nil` branches, and their
  doc comments say "no remnant store exists yet" and "answers 'no remnant' until that slice ships a store" while
  `app_host_remnants.go` is 783 lines of that store.
- `remnantGated` also ORs in `testOnlyRemnantGated`, a production-struct field whose comment says it exists so S12
  "replaces this seam by wiring that fence" (already done).
- `app_host_receipts.go:22-27` says the staged-marker protocol is deferred and "no post-commit failure path exists
  yet"; `app_host_commit.go` (`stageCommit`, `flipRuntimeSwapped`, `finalizeOrphanMarkerIfAny`) implements it.
- `app_host_remnants.go:50-53` says no cross-file intent record exists; `HostStoreSyncIntent` and
  `compensateStagedCrossFile` (`app_host_mutations.go:437`) implement it.
- `lastKnownPublish` is never set outside `app_host_deploy_test.go:257`; production leaves it nil and the comment
  admits "nil is the honest 'nowhere to publish yet'".
- `breakBootTombstoneCollision` (`app_host_manage.go:1667`) has a bare `{ ... }` block wrapping the remnant case
  and a doc comment saying the gate "is empty today".

Impact. Misleading comments are worse than none: a reader is told a fence is unwired when it is wired, and
nil-guards imply a production path that does not exist. This also feeds finding 5.

Fix plan.
1. Make `remnantGated`/`remnantOpen` call `m.openRemnantID` directly; delete `cfg.remnantFence`, both nil
   branches, and `testOnlyRemnantGated`. Tests that need a gated name create a real open remnant in the store
   (`app_host_deploy_test.go:255-476` and the retention tests set the hooks today).
2. Delete `lastKnownPublish` and its call site (YAGNI). Re-add when the last-known store exists.
3. Delete or rewrite the stale blocks listed above; keep only comments that state current behavior.
4. Remove the bare braces in `breakBootTombstoneCollision`.

Size: about -90 lines code, -150 lines comments. Risk: low. Tests: existing remnant/retention tests, rewritten
to seed real remnants.

---

### hub-app-host-5: Two parallel seam sets for the same dependency (SSH manager plus five closures), 15 "tests, embedders" fallback branches (severity: medium)

Locations: `app_host_manage.go:97-215` (`hostManagerConfig`), `:1434-1560` (`newHubHostManager` wiring),
`:2074-2320` (`hostOnline`, `hostRow`, `attachedClient`, `registerSource`), `app_host_probe.go:103-116`
(`hostGateFor`), `app_host_attach.go:70-160`, `internal/hubcore/config.go:117-172`, `main.go:747-767`.

Evidence. The config carries `manager *sshconn.Manager` and also `client`, `online`, `clientIfAttached`,
`handshake`, `facts`, `planFacts`, `planProbe`, `deployHost`, `restartHost`, `recoverDeployment`,
`attachedFacts`, `attachUnderGate`: 12 function fields. In production, `main.go:747-767` derives all of the closure
fields from the same `sshManager`, and `newHubHostManager` then back-fills the rest from `manager` (`:1473-1560`,
6 `if m.cfg.X == nil && manager != nil` blocks). The code then branches on both: `hostRow` (`:2150-2158`) has
`if m.cfg.manager != nil { attachedClient } else if m.hostOnline(host.Name) { attachedClient }`, two arms that
make the same call (`attachedClient` itself re-checks `manager` then `clientIfAttached`). `grep -c "tests,
embedders"` in `app_host_manage.go` returns 15 nil-seam comments. `hostGateFor` returns `manager` or a standalone
gate "(tests, embedders)".

Impact. Each new capability needs a field, a back-fill block, a nil-branch, and a test seam; two sources of truth
for "is this host attached".

Fix plan.
1. Define one small `hostChannels` interface in this package (`ChannelIfAttached`, `Preflight`, `PreflightIfAttached`,
   `AddHost`, `UpdateHostUnderGate`, `RemoveHostUnderGate`, `AttachUnderGate`, `DeployForOperation`,
   `RestartForOperation`, `RecoverDeploymentForRestart` ... only what is called) implemented by `*sshconn.Manager`.
2. Replace the 12 function fields with that one interface. Tests that currently inject closures use a small fake
   implementing the interface (a boundary fake; it is the same boundary the closures cover today).
3. Delete the back-fill blocks and nil-branches; `hostGateFor` becomes part of the fake.
4. Remove the matching `RemoteHost*` closure fields from `hubcore.WebConfig` once no other consumer needs them
   (`app_rpc.go:115-118` and `app_threadlist.go:373` also read them; keep them if those readers remain, and
   migrate in a later PR).

Size: about -200 net lines in-slice. Risk: medium (wide test churn: 48 test-file references to seam fields).
Needs a short design check with Jesse before starting because it changes test wiring broadly.

---

### hub-app-host-6: Legacy `hub.hosts.json` sidecar migration and its "poisoned store" machinery (severity: medium; needs Jesse's decision)

Locations: `app_host_manage.go:45-66, 617-745` (sidecar types, `parseLegacyHostSidecar`), `:1860-1970`
(`migrateLegacyHostSidecar`, 110 lines), `:747-760` (`writeHubTOMLHostsMarked`, `migrated` parameter), `:2532`
(`persistHostsMarked`), `hostStore.loadErr`/`poison`/`poisoned` (`:601-615`), and the `legacySidecarMigratedKey`
document marker (`:753`, `:896`).

Evidence. `git log -S'hub.hosts.json'`: the sidecar was introduced in the 2026-09-20 host-registry slice and
retired by #2494 on 2026-09-26, six days later. Production code: `migrate...` (110 lines) + parse/types (~130) +
constants/comments (~30) + `migrated bool` threaded through `writeHubTOMLHostsRecords` and `persistHostsMarked` +
the `loadErr` poison guard that exists only so a failed migration refuses later writes (`persistHostsMarked`
`:2535`). All of it is exercised by 12+ test references (`loadLegacyHostSidecar` 5, `legacySidecarPathFor` 12).

Impact. About 330 lines of one-shot compat code, plus a permanent extra write-path parameter and a failure state.

Fix plan. This is compat removal and the repo rules require Jesse's explicit approval, so: ask whether any
install could still hold a `hub.hosts.json` (released tags go up to v0.3.9; the sidecar lived six days in
September). If approved: delete the sidecar types/parser/migrate/marker, drop `migrated` from the writer and
`persistHostsMarked` (merging it into `persistHosts`, see finding 8), delete `loadErr`/`poison`, remove the
migration tests. Update `docs/superpowers/specs/2026-09-16-multi-host-08-host-management-ui.md` mentions only if
it is treated as living (it is a spec record, so leave it).

Size: about -330 lines code, -400 lines tests. Risk: low if no install can carry the file; otherwise keep and
note in the code why. Do not start without the answer.

---

### hub-app-host-7: Retired-format decoders that nothing writes or reads (severity: low; needs Jesse's decision)

Locations: `app_host_records.go:43-73` (six `Retired*` fields on `HostRecord`, `withoutRetiredFencingFields`,
called from `config.go:473`), `app_host_remnants.go:183-222` (`HostCleanupHandle`: `RemoteGuardFile`,
`LeaseEntryTokens`, `FencingEpochBoot/OpSeq`, and the `case "remote-lease"` arm in `resolvable`),
`app_host_manage.go:2809-2858` (`stashPathOwnedBy` accepts the legacy `<hub.toml>.stash` spelling).

Evidence. The crash-fencing program was withdrawn; the comments say these fields are "decoded only so that a hub.toml
written by the build that carried the bootstrap stack still loads" and "No behavior reads them". The `remote-lease`
case in `resolvable()` is reachable only from two tests (`app_host_remnants_test.go:1288, 1918`) that assert it is
unresolvable. The stash comment is explicit: "the pre-rename shared name ... never shipped — this branch introduced
it" (`:2802-2808`), yet `stashPathOwnedBy` still accepts it and the comment calls it "the canonical legacy name".
(`HostTeardownAttempt.FencingEpochBoot` is not retired: `app_host_teardown_ops.go:141, 453` still use it.)

Impact. Small but permanent; every reader must work out that these are dead.

Fix plan. The stash legacy spelling is provably dead (never shipped): delete the `stash == path+".stash"` arm and
its test. The `Retired*` and `remote-lease` decoders protect real files written in late September; ask Jesse
before removing (the reserved-record rule makes an undecodable key a hard startup error, so removal is a
potential boot failure for an old file). If approved, the safer sequence is a one-time strip on load (already what
`decodeConfig` does) and then deleting the fields after one release.

Size: about -60 lines. Risk: low for the stash arm, medium for the TOML fields.

---

### hub-app-host-8: Production-unused wrappers and a four-deep writer chain (severity: medium)

Locations: `app_host_manage.go:747-791` (`writeHubTOMLHosts`, `writeHubTOMLHostsMarked`, `writeHubTOMLHostsKnown`,
`writeHubTOMLHostsRecords`), `:2517-2532` (`persistHosts` -> `persistHostsMarked`),
`app_host_mutations.go:171-197` (`Update`, `Remove`) and `app_host_manage.go:2434-2437` (`Add`),
`app_host_mutations.go:44-98` (`committedRow`, `committedHostRow`).

Evidence. Both `writeHubTOMLHostsKnown` and `writeHubTOMLHostsRecords` are documented as "the one writer".
Non-test callers: `writeHubTOMLHostsRecords` has one (`:2540`); `writeHubTOMLHosts`, `...Marked`, `...Known` have
none (only tests; the sole production reference is the doc comment chain). `Add`/`Update`/`Remove` are
"pre-union adapters": `registerHostManageHandlers` (`:2015-2062`) calls only the `*Result` methods, and no
non-test file calls `m.Add/Update/Remove` (grep). `committedRow` converts the union back to the shipped shape,
copying 16 `RemovedRow` fields by hand (`:55-72`). About 100 test call sites use `m.Add(` and ~130 use
`.Update(`/`.Remove(` forms.

Impact. Three names for one writer, three for one mutation, and an adapter whose only job is to keep tests
compiling. The "legacy caller" the comment protects does not exist in production.

Fix plan.
1. Keep `writeHubTOMLHostsRecords` (rename `writeHubTOMLHosts`); make tests build a `hostTOMLRecords` or use a
   2-line test helper `writeHosts(t, path, entries)` in a `_test.go` file. Delete the other three.
2. Delete `Add/Update/Remove` and `committedHostRow`; add a test helper `mustAdd(t, m, params) appwire.HostRow`
   (wrapping `committedRow`) so the ~230 call sites become mechanical replacements. Move `committedRow` to a
   `_test.go` file since only tests use it.
3. Fold `persistHostsMarked` into `persistHosts` (or keep `migrated` only until finding 6 is decided).

Size: about -90 lines production, +30 test helper, mechanical test edits. Risk: low. Needs no behavior change;
compile errors guide the migration.

---

### hub-app-host-9: Atomic temp-file/fsync/rename/dir-sync is copy-pasted inside the slice and re-implemented across the hub (severity: medium)

Locations: `app_host_manage.go:~908-948` (tail of `writeHubTOMLHostsRecords`) and `:2907-2941`
(`hubTOMLReplaceWithBytes`); `app_host_running.go:239-286` (`hostStateRootWriteProbe`); out of slice:
`internal/hostops/store.go:~1135-1160`, `internal/hostops/quarantine.go:~878`, `internal/hubcore/state_file.go:64-85`,
`internal/plugins/atomic.go:32`, `internal/transcriptindex/index.go:1294`, `cmd/evener-dev/agentshards.go:750`.

Evidence. The two in-slice blocks are line-for-line the same: `MkdirAll(0o700)`, `CreateTemp(".hub.toml-*.tmp")`,
`defer os.Remove`, `Chmod(0o600)`, `Write`, `Sync`, `Close`, `Rename`, then dir sync, with `_ = tmp.Close()` on three
error paths each (six occurrences). They differ only in the error wording and the post-rename error wrapper
(`hubTOMLPostRenameError`). `internal/fsdurability` exists and holds a single predicate (`SyncUnsupported`), with
the comment "the one predicate the hub's atomic-rename stores share" -- the shared piece stopped one step short
of the shared procedure.

Impact. A durability bug fix (e.g. fsync-the-parent ordering) has to be applied N times.

Fix plan.
1. Add `fsdurability.WriteFileAtomic(path string, data []byte, perm os.FileMode, syncDir func(string) error)
   (renamed bool, err error)` where `renamed` tells the caller the failure came after the commit point (so
   `hubTOMLPostRenameError` is derived from it).
2. Use it for both in-slice sites (the dir-sync seams `hubTOMLSyncDir`/`hubTOMLStashSyncDir` pass through as
   the `syncDir` argument, preserving the test injection points).
3. Follow-up PRs (separate slices) migrate the other hub copies; do not widen this PR.
4. Existing tests `app_host_manage_round15/16_test.go` (post-rename failure) and `app_host_toml_store_test.go`
   are the guard; add a unit test for the helper's pre-rename vs post-rename failure reporting.

Size: about -70 lines in-slice, +45 in `fsdurability`. Risk: low-medium (durability code, but a pure extraction
with the same seams).

---

### hub-app-host-10: `runOperationWorker` is one 234-line function holding both the deploy and the restart workflow (severity: medium)

Locations: `app_host_deploy.go:725-960` (`gocognit` 66); helpers at `:965-1200`.

Evidence. After shared setup (handoff/fail closures), a single `if work.record.Kind == hostops.KindDeploy { ...deploy
+ optional restart... } else { ...recover/attach-first/probe/restart... }` holds two unrelated flows, followed by
shared reattach/refresh/terminal code. The local state shared between the branches (`restartFollows`, `beforeProbe`,
`targetVersion`, `startedAt`, `handoff`) is mutated from inside the branches. `fail(err); return` appears about 14
times. Similarly `startOperation` (`:216-436`, 220 lines) mixes dedup, token check, remnant fence, probe window,
and record creation.

Impact. Hardest function to change safely in the deploy pipeline; the two flows evolve independently.

Fix plan. Introduce `type opRun struct{ id; work; handoff; ...}` with methods `deploy(ctx)`, `restart(ctx)`
returning a small `stepResult{restartFollows bool; beforeProbe; targetVersion; startedAt}`, and keep
`runOperationWorker` as the sequencer (interrupt checks, running transition, call deploy-or-restart, reattach,
refresh, terminal). Split `startOperation` into `resolveOperationEntry`, `admitToken`, and `createOperationRecord`.
Behavior-preserving; guard with `app_host_deploy_test.go` (2119 lines) and the restart recovery tests.

Size: about +40 / -40 net but each function under 80 lines. Risk: medium. No doc change.

---

### hub-app-host-11: `EnsureDeploy`/`EnsureRestart` and `Deploy`/`Restart` are paired copies (severity: low)

Locations: `app_host_deploy.go:1250-1282` and `:1292-1324` (`dupl` clone), `:88-157` (`Deploy`, `Restart`).

Evidence. The Ensure pair differs only in `Kind` and the two error strings ("an Ensure-triggered deploy" vs "a
restart-only Ensure attempt"). The `Deploy`/`Restart` RPC handlers end with the identical
`operationWireState` + `appwire.Host{Deploy,Restart}Response{ID, ClientOperationID, State}` block (the two response
types have the same three fields).

Fix plan. `func (m *hubHostManager) ensureOperation(host hostreg.Host, kind hostops.Kind) (func(error), error)`; the
two methods become one-line calls (they must stay separate because `manager.SetEnsureDeployHook` and
`SetEnsureRestartHook` take them). Add a tiny `operationAck(record) (id, clientOpID, state, err)` for the handler
tails. The error strings vary by kind, so derive them from `kind` ("deploy"/"restart") without asserting on copy
in tests.
Size: about -35 lines. Risk: low.

---

### hub-app-host-12: Compiler-style dead code and slop (severity: low)

Locations and evidence (all from `unparam` plus reading):
- `app_host_receipts.go:387` `newHostMutationReceipt(mutationID, kind, entry, committedAt)`: `mutationID` unused and
  `kind` also unused by the body; every caller (`app_host_mutations.go:320, 701, 973`) supplies both (`kind` is also unused by the body).
- `app_host_mutations.go:202` `seamForReceipt(receipt, kind)`: `receipt` unused.
- `app_host_remnants.go:697` `validatePendingTeardown(owner, target)`: `owner` unused.
- `app_host_teardown_ops.go:460` `attemptBusyRefusal(name, remnantID, attemptID)`: `remnantID` unused.
- `app_host_ops.go:93` `planNoToken(..., remnantID)`: always receives `""`.
- `app_host_commit.go:503` `finalizeOrphanMarkerIfAny` returns `*HostMutationReceipt` that no caller reads
  (5 callers).
- `app_host_manage.go:1068` `hostStore.remove` returns a `bool` nobody uses.
- `_ = intent` (`app_host_manage.go:3396`) and `_ = stored` (`app_host_mutations.go:793`): variables bound only to be
  silenced.
- `app_host_manage.go:2150-2158`: `if manager != nil { attachedClient } else if hostOnline { attachedClient }`.
- `hostEntryRow` (`:2107`) builds `row := ...; return row`.
- Five "Unreachable"/"Defensive only" branches with comments instead of removal
  (`app_host_records.go:370, 446`; `app_host_mutations.go:828`; `app_host_manage.go:3026, 3787`).
- `sameEffectiveHostEntryFields` (`app_host_mutations.go:596`) duplicates `hostreg.Host.Equal`
  (`internal/hostreg/hostreg.go:171`: the same eight fields, `slices.Equal` on roots); `migrateLegacyHostSidecar` already
  calls `live.Equal(e)`.
- `slicesEqualStrs` (`app_host_mutations.go:865`) reimplements `slices.Equal` (the package imports `slices`).
- `sortedStagedMarkerNames` (`app_host_manage.go:3847`, `sort.Strings`) duplicates `sortedMapKeys` (`:2984`); both are
  `slices.Sorted(maps.Keys(m))` (go 1.27 in `go.mod`).
- `mintRemnantID`, `mintAttemptID`, `mintFinalizingToken` (`app_host_remnants.go:376-384`) are three identical
  `uuid.NewString()` wrappers.
- `probeNonce` (`app_host_running.go:299`), documented for probe file names, is also the ID source for
  `newEnsureOperationID` (`app_host_deploy.go:1358`).

Fix plan. Drop the unused parameters/results (one commit, compile-guided), replace with stdlib/`Host.Equal`, delete
the silencing variables and unreachable branches, collapse the three mint helpers into one `newID()`.
Size: about -70 lines. Risk: low.

---

### hub-app-host-13: Docs drift: line-anchored links into these files and S07 map omit them (severity: low)

Locations: `docs/product/friction.md:289, 888, 916-918` (links `app_host_credentials.go#L38`,
`app_host_attach.go#L108`, `#L86`, `app_host_teardown_ops.go#L585`, `#L747`), `docs/product/subsystems.md` S07 row.

Evidence. Reading the anchored lines: `app_host_credentials.go:38` is mid-comment of `Push`; `app_host_attach.go:86`
is a comment line and `:108` is `}`; `app_host_teardown_ops.go:585` and `:747` land in unrelated comment text. All five
anchors point at the wrong code. The S07 row lists `sshconn`, `hostreg`, `hostops` but no `cmd/evener-hub/app_host_*.go`,
which hold the lifecycle handlers it describes ("Lifecycle handlers own provisioning and teardown reconciliation").

Fix plan. Replace the line anchors with file links plus function names (`hubHostAttach`, `Push`,
`runPinnedTeardown`, `resolveCleanupHandle`); add the app_host files (grouped by role) to S07's source column.
AGENTS.md requires evergreen docs in the same change as code, so do this in whichever PR moves those functions
first. Size: about 10 doc lines. Risk: none.

---

### hub-app-host-14: `app_host_manage.go` is a 3854-line grab bag; two unrelated subsystems share the `app_host_` prefix (severity: medium)

Locations: `app_host_manage.go` (120 funcs, 17 types); `app_host_admin.go`, `app_host_credentials.go`,
`app_host_attach.go`.

Evidence. `app_host_manage.go` contains: config struct (`:97-440`), `hostStore` (`:380-615`), the legacy sidecar
(`:617-745`), the TOML writer (`:747-1060`), attach state (`:1128-1420`), constructor (`:1434-1660`), boot reconcile
passes (`:1667-1860, 3000-3350`), the compensation resume state machine (`:3350-3700`), stash files (`:2809-3000`),
registration, validators, row building (`:2107-2320`), list/status (`:3759-3854`). Comments are 1626 of its lines
(42%). Separately, `app_host_admin.go` (admin proxy and notification fan-out), `app_host_credentials.go` (credential
push) and `app_host_attach.go` use `hostreg.Registry` and `appsource` only and share no state with the registry write
path. `hostNotificationBroadcaster` (`app_host_admin.go:147`) is also the broadcast seam for `app_rpc.go:2283-2306` and
`app_notices.go:258`, so it lives in the wrong file.

Impact. Navigation and review cost; a change to the boot pipeline shares a file with the row renderer.

Fix plan. Pure file moves, no code edits, in this order: `app_host_toml_file.go` (writer, document read, sync seams,
stash), `app_host_store.go` (`hostStore`, snapshots), `app_host_boot.go` (`newHubHostManager` tail,
`reconcilePipelineBoot` and passes, compensation resume), `app_host_rows.go` (`hostEntryRow`, `hostRow`,
`hostAttachState`, list/status). Put the three proxy files under a clear name prefix (`app_remote_host_*.go`) or a
package-internal directory when the package split is considered; move `hostNotificationBroadcaster` next to its
other users. Do the moves after findings 1-3 so the diff stays reviewable. Size: 0 net lines. Risk: low (moves
only); verify with `git diff --stat -M` showing renames.

---

### hub-app-host-15: `TeardownRetry` and `TeardownRecover` repeat the claim and finalize blocks; response arms are hand-expanded six ways (severity: medium)

Locations: `app_host_teardown_ops.go:78-266` (188 lines) and `:268-433` (165 lines); `:797-852` (`retryArm`,
`dupl` hit at `:801-830`); `:888-907` (`removedRowForRow`); `app_host_mutations.go:55-72` (`committedRow`),
`:181-195` (`hostRowForReceiptRow`), `app_host_receipts.go:398-440` (`hostReceiptRowFor`, `hostReceiptRow`),
`app_host_manage.go:984` (`hubTOMLHostTables`), `:2107` (`hostEntryRow`).

Evidence.
- Claim block (read open attempt, `mintAttemptID`, build `HostTeardownAttempt{RemnantID, State, StartedAt,
  FencingEpochBoot, FencingEpochOpSeq}`, fence the prior attempt, `persistHosts(entries, entries, change)`) appears
  verbatim in both. The finalize block (build `HostResolvedRemnant`, closed attempt, `resolvedReceipt`, persist,
  `dropHostDerivedState` for removed hosts) appears verbatim in both, differing only in `ResolutionKind` and the
  attestation. `m.nowTime().UTC().Format(time.RFC3339)` occurs 17 times across the slice; `entries :=
  m.cfg.store.snapshot(); m.persistHosts(entries, entries, ...)` 12 times.
- `retryArm` expands `{outcome} x {live|removed}` into six struct literals that differ only in type and the
  presence of `Seam`.
- Field-by-field row copiers: `hostEntryRow`, `hostReceiptRow`, `hostRowForReceiptRow`, `hostReceiptRowFor`,
  `hubTOMLHostTables`, `removedRowForRow`, `committedRow` (16 fields). Adding one host field means touching up to
  seven functions.

Fix plan.
1. Extract `claimRemnantAttempt(remnantID) (attemptID, attempt, remnant, doneResult, err)` and
   `clearRemnant(remnant, attempt, kind, attestation) error` (persist + derived-state drop); `TeardownRetry`/`Recover`
   keep only their distinct middle (run vs attestation + safety check).
2. Add `m.persistUnchangedHosts(change)` wrapping the `entries, entries` idiom and a `rfc3339(t)` helper.
3. Reduce the row copiers to two canonical conversions (`hostEntryRow` for `hostreg.Host`, `hostReceiptRow` for a receipt row)
   plus one `removedRowFromHostRow`; the `appwire.RemovedRow` and `HostRow` type pair is the underlying cause
   (generated union types in `appwire/types.go`: 19 `HostTeardown*`/`HostMutation*` types), so check with Jesse
   whether `RemovedRow` can embed or alias the shared field set on the wire side before expanding Go-side helpers.
Size: about -160 lines. Risk: medium (retry/recover are crash-recovery paths); `app_host_remnants_test.go`
(2249 lines) is the guard; add one test that retry and recover produce the same attempt-record transitions.

---

### hub-app-host-16: Test-only hooks live in production structs (severity: low)

Locations: `app_host_manage.go:1360-1416` (`testOnlyRemnantGated`, `testOnlyParkPostCommit`, `testOnlyParkInCommit`,
`testOnlyBeforePinnedRun`, `testOnlyAfterStage`, `testOnlyTeardown`, `testOnlyFailRuntimeRevert`,
`testOnlyFailCompensationStep`), call sites at `app_host_mutations.go:305, 553-560, 759-766, 1103-1110`,
`app_host_commit.go:169`, `app_host_teardown_ops.go:185`, `app_host_manage.go:3560-3630`.

Evidence. Eight `testOnly*` func fields on `hubHostManager`, each with a nil-guarded call in a mutation path
(48 references in tests). `testOnlyTeardown` replaces the production teardown through a `switch { case
m.testOnlyTeardown != nil: ... case m.cfg.manager != nil: ... default: ... }` that appears twice
(`app_host_mutations.go:765`, `:1109`), so the test seam is a third arm of production control flow.

Impact. Low-moderate: production code carries branches that only tests take; reviewers must reason about them.

Fix plan. Gather the hooks into one `*hostTestHooks` pointer field (nil in production) with nil-safe methods
(`h.parkPostCommit(name)`), so each call site is one line and the production struct has one field. After finding 5,
`testOnlyTeardown` can disappear because tests inject a fake `hostChannels`. Do not add new hooks.
Size: about -40 lines. Risk: low.

---

### hub-app-host-17: Comments restate spec text and review-round history (severity: medium)

Locations: whole slice; examples `app_host_mutations.go:1-31` (30 lines quoting spec §5/§6 verbatim), `app_host_manage.go:96-440`
(field comments), `app_host_credentials.go:263-291` (a 30-line comment on a 5-line heuristic),
`app_host_admin.go:488-519` (30 lines on one goroutine), `app_host_deploy.go:11-60`.

Evidence. 5196 of 14242 lines (36%) are comments; 406 lines cite `spec 08`/`§`. Many are "history" or "why we did not do X
earlier" rather than present-tense contracts: "The remote guard/lease protocol an earlier revision named here was
withdrawn", "S10 testOnlyParkPostCommit precedent", "the r6 liveness refusal stands", "(the spec-correction PR rewrites
§6/§19 from the sidecar design this tree still shows)" at `app_host_manage.go:30-33`. The mutation file quotes the spec
"verbatim" in three paragraphs, which then drifts from `docs/superpowers/specs/2026-09-16-multi-host-08b-deploy-pipeline.md`
when either changes. The review-round test names (`app_host_manage_round12/15/16/17_test.go`) show the same
accretion on the test side.

Impact. Reading cost, and comments contradicting code (finding 4). Per Jesse's naming rule, history belongs in commit
messages.

Fix plan. As each function is touched in findings 1-16, delete comments that narrate history or quote the spec; keep
a one-line pointer (`see docs/.../08b-deploy-pipeline.md §6`) and the "why" that the code cannot show. Rename the four
`*_roundNN_test.go` files by behavior (e.g. `app_host_manage_rollback_test.go`, `app_host_manage_attach_state_test.go`)
via `git mv`. No dedicated PR; fold into the others. Size: about -1500 comment lines over the sequence. Risk: none.

---

### hub-app-host-18: Some tests assert on message copy (severity: low)

Locations: `app_host_credentials_test.go:258` (`"configuration revision no longer matches"`), `app_host_admin_test.go:301`
(`"looks like a credential"`), `app_host_deploy_test.go:1870` (`want the deploy build "v1.2.3"`), `:1989`
(`"stale-entry (hub.toml-fingerprint)"`), `app_host_remnants_test.go:345` (`hostRecoveryStatement`),
`app_host_probe_test.go:239` (`"unauthenticated"`); 25 `strings.Contains(... err/Message/Reason ...)` in total
(many only check a host name like `"alpha"`).

Evidence and rule. AGENTS.md and `docs/developing-evener/testing.md` ("Copy Is Not a Test Oracle") forbid asserting on
human-readable strings. Several above supply the phrase in the test and assert it survives a pass-through (acceptable
in intent but still copy); the deploy ones assert on text the code generates.

Fix plan. Replace with typed assertions: wire error code/class (`appwire.WireError` kind), the structured
`StaleEntry` binding (`appwire.StaleEntryBinding`), or the record's state and refusal field. For pass-through tests,
compare against the exact input variable instead of a second literal. Size: about 25 test edits. Risk: low.

---

### hub-app-host-19: Hand-rolled percent codec for receipt keys (severity: low)

Locations: `app_host_receipts.go:239-312` (`percentEncodeReceiptPart`, `percentDecodeReceiptPart`,
`isReceiptUnreserved`, `receiptHexValue`).

Evidence. About 75 lines implement RFC 3986 unreserved-set percent-encoding with uppercase hex. `net/url.QueryEscape`
produces the same output for the same unreserved set (`A-Za-z0-9-_.~`) except that it encodes a space as `+`;
`parseHostReceiptScopedKey` already rejects any key that does not round-trip (`hostReceiptScopedKey(scope) != key`).

Fix plan. Reimplement as `strings.ReplaceAll(url.QueryEscape(s), "+", "%20")` and `url.PathUnescape` (strict
decoder) with the existing canonical round-trip check. Because these keys are persisted in `hub.toml`, first add a
golden test over the bytes of keys the current encoder emits (space, `%`, `/`, `+`, non-ASCII, `~`) and keep the
hand-rolled version if any case differs. Size: about -60 lines. Risk: medium for a persisted format, so only with the
golden test; if it differs, drop this finding.

---

## Recommended fix sequence

1. **Quick wins, no design needed** (one PR): findings 12 (dead params/results, stdlib replacements), 4 (delete
   `remnantFence` nil-branches, `testOnlyRemnantGated`, `lastKnownPublish`, stale slice comments), the legacy
   `.stash` spelling from 7, and the line-anchor docs fix from 13. Tests: existing suites plus rewritten
   remnant-seeding in the retention tests.
2. **Ask Jesse**: findings 6 (sidecar migration) and 7 (retired TOML decoders), and the finding 5 interface design.
   Do nothing further on those until answered.
3. **Record-table consolidation** (one or two PRs): finding 1 (generic preserve helper, red table test first),
   then finding 2 (`recordSet[V]`, single `hostTOMLRecords` in the store). These unblock everything that touches records.
4. **Mutation preamble extraction**: finding 3, then finding 15 (retry/recover claim and finalize blocks, row copiers).
   Add the lock-balance test first.
5. **Wrapper removal**: finding 8 (writer chain, `Add/Update/Remove` adapters via a test helper) after 3 so the
   mutation functions are not churned twice.
6. **Shared durability helper**: finding 9 (`fsdurability.WriteFileAtomic`), then separate PRs for the other hub copies.
7. **Deploy pipeline**: finding 10 (split `runOperationWorker`/`startOperation`) and 11 (`ensureOperation`).
8. **Seam consolidation**: finding 5 (`hostChannels` interface), then finding 16 (test hooks).
9. **File layout**: finding 14 (pure moves) once the above diffs have landed.
10. **Continuous**: finding 17 comment cleanup during every PR above; finding 18 and 19 as small follow-ups.

Net effect if all land: roughly -1,500 code lines, -1,500 comment lines, and the per-kind plumbing reduced from
~38 sites to ~3.
