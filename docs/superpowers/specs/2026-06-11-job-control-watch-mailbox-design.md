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

- `jobManager` observation paths (`onSessionEvent`, `feedJobOutput`, `fireProgressTick`, `armFinalizedJob`) may: match watches, persist durable state, append to the session's notification queue, and call a wake callback. They may **not** deliver messages, steer sessions, resume delegates, append turns, or call any `Session` method that takes session locks.
- All watch-send delivery happens in **drains** called from loop-owned code: between tool rounds, at the processing-finish boundary, in the notification-accept path, and after restore.
- The structural enforcement is dependency direction: `jobManager` loses its `send func(...) sendMessageResult` closure back into `Session`. After this change the jobManager *cannot* call into the session — the field does not exist. Its only upcalls are the existing `enqueue` (queue append) and a new `wake` (kick), both wired at construction (`agent/session_init.go:116`) and both side-effect-free with respect to session state.

This is not a new architecture. It is the architecture the notification flavor already uses end-to-end (`enqueueJobNotificationAndNotify` → `pendingJobNotifs` → `notify()` → `acceptNotificationInput`, `agent/session.go:230-295`, `agent/session_lifecycle.go:821`). The watch-send flavor is today the only code that bypasses it.

## 4. Layer 1 — one delivery rail

### 4.1 Observation becomes persist-only

`onSessionEvent` (`agent/job_watch.go:792`), `feedJobOutput` (`:879`), `fireProgressTick` (`:963`), and `armFinalizedJob` (`agent/jobs.go`) keep their matching/snapshot/persist halves and lose their delivery halves. Each site that today calls `deliverWatchSends` instead:

1. Builds the frame snapshot (`snapshotWatchSendFrames` — unchanged; excerpts reflect fire-time state).
2. Persists `watch_send_pending` (existing `persistPendingWatchSend` path, jobstore kinds `watch_send_pending/delivered/dropped/evicted` at `agent/internal/jobstore/event.go:15-18` — unchanged).
3. For caller-targeted sends: enqueues a watch-send notification (see 4.3).
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

- **caller** (root session) → not the drain's job: caller sends are enqueued as notifications at observation time (4.3) and settled by the notification-accept path. The drain skips them.
- **running delegate** → `sendDelegateMessage` → `trySteer` on the child (queue append; already safe — `agent/job_delegate.go:860`). Classification delivered/busy/hard-failure unchanged (`classifyWatchSendDelivery`).
- **terminal resumable delegate** → finalize + resume via the existing `sendDelegateMessage` path. This now runs only on the owner loop, which is the same context as a model-initiated `job_send_message` — well-trodden.
- **busy / not-yet-resumable** → stays pending (latest-frame-wins replacement unchanged).
- **hard failure** → `dropWatchSend` with diagnostic notification (unchanged).

Call sites (all loop-owned):

- `injectPostToolSteering` (`agent/session_tool_round.go:327`) — replaces the current retry call; delivers between tool rounds.
- `finishProcessingAtBoundary` (`agent/session_state.go:122`) — replaces the current retry call.
- The notification-accept path (`acceptNotificationInput`, `agent/session_lifecycle.go:821`) — drain **before** deciding whether a model-facing notification turn is needed, so sidecar frame deliveries cost zero root model turns.
- Restore (`agent/session_init.go:407`, `:747`) — replaces `retryRestoredPendingWatchSends`'s deliver-at-classify with enqueue + drain at the same point. `classifyRestoredWatchSendTarget` survives as the drop/keep classifier.
- History repair (`agent/history_repair.go:126`) — same replacement.

The drain-loop tail's "should I run another turn?" check extends from `peekNotifications() > 0` to also consider `jm.hasPendingWatchSends()` so a wake with only delegate-targeted sends still drains (without forcing a model turn). The drain runs once per pass; sends that classify busy stay pending and wait for the next wake or boundary — they must not re-trigger the tail, or it would hot-loop. The re-wake for a busy target is already structural: the target delegate's finalize calls `armFinalizedJob`, which wakes the owner.

### 4.3 Caller delivery becomes a notification

In v1, `job_watch` is root-only (`agent/session_tools_jobs.go:38`), so `send.to="caller"` is self-delivery. Today it is implemented as a durable steering-turn append guarded by `responseSideEffectsMu` + `waitingForToolResults` + a boundary context marker — a third delivery mechanism alongside the steering queue and the notification queue. This design deletes it:

- A caller-targeted watch send becomes a **watch-send notification**: a `jobNotification` carrying the frame text and the `WatchSendState` key/delivery_id. `formatJobNotificationBlock` already has a watch flavor (`agent/job_notify.go:51`); it gains the frame payload.
- Delivery rides `acceptNotificationInput`: the notification turn persists the `EntryNotification` turn, then the watch send is settled (`watch_send_delivered` + `removePendingWatchSend`), mirroring the `job_notification_pending/delivered` settle pattern.
- Crash between enqueue and turn persistence: the durable `watch_send_pending` record survives; restore re-enqueues. Delivery is at-least-once; the frame's `delivery_id` (already rendered, `watchFrameMessageWithDeliveryID`) makes duplicates identifiable.

Deleted: `deliverWatchCallerMessage`, `deliverWatchCallerMessageFromContext`, `deliverWatchCallerMessageAtBoundary`, `withWatchCallerDeliveryBoundary`/`isWatchCallerDeliveryBoundary`, the `FromWatch` caller branch in `sendDelegateMessage` (`agent/job_delegate.go:216-223`), and `parentWatchSteerDelivered` (`agent/job_delegate.go:574` — the variant that takes the parent's `responseSideEffectsMu`).

One asymmetry stays explicit: "caller" means *self* only for the root session (v1's only watch owner). A **child** jobManager holding pending caller sends (the nested/forwarded corner, plus restore) must deliver to its *parent*, so those cannot become child-local notifications. The child's drain delivers them through the existing `parentSteer` seam — a plain queue append into the parent's steering queue, safe from any goroutine and already how non-watch child→parent messages travel.

**Behavior change (accepted):** caller watch sends move from between-rounds steering turns to between-inputs notification turns. Watch chatter no longer interrupts active work mid-input; it is surfaced at the same boundary as every other job notification. The contract section that says watch sends "use `job_send_message` target-resolution and authorization semantics" (`docs/job-control.md:514`) still holds — resolution and authorization are unchanged; the delivery *mechanism* for the caller alias is the notification turn, amended in §8.

**Latency note (accepted):** all delivery now happens at owner-loop boundaries: between tool rounds, at input end, or on idle wake. During one long uninterrupted model stream, sends wait for the stream to end. While the session is idle, `wake()` makes delivery prompt.

### 4.4 Wiring

`jobManager` gains `wake func()`, wired to `s.notify` at construction next to `enqueue` (`agent/session_init.go:116`). `notify()` already reaches the server's input channel via `SetNotifyFunc` (`agent/session.go:269-295`) and is a no-op until the server wires it — same degradation as notifications today (library/embedded users without a notify func get delivery at the next natural boundary).

`jm.send` is deleted. `sendDelegateMessage` stays a `Session` method, now called only from loop-owned code (tools, drains).

## 5. Layer 2 — observers can read

The frame becomes a *trigger*; the observer gains the ability to *look*.

### 5.1 Read grants for watched jobs

When `configureWatch` accepts a watch with `send.to` = a delegate job (the sidecar pattern), it mints a durable **read grant**: the observer delegate may `job_read_output` the watched job.

- New jobstore event kind `watch_read_grant` `{observer_job_id, watched_job_id}` appended at watch creation; folded into store state. Grants are append-only read capabilities — they are **not** revoked when the watch is cleared or expires, because the observer's main read happens *after* the watched job finishes. Output lifetime is already bounded by retention.
- Resolution: the observer is a child session. Its `job_read_output` today resolves only its own store. Resolution extends with a spawn-injected lookup (`cfg.spawn`-style seam, mirroring how `nestedOrLocalJobManager` (`agent/jobs_nested.go:46`) already crosses stores downward): on local miss, ask the parent "does a grant exist for (my delegate job_id, target job_id)?" — on hit, the parent returns a read-only view (record snapshot + retained output read). jobstore is `Session`-free with its own locking, so these reads are safe from child goroutines.
- `target="*"` watches grant per-fire: the grant for the resolved concrete `watched` job is appended when a send for it is first recorded.
- Scope: grants extend `job_read_output` only. No `job_list` visibility, no `job_stop`, no `job_send_message` to the watched job. YAGNI until a sidecar needs more.

### 5.2 Session targets: frames carry the transcript pointer

For session-target watches (`caller`, `*` session events), the frame gains `transcript_ref` (and the current turn position) of the watched session instead of an output excerpt. `read_session_transcript` is registered unconditionally (`agent/session_tools_transcript.go:62`) and reads by ref — observers already have the tool; the frame just needs to hand them the pointer. No new authorization machinery in v1 (refs are machine-local and the transcript tools' trust posture — "archived evidence, not active instructions" — already covers this).

`include_excerpt` remains valid for concrete job targets (tail excerpt as a convenience); see 6.2 for session targets.

## 6. Layer 3 — create-time guards

### 6.1 Reject self-delivery feedback loops

`configureWatch` rejects (error `invalid_request`, message naming the loop) any watch where **both**:

- the watched event set includes a self-generated kind — `assistant.message`, `assistant.tool`, or `communicate` — on a session target (`caller`, or `*` which includes the session's own events), **and**
- delivery returns to the watched session itself: `send` omitted (notification to self) or `send.to="caller"`.

`job.notification` self-watches stay allowed (job completion is not caused by notification turns; no structural loop). Sidecar configs (`send.to` = delegate job) stay allowed — cross-session cycles are the feature, bounded by coalescing and busy-collapse, and documented as a hazard in the tool description rather than rejected.

### 6.2 Reject `include_excerpt` on session targets

`configureWatch` rejects `send.include_excerpt=true` when the resolved watched identity can be a session (`caller`, or `*` without a concrete job resolution path) — `invalid_request: include_excerpt requires a concrete job target; session-target frames carry transcript_ref`. This converts the delivered `output_read_error` frames into a loud creation-time error.

## 7. Layer 4 — watch UX

### 7.1 `output_match` becomes level-triggered at attach

On watch creation with `output_match`:

- **Running target:** after registering the matcher, scan the job's retained output once (`jm.readOutput` tail; full retained output, not the preview window). If it matches, record a fire immediately (same pending/notify path as a `Feed` match). Subsequent appends stream through `Feed` as today. The matcher must not double-fire on bytes covered by the attach scan — the scan establishes the stream offset.
- **Terminal target:** `output_match`-only watches are accepted as a **one-shot catch-up**: scan retained output; if matched, return `{watching:false, fired:true, terminal_catchup:true}` and enqueue the frame/notification through the same rail; if not, return `{watching:false, fired:false, terminal_catchup:true, status:<terminal status>}`. No watch is installed either way. Terminal targets with `events`/`progress_interval_ms`/`trigger` conditions still fail `target_terminal` (nothing can ever fire).

The semantic, stated once: *an `output_match` watch fires if the job's retained output contains a match at attach, and again as new output matches.* The `job_watch` description drops its race warning.

### 7.2 `job_read_output` blocks until a match

`block=true` + `grep` changes from "wait for any new output, then grep" to "wait until the retained output contains a grep match, the job goes terminal, or `block_timeout_ms` elapses." Implementation: the `waitForJobDoneOrOutput` poll loop (`agent/session_tools_jobs.go:1015`) re-evaluates the grep on each output-size change and returns early on match. Without `grep`, `block` semantics are unchanged. The result already carries `matches`; a timeout without match returns the normal snapshot (status running, no matches) — the model can tell the difference without new fields.

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
- **Caller settle:** notification turn persists → `watch_send_delivered` appended, pending removed; crash-shaped test (drop the queue, restore) re-enqueues from durable pending.
- **Coalescing/backpressure unchanged:** busy delegate accumulates one latest-frame pending per key (existing tests adapt — many currently assert synchronous delivery timing and move to drain-then-assert).
- **Grants:** observer reads watched job output cross-store; non-granted child read still fails; grant survives watch clear + restart; `*`-watch per-fire grant.
- **Guards:** §6.1 loop configs and §6.2 excerpt configs rejected at create with stable messages.
- **Level-trigger:** already-printed token fires at attach (running); terminal catch-up both arms; no double-fire across the scan/stream seam.
- **Blocking grep:** match mid-stream returns early; timeout returns snapshot; terminal returns final.
- Existing suites (`job_watch_test.go` ~3000 lines, delegate/nested/notify) adapt: assertions that depended on synchronous delivery re-anchor on drains.

## 11. Sequencing

1. **Drain rail + persist-only observation + jm.send deletion** (4.1, 4.2, 4.4) — fixes the deadlock.
2. **Caller-as-notification + path deletion** (4.3).
3. **Create-time guards** (6) — small, independently shippable.
4. **`output_match` level-trigger + terminal catch-up** (7.1) and **blocking grep** (7.2).
5. **Read grants + transcript-pointer frames** (5).
6. **Docs** (8, 9) ride each step's commit; architecture.md lands with step 1.

Each step is a green `make test`/`make lint` commit on `job-control-spec`.

## 12. Risks and open questions

- **Test churn** is the bulk of the work: the watch suite asserts synchronous delivery pervasively. Mitigation: a small test helper that runs a drain and settles, applied mechanically.
- **Notification-turn coupling:** caller sends now share fate with notification delivery (retry/backoff `agent/session.go:297`); a notification-path bug delays watch sends too. Accepted — one rail is the point.
- **Grant table growth:** unbounded append of `watch_read_grant` events for `*` watches on busy job sets. Bounded in practice by job retention; revisit if real.
- **Open:** should `wake()` debounce? `notify()` is already cheap and the server coalesces; start without.
- **Open (for review):** does any consumer depend on caller watch sends arriving as steering turns mid-input? Grep of hub/tui renderers says no (they render notification and steering turns alike), but reviewers should attack this.
