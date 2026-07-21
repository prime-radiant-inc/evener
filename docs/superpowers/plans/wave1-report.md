# Web Rewrite Wave 1 — Report (M0 foundation + M1 protocol core)

Status: COMPLETE. All nine tasks done, per-task adversarial reviews passed, live proof captured,
full gate green. Integration branch: `worktree-webui-workspace-shell`.

## What shipped

- **Toolchain** (T1): `cmd/serf-hub/frontend/` — Vite 8 + React 19 + TS strict + vitest; npm with
  `ignore-scripts` enforced; `make build-web`/`test-web`; CI Node job; web tests gate CI for the
  first time.
- **Flag-gated serving** (T2): `SERF_HUB_WEB=new` serves the SPA shell for page GETs only;
  `/webassets/*` immutable hashed assets; legacy UI byte-identical when the flag is off;
  unmatched-path SPA fallback pinned as intended.
- **Generated types** (T3): `internal/appwirets` emits `types.gen.ts` (159 interfaces + method/
  notification tables + `AnyNotification`) from the appwire catalog, drift-tested like the docs.
  Bonus root-cause fix: the hand-edited `thread/fork` doc summary moved into the catalog.
- **Typed client** (T4+T5): `AppwireClient` — RPC correlation, initialize gate, heartbeat
  (20s/10s), reconnect (250ms→5s backoff), `onReady` refire, close-abort machinery.
- **Reducer** (T6): pure `ThreadModel` fold with golden JSONL fixtures; `turn/completed` gated on
  `activeTurnId` (cross-thread-safe).
- **Stores** (T7): connection + threads (refcounted ensure/release, additive multi-thread
  subscribe, reconnect re-hydration, Conflict→`ConflictError`).
- **Live proof** (T8): committed evidence (`.superpowers/sdd/t8-evidence/`) — real gpt-5.5
  session streaming through the full stack (monotonic pendingText growth over 7 samples ×5
  turns) and `kill -9` → reconnecting (510ms) → ready (511ms) with zero page reloads.
  Evidence-coherence review verdict: Supported.

## Review-caught defects (all fixed + independently re-verified)

fsevents postinstall vs no-scripts constraint · SPA guard swallowing `/s/{ref}` action
POSTs/images · stderr-leaking generator test · hyphen-unsafe `deriveName` (invalid TS) ·
`close()`-races-open hang · throwing subscriber tearing down a healthy handshake ·
reentrant-close timer/orphan-socket leaks (3 sites) · cross-thread `turn/completed` corruption ·
refcount leak on failed hydrate · wire-nullable `thread/list` crash · App.tsx delta shape.

## Deviations from the plan (all controller-ruled, recorded in the SDD ledger)

- `AnyNotification` emitted by the generator (not defined in client.ts) — layering.
- `internal/appwirets` mirrors appwiredoc's flat-main layout, not the brief's `main/` split.
- `ConnectionClosedError` added to the locked error surface (close()-induced rejections only).
- T4-era "server close → closed" test superseded by T5's reconnect contract (updated, not
  deleted).
- `connectionStore` carries a non-locked `client` field (shell wiring seam); `serverInfo` is
  populated by the Wave-3 shell from `connect()`'s return (no seam path exists by design).
- Conflict mapping extended beyond `send` to steer/queue/interrupt (matches daemon 409 surface).

## Standing patterns for later waves

- **Go wire-nullable arrays**: nil slices arrive as JSON `null`; every list consumer defaults
  (`?? []`).
- **`setState`-then-continue must re-check `isClosed()`** (sync reentrancy twin of the
  await-boundary hazard).
- **Snapshot recovery is the truth model** — rehydrate wholesale on any doubt.
- **M9 constraint**: dedicated hub port + serialized Chrome per e2e stream (parallel agents
  contended on a shared port/profile; DOM-eval evidence held, screenshots didn't).
