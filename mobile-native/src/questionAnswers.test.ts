import { expect, it } from "vitest";
import type { AskQuestionRef } from "@evener/appwire-client";
import { hydrateThread } from "@evener/appwire-client";
import type { Thread } from "@evener/appwire-client";
import type { MobileConversation } from "../../mobile/src/conversation/project";
import {
  boundQuestion,
  MAX_ITEM_BYTES,
  projectConversation,
  truncateText,
} from "../../mobile/src/conversation/project";
import {
  composeQuestionAnswers,
  pendingQuestions,
  questionAdvanceTarget,
  questionsIdentity,
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

// The server resolves a pending ask on an interrupt or a user steer exactly
// like a plain user message (agent/session_lifecycle.go: an interrupted
// turn calls clearAskPending directly and appends a steering turn carrying
// SteeringKindInterrupted; a user steer enters the drain loop as
// EntryUserInput, the same accepted-turn path that clears askPending for a
// plain user message). The phone must not keep offering a question the hub
// has already closed out.
it.each([
  ["an interrupt", { steeringKind: "interrupted" }],
  ["a user steer", { source: "user" }],
])("offers nothing once %s resolves the ask", (_label, steeringFields) => {
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
                {
                  id: "steer-1",
                  turnId: "t1",
                  type: "steering",
                  text: "resolved",
                  ...steeringFields,
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
  expect(pendingQuestions(conversation)).toEqual([]);
  expect(conversation.items.some((row) => row.kind === "question")).toBe(false);
});

it("bounds a question's display copy while the canonical refs stay uncut", () => {
  const bound = (text: string) => truncateText(text, MAX_ITEM_BYTES);
  const huge = "x".repeat(MAX_ITEM_BYTES + 10);
  const oversized: AskQuestionRef = {
    ...question,
    header: huge,
    question: huge,
    why: huge,
    options: [{ label: huge, detail: huge }],
  };
  const display = boundQuestion(oversized, bound);
  expect(display.header).toBe(truncateText(huge, MAX_ITEM_BYTES));
  expect(display.header.length).toBeLessThan(huge.length);
  expect(display.question).toBe(truncateText(huge, MAX_ITEM_BYTES));
  expect(display.why).toBe(truncateText(huge, MAX_ITEM_BYTES));
  expect(display.options[0]?.label).toBe(truncateText(huge, MAX_ITEM_BYTES));
  expect(display.options[0]?.detail).toBe(truncateText(huge, MAX_ITEM_BYTES));
  // The refs boundQuestion was given are untouched: composition still names
  // the exact, uncut label the agent's options carried.
  expect(oversized.header).toBe(huge);
  expect(oversized.options[0]?.label).toBe(huge);
  expect(
    composeQuestionAnswers([oversized], {
      [oversized.key]: {
        resolution: { kind: "option", labels: [huge] },
        note: "",
      },
    }),
  ).toContain(huge);
});

it("bounds a question set's identity so a React key, the sheet's signature and its persisted draft never carry the full prose", () => {
  const huge = "x".repeat(MAX_ITEM_BYTES * 50);
  const oversized: AskQuestionRef = { ...question, header: huge };
  const identity = questionsIdentity([oversized]);
  // Bounded regardless of how oversized the input is — the identity does not
  // scale with it.
  expect(new TextEncoder().encode(identity).length).toBeLessThan(huge.length / 2);
  // Stable for byte-identical input, but still distinguishes different
  // (bounded) content — an identity that never changes could not tell a
  // batch's questions apart across a real edit.
  expect(questionsIdentity([oversized])).toBe(identity);
  expect(
    questionsIdentity([{ ...oversized, header: "different" }]),
  ).not.toBe(identity);
  // The canonical ref handed to questionsIdentity is untouched — composition
  // still reads the exact, uncut prose the agent sent.
  expect(oversized.header).toBe(huge);
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
