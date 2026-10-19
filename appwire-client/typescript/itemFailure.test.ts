// @vitest-environment node

import { describe, expect, test } from "vitest";
import {
  hasErrorText,
  hasFailureStatus,
  hasItemFailure,
  isExitOnly,
  isInProgressStatus,
  isNonZeroExit,
  isTurnError,
} from "./itemFailure";
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

  // Jesse, 2026-10-16: a non-zero exit is common and expected (a grep that
  // finds nothing, a check inside a passing run), so it must not, by itself,
  // raise a tool call to failed. The exit stays visible as an inline mark on
  // the call (isNonZeroExit, still exported below); it is not the failure.
  test("a nonzero exit alone (even with an empty error) is not a failure", () => {
    expect(hasItemFailure(wireItem({ error: "", exitCode: 7 }))).toBe(false);
    expect(hasItemFailure(wireItem({ exitCode: 1 }))).toBe(false);
  });

  test("a nonzero exit alongside real failure text is still a failure", () => {
    expect(hasItemFailure(wireItem({ error: "denied", exitCode: 7 }))).toBe(true);
    expect(hasItemFailure(wireItem({ status: "failed", exitCode: 7 }))).toBe(true);
  });

  test("a zero exit code is a clean exit, and an absent one says nothing", () => {
    expect(hasItemFailure(wireItem({ exitCode: 0 }))).toBe(false);
    expect(hasItemFailure(wireItem({ exitCode: undefined }))).toBe(false);
  });

  test("an item still in progress has not failed", () => {
    expect(hasItemFailure(wireItem({ status: "inProgress" }))).toBe(false);
  });

  test("a projected ItemModel answers the same question as the wire item", () => {
    const clean: ItemModel = { id: "item-1", turnId: "turn-1", type: "commandExecution", text: "" };
    expect(hasItemFailure(clean)).toBe(false);
    // An exit code alone, projected, says nothing about failure.
    const exited: ItemModel = { id: "item-1", turnId: "turn-1", type: "commandExecution", text: "", exitCode: 2 };
    expect(hasItemFailure(exited)).toBe(false);
    // Real failure text still does.
    const errored: ItemModel = { id: "item-1", turnId: "turn-1", type: "commandExecution", text: "", error: "boom" };
    expect(hasItemFailure(errored)).toBe(true);
  });
});

describe("isTurnError", () => {
  test("narrows by the one required field: an object with a string message", () => {
    expect(isTurnError({ message: "the model call failed" })).toBe(true);
    expect(isTurnError({ message: "boom", title: "Provider", hint: "retry" })).toBe(true);
  });

  test("rejects absent, null, scalar, and message-less shapes", () => {
    expect(isTurnError(undefined)).toBe(false);
    expect(isTurnError(null)).toBe(false);
    expect(isTurnError("boom")).toBe(false);
    expect(isTurnError(0)).toBe(false);
    expect(isTurnError({})).toBe(false);
    expect(isTurnError({ message: 7 })).toBe(false);
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

// The inline mark's rule: a call is "exit only" exactly when it settled
// otherwise clean but its process exited nonzero. It is NOT a failure (see
// hasItemFailure); the mark is how the exit stays visible without the alarm.
describe("isExitOnly", () => {
  test("a nonzero exit with no error text and a clean status is exit only", () => {
    expect(isExitOnly(wireItem({ exitCode: 7 }))).toBe(true);
  });

  test("a clean exit, or no exit at all, is not exit only", () => {
    expect(isExitOnly(wireItem({ exitCode: 0 }))).toBe(false);
    expect(isExitOnly(wireItem())).toBe(false);
  });

  test("a real failure is a failure, never exit only", () => {
    expect(isExitOnly(wireItem({ error: "denied", exitCode: 7 }))).toBe(false);
    expect(isExitOnly(wireItem({ status: "failed", exitCode: 7 }))).toBe(false);
    expect(isExitOnly(wireItem({ status: "interrupted", exitCode: 7 }))).toBe(false);
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
