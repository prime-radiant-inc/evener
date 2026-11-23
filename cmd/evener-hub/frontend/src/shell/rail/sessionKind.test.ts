import { describe, expect, test } from "vitest";
import { isConfirmedCrashedSession, isNestedSessionKind, isTopLevelSession } from "./sessionKind";

describe("nested session kinds", () => {
  test("names exactly the two kinds that are never top-level", () => {
    expect(isNestedSessionKind("subagent")).toBe(true);
    expect(isNestedSessionKind("fork")).toBe(true);
    expect(isNestedSessionKind("session")).toBe(false);
    expect(isTopLevelSession({ kind: "session" })).toBe(true);
  });
});

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
