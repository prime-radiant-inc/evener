# /simplify — PR #1915 (D2: screens survive a connection flap behind a banner)

Head reviewed: `a48f7e0c85a37e960db059f88801b4f696b4feab`, own diff vs `origin/main` (`ead2caba7`).
Quality only — reuse, simplification, efficiency, altitude. Correctness is RoboRev's. Nothing below
changes intended behaviour: the wall/banner split stands as Jesse's ruling 7 has it, and no finding
proposes a "refresh to see the latest" affordance (the native rule is auto-refresh on reconnect).

---

## 1. `fatal` re-derives what `connectionFailure()` already classifies, on the same line — must-fix-before-merge

`mobile-native/src/hubConnection.ts:108-111` and `:118-121`.

The PR calls the existing classifier and then re-asks it the same question by hand:

```ts
setError(connectionFailure(currentConnection.terminalReason).message);
setFatal(currentConnection.terminalReason === "protocol");
```

`connectionFailure` (`mobile-native/src/connectionRecovery.ts:5-21`) exists precisely to turn a
`TerminalReason` into a `{ kind: "protocol" | "transport"; message }` verdict, and the new code
throws `kind` away and re-derives it from the raw reason — twice, at the `onStateChange` site and
again in the `.catch`. The PR's own doc comment on `HubConnection.fatal:14-17` even points at
`connectionRecovery.ts`'s `ConnectionFailureKind` as the definition, while the code never reads it.

Cost: "which closes are unrecoverable" is now written in two places that must be edited together.
The day a second unrecoverable reason appears (an auth rejection the hub will never accept, a
revoked token), `connectionFailure` grows a third kind and `fatal` silently keeps saying `false` —
exactly the class of asymmetry that eats review rounds.

Simpler form, at both sites:

```ts
const failure = connectionFailure(currentConnection.terminalReason);
setError(failure.message);
setFatal(failure.kind === "protocol");
```

Better still, since `fatal` means "no retry can fix this" and that is a property of the kind, not of
the call site: put it on the verdict — `connectionFailure` returns `{ kind, message, fatal }` (or
export `isFatalFailure(kind)`) and both sites read it. Either way the `=== "protocol"` literal
disappears from `hubConnection.ts` entirely. ~6 lines.

---

## 2. `whenReady` wrapped around a handler whose only effect goes through `act()`/`confirm()` is a guard that can never fire — must-fix-before-merge

`mobile-native/src/ProvidersScreen.tsx:383`, `:471`, `:482`, `:494`, `:506`.

The PR adds three gates to the same decision, and at five sites two of them are the same gate:

- `act()` bails at `:171` (`if (!ready) return;`)
- `confirm()` bails at `:191` (`if (!ready) return;`)
- and the `onPress` is *also* wrapped in `whenReady(ready, …)`.

At those five sites the wrapped body does nothing but call `act()` or `confirm()`:

| line | handler body | already gated by |
|---|---|---|
| 383 | `const value = key.trim(); void act(…)` | `act` |
| 471 | `void act(() => model.setDefault(instance.name))` | `act` |
| 482 | `confirm("Clear stored key?", …)` | `confirm` |
| 494 | `confirm("Clear active credentials?", …)` | `confirm` |
| 506 | `confirm("Remove provider instance?", …)` | `confirm` |

So `whenReady`'s false branch is unreachable in effect: whenever it would suppress the call, the
callee returns immediately anyway, with no state touched in between (the PR deliberately moved the
`setKey("")` clear onto `act()`'s success path at `:383-392`, which is what makes the body free of
side effects).

Cost: a reader deciding "can this press do anything while disconnected?" has to check three layers,
and the next mutation added to this screen has five precedents showing the belt-and-braces pair and
five showing something else — the file already has four spellings of one gate (`disabled={!ready}`,
`whenReady(ready, …)`, `if (ready) void …`, `if (!ready) return`).

Simpler form: drop `whenReady` at those five sites and leave the two real layers — `disabled` for
the affordance, `act()`/`confirm()` for the programmatic guard that cannot be forgotten. Keep
`whenReady` exactly where the handler reaches the model or local state without going through them
(`:223` add-instance, `:419` `testCredentials`, `:434` edit, `:455`/`:463` credential editors).
−5 lines, and the surviving `whenReady` calls then mean something: "this one is not covered by
`act`".

---

## 3. `runGatedMutation`'s new `ready` refusal is indistinguishable from a busy refusal, so every caller pre-checks `ready` anyway — follow-up

`mobile-native/src/pluginMutationGate.ts:81-86`, callers at `MarketplaceBrowser.tsx:120`, `:132`,
`:136`, `:386` and `PluginsScreen.tsx:152`.

`runGatedMutation(gate, ready, action)` folds "disconnected" into the gate's existing `"refused"`
outcome, and both call sites map `"refused"` to one string:
`PLUGIN_MUTATION_BUSY = "Another change is still running. Wait for it to finish."`
(`pluginMutationGate.ts:33`). A refusal caused by a dropped connection therefore reports a
concurrent write that does not exist — which is why `refresh()` and `remove()`
(`MarketplaceBrowser.tsx:132`, `:136`) have to pre-check `!ready` themselves, and `submit()`
(`:373-386`) checks `disabled` first. The helper's stated value ("a caller that forgets a
`disabled`/`whenReady` check still cannot reach the wire") is already provided by AppWire rejecting
the request, so what the parameter actually buys is one wrong error string.

Cost: a third state hidden inside a two-state outcome; the guard that is supposed to be the
backstop is the one path with misleading copy, so callers keep their own copies of the check.

Simpler form, either direction:

- keep the backstop and make it honest — `GatedMutationOutcome` grows `"disconnected"`, the two
  `act()` helpers show a disconnect string for it, and the pre-checks in `refresh`/`remove`/`submit`
  go away; or
- take `ready` back out of `runGatedMutation` and let `disabled` + `whenReady` (+ AppWire's own
  rejection) be the gate, which is what the Providers screen already does with `act()`.

The first changes copy, so it is a behaviour call for the row owner rather than something I would
apply unilaterally. Either way the positional boolean in the middle of a three-argument call
(`runGatedMutation(gate, true, async () => …)` in `pluginMutationGate.test.ts:89`, `:99`, `:106`)
reads as a puzzle at every test call site and would be better named.

---

## 4. The wall/banner preamble is copy-pasted across the three screens — follow-up

`PluginsScreen.tsx:63-85`, `HubSettingsScreen.tsx:38-58`, `ProvidersScreen.tsx:44-81`.

All three now open with the same seven-step shape, differing only in one sentence of copy and the
child they wrap:

```tsx
const { activeProfile, client, state, fatal, retry } = useConnection();
const display = useConnectionDisplay(state, fatal);
const renderClient = useRenderClient(client);              // Providers omits this one
if (activeProfile?.id !== route.params.hubId) return <Copy>This hub is no longer selected. …</Copy>;
if (display === "wall" || !renderClient) return (
  <View style={{ padding: 20 }}>
    <Copy>Connect to {activeProfile.name} to manage plugins.</Copy>   // "to view hub settings." / "to manage providers."
    <Action onPress={retry}>Reconnect</Action>
  </View>
);
return <>{display === "banner" ? <ConnectionStatus /> : null}<Plugins … /></>;
```

Three copies of the wall JSX, three copies of the `display === "banner" ? … : null` conditional,
three copies of the "no longer selected" string. The variation is one noun phrase.

Cost: the fourth ready-only screen copies it a fourth time, and any later change to the wall (the
inset, a second action, the fatal-specific copy the `fatal` flag makes possible) has three edit
sites that can drift. The PR already found the two extractions worth making (`ConnectionStatus`,
`connectionDisplay.ts`); this is the third and last one.

Simpler form: one wrapper in `connectionDisplay`'s neighbourhood, e.g.

```tsx
<HubScreenConnection hubId={route.params.hubId} prompt="to manage plugins.">
  {({ client, ready }) => <Plugins client={client} … />}
</HubScreenConnection>
```

or, keeping it a hook, `useHubScreenConnection(route.params.hubId)` returning
`{ activeProfile, display, renderClient, ready, retry }` plus a `<ConnectionWall>`/`<ConnectionGate>`
component for the two early returns. ~50 lines added, ~60 removed across the three screens.

---

## 5. `ready` in the sign-in effect's dependency array is derived from `state`, which is already there — follow-up

`mobile-native/src/ProvidersScreen.tsx:66` (`}, [signIn, activeProfile?.id, client, state, ready]);`).

`ready` is `isReady(state)` computed at `:45`, so it cannot change without `state` changing. Listing
both says the effect has two independent connection inputs when it has one.

Cost: nothing at runtime; it is a reader trap and the kind of dep-array padding that later hides a
genuinely missing dep. Drop `ready` from the array (the body's `ready ? client : null` still reads
the current value). 1 line.

---

## 6. Efficiency: nothing to fix

Checked the four hazards the brief names:

- `useConnectionDisplay` / `useRenderClient` (`connectionDisplay.ts:41-52`, `:71-78`) are ref
  mutations during render with no effect and no subscription — no extra render, nothing to tear
  down. The `ForkScreen.tsx` `owner.current` precedent the doc cites is real.
- No new subscription is created anywhere in this PR; `ConnectionStatus` consumes the context the
  screens already consume.
- `useState(false)`/`setFatal` in `hubConnection.ts` adds at most one extra render per close, and
  only on a transition that already re-renders through `setError`.
- No store is created per render: `createHubOverviewStore`, `createPluginsStore` and
  `createMarketplacesStore` stay inside their existing `useMemo`s, and the new `ready`/`display`
  values are not in those memo keys, so a flap does not rebuild a store.

One note rather than a finding: `ConnectionStatus` is rendered as a *sibling above* the child
(`<>{banner}<Plugins/></>`), not floated over it, so revealing it shifts the list down by the
reserved 44/48pt. The reserved-height comment it inherited from `screens.tsx`
(`ConnectionStatus.tsx:16-18`, "so offscreen header changes do not shift the conversation underneath
the reader") is now the rationale of only one of its four call sites. Layout is a product call, not
a simplification; the comment should name both uses once the component is shared.

---

## 7. Dead code check: clean

`screens.tsx`'s `ConnectionStatus` was moved out whole, and every symbol its body used is still used
by the rest of the file (`Platform` ×19, `useConnection` ×3, `ErrorMessage` ×6, `styles.row`/
`styles.fill` ×6/×8), so no import was left orphaned. The extracted-file comment explaining why it
is not in `screens.tsx` (the `@react-navigation/elements` graph vitest cannot load) is the right
note to leave.

---

## 8. Altitude: one connection rule per host, twice — follow-up (file it, do not lift it here)

Three related decisions now exist in both hosts, phrased independently:

| rule | web | native (this PR) |
|---|---|---|
| a protocol close cannot be retried away | `ConnectionBanner.tsx:146-150` (`client?.terminalReason === "protocol"` → `closedReason: "protocol"`, Reload not Retry) | `hubConnection.ts:111`/`:121` `fatal`, `connectionRecovery.ts:9` `kind: "protocol"` |
| which states deserve a banner | `ConnectionBanner.tsx:79` `ATTENTION_STATES = {reconnecting, closed}`, plus a 10s reveal delay | `connectionDisplay.ts:27-35` any non-`ready` state, no delay |
| "may a request go out right now" | `state === "ready"` inline at ~6 sites (`useConnectedEffect.ts:44`, `useProviderSetup.ts:36,66`, `notifications/index.ts:134`, …) | `connectionDisplay.ts:17` `isReady(state)` |

The package already owns the third rule in one shape — `isClientReady(client)` at
`appwire-client/typescript/state/mutation/outbox.ts:51`, whose comment calls itself "the one
readiness notion this subpath owns". Native now has a second one, keyed on `ConnectionState` rather
than a client. That pair (`isClientReady(client)` / `isReady(state)`) is the natural seed for
`appwire-client/typescript/state/connection`: a `readiness.ts` exporting the state-shaped predicate
and the fatal-close classification, with `isClientReady` defined over it.

What I would *not* lift: `connectionDisplay` itself. The two hosts genuinely differ — the web floats
a banner over a shell that never unmounts and has a reveal delay, native chooses between replacing
the screen and bannering it based on per-mount history. The shared core is the two predicates
("is this close fatal", "is this state ready"), not the display decision, and lifting more than that
would be inventing a common rule rather than finding one.

Scope: a D-row-shaped follow-up (package predicate + both hosts pointing at it, ~80 lines), not
something to bolt onto a PR at its first review round. The reveal-delay asymmetry (a sub-second
native flap flashes the banner where the web stays silent for 10s) is a product question for Jesse,
listed here only so it is not mistaken for an oversight.

---

## 9. Test fixture duplication — follow-up

`mobile-native/src/ProvidersScreen.test.tsx` repeats the same eight-line preamble in all five tests
(`:42-53`, `:66-77`, `:95-106`, `:124-135`, `:153-164`):

```ts
const hub = scriptedClient(rows);
harness.connection = { activeProfile: { id: "hub-1", name: "Work hub" }, client: hub.client,
                       state: "ready", fatal: false, retry: () => {} };
const props = { route: { params: { hubId: "hub-1" } } } as unknown as ComponentProps<typeof ProvidersScreen>;
```

and the flap itself — `harness.connection = { ...harness.connection, state: "reconnecting" };
await act(async () => { tree.update(<ProvidersScreen {...props} />); })` — four more times. The
`fatal: false` key had to be added to the one pre-existing fixture too, which is the tell: every new
field on the connection contract edits N fixtures.

Simpler form, in `renderNative.testkit.tsx` beside `render`/`renderHook` (it is already the shared
home for this): `readyConnection(client)` returning the connection object, `hubRouteProps(hubId)` for
the cast, and `transitionTo(tree, harness, element, state)` for the flap. ~25 lines in the testkit,
~60 removed from the two test files — and #1922's `PluginsScreen.test.tsx` is the third file about to
copy the same preamble, so the helper pays for itself immediately.

Related, smaller: `scriptedClient` (`renderNative.testkit.tsx:105`) is hard-typed to
`InstanceListResponse`, so #1922's `PluginsScreen.test.tsx` writes its own `pluginsClient` doing the
same recording-and-answering job. One `recordingClient(responsesByMethod)` would serve both.

`connectionDisplay.test.ts` has no fixture duplication worth touching — its per-case `let state`
plus `renderHook` is about as small as the transitions get.

---

## Verdict — fix before merge (2 must-fix, 7 follow-up)

Two things I would not merge past: `hubConnection.ts:108-121` re-derives `terminalReason ===
"protocol"` by hand at two sites on the same lines that already call `connectionFailure()`, the
existing helper whose whole job is that classification — so "which closes are unrecoverable" is now
two encodings of one rule inside one file, and the second one silently stops being right the day a
third failure kind appears (finding 1, ~6 lines); and five `whenReady(ready, …)` wrappers in
`ProvidersScreen.tsx` sit on handlers whose only effect runs through `act()`/`confirm()`, both of
which already return early when `!ready`, so the wrapper's suppressing branch can never change an
outcome — a dead guard, in a file that now spells one gate four different ways (finding 2, −5 lines).
Both are small, mechanical, and in the code this PR is adding. Everything else is genuinely good
work at the right altitude: `connectionDisplay.ts` is the extraction this row needed, the
`ConnectionStatus` move is clean with no orphaned imports, no store is rebuilt per render, and the
hooks add no subscriptions. The follow-ups worth queuing, in order of value: the three-way copy-paste
of the wall/banner preamble across the screens (4), the `runGatedMutation` refusal that reports
"another change is still running" for a disconnect (3), the test-fixture helper that #1922 is about
to need too (9), and the package-level pair of `isFatalClose`/`isReady` predicates both hosts should
share (8).
