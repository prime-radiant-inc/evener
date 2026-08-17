# Controllable Subagents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** specced, not started, and **safe to execute** — this is the one plan
in the 2026-08-16 turn-control set that has not run. Split out of
`2026-08-16-stop-always-works.md` at Jesse's request because it is larger than
everything else in that plan combined and has a different risk profile.

**Revision:** corrected 2026-08-16 against the source (kata `tb2k`). Two of its
premises were factually wrong about the code — where a delegate runs, and where
descendant capabilities come from — and three of its tasks could be marked done
while a delegate was still uncontrollable. Every claim below was re-checked in
the tree before this revision; each correction is marked where it sits. Steer had
no task at all and now has Task 8.

**Goal:** A subagent (delegate) thread in the web UI renders separated turns,
shows busy while working, and supports Stop and Steer.

**Decision:** Jesse, 2026-08-16, choosing this over "watchable, read-only" and
explicitly accepting the durable write per delegate wake that the current design
avoids.

**Tech Stack:** Go 1.25 multi-module workspace, `appwire` JSON-RPC,
React/TypeScript frontend.

**Spec:** this document.

---

## Why this is the risky one

It puts a **new class of session into the client-mutation store**, which is where
kata `eptj` lost data: two minters sharing one turn-id namespace collided, and
`turn/completed` overwrote a persisted turn's content. Child sessions share the
parent's `StateDir` while having distinct session ids, so the first thing to
establish is that each child gets its own mutation file and that
`reserveClientMutationTurnID` remains the only minter.

## Answer this before writing any code

**What does the parent see when you stop its delegate?**

> **Corrected 2026-08-16 (kata `tb2k`), against the source.** An earlier draft
> opened "a delegate runs *inside* the parent's tool call" and concluded that a
> stop must "synthesise one coherent response for the parent's tool call". Both
> are wrong, and the second would corrupt the transcript it was trying to protect.

**A delegate does not run inside the parent's tool call.** The code says so
outright: `agent/subagents.go:961-965` reads "Subagent execution must outlive the
parent tool-call context", and `:966` builds the run context with
`context.WithCancel(context.Background())`. Stable delegates do the same at
`agent/delegate_tree_start.go:145`, `:205` and `:485`.

Creation returns before the child finishes. `agent/delegate_runtime.go:924`
launches the run and immediately returns a result carrying
`RunningInBackground: true` (`agent/delegate_runtime.go:1533`), and the create
tool refuses to wait at all — `agent/session_tools.go:173-174` rejects
`max_wait_ms` outright.

So there are exactly two cases, and they need different answers:

1. **A background delegate.** Nothing is blocked on it. Its tool call is already
   settled, and writing a second result into a settled call is the transcript
   corruption this question was asked to avoid. Stop it through the existing
   durable delegate outcome and delivery machinery; the parent learns about it
   the same way it learns about any other terminal delegate outcome. **There is
   nothing to return.**
2. **An inline `delegate_send` waiter.** The only call that can still be blocked
   on a child is `delegate_send` with `max_wait_ms > 0`
   (`agent/session_tools.go:156` → `agent/delegate_runtime.go:639-648`). That
   waiter must be resolved. Note it *already* has a graceful degradation for a
   deadline: `agent/delegate_runtime.go:649-652` sets `TimedOut: true` and leaves
   the delegate running in background. A stop needs the analogous shape — resolve
   the waiter, say the delegate was stopped — and it should reuse that path rather
   than invent a second one.

Task 1 is to write the exact wording each case produces, not to re-derive which
cases exist.

## What blocks it today

| Blocker | Where |
| --- | --- |
| Every mutation handler rejects non-root threads with `SessionUnavailable("thread is not served by this daemon")` | `server/appwire_runtime.go:1087-1104` (`requireRootMutationTarget`), called first by **14** handlers: `:736`, `:773`, `:799`, `:822` (`handleAppTurnInterrupt`), `:840`, `:866`, `:891`, `:917`, `:946`, `:974`, `:987`, `:1012`, `:1049`, `:1067`. Re-derive with `grep -c` before scoping work off this — an earlier revision of this row said "ten", from a `grep | head` that silently truncated. |
| Nothing routes an accepted mutation to a child session | the mutation path is wired to the root session only |
| Descendant threads carry **no** capabilities at all | only the root fills them, at `server/appwire_runtime.go:1236`; the descendant list (`:548-552`) and `appThreadForID` (`:640-654`) stamp `ActiveTurnID` and nothing else |
| Child sessions never mint a name — **and removing the `servedByDaemon()` gate is not enough** | `servedByDaemon()` gates the mint (`agent/session_active_turn.go:76-78`) and the boundary emit (`agent/session_lifecycle.go:1671`), but `mintRunningTurnID` is only *reached* for two entry kinds, and a delegate's primary run is neither. See Task 3. |

## Global Constraints

- **Strict TDD.** Every task red first, for the stated reason.
- **One minter.** `reserveClientMutationTurnID`. See `eptj` above.
- **Per-thread capabilities land BEFORE any control is offered.** Nothing lies
  today, so there is no regression to avoid — there is a hole to fill correctly.

  > **Corrected 2026-08-16 (kata `tb2k`).** An earlier draft claimed "a
  > descendant status frame can be stamped with the root's capability set
  > (`stampCapabilitiesOnStatusChange`)". It cannot.
  > `stampCapabilitiesOnStatusChange` (`server/appwire_runtime.go:399-424`) has
  > exactly one call site, `:229`, on the **root** egress path. Descendant egress
  > (`:311`) calls only `stampAppNotificationTarget`, and descendant `thread/read`
  > is assembled at `:258-289` / `:640-654` and never fills capabilities. Only the
  > root sets them, at `:1236`.

  So descendant capabilities are **absent** on notifications
  (`ThreadStatusChangedParams.Capabilities` is `*ThreadCapabilities` with
  `omitempty`, `appwire/types.go:1666`) and **present-but-all-false** on
  `thread/read` (`SerfThread.Capabilities` is a value struct with no `omitempty`,
  `appwire/types.go:276`; `SerfThread` begins at `:266` and is reached through
  `Thread.Serf`, `Thread` itself being at `:216`). Nothing is copied from the
  root.

  **Why this is hard, which is the part worth carrying forward.** There is no
  per-thread source of truth to compute a capability set *from*. `appCapabilities`
  (`server/appwire_runtime.go:1353-1376`) derives every field from server-wide
  state: the callback registrations `s.steerFunc` / `s.steerWithImagesFunc`,
  `s.cancelFunc`, `s.compactFunc`, `s.shutdownFunc`, `s.modelFunc`, `s.nameFunc`,
  `s.queueFunc`, `s.goalFunc`, plus `s.appReservedTurnID` and the `processing`
  flag. Each is one value on the `Server`, wired to the root session. Ask it about
  a descendant and it can only answer about the root — which is why the honest
  thing it does today is not answer at all.

  So Task 2 is not "pass a thread id to `appCapabilities`". It is: give a
  descendant thread a state the capability set can be computed from, then fill
  that set on `thread/read`, on thread lists, and on status notifications,
  enabling **only** the controls genuinely routed to that thread. "Its own
  capability set" is not a sufficient acceptance criterion — copying the root's
  would satisfy it, and so would the all-false set that already ships.
- **Measure the cost.** A durable write per delegate wake, on a path that fires
  per delegate event. If it is worse than expected, say so rather than absorb it.
- **An acceptance criterion that a broken implementation passes is not one.**
  Three of the tasks below originally had that shape — "the mutation is
  accepted", "the descendant has its own capability set", "drop the
  `servedByDaemon()` gate" — each satisfiable with a delegate still
  uncontrollable. Every task now states the observable that fails if the delegate
  is not actually controlled. Keep it that way for any task added later.

## Tasks

- [ ] **Task 1: Write the exact parent-facing wording for both cases** —
      background delegate and inline `delegate_send` waiter — into the section
      above. **Acceptance:** a test per case. For the background case, assert the
      parent's transcript gains **no** new tool result for the stopped delegate's
      original call. For the waiter case, assert the blocked `delegate_send`
      returns and says the delegate was stopped.

- [ ] **Task 2: Per-thread capabilities.** **Acceptance:** for a delegate thread
      whose controls are NOT yet routed, `thread/read`, `thread/list` and
      `thread/statusChanged` all report `steer:false` and `interrupt:false`; once
      routed, all three report `true` **and** a mutation sent against them is
      applied. A test that only asserts "the descendant's set differs from the
      root's" is not enough — an all-false set differs from the root's today and
      is already what ships.

- [ ] **Task 3: Child sessions take durable names on EVERY entry path.** Confirm
      each child gets its OWN mutation file and that the single-minter constraint
      holds. A collision here is `eptj` exactly.

      **Why the `servedByDaemon()` gate is not the whole blocker.**
      `mintRunningTurnID` is only *reached* for two entry kinds:
      `EntryNotification` (`agent/session_lifecycle.go:1040`) and
      `EntryContinuation` (`:1123`). A delegate's initial, resumed and
      owner-input runs are all `EntryUserInput` (`agent/subagents.go:1424-1426`,
      reached from `agent/delegate_runtime.go:924`, `:625` and
      `agent/subagents.go:1129`). That path takes its `StableTurnID` from
      `queuedClientMutationFromContext` (`agent/session_lifecycle.go:1408`, spent
      at `:1472`, `:1486`, `:1505`), and the only writers of that context value
      are the two **root** serve-loop entry points —
      `ProcessClientMutationStart` (`agent/session_client_mutation.go:422`) and
      `ProcessPendingUserInput` (`agent/session_client_mutation_queue.go:206`),
      called only from `cmd/serf/serve.go:1030` and `:1037` — plus the drain-loop
      re-wraps at `agent/session_lifecycle.go:719` and `:841`. A delegate run
      context is rooted in `context.Background()` and carries only lease and
      preseed keys, so the field is `""`.

      **The primary delegate turn therefore stays unaddressable however the
      boundary emit is gated.**

      **Do not reach this by marking a child served.** `servedByDaemon()` is
      exactly `authoritativeConsumer` (`agent/session_active_turn.go:32-36`),
      and its only writer anywhere is `ConsumeEventsLossless`
      (`agent/session_events.go:78`, which sets it at `:92`). So the cheapest
      mechanism available — register the child, let the existing gate open — is
      precisely the one `TestPreparedSubagentGetsNoAuthoritativeConsumer`
      (`agent/session_lossless_events_test.go:238`) exists to forbid, and that
      test states the cost: nothing in this package receives on a child's event
      channel, so a child marked served does not fail, it **wedges** the moment
      its buffer fills. That is a hang under load sitting behind a green suite,
      which is worse than the bug this task fixes. Deleting the test to get past
      it removes the only warning that shape of mistake has. If you take this
      route anyway, you owe the argument for why the wedge cannot happen — not a
      deleted test.

      Name a mechanism that reserves, emits and releases an identity on every
      child entry path — initial, resumed, owner-input, continuation and
      notification. **Acceptance:** one test per path asserting the child's
      published `activeTurnId` is a `turn_m<n>` the child's own mutation store
      holds, not a projector `turn_<n>`. See Task 9 for the audit label and the
      pinned tests this task falsifies.

- [ ] **Task 4: Route accepted mutations to the addressed child, and prove the
      child stops.** Expect `SessionUnavailable` first — that is the routing gate
      at `server/appwire_runtime.go:822`.

      **Acceptance is cancellation, not acceptance.** "A `turn/interrupt` aimed at
      a subagent thread is accepted" is a routing fact; a routed mutation reaches
      none of the cancel channels that actually stop a child:

      - the daemon's interrupt callback cancels the **root** runner
        (`cmd/serf/serve.go:832` → `cancelAndWaitMutationRunner`,
        `cmd/serf/serve.go:662-673`). It reads `mutationRunnerCancel`
        (declared `:646`), whose **only** writer is `setMutationRunner`
        (`:648-651`), called from four places in the root serve loop: `:1010`,
        `:1020`, `:1032`, `:1039`. Note `:1010` is the drain-loop re-arm inside
        `nextTurnCtx` — it re-points the seam at `cancelDrain` mid-drain, and it
        is the path most likely to break when `requireRootMutationTarget` comes
        down. `srv.SetCancelFunc` (`:1009`, `:1019`, `:1031`, `:1038`) sits beside
        these calls but is a **different** seam and does not feed this one; do not
        substitute one for the other;
      - ordinary child runs cancel via `sub.cancel` (`agent/subagents.go:968`,
        `:1341`; used by `cancelAgent` at `:1359-1378`);
      - notification-drive child turns use a local `driveCancel`
        (`agent/subagents.go:1222`, used only at `:1228` and `:1239`) that is
        never stored on `sub`, so `sub.cancel` cannot stop one;
      - stable delegates add a fourth, the controller's per-start record cancel
        (`agent/delegate_tree_stop.go:166-167`, `:293-294`, `:308-309`).

      So this task needs per-child current-turn cancellation and completion-wait
      plumbing for **both** run types. **Acceptance:** the addressed child stops
      while the root and its sibling delegates keep running — assert all three,
      not just the first.

      **Watch the session-scoped interrupt.** `InterruptClientMutation` reads
      `snapshot.ActiveTurnID` as its target (`agent/session_client_mutation.go:464`)
      with the precondition "the session is processing". That is the shape most
      likely to cancel the root by accident once `requireRootMutationTarget` comes
      down. Note `TurnInterruptParams` already carries `threadId`
      (`appwire/types.go:1052-1056`) — that field, not a turn id, is what has to
      select the child.

- [ ] **Task 5: Turn separation and status.** Drop the `servedByDaemon()` gate on
      the boundary emit (`agent/session_lifecycle.go:1671`) and invert
      `TestUnservedSessionAnnouncesNoBoundary`
      (`agent/session_turn_boundary_test.go:599`), which pins today's behaviour
      deliberately. Note this also makes unserved *root* sessions (one-shot
      `serf run`) emit the boundary; state it rather than discover it. This gets
      turn *separation* on notification-driven child turns; it does not name any
      other child turn — that is Task 3. See Task 9 for the EntryKind audit
      label this also invalidates.

- [ ] **Task 6: Measure the added durable-write cost.**

- [ ] **Task 7: Live browser check** on a real delegate: separated turns, busy
      status, **Stop and Steer both work**, and the parent behaves sanely
      afterwards — matching whichever Task 1 case the delegate was in.

- [ ] **Task 8: Steer reaches the addressed child.** The goal promises Steer and
      no other task tests it. **Acceptance:** steering a delegate thread reaches
      that child's next model request exactly once, and reaches neither the root
      nor a sibling delegate.

- [ ] **Task 9: Update the EntryKind turn-opening audit and the tests that pin
      what this plan removes.**

      **Sequencing: this closes with Task 4, not after Task 8.** Its constraint
      on Task 3's mechanism is stated inside Task 3, because it has to be read
      before that mechanism is chosen. This task sits last in the file only
      because renumbering would break the task cross-references above.

      Tasks 3 and 4 falsify a label that nothing in the suite can catch.
      `agent/entrykind_audit_test.go:78` records
      `EntryDelegateAttention: unservedSoUnaddressable`, and the label's own
      definition (`:30-33`) justifies that as "the kind only ever runs on a
      child session, which has no authoritative consumer and takes no client
      mutations". Task 4 makes the second clause false.

      **The audit cannot notice.** Its header says so outright (`:15-20`): it
      checks that every kind carries a label, NOT that the label is true, so the
      table stays green while becoming a lie. This is not a documentation nit —
      that table is what the next person reads to decide how a newly added kind
      gets named, and `entryKindTurnOpening` was built by
      `2026-08-16-one-turn-boundary.md` Task 4 for exactly this purpose.

      Three tests pin behaviour this plan removes. Task 5 names the first; the
      other two went unmentioned until this task:

      | test | where | what it pins |
      | --- | --- | --- |
      | `TestUnservedSessionAnnouncesNoBoundary` | `agent/session_turn_boundary_test.go:599` | the boundary emit — Task 5 inverts it |
      | `TestUnservedSessionNamesNoTurn` | `agent/session_turn_boundary_test.go:576` | the mint gate at `agent/session_active_turn.go:76-78`, which Task 3 must get past. It already carries a note anticipating this plan |
      | `TestPreparedSubagentGetsNoAuthoritativeConsumer` | `agent/session_lossless_events_test.go:238` | that a child is never registered as served. A **constraint on Task 3**, not a test to invert — see Task 3 |

      **Acceptance:**

      - `entryKindTurnOpening[EntryDelegateAttention]` no longer reads
        `unservedSoUnaddressable`, and its replacement is derived from what the
        shipped mechanism actually does. Re-derive it; do not pick whichever
        label makes the table compile.
      - The label's definition comment no longer claims child sessions take no
        client mutations, and no longer cites a test that no longer pins it.
      - Each of the three tests above is deliberately inverted, re-pointed, or
        (the third) left intact with its constraint honoured — each carrying a
        comment naming the task that changed it and why.
      - Reverting Task 3's mechanism makes the changed tests fail again. A test
        inverted into a tautology passes both ways and guards nothing, which is
        the failure this whole task exists to prevent.

## Definition of done

- [ ] `make lint` (nine targets), `make build`, root suite, all seven module
      suites, `make test-web`, all live-stack e2e tests.
- [ ] A live delegate can be **stopped and steered** from the browser, and the
      parent's transcript afterwards is coherent and matches the answer to Task 1
      for the case that delegate was in.
- [ ] Stopping one delegate leaves the root and its siblings running.
- [ ] No control is offered on any thread that cannot honour it — and every
      control that IS offered has been shown to take effect on that thread.
- [ ] The EntryKind turn-opening table (`agent/entrykind_audit_test.go`)
      describes what the code now does, and every test this plan inverted was
      shown to fail again when its task's mechanism is reverted.
