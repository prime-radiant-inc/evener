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
    queue: true,
    goal: true,
    rename: true,
    ...overrides,
  };
}

// One test per row of the legacy precedence table (parity-m5-composer.md,
// §A "Send-vs-Queue capability precedence", lines 64-71 — cited verbatim in
// sendQueueAvailability.ts's own header comment), collapsed from 5 tiers to
// 4: the wave plan verified capabilities have no live push on the wire, so
// the original tier 2 ("the source already advertised live send/queue
// capabilities for the CURRENT state") can never apply — capabilities are
// always a possibly-stale snapshot, never freshly re-advertised per status
// change — and is correctly absent below, not merely untested.
describe("deriveSendQueueAvailability", () => {
  test("tier 1: ended -> both false, even with fully-permissive capabilities (parity row 1)", () => {
    expect(deriveSendQueueAvailability({ statusType: "ended", capabilities: caps(), activeTurnId: "turn_1" })).toEqual({
      canSend: false,
      canQueue: false,
    });
  });

  test("tier 1: closed -> both false, even with fully-permissive capabilities (parity row 1)", () => {
    expect(
      deriveSendQueueAvailability({ statusType: "closed", capabilities: caps(), activeTurnId: undefined }),
    ).toEqual({ canSend: false, canQueue: false });
  });

  test("tier 3 (parity row 3): busy (active + activeTurnId) with capabilities.queue explicitly false -> both false", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "active",
        capabilities: caps({ queue: false }),
        activeTurnId: "turn_1",
      }),
    ).toEqual({ canSend: false, canQueue: false });
  });

  test("tier 4 (parity row 4): busy -> send=false, queue=true (queue-mode default), ignoring capabilities.send entirely", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "active",
        capabilities: caps({ send: false }),
        activeTurnId: "turn_1",
      }),
    ).toEqual({ canSend: false, canQueue: true });
  });

  test("tier 5 (parity row 5): idle -> send=true, queue=false (plain-send default), ignoring capabilities entirely", () => {
    expect(
      deriveSendQueueAvailability({
        statusType: "idle",
        capabilities: caps({ send: false, queue: true }),
        activeTurnId: undefined,
      }),
    ).toEqual({ canSend: true, canQueue: false });
  });

  test("tier 5 (parity row 5): awaiting -> send=true, queue=false, same plain-send default as idle", () => {
    expect(
      deriveSendQueueAvailability({ statusType: "awaiting", capabilities: caps(), activeTurnId: undefined }),
    ).toEqual({ canSend: true, canQueue: false });
  });

  test("tier 5: an unrecognized/future status type falls to the plain-send default rather than throwing", () => {
    expect(
      deriveSendQueueAvailability({ statusType: "something-new", capabilities: caps(), activeTurnId: undefined }),
    ).toEqual({ canSend: true, canQueue: false });
  });

  test("status type 'active' with no activeTurnId is not busy (mirrors the legacy centralized busy predicate, parity §A first bullet: state==='active' && !!activeTurnId) - falls to the plain-send default, not queue-mode", () => {
    expect(
      deriveSendQueueAvailability({ statusType: "active", capabilities: caps(), activeTurnId: undefined }),
    ).toEqual({ canSend: true, canQueue: false });
  });
});
