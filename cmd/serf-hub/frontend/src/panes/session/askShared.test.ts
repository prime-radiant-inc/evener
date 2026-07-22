// @vitest-environment node
import { expect, test } from "vitest";
import type { ItemModel } from "../../protocol/model";
import { parseAskUserQuestions } from "./askShared";

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
