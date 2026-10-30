# Tech-debt audit: hub-hubcore

Slice: `cmd/evener-hub/internal/hubcore` (non-test). Audited at worktree
`.worktrees/debt-audit` (origin/main). Read-only; no test suites were run.

## Overview and inventory

| Metric | Value |
| --- | --- |
| Non-test Go files | 34 (13,452 lines) |
| Test Go files | 82 (26,165 lines, 1.95x the source) |
| Exported + unexported funcs/methods (distinct names) | 461 |
| Comment lines / total lines | 3,339 / 13,452 (24%); tree.go 29%, registry.go 43%, config.go 48% |
| Longest functions | `buildTreeAtWithProjects` 834 lines, `Roster.refresh` 211, `rosterFingerprint` 202, `StatusProber.Probe` 122, `MessageSearch.Refresh` 110 |
| Largest files | tree.go 1921, past.go 1730, roster.go 1630, resumelocks.go 1009, pin_section.go 752, message_search.go 729, registry.go 727 |
| Consumers | `package hub` (cmd/evener-hub, 137 non-test files); 1,307 `WebConfig{` literals in tests |

What lives here, by concern:

- Navigation tree: `tree.go`, `attention.go`, `root_index.go`, `session_order.go`, `treecache.go`.
- Live daemon roster and probing: `roster.go`, `prober.go`, `roster_unix.go`, `roster_other.go`, `daemon_auth.go`.
- Session index and search: `past.go`, `past_root_fingerprint.go`, `message_search.go`, `remotecache.go`.
- SQLite `index.db` decision stores: `archive.go`, `favorite.go`, `favorite_authority.go`, `pin_section.go`, `session_seen.go`, `index_schema.go`, `sqlite_dsn.go`.
- JSON state stores: `keybindings_store.go`, `transcript_display_store.go`, `deletion_store.go`, `recovery_store.go`, `state_file.go`, `write_applied.go`.
- Resume ownership and force-stop recovery: `resumelocks.go`.
- Provider registry and credential revision: `registry.go`.
- Config and grab-bag: `config.go` (84-field `WebConfig`), `hostplan.go`, `proxy.go`, `types.go`.

Overall: the package is carefully reasoned (most comments explain real races) but
shows the classic LLM-accretion pattern. Each new "fact" or store is added by
copying the previous one, a few god functions and god structs absorb every
requirement, production code carries test-only entry points and seams, and
changes are narrated with issue numbers in comments. The biggest levers are (1)
break up `buildTreeAtWithProjects` and collapse the live-fact propagation chain,
(2) extract the SQLite store and JSON store skeletons the package has already
copied four and two times, and (3) delete the dead and test-only production
surface.

Findings are ordered roughly by value. Locations are `file:line` against this
checkout.

---

## hub-hubcore-1 (high): `buildTreeAtWithProjects` is an 834-line function with 15 single-use closures and three hand-built node variants

Locations:
- `tree.go:921-1755` (the function).
- Closures that are called exactly once (inside `liveFieldsFor`): `askPendingFor`, `approvalPendingFor`, `firstApprovalFor`, `pendingQuestionFor`, `failureFor`, `tasksFor`, `subagentsFor`, `lastMessageFor`, `modelFor`, `turnEndedAtFor`, `dormantFor`, `watchesFor`, `runningSubagentState` (`tree.go:1038-1240`, `grep -c "name("` returns 1 for each; `stateFor` is the only one with more than one call, 4).
- Three separate overlays after `liveFieldsFor`: `buildNode` (`tree.go:1334-1352`), the meta-less Live leaf (`tree.go:1610-1620`), and the NeedsYou node (`tree.go:1706-1734`). Each re-sets `Title/Project/Branch/Kind/CreatedAt/UpdatedAt/Age`.
- `workingRoots` rollup walk inline at `tree.go:1375-1425`; project accumulation at `tree.go:1250-1332`; project tier split/rollup at `tree.go:1428-1600`.

Evidence: the function builds the live-entry indexes, project accumulators,
node builder, rollup, tier split, Live tier, NeedsYou tier and sorts in one
scope. Every closure comment exists to justify "so every builder agrees", which
is a symptom: the shared fact lookup is not a value, it is 15 closures over
`liveMap`. `liveFieldsFor` (`tree.go:1192`) is the only caller of 13 of them.
The NeedsYou branch even re-assigns `node.State = st` (`tree.go:1707-1709`)
after `liveFieldsFor` already set the same value via `stateFor`, and sets
`Kind = "session"` while titling with `nodeKind(*meta)`, so a fork original
could carry different Title kind and Kind.

Impact: this is the most-edited file (46 commits; roster/tree facts S1b, S1c,
S1d, S3, S4, S17 each added a closure here). Adding any row fact means touching
the closure block, `liveFieldsFor`, three overlays, and the clone function.
Nothing in the function is unit-testable without a full BuildTree call, which
is why `tree_test.go` is 2,350 lines.

Fix plan (behavior-preserving; `tree_test.go`, `tree_live_agreement_test.go`,
`buildtree_fuzz_test.go` and the navigation tests are the guard):
1. Replace the 13 single-use closures with one pure function
   `liveRowFacts(entry LiveEntry, meta *schema.SessionMeta, runningState string, indexed bool) TreeNode`. Build `map[string]TreeNode` once per call from `live`, and have the three node paths copy from it. `stateFor` becomes a lookup into that map plus the running-subagent fallback.
2. Introduce a small unexported `treeBuild` struct holding `liveMap`, `metaMap`, `roots`, `runningSubagentIDs`, `reportedBy`, `decisions`, `now`, `resolvedProjects`. Make the phases methods: `indexLive`, `groupProjects`, `buildNode`, `workingRoots`, `projectTiers`, `liveTier`, `needsYouTier`.
3. Add one `metaRowFields(m schema.SessionMeta, kind string) TreeNode` shared by `buildNode`, the NeedsYou node and (with a `liveOnly` variant) the meta-less leaf. Delete the duplicated `Title/Project/Branch/CreatedAt/UpdatedAt/Age` overlays and the redundant `node.State = st`.
4. Land in three PRs by phase (indexes + live facts, node builder, tiers) with the existing tests untouched.
Docs: no product doc change; keep `docs/developing-evener/agentic-testing.md` reference to `NormalizeState` valid.

Size: roughly 500-600 lines moved, net -150 to -250.
Risk: medium. Ordering is covered by tests; the rollup and Live-tier agreement tests must stay green at each step.

---

## hub-hubcore-2 (high): adding a live fact touches seven hand-synchronized places (LiveEntry/ProbeResult twin structs)

Locations:
- `roster.go:28-135` `LiveEntry` (about 35 fields) and `roster.go:137-200` `ProbeResult` (about 30 fields, ~25 identical names and types).
- `prober.go:165-196` `ProbeResultFromThread`, `prober.go:205-224` `LiveEntryThreadFacts`, `roster.go:1500-1514` `liveEntryFromProbe`, `roster.go:239-258` `CloneLiveEntry`, `roster.go:472-677` `rosterFingerprint`, `tree.go:1192-1220` `liveFieldsFor`, `tree.go:141`/`cloneTreeNodesContext` (`tree.go:113-139`), plus `TreeNode` (`tree.go:396`).
- Doc comments on each probe field say "mirrors LiveEntry.X" (`roster.go:140-185`).

Evidence: `ProbeResult.PendingEscalations`, `PendingQuestion`, `Failure`, `Capabilities`, `RunningJobs`, `CompletedJobs`, `Lifecycle`, `Watches`, `ChildWatches`, `Tasks`, `Activity`, `Subagents`, `LastTurnEndedAt`, `Profile`, `LastMessage`, `CurrentModel` each appear in both structs with the same type and a paraphrased comment. `LiveEntryThreadFacts` is a 16-line field-by-field copy created to stop a field being "missed on one path (#2962, #2638)", which names the problem without removing it. `liveEntryFromProbe` then copies another 8 fields by hand and re-clones.

Impact: each live fact (S1b, S1c, S1d, S3, S4, S17, #2506) required edits in all seven sites; the fingerprint site fails silently (stale navigation) if forgotten.

Fix plan:
1. Embed the probe-derived facts once: define `type ThreadFacts struct { ... }` holding the fields common to `ProbeResult` and `LiveEntry` (everything except `OK`, `ProtocolMismatch`, `SessionID`, and roster-only fields like `Crashed`, `Entry`), with one `Clone()` method. `ProbeResult{ThreadFacts; SessionID; OK; ProtocolMismatch}` and `LiveEntry{rendezvous.Entry; ThreadFacts; SessionID; Crashed}`.
2. Delete `LiveEntryThreadFacts` and the copy block in `liveEntryFromProbe`: assignment becomes `live.ThreadFacts = result.ThreadFacts.Clone()`. `CloneLiveEntry` shrinks to `out.ThreadFacts = in.ThreadFacts.Clone()`.
3. Keep a reflection guard test (like `TestShownMetaFieldsCoverSessionMeta`, see finding 6) that fails when a `ThreadFacts` field is not classified as fingerprinted or deliberately excluded.
4. Update `web_api_tree.go:1077` (the only external caller) to the new shape.
Docs: none beyond Go comments.

Size: -120 net in hubcore, ~20 lines touched in `web_api_tree.go`.
Risk: medium; mechanical, compile-guided. The exported field set moves under an embedded struct, so struct literals in 158 test call sites of `NewRosterWithEntries` keep working only for promoted fields in selectors, not in literals. Plan the test-literal sweep up front.

---

## hub-hubcore-3 (high): `PinSectionStore` repeats a transaction retry loop five times, with 27 string-matching retry checks and 2,682 test lines

Locations:
- Five copies of `for attempt := range 8 { db := openWriteAttempt; BeginTx; ...; Rollback; Close; isSQLiteRetryable; continue }` at `pin_section.go:250` (Assign), `:327` (CreateOrReuseAndAssign), `:401` (Rename), `:502` (DeleteSection), `:567` (DeleteSession).
- `isSQLiteRetryable` matches `strings.Contains(strings.ToLower(err.Error()), "busy")` (`pin_section.go:738-744`); `grep -c isSQLiteRetryable pin_section.go` = 27.
- Tests: `pin_section_{coverage,gaps2,nonretry,retry,migration,source,}_test.go` total 2,682 lines for a 752-line source file; `pin_section_nonretry_test.go` has 12 copies of `strings.Contains(err.Error(), "locked")`; `pin_section_gaps2_test.go` has 18 comments citing source line numbers (e.g. "lines 131-134").

Evidence: each of the five bodies is the same ceremony around a distinct middle (three or four `*Tx` helper calls). The retry predicate is a substring match on the driver's message including the bare word "busy", while the DSN already sets `busy_timeout(5000)` (`sqlite_dsn.go:19`) and `Assign`-style callers re-run the entire open path on retry.

Impact: every new write operation copies 40 lines and about 6 error branches; each branch demands its own test, which is where the 2,682 lines (and the coverage-chasing files, finding 19) come from. A string-matched retry is also brittle against a driver upgrade.

Fix plan:
1. Add `func (s *PinSectionStore) writeTx(label string, fn func(tx *sql.Tx) error) (retryExhausted error)` that owns attempt counting, open, BeginTx, rollback, close, retry classification, commit and the "retry limit reached" error. Each public method becomes about 12 lines: normalize inputs, call `writeTx` with a closure over the existing `*Tx` helpers, fire change.
2. Classify retryability with the driver's typed error (modernc `*sqlite.Error` code `SQLITE_BUSY`/`SQLITE_LOCKED`) via `errors.As`, falling back to nothing else. Delete the substring path.
3. Collapse the five error-branch test families to table tests against `writeTx` (inject failures at begin/helper/commit) plus one happy-path test per operation. Drop line-number comments.
Docs: none.

Size: -350 source lines, -1,200 to -1,500 test lines.
Risk: low-medium. Behavior is covered by the retry/nonretry tests that become the `writeTx` table. Keep one concurrency test.

---

## hub-hubcore-4 (medium): Archive/Favorite/SessionSeen/PinSection stores are four copies of one SQLite store skeleton, with a half-fake `afero.Fs` seam

Locations:
- Identical struct preamble (`dbPath`, `fs`, `openDB`, `onChange`), `SetOnChange`, `fireChange`, `SetFs`, `open()` (`MkdirAll`, `sql.Open`, `CREATE TABLE IF NOT EXISTS`, `ensureIndexSchema`): `archive.go:60-135`, `favorite.go:14-76` ("cloned from ArchiveStore's shape", `favorite.go:13`), `session_seen.go:19-129`, `pin_section.go:86-216`.
- `Set`/`Delete` bodies in `archive.go:141-159,198-216` vs `favorite.go:60-110` differ only in table/column names (`archived` vs `favorited`).
- Each call opens a fresh `sql.DB`, re-runs `CREATE TABLE IF NOT EXISTS` and `ensureIndexSchema` (a `PRAGMA user_version` read), then closes it.
- `SetFs` has no production caller (`grep` of non-test code returns none, 25 test uses). `archive.go:62-68` documents that `sql.Open` does not use the injected `fs` at all, so the seam fakes only `MkdirAll`/`Stat`.

Evidence: four stores, each about 40 lines of identical plumbing, all documented as writing the same file.

Impact: any change to open behavior (pragmas, schema ensure, DSN) must be made four times; `sqliteDSN`'s doc comment already has to enumerate "five stores" that share the file.

Fix plan:
1. Extract `indexDB` in a new `index_db.go`: `newIndexDB(path string) *indexDB`, `(*indexDB).open(tables ...string) (*sql.DB, error)`, `onChange` hook with `fire()`. Embed it in the four stores. Stores keep their SQL.
2. Merge Archive and Favorite behind one `decisionTable{name, valueColumn}` helper (`Set/Delete/Read`), keeping the two exported types as thin wrappers so callers do not change (no behavior change; do not add compat shims beyond that).
3. Ensure the schema once per store (`sync.Once` keyed by path) instead of per call. Verify with `index_schema` tests.
4. Delete `SetFs` and the `afero.Fs` field from the SQL stores; tests use `t.TempDir()` for real files. Delete the `openDB` injection where only fault tests use it, replacing with real failure injection (read-only dir).
Docs: update `sqlite_dsn.go` comment, no product docs.

Size: -250 source, about -300 test lines.
Risk: low-medium; the per-call open/close also papers over cross-process behavior, so keep the `_txlock` and WAL pragmas in the DSN.

---

## hub-hubcore-5 (medium): Keybindings and TranscriptDisplay stores are structural copies, with duplicated helpers and three different post-write error types

Locations:
- `keybindings_store.go` (318) vs `transcript_display_store.go` (406): same `Snapshot/Patch/loadErr/faults/newXStoreFS/saveXSnapshotFS/validateXSnapshotShape` skeleton. Verbatim-identical helpers: `keybindingsSnapshotObjectFields` (`keybindings_store.go:250-259`) = `transcriptDisplaySnapshotObjectFields` (`transcript_display_store.go:333-342`); `requireKeybindingsSnapshotFields` (`:261-268`) = `requireTranscriptDisplaySnapshotFields` (`:344-351`).
- Hand-written JSON shape validators with required-field walking: `validateKeybindingsSnapshotShape` (`keybindings_store.go:220-248`) and `validateTranscriptDisplaySnapshotDefaultShape` (`transcript_display_store.go:268-331`, 70 lines).
- Error types: `KeybindingsPostRenameError` (Unwrap includes `ErrWriteApplied`, `keybindings_store.go:47`), `TranscriptDisplayPostApplyError` (Unwrap returns only `Err`, `transcript_display_store.go:48`), and `app_write_applied.go`'s `appliedError`. Only the first answers `errors.Is(err, ErrWriteApplied)`, so the transcript store's applied-write case is invisible to `writeDidApply` (`app_write_applied.go:94`) unless the RPC layer special-cases it (`app_rpc_transcript_display.go:34`).
- `ForTest` constructors in production: `NewKeybindingsStoreForTest` (`keybindings_store.go:70`), `NewTranscriptDisplayStoreForTest` (`transcript_display_store.go:73`).
- `deletionSyncUnsupported` (`state_file.go:125-129`) is a pass-through to `fsdurability.SyncUnsupported` kept "because the deletion store's tests use that name"; it is used by non-deletion code (`state_file.go:56,76`).

Evidence: see above; `diff`-level identical functions, and a documented-by-comment inconsistency in applied-write semantics.

Impact: a bug fix in one validator or error type must be mirrored by hand; the missing `ErrWriteApplied` on the transcript error is a latent "other clients stay on the old revision" bug of the exact class `write_applied.go` was written to prevent.

Fix plan:
1. Move `objectFields/requireFields/requireBooleans` into `state_file.go` as `jsonShape` helpers (one copy). Delete the two duplicated pairs.
2. Make `TranscriptDisplayPostApplyError.Unwrap` return `[]error{e.Err, ErrWriteApplied}`, with a regression test (red first: `errors.Is(err, ErrWriteApplied)` false today).
3. Replace the two `ForTest` constructors with the existing `newXStoreFS` plus an exported `Faults` option only if cross-package tests truly need it; otherwise move those tests into the package or use `hubtest`.
4. Rename `deletionSyncUnsupported` call sites to `fsdurability.SyncUnsupported` and delete the wrapper.
5. Optional second step: replace the hand-written shape validators with `decodeStateFileStrict` plus pointer fields (a `*uint64`, `*bool` struct for decode, nil means missing). That deletes about 150 lines but changes error text, so it needs Jesse's call.
Docs: none.

Size: steps 1-4 -90 lines; step 5 -150 more.
Risk: low for 1-4.

---

## hub-hubcore-6 (medium): three change-detection fingerprints, two hand-written; `rosterFingerprint` is 202 lines of `h.Write` calls and ignores the guard-test pattern `rootFingerprint` already has

Locations:
- `roster.go:472-677` `rosterFingerprint` plus `writeWatchFingerprint` (`:679-738`), `writeTaskFingerprint` (`:740-765`): about 290 lines of `_, _ = h.Write([]byte(x)); _, _ = h.Write([]byte{0})`.
- `past.go:223-242` `contentFingerprint` (hand-written hash of 4 fields).
- `past_root_fingerprint.go:1-152` `rootFingerprint`: a declared `shownMetaFields` / `notShownMetaFields` list, reflection hashing (`writeHashValue`), and a completeness guard test.

Evidence: `rosterFingerprint` carries one comment per field ("(S1b)", "(S1c)", "(S17)"), shows manual sorting of copies (`runningJobs := append([]appwire.EvenerJobInfo(nil), ...)`), and already uses reflection for `Capabilities` (`roster.go:553-565`) with the comment "so a capability bit added later moves the fingerprint without anyone having to remember this site", i.e. the authors saw the problem. `past_root_fingerprint.go` solves it generically and has `writeHashString`/`writeHashValue` helpers that the roster does not use.

Impact: a field added to `LiveEntry` and forgotten here silently produces missed invalidations; the file is hard to review.

Fix plan:
1. Generalize `writeHashValue` (add `Map` with sorted keys, `Ptr`, `Float`, `Struct` recursion) and move it plus `writeHashString` to `fingerprint.go`.
2. Define `shownLiveFields` / `notShownLiveFields` over the `ThreadFacts` struct (finding 2) with a guard test mirroring `TestShownMetaFieldsCoverSessionMeta`. Keep order normalization (sort flags, jobs, watches) as small canonicalizers applied before hashing.
3. Delete the hand hash. Expected result is about 60 lines for fingerprint + guard test, versus 290.
4. Test: golden pairs (two entries that must differ, two that must not: reordered flags/watches) written red-first against the old function, then switch.
Docs: none.

Size: -230 source lines.
Risk: medium; the fingerprint is an invalidation gate, so equality semantics must match exactly (Activity and Profile are intentionally excluded, `roster.go:105,119`).

---

## hub-hubcore-7 (medium): dead and test-only production surface (RESTProxy, `RemoteThreadCache` compat stores, and others)

Each item below has zero non-test callers outside its own file (verified with grep over `cmd`, `agent`):

- `RESTProxy`, `NewRESTProxy`, `SessionResolver`, `splitLivePath` (`proxy.go`, 83 lines). Only `proxy_test.go` references them. `daemon_auth.go`'s `SetDaemonAuthorization` has one remaining production caller (`prober.go:46`).
- `SendMaxRequestBytes` (`types.go:8`): only `appwire_validation_test.go:46-50` reads it.
- `RemoteThreadCache.Store`, `StoreSnapshot`, `StoreSnapshotData`, `inferRemoteSources` (`remotecache.go:70-93,429-443`): doc admits "Production's refresh path uses StoreWalkSnapshot" (`remotecache.go:74`); only tests call the other three.
- `Tree.FavoriteCandidates` (`tree.go:141`): 0 production callers, 5 test uses. `TopLevelSessionIDs` (`tree.go:672`): 0 production callers, 3 test uses.
- `SessionSeenStore.MarkSeen`/`MarkUnread` (`session_seen.go:203,209`): production uses `MarkBatch` only.
- `DeriveAttentionFromRoots` (`attention.go:87`): exported with one internal caller and 0 tests; its doc promises callers holding a `RootIndex` "pay nothing", but no such caller exists.
- `NewPastIndex` (`past.go:149`): only wrapped by `NewPastIndexWithDB`; tests construct it directly.
- `PastIndex.SetOnChange` and `contentFingerprint` (`past.go:202,223`): production registers only `SetOnRootChange` (`main.go:1418`), but every publish still hashes all entries for `contentFingerprint` (`past.go:550`).
- Test seams compiled into production types: `SetSearchProbeForTest`, `SeedForTest` (`past.go:216,1267`), `SetRecoveryStoreFaultsForTest` (`resumelocks.go:131`), `NewRosterWithEntries` (`roster.go:443`, 158 test uses), `SetProcessAlive`/`SetProcessIdentity`, `afterFindProbe`/`afterFindCacheMiss`/`searchProbe` fields (`past.go:121-135`), `indexMigrationInterleave` (`index_schema.go:23`), `pinSectionBeforeSectionInsertHook` and friends (`pin_section.go:29-31`).

Impact: dead code that tests keep alive; every test-only seam is behavior future maintainers must preserve and reason about.

Fix plan: delete in three PRs, in this order (each is independently safe):
1. `proxy.go`, `SendMaxRequestBytes` (and the test asserting it), `Store/StoreSnapshot/StoreSnapshotData/inferRemoteSources` (migrate the tests that call them to `StoreWalkSnapshot`), `FavoriteCandidates`, `MarkSeen/MarkUnread`, unexport `DeriveAttentionFromRoots` (or fold it into `DeriveAttention`).
2. Remove `PastIndex.onChange`/`SetOnChange`/`contentFingerprint`/`fingerprint` and the `bool` return of `publishAndSignal`/`Rebuild` if no production caller reads it (check `main.go` first). This saves one O(n) hash on every publish.
3. Move test constructors and seams into `export_test.go` files where the test is in-package, and into `hubtest` where cross-package. Keep only seams that production needs.
Docs: `docs/product/subsystems.md` S06 row does not mention these; no change.

Size: -400 source lines, -150 to -300 test lines migrated.
Risk: low.

---

## hub-hubcore-8 (medium): `PastIndex` is a 1,730-line god object (index + FTS mirror + search + recents + probe/generation fences)

Locations: `past.go` 35-148 (struct with 24 fields and 70 lines of generation-fence commentary), FTS mirror `:760-1130`, search `:618-810`, recents `:1303-1400`, probe/fold `:1433-1730`; shared search tokenizer `SearchTokens`/`IsIndexWordRune`/`ftsQuery` at `:1133-1158` is used by `message_search.go:625-660`.

Evidence: concerns that change for different reasons share one lock domain and one 24-field struct (`mu`, `ftsMu`, `idGen`, `probePins`, `rootIndexGen`, `published`...). `RecentModels` (`past.go:1303`) and `RecentProjectDirs` (`:1354`, a 30-line comment, calls `os.Stat`) are read models for the model picker/spawn form, not index operations. `matches()` (`:1160-1176`) lowercases four fields per entry per query. `MessageSearch` (a second FTS5 implementation, own file `search.db`) depends on tokenizer helpers that live in `past.go`.

Impact: any index change risks the fences; reviewing past_test.go (2,641 lines) is expensive.

Fix plan (file moves first, behavior unchanged):
1. Move `SearchTokens`, `IsIndexWordRune`, `ftsQuery`, `wordRuneCount`, `hasMessageSearchWord` to `search_tokens.go`.
2. Move the FTS mirror (`publishFTS`, `rewriteFTSDelta`, `writeFTSTx`, `rebuildFTS`, `searchFTS`, `ftsMatchIDs`, `ftsRowEqual`, `chmodSQLiteIndexFiles`) to `past_fts.go` as methods on an `ftsMirror` struct owned by `PastIndex` (own `ftsMu`, `published`, `ftsOwner/ftsSeq/lastSeq`).
3. Move `RecentModels`/`RecentProjectDirs` to `past_recents.go`; later consider moving them to `package hub` as read models over `AllMetas()`.
4. Precompute lowercase fields on `PastEntry` fold for `matches`.
Docs: none.

Size: about 0 net (moves), -60 on cleanup; 4 PRs.
Risk: low for moves; medium for the `ftsMirror` ownership change.

---

## hub-hubcore-9 (medium): two `Ref` types and a literal `"local"` used in both, with different grammars

Locations:
- `hubapi.ParseRef`/`hubapi.Ref{HostID, SessionID}` (`hubapi/refs.go:13-52`) used at `favorite_authority.go:129,139,232`, `pin_section.go:70,79`, `tree.go:864`.
- `appwire.ParseRef`/`appwire.Ref{SourceID, ThreadID}` (`appwire/refs.go:22-48`) used at `deletion_store.go:223,243,252`, `archive.go:52`, `remotecache.go:363`, `tree.go:848,1876`, `roster.go:1341,1354,1388,1391`.
- `"local"` string literals: 15 in this package (`deletion_store.go:224,252`, `archive.go:37`, `roster.go:1342,1354,1362,1388,1391`, `favorite_authority.go:140,233`, ...).

Evidence: the grammars differ. `hubapi.refPattern` is `^[A-Za-z0-9._~:-]+$` and splits at the first colon, so `host:a:b` parses as host `host`, session `a:b`. `appwire.refPartPattern` is `^[A-Za-z0-9._~-]+$` (no colon), so the same string fails to parse. Within hubcore, `SessionPinIdentity("host:a:b")` addresses a pin key while `NormalizeDecisionSessionID("host:a:b")` (same decision-key family, `archive.go:51-60`) treats it as a bare id. Field names also disagree (`HostID/SessionID` vs `SourceID/ThreadID`).

Impact: identity normalization is the foundation of the decision stores; two parsers in one package is a standing source of key mismatches (a pin and an archive decision for the same session landing under different keys).

Fix plan:
1. Add one `sessionKeyFromIdentity(identity string) ArchiveKey` in hubcore used by `SessionPinIdentity`, `NormalizeDecisionSessionID`, `sessionSourceFromID`, `classifyFavoriteSession` and `deletion_store.normalizeDeletionLookup`, built on `appwire.ParseRef` only (the shared grammar).
2. Add `appwire.LocalSourceID = "local"` (if missing) and replace the literals in this package.
3. Pin the `a:b:c` case with a test first (decide with Jesse which grammar wins; the product-visible ref grammar is appwire's).
4. Remove the `hubapi.Ref` imports from hubcore. Whether to delete `hubapi.Ref` entirely is a separate cross-slice question (18 non-test uses elsewhere).
Docs: none unless the ref grammar changes.

Size: -60 lines, 8 files touched.
Risk: medium (identity semantics). Needs a red test for the divergent input.

---

## hub-hubcore-10 (medium): `WebConfig` is an 84-field dependency bag that includes test hooks, nil-in-tests fallbacks, and 30 host-management knobs

Locations: `config.go:42-257`.

Evidence: 84 fields; 13 comments say "nil in tests"/"(tests)"; `RelayLifecycleHooks` has 11 function fields documented "nil in production" (`config.go:28-40,260`) and is read in `app_relay.go:481-486,782-803,998`; about 30 host-related fields (`RemoteHost*` 11, `HostTombstone*` 5, `HostSupersededReceipt*`, `HostPruned*`, `HostKeylessAudit*`, `HostRemnant*` 7, `HostProbeTimeout`, `HostMinFreeSpaceBytes`); `main.go:670` builds a throwaway `WebConfig{Past, Archive, Roster}` just to construct a scratch reconciler; 1,307 `WebConfig{` literals in tests. `PluginManager`'s 10-line comment documents a "fresh fallback" for tests.

Impact: every consumer receives every dependency; 137 files in `package hub` can reach anything; changing a field renames across 1,300 test literals.

Fix plan:
1. Group by owner into value structs: `HostRecordLimits` (18 retention/remnant knobs with documented defaults), `RemoteHostWiring` (the 11 `RemoteHost*` callbacks), `RelayTestHooks` (move to a `hub`-local test file via an interface the relay reads from `cfg`; see finding 7 on seams).
2. Make `newScratchReconciler` take `(past, archive, roster)`, not a config.
3. Do this opportunistically per consumer; do not mass-rewrite the 1,307 test literals in one PR. The new sub-structs keep literals valid only if embedded; use embedding for the first step and plan a test-helper (`hubtest.NewConfig`) for the second.
Docs: update `docs/product/subsystems.md` S06 only if responsibilities move (they should not).

Size: -100 lines config, +60 for sub-structs; touches 40+ files.
Risk: medium-high due to test-literal churn; sequence after findings 1-7.

---

## hub-hubcore-11 (medium): `resumelocks.go` combines four state machines in 1,009 lines under a name that describes one

Locations: `resumelocks.go`: `ResumeLocks` (23-65), `ResumeMutex` (216-250), `ActiveResume` (256-450), `RegisterResume`/`ResumeStop` (494-700), `SessionRecoveryState`/`RecoveryState*` (706-818), `ForceStopFence` (820-960), `ExplicitResumeCompleted` (966). Persistence: `PersistForceStop*` (67-130), `NewPersistentResumeLocks` (41-65).

Evidence: one struct holds `locks`, `active`, `recovery`, `sequence`, `fences` behind two mutexes (`persistenceMu`, `mu`); the file name says "locks" but 40% of it is durable recovery authority and force-stop admission fences. `NewPersistentResumeLocks` re-derives `SessionRecoveryState` values with a 220-character struct literal (`:56`). `RegisterResume` is 62 lines.

Impact: readers must hold all four machines in mind to change one; the 537-line `recovery_store_test.go` plus `resumelocks_{covtest,recovery}_test.go` are the only map of the invariants.

Fix plan: no behavior change; split by file and type while keeping one `ResumeLocks` façade for callers: `resume_mutex.go`, `active_resume.go`, `recovery_state.go` (state + fences + persistence), leaving `resumelocks.go` as constructor and façade. Replace the long literal with a `(authority).toState(group)` method. Rename after the split if the façade still reads wrong (Jesse's call on naming).
Docs: `docs/product/subsystems.md` S08 recovery-fence text stays correct.

Size: about 0 net; 4 files.
Risk: low (mechanical), but do it with the lock ordering preserved.

---

## hub-hubcore-12 (medium): `Roster.refresh` is a 211-line decision tree and `ReadSpawnedThread` derives subagent state a second way

Locations: `roster.go:770-980` (`refresh`), `roster.go:1557-1599` (`ReadSpawnedThread`), `prober.go:80-135` (`Probe` child-state derivation), `prober.go:277-291` (`idleStableDelegateChildIDs`).

Evidence: `refresh` interleaves probing, previous-snapshot retention, unconfirmed-claim tracking, crash classification with file garbage collection (`:880-935`), generation merging, and publication; `ReadSpawnedThread` maps `delegate.Lifecycle` `"idle"/"running"/needs-attention` to `appwire.ThreadStatus*` (`roster.go:1578-1596`) while `Probe` derives the same `RunningSubagentStates` from `ThreadList` statuses plus `idleStableDelegateChildIDs`. Two derivations of one field can drift (for example `Lifecycle == "closed"` is filtered in one, `ThreadStatusClosed` in the other).

Impact: the highest-risk code in the package (it decides crash vs alive vs unconfirmed) has no unit seam beyond full refresh runs, hence `roster_test.go` at 2,434 lines.

Fix plan:
1. Extract `classifyMissedProbe(entry, prev, previousUnconfirmed, identity) probeOutcome` (kept / unconfirmed / crashed / dropped) as a pure function and test its table directly; `refresh` shrinks to scan, probe, apply outcomes, publish.
2. Extract `crashedEntry(prev, entry)` and `gcStaleRendezvousFile`.
3. Define one `subagentStatesFromDelegates(diagnostics)` and use it from both `Probe` (as the idle override) and `ReadSpawnedThread`; pin both with a shared test.
4. Delete `roster_other.go`'s stub if the hub never ships on other platforms: its own comment says "evener-hub never ships here" and "as it always has" is history narration; confirm with the release matrix first.
Docs: S08 row unchanged.

Size: about -80 net, many moves.
Risk: medium; crash and unconfirmed semantics are subtle, so characterize with tests before moving.

---

## hub-hubcore-13 (low-medium): the crash-skip rule for running subagents is implemented four times

Locations: `root_index.go:54-69` (`RunningSubagentIDs`), `root_index.go:76-90` (`RunningSubagentParents`), `tree.go:950-970` (inline fused loop, whose comment says "the same rule Roster.SubagentState applies"), `roster.go:1250-1290` (`SubagentState`, `SubagentOwner`).

Evidence: each repeats `if le.Crashed { continue }` followed by iterating `RunningSubagentIDs` with an empty-id check. `tree.go:945-970` recomputes `runningSubagentIDs`, `reportedBy`, `runningSubagentStates` in one pass instead of calling the exported helpers.

Fix plan: add `type runningChildren struct{ ids map[string]bool; parent map[string]string; state map[string]string }` and `indexRunningChildren(live)`; make the two exported helpers and the tree loop use it, and have `Roster.SubagentState` use the same predicate (`LiveEntry.runsChildren() bool { return !Crashed }`). Add a test that a crashed entry's children are never indexed through any of the four entry points.
Size: -40 lines. Risk: low.

---

## hub-hubcore-14 (low-medium): `Tree.favoriteLive` is a second copy of `Tree.Live`, and `TierRows` and navigation pin-candidate code contain fixture-only fallbacks

Locations: `tree.go:1745-1752` (`favoriteLive := liveNodes`, comment "one shared result, not a second copy"), `tree.go:32-37,68` (separately deep-cloned in `SnapshotContext`), `tree.go:332-354` (`TierRows` falls back from `allCurrent` to `Current` when nil), `navigation_projection.go:648-690` (`navigationPinCandidatesContext` calls `tree.PinCandidates()`, then re-walks every tier through `TierRows` appending the same rows again, citing "snapshot fixtures"), `navigation_projection.go:632-646` (`cloneNavigationNodesContext`, a lighter duplicate of `cloneTreeNodesContext`, `tree.go:113-139`).

Evidence: `favoriteLive` is the same slice as `Live`; no production code mutates `Live` after build (`grep` for `.Live =` shows only struct literals). `SnapshotContext` deep-copies the live tree twice. `PinCandidates` and `FavoriteCandidates` (`tree.go:141-208`) are two near-identical dedupe-and-filter walks (differing on `Kind` filter and which tiers).

Fix plan:
1. Delete `favoriteLive`; `PinCandidates` reads `t.Live`. Update the 6 tests that set the private field (`tree_gaps_test.go:20-54,251`, `tree_covtest_test.go:330`) to set `Live`.
2. Remove the `allX == nil` fallback in `TierRows` by building the trees through `BuildTree*` in the fixtures, and delete the second walk in `navigationPinCandidatesContext`. If a fixture needs tier rows, give the test a builder.
3. Share one `cloneTreeNodes` between hubcore and navigation (export once, delete `cloneNavigationNodesContext`).
4. Fold `FavoriteCandidates` into the deletion in finding 7.
Size: -90 lines. Risk: low-medium (snapshot immutability guarantees; keep the snapshot tests).

---

## hub-hubcore-15 (low-medium): `ProviderRegistry` and credential-revision code is provider-domain logic parked in `hubcore`, with a 5-map staleness-fence protocol

Locations: `registry.go:1-727`. Maps and clocks: `generation`, `fetchClock`, `lastApplied`, `lastGoodLive`, `incarnations`, `goodNames`; `LiveToken` protocol (`BeginLiveFetchReg`, `ReapplyLive`); credential and ADC fingerprints (`instanceIdentity`, `oauthAccountFingerprint`, `adcFingerprint`, `CredentialConfigRevision*`, `DestinationIdentity`); only consumers are `app_auth.go` and `app_instances.go`. Comment density is 43%.

Evidence: imports `auth/openai`, `internal/credentials`, `llm/registry`, nothing hub-specific. `Reload` is 87 lines handling failed-load fallback, incarnations, identity carry-over and last-good re-application.

Impact: complexity concentration that only the live model-list cache needs; the heavy protocol (incarnation counters, identity strings, token orders) is justified by comments but is hard to verify (1,110 test lines).

Fix plan: do not rewrite (provider registry redesign is Jesse's recent decision, see memory). Propose, for discussion: (a) move to a `hubproviders` package or into `llm/registry` consumers so `hubcore` stops importing credentials and OpenAI auth; (b) ask whether "live listing cache keyed by endpoint identity + incarnation" can be reduced to "drop all live rows on any Reload and refetch" (the model-list endpoint already caches with TTL, `registry.go:118-127`). Item (b) deletes `incarnations`, `goodNames`, `lastGoodLive`, `carryLive`, `pruneLastGoodLive` (about 200 lines) at the cost of one refetch per instance write.
Size: (a) 0 net; (b) -250.
Risk: (a) low; (b) behavior change needing Jesse's approval.

---

## hub-hubcore-16 (low): terminal job-status vocabulary and time helpers are re-declared locally

- Terminal job statuses: `prober.go:329-337` (`terminalJobStatus`), `app_threadread.go:1698-1706` (`isTerminalHistoricalJobStatus`), `agent/internal/jobstore/record.go:50-57` (`Status.IsTerminal`), `cmd/evener-doctor/reconstruct.go:584`. The same seven strings appear in three places; `jobstore` is `agent/internal` and unimportable by the hub.
- Time conversion: `session_order.go:139-170` defines `UnixTime`, `UnixSeconds`, `UnixMilliTime`, `UnixMilliseconds` (misfiled in the ordering file, 9 external uses), while `pin_section.go:234,235,638,675,676,689` repeat `time.Unix(x, 0).UTC()` inline.

Fix plan: add `appwire.IsTerminalJobStatus(string) bool` (the wire already carries these statuses) and use it in both hub sites; have `jobstore` assert equality in a test. Move the four time helpers to `timeconv.go` and use `UnixTime` in `pin_section.go`.
Size: -25 lines. Risk: low.

---

## hub-hubcore-17 (low): string-typed state, tier and kind vocabularies, with drift already visible

Locations: `tree.go:576-607` (`NormalizeState` returns "idle", "awaiting", "active", "errored", "warning", "ended", "notLoaded", and the restart-required constant), `tree.go:377-394` (`classifySession` returns "current"/"recent"/"archived"), `tree.go:609-620` (`nodeKind` returns "session"/"fork"/"subagent"), `attention.go:10-23` (levels "working", "needs_you", "error", "idle"), `tree.go:1840` (`projectArchivedDecision` source strings).

Evidence: `NormalizeState` has two arms returning identical values (`ThreadStatusNotLoaded` and `default` both "notLoaded"; `"errored"` and `"ended"` explicit pass-throughs). The `TreeNode` doc comment says `Kind` is "session | fork" while `nodeKind` can return "subagent" (`tree.go:396-401` vs `:609`). `agentic-testing.md:441` hard-documents the string set. The shared-client rule (AGENTS.md: shared client owns shared derivations) suggests these belong with `hubapi`.

Fix plan: introduce `type TreeState string`, `type SessionTier string`, `type NodeKind string` with constants in `hubapi` where the wire already uses them (`hubapi.AttentionState`, `NeedsYouBand`), collapse the duplicate `NormalizeState` arms, correct the `Kind` doc. Mechanical; no wire change.
Size: +40/-20. Risk: low.

---

## hub-hubcore-18 (low): `favorite_authority.go` small dead branches and defensive copies

- `indexFavoriteProjects` claim-key loop (`favorite_authority.go:196-211`): both branches add `authority.ClaimKey` to the set; the `if ClaimKey == "" { add; break }` branch adds the same value then exits, so the loop is "ambiguous when any key is empty or two keys differ". Replace by a 6-line loop and a test for the three cases.
- `LocalSessionDecisionAliases` copies `append([]string(nil), authorities[0].Aliases...)` before reading only (`favorite_authority.go:138`).
- `appendUniqueString` (`:322-327`) re-implements the standard `slices.Contains` guard; fine, but it is also what `slices.ContainsFunc`/a set would replace at its two call sites.

Size: -15 lines. Risk: low (add the three-case test first).

---

## hub-hubcore-19 (medium): coverage-chasing test files assert on driver text and source line numbers

Locations: `tree_covtest_test.go` (807 lines, 43 comments citing `tree.go:NNN`), `pin_section_gaps2_test.go` (18 line refs), `pin_section_nonretry_test.go` (10), `deletion_store_gaps2_test.go` (6), `resumelocks_covtest_test.go` (4), `pin_section_coverage_test.go`, `coverage_edges_test.go`, `external_edges_test.go`, `past_coverage_test.go`; 78 line-number comments in total. Error-text assertions: `archive_test.go:241` (`"unable to open database file"`), `pin_section_coverage_test.go:80` (`"driver not registered"`), `:361` (`"entropy exhausted"`), `tree_covtest_test.go:119,135` (`resolve project "..."`), 12 in `pin_section_nonretry_test.go` (`"locked"`).

Evidence: file names (`covtest`, `gaps2`, `coverage`, `edges`) and bodies like "covers the QueryRowContext(PRAGMA foreign_keys).Scan error path (lines 131-134)" show tests written to hit a branch, not to pin behavior. Several assert on driver or error message text, which `docs/developing-evener/testing.md` ("Copy Is Not a Test Oracle") forbids.

Impact: 26,165 test lines (1.95x source) are costly to maintain and resist refactors in findings 1-12, because they pin implementation detail.

Fix plan: do this alongside each source finding rather than as a separate sweep. As each area is refactored, rewrite its coverage tests as behavior tables (`errors.Is`/`errors.As`/typed outcome assertions), delete line-number comments, and fold `*_gaps2`, `*_coverage` and `*_covtest` into the behavior file of the same subject.
Size: roughly -3,000 test lines over the series.
Risk: low; never reduce behavior coverage, only duplicated branch coverage.

---

## hub-hubcore-20 (low): history-narrating and issue-ID comments

Evidence: 53 issue or spec tags in non-test source (`tree.go` 18, `roster.go` 24, `past.go` 6, `prober.go` 3): `(#4199)`, `(S1b)`, `(S17)`, `kata zm6s`, `round-4 A4/B7`, `spec v5`, `#2506`. Examples: `tree.go:985-991`, `roster.go:866` ("kata zm6s"), `tree.go:1585`. History phrasing: `roster_other.go:16` "as it always has", `config.go:100` "fall back to the old synchronous walk", `remotecache.go:74` "compatibility source inference", `tree.go:1619` "the way the original loop did", `BuildProjectTreeAt`'s "original search loop provided". Several multi-paragraph comments restate what the code does (the 20-line `liveFieldsFor` comment, the 14-line `modelFor` comment).

Impact: 24% comment density; the true "why" comments (race fences) are buried among narration. Jesse's rule: no history in comments, it belongs in the commit message.

Fix plan: strip issue-number tags and past-tense narration when touching each function (rides on findings 1, 2, 6, 12); keep the invariant statements. No separate sweep.
Size: -600 comment lines over time. Risk: none.

---

## hub-hubcore-21 (low): `hubcore` is a catch-all package

Evidence: 34 files and at least eight unrelated concerns (see inventory). `hostplan.go` (deploy-plan fact digest, 74 lines, consumed only by `app_host_*`), `proxy.go` (dead, finding 7), `types.go` (image limits used by `appwire_validation.go`/`image_serve.go`), `registry.go` (finding 15), `message_search.go` (transcript search, imports `agent/transcript`, `internal/transcriptindex`).

Fix plan: no package-wide split now. After findings 7 and 15, move `hostplan.go` next to `app_host_ops.go` (`package hub`) and `types.go` limits into `appwire` (where the wire cap belongs), leaving `hubcore` as navigation, roster, index, stores and resume state.
Size: -90 in hubcore. Risk: low.

---

## Recommended fix sequence

1. Delete dead and test-only surface (finding 7 step 1), plus `deletionSyncUnsupported` wrapper (finding 5 step 4). Smallest, safest, shrinks everything after.
2. Fix the latent bug: `TranscriptDisplayPostApplyError` lacks `ErrWriteApplied` (finding 5 step 2), red test first. Also the ref-grammar divergence test (finding 9 step 3) to decide semantics with Jesse.
3. `PinSectionStore.writeTx` extraction and typed busy detection (finding 3), rewriting that file's tests as tables (finding 19).
4. Shared SQLite store skeleton for Archive/Favorite/SessionSeen/Pin and remove `SetFs` (finding 4).
5. Introduce `ThreadFacts` embedded struct for `ProbeResult`/`LiveEntry` (finding 2), then the reflection-guarded `rosterFingerprint` (finding 6).
6. Break up `buildTreeAtWithProjects` in three phases (finding 1), including `favoriteLive` and pin-candidate cleanup (finding 14) and the running-children index (finding 13).
7. Extract `Roster.refresh` outcomes and shared subagent-state derivation (finding 12).
8. File-level splits with no behavior change: `PastIndex` (finding 8) and `resumelocks.go` (finding 11).
9. JSON store helper dedupe (finding 5 steps 1, 3) and optional strict-decode rewrite (needs Jesse).
10. Unify identity parsing and `"local"` (finding 9), terminal-status and time helpers (finding 16), typed state vocab (finding 17), favorite-authority tidy (finding 18).
11. `WebConfig` decomposition (finding 10) and package-placement moves (finding 21), after the consumers have settled.
12. Discuss with Jesse before any work: `ProviderRegistry` live-cache simplification (finding 15b) and removing `roster_other.go`.

Throughout: strip narration comments (finding 20) in each touched function and rewrite coverage-chasing tests to behavior assertions (finding 19). Run the targeted hubcore tests per PR and let CI run the full suite, per AGENTS.md.
