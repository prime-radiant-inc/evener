# War-game: a flat session scheduler for serf

Adversarial design exploration. Design-only; no code was changed.
Baseline: branch `job-control-spec`, HEAD `5f3c2e7d` ("feat(recursion): drive-down delivery").
Author posture: skeptic of this design. The job is to bend it until it breaks, not to sell it.

---

## 0. The problem, stated in serf's terms

A session takes a "turn" (an LLM call via `Session.ProcessInputKind`, `agent/session_lifecycle.go:260`) only while a goroutine is executing that method. Notifications are durable (`pendingJobNotifs`, `agent/session.go:124`) and a wake (`notify()`, `:290`) is just a kick; the *delivery* happens inside the next `ProcessInputKind(..., EntryNotification)` turn. So a session can only "hear" its inbox while something is running its loop.

Two asymmetric owners exist today:

- **Root.** `serve.go` is a permanent, long-lived owner. The input loop at `cmd/serf/serve.go:376-414` parks on `srv.InputCh()` and runs `sess.ProcessInputKind` whenever a message arrives. The wake is wired by `SetNotifyFunc(func() { srv.SubmitNotification() })` (`serve.go:304`), which pushes a text-less `EntryNotification` onto the 1-slot `inputCh` (`server/server.go:490`). The root is therefore genuinely inbox-driven: its inbox wakes its own permanent loop.

- **Subagents.** A child has **no** long-lived loop. It is spawned (`launchSubagentRun`, `agent/subagents.go:709`), `sub.run` calls `ProcessInput` to completion (`:761`), the goroutine exits, the child goes idle. An idle child cannot hear its own inbox.

The shipped workaround is **drive-down** (recursion spec §3): the child's `notify` is rewired to call the *parent's* `driveSubagentNotificationTurn` (`agent/subagents.go:533`), and the parent — at its own loop boundaries (`drainPendingWatchSends` → `driveChildrenWithUndeliveredAttention`, `agent/job_watch.go:2585`/`2606`) — launches one `EntryNotification` turn on the idle child's drain loop. Correct behavior; but the **parent is the mechanical goroutine-launcher** because the parent owns the child's lifecycle.

The spec defers "persistent always-driven children / a flat session scheduler" with no recorded rationale (§Deferred: "drive-down is the stepping stone: a persistent child is one whose parent drives it continuously").

This doc war-games that flat scheduler.

---

## 1. What the flat scheduler IS

A flat scheduler makes **every** session (root + every subagent) a first-class schedulable entity woken directly by its own inbox, independent of the parent-owns-child tree. The tree still exists for *capability, teardown, and visibility*; it stops being the thing that decides *who runs a session's turn*.

Two concrete shapes:

### (a) One parked loop-goroutine per live session

Each session gets a dedicated goroutine that parks on its own wake channel and runs `ProcessInputKind` when woken — exactly what `serve.go:376-414` is for the root, generalized to every node. `SetNotifyFunc` on a child would push `EntryNotification` onto *that child's own* 1-slot inbox channel, drained by *that child's own* loop.

- **Pro:** minimal conceptual change. It is literally "give every child a serve-loop." The turn machinery (`ProcessInputKind`, the drain-loop gate, `EntryNotification`) is unchanged. The wake path is unchanged in shape — `notify()` already exists; you only change *what it kicks* (own loop, not parent's `driveSubagentNotificationTurn`).
- **Con:** one parked goroutine per live session. With `maxRetainedTerminal = 128` per level (`agent/subagent_manager.go:39`) and recursion, the *live-Session* retention is already ~128^depth worst case (spec §4 acknowledges this). A parked goroutine per *live but idle terminal-retained* Session multiplies that by a goroutine + channel + select each. Goroutines are cheap (~2-8KB stack) but 128^depth of anything is not. The mitigation is to only park goroutines for **non-terminal** sessions (a retained-terminal child has no inbox that can fire — its watches are torn down at finalize), which collapses the count back to "live working set," not "retained history."

### (b) Shared run-queue + worker pool

A central `scheduler` holds a queue of `(sessionID, EntryKind)` wake-tickets. A bounded worker pool pulls a ticket, looks up the session, and runs one `ProcessInputKind`. `notify()` enqueues a ticket instead of kicking a goroutine.

- **Pro:** goroutine count is bounded by pool size, not tree size. Natural place to enforce the tree-wide concurrency cap (the `treeCounter`, `agent/tree_counter.go`, currently dormant) — the pool size *is* the cap. Natural place for fairness policy.
- **Con:** this is a real runtime, not a refactor. You must guarantee **at most one in-flight turn per session** (the entire turn machinery assumes single-threaded ownership of `s.history`, `s.state`, the drain loop — see §4). That means a per-session "running" latch in the scheduler, ticket coalescing, and re-enqueue-on-completion-if-inbox-nonempty. You are rebuilding the `sub.running`/`sub.driving` guard (`agent/subagents.go:79`, `subagents.go:658`) as a scheduler-global invariant. And the root's per-turn cancellable context plumbing (`serve.go:386-407`, `WithQueuedInputDrainOnInterruptHandler`) has to be reproduced per session inside the worker.

### Pick

**(a) parked-loop-per-live-non-terminal-session**, not the worker pool — *if* the flat scheduler is built at all (see §8, where the recommendation is "don't, yet").

Justification: serf's turn engine is already written around "one goroutine owns this session's loop." Shape (a) preserves that ownership model exactly and changes only the *wake target*. Shape (b) is a genuine actor runtime — the mailbox design explicitly lists "No full actor/mailbox runtime conversion. The session loop, serve.go driver, and lock model stay" as a **non-goal** (`docs/.../2026-06-11-job-control-watch-mailbox-design.md:39`). Shape (b) violates that non-goal head-on; shape (a) honors it. The goroutine-cost objection to (a) is real but is solved by the "park only non-terminal sessions" rule, which also happens to be *correct* (terminal sessions have no live inbox).

The honest caveat: shape (a) re-introduces a long-lived goroutine per child, and a long-lived goroutine needs a clean park/unpark/teardown protocol — which is exactly the hard part (§3). Drive-down avoids that entirely by being **edge-triggered and stateless**: there is no parked goroutine to leak, because the goroutine is created on demand and exits when the turn ends.

---

## 2. How the root folds in

Under a flat scheduler the root becomes "the depth-0 scheduled session." But the root has a property no subagent has: it is wired to **external** input (the human / hub) over appwire, via `srv.InputCh()`. That channel carries `EntryUserInput`, `EntryContinuation`, and `EntryNotification` (`server/server.go:477`/`490`). A subagent's inbox carries only `EntryNotification` (and, post-recursion, steering from its parent via `trySteer`).

So "fold the root in" really means: **the root's loop is the prototype the children copy, not a thing that goes away.** `serve.go:376-414` stays — it is the root's per-session loop. What generalizes is the *pattern* (`for { select { wake → ProcessInputKind } }`), instantiated per child inside the agent module.

`SetNotifyFunc` does **not** disappear. Today it has two distinct meanings overloaded onto one method name:

- On the **root**, `SetNotifyFunc` is wired by `serve.go` to `srv.SubmitNotification()` — kick *my own* external loop. (`serve.go:304`)
- On a **child**, `SetNotifyFunc` is wired by the parent to `s.driveSubagentNotificationTurn(sub)` — ask *my parent* to run me. (`agent/subagents.go:533`)

A flat scheduler unifies these: every session's `notify` kicks *its own* inbox/loop. The child wiring at `subagents.go:533` would change from "call parent.drive" to "push onto my own inbox," and the parent's `driveChildrenWithUndeliveredAttention` (`job_watch.go:2606`) would be **deleted** — the child no longer needs anyone to drive it.

But note the subtlety that makes "fold the root in" not free: the root's loop lives in `cmd/serf` and uses `server`. The agent module **must not import server** (stated repeatedly: `serve.go:298`, `session.go:197`, `:273`). So the child's per-session loop must live *inside* package `agent` and must be wired with callbacks, not direct server calls. You cannot literally reuse `serve.go`'s loop; you reimplement its shape in `agent`, which means re-deriving the per-turn cancellable-context dance (`serve.go:386-407`) and the `EntryContinuation` kick handling inside the agent module. That is net-new agent-module code that today does not exist, and it duplicates logic that currently lives correctly in `cmd/serf`.

---

## 3. Teardown vs scheduling — the hard part

This is where the design bends hardest. Today, **the ownership tree and the scheduling tree are the same tree**, and `Close` exploits that identity ruthlessly.

### What Close relies on today

`Session.close` (`agent/session_lifecycle.go:76-182`) does a **synchronous, sequential, recursive cascade**:

1. Take `responseSideEffectsMu` then `mu`, set `closing = true`, `state = SessionClosed`.
2. `subs := s.subagents.drainForClose()` — clears the child map under the manager lock and marks it `closing` (`subagent_manager.go:189`).
3. Release locks; wait for in-flight reconstructions/side-effects.
4. Cancel in-flight LLM calls (`cancelFunc`).
5. Close durable job runtime state.
6. `for _, sub := range subs { sub.sess.close(false) }` — recurse into each child synchronously.
7. Kill child processes; run SessionEnd hooks; emit SESSION_END.
8. `s.sendersWG.Wait()` — **join every detached goroutine** the session launched (subagent runs, the namer, and crucially the drive turns: `driveSubagentNotificationTurn` does `s.sendersWG.Add(1)` at `subagents.go:670` and `defer s.sendersWG.Done()` at `:677`).
9. Close events channel under `eventsMu`.

The teardown ordering is *load-bearing* and depends on three facts that a flat scheduler breaks:

**Fact 1 — the parent's `sendersWG` joins the child's run/drive goroutines.** Because the parent launches the child's goroutines, the parent can *wait for them*. `close()` step 8 (`session_lifecycle.go:173`) is the barrier that guarantees no child goroutine is still emitting when the parent closes its events channel. In a flat scheduler, the child's loop goroutine is **owned by the scheduler, not the parent** — the parent's `sendersWG` no longer covers it. So who joins it on teardown? The scheduler must. That means teardown becomes a **two-party protocol**: the parent says "close this subtree," but the scheduler owns the goroutines that must be quiesced first. The clean sequential `sub.sess.close(false)` recursion (`:114-116`) no longer joins the thing it needs to join.

**Fact 2 — `drainForClose` + `closing` is a one-shot latch against *new* goroutine launches.** The comment at `session_lifecycle.go:85-90` is explicit: "Mark closing BEFORE draining so a spawn or namer launch racing teardown is either registered here (and cancelled below) or observes closing and refuses — there is no window for a late goroutine to escape the drain." This works because **every launch path checks `s.closingOrClosedLocked()` under `s.mu` before `sendersWG.Add`** (`trackAndLaunchPreparedSubagent:580`, `startOrSteerSubagentRun:621`, `driveSubagentNotificationTurn:664`). The parent is the single gatekeeper. A flat scheduler has an *external* actor (the scheduler) that can decide to run a session's turn. Now the latch is not enough: the scheduler could pull a wake-ticket for a session whose parent has just begun closing it. The "no window" guarantee requires the scheduler to also consult `closing` under the *same* lock discipline, on every dispatch — i.e., the scheduler must participate in the session's close handshake. This is a genuinely new synchronization edge that does not exist today.

**Fact 3 — orphan reconciliation is currently lazy and tree-shaped.** Spec §3/§7: a mid-level subtree reconciles "when the mid is next driven or resumed." Drive-down made this *systematic* — the parent drives the mid when undelivered attention exists. The driving is **depth-ordered**: root driven by serve.go, root drives mid, mid (once running) drives its child. The ordering falls out of the tree for free. A flat scheduler removes the ordering: every session is woken directly. That is the *point*, but it means **restore/reconcile can no longer assume a parent has run before a child runs.** Today `prepareSubagentRun` wires the child's whole environment from the live parent (`subagents.go:341-453`: shared task store, `parentSteer`, `forwardJobEvent`, granted job reads, the env). A child woken by the scheduler *before its parent has reconstructed it* has none of that wiring. So the flat scheduler must either (i) guarantee parent-before-child ordering on restore anyway — at which point you've re-introduced the tree ordering you tried to remove — or (ii) make child reconstruction fully self-contained from the `DelegateRestoreDescriptor`, severing the live-parent wiring. (ii) is a large change to how children get their collaborators.

### The teardown war-game verdict

Teardown is the part drive-down gets *for free* and the flat scheduler has to *rebuild*. Drive-down's child goroutine is ephemeral and parent-owned, so `sendersWG.Wait()` + sequential recursive `close` is correct and simple. A flat scheduler's child loop is long-lived and scheduler-owned, so:

- Teardown becomes a handshake: parent marks subtree closing → scheduler must stop dispatching to those sessions → scheduler must join their parked loop goroutines → only then can `close` proceed.
- The `closing`-latch "no late goroutine escapes" invariant (`session_lifecycle.go:85`) must be extended to cover scheduler dispatch, not just parent-side launches.
- A parked loop goroutine that is *blocked in an LLM call* when teardown starts must be cancellable and joinable; today the per-run `runCtx`/`cancelFunc` covers this, but it is owned by the launcher. Ownership moves to the scheduler.

None of this is impossible. But it converts the **simplest, most correct part of the current system** (Close is "unchanged; correct; latency acceptable" per spec §6) into a distributed quiescence protocol. That is a bad trade unless something forces it.

---

## 4. Concurrency hazards

### The single most important invariant: one turn per session at a time

Everything in `ProcessInputKind` assumes the session's loop is single-threaded. `s.history` is appended under `s.mu` but *read-modified* across many calls without holding `mu` for the whole turn; `s.comm` is reset at the top of each `processOneInput` (`session_lifecycle.go:505`) and read at the bottom; the drain-loop gate (`session_lifecycle.go:295-471`) reads and mutates `sessionEndEmitted`, `followups`, `inputQueue`, goal state across iterations. Two concurrent `ProcessInputKind` on the same session would corrupt history and double-deliver notifications.

Today this is guaranteed structurally: the root has exactly one loop (`serve.go`), and a child has the `sub.running` + `sub.driving` guards (`subagents.go:607`, `:658`) — both checked under `sub.mu` before any launch. **The flat scheduler must reproduce this guarantee as a global property of the dispatcher.** Shape (a) gets it for free (each session has exactly one loop goroutine, and a 1-slot inbox coalesces wakes — same as `SubmitNotification`'s drop-if-full at `server.go:492`). Shape (b) must enforce it with a per-session running latch in the scheduler. This is the hazard that kills shape (b) for a casual implementation: it is easy to write a worker pool that occasionally double-dispatches a session under a wake storm.

### Lock ordering

The documented order is `responseSideEffectsMu > mu` (`session.go:72-75`), plus the manager mutex is **outer** to each `sub.mu` (`subagent_manager.go:19-22`), plus `queueEventsMu > mu` (`session.go:101`) and `pendingJobNotifsMu` is a leaf "never taken while holding sub.mu or the manager mutex" (`session.go:122`). The mailbox invariant (§3 of that spec, `:50-51`) is precise: observation paths may take **leaf** locks (`s.mu`, `eventsMu`, `pendingJobNotifsMu`) but **never** `responseSideEffectsMu`, because emit contexts may already hold it.

A flat scheduler adds a **scheduler mutex** (the run-queue / dispatch latch). Where does it sit in the order? It is taken by `notify()` (to enqueue/kick) and by the dispatcher (to pick a session). `notify()` today takes `s.mu` (`session.go:290-296`) and is called from observation contexts (`enqueueJobNotificationAndNotify`, `session.go:238`). So the scheduler mutex would be taken *from within a `notify` that may run under an emit context.* It must therefore be a **leaf** lock, below `responseSideEffectsMu` and ideally not nested under `s.mu` either (or strictly ordered with it). Getting this wrong reintroduces exactly the deadlock class the mailbox design was written to kill (the `2026-06-12-job-control-watch-deadlock-design.md` incident: a watch delivery re-entering session mutation under held locks). **The scheduler is a new global lock on the hottest path (every wake), and the existing lock-order proof does not cover it.** That proof would have to be redone.

### Starvation / fairness

Drive-down has no fairness problem because there is no shared scheduling resource — each parent drives its own children, depth by depth, at its own pace. A flat scheduler with a bounded worker pool (shape b) introduces fairness: under a notification storm, a hot session can monopolize workers and starve a quiet sibling whose single notification waits behind a thousand. Shape (a) avoids pool-level starvation (every session has its own goroutine) but reintroduces it at the **tree-counter** level: the `treeCounter` cap of 16 (`tree_counter.go:15`) means only 16 delegate turns run tree-wide. If 16 hot sessions hold all reservations, a 17th session's notification turn cannot run — and unlike drive-down (where the parent retries the drive at subsequent boundaries, spec §3 "the drive retries at subsequent boundaries"), a scheduler must implement explicit re-enqueue-on-capacity. The retry that drive-down gets from the parent's loop-boundary cadence has to become a scheduler wait-queue.

### Goroutine cost at scale

- Drive-down: O(active drives) transient goroutines. An idle tree costs **zero** goroutines beyond the retained Sessions' own (none — idle children have no loop).
- Flat shape (a): O(live non-terminal sessions) parked goroutines, continuously. Deep/wide trees (the 128^depth retention worst case, spec §4) make this matter *only if* you park terminal-retained sessions — which you must not (they have no live inbox). Parking only non-terminal sessions bounds it to the working set, which the `treeCounter` already caps at 16 running + however many idle-but-non-terminal. Defensible, but not free.
- Flat shape (b): O(pool size) goroutines. Cheapest. But see the single-turn-per-session and fairness hazards.

### Ordering guarantees

Today notifications to a single session are delivered in the order its loop drains them (`drainJobNotifications`, `session.go:253`, FIFO). Across sessions there is no global order and none is promised. A flat scheduler preserves per-session FIFO trivially (the inbox is still per-session). No regression here — but also no improvement, so it is not a *reason* to build it.

### Mailbox invariant preservation

The invariant — "observation persists+wakes; only a session's own loop mutates it; no session synchronously mutates another" — is actually **better served** by a flat scheduler than by drive-down, and this is the one honest point in the scheduler's favor. Under drive-down, the parent's loop boundary is the thing that pumps the child (`driveChildrenWithUndeliveredAttention`, `job_watch.go:2606`): the parent reaches *into the child's lifecycle* to launch the child's turn. The spec is at pains to argue this is invariant-preserving ("a parent never mutates a child; it *runs* the child," §0 closing line) — and it is, technically, because launching `ProcessInputKind` is not mutation. But it is a coupling: the child's liveness depends on the parent's loop cadence. A flat scheduler makes the child woken by *its own* inbox, which is the invariant in its purest form. Drive-down is a faithful-but-coupled approximation; the scheduler is the uncoupled ideal. (This does not make it worth building — see §8 — but it is the strongest pro.)

---

## 5. Migration path from drive-down

Mapping the recursion campaign's drive-down pieces (T14-T16) to a flat scheduler:

| Piece | Today (drive-down) | Under flat scheduler | Verdict |
|---|---|---|---|
| `notify()` + `pendingJobNotifs` + durable queue (`session.go:124-297`) | child arms, kicks parent's drive | child arms, kicks **own** loop | **Generalizes wholesale.** The durable inbox is the substrate either way. This is the mailbox design's core and it is scheduler-agnostic. |
| `SetNotifyFunc` (`session.go:275`) | child→parent.drive; root→server.submit | every session→own loop | **Generalizes**, but the child wiring changes target. The *seam* survives. |
| `EntryNotification` turn + `acceptNotificationInput` (`session_lifecycle.go:821`) | run by parent's drive goroutine | run by own loop goroutine | **Generalizes wholesale.** The turn body does not care who launched it. |
| `driveSubagentNotificationTurn` (`subagents.go:653`) | parent launches child turn, `sub.driving` idempotency latch | **OBSOLETED.** The child's own loop replaces it. The `sub.driving` flag dies. | **Torn out.** |
| `driveChildrenWithUndeliveredAttention` + the boundary reader in `drainPendingWatchSends` (`job_watch.go:2585-2616`) | parent reads child signal at its boundaries, launches drives | **OBSOLETED.** No parent-side signal-reading; children wake themselves. | **Torn out.** |
| The parent-iterates-child-jobManagers caller-send re-token (`drainJobManagerWatchSends`, `job_watch.go:2618-2641`) | parent's drain reaches child-held caller pendings | child's own loop drains its own pendings | **Mostly torn out / simplified.** The "child has no loop while idle" premise (mailbox §4.2, `:89`) evaporates, so the whole "parent's drains are the only driver that can reach child-held pendings" rationale dies. |
| `treeCounter` (`tree_counter.go`, dormant) | will be reserved on spawn/resume/**drive** | reserved on spawn/resume; **the "drive" reservation site disappears**, replaced by the scheduler's own admission control | **Re-homed.** The counter's *purpose* (bound tree-wide concurrency) generalizes; its *call sites* change. Under shape (b) the pool size subsumes it. |

### Incremental or big-bang?

It is **incremental in principle, big-bang in the teardown layer.** The inbox/notify/EntryNotification substrate is already shared, so you could migrate one wake-target at a time. But you cannot half-migrate teardown: the moment a child loop goroutine is scheduler-owned rather than parent-owned, the `sendersWG.Wait()` join (`session_lifecycle.go:173`) and the `closing`-latch invariant (`:85`) must *both* change in the same commit, or close races a live child loop. So the realistic shape is: substrate stays, drive-down's parent-side machinery (`driveChildren*`, `sub.driving`, the boundary reader) is deleted **and** the per-child loop + teardown handshake lands **together**. That is the big-bang core.

### Is drive-down a non-trap stepping stone, as the spec claims?

The spec asserts (§Deferred): "a persistent child is one whose parent drives it continuously." Evaluated adversarially:

**Mostly true, with one trap.** The substrate (durable inbox, notify seam, EntryNotification) is exactly what a scheduler needs and drive-down builds it cleanly. Tearing out `driveSubagentNotificationTurn` and `driveChildrenWithUndeliveredAttention` is *deletion*, not *unwinding* — they are leaf mechanisms with a clean boundary (one launch function, one boundary reader). That is the signature of a non-trap stepping stone: the thing you'd remove is additive, not load-bearing-for-other-features.

**The trap:** drive-down deliberately couples child liveness to the parent's loop cadence, and it leans on that coupling for **ordering** (depth-ordered reconciliation, spec §7: "the mid IS driven by its parent when undelivered attention exists, so post-restore catch-up is now systematic"). Any code or test that comes to *rely* on "a child only runs when its parent's loop runs it" — for restore ordering, for the stop-gate-before-drive sequencing (spec §3 "Stop-gating: a child whose latest record terminated by deliberate stop is not driven"), or for the assumption in `prepareSubagentRun` that the live parent wires the child's collaborators — becomes work the scheduler has to *re-solve*, not just delete. The stop-gate is the sharpest: today the parent checks the stop-gate *before* launching the drive (it owns the decision). A self-waking child must check its own stop-gate before draining — the gate logic moves from the launcher to the wakee. That is a relocation, not a deletion, and it is easy to miss.

So: **non-trap for the wake/delivery substrate; mild trap for the ordering/gating semantics that drive-down quietly provides via tree structure.** The spec's claim is ~80% right. The 20% is §3's teardown/ordering coupling.

---

## 6. Adversarial failure modes

Where the flat design breaks:

1. **Scheduled-while-tearing-down (the headline race).** Scheduler pulls a wake-ticket for session X; concurrently X's parent enters `close()` and `drainForClose()` (`session_lifecycle.go:90`) marks the subtree closing. Today this is impossible — the parent is the only launcher and it holds the latch. Under a scheduler, the dispatcher and the closer are different actors. If the dispatcher checks `closing` *before* the closer sets it but starts `ProcessInputKind` *after*, you have a turn running on a session the closer believes is quiesced, and `close()`'s `sendersWG.Wait()` either deadlocks (waiting on a goroutine it doesn't track) or completes early (closes the events channel under the running turn's feet → send-on-closed-channel panic, the exact thing `close()`'s `eventsMu` dance at `:177` exists to prevent). **This is the single biggest correctness risk and it is structural, not incidental.**

2. **Crash/restore mid-schedule.** A wake-ticket in a worker-pool queue (shape b) is **not durable** — it lives in memory. On crash, the durable `pendingJobNotifs`/`watch_send_pending` records survive (that's the point of the mailbox), but the *scheduling intent* does not. Restore must re-derive every session's "does it have undelivered attention?" from durable state and re-enqueue tickets. Drive-down gets this for free: the parent's next loop boundary re-reads `child.peekNotifications()` (`job_watch.go:2612`) and re-drives. A scheduler must reconstruct its queue from durable inboxes on startup — a new restore step. Miss it and post-crash notifications strand until something else happens to wake the session.

3. **Notification storm + depth-N fan-out.** A wide watch (e.g. `every:1` on `assistant.tool` across many delegates) fires constantly. Drive-down naturally rate-limits: the child can only be driven at the parent's loop boundaries, and `sub.driving` (`subagents.go:658`) makes it strictly one drive in flight per child (the EntryNotification drain self-services the whole queue in that one turn). A flat scheduler must reproduce the "one in flight per session, coalesce the rest" rule. Shape (a) gets it from the 1-slot inbox (drop-if-full, like `SubmitNotification`). Shape (b) must implement coalescing explicitly or it will enqueue N tickets for N fires and burn N turns. The `watchDeliveryBudget` circuit breaker (50, `job_watch.go:50`) is the backstop either way, but a scheduler that doesn't coalesce will hammer the budget far faster than intended.

4. **The tree-counter interaction.** The counter (`tree_counter.go`) reserves a slot per running delegate turn, cap 16. Under drive-down, a drive turn reserves for its duration (spec §4: "§3 drive turns" are a reserve site). Under a scheduler, *every* turn the scheduler launches must reserve before dispatch and release on completion — and if it can't reserve (tree at capacity), it must re-enqueue, not drop. The failure: a scheduler that reserves *inside* the worker (after dispatch) rather than *before* can deadlock if all 16 slots are held by sessions blocked waiting on a sibling that can't get a slot. Drive-down sidesteps this because reservation failure just means "retry at the next boundary" — there is no worker blocked holding nothing. The scheduler must reserve-then-dispatch atomically, which couples the counter to the scheduler mutex (see §4 lock-ordering).

5. **Orphan subtree, uncounted.** Spec §4 already documents: "a detached orphan subtree is uncounted until resumed." Under drive-down, an orphan is simply not driven (no parent loop reaches it) until explicitly resumed — benign. Under a flat scheduler, an orphan with a durable inbox *would* try to wake itself — but its parent-supplied collaborators (`parentSteer`, `forwardJobEvent`, shared task store, granted reads, wired in `prepareSubagentRun:341-453`) are gone. A self-waking orphan that drains a notification and tries to `job_send_message(caller)` finds no caller. This needs an explicit "no live parent → render-to-self or hold" policy that drive-down never needs because an unreachable child is simply never driven.

6. **Re-entrancy through notify.** `enqueueJobNotificationAndNotify` (`session.go:238`) is called from observation paths. If `notify()` now enqueues a scheduler ticket and the scheduler synchronously tries to dispatch on the calling goroutine (a naive implementation), you re-enter session mutation from an emit context — the precise violation the mailbox invariant forbids (`:50`). The scheduler dispatch **must** be asynchronous (hand off to a worker / wake a parked goroutine, never run inline). Easy to get wrong.

---

## 7. Cost / risk estimate (frontier-LLM execution)

**Blast radius — files touched:**

- `agent/session.go` (notify/SetNotifyFunc semantics; new scheduler hooks) — moderate.
- `agent/subagents.go` (delete `driveSubagentNotificationTurn`, `sub.driving`; rewire child `SetNotifyFunc`; relocate stop-gate to wakee) — moderate-to-heavy.
- `agent/job_watch.go` (delete `driveChildrenWithUndeliveredAttention`, simplify `drainPendingWatchSends`'s child traversal, possibly the caller-send re-token) — moderate.
- `agent/session_lifecycle.go` `close()` (the teardown handshake — **the dangerous one**) — heavy.
- `agent/subagent_manager.go` (close/quiesce coordination with the scheduler) — heavy.
- New: `agent/scheduler.go` (the scheduler itself) — net-new, moderate (shape a) to heavy (shape b).
- New per-session loop in `agent` reproducing `serve.go:376-414`'s per-turn context dance — net-new, moderate, and a **duplication** of correct cmd/serf code.
- `cmd/serf/serve.go` — light (root may keep its loop or delegate to the scheduler).
- Restore path (`agent/session_init.go`) — re-derive scheduler tickets from durable inboxes — moderate, and a new correctness-critical step.
- Tests: the entire drive-down test suite (`job_delegate_drivedown_test.go`, `job_watch_deadlock_test.go`, `job_supervision_test.go`, `job_nested_test.go`, the depth-3 agenttest trees from spec §9) must be re-anchored. Many assert "parent drives child" semantics that are deleted.

**Rough size:** ~600-1200 LOC of production change, of which the genuinely hard part (teardown handshake + the scheduled-while-closing race + restore ticket reconstruction) is maybe 200-300 LOC but is **the highest-risk code in the agent module** — it sits on top of the `responseSideEffectsMu > mu` lock proof and the `sendersWG`/`closing`-latch quiescence guarantees that took the mailbox + recursion campaigns significant /par effort to get right.

**Compared to the recursion campaign:** the recursion campaign (allowance, drive-down, counter, include_descendants, stop-cascade — spec tasks through T16) is the larger *feature* surface, but it was built to *preserve* the existing teardown/lock model (spec §6: "Close is unchanged"). The flat scheduler is *smaller in surface* but *deeper in risk* because it deliberately modifies the one thing recursion left alone: who owns a session's goroutine and therefore who tears it down. It is a smaller diff against a more dangerous invariant.

**What could regress:** session teardown (panic-on-closed-channel, leaked goroutines, deadlock on close), the deadlock class the mailbox design killed (via the new scheduler lock on the wake path), post-crash notification delivery (stranded if ticket reconstruction is incomplete), and the stop-gate "no resurrection" guarantee (if the gate relocation to the wakee is botched, a stopped subtree could wake itself).

---

## 8. Recommendation

**Do not build the flat scheduler now. Build it only under a named trigger, after recursion ships and proves a specific need.**

Reasoning:

1. **Drive-down already produces the correct behavior.** The user-visible outcome — an idle child hears its inbox and takes a notification turn — is achieved. The flat scheduler is an *architectural* improvement (uncoupling child liveness from parent cadence), not a *behavioral* one. YAGNI applies hard here.

2. **The cost is concentrated in the most dangerous code in the module.** It modifies teardown, the lock-order proof, and the quiescence guarantees — exactly the invariants that cost the mailbox and recursion campaigns their hardest /par rounds. A smaller diff against a more fragile invariant is a worse bet than a larger diff against a stable one.

3. **The mailbox design explicitly made "no actor/mailbox runtime conversion" a non-goal** (`2026-06-11-...:39`). Shape (b) violates it outright; shape (a) honors the letter but still introduces the long-lived-goroutine-per-session ownership change the non-goal was protecting against. The deferral is consistent with a stated architectural boundary, not an oversight — even though the rationale wasn't archived.

4. **Drive-down is a genuine (mostly) non-trap stepping stone** (§5): its parent-side machinery is deletable, not unwindable, and the durable-inbox substrate transfers wholesale. So waiting costs almost nothing — you are not accruing debt that compounds. The only debt is the ordering/gating semantics that ride the tree (§5 trap), and those are cheap to re-solve *if and when* forced.

**Concrete triggers that would flip this to "build it":**

- **Trigger A — deep idle trees with latency-sensitive notifications.** If recursion ships and real coordinator trees run deep (depth ≥ 3) with long-idle middles, drive-down's depth-by-depth, loop-boundary-cadence delivery imposes serial wake latency: a leaf's notification must wait for root→mid→…→leaf drives to ripple down, each gated on the prior level's loop boundary. If measured end-to-end notification latency on deep trees becomes a complaint, the flat scheduler's direct wake is the fix. *Measure first.*

- **Trigger B — persistent / cross-process children.** The mailbox non-goals name "no cross-process owners, no persistent child sessions (future work; this design must not block them)" (`:41`). If serf grows persistent child sessions (a child that outlives a single tasked run and accumulates work over time, e.g. a long-lived specialist), drive-down's "parent must be looping to drive me" model breaks down — a persistent child *is* a session that needs its own scheduling. That is the natural trigger, and it is exactly the spec's framing ("a persistent child is one whose parent drives it continuously" → just give it its own loop).

- **Trigger C — the parent-cadence coupling causes a correctness bug, not just latency.** If a real scenario surfaces where a child must take a turn *while its parent is blocked in a long LLM call* (the parent's loop boundary is the only drive point, and it won't arrive for minutes), and `job_send_message`/steering can't paper over it, that is a behavioral break drive-down cannot fix without the scheduler.

Until A, B, or C is concretely observed: keep drive-down, ship recursion dark behind the double opt-in, and revisit only with measurements or a persistent-child requirement in hand. Note the recommendation explicitly *against* doing this speculatively "to be clean" — the cleanliness gain (§4 mailbox-invariant purity) is real but does not justify modifying teardown and the lock proof without a forcing function.

---

## Appendix: key symbols cited

- Root loop & wake: `cmd/serf/serve.go:304` (`SetNotifyFunc`→`SubmitNotification`), `:376-414` (input loop + per-turn ctx).
- Server wake sends: `server/server.go:477` (`SubmitContinuation`), `:490` (`SubmitNotification`, 1-slot drop-if-full).
- Turn engine: `agent/session_lifecycle.go:260` (`ProcessInputKind`), `:295-471` (drain-loop gate), `:821` (`acceptNotificationInput`).
- Inbox substrate: `agent/session.go:124` (`pendingJobNotifs`), `:232-297` (enqueue/notify/SetNotifyFunc), `:253` (`drainJobNotifications` FIFO), `:265` (`peekNotifications`).
- Drive-down: `agent/subagents.go:533` (child `SetNotifyFunc`→parent drive), `:653` (`driveSubagentNotificationTurn`, `sub.driving` latch at `:658`); `agent/job_watch.go:2572` (`drainPendingWatchSends`), `:2606` (`driveChildrenWithUndeliveredAttention`).
- Teardown: `agent/session_lifecycle.go:76-182` (`close`), `:85-90` (closing-latch invariant), `:114-116` (recursive child close), `:173` (`sendersWG.Wait()`).
- Tree counter: `agent/tree_counter.go` (dormant, cap 16).
- Retention: `agent/subagent_manager.go:39` (`maxRetainedTerminal=128`), `:189` (`drainForClose`), `:233` (`reserveSlot`).
- Lock order: `agent/session.go:72-75` (`responseSideEffectsMu > mu`), `agent/subagent_manager.go:19-22` (manager outer, sub.mu inner).
- Mailbox invariant: `docs/.../2026-06-11-job-control-watch-mailbox-design.md:46` (the invariant), `:50-51` (what observation may/may not do), `:39` (no-actor-runtime non-goal), `:41` (persistent-children future work).
- Recursion spec drive-down: `docs/.../2026-06-12-recursive-subagents-design.md:40-51` (§3), `:88` (deferral line).
