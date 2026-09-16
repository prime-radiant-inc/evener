import { expect, it } from "vitest";
import {
  canComposeFor,
  conversationControls,
  queueActionRefusal,
  queueSheetPresentation,
} from "./conversationControls";

const STEERING = {
  send: true,
  steer: true,
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

function conversation(status: string, depth = 0, capabilities = STEERING) {
  return { status, capabilities, queue: { revision: 0, depth } };
}

// The hub advertises steer as harness support, so an idle steering harness
// carries steer:true; the Steer action follows the status.
it("offers no Steer action on an idle steering harness", () => {
  expect(conversationControls(conversation("idle")).steer).toBe(false);
  expect(conversationControls(conversation("active")).steer).toBe(true);
});

// awaiting with a queue is the ask boundary: that queue runs next on its own,
// so the sheet offers no steering action and says what will happen instead.
it("offers no steering queue actions while awaiting with a queue", () => {
  const sheet = queueSheetPresentation(conversation("awaiting", 2));
  expect(sheet.canRun).toBe(false);
  expect(sheet.explanation).toBe("Messages run in order. Your next message runs first, then the queue.");
});

it("offers the steering queue actions for a running turn and for a queue a Stop parked", () => {
  expect(queueSheetPresentation(conversation("active", 1)).canRun).toBe(true);
  expect(queueSheetPresentation(conversation("idle", 1)).canRun).toBe(true);
  expect(queueSheetPresentation(conversation("idle", 1, { ...STEERING, steer: false })).canRun).toBe(false);
});

it("can compose when any action or a goal is available, not otherwise", () => {
  expect(canComposeFor(conversation("idle"))).toBe(true);
  expect(canComposeFor(conversation("active", 0, { ...STEERING, send: false, goal: false }))).toBe(true);
  expect(canComposeFor(conversation("active", 0, { ...STEERING, send: false, steer: false, goal: false }))).toBe(false);
});

// The sheet re-checks at press time: a status that flipped to awaiting between
// the render that offered the action and the press refuses with the status
// reason, and a harness without steer with the capability reason.
it("refuses a queue action pressed after the status flipped, with the control's reason", () => {
  expect(queueActionRefusal(conversation("active", 1))).toBeNull();
  expect(queueActionRefusal(conversation("awaiting", 1))).toBe("no active turn");
  expect(queueActionRefusal(conversation("idle", 1, { ...STEERING, steer: false }))).toBe(
    "Steer is not available for this session",
  );
});
