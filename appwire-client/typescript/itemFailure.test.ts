import { describe, expect, test } from "vitest";
import { hasErrorText, hasFailureStatus, hasItemFailure, isInProgressStatus, isNonZeroExit } from "./itemFailure";
import type { ItemModel } from "./model";
import type { ThreadItem } from "./types.gen";

// Wire-shaped fixtures: a ThreadItem exactly as the daemon frames a settled
// shell call, so the union of the web projector's and the native projection's
// cases is exercised against the shape both clients actually receive.
function wireItem(overrides: Partial<ThreadItem> = {}): ThreadItem {
  return { id: "item-1", type: "commandExecution", toolName: "shell", status: "completed", ...overrides };
}

describe("hasItemFailure", () => {
  test("a clean settled call is not a failure", () => {
    expect(hasItemFailure(wireItem())).toBe(false);
  });

  test("a non-blank error is a failure even when the wire calls the item completed", () => {
    expect(hasItemFailure(wireItem({ error: "denied" }))).toBe(true);
  });

  test("an empty or whitespace-only error is not a failure on its own", () => {
    expect(hasItemFailure(wireItem({ error: "" }))).toBe(false);
    expect(hasItemFailure(wireItem({ error: "  \n\t " }))).toBe(false);
  });

  test.each(["failed", "interrupted"] as const)("a %s status is a failure with no error and no exit code", (status) => {
    expect(hasItemFailure(wireItem({ status }))).toBe(true);
  });

  test("a nonzero exit code is a failure even with an empty error", () => {
    expect(hasItemFailure(wireItem({ error: "", exitCode: 7 }))).toBe(true);
  });

  test("a zero exit code is a clean exit, and an absent one says nothing", () => {
    expect(hasItemFailure(wireItem({ exitCode: 0 }))).toBe(false);
    expect(hasItemFailure(wireItem({ exitCode: undefined }))).toBe(false);
  });

  test("an item still in progress has not failed", () => {
    expect(hasItemFailure(wireItem({ status: "inProgress" }))).toBe(false);
  });

  test("a projected ItemModel answers the same question as the wire item", () => {
    const model: ItemModel = { id: "item-1", turnId: "turn-1", type: "commandExecution", text: "", exitCode: 2 };
    expect(hasItemFailure(model)).toBe(true);
  });
});

describe("hasErrorText", () => {
  test("a non-blank error is text worth showing", () => {
    expect(hasErrorText(wireItem({ error: "denied" }))).toBe(true);
  });

  test("absent, empty and whitespace-only errors carry nothing to show", () => {
    expect(hasErrorText(wireItem())).toBe(false);
    expect(hasErrorText(wireItem({ error: "" }))).toBe(false);
    expect(hasErrorText(wireItem({ error: "  \n\t " }))).toBe(false);
  });

  test("a failed item with no error text still has no error text", () => {
    expect(hasErrorText(wireItem({ status: "failed", exitCode: 3 }))).toBe(false);
  });
});

describe("hasFailureStatus", () => {
  test.each(["failed", "interrupted"] as const)("%s settles as a failure status", (status) => {
    expect(hasFailureStatus(wireItem({ status }))).toBe(true);
  });

  test("every other status, including none at all, is not a failure status", () => {
    expect(hasFailureStatus(wireItem({ status: "completed" }))).toBe(false);
    expect(hasFailureStatus(wireItem({ status: "inProgress" }))).toBe(false);
    expect(hasFailureStatus(wireItem({ status: undefined }))).toBe(false);
  });

  test("an error or a nonzero exit alone is not a failure STATUS", () => {
    expect(hasFailureStatus(wireItem({ error: "denied", exitCode: 3 }))).toBe(false);
  });
});

describe("isNonZeroExit", () => {
  test("a nonzero code exits nonzero", () => {
    expect(isNonZeroExit(wireItem({ exitCode: 1 }))).toBe(true);
    expect(isNonZeroExit(wireItem({ exitCode: -1 }))).toBe(true);
  });

  test("zero and undefined are not nonzero exits", () => {
    expect(isNonZeroExit(wireItem({ exitCode: 0 }))).toBe(false);
    expect(isNonZeroExit(wireItem())).toBe(false);
  });
});

describe("isInProgressStatus", () => {
  test("only the wire's inProgress means still running", () => {
    expect(isInProgressStatus("inProgress")).toBe(true);
    expect(isInProgressStatus("completed")).toBe(false);
    expect(isInProgressStatus("failed")).toBe(false);
    expect(isInProgressStatus(undefined)).toBe(false);
  });

  test("the wire never says running, so neither does this", () => {
    expect(isInProgressStatus("running")).toBe(false);
  });
});
