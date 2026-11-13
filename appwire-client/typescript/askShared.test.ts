// @vitest-environment node
import { expect, test } from "vitest";
import { answeredAskUserSuffix, parseAskUserQuestions } from "./askShared";
import type { ItemModel, ThreadModel, TurnModel } from "./model";

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

// --- the single-question shorthand form (see askShared.ts for the form) ---

test("parses the single-question shorthand form into one question", () => {
  const shorthand = JSON.stringify({
    question: "Should I deploy this to production now?",
    options: [
      { label: "Yes", detail: "Ship it", recommended: true },
      { label: "No", detail: "Wait" },
    ],
  });
  expect(parseAskUserQuestions(item({ argumentsJSON: shorthand }))).toEqual([
    {
      header: "Question 1",
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

test("carries the shorthand form's header, multi_select, why and if_unanswered", () => {
  const shorthand = JSON.stringify({
    header: "Deploy?",
    question: "Ship it?",
    options: [{ label: "Yes", detail: "now" }],
    multi_select: true,
    why: "affects release",
    if_unanswered: "assume yes",
  });
  expect(parseAskUserQuestions(item({ argumentsJSON: shorthand }))).toEqual([
    expect.objectContaining({
      header: "Deploy?",
      multiSelect: true,
      why: "affects release",
      ifUnanswered: "assume yes",
    }),
  ]);
});

test("shorthand with question but no options stays unavailable (Go rejects it at dispatch)", () => {
  const shorthand = JSON.stringify({ question: "Q?" });
  expect(parseAskUserQuestions(item({ argumentsJSON: shorthand }))).toBeUndefined();
});

test("shorthand with no usable options stays unavailable (Go rejects it at dispatch)", () => {
  const shorthand = JSON.stringify({ question: "Q?", options: [{ label: 5 }] });
  expect(parseAskUserQuestions(item({ argumentsJSON: shorthand }))).toBeUndefined();
});

// Go's wrap drops a non-string or empty header (normalizeAskUserArgs's type
// assertion), so such a call is accepted and live with the default header.
// The raw object must not pass through to parseQuestion, whose shape check
// treats a present-but-non-string header as a malformed question - the
// recorded call would read back as unavailable and the dock would render
// nothing again.
test("shorthand with a null header parses into the default header", () => {
  const shorthand = JSON.stringify({
    question: "Q?",
    options: [{ label: "A", detail: "a" }],
    header: null,
  });
  expect(parseAskUserQuestions(item({ argumentsJSON: shorthand }))).toEqual([
    expect.objectContaining({ header: "Question 1", question: "Q?" }),
  ]);
});

test("shorthand with an empty header parses into the default header (Go drops it when wrapping)", () => {
  const shorthand = JSON.stringify({
    question: "Q?",
    options: [{ label: "A", detail: "a" }],
    header: "",
  });
  expect(parseAskUserQuestions(item({ argumentsJSON: shorthand }))).toEqual([
    expect.objectContaining({ header: "Question 1", question: "Q?" }),
  ]);
});

test("a questions array stays authoritative when shorthand fields sit beside it", () => {
  // Go rejects a call carrying both forms outright (normalizeAskUserArgs's
  // both-forms case), so this shape only reaches a recorded item as a rejected
  // call; the parser stays deterministic either way: the batch form wins.
  const both = JSON.stringify({
    questions: [{ header: "Batch", question: "batch question", options: [{ label: "B", detail: "b" }] }],
    question: "stray shorthand question",
    options: [{ label: "S", detail: "s" }],
  });
  const result = parseAskUserQuestions(item({ argumentsJSON: both }));
  expect(result?.map((question) => question.header)).toEqual(["Batch"]);
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

// A thread of one turn per item group, for the reply-finding tests whose
// answer lives in a later turn than the call.
function turnsModel(...groups: ItemModel[][]): ThreadModel {
  const turns: TurnModel[] = groups.map((items, at) => ({ id: `turn_${at + 1}`, status: "completed", items }));
  return { turns } as unknown as ThreadModel;
}

test("answeredAskUserSuffix reads the answer back from a later [answers] reply", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION) }); // header "Deploy?"
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"' });
  expect(answeredAskUserSuffix(threadModel([ask, reply]), ask)).toBe(' — answered: "Yes"');
});

test("answeredAskUserSuffix decodes a JSON-encoded unsafe header", () => {
  const unsafeHeader = "A]B\n<C&\u2028";
  const ask = item({ argumentsJSON: askUserArgs([{ ...ONE_QUESTION[0], header: unsafeHeader }]) });
  const reply = userMessage({ text: '[answers]\n1. ["A]B\\n<C&\\u2028"] → "Yes"' });
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

// No link, no composed shape: nothing names this message as the round's
// answer, so it stays ignored (the link, not the text shape, is the durable
// evidence; the composed form is only its fallback).
test("answeredAskUserSuffix ignores a plain user message with no answer link and no composed shape", () => {
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

// --- the durable answer link (issue #5836) ----------------------------
//
// The wire stamps the reply of an answered round with
// ThreadItem.observation.answerToTurnIds naming the question's turn (see
// model.ts's answerToTurnIds note). A reply that carries the link is the
// round's answer by server-stamped evidence, whatever its text looks like:
// the composed [answers] form is only ONE of the two reply forms the ask
// spec allows (docs/superpowers/specs/2026-07-03-ask-user-question-tool-design.md §4.3:
// "either the numbered [answers] form ... or free prose").

test("answeredAskUserSuffix resolves a linked reply by its answer link, not its text shape", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({ text: "sure, go with Yes", turnId: "turn_2", answerToTurnIds: ["turn_1"] });
  expect(answeredAskUserSuffix(turnsModel([ask], [reply]), ask)).toBe(" — answered: sure, go with Yes");
});

test("a linked reply keeps the composed [answers] recap, parsing resolutions by header", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({
    text: '[answers]\n1. [Deploy?] → "Yes"',
    turnId: "turn_2",
    answerToTurnIds: ["turn_1"],
  });
  expect(answeredAskUserSuffix(turnsModel([ask], [reply]), ask)).toBe(' — answered: "Yes"');
});

test("a reply linked to another round never answers this call", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"', turnId: "turn_2", answerToTurnIds: ["turn_8"] });
  expect(answeredAskUserSuffix(turnsModel([ask], [reply]), ask)).toBeUndefined();
});

test("a linked user-authored steering reply answers the call (the ask fence records answers as steering)", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply: ItemModel = {
    id: "reply_1",
    turnId: "turn_2",
    type: "steering",
    source: "user",
    text: "use Postgres",
    answerToTurnIds: ["turn_1"],
  };
  expect(answeredAskUserSuffix(turnsModel([ask], [reply]), ask)).toBe(" — answered: use Postgres");
});

test("a linked human-note steer is never the answer carrier", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  // The whiteboard speaking through the user-sourced steering rail, link and
  // all: not the human answering.
  const note: ItemModel = {
    id: "reply_1",
    turnId: "turn_2",
    type: "steering",
    source: "user",
    steeringKind: "human-note",
    text: "remember the DB choice",
    answerToTurnIds: ["turn_1"],
  };
  expect(answeredAskUserSuffix(turnsModel([ask], [note]), ask)).toBeUndefined();
});

test("a linked interrupted steer is not the answer carrier: a cancelled round never recaps as answered", () => {
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  // The daemon appends an interrupted steer as the marker of a cancelled
  // round (session_lifecycle.go), and the fence can stamp it with the link
  // of the ask it resolved. Resolving an ask is not answering it.
  const interrupted: ItemModel = {
    id: "reply_1",
    turnId: "turn_2",
    type: "steering",
    source: "user",
    steeringKind: "interrupted",
    text: "user cancelled the round",
    answerToTurnIds: ["turn_1"],
  };
  expect(answeredAskUserSuffix(turnsModel([ask], [interrupted]), ask)).toBeUndefined();
});

test("a composed reply from before a later round's linked reply still answers its own call", () => {
  // Mixed thread: this round's reply predates the link-stamping daemon, so
  // only the composed shape identifies it; a LATER round carries links. The
  // foreign link must not erase the earlier reply this call already found.
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"', turnId: "turn_2" });
  const laterAsk = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_3" });
  const laterReply = userMessage({
    text: '[answers]\n1. [Deploy?] → "No"',
    turnId: "turn_4",
    answerToTurnIds: ["turn_3"],
  });
  expect(answeredAskUserSuffix(turnsModel([ask], [reply], [laterAsk], [laterReply]), ask)).toBe(' — answered: "Yes"');
});

test("a linked reply whose composed text parses to nothing recaps nothing, not the raw framing", () => {
  // The composed form with its numbering stripped (the user edited the draft
  // in the composer, spec §6.1) parses to an empty map: an attempted composed
  // reply that says nothing is not free prose to echo back as the answer.
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({ text: "[answers]\nDeploy? Yes", turnId: "turn_2", answerToTurnIds: ["turn_1"] });
  expect(answeredAskUserSuffix(turnsModel([ask], [reply]), ask)).toBeUndefined();
});

test("a linked reply never guesses between same-execution ask calls", () => {
  // The link names the execution turn, not the individual call, and the
  // fence keeps a call appended after a scoped answer pending even though
  // it shares the answered call's turn (agent's
  // TestAskFenceScopedAnswerPreservesPostAdmissionAppend). With two
  // answerable calls in one execution, a free-prose reply's turn link
  // cannot say which call it resolved - the recap shows nothing rather
  // than claim a specific one answered.
  const first = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1", id: "ask_1" });
  const second = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1", id: "ask_2" });
  const reply = userMessage({ text: "do the first thing", turnId: "turn_2", answerToTurnIds: ["turn_1"] });
  const model = turnsModel([first, second], [reply]);
  expect(answeredAskUserSuffix(model, first)).toBeUndefined();
  expect(answeredAskUserSuffix(model, second)).toBeUndefined();
});

test("a linked composed reply still recaps a sibling call it answers by a distinct header", () => {
  // The dock composes ONE [answers] reply for the whole pending set, naming
  // each answer by header: when sibling calls ask distinct headers, the
  // reply identifies its calls exactly and must not be suppressed.
  const alpha = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1", id: "ask_alpha" });
  const beta = item({
    argumentsJSON: askUserArgs([
      { header: "Beta", question: "Which database?", options: [{ label: "Postgres", detail: "ok" }] },
    ]),
    turnId: "turn_1",
    id: "ask_beta",
  });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"', turnId: "turn_2", answerToTurnIds: ["turn_1"] });
  const model = turnsModel([alpha, beta], [reply]);
  expect(answeredAskUserSuffix(model, alpha)).toBe(' — answered: "Yes"');
  expect(answeredAskUserSuffix(model, beta)).toBeUndefined();
});

test("a linked composed reply answering a header a sibling also asks recaps neither call", () => {
  // Both calls ask "Deploy?": the shared header cannot say which call the
  // reply answered, so neither claims it.
  const alpha = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1", id: "ask_alpha" });
  const beta = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1", id: "ask_beta" });
  const reply = userMessage({ text: '[answers]\n1. [Deploy?] → "Yes"', turnId: "turn_2", answerToTurnIds: ["turn_1"] });
  const model = turnsModel([alpha, beta], [reply]);
  expect(answeredAskUserSuffix(model, alpha)).toBeUndefined();
  expect(answeredAskUserSuffix(model, beta)).toBeUndefined();
});

test("a free-prose recap carries only the reply's first line, with an ellipsis for the rest", () => {
  // The recap's home is a single collapsed row; spec §4.3 lets the reply be
  // any free prose, so a multi-paragraph answer must not be pasted whole
  // into the ask row's summary.
  const ask = item({ argumentsJSON: askUserArgs(ONE_QUESTION), turnId: "turn_1" });
  const reply = userMessage({
    text: "Option B — but see my notes below:\nActually use Postgres everywhere, and then re-run the migration.",
    turnId: "turn_2",
    answerToTurnIds: ["turn_1"],
  });
  expect(answeredAskUserSuffix(turnsModel([ask], [reply]), ask)).toBe(
    " — answered: Option B — but see my notes below: …",
  );
});
