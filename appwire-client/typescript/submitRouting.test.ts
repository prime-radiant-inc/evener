// @vitest-environment node

import { expect, test } from "vitest";
import type { ThreadModel } from "./model";
import { applyNotification, hydrateThread } from "./reducer";
import {
  decideSteerRoute,
  decideSubmitRoute,
  isTurnActive,
  NO_ACTIVE_TURN,
  STEER_UNAVAILABLE,
  sessionControls,
} from "./submitRouting";
import { wireThread } from "./testing/notifications";
import type { AnyNotification } from "./types.gen";

// --- decideSubmitRoute: send vs queue vs no-op --------------------------
// parity-m5-composer.md §A: submit is a no-op when the composer is empty of
// both text and attachments; otherwise routes to queue when queue-mode is
// available, else send, else no-op with neither capability.

test("empty composer is a no-op even when send is available", () => {
  expect(decideSubmitRoute({ hasContent: false, availability: { canSend: true, canQueue: false } })).toBe("none");
});

test("content + send available (queue not) routes to send", () => {
  expect(decideSubmitRoute({ hasContent: true, availability: { canSend: true, canQueue: false } })).toBe("send");
});

test("content + queue available (send not) routes to queue", () => {
  expect(decideSubmitRoute({ hasContent: true, availability: { canSend: false, canQueue: true } })).toBe("queue");
});

test("content + neither capability available is a no-op", () => {
  expect(decideSubmitRoute({ hasContent: true, availability: { canSend: false, canQueue: false } })).toBe("none");
});

test("if both capabilities were ever true at once, queue wins (defensive tie-break; the current derivation never produces this)", () => {
  expect(decideSubmitRoute({ hasContent: true, availability: { canSend: true, canQueue: true } })).toBe("queue");
});

// --- decideSteerRoute: classic steer vs drain-as-steer vs no-op ---------
// parity-m5-composer.md §A (kata 0bq1): text + empty queue -> classic
// steer; non-empty queue OR any attachments (regardless of text) -> drain;
// empty text + empty queue + no attachments -> no-op (focus only).

test("text with an empty queue and no attachments routes to classic steer", () => {
  expect(decideSteerRoute({ hasText: true, hasAttachments: false, queueDepth: 0 })).toBe("steer");
});

test("empty text with a non-empty queue routes to drain (anything + non-empty queue)", () => {
  expect(decideSteerRoute({ hasText: false, hasAttachments: false, queueDepth: 2 })).toBe("drain");
});

test("text with a non-empty queue also routes to drain, not classic steer", () => {
  expect(decideSteerRoute({ hasText: true, hasAttachments: false, queueDepth: 1 })).toBe("drain");
});

test("attachments present with an empty queue route to drain even with no text", () => {
  expect(decideSteerRoute({ hasText: false, hasAttachments: true, queueDepth: 0 })).toBe("drain");
});

test("text AND attachments with an empty queue still route to drain (attachments force drain)", () => {
  expect(decideSteerRoute({ hasText: true, hasAttachments: true, queueDepth: 0 })).toBe("drain");
});

test("empty text, no attachments, empty queue is a no-op (focus-only, no request)", () => {
  expect(decideSteerRoute({ hasText: false, hasAttachments: false, queueDepth: 0 })).toBe("none");
});

// Skill selections are content everywhere else the composer decides (hasContent,
// draft persistence, builtin-command interception): a selection-only steer must
// submit, not fall through to the focus-only no-op.

test("skill selections alone with an empty queue route to classic steer", () => {
  expect(decideSteerRoute({ hasText: false, hasAttachments: false, hasSkills: true, queueDepth: 0 })).toBe("steer");
});

test("skill selections with a non-empty queue route to drain (anything + non-empty queue)", () => {
  expect(decideSteerRoute({ hasText: false, hasAttachments: false, hasSkills: true, queueDepth: 1 })).toBe("drain");
});

// --- isTurnActive: the interrupt/steer busy predicate -------------------
// The thread status is the daemon's own answer to "is this session working",
// and the one the hub derives the steer/interrupt capabilities from
// (server/appwire_runtime.go appCapabilitiesLocked). Nothing else feeds it: the
// transcript's activeTurnId is cleared and re-set across an inline turn
// boundary while the status stays active (the boundary test below).

test("active status is busy", () => {
  expect(isTurnActive("active")).toBe(true);
});

test.each(["idle", "awaiting", "ended", "closed", "notLoaded", "restartRequired"])(
  "%s status is not busy",
  (statusType) => {
    expect(isTurnActive(statusType)).toBe(false);
  },
);

// --- isTurnActive across an inline turn boundary ------------------------
// The daemon runs consecutive turns inside ONE input whenever a queued
// message, a notification turn, a goal continuation or a drained steering
// carrier follows a turn. The projector's openTurn
// (internal/appprojector/appwire_projection.go) then emits turn/completed
// (previous) + turn/started (next) + thread/status/changed(active) from one
// session event, and the thread status never leaves active: the projector
// publishes idle only at EventSessionEnd. The hub relays each of those as its
// own WebSocket message and the client folds them one at a time, so the
// predicate is evaluated between the two turn frames. Issue #1330.
function activeThread(): ThreadModel {
  const thread = wireThread("ref_t", {
    status: { type: "active" },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
  });
  thread.evener.activeTurnId = "turn_1";
  return hydrateThread({ thread }, "ref_t", 1000);
}

const INLINE_TURN_BOUNDARY: AnyNotification[] = [
  {
    method: "turn/completed",
    params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "completed", itemsView: "" } },
  },
  {
    method: "turn/started",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      turn: { id: "turn_2", status: "inProgress", itemsView: "full", startedAt: 2000 },
    },
  },
  {
    method: "thread/status/changed",
    params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" } },
  },
];

test("a session stays busy at every step of an inline turn boundary (turn/completed, turn/started, status active as separate frames)", () => {
  let model = activeThread();
  expect(isTurnActive(model.status.type)).toBe(true);
  const afterEachFrame = INLINE_TURN_BOUNDARY.map((frame) => {
    model = applyNotification(model, frame, 2000);
    return { method: frame.method, busy: isTurnActive(model.status.type), activeTurnId: model.activeTurnId };
  });
  // activeTurnId is the regression window: the reducer clears it on
  // turn/completed and sets it again on turn/started, and busy must not follow.
  expect(afterEachFrame).toEqual([
    { method: "turn/completed", busy: true, activeTurnId: undefined },
    { method: "turn/started", busy: true, activeTurnId: "turn_2" },
    { method: "thread/status/changed", busy: true, activeTurnId: "turn_2" },
  ]);
});

// --- sessionControls: one derivation for every control surface -----------
const ALL = { steer: true, interrupt: true, queue: true, send: true };

test("a running turn on a fully capable harness offers stop, steer, drain and queue, not send", () => {
  const c = sessionControls("active", ALL, 0);
  expect(c).toMatchObject({ stop: true, steer: true, drain: true, queue: true, send: false });
  expect(c.reason.send).toBeDefined();
});

test("an idle session offers send only, with 'no active turn' as every other reason", () => {
  const c = sessionControls("idle", ALL, 0);
  expect(c).toMatchObject({ stop: false, steer: false, drain: false, queue: false, send: true });
  expect(c.reason).toEqual({
    stop: NO_ACTIVE_TURN,
    steer: NO_ACTIVE_TURN,
    drain: NO_ACTIVE_TURN,
    queue: NO_ACTIVE_TURN,
  });
});

test("a queue parked by Stop (idle, depth > 0) makes drain available and nothing else", () => {
  const c = sessionControls("idle", ALL, 2);
  expect(c).toMatchObject({ stop: false, steer: false, drain: true, queue: false, send: true });
  expect(c.reason.drain).toBeUndefined();
});

test("a harness without steer names the capability, not the status, for steer and drain", () => {
  const c = sessionControls("active", { ...ALL, steer: false }, 1);
  expect(c).toMatchObject({ steer: false, drain: false });
  expect(c.reason).toMatchObject({ steer: STEER_UNAVAILABLE, drain: STEER_UNAVAILABLE });
});

test("awaiting with a queue is the ask boundary: no drain, and the reason is the status", () => {
  const c = sessionControls("awaiting", ALL, 1);
  expect(c.drain).toBe(false);
  expect(c.reason.drain).toBe(NO_ACTIVE_TURN);
});
