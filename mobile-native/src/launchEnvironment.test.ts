import { expect, it } from "vitest";
import {
  addEnvironmentVariable,
  assertEnvironmentCurrent,
  collectEnvironment,
} from "./launchEnvironment";

it("preserves embedded equals and empty values while replacing a named variable", () => {
  const original = { KEEP: "yes", TOKEN: "old" };
  expect(addEnvironmentVariable(original, " TOKEN ", "a=b==")).toEqual({
    KEEP: "yes",
    TOKEN: "a=b==",
  });
  expect(addEnvironmentVariable({}, "EMPTY", "")).toEqual({ EMPTY: "" });
  expect(original.TOKEN).toBe("old");
});
it("rejects missing names and ambiguous name delimiters", () => {
  for (const name of ["", "  ", "A=B"])
    expect(() => addEnvironmentVariable({}, name, "value")).toThrow();
});
it("restores inheritance when the final variable is removed", () => {
  expect(collectEnvironment({})).toBeUndefined();
  expect(collectEnvironment({ EMPTY: "" })).toEqual({ EMPTY: "" });
});
it("blocks stale environment edits but accepts reordered or convergent maps", () => {
  expect(() =>
    assertEnvironmentCurrent({ A: "1" }, { A: "2" }, { A: "3" }),
  ).toThrow();
  expect(() =>
    assertEnvironmentCurrent({ A: "1", B: "2" }, { B: "2", A: "1" }, undefined),
  ).not.toThrow();
  expect(() =>
    assertEnvironmentCurrent(undefined, { A: "2" }, { A: "2" }),
  ).not.toThrow();
  expect(() =>
    assertEnvironmentCurrent({ A: "1" }, undefined, undefined),
  ).not.toThrow();
});
