# D28 phone-half lane state note (retired 2026-09-18 ~13:45 PDT, past 500k tokens)

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-d14-navigation-store`.
Current branch: `claude/sdk-d28-d2-flap-banner` @ `acdb12de8248c506984d7c12064e2f2f5cd4d086` (base main,
rebased clean onto `d4f2414f2` after #1912 squash-merged; own-diff content verified byte-identical
before/after via `diff <(git show <old>) <(git show <new>)`). Working tree clean.

## Landed
- D1 #1888 → squash-merged `02e391dc0`: `ConnectionProvider` moved onto the package's
  `createConnectionStore`. Went through 3 review rounds fixing real races (stale client across a
  hub switch; the same class, not fully closed round 2, for a same-hub retry — final fix keys
  `connectedFor` on the full `{activeId, activeOrigin, foreground, attempt}` generation, not just
  the hub id).
- D1 follow-ups #1912 (opus `/simplify` on #1888) → squash-merged `d4f2414f2`: single JSON key
  instead of the `ConnectionTarget` struct+comparator, dropped the always-true `foreground &&`
  term, teardown consolidated into the effect's cleanup (was in two places), ref carries its own
  client (drops the `AppwireClientLike` cast), the connecting client write merged with its
  `"connecting"` state (was two `setState` calls). Finding #7 (package `FakeClient` needs
  `close()`+listener count) filed as #1911, not implemented — low value, said so in the review.

## #1915 — D2 (screens survive a flap behind a banner), open, base main
Head `acdb12de8248c506984d7c12064e2f2f5cd4d086`, just rebased and force-pushed
(`--force-with-lease=claude/sdk-d28-d2-flap-banner:d86b50bf495f57249b0847daa4a37d4244203901`),
comment posted. `git diff -M --stat origin/main...HEAD` shows only D2's 11 files (381
insertions(+), 92 deletions(-)). CI had not reported back at handoff — **next lane should check CI
and RoboRev on #1915 before merging**, same as any other PR in the queue; nothing else is owed on
my account. Gates were green locally before the rebase (86 files / 821 tests, `npm run check`
clean, `make lint-package-imports` PASS) and the rebase touched nothing but the base commit.

What it does: `PluginsScreen`/`HubSettingsScreen`/`ProvidersScreen` no longer unmount their
ready-only child into a full-screen "Reconnect" wall on every `state !== "ready"` transition
(including a passive auto-reconnect where the client's identity never even changes). New pure
`connectionDisplay(state, everReady, fatal)` in `mobile-native/src/connectionDisplay.ts` decides
`"none"` / `"wall"` (never connected yet, or a protocol close a retry can't fix) / `"banner"`
(every other flap). `fatal` is new on `HubConnection`/`Connection`. `ConnectionStatus` (the
existing status-row banner `screens.tsx` already used for Sessions/Conversation) was extracted
into its own file — **do not import it (or anything else) as a VALUE from `screens.tsx` into a
vitest-reached module**: that file pulls in `@react-navigation/elements`, which fails to load
under vitest (a `.png` asset require) and silently kills any test file that transitively imports
it. Type-only imports from `screens.tsx` (`type Routes`) are fine; they're erased and never load
the module at runtime.

Closes #1595.

## #1914 — filed, not started, scope
Second half of #1595's own framing, explicitly deferred there and again in #1915's body:
`PluginsScreen`'s `PluginsStore` (from `createPluginsStore`) already has `connectionChanged`
(`StoreLifecycle`'s reconnect-recovery — re-reads the list once `state` returns to `"ready"`, for
whatever a client disconnected during might have missed) but nothing calls it; the screen still
builds the store via `useMemo(() => createPluginsStore(client), [client])`, keyed on client
identity. A passive flap doesn't need this (client identity is stable through one, per
hubConnection.ts's generation guard, so the store already isn't rebuilt or lost — #1915 alone
fixes the common case). What's still missing: if the hub's plugin set changes WHILE this app was
disconnected, nothing tells the store to re-check once reconnected — a real, narrow staleness gap.
Suggested fix in the issue: a `usePluginsStore()` hook mirroring `useCredentialStore()`
(`credentialStore.ts` — build once via `useState`, rebind via `connectionChanged` in a layout
effect), built lazily on the first non-null client since `createPluginsStore` needs one at
construction (unlike `createCredentialInstancesStore`, which takes none).
`HubSettingsScreen`/`createHubOverviewStore` is NOT in scope for this — it has no push
notification by design (its own module doc) and already has pull-to-refresh (`RefreshControl`);
nothing to wire.

## Residual not filed (minor, noticed not acted on)
`ProvidersScreen`'s "Sign in"/"Refresh sign-in" button is now reachable while `display ===
"banner"` (not `"ready"`) since the wall no longer hides `<Providers>` during a flap — previously
it was unreachable in that state because the wall replaced the whole child. Worst case today: the
credential mutation methods reject cleanly (package convention — "no client connected" style
errors) and `Providers`'s own `act()` wrapper already shows a generic failure message; not a
crash, not silent data loss. Didn't gate it behind `state === "ready"` to keep #1915 to one
mechanism. Worth a follow-up issue if it bothers anyone in practice.

## Gates cheat-sheet, mobile-native
- `cd mobile-native && npx vitest run` — full suite, ~2s, 86 files / 821 tests at handoff. Run the
  single touched file first while iterating (`npx vitest run src/<file>.test.tsx`), full suite
  before push.
- `npm run check` (tsc, `tsconfig.check.json` — includes test files, `strict: true`).
- Root `make lint-package-imports` — only matters if `appwire-client/typescript` files changed;
  neither D1 nor D2 touched the package, so this was a formality both times.
- **Never** `npx biome` over `mobile/` or `mobile-native/` — no config there, and running it
  reformats whole files (memory: `no-biome-over-mobile-src`).
- `node_modules` in this worktree's `mobile-native/` was a REAL directory, not a symlink, at lane
  start; `react-test-renderer` was missing so I ran `npm ci` once (said so in #1888's PR body). If
  it's a symlink in your worktree, never `npm ci` — it's the shared install every other lane's
  gates are running against.
- No RTL harness for screens/providers (#1908, pre-existing). `PluginsScreen`/`HubSettingsScreen`
  have zero test files. `ProvidersScreen.test.tsx` is the one screen with a real-mount harness
  (`renderNative.testkit.tsx`'s `render`/`renderedText`, `react-native` fully mocked via
  `nativeModuleMock()`) — extend that one if you touch these three screens again. Everything else
  gets tested as an extracted pure function/hook (`connectionDisplay.ts`'s own pattern, matching
  `keybindingOfflineRecovery.ts`'s precedent from D6).
- Falsification pattern used throughout: `git diff <file> > /tmp/x.diff && git checkout HEAD --
  <file>` (or `git checkout <old-commit> -- <file>` mid-lane), run the new test, confirm it fails,
  `git apply /tmp/x.diff` to restore, confirm green, `git status --short` clean.

Stopping here per the coordinator's instruction (past 500k tokens). #1915 is the only open item on
this lane; #1914 and the residual above are filed/noted for whoever picks up next.
