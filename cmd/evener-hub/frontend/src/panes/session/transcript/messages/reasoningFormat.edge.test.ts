// Edge cases for reasoningFormat that close the remaining uncovered lines:
// - parseTimestamp's calendar normalization failure (line 83)
// - plainTextFromToken's br/space, list, table, and default branches
// - lastMeaningfulThoughtLine returning "" when every paragraph is blank

import { expect, test } from "vitest";
import {
  formatThoughtDuration,
  lastMeaningfulThoughtLine,
  segmentReasoningTrace,
  thoughtDurationMs,
} from "./reasoningFormat";

// Line 83: parseTimestamp rejects a timestamp whose local wall-clock
// components don't match the input after offset normalization — e.g.
// a Feb 29 date on a non-leap year passes Date.parse but fails the
// component check. 2026 is not a leap year.
test("thoughtDurationMs rejects February 29 on a non-leap year via calendar normalization", () => {
  expect(thoughtDurationMs("2026-02-29T00:00:00.000Z", "2026-03-01T00:00:00.000Z")).toBeUndefined();
});

// Line 83: an invalid day-of-month that passes Date.parse's lenient
// normalization (e.g. April 31 rolls to May 1) but fails the component check.
test("thoughtDurationMs rejects April 31 via calendar normalization", () => {
  expect(thoughtDurationMs("2026-04-31T00:00:00.000Z", "2026-05-01T00:00:00.000Z")).toBeUndefined();
});

// Lines 127, 131-132, 137: plainTextFromToken branches exercised through
// lastMeaningfulThoughtLine. A markdown table, a list, and a hard break
// exercise the table, list, and br/space token paths.
test("lastMeaningfulThoughtLine flattens a markdown table into readable text", () => {
  const table = ["| col1 | col2 |\n| --- | --- |\n| a | b |"];
  expect(lastMeaningfulThoughtLine(table, 80)).toBe("| a | b |");
});

test("lastMeaningfulThoughtLine flattens a markdown list into readable text", () => {
  const list = ["- item one\n- item two\n- item three"];
  expect(lastMeaningfulThoughtLine(list, 80)).toBe("item three");
});

test("lastMeaningfulThoughtLine handles a hard line break token", () => {
  // A line with a trailing double-space creates a <br> token in markdown
  const br = ["first line  \nsecond line"];
  expect(lastMeaningfulThoughtLine(br, 80)).toBe("second line");
});

test("lastMeaningfulThoughtLine handles a checkbox token", () => {
  const checkbox = ["- [ ] task one\n- [x] done"];
  expect(lastMeaningfulThoughtLine(checkbox, 80)).toBe("done");
});

// Line 215: lastMeaningfulThoughtLine returns "" when all paragraphs
// contain only blank/whitespace lines
test("lastMeaningfulThoughtLine returns empty string when all paragraphs are blank", () => {
  expect(lastMeaningfulThoughtLine(["", "  ", "\n\n"], 80)).toBe("");
  expect(lastMeaningfulThoughtLine([], 80)).toBe("");
});

// Line 137: default branch of plainTextFromToken — a token with no
// recognized type, no tokens, and no text property yields "".
// This is exercised through a frontmatter or unknown token.
test("lastMeaningfulThoughtLine handles unknown token types gracefully", () => {
  // A frontmatter token (---) is not a standard markdown token that
  // plainTextFromToken recognizes; it falls through to the default case
  const frontmatter = ["---\nkey: value\n---\nactual content"];
  expect(lastMeaningfulThoughtLine(frontmatter, 80)).toBe("actual content");
});

// Additional edge: formatThoughtDuration at exact boundaries
test("formatThoughtDuration at exact 1ms rounds up from sub-1ms", () => {
  expect(formatThoughtDuration(0.4)).toBe("1ms");
  expect(formatThoughtDuration(0.5)).toBe("1ms");
});

test("formatThoughtDuration at 9999ms stays in sub-10s tier", () => {
  expect(formatThoughtDuration(9_999)).toBe("10.0s");
});

test("formatThoughtDuration at exactly 1000ms", () => {
  expect(formatThoughtDuration(1_000)).toBe("1s");
});

// segmentReasoningTrace with only whitespace-only paragraphs
test("segmentReasoningTrace with whitespace-only source returns empty", () => {
  expect(segmentReasoningTrace(["   ", "\n\n", "  "])).toEqual([]);
});
