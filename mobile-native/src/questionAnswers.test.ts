import { expect, it } from "vitest";
import type { MobileAskQuestion } from "../../mobile/src/conversation/model";
import { composeQuestionAnswers } from "./questionAnswers";

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
it("requires an explicit valid answer for every displayed question", () => {
  expect(composeQuestionAnswers([question], {})).toBeNull();
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
