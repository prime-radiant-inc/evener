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
  expect(canEditQueuedMessage(attemptedQueue)).toBe(false);
  expect(canRemoveQueuedMessage(attemptedQueue)).toBe(false);
  expect(canEditQueuedMessage(contradictoryCanceledQueue)).toBe(false);
  expect(canRemoveQueuedMessage(contradictoryCanceledQueue)).toBe(false);
  expect(canEditQueuedMessage(unknownQueue)).toBe(false);
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
