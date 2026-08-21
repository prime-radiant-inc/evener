# A Run Cannot Finish With an Undisposed Background Process

Fixes #297. Supersedes `2026-08-20-end-turn-background-job-guidance-design.md`,
which remains the record of two failed implementation attempts, and replaces an
earlier draft of this file that tuned timeout constants to one benchmark's
handoff distribution. Two adversarial reviews found that draft's numbers were
fit to the wrong run and its central mechanism inverted; the timing layers are
gone.

## The principle

**A coding agent that leaves an orphaned process running has not finished its
job.** That is true interactively, in CI, in an eval harness, and in a delegate.
It is a statement about task completion, not about runtime policy, which is why
it needs no timeout to express.

The earlier draft treated the drain's *wait* as the thing to bound, so every
version of it needed a number — and no number generalizes. An interactive user
wants control back in seconds; a CI job doing a cold `cargo build` needs ten
minutes and must not be interrupted; an eval harness has a fixed per-task
budget. Any constant is wrong for two of those three.

The wait is not the problem. The **undisposed process** is, and the agent is the
only party who can dispose of it. So we tell the agent.

## The failure

Terminal-Bench trial `hf-model-inference__X64mGVa` (run
`tb21-luna-max-89-9ef823040-20260820T185134Z`, binary `9ef823040`) asked for a
Flask API served on `0.0.0.0:5000` in the background. The agent did that
correctly in 149 seconds. The process then sat idle **751 seconds** until the
harness killed it. The server died with the session; the trial scored 0.0, with
1 of 4 verifier tests passing.

Two mechanisms are at fault: the drain waits for every session-owned shell job
and a service never finishes; and the `end_turn` warning promised the job would
"report separately on completion", which under `evener run` is false. The second
is fixed (`cc27d687e`).

## What the evidence supports

**The launch decision is what determines the outcome.** In the Aug 11 run
(`tb21-luna-max-89-8e4fd1181-20260811T153812Z`, binary `8e4fd1181`) the same
task, `hf-model-inference__t7vKYLs`, **scored 1.0** — that agent launched the
server with `mode:"detached"`. Same task, opposite outcome, decided by the mode
argument. No timeout value changes the Aug 20 result: the server dies at exit
whatever the deadline.

**Detach demonstrably works.** Across the run archive, ~20 `mode:"detached"`
launches; every service trial using it scored 1.0 with the verifier connecting
after the agent process exited.

**Prose alone is not sufficient.** `agent/prompts/sections/background-jobs.md:10-12`
already says "Use `detached`, not `background`, for a server", and
`agent/internal/tool/definitions.go:91` already says "Evener stops it when the
session ends; detached is the only mode that lets a process survive".
`git merge-base --is-ancestor 00adf9700 9ef823040` confirms both shipped in the
failing binary. The model had correct, on-point guidance in two places and chose
`background` anyway. Prose is necessary; it is not the mechanism.

**Evidence deliberately not used.** The earlier draft justified a 300s kill from
one run's handoff distribution. That reasoning is withdrawn along with the
constant. Three specific corrections, recorded so they are not repeated:

- `overfull-hbox` was cited as a delegate-owned drain hang. It was not: its root
  session's last entry is 17:48:13 and its delegate ran until 17:55:17, one
  second before the harness kill. The root was correctly waiting on live work.
  There is no observed delegate-owned drain hang in either run.
- The idle-tail instrument cannot see its own worst case. `mailman__s563GPY` has
  the largest dead time in both runs (~1160s and ~1060s) and no
  `trajectory.json`, because it hung and was SIGKILLed, so it is absent from
  every denominator. It is also a promotion case.
- Session transcripts of the failure cohort in the Aug 20 run were mutated after
  the fact by a post-mortem interview pass. `trajectory.json` and `result.json`
  are untouched; any transcript-timestamp analysis over the 0.0 trials is not
  trustworthy without clipping to `agent_execution`.

## Design

### The announcement

When a one-shot session's drain (`TurnEndsProcess`) has nothing outstanding
except live background shell jobs, and no watch is armed on them, deliver a
notification turn stating that the run cannot finish while the job is
outstanding, and naming three remedies:

- **`job_stop`** — it was scratch work; kill it.
- **stop it, then relaunch with `mode="detached"`** — it must outlive the run.
  Stop first: relaunching without stopping leaves the original running, and
  anything binding a port fails to rebind. A detached process's output is
  discarded and it sends no completion notification.
- **`job_watch`** — it terminates on its own and the answer needs its result.

The third option is what makes this safe for the most common long-running
coding-agent workload — *run the test suite, then fix what fails*. At exit time
a running test suite is mechanically identical to a Flask server, and both other
remedies destroy it. Arming a watch is the agent asserting the categorical fact
that separates the two cases: **this one terminates.** Models are good at that
question; they are bad at estimating durations, which is why `max_runtime_ms`
was withdrawn from the shell schema and why nothing here asks for a number.

### Escalation, with no clock

State is a per-job-set announcement count, advanced only on a drain pass where
the condition still holds after the agent has had a turn. Pacing therefore comes
from model turns, not from the 250ms recheck ticker.

1. **First** — announce, three options.
2. **Second** — the set is unchanged and no watch is armed: announce again,
   stating plainly that if nothing is chosen this time the jobs are killed and
   the run exits.
3. **Third** — kill them, report them, exit.

Any of the three remedies resolves it: `job_stop` and the detached relaunch
change the outstanding set; an armed watch suppresses the announcement outright.
Changing the set resets the count.

An armed watch means the drain waits as it does today, which is correct for a
job that terminates. That is not unbounded: `job_watch` auto-clears at
`watchDeliveryBudget = 50` deliveries, after which the condition holds again and
escalation resumes from step 1. No new bound is needed and none is added.

**The armed-watch check must be explicit.** Watch frames currently suppress the
guard incidentally, because they queue as notifications and the drain checks
`peekNotifications()` before the guard block. With a long interval there are
gaps between frames where that queue is empty, and the guard would announce —
and eventually kill — a job the agent explicitly said it was waiting for. Query
the watch registry directly.

### `job_watch` becomes available to any session that can run jobs

Today `rootOnlyJobControlTools = {"delegate", "job_watch"}` and
`agent/session_init.go:1193-1199` strips both when `delegationAllowance <= 0`.
So a leaf delegate can start background jobs but cannot watch them, and would
receive an announcement naming a tool it does not have — the exact failure the
drain's own comment warns about: *"naming a tool the model does not have is
advice it cannot follow."*

**A session must always be able to watch its own jobs.** Remove `job_watch` from
the root-only strip. This opens nothing, because every source is independently
authorized and the strip is defense in depth rather than the enforcement point:

- `parent` — `agent/delegate_tree_watch.go:62-64` rejects it without
  `delegate(watch_parent=true)`, at source resolution.
- a concrete job id — `agent/job_watch.go:836` rejects any job owned by another
  session.
- `dlg_...` — must resolve against delegates this session owns; a leaf has none.

### Never let housekeeping corrupt the run

Independent of any policy, and required regardless:

- **The announcement turn's reply must not become the run's answer.**
  `agent/session_jobtree_drain.go:626-628` folds a drain turn's reply into
  `lastResult`; `cmd/evener/run.go:322` prints it. So
  `evener run "audit the logs" > report.txt` can write "I've stopped the
  aggregation job" into `report.txt` and exit 0. The PRI-2441 rationale for
  preferring a drained result applies to *completion* turns, which carry the
  real answer; a housekeeping turn does not.
- **An error on the announcement turn must not fail the run.**
  `cmd/evener/run.go:321` assigns `err = derr` and `:327` returns before
  printing, so a provider error on a housekeeping turn converts a successful run
  into a failed one with no output.
- Both defects have delegate twins at `agent/subagents.go:1531-1534`, where a
  delegate's task result is replaced by its housekeeping reply and the parent
  then reasons on it.
- **Report what was killed**, with command text and runtime — a bare job id is
  not diagnosable. This goes to **stderr**, alongside the existing warning
  stream, because stdout is the run's answer and appending to it is the same
  corruption as the first bullet. The earlier draft proposed stdout and
  contradicted itself.

## Correctness fixes required regardless

1. **Foreground promotion never marks the record.** `rec.Background` is stamped
   once from `mode == "background"` (`agent/job_shell.go:508`) and never updated
   when a foreground command is promoted at the block timeout, so a promoted job
   — the **default** shell mode — is invisible to any rule keyed on it. Set it at
   the promotion site (`agent/job_shell.go:296-311`) under `jm.mu`, **not** in
   `commitDelayedShell`, which has four callers of which two (`:256` synchronous
   keep, `:434` runtime timeout) are already terminal and would be mislabelled.
   Update the field's doc comment, which claims it is stamped "at launch and
   nowhere else". Note this is *not* what fixes #297 — that job was launched
   explicitly `mode:"background"` — but `mailman`, the largest unmeasured hang in
   both runs, is a promotion.
2. **Two drain returns skip the wake re-check.** The quiescence return re-takes
   the wake edge first (`agent/session_jobtree_drain.go:571-574`) per the
   documented PRI-2441 B1 protocol. Neither the background give-up nor the
   **stall-watchdog give-up at `:666`** does, and both let `Close()` cancel the
   subtree. `treeHasOutstandingWorkBesidesOwnJobs` reads
   `hasPendingRootDelegateAttention` early and `sub.finalizing` late — the exact
   straddle that comment describes — so a delegate packet arming attention in
   that window means the drain returns and `Close()` kills an armed completion.
3. **`TurnEndsProcess` leaks into serve** through the frozen stable-delegate
   descriptor: `agent/subagents.go:622-637` re-takes process-scoped fields from
   the live parent and omits this one, while the sibling restore path
   (`agent/delegate_runtime.go:1322`) correctly re-takes it. It is reconstituted
   at its freeze-time value, so it leaks in both directions.
4. **The grace clock is stale across notification turns.** `armFinalizedJob`
   (`agent/jobs.go:1965-1973`) removes a job from `jm.running` *before*
   `flushNotices` enqueues its notification, so the drain wakes with
   `peekNotifications() > 0`, takes that branch, and `continue`s without
   resetting state. This is the normal path, not a race. Under the design here
   the announcement count must key on the outstanding job-id set so a job started
   during a notification turn starts at step 1.
5. **Prose.** Split `agent/prompts/sections/background-jobs.md:27-30` — "waiting
   costs nothing and is the correct move" is aimed at polling, where it is true,
   and overshoots into "ending your turn to wait is free", which is false; wall
   clock is a real budget. Remove the "such as a dev server" example from
   `docs/job-control.md:256`, which contradicts the prompt. Add to the shell tool
   description that a detached process discards output and sends no completion
   notification — `docs/job-control.md:284` says so but
   `agent/internal/tool/definitions.go:91` does not.

## Validation

- One-shot with a live background shell: announcement names the job and all
  three remedies; a second announcement warns; the third pass kills and exits.
- The same in serve: nothing fires, behaviour byte-identical.
- Arming a watch suppresses the announcement, and keeps suppressing it between
  frames on a long interval (the explicit-check regression).
- After the watch's delivery budget is exhausted, escalation resumes at step 1.
- `job_stop`, and stop-then-relaunch-detached, each resolve it.
- A promoted foreground job is covered identically to an explicit background job.
- A leaf delegate has `job_watch`, can watch its own job, and still cannot watch
  `parent` without the grant or another session's job.
- The announcement turn's reply never becomes the run's printed result; an error
  on that turn does not fail the run; both hold for delegates.
- A killed job is reported on stderr with command text and runtime.
- A background job alongside other outstanding work does not trigger anything.
- Tests must drive the real `ProcessInputKind`. Both previous attempts passed
  their own tests while being inert; one substituted the turn runner and could
  not observe delivery at all.

Acceptance: rerun `hf-model-inference` and score 4 of 4 with the server still
serving when the verifier connects.

## Explicitly not doing

- **No timeout constants.** No `backgroundGuidanceAfter`, no
  `backgroundKillAfter`, no grants, no ceiling. The escalation is driven by drain
  wake-ups paced by model turns.
- **No new tools.** `job_watch` already exists; it becomes available where it was
  wrongly withheld.
- **No model-supplied durations.** The agent answers a categorical question by
  acting, never a numeric one.
- **No recursive subtree predicate.** `runSubagent` already calls
  `DrainJobTree` on every subagent run and the child inherits `TurnEndsProcess`,
  so a delegate runs this same guard on its own jobs one level down. Adding a
  root-level recursive predicate would put a second guard on the same job, and
  the empirical case for it (`overfull-hbox`) was a misreading.
- **No progress- or output-rate trigger.** Quiet does not mean undisposed: a
  training job can emit nothing for many minutes, and a server under a health
  check logs continuously.
