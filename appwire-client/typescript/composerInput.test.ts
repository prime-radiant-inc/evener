// @vitest-environment node

import { expect, test } from "vitest";
import { buildInput } from "./composerInput";

// Skill names reach buildInput from drafts, recovery records and queue
// projections. Any of those can carry a stray space, an empty string or a
// repeat, and the wire must never receive an empty or duplicated skill item:
// the backend rejects an empty canonical name, and a repeat renders duplicate
// markers and sends the same selection twice.
test("buildInput trims, drops empty names and dedupes skill selections preserving order", () => {
  const input = buildInput("hello", undefined, [" pkg:probe ", "", "pkg:probe", "pkg:other", "pkg:other"]);

  expect(input.filter((item) => item.type === "skill")).toEqual([
    { type: "skill", name: "pkg:probe" },
    { type: "skill", name: "pkg:other" },
  ]);
});

test("buildInput emits no skill items when every name is empty", () => {
  const input = buildInput("hello", undefined, ["", "   "]);

  expect(input.filter((item) => item.type === "skill")).toEqual([]);
});
