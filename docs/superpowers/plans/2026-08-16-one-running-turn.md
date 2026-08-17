# One Running Turn Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Supersedes** `2026-08-16-busy-means-named.md`, which tried to keep four
representations of "the running turn" in step with each other. Two independent
reviews found that most of its tasks existed only to patch seams between those
four, and that several of its central premises were false. This plan removes the
seams instead. The superseded document's task list is not safe to execute; its
rejected-options record is preserved below.

**Goal:** It is always possible to steer or stop a session that is running.
Not as a property maintained at eleven call sites, but as one the code cannot
express the negation of.

**Architecture:** A running turn becomes a thing with an owner instead of an
agreement between four variables. One handle is created where a turn opens, owns
that turn's durable name for exactly its lifetime, is the only thing that tells
the daemon a turn is running, and releases the name when it closes. The serve
loop stops writing `processing`; the projector stops inventing names for real
turns.

**Tech Stack:** Go 1.25 multi-module workspace (`agent` module, root module's
`internal/appprojector` / `server` / `cmd/serf` / `cmd/serf-hub`), `appwire`
JSON-RPC, React/TypeScript frontend.

**Spec:** this document.

**Prior art:** `2026-08-16-one-active-turn-identity.md` (goal continuations) and
`2026-08-16-one-turn-boundary.md` (notification wakes) are landed and correct;
they fixed two turn kinds by teaching the projector to adopt the daemon's name.
This plan generalises that from "two kinds adopt" to "there is only one name."
Katas: `c2ty` (the window and Jesse's ruling), `2f41` (rejections are illegible),
`7vmd` (the notification turn), `eptj` (the data-loss precedent).

---

## The diagnosis

"The currently running turn" has no owner. It is an emergent agreement between:

| Representation | Written by | Where |
| --- | --- | --- |
| `server.processing` (bool) | the serve loop goroutine | `cmd/serf/serve.go:1001,1014,1031` |
| `server.appActiveTurnID` (string) | two different setters | `server/server.go:691,715`, `server/appwire_runtime.go:212` |
| `projector.activeTurnID` / `reservedTurnID` | the event goroutine | `internal/appprojector/appwire_projection.go` |
| `clientMutationSnapshot.ActiveTurnID` (durable) | **eight** sites | `session_client_mutation.go:246,566`, `session_client_mutation_queue.go:1043,1136`, `session_active_turn.go:70,92`, `session_client_mutation_persist.go:83`, `session_queue.go:581` |

Four representations, three goroutines, six pairwise seams. Every kata in this
family is one of those seams disagreeing, and every fix attempted so far has
closed one seam and opened another.

A second, compounding error: **the serve loop is shaped as though one message is
one turn.** It is not. `processInputKindWithProvenance`'s drain loop
(`agent/session_lifecycle.go:642-880`) runs follow-ups, queued drains,
interleaved notification turns and a deferred goal continuation inside a single
`ProcessInputKind` call. Any design that scopes a turn's identity to the message
is wrong before it is written, which is what happened to the superseded plan.

## Two false premises the superseded plan was built on

Both verified; both are why that plan could not have worked.

1. **`SetProcessingTurn` publishes nothing.** It mutates `s.processing` and
   `s.appActiveTurnID` under `s.mu` and returns (`server/server.go:690-700`).
   The only producer of `thread/status/changed` in the daemon is the projector
   (`appwire_projection.go:1292-1298`; the `server/appwire_runtime.go:358,400`
   sites decorate frames already produced). So a watching client learns "busy" at
   the turn's **first event** — which is exactly when the window that plan
   claimed to close already ends. Its central mechanism affected only
   `thread/read`.
2. **`POST /input` has no production client.** The web UI routes every composer
   action through `turn/start` (`stores/threads.ts:1929`) and so does the TUI
   (`cmd/serf-tui/hub_model.go:232`). The one in-repo caller is a live-scenario
   doc. So that plan's Task 1 changed what `thread/read` would say on a path
   nothing reads, and justified it with a user-visible win no user could reach.

## Jesse's ruling (2026-08-16)

> "It should always be possible to steer or stop a session that's running."

This settles kata `c2ty`, reversing its 2026-07-26 ruling. A button that does not
work and a button that is not there are the same failure.

`turn/interrupt` hard-requires a non-empty `expectedTurnId` equal to
`ActiveTurnID` (`agent/session_client_mutation.go:414-425`) — there is no
"stop whatever is running" escape hatch. So the ruling reduces to a single
invariant, which is what this plan builds:

> **A turn that is running has a durable name, or it does not run.**

## Global Constraints

- **No backward compatibility.** Delete the superseded path.
- **One minter.** `reserveClientMutationTurnID`
  (`agent/session_client_mutation_queue.go:642`). Kata `eptj` is what two
  minters in one namespace did: a collision made `turn/completed` overwrite a
  persisted turn's content. Real data loss. No task may add a second mint site.
- **Turn identity is scoped to a turn, never to a message.** See the diagnosis.
- **`turn_<n>` names non-turns only.** The prelude and the announcement gaps
  between turns (`preTurnAnnouncementTurnID`, katas `bz2z`, `9ekv`) keep it.
  Nothing that is a turn may carry one.
- **Every production line added must be killed by a named test**, verified by
  reverting the line and watching a specific test fail. Note that
  `invariant.Hold` is a **no-op** outside `-tags serffuzz`
  (`invariant/invariant.go:23-32`), so an invariant is never a substitute for a
  test.
- **Live-stack proof for the user-visible claim.** Unit tests do not demonstrate
  "Steer and Stop work."

---

## Already landed (keep)

Verify with `git log --oneline 6af43a95a..HEAD`. These stand on their own merit
and are not affected by the change of approach.

| Commit | What |
| --- | --- |
| `457369f11` | The live-stack tests stop leaking ~118MB of Go module cache per run. **The bug is on `main` and the fix is not** — see kata `tx4q`. |
| `ff189e7fe` | Three production lines that no test killed. |
| `1faaa6266`, `8bf6858e6` | `openTurn`: one copy of the boundary sequence, not three. |
| `8360363c1`, `c0692a4e4`, `f40904960` | A wake stands down rather than run a turn it cannot name; the name is taken atomically, before any wake state is consumed; the stand-down settles and re-arms without spinning. |

The stand-down is the invariant above, applied at one entry kind. This plan
generalises it; do not remove it.

---

## The design

One type, owned by the agent:

```go
// runningTurn is the turn that is executing right now. It exists only while a
// turn is running, owns that turn's durable name for exactly that long, and is
// the only thing that reports a turn running. There is no way to be busy
// without one, which is the whole point.
type runningTurn struct {
    id string // the durable turn_m<n>; never empty for a served session
}
```

Created at the single site a turn opens (`processOneInput`), closed when that
turn ends. Its lifetime is one iteration of the drain loop, so the message-vs-turn
mismatch cannot recur.

**Who learns about it, and how:**

- The **daemon** learns through a callback the session invokes when a turn opens
  and when it closes — the seam `ProcessClientMutationStart` already uses
  (`cmd/serf/serve.go:1022-1028`). Per-turn by construction. `serve.go` stops
  calling `SetProcessing(true)` for turn starts.
- The **projector** learns through the opening event, as it already does. It
  always adopts; it never mints a name for a turn.
- The **read path** derives both status and name from the same handle, rather
  than from `processing` and `appActiveTurnID` independently
  (`server/appwire_runtime.go:1198,1238`).

This is the callback shape the earlier "option (a)" described. It was rejected
then for opening an idle-looking window that would let a fast second message
bounce; that reasoning is void, because `SetProcessingTurn` never published
anything, so the alternative had no window advantage to trade against.

## Open design calls — decide before Task 3

Both are Jesse's, and both are recorded here rather than assumed.

1. **What happens when a turn cannot be named?** `mintRunningTurnID` returns
   empty under an `InterruptFence`, on a store failure, and for an unserved
   session (`agent/session_active_turn.go:47-77`). The invariant says such a
   turn does not run. Options: (a) refuse the input, which is what the landed
   stand-down does for wakes and would extend to every kind; (b) run it, but
   report the thread idle so no control is offered — which is the "button that
   is not there" the ruling rejects; (c) run it under a non-durable name, which
   reintroduces a second namespace. Recommendation: (a), with the store-failure
   case surfaced as a fault rather than swallowed.
2. **Do descendant (subagent) threads get turn boundaries and status?** They are
   unserved, so they can take no client mutation and need no name — but they do
   have a rendered projection a client reads, and today the unconditional
   `threadStatus(Active)` (`appwire_projection.go:241`) is the only reason a
   subagent thread reads busy. Making the status gate uniform without deciding
   this regresses every subagent thread to permanently idle. Recommendation:
   descendants get boundaries (turn separation is a rendering property) and
   status, but never a durable name.

---

## Task 1: Make the invariant assertable

Nothing can be verified until "the wire says busy" and "the wire names an
addressable turn" can be compared in one place, on both the push and read paths.

**Files:** create `server/running_turn_invariant_test.go`

- [ ] **Step 1:** Write a helper that, given a live server, returns
      `(statusType, activeTurnID)` from `thread/read`, and a second that
      collects the same pair from pushed frames.
- [ ] **Step 2:** Write the invariant assertion: status `active` implies a
      non-empty id with the `turn_m` prefix. Note that the push-path status
      frame carries no id at all (`threadStatus`, `appwire_projection.go:1292-1298`),
      so on that path the property is adjacency of `turn/started` and
      `statusChanged` — assert it as such rather than as a field check.
- [ ] **Step 3:** Apply it to a goal-continuation turn and a notification turn.
      Both should pass (their plans landed). Commit the harness green.
- [ ] **Step 4:** Apply it to a turn started through the drain loop's queued
      path. Expect FAILURE. That failing test is this plan's target.

## Task 2: Delete the dead reservation machinery

Independent of everything else; do it early to shrink the surface.

`turn/start` moved to the durable client-mutation path in `954e5ff93`, and
`handleAppTurnStart` (`server/appwire_runtime.go:735`) goes entirely through
`retrySafeTurns.Start`. The old mechanism was left standing:
`reserveAppTurnIDForStart` and `releaseAppTurnID` have **no production callers**,
so `appReservedTurnID` is never non-empty in production.

- [ ] Delete both functions, the field (`server/server.go:280`, writes at `:697`,
      `:717`), and its reads at `appwire_runtime.go:119`, `:1039`, `:1371`,
      `:1412-1418`, `server_handlers.go:241`. Both surviving readers refuse a
      model change while active-or-reserved and reduce to `if processing`;
      `appCapabilities`'s `active` reduces the same way.
- [ ] Six test files reference these: the four that call the helpers, plus
      `server/model_set_test.go` and `server/appwire_runtime_test.go`.
- [ ] `go build ./... && go test ./server/`. The compiler is the completeness
      net (`docs/conventions/go-workspace.md`). Commit.

## Task 3: The handle owns the name, per turn

**Files:** `agent/session_active_turn.go`, `agent/session_lifecycle.go`

- [ ] **Step 1:** Write the failing test: two turns run inside one
      `ProcessInputKind` call (a user turn whose tail gate runs a notification
      turn), and **both** carry a `turn_m` name. This fails today only if the
      name is held at message scope; it is the regression guard for the mistake
      the superseded plan made.
- [ ] **Step 2:** Introduce `runningTurn`, created and released inside
      `processOneInput`, replacing the loose `runningTurnID` + `defer` pair
      landed in `f40904960`. The release stays adjacent to the take.
- [ ] **Step 3:** Apply the open call from decision 1 to every entry kind, so
      the stand-down currently special-cased for `EntryNotification` becomes the
      general rule.
- [ ] **Step 4:** Mutation-check each line; run `./agent/`. Commit.

## Task 4: The daemon learns from the handle, not from the serve loop

**Files:** `cmd/serf/serve.go`, `server/server.go`, `server/appwire_runtime.go`

- [ ] **Step 1:** Failing test — `thread/read` during a drain-loop turn reports
      the running turn's durable name, not a bucket id.
- [ ] **Step 2:** Add the open/close callback and wire it in `serve.go`, next to
      the `ProcessClientMutationStart` callback it mirrors.
- [ ] **Step 3:** Delete `SetProcessing(true)` at `serve.go:1001` and `:1014`,
      and the minting branch in `setProcessingLocked` (`server/server.go:712-718`).
      `SetProcessing(false)` stays.
- [ ] **Step 4:** Make the read path derive status and name from the handle
      (`appwire_runtime.go:1198,1238`) so they cannot disagree.
- [ ] **Step 5:** Release-before-idle. The superseded plan's ordering left the
      name held after the thread read idle, which is the inverse window the goal
      forbids. Assert the ordering.
- [ ] **Step 6:** Mutation-check, run `./server/ ./cmd/serf/ ./agent/`. Commit.

## Task 5: The projector never names a turn

`startTurn`'s fallback (`appwire_projection.go:1668-1676`) mints `turn_<n>` for a
real turn whenever nobody handed it a name, and it is reached from `ensureTurn`
(`:1789-1803`) at **five** event sites — `EventAssistantTextStart` (`:272`),
`EventAssistantTextEnd` (`:333`), `EventCommunicate` (`:413`),
`EventToolCallStart` (`:432`) and `EventError` (`:658`). `EventError` clears the
active turn mid-input, so the next content event re-opens a real turn with a
bucket id while `processing` is still true, and `appwire_runtime.go:212`
republishes it.

This is the site the superseded plan's architecture paragraph missed entirely,
and it is why its Definition of Done was unreachable.

- [ ] **Step 1:** Failing test — after `EventError` mid-turn, a following
      content event does not produce an active thread with a bucket id.
- [ ] **Step 2:** With Tasks 3 and 4 landed, every real turn arrives named.
      Make `ensureTurn`'s no-active-turn path a fault rather than a silent mint,
      subject to open call 2 for descendants.
- [ ] **Step 3:** Gate the active status frame on **what `startTurn` did**
      (promoted a reservation vs minted a bucket id), not on the event's
      `StableTurnID` field. The two differ: a turn named via the projector
      reservation has a `turn_m` id while its event's field is empty, so gating
      on the field would hide Stop on exactly the turns this plan fixes.
- [ ] **Step 4:** Confirm the descendant decision holds; mutation-check; commit.

## Task 6: Descendant threads get their boundary

Per open call 2. `sendEvent` forwards every event to `descendantEvent`
unconditionally (`agent/session_events.go:343-345`) — a synchronous callback,
not the droppable channel send, so the cost is real and must be judged rather
than waved away. Child sessions do run notification turns
(`agent/subagents.go:1251`) and have real projections
(`server/appwire_runtime.go:258-289`).

- [ ] Invert `TestUnservedSessionAnnouncesNoBoundary`
      (`agent/session_turn_boundary_test.go:510`), which pins today's behaviour.
- [ ] Drop the `servedByDaemon()` gate on the emit at
      `agent/session_lifecycle.go:1620`, keeping it on the mint.
- [ ] Note in the commit that unserved **root** sessions (one-shot `serf run`)
      also begin emitting the boundary; harmless via `sendEvent`'s drop path,
      but it should be stated rather than discovered.

## Task 7: A rejected control says why (kata `2f41`)

**The premise the superseded plan used was wrong** and is corrected here.
Rejections are *not* invisible: the daemon stamps `ClientMutationID` +
`MutationOutcome = notAccepted` (`session_client_mutation_queue.go:177-195`),
those reach the wire (`appwire/errors.go:44-50`), the dispatcher calls
`transferToRecovery(..., "rejected")` (`stores/mutationDispatcher.ts:137-141`),
and `QueueStrip` renders a row (`QueueStrip.tsx:143-150, 377-394`).

The real defects:

- [ ] The row states **no reason** and is indistinguishable from a pending one.
      Give it the rejection's reason.
- [ ] `turn/interrupt` carries no input, so its recovery row renders an empty
      `recordPreview` (`QueueStrip.tsx:113-116`) under an "Edit message" button.
      A Stop is not a message to recover; interrupts do not belong in a
      text-recovery store.
- [ ] `handleInterruptClick` already toasts on a thrown error
      (`Composer.tsx:872-883`) and is dead today, because the promise resolves
      at the IndexedDB commit (`pendingTurnsStore.ts:133-146`) and never sees
      the server's answer. Wire it or delete it.
- [ ] Any test must drive `MutationDispatcher`; a test against
      `threadsStore.steer` cannot observe a server rejection and would be
      tautological.

## Task 8: Retire the superseded documents and the weak audit

- [ ] `2026-08-16-one-active-turn-identity.md` still instructs the
      `SteeringInjectedData.StableTurnID` emit that `b5ce354a5` reverted and
      that `appwire_projection.go:693-699` says "must never be adopted" — at
      `:144`, `:501` and `:526-529`, with stale claims at `:12-15` and `:149`.
      Its header tells an agent to execute it task-by-task, so it is a live
      trap. Mark the superseded approach historical rather than patching two
      lines, and point to this plan.
- [ ] Mark `2026-08-16-busy-means-named.md` superseded by this document, keeping
      its rejected-options record.
- [ ] `agent/entrykind_audit_test.go` is weak — a kind added after
      `entryKindCount` (`session_lifecycle.go:412`) leaves both assertions green.
      But do **not** replace it with a `switch` + `invariant.Hold` default: that
      is a no-op in production builds and its test cannot fail. Strengthen the
      audit to derive the kind set from something that changes when a kind is
      added, or leave it and record why.

## Task 9: Coverage and hygiene

- [ ] `EventTurnStarted` is in neither fuzz corpus:
      `internal/appprojector/project_fuzz_test.go`'s `projectorCases` (`:18-94`)
      and `agent/events/eventdata_program_fuzz_test.go` (`:14-17`), whose doc
      claims completeness and omits `TurnStartedData` and `ModelRetryData`. A
      bare length assertion does not prevent the next omission — compare against
      a set that changes when the sealed set changes.
- [ ] `agent/session_client_mutation.go:128-141` says three sites write
      `ActiveTurnID`. There are eight, and the omitted
      `finalizeClientMutationInterrupt` (`:566`) is the unconditional clearer
      the identity guard exists to survive.
- [ ] Duplicate helpers in `internal/appprojector` (**not** `agent/`):
      `startedTurnID` (`goal_turn_identity_test.go:12`) and `turnStartedID`
      (`turn_boundary_test.go:10`) are the same function;
      `completedTurnID` is re-implemented inline at `goal_turn_identity_test.go:90-104`.
      `TestUnservedSessionNamesNoTurn` and `TestMintRunningTurnIDSkipsUnservedSessions`
      assert the same thing.
- [ ] `server/thread_envelope.go:174-190`: the justification for
      `EventTurnStarted: facetWork` sits after that row and before
      `EventSteeringInjected`, reading as documentation of the wrong entry; and
      `thread_envelope_test.go:71-85` asserts `WorkMillis` while the comment
      argues `ActiveTurnStartedAt`.
- [ ] One `resetTurnScopedState()`. `closeActiveTurn` clears seven fields,
      `startTurn` four *different* ones, `EventError` (`appwire_projection.go:659-664`)
      a third four. After a failed turn `reasoningItem`, `toolArgsByKey` and
      `toolStartByKey` survive, and `ensureReasoningItem` (`:1818-1825`) then
      reuses a stale item id with no `item/started`.
- [ ] Dedupe `cmd/serf-hub/e2e_turn_control_test.go` (700 lines): the
      `thread/start` + cleanup block appears three times, steer-and-prove and
      interrupt-and-prove twice each. ~140 lines, and it makes visible that only
      one test covers Send-while-busy.

---

## Definition of done

- [ ] Task 1's invariant harness passes for every turn kind, on the push path
      and the read path.
- [ ] `make lint` (7 gates), `make build`, root suite, all seven module suites,
      `make test-web`, all live-stack e2e tests.
- [ ] Live browser check: Steer and Stop render and apply on a notification
      turn, a goal turn and a user turn, with zero rejections in the mutation
      journal — and with Task 7 landed, any rejection that *does* occur is
      legible on screen.
- [ ] Katas `c2ty`, `7vmd` and `2f41` closed, each citing a test name.
- [ ] `docs/web-ui/decisions.md` updated if any recorded decision changed.

## Rejected options

Preserved from the superseded plan and extended, so none is re-proposed.

| Option | Why not |
| --- | --- |
| Capability gating (hide Stop/Steer while unnamed) | Reverses `c2ty`'s premise and picks the "button that is not there". Note the earlier claim that it leaves "nothing actionable" was overstated: with `statusType === "active"` the composer is in queue-mode, so Queue stays live. |
| Split `ActiveTurnID` into "running" and "claimed" | The accept-time write is load-bearing: it makes a turn steerable the instant the client is told it exists. Removing it moves the window. |
| Eager reservation in the serve loop (superseded Task 1) | Scoped to a message; identity is scoped to a turn. Strands the drain loop's inline turns. Its central mechanism also published nothing. |
| `switch` + `invariant.Hold` default for `EntryKind` | `invariant.Hold` is a no-op outside `-tags serffuzz`; the test cannot fail and the switch is weaker than the audit it replaces. |
| A cheap pre-check before taking the name | The wake's own accounting enqueues work before the count is taken, so a pre-check would sometimes see zero and drop a real wake. |

## Known limits

- **The `turn/start` window is not closed by this plan.** A second message
  composed between a `turn/start` being accepted and the serve loop dequeuing it
  is built as another `turn/start` and rejected. Task 7 makes that legible;
  closing it is client-side work (routing on the client's own optimistic state)
  and belongs in its own kata.
- **A leaked durable name is recoverable, not impossible.** Crash leaks are
  already bounded — `forgetRunningTurnNoOneOwns` runs on load
  (`session_client_mutation_persist.go:61,74-84`) and after a crash there is no
  live session to wedge. The only live-process cause is a failed release write,
  and any heal is another write to the same failed disk. The superseded plan's
  four-cause taxonomy double-counted, and its "healed within one serve-loop
  iteration" guarantee did not hold for that cause. No self-heal task here; the
  residual exposure is stated instead.
- **Descendant naming is deliberately absent.** Subagent turns get separation and
  status but no durable name, because no client can address them.
