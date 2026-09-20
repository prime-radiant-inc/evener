# Task 76 — PR #1100, Task 73 fix round 3

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/pr-1100`,
branch `codex/mobile-round-timing-replay`, HEAD `901bf98f0`, tree clean.
Nothing pushed, main not merged, the four existing commits untouched.

Base for this round: `8bc358512` (the four Task 73 commits on top of the GitHub
head `32bb3b9dd`). Four new commits on top:

| Commit | Finding |
|---|---|
| `8d2307809` `fix(agent): name a hook completion's owner on the run that dispatched it` | CRITICAL + Important 1 (one commit; the threading closes both) |
| `ebaad8f3e` `test(agent): pin that a hook completion's entry precedes its event` | Important 2 |
| `75c84f7da` `test(apptranscript): name the grouping case for what it exercises` | Important 3 |
| `901bf98f0` `test(agent): await the fold's live hook end instead of racing it` | Minors 1 and 2 |

A Claude process restart interrupted this task mid-way, with the threading work
uncommitted. The diff was re-read in full afterwards and **both** the RED and
the GREEN below were re-established from scratch rather than carried over.

---

## CRITICAL (session-global `foldHookOwner`) + Important 1 (a concurrent producer takes the fold's owner) — fixed in `8d2307809`

Both findings are one defect — a session field cannot say which *run* a
completion came from — so the ruling's threading closes them together, with one
regression test.

### The reviewer is right; round 2's defence is factually wrong

Round 2 argued that "hook execution inside the runner is synchronous and the
callback fires on the calling goroutine, so the only completion that can fire
during a fold's `RunPreCompact` is that fold's own" (task-33-report.md, Task 73
fix round 2). Falsified at `agent/internal/hooks/hooks.go:585`: `runAll`
dispatches **each matched hook on its own goroutine** (`go func(idx int, h
plugin.RegisteredHook)`) and calls `r.onEvent(events.EventHookEnd, …)` from that
goroutine. `runAll` does join them before returning, so the fold's own hooks are
inside the window — but the window was a session field, so any hook completing
anywhere in the session while it was open took the fold's owner, whatever run it
belonged to. The new test demonstrates exactly that against the committed code.

Overlapping folds are reachable, not theoretical:
`TestFoldPublication_ClaimedNoteNotReinjectedByConcurrentFold`
(`agent/session_fold_publication_test.go:248`) already drives two folds whose
windows overlap, parking fold A while fold B runs a complete fold.

### The shape

The owner rides on the run that dispatched the hook:

- `agent/internal/hooks/hooks.go`: new `WithRecordOwner(ctx, owner)` plus an
  unexported `recordOwner(ctx)`; `runAll` stamps `OwningTurnID:
  recordOwner(ctx)` on the `HookEndData` it already builds.
- `agent/session_compaction.go`: `runPreCompactHook` takes the owner as a
  **parameter** (Minor 3) and names it on the run:
  `RunPreCompact(hooks.WithRecordOwner(s.apiLogContext(ctx), owner), …)`. The
  `compactionRecordOwnerKey` ctx carrier round 2 added is deleted, as is
  `beginFoldHookOwner`.
- `agent/session_events.go`: `emitHookCompleted` keeps the owner the run named
  and falls back to `activeTurnOwner()` only when the run named none;
  `hookCompletionOwner` is deleted.
- `agent/session.go`: the `foldHookOwner` field is deleted — nothing to restore,
  so non-LIFO overlap has nothing to get wrong. (The field was added in
  `452be85ba`; it is net-absent from the cumulative diff against `32bb3b9dd`.)

**Size of the hook-runner API change**, against the ruling's bound: one new
exported helper, one unexported reader, one field added to an existing struct
literal. No signature changed — `SetEventCallback` is untouched, so none of its
twelve call sites move. Inside "adding a field or parameter", so no
`NEEDS_CONTEXT`.

The six existing `runPreCompactHook` call sites in
`agent/session_self_compact_test.go` pass `""`: a direct caller outside a fold
names no owner, which is what the old code computed for them anyway (an absent
ctx value fell through to `activeTurnOwner`). No expected value moved.

### RED / GREEN

New test `TestCompactionReplay_HookCompletionBesideAFoldKeepsItsOwnOwner`
(`agent/session_compaction_replay_transcript_test.go`): the fold's PreCompact
command hook parks on a FIFO rendezvous (no sleep, no poll); while it is parked
the test runs the session's Notification hook from another goroutine; the
transcript is then read for that completion's owner.

- **RED** (implementation reverted to `8bc358512`, new test only):
  `a Notification hook completing beside the fold is owned by
  "turn_compaction_01M2BE638HWTG9NKT2PZ2CC7K5" (the fold's own owner is
  "turn_compaction_01M2BE638HWTG9NKT2PZ2CC7K5"), want the owner of its own run:
  none`
- **GREEN** (with `8d2307809`): `ok primeradiant.com/evener/agent` — and round
  1's `TestCompactionReplay_FoldHookCompletionCarriesTheFoldsOwner` passes
  beside it, so the fix did not trade one attribution for the other.

---

## Important 2 (no regression test for write-before-announce) — fixed in `ebaad8f3e`

The reviewer found the seam round 2 said did not exist, and it is real:
`Session.sendEvent` (`agent/session_events.go:474`) calls
`testOnlyBlockedSendEntered` at the moment an authoritative consumer's send
parks on a saturated channel. New test
`TestHookEndWritesTheEntryBeforeAnnouncingIt`
(`agent/session_hook_turn_test.go`) builds a session with a real transcript
writer, a one-slot event channel already full and `authoritativeConsumer: true`.
The announce parks, the callback says exactly when, and the transcript is read
from outside the emitter while the event is provably still held.

- **RED** (the two statements in `emitHookCompleted` swapped back locally, never
  committed): `HOOK_COMPLETED entries while the event is still held at the
  channel: got 0, want 1`
- **GREEN** (order restored): `ok primeradiant.com/evener/agent`

---

## Important 3 (mislabelled grouping case) — relabelled in `75c84f7da`

The reviewer is right. Every case in
`TestCompactionOwnershipAcrossIncrementalItemReaders` is appended after
`writeEntries(t, reservedUserEntry(1, "input", "turn_active"))`
(`internal/apptranscript/compaction_grouping_test.go:115`), so the case's first
record is never the transcript's first record and the copy it opens with always
has a group to join. Renamed to what it does exercise — **copies with no opener
among them preserve the open group**: this fold ran mid-turn, so its first copy
is an assistant record rather than the user input its sibling case copies, and
it must join the open group instead of starting one.

**A genuinely leading replay copy is not reachable on disk**, so no second case
was added. A fold's copies are `rewriteTail`, a slice of `persistedAppendLog`
(`agent/session_compaction.go:195`), written with `ContextReplay` at
`agent/session_compaction.go:255-262`. Nothing enters that log except through an
append/write pair that puts the same turn in the same transcript:
`appendTurnAfterTranscriptWriteLocked` writes before it logs
(`agent/session.go:1727-1736`), and `recordTurn` logs and writes under one
`attentionMu` hold (`agent/session.go:1776-1786`). So every copy has its
original earlier in the same file and record 0 is never a copy. That was the
round-1 commit message's claim; it now sits next to the case instead of only in
the history.

No RED/GREEN: this finding is a naming defect, not a behavior defect. The case
itself is unchanged and still passes (all 12 subtests green).

---

## Minors

1. **Happens-before in the PreCompact test** — fixed in `901bf98f0`. The old
   form collected live hook ends on a consumer goroutine and snapshotted the
   slice once `Compact` returned, with no ordering between the two: its "no
   PreCompact hook end reached the live stream" branch was a flake waiting to
   happen. It now receives the event from a channel — that receive is the
   synchronization — with a 10s tripwire only a genuinely missing event reaches.
2. **`hooks_` naming** — same commit: the counter is `preCompactEntries`.
3. **ctx plumbing vs a parameter** — fixed in `8d2307809`: `runPreCompactHook`
   takes `owner string`. The one remaining ctx hop is across the hook-runner
   boundary, where a context value is the only carrier a run has
   (`RunPreCompact(ctx, input)`; `Input` is the JSON handed to the hook process).

---

## Overlap-test counts (`TestCompactionOwnerDuringOverlappingMutations`)

Not re-pointed, not touched.

| Command | Before (`8bc358512`) | After (`901bf98f0`) |
|---|---|---|
| `go test ./server/ -run '^TestCompactionOwnerDuringOverlappingMutations$' -count=20` | ok, 0 failures (39.7s) | ok, 0 failures (40.7s) |
| `GOMAXPROCS=1 … -count=40` | ok on the first run (84.1s); **1 failure in 8 further runs** | **2 failures in 11 runs**, 9 clean |

### The residual flake, root-caused as far as this round can take it

`GOMAXPROCS=1 -count=40` is not reliably green — at HEAD **or at the base**.
Captured failures are one shape, at `appwire_compaction_owner_overlap_test.go:345`:

```
staged owner replay item 6 differs: kind=round_timings owner=turn_m7
key=apptranscript-item-v1:turn_m7:8:2, want …:8:3 (payloadEqual=true)
```

Same payload, different entry ordinal inside logical turn 8: the live projection
put another item ahead of the round timing, the transcript did not. That is the
**cross-producer** ordering gap, which `8bc358512`'s own site comment says it
does not close ("This is THIS producer's discipline… issue #1150 carries that
rule"), and it matches issue #1150's description exactly (live-vs-cold
divergence around a fold's publication).

Evidence that it is pre-existing rather than introduced here:

- The identical failure (same subtest, same line, same `8:2` vs `8:3`)
  reproduces on a detached checkout of `8bc358512`: 1 failing run in 8
  `GOMAXPROCS=1 -count=40` runs (320 runs, 1600 subtests).
- At HEAD: 2 failing runs in 11 (440 runs, 2200 subtests). Rates
  indistinguishable.
- Mechanically, this round cannot reach it: the overlap fixture's plugin
  registers **only** a PreCompact hook (`preCompactSteeringPlugin`,
  `server/appwire_compaction_owner_overlap_test.go:42`), so no second hook run
  overlaps a fold's window there, and the fold's own hook carries the same
  `compactionOwner` before and after `8d2307809`.
- Round 2's "0 failures in 240 subtest runs" was a sample too small to see a
  ~1/300 event, not a fix.

I did not attempt a fix: the mechanism is a publication discipline shared by
every producer (issue #1150 calls for a design pass), the ruling forbids
re-pointing this test, and it is out of this round's scope. Handing the
reproduction to the coordinator rather than commenting on #1150 myself.

---

## Gates (all at HEAD `901bf98f0`)

| Gate | Result |
|---|---|
| `gofmt -l` on every touched file | clean |
| `go vet ./...` — `agent/` and root | exit 0, no output |
| `agent/` `go test -count=1 ./...` | exit 0, **zero non-`ok` lines** |
| root `go test -count=1 ./server/... ./internal/apptranscript/... ./internal/appprojector/...` | exit 0, zero non-`ok` lines |
| `-race -count=3` agent sets (`-run 'Compact\|Fold\|Hook'`, `./ ./internal/hooks/`) | ok 134.8s / 5.0s |
| `-race -count=3` root sets (`-run 'Compaction\|Grouping\|Replay\|Owner'`, server + apptranscript + appprojector) | ok 18.8s / 4.2s / 1.7s |
| golangci-lint 2.13.1 (pinned) `./...` in `agent/` | **0 issues** |
| golangci-lint 2.13.1 `./...` in root | **0 issues** |
| golangci-lint `--config .golangci-appwire.yml ./server/...` | **0 issues** |
| `make merge-approval-gate`, `make test-race` | not run (forbidden by the task) |

Notes:

- `gofmt -l agent/` also lists three files nobody touched this round
  (`agent/delegate_tree_start.go`, `agent/responses_continuation_eligibility.go`,
  `agent/tool_args_fuzz_test.go`). They differ under the locally installed
  go1.27 gofmt's composite-literal indentation, at the base commit as well. Left
  alone.
- **Deadline audit**: this repo has no such target. Searched `AGENTS.md`,
  `make/*.mk`, `Makefile`, `scripts/`, and `cmd/evener-dev/` — nothing named
  deadline/timeout audit exists (the nearest gate is
  `make test-timing-budget`, which is explicitly not part of the merge gate).
  Manual audit of every ceiling this round adds, against
  `docs/developing-evener/testing.md` §Flakes and Timeouts: five ceilings, all
  tripwires over an awaitable completion, none of them pacing —
  two `awaitWithin(…, 30s)` over FIFO rendezvous (the hook's own open/close is
  the completion), one 10s receive of the live hook end, one 10s wait on
  `testOnlyBlockedSendEntered`, one 10s wait on the released send. No sleeps, no
  polling, no widened deadline.

## Concerns

1. The `round_timings` `8:2`/`8:3` residual above: `GOMAXPROCS=1 -count=40` of
   the overlap test fails roughly once per 300 runs, at HEAD and at the base
   alike. It is issue #1150's cross-producer ordering gap. Anyone reading
   round 2's "0 failures in 240 subtest runs" as "closed" should read it as
   "not sampled deeply enough".
2. `WithRecordOwner` is a new exported name in `agent/internal/hooks`. It is an
   internal package, so the blast radius is the agent module, but it is API
   surface a future hook path can misuse by naming an owner on a run whose
   records do not belong to that turn. The doc comment states the rule.
3. Unchanged from round 2 and not addressed here: every hook completion now
   waits for `attentionMu` before its live event, so a hook completing during a
   long fold publication is announced later than before. That is the intended
   ordering; nothing measures the latency.

---

# Task 76 — fix round 4

HEAD `893afd283`, tree clean, nothing pushed, main not merged, no commit amended.

| Commit | Findings |
|---|---|
| `d42b0d664` `test(agent): keep the FIFO rendezvous out of the portable build` | Criticals 1 and 2 |
| `893afd283` `test(agent): check every PreCompact end the stream carried` | Minor 3 |

**Critical 1.** `syscall.Mkfifo` has no Windows definition, so the
concurrent-hook-owner case broke `GOOS=windows go vet -tags evenerfuzz ./...`
(`make/linting.mk:54`, the first step of the lint gate) from an untagged file.
The case moved to `agent/session_compaction_replay_transcript_unix_test.go`
(`//go:build unix`, the convention `agent/delegate_tree_stop_unix_test.go`
already uses); every portable case stayed in the original file, whose now-unused
`fmt`/`os`/`path/filepath`/`syscall` imports went with it. Both owner tests were
re-run by name afterwards to prove the moved one still executes rather than
being silently excluded.

**Critical 2.** `agent/session_tools_core_exact_fuzz_test.go:202` and `:222` are
two more `runPreCompactHook` call sites behind the `evenerfuzz` tag; both now
pass `""` like the six direct callers already updated. This is the "a test that
never runs" hazard from `docs/developing-evener/testing.md:642` in its compile
form: an untagged build says nothing about the tagged one.

**Minor 3.** The live-event assertion receives one event for its happens-before
and then drains whatever else the stream already carried, checking each — the
claim the pre-`901bf98f0` loop made, without giving the ordering back up.

## Gates (all at `893afd283`; agent module unless stated)

| Gate | Result |
|---|---|
| `gofmt -l` | clean (excluding the three pre-existing files noted above) |
| `go vet ./...` | exit 0 |
| `go vet -tags evenerfuzz ./...` | exit 0 |
| `GOOS=windows go vet -tags evenerfuzz ./...` | exit 0 |
| root `GOOS=windows go vet -tags evenerfuzz ./server/... ./internal/apptranscript/...` | exit 0 |
| `go test -count=1 ./...` | exit 0, zero non-`ok` lines |
| `-race -count=3 -run 'TestCompactionReplay_\|TestHookEnd\|TestRunPreCompactHook'` | ok 3.8s |
| golangci-lint 2.13.1 `--concurrency=2 GOGC=50 ./...`, agent and root | **0 issues** each |
| root `go test -count=1 ./server/... ./internal/apptranscript/... ./internal/appprojector/...` | exit 0, zero non-`ok` lines |

## Task 76 — #1150 diagnosis

**The interleaving that makes `8:2` vs `8:3`.** In
`assertReplayItemParity(t, mode, want, got)` the overlap test passes
`(liveItems, coldItems)`, so the failure's `key=…:8:2` is **cold** and
`want …:8:3` is **live**: the live stream has one more item ahead of the round
timing inside logical turn 8 than the transcript does. Both producers already
write before they announce —
`persistAndEmitRoundTimings` (`agent/session_lifecycle.go:29-42`) writes the
`ROUND_TIMINGS` entry through `appendTurnAfterTranscriptWrite` and only then
emits `EventRoundTimings`; `emitHookCompleted` does the same since `8bc358512`.
The gap is that neither pairing is atomic against the *other* producer:

1. Round R writes its `ROUND_TIMINGS` entry under `attentionMu` and releases.
2. Fold F takes `attentionMu`, commits its markers, copies and steering
   (`publishFoldTransaction`, `agent/session_compaction.go:255-265`), releases,
   and then `commit.flush()` emits F's staged compaction events.
3. Round R finally reaches its `s.emit(events.EventRoundTimings, …)`.

Transcript order is R-then-F, so cold gives R ordinal 2. Live order is F-then-R,
so live gives R ordinal 3. That is issue #1150's second interleaving verbatim —
the issue states it for `appendEnvironmentContext`; `persistAndEmitRoundTimings`
is the same pairing with a different record, which is why PR #1098's fix (emit
under the door for the environment append) does not reach it.

**Smallest fix that would close it.** A session-scoped publication lock held
across "commit then emit" by *both* pairings, exactly the Direction #1150
already names: `appendTurnAfterTranscriptWrite`'s write-then-emit callers
(round timings, hook completions, the environment append) take it around the
pair, and `publishFoldTransaction` holds it from `commitTranscriptsLocked`
through the event-publishing part of `flush()`. Files: `agent/session.go` (the
lock plus one helper that owns "write, then announce"),
`agent/session_lifecycle.go`, `agent/session_events.go`,
`agent/session_compaction.go`, plus a regression test. Estimate 60-120 lines,
most of it in the fold: `flush()` also runs session naming, hook user-message
delivery and the nudge latch, which were deliberately built without this
guarantee and must stay OUTSIDE the lock, so the flush needs splitting into its
publication half and its side-effect half. The known cost, recorded in #1150,
is that `emit` can park indefinitely against an authoritative consumer, so this
lock is held across a send — the same objection that kept `attentionMu` off
that path. Making the new lock narrower than `attentionMu` (publication order
only, no transcript writes) is what keeps that acceptable.

**A deterministic seam exists.** `testOnlyBlockedSendEntered` +
`authoritativeConsumer` (`agent/session_events.go:474`) park a producer's
announce precisely between its write and its emit — that is exactly what
`TestHookEndWritesTheEntryBeforeAnnouncingIt` uses — and
`cfg.testOnly.beforeFoldSideEffectsFlush` already parks a fold between its
commit and its flush (`agent/session_fold_publication_test.go:248`). Holding the
round timing's announce with the first while driving a fold through the second
forces the exact step order above, so the live-vs-cold ordinal divergence can be
pinned as a deterministic test instead of sampled at ~1 in 300 runs. That test
is worth writing before the lock, since it is what proves the lock fixed
anything.

No code was written for this item, per the ruling.

---

# Task 86 — round 12 Mediums

Head was `43d93cb46` (the coordinator's main merge, untouched). Two commits on
top; HEAD `ece521c4a`, tree clean, nothing pushed, no amend, no merge.

| Commit | Finding |
|---|---|
| `17e62b585` `fix(agent): announce no hook completion the transcript refused` | Medium 1 |
| `ece521c4a` `fix(agent): publish nothing for a marker the transcript never received` | Medium 2 |

**Medium 1.** `recordTurn` appends to live history first and reports a failed
write as a warning, so `emitHookCompleted` announced `EventHookEnd` — and kept a
history entry plus a `persistedAppendLog` entry — for a completion the
transcript refused. It now uses `appendTurnAfterTranscriptWrite`, the shape
`appendEnvironmentContext` already uses (`agent/session.go:1633-1648`): entry
first, history append only if it landed, warning and return on failure. The
write stays `writeTranscriptLocked` rather than the durable variant — the
finding is live/durable divergence on failure, and an fsync per hook completion
would be a latency change on every hook, not the fix.
RED `HOOK_END events after a failed transcript write = 1, want 0` →
GREEN `ok primeradiant.com/evener/agent` (new
`TestHookEndAnnouncesNothingWhenTheTranscriptWriteFails`, fault-injecting
`transcriptWriteFailFS`, asserting no `EventHookEnd`, exactly one warning, and
an empty history and append log).

**Medium 2.** A withheld marker kept a nil entry in `compactionTurnWriteErrs`,
so `flush` read it as written. `compactionTurnWithheld []bool` now records the
markers `commitTranscriptsLocked` skipped and `flush` leaves them alone — no
`EventCompactionTurn`, no namer, no task-list steering. The site comment that
called the divergence the accepted price now states what the remaining price
actually is. 24 lines of production change, well inside the ~80-line bound.
RED `a fold whose marker was withheld published 2 compaction-turn event(s)` →
GREEN ok (Task 68's `TestFoldPublication_FailedReplayCopyWriteLeavesNoAnchor`
extended with the `nameSessionFromTextFunc` seam and a sentinel-event drain that
makes both negative assertions ordered rather than merely early).

## Gates (HEAD `ece521c4a`)

gofmt clean (bar the three pre-existing files noted in round 4); host vet,
`-tags evenerfuzz` vet and `GOOS=windows -tags evenerfuzz` vet all exit 0 in
**both** modules; agent `go test -count=1 ./...` zero non-`ok` lines;
`-race -count=3` on the touched sets ok (5.6s); golangci-lint 2.13.1
`--concurrency=2 GOGC=50 ./...` **0 issues** in both modules; overlap test
`-count=20` ok (35.7s).

Root module `go test -count=1 ./...` is **not** clean: six failures in
`internal/selfupdate` (`installdirs_custom_test.go`, `installdirs_symlink_test.go`)
comparing `/private/var/...` against `/var/...`. They reproduce identically on a
detached checkout of `43d93cb46`, before either commit, and nothing in this task
touches that package: it is a macOS TMPDIR symlink-resolution assumption in
those tests, i.e. the ambient-machine dependence AGENTS.md forbids. Not fixed —
out of this lane and not mine to re-point without a ruling.

## Concerns

1. The `internal/selfupdate` failures above. They will fail any root-module gate
   on a Mac; someone should own them (`filepath.EvalSymlinks` on the fixture
   dir is the likely fix).
2. Medium 2 suppresses effects only for markers that were *withheld*. A marker
   whose write genuinely FAILED still reaches `handleCompactionTurnEffects`,
   which warns and then emits its event and runs the namer anyway
   (`agent/session_namer.go:483-497`). That is the same publish-without-durable
   class, one step further out, and the reviewer did not raise it — flagging it
   rather than widening the change unasked.

---

# Task 91 — round 13

Head was `920278126` (the coordinator's merge, untouched). One commit on top:
`204127b77`, tree clean, nothing pushed, no amend, no merge.

## 1. HIGH, retained turns across successive folds — REAL, and PRE-EXISTING on main. NEEDS_CONTEXT.

Built the scenario the ruling asked for (turn T recorded during fold 1, so fold 1
copies it; fold 2 retains T live; read the transcript back):

- After fold 1: `live has T = true`, `ResumeHistory has T = true`.
- After fold 2: `live has T = true`, `ResumeHistory has T = false`. The entire
  resumed history is fold 2's SUMMARY — fold 2 wrote no replay copies at all.

So T is lost. But the same probe run on a detached checkout of `origin/main`
(`eee90925a`) prints exactly the same three lines, so **this PR did not
introduce it**. A second probe narrows it further: with twelve *durable* turns
and a SINGLE fold, `ResumeHistory` already returns only the SUMMARY on main —
the six turns the fold retained live are gone from a restart. The replay copies
were never a general retention mechanism: `rewriteTail` is
`persistedAppendLog[snapAppends-base:]`, the pairs appended **since this fold's
snapshot** (`agent/session_compaction.go:164,195`), i.e. only turns recorded
DURING the fold — the case the fold's own checkpoint text could not cover. The
retained tail's durability has always been the checkpoint/summary prose, not the
entries, because the anchor discards everything before it
(`agent/transcript_read.go:144-146`, unchanged by this PR).

Why I stopped rather than fixed: closing it means changing what a fold writes.
Either (a) the rewrite set becomes "the persisted form of every turn the newly
published history retains", so each fold re-appends its whole retained tail
inside its run — a change to the size and meaning of a fold run on disk that
`foldRun`, the anchored branch, ATIF, the fork-context filter and the
apptranscript/appprojector grouping all read; or (b) `ResumeHistory` keeps
pre-marker entries the marker's fold retained, which needs a new durable field
naming them — a transcript format change. Both exceed the ~150-line bound, both
change behaviour for every session on main, and (a) also needs a ruling on
transcript growth. Shape reported; no code written.

## 2. Medium, cold/live compaction-group closed state — NOT in #1152; not reproduced; no code change.

`gh issue view 1152` lists four residuals (interrupt-marker owner, `turnNameHeld`
id disagreement, the job-notification predicate arm, `turn_%d` namespace) — this
is none of them, so no refute-by-reference.

The rules do disagree in the abstract: `groupOpenAfter`
(`internal/apptranscript/logical_turn.go:69-71`) leaves a group open after any
owned kind with a non-empty owner, while the live projector closes a
`turn_compaction_`-owned group the moment its records publish
(`internal/appprojector/appwire_projection.go:1595,1636-1638`). Divergence needs
an **ownerless continuation-kind record** (`continuesLogicalTurn`: ASSISTANT,
TOOL, TOOL_RESULTS, FAILURE, STEERING, ROUND_TIMINGS) written directly after an
idle fold's records with nothing owned or opening in between. I could not
construct one: a compaction-gap owner is minted only when no turn is running
(`stageCompactionEffects`, `agent/session_compaction.go:475-478`), and every path
that writes a continuation-kind record next opens with a USER_INPUT entry or an
owned record first — the notification wake writes its reminder with
`appendSteeringTurnDurablyForOwner(turnID)` (`agent/session_lifecycle.go:2258`)
before any assistant record, and after commit `204127b77` the remaining daemon
steering producers all carry their entry's owner too.

I did not change the grouping state machine on an unreproduced finding: closing
the cold group for a compaction-gap owner also makes each of the fold's OWN
records start a new group (`recordStartsGroup`'s `!previousOpen` arm), which is
a state-machine change with the paging/index mirror and the whole grouping table
behind it. Recommend adding it to #1152 as a fifth residual — it is the same
class: grouping that is right by coincidence of the current write order.

## 3. Medium, `SteeringInjectedData.OwningTurnID` — fixed in `204127b77`.

Four producers persisted an owner and omitted it from the live event
(`session_queue.go` ×3, `session_lifecycle.go:2274`); the projector reads the
field when present and otherwise falls back to the turn running when the event
arrives (`internal/appprojector/appwire_projection.go:970-984`). One helper,
`announceSteeringTurn(owner, data)`, now carries the entry's own owner at every
site, the fold's steering records included (they had the only correct copy).
RED (production path, notification wake): `live steering "<job-notification …>"
(kind "notification") is owned by "", want the notification turn "turn_m1" that
owns its entry` → GREEN. The shared `boundaryRecorder` gained a steering
capture, so the existing owner-parity test asserts both publications.

## Gates (HEAD `204127b77`)

gofmt clean; host / `-tags evenerfuzz` / `GOOS=windows -tags evenerfuzz` vet all
exit 0 in **both** modules; agent `go test -count=1 ./...` zero non-`ok` lines;
`-race -count=3` on Notification|Steering|Steer|Compact|Fold ok (216.9s);
golangci-lint `--concurrency=2 GOGC=50` **0 issues** both modules; overlap test
`-count=20` ok (33.8s). Root `go test ./...` carries the same six pre-existing
`internal/selfupdate` `/private/var` failures reported in Task 86 — reproduced
before these commits, untouched here.

---

# Task 98 — round 14

Head was `d664c4a20` (the coordinator's merge, untouched). One commit on top:
`eff7a7e15` `fix(agent): keep the compaction fold id out of public transcript output`.
Tree clean, nothing pushed, no amend, no merge.

The Low is real. `publicTranscriptEntry` (`agent/session_tools_transcript.go:1028`)
and `publicTranscriptLine` (`:1100`) strip `attention_id`,
`attention_resolution` and `delegate_delivery_commits`, and `CompactionFoldID`
was in neither list, so every marker, context-compaction record and steering
turn a fold wrote carried it into `read_transcript`'s JSONL and the transcript
expansions. A reader cannot interpret it either: the copies the id claims are
exactly the entries this output drops (`ContextReplay` is excluded above it).
The field now joins the other correlation fields in the one place each path
strips them — no new special case.

RED (both public paths, existing cases extended with a fold-tagged fixture):
`compaction_fold_id should be stripped, got "fold_123"` and `compaction_fold_id
should be deleted; it is crash-recovery metadata, not public transcript content`
→ GREEN `ok primeradiant.com/evener/agent`.

Gates: gofmt clean; `go vet ./...`, `go vet -tags evenerfuzz ./...` and
`GOOS=windows go vet -tags evenerfuzz ./...` all exit 0 in the agent module;
full agent module `go test -count=1 ./...` zero non-`ok` lines; `-race -count=3`
on the transcript set ok (26.7s); golangci-lint 2.13.1 `--concurrency=2 GOGC=50`
**0 issues**.

---

# Task 103 — round 15

Head was `c7a90750b` (the coordinator's merge, untouched). One commit on top:
`4be675ded`, tree clean, nothing pushed, no amend, no merge.

## 1. Medium, TUI steering regression from `204127b77` — REAL, fixed in `4be675ded`

The reviewer is right, and it is my regression. Stamping an owner on every
steering turn moved steering onto the item lifecycle
(`internal/appprojector/appwire_projection.go:970-984`), and this client had no
case for it: `ApplyThreadItem` dropped it, the job-notification headline stopped
tying to its rail row, and the optimistic steer placeholder never retired
because `reconcilePendingFromNotification` matched only
`NotifyEvenerSteeringInjected`.

Fixed in the TUI, keeping one wire encoding: a `steering` case in
`ApplyThreadItem` (append `MsgSteering`, dedupe by item id, then
`ApplyTieHeadline` for every `<job-notification>` block, which is both halves of
what the legacy handler did), and reconciliation on `NotifyItemCompleted` with
`item.Type == "steering"`, making the same two `TryReconcile` calls the legacy
branch makes. Because it lives in the reducer, `MessagesFromThread` history
rebuilds render it too — the legacy handler never covered those.

**The legacy handler stays** and is still reachable: steering drained while
nothing is running carries an empty owner (`appendSteeringTurn` and
`consumeSteeringMessage` both take `s.activeTurnOwner()`, empty when idle), and
the projector sends exactly one notification per event — the owned branch
returns before the legacy params are built, so the web still renders it once,
with no double emission.

RED (all three, at `c7a90750b`): `messages = [], want one MsgSteering carrying
the steering text`; the tie assertion on the same test; and `expected 1
confirmed msg, got 0`. GREEN after the commit, with the full
`cmd/evener-tui/...` suite clean.

## 2. Medium, self-compaction timing coordinates — REFUTED by an existing test

`server/appwire_round_timings_compaction_test.go:23`,
`TestRoundTimingsAfterSelfCompactionRetainLiveReplayIdentity`, is exactly the
parity case the ruling describes: compaction is requested through the real tool
(so the fold is a genuine self-compaction, not an external `Compact`), the
round's timing record follows it, and the test compares the live thread read
against the cold transcript projection with `reflect.DeepEqual` over
`replayItemIdentity` — which carries **both** fields the finding names, `Key`
and `Position` (`server/appwire_steering_owner_replay_test.go:260-267`) — for
the context-compaction, checkpoint/summary, steering and round-timings items.
It then repeats the comparison against bounded paging
(`:141-158`). Green 10/10 at this head.

No code changed for this finding, per the "a finding that is factually wrong
gets no code change" rule. The one real live/cold coordinate divergence I have
reproduced on this branch remains the cross-producer interleaving of issue
#1150 (Task 76, round 4 diagnosis) — a different mechanism, present on main.

## Gates (HEAD `4be675ded`)

gofmt clean; host / `-tags evenerfuzz` / `GOOS=windows -tags evenerfuzz` vet
exit 0 in **both** modules; agent `go test -count=1 ./...` zero non-`ok` lines;
`cmd/evener-tui/...` `go test -count=1 ./...` zero non-`ok` lines;
`-race -count=3` over the TUI steering/pending/notification sets ok;
golangci-lint 2.13.1 `--concurrency=2 GOGC=50` **0 issues** in both modules.
Root `go test ./...` carries the same six pre-existing `internal/selfupdate`
`/private/var` failures reported in Task 86 and 91 — unrelated, still unowned.

---

# Task 106 — round 16

Head was `20afd7c09` (the coordinator's merge, untouched). Two commits on top;
HEAD `dc90ff686`, tree clean, nothing pushed, no amend, no merge.

| Commit | Finding |
|---|---|
| `1cffc4c38` `fix(doctor): reconstruct an archive that holds presentational records` | Medium 1 |
| `dc90ff686` `fix(agent): let a fold's steering go with the anchor it never wrote` | Medium 2 |

**Medium 1.** Real: `reconstructEntries`' kind switch ended in
`unsupported archived turn kind`, so one `ROUND_TIMINGS` or `CONTEXT_COMPACTION`
record — which every recent session's archive now holds — failed the whole
reconstruction. Fixed by the rule already there for attention resolutions
(`cmd/evener-doctor/reconstruct.go:511-516`) rather than a case per kind: skip,
count, keep in `source-snapshot.json`. The archive stores their prose and none
of their structured payload, so a rebuilt record would announce timings nobody
measured and a shrink this transcript never performed. New counter
`historical_presentational_records`, a matching limitation line, and the
`RECONSTRUCTION.md` paragraph beside the attention one.
RED `exit 1: evener-doctor reconstruct: unsupported archived turn kind
"ROUND_TIMINGS"` → GREEN, with the fixture archive asserting the count of 2,
that neither kind reaches the transcript, and that the snapshot keeps both.

**Medium 2.** Real, and the same class as `ece521c4a`: `commitTranscriptsLocked`
withheld the marker and wrote the fold's steering anyway, so a resume that finds
no anchor drops the copies and keeps steering describing a compaction it cannot
see — stale, and duplicated once the retry injects the same handoff again. The
steering is now withheld with the anchor and announced no more than it is
written. A fold with no marker at all is unaffected: `anchor` is false only when
this fold HAS a marker whose tail write failed.
RED (Task 68's fault-injection test, extended with a pinned note so the fold
injects steering): `steering from a fold that never anchored survives a restart:
"Here's your note to yourself from before compaction:…"` → GREEN.

## Gates (HEAD `dc90ff686`)

`cmd/evener-doctor` is part of the root module, so its gates ride the root ones;
its package suite was also run on its own (`go test -count=1 ./...`, clean).
gofmt clean; host / `-tags evenerfuzz` / `GOOS=windows -tags evenerfuzz` vet exit
0 in both modules; agent `go test -count=1 ./...` zero non-`ok` lines; root
`go test ./...` clean apart from the six pre-existing `internal/selfupdate`
`/private/var` failures (Tasks 86, 91, 103); `-race -count=3` on the agent
fold/compaction set (148.8s) and the doctor reconstruct set (10.2s);
golangci-lint 2.13.1 `--concurrency=2 GOGC=50` **0 issues** in both modules;
overlap test `-count=20` ok (45.3s).

---

# Task 107b — merging main after #1098 squashed

Merge commit `2a163831a` (`--no-ff origin/main` onto `dc90ff686`). Ten conflicts.
Rule applied throughout: main's #1098 form wins; only lines that are #1100's own
work are re-applied on top.

| File | Resolution |
|---|---|
| `agent/session.go` | main's whole file (ambiguous-write reconciliation, emit under the door, `closingOrClosedLocked` refusal), then #1100's two additions re-applied by hand: the `directTurnID` field and `appendDurableTurn` (with `appendTurnWithDurableTranscriptMessage` routed through it, which main's copy had inlined). **Judgement.** |
| `agent/session_lifecycle.go` (3) | main's rollback shape everywhere (`returnAcceptedUserTurn`, `appendUserInputTurnRefusingPoison`), with #1100's naming folded in: `delegatePreseededTurnID`, the preseeded branch adopting that id, and `turn.StableTurnID = s.nameTurnItself()` on the turn main's poison-refusing append now receives. **Judgement.** |
| `agent/delegate_runtime.go` | ours — #1100's `preseedInput` returning `(string, error)` already carries main's error wrap verbatim. |
| `agent/session_compaction.go` (2) | ours — `foldCommit`'s `commitTranscriptsLocked(anchor bool)`, `writesCompactionMarker`, `foldID` are #1100's; main's side was field realignment plus a duplicate `commit := &foldCommit{}` the branch already sets up earlier. |
| `agent/session_init.go` (2) | ours — main's rule plus #1100's two kinds; the branch line is a superset of main's. |
| `agent/session_namer.go` | ours — main's line plus #1100's `OwningTurnID`. |
| `agent/session_queue.go` | theirs — main's new `queueHeadClaimable`; the branch side had only deleted `pushQueueHead`. |
| `agent/session_client_mutation_queue.go` | theirs — main's `returnClaimedDirectClientMutationTurn()` without the caller-floor argument. |
| `agent/session_envctx_test.go` | theirs — main's version asserts the error `maybeAppendEnvironmentContext` now returns. |
| `agent/session_environment_rollback_regression_test.go` (add/add) | theirs — main's file is a strict superset (same three tests plus 26 more from #1098 round 15). |

One more adaptation outside the conflicts: `publishFoldTransaction` gained
main's third return (the fold refusal), so a #1100 test call site takes three
values now — main's signature, #1100's test.

Gates on `2a163831a`: gofmt clean; host / `-tags evenerfuzz` /
`GOOS=windows -tags evenerfuzz` vet exit 0 in both modules; agent
`go test -count=1 ./...` **zero non-`ok` lines**; server, apptranscript,
appprojector, doctor and TUI suites clean; `-race -count=3` over
Compact|Fold|Owner|Steer|Steering|Hook|Environment ok (197.5s); golangci-lint
`--concurrency=2 GOGC=50` **0 issues** in both modules; overlap test `-count=20`
ok (40.9s). Root `go test ./...` fails only the six `internal/selfupdate`
`/private/var` tests (#1201).

---

# Task 112 — round 17

Head was `2a163831a`. Two commits on top; HEAD `0cebded14`, tree clean, nothing
pushed, no amend, no merge. Finding 2 is **NEEDS_CONTEXT**.

## 1. High, nil transcript writer on a hook completion — REFUTED, pinned in `157b0d9e9`

No nil dereference exists. `transcript.Writer.append` returns nil on a nil
receiver (`agent/transcript/transcript.go:455-458`) — the documented no-op every
durable write goes through — and `attachTranscript` marks a session with no
state directory ready **with** a nil writer precisely so held turns are released
rather than accumulating (`agent/session.go:1986-1998`); before that transition
`holdTurnUntilTranscriptReady` queues the turn and reports success
(`:1976-1984`). So a `SessionStart` completion queued before attachment and any
completion after it both take the ordinary path and announce normally. No code
changed; the contract is now pinned instead of assumed, both halves:
`TestHookEndAnnouncedByANonPersistentSession` runs a real non-persistent session
with a SessionStart hook, then drives a completion through a ready session whose
writer is nil. Written as a would-be RED — it passed at `2a163831a`.

## 2. High, the preserved window lost on restart (#1200) — NEEDS_CONTEXT

Reproduced and already reported in Task 91: after one fold a restart resumes
`[SUMMARY]` alone, on this branch and on `origin/main` alike. The reviewer's
shape — re-append `history[cutoff:]` inside the fold's run as tagged replay
copies — cannot be built out of the mechanism this PR has, for one reason:
**the copies must be the PERSISTED forms, never the live turns**
(`agent/session_compaction.go:175-181`; a tool result's live form retains
private API-log evidence the persisted projection replaces, and delegate
delivery commits live only on the persisted form). The fold has persisted forms
only for pairs appended since its own snapshot (`persistedAppendLog`), and the
preserved window is by definition older than that.

Closing it therefore needs one of:

- a durable per-entry identity so a retained live turn can be paired with its
  transcript entry, then kept in (or re-read from) the log. `StableTurnID` is
  set only for named turns — client mutations, environment, attention
  (`agent/session_client_mutation_queue.go:930`, `agent/session.go:1648`) —
  never for the assistant and tool turns that make up most of a window. That is
  a new field on every entry: a **transcript format change**;
- or reading the retained entries back out of the transcript inside the
  publication transaction, with the door held, and pairing them positionally —
  new I/O under `attentionMu` plus a fragile pairing rule;
- or a list on the marker naming the pre-marker entries its anchor keeps, which
  `ResumeHistory` would honour — also a format change.

Each is well past the ~150-line bound and each changes behaviour for every
session already on main, so #1200 does **not** close with this PR. Stopping per
the ruling.

## 3. Medium, round-timing owner — fixed in `0cebded14`

`events.RoundTimings` gains `OwningTurnID` (`json:"-"`, so the item's structured
detail stays byte-identical to the cold side, which builds it from
`schema.RoundTimings`), `persistAndEmitRoundTimings` stamps the entry and the
event from one read of the active turn, and the projector routes the event
through `ownedSystemAnnouncementItem`. The identity-typed conversion
`schema.RoundTimings(timings)` became the `Timings()` method, mirroring
`ContextCompactionData.Compaction()`.
RED `the live round-timing event is owned by "", want the owner its entry
carries, "turn_direct_01M2C9S1…"` → GREEN, plus a projector case asserting an
owned timing lands on its owner while a different turn is open.

## Gates (HEAD `0cebded14`)

`$(go env GOROOT)/bin/gofmt -l` clean across agent, cmd, internal, server — the
three files earlier rounds reported are a local gofmt-version artifact, not real
drift. Host / `-tags evenerfuzz` / `GOOS=windows -tags evenerfuzz` vet exit 0 in
both modules; agent `go test -count=1 ./...` zero non-`ok` lines; server,
apptranscript, appprojector, doctor and TUI suites clean; `-race -count=3` on
RoundTimings|Hook|Compact|Fold (132.3s) and the projector owner set (1.3s);
golangci-lint `--concurrency=2 GOGC=50` **0 issues** in both modules; overlap
test `-count=20` ok (40.8s).
