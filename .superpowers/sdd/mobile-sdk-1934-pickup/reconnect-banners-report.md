# Reconnect banner and screen-retention report

## B branch and anchors

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-d14-navigation-store`

Branch: `codex/reconnect-banners`

- B source endpoint: `acdb12de8248c506984d7c12064e2f2f5cd4d086`
  (`feat(mobile): screens survive a connection flap behind a banner (D28 D2)`).
- Current main fetched at: `e7630003ea3d93b392384a90e6347ff4d2da73c5`.
- B merge commit: `8136fc1b5b023717500a0a3e81cf385ab5e06bd7`.
- Merge parents: B source `acdb12de8` and refreshed `origin/main`
  `e7630003e`.
- The prior round-four work remains preserved at
  `codex/reconnect-round-four` ->
  `ed3144fbb025eb0412e0f2cb2dcf4f97ac27b878`.
- The old remote #1915 branch remains at
  `b689acb3b36bcf52d4463b46d7f47c9900480c22`.
- The parked #1922 WIP deletion remains at
  `613dac7f1edb120dae41f3c4615e5460670b5da1`.

No push, PR operation, review comment, or merge to a shared branch was made.

## Own patch identity

The refreshed B patch is the original banner/retention patch applied to current
main. Comparing `origin/main..HEAD` only within `mobile-native/src` gives:

- **Non-test:** 184 added / 82 removed = **266 changed lines**.
- All B files, including tests: 381 added / 92 removed.
- Stable patch ID before and after main integration:
  `10c9c41ba1714063a29bec239f10b437373ed556`.

The non-test files are:

- `ConnectionProvider.tsx`: fatal connection state in the provider value.
- `ConnectionStatus.tsx`: the floating reconnect banner.
- `HubSettingsScreen.tsx` and `PluginsScreen.tsx`: retained child and
  last-client fallback during recoverable flaps.
- `ProvidersScreen.tsx`: store-backed retained screen with wall/banner display.
- `connectionDisplay.ts`: wall/banner/none decision and per-screen `everReady`.
- `hubConnection.ts`: protocol-close fatal classification.
- `screens.tsx`: extraction of the connection status presentation.

Duplicate declaration inspection across these files found no duplicate named
functions. `git diff --check` passed.

## Focused validation

Attempted focused display, hub-connection, and screen tests:

```text
npx vitest run src/connectionDisplay.test.ts src/hubConnection.test.tsx src/ProvidersScreen.test.tsx
```

The command stopped before collection because this checkout has no installed
Vitest package:

```text
Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'vitest'
```

No test cases executed. Dependencies were not installed and no broader suite
was substituted.

Native typecheck was also attempted:

```text
cd mobile-native && npm run check
```

It is blocked by missing checkout dependencies:

```text
error TS2688: Cannot find type definition file for 'node'.
tsconfig.json(2,14): error TS6053: File 'expo/tsconfig.base' not found.
```

The requested package import gate passed:

```text
make lint-package-imports
PASS lint-package-imports
```

Native/mobile Biome and the full suite were not run.

## Immediate C readiness seam

C remains held until B is reviewed and pushed. C must be based immediately on
this merged B endpoint and extend the existing `connectionDisplay.ts` seam;
it must not create a parallel utility or rewrite B.

- C adds the live scope/client/state predicate, `whenReady`, and the
  predicate-aware `runGatedMutation` API to B's display/gate surfaces.
- The live predicate must capture the raw current `client` from
  `useConnection`, not B's retained `renderClient`. The retained client is
  valid for rendering a store while a manual retry dials, but it must never
  authorize a request.
- The `runGatedMutation` signature and every Plugins/Marketplace caller must
  change together so a request cannot bypass invocation-time readiness.
- C preserves B's disabled affordances and adds deferred invocation checks;
  it does not change cancellation, form editing, or navigation behavior.

## Recovery and #1922 seams

B intentionally contains no live readiness fix and no #1922 recovery behavior.
The following remain outside this branch:

- C owns the static and live readiness fixes, including the real deferred
  upgrade-confirmation test and provider/marketplace choke-point guards.
- #1922 owns fatal-close retry/recovery ordering and the replacement-client
  remount hazard. That work must not be folded into B or C.
- #1922 also owns the refused-versus-busy-copy review question.
- The parked WIP deletion at `613dac7f1` was not applied or removed.

B will not be merged before its immediate C follow-up is concrete. This branch
is prepared for review only; it is not pushed.
