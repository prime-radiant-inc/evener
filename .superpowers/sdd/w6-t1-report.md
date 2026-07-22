# Wave 6 — T1 (surfaces chokepoint) report

**Status: DONE.** Every chokepoint touch landed once against the locked seam interfaces; the four
streams (T2–T5) can now build behind typed seams without touching a chokepoint. Live smoke passed all
three acceptance items (plus a full model turn end-to-end).

Worktree: `webui-w6-surfaces`, branch `w6-surfaces`, base `d043a1b34`.
**Commit range: `51f67ebc9..5e8a9ebf7`** (4 code commits) + this report commit on top.

| commit | what |
|---|---|
| `51f67ebc9` | palette, notifications, rail-host seam stubs |
| `735712bd6` | spawn seams (`startThread`/`preflight`) + minimal spawn pane |
| `e55cd045b` | AppShell chokepoint (spawn route, palette, notifications, RailHost swap) |
| `5e8a9ebf7` | Composer leading-`/` opens the palette |

## What shipped (per plan step)

**Seam stubs (compiling, real signatures, minimal working bodies):**
- `shell/palette/paletteController.ts` — vanilla `paletteStore` `{open,query}` + `openPalette(q?)`/
  `closePalette()`/`usePaletteStore(selector)`. T3 fills the overlay body.
- `shell/palette/CommandPalette.tsx` — empty, dismissable `Dialog` overlay off the store (renders no
  palette content; T3 fills).
- `notifications/index.ts` — idempotent no-op `initNotifications()`. T4 fills.
- `shell/rail/railController.ts` — no-op `revealSessionInRail(ref)`. T5 fills, T3 consumes (PIN-A).
- `shell/rail/RailHost.tsx` — pass-through to `<Rail/>` (+ barrel export). T5 fills mode logic.

**Spawn pane + seams:**
- `panes/spawn/startThread.ts` — the `thread/start` seam (`SpawnRequest`/`SpawnResult`/`startThread`).
- `panes/spawn/preflight.ts` — `preflightDir` (serf/path/validate, fail-open) + `createDir`
  (POST /api/dirs/create).
- `panes/spawn/Spawn.tsx` + `index.tsx` — the minimal working pane (prompt `Textarea` + working-dir
  `PathPicker` + `Spawn` button), self-registering as the `spawn` singleton titled "New session".

**Chokepoints (touched once):**
- `AppShell.tsx` — `openRouteAsPane` routes `spawn`→`openPane("spawn")`; `SPAWN_NOT_READY_NOTE` and the
  welcome fallback deleted; `initNotifications()` at module-eval beside `initPrefs()`; `<CommandPalette/>`
  mounted beside `<ToastRegion/>`; global ⌘K/Ctrl-K + `[data-search-trigger]` listeners → `openPalette()`;
  both `<Rail/>` mounts swapped to `<RailHost/>`.
- `Composer.tsx` — one branch: `/` on an empty composer (no meta/ctrl/alt) preventDefaults + `openPalette("/")`.

The **recent-prompts row is DROPPED per Jesse (2026-07-22)** — nothing here references it.

## LOAD-BEARING DECISION — spawn ref is the QUALIFIED `thread.serf.ref`, NOT `local:`-stripped

The seam comment cites floor §1.14 (`spawn.js:404-417` `routeID`, which strips a leading `local:`).
That describes the **legacy server's** routing and would **break the new SPA**. `startThread` returns
`resp.thread.serf.ref` **verbatim** (e.g. `local:033u2Ikb…`). Wire truth, verified against Go source:
- `thread.serf.ref` is always qualified `<source>:<threadId>` (`app_threadlifecycle.go:145,169,178`;
  `appwire/refs.go:16-21` `Ref.String()` = `sourceID + ":" + threadID`).
- `thread/read` resolves `ref` through `SourceForRef → appwire.ParseRef`
  (`cmd/serf-hub/internal/appsource/registry.go:54`; `appwire/refs.go:23-34`), which **requires the `:`
  separator** — a stripped bare id is rejected.
- Every shipped SPA session-open path already uses the qualified ref (`Rail.tsx:177` `node.session.ref`;
  `SessionActionsMenu.tsx:87,144` `resp.thread.serf.ref`). No `local:` stripping exists anywhere in the frontend.

**Proven live:** the spawn routed to `/s/local%3A033u2IkbcfB8C5FdTWKVmH` and the session pane hydrated.
A stripped `/s/033u2Ikb…` would have been dead-on-arrival. Documented in `startThread.ts` with citations.
This is the one place a reviewer checking "did T1 ship the seam verbatim" should read the divergence rationale.

## Gates (honest exit codes, structural order, AND-chained; vitest bare/captured)

```
npx tsc --noEmit   → EXIT 0
npx vitest run     → EXIT 0   194 files / 2898 tests   (baseline 185/2866:
                              +9 test files, +32 net tests; the reworked /new
                              AppShell test was rewritten in place, not added)
npm run lint       → EXIT 0   (biome ci, 553 files)
npm run build      → EXIT 0   → git restore dist/PLACEHOLDER → tree clean
```
Test output pristine (0 noise lines beyond the benign Node `localStorage`/`--localstorage-file`
ExperimentalWarning present in the baseline). All work TDD RED-first; the Composer non-empty guard test
was **mutation-verified** (removing the `textRef.current === ""` guard makes it fail).

## Live smoke (isolated fake-`$HOME` hub, real credentials, no mocks)

Built `serf-hub` + `serf` from the worktree (embedding the fresh `npm run build` dist). Ran an **isolated**
hub under a fake `HOME` (`SERF_HUB_WEB=new`, `127.0.0.1:19286`, provider keys sourced from repo `.env`,
minimal `oai-work`/`gpt-5-nano` providers.toml + launch.toml in the fake `HOME/.serf`). The host-global
`$HOME/.serf/hub.lock` was **never touched** (verified: real lock mtime unchanged). No credential material
echoed.

| # | item | verdict |
|---|---|---|
| 1 | `/new` opens the spawn shell | **Pass** — prompt field + working-dir PathPicker + Spawn button; rail (RailHost→Rail) rendered |
| 2 | bare-prompt spawn creates a session, routes to `/s/{ref}` | **Pass (strong)** — routed to `/s/local%3A033u2Ikb…` (**qualified** ref), a **real serf daemon** spawned (`[serve] listening … session 033u2Ikb…`), session pane loaded with the sent prompt + full chrome; **the model ran the turn and replied "pong"** exactly as prompted (Round 0/1 metrics, `10s · ↑18k ↓1k`); rail live-updated to LIVE/NEEDS-YOU/PROJECTS |
| 3 | ⌘K opens an (empty) overlay | **Pass** — the empty "Command palette" `role="dialog"` opened; Escape closed it (onClose wiring) |

Teardown clean: hub + spawned daemon killed, ports free, browser released, real hub.lock untouched.

## Notes for downstream streams

- **T2** owns `panes/spawn/**` (incl. T1's `Spawn.tsx`/`index.tsx`/`startThread.ts`/`preflight.ts`).
  `startThread` maps the direct `ThreadStartParams` fields only; `branch`/`accessMode` are in
  `SpawnRequest` but T2 wires them into `launchOverrides` (floor §1.7/§1.8). `preflightDir` returns
  `ok`/`abort` today; T2 adds the `offer-create` discrimination. `SpawnPaneParams = Record<string,never>`;
  T2 adds `?dir=`/`?prompt=` (read from `window.location.search`, not params). Keep returning the
  **qualified** `thread.serf.ref` — do not strip.
- **T3** owns `shell/palette/**` (incl. T1's `paletteController.ts`/`CommandPalette.tsx`). The store is
  `{open,query}`; `openPalette` seeds `query`. Rewrite `CommandPalette.tsx`'s body freely (T1 uses a bare
  `Dialog` stub). `revealSessionInRail` is the T1 no-op stub until T5 lands (PIN-A).
- **T4** owns `notifications/**`. `initNotifications()` is called at AppShell module-eval; keep it
  idempotent + test-safe (every AppShell test imports it). Read the pinned all-OFF prefs; do not re-default.
- **T5** owns `shell/rail/**` + `tokens.css`. `RailHost` is a plain `<Rail/>` pass-through today; fill the
  sidebarMode logic + `revealSessionInRail`. ⌘K is T1's; ⌘B is yours (PIN-D, disjoint listeners).

## Concerns

- None blocking. One flagged divergence (the qualified-ref decision above) — it is correct per wire truth
  and proven live, but it consciously **does not** port floor §1.14's literal `local:`-strip. If a reviewer
  expects the verbatim strip, this is the item to reconcile.
