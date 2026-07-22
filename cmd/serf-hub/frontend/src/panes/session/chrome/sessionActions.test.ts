// @vitest-environment node
import { expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { lastUserMessageText } from "./sessionActions";

// lastUserMessageText backs the chrome-level "Fork" action: unlike the
// legacy per-message fork-into-composer (issue #42), a session-chrome menu
// item has no specific transcript message as its context, so it forks from
// the most recent turn that actually has a userMessage item - scanning
// backward rather than trusting turns[length-1] blindly, since the very
// last turn may not be user-initiated (e.g. a goal-continuation turn).
// thread/fork REQUIRES either editedInput or deferInput when sourceTurnId
// is set (cmd/serf-hub/app_threadlifecycle.go:361-363, InvalidParams
// "editedInput is required") - editedInput is what this feeds, pre-filled
// from the ALREADY-LOADED transcript so nothing needs a deferInput round
// trip just to recover text the client already has.

function model(turns: ThreadModel["turns"]): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns,
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
  };
}

test("undefined when the transcript has no turns at all", () => {
  expect(lastUserMessageText(model([]))).toBeUndefined();
});

test("finds the user message in the most recent turn", () => {
  const m = model([
    { id: "turn_1", status: "completed", items: [{ id: "i1", turnId: "turn_1", type: "userMessage", text: "hi" }] },
    { id: "turn_2", status: "completed", items: [{ id: "i2", turnId: "turn_2", type: "userMessage", text: "second" }] },
  ]);
  expect(lastUserMessageText(m)).toEqual({ turnId: "turn_2", text: "second" });
});

test("scans backward past a trailing non-user turn (e.g. a goal-continuation turn with no userMessage item)", () => {
  const m = model([
    { id: "turn_1", status: "completed", items: [{ id: "i1", turnId: "turn_1", type: "userMessage", text: "hi" }] },
    {
      id: "turn_2",
      status: "completed",
      items: [{ id: "i2", turnId: "turn_2", type: "systemMessage", text: "continuing" }],
    },
  ]);
  expect(lastUserMessageText(m)).toEqual({ turnId: "turn_1", text: "hi" });
});

test("undefined when no turn anywhere has a userMessage item", () => {
  const m = model([
    { id: "turn_1", status: "completed", items: [{ id: "i1", turnId: "turn_1", type: "systemMessage", text: "x" }] },
  ]);
  expect(lastUserMessageText(m)).toBeUndefined();
});

test("a userMessage item with blank text does not count - nothing to fork from", () => {
  const m = model([
    { id: "turn_1", status: "completed", items: [{ id: "i1", turnId: "turn_1", type: "userMessage", text: "" }] },
  ]);
  expect(lastUserMessageText(m)).toBeUndefined();
});

test("takes the FIRST userMessage item within the winning turn when a turn has more than one", () => {
  const m = model([
    {
      id: "turn_1",
      status: "completed",
      items: [
        { id: "i1", turnId: "turn_1", type: "userMessage", text: "first" },
        { id: "i2", turnId: "turn_1", type: "userMessage", text: "second" },
      ],
    },
  ]);
  expect(lastUserMessageText(m)).toEqual({ turnId: "turn_1", text: "first" });
});
