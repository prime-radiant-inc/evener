import { expect, test } from "vitest";
import { decideSteerRoute, decideSubmitRoute, isTurnActive } from "./submitRouting";

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

// --- isTurnActive: the interrupt/steer/model-switch busy predicate ------
// Deliberately DIFFERENT from deriveSendQueueAvailability's own gate (which
// checks statusType alone) - this one requires BOTH statusType==="active"
// AND a non-empty activeTurnId, matching thread-state.js's legacy
// SerfThreadState.isBusy (the predicate interrupt/steer/model-switch share,
// never the composer's send/queue chain - see protocol/sendQueueAvailability.ts's
// own comment on why the two must not be folded together).

test("active status with a populated activeTurnId is busy", () => {
  expect(isTurnActive("active", "turn_1")).toBe(true);
});

test("active status with no activeTurnId yet (status arrived before turn/started) is not busy", () => {
  expect(isTurnActive("active", undefined)).toBe(false);
});

test("active status with an empty-string activeTurnId is not busy", () => {
  expect(isTurnActive("active", "")).toBe(false);
});

test("a non-active status is never busy even with an activeTurnId present", () => {
  expect(isTurnActive("awaiting", "turn_1")).toBe(false);
});

test("idle status with no activeTurnId is not busy", () => {
  expect(isTurnActive("idle", undefined)).toBe(false);
});
