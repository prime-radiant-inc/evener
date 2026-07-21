import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./askUser";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

// A unique id per call (rather than a fixed "item_1"): helpers.ts's
// rememberedArgs caches parsed args by callId/id, module-scoped for the
// whole test file's run, so two tests sharing one id can silently read
// each other's cached args when this file's own malformed/blank-args
// cases resolve to {} (an empty object also skips the cache write, per
// rememberedArgs' own contract) - confirmed live: an earlier version of
// this file's "fallback" tests intermittently read a PRIOR test's cached
// question data through exactly this path.
let nextId = 0;
function item(overrides: Partial<ItemModel> = {}): ItemModel {
  nextId += 1;
  return { id: `item_${nextId}`, turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// Ground truth: agent/internal/tool/definitions.go's DefAskUser - exact
// argumentsJson shape: {questions:[{header(<=12 chars), question,
// options:[{label,detail,recommended?}], multi_select?, why?,
// if_unanswered?}]}, 1-4 questions. ask_user's own Output is always the
// SAME fixed string on success (agent/session_tools_ask.go's
// askUserAckText) - not useful for rendering, so this descriptor reads
// entirely from argumentsJSON (rememberedArgs, since it settles like
// every other tool call - see helpers.ts's own header).

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

// --- summary ----------------------------------------------------------

test("summary lists each question's header, bracketed and comma-joined", () => {
  const d = toolRendererFor("ask_user");
  expect(d.summary(item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) }))).toBe(
    "Asked: [Deploy?]",
  );
});

test("summary joins multiple question headers", () => {
  const d = toolRendererFor("ask_user");
  const questions = [
    { header: "A", question: "q1", options: [{ label: "x", detail: "y" }, { label: "z", detail: "w" }] },
    { header: "B", question: "q2", options: [{ label: "x", detail: "y" }, { label: "z", detail: "w" }] },
  ];
  expect(d.summary(item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) }))).toBe("Asked: [A], [B]");
});

test("summary degrades to a bare label for malformed argumentsJSON, never throws", () => {
  const d = toolRendererFor("ask_user");
  expect(() => d.summary(item({ toolName: "ask_user", argumentsJSON: "{not json" }))).not.toThrow();
  expect(d.summary(item({ toolName: "ask_user", argumentsJSON: "{not json" }))).toBe("Asked a question");
});

// --- body: renders read-only question cards --------------------------

test("body renders the question text, options with detail, and the recommended marker", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) })} live={false} />);

  expect(screen.getByText("Should I deploy this to production now?")).toBeTruthy();
  expect(screen.getByText("Yes")).toBeTruthy();
  expect(screen.getByText("Ship it")).toBeTruthy();
  expect(screen.getByText("No")).toBeTruthy();
  expect(screen.getByText(/recommended/i)).toBeTruthy();
});

test("body notes the answer happens in the composer (this wave is read-only)", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) })} live={false} />);
  expect(screen.getByText(/answer in the composer/i)).toBeTruthy();
});

test("body shows a multi_select note when the question allows multiple answers", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ ...ONE_QUESTION[0], multi_select: true }];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(screen.getByText(/select multiple/i)).toBeTruthy();
});

test("body shows why/if_unanswered notes when present", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ ...ONE_QUESTION[0], why: "affects the release", if_unanswered: "assume yes" }];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(screen.getByText("affects the release")).toBeTruthy();
  expect(screen.getByText(/assume yes/)).toBeTruthy();
});

test("body renders multiple question cards in order", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [
    { header: "First", question: "question one", options: [{ label: "a", detail: "b" }, { label: "c", detail: "d" }] },
    { header: "Second", question: "question two", options: [{ label: "e", detail: "f" }, { label: "g", detail: "h" }] },
  ];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(screen.getByText("question one")).toBeTruthy();
  expect(screen.getByText("question two")).toBeTruthy();
});

// --- defensive parsing: malformed input never crashes ---------------------
//
// Two distinct fallback messages, not one: a genuine JSON syntax error in
// THIS item's own argumentsJSON is "malformed" (something arrived and it's
// broken); everything else that still ends up with no usable questions -
// argumentsJSON absent entirely (the cold-open case: an already-completed
// historical item rememberedArgs has no item/started cache entry for), or
// syntactically valid JSON that simply carries no questions - is honest
// absence, not corruption, and must not be described as "malformed".

test("body renders the malformed-data fallback for a genuine JSON syntax error", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  expect(() =>
    render(<Body item={item({ toolName: "ask_user", argumentsJSON: "{not json" })} live={false} />),
  ).not.toThrow();
  expect(screen.getByText(/couldn.t read/i)).toBeTruthy();
});

test("body renders the absent-data fallback (not malformed) when questions is missing from otherwise-valid JSON", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: "{}" })} live={false} />);
  expect(screen.getByText(/question data unavailable/i)).toBeTruthy();
  expect(screen.queryByText(/malformed/i)).toBeNull();
});

test("body renders the absent-data fallback (not malformed) when argumentsJSON is missing entirely - the cold-open case", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user" })} live={false} />);
  expect(screen.getByText(/question data unavailable/i)).toBeTruthy();
  expect(screen.queryByText(/malformed/i)).toBeNull();
});

test("body skips a malformed individual question (missing required fields) rather than crashing the whole card", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ header: "Good", question: "a real question", options: [{ label: "a", detail: "b" }, { label: "c", detail: "d" }] }, { header: "Bad" /* missing question/options */ }];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(screen.getByText("a real question")).toBeTruthy();
  expect(screen.queryByText("Bad")).toBeNull();
});
