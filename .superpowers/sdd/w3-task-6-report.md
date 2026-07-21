# Wave 3 Task 6 report — `serf/tree/changed` hub broadcast

Branch `w3-treepush`, off `0996d8afb`. 4 commits (the 4th is a post-review fix — see "Review-fix
round" below). Full mandated suite green (`go test ./cmd/serf-hub/... ./appwire/...
./internal/appwirets/... ./internal/appwiredoc/...`, exit 0), `make generate` idempotent (zero
further diff), `gofmt -l`/`go vet` clean on every touched file, `golangci-lint run
--max-issues-per-linter 0 --max-same-issues 0` on the touched packages clean (0 issues),
`serf-namingcheck`/`serf-internalcheck`/`serf-docscheck` all clean. `go build -tags serffuzz
./cmd/serf-hub/...` and `go vet -tags serffuzz ./cmd/serf-hub/...` also clean (belt and
suspenders — that tag isn't part of the mandated verification, but main.go/main_background.go
adjacency made it worth a check). Zero touches to hand-written frontend src, templates, or
assets (`git diff --stat 0996d8afb..HEAD -- cmd/serf-hub/templates/ cmd/serf-hub/assets/
cmd/serf-hub/frontend/src/` shows only the regenerated `types.gen.ts`).

## Commits

| Commit | Unit |
|---|---|
| `6238fbcf9` | appwire: catalog `serf/tree/changed` notification |
| `1f996da5b` | hub: broadcast on roster and past-index deltas |
| `91b8c8900` | hub: broadcast after archive/favorite/rename/project-delete |
| `25b5bbdfe` | hub: single tree-changed broadcast per mutation — PastIndex routing already notifies (review-fix round) |

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

## Trigger-site inventory (file:line, current)

| Trigger | Site | Mechanism |
|---|---|---|
| 1. Roster delta | `main.go:323` (`roster.SetOnChange`) | composed hook, fires from `Roster.Refresh` only on fingerprint delta |
| 2. Past-index change | `main.go:322` (`past.SetOnChange`) | composed hook, fires from `PastIndex.Rebuild`/`UpdateMeta` only on fingerprint delta |
| 3. Archive mutation | `web_api_archive.go:70` (`s.notifyMutation()`) | explicit, after `Archive.Set` succeeds — ArchiveStore never routes through PastIndex |
| 3. Favorite mutation | `web_api_favorite.go:41` (`s.notifyMutation()`) | explicit, after `Favorite.Set` succeeds — FavoriteStore never routes through PastIndex |
| 3. Rename (ended-session path) | `web_api_rename.go:105` (`Past.UpdateMeta` call itself) | **via trigger 2's composed hook**, not an explicit call — see below |
| 3. Rename (live-daemon path, both the live and became-live branches) | `web_api_rename.go:120` and `:130` (both inside `refreshRenamedMeta`'s `Past.UpdateMeta` calls) | **via trigger 2's composed hook** |
| 3. Project-delete | `web_api_project_delete.go:189` (`Past.Rebuild` call itself) | **via trigger 2's composed hook** |

Helpers: `web_api_tree.go:42` (`func notifyTreeChanged`), `web_api_tree.go:52`
(`func (s *WebServer) notifyMutation`, folds the PokeAttention+notifyTreeChanged pair for archive/
favorite).

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

## Files

- `appwire/types.go`, `appwire/protocol.go` — catalog entry.
- `appwire/wiretypes_fuzz_test.go` — nil-payload count update (see Design note above).
- `docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts` — regenerated,
  not hand-edited.
- `cmd/serf-hub/web_api_tree.go` — `notifyTreeChanged` + `notifyMutation` helpers.
- `cmd/serf-hub/main.go` — roster/past-index composed wiring + corrected comment.
- `cmd/serf-hub/web_api_archive.go`, `web_api_favorite.go` — use `s.notifyMutation()`.
- `cmd/serf-hub/web_api_rename.go`, `web_api_project_delete.go` — explicit calls removed
  (double-broadcast fix); rely on `Past.UpdateMeta`/`Rebuild`'s composed hook.
- `cmd/serf-hub/app_rpc_test.go` — `newHubRPCTestServerWithWeb` helper.
- `cmd/serf-hub/web_api_tree_test.go`, `web_api_archive_test.go`, `web_api_favorite_test.go`,
  `web_api_rename_test.go`, `web_api_project_delete_test.go` — tests (original + review-fix round).

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
4. **Two narrow residual gaps I found while fixing the double-broadcast, and deliberately did NOT
   patch**, since the reviewer's ask was specifically to *remove* the redundant calls and this is
   a "refetch hint" over an always-authoritative REST endpoint (`/api/tree`), not a
   correctness-critical data path — flagging so this judgment call is visible rather than silent:
   - `refreshRenamedMeta`: if `Past.Find(rid)` misses entirely (session not yet indexed — though
     `Find` itself self-heals via an internal `Rebuild()` on a cache miss, so this needs the
     session to also be absent from a *fresh* disk scan, e.g. a rename raced within the same
     instant as spawn, before its meta.json even exists), no `UpdateMeta` call happens at all on
     that path, so nothing broadcasts. Self-heals on the next periodic `Rebuild` tick regardless.
   - `handleAPIProjectDelete`: the project-level `Archive.Delete`/`Favorite.Delete` calls run
     unconditionally, but those stores are bump-only (never routed through PastIndex per this
     design). If every session in the target project is skipped (none actually removed from
     disk), `Past.Rebuild()` finds no delta and its hook doesn't fire, so a project-delete that
     only cleared archive/favorite decisions (nothing physically deleted) broadcasts nothing.
   Both are eventually consistent (any later unrelated trigger, or the requester's own optimistic
   client-side update from the response body, catches it up) — I judged them out of scope for a
   "single broadcast per mutation" bug fix rather than building conditional-guard logic for two
   more edge cases on top of it, but a reviewer who wants these closed too should say so.
