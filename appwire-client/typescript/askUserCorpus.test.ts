// @vitest-environment node

import { expect, test } from "vitest";
import { composeAskAnswers } from "./askAnswers";
import { answeredAskUserSuffix, parseAskUserQuestions } from "./askShared";
import { toolWireItems, toolWireModel, toolWireStep } from "./testing/toolWireFixtures";

// agent/testdata/toolwire records a real ask_user call and the user's
// answer after it, written in the [answers] form the clients compose.

test("the recorded answer is what the clients compose for choosing the recommended option", () => {
  const [question] = parseAskUserQuestions(toolWireStep("call_ask_user")) ?? [];
  const reply = toolWireItems().find((item) => item.type === "userMessage");
  expect(reply?.text).toBe(
    composeAskAnswers([
      { header: question?.header, resolution: { kind: "option", labels: ["Ship tonight"] }, note: "" },
    ]),
  );
});

test("a settled question reads the answer the user's reply gave", () => {
  expect(answeredAskUserSuffix(toolWireModel(), toolWireStep("call_ask_user"))).toBe(' — answered: "Ship tonight"');
});
