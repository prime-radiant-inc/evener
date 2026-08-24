// @vitest-environment node

import { expect, test } from "vitest";
import { formatThoughtDuration, lastMeaningfulThoughtLine, segmentReasoningTrace } from "./reasoningFormat";

test("lastMeaningfulThoughtLine returns empty when no paragraph has visible text", () => {
  expect(lastMeaningfulThoughtLine(["", "  ", "\n\n"], 80)).toBe("");
  expect(lastMeaningfulThoughtLine([], 80)).toBe("");
});

test("formatThoughtDuration clamps a positive sub-millisecond duration to one millisecond", () => {
  expect(formatThoughtDuration(0.4)).toBe("1ms");
  expect(formatThoughtDuration(0.5)).toBe("1ms");
});

test("formatThoughtDuration changes tiers at exactly one second", () => {
  expect(formatThoughtDuration(999)).toBe("999ms");
  expect(formatThoughtDuration(1_000)).toBe("1s");
});

test("segmentReasoningTrace drops a whitespace-only source", () => {
  expect(segmentReasoningTrace(["   ", "\n\n", "  "])).toEqual([]);
});
