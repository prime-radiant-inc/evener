import assert from "node:assert/strict";
import { test } from "node:test";
import { composeAskAnswers } from "@evener/appwire-client";
import { ownedHub, scriptedHub, withMutationEnv } from "./management-contract-fixtures.mjs";
import { runQuestions } from "./questions-logic.mjs";
import { projectQuestions, validateQuestionSelections } from "./questions-projection.mjs";

const ref = "local:questions";
const instanceId = "instance";
const clientMutationId = "answer-once";
const question = (overrides = {}) => ({
  header: "Delivery",
  question: "Choose a delivery mode.",
  options: [
    { label: "Draft", detail: "", recommended: true },
    { label: "Apply", detail: "" },
  ],
  ...overrides,
});
const item = (entry, type, overrides = {}) => ({
  id: `item-${entry}`,
  transcriptKey: `key-${entry}`,
  position: { entry, item: 0 },
  type,
  ...overrides,
});
const user = item(1, "userMessage", { text: "fixture" });
const ask = (entry = 2, questions = [question()], overrides = {}) =>
  item(entry, "commandExecution", {
    toolName: "ask_user",
    callId: `call-${entry}`,
    status: "completed",
    argumentsJson: JSON.stringify({ questions }),
    ...overrides,
  });
const fragment = (items, overrides = {}) => ({
  id: "turn-1",
  itemsView: "fragment",
  status: "completed",
  items,
  ...overrides,
});
const snapshot = (items = [user, ask()], options = {}) => ({
  thread: {
    id: "questions",
    evener: { ref, instanceId, capabilities: { send: true }, askPending: true, ...options.evener },
    turns: [fragment(items, options.turn)],
  },
  ...(options.cursor ? { olderCursor: options.cursor } : {}),
});
const pending = projectQuestions([user, ask()]);
const selections = [{ resolution: { kind: "option", labels: ["Draft"] }, note: "" }];
const params = () => ({
  ref,
  expectedInstanceId: instanceId,
  clientMutationId,
  reviewedCalls: structuredClone(pending.calls),
  selections: structuredClone(selections),
});
const receipt = (overrides = {}) => ({
  turn: { id: "turn-answer", itemsView: "full", status: "inProgress" },
  receipt: {
    clientMutationId,
    threadId: "questions",
    instanceId,
    turnId: "turn-answer",
    disposition: "applied",
    projectionState: "pending",
    ...overrides,
  },
});
const metadata = (evener = {}) => ({
  thread: { id: "questions", evener: { ref, instanceId, capabilities: { send: false }, ...evener } },
});
const answer = (script, input = params()) => runQuestions(script.hub, { action: "answer", params: input, ownedHub });
const mutation = (run) => withMutationEnv("EVENER_QUESTION_MUTATION", run);

test("the latest user bounds all pending calls and local default headers", () => {
  const result = projectQuestions([
    user,
    ask(),
    item(3, "userMessage"),
    ask(4, [question({ header: undefined })]),
    ask(5, [question({ header: undefined })]),
    ask(6, [question()], { error: "failed" }),
    ask(7, [question()], { status: "inProgress" }),
  ]);
  assert.deepEqual(
    result.calls.map((call) => call.callId),
    ["call-4", "call-5"],
  );
  assert.deepEqual(
    result.questions.map((q) => q.header),
    ["Question 1", "Question 1"],
  );
  assert.throws(() => projectQuestions([ask()]));
});

test("malformed successful questions never become a partial batch", () => {
  for (const change of [
    { argumentsJson: "{" },
    { argumentsJson: JSON.stringify({ questions: [question(), { question: "bad" }] }) },
    { argumentsJson: JSON.stringify({ questions: [question({ header: null })] }) },
    { argumentsJson: JSON.stringify({ questions: [question({ multi_select: "false" })] }) },
    {
      argumentsJson: JSON.stringify({
        questions: [
          question({
            options: [
              { label: "Draft", detail: "" },
              { label: "Draft", detail: "" },
            ],
          }),
        ],
      }),
    },
    {
      argumentsJson: JSON.stringify({
        questions: [
          question({
            options: [
              { label: "Draft", detail: "", recommended: true },
              { label: "Apply", detail: "", recommended: true },
            ],
          }),
        ],
      }),
    },
    { callId: "" },
    { error: null },
  ])
    assert.throws(() => projectQuestions([user, ask(2, [question()], change)]));
});

test("explicit resolutions and universal notes preserve authored values", () => {
  const q = pending.questions[0];
  for (const resolution of [
    { kind: "option", labels: ["Draft"] },
    { kind: "free", text: "" },
    { kind: "decide", leaning: "  " },
    { kind: "skip" },
    { kind: "fallback" },
  ]) {
    const [value] = validateQuestionSelections([{ ...q, if_unanswered: "" }], [{ resolution, note: "sentinel" }]);
    assert.deepEqual(value.resolution, resolution);
    assert.equal(value.note, "sentinel");
  }
  assert.deepEqual(
    validateQuestionSelections(
      [{ ...q, multi_select: true }],
      [{ resolution: { kind: "option", labels: ["Apply", "Draft"] }, note: "" }],
    )[0].resolution.labels,
    ["Apply", "Draft"],
  );
});

test("invalid selections are refused rather than filled with recommendations", () => {
  for (const choice of [
    null,
    { kind: "option", labels: [] },
    { kind: "option", labels: ["Missing"] },
    { kind: "option", labels: ["Draft", "Draft"] },
    { kind: "option", labels: ["Draft", "Apply"] },
    { kind: "free" },
    { kind: "decide", leaning: null },
    { kind: "fallback" },
    { kind: "wat" },
    { kind: "skip", text: "ignored" },
    { kind: ["skip"] },
  ])
    assert.throws(() => validateQuestionSelections(pending.questions, [{ resolution: choice, note: "" }]));
  assert.throws(() => validateQuestionSelections(pending.questions, []));
  assert.throws(() => validateQuestionSelections(pending.questions, [...selections, ...selections]));
});

test("list reads a bounded batch without mutation or subscription", async () => {
  const s = scriptedHub([() => snapshot()]);
  const result = await runQuestions(s.hub, { params: { ref } });
  assert.equal(result.outcome, "read");
  assert.deepEqual(result.calls, pending.calls);
  assert.deepEqual(
    s.calls.map((c) => c.method),
    ["connect", "thread/read"],
  );
  assert.equal(s.calls[1].params.subscribe, false);
});

test("paging reaches the latest user and retains every call in posting order", async () => {
  const tail = snapshot([ask(4)], { cursor: "opaque-new", turn: { hasEarlierItems: true } });
  const s = scriptedHub([
    () => tail,
    (method, input) => {
      assert.equal(method, "thread/turns/list");
      assert.equal(input.cursor, "opaque-new");
      return { data: [fragment([ask(3)], { hasEarlierItems: true, hasLaterItems: true })], nextCursor: "opaque-old" };
    },
    (_, input) => {
      assert.equal(input.cursor, "opaque-old");
      return { data: [fragment([user, ask()], { hasLaterItems: true })], nextCursor: "older-unneeded" };
    },
    () => structuredClone(tail),
  ]);
  const result = await runQuestions(s.hub, { params: { ref } });
  assert.deepEqual(
    result.calls.map((c) => c.callId),
    ["call-2", "call-3", "call-4"],
  );
  assert.equal(s.calls.filter((c) => c.method === "thread/turns/list").length, 2);
});

test("paging refuses stale, repeated, overlapping or changing windows", async () => {
  const tail = snapshot([ask(4)], { cursor: "opaque", turn: { hasEarlierItems: true } });
  const stale = Object.assign(new Error("stale"), { evenerErrorInfo: "transcriptItemCursorStale" });
  for (const responses of [
    [
      () => tail,
      () => {
        throw stale;
      },
    ],
    [() => tail, () => ({ data: [], nextCursor: "older" })],
    [() => tail, () => ({ data: [fragment([ask(3)])], nextCursor: "opaque" })],
    [() => tail, () => ({ data: [fragment([user, ask(4)])] })],
    [() => tail, () => ({ data: [fragment([user, ask(3)])] }), () => snapshot([item(5, "userMessage")])],
    [
      () => tail,
      () => ({ data: [fragment([user, ask(3)])] }),
      () => snapshot([ask(4)], { evener: { instanceId: "replacement" }, cursor: "opaque" }),
    ],
  ]) {
    const s = scriptedHub(responses);
    await assert.rejects(runQuestions(s.hub, { params: { ref } }));
    assert.equal(
      s.calls.some((c) => c.method === "turn/start"),
      false,
    );
  }
});

test("answer guards the complete batch and validates its receipt", () =>
  mutation(async () => {
    const s = scriptedHub([
      () => snapshot(),
      (_, input) => {
        assert.deepEqual(input, {
          ref,
          expectedInstanceId: instanceId,
          clientMutationId,
          input: [{ type: "text", text: composeAskAnswers(validateQuestionSelections(pending.questions, selections)) }],
        });
        return receipt();
      },
      () => metadata({ askPending: false }),
    ]);
    const result = await answer(s);
    assert.equal(result.outcome, "acknowledged");
    assert.equal(result.execution, "unverified");
    assert.deepEqual(result.receipt, receipt().receipt);
    assert.equal(s.calls.filter((c) => c.method === "turn/start").length, 1);
  }));

test("readonly default and owned-hub opt-in reject accidental writes before connecting", async () => {
  for (const options of [
    { action: "answer", params: params(), ownedHub },
    { action: "unknown", params: { ref } },
    { params: { ref, approve: true } },
  ]) {
    const s = scriptedHub([]);
    await assert.rejects(runQuestions(s.hub, options));
    assert.equal(s.calls.length, 0);
  }
  await mutation(async () => {
    const s = scriptedHub([]);
    await assert.rejects(runQuestions(s.hub, { action: "answer", params: params(), ownedHub: "ws://other/rpc" }));
    assert.equal(s.calls.length, 0);
  });
});

test("replaced, incomplete, changed and settled decisions never dispatch", () =>
  mutation(async () => {
    for (const before of [
      snapshot([user, ask()], { evener: { instanceId: "replacement" } }),
      snapshot([user, ask()], { evener: { capabilities: { send: false } } }),
      snapshot([user, ask()], { evener: { askPending: false } }),
      snapshot([user, ask()], { turn: { hasLaterItems: true } }),
      snapshot([user, ask(2, [question()], { position: { entry: 1, item: 0 } })]),
      snapshot([user, ask(2, [question()], { position: { entry: -1, item: 0 } })]),
      snapshot([user, ask(2, [question({ why: "changed" })])]),
      snapshot([user, ask(), ask(3)]),
      snapshot([user]),
    ]) {
      const s = scriptedHub([() => before]);
      await assert.rejects(answer(s));
      assert.equal(
        s.calls.some((c) => c.method === "turn/start"),
        false,
      );
    }
  }));

test("caller input stays stable across asynchronous preflight", () =>
  mutation(async () => {
    const input = params();
    const s = scriptedHub([
      () => {
        input.selections[0].resolution.labels = ["Apply"];
        input.reviewedCalls[0].argumentsJson = "{}";
        return snapshot();
      },
      (_, request) => {
        assert.equal(
          request.input[0].text,
          composeAskAnswers(validateQuestionSelections(pending.questions, selections)),
        );
        return receipt();
      },
      () => metadata(),
    ]);
    assert.equal((await answer(s, input)).outcome, "acknowledged");
  }));

test("lost and malformed acknowledgments remain uncertain without replay", () =>
  mutation(async () => {
    for (const response of [
      () => {
        throw undefined;
      },
      () => {
        throw null;
      },
      () => ({}),
      () => receipt({ clientMutationId: "wrong" }),
      () => receipt({ instanceId: "wrong" }),
      () => receipt({ threadId: "wrong" }),
      () => receipt({ turnId: "wrong" }),
      () => receipt({ disposition: "ignored" }),
      () => receipt({ projectionState: "reflected" }),
    ]) {
      const s = scriptedHub([() => snapshot(), response, () => metadata({ askPending: true })]);
      const result = await answer(s);
      assert.equal(result.outcome, "uncertain");
      assert.equal(result.execution, "unverified");
      assert.equal(result.receipt, undefined);
      assert.equal(s.calls.filter((c) => c.method === "turn/start").length, 1);
    }
  }));

test("readback may contain a new question but must retain the binding", () =>
  mutation(async () => {
    const s = scriptedHub([
      () => snapshot(),
      () => receipt({ disposition: "replayed" }),
      () => metadata({ askPending: true }),
    ]);
    assert.equal((await answer(s)).outcome, "acknowledged");
    const replaced = scriptedHub([() => snapshot(), () => receipt(), () => metadata({ instanceId: "replacement" })]);
    await assert.rejects(answer(replaced));
  }));

test("both errors survive when an uncertain write and readback fail", () =>
  mutation(async () => {
    const readError = new Error("read");
    const s = scriptedHub([
      () => snapshot(),
      () => {
        throw undefined;
      },
      () => {
        throw readError;
      },
    ]);
    await assert.rejects(answer(s), (error) => {
      assert.ok(error instanceof AggregateError);
      assert.deepEqual(error.errors, [undefined, readError]);
      return true;
    });
  }));
