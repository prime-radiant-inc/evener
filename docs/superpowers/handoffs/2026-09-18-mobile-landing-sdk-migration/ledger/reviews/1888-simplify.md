# /simplify — PR #1888 (D28 D1: ConnectionProvider over the package connection store)

Head `dbdc2265bd86fc842d625915717c35b056735bfb`, base `main`. Reuse / simplification /
efficiency / altitude only; correctness was RoboRev's pass. Scope: the three files in
`git diff -M origin/main...pr-1888-review` (ConnectionProvider.tsx, hubConnection.ts,
hubConnection.test.tsx).

## Must-fix before merge

None. Checked the two things that would qualify:

- **No duplicated helper.** The house idiom for a native adapter over a package
  store is `useState(create…)` + `useSyncExternalStore(store.subscribe, store.getState)`
  (`mobile-native/src/credentialStore.ts:35`, `ActivitySheet.tsx:47`,
  `CommandCompletion.tsx:37`, `HubSettingsScreen.tsx:129`); `hubConnection.ts:60-61`
  is exactly that. There is no shared `useFrameworkFreeStore` wrapper in the repo to
  reuse, and no existing target/generation helper (`git grep sameTarget|targetKey|
  generationOf` finds only per-file comparators). The test reuses
  `renderHook` from `renderNative.testkit.tsx:107` rather than rolling its own.
- **No dead code left in ConnectionProvider.tsx.** The old effect and the `session` /
  `state` `useState` pairs are gone; `createHubClient`, `WebSocketLike` and
  `connectionRecovery` imports moved out with them; the surviving `AppwireClient` /
  `ConnectionState` type imports are still used by the `Connection` interface
  (`ConnectionProvider.tsx:34-35`). `error`, `attempt`, `foreground`, `selected` all
  still have readers.

## Follow-up

### 1. `mobile-native/src/hubConnection.ts:18-36` — a 4-field struct plus a comparator where one string key does the job

`ConnectionTarget` + `sameTarget` exist only to answer `===`. Cost: 19 lines (interface,
comparator, their doc comment) plus a fresh object allocated twice per render
(`:99`, `:136`) for a value that is never read field-by-field. Simpler form: one key,

```ts
const targetKey = JSON.stringify([activeId, activeOrigin, foreground, attempt]);
const connectedFor = useRef<string | undefined>(undefined);
// …
connectedFor.current = targetKey;            // in the effect, beside store.setState({ client })
const client = connectedFor.current === targetKey ? coreState.client : null;
```

`JSON.stringify` of the tuple rather than a `|`-joined template so no origin or id can
forge a collision. The explanatory comment at `:62-73` is worth keeping verbatim; it is
the struct and the comparator that are the ceremony, not the idea.

### 2. `mobile-native/src/hubConnection.ts:138` — `foreground &&` in the client gate can never be the deciding term

`connectedFor.current` is written only at `:99`, which is downstream of the effect's
`if (!activeId || !activeOrigin || !foreground) return;` at `:83`, so every recorded
target has `foreground: true`. `sameTarget` already compares `foreground`, so a render
with `foreground === false` fails on that field regardless. Cost: a reader has to prove
this to themselves to know whether the guard is complete, and the redundancy suggests
`sameTarget` is *not* the whole fence when it is. Simpler form: drop `foreground &&` and
leave `sameTarget(connectedFor.current, target)` alone. (The `activeId && foreground`
in the `state` fallback at `:145` is load-bearing — it reproduces the pre-PR
`activeProfile && foreground ? "connecting" : "idle"` — and stays.)

### 3. `mobile-native/src/hubConnection.ts:80-81` vs `:120-129` — a connection is retired in two places

The body's `store.setState({ client: null })` at `:80` is the same line the cleanup runs
at `:128`, and React always runs the cleanup before the next effect body; on the very
first run the store is freshly constructed (`state: "idle"`, `client: null`) so there is
nothing to reset. The asymmetry is `connectedFor.current = undefined`, which only the
body does. Cost: one extra store publish per effect run — `frameworkFreeStore` publishes
a fresh snapshot object on every `setState`, same value or not (see the note in
`cmd/evener-hub/frontend/src/stores/connection.ts:39-44`), so this is a `coreState`
identity change and a ConnectionProvider re-render for a write that changes nothing —
plus two places to keep in step when teardown changes. Simpler form: move
`connectedFor.current = undefined` into the cleanup beside `:128` and delete `:80-81`.
`setError(null)` at `:82` must stay in the body (it is the per-attempt reset the old
effect did).

### 4. `mobile-native/src/hubConnection.ts:98` — two writes where one does, and the store briefly reports a state the connection is never in

`store.setState({ client: connection })` takes the core's swap path, which publishes
`state: client.state` (`appwire-client/typescript/state/connection/core.ts:145`) — and
the fresh client has not been dialed yet, so that is `"idle"`, overwriting the
`"connecting"` written at `:84` until `connection.connect()` at `:109` transitions it
back. The core deliberately lets a caller's own partial win in the same write
(core.ts:136-145), so:

```ts
store.setState({ client: connection, state: "connecting" });
```

Cost today: five publishes per attempt (cleanup null, body null, `"connecting"`,
swap-to-`"idle"`, mirror-to-`"connecting"`) where three suffice with this and #3, and
one of them carries a `ConnectionState` this connection is never actually in. No visible
flash — React batches the promise-callback writes and `useSyncExternalStore` reads
`getState` at render — so this is publish churn and a false intermediate for any
non-React subscriber, not a UI bug.

### 5. `mobile-native/src/hubConnection.ts:139` — the `as AppwireClient | null` cast is avoidable

The cast exists only because the store types its holder as `AppwireClientLike`, while the
hook promises `AppwireClient`. If the ref carries the client it recorded — `useRef<{ key:
string; client: AppwireClient } | undefined>`, written in the same step at `:98-99` and
cleared in the cleanup per #3 — the render reads the concretely-typed instance it put
there and the cast goes, along with the comment at `:131` that has to vouch for it
("The store only ever holds the AppwireClient instances created above"). The store keeps
holding the client either way; that is what its swap-safety mirror needs. Stacks with
#1 and #3.

### 6. `mobile-native/src/hubConnection.test.tsx:110-153` and `:154-190` — two near-identical race tests differing in one mutation

Both mount the hook with a render log, settle, succeed, swap `harness.client`, mutate one
input (`activeId`/`activeOrigin` vs `attempt`), rerender, and assert no render tagged with
the new input hands back `first`. Cost: ~44 duplicated lines, and the `const setError =
vi.fn()` / `const repository = { token: … }` / 6-argument mount boilerplate is copied in
all six tests (`:65-68`, `:77-89`, `:113-128`, `:157-175`, `:195-206`, `:227-237`). Simpler
form: a local `mount(overrides)` helper over the six arguments, plus `it.each` over
`[{ label: "a switched hub", mutate }, { label: "a bumped retry", mutate }]` for the two
race tests. `:192` (teardown order: listener count 0 and closed before the new attempt's
body) and `:225` (unmount releases the listener) keep their own assertions and stay
separate. No coverage lost — the two parametrized cases are the same two properties.

### 7. `mobile-native/src/hubConnection.test.tsx:20-58` — `FakeHubClient` overlaps the package's `FakeClient`, but not enough to reuse today

`@evener/appwire-client/testing/fakeClient`'s `FakeClient` already models `state`,
`terminalReason`, `onStateChange` and `emitStateChange`, and mobile-native already imports
from that subpath (`navigationPages.test.ts:8`, `providerInstances.test.ts:10`,
`keybindingRecovery.test.ts:8`; `metroResolver.test.ts:45` even pins `fakeClient`'s
resolution). Three real gaps stop a straight reuse: `FakeClient` has no `close()` (and
`AppwireClientLike` does not declare one, while this hook calls `connection?.close()`), it
exposes no listener count (the observable the unmount test at `:225` needs — the leak this
PR found), and its `connect()` resolves an `InitializeResponse` immediately rather than
staying pending until the test settles it, which is what `succeed()`/`fail()` drive here.
So the local double is justified; the follow-up is the *package* side — a `listenerCount`
getter and a `close()` on `FakeClient` would let native delete ~35 lines and give the web
the same listener assertion. Low value, do not block on it.

## Altitude: should `createConnectionStore` carry the target key?

No, and I'd not file it as a row. Three reasons:

1. The core's charter is explicit that it knows client identity and wire state only
   (`state/connection/core.ts:1-14`); hub profile id, origin, app foreground and a retry
   counter are native product concepts, not connection-layer ones.
2. The web has no equivalent skew to solve. It holds its client in a React state slot
   (`shell/AppShell.tsx:309-311`, `createClientSlot`/`adoptClientSlot`) and consumers read
   the client from that slot through context, not from the store; the store is read for
   `state`/`serverInfo`. Where the web does fence a late write it uses a plain identity
   check against the store's current client (`AppShell.tsx:333`), the same class of fence
   the core itself uses at `core.ts:132`. A target key would be a one-host abstraction.
3. The generalizable half — "a replaced client must not keep a live subscription, and an
   event from a client that is no longer wired reaches nothing" — is *already* in the
   package, and this PR's value is that native now gets it instead of hand-rolling it.
   The `connectedFor` ref is the host-side remainder: which of *its own* input generations
   the store's client belongs to. Revisit only if the web ever gains hub switching.

## Considered and not filed

- Replacing the hook's own `client.onStateChange` listener (`:101-108`, whose only job is
  `setError`) with a `store.subscribe` listener reading `state` transitions, so each client
  carries one listener instead of two (the core's mirror plus this one). It would drop the
  `unsubscribe`/`currentConnection` bookkeeping, but it moves `setError` from the client's
  dispatch to the store's publish and makes the terminal reason a `store.getState().client`
  read — behaviour-adjacent for a listener that costs nothing measurable. Left alone.
- The `setError` parameter (the caller's shared error slot) rather than an error in the
  hook's own return. That is deliberate and documented at `:47-50`: the saved-hubs-load
  failure writes the same slot, so pulling it into the hook would change what
  `useConnection().error` means.
- `ConnectionProvider.tsx:28` and `TranscriptImages.tsx:18` each construct their own
  `new HubProfiles(SecureStore)`. Pre-existing, untouched by this PR, out of scope here.

## Verdict

**Follow-up only — 7 findings, 0 must-fix.** The extraction is the right shape: it adopts
the package's client-swap safety instead of re-deriving it, uses the same
`useState(create…)` + `useSyncExternalStore` binding the other native store adapters use,
narrows `repository` to a one-method port, leaves no dead code or orphaned imports behind
in `ConnectionProvider.tsx`, keeps `Connection.client`/`state`/`error` byte-identical for
consumers, and pays for itself by finding a real listener leak on unmount. What is left is
all local trimming of the new hook: four of the seven (#1 key instead of struct + comparator,
#2 the unreachable `foreground &&`, #3 teardown in one place instead of two, #5 the ref
carrying its own client so the `AppwireClientLike` cast goes) collapse into one ~25-line
pass over `hubConnection.ts` that also removes a store publish per effect run; #4 merges the
client write with its `"connecting"` state so the store never reports the pre-dial `"idle"`;
#6 folds two copy-paste race tests into one `it.each` and a mount helper; #7 is a package-side
`FakeClient` nicety (add `close()` and a listener count) that would let native drop its own
double later. None of it changes intended behaviour and none of it needs to hold the merge.
