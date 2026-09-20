Superseded by #1952 (retained screens and reconnect banners) and #1955 (live mutation readiness). #1922 remains the required recovery successor. Both replacement PRs preserve the reviewed original implementation in smaller pieces; codex/reconnect-round-four preserves the combined round-four head. The replacement stack remains held for recovery qualification.

---

Stacked on #1912 (the hubConnection follow-ups PR): this branch is built on
top of it because this PR also touches `hubConnection.ts` (adding `fatal`).
The diff below therefore includes #1912's commit until that one merges;
only this PR's own commit (`d86b50bf4`) needs reviewing here.

Per Jesse's ruling 7 (2026-09-18, #1595): screens survive a connection flap
behind a banner instead of the full-screen Reconnect wall. Closes #1595.

## Measurement

- The wall: `PluginsScreen.tsx`, `HubSettingsScreen.tsx` and
  `ProvidersScreen.tsx` all had the same shape - `if (!client || state !==
  "ready") return <View>...Reconnect...</View>` (or the ternary
  equivalent) - unmounting the ready-only child (and its store, scroll
  position, selection) on ANY non-ready transition, including a passive
  auto-reconnect where the underlying `AppwireClient` never even changes
  identity.
- The web equivalent (`cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx`):
  the workspace always stays mounted; a floating banner appears only for
  `ATTENTION_STATES = {"reconnecting", "closed"}`, after a `delayMs` grace
  window (10s default - "a routine sub-second reconnect never surfaces, a
  genuinely stuck connection tells the user promptly"). That's what "brief
  flap" means on the web: immediate banner-eligibility, delayed reveal - not
  something D2 needed to reproduce (mobile's own reconnect backoff, and this
  PR's own everReady/fatal gating, already keep a routine blip silent-ish
  behind a banner rather than a wall; a reveal-delay timer is a separate,
  optional polish this PR does not add).
- `#1595`'s own body already named the fix in two halves: "screens stay
  mounted... (then the native wiring of connectionChanged becomes the
  SECOND HALF of that change)". This PR is the first half only. Filed the
  second half as prime-radiant-inc/evener#1914 (PluginsScreen's store has no
  push-notification reconnect recovery - `connectionChanged` is never
  called - though a passive flap alone doesn't need it, since `client`'s
  identity is stable through one; only a missed hub-side change during the
  blip would be stale, which #1914 covers).

## Design

`connectionDisplay(state, everReady, fatal)` (`mobile-native/src/connectionDisplay.ts`,
pure, react-native-free per #1908's constraint):
- `"none"` once `state === "ready"`.
- `"wall"` when the screen has never shown anything yet (`!everReady`), or
  the close is fatal (a protocol mismatch a retry can never fix).
- `"banner"` for every other non-ready state, once something has already
  been shown - the flap the screen now survives.

`useConnectionDisplay(state, fatal)` wraps it with an `everReady` ref
(mutated during render, the same pattern `ForkScreen.tsx`'s `owner.current`
already uses).

`fatal` is new on `HubConnection`/`Connection` (`hubConnection.ts`,
`ConnectionProvider.tsx`): true only for a protocol close, computed
alongside the existing error-message classification, reset on every fresh
attempt.

`ConnectionStatus` (the existing status-row banner `screens.tsx` already
used for the Sessions/Conversation lists - name/state text + a conditional
Reconnect action + `ErrorMessage`) is reused rather than building a new
banner. It had to move out of `screens.tsx` into its own file first:
`screens.tsx` pulls in `@react-navigation/elements` (among a large import
graph), which fails to load under vitest (a `.png` asset import) - a VALUE
import of `ConnectionStatus` from a test-reached module broke
`ProvidersScreen.test.tsx` until the move (measured: reproduced the
failure, fixed by extraction, confirmed green).

Plugins/HubSettings additionally track the last non-null client in a ref:
`client` stays set through a passive flap (hubConnection.ts's own
generation guard), but a manual retry's token-refetch window does clear it
briefly, and the last-known-client fallback keeps the child mounted through
that gap too rather than dropping to the wall for a moment the banner
already covers. `ProvidersScreen` needs no such fallback -
`useCredentialStore()` already survives a flap on its own
(`connectionChanged` rebinds it, `credentialStore.ts`), and `<Providers>`
takes only `store`, never `client`, directly.

## Auto-refresh (native rule: never a manual "refresh to see the latest" affordance)

- `ProvidersScreen`: already correct via `connectionChanged`'s reconnect
  recovery (pre-existing, unaffected by this PR).
- `HubSettingsScreen`: `createHubOverviewStore` has no push notification by
  design (its own module doc: "no push-driven invalidation... callers
  decide when to fetch()/refresh()") and already offers pull-to-refresh
  (`RefreshControl`) - a standard native idiom, not the banned affordance.
  Nothing to add.
- `PluginsScreen`: a passive flap never changes `client` identity, so
  nothing was stale to begin with for that case. A missed hub-side plugin
  change during a flap this screen was disconnected for is a real,
  pre-existing gap (confirmed: `connectionChanged` was never called) -
  filed as #1914 rather than bundled here, matching #1595's own two-halves
  framing.

## Tests (mobile-native has no RTL harness - #1908)

- `connectionDisplay.test.ts`: the pure function's 4 branches, plus
  `useConnectionDisplay` hook tests for the three named scenarios - "ready
  -> reconnecting keeps mounted + banner", "reconnecting -> ready removes
  the banner", "fatal shows the wall even after having been ready".
- `hubConnection.test.tsx`: `fatal` reports true only for a protocol close,
  clears on `"ready"` and on a fresh attempt's own generation.
- `ProvidersScreen.test.tsx` (extends the one existing real-mount screen
  test - `PluginsScreen`/`HubSettingsScreen` have no test file to extend,
  a pre-existing gap #1908 already tracks): the same three scenarios driven
  through the REAL mounted screen via `tree.update` + the mocked
  `useConnection` - the list's rendered rows stay present through
  `reconnecting`, the banner text appears and disappears with `state`, and
  a fatal close replaces the list with the wall.

## Falsification

- `connectionDisplay.ts` moved aside: `connectionDisplay.test.ts` failed on
  the missing module; restored, green.
- `ProvidersScreen.tsx` reverted to its pre-PR content: the new "ready ->
  reconnecting keeps ... mounted" test failed (list content gone, replaced
  by the wall) exactly as measured; restored, green; `git status --short`
  showed only the intended files throughout.

## Gates

- `npx vitest run` (mobile-native): 86 files / 821 tests passed
- `npm run check` (mobile-native, tsc): clean
- `make lint-package-imports`: PASS (no `appwire-client` files touched)

## Diff (includes #1912's commit until it merges - see the stacking note above)

```
$(git diff -M --stat origin/main...HEAD)
```

Closes #1595.