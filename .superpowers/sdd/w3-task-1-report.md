# Wave 3 Task 1 report — shell skeleton

Branch `w3-shell`, off Task-1 tip `3bb0528e7`. 7 commits, purely additive except App.tsx's
rewire (26/13 +/- lines), zero touches to any forbidden path (verified via
`git diff --stat 3bb0528e7 HEAD` — every changed file is under `src/App.tsx`, `src/App.test.tsx`,
`src/shell/**`, or `src/panes/welcome/**`).

## Commits

| Commit | Unit |
|---|---|
| `64464097b` | pane registry (register/lookup/singleton) |
| `366916d2e` | routing — paneToURL/urlToPane/navigate |
| `43a55b4f9` | client context |
| `01e6d1d68` | connection banner |
| `7213bf565` | welcome pane |
| `16fd01dda` | AppShell |
| `7ce5cc43c` | App.tsx rewire (mount AppShell, demote dev harness to /dev/harness) |

## TDD evidence

Every unit followed red→green in the order above: test file written first, run to confirm
failure (module-resolution error — the file didn't exist yet, the expected "red" shape for a
brand-new module), then implementation added until green, before moving to the next unit. Two
cases where red caught something real rather than "not implemented yet":

- **App.test.tsx's new `/dev/harness` test passed against the OLD App.tsx** the first time I ran
  it — for the wrong reason (DevHarness rendered unconditionally at every path in the old code,
  so the route-gated assertion happened to be satisfied without the route actually existing yet).
  I strengthened the default-route test to also assert DevHarness's content is *absent* there
  (`queryByText(/connection:/i)).toBeNull()`), which does fail red against the old code, before
  implementing the rewire — otherwise the DevHarness-demotion behavior would have shipped with no
  test actually pinning it.
- **AppShell's real-client bootstrap would open a real jsdom WebSocket.** Building AppShell's
  effect the first way I wrote it (unconditionally calling `.connect()` on a client it
  constructs itself) would have made `App.test.tsx` — which renders `<App/>` → `<AppShell/>` with
  no injected client — dial a real socket under vitest the moment I wired `App.tsx` to mount
  `AppShell`. Caught this by inspection before it caused a hang/flake (traced the exact hazard
  `dev/DevHarness.tsx`'s own comment already documented), and added the same
  `import.meta.env.MODE === "test"` guard around the connect side effect specifically (not the
  whole effect — see AppShell section below for why the scoping matters).

Full suite: 609 tests across 45 files (previously ~522 across ~34 before this task — exact
pre-existing count not recorded, but the diff is purely additive: 87 new tests across 9 new test
files, 2 files' assertions changed in place). All 609 pass. Confirmed stable across three full
reruns (`npx vitest run` x3, both before and after committing) — identical 609/609 every time, no
flakiness.

```
npx vitest run       → EXIT=0  (609 passed, 45 files, x3 reruns)
npx tsc --noEmit      → EXIT=0  (no output)
npx eslint src         → EXIT=0  (no output)
npm run build          → EXIT=0  (tsc --noEmit && vite build; dist/ shows a separate
                                    Welcome-*.js chunk, confirming code-splitting; no
                                    gallery/harness chunk, confirming both dev-only
                                    routes stay out of the production bundle)
```

## Routing-approach justification: hand-rolled, not react-router

`shell/routing.ts` is pure string transforms (`paneToURL`/`urlToPane`) plus one browser-
integration helper (`navigate`: `pushState` + a synthetic `popstate` dispatch, since `pushState`
alone doesn't fire `popstate` and that's the one event AppShell needs to listen for to cover both
programmatic navigation and the browser's real back/forward buttons). AppShell reacts to path
changes with a small local `usePathname()` hook (state + a `popstate` listener), no router
component tree.

Chose this over `react-router` (present in `package.json` at `^8.2.0` — note the plan's Global
Constraints text says "v7"; the installed version is 8, worth a doc fix but out of scope here)
because Task 1's actual routing surface is: parse 7 known path shapes into a `{type, params}`
pair, format the reverse, and re-render on navigation. That's it — no nested routes, no loaders,
no data APIs, and (per the plan) no dockview host yet to route *into*. Standing up
`<BrowserRouter>`/`<Routes>` for that would be more code and more indirection than the ~90-line
hand-rolled module, with no capability gained yet. `paneToURL`/`urlToPane` are plain functions
with zero React/router dependency, so they don't lock Task 2+ into this choice either way —
whoever first needs real nested-route composition (Task 2's dockview host is the most likely
candidate, or Task 5's auth redirect handling) can introduce `react-router` at that point without
touching routing.ts's contract, or keep extending the hand-rolled approach if it still suffices.
I'd lean toward introducing the router only when something needs what it actually buys you
(e.g. Task 4's mobile stack-navigator back-stack) rather than by default.

## Design decisions worth flagging explicitly

- **`/credentials` is an inbound-only alias.** `urlToPane("/credentials")` resolves to
  `{type: "settings", params: {section: "credentials"}}` (Global Constraints lists it as its own
  top-level deep link, distinct from the generic `/settings/{section}` pattern), but
  `paneToURL("settings", {section: "credentials"})` always emits `/settings/credentials` — the
  app itself only ever constructs the canonical nested form; `/credentials` exists purely so old
  bookmarks/links keep resolving. Both directions are tested.
- **`doc` panes have no deep link yet.** `paneToURL("doc", …)` returns `null` unconditionally —
  the Global Constraints deep-link list doesn't include one, and doc panes are described
  elsewhere as opening contextually from a session. Revisit if/when a wave needs a standalone doc
  URL.
- **AppShell's routing awareness is deliberately minimal in Task 1.** It only distinguishes `/new`
  (shows the welcome pane with a "starting a new session isn't available yet" note, since spawn
  panes are Wave 6) from everything else (always shows the plain welcome pane). It does **not**
  yet render distinct content for `/s/{ref}`, `/thread/{ref}`, `/settings`, etc. — there's no
  dockview/pane-hosting machinery to route into until Task 2, and building speculative dispatch
  logic for those routes now risked guessing wrong about what Task 2 actually needs. `urlToPane`
  itself fully supports all seven deep links (tested both directions); only AppShell's *use* of
  the result is narrow.

## Concern: `AppwireClientLike` has no `connect()` — this materially shapes what AppShell's test
can prove

This is the most important thing to flag for whoever picks up Task 2+ or reviews this task.

`protocol/testing/fakeClient.ts`'s `AppwireClientLike` interface (which `stores/connection.ts`'s
`connect(client)` and my `AppShellProps.client` are both typed against, and which `FakeClient`
implements) exposes `request`/`onNotification`/`onReady`/`onStateChange`/`state` — **no
`connect()` method, and no way to read an `InitializeResponse`.** That's a deliberate Wave 1
design (fakes simulate readiness directly via their constructor or `emitStateChange`, not a real
handshake), documented in `stores/connection.ts`'s own comment on why `serverInfo` stays
`undefined` there ("populating it needs a path this task's locked interface doesn't provide —
e.g. whoever owns the real client.connect() promise setting it directly").

Consequence: **AppShell's "populates connectionStore.serverInfo from connect()'s
InitializeResponse" duty is only exercised by the real-`AppwireClient` code path, which
`AppShell.test.tsx` cannot drive** (no real sockets, per the task's own constraint, and
`FakeClient` structurally can't return an `InitializeResponse`). I implemented it correctly (the
`owned` branch in `AppShell.tsx` calls `.connect()` on the concrete `AppwireClient` it
constructed, then sets `serverInfo` from the resolved value) and it's exercised transitively by
`App.test.tsx` insofar as that code path runs without crashing — but the actual serverInfo
assignment has no unit-test coverage in this task. This isn't something Task 1 can fix without
touching `src/protocol/**` (forbidden here); flagging it rather than papering over it with a test
that doesn't really prove the thing.

A related, separate finding: **`AppwireClient.connect()` is single-shot and never resets**
(`connectPromise` is cached for the object's whole lifetime once set, whether it resolved or
rejected). That means there is no public-API way to make a `"closed"` (or `"reconnecting"`)
client retry by calling `.connect()` again — it just replays the original result. `ConnectionBanner`'s
retry button therefore calls `window.location.reload()` unconditionally rather than
`client.connect()`, which is the only thing actually guaranteed to hand the app a fresh client.
Documented inline in `ConnectionBanner.tsx`; worth a look if Task 5's fuller connection chrome
wants something less blunt (e.g. a `reset()`/re-mint path on `AppwireClient` itself).

## Self-review

- **`registerPane`'s signature is generic** (`registerPane<P>(descriptor: PaneDescriptor<P>): void`)
  rather than the plan's literal `registerPane(d: PaneDescriptor): void`. This is a
  strictly-compatible superset — every call site the locked signature permits still compiles
  identically, TS infers `P` from the argument with no explicit type argument required — and it
  buys call sites (e.g. `panes/welcome/index.tsx`) a compile-time check that `component`'s prop
  shape actually matches the descriptor's own params type. Flagging it since "exactly per Locked
  interfaces" was an explicit requirement and I want that judgment call visible rather than
  silent.
- **CSS module filenames use PascalCase** (`AppShell.module.css`, `ConnectionBanner.module.css`)
  matching each component's own name, following `src/dev/DevHarness.module.css`'s precedent
  rather than `src/widgets/*/​<name>.module.css`'s all-lowercase convention — these files live in
  `src/shell/`, not `src/widgets/`, so I matched the sibling directory's existing convention.
  `token-contract.test.ts`'s file-naming check only requires the `.module.css` suffix, not a
  specific case, so both conventions satisfy it.
- **Neither new stylesheet uses `--attention`/`--alive`/`--danger`.** Confirmed deliberately, not
  by accident: `token-contract.test.ts`'s allowlist mechanism only permits
  `src/widgets/<name>/<name>.module.css` paths to use those tokens at all (its own assertion
  fails for *any* other path that reaches for them, allowlisted or not), so a "quiet" banner
  living in `src/shell/` was the only option compatible with that existing, passing gate — not a
  new judgment call I could have gotten wrong. Verified by reading that test's regex directly
  rather than assuming.
- **`requireClass` import from `widgets/internal/`**: `ConnectionBanner.tsx` imports it for the
  same CSS-module type-safety reason every widget does. Checked first whether this crosses an
  ownership boundary — it's not exported from the widgets barrel — but `src/dev/ThemeFlip.tsx`
  already imports it the identical way from outside `src/widgets/`, so this is a followed
  precedent, not a new one.
- **`PaneTitleCtx` is `Record<string, never>`**, not an empty `interface {}` (which
  `@typescript-eslint/no-empty-object-type` flags) and not a speculative shape with guessed
  fields (the plan says "ctx carries thread name lookups" but no registered pane in this task
  needs one — welcome's title is constant). Left empty and documented as a seam; add fields when
  a real `title()` implementation needs them.
- Ran the full suite, typecheck, lint, and build **after** committing (not just before), to
  verify the committed tree — not just my working state — is what's actually green.

## Files

Created:
- `cmd/serf-hub/frontend/src/shell/paneRegistry.ts`, `paneRegistry.test.ts`
- `cmd/serf-hub/frontend/src/shell/routing.ts`, `routing.test.ts`
- `cmd/serf-hub/frontend/src/shell/clientContext.tsx`, `clientContext.test.tsx`
- `cmd/serf-hub/frontend/src/shell/ConnectionBanner.tsx`, `.module.css`, `.test.tsx`
- `cmd/serf-hub/frontend/src/shell/AppShell.tsx`, `.module.css`, `.test.tsx`
- `cmd/serf-hub/frontend/src/panes/welcome/Welcome.tsx`, `Welcome.test.tsx`
- `cmd/serf-hub/frontend/src/panes/welcome/index.tsx`, `index.test.tsx`

Modified:
- `cmd/serf-hub/frontend/src/App.tsx` — mounts `AppShell` by default; `/dev/widgets` unchanged;
  `/dev/harness` added (same lazy + `import.meta.env.DEV` treatment as the gallery route).
- `cmd/serf-hub/frontend/src/App.test.tsx` — updated for the above; `dev/DevHarness.tsx` and
  `dev/DevHarness.test.tsx` were **not** touched (DevHarness stays a named export; the route
  adapts it via `.then((m) => ({default: m.DevHarness}))`).

## Locked-interface compliance

`PaneTypeId`, `PaneProps<P>`, `PaneDescriptor<P>` (field names/types), `paneToURL`, `urlToPane`
signatures match the plan's Locked interfaces block exactly (registerPane's generic-vs-literal
nuance noted above). Did not build `shell/workspace.ts`/`useWorkspaceStore` — explicitly Task 2's,
per the plan.
