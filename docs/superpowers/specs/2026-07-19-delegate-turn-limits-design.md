# Delegate-turn limits: configurable cap, honest occupancy, drive budget

**Status:** approved design (2026-07-19) · implementation pending
**Worktree:** `subagent-limit-study`
**Origin:** session `local:033rRr4hCSjZLuIs7XT5Nw` hit `tree_at_capacity` while
every delegate job was terminal and every delegate idle; `job_list` was
truncated at exactly 50 rows, producing the user-visible belief that "serf is
limited to 50 subagents per session no matter what."

## Problem statement

Three hardcoded, partially invisible limits govern subagent fan-out:

1. **Tree counter (cap 16, hardcoded).** `agent/tree_counter.go` bounds
   concurrently *running* delegate turns across the whole session tree. Idle
   delegates hold no slot by design (verified). The cap value, and the "16"
   baked into the spec-pinned error text, are not configurable. Design intent
   (user, 2026-07-19): bound *actively engaging* turns at a configurable N,
   default 50; inactive subagents must not count.
2. **Drive-down turns consume the same budget invisibly.**
   `driveSubagentNotificationTurn` (`agent/subagents.go`) reserves a tree slot
   but mints no job record, has no timeout, and hot-loops a re-drive whenever
   attention remains. In the origin session the counter read 16 with zero
   running jobs: the holders were drive activity or leaked reservations —
   indistinguishable from the outside because occupancy is not observable.
3. **Listing limits masquerade as system caps.** `job_list` slices
   `jobs[:limit]` (`agent/jobs.go:784`, default 50, max 100) with no offset
   and prints "50 job(s)." — the exact number the user reported as a cap.
   The retained-terminal cap (128, `agent/subagent_manager.go:39`) is also
   hardcoded and lower than intended.

## Goals

- Concurrent delegate-turn cap configurable, default **50**; idle delegates
  never count (existing semantic, preserved and regression-tested).
- Occupancy observable: what holds slots, surfaced in `job_list`/`status` and
  in the capacity error itself.
- Drive turns on their own small budget with a timeout, so they can never
  starve user spawns or pin a slot on a hung child.
- Retained-terminal cap default **2048**, configurable.
- `job_list` windowed (`offset`) with an honest `showing A-B of N` footer.

Non-goals: bounding concurrent shell jobs (standing gap, out of scope);
changing delegate/depth allowance semantics; persistence of counter state
across restarts (the counter is rebuilt from post-reconciliation state, as
today).

## Design

### 1. Configurable concurrent-turn cap (default 50)

- New `SessionConfig.MaxConcurrentDelegateTurns int`
  (`json:"max_concurrent_delegate_turns,omitempty"`); `applyDefaults` maps
  `<= 0` → 50. Snapshot round-trip via `schema.ConfigSnapshot` /
  `toSnapshot` / `fromSnapshot`, same as `MaxSubagentDepth`.
- CLI: `--max-concurrent-delegates` on `serf run` and `serf serve`
  (`-1` = default), wired exactly like `--max-subagent-depth`
  (`cmd/serf/main.go`, `run.go`, `serve.go`).
- Launch surfaces: appwire `maxConcurrentDelegateTurns *int`
  (`appwire/types.go`); hub launchconfig schema + merge
  (`cmd/serf-hub/internal/launchconfig`); TUI launchconfig. Naming follows
  `docs/conventions/naming.md` (kebab flag / snake config / camel wire).
- `newTreeCounter(cap int64)` takes the cap; the two root mint sites
  (`agent/session_init.go:124,464`) pass `int64(cfg.MaxConcurrentDelegateTurns)`.
  Children inherit the counter pointer unchanged — the cap is root-defined,
  tree-wide, exactly as today.
- `errTreeAtCapacity` (a package var with "16" baked in) becomes
  `treeAtCapacityError(cap int64) error`, formatted at the failure site
  with the configured value. Phase 1 text:
  `tree_at_capacity: 50 delegate turn slots in use across this session tree. Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.`
  Phase 2 extends the same sentence with the occupancy clause (item 2):
  `...(J delegate jobs, D drive turns). Wait for...`
  The wording change from "16 delegate jobs running" to "N delegate turn
  slots in use" is deliberate: the old text lies when drives hold slots.
  The `tree_at_capacity` prefix token is preserved across both phases.
  `docs/job-control.md` §tree-cap and the pinned-text tests
  (`agent/tree_counter_test.go:362` and kin) update to match.

### 2. Occupancy observability

- `treeCounter` tracks holders by kind. `treeReservation` gains a
  `kind` (`slotKindJob | slotKindDrive`); reserve sites pass it
  (spawn/resume attach → job; drive → drive). Counter keeps `n`, `jobs`,
  `drives` atomics; `release` decrements per kind. Release stays
  idempotent and nil-safe.
- New accessor `treeCounter.occupancy() (total, jobs, drives, cap int64)`.
- Surfaces:
  - `job_list` footer gains one line when any slot is held:
    `delegate turn slots: X/Y in use (J jobs, D drive turns).`
  - The capacity error carries the same numbers (item 1).
  - `status` detailed view: same tuple beside the job summary
    (`agent/status.go`).
- This is diagnostic honesty, not a new control surface: no behavior change
  beyond text.

### 3. Drive turns: separate budget + timeout + pacing

- Drives stop reserving from the shared spawn tree counter. A second
  root-minted, tree-shared counter — `driveCounter`, cap constant
  `defaultMaxConcurrentDriveTurns = 8` — bounds concurrent drive turns.
  It is created beside the tree counter in `session_init.go` and inherited
  the same way. Not configurable (YAGNI; a constant is tunable later if
  telemetry says so).
- At drive capacity, `driveSubagentNotificationTurn` returns false exactly
  as it does at tree capacity today: the child's durable ledger stays
  queued and the next wake-edge/loop boundary retries. No delivery-loss
  regression; drives can no longer starve spawns.
- **Timeout:** `driveCtx` becomes
  `context.WithTimeout(context.Background(), driveTurnTimeout)` with
  `driveTurnTimeout = 5 * time.Minute`. A hung child turn is cancelled and
  its drive slot freed instead of pinning until parent close. The clock
  comes from `s.sclock()` so tests use the fake clock.
- **Pacing:** the post-turn re-drive check (`subagents.go:999-1025`) keeps
  its condition (`peekNotifications() > 0 || hasPendingWatchSends()`) but
  waits `driveRedriveMinInterval = 1 * time.Second` before re-driving the
  same child, instead of launching immediately. Wake-edges and loop
  boundaries are unaffected.

### 4. Retained-terminal cap: default 2048, configurable

- `defaultMaxRetainedTerminal` 128 → 2048
  (`agent/subagent_manager.go:39`).
- New `SessionConfig.MaxRetainedTerminal int`
  (`json:"max_retained_terminal,omitempty"`); `<= 0` → 2048; snapshot +
  CLI `--max-retained-terminal` + launchconfig, same wiring pattern as
  item 1.
- `newSubagentManager` gains a cap parameter; both call sites
  (`session_init.go:163,507`) pass the configured value.
- GC/fail-loud semantics unchanged: closed/consumed records evict first,
  oldest by `endedAt`; the error still names the remedy. At 2048 the
  fail-loud path should be unreachable in practice.

### 5. `job_list` windowing

- New `offset` arg (default 0, `>= 0`, error on negative) beside `limit`
  in `jobListFilterFromArgs`; applied in `listWithError`
  (`agent/jobs.go:784`) and in the `walkDescendantJobs` path: slice
  `jobs[min(offset,total):min(offset+limit,total)]` after computing
  `total := len(jobs)`.
- Footer: `showing A-B of N jobs.` where `A = offset+1`,
  `B = offset+len(page)`; when the page covers everything, keep today's
  `N job(s).` shape (no noise for the common case). The delegate section
  stays unwindowed and gains `N delegate(s).`
- `jobListResult` JSON gains `offset` and `total` (`count` stays the page
  size for compatibility).

### 6. Tests

Deterministic, scripted provider (docs/testing.md; no live calls).

**Phase 1**
- flag → `SessionConfig` → counter cap plumbing (run/serve flag parse,
  launchconfig merge, snapshot round-trip including new fields).
- Spawn delegates with cap configured to 3: 3 concurrent succeed, 4th gets
  the formatted error naming "3"; after one completes, retry succeeds.
- Retained cap: default is 2048; a small override (e.g. 2) reproduces
  today's eviction/fail-loud behavior.
- `job_list` offset: window 51-100 of a 120-job fixture reports
  `showing 51-100 of 257`-shaped text with the real numbers; full-page
  case keeps `N job(s).`

**Phase 2**
- Occupancy tuple: saturate with jobs vs drives, assert breakdown in the
  error and in the `job_list` footer line.
- Release idempotence preserved (existing tests keep passing).

**Phase 3**
- Drives do not consume tree slots: pin all 8 drive slots, a spawn still
  succeeds; pin the tree counter, a drive still launches.
- Drive timeout: fake-clock advance past 5 min cancels the drive and
  frees its drive slot.
- Re-drive pacing: immediate second drive does not launch within the
  1s interval; after the interval it does.
- **Regression for the origin failure:** 50 completed idle delegates +
  drive budget saturated by synthetic attention → `createDelegate`
  succeeds; and `job_list` shows the occupancy line that would have
  diagnosed it.

## Phasing

- **Phase 1** (one commit set): items 1, 4, 5 — configurable cap +
  formatted error, retained cap 2048/configurable, `job_list` windowing.
- **Phase 2:** item 2 — reservation kinds, occupancy tuple, surfaces.
- **Phase 3:** item 3 — drive counter split, timeout, pacing.
Each phase is independently green (`make test`, `make vet`) and
committable; Phase 3 changes no Phase 1/2 semantics.

## Touch list

`agent/tree_counter.go`, `agent/session_config.go`,
`agent/session_init.go`, `agent/subagents.go`, `agent/jobs.go`,
`agent/job_delegate.go`, `agent/subagent_manager.go`,
`agent/session_tools_jobs.go`, `agent/status.go`,
`agent/schema/config_snapshot.go`, `appwire/types.go`,
`cmd/serf/{main,run,serve}.go`,
`cmd/serf-hub/internal/launchconfig/{schema,merge}.go`,
`cmd/serf-tui/internal/launchconfig/`,
`agent/internal/tool/definitions.go` (job_list offset param),
`docs/job-control.md`, `docs/conventions/naming.md`,
plus the test files named in item 6.

## Risks / decisions called out

- **Default 16 → 50** is a deliberate behavior change: 3x fan-out
  headroom. Provider rate limits are the exposure; the knob is the
  mitigation. Documented in `docs/job-control.md`.
- **Drive budget constant (8), not a knob.** Fewer moving parts; promote
  to config later only if needed.
- **Error text changes shape.** It is model-facing and test-pinned; the
  prefix token `tree_at_capacity` is preserved for any matcher.
- **Counter rebuild on restart is unchanged** (post-reconciliation
  zero + re-reserve on re-attach); no new crash-consistency surface.
