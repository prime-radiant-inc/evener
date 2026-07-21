# Wave 3 Task 6 report — `serf/tree/changed` hub broadcast

Branch `w3-treepush`, off `0996d8afb`. 3 commits. Full mandated suite green (`go test
./cmd/serf-hub/... ./appwire/... ./internal/appwirets/... ./internal/appwiredoc/...`, exit 0),
`make generate` idempotent (zero further diff), `gofmt -l`/`go vet` clean on every touched file,
`golangci-lint run --max-issues-per-linter 0 --max-same-issues 0` on the touched packages clean
(0 issues), `serf-namingcheck`/`serf-internalcheck`/`serf-docscheck` all clean. `go build -tags
serffuzz ./cmd/serf-hub/...` and `go vet -tags serffuzz ./cmd/serf-hub/...` also clean (belt and
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

**Trigger 3 — explicit per-handler calls**, mirroring the existing `if s.cfg.PokeAttention != nil
{ s.cfg.PokeAttention() }` pattern already at each of these same four call sites (unguarded here
since `s.appRPC` is unconditionally constructed by `NewWebServer`, never nil).

## Trigger-site inventory (file:line)

| Trigger | Site | Mechanism |
|---|---|---|
| 1. Roster delta | `main.go:323` (`roster.SetOnChange`) | composed hook, fires from `Roster.Refresh` only on fingerprint delta |
| 2. Past-index change | `main.go:322` (`past.SetOnChange`) | composed hook, fires from `PastIndex.Rebuild`/`UpdateMeta` only on fingerprint delta |
| 3. Archive mutation | `web_api_archive.go:74` | explicit call after `Archive.Set` succeeds |
| 3. Favorite mutation | `web_api_favorite.go:44` | explicit call after `Favorite.Set` succeeds |
| 3. Rename (ended-session path) | `web_api_rename.go:109` | explicit call after `Past.UpdateMeta` |
| 3. Rename (live-daemon path, both the live and became-live branches) | `web_api_rename.go:124` and `:135` (both inside `refreshRenamedMeta`) | explicit call after the daemon confirms the rename |
| 3. Project-delete | `web_api_project_delete.go:193` | explicit call after cleanup + `Past.Rebuild` |

Helper: `web_api_tree.go:42` (`func notifyTreeChanged`).

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
  `TestProjectDeleteBroadcastsTreeChanged` — each RED with "timed out waiting for a
  notification" before its one-line handler change, GREEN after. All POST to the real HTTP
  endpoint (or, for the live-daemon rename path, call `refreshRenamedMeta` directly — see below)
  through `newHubRPCTestServer`/`dialHubRPC`, a real hub + real dialed WS client, asserting the
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

## Files

- `appwire/types.go`, `appwire/protocol.go` — catalog entry.
- `appwire/wiretypes_fuzz_test.go` — nil-payload count update (see Design note above).
- `docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts` — regenerated,
  not hand-edited.
- `cmd/serf-hub/web_api_tree.go` — `notifyTreeChanged` helper.
- `cmd/serf-hub/main.go` — roster/past-index composed wiring.
- `cmd/serf-hub/web_api_archive.go`, `web_api_favorite.go`, `web_api_rename.go`,
  `web_api_project_delete.go` — one-line calls at each success path.
- `cmd/serf-hub/web_api_tree_test.go`, `web_api_archive_test.go`, `web_api_favorite_test.go`,
  `web_api_rename_test.go`, `web_api_project_delete_test.go` — new tests (all above).

## Concerns / things worth a second look

1. **`appwire/wiretypes_fuzz_test.go` count bump** (see Design). I'm confident this is routine,
   not a "legacy exhaustive enumeration" the standing instruction meant to gate on, but flagging
   per the letter of that instruction.
2. **Rename's two call sites inside `refreshRenamedMeta`** mean a live-daemon rename that hits
   the `loadSessionMetaForRename` failure sub-branch still broadcasts (falls through to the
   trailing call) — intentional: `refreshRenamedMeta` is only ever invoked after the daemon
   already confirmed the rename succeeded, so every path through it represents a genuine
   successful mutation regardless of whether the local past-index reload happened to succeed.
3. **No new production diff-logic was written for triggers 1/2** — I reused
   `Roster.SetOnChange`/`PastIndex.SetOnChange`'s pre-existing fingerprint gating rather than
   building a new AttentionWatcher-style diff, since it's a closer semantic match and avoids
   duplicating tested logic. Worth confirming this reading of "study how the attention watcher
   decides changed" matches intent — happy to switch to a dedicated diff if the reuse is
   unwanted for some reason I'm not seeing.
4. Legacy UI: checked `cmd/serf-tui/hub_notifications.go` (switches on known methods, silently
   ignores unhandled ones — safe) and `cmd/serf-hub/jstest/` (grepped for exhaustive
   notification-set assertions; found none). Nothing legacy asserts an exact notification set.
