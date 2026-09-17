import { expect, it } from "vitest";
import type { AskQuestionRef } from "@evener/appwire-client";
import { hydrateThread } from "@evener/appwire-client";
import type { Thread } from "@evener/appwire-client";
import type { MobileConversation } from "../../mobile/src/conversation/project";
import { projectConversation } from "../../mobile/src/conversation/project";
import {
  composeQuestionAnswers,
  pendingQuestions,
  questionAdvanceTarget,
  seedQuestionAnswers,
} from "./questionAnswers";

const question: AskQuestionRef = {
  key: "call:0",
  callId: "call",
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

// What the phone offers to answer is what the MODEL says is answerable, through
// the package's own rule — the same call the projection's question rows come from,
// so the refs are canonical (uncut) while the rows a reader scrolls carry the
// display bound's copies. The wire's thread-level askPending is not consulted: it
// can be true for an ask this window does not hold, where there is nothing here to
// answer.
it("offers the model's answerable asks", () => {
  const conversation = projectConversation(
    hydrateThread(
      {
        thread: {
          id: "thread-1",
          sessionId: "session-1",
          preview: "",
          ephemeral: false,
          modelProvider: "anthropic",
          createdAt: 0,
          updatedAt: 0,
          status: { type: "idle" },
          cwd: "",
          cliVersion: "",
          source: "",
          turns: [
            {
              id: "t1",
              status: "completed",
              itemsView: "default",
              items: [
                {
                  id: "ask-1",
                  turnId: "t1",
                  type: "commandExecution",
                  toolName: "ask_user",
                  status: "completed",
                  argumentsJson: JSON.stringify({
                    questions: [
                      {
                        header: "Choice",
                        question: "Choose",
                        options: [{ label: "A", detail: "" }],
                        multi_select: false,
                      },
                    ],
                  }),
                },
              ],
            },
          ],
          evener: { ref: "ref-1", askPending: false },
        } as unknown as Thread,
      },
      "ref-1",
      0,
    ),
  );
  expect(pendingQuestions(conversation).map((ref) => ref.header)).toEqual(["Choice"]);
  expect(conversation.items.some((row) => row.kind === "question")).toBe(true);
});

it("offers nothing when nothing is answerable, whatever the wire's flag says", () => {
  const conversation = {
    askPending: true,
    turns: [],
    items: [{ kind: "user", id: "u1", markdown: "hi" }],
  } as unknown as MobileConversation;
  expect(pendingQuestions(conversation)).toEqual([]);
  expect(pendingQuestions(null)).toEqual([]);
});
