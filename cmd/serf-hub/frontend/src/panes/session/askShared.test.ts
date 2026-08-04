// @vitest-environment node
import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { answeredAskUserSuffix, parseAskUserQuestions } from "./askShared";

// Direct unit coverage for the shared ask_user parsing helper (extracted
// from transcript/tools/askUser.tsx per the wave-5 T4 manifest so the
// composer's answering dock can reuse it without duplicating the shape
// checks). transcript/tools/askUser.test.tsx continues to exercise the
// same logic transitively through the registered tool renderer - these
// tests exist because askDock depends on this module directly and its
// own correctness must be verified independently of that renderer.

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function askUserArgs(questions: unknown[]): string {
  return JSON.stringify({ questions });
}

const ONE_QUESTION = [
  {
    header: "Deploy?",
    question: "Should I deploy this to production now?",
    options: [
      { label: "Yes", detail: "Ship it", recommended: true },
      { label: "No", detail: "Wait" },
    ],
  },
];

test("parses a well-formed single question with two options", () => {
  const result = parseAskUserQuestions(item({ argumentsJSON: askUserArgs(ONE_QUESTION) }));
  expect(result).toEqual([
    {
      header: "Deploy?",
      question: "Should I deploy this to production now?",
      options: [
        { label: "Yes", detail: "Ship it", recommended: true },
        { label: "No", detail: "Wait", recommended: false },
      ],
      multiSelect: false,
      why: undefined,
      ifUnanswered: undefined,
    },
  ]);
});

test("uses a stable Question N fallback when headers are omitted", () => {
  const questions = [
    { question: "First?", options: [{ label: "A", detail: "a" }] },
    { question: "Second?", options: [{ label: "B", detail: "b" }] },
  ];
  const result = parseAskUserQuestions(item({ argumentsJSON: askUserArgs(questions) }));
  expect(result?.map((question) => question.header)).toEqual(["Question 1", "Question 2"]);
});

test("carries multi_select, why, and if_unanswered through when present", () => {
  const questions = [{ ...ONE_QUESTION[0], multi_select: true, why: "affects release", if_unanswered: "assume yes" }];
  const result = parseAskUserQuestions(item({ argumentsJSON: askUserArgs(questions) }));
  expect(result).toEqual([
    expect.objectContaining({ multiSelect: true, why: "affects release", ifUnanswered: "assume yes" }),
  ]);
});

test("returns undefined for absent argumentsJSON", () => {
  expect(parseAskUserQuestions(item())).toBeUndefined();
});

test("returns undefined for a JSON syntax error", () => {
  expect(parseAskUserQuestions(item({ argumentsJSON: "{not json" }))).toBeUndefined();
});

test("returns undefined for valid JSON with no questions array", () => {
  expect(parseAskUserQuestions(item({ argumentsJSON: "{}" }))).toBeUndefined();
});

test("returns undefined when questions is present but empty", () => {
  expect(parseAskUserQuestions(item({ argumentsJSON: askUserArgs([]) }))).toBeUndefined();
});

test("skips an individual malformed question but keeps the well-formed ones", () => {
  const questions = [ONE_QUESTION[0], { header: "Bad" }];
  const result = parseAskUserQuestions(item({ argumentsJSON: askUserArgs(questions) }));
  expect(result).toHaveLength(1);
  expect(result?.[0]?.header).toBe("Deploy?");
});

test("drops a question whose every option is malformed (no usable options left)", () => {
  const questions = [{ header: "H", question: "Q", options: [{ label: 123 }] }];
  expect(parseAskUserQuestions(item({ argumentsJSON: askUserArgs(questions) }))).toBeUndefined();
});

test("filters out an individual malformed option but keeps the well-formed ones in the same question", () => {
  const questions = [
    {
      header: "H",
      question: "Q",
      options: [{ label: "Good", detail: "ok" }, { label: "missing detail" }],
    },
  ];
  const result = parseAskUserQuestions(item({ argumentsJSON: askUserArgs(questions) }));
  expect(result).toEqual([expect.objectContaining({ options: [{ label: "Good", detail: "ok", recommended: false }] })]);
});

// answeredAskUserSuffix (kata h70z): the collapsed row's "— answered: ..."
// recap, read back from a LATER [answers] userMessage this item alone
// can't see.

function userMessage(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "reply_1", turnId: "turn_2", type: "userMessage", text: "", ...overrides };
}

function threadModel(items: ItemModel[]): ThreadModel {
  const turns: TurnModel[] = [{ id: "turn_1", status: "completed", items }];
  return { turns } as unknown as ThreadModel;
}

test("answeredAskUserSuffix reads the answer back from a later [answers] reply", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) }); // header "Deploy?"
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBe(' — answered: "Yes"');
});

test("answeredAskUserSuffix strips a trailing note from the recap", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes" — note: "ship it now"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBe(' — answered: "Yes"');
});

test("answeredAskUserSuffix labels each answer by header when a call carries multiple questions", () => {
  const deployQuestion = ONE_QUESTION[0];
  if (deployQuestion === undefined) throw new Error("ONE_QUESTION[0] must exist");
  const twoQuestions = [
    deployQuestion,
    { header: "Rollback?", question: "If it breaks?", options: deployQuestion.options },
  ];
  const ask = item({ argumentsJSON: askUserArgs(twoQuestions) });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"\n2. [Rollback?] → "No"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBe(' — answered: Deploy?: "Yes", Rollback?: "No"');
});

test("answeredAskUserSuffix returns undefined when no [answers] reply exists yet (still pending)", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) });
  expect(answeredAskUserSuffix(threadModel([ask]), ask)).toBeUndefined();
});

test("answeredAskUserSuffix returns undefined for an errored call", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), error: "denied" });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBeUndefined();
});

test("answeredAskUserSuffix returns undefined when the call carries no parseable questions", () => {
  const ask = item({ argumentsJSON: "{}" });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBeUndefined();
});

test("answeredAskUserSuffix ignores a same-shaped [answers] line carried by a non-userMessage item (type check, not just prefix)", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) });
  const notAReply: ItemModel = {
    id: "sys_1",
    turnId: "turn_2",
    type: "systemMessage",
    text: '[answers]\n1. [Deploy?] → "Yes"',
  };
  expect(answeredAskUserSuffix(threadModel([ask, notAReply]), ask)).toBeUndefined();
});

test("answeredAskUserSuffix ignores a plain user message that isn't a composed [answers] reply", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) });
  const reply = userMessage({ text: "sure, go with Yes" });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBeUndefined();
});

test("answeredAskUserSuffix ignores an unrelated [answers] reply that doesn't mention this call's header", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) }); // header "Deploy?"
  const reply = userMessage({ text: '[answers]\n1. [Other] → "Yes"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBeUndefined();
});

test("answeredAskUserSuffix finds the reply across multiple turns", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"', turnId: "turn_2" });
  const model: ThreadModel = {
    turns: [
      { id: "turn_1", status: "completed", items: [ask] },
      { id: "turn_2", status: "completed", items: [reply] },
    ],
  } as unknown as ThreadModel;
  expect(answeredAskUserSuffix(model, ask)).toBe(' — answered: "Yes"');
});
