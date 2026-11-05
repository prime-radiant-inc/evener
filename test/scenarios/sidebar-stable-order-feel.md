# sidebar-stable-order-feel: projects keep their name order and sessions keep their creation order through a turn

**What this covers**: the two sidebar lists do not reorder on activity.
Projects are emitted in name order (`ProjectNameLess`,
`cmd/evener-hub/internal/hubcore/tree.go`), and a project's sessions are
emitted newest-created first (`ProjectSessionOrderLess`,
`cmd/evener-hub/internal/hubcore/session_order.go`). `UpdatedAt` drives each
row's age, not its position. The AppWire navigation projection re-sorts merged
project buckets by name and merged session tiers by creation order, and the
rail joins the Current and Recent tiers with the same key, so the visible list
keeps creation order across the tier boundary.

Layer (1), the comparators, is pinned deterministically by the hubcore
scenarios `fuzzScenarioBuildTree_OrdersProjectsByName`,
`fuzzScenarioBuildTree_OrdersProjectsByNameNotLastActivity` and
`fuzzScenarioBuildTree_OrdersProjectSessionsByCreatedTitleAndID`
(replayed by `FuzzHubcoreScenarios`). This card covers the layer unit tests
cannot see: a completed turn reaches the navigation projection's memoized
input without moving either list.

**Surface**: see `docs/developing-evener/agentic-testing.md`. The session
verbs live under `/api/sessions/local:$SID/…`.

## Pre-state

- Fresh binaries and a hub on an isolated `$HOME` with a kernel-assigned port —
  the Setup checklist in `docs/developing-evener/agentic-testing.md`. Never a
  real hub, never a hardcoded port.
- A model that can run real turns. A local `ollama` model is the cheapest
  credential-free option (`ollama/gemma4:e4b`).
- Leave the hub's auto-materialized `$HOME/.evener/providers.toml` in place.

## Steps

Every step is **browser-free**: the assertion is the order of the AppWire
manifest's `data.catalogs.projects` and the project's session rows. The rail
renders those orders verbatim (`Rail.tsx` maps them through `projectNodes`,
which preserves incoming order).

1. Create three project dirs under one scratch base: `proj-zulu`,
   `proj-mike`, `proj-alpha`.
2. Spawn `S1` in `proj-zulu` (`POST /api/spawn`, `working_dir` = proj-zulu).
   Poll `GET /api/sessions/local:$S1` until its turn has finished (settled on
   `idle`).
3. Wait ~10s, then spawn `S2` in the same `proj-zulu`; poll to settled. `S2`
   is now the project's newest-created session.
4. Spawn `S3` in `proj-mike` and `S4` in `proj-alpha`; poll each to settled.
5. Capture the navigation manifest as the baseline.
6. Send a follow-up turn to **S1** — the oldest-created session in
   `proj-zulu` — via `POST /api/sessions/local:$S1/send` with body
   `{"text":"…"}`. Poll until it settles again with an incremented turn count.
7. Read the manifest again and record the reported `updated_at` of S1 and the
   order of the project catalog and of `proj-zulu`'s rows.

## Expected

- **Step 5 baseline**: the project catalog is `alpha` → `mike` → `zulu`,
  established by name alone. `proj-zulu`'s rows are `S2` → `S1`, newest
  creation first. Falsify: any other order.
- **Step 7 after S1's turn**: the project catalog is still
  `alpha` → `mike` → `zulu`, and `proj-zulu`'s rows are still `S2` → `S1`.
  S1's reported `updated_at` has advanced to the on-disk value.
  - Falsify: `proj-zulu` moves, or S1 moves above S2. That means an
    update-time sort is back in the tree or the projection.
  - Falsify: S1's reported `updated_at` stays at the pre-turn value while the
    order holds. The order is then stable for the wrong reason — the
    `SetOnStatusChange` → `RefreshOne` → `UpdateMeta` → `bump` chain is broken
    and the projection is waiting for the periodic rebuild.

## Cleanup

- `POST /api/sessions/local:$SID/shutdown` for each spawned session.
- Kill the hub by the PID you captured; `rm -rf` the run directory and the
  scratch project dirs (Cleanup recipe in
  `docs/developing-evener/agentic-testing.md`).

## Sharp edges

- **A normal turn settles on `idle`; it rests `awaiting` only when the agent
  ends on `needs_response` or asks a question.** Poll for
  `capabilities.send: true` before POSTing the follow-up in step 6.
- **Creation order needs distinct creation times.** The ~10s gap in step 3
  keeps S1 and S2 out of the same second. If they tie, title and ID decide the
  order (see `ProjectSessionOrderLess`), so the assertion no longer tests the
  creation key.
- **A row crossing the Current/Recent boundary stays put too.** The rail merges
  the two tiers by creation time (`compareProjectSessions`), so a session that
  ages from Current into Recent, or moves back after a turn, keeps its
  creation position. That merge is pinned by
  `railResources.test.ts`; this card's sessions all stay Current.
- **`RefreshOne` keys off a status transition, not the meta write.** If the
  daemon writes its meta after the roster observed the status flip, the
  reported `updated_at` can stay stale until the next transition or the 60s
  rebuild. The order stays correct either way; only the age can lag.
- **A meta file mutated out-of-band waits for the periodic rebuild.** There is
  no status change to key off, by design.
