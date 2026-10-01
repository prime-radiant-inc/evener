import { describe, expect, test } from "vitest";
import { isConfirmedCrashedSession } from "./sessionKind";

describe("isConfirmedCrashedSession", () => {
  test("recognizes an errored session with the crashed navigation cause", () => {
    expect(isConfirmedCrashedSession({ state: "errored", failure: { cause_kind: "crashed" } })).toBe(true);
  });

  test("does not treat ordinary errors or other states as confirmed crashes", () => {
    expect(isConfirmedCrashedSession({ state: "errored", failure: { cause_kind: "provider" } })).toBe(false);
    expect(isConfirmedCrashedSession({ state: "idle", failure: { cause_kind: "crashed" } })).toBe(false);
    expect(isConfirmedCrashedSession({ state: "errored" })).toBe(false);
  });
});
