# D28 lane 2 state note (retired 2026-09-18 ~15:50 PDT, past 550k tokens)

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-d14-navigation-store`.
Working tree clean, checked out on `claude/sdk-d28-d2-flap-banner` at handoff.

## Heads

- **#1915** `claude/sdk-d28-d2-flap-banner` @ `a48f7e0c85a37e960db059f88801b4f696b4feab` (base main via
  `d4f2414f2`). Round 3 posted: https://github.com/prime-radiant-inc/evener/pull/1915#issuecomment-5737003239
- **#1922** `claude/sdk-d28-d2a-reconnect-recovery` @ `a2fd89ae6b01e77c10f3dfe26970b8ff720ba962`, stacked
  on #1915's tip above (rebased onto it this round; own lines verified byte-identical before/after via
  `diff <(git show <old>:<file>) <(git show <rebased>:<file>)` per touched file). Round 2 posted:
  https://github.com/prime-radiant-inc/evener/pull/1922#issuecomment-5737072401

Both branches are pushed and match origin exactly (`git ls-remote` confirmed at handoff). Gates green on
both: `mobile-native && npx vitest run` (87 files / 834 tests), `npm run check` clean, root
`make lint-package-imports` PASS. Neither PR's CI had reported back yet at handoff — **next lane should
check CI and RoboRev on both before merging**, per the queue's usual rule.

## What each still owes

- **#1915**: nothing known outstanding besides CI/RoboRev. Non-test diff vs origin/main is 350 lines
  (under the 400 ceiling), so no split was needed this round.
- **#1922**: nothing known outstanding besides CI/RoboRev. Non-test diff vs #1915's tip is 159 lines.
  Since it's stacked, its own PR diff (what a reviewer sees) is against #1915, not origin/main - re-run
  `git diff -M --stat a48f7e0c85a37e960db059f88801b4f696b4feab...HEAD` from `claude/sdk-d28-d2a-reconnect-recovery`
  if #1915 moves again and #1922 needs another rebase.
- Neither PR has been merged. Merge order matters: #1922 is stacked on #1915, so #1915 merges first;
  #1922 needs a rebase onto main afterward (same procedure as this round: `git rebase --onto <new-main-tip>
  <old-1915-tip> claude/sdk-d28-d2a-reconnect-recovery`, expect conflicts in whichever files both PRs
  touched, union-resolve, re-run gates).
- **#1914** (the plugin-store staleness issue named in D2's own body) is CLOSED by #1922's
  `model.connectionChanged` wiring in PluginsScreen - see below. No further action needed once #1922
  lands; the issue can be closed in the same breath as the merge, or left for the coordinator's own
  closing comment.
- One item raised by round-1 review and explicitly deferred, not re-raised since: nothing else is known
  outstanding. If a round 4/round 3-of-D2a shows up, re-read both PRs' current disposition comments
  first (they're the authoritative record of what's fixed and why - this note summarizes, doesn't
  replace them).

## The whenReady / act / confirm gating pattern (#1915 round 3)

One predicate (`isReady(state)`, `connectionDisplay.ts`) is applied through exactly three mechanisms,
not sprinkled ad hoc `if (ready)` copies:

1. **`whenReady(ready, handler)`** (`connectionDisplay.ts`) - wraps an `onPress` that issues a request
   but doesn't route through a shared choke point below. Used for: Plugins' list-error "Retry",
   HubSettings' "Retry hub information" and the upgrade's `onStart`, Providers' "Test credentials"/
   "Edit instance"/"Replace key"/"Replace credential JSON"/"Add provider instance", MarketplaceBrowser's
   "Retry marketplaces"/"Retry catalog"/"Retry installed status"/"Add marketplace" open button/its
   modal's submit button.
2. **`act()`/`confirm()`** (screen-local closures, ProvidersScreen.tsx and MarketplaceBrowser.tsx each
   have their own) - the pre-existing per-screen consolidation point every gated mutation already routed
   through for busy-tracking; each now also checks `!ready` at the top and bails before touching any
   component state. This is the ONE place that covers: Providers' save-key/JSON, make-default, clear
   stored key, logout, remove (all via `act`, and the confirm-wrapped three also via `confirm`);
   MarketplaceBrowser's install/refresh/remove (via its own `act`).
3. **`runGatedMutation(gate, ready, action)`** (`pluginMutationGate.ts`) - the package-level choke point
   every gated mutation across BOTH Plugins and MarketplaceBrowser (and `AddMarketplace`) funnels
   through; it now refuses before calling `gate.run` at all if `!ready`, so a caller whose own
   `disabled` prop is stale or bypassed still can't reach the wire. Every `disabled` prop was ALSO
   updated with `|| !ready` directly - `whenReady`/`act`/`confirm`/`runGatedMutation` are the onPress-side
   belt to the disabled-prop suspenders, not a replacement for it.

**Deliberately ungated: "Cancel" buttons.** ProvidersScreen's credential-editor Cancel
(`disabled={state.busy}`, no `!ready`) and `AddMarketplace`'s top-level Cancel are NOT gated on
readiness - they issue no request, and gating them would trap a user who lost connection mid-edit with
no way to back out. **Precedent measured before deciding this, not asserted**:
`mobile-native/src/ProviderEditor.tsx`'s own Cancel button (line ~191, `disabled={saving}`) is *already*
excluded from its `disabled` prop for exactly this reason, predating this round entirely - readiness
gates writes, never the exit from a form. Also NOT gated: MarketplaceBrowser's kind-selector Choice
buttons and both TextInputs in `AddMarketplace` (pure local form state, no wire call - blocking them
while merely disconnected stops a user composing their pending add for no safety benefit); the
per-plugin "Open" action on an already-installed plugin (pure local navigation, `!existing && !ready`
is the actual gate, not a blanket `!ready`).

## The render-client adoption rule (#1922 round 2)

`useRenderClient(client, state, hubId)` (`connectionDisplay.ts`) now takes the connection `state`
alongside `client`, and adopts a NEW client only once `state === "ready"` for it:

```
if (state === "ready") lastClient.current = client;
return state === "ready" ? client : lastClient.current;
```

**Why this changed**: hubConnection.ts's manual-retry sequence clears `client` to `null` while it dials,
then reports the FRESH client object while `state` is still `"connecting"` (constructed, not yet
dialed). The old rule (`client ?? lastClient.current`) adopted that fresh-but-not-ready client
immediately (any truthy value wins), which changed `Plugins`/`HubSettings`'s `useMemo(() =>
createXStore(client), [client])` dependency, rebuilt the store, and fired its mount effect's
`fetchX()` against a socket that hadn't dialed yet - AppWire rejects, so the result was a rejected
request, blank content, or a stale-looking upgrade error, exactly through the retry gap the banner
exists to smooth over. The new rule keeps rendering whichever client was last actually ready through
BOTH the null gap and the whole `"connecting"` window, so the store is never rebuilt (and never
re-fetches) until the replacement is genuinely usable.

**Web analogue, measured not assumed**: `cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx` only
calls its own `onClientReplaced` callback (which swaps `AppShell`'s `ClientProvider` slot) AFTER `await
fresh.connect()` resolves and confirms the fresh client isn't already `"closed"` - never on
construction. `useRenderClient`'s new rule is the native equivalent of that same principle, expressed
as a pure ref-based hook instead of an async callback gate.

Both `PluginsScreen.tsx` and `HubSettingsScreen.tsx` call sites were updated
(`useRenderClient(client, state, hubId)`); ProvidersScreen doesn't use this hook (it relies on
`useCredentialStore()`'s own `connectionChanged`-based rebinding instead) and needed no change here.

## Reconnect-recovery wiring, per screen (mixed across #1915 round 1 and #1922)

- **PluginsScreen** (`PluginsScreen.tsx`, its `Plugins` inner component): `model.connectionChanged(client,
  connectionState)` in an effect declared **BEFORE** the mount-fetch effect (`model.start();
  fetchPlugins()`). Ordering matters and is load-bearing, not stylistic: `fetchPlugins()`'s
  `readRevisioned` marks a revision live SYNCHRONOUSLY (before its `await`), so if the connectionChanged
  effect ran second, it would see that revision already live on the very first mount and refetch a
  second time for the same first load. Verified by measurement (counted `evener/plugin/list` calls),
  not by reading the docs alone.
- **MarketplaceBrowser** (#1922 round 2): identical wiring, identical ordering, identical reasoning -
  `model.connectionChanged(client, connectionState)` before `model.start(); fetchMarketplaces()`. Closes
  the "retained but never told to recover" gap for the marketplace list and catalog cache.
- **HubSettingsScreen**: `createHubOverviewStore` has NO `connectionChanged` method at all (verified in
  `hubOverview.ts`'s own module doc: "callers decide when to fetch() or refresh()" - no push
  notification exists on the wire for it). Instead, a small new hook `useReconnectRecovery(state,
  onReconnect)` (`connectionDisplay.ts`) fires `onReconnect` the moment `state` moves back to `"ready"`
  from anything else - never on the render that STARTS ready (that's the screen's own mount/focus
  fetch, not a recovery). HubSettingsScreen wires it to `model.getState().refresh(); void
  upgrade.reconcileAfterReconnect();` - the same pair `useFocusEffect` already calls once on focus.
- **ProvidersScreen**: no new wiring needed. `useCredentialStore()` (`credentialStore.ts`, pre-existing)
  already rebinds via its own `connectionChanged` on every `useConnection()` transition, from a layout
  effect bound before the list's own mount effect - this was already correct before D28 started.

**#1914 closure**: #1914 was "PluginsScreen's PluginsStore has `connectionChanged` but nothing calls
it - a plugin set change while disconnected is never picked up on reconnect." The `model.connectionChanged`
wiring above (PluginsScreen, #1915 round 1) is the fix; #1914 is closed by #1915, not by #1922 (#1922
only extended the SAME pattern to MarketplaceBrowser, which was #1914's implicit sibling gap, not #1914
itself).

## mobile-native gates cheat-sheet

- `cd mobile-native && npx vitest run` - full suite, ~1.5-2s, 87 files / 834 tests at handoff. Run the
  single touched file first while iterating (`npx vitest run src/<file>.test.ts`), full suite before
  push.
- `npm run check` (tsc, `tsconfig.check.json` - includes test files, `strict: true`).
- Root `make lint-package-imports` - only matters if `appwire-client/typescript` files changed; neither
  #1915 round 3 nor #1922 round 2 touched the package, so this was a formality both times.
- **Never** `npx biome` over `mobile/` or `mobile-native/` - no config there.
- `node_modules` in this worktree's `mobile-native/` was a REAL directory (not a symlink) at lane start
  (per the retired lane-1 note); still true. If it's a symlink in your worktree, never `npm ci` - it's
  the shared install every other lane's gates are running against.
- Falsification pattern used throughout, every time: `git diff <file> > /tmp-or-scratch/x.diff &&
  git checkout HEAD -- <file>` (or a targeted `python3` string-splice when only ONE function/block needs
  to disappear temporarily, e.g. removing a single `useEffect` call), run the ONE new test, confirm it
  fails FOR THE STATED REASON (read the actual assertion failure, don't just check exit code), then
  restore and confirm green again. Every fix this round was falsified this way before being trusted; the
  exact failure text for each is quoted in the two disposition comments linked above.

## vitest traps hit this round

- **Effect ordering inside one component determines correctness, not just style.** Declaring the
  `connectionChanged` effect AFTER the mount-fetch effect (the natural-looking order: "mount, then wire
  up recovery") causes a real double-fetch on first mount, because `fetchPlugins()`'s internal
  `readRevisioned` marks its own read "live" synchronously before its `await`, and effects in one
  component fire in declaration order within the same commit. Caught this by writing the reconnect test
  BEFORE assuming the ordering didn't matter, watching it assert 2 calls on the FIRST render (should be
  1), and only then finding the cause. Lesson: when wiring two effects that both touch the same store on
  mount, trace which one can observe the other's synchronous side effects before picking an order -
  don't assume declaration order is free to pick arbitrarily just because both effects "look independent."
- **`react-test-renderer`'s host mocks need to be "smart" (render their children/props) the moment a test
  needs to inspect anything beyond top-level text.** `nativeModuleMock()` in `renderNative.testkit.tsx`
  had `SectionList` already built this way (from the pre-existing Providers test); this round added the
  same treatment to `FlatList` (needed for PluginsScreen's reconnect test to render plugin rows/ListHeader/
  ListEmpty) - a plain inert `"FlatList"` string renders NOTHING inside it since `ListHeaderComponent`/
  `renderItem`/`ListEmptyComponent` are just unused props on a host string, not children.
- **`node.type === "Pressable"` fails typecheck**, not lint, when querying `ReactTestInstance.type`
  against a string literal in the mocked (string-typed) react-native surface - `tsc` sees `ElementType`
  vs the literal `"Pressable"` as non-overlapping types even though it's correct at runtime (the mock
  really does use the string `"Pressable"` as the host type). Fix: `(node.type as unknown) === "Pressable"`.
  Caught by `npm run check`, not by vitest itself (vitest doesn't type-check).
- **Rebasing a stacked branch across a busy predecessor produces REAL conflicts, not just noise** -
  `HubSettingsScreen.tsx`'s import list and `connectionDisplay.test.ts`'s test list both conflicted
  during #1922's rebase onto #1915's new tip, because both PRs touched the same import block/test
  section independently. Resolved by keeping BOTH sides' additions (never picking one over the other) and
  verifying with `diff <(git show <old-commit>:<file>) <(git show <rebased-commit>:<file>)` per file
  that nothing from D2a's own content silently vanished in the union.
- **`gh api ... -f body=@file`** does NOT read the file's contents the way the top-level `gh` CLI's `-f`
  flag sometimes implies - it posts the literal string `@/path/to/file` as the comment body. Caught
  immediately by reading the POST response's echoed `body` field. Fix: write a small JSON file with
  Python (`json.dump({"body": open(path).read()}, ...)`) and pass it via `gh api ... --input file.json`.
