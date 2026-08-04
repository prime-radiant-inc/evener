// @vitest-environment node
import { expect, test } from "vitest";
import { type AskAnswerItem, composeAskAnswers, quoteGoString } from "./askCompose";

// Byte-exact / golden-string coverage for the [answers] reply format
// (contracts-composer-queue-pending.md's test-ask-compose.js rows), ported
// verbatim from cmd/serf-hub/assets/renderer.js's composeAskAnswers/
// askResolutionText/quoteGoString (renderer.js:6980-7031) - this is the
// text the daemon parses back on the other end, so it must match exactly,
// not just "look right".

function item(overrides: Partial<AskAnswerItem> = {}): AskAnswerItem {
  return { header: "Deploy?", resolution: null, note: "", ...overrides };
}

// --- quoteGoString: Go's %q escaping ------------------------------------

test("quoteGoString wraps plain text in quotes", () => {
  expect(quoteGoString("hello")).toBe('"hello"');
});

test("quoteGoString escapes backslash and double-quote", () => {
  expect(quoteGoString('a\\b"c')).toBe('"a\\\\b\\"c"');
});

test("quoteGoString escapes newline, tab, and carriage return", () => {
  expect(quoteGoString("a\nb\tc\rd")).toBe('"a\\nb\\tc\\rd"');
});

test("quoteGoString escapes other C0 control characters as \\xHH", () => {
  expect(quoteGoString("a\x01b\x1fc")).toBe('"a\\x01b\\x1fc"');
});

test("quoteGoString treats undefined as an empty string", () => {
  expect(quoteGoString(undefined)).toBe('""');
});

// --- composeAskAnswers: per-kind resolution text ------------------------

test("an option resolution joins labels with a comma, each quoted", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "option", labels: ["Yes"] } })]);
  expect(text).toBe('[answers]\n1. [Deploy?] → "Yes"');
});

test("a multi-select option resolution quotes each label separately, comma-joined", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "option", labels: ["Yes", "with, a comma"] } })]);
  expect(text).toBe('[answers]\n1. [Deploy?] → "Yes", "with, a comma"');
});

test('a free-text resolution renders as free text: "..."', () => {
  const text = composeAskAnswers([item({ resolution: { kind: "free", text: "ship it Friday" } })]);
  expect(text).toBe('[answers]\n1. [Deploy?] → free text: "ship it Friday"');
});

test("a decide resolution with no leaning renders as you decide", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "decide", leaning: "" } })]);
  expect(text).toBe("[answers]\n1. [Deploy?] → you decide");
});

test("a decide resolution with a leaning appends it", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "decide", leaning: "probably yes" } })]);
  expect(text).toBe('[answers]\n1. [Deploy?] → you decide — leaning: "probably yes"');
});

test("a decide resolution with a whitespace-only leaning is treated as no leaning", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "decide", leaning: "   " } })]);
  expect(text).toBe("[answers]\n1. [Deploy?] → you decide");
});

test("a fallback resolution embeds the model's own if_unanswered text verbatim", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "fallback" }, ifUnanswered: "assume yes" })]);
  expect(text).toBe('[answers]\n1. [Deploy?] → do your stated fallback ("assume yes")');
});

test("an explicit skip resolution renders as skipped (no answer)", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "skip" } })]);
  expect(text).toBe("[answers]\n1. [Deploy?] → skipped (no answer)");
});

test("an untouched/unresolved (null) question composes identically to an explicit skip", () => {
  const untouched = composeAskAnswers([item({ resolution: null })]);
  const skipped = composeAskAnswers([item({ resolution: { kind: "skip" } })]);
  expect(untouched).toBe(skipped);
  expect(untouched).toBe("[answers]\n1. [Deploy?] → skipped (no answer)");
});

// --- composeAskAnswers: structure ----------------------------------------

test("numbers questions globally in posting order across the whole set, one line each", () => {
  const text = composeAskAnswers([
    item({ header: "First", resolution: { kind: "option", labels: ["A"] } }),
    item({ header: "Second", resolution: { kind: "skip" } }),
  ]);
  expect(text).toBe('[answers]\n1. [First] → "A"\n2. [Second] → skipped (no answer)');
});

test("a note attaches to whichever resolution was chosen, suffixed after a dash", () => {
  const text = composeAskAnswers([
    item({ resolution: { kind: "option", labels: ["Yes"] }, note: "please double check first" }),
  ]);
  expect(text).toBe('[answers]\n1. [Deploy?] → "Yes" — note: "please double check first"');
});

test("a whitespace-only note is treated as no note at all", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "skip" }, note: "   " })]);
  expect(text).toBe("[answers]\n1. [Deploy?] → skipped (no answer)");
});

test("a note on an explicit skip still attaches (the note is universal, not chip-only)", () => {
  const text = composeAskAnswers([item({ resolution: { kind: "skip" }, note: "will revisit" })]);
  expect(text).toBe('[answers]\n1. [Deploy?] → skipped (no answer) — note: "will revisit"');
});

test("an empty items array composes just the [answers] header line", () => {
  expect(composeAskAnswers([])).toBe("[answers]");
});

test("composes the stable fallback label for an omitted header", () => {
  expect(composeAskAnswers([item({ header: undefined, resolution: { kind: "skip" } })])).toBe(
    "[answers]\n1. [Question 1] → skipped (no answer)",
  );
});
