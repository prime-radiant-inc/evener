# sidebar-project-order-lastactivity-feel: does a just-touched project actually surface at the top?

**What this covers**: Investigate T1 from the 2026-07-05 consistency-sweep
Track D plan. WS3 pinned active-project ordering to `LastActivity` (max
`OrderUpdatedAt` across a project's top-level sessions), sorted desc —
`cmd/serf-hub/internal/hubcore/tree.go` (`byLastActivityDesc`, ~line
567-571; `LastActivity` computed ~line 494-499). Two unit tests already pin
the comparator (`TestBuildTree_OrdersProjectsByLastActivity` and
`TestBuildTree_OrdersProjectsByLastActivityNotCreatedAt`,
`cmd/serf-hub/internal/hubcore/tree_test.go:682-732`). This card verifies
the same claim against the **live, assembled system** (`/api/tree` served
by a real running hub over real spawned sessions), which unit tests can't
prove — they don't exercise the past-index cache/rebuild path that
actually feeds `/api/tree`.

## Pre-state

- Fresh binaries: `make build-hub && make build` at the worktree root
  (`./serf-hub`, `./serf`).
- A fully isolated fake `$HOME` (real `~/.serf` and
  `~/.local/state/serf` untouched): `FAKE_HOME=$(mktemp -d)`, hub launched
  with `env -i HOME="$FAKE_HOME" PATH="$PATH" ./serf-hub -addr
  127.0.0.1:9280 -serf ./serf` (the `env -i` + explicit `PATH` avoids any
  ambient `XDG_STATE_HOME`/`SERF_PROVIDERS_CONFIG` leaking through, since
  the hub's own defaults key off `$HOME` alone but inherit the full
  parent env otherwise).
- Local `ollama` running with a pulled model (`ollama list` shows
  `gemma4:e4b`) — a free, fully-local, credential-free provider, so real
  turns can run without spending API credits. **Sharp edge**: the hub
  auto-materializes a `providers.toml` with an `[instances.ollama] type =
  "ollama"` entry on first launch; with that file present,
  `validateProviderCredentials`'s config-aware branch
  (`cmd/serf-hub/spawn.go:461-535`) incorrectly demands a credential for
  ollama anyway (see Sharp edges below — a real bug, out of scope here).
  Workaround used for this card: `mv
  $FAKE_HOME/.serf/providers.toml{,.bak}` after hub startup, which routes
  credential validation through the no-config path that correctly
  special-cases `SourceNone` providers.

## Steps

1. Create 3 project dirs under a scratch base: `proj-old`, `proj-mid`,
   `proj-new`.
2. Spawn S1 in `proj-old` (`POST /api/spawn`,
   `model:"ollama/gemma4:e4b"`, `working_dir` = proj-old). Poll
   `/api/sessions/local:$SID` until `state` is `awaiting` (this harness's
   idle-equivalent post-turn state).
3. ~45s later, spawn S2 in `proj-mid`. Poll to `awaiting`.
4. ~45s later, spawn S3 in `proj-new` — now `proj-new` is both the
   most-recently-CREATED and most-recently-TOUCHED project. Poll to
   `awaiting`.
5. Capture `/api/tree` — baseline, before the final touch.
6. Send a follow-up turn to **S1** (`proj-old`, the OLDEST-created
   project) via `POST /s/$SID1/send`. Poll until it returns to
   `awaiting` with an incremented `turn_count`.
7. Capture `/api/tree` again, immediately, and repeatedly poll it every
   ~5s for up to 90s. Also read S1's `meta.json` directly off disk
   (ground truth, bypassing the hub) for its real `UpdatedAt`.

## Expected / Observed

- **Step 5 baseline** (`tree_before_touch.json`): project order
  `proj-new` (`updated_at` 12:30:11Z) → `proj-mid` (12:30:05Z) →
  `proj-old` (12:28:29Z) — strictly newest-updated_at-first, as
  expected (proj-new was both created and touched last of the three
  initial spawns).
- **Step 6 disk ground truth**: S1's on-disk `meta.json` `UpdatedAt` =
  `2026-07-06T12:31:50.324048Z` — newer than both proj-mid
  (12:30:05Z) and proj-new (12:30:47Z). By the LastActivity rule,
  `proj-old` should now rank #1.
- **Step 7 immediate `/api/tree`** (captured ~9s after the touch, at
  12:31:59Z): **STILL WRONG** — `proj-new` still ranks #1
  (`updated_at` shown: 12:30:47Z), `proj-old` still ranks #3
  (`updated_at` shown: **12:28:29Z**, i.e. the STALE pre-touch value,
  not the real 12:31:50Z from disk). The rendered claim lagged the
  ground truth.
- **Step 7 polling**: `proj-old` did not reach #1 in `/api/tree` until
  **12:33:16Z** — ~86s after the touch write landed on disk (touch
  request sent 12:31:47Z, meta write completed 12:31:50Z, visible in
  `/api/tree` at 12:33:16Z).
- Falsification (as literally posed by the plan): "a project only
  recently CREATED but not touched outranking a project JUST touched."
  That specific case did **not** reproduce — no untouched-but-newer
  project ever outranked a touched one once the tree finally
  recomputed. The **comparator itself is correct**, confirmed by both
  the pre-existing unit tests and this live run's eventual state.

## Verdict

**Ordering logic: correct, no change.** But live observation surfaced a
**related, real gap the unit tests can't see**: `/api/tree`'s project
order is driven by `cfg.Past.AllMetas()`
(`cmd/serf-hub/web_api_tree.go:200`), an in-memory snapshot that only
resyncs with on-disk `meta.json` changes via `past.Rebuild()`. That
rebuild runs on a `time.NewTicker(cfg.PastIndexRebuild)` (default 60s,
`cmd/serf-hub/config.go:59`) started once at hub boot
(`cmd/serf-hub/main.go:263-274`) — nothing on the ordinary
turn-completion path (`handleSend` / `processOneInput`) calls
`Past.Rebuild()` or otherwise bumps `hubcore.InputsVersion` early.
(Explicit `Past.Rebuild()` call sites are thread-fork, transcript
compaction, project delete, and one `web_session.go:476` path — none of
them "a live session finished an ordinary turn.") The result: **a
user who sends a message to an existing live session's project does not
see that project jump to the top of the sidebar until the next
periodic past-index tick fires — up to ~60s later, worst case just under
two ticks if the write lands right after a tick.** This is a genuine,
live-observed UX gap distinct from the ordering comparator itself (which
is provably correct) — it is a propagation-latency issue in the
pipeline that feeds the comparator its input.

**Not fixed here.** This isn't a small, precisely-scoped change to
`tree.go`'s comparator (the thing this card's falsification target
covers) — the fix would mean deciding whether/how to trigger a
Past-index resync (or a lighter per-session incremental update) from the
ordinary turn-completion path, which is an architectural tradeoff
(resync-on-every-turn cost vs. staleness) deserving its own review, not
a sweep-scoped patch. Recommend opening a follow-up kata: "sidebar
project order lags up to ~60s after a live session's turn completes,
because /api/tree reads from the periodically-rebuilt past index, not a
live meta read."

## Cleanup

- `POST /s/$SID/shutdown` for each spawned session (all 3 exited
  cleanly).
- Kill the hub process; `rm -r` the fake `$HOME` tmpdir.
- Evidence retained in scratch (not committed): `tree_before_touch.json`,
  `tree_after_touch.json`, `hub.log`, `timeline.txt` — see the
  session's scratchpad if you need to re-inspect.

## Sharp edges

- **Unrelated bug found incidentally, NOT fixed (out of this card's
  scope)**: `validateProviderCredentials`'s config-aware branch
  (`cmd/serf-hub/spawn.go:461-509`, taken whenever a `providers.toml`
  exists — which the hub auto-materializes on first launch) has no
  "this instance type needs no credential" case. The no-config branch
  correctly treats `credentials.Store.List()`'s `SourceNone` providers
  (e.g. `ollama`) as needing nothing (`spawn.go:519-527`), but the
  config-aware branch unconditionally requires an `api_key` / OAuth /
  env credential and returns `HubLaunchError("provider credentials
  missing for ollama: ...")` otherwise — a legible message, but
  factually wrong, blocking a valid credential-free spawn. Reproduced
  live: with the auto-materialized `providers.toml` in place, `ollama`
  spawns always 503'd; removing the file let them through immediately.
  Worth its own kata — did not touch it here since it's neither an
  ordering bug nor an illegible-error bug, the two things this sweep's
  Investigate track is scoped to.
- `state` values seen in practice: `active` → `awaiting` (not `idle`)
  after a normal turn completes for this profile/model. `awaiting`
  carries `capabilities.send: false` transiently while the daemon is
  between an internal loop step and the next steady state — poll for
  `send: true` before POSTing a follow-up turn, or the hub replies `503
  send is not available for this session`.
- A small local model (`gemma4:e4b`, 4B-class quantized) given a
  trivial "reply with one word" prompt still ran ~40+ real seconds and
  cycled through `active`↔`awaiting` more than once before settling —
  it isn't a fast rubber-stamp turn even for a one-word ask. Budget
  real wall-clock time (tens of seconds per turn) when scripting this
  scenario.
