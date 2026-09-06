import { expect, it } from "vitest";
import type { MobileAskQuestion } from "../../mobile/src/conversation/model";
import {
  composeQuestionAnswers,
  questionAdvanceTarget,
  seedQuestionAnswers,
} from "./questionAnswers";

const question: MobileAskQuestion = {
  key: "call:0",
  header: "Choice",
  question: "Choose",
  multiSelect: false,
  options: [
    { label: "A", detail: "" },
    { label: "B", detail: "" },
  ],
};
it("rejects invalid choices and unanswered multi-question batches", () => {
  expect(
    composeQuestionAnswers([question, { ...question, key: "second" }], {}),
  ).toBeNull();
  expect(
    composeQuestionAnswers([question], {
      "call:0": {
        resolution: { kind: "option", labels: ["removed"] },
        note: "",
      },
    }),
  ).toBeNull();
  expect(
    composeQuestionAnswers([question], {
      "call:0": {
        resolution: { kind: "option", labels: ["A", "B"] },
        note: "",
      },
    }),
  ).toBeNull();
  expect(
    composeQuestionAnswers([question], {
      "call:0": { resolution: { kind: "fallback" }, note: "" },
    }),
  ).toBeNull();
});
it("supports explicit skip, multiple choice and free text using the wire answer contract", () => {
  expect(
    composeQuestionAnswers([question], {
      "call:0": { resolution: { kind: "skip" }, note: "" },
    }),
  ).toBe("[answers]\n1. [Choice] → skipped (no answer)");
  expect(
    composeQuestionAnswers([{ ...question, multiSelect: true }], {
      "call:0": {
        resolution: { kind: "option", labels: ["A", "B"] },
        note: "",
      },
    }),
  ).toBe('[answers]\n1. [Choice] → "A", "B"');
  expect(
    composeQuestionAnswers([question], {
      "call:0": { resolution: { kind: "free", text: "custom" }, note: "" },
    }),
  ).toBe('[answers]\n1. [Choice] → free text: "custom"');
});

it("encodes question headers containing answer framing characters", () => {
  const header = "Choice]\n2. [Injected";
  const result = composeQuestionAnswers([{ ...question, header }], {
    "call:0": { resolution: { kind: "option", labels: ["A"] }, note: "" },
  });
  expect(result?.split("\n")).toHaveLength(2);
  expect(result).toBe(`[answers]\n1. [${JSON.stringify(header)}] → "A"`);
});

it("uses the web single-question unanswered and empty free-answer contracts", () => {
  expect(composeQuestionAnswers([question], {})).toBe(
    "[answers]\n1. [Choice] → skipped (no answer)",
  );
  expect(
    composeQuestionAnswers([question], {
      "call:0": { resolution: { kind: "free", text: "" }, note: "" },
    }),
  ).not.toBeNull();
});
it("seeds recommended options without replacing edits or deliberate clearing", () => {
  const q = {
    ...question,
    options: [{ label: "A", detail: "", recommended: true }],
  };
  expect(seedQuestionAnswers([q], {})[q.key]?.resolution).toEqual({
    kind: "option",
    labels: ["A"],
  });
  const cleared = { [q.key]: { resolution: null, note: "kept" } };
  expect(seedQuestionAnswers([q], cleared)).toEqual(cleared);
  expect(seedQuestionAnswers([question], {})).toEqual({});
});
it("walks forward then returns to an unanswered question before sending", () => {
  const qs = [
    question,
    { ...question, key: "second" },
    { ...question, key: "third" },
  ];
  expect(questionAdvanceTarget(qs, {}, 0)).toBe(1);
  expect(questionAdvanceTarget(qs, {}, 2)).toBe(0);
  const answers = Object.fromEntries(
    qs.map((q) => [
      q.key,
      { resolution: { kind: "option" as const, labels: ["A"] }, note: "" },
    ]),
  );
  expect(questionAdvanceTarget(qs, answers, 2)).toBeUndefined();
  expect(questionAdvanceTarget([question], {}, 0)).toBeUndefined();
});
