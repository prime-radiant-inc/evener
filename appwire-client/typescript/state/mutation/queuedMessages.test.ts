import { expect, test } from "vitest";
import { canEditQueuedMessage, canRemoveQueuedMessage } from "./queuedMessages";
import type { MutationOutboxRecord } from "./records";

function queuedRecord(method: string, overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
  const input = [{ type: "text" as const, text: "queued input" }];
  return {
    version: 1,
    clientMutationId: `mutation-${method}`,
    intentSequence: 1,
    createdAt: 1,
    targetRef: "local:thread-1",
    threadId: "thread-1",
    method,
    payload: { ref: "local:thread-1", input },
    attachments: [],
    optimisticDisplay: { method, input },
    state: "submitting",
    attempted: false,
    unresolvedAttempts: 0,
    ...overrides,
  };
}

test("queuedMessageActionsRespectOwnership", () => {
  const canceledQueue = queuedRecord("turn/queue", {
    state: "canceled",
    attempted: undefined,
    unresolvedAttempts: undefined,
  });
  const unattemptedQueue = queuedRecord("turn/queue");
  const attemptedQueue = queuedRecord("turn/queue", {
    attempted: true,
    unresolvedAttempts: 1,
  });
  const contradictoryCanceledQueue = queuedRecord("turn/queue", {
    state: "canceled",
    attempted: false,
    unresolvedAttempts: 1,
  });
  const unknownQueue = queuedRecord("turn/queue", {
    state: "blockedUnknown",
    attempted: true,
    unresolvedAttempts: 1,
  });
  const legacyUnknownQueue = queuedRecord("turn/queue", {
    attempted: undefined,
    unresolvedAttempts: undefined,
  });
  const note = queuedRecord("notes/human/set");
  const stop = queuedRecord("turn/interrupt");
  const unattemptedPromotion = queuedRecord("turn/promoteQueuedAsSteer", {
    payload: { ref: "local:thread-1", expectedEntryId: "queue-1" },
  });
  const emptyDrain = queuedRecord("turn/drainAsSteer", {
    payload: { ref: "local:thread-1", expectedQueueRevision: 3 },
  });
  const contentDrain = queuedRecord("turn/drainAsSteer", {
    payload: {
      ref: "local:thread-1",
      expectedQueueRevision: 3,
      input: [{ type: "command", name: "inspect" }],
    },
  });

  expect(canEditQueuedMessage(canceledQueue)).toBe(true);
  expect(canRemoveQueuedMessage(canceledQueue)).toBe(true);
  expect(canEditQueuedMessage(unattemptedQueue)).toBe(true);
  // Another pass is sending the submitting row: the click must not race it.
  expect(canEditQueuedMessage(attemptedQueue)).toBe(false);
  expect(canRemoveQueuedMessage(attemptedQueue)).toBe(false);
  // A canceled row is parked, and an uncertain row closes its queue: both are
  // the user's to clear, whatever their attempt evidence says.
  expect(canEditQueuedMessage(contradictoryCanceledQueue)).toBe(true);
  expect(canRemoveQueuedMessage(contradictoryCanceledQueue)).toBe(true);
  // Uncertain delivery is the user's to clear or to send again as a new message.
  expect(canEditQueuedMessage(unknownQueue)).toBe(true);
  expect(canRemoveQueuedMessage(unknownQueue)).toBe(true);
  expect(canEditQueuedMessage(legacyUnknownQueue)).toBe(false);
  expect(canEditQueuedMessage(note)).toBe(false);
  expect(canRemoveQueuedMessage(note)).toBe(false);
  expect(canEditQueuedMessage(stop)).toBe(false);
  expect(canRemoveQueuedMessage(stop)).toBe(false);
  expect(canEditQueuedMessage(unattemptedPromotion)).toBe(false);
  expect(canRemoveQueuedMessage(unattemptedPromotion)).toBe(true);
  expect(canEditQueuedMessage(emptyDrain)).toBe(false);
  expect(canRemoveQueuedMessage(emptyDrain)).toBe(true);
  expect(canEditQueuedMessage(contentDrain)).toBe(true);
  expect(canRemoveQueuedMessage(contentDrain)).toBe(true);
});

test.each(["turn/start", "turn/steer"])("allows unattempted and canceled %s compositions", (method) => {
  for (const state of ["submitting", "canceled"] as const) {
    const record = queuedRecord(method, { state });
    expect(canEditQueuedMessage(record)).toBe(true);
    expect(canRemoveQueuedMessage(record)).toBe(true);
  }
});

// RoboRev round-2 H2: an inherited wire identity IS uncertainty. A
// replacement minted from delivery-uncertain work carries its predecessor's
// id inside the payload while owning a fresh storage key - the predecessor's
// attempt may already be delivered, so editing the replacement into a
// recovery and re-sending would put the composition under a NEW wire id the
// daemon's journal has never seen, and the replay dedup would not apply: the
// send could double-apply. Such a row is Retry-only - the mint re-queues it
// under the SAME inherited id. Ordinary rows (an enqueue's own id inside its
// payload - every row a host without the mint ever writes, native's
// included) carry no inherited id, so the term never fires for them.
test("a replacement carrying an inherited wire id is retry-only, never editable or removable", () => {
  const input = [{ type: "text" as const, text: "queued input" }];
  const inheritedReplacement = queuedRecord("turn/start", {
    state: "canceled",
    clientMutationId: "mutation-fresh",
    payload: { ref: "local:thread-1", clientMutationId: "mutation-inherited", input },
  });
  expect(canEditQueuedMessage(inheritedReplacement)).toBe(false);
  expect(canRemoveQueuedMessage(inheritedReplacement)).toBe(false);
  // The no-op pin: a row whose payload id is its own key keeps its
  // eligibility - this is every row native ever writes.
  const ordinaryRow = queuedRecord("turn/start", {
    state: "canceled",
    clientMutationId: "mutation-own",
    payload: { ref: "local:thread-1", clientMutationId: "mutation-own", input },
  });
  expect(canEditQueuedMessage(ordinaryRow)).toBe(true);
  expect(canRemoveQueuedMessage(ordinaryRow)).toBe(true);
});
