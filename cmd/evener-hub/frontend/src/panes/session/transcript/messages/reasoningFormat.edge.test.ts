// @vitest-environment node

import { expect, test } from "vitest";
import { lastMeaningfulThoughtLine, segmentReasoningTrace } from "./reasoningFormat";

test("lastMeaningfulThoughtLine returns empty when no paragraph has visible text", () => {
  expect(lastMeaningfulThoughtLine(["", "  ", "\n\n"], 80)).toBe("");
  expect(lastMeaningfulThoughtLine([], 80)).toBe("");
});

test("segmentReasoningTrace drops a whitespace-only source", () => {
  expect(segmentReasoningTrace(["   ", "\n\n", "  "])).toEqual([]);
});
