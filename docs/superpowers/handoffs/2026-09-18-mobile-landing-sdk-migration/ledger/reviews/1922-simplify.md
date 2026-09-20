# /simplify — PR #1922 (D2a: stores recover on reconnect, hub-scoped display, adopt-when-ready)

Head reviewed: `a2fd89ae6b01e77c10f3dfe26970b8ff720ba962`, own diff vs `pr-1915-review`
(`a48f7e0c8`, PR #1915's head — #1922 is stacked on it). Quality only — reuse, simplification,
efficiency, altitude. Correctness is RoboRev's. Nothing below touches the native rule this row
exists to serve: the screens auto-refresh on reconnect and never grow a "refresh to see the latest"
affordance.

---

## 1. `useConnectionDisplay: a hub change resets everReady` asserts nothing about the reset — must-fix-before-merge

`mobile-native/src/connectionDisplay.test.ts:79-91`.

```ts
let hubId = "hub-1";
const state: ConnectionState = "connecting";
const hook = renderHook(() => useConnectionDisplay(hubId, state, false));
// hub-1 was never ready either, but prove the reset by getting hub-1
// ready first, then switching hubId without unmounting the hook.
expect(hook.result.current).toBe("wall");
hubId = "hub-2";
hook.rerender();
expect(hook.result.current).toBe("wall");
```

`state` is a `const` fixed at `"connecting"`, so hub-1 is never ready and `everReady` is `false` for
the whole test. Delete the `scope` reset block the test is named after
(`connectionDisplay.ts:53-57`) and it still passes: `wall` → `wall` either way. The comment promises
the opposite of what the body does ("prove the reset by getting hub-1 ready first" — it never does),
and the very next test (`:93-105`, `everReady for the previous hub does not banner the next hub`)
is the one that genuinely fails without the reset.

Cost: a test that cannot fail for the reason it names, carrying a comment that tells the next reader
it can. That is worse than no test — it is the thing someone points at when asked whether the
hub-scoping is covered.

Simpler form: delete it (`:93-105` subsumes it), or make it do what its comment says — start
`state` at `"ready"`, assert `"none"`, then flip `hubId` *and* `state` to `"connecting"` and assert
`"wall"`. Which is exactly `:93-105`, so deleting is the honest answer. −13 lines.

The same check on the other three new tests: `useRenderClient: a retry's not-yet-ready replacement`
(`:118-143`) and `a hub change drops the previous hub's client` (`:145-159`) both fail without their
production change, and `useReconnectRecovery` (`:169-181`) fails without the edge check. Those three
are sound.

---

## 2. HubSettings declares the same recovery callback twice, eleven lines apart — follow-up

`mobile-native/src/HubSettingsScreen.tsx:167-182`.

```tsx
useFocusEffect(
  useCallback(() => {
    void model.getState().refresh();
    void upgrade.reconcileAfterReconnect();
  }, [model, upgrade]),
);
// …
const recoverAfterReconnect = useCallback(() => {
  void model.getState().refresh();
  void upgrade.reconcileAfterReconnect();
}, [model, upgrade]);
useReconnectRecovery(connectionState, recoverAfterReconnect);
```

Byte-identical bodies, identical deps, and the second one's comment even says "the way
`useFocusEffect` above already does once on focus". Two `useCallback`s where one serves both, and
two places to edit the day the recovery grows a third call.

Simpler form:

```tsx
const reload = useCallback(() => {
  void model.getState().refresh();
  void upgrade.reconcileAfterReconnect();
}, [model, upgrade]);
useFocusEffect(reload);
useReconnectRecovery(connectionState, reload);
```

−5 lines, one name for one rule ("reload everything this screen reads"), and `useFocusEffect` and
`useReconnectRecovery` visibly share it. Trivial enough that I would take it in-round rather than
queue it.

---

## 3. The `connectionChanged` wiring effect is copy-pasted, comment and all — follow-up (highest value here)

`mobile-native/src/PluginsScreen.tsx:131-143` and `MarketplaceBrowser.tsx:78-90`.

Both are the same four lines under the same ~11-line comment, differing only in which store they
name and one parenthetical about the catalog cache:

```tsx
// Tells the store which connection its list belongs to, on every transition …
// Declared BEFORE the mount effect: on mount, nothing has asked for the list yet, so this
// call's own "does anything want it" check is answered honestly before fetchPlugins() below …
useEffect(() => {
  model.connectionChanged(client, connectionState);
}, [model, client, connectionState]);
```

That is ~22 duplicated lines, and there is a *third* spelling of the same rule one file over:
`credentialStore.ts:36-42` wires its store in a **`useLayoutEffect`** — with a documented reason
(React runs a parent's layout effects before any child's passive effects, so the store knows its
client before the child's `start()` reads through it) — and pairs it with an unmount
`store.connectionChanged(null, "closed")` that neither new copy has. The web wires all three
extension stores from a single place, `cmd/evener-hub/frontend/src/stores/extensions.ts:104-106`.

Cost: three encodings of "tell this store which connection its data belongs to" inside one app, two
of them verbatim twins and one of them structurally different for reasons the twins do not mention.
The next store that needs it is a fourth copy, and the `useEffect`-vs-`useLayoutEffect` divergence
is currently an accident of who wrote which file rather than a decision anyone made.

Simpler form: one hook beside `useCredentialStore`, e.g.

```ts
/** Tells a lifecycle-backed store which connection its data belongs to … */
export function useStoreConnection(
  store: { connectionChanged(client: ConversationClientLike | null, state: ConnectionState): void },
  client: ConversationClientLike,
  state: ConnectionState,
): void
```

called three times (plugins, marketplaces, credentials), with the ordering rationale and the unmount
`"closed"` written once. ~20 lines added, ~30 removed, and the layout-vs-passive question gets
decided once. The missing unmount call in the two new copies is RoboRev's to rule on; I mention it
only because the shared hook is where the answer belongs either way.

---

## 4. `reconnected()` is the fifth hand-written copy of the "became ready" edge — follow-up

`mobile-native/src/connectionDisplay.ts:113-118`.

```ts
return current === "ready" && previous !== "ready";
```

The same edge is already written out, inline, in:

- `appwire-client/typescript/state/credentials/instances.ts:1208` —
  `client && state === "ready" && (clientChanged || previous.state !== "ready")`
- `cmd/evener-hub/frontend/src/stores/agentsDoc.ts:143-148` — same expression, guarding a re-fetch
  of a fetch-once document after a drop (the closest analogue to what this PR does for hub overview)
- `cmd/evener-hub/frontend/src/notifications/index.ts:138-140` — `initNavigation` on the edge
- `cmd/evener-hub/frontend/src/notifications/index.ts:172-180` — the attention re-baseline, with a
  comment explaining (kata p5w9) that reading a *republish* of `"ready"` as a reconnect was a bug

Cost: five places that must agree on what counts as a connection event, four of which also fold in
the "…or the client identity changed" half and one of which (this PR's) deliberately does not. The
kata p5w9 note in `notifications/index.ts` is the record of what getting this edge wrong costs.

Simpler form: the predicate belongs in `appwire-client/typescript/state/connection`, next to
`createConnectionStore`, as the pair the hosts actually need —
`becameReady(previous, current)` and `reconnected(previous, current, { clientChanged })` — with
`reconnected()` here re-exported rather than re-derived. That is a package-surface change touching
the web, so it is a D-row follow-up (~60 lines), not something to bolt onto this PR.

---

## 5. The `hubId` scope-reset block is written twice in the same file, and both hooks are always called together — follow-up

`mobile-native/src/connectionDisplay.ts:53-57` and `:101-105`:

```ts
const scope = useRef(hubId);
if (scope.current !== hubId) {
  scope.current = hubId;
  everReady.current = false;          // lastClient.current = null;
}
```

Identical bookkeeping, and at both real call sites the two hooks are invoked with the same triple on
adjacent lines (`PluginsScreen.tsx:63`+`:71`, `HubSettingsScreen.tsx:44`+`:48`), each under its own
copy of a "Scoped to the hub: see `useRenderClient`'s own doc" comment.

Cost: two refs, two reset blocks, two call-site params and two comments for one fact — "this screen
instance has moved to a different hub, forget everything". A third piece of per-hub render state
(the PR's own trajectory suggests there will be one) is a third copy.

Simpler form, smallest first:

```ts
/** A ref that resets to `initial` whenever `scope` changes … */
function useScopedRef<T>(scope: string, initial: T): MutableRefObject<T>
```

used by both hooks — or, since the two are never used apart on the screens that need both, one
`useHubConnectionView(hubId, client, state, fatal): { display, renderClient }` that keeps a single
scope ref, which also collapses the duplicated call-site comments. ~15 lines net.

(`ProvidersScreen.tsx:44` calls only `useConnectionDisplay`, since its credential store survives a
flap on its own — a combined hook would hand it a `renderClient` it ignores, which is why I would
accept the smaller `useScopedRef` version too.)

---

## 6. `useReconnectRecovery` vs a store's own `connectionChanged`: the hook is a symptom, not the duplication — follow-up

The brief asks whether `useReconnectRecovery` duplicates what `connectionChanged` already does, and
whether HubSettings' store should grow `connectionChanged` instead. It does not duplicate it — it
*substitutes* for it, and that is the asymmetry:

| store on these screens | recovers via |
|---|---|
| plugins, marketplaces | `connectionChanged` → `createStoreLifecycle`'s own `wantsList`/away bookkeeping (`state/extensions/storeLifecycle.ts:12-17`) |
| credentials | `connectionChanged` → `state/credentials/instances.ts:1208` |
| hub overview | **this PR's host-side hook** (`hubOverview.ts` has no `connectionChanged` at all) |

So the native app now has two mechanisms for one rule ("a list something has already read is read
again once the connection is ready again"), and the web has a third for the same store shape
(`stores/agentsDoc.ts:143-148` hand-rolls the subscription for its own fetch-once document).

The right end state is `createHubOverviewStore` growing `connectionChanged(client, state)` with the
lifecycle's own rule — refresh iff `data !== null || error !== null || loading` (the exact analogue
of `wantsList`) — after which `useReconnectRecovery` and `reconnected` both delete themselves from
`connectionDisplay.ts` and the web's `agentsDoc` subscription can follow. Note that this does not
contradict `hubOverview.ts:12-13` ("no push-driven invalidation exists, so callers decide when to
fetch() or refresh()"): that paragraph is about the absence of a *notification*, and a reconnect is
not a notification — `storeLifecycle.ts:12-15` makes exactly that distinction ("the notification is
not a recovery path and the reconnect is").

I am **not** asking for it in this PR: it is package surface plus a web call site, i.e. its own row,
and the hook is the correct local move for landing D2a. ~50 lines when it happens. What I would do
here is one sentence in `useReconnectRecovery`'s doc saying the hook exists because this one store
has no `connectionChanged` *yet*, so the next reader does not take it as the native pattern and wire
a fourth store through it.

---

## 7. `useRenderClient`'s adopt-when-ready rule does **not** belong in `createConnectionStore` — no finding

Checked, because the brief asks. `createConnectionStore`'s `client` key is deliberately "the client
this host is wired to *now*", written the instant a host has one: the core registers the
`onStateChange` listener **before** publishing the swap, specifically so no transition is lost in the
gap (`appwire-client/typescript/state/connection/core.ts`, "Register before publishing"). Making the
store withhold a client until it reaches `"ready"` would break that — it is the store's job to
observe the not-yet-ready client, which is how `state` becomes `"ready"` in the first place.

"Which client is safe to hand a *store*" is a separate, host-render question, and both hosts already
answer it outside the connection store: the web adopts into `ClientProvider` only after
`await fresh.connect()` resolves (`shell/ConnectionBanner.tsx:173-176`, `onClientReplaced`), native
now answers it with `useRenderClient`. Those two are the same rule at the same altitude — a host's
render/context layer — so the shared part is only the predicate (`state === "ready"`, finding 4 of
#1915's review). `useRenderClient` is in the right place.

---

## 8. Efficiency: one note, no finding

- `useConnectionDisplay` / `useRenderClient` stay pure ref mutations during render: no effect, no
  subscription, no extra render, nothing to tear down.
- The two new `connectionChanged` effects re-run on every `connectionState` transition
  (idle → connecting → ready), which is the lifecycle's contract, not churn — `storeLifecycle.ts`'s
  own table documents the two-call "named before dialled" sequence and latches across it.
- No store is created or rebuilt by anything this PR adds; `createPluginsStore`,
  `createMarketplacesStore`, `createHubOverviewStore` and `createHubUpgradeController` stay in their
  existing `useMemo`s keyed on `client`, and `connectionState` is not a key of any of them — so a
  flap re-wires rather than rebuilds. That is the point of the row, and it holds.
- `useReconnectRecovery`'s effect lists `onReconnect` (`connectionDisplay.ts:130-134`), so an
  unstable callback re-runs it every render. Harmless today — the body re-checks
  `reconnected(previous.current, state)` and fires nothing when `state` has not moved, and
  HubSettings passes a `useCallback` — but the doc should say the callback must be stable, since the
  next caller will not know the effect depends on it. One comment line.
- `renderNative.testkit.tsx:61-78`'s `FlatList` stub renders every row with no windowing; fine for a
  one-plugin fixture, worth remembering before anyone points a 500-row fixture at it.

---

## 9. Test fixture duplication now has two files proving it — follow-up

`PluginsScreen.test.tsx:16-19` + `:51-62` and `:84-95` repeat, verbatim from
`ProvidersScreen.test.tsx:16-21` + `:42-53`: the `vi.hoisted` harness, the three `vi.mock` calls, the
`{ activeProfile, client, state: "ready", fatal: false, retry }` object, the
`as unknown as ComponentProps<typeof Screen>` props cast, and the two-step flap
(`{ ...harness.connection, state: "reconnecting" }` → `act(() => tree.update(…))` → `"ready"`)
four more times across the two files.

It also adds `pluginsClient` (`:25-39`) beside the testkit's `scriptedClient`
(`renderNative.testkit.tsx:105`), which does the same recording-and-answering job but is hard-typed
to `InstanceListResponse`.

Cost: the connection contract grew one field in #1915 (`fatal`) and that edited every fixture in the
file; it now edits both files. The flap sequence — the exact thing this row is about — is written
six times and asserted differently each time.

Simpler form, in `renderNative.testkit.tsx` (already the shared home): `readyConnection(client)`,
`hubRouteProps(hubId)`, `recordingClient(responsesByMethod)` replacing/generalizing
`scriptedClient`, and `flapTo(tree, element, harness, state)` for the transition. ~30 lines in the
testkit, ~70 removed across the two screen tests. This is the same finding as #1915's §9, upgraded:
with a second caller it is no longer speculative.

---

## Verdict — fix before merge (1 must-fix, 8 follow-up)

One thing I would not merge past, and it is in the tests rather than the product code:
`connectionDisplay.test.ts:79-91` is named "a hub change resets `everReady`" and proves nothing of
the kind — its `state` is a `const "connecting"`, so `everReady` is never set, and deleting the
production reset block the test exists to cover leaves it green. Its comment tells the next reader it
does the opposite ("prove the reset by getting hub-1 ready first"), and the following test already
does the real work, so deleting the thirteen lines is the honest fix. Everything else in this PR is
sound and at the right altitude: the reconnect recovery is genuinely the gap #1915's review found,
`useRenderClient`'s adopt-when-ready rule belongs exactly where it is rather than in
`createConnectionStore` (§7), no store is rebuilt by a flap, and the `FlatList` testkit stub is the
minimum needed to mount the screen for real. The follow-ups worth queuing, in order of value: one
`useStoreConnection` hook for the three copies of the `connectionChanged` wiring, two of them
verbatim twins and one spelled with `useLayoutEffect` plus an unmount close (§3); the test-fixture
helpers now that two files need them (§9); `createHubOverviewStore` growing its own
`connectionChanged` so `useReconnectRecovery` and `reconnected` can delete themselves and the web's
`agentsDoc` subscription can follow (§6); lifting the "became ready" edge into the package, where
four other hand-written copies are waiting for it (§4); the twice-written `hubId` scope reset (§5);
and HubSettings' duplicate recovery callback, which is a five-line deletion I would simply take
in-round (§2).
