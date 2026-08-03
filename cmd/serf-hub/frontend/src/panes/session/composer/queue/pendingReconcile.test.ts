import { expect, test } from "vitest";
import type { ThreadModel } from "../../../../protocol/model";
import type { PendingMutation } from "../../../../protocol/types.gen";
import type { MutationOutboxRecord } from "../../../../stores/mutationOutbox";
import { reconcilePendingEntries } from "./pendingReconcile";

function outbox(
  clientMutationId: string,
  method = "turn/start",
  text = "hello",
  state: MutationOutboxRecord["state"] = "submitting",
): MutationOutboxRecord {
  const input = [{ type: "text", text }];
  return {
    version: 1,
    clientMutationId,
    intentSequence: Number(clientMutationId.replace(/\D/g, "")) || 1,
    createdAt: 1,
    state,
    targetRef: "ref_a",
    threadId: "thread_a",
    method,
    payload: { ref: "ref_a", input, clientMutationId },
    attachments: [],
    optimisticDisplay: { method, input },
  };
}

function model(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thread_a",
    name: "",
    status: { type: "active" },
    modelProvider: "",
    model: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: { revision: 1 },
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: {} as ThreadModel["capabilities"],
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "",
    createdAt: "",
    updatedAt: "",
    ...rest,
    jobsTreeRevision,
  };
}

test("two same-text outbox records remain distinct by client mutation identity", () => {
  expect(reconcilePendingEntries("ref_a", [outbox("mutation_1"), outbox("mutation_2")], model())).toMatchObject([
    { id: "mutation_1", text: "hello", source: "outbox" },
    { id: "mutation_2", text: "hello", source: "outbox" },
  ]);
});

test("the authoritative pending projection replaces the same outbox identity", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/steer",
    input: [{ type: "text", text: "keep steering" }],
    executionState: "claimed",
    projectionState: "reflected",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/steer", "keep steering")],
      model({ pendingMutations: [pending] }),
    ),
  ).toEqual([
    expect.objectContaining({
      id: "mutation_1",
      method: "steer",
      state: "claimed",
      source: "authoritative",
    }),
  ]);
});

test("a transcript item with the identity removes the optimistic projection regardless of text", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1")],
      model({
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            items: [
              {
                id: "item_1",
                turnId: "turn_1",
                type: "userMessage",
                text: "server normalized text",
                clientMutationId: "mutation_1",
              },
            ],
          },
        ],
      }),
    ),
  ).toEqual([]);
});

test("an authoritative queue identity is rendered by QueueStrip rather than duplicated as pending", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/queue")],
      model({ queue: { revision: 1, clientMutationIds: ["mutation_1"] } }),
    ),
  ).toEqual([]);
});

test("blockedUnknown remains visible and is not converted by elapsed time", () => {
  expect(
    reconcilePendingEntries("ref_a", [outbox("mutation_1", "turn/start", "uncertain", "blockedUnknown")], model()),
  ).toEqual([
    expect.objectContaining({
      id: "mutation_1",
      state: "blockedUnknown",
      text: "uncertain",
    }),
  ]);
});
