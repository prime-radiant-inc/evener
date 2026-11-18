// @vitest-environment node

import { expect, test } from "vitest";
import { composeAskAnswers } from "./askAnswers";
import { answeredAskUserSuffix } from "./askShared";
import { liveAskQuestions } from "./deriveAskQuestions";
import { applyHistory, testHydrate, testHydrateV6 } from "./testing/reducerTestKit";
import { toolWireItems, toolWireModel, toolWireStep } from "./testing/toolWireFixtures";

// agent/testdata/toolwire records a real ask_user call and the user's
// answer after it, written in the [answers] form the clients compose.

test.each(["live", "reloaded"] as const)(
  "the %s answering dock can submit the question Chat encoded as a string",
  (path) => {
    const [call, result] = toolWireItems().filter((item) => item.callId === "call_ask_user");
    if (!call?.argumentsJson || !result?.raw) throw new Error("missing recorded ask_user call or result");
    const sent = JSON.parse(call.argumentsJson).questions;
    expect(typeof sent).toBe("string");
    const [question] = JSON.parse(sent);
    const turn = { id: "turn_1", status: "completed", itemsView: "full" };
    const evener = { askPending: true };
    const model =
      path === "reloaded"
        ? testHydrate({ evener, turns: [{ ...turn, items: [call, result] }] })
        : applyHistory(
            applyHistory(testHydrateV6({ evener }), { turns: [turn], items: [{ ...call, version: 1 }] }, 1001),
            { items: [{ ...result, id: call.id, version: 2 }] },
            1002,
          );
    const [card] = liveAskQuestions(model);
    if (!card) throw new Error(`missing ${path} answer card for ${call.callId}`);
    expect(card).toMatchObject({ callId: call.callId, question: question.question, options: question.options });
    const recommended = card.options.filter((option) => option.recommended).map((option) => option.label);
    expect(
      composeAskAnswers([{ header: card.header, resolution: { kind: "option", labels: recommended }, note: "" }]),
    ).toBe(toolWireItems().find((item) => item.type === "userMessage")?.text);
    expect(model.turns[0]?.items[0]?.argumentsJSON).toBe(call.argumentsJson);
  },
);

test("a settled question reads the answer the user's reply gave", () => {
  expect(answeredAskUserSuffix(toolWireModel(), toolWireStep("call_ask_user"))).toBe(' — answered: "Ship tonight"');
});
