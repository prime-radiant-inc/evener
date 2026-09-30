import { describe, expect, it } from "vitest";
import {
  type CachedSessionRecord,
  cachedSessionRecord,
  hydrateThread,
  type ThreadModel,
  threadModelFromCache,
} from "./index";

// The wire fixture a v6 read answers with; minimal but content-bearing.
function readResponseFixture(): Parameters<typeof hydrateThread>[0] {
  return {
    ref: "local:thr_1",
    requestGeneration: 4,
    bootGeneration: "bg-1",
    epoch: 2,
    snapshot: { incarnation: "inc-9", length: 900 },
    olderCursor: "cursor-old",
    thread: {
      id: "thr_1",
      name: "session",
      status: { type: "idle" },
      preview: "",
      ephemeral: false,
      cwd: "/tmp",
      cliVersion: "1",
      source: "local",
      sessionId: "sess-1",
      createdAt: 1,
      updatedAt: 1,
      model: "m",
      modelProvider: "p",
      evener: {
        ref: "local:thr_1",
        capabilities: {
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
          sharedNotes: true,
          rename: true,
        },
        queue: { revision: 0 },
      },
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ type: "assistantMessage", id: "item_1", turnId: "turn_1", status: "completed" }],
        },
      ],
    },
  } as Parameters<typeof hydrateThread>[0];
}

// The type-level pin (spec: "a type-level test pins that no required field is
// left undefined, so a future ThreadModel field fails the build here").
// RequiredKeysOf drops optional fields; the Record annotation fails
// compilation when ThreadModel gains a required field this list lacks, and
// the runtime walk then covers the constructor for the fields it names.
type RequiredKeysOf<T> = { [K in keyof T]-?: undefined extends T[K] ? never : K }[keyof T];
const REQUIRED_THREAD_MODEL_KEYS: Record<RequiredKeysOf<ThreadModel>, true> = {
  ref: true,
  threadId: true,
  name: true,
  status: true,
  modelProvider: true,
  model: true,
  visionModel: true,
  askPending: true,
  pendingEscalations: true,
  turns: true,
  queue: true,
  tasks: true,
  jobsUpdatedAt: true,
  jobsTreeRevision: true,
  lastFrameAt: true,
  capabilities: true,
  goal: true,
  humanNote: true,
  agentNote: true,
  sessionUrls: true,
  contextUsed: true,
  contextWindow: true,
  contextPressure: true,
  usage: true,
  workMillis: true,
  reasoningEffortLevels: true,
  supportsReasoning: true,
  cwd: true,
};

const record: CachedSessionRecord = {
  ref: "local:thr_1",
  threadId: "thr_1",
  name: "session",
  modelProvider: "p",
  model: "m",
  imageSessionId: "sess-1",
  olderCursor: "cursor-old",
  savedAt: 1234,
  history: {
    bootGeneration: "bg-1",
    epoch: 2,
    incarnation: "inc-9",
    length: 900,
    appliedGeneration: 4,
    issuedGeneration: 4,
    turns: [{ id: "turn_1", status: "completed", items: [], version: 2 }],
  },
};

describe("threadModelFromCache", () => {
  it("fills every required ThreadModel field (the type-level pin walks the same list)", () => {
    const model = threadModelFromCache(record, 5000);
    for (const key of Object.keys(REQUIRED_THREAD_MODEL_KEYS)) {
      expect(
        (model as unknown as Record<string, unknown>)[key],
        `field ${key} must not be undefined`,
      ).not.toBeUndefined();
    }
  });

  it("carries the recorded history and identity, resets every transient field, and zeroes the live surface", () => {
    const model = threadModelFromCache(record, 5000);
    expect(model.history?.turns).toEqual(record.history.turns);
    expect(model.history?.bootGeneration).toBe("bg-1");
    expect(model.history?.epoch).toBe(2);
    expect(model.history?.incarnation).toBe("inc-9");
    expect(model.history?.length).toBe(900);
    expect(model.history?.invalidatedAtGeneration).toBeUndefined();
    expect(model.history?.awaited).toBeUndefined();
    expect(model.history?.pendingIncarnation).toBeUndefined();
    expect(model.history?.deferredPages).toEqual([]);
    expect(model.history?.failed).toBeUndefined();
    expect(model.capabilities.send).toBe(false); // the empty set: every capability-gated action refuses
    expect(model.queue).toBeNull();
    expect(model.tasks).toBeNull();
    expect(model.pendingEscalations).toEqual([]);
    expect(model.instanceId).toBeUndefined();
    expect(model.olderCursor).toBe("cursor-old");
    expect(model.lastFrameAt).toBe(5000);
    expect(model.status).toEqual({ type: "" }); // the neutral status
  });

  it("derives display turns from the recorded history (turn_1 renders)", () => {
    const model = threadModelFromCache(record, 5000);
    expect(model.turns.map((turn) => turn.id)).toEqual(["turn_1"]);
  });
});

describe("cachedSessionRecord", () => {
  it("encodes a hydrated v6 model and round-trips the identity through threadModelFromCache", () => {
    const hydrated = hydrateThread(readResponseFixture(), "local:thr_1", 7000);
    const encoded = cachedSessionRecord(hydrated, 7000);
    expect(encoded).toBeDefined();
    const back = threadModelFromCache(encoded!, 8000);
    expect(back.history?.incarnation).toBe("inc-9");
    expect(back.history?.epoch).toBe(2);
    expect(back.history?.length).toBe(900);
    expect(back.history?.issuedGeneration).toBe(4);
    expect(back.history?.turns).toEqual(hydrated.history?.turns);
    expect(back.olderCursor).toBe("cursor-old");
  });

  it("refuses a model with no recorded history identity (incarnation absent)", () => {
    const bare = { ...hydrateThread(readResponseFixture(), "local:thr_1", 7000) };
    const noHistory = { ...bare, history: { ...bare.history!, incarnation: undefined } };
    expect(cachedSessionRecord(noHistory, 7000)).toBeUndefined();
    const handBuilt: ThreadModel = { ...bare, history: undefined };
    expect(cachedSessionRecord(handBuilt, 7000)).toBeUndefined();
  });
});
