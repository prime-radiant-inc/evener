import { describe, expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import { collectItemIds, computeReconciledIds, type PendingTurnEntry } from "./pendingReconcile";

// baseModel builds a minimal-but-complete ThreadModel directly against
// model.ts's own interface (this is OUR internal contract, not a wire shape -
// unlike protocol/reducer.test.ts's fixtures, there is no wire notification
// shape to be unfaithful to here; the wire-truth boundary this module cares
// about is enforced instead by pendingTurnsStore.test.ts's own FakeClient-
// driven integration coverage, which proves the real hydrate/notification
// pipeline actually produces models this function reconciles against
// correctly).
function baseModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "active" },
    modelProvider: "anthropic",
    model: "claude-sonnet-4-5",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    lastFrameAt: 0,
    capabilities: {
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
    },
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    ...overrides,
  };
}

function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, status: "inProgress", items };
}

function item(overrides: Partial<ItemModel> & Pick<ItemModel, "id" | "type">): ItemModel {
  return { turnId: "turn_1", text: "", status: "completed", ...overrides };
}

let nextEntryId = 0;
function entry(overrides: Partial<PendingTurnEntry> & Pick<PendingTurnEntry, "method">): PendingTurnEntry {
  nextEntryId += 1;
  return {
    id: `pending_${nextEntryId}`,
    ref: "ref_a",
    text: "",
    imageCount: 0,
    createdAt: 0,
    ...overrides,
  };
}

describe("collectItemIds", () => {
  test("returns an empty set for an undefined model", () => {
    expect(collectItemIds(undefined).size).toBe(0);
  });

  test("collects every item id across every turn", () => {
    const model = baseModel({
      turns: [
        turn("turn_1", [item({ id: "item_1", type: "userMessage" }), item({ id: "item_2", type: "agentMessage" })]),
        turn("turn_2", [item({ id: "item_3", type: "userMessage" })]),
      ],
    });
    expect(collectItemIds(model)).toEqual(new Set(["item_1", "item_2", "item_3"]));
  });
});

describe("computeReconciledIds - queue method", () => {
  test("matches a queue entry whose normalized text appears in queue.texts", () => {
    const entries = [entry({ method: "queue", text: "Fix the lint errors" })];
    const model = baseModel({ queue: { depth: 1, texts: ["Fix the lint errors"] } });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("falls back to queue.preview when queue.texts is absent (old daemon)", () => {
    const entries = [entry({ method: "queue", text: "Update the README" })];
    const model = baseModel({ queue: { depth: 1, preview: ["Update the README"] } });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("prefers queue.texts over queue.preview when both are present", () => {
    // preview is server-truncated to the first line; texts is the full
    // untruncated text - a message whose preview line differs from its own
    // full text (a multi-line message) must still reconcile against the
    // full text, not the truncated preview.
    const entries = [entry({ method: "queue", text: "full line one\nline two" })];
    const model = baseModel({
      queue: { depth: 1, preview: ["full line one"], texts: ["full line one\nline two"] },
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("matches an image-only queue entry via the synthetic image placeholder", () => {
    const entries = [entry({ method: "queue", text: "", imageCount: 1 })];
    const model = baseModel({ queue: { depth: 1, texts: ["[image]"] } });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("does not match when the entry's text is absent from queue.texts", () => {
    const entries = [entry({ method: "queue", text: "not queued yet" })];
    const model = baseModel({ queue: { depth: 1, texts: ["something else"] } });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });

  test("a single matching preview entry reconciles exactly one of two identical-text chips (one-for-one)", () => {
    const first = entry({ method: "queue", text: "same text" });
    const second = entry({ method: "queue", text: "same text" });
    const model = baseModel({ queue: { depth: 1, texts: ["same text"] } });
    expect(computeReconciledIds([first, second], model, new Set())).toEqual([first.id]);
  });

  test("two matching preview entries reconcile both duplicate-text chips", () => {
    const first = entry({ method: "queue", text: "same text" });
    const second = entry({ method: "queue", text: "same text" });
    const model = baseModel({ queue: { depth: 2, texts: ["same text", "same text"] } });
    const result = computeReconciledIds([first, second], model, new Set());
    expect(result.sort()).toEqual([first.id, second.id].sort());
  });

  test("never matches a queue entry when the model has no queue at all", () => {
    const entries = [entry({ method: "queue", text: "anything" })];
    const model = baseModel({ queue: null });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });
});

describe("computeReconciledIds - send method (new userMessage items)", () => {
  test("matches a send entry against a newly-appended userMessage item with equal normalized text", () => {
    const entries = [entry({ method: "send", text: "hello there" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "userMessage", text: "hello there" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("does not match a send entry against a userMessage item with different text", () => {
    const entries = [entry({ method: "send", text: "hello there" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "userMessage", text: "goodbye" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });

  test("ignores an item id already present before this notification (not genuinely new)", () => {
    const entries = [entry({ method: "send", text: "hello there" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_old", type: "userMessage", text: "hello there" })])],
    });
    expect(computeReconciledIds(entries, model, new Set(["item_old"]))).toEqual([]);
  });

  test("matches an image-only send entry against a new textless userMessage item that has images", () => {
    const entries = [entry({ method: "send", text: "", imageCount: 1 })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "userMessage", text: "", images: ["data:..."] })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("does not match an image-only send entry against a new item with neither text nor images", () => {
    const entries = [entry({ method: "send", text: "", imageCount: 1 })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "userMessage", text: "" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });

  test("ignores a new item of a non-userMessage type entirely", () => {
    const entries = [entry({ method: "send", text: "hello there" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "agentMessage", text: "hello there" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });
});

describe("computeReconciledIds - steer / drain methods (new steering items)", () => {
  test("matches a steer entry against a new steering item with equal normalized text", () => {
    const entries = [entry({ method: "steer", text: "steer this" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "steering", text: "steer this" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("does not match a steer entry against a new steering item with different text", () => {
    const entries = [entry({ method: "steer", text: "steer this" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "steering", text: "something else" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });

  test("matches a drain entry against ANY new steering item regardless of text (drain merges texts server-side)", () => {
    const entries = [entry({ method: "drain", text: "whatever the composer had" })];
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "steering", text: "totally different merged text" })])],
    });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([entries[0]!.id]);
  });

  test("prefers an exact-text steer match over a same-ref pending drain entry", () => {
    const drainEntry = entry({ method: "drain", text: "drain placeholder" });
    const steerEntry = entry({ method: "steer", text: "steer this" });
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "steering", text: "steer this" })])],
    });
    // Registration order deliberately puts drain first - the specific text
    // match must still win over an earlier-registered, always-eligible
    // drain entry.
    expect(computeReconciledIds([drainEntry, steerEntry], model, new Set())).toEqual([steerEntry.id]);
  });

  test("falls back to the first-registered drain entry (FIFO) when no steer entry matches", () => {
    const firstDrain = entry({ method: "drain", text: "a" });
    const secondDrain = entry({ method: "drain", text: "b" });
    const model = baseModel({
      turns: [turn("turn_1", [item({ id: "item_new", type: "steering", text: "unrelated merged text" })])],
    });
    expect(computeReconciledIds([firstDrain, secondDrain], model, new Set())).toEqual([firstDrain.id]);
  });

  test("two new steering items in one flush reconcile two distinct drain entries in FIFO order", () => {
    const firstDrain = entry({ method: "drain", text: "a" });
    const secondDrain = entry({ method: "drain", text: "b" });
    const model = baseModel({
      turns: [
        turn("turn_1", [
          item({ id: "item_new_1", type: "steering", text: "merged one" }),
          item({ id: "item_new_2", type: "steering", text: "merged two" }),
        ]),
      ],
    });
    expect(computeReconciledIds([firstDrain, secondDrain], model, new Set())).toEqual([firstDrain.id, secondDrain.id]);
  });
});

describe("computeReconciledIds - cross-cutting", () => {
  test("reconciles a queue match and a new-item match together in one call", () => {
    const queueEntry = entry({ method: "queue", text: "queued text" });
    const steerEntry = entry({ method: "steer", text: "steer this" });
    const model = baseModel({
      queue: { depth: 1, texts: ["queued text"] },
      turns: [turn("turn_1", [item({ id: "item_new", type: "steering", text: "steer this" })])],
    });
    const result = computeReconciledIds([queueEntry, steerEntry], model, new Set());
    expect(result.sort()).toEqual([queueEntry.id, steerEntry.id].sort());
  });

  test("returns an empty array when nothing matches (entries stay pending)", () => {
    const entries = [entry({ method: "send", text: "hello" }), entry({ method: "queue", text: "world" })];
    const model = baseModel({ queue: { depth: 0 } });
    expect(computeReconciledIds(entries, model, new Set())).toEqual([]);
  });

  test("returns an empty array when given no entries at all", () => {
    const model = baseModel({ queue: { depth: 1, texts: ["anything"] } });
    expect(computeReconciledIds([], model, new Set())).toEqual([]);
  });
});
