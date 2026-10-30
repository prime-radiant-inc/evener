# Tech-debt audit: hub-hostops-appsource

Scope: `cmd/evener-hub/internal/hostops` and `cmd/evener-hub/internal/appsource`
(non-test). Worktree `.worktrees/debt-audit`, read-only. No test suites were run.

## Overview and inventory

| Package | Non-test files | Non-test LOC | Test files | Test LOC |
|---|---|---|---|---|
| hostops | 14 | 9,751 | 24 | 11,763 |
| appsource | 17 | 9,396 | 46 | 21,496 |
| Total | 31 | 19,147 | 70 | 33,259 |

Largest files: `hostops/store.go` 1,843; `appsource/remote_hub_source.go` 2,042;
`appsource/local_daemon.go` 1,471; `hostops/cursor.go` 1,244;
`appsource/remote_hub_subscription.go` 1,290 (51% comment lines);
`hostops/token.go` 983; `hostops/retention.go` 957; `hostops/custody.go` 921;
`hostops/quarantine.go` 916; `appsource/relay_session.go` 992.

Comment density (comment lines / total) is very high in places:
`remote_hub_subscription.go` 663/1290 (51%), `remote_hub_source.go` 837/2042
(41%), `hostops/boot.go` 41%, `hostops/gate.go` 39%, `hostops/hostops.go` 37%,
`hostops/cursor.go` 34%. About 335 `§` spec-section citations in hostops
comments point at `docs/superpowers/specs/2026-09-16-multi-host-08b-deploy-pipeline.md`,
a dated spec rather than an evergreen product doc.

Ownership context (docs/product/subsystems.md S06 and S07): appsource is the
hub's source layer (local daemon source, remote hub source, relay sessions);
hostops is the durable host-operations journal. H03 in friction.md is an
existing recorded decision about hostops startup failure scope. This audit does
not re-litigate it, only notes where code touches it.

Production consumers: hostops is consumed by `cmd/evener-hub/app_host_*.go`
and `main.go`; appsource by `app_rpc.go`, `app_relay.go`, `app_sources.go`,
`app_host_manage.go` and others.

Overall health: the code is carefully built and heavily tested, but it carries
three kinds of debt. (1) Mechanical copy-paste: roughly 55 near-identical RPC
forwarders, mirrored error mappers, mirrored decode blocks. (2) Dead or
speculative surface: a capability probe whose results nobody reads, an interface
with zero production implementers, six Store methods only tests call, retired
prior-build vocabulary kept decodable. (3) Narrative drift: stale "a later slice"
comments, review-round history in comments and test filenames, and invariants
that live only in 40-line comment blocks.

---

## Findings

### hub-hostops-appsource-1: Remote capability probe does 5+N wire reads per attach and nothing reads the result (high)

Locations:
- `appsource/remote_hub_probe.go:18-35` (HostCapabilities struct), `:99-221`
  (HostCapabilities probe), `:255-402` (clone helpers)
- `cmd/evener-hub/app_host_attach.go:196-203` (only production caller)
- `appsource/remote_hub_refs.go` (`cachedCapabilityFeatures` is the only reader)

Evidence: `HostCapabilities()` issues `evener/launch/getLayer`, `model/list`,
`evener/plugin/list`, `evener/auth/list`, `evener/instance/list` and one
`evener/launch/resolve` per configured root on every attach and every
reconnect (probe is cached per client pointer). The sole production call site
is `if _, err := capability.HostCapabilities(probeCtx); err != nil ...`, which
discards the value. A grep of non-test code for `.LaunchResolved`,
`.LaunchGlobal`, `caps.Models/Plugins/Auth/Instances/Roots` finds only the
probe's own assembly and clone code. The only consumed piece is `Features`,
which `cachedCapabilityFeatures` reads, and `client.Features()` (the initialize
handshake) already supplies it (the code comment says the mask "falls back to
the client's own initialize response"). The seven unused fields cost about 130
lines of hand-written deep-clone code (`cloneLaunchConfigLayer`,
`cloneLaunchConfigResolved`, `cloneModelDescriptors`, `cloneAuthStatusResponses`,
`cloneInstanceEntry`, `cloneInstanceListResponse`, `ptrClone`) plus about 90
lines of probe I/O and error juggling.

Impact: attach latency and remote load for data discarded; any appwire field
added to these types silently drifts from the hand clones; 400 lines of
code and about 970 test lines (`remote_hub_probe_test.go`) guard dead data.

Fix plan:
1. Confirm with a grep that nothing outside tests reads the fields (done above).
2. Reduce `HostCapabilities` to what is read: protocol version, hub version,
   OS/arch, features, source id (the facts and handshake seams). Delete the five
   hub reads, the per-root resolve loop, `Roots`, and all clone helpers except
   what the smaller struct needs (it needs none if Features is copied by value).
3. Keep the "re-probe on new client" cache only if the facts/handshake still need
   it; otherwise read facts directly where the mask needs them and drop the cache.
4. Delete the probe tests that assert the removed fields; keep tests for the
   feature mask behavior (`remote_hub_refs_test.go`).
5. Update `docs/evener-hub-remote-operations.md` if it names the probe contents.

Size: about -450 LOC source, -700 LOC tests. Risk: low-medium; check
`app_host_attach.go` warm-up timing and the `hostCapabilityProbeBudget`
constant, which becomes unneeded or shrinks.

---

### hub-hostops-appsource-2: About 55 hand-written RPC forwarders across LocalDaemonSource and RemoteHubSource (high)

Locations:
- `appsource/local_daemon.go:471-783` (about 22 methods, about 310 lines)
- `appsource/remote_hub_mutations.go:73-439` (about 24 methods, about 370 lines)
- `appsource/session_activity.go` (8 Local/Remote pair methods; Remote side
  already uses the generic `readTranslatedActivityPage` at `:139`)

Evidence: each Local method is the same six lines: resolve entry via
`entryForRef`, optionally wrap with `localDaemonMutationEntryError`, call
`withClient` or `withMutationClient` with `client.Request(ctx, Method..., params, &out)`.
Each Remote method is the same eight lines: `toRemoteRef`, copy params, set
`Ref` (and sometimes `ThreadID`), `call` or `mutationCall`, return `out`.
Examples: `SetThreadModel`, `SetThreadVisionModel`, `SetThreadReasoningEffort`,
`SetThreadName`, `CompactThread`, `ShutdownThread` differ only in the method
constant and the typed client helper; `ListTasks`, `ListJobs`, `JobOutput`,
`JobGet` are identical modulo types. Drift already exists: `CancelQueued` and
`ClearThread`/`GoalSet` use typed client helpers while siblings use
`client.Request`; local `QueueTurn` passes `""` for threadID while `SteerTurn`
passes `params.ThreadID`; `ForceStopThread` exists only on Remote.

Impact: about 680 lines for what is a method table; every new Source RPC is a
two-file copy-paste and a chance to forget skill-input gating or the
mutation-vs-plain error mapper.

Fix plan: add generic helpers in each source and make each method a one-liner
or table entry, keeping the exported method set (the `Source` interface stays):

```go
func forwardRemote[P, R any](s *RemoteHubSource, ctx context.Context, method string,
    params P, target func(*P) (ref *string, threadID *string), mutationID string) (R, error)
```

Alternatively one `remoteRefCall` taking a closure `func(ref appwire.Ref) any`
to build the forwarded params. Local side gets the same via
`localForward[P,R](s, ctx, method, ref, threadID, mutationID, params)`.
Migration order: (a) add helper plus a table-driven test that every method
translates the ref and maps errors (behavior already covered by
`remote_hub_mutations_test.go`, `local_daemon_test.go`); (b) convert the
methods in batches; (c) delete the old bodies. Behavior-preserving; no new
behavior tests needed beyond the existing suites.

Size: about -400 LOC. Risk: medium (careful with the per-method differences in
threadID handling and mutation id; keep those as explicit arguments).

---

### hub-hostops-appsource-3: Transport and mutation error mapping duplicated between local and remote sources (medium)

Locations:
- `appsource/local_daemon.go:953-1094` (`localDaemonDialError`,
  `localDaemonCallError`, `localDaemonMutationCallError`,
  `localDaemonMutationEntryError`, `localDaemonInitializeError`,
  `localDaemonSubscribeReadError`)
- `appsource/remote_hub_source.go:296-495` (`mapCallError`, `mapConnectError`,
  `transportUnavailable`, `remoteHubTransportText`, `containsWord`, `isWordByte`)
- `appsource/remote_hub_mutations.go:12-72` (`mutationCall`,
  `remoteHubMutationCallError`)
- `appsource/remote_hub_source.go:1725-1905` (`AdminCall`, `AdminMutationCall`,
  `remoteHubAdminMutationCallError`, `hubAdminMutationOutcomeUnknown`)

Evidence: `localDaemonMutationCallError` and `remoteHubMutationCallError` are
the same function (delivered-wire-error short-circuit, map, if unavailable then
build the `WireError{Code: InternalError, Data: ErrorData{EvenerErrorInfo:
ErrorMutationOutcomeUnknown, ClientMutationID, MutationOutcome: Unknown,
RetryDisposition: Automatic}}`), differing in the message text and prefix. A
third copy of the same `WireError` literal exists in
`hubAdminMutationOutcomeUnknown` (`:1883`, with `Blocked` disposition), and
others in `app_rpc.go:495` and `agent/session_client_mutation.go:46`.
`localDaemonDialError` and `transportUnavailable` carry the same sentinel list
(ECONNREFUSED, ECONNRESET, EPIPE, EOF, UnexpectedEOF, DeadlineExceeded,
net.Error timeout) and text fallback, but the remote copy has grown extra
members (io.ErrClosedPipe, os.ErrClosed, net.ErrClosed, ErrStreamClosed,
sshconn errors) the local copy lacks, so the two classify the same failure
differently. `transportUnavailable` repeats
`appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())`
five times in one function; the file has 7 such sites, plus 3 in subscription
and 1 in probe. The ctx-first dance
(`if err := ctx.Err()...; if cerr := ctx.Err()...`) appears 11 times in
`source.go`, 6 in `local_daemon.go`, 6 in `remote_hub_source.go`.

Impact: classification drift (see above) decides whether a mutation is retried
automatically, which is a data-duplication risk; fixes to one side get missed.

Fix plan: new file `appsource/errmap.go` with one
`isTransportLoss(err) bool` (union of both sentinel lists), one
`unavailable(prefix, err)` constructor, and one
`mutationOutcomeUnknown(msg, clientMutationID, disposition)` builder. Each
source passes its label ("local daemon" / "remote hub: <id>") and keeps its own
thin wrappers. Add a table test that feeds the same error set to both mappers
and asserts they agree (TDD for the local side picking up the extra remote
sentinels, which is a behavior change to confirm with Jesse: it makes local
classify closed-pipe errors as unavailable).

Size: about -150 LOC. Risk: medium (retry semantics); the shared table test is
the guard.

---

### hub-hostops-appsource-4: Retained item-paging has two parallel engines plus four copies of the position comparator, one that ignores `Sub` (medium)

Locations:
- `appsource/source.go:123-728` (LocalDaemonSource item engine hosted in a file
  named `source.go`), `appsource/item_paging_state.go` (fingerprint/digest
  engine)
- `appsource/remote_hub_source.go:665-1722` (span engine, about 1,050 lines)
- Comparators: `internal/appitempaging/page.go:194-201` (`comparePosition`
  wrapper around `ComparePosition`), `remote_hub_source.go:1158`
  (`remotePositionCompare`, byte-identical body),
  `cmd/evener-hub/internal/hubcore/message_search.go:692` (`compareItemPositions`),
  inline in `item_paging_state.go:188-189`, `:215`, `source.go:511`, `:555`
- `localDaemonCandidatesBefore` (`source.go:552`) vs `remoteCandidatesBefore`
  (`remote_hub_source.go:1164`)
- Caches: `itemSnapshotStateCache` (`item_paging_state.go:41`, container/list
  LRU) vs `remoteItemPagingCache` (`remote_hub_source.go:731`, slice-order LRU)

Evidence: `ComparePosition` orders by `Entry`, `Item`, `Sub`. Inline versions in
`source.go:555` and `item_paging_state.go:188/215` compare only Entry and Item,
so two positions differing only in `Sub` compare equal there and unequal in the
canonical function. `appitempaging.comparePosition` is a one-line pass-through
to the exported `ComparePosition`. The two caches implement the same bounded
recency map with different data structures (capacity 32 vs 64). The two engines
validate the same page properties in different helper families
(`appitempaging.ValidateCandidates` plus `validateRemotePageAdjacency` vs
`localDaemonInitialItemCandidates`).

Impact: any cursor-stability bug has two homes; the `Sub` mismatch is a latent
correctness divergence.

Fix plan: step 1 (cheap, behavior-preserving): delete the wrapper
`comparePosition`, `remotePositionCompare`, and `compareItemPositions`; use
`appitempaging.ComparePosition` everywhere, and add `appitempaging.CandidatesBefore`
replacing the two clip helpers. Add a test with positions differing only in Sub
first (red for the inline versions). Step 2: one generic bounded LRU
(`internal/boundedlru`, about 40 lines) replacing both caches; `navigation_history.go`
and `internal/appoverlay/budget.go` also hand-roll `container/list` and may
adopt it. Step 3: move the local item engine out of `source.go` into
`local_daemon_item_paging.go` (the test file of that name already exists). The
engine merge itself (one retained-window model) is out of scope for a cleanup PR
and needs a design discussion with Jesse.

Size: step 1 about -60 LOC, step 2 about -80 LOC net, step 3 file move.
Risk: step 1 low (plus one deliberate Sub fix), step 2 low.

---

### hub-hostops-appsource-5: `CombinedItemReadSource` has no production implementer; the hub branch is dead outside tests (medium)

Locations: `appsource/source.go:117-121`, `cmd/evener-hub/app_relay.go:1451-1457`,
test fake only at `cmd/evener-hub/app_rpc_item_paging_test.go:163`.

Evidence: grep for `ReadThreadWithItemCandidates` finds the interface, the one
call site, and one test fake. Neither `LocalDaemonSource` nor `RemoteHubSource`
implements it. The sibling optional interfaces are also single-implementer
shape checks: `ItemReadCandidateSource` (only Remote implements; Local
implements the method but has no `var _` assertion), `DelegateStopSource`
(both), `CapabilitySource` (Remote only, one caller, see finding 1),
`OnlineSource` (Remote only, two callers).

Impact: an untested-in-production code path in the hub read pipeline plus an
interface that suggests a contract nobody honors.

Fix plan: delete `CombinedItemReadSource`, the `combined` branch in
`app_relay.go:1451-1457`, and the test fake plus any test that drives it
(rewrite such a test to use the real `ReadItemCandidates` path). Then fold the
remaining optional interfaces: since only two concrete sources exist, consider
adding the item-candidate methods to the `Source` interface with a trivial
`ErrUnsupported` default via an embedded struct, removing five type assertions
in `app_item_page_fit.go` and `app_rpc.go`. That second step is a design change
to propose, not required.

Size: about -60 LOC. Risk: low.

---

### hub-hostops-appsource-6: Three separate ref/URL translation walkers over typed, raw-JSON and untyped-tree payloads (medium)

Locations:
- Typed: `appsource/remote_hub_refs.go:383-420` (`fromRemoteThread`),
  `:556-660` (`translateOutWithCapabilities`, 100-line type switch)
- Raw JSON: `remote_hub_refs.go:250-380`, `:443-505`
  (`translateNestedRefs`, `translateThreadRaw`, `translatePendingEscalationsRaw`,
  `maskRemoteCapabilitiesRaw`), `remote_hub_images.go:174-310`
  (`rewriteRawThreadImageURLs`, `rewriteRawTurnImageURLs`,
  `rewriteRawItemImageURLs`, `rewriteRawImageURL`, `rewriteRawArray`)
- Untyped tree: `remote_hub_refs.go:703-873` (`translateActivityRefs` and six
  node walkers over `map[string]any`)

Evidence: the same logical visit (thread, turns, items, images; sessions,
entries, jobs, delegates) is implemented once per payload representation. The
images visitor exists as typed (`rewriteThreadImageURLs`, `:149`) and raw
(`rewriteRawThreadImageURLs`, `:181`) twins. The activity walker needs two
gating functions (`activityTreeRecognized`, `activityTreeDecodable`) because it
works on `any`. `translateLegacyJobRefs` (`:791`) keeps translating "the retired
flat array of EvenerJobInfo" for older daemons, a compatibility path written
without recorded approval.

Impact: about 900 lines across three mechanisms; a new ref-bearing field must
be added to up to three walkers, and the walkers disagree on failure policy
(skip row, pass through, error).

Fix plan: (a) ask Jesse whether the old flat jobs array still needs support;
if not, delete `translateLegacyJobRefs` and its tests. (b) Introduce one small
declarative `jsonPathRewriter` (list of path templates such as
`turns[].items[].images[].url`, applied to `json.RawMessage`) and reimplement
the raw image rewriter and nested-ref translator on top of it; the typed
visitors stay for typed responses. Guard with the existing byte-for-byte
pass-through tests (`remote_hub_refs_test.go`, `remote_hub_images_test.go`).

Size: about -250 LOC. Risk: medium (byte-for-byte preservation contracts).

---

### hub-hostops-appsource-7: Seven/eight-method Store API surface that only tests call, plus two permanently unused methods (medium)

Locations (hostops):
- `store.go:444` `RecordOrReplay` (zero uses anywhere, tests included)
- `retention.go:382` `RemovedHosts()` (zero uses anywhere)
- `token.go:377` `ConsumeToken` and the `consume bool` parameter of
  `tokenPass` (`:387`); production uses `ConsumeTokenAndCreateOperation`
- `token.go:522` `RevokeTokens`; `token.go:228` `MintToken` (production uses
  `MintTokenIfQuiescent`)
- `boundary.go:132` `MirrorBoundaries` (28 test uses; production uses
  `MirrorHostState`, `retention.go:404`, which re-implements the same
  validate/clone/maps.Copy/compare/commit body)
- `cursor.go:458` `CursorEpoch()` (33 test uses; production reads
  `cursorEpochLocked`)

Evidence: a grep of non-test code under `cmd/` for `\.Name\(` finds no call
site outside hostops for these (`ConsumeToken`, `CursorEpoch`, `MintToken`,
`MirrorBoundaries`, `RecordOrReplay`, `RemovedHosts`, `RevokeTokens`).
`MirrorBoundaries` and `MirrorHostState` duplicate about 40 lines of
validation and commit logic.

Impact: tests pin helpers the product never runs, so passing tests overstate
coverage of the real mirror and token paths.

Fix plan: delete `RecordOrReplay`, `RemovedHosts`, `RevokeTokens` (revocation
is already covered by `ApplyHostRemovedPass`), `ConsumeToken` and the
`consume` flag branch if no longer needed by `ConsumeTokenAndCreateOperation`
(check `ops.go:363`), `MirrorBoundaries` (migrate its 28 tests to
`MirrorHostState` so they exercise the production path), `MintToken` (tests use
`MintTokenIfQuiescent`), and make `CursorEpoch` unexported or test-only.

Size: about -250 LOC source, tests rewritten in place. Risk: low; the mirror
test migration is the only real work.

---

### hub-hostops-appsource-8: Hand-maintained store schema in four places, with duplicate decode blocks (medium)

Locations (hostops):
- Struct tags: `store.go:101-180` (`snapshot`), `:661-701` (`storeFile`),
  `custody.go:310-332` (`custodyDocument`)
- Key allowlist: `store.go:1290-1330` (`ownedObjectKeys`, about 40 lines of
  string literals including the 16-key token row set listed twice, once for
  `tokens[]` and once for `pendingCompensation[].rows[]`)
- Decode blocks: `store.go:831-869` (`recordFile.record`) vs `:766-803`
  (`tombstoneFile.tombstone`), a near-verbatim progress and result decode
  (`DisallowUnknownFields`, null check, `OK == nil` check)
- `hostops.go:288-300`: a `for ... range []struct{raw; what}{ {FencingEpoch,
  "fencing epoch"} }` over a one-element literal

Evidence: a new persisted field requires edits to `snapshot`, `storeFile`,
`custodyDocument`, `ownedObjectKeys` (plus `cloneSnapshot`, `validateSnapshot`),
and nothing fails to compile if one is missed; the allowlist is checked only at
runtime on load. The custom `validateKeys` token walker (about 110 lines,
`:1454-1545`) exists to refuse case-variant keys because Go's JSON decoder
matches case-insensitively; this is protection against hand-edited files in a
file the hub exclusively writes.

Impact: schema drift between four lists; the key allowlist and strictness
rules make a hand-edited or slightly-off file a startup failure, which interacts
with H03.

Fix plan: (1) extract `decodeProgressAndResult(raw, id) ([]ProgressEntry,
*Result, error)` used by both record and tombstone (-30 LOC, pure refactor,
existing tests guard). (2) Delete the one-element loop at `hostops.go:288`.
(3) Derive `ownedObjectKeys` from struct tags by reflection at init (one
`keysFromTags(reflect.Type)` helper, about 25 lines) so the token key list
exists once; add a test asserting every JSON tag on `snapshot` appears in
`ownedObjectKeys`. (4) Discuss with Jesse whether the case-variant and
duplicate-key walkers earn their 150 lines for a hub-owned file; this is a
product/recovery question (a malformed-but-recoverable file currently
quarantines or blocks startup).

Size: (1)-(3) about -120 LOC. Risk: low for 1-2, medium for 3.

---

### hub-hostops-appsource-9: Retired prior-build vocabulary kept decodable and in the state machine (medium, needs Jesse's approval)

Locations (hostops):
- `custody.go:30-36`, `:79-92` (`custodyFence.Quarantine`, `.Boundary` "retired
  (comp08 2b)"), `:327-331` (`FencingQuarantines`)
- `store.go:677-681` (`storeFile.FencingQuarantines`), `:754-760`
  (`tombstoneFile.OrphanResolved`, `.Attestation`), `:805-822`
  (`recordFile.OrphanBoundary`, `OrphanResolved`, `Attestation`, `PendingSpawns`)
- `store.go:1291-1305` (allowlist entries `records[].pendingSpawns[]`,
  `records[].attestation`, `tombstones[].attestation`, `fencingQuarantines[]`)
- `intent.go:165-180`, `:182-205` (`compensating-sidecar` alias,
  `NormalizeCompensationPhase`, `normalized()`)
- `hostops.go:92-102` and `store.go:558`, `custody.go:125,143,596`
  (`StateOrphanUnverified`: "This build neither creates nor resolves it")
- `retired_keys_test.go` (159 lines pinning the retired keys)

Evidence: the comments say the fields are "decoded for tolerance, never mapped
and never emitted". `StateOrphanUnverified` is produced only when quarantine
imports a prior custody file, and `app_host_deploy.go:185` plus
`appwire/types.go:5360` carry it to the wire. This is backward compatibility
with older on-disk shapes. Per CLAUDE.md, new compat needs approval; removal of
existing compat is a product decision about whether any such stores can still
exist.

Impact: about 120 lines of source, a dozen allowlist entries, a test file, a
wire enum value, and a state-machine special case that new maintainers must
learn.

Fix plan: ask Jesse: can any prior-build (pre comp08 2b) store or custody file
still exist on a live machine? If not, delete the retired fields and the
phase alias, drop `StateOrphanUnverified` end to end (hostops, custody import
of fences, appwire enum, web/native rendering), and delete
`retired_keys_test.go`. If yes, set an explicit removal date and record it in
the subsystem map rather than leaving silent tolerance.

Size: about -250 LOC across packages. Risk: medium (wire enum removal touches
clients); gated on Jesse's answer.

---

### hub-hostops-appsource-10: Durable atomic-write and post-rename error handling re-implemented in at least four places (medium, cross-cutting)

Locations:
- `hostops/store.go:1039-1175` (`saveFS`), `:1175-1290` (`ensureStoreDir`,
  `syncDirFS`, `preservedMode`), `:46-90` (`postRenameError`, `RenameLanded`)
- `hostops/quarantine.go:839-892` (`writeQuarantineFile`)
- `hubcore/state_file.go:35-93` (`writeStateFileAtomic`),
  `hubcore/keybindings_store.go:40` (`KeybindingsPostRenameError`),
  `hubcore/write_applied.go` (`ErrWriteApplied`)
- `hubcore/recovery_store.go:158-241`, `internal/plugins/atomic.go:32`
- `hostops/store.go:67-70` comment: "The repo's host sidecar store handles the
  same class the same way."

Evidence: `internal/fsdurability` exports only `SyncUnsupported`; each store
copies temp-file, write, fsync, close, rename, dir-fsync and the "rename landed
but sync failed" classification. Three differently named types carry the same
meaning (`postRenameError`, `KeybindingsPostRenameError`, `ErrWriteApplied`).

Impact: a durability bug fix must be repeated; consumer code differs in how it
asks "did the write land".

Fix plan: grow `fsdurability` into `WriteFileAtomic(fs, path, data, opts)
(renamed bool, err error)` with the fault hooks hostops needs (`beforeRename`,
`afterRename`, `syncDir`) and one `PostRenameError` type satisfying
`errors.Is(err, ErrWriteApplied)`. Migrate hostops first (largest user, best
fault tests), then hubcore state files. Keep recovery_store's differing
contract out of scope as hubcore documents.

Size: about -250 LOC net across packages. Risk: medium-high (durability code);
needs the existing fault-injection tests to stay green. Propose as a separate
PR after design agreement.

---

### hub-hostops-appsource-11: LocalDaemonEntry is a field-for-field mirror of hubcore.LiveEntry, copied by hand at each hop (medium)

Locations: `appsource/local_daemon.go:41-118` (about 20 mirrored fields, each
with a "mirrors hubcore.LiveEntry.X" comment), `cmd/evener-hub/app_rpc.go:129-186`
(`localDaemonEntriesFromRoster` copies them), `hubcore/roster.go:28-` (LiveEntry),
`hubcore/prober.go:191` (probe to LiveEntry), `local_daemon.go:1192-1282`
(`threadFromEntry` projects to Thread).

Evidence: hubcore imports appsource (`hubcore/config.go:11`), so appsource cannot
import hubcore; the struct was duplicated to break the cycle. Adding a field
(see `LastTurnEndedAt`, `CurrentModel`, `Tasks`) means editing the prober,
LiveEntry, LocalDaemonEntry, the copier and `threadFromEntry`: five places.

Impact: field-propagation bugs; each new roster field is a five-file change.

Fix plan: define the projection input once in appsource (or a tiny leaf
package) as `LiveSession`, have `hubcore.LiveEntry` embed it (it already embeds
`rendezvous.Entry`), and delete `LocalDaemonEntry`, the copy loop and the
mirroring comments. `app_rpc.go` keeps only the alias-building logic for
descendants.

Size: about -90 LOC. Risk: medium (touches hubcore roster fingerprinting; keep
`rosterFingerprint` field coverage tests).

---

### hub-hostops-appsource-12: Stale and narrating comments, including "later slice" claims that are false (low-medium)

Locations (hostops): `cursor.go:66` ("CursorEpoch returns zero until that slice
lands", false: it returns the live quarantine epoch at `:469-471`), `store.go:95-96`
("Retention and compaction (§4) are a later slice, so nothing here yet removes a
record", false: `retention.go` exists), `store.go:38`, `boundary.go:23`,
`hostops.go:1-40` (package doc narrates slice-by-slice what "has landed"),
`custody.go:30-36` ("the later slices that once owned the verifier... were
withdrawn"), `hostops.go:215` ("today").
(appsource): `remote_hub_source.go:361` ("round eight"), `:1778` ("round
seven"), `:1791`, `:1796` ("since round nine"), `online.go`/`remote_hub_source.go:200`
("the pre-06 default"), `local_daemon.go:77` (issue #1840, #2589 style tags),
`host_routing_origin.go` and `remote_hub_source.go` citing "component 05/07 §..."
specs.

Evidence: 15 non-test matches for `round <n>` / `since round` / `issue #` in the
two packages. `AGENTS.md` asks for evergreen docs and CLAUDE.md for no
history comments.

Impact: misleading to readers (the false statements) and noisy; spec section
numbers rot because they cite a dated spec.

Fix plan: delete the false and history sentences; rewrite the hostops package
doc as a present-tense map of files and invariants and move the invariant prose
(store mutex order, custody-first order, token supersede, cursor pinning) into
`docs/evener-hub-remote-operations.md` or a new `docs/product`-linked
`host-operations.md` with stable names; replace `§N` citations with links to
that doc. Pure comment edits, no behavior.

Size: about -400 comment lines. Risk: none.

---

### hub-hostops-appsource-13: Subscription and admin-mutation concurrency contracts live only in 40-line comment blocks (low-medium)

Locations: `appsource/remote_hub_subscription.go:160-334` (SubscribeThread is
175 lines of which about 100 are comments), `remote_hub_source.go:1740-1810`
(`AdminMutationCall` doc is 65 lines of review-round reasoning),
`remote_hub_source.go:340-375` (`transportUnavailable` doc).

Evidence: the file is 51% comments; decision rationale is embedded as
"round N" narration (see finding 12). `SubscribeThread` mixes target
derivation, parameter scrubbing, admission, detached request, and four
distinct outcome branches in one function with five near-identical
`SessionUnavailable("remote hub unavailable: "+s.id+": ...")` returns.

Impact: hard to review; the invariants are not discoverable from product docs.

Fix plan: split `SubscribeThread` into `newRemoteSubscription` (target and
params), `awaitSubscribe` (the select) and keep the pump as is; collapse
the repeated unavailable constructors (finding 3); move the invariants into a
short "Remote subscription lifecycle" section of
`docs/evener-hub-remote-operations.md`. No behavior change; existing
`remote_hub_subscription*_test.go` guards.

Size: about -150 LOC net. Risk: low-medium.

---

### hub-hostops-appsource-14: Test files named for review rounds and coverage chasing; tests assert on message copy (medium)

Locations (tests): `hostops/quarantine_round1_test.go` ... `_round6_test.go`
(1,500 lines), `appsource/remote_hub_subscription_roundnine_test.go`,
`_roundten_`, `_roundeleven_`, `_roundtwelve_` (1,070 lines),
`appsource/coverage_completion_test.go`, `local_daemon_covtest_test.go`,
`local_daemon_gaps_test.go`, `relay_session_gaps_test.go`,
`cov_rp_local_helpers_test.go`, `bounded_complete_corrections_test.go`,
`paging_corrections_test.go`, `flag_day_item_only_test.go`; copy assertions
(`strings.Contains(err.Error(), "...")` against literals):
`remote_hub_source_paging_test.go:1549,1570,1595,1607,1648`,
`remote_hub_refs_test.go:257,341,359`, `remote_hub_mutations_test.go:508`,
`remote_hub_subscription_test.go:425`, `transport_seams_test.go:1175`,
`hostops/retention_test.go:824,840`, `hostops/gate_test.go:61,110,168`,
`hostops/quarantine_round3_test.go:114`.

Evidence: `docs/developing-evener/testing.md` "Copy Is Not a Test Oracle"
forbids asserting human-readable phrases; AGENTS.md repeats it. About 14
literal-phrase assertions found, several asserting the same string
("source not found: other") in three files. Files named by round/coverage
describe history or intent to raise a number, not the behavior under test.

Impact: renaming an error message breaks tests; the file layout hides which
behavior each file pins; coverage-driven files tend to test implementation
detail.

Fix plan: assert on typed sentinels (`errors.Is`/`errors.As`, wire code,
`EvenerErrorInfo`) instead of text; where no typed signal exists, add one
(e.g. a `TranscriptItemCursorStale` variant) rather than matching text. Merge
round files into the subject files (`quarantine_test.go`,
`remote_hub_subscription_test.go`), name by behavior, and delete duplicated
cases in the process. Also dedupe identical fixtures (`positionedItemReadResponse*`
helpers vs `cov_rp_local_helpers_test.go`).

Size: net -600 to -1,000 LOC of tests. Risk: low if done as move-then-trim with
coverage compared on CI (do not run locally per repo rule).

---

### hub-hostops-appsource-15: Wrapper and defensive-check sprawl in hostops (low)

Locations: `if s == nil { return ..., errors.New("hostops: store is not configured") }`
repeated in 29 methods (`boundary.go` 3, `probeepoch.go` 6, `ops.go` 7,
`intent.go` 8, `reconcile.go` 4, `retention.go` 4, `token.go` 5, `store.go` 7);
callers nil-check as well (`app_host_deploy.go` 5, `app_host_ops.go` 5,
`app_host_manage.go` 3, ... 17 sites `m.cfg.ops == nil`);
`host == "" || !utf8.ValidString(host)` repeated 3 times in token.go alone;
`ProcessGate` (`gate.go:123-235`) is used only when `sshconn.Manager` is nil
(`app_host_probe.go:103-107`), i.e. tests ("embedders" do not exist in-tree).

Evidence: the same nil store check is made at both the call site and every
method. `Gate` has implementations `*sshconn.Manager`, `*ProcessGate`, and four
test wrappers.

Impact: noise and a false sense of nil-safety; 235 lines of gate used only in
tests live in the production package.

Fix plan: make `*Store` always non-nil at construction (hub startup already
fails or degrades; see H03) and remove the receiver nil checks; keep a single
nil check at the hub boundary. Move `ProcessGate` to a `_test.go` helper or
`hostopstest` subpackage and have `hostGateFor` require a manager. Fold the
host-name validation into one `validateHost(host)`.

Size: about -150 LOC. Risk: low-medium (H03 recovery path may rely on a nil
store; check `main.go` `openHostOpsStore` before removing).

---

### hub-hostops-appsource-16: `LocalDaemonSource` carries a legacy two-step relay hand-off that the hub's atomic path bypasses (low, needs verification)

Locations: `appsource/local_daemon.go:35-44` (`legacyRelays`, `legacyRelayRead`),
`:354-402` (`ReadThread` with `Subscribe`), `:785-835` (`SubscribeThread`
consuming the pending lease).

Evidence: `app_relay.go:1449,1580` route any `RelaySessionSource` (which
`LocalDaemonSource` is, `local_daemon.go:48`) through
`lease.ReadWithRoutePublication`/`Listen`, never `Source.ReadThread(Subscribe)`
then `SubscribeThread`. The `legacyRelays` map and `context.AfterFunc` cleanup
therefore look reachable only from direct source callers and tests
(`local_daemon_test.go` has 4 `SubscribeThread` calls). I could not rule out
the recovery path at `app_relay.go:2023` (`subscribeRelayRecovery` calls
`source.SubscribeThread` for any source), so this needs a short trace before
deleting.

Impact: about 90 lines of subtle lease-handoff code with its own race surface.

Fix plan: trace `subscribeRelayRecovery` for a local source; if local never
reaches it, delete `legacyRelays`, make `ReadThread(Subscribe=true)` return an
`InvalidParams`-class error for the local source, and simplify `SubscribeThread`
to acquire-read-listen. Guard with the relay supervisor tests in `cmd/evener-hub`.

Size: about -90 LOC. Risk: medium until traced.

---

### hub-hostops-appsource-17: `cursor.go` operation pagination is heavyweight for a store bounded at 500 terminal records (low, discuss)

Locations: `hostops/cursor.go` (1,244 lines), `hostops/retention.go:42-56`
(defaults), `retention.go:281-300` (`CompactionMark`), `store.go` fields
`CompactSeq`, `CompactionMarks`, `CompactionFloor`.

Evidence: the retention defaults cap terminal records at 50 per host and 500
store-wide (`DefaultTerminalPerHost`, `DefaultTerminalStoreWide`), yet reads
use a versioned base64url cursor envelope with a pinned quarantine epoch, a
per-host bounds map (cap 8 KiB), a window mode, a compaction ledger with a
dropped-marks floor and coarse-refusal logic, and tombstones to answer
dedup replay. A page is 50 by default, 200 max, so the whole store is at most
about 3 pages.

Impact: roughly 2,000 lines (cursor + retention + their tests) to page at most
a few hundred rows; every refusal path forces the client to re-list.

Fix plan: not a cleanup PR. Raise with Jesse whether `evener/host/operations`
can return the (bounded) full list or a simple id-after cursor, with
tombstones kept only for dedup replay. If agreed, this deletes the cursor
envelope, compaction ledger and floor. Needs an appwire contract change and
client updates, so treat as a design decision, not a refactor.

Size: potentially -1,200 LOC source, -1,700 LOC tests. Risk: high (wire
contract); defer until decided.

---

## Recommended fix sequence

1. Finding 12 (comment cleanup and false statements). Zero risk; do first
   so later diffs are readable.
2. Finding 1 (delete unused capability probe data). Biggest single deletion,
   low risk, removes wire traffic.
3. Finding 5 and finding 7 (dead interface, dead/test-only Store methods).
   Small, independent deletions; migrate the `MirrorBoundaries` tests to
   `MirrorHostState` inside the same PR.
4. Finding 4 step 1 (single position comparator, with the Sub regression test
   written red first), then step 2 (shared LRU) and step 3 (move the item
   engine out of `source.go`).
5. Finding 2 (generic forwarders), then finding 3 (shared error mapper) in
   that order, since the forwarders reduce the call sites the mapper must
   cover. Ask Jesse about the local-side sentinel change in finding 3.
6. Finding 14 (rename/merge round and coverage test files, replace copy
   assertions) alongside whichever PR touches each subject file.
7. Finding 8 (schema dedupe: decode helper, one-element loop, tag-derived
   allowlist) and finding 15 (nil checks, ProcessGate to test code).
8. Finding 11 (single live-entry type) and finding 13 (SubscribeThread split)
   as standalone refactors.
9. Questions for Jesse before any work: finding 9 (retire prior-build
   vocabulary and `orphan-unverified`), finding 6a (legacy jobs array),
   finding 16 (trace first), finding 10 (shared durable-write helper), and
   finding 17 (operations pagination design). None of these start without an
   answer.
