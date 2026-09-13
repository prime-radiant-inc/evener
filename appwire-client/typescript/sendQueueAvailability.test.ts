import { describe, expect, test } from "vitest";
import { deriveSendQueueAvailability } from "./sendQueueAvailability";
import type { ThreadCapabilities } from "./types.gen";

function caps(overrides: Partial<ThreadCapabilities> = {}): ThreadCapabilities {
  return {
    send: true,
    steer: true,
    interrupt: true,
    compact: true,
    clear: true,
    forkFromTurn: true,
    shutdown: true,
    changeModel: true,
    changeVisionModel: true,
    queue: true,
    goal: true,
    rename: true,
    ...overrides,
  };
}

// The capability set a real daemon publishes for an IDLE thread, read off
// server/appwire_runtime.go's appCapabilities with every optional callback
// wired: `active := processing || appReservedTurnID != ""` is false there, so
// Steer and Queue are both false; Clear and ForkFromTurn are hardcoded false;
// Interrupt is gated on the callback alone, not on active.
//
// caps() above is nobody's snapshot. It reports queue:true for a thread that
// is not running anything, which no daemon ever sends. It is the right
// fixture for the tiers that must IGNORE capabilities, and the wrong one for
// any rule that reads them: a rule proved only against caps() is proved
// against a state that cannot occur (kata 8c65).
function daemonIdleCapabilities(): ThreadCapabilities {
  return {
    send: true,
    steer: false,
    interrupt: true,
    compact: true,
    clear: false,
    forkFromTurn: false,
    shutdown: true,
    changeModel: true,
    changeVisionModel: true,
    queue: false,
    goal: true,
    rename: true,
  };
}

// One test per row of the legacy precedence table (parity-m5-composer.md,
// §A "Send-vs-Queue capability precedence", lines 64-71, citing
// cmd/evener-hub/assets/renderer.js:479-513 — cited verbatim in
// sendQueueAvailability.ts's own header comment), collapsed from 5 tiers to
// 4: the wave plan verified capabilities have no live push on the wire, so
// the original tier 2 ("the source already advertised live send/queue
// capabilities for the CURRENT state") can never apply — capabilities are
// always a possibly-stale snapshot, never freshly re-advertised per status
// change — and is correctly absent below, not merely untested.
describe("deriveSendQueueAvailability", () => {
  test("tier 1: ended -> both false, even with fully-permissive capabilities (parity row 1)", () => {
    expect(deriveSendQueueAvailability({ statusType: "ended", capabilities: caps() })).toEqual({
      canSend: false,
      canQueue: false,
    });
  });

  test("tier 1: closed -> both false, even with fully-permissive capabilities (parity row 1)", () => {
    expect(deriveSendQueueAvailability({ statusType: "closed", capabilities: caps() })).toEqual({
      canSend: false,
      canQueue: false,
    });
  });

  test("tier 3 (parity row 3): active with capabilities.queue explicitly false -> both false", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "active",
        capabilities: caps({ queue: false }),
      }),
    ).toEqual({ canSend: false, canQueue: false });
  });

  test("tier 4 (parity row 4): active -> send=false, queue=true (queue-mode default), ignoring capabilities.send entirely", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "active",
        capabilities: caps({ send: false }),
      }),
    ).toEqual({ canSend: false, canQueue: true });
  });

  test("tier 5 (parity row 5): idle -> send=true, queue=false (plain-send default), ignoring capabilities entirely", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "idle",
        capabilities: caps({ send: false, queue: true }),
      }),
    ).toEqual({ canSend: true, canQueue: false });
  });

  test("tier 5 (parity row 5): awaiting -> send=true, queue=false, same plain-send default as idle", () => {
    expect(deriveSendQueueAvailability({ statusType: "awaiting", capabilities: caps() })).toEqual({
      canSend: true,
      canQueue: false,
    });
  });

  test("tier 5: an unrecognized/future status type falls to the plain-send default rather than throwing", () => {
    expect(deriveSendQueueAvailability({ statusType: "something-new", capabilities: caps() })).toEqual({
      canSend: true,
      canQueue: false,
    });
  });

  // Race window: thread/status/changed (flips statusType to "active") and
  // turn/started (populates ThreadModel.activeTurnId) are two SEPARATE
  // notifications - there is a real gap between them where a model has
  // status "active" but no activeTurnId yet. The verbatim legacy precedence
  // (renderer.js:479-513) keys ONLY on the state string in this branch, so
  // it queues in that window; this helper must match exactly; it does not
  // take activeTurnId as an input at all (see sendQueueAvailability.ts's own
  // header comment for why folding in activeTurnId would reintroduce this
  // exact race as a bug - a legitimate queue attempt bouncing off a busy
  // daemon as a ConflictError).
  test("statusType 'active' alone - independent of whether a turn id has arrived yet - resolves to queue-mode, never plain-send", () => {
    expect(deriveSendQueueAvailability({ statusType: "active", capabilities: caps() })).toEqual({
      canSend: false,
      canQueue: true,
    });
  });

  // Tier 6, and the mirror image of the race above: send two messages quickly
  // and the second is composed BEFORE any status frame for the first has
  // arrived. statusType still reads idle, so tier 5 routed it to turn/start
  // and the daemon refused it with Conflict("turn is already active").
  //
  // The client knows something the status does not - it submitted a turn
  // itself. That is its OWN record of what it did, not a late guess about the
  // daemon's state, so this is not the activeTurnId fold the header rejects.
  //
  // It has to be its own tier, ABOVE the tier-3 capability veto, because the
  // capabilities in hand during this window are the IDLE ones and idle
  // advertises queue:false. Folding it into the active branch instead makes
  // the veto fire and DISABLES the composer in exactly the window the rule
  // exists to serve - strictly worse than the bounce, which at least left a
  // recovery row. An idle queue:false means "nothing to queue behind", not
  // "this harness has no queue"; the two are indistinguishable from here, so
  // this tier does not consult it. A harness with no queue at all answers
  // turn/queue with Unavailable, which the user sees.
  //
  // The queue lands with no turn id: appwire v3 dropped expectedTurnId from
  // turn/queue outright (appwire/types.go's ProtocolVersion note), so neither
  // handleAppTurnQueue nor the agent's own clientMutationQueue has a turn
  // precondition left to fail - pinned live by
  // cmd/evener-hub/e2e_control_without_turn_ids_test.go's
  // TestE2E_ASendThatRacedAStopStillRuns.
  test("tier 6: a turn this client already submitted queues the next message, against the REAL idle capability set", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "idle",
        capabilities: daemonIdleCapabilities(),
        hasPendingSend: true,
      }),
    ).toEqual({ canSend: false, canQueue: true });
  });

  test("tier 6 leaves tier 5 alone: the same idle capabilities with no pending send are still plain-send", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "idle",
        capabilities: daemonIdleCapabilities(),
        hasPendingSend: false,
      }),
    ).toEqual({ canSend: true, canQueue: false });
  });

  // Tier 6 reaches inside tier 1 rather than being shadowed by it. A finished
  // session is resumable - turn/start alone carries the hub's auto-resume
  // (app_rpc.go) - so the first message wakes a daemon, and the seconds it
  // takes to spawn are all window: the status still says the session is over
  // while a turn this client submitted is already in flight. Routing the second
  // message by the status alone sends another turn/start into the daemon that
  // just started, which refuses it with Conflict("turn is already active"). The
  // "notLoaded" spelling of the same race was fixed first and called the widest
  // instance of it; nothing about that argument was specific to which finished
  // status the thread happened to carry.
  test("tier 6 reaches into tier 1: a pending send queues the next message on an ended or closed session", () => {
    for (const statusType of ["ended", "closed"]) {
      expect(
        deriveSendQueueAvailability({ statusType, capabilities: daemonIdleCapabilities(), hasPendingSend: true }),
      ).toEqual({ canSend: false, canQueue: true });
    }
  });

  test("tier 1 with nothing pending is unchanged: an ended or closed session offers neither action", () => {
    for (const statusType of ["ended", "closed"]) {
      expect(
        deriveSendQueueAvailability({ statusType, capabilities: daemonIdleCapabilities(), hasPendingSend: false }),
      ).toEqual({ canSend: false, canQueue: false });
    }
  });

  // The veto tier 6 must not inherit still applies where it is meaningful: a
  // thread the daemon reports as ACTIVE with queue:false really has no queue.
  test("tier 3 survives tier 6: an active thread with queue:false is still both-false, pending send or not", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "active",
        capabilities: caps({ queue: false }),
        hasPendingSend: true,
      }),
    ).toEqual({ canSend: false, canQueue: false });
  });
});

test("an incompatible daemon cannot receive another message even with an outstanding send", () => {
  expect(
    deriveSendQueueAvailability({ statusType: "restartRequired", capabilities: caps(), hasPendingSend: true }),
  ).toEqual({ canSend: false, canQueue: false });
});
