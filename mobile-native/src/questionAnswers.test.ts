import { expect, it } from "vitest";
import { hydrateThread } from "@evener/appwire-client";
import type {
  AskQuestionRef,
  EvenerThread,
  QueueState,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "@evener/appwire-client";
import {
  MAX_ITEM_BYTES,
  projectConversation,
  type MobileConversation,
  type MobileTimelineItem,
} from "../../mobile/src/conversation/project";
import { truncateItem as storeTruncateItem } from "../../mobile/src/state/conversation";
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

// --- answers compose from canonical refs, never a display-bound copy ------

// The store's live publish path bounds every retained row (state/conversation.ts's
// truncateAndRecord: items.map(truncateItem)), so the question rows a reader
// scrolls carry cut copies of an ask's prose. The answer path must not compose
// from those copies: an option label longer than the display bound would
// submit as its truncated remnant — a label the agent never offered — and two
// options whose labels share a prefix longer than the bound cut to the same
// string, making the pair indistinguishable to the composer's label
// validation. These fixtures reproduce the store's publish exactly: hydrate a
// wire thread, project it, and bound the rows the way the store does.

const ALL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  sharedNotes: false,
  queue: true,
  goal: true,
  rename: true,
};

function askConversation(
  optionLabels: string[],
  multiSelect: boolean,
): MobileConversation {
  const argumentsJson = JSON.stringify({
    questions: [
      {
        header: "Choose",
        question: "Pick one",
        options: optionLabels.map((label) => ({ label, detail: "" })),
        multi_select: multiSelect,
      },
    ],
  });
  const evener: EvenerThread = {
    ref: "ref-1",
    capabilities: ALL_CAPABILITIES,
    queue: { revision: 0 } as QueueState,
    askPending: true,
  };
  const askItem = {
    id: "ask-1",
    turnId: "t1",
    type: "commandExecution",
    toolName: "ask_user",
    status: "completed",
    argumentsJson,
  } as ThreadItem;
  const askTurn: Turn = {
    id: "t1",
    items: [askItem],
    itemsView: "default",
    status: "inProgress",
  };
  const wire: Thread = {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    turns: [askTurn],
    evener,
  };
  const projected = projectConversation(hydrateThread({ thread: wire }, "ref-1", 0));
  const bound = (row: MobileTimelineItem) => storeTruncateItem(row);
  return { ...projected, items: projected.items.map(bound) };
}

it("an oversized option label round-trips its original full label through the answer composer", () => {
  const label = "x".repeat(MAX_ITEM_BYTES + 100);
  const conversation = askConversation([label], false);
  const pending = pendingQuestions(conversation);
  // The ref the sheet answers from must be the label the agent offered, not
  // the display bound's cut copy.
  expect(pending[0]?.options[0]?.label).toBe(label);
  const answer = composeQuestionAnswers(pending, {
    "ask-1:0": { resolution: { kind: "option", labels: [label] }, note: "" },
  });
  expect(answer).toContain(label);
});

it("still submits both distinct options whose labels share a prefix longer than the display bound", () => {
  const sharedPrefix = "y".repeat(MAX_ITEM_BYTES + 50);
  const first = `${sharedPrefix}-first`;
  const second = `${sharedPrefix}-second`;
  const conversation = askConversation([first, second], true);
  const pending = pendingQuestions(conversation);
  // The two options must stay distinguishable after the display bound: on the
  // bounded copies both labels cut to the same string.
  expect(new Set(pending[0]?.options.map((option) => option.label)).size).toBe(2);
  const answer = composeQuestionAnswers(pending, {
    "ask-1:0": {
      resolution: { kind: "option", labels: [first, second] },
      note: "",
    },
  });
  expect(answer).toContain(`"${first}", "${second}"`);
});
