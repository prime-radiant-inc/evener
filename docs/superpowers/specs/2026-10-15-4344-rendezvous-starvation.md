# #4344: Hub CPU saturation starves the daemon rendezvous poller past the client keepalive window

Status: v3 — PAR rounds 1 and 2 folded. Both rounds returned "not implementable as written" while endorsing the verify-and-close scope; round 2 verified every round-1 fold real and supplied the rescue design folded here. Awaiting PAR round 3.
Date: 2026-10-15
Issue: https://github.com/prime-radiant-inc/evener/issues/4344

## The incident (2026-10-11 evening, session 034ZofFCdFFliT15Mqx1bm)

Hub-spawned resume daemons registered within ~100ms, but the hub's
rendezvous stage took 21.5s and 27.1s to see them (lifecycle log
`stage_elapsed_ms`); two other spawns that evening took 1.7s and 2.45s.
The requesting connections died before the resume completed — the hub,
CPU-saturated by its own read storm, had gone silent past the browser
client's app-level heartbeat (20s quiet + 10s unanswered; every received
frame resets it, client.ts:93-94, 271-272) — so the resume "succeeded"
into a dead connection and the reconnect-retry cycle repeated: the
"queues forever" loop.

**A correction the closure comment owes the issue itself:** the issue
body's "The client keepalive budget is 15s ping + 10s pong" misattributes
the hub's own server-side websocket keepalive
(internal/appserver/websocket.go:27-28: keepalivePingInterval=15s,
keepalivePongTimeout=10s — the hub tears down a CLIENT that stops
ponging). The client-side death under hub starvation is the app
heartbeat above. The two budgets coexist; neither alone is "the client
keepalive".

## Post-fix re-examination (v3, corrected per PAR rounds 1+2)

The issue was deliberately sequenced last because the two companion
fixes remove much of the starvation source. Verified against current
main (a666255aa2), every claim code-cited by at least one reviewer and
spot-verified:

1. **The per-read inline rebuild is gone.** A read of a stale session
   answers the structured, retryable "rebuilding" result immediately and
   arms a repair; the repair recipe is the only production full-sidecar
   rebuild. Serialization is **per transcript path** (read-armed repairs
   run one at a time per path and bypass the refresher lane,
   app_threadread_repair.go:251-294); only refresher-fed asks serialize
   hub-wide. N distinct stale sessions read concurrently arm N
   concurrent rebuilds — unbounded hub-wide across paths. For the
   incident's single-session shape, per-path single-flight suffices.
2. **The 10s doomed full past projections are gone.** The full
   O(transcript) projection is off every production read path
   (app_sources.go:262 — the only production `pastThreadForRead` caller
   passes IncludeTurns=false); a stale-session read answers rebuilding
   in milliseconds; the 3-failure budget changes the read's *answer*
   (history-failed vs rebuilding), not the re-arm cadence — read-driven
   re-arms are not paced; the 10-minute re-arm pacing gates only
   refresher asks.
3. **The dispatch queue is bounded.** #4342's high-water refusal
   (>=32/64) turns a saturated hub's retry-safe mutation storm into
   in-band refusals with client-paced retries; teardown no longer
   silently discards queued user intent (the outbox owns it).

The residual risks:

- **R1 — the rendezvous wait is load-fragile.**
  `waitForRendezvousOrExit` (spawn.go:1256-1283) polls the hub-owned
  runDir every 50ms via `rendezvous.List` — `os.ReadDir` plus a read of
  every `.json` entry — running inline on the requesting connection's
  serial worker (thread/resume is serial-dispatched,
  app_threadlifecycle.go:771-773; server.go:1619-1662). Discovery latency
  depends on scheduler headroom and read-side directory IO. (The sidecar
  writer never fsyncs — "the index is derived",
  transcriptindex/index.go:1271-1273 — and a 510MB transcript writes only
  ~4MB of sidecar records, so write-side hammering is NOT part of this
  risk; the pressure is CPU contention plus read-side IO.)
- **R2 — hub-wide slow-read concurrency is unbounded, and the
  per-connection 16-slot semaphore does not carry only cheap work.**
  The cap carries every concurrent-dispatch method, and the code prices
  several of them itself (server.go:1610-1662): evener/jobs/list is
  "seconds and megabytes for a delegate-heavy session", refetched by the
  web UI on every delegate update; evener/update/apply is multi-minute;
  thread/turns/list and evener/subagentPreview scan a full transcript;
  activity/read waits per-host budgets. The per-unit-cheap claim holds
  only for thread/read. A jobs/list-class storm is a live path of this
  issue's exact defect shape.
- **R3 — resume budget and connection-death mechanics (corrected in
  v3).** thread/resume is neither in the retry-safe set nor
  outbox-owned: it rides the requesting connection's serial worker, and
  the client re-issues it on reconnect. Budgets: an explicit browser
  thread/resume is completion-owned on both sides (client.ts:116, 48 —
  RESUME_REQUEST_TIMEOUT_MS = 10 minutes; app_threadlifecycle.go:596 —
  completionOwned: !automatic; spawn.go:695-702 — the 10-minute hub-side
  completion-owned timeout), while the hub's 30s wait default
  (spawn.go:226-229) applies to automatic resumes. Ping is answered
  inline in the receive loop, before the queue (server.go:1030-1036), so
  a slow rendezvous alone does not kill the connection: the incident's
  deaths required process-wide starvation that silenced the connection
  past the client's 20s+10s app heartbeat. There is NO narrow 25-30s
  dead band (a v2 claim, refuted in round 2: it misattributed the hub's
  own websocket keepalive as the client's window).

## Decision (Jesse, 2026-10-15)

**Verify and close.** The deliverable is a storm-shaped regression test
plus closure evidence; no product code change. The companion fixes
removed the incident's sources at their root; the remaining 50ms-poll
fragility does not justify new machinery today. Shelved, with defined
re-arm triggers: Option A (event-driven rendezvous via fsnotify,
already a dependency) if a real-world rendezvous stall recurs; Option C
(a hub-wide expensive-work cap) if a jobs/list-class storm or a
fleet-wide staleness burst (N concurrent per-path repairs) is observed
starving the control plane.

## Deliverable 1 — the merged regression test (v3 design)

**The load source — a continuously re-staled sidecar.** The storm must
keep refusing reads across the whole measurement window. A once-staled
fixture does not: the first refused read arms one repair, the repair
rebuilds the sidecar, and every later read answers real data (the
heal-after-one property the existing suite already evidences,
app_threadread_repair_test.go:343-390). And the staleness must be the
**foreign-projection idiom** (`staleSidecarForTest`,
app_threadread_repair_test.go:91-125 — rewrite the sidecar's projection
meta so a rebuild is required): an append-grown transcript is the
incremental-extension case, which the refusing handle extends inline
(index.go:648-657, 802-809) — it never refuses and never answers
rebuilding. The test therefore runs a background re-staler that flips
the sidecar's projection meta throughout the measurement window, at an
interval that keeps at least one refusal in flight at all times.

**The fixture.** A rounds-parameterized transcript — pinned at design
time to the ~7MB class on the strength of 72c4df05ac's measured
rebuild rates — that BUILDS a real sidecar first (never the
never-indexed shape: a cold read of a never-indexed session answers
history-failed with the registry unwired, the #4654 trap), then stales
it with the foreign-projection idiom. seedLargePastThread as-is does
not qualify (it builds no sidecar); the builder composes it with a
sidecar build. (The delivered test runs ~67MB, not ~7MB: the red's
actual mechanism — one shared rebuild plus per-read response scans —
was measured during TDD and needs the larger fixture to hold the
window past 2x the bound; the implementation notes below carry the
numbers and the reasoning, and the rounds parameter stays the tuning
knob.)

**The flow.** On ONE real appserver connection (not direct dispatch —
that bypasses the appserver machinery the incident exercised):

1. Queue enough thread/reads of the staled storm session ahead of a
   thread/resume. The storm session and the resumed session are
   DIFFERENT sessions: the resume's post-rendezvous roster confirmation
   (ReadSpawnedThread) must not have to read the storm's staled
   sidecar.
2. The resumed session uses the existing fake-daemon pattern (spawn
   test fixtures): rendezvous entry written promptly.
3. Green expectations: reads during the active re-staling window
   answer rebuilding-shaped (retryable, not history-failed — at least
   one such read is asserted, scoped to the window); the armed repairs
   are the REAL recipe (no installRepairSeam no-op); the resume's
   request-to-discovery window completes within **2s** and is recorded.

**The bound.** The resume request-to-discovery window (issue →
rendezvous entry returned) <= 2s: ~40 healthy poll intervals of CI
headroom, a tenth of the incident's starved stages (21.5s/27.1s).
(The v2
requirement to also record the stage elapsed inside the CI test is
dropped: the lifecycle log has no injection seam — it writes to
os.Stderr with no hook, app_threadlifecycle.go:460,
thread_lifecycle_logging.go:37-38 — and capturing os.Stderr races
parallel tests in the shared binary. The stage-elapsed number for the
closure record comes from Deliverable 2's production run, where the
lifecycle log is real.)

**The probe (bite-proof), v3.** Mutate the one-line seam —
`NewCacheWithOpen(..., OpenRefusing)` (app_threadread.go:263) back to
pre-#4343 `NewCache(...)`, whose CatchUp projected inline under the
exclusive lock (verified at 72c4df05ac~1:260, 315-320). With the
re-staler running, each racing read pays a full inline rebuild
(~100-240ms at the pinned scale) instead of refusing in milliseconds,
the resume queues behind them, and the window reddens. **Acceptance:
the probe red must exceed 2x the bound (>=4s) on its first run.**
Without continuous re-staling the mutation heals after one read (the
cache single-flights, cache.go:57-59, 84-112 — one inline open, then
cache hits) and no queued-read count fixes it: that was v2's fatal
flaw, found independently by both round-2 reviewers. (TDD measured the
red's real shape as one shared rebuild plus each read's response scan,
not a rebuild per read — the implementation notes below carry that
correction and the scale it forced; the acceptance stands as written,
met at 7.007s.)

**What the test guards, honestly.** The resume flow's latency under a
per-read-expensive storm of exactly the regression class this issue
guards (someone reintroducing inline projection). The CI probe proves
the test's power at CI scale; the incident's production magnitude —
which CI cannot carry (round-1 arithmetic: poller starvation needs
~200 runnable CPU-bound goroutines per core sustained, >=100MB
transcripts across ~50 connections) — is closed by Deliverable 2's
production-scale datum. The closure record states this split exactly.

## Deliverable 2 — production-scale datum

The issue carries the repro recipe (cold past-path thread/read measured
8.8-10.2s each pre-fix, 16 slow-read slots x several wedged connections,
510MB/15,213-turn session). Re-run it once post-fix against a real hub:
the expectation is milliseconds-class rebuilding answers plus one
detached per-path repair, with the rendezvous stage elapsed recorded
from the real lifecycle log. This is evidence work inside verify-and-close,
not a product change.

## Non-goals

- No change to the rendezvous wait, the repair registry, the request
  queue, the slow-read semaphores, or any client surface.
- No fsnotify watcher, no hub-wide expensive-work cap, no lag advisory
  — shelved with their triggers, not work.
- No attempt to pin scheduler behavior in the abstract; the CI test
  pins the post-fix resume flow's latency under its own storm shape.

## Test plan (TDD order)

1. **Red first:** build the fixture + re-staler + journey test, apply
   the probe mutation, and watch the window exceed 4s — the red IS the
   first run, proving power before trusting green.
2. **Green:** restore the seam, watch the window come in under 2s and
   the rebuilding-shaped read assert hold; record the numbers.
3. **Shape guard:** reads during the re-staling window answer
   rebuilding-shaped (retryable, not history-failed) — the guard's
   justification is the refusal shape itself. (The v3 claim that this
   also catches a never-indexed drift was withdrawn in round 3: with
   the registry armed the never-indexed shape answers rebuilding too,
   so that trap does not distinguish here.)
4. **Both directions of the sustainment pin:** once via the probe
   (mutated reads sustained past 2x the bound) and once in green (the
   drain of the same queued reads finishes well under the bound).

## Closure criteria for the issue (v3)

A comment on #4344 carrying all of:

1. The corrected post-fix re-examination above — per-path (not
   hub-wide) repair serialization, unpaced read-driven re-arms, the
   no-fsync disk story, the slow-read cap's expensive members, and the
   corrected R3 budgets/heartbeat mechanics including the issue body's
   own keepalive misattribution.
2. Residual risks R1-R3 verbatim as corrected.
3. The merged test's measured green window (and the probe's red number)
   at the pinned fixture scale.
4. The probe red/green: fixture scale, the re-staler mechanism, the
   disclosed split (CI-scale power proof vs production-magnitude
   evidence), and the >=4s threshold met — or, if CI cannot sustain
   the threshold on some runner, that miss disclosed alongside item 5,
   which alone carries the magnitude claim.
5. The production-scale datum from Deliverable 2, with its measurement
   and the recorded rendezvous stage elapsed. Required in every case:
   the magnitude claim never rests on the CI probe.
6. The shelved options with their triggers, and #4654 (the broken
   cold-read bench, filed separately).

Items 1-3 plus 5 are required to close as verified; item 4's threshold
miss, if it happens, is disclosed rather than blocking, because item 5
carries the magnitude. (This replaces v2's contradictory fallback.)

## PAR record

**Round 1** (both: "not implementable as written", scope endorsed):
probe-unreachable-at-fixture-scale (both, independent arithmetic);
per-path-not-hub-wide repair serialization (both); unpaced read-driven
re-arms (A1); no-fsync (B); slow-read cap's expensive members (B);
thread/resume naming (B); probe threshold + fallback + production datum
(A); closure residuals verbatim (B); storm sustainment/real-recipe pins
(B); drop healthy-relay leg (A); 2s-bound clock fix (A+B); defined
Option C trigger (A+B); #4654 filed (A). Folded into v2.

**Round 2** (both: "not implementable as written", all 14 round-1 folds
verified real, same two critical flaws found independently): append-
staling never refuses — the foreign-projection idiom is required
(A-F1/B-1, index.go:648-657, 802-809 vs repair_test.go:91-125); the
probe heals after one read — the cache single-flights, continuous
re-staling is required (A-F2/B-2, cache.go:57-59, 84-112,
repair_test.go:343-390); **the v2 R3 fold was itself wrong** — 15s+10s
is the hub's own websocket keepalive, the client heartbeat is 20s+10s
reset by frames, explicit resumes are completion-owned 10 minutes both
sides, the 30s default is automatic-resume-only (A-F3, verified
independently by me at websocket.go:27-28, client.ts:93-94/116/48,
app_threadlifecycle.go:596); closure fallback contradiction (A-F4);
stage-elapsed has no CI seam (A-F5, dropped); shape-assertion scoping
(B); storm session vs resumed session separation (B); seedLargePastThread
builds no sidecar (B); fixture-size and milliseconds inconsistencies
(A-F6/F7). Folded into this v3. Round-2 points: A (unique significant
F3 plus the two shared criticals and the rescue design).

**Round 3** (CONVERGED — both "implementable as written"; the process
proceeds to implementation): all three charge-named blocker candidates
verified safe in code — the re-staler cannot fail an in-flight repair
(a flip lands on the outgoing build's meta and dies at commit; the
recipe checks only the CURRENT build name, index.go:400-409, 1033) or
exhaust the 3-failure budget (history-failed needs 3 consecutive recipe
errors on an unchanged transcript); the refusing handle never blocks
(lockFileTry, index.go:418-428) and lock-busy with a registry repair in
flight answers rebuilding (app_threadread.go:383-385); the heal-after-one
property and the mutated CatchUp path are a faithful reproduction
(extend's empty-build branch rebuilds under the blocking exclusive lock,
index.go:429, 654-661). Implementation annotations folded from the two
reports:

1. **Queue depth (the load-bearing tuning fact):** a serial request
   issued after a slow read runs WITHOUT waiting for it unless the
   16-slot slow-read cap is full (server.go:1874-1877) — only a full
   cap parks the worker so the resume queues behind the storm
   (server.go:2101-2145). The red needs (N-16) x per-read-rebuild >= 4s:
   N ~= 33-53 reads at the ~7MB scale, which fits the 64-frame inbound
   queue (blocking backpressure, never a wire error, server.go:92-100).
   The spec's >=4s red-first acceptance forces this tuning; name it so
   the first probe run is tuned, not debugged.
2. **Re-staler mechanics:** no t.Fatal off the test goroutine; a flip
   racing a build rotation can hit ENOENT on the reader side — a partial
   read classifies as corrupt and refuses, which is the intended
   rebuilding answer; prefer a leaner re-staler that rewrites the
   sidecar meta directly rather than the staleSidecarForTest shape
   (whose non-refusing Open pays an inline heal-rebuild on the re-staler
   goroutine, repair_test.go:99-104); one flip yields one refusal cycle
   (heal-then-stale), so the flip interval must keep one refusal in
   flight at all times.
3. **Shape guard (corrected):** assert EXISTENTIALLY — at least one
   read in the window answers rebuilding-shaped — or assert
   retryable-not-history-failed; a universal rebuilding-typed assertion
   flakes, because a green read can answer the typed locked answer under
   a non-repair lock holder (app_threadread.go:391). The v3 claim that
   this guard also catches the never-indexed drift was wrong: with the
   registry wired, a never-indexed session answers rebuilding too
   (repair_test.go:1424-1449); the guard's justification is the
   shape itself, not the #4654 trap.
4. **Send ordering:** the resume must be sent while the reads are still
   queued (raw-send patterns: app_force_stop_test.go:1146,
   websocket_worker_cap_test.go:38-50).
5. **Timing ceilings:** the green test should stay under the 3s
   per-test ceiling (warn-only for unclassified tests,
   testing-budget.json); at the fast rebuild end (62-67 MB/s real-
   transcript rates, ~104-113ms at 7MB) the red needs the deepest queue,
   so fixture scale and queue depth are tuned together. The 30s
   slow-read stall advisory fires far above the red window; the 64-frame
   queue's blocking backpressure cannot kill the test connection.

Round-3 points: B (the moderate queue-depth finding is the round's
only new load-bearing fact; A's five minors overlap B's 2 and 3).

## Implementation notes (post-TDD, 2026-10-15)

The merged test is `TestResumeRendezvousSurvivesAReadStorm`
(cmd/evener-hub/app_rendezvous_starvation_test.go). Measured numbers:

- **Probe red (the mutation applied): window 7.007s** — 3.5x the 2s
  bound, past the >=4s acceptance, at a 67.3MB/40000-round fixture with
  60 queued reads. Within the window the resume handler itself ran
  54ms (rendezvous poll 50ms — one interval), so the starved component
  is exactly the queue wait behind the read storm. The mutated reads
  answered data (the inline heal's signature).
- **Green (seam restored): window 83.7ms** — 24x under the bound, and
  all 60 reads answered rebuilding (0 locked, 0 data, 0 unexpected, 0
  history-failed), the sustained refusal storm the design wanted. The
  green test runs in ~2.3s, under the 3s ceiling, and passed 3
  consecutive repetitions.

Two facts the runs established that the design arithmetic above did not
carry, both now reflected in the test's tuning comments:

1. **The mutated reads share one rebuild, then heal-immune fast
   extends.** The cache single-flights the open and the healed handle's
   in-memory state (extend checks x.build/x.stale, index.go:650-652)
   never re-reads the re-staler's flipped meta — so the round-3 model
   of "(N-16) serialized rebuilds" overstates the red: the actual
   mutated window is one shared rebuild plus each read's response scan
   (the derived-totals full-transcript pass every data answer pays). A
   once-staled 8.4MB fixture produced a 2.5s window — red, but under
   the 4s acceptance — which is why the fixture scale is the load-
   bearing lever: both terms are O(transcript), so 67MB holds the
   window past 6s with the 60-read queue the 64-frame cap permits.
2. **A resume cannot complete against an unreachable daemon endpoint.**
   Every completion path reads through the spawned daemon's endpoint
   (ReadSpawnedThread's confirm read dials it; a live roster entry dials
   it), so the fake daemon is two pieces: the script binary answers
   launch-check and writes the rendezvous entry, and the entry points
   at an in-process appserver (the app_session_resume_recovery_test
   daemon shape) serving thread/read with the resumed session's
   identity. The rendezvous entry carries protocol, endpoint, session
   and thread ids — the production shape.
