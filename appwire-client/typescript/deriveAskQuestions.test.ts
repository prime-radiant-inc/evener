// @vitest-environment node
import { expect, test } from "vitest";
import { liveAskQuestions } from "./deriveAskQuestions";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import type { ThreadCapabilities } from "./types.gen";

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
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

// askPending defaults to true: this file's tests exercise the item-scan
// half of liveAskQuestions ("which questions"), not the wire gate itself
// ("is anything pending") - that gate has its own tests below, and its own
// coverage from the thread snapshot in reducer.test.ts ("askPending is
// wire-authoritative from the thread snapshot").
function model(turns: TurnModel[], overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "test",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    visionModel: "",
    askPending: true,
    turns,
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    pendingEscalations: [],
    lastFrameAt: 0,
    capabilities: NO_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...overrides,
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

// The server resolves the whole pending set on an interrupt exactly like a
// user message resolves it (agent/session_lifecycle.go: an interrupted turn
// calls clearAskPending directly - "the cards stay rendered from the
// transcript regardless; only the pending set... clears" - then appends a
// steering turn carrying SteeringKindInterrupted as the transcript's own
// marker of that boundary). A client deriving the live set locally must
// treat that marker as a resolution too, or it renders a question the
// server has already closed out.
test("a steering item with steeringKind interrupted AFTER an ask_user ack resolves it", () => {
  const m = model([
    turn("t1", [
      askItem("i1", "t1", "call_1"),
      item("i2", "t1", { type: "steering", text: "interrupted", steeringKind: "interrupted" }),
    ]),
  ]);
  expect(liveAskQuestions(m)).toEqual([]);
});

// The salvage explanation is persisted beside a partial response, but it is
// daemon-authored context rather than the admitted interrupt boundary. It must
// leave an ask opened when the durable interrupt marker was not accepted.
test("a steering item with steeringKind interrupted-salvage does NOT resolve the ask", () => {
  const m = model([
    turn("t1", [
      askItem("i1", "t1", "call_1"),
      item("i2", "t1", {
        type: "steering",
        text: "This response was interrupted; the content above was produced before the interruption and was not delivered.",
        steeringKind: "interrupted-salvage",
      }),
    ]),
  ]);
  expect(liveAskQuestions(m).map((q) => q.key)).toEqual(["call_1:0"]);
});

// A user steer reaches processOneInput as EntryUserInput (session_lifecycle.go:
// "the steering carrier enters as queued user input... it must reach the
// model rather than wait behind a question the user has already moved
// past"), which is the same accepted-turn path that clears askPending for a
// plain user message. Its transcript item carries source "user" (the wire's
// SteeringSourceUser), never a steeringKind.
test("a steering item with source user AFTER an ask_user ack resolves it", () => {
  const m = model([
    turn("t1", [
      askItem("i1", "t1", "call_1"),
      item("i2", "t1", { type: "steering", text: "focus on the tests", source: "user" }),
    ]),
  ]);
  expect(liveAskQuestions(m)).toEqual([]);
});

// A human-note update also carries source "user" (the note text a human
// wrote, steered in as SteeringKindHumanNote - agent/session_notes_rpc.go's
// steeringOrigin machinery), but writing a note is not answering the
// question: it must not resolve a pending ask, or the dock disappears while
// the ask itself stays open on the server (the TS twin of the server's own
// exclusion for this kind).
test("a steering item with source user AND steeringKind human-note does NOT resolve the ask", () => {
  const m = model([
    turn("t1", [
      askItem("i1", "t1", "call_1"),
      item("i2", "t1", { type: "steering", text: "note: check the logs", source: "user", steeringKind: "human-note" }),
    ]),
  ]);
  expect(liveAskQuestions(m).map((q) => q.key)).toEqual(["call_1:0"]);
});

// A daemon-originated steer with no user provenance (no steeringKind naming
// an interrupt, no source "user") never resolves a pending ask - the user
// has not spoken and has not stopped anything.
test("a daemon steering item with neither steeringKind interrupted nor source user does not resolve the ask", () => {
  const m = model([
    turn("t1", [
      askItem("i1", "t1", "call_1"),
      item("i2", "t1", { type: "steering", text: "a reminder", steeringKind: "task-nudge" }),
    ]),
  ]);
  expect(liveAskQuestions(m).map((q) => q.key)).toEqual(["call_1:0"]);
});

// A turn with no items of its own contributes nothing to the item scan
// either way, whether or not it also carries an error: it is not a question
// to show, and (since the turn/deriveAskQuestions round of this file) it is
// no longer a boundary this function looks for. Whether that failure
// resolved anything is the server's call, already folded into
// ThreadModel.askPending - the wire-gate tests below cover that half.
test("a turn with no items and an error is not a boundary — the earlier ask_user ack stays live", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1")]), turn("t2", [], { error: { message: "failed" } })]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_1:0"]);
});

test("a turn with items stays live by its items, even if the turn also errors", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1")], { error: { message: "failed" } })]);
  const result = liveAskQuestions(m);
  expect(result.map((q) => q.key)).toEqual(["call_1:0"]);
});

// --- the wire gate: "is anything pending" is ThreadModel.askPending's call,
// not this scan's -------------------------------------------------------

test("askPending false returns nothing, even with an unanswered ask_user item in the transcript", () => {
  const m = model([turn("t1", [askItem("i1", "t1", "call_1")])], { askPending: false });
  expect(liveAskQuestions(m)).toEqual([]);
});

test("askPending true with no ask_user items at all still returns nothing (there is nothing to scan)", () => {
  const m = model([turn("t1", [item("i1", "t1", { type: "userMessage", text: "hi" })])], { askPending: true });
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
