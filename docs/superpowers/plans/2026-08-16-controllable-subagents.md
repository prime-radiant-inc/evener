# Controllable Subagents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** specced, not started. Split out of
`2026-08-16-stop-always-works.md` at Jesse's request because it is larger than
everything else in that plan combined and has a different risk profile.

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

A delegate runs *inside* the parent's tool call (`agent/subagents.go` owns the
drive loop). Cancelling the child mid-flight has to return something coherent to
that call: an error the parent's model can act on, a partial result, or a
synthesised "the user stopped this delegate" message. A wrong answer corrupts
the parent's transcript, which is worse than not shipping the button at all.

This is a design decision, not an implementation detail. Write the answer into
this document before Task 1.

## What blocks it today

| Blocker | Where |
| --- | --- |
| Every mutation handler rejects non-root threads with `SessionUnavailable("thread is not served by this daemon")` | `server/appwire_runtime.go:1102-1119` (`requireRootMutationTarget`), called first by `handleAppTurnStart`, `handleAppTurnSteer` and every sibling |
| Nothing routes an accepted mutation to a child session | the mutation path is wired to the root session only |
| Capabilities come from the ROOT session's setters, not per thread | `appCapabilities` (`:1367`) reads `s.steerFunc`/`s.cancelFunc`; the descendant list (`:551-552`) stamps only `ActiveTurnID` |
| Child sessions never mint a name | `servedByDaemon()` gates both the mint (`agent/session_active_turn.go:47-49`) and the boundary emit (`agent/session_lifecycle.go:1652`) |

## Global Constraints

- **Strict TDD.** Every task red first, for the stated reason.
- **One minter.** `reserveClientMutationTurnID`. See `eptj` above.
- **Per-thread capabilities land BEFORE any control is offered.** Today a
  descendant status frame can be stamped with the root's capability set
  (`stampCapabilitiesOnStatusChange`, `server/appwire_runtime.go:399-422`), which
  would render Stop on a thread that cannot honour it. That is the lying button
  the whole turn-identity effort exists to remove; reintroducing it here would
  be a self-inflicted regression.
- **Measure the cost.** A durable write per delegate wake, on a path that fires
  per delegate event. If it is worse than expected, say so rather than absorb it.

## Tasks

- [ ] **Task 1: Answer the parent-transcript question** and write it here.
- [ ] **Task 2: Per-thread capabilities.** Failing test: a subagent thread
      advertises its own capability set, not the root's. Must land before any
      control is offered.
- [ ] **Task 3: Child sessions take durable names.** Confirm each child gets its
      OWN mutation file and that the single-minter constraint holds. A collision
      here is `eptj` exactly.
- [ ] **Task 4: Route accepted mutations to the addressed child.** Failing test:
      a `turn/interrupt` aimed at a subagent thread is accepted. Expect
      `SessionUnavailable` first — that is the routing gate.
- [ ] **Task 5: Turn separation and status.** Drop the `servedByDaemon()` gate on
      the boundary emit (`agent/session_lifecycle.go:1652`) and invert
      `TestUnservedSessionAnnouncesNoBoundary`
      (`agent/session_turn_boundary_test.go:569`), which pins today's behaviour
      deliberately. Note this also makes unserved *root* sessions (one-shot
      `serf run`) emit the boundary; state it rather than discover it.
- [ ] **Task 6: Measure the added durable-write cost.**
- [ ] **Task 7: Live browser check** on a real delegate: separated turns, busy
      status, Stop works, and the parent behaves sanely afterwards.

## Definition of done

- [ ] `make lint` (nine targets), `make build`, root suite, all seven module
      suites, `make test-web`, all live-stack e2e tests.
- [ ] A live delegate can be stopped from the browser, and the parent's
      transcript afterwards is coherent and matches the answer to Task 1.
- [ ] No control is offered on any thread that cannot honour it.
