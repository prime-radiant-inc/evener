// @vitest-environment node
import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import type { ThreadCapabilities } from "../../../../protocol/types.gen";
import { liveAskQuestions } from "./deriveAskQuestions";

// --- fixtures (mirrors the local item/turn/model builder convention used
// by transcript/flow/useTranscriptScroll.test.ts, kept local rather than
// shared - see that file's own precedent) ---------------------------------

function item(id: string, turnId: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId, type: "agentMessage", text: "x", status: "completed", ...overrides };
}

function askQuestions(questions: Array<Record<string, unknown>>): string {
  return JSON.stringify({ questions });
}

function askItem(id: string, turnId: string, callId: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return item(id, turnId, {
    type: "commandExecution",
    toolName: "ask_user",
    callId,
    status: "completed",
    argumentsJSON: askQuestions([{ header: "H1", question: "Q1?", options: [{ label: "A", detail: "d" }] }]),
    ...overrides,
  });
}

function turn(id: string, items: ItemModel[], overrides: Partial<TurnModel> = {}): TurnModel {
  return { id, status: "completed", items, ...overrides };
}

const NO_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  queue: false,
  goal: false,
  rename: false,
};

function model(turns: TurnModel[]): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "test",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    askPending: false,
    turns,
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    pendingEscalations: [],
    lastFrameAt: 0,
    capabilities: NO_CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
  };
}

// --- tests ----------------------------------------------------------------

test("returns an empty list for a thread with no ask_user calls", () => {
  const m = model([turn("t1", [item("i1", "t1")])]);
  expect(liveAskQuestions(m)).toEqual([]);
});

test("returns a single acked ask_user call's questions, keyed by callId:idx", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1")])]);
  const result = liveAskQuestions(m);
  expect(result).toEqual([
    expect.objectContaining({ key: "call_1:0", callId: "call_1", header: "H1", question: "Q1?" }),
  ]);
});

test("excludes an ask_user call that has not yet been acked (still inProgress)", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1", { status: "inProgress" })])]);
  expect(liveAskQuestions(m)).toEqual([]);
});

test("excludes an errored/denied ask_user call — error presence, not status, disqualifies it", () => {
  // The projector stamps status "completed" even on a denied/errored ask_user
  // (a Go follow-up), so status alone can't tell a real question apart from a
  // denied one; ItemModel.error presence is the disqualifier. A denied ask
  // must never render an answerable card the human can submit into the void.
  const m = model([turn("t1", [askItem("i1", "t1", "call_1", { error: "user denied the ask_user request" })])]);
  expect(liveAskQuestions(m)).toEqual([]);
});

test("flattens every question across a single call's multiple questions in order", () => {
  const twoQuestions = askQuestions([
    { header: "First", question: "q1", options: [{ label: "a", detail: "b" }] },
    { header: "Second", question: "q2", options: [{ label: "c", detail: "d" }] },
  ]);
  const m = model([turn("t1", [askItem("i1", "t1", "call_1", { argumentsJSON: twoQuestions })])]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_1:0", "call_1:1"]);
  expect(result.map((q) => q.header)).toEqual(["First", "Second"]);
});

test("accumulates questions from multiple ask_user calls with no reply in between, in call order", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1"), askItem("i2", "t1", "call_2")])]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_1:0", "call_2:0"]);
});

test("a userMessage AFTER an ask_user ack resolves it - excluded from the live set", () => {
  const m = model([
    turn("t1", [askItem("i1", "t1", "call_1"), item("i2", "t1", { type: "userMessage", text: "[answers]..." })]),
  ]);
  expect(liveAskQuestions(m)).toEqual([]);
});

test("an ask_user call acked AFTER the last userMessage stays live", () => {
  const m = model([
    turn("t1", [
      item("i1", "t1", { type: "userMessage", text: "hi" }),
      item("i2", "t1", { type: "agentMessage", text: "ok" }),
      askItem("i3", "t1", "call_1"),
    ]),
  ]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_1:0"]);
});

test("only calls acked after the LAST userMessage are live - earlier ones stay resolved", () => {
  const m = model([
    turn("t1", [
      askItem("i1", "t1", "call_1"),
      item("i2", "t1", { type: "userMessage", text: "[answers] 1. [H1] -> skip" }),
      askItem("i3", "t1", "call_2"),
    ]),
  ]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_2:0"]);
});

test("the boundary spans multiple turns - order follows the turns array, not per-turn resets", () => {
  const m = model([
    turn("t1", [item("i1", "t1", { type: "userMessage", text: "hi" })]),
    turn("t2", [askItem("i2", "t2", "call_1")]),
  ]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_1:0"]);
});

test("an ask_user item with unparseable argumentsJSON contributes no questions", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1", { argumentsJSON: "{not json" })])]);
  expect(liveAskQuestions(m)).toEqual([]);
});

test("an ask_user item with no callId falls back to its item id for the key", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1", { callId: undefined })])]);
  expect(liveAskQuestions(m)[0]?.key).toBe("i1:0");
});

test("carries options/multiSelect/why/ifUnanswered through onto each question ref", () => {
  const q = askQuestions([
    {
      header: "H",
      question: "Q",
      options: [{ label: "a", detail: "b", recommended: true }],
      multi_select: true,
      why: "matters",
      if_unanswered: "assume no",
    },
  ]);
  const m = model([turn("t1", [askItem("i1", "t1", "call_1", { argumentsJSON: q })])]);
  expect(liveAskQuestions(m)).toEqual([
    {
      key: "call_1:0",
      callId: "call_1",
      header: "H",
      question: "Q",
      options: [{ label: "a", detail: "b", recommended: true }],
      multiSelect: true,
      why: "matters",
      ifUnanswered: "assume no",
    },
  ]);
});
