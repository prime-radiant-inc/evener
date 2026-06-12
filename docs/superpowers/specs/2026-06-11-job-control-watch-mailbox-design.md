# Job-Control Watch Mailbox Design

Date: 2026-06-11
Status: draft for adversarial review
Builds on: `docs/specs/2026-06-12-job-control-watch-deadlock-design.md` (incident analysis), `docs/job-control.md` (contract), `docs/superpowers/specs/2026-06-08-job-control-design.md` (implementation design)

## 1. Problem

Session `01KTWN9KEHZ041D77B3GKK572M` deadlocked: a `job_watch(target="caller", events=["assistant.message", ...], send={to:"caller"})` watch fired while the session was emitting `EventAssistantTextEnd` under `responseSideEffectsMu`, and the delivery path re-acquired the same mutex on the same goroutine.

The verified chain:

1. `emitAssistantResponse` holds `responseSideEffectsMu` via `withResponseSideEffects` (`agent/session_model_call.go:267`, `agent/session_state.go:165`).
2. `s.emit` synchronously calls `jobManager.onSessionEvent` (`agent/session_events.go:50-52`).
3. `onSessionEvent` synchronously calls `deliverWatchSends` (`agent/job_watch.go:841`).
4. Delivery reaches `sendDelegateMessage(target="caller", FromWatch=true)` (`agent/job_watch.go:1094`, `agent/job_delegate.go:214-223`).
5. `deliverWatchCallerMessageAtBoundary` re-locks `responseSideEffectsMu` (`agent/session_queue.go:115`). Go mutexes are not re-entrant. Wedge.

The class is broader than the incident: `EventToolCallEnd` is also emitted under the lock (`agent/session_tools.go:333-371`), so a caller-send watch on `assistant.tool` wedges on the next tool call. A latent second instance exists inside the delivery path itself: `deliverWatchCallerMessageAtBoundary` emits `EventSteeringInjected` while holding the lock (`agent/session_queue.go:134`) — harmless only while `steering.injected` is not a watchable kind.

Adjacent defects in the same corner:

- **Feedback loops.** A no-send watch on the session's own `assistant.message` enqueues a notification per match; the notification turn produces another assistant message; with the default `trigger.every=1` this loops forever. The deadlock was masking this.
- **Excerpts on session targets.** `buildWatchFrame` unconditionally calls `readOutput(watchedIdentity)`; for session targets that is `readOutput("caller")` → `output_read_error: job "caller" not found` in delivered frames (`agent/job_watch.go:1897`).
- **Attach races on `output_match`.** A job that exits before the watch attaches returns `target_terminal` (loud). A *running* job that already printed the token before attach silently never fires — `OutputMatcher.Feed` only sees post-attach appends (`agent/internal/jobstore/watch.go:22`). The `job_watch` tool description currently warns the model about a race it cannot avoid.
- **Keyhole observers.** A sidecar observer's entire view is a ≤4KB frame + tail excerpt. It cannot read the watched job's output or the watched session's transcript, so it cannot do real review work.

## 2. Goals

- Make the deadlock class structurally impossible — not avoided by discipline, but inexpressible.
- **Sidecar observers are a first-class v1 feature.** They must work well: cheap to wake, able to read what they observe, safe to compose.
- Preserve the contract's durable delivery semantics: latest-frame-wins coalescing, busy retry, crash-safe pending state (`docs/job-control.md:514`, `:539`).
- Fix the watch UX so the tool needs no race warnings.
- Keep the runtime shape: no persistent per-session goroutines, no actor migration (the 2026-06-01 actor-core spec was sunk for reasons that still hold — `docs/superpowers/specs/2026-06-01-agent-session-actor-core-design.md`).
- Net code should go down in the delivery corner.

## Non-goals

- No full actor/mailbox runtime conversion. The session loop, serve.go driver, and lock model stay.
- No replay of arbitrary historical events. Terminal catch-up applies to `output_match` only.
- No cross-process owners, no persistent child sessions (future work; this design must not block them).
- No event-kind vocabulary expansion.

## 3. The invariant

> **Event observation records durable intent and wakes the owner. Only a session's own loop mutates that session.**

Concretely:

- `jobManager` observation paths (`onSessionEvent`, `feedJobOutput`, `fireProgressTick`, `armFinalizedJob`) may: match watches, persist durable state, append to the session's notification queue, and call a wake callback. They may **not**: acquire `responseSideEffectsMu`, append turns, steer sessions, resume delegates, or otherwise deliver messages.
- The retained upcalls — `emit` (event publication), `enqueue` (notification queue append), `wake` (kick), and `forward` (nested store/notification relay) — are each *publication or queueing*, never delivery. They may take leaf locks (`s.mu`, `eventsMu`, `pendingJobNotifsMu`): the documented lock order is `responseSideEffectsMu > mu` (`agent/session.go:72-75`), so leaf-lock upcalls from an emit context are order-consistent. The forbidden lock is `responseSideEffectsMu`, which emit contexts may already hold. (Precision matters here: `wake` = `s.notify` takes `s.mu` (`agent/session.go:288`), and `jm.emit` → `EventWarning` → Notification hook → `Steer` is a queue append under `s.mu` — both fine under this rule, both fatal under a naive "no session locks" rule.)
- All watch-send delivery happens in **drains** called from loop-owned code: between tool rounds, at the processing-finish boundary, in the notification-accept path, and after restore.
- The structural enforcement is dependency direction: `jobManager` loses its `send func(...) sendMessageResult` closure back into `Session` — the one upcall that performs delivery. With the field gone, observation paths *cannot* deliver; the remaining upcalls are queue appends and kicks wired at construction (`agent/session_init.go:116`).

This is not a new architecture. It is the architecture the notification flavor already uses end-to-end (`enqueueJobNotificationAndNotify` → `pendingJobNotifs` → `notify()` → `acceptNotificationInput`, `agent/session.go:230-295`, `agent/session_lifecycle.go:821`). The watch-send flavor is today the only code that bypasses it.

## 4. Layer 1 — one delivery rail

### 4.1 Observation becomes persist-only

`onSessionEvent` (`agent/job_watch.go:792`), `feedJobOutput` (`:879`), `fireProgressTick` (`:963`), and `armFinalizedJob` (`agent/jobs.go`) keep their matching/snapshot/persist halves and lose their delivery halves. Each site that today calls `deliverWatchSends` instead:

1. Builds the frame snapshot (`snapshotWatchSendFrames` — unchanged; excerpts reflect fire-time state).
2. Persists `watch_send_pending` (existing `persistPendingWatchSend` path, jobstore kinds `watch_send_pending/delivered/dropped/evicted` at `agent/internal/jobstore/event.go:15-18` — unchanged).
3. For caller-targeted sends: enqueues a watch-send wake token (see 4.3).
4. Calls `jm.wake()`.

`armFinalizedJob` additionally stops calling `retryPendingWatchSendsForWatchTarget`/`retryPendingWatchSendsForRunTarget` inline; the wake makes the owner loop drain instead.

### 4.2 One drain, called from loop-owned boundaries

New method, replacing `retryPendingCallerWatchSendsAtBoundary`:

```go
// drainPendingWatchSends delivers pending watch sends. It must only be called
// from loop-owned code: never from event observation, never under
// responseSideEffectsMu.
func (s *Session) drainPendingWatchSends(ctx context.Context) error
```

It asks the jobManager for pending deliveries (`pendingWatchSendDeliveries` — exists), classifies targets, and delivers:

- **caller** (root session) → not the drain's job: caller sends are enqueued as wake tokens at observation time and rendered/settled by the notification-accept path (4.3). The drain skips them.
- **running delegate** → `sendDelegateMessage` → `trySteer` on the child (queue append; already safe — `agent/job_delegate.go:860`). Classification delivered/busy/hard-failure unchanged (`classifyWatchSendDelivery`).
- **terminal resumable delegate** → finalize + resume via the existing `sendDelegateMessage` path, on the owner loop — the same context as a model-initiated `job_send_message`. **Explicit behavior change:** today delegate-targeted pendings are retried only when the *target* finalizes (`armFinalizedJob`, `agent/jobs.go:930-934`); under this design every drain delivers to resumable terminal targets, which is what the contract specifies ("retries when the target is idle or resumable", `docs/job-control.md:514`). A restored pending send therefore resumes its target at the first post-restore drain. `classifyRestoredWatchSendTarget`'s conservative keep-pending applies only at restore-classification time (it drops hard failures and keeps the rest); the drain's live `sendDelegateMessage` preflight is authoritative thereafter — `target_not_resumable` at delivery becomes a hard drop with the standard diagnostic notification, per the contract's drop-with-diagnostic clause (`docs/job-control.md:539`).
- **busy / not-yet-resumable** → stays pending (latest-frame-wins replacement unchanged).
- **hard failure** → `dropWatchSend` with diagnostic notification (unchanged).

The root session's drain also iterates child jobManagers' pending caller sends, exactly as `retryPendingCallerWatchSendsAtBoundary` does today (`agent/job_watch.go:1749-1757`) — a child session has no loop of its own while idle, so the parent's drains are the only driver that can reach child-held pendings (the nested/forwarded corner, plus restore). Child-held caller pendings deliver through the parent's notification rail (4.3) with render-by-key against the *child's* jobManager, and settle there. `parentSteer` is **not** used for watch sends: settling a durable pending against an in-memory steering-queue append would trade crash-safe delivery for a volatile queue.

Call sites (all loop-owned):

- `injectPostToolSteering` (`agent/session_tool_round.go:327`) — replaces the current retry call; delivers between tool rounds.
- `finishProcessingAtBoundary` (`agent/session_state.go:122`) — replaces the current retry call.
- The notification-accept path (`acceptNotificationInput`, `agent/session_lifecycle.go:821`) — drain **before** deciding whether a model-facing notification turn is needed, so sidecar frame deliveries cost zero root model turns.
- Restore (`agent/session_init.go:407`, `:747`) — replaces `retryRestoredPendingWatchSends`'s deliver-at-classify with enqueue + drain at the same point. `classifyRestoredWatchSendTarget` survives as the drop/keep classifier.
- History repair (`agent/history_repair.go:126`) — same replacement.

The drain-loop tail's "should I run another turn?" check extends from `peekNotifications() > 0` to also consider `jm.hasPendingWatchSends()` so a wake with only delegate-targeted sends still drains (without forcing a model turn). The drain runs once per pass; sends that classify busy stay pending and wait for the next wake or boundary — they must not re-trigger the tail, or it would hot-loop. The re-wake for a busy target is already structural: the target delegate's finalize calls `armFinalizedJob`, which wakes the owner.

### 4.3 Caller delivery becomes a notification

In v1, `job_watch` is root-only (`agent/session_tools_jobs.go:38`), so `send.to="caller"` is self-delivery. Today it is implemented as a durable steering-turn append guarded by `responseSideEffectsMu` + `waitingForToolResults` + a boundary context marker — a third delivery mechanism alongside the steering queue and the notification queue. This design deletes it:

- A caller-targeted watch send enqueues a **wake token**, not a frame: a `jobNotification` carrying only the watch-send identity (jobManager ref or child session id, `WatchSendKey`, `UpdateSeq`, `DeliveryID`). The `jobNotification` struct gains these fields; the frame text is **not** copied into the queue.
- **Render-by-key at accept time.** `acceptNotificationInput` resolves each token against the owning jobManager's *current* pending state (`isCurrentPendingWatchSend`): a token whose pending was since replaced (latest-frame-wins), cleared, dropped, or already settled renders **nothing**. A current token renders the pending's current frame via a new watch-send branch in `formatJobNotificationBlock` (the existing watch flavor at `agent/job_notify.go:51` is gated on `JobID == ""` and cannot render concrete-job sends; the new branch keys on the watch-send fields). This is what preserves the contract's latest-frame-wins coalescing (`docs/job-control.md:514`, `:549`) end to end: N fires against a busy caller produce N tokens but exactly one rendered frame — the current one — and stale tokens are skipped, which also prevents delivery-after-drop (a cleared watch's queued tokens render nothing). The durable-notification path already uses this re-read shape (`jobNotificationFromRecord` re-reads the record; tokens mirror it for watch sends).
- After the notification turn persists the `EntryNotification` turn, rendered sends settle (`watch_send_delivered` + `removePendingWatchSend`), mirroring the `job_notification_pending/delivered` settle pattern.
- Crash between enqueue and turn persistence: the durable `watch_send_pending` record survives; restore re-enqueues a token. Delivery is at-least-once; the frame's `delivery_id` (already rendered, `watchFrameMessageWithDeliveryID`) makes duplicates identifiable.

Deleted: `deliverWatchCallerMessage`, `deliverWatchCallerMessageFromContext`, `deliverWatchCallerMessageAtBoundary`, `withWatchCallerDeliveryBoundary`/`isWatchCallerDeliveryBoundary`, the `FromWatch` caller branch in `sendDelegateMessage` (`agent/job_delegate.go:216-223`), and `parentWatchSteerDelivered` (`agent/job_delegate.go:574` — the variant that takes the parent's `responseSideEffectsMu`).

One asymmetry stays explicit: "caller" means *self* only for the root session (v1's only watch owner). A **child** jobManager holding pending caller sends (the nested/forwarded corner, plus restore) must deliver to its *parent*. Those route through the parent-driven model in §4.2: the parent's drain/notification rail discovers them by iterating child jobManagers, renders by key against the child's pending state, and settles there. They never become child-local notifications and never ride the volatile `parentSteer` queue.

**Behavior change (accepted):** caller watch sends move from between-rounds steering turns to between-inputs notification turns. Watch chatter no longer interrupts active work mid-input; it is surfaced at the same boundary as every other job notification. The contract section that says watch sends "use `job_send_message` target-resolution and authorization semantics" (`docs/job-control.md:514`) still holds — resolution and authorization are unchanged; the delivery *mechanism* for the caller alias is the notification turn, amended in §8.

**Latency note (accepted):** all delivery now happens at owner-loop boundaries: between tool rounds, at input end, or on idle wake. During one long uninterrupted model stream, sends wait for the stream to end. While the session is idle, `wake()` makes delivery prompt.

### 4.4 Wiring

`jobManager` gains `wake func()`, wired to `s.notify` at construction next to `enqueue` (`agent/session_init.go:116`). `notify()` already reaches the server's input channel via `SetNotifyFunc` (`agent/session.go:269-295`) and is a no-op until the server wires it — same degradation as notifications today (library/embedded users without a notify func get delivery at the next natural boundary).

`jm.send` is deleted. `sendDelegateMessage` stays a `Session` method, now called only from loop-owned code (tools, drains).

## 5. Layer 2 — observers can read

The frame becomes a *trigger*; the observer gains the ability to *look*.

### 5.1 Read grants for watched jobs

When `configureWatch` accepts a watch with `send.to` = a delegate job (the sidecar pattern), it mints a durable **read grant**: the observer delegate may `job_read_output` the watched job.

- New jobstore event kind `watch_read_grant` `{observer_session_id, watched_job_id}` appended at watch creation; folded into store state. **The grant keys on the observer's *session* identity, not its job_id**: frame delivery to an idle observer resumes it as a *new* delegate job (`attachDelegateJobFromWatch` mints a fresh job_id; `relinkDelegateChildToJob` repoints the child, `agent/job_delegate.go:917-946`), so a job_id-keyed grant would fail on exactly the canonical flow (fire → resume observer → observer reads). The child session id is stable across resumes; `configureWatch` resolves `send.to` → job record → `transcript_ref` → child session id (`decodeRef`) at mint time. Grants are append-only read capabilities — **not** revoked when the watch is cleared or expires, because the observer's main read happens *after* the watched job finishes. Output lifetime is already bounded by retention.
- Resolution: the observer is a child session. Its `job_read_output` today resolves only its own store. Resolution extends with a spawn-injected lookup (`cfg.spawn`-style seam, mirroring how `nestedOrLocalJobManager` (`agent/jobs_nested.go:46`) already crosses stores downward): on local miss, ask the parent "does a grant exist for (my session id, target job_id)?" — on hit, the parent returns a read-only view (record snapshot + retained output read). jobstore is `Session`-free with its own locking, so these reads are safe from child goroutines.
- `target="*"` watches grant per-fire: the grant for the resolved concrete `watched` job is appended when a send for it is first recorded.
- Scope: grants extend `job_read_output` only. No `job_list` visibility, no `job_stop`, no `job_send_message` to the watched job. YAGNI until a sidecar needs more.

### 5.2 Session targets: frames carry the transcript pointer

For session-target watches (`caller`, `*` session events), the frame gains the `transcript_ref` of the watched session instead of an output excerpt. (No turn position: frames are built in observation context, which cannot read session history under the §3 invariant, and no event payload carries one — the observer reads the transcript tail itself, which is `read_session_transcript`'s default.) `read_session_transcript` is registered unconditionally (`agent/session_tools_transcript.go:62`) and reads by ref — observers already have the tool; the frame just needs to hand them the pointer. No new authorization machinery in v1 (refs are machine-local and the transcript tools' trust posture — "archived evidence, not active instructions" — already covers this).

`include_excerpt` remains valid for concrete job targets (tail excerpt as a convenience); see 6.2 for session targets.

## 6. Layer 3 — create-time guards

### 6.1 Reject self-delivery feedback loops

`configureWatch` rejects (error `invalid_request`, message naming the loop) any watch where **both**:

- the **resolved** event-kind set includes a self-generated kind — `assistant.message`, `assistant.tool`, or `communicate` — **and**
- delivery returns to the session that generates those events: `send` omitted (notification to self) or `send.to="caller"`.

"Resolved" is load-bearing, and the guard applies **regardless of target**, for two reasons verified against the matcher: (1) event-kind matching in `onSessionEvent` is independent of target identity — a watch on `target=job_X` with `events=["assistant.message"]` fires on the *owning session's* assistant messages for as long as job_X runs (`agent/job_watch.go:799-834`), so a session-target-only guard leaves the loop expressible; (2) `newWatchConfig` injects `trigger.event` into the matched kinds (`agent/job_watch.go:466-469`), so a config with no `events` list and only `trigger={event:"assistant.message"}` is the same loop — the guard must evaluate events ∪ trigger-derived kinds ∪ the `["*"]` expansion, not the raw `events` array. (That event kinds match independently of watch target is a pre-existing semantic muddle worth revisiting; this spec only closes the loop class.)

`job.notification` self-watches stay allowed (job completion is not caused by notification turns; no structural loop). Sidecar configs (`send.to` = delegate job) stay allowed — cross-session cycles are the feature, bounded by coalescing and busy-collapse, and documented as a hazard in the tool description rather than rejected.

### 6.2 Reject `include_excerpt` on session targets

`configureWatch` rejects `send.include_excerpt=true` when the resolved watched identity can be a session (`caller`, or `*` without a concrete job resolution path) — `invalid_request: include_excerpt requires a concrete job target; session-target frames carry transcript_ref`. This converts the delivered `output_read_error` frames into a loud creation-time error.

## 7. Layer 4 — watch UX

### 7.1 `output_match` becomes level-triggered at attach

On watch creation with `output_match`:

- **Running target:** the scan/stream seam needs a real synchronization protocol — `output.Append(b)` runs *outside* `jm.mu` with `feedJobOutput` taking `jm.mu` afterwards (`agent/jobs.go:544-553`), and `OutputMatcher.Feed` is a carry-buffered line streamer with no notion of position (`agent/internal/jobstore/watch.go:23-48`), so a bare "scan then stream" both double-fires (a chunk appended between scan and its deferred `Feed` is seen by both) and silently misses (a token straddling the scan boundary is split between scan and carry). Protocol:
  1. The per-job output pump is single-goroutine, so store offsets are monotone per job. `appendJobOutput` captures the post-append store offset from `Append` and `feedJobOutput` carries `(chunk, endOffset)`.
  2. At attach, under one `jm.mu` critical section: register the matcher, read the retained length N, and record `scanOffset = N` on the matcher.
  3. The attach scan matches the retained output `[0, N)` line-wise, and seeds the matcher's carry with the bytes after the last newline in the retained tail — so a token half-written at attach completes through `Feed` and still matches (no-miss).
  4. `Feed` discards chunks with `endOffset ≤ scanOffset` and slices a chunk that straddles it (no double-fire).
- **Attach-scan fire cardinality:** the scan fires **once** regardless of how many retained lines match — it is a level check ("the output already contains the pattern"), not a replay of N events. This deliberately diverges from stream behavior (per matching line) because a retro-burst of thousands of fires is abusive; the single fire counts once for `trigger.every` purposes and its frame carries the last matching line.
- **Terminal target:** `output_match`-only watches are accepted as a **one-shot catch-up**: scan retained output; if matched, return `{watching:false, fired:true, terminal_catchup:true}` and route the frame/notification through the same rail; if not, return `{watching:false, fired:false, terminal_catchup:true, status:<terminal status>}`. No live watch is installed either way — which means a catch-up **send** has no home in the pending machinery as-is (`pendingWatchSendDeliveries` iterates only `jm.watches` and `jm.terminalFlush`, `agent/job_watch.go:1817-1851`): the catch-up mints a one-shot detached config with its own generation, registered in `terminalFlush` via the existing `rememberDetachedPendingLocked` machinery so drains and restore can see and settle it; a catch-up *notification* needs none of this and enqueues directly. Terminal targets with `events`/`progress_interval_ms`/`trigger` conditions still fail `target_terminal` (nothing can ever fire).

The semantic, stated once: *an `output_match` watch fires if the job's retained output contains a match at attach, and again as new output matches.* The `job_watch` description drops its race warning.

### 7.2 `job_read_output` blocks until a match

`block=true` + `grep` changes from "wait for any new output, then grep" to "wait until the retained output contains a grep match, the job goes terminal, or `block_timeout_ms` elapses." Implementation on the `waitForJobDoneOrOutput` poll loop (`agent/session_tools_jobs.go:1015`), with two requirements an implementer would otherwise miss:

- **Entry check first:** grep the retained output *before* the first wait — the match may already exist, and the current loop only watches for growth past the entry size.
- **Incremental matching:** re-grepping the full retained buffer (up to 8MB, `maxJobOutputRetentionBytes`) on every 20ms size change is O(n²) for chatty jobs. Each wake greps only the new bytes from the last scanned newline boundary, with a partial-line carry — the same offset+carry shape as §7.1's seam protocol.

Without `grep`, `block` semantics are unchanged. The result already carries `matches`; a timeout without match returns the normal snapshot (status running, no matches) — the model can tell the difference without new fields.

This is the one-call "wait for the server to print ready" primitive; most monitoring needs never touch `job_watch`.

## 8. Contract amendments (`docs/job-control.md`)

| Line(s) | Today | Amended |
|---|---|---|
| 506, 542, 546 | `output_match` fires on output "appended while the watch is active" | Level-triggered: matches retained output at attach, then appended output; no-silent-miss extends to the attach scan |
| 534 | watch fails `target_not_found` for "already-terminal" targets | `output_match`-only watches on terminal retained jobs perform one-shot catch-up; other conditions still fail `target_terminal` |
| 513-514 | send delivery via `job_send_message` semantics; busy → durable latest-frame-wins pending | Unchanged semantics; add: caller-alias delivery surfaces as a notification turn; all delivery occurs at owner-session boundaries (steer/resume for delegate targets unchanged) |
| 516 | `include_excerpt=true` attaches a bounded output excerpt | Restricted to concrete job targets; session-target frames carry `transcript_ref` + position |
| new (§ `job_watch`) | — | Read grants: a send-to-delegate watch grants the observer `job_read_output` on the watched job; grant lifetime/scope per §5.1 |
| new (§ `job_watch`) | — | Self-delivery feedback configs rejected per §6.1 |
| 38 / 369 | `caller` is "available for runtime-originated steering" | Reword: runtime-originated delivery (steering for `job_send_message` from children; notification turn for watch sends) |

Each amendment lands in the same commit as the code that implements it.

## 9. Architecture documentation

- **`docs/architecture.md`**: new section after "How a turn flows" — "Ownership and mailboxes" — stating the invariant (§3), the three queues (steering, job notifications, watch outbox), the wake path, who drains where, and the lock-order rule it protects (`responseSideEffectsMu > mu`, `agent/session.go:72-75`). This is the durable home for the rule "event observers must not mutate sessions."
- **`docs/specs/2026-06-12-job-control-watch-deadlock-design.md`**: resolution addendum — chosen direction (Option 4 implemented as the mailbox the codebase already had; jm.send deleted), pointer to this spec, and the corrections from review (the doc's proposed durable states already existed; the idle-wake hole; the broader deadlock surface; Option 5-as-written previously sunk).
- **`docs/superpowers/specs/2026-06-08-job-control-design.md`**: delta note pointing here for watch delivery, grants, and `output_match` semantics (do not rewrite history in the original spec).
- `job_watch`/`job_read_output` tool descriptions updated (drop the race warning; document grants, blocking grep, transcript_ref frames).

## 10. Testing

TDD per task; the headline regression tests:

- **Deadlock regression:** watch `events=["assistant.message"]`/`["assistant.tool"]` with `send.to="caller"` on a live session whose turn includes tool calls completes the turn and persists `TOOL_RESULTS` (the incident shape; would wedge before this change). Run under `-race` and a watchdog timeout.
- **Invariant test:** event observation paths perform no delivery — after `onSessionEvent`/`feedJobOutput`/finalize, sends are pending, not delivered, until a drain runs.
- **Idle wake:** send becomes pending while the session is idle → wake → delivered without user input; delegate-only sends deliver with zero model turns.
- **Caller settle + render-by-key coalescing:** notification turn persists → `watch_send_delivered` appended, pending removed; N fires against a busy caller → N tokens but exactly ONE rendered frame (the current one); a cleared/replaced watch's queued tokens render nothing; crash-shaped test (drop the queue, restore) re-enqueues from durable pending.
- **Coalescing/backpressure unchanged:** busy delegate accumulates one latest-frame pending per key (existing tests adapt — many currently assert synchronous delivery timing and move to drain-then-assert).
- **Drain behavior change:** a restored pending send to a resumable terminal delegate resumes it at the first drain; a non-resumable target drops with the standard diagnostic; child-jm caller pendings are discovered and delivered by the parent's rail.
- **Grants:** observer reads watched job output cross-store; non-granted child read still fails; grant survives watch clear + restart; **grant survives observer resume under a new job_id** (the canonical fire→resume→read flow); `*`-watch per-fire grant.
- **Guards:** §6.1 loop configs rejected at create with stable messages — including the trigger-only shape (no `events`, `trigger.event="assistant.message"`) and the job-target shape (`target=job_X`, `events=["assistant.message"]`, self-delivery); §6.2 excerpt configs likewise.
- **Level-trigger:** already-printed token fires at attach (running), exactly once regardless of matching-line count; a token straddling the attach boundary still matches (carry seeding); no double-fire across the offset seam; terminal catch-up both arms, including settle of a catch-up send via the detached terminalFlush config.
- **Blocking grep:** match already present before the first wait returns immediately; match mid-stream returns early; timeout returns snapshot; terminal returns final.
- Existing suites (`job_watch_test.go` ~3000 lines, delegate/nested/notify) adapt: assertions that depended on synchronous delivery re-anchor on drains.

## 11. Sequencing

1. **The delivery rail, whole** (4.1-4.4): persist-only observation + drain + jm.send deletion + caller-as-notification + path deletion, in one step. These cannot split: the drain skips caller sends by design, so a "rail without 4.3" commit leaves caller sends persisted but undeliverable and the watch suite's caller-delivery assertions red. This step fixes the deadlock.
2. **Create-time guards** (6) — small, independently shippable.
3. **`output_match` level-trigger + terminal catch-up** (7.1) and **blocking grep** (7.2).
4. **Read grants + transcript-pointer frames** (5).
5. **Docs** (8, 9) ride each step's commit; architecture.md lands with step 1.

Each step is a green `make test`/`make lint` commit on `job-control-spec`.

## 12. Risks and open questions

- **Test churn** is the bulk of the work: the watch suite asserts synchronous delivery pervasively. Mitigation: a small test helper that runs a drain and settles, applied mechanically.
- **Notification-turn coupling:** caller sends now share fate with notification delivery (retry/backoff `agent/session.go:297`); a notification-path bug delays watch sends too. Accepted — one rail is the point.
- **Grant table growth:** unbounded append of `watch_read_grant` events for `*` watches on busy job sets. Bounded in practice by job retention; revisit if real.
- **Append→Feed offset plumbing** (§7.1) touches the output pump path (`agent/jobs.go:544-553`); it is small but on the hot path of every job — keep it allocation-free.
- **Open:** should `wake()` debounce? `notify()` is already cheap and the server coalesces; start without.
- **Resolved by adversarial review:** no consumer depends on caller watch sends arriving as steering turns mid-input (hub/tui render notification and steering turns alike; nothing in test/scenarios or docs depends on mid-input delivery), and the drain-tail extension cannot hot-loop (the `ranKind != EntryNotification` gate bounds it to one no-op pass).
