# Wave 3 Task 6 report — `serf/tree/changed` hub broadcast + tree-wire gaps

Branch `w3-treepush`, off `0996d8afb`. 12 commits (4th/6th/9th/11th are post-review fix rounds —
see "Review-fix round", "Invariant round", "Tree-wire gaps round" below; the odd ones out are
report-doc commits). Full mandated suite green (`go test ./cmd/serf-hub/... ./appwire/...
./internal/appwirets/... ./internal/appwiredoc/...`, exit 0), `make generate` idempotent (zero
further diff), `gofmt -l`/
`go vet` clean on every touched file, `golangci-lint run --max-issues-per-linter 0
--max-same-issues 0` on the touched packages clean (0 issues), `serf-namingcheck`/
`serf-internalcheck`/`serf-docscheck` all clean. `go build -tags serffuzz ./cmd/serf-hub/...`,
`go vet -tags serffuzz ./cmd/serf-hub/...`, and `go test -tags serffuzz ./cmd/serf-hub/...` (seed
corpus) all clean throughout (belt and suspenders — that tag isn't part of the mandated
verification, but the PastIndex signature change in the Invariant round touched
main.go/main_background.go adjacency, making it worth the extra check on every round since). Zero
touches to hand-written frontend src, templates, or assets (`git diff --stat 0996d8afb..HEAD --
cmd/serf-hub/templates/ cmd/serf-hub/assets/ cmd/serf-hub/frontend/src/` shows only the
regenerated `types.gen.ts`).

## Commits

| Commit | Unit |
|---|---|
| `6238fbcf9` | appwire: catalog `serf/tree/changed` notification |
| `1f996da5b` | hub: broadcast on roster and past-index deltas |
| `91b8c8900` | hub: broadcast after archive/favorite/rename/project-delete |
| `25b5bbdfe` | hub: single tree-changed broadcast per mutation — PastIndex routing already notifies (review-fix round) |
| `754c87daa` | hub: successful mutations always broadcast tree-changed exactly once (invariant round) |
| `b8577f66b` | hub: stamp Tier/Favorite/Rename on live tree rows (tree-wire gaps round) |
| `41923b6ab` | hub: expose project favorite state on the tree wire (tree-wire gaps round) |
| `d08bd8a42` | hub: document the PastIndex seed-before-wire ordering hazard (tree-wire gaps round) |
| `d259cddd2` | hub: orphan-live rows carry tier/favorite/rename too (tree-wire gaps round, follow-up) |

## Design

**Catalog.** `appwire/types.go` adds `NotifySerfTreeChanged = "serf/tree/changed"` (types.go:119)
at the end of the `Notify*` block; `appwire/protocol.go:186` catalogs it with `Payload: nil`,
mirroring `serf/marketplace/updated`/`serf/plugin/updated` exactly (inline-empty, no dedicated Go
type). `make generate` produced the expected `SerfTreeChangedPayload` (empty interface) in
`types.gen.ts` and the doc row in `docs/appwire-protocol.md`. One pre-existing test needed a
routine, self-documented update: `appwire/wiretypes_fuzz_test.go`'s
`TestWireTypeRegistryCoverage` asserts an exact nil-payload notification count, and its own
comment says "adding a notification should update ... this acceptance check" — bumped 14→15.
This is a live appwire-package registry guard, not a legacy-UI assertion, so I updated it rather
than treating it as a "legacy exhaustive enumeration" to flag-and-leave; flagging it here per the
standing instruction anyway, in case that read is wrong.

**Broadcast helper.** `notifyTreeChanged(server *appserver.Server)` (web_api_tree.go:34-43)
wraps `server.BroadcastAll(appwire.NotifySerfTreeChanged, map[string]string{})` — same shape as
`notifyMarketplaceUpdated`/`notifyPluginUpdated` in app_rpc.go. It's the single call site every
trigger below goes through.

**Delta detection (triggers 1 & 2) — reused existing machinery, wrote no new diff logic.**
`hubcore.Roster` and `hubcore.PastIndex` already expose a `SetOnChange(func())` hook that fires
*only* on an actual content-fingerprint delta: Roster's covers membership + per-session status +
running-child-set (exactly "a daemon appeared/disappeared/changed liveness"); PastIndex's covers
the sorted (id, name, updatedAt) set, fired by both `Rebuild` and `UpdateMeta` ("a session
appeared/ended/changed in the past index"). This is the same mechanism the codebase already uses
to gate the `/api/tree` memo-bust (`bump`, wired via the identical hook, Task 10) and is a closer
match to "emit only on actual change" than re-deriving a diff the way `AttentionWatcher.Tick`
does over a *different* derived quantity (attention levels, not raw roster/past-index content) —
so I composed the broadcast into the same hook rather than duplicating gating logic.
`main.go:319-323` moves `past.SetOnChange`/`roster.SetOnChange` from their old pre-`web`
registration point to just after `web := NewWebServer(...)` (so the closure can reach
`web.appRPC`), composing `bump()` with `notifyTreeChanged(web.appRPC)`. `archive.SetOnChange`/
`favorite.SetOnChange` stay exactly where they were — those two mutations broadcast explicitly
at their handlers instead (see below), since ArchiveStore/FavoriteStore aren't reflected in
PastIndex content at all, so there's no shared hook to reuse for them.

**Trigger 3, as originally shipped** — explicit per-handler calls at all four mutations, mirroring
the existing `if s.cfg.PokeAttention != nil { s.cfg.PokeAttention() }` pattern already at each
call site. **This was wrong for rename/project-delete — see "Review-fix round" below**; the table
below reflects the corrected, current state.

## Trigger-site inventory (file:line, current after the Invariant round)

| Trigger | Site | Mechanism |
|---|---|---|
| 1. Roster delta | `main.go:323` (`roster.SetOnChange`) | composed hook, fires from `Roster.Refresh` only on fingerprint delta |
| 2. Past-index change | `main.go:322` (`past.SetOnChange`) | composed hook, fires from `PastIndex.Rebuild`/`UpdateMeta` only on fingerprint delta |
| 3. Archive mutation | `web_api_archive.go:70` (`s.notifyMutation()`) | explicit, unconditional, after `Archive.Set` succeeds — ArchiveStore never routes through PastIndex |
| 3. Favorite mutation | `web_api_favorite.go:41` (`s.notifyMutation()`) | explicit, unconditional, after `Favorite.Set` succeeds — FavoriteStore never routes through PastIndex |
| 3. Rename (ended-session path) | `web_api_rename.go:107` (`Past.UpdateMeta`'s return) + `:115` (compensating `notifyTreeChanged`, conditional on `!notified`) | via trigger 2's composed hook in the common case; explicit compensating call when it didn't fire |
| 3. Rename (live-daemon path, all three reachable states inside `refreshRenamedMeta`) | `web_api_rename.go:134`/`:139` (`Past.UpdateMeta` calls) + `:146` (compensating `notifyTreeChanged`, conditional on `!notified`) | same pattern; the compensating call is the sole source when `Find` misses (session not yet indexed) |
| 3. Project-delete | `web_api_project_delete.go:189` (`Past.Rebuild`'s return) + `:202` (compensating `notifyTreeChanged`, conditional on `!rebuilt`) | via trigger 2's composed hook when a session was actually removed; explicit compensating call when every session was skipped (only archive/favorite rows changed) |

Helpers: `web_api_tree.go:55` (`func notifyTreeChanged`, doc comment states the exactly-once
invariant), `web_api_tree.go:68` (`func (s *WebServer) notifyMutation`, unconditional — archive/
favorite only).

## TDD evidence

Every production line was preceded by a failing test, watched red for the right reason
(`undefined: notifyTreeChanged` compile error for the first pair; `timed out waiting for a
notification` for each handler), then made minimally green:

- `TestRosterOnChangeNotifiesTreeChangedOnDeltaOnly` / `TestPastIndexOnChangeNotifiesTreeChangedOnDeltaOnly`
  (web_api_tree_test.go) — RED on `undefined: notifyTreeChanged`; GREEN after adding the helper.
  Each wires `roster.SetOnChange`/`past.SetOnChange` directly against a real `appserver.Server` +
  a real dialed `appwire.Client` (no mocks), asserts the seeding refresh/rebuild broadcasts, then
  asserts a second, no-op refresh/rebuild does **not** — proven via an ordering sentinel
  (`server.BroadcastAll("test/sentinel", nil)` sent right after the no-op call; asserting the
  *next* received notification is the sentinel, not `serf/tree/changed`, rules out a wrongly-sent
  broadcast without a race-prone sleep-based negative check).
- `TestArchiveEndpointBroadcastsTreeChanged`, `TestFavoriteEndpointBroadcastsTreeChanged`,
  `TestRenameEndedSessionBroadcastsTreeChanged`, `TestRefreshRenamedMetaBroadcastsTreeChanged`,
  `TestProjectDeleteBroadcastsTreeChanged` (original names — all renamed `...ExactlyOnce` in the
  review-fix round, see below) — each RED with "timed out waiting for a notification" before its
  one-line handler change, GREEN after. All POST to the real HTTP endpoint (or, for the
  live-daemon rename path, call `refreshRenamedMeta` directly — see below) through
  `newHubRPCTestServer`/`dialHubRPC`, a real hub + real dialed WS client, asserting the
  notification actually reaches a subscribed connection (per the task's explicit ask), not just
  that some internal call happened.
- `refreshRenamedMeta`'s live-daemon success path has no existing fixture for a *successful*
  `SetThreadName` — `scriptedAppSource.SetThreadName` (web_test.go) only ever returns
  `Unavailable`. Rather than extend a widely-shared test fixture for one call site, the test
  calls `web.refreshRenamedMeta(id, name)` directly against a `WebServer` built with
  `NewWebServer` (not the `newHubRPCTestServer` wrapper, since that returns only the
  `*httptest.Server`, not the `*WebServer` the direct call needs) with its `appRPC` re-wrapped in
  its own `httptest.Server` for the dial.

All 4 mutation handlers' full existing test files were re-run after each change (archive: 9
tests, favorite: 3, rename: 4, project-delete: 12) — zero regressions.

## Review-fix round (commit `25b5bbdfe`)

A coordinator-relayed review caught an Important, empirically-measured bug: rename (both paths)
and project-delete broadcast **twice** per request. Root cause, which I verified myself by
re-reading the code before touching anything: `Past.UpdateMeta`/`Past.Rebuild` (called inside
those three handlers) already trigger PastIndex's composed `onChange` hook from trigger 2
(`main.go:322`, wired to `notifyTreeChanged`) whenever they detect a delta — which a genuine
rename or deletion always produces — so the explicit calls I'd also placed in those handlers
fired a second, redundant broadcast. The `main.go`/`notifyTreeChanged` comments I'd written
claimed "their stores don't all route back through Roster or PastIndex" for all four mutations;
that's true for archive/favorite (ArchiveStore/FavoriteStore, a different store) but false for
rename/project-delete, which edit PastIndex directly. Archive/favorite were correctly untouched —
their stores genuinely never route through PastIndex, so they still need the explicit call.

**Fix:** dropped the three now-redundant `notifyTreeChanged` calls (rename's two internal sites
plus project-delete's), corrected both comments, and folded archive/favorite's duplicated
`PokeAttention`+`notifyTreeChanged` pair into a new `(*WebServer).notifyMutation()` helper
(`web_api_tree.go:52`).

**Test fix, and why the original suite missed this class of bug:** the three affected tests built
a bare `hubcore.NewPastIndex(...)`/`NewPastIndexWithDB(...)` and handed it straight to
`hubcore.WebConfig{Past: ...}` without ever calling `.SetOnChange(...)` on it — meaning the
composed hook from trigger 2 was never present in the test environment at all, only in
production (`runMain`). The tests could only ever observe the explicit call, structurally
incapable of noticing a *second* one from a hook that wasn't wired. Fixed by adding
`newHubRPCTestServerWithWeb` (app_rpc_test.go — a thin wrapper around `newHubRPCTestServer`'s
existing unstarted-server dance that also returns the `*WebServer`, so a test can reach
`web.appRPC`) and wiring `past.SetOnChange(func() { notifyTreeChanged(web.appRPC) })` in each of
the three tests before triggering the mutation, mirroring `runMain`'s actual wiring. Added
`assertSingleNotification`/`assertNoNotification` (web_api_tree_test.go, both using the existing
ordering-sentinel technique) and applied the exactly-once assertion to all five mutation-broadcast
tests (including archive/favorite, which were never buggy, for the same regression-proofing).

**Verified the fix and the tests genuinely catch the bug class**, not just that everything happens
to pass: for each of the three sites, I temporarily re-added the just-removed explicit call, ran
the specific test, confirmed it failed with `got a second notification "serf/tree/changed" before
the sentinel; want exactly one "serf/tree/changed"`, then reverted (`git diff --stat` back to the
intended 4-insertion/5-deletion shape confirmed no leftover debug edit). Also caught a second,
independent bug while doing this: `TestRefreshRenamedMetaBroadcastsTreeChanged`'s original body
never wrote a changed name to disk before calling `refreshRenamedMeta` — it only worked before
because the unconditional explicit call didn't care whether `UpdateMeta` found a real delta.  With
that call gone, reloading identical on-disk content produces no delta and nothing broadcasts, so
the test now writes a simulated out-of-process daemon rewrite (what the real `SetThreadName`
handler does) before calling it, matching production's actual precondition.

Full suite (`go test ./cmd/serf-hub/... ./appwire/... ./internal/appwirets/...
./internal/appwiredoc/...`) exit 0 after the fix; `make generate` still idempotent; `golangci-lint
run --max-issues-per-linter 0 --max-same-issues 0` on the same four packages: 0 issues.

## Invariant round (commit `754c87daa`)

A second coordinator-relayed ruling: close the two residual gaps flagged above rather than accept
them, under an explicit invariant — a successful mutation broadcasts exactly once (never zero,
never two); a failed/no-op request broadcasts zero. Suggested shape: surface whether
`PastIndex.UpdateMeta`/`Rebuild`'s internal delta computation actually fired `onChange`, and
compensate with an explicit broadcast on the paths where it didn't.

**Signature change, mechanically propagated.** `Rebuild() error` → `Rebuild() (bool, error)`;
`UpdateMeta(id, meta)` → `UpdateMeta(id, meta) bool`. Both bools mean "onChange fired" (delta
found AND a callback is registered), not just "delta found" — a caller compensates exactly when
this is false. `Find`'s own internal self-healing `Rebuild()` call (on a cache miss) and
`RefreshOne`'s `UpdateMeta` call were updated too, though neither needed new compensating logic —
`Find`/`RefreshOne` aren't user-mutation success paths. Blast radius: 8 non-test production call
sites (fixed by hand: `main.go` x2, `app_threadlifecycle.go` x2, `app_transcripts.go`,
`web_session.go` x2, `web_api_project_delete.go`) plus roughly 130 test call sites across ~30
files, all one of two mechanical discard patterns (`_ = X.Rebuild()` → `_, _ = X.Rebuild()`;
`if err := X.Rebuild(); err != nil` → `if _, err := X.Rebuild(); err != nil`) — fixed with a
scripted `perl -pi -e` substitution over both patterns, then driven to zero remaining errors via
`go vet ./...` and `go vet -tags serffuzz ./...` (compiler-as-completeness-net, not grep).

**Compensating logic**, applied only where a path genuinely succeeded but PastIndex's hook didn't
fire — never on a genuine failure or no-op:
- `refreshRenamedMeta`: restructured from two exit points (an early `return` inside the found
  branch, plus a shared trailing block for the other two) into a single trailing check. Every one
  of its three reachable states — Find hit + reload succeeded, Find hit + reload failed
  (fallback edit), Find missed entirely — now funnels through one `notified` flag, so the
  compensating call covers all of them, not just the specifically-named "session not indexed"
  case. `refreshRenamedMeta` is only ever invoked after the daemon already confirmed the rename,
  so every one of these states is a genuine success.
- `handleAPIRename`'s ended-session path: same one-flag pattern, single call site (only one
  `UpdateMeta` call exists on this path).
- `handleAPIProjectDelete`: compensates when `Rebuild()` found no delta. Reaching that line always
  means the request succeeded (every error/conflict path returns earlier), so `!rebuilt` there
  unconditionally means "compensate" — no further no-op/error branching needed at that call site.

**Tests**, each verified against its broken condition (temporarily reverted/reintroduced, watched
fail for the stated reason, restored — same technique as the prior round):
- `TestRefreshRenamedMetaBroadcastsTreeChangedExactlyOnceWhenSessionNotIndexed`: a session never
  written to disk. Seeds `past.Rebuild()` once before wiring `SetOnChange` (mirrors `runMain`'s
  actual ordering — see the bug this caught, below) so `Find`'s internal self-heal `Rebuild` finds
  nothing further and stays silent, isolating the compensating call as the sole source. Broke by
  disabling the compensating branch (`if false && !notified`): failed with "timed out waiting for
  a notification" (zero instead of one).
- `TestProjectDeleteBroadcastsTreeChangedExactlyOnceWhenNothingRemoved`: reuses
  `TestProjectDeleteSkipsSessionThatBecomesLive`'s technique (override `projectSessionLive` to
  force the project's one session to "become live" mid-request, so it's skipped, not removed) and
  asserts `deleted` is empty before checking the broadcast. Same break/confirm/restore.
- `TestRenameNotFoundBroadcastsNothing`: a rename for a session in neither the roster nor the past
  index — a genuine 404. Verified in **both** directions: disabling the (nonexistent, correct)
  broadcast isn't applicable here since there's no compensating call on this path by design, so I
  instead injected an *errant* `notifyTreeChanged` call right before the 404 response and confirmed
  the test caught it ("got notification ... before the sentinel"), then reverted — proving
  `assertNoNotification` genuinely detects a wrongly-added broadcast, not just a coincidentally
  quiet run.

**A test bug the reintroduction technique caught, not a production one:** `TestRenameNotFoundBroadcastsNothing`'s
first draft didn't seed `past.Rebuild()` before wiring `SetOnChange`, so `Find`'s internal
self-healing `Rebuild()` (triggered by the 404 lookup itself) was the *first-ever* `Rebuild()` on
that `PastIndex` — and an unseeded index's first `Rebuild()` always looks like a delta, because
`contentFingerprint(nil)` (the empty-content hash) is a specific non-zero FNV constant, never
equal to the zero-value initial `i.fingerprint`. That produced a real, reproducible spurious
broadcast in the test, caught by the new `assertNoNotification` check on the very first run —
before I'd have needed to break anything intentionally. Not a production bug: `runMain` always
calls the seeding `Rebuild()` (main.go:162) *before* `SetOnChange` is ever wired (main.go:322-323
runs after `web` is constructed), so this ordering hazard can't occur outside a test that gets the
sequencing wrong. Fixed by seeding first, matching the other two new tests.

Full suite exit 0; `make generate` idempotent; `golangci-lint` 0 issues; `go build -tags serffuzz
./...`, `go vet -tags serffuzz ./...`, and `go test -tags serffuzz ./cmd/serf-hub/...` (seed
corpus) all clean — worth the extra check given the signature change's reach into
main.go/main_background.go adjacency.

## Tree-wire gaps round (commits `b8577f66b`, `41923b6ab`, `d08bd8a42`)

A third coordinator-relayed ruling, this time from the rail stream's review of `/api/tree`'s wire
shape: two gaps plus a documentation fold, unrelated to the broadcast plumbing above but touching
the same `web_api_tree.go`/`hubapi` surface. All TDD, same verification bar.

**Gap 1 — live rows never stamped (`b8577f66b`).** `handleAPITree`'s Live loop called
`apiTreeNode("live", "", n, true)` directly (web_api_tree.go, pre-fix line 116), bypassing
`apiTreeNodeTier` — the only path that stamps `Tier`/`Branch`/`ClusterCount`/`Favorite`/`Rename`.
Reviewer-verified consequence: a session both live and archived showed `tier=""` (undefined) on
the wire, so the sidebar's archive toggle had no signal to render "unarchive"; live rows also
never carried favorite state or the rename affordance regardless of the session's actual
decisions. Fix is the one-line swap the task specified: `s.apiTreeNodeTier("live", "", "live",
favs, n)`. Verified against `hubcore`'s `liveNodes` construction
(`internal/hubcore/tree.go:748-783`) that everything `apiTreeNodeTier` needs is present: `ID` is
always set (from `le.SessionID`), which is what the `Favorite`/`Rename` lookups key on;
`Branch`/`ClusterCount` stay zero-valued on live nodes (no fork-tree hierarchy computed for the
flat Live tier), which is fine — `omitempty` just omits them, identical to how the NeedsYou/Pinned
tiers already behave for nodes without that data.

Test: `TestWeb_APITreeLiveRowsCarryTierFavoriteRename` — a live, favorited, local session; asserts
`Tier=="live"`, `Favorite==true`, `Rename==true`. RED first (`Tier="", ... Favorite:false
Rename:false` in the failure output), GREEN after the one-line fix.

**No legacy-test conflict** (the task's explicit STOP condition never triggered): searched every
`/api/tree`-touching assertion in `web_test.go` (`grep -n "resp.Live\|got.Live\|api/tree"`) — all
are field-specific (`got.Live[0].Ref != ...`, `len(got.Live) != ...`), none do an exact-struct or
exact-JSON match the new additive fields could break. Ran the full suite (153 `TestWeb_*` cases)
plus the serffuzz-tagged suite after the fix: all green, zero edits to `web_test.go`.

**Gap 2 — project favorites unreadable (`41923b6ab`).** `POST /api/favorite` already accepted
`kind:"project"` (`handleAPIFavorite` validates `Kind != "session" && Kind != "project"`, no
`"project"`-specific rejection), but `hubapi.TreeProject` had no `Favorite` field to carry the
decision back out on `GET /api/tree`. Added `Favorite bool` `json:"favorite,omitempty"`
(additive; confirmed TUI-safe with a full repo `go build`/`go vet`, not just `cmd/serf-hub`) and
stamped it in `apiTreeProject` from the same per-request `favs` map `apiTreeNodeTier` already
uses: `favs[hubcore.ArchiveKey{Kind: "project", ID: p.Key}]` — `p.Key` is documented in
`hubcore.TreeProject` as "the canonical `identifier.Project.ID`", the exact value `POST
/api/favorite` validates `kind:"project"` IDs against, and the same key shape every existing
`ArchiveKey{Kind: "project", ...}` call site in the codebase already uses (checked all of them
before writing this).

Tests assert both halves at the **raw JSON** level via a small helper
(`projectRawFromResponse`), not just the unmarshaled Go struct — an `omitempty bool` can't
distinguish "false because absent" from "false because decoded" once it's back in a Go value:
`TestWeb_APITreeProjectFavoriteStampedOnWire` (favorited → raw `"favorite":true` present) and
`TestWeb_APITreeProjectFavoriteOmittedWhenUnfavorited` (unfavorited → key absent from the raw
object entirely). Both RED-first (compile error: `Favorite undefined` before the field existed),
GREEN after. `make generate` confirmed producing no diff — `hubapi` is a separate package from
the `appwire` catalog, unaffected by this change.

**Fold — ordering-hazard doc comment (`d08bd8a42`).** Added to `PastIndex.SetOnChange`'s doc
comment in `internal/hubcore/past.go`: an unseeded index's first-ever `Rebuild` always looks like
a delta (the empty-content fingerprint is a non-zero FNV constant, never equal to the zero-value
initial fingerprint), so calling `SetOnChange` before an initial `Rebuild` fires a spurious
"change" on nothing — the exact hazard a test caught in the Invariant round. Doc-only, no
behavior change; `go build`/`go vet` confirm.

**Follow-up — the same bypass survived at a second call site (`d259cddd2`).** Review caught that
Gap 1's fix was incomplete: `handleAPITree` projects a live session TWICE — once unconditionally
into the flat `resp.Live` array (fixed above), and again into `resp.Projects` via a separate
"orphan-live" fallback loop (web_api_tree.go, then-line 181) whenever the PastIndex walk hasn't
seen that session yet (no meta.json indexed — e.g. spawned moments ago). That second call site
still called `apiTreeNode` directly. Reviewer-probe-confirmed consequence: a live session
archived+favorited immediately after spawn (before its meta lands) got a correctly-stamped
`resp.Live` row but an unstamped `resp.Projects` row for the identical session. Fix: the same
one-line swap, `s.apiTreeNodeTier("project", key, "live", favs, node)` — controller ruling: an
orphan-live row IS a live session, so it reuses the "live" tier rather than inventing a second
vocabulary term. Confirmed `apiTreeNodeTier`'s own live-recomputation
(`treeNodeCanActLive(n) && s.isLive(n.ID)`) doesn't silently flip `Live` to `false` for these rows
in the process: `treeNodeCanActLive` only excludes `state == "ended"`, and every row here comes
from a live roster entry, so its state can't be `"ended"`.

`TestWeb_APITreeOrphanLiveRowsCarryTierFavoriteRename` mirrors
`TestWeb_APITreeLiveRowsCarryTierFavoriteRename`'s exact setup (a live, favorited, local session
with no PastIndex entry) but asserts against the `resp.Projects[...].Sessions[...]` row instead
of `resp.Live[0]` — RED first (`Tier="" ... Favorite:false Rename:false` in the failure output,
identical shape to Gap 1's), GREEN after. Full suite (including serffuzz) green; zero legacy-test
edits; `make generate` idempotent; `golangci-lint` clean.

**Deferred — reviewer Minors, no action taken (per controller ruling):**
- The narrow `isLive` TOCTOU on `resp.Live`/orphan-live rows (the roster could theoretically
  change between tree-build and per-node projection) — matches every other tier's existing
  behavior (NeedsYou, Pinned already have the identical race), not a regression this work
  introduces.
- `handleAPIFavorite` lacks the project-ID validation `handleAPIArchive` has (rejecting
  `"no-project"`, cross-checking `working_dir` against the resolved project) — pre-existing gap,
  unrelated to the tree-wire read side this round touched. Fix whenever favorite's write side is
  next touched.

## Files

- `appwire/types.go`, `appwire/protocol.go` — catalog entry.
- `appwire/wiretypes_fuzz_test.go` — nil-payload count update (see Design note above).
- `docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts` — regenerated,
  not hand-edited.
- `cmd/serf-hub/internal/hubcore/past.go` — `Rebuild`/`UpdateMeta` signature change (Invariant
  round) + `SetOnChange` ordering-hazard doc comment (Tree-wire gaps round).
- `cmd/serf-hub/web_api_tree.go` — `notifyTreeChanged` + `notifyMutation` helpers; Live loop routed
  through `apiTreeNodeTier`; `apiTreeProject` stamps `Favorite`.
- `hubapi/types.go` — `TreeProject.Favorite` field (Tree-wire gaps round).
- `cmd/serf-hub/main.go` — roster/past-index composed wiring + corrected comment (both rounds).
- `cmd/serf-hub/web_api_archive.go`, `web_api_favorite.go` — use `s.notifyMutation()`.
- `cmd/serf-hub/web_api_rename.go`, `web_api_project_delete.go` — compensating-broadcast logic.
- `cmd/serf-hub/app_rpc_test.go` — `newHubRPCTestServerWithWeb` helper.
- `cmd/serf-hub/web_api_tree_test.go`, `web_api_archive_test.go`, `web_api_favorite_test.go`,
  `web_api_rename_test.go`, `web_api_project_delete_test.go` — tests (all rounds).
- ~30 more `cmd/serf-hub/**/*_test.go` files — mechanical `Rebuild()` call-site signature fixes
  only, no behavior change (see Invariant round).

## Concerns / things worth a second look

1. **`appwire/wiretypes_fuzz_test.go` count bump** (see Design). I'm confident this is routine,
   not a "legacy exhaustive enumeration" the standing instruction meant to gate on, but flagging
   per the letter of that instruction.
2. **No new production diff-logic was written for triggers 1/2** — I reused
   `Roster.SetOnChange`/`PastIndex.SetOnChange`'s pre-existing fingerprint gating rather than
   building a new AttentionWatcher-style diff, since it's a closer semantic match and avoids
   duplicating tested logic. Worth confirming this reading of "study how the attention watcher
   decides changed" matches intent — happy to switch to a dedicated diff if the reuse is
   unwanted for some reason I'm not seeing.
3. Legacy UI: checked `cmd/serf-tui/hub_notifications.go` (switches on known methods, silently
   ignores unhandled ones — safe) and `cmd/serf-hub/jstest/` (grepped for exhaustive
   notification-set assertions; found none). Nothing legacy asserts an exact notification set.
4. ~~Two narrow residual gaps~~ **Closed in the Invariant round** (commit `754c87daa`): both
   `refreshRenamedMeta`'s "session not yet indexed" case and `handleAPIProjectDelete`'s "nothing
   actually removed" case now compensate with an explicit broadcast when `PastIndex`'s composed
   hook didn't fire, each pinned by a dedicated exactly-one test. See "Invariant round" above.
