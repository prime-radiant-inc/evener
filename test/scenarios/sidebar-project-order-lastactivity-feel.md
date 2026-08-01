# sidebar-project-order-lastactivity-feel: a just-touched project surfaces at the top, and does so promptly

**What this covers**: two things that live at different layers. (1) The
project-ordering comparator: active projects are emitted newest-first by
`LastActivity`, the max `UpdatedAt` across a project's sessions, not by when a
session was created (`byLastActivityDesc`,
`cmd/serf-hub/internal/hubcore/tree.go:970-974`; `LastActivity` computed at
`:886-895`, field doc at `:135-138`). (2) The **propagation** of a completed
turn into that comparator's input, which is what a person actually experiences
as "did the project I just touched move to the top?".

Layer (1) is already pinned deterministically by two scenarios —
`fuzzScenarioBuildTree_OrdersProjectsByLastActivity` and
`…OrdersProjectsByLastActivityNotCreatedAt`
(`internal/hubcore/tree_test.go:958,984`, replayed by `FuzzHubcoreScenarios`,
`internal/hubcore/scenarios_fuzz_test.go#FuzzHubcoreScenarios`). This card exists for layer
(2), which unit tests cannot see: `/api/tree` builds from
`cfg.Past.AllMetas()` (`cmd/serf-hub/web_api_tree.go:389`, memoized on the
inputs version at `:261-280`), an in-memory index that has to *learn* a
session's meta changed.

**Surface**: see `docs/agentic-testing.md` — the REST surface table there is
authoritative for the session verbs this card drives. The old text used
`POST /s/$SID/send` and `POST /s/$SID/shutdown`; that shim was deleted at
`660376f78` and now 404s silently
(`cmd/serf-hub/web_workspace.go:19-22,37-46`). The namespace is
`/api/sessions/local:$SID/…`.

## The gap this card found, and its current status

The 2026-07-06 run recorded a real, live-observed propagation gap: a follow-up
turn on the oldest project's session landed on disk at 12:31:50Z, and
`/api/tree` did not reflect the new order until 12:33:16Z — **~86s**. At the
time nothing on the turn-completion path refreshed the past index; only a
`time.NewTicker(cfg.PastIndexRebuild)` (default 60s,
`cmd/serf-hub/config.go:62,134-136`; started at `cmd/serf-hub/main.go:400`)
ever did. The card's own recommendation was "a lighter per-session incremental
update triggered from the turn-completion path".

**That fix has since landed, and this card is now its regression guard.** The
chain is:

1. `roster.SetOnStatusChange(refreshPastOnStatus(past))` (`main.go:279`, whose
   comment names this exact symptom: "so the sidebar order (which is keyed off
   UpdatedAt) doesn't lag behind a completed turn").
2. `Roster.Refresh` diffs per-session status and fires the callback once per
   changed id (`internal/hubcore/roster.go#Refresh`), driven by an fsnotify
   watch on the run dir plus a 5s ticker (`:433-471`).
3. `refreshPastOnStatus` calls `past.RefreshOne(id)`
   (`cmd/serf-hub/main_background.go#refreshPastOnStatus`), which re-reads that one session's
   `meta.json` and folds it in via `UpdateMeta`
   (`internal/hubcore/past.go:371-397,327-350`) — no full rescan.
4. `past.SetOnChange(func(){ bump(); notifyTreeChanged(web.appRPC) })`
   (`main.go:372`) busts the `/api/tree` memo *and* pushes `serf/tree/changed`,
   so an open rail refetches on its own 250ms debounce
   (`frontend/src/stores/tree.ts:443-450,455-467`).

So the expectation flipped: the order should now update in **seconds**, not on
the next 60s tick. If the ~86s lag reproduces, one of those four links is
broken — that is the failure this card now catches.

## Pre-state

- Fresh binaries and a hub on an isolated `$HOME` with a kernel-assigned port —
  the Setup checklist in `docs/agentic-testing.md`. Never a real hub, never a
  hardcoded port.
- A model that can run real turns. A local `ollama` model is the cheapest
  credential-free option and is what the recorded run used
  (`ollama/gemma4:e4b`).
- Leave the hub's auto-materialized `$HOME/.serf/providers.toml` in place. The
  old text told you to move it aside because the config-aware branch of
  `validateProviderCredentials` demanded a credential for ollama; **that is
  fixed** — the branch now returns early for any instance type whose auth mode
  is `none` (`cmd/serf-hub/spawn.go:566-572`, `envvars.RequiresNoCredential`,
  `envvars/providers.go#RequiresNoCredential`; ollama is `AuthModes: ["none"]` at
  `envvars/providers.go:155-161`). If an ollama spawn 503s with "provider
  credentials missing", that regression is back and is worth its own kata.

## Steps

Every step here is **browser-free**: the assertion is the order of
`/api/tree`'s `projects[]`, and the rail renders that order verbatim
(`Rail.tsx:588-594` maps `tree.projects` straight through `projectNodes`,
which preserves incoming order). Open the rail if you want to eyeball it; the
verdict comes from the JSON.

1. Create three project dirs under one scratch base: `proj-old`, `proj-mid`,
   `proj-new`.
2. Spawn S1 in `proj-old` (`POST /api/spawn`, `working_dir` = proj-old). Poll
   `GET /api/sessions/local:$SID1` until its turn has finished — for this
   harness that settles on `awaiting`, not `idle` (see Sharp edges).
3. Wait ~45s, then spawn S2 in `proj-mid`; poll to settled.
4. Wait ~45s, then spawn S3 in `proj-new`; poll to settled. `proj-new` is now
   both the most-recently-created and most-recently-touched project.
5. Capture `GET /api/tree` as the baseline, and record the wall-clock time.
6. Send a follow-up turn to **S1** — the OLDEST-created project — via
   `POST /api/sessions/local:$SID1/send` with body `{"text":"…"}`. Poll until
   it settles again with an incremented turn count. Record the moment S1's
   on-disk `meta.json` `UpdatedAt` changes (that is the ground truth the tree
   is supposed to catch up to).
7. Poll `GET /api/tree` every ~2s for up to 120s, recording the first capture
   at which `proj-old` ranks first. Compare that timestamp against step 6's
   disk write.

## Expected

- **Step 5 baseline**: `projects[]` is ordered strictly newest-`updated_at`
  first: `proj-new` → `proj-mid` → `proj-old`. Falsify: any other order — the
  comparator itself is wrong, which would also break the two hubcore scenarios
  cited above.
- **Step 6 ground truth**: S1's on-disk `meta.json` `UpdatedAt` is newer than
  either of the other two projects'. By the LastActivity rule `proj-old` must
  now rank first.
- **Step 7 (the regression guard)**: `proj-old` reaches rank 1 within a few
  seconds of the disk write — bounded above by the roster's 5s status-poll
  ticker plus the fetch, well inside the 60s past-index rebuild interval.
  Assert **< 30s**, generously: the point is to distinguish "an incremental
  refresh ran" from "we waited for a tick". Also confirm the `updated_at` the
  tree reports for `proj-old` matches the value on disk, not a stale
  pre-touch one — a correct rank with a stale timestamp means something else
  reordered it.
  - Falsify: `proj-old` only reaches rank 1 at ~60s or later, or its reported
    `updated_at` stays at the pre-touch value while its rank changes. Either
    means the `SetOnStatusChange` → `RefreshOne` → `UpdateMeta` → `bump` chain
    is broken and `/api/tree` is back to waiting on the periodic rebuild.
- **The original falsification still applies too**: "a project only recently
  CREATED but not touched outranking a project JUST touched." It did not
  reproduce in the 2026-07-06 run and must not now.

## Recorded run (2026-07-06, pre-fix — kept as the failure signature)

Baseline order `proj-new` (12:30:11Z) → `proj-mid` (12:30:05Z) → `proj-old`
(12:28:29Z), correct. S1 touched: request 12:31:47Z, meta write 12:31:50Z. The
`/api/tree` capture at 12:31:59Z still ranked `proj-new` first and still
reported `proj-old`'s `updated_at` as **12:28:29Z** — the stale pre-touch
value. `proj-old` did not reach rank 1 until 12:33:16Z, ~86s after the write.
The comparator was correct throughout; only the pipeline feeding it lagged.
Those numbers are the shape of the regression, not the current expectation.

## Cleanup

- `POST /api/sessions/local:$SID/shutdown` for each spawned session.
- Kill the hub by the PID you captured; `rm -rf` the run directory and the
  scratch project dirs (Cleanup recipe in `docs/agentic-testing.md`).

## Sharp edges

- **This harness settles on `awaiting`, not `idle`, after a normal turn.**
  `awaiting` also carries `capabilities.send: false` transiently while the
  daemon is between an internal loop step and its next steady state — poll for
  `capabilities.send: true` before POSTing the follow-up turn in step 6, or the
  hub replies `503 send is not available for this session`.
- **Wall-clock time is part of the measurement, so don't collapse the waits.**
  The 45s gaps in steps 3-4 exist to make the three projects' `updated_at`
  values unambiguously ordered; shrinking them can put two projects inside the
  same second and make the tiebreak (a stable sort on the insertion order,
  `tree.go:973`) decide the outcome instead of the comparator.
- **A small local model is not a fast rubber stamp.** In the recorded run a
  4B-class quantized model given a "reply with one word" prompt still ran ~40+
  real seconds and cycled through `active`↔`awaiting` more than once before
  settling. Budget tens of seconds per turn.
- **`RefreshOne` keys off a *status transition*, not off the meta write, and
  that leaves one narrower residual race.** The hub re-reads a session's
  `meta.json` at the moment `Roster.Refresh` notices its status changed
  (`internal/hubcore/roster.go#Refresh`). The daemon writes its meta on its own
  schedule — `maybeAutoSave` has many call sites, including a deferred flush on
  every exit from a turn (`agent/session_lifecycle.go:908-919`) — so if the
  write lands *after* the status flip the roster observed, that `RefreshOne`
  reads the pre-touch meta
  and nothing re-reads it until the next transition or the 60s rebuild. If step
  7 misses its bound, check that ordering in the hub log and on disk before
  concluding the whole chain is broken: a lost race on one transition is a much
  narrower gap than the original "nothing on the turn-completion path refreshes
  at all", and is worth its own kata rather than a rerun of this one.
- **A meta file mutated out-of-band still waits for the periodic rebuild.**
  There is no status change to key off, by design (`main.go:272-278`). Don't
  mistake that for the regression this card guards.
- **`RefreshOne` is a no-op for an id the index has never seen**
  (`past.go#RefreshOne`), so a brand-new session's very first appearance still
  depends on a rebuild or on the live roster's own path. Step 6 deliberately
  touches an *already-indexed* session for this reason.
