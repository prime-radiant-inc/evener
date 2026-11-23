import type { ComposerMention, ImageMarkerRange } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { sameImageRanges, sameMarkers, sameMentions, sameStrings } from "./selection";

test("sameStrings compares values in order", () => {
  expect(sameStrings(["a", "b"], ["a", "b"])).toBe(true);
  expect(sameStrings(["a", "b"], ["b", "a"])).toBe(false);
  expect(sameStrings(["a"], ["a", "b"])).toBe(false);
});

test("sameImageRanges ignores order and repeats but not position or marker", () => {
  const staged: ImageMarkerRange = { marker: 1, start: 0, end: 8 };
  const adopted: ImageMarkerRange = { marker: 2, start: 12, end: 20 };
  expect(sameImageRanges([staged, adopted], [adopted, staged, staged])).toBe(true);
  expect(sameImageRanges([staged], [{ ...staged, start: 1 }])).toBe(false);
  expect(sameImageRanges([staged], [{ ...staged, marker: 3 }])).toBe(false);
});

test("sameMarkers ignores order and repeats", () => {
  expect(sameMarkers([1, 2], [2, 1, 2])).toBe(true);
  expect(sameMarkers([1], [2])).toBe(false);
});

test("sameMentions compares atom identity in order, not key insertion order", () => {
  const left: ComposerMention[] = [{ kind: "skill", name: "review", offset: 4 }];
  const right: ComposerMention[] = [{ offset: 4, name: "review", kind: "skill" }];
  expect(sameMentions(left, right)).toBe(true);
  expect(sameMentions(left, [{ offset: 5, name: "review", kind: "skill" }])).toBe(false);
  expect(
    sameMentions(
      [
        { kind: "skill", name: "a", offset: 0 },
        { kind: "command", name: "b", offset: 5 },
      ],
      [
        { kind: "command", name: "b", offset: 5 },
        { kind: "skill", name: "a", offset: 0 },
      ],
    ),
  ).toBe(false);
});
