import { expect, it, vi } from "vitest";
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
  boundQuestion,
  MAX_ITEM_BYTES,
  projectConversation,
  type MobileConversation,
  type MobileTimelineItem,
  truncateText,
} from "./projectedRows";
import { truncateItem as storeTruncateItem } from "../../mobile/src/state/conversation";
import {
  composeQuestionAnswers,
  pendingQuestions,
  questionAdvanceTarget,
  questionDefinition,
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
          evener: { ref: "ref-1", askPending: true },
        } as unknown as Thread,
      },
      "ref-1",
      0,
    ),
  );
  expect(pendingQuestions(conversation).map((ref) => ref.header)).toEqual(["Choice"]);
  expect(conversation.items.some((row) => row.kind === "question")).toBe(true);
});

// pendingQuestions must answer from the canonical refs (liveAsksFor), never
// from a display-bound copy: a store that wires project.ts's truncateItem
// into its own cap-and-truncate pass (mobile/src/state/conversation.ts) can
// bound a "question" row's option labels for display, and two options that
// share a prefix past that bound collide once cut — composeQuestionAnswers
// must validate a selection against the full label, or a cut label could
// pass as a choice the agent never actually offered under its real name.
// projectConversation itself never truncates (bounding is a pass a store
// applies afterward), so this checks pendingQuestions' own refs directly
// rather than reproducing that store pass here.
it("returns the option's full, untruncated label regardless of size", () => {
  const prefix = "x".repeat(MAX_ITEM_BYTES);
  const labelA = `${prefix}-first-option`;
  const labelB = `${prefix}-second-option`;
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
                        options: [
                          { label: labelA, detail: "" },
                          { label: labelB, detail: "" },
                        ],
                        multi_select: false,
                      },
                    ],
                  }),
                },
              ],
            },
          ],
          evener: { ref: "ref-1", askPending: true },
        } as unknown as Thread,
      },
      "ref-1",
      0,
    ),
  );
  const [refs] = pendingQuestions(conversation);
  const labels = refs?.options.map((o) => o.label);
  expect(labels).toEqual([labelA, labelB]);
});

// liveAskQuestions (the package's deriveAskQuestions.ts) has "no memory of
// its own" by its own doc comment: every call re-parses every pending
// ask_user's argumentsJson. projectConversation's own default argument pays
// that scan once per publish; pendingQuestions used to pay it again for the
// exact same conversation — same model, same turns, same answerable asks,
// computed twice. liveAsksFor (project.ts) shares the one scan between them,
// keyed on model.turns (the array projectConversation's spread carries onto
// the MobileConversation it returns), so a caller reading the conversation
// this publish already produced never re-parses it.
it("shares one askQuestionsByCall scan between projectConversation and pendingQuestions", () => {
  const askArgs = JSON.stringify({
    questions: [{ header: "MARK-1", question: "q", options: [{ label: "A", detail: "" }] }],
  });
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
            { id: "t0", status: "completed", itemsView: "default", items: [{ id: "u0", turnId: "t0", type: "userMessage", text: "hi" }] },
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
                  argumentsJson: askArgs,
                },
              ],
            },
          ],
          evener: { ref: "ref-1", askPending: true },
        } as unknown as Thread,
      },
      "ref-1",
      0,
    ),
  );

  const parse = vi.spyOn(JSON, "parse");
  try {
    parse.mockClear();
    // No notification landed: this is the exact conversation projectConversation
    // already produced, read the way screens.tsx reads it (repeatedly, across
    // several call sites, on the same store snapshot).
    pendingQuestions(conversation);
    pendingQuestions(conversation);
    const mark1Parses = parse.mock.calls.filter((call) => String(call[0]).includes("MARK-1"));
    expect(mark1Parses).toHaveLength(0);
    // The shared derivation still names the pending question correctly.
    expect(pendingQuestions(conversation)).toMatchObject([{ header: "MARK-1" }]);
  } finally {
    parse.mockRestore();
  }
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

// The bounded identity is a hash of the canonical fields, not a truncated
// copy of them: two questions whose headers agree only up to the display
// bound (MAX_ITEM_BYTES) and then genuinely differ must still get distinct
// identities, or a React key stops remounting the sheet and a persisted
// draft survives a question the agent actually changed.
it("distinguishes two oversized questions that share a prefix past the display bound", () => {
  const prefix = "x".repeat(MAX_ITEM_BYTES * 4);
  const headerA = `${prefix}-first-question-tail`;
  const headerB = `${prefix}-second-question-tail`;
  const a = questionsIdentity([{ ...question, header: headerA }]);
  const b = questionsIdentity([{ ...question, header: headerB }]);
  expect(a).not.toBe(b);
});

// The identity signs each question twice over: the canonical digest that
// distinguishes questions (above), and the digest of the display-bound copy
// an older build persisted as a draft's definition (the pre-identity sheet
// serialized the bounded timeline rows, so draftRepository.ts's comparison
// needs that digest to recognize a stored truncated copy). The two digests
// agree exactly where the bounded copies do — and never on an oversized
// question, whose cut copy differs from its canonical form.
it("signs each question with its canonical digest and the digest of its bounded copy", () => {
  const huge = "x".repeat(MAX_ITEM_BYTES + 10);
  const [element] = JSON.parse(questionsIdentity([{ ...question, header: huge }]));
  expect(element.key).toBe(question.key);
  expect(element.digest).not.toBe(element.boundDigest);
  const prefix = "x".repeat(MAX_ITEM_BYTES * 4);
  const [a] = JSON.parse(
    questionsIdentity([{ ...question, header: `${prefix}-first-tail` }]),
  );
  const [b] = JSON.parse(
    questionsIdentity([{ ...question, header: `${prefix}-second-tail` }]),
  );
  // The bounded digests agree where the bounded copies do — and the
  // canonical digests still tell the two questions apart.
  expect(a.boundDigest).toBe(b.boundDigest);
  expect(a.digest).not.toBe(b.digest);
});

// Every persisted era's element normalizes to the one digest the identity
// computes for the same canonical question, whatever order its era's builder
// wrote the fields in — or what it wrapped them in. History's producers each
// serialized the same question differently: the display era's rows wrapped
// the canonical ref inside a nested bounded twin (d0f40080cb), the landed
// checkpoint's builder wrote key-first with callId spread on last (#1096),
// and #1488's shim appended key and callId after the parsed wire fields.
// questionDefinition rebuilds each into the package's own field order before
// hashing, so the digest names the question, never the era that persisted it.
it("normalizes every persisted era's element to the canonical question's digest", () => {
  const canonical = questionDefinition(question);
  const eras = [
    // d0f40080cb's withQuestionDisplay: the canonical ref beside the
    // store-bounded display twin.
    {
      ...question,
      display: {
        header: question.header,
        question: question.question,
        options: question.options.map((option) => ({
          label: option.label,
          detail: option.detail,
        })),
      },
    },
    // 96dc079d06's projection question, callId spread on last.
    {
      key: question.key,
      header: question.header,
      question: question.question,
      options: question.options,
      multiSelect: question.multiSelect,
      callId: question.callId,
    },
    // 66727cbe6c's shim question: parsed fields first, key and callId last.
    {
      header: question.header,
      question: question.question,
      options: question.options,
      multiSelect: question.multiSelect,
      key: question.key,
      callId: question.callId,
    },
  ];
  for (const era of eras) expect(questionDefinition(era)).toEqual(canonical);
});

// Recomputing a hash over the full canonical payload on every render/keystroke
// is exactly the O(payload)-per-render cost this identity exists to avoid
// paying twice: the SAME question array (the reference reconcileBatches.ts
// hands back unchanged, per its own "same array when nothing changed" rule)
// must answer from a memo, not rehash. Counted, not timed (a wall-clock
// delta flakes under scheduler pauses or a loaded CI runner): questionHash's
// digest inputs are built with JSON.stringify — once over the canonical
// question and once over its bounded copy — so a spy on the global counts
// exactly one fresh-computation pass per distinct array reference, and zero
// for every memoized read of the same one.
it("memoizes by question-array reference instead of rehashing on every call", () => {
  const many: AskQuestionRef[] = Array.from({ length: 20 }, (_, i) => ({
    ...question,
    key: `call_${i}:0`,
    callId: `call_${i}`,
    header: `header-${i}`,
  }));

  const stringifySpy = vi.spyOn(JSON, "stringify");
  const first = questionsIdentity(many);
  const firstCallCount = stringifySpy.mock.calls.length;
  expect(firstCallCount).toBeGreaterThan(0); // the fresh pass really did stringify something

  stringifySpy.mockClear();
  for (let i = 0; i < 200; i++) {
    expect(questionsIdentity(many)).toBe(first);
  }
  // A memoized read returns before touching JSON.stringify at all.
  expect(stringifySpy).not.toHaveBeenCalled();
  stringifySpy.mockRestore();
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
