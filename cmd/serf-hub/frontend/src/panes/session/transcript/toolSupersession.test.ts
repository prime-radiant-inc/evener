import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import { supersededBySuccess } from "./toolSupersession";

function item(overrides: Partial<ItemModel> & { id: string }): ItemModel {
  return { turnId: "t1", type: "commandExecution", text: "", ...overrides };
}

function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, status: "completed", items };
}

function thread(turns: TurnModel[]): ThreadModel {
  return { turns } as unknown as ThreadModel;
}

// The exact hgm1 scenario: a preval-only ask_user bounce immediately
// followed, in the next turn, by a successful ask_user call.
test("a preval-only failure is superseded when the next same-tool call succeeds", () => {
  const failed = item({ id: "i1", toolName: "ask_user", error: "missing required field", prevalOnly: true });
  const ok = item({ id: "i2", toolName: "ask_user", output: "answered" });
  const t = thread([turn("t1", [failed]), turn("t2", [ok])]);

  expect(supersededBySuccess(failed, t)).toBe(true);
});

test("a real execution failure is never superseded, even if the next same-tool call succeeds", () => {
  const failed = item({ id: "i1", toolName: "shell", error: "command not found" });
  const ok = item({ id: "i2", toolName: "shell", output: "ok" });
  const t = thread([turn("t1", [failed]), turn("t2", [ok])]);

  expect(supersededBySuccess(failed, t)).toBe(false);
});

test("a preval-only failure stays open when the next same-tool call ALSO failed", () => {
  const failed1 = item({ id: "i1", toolName: "ask_user", error: "missing required field", prevalOnly: true });
  const failed2 = item({ id: "i2", toolName: "ask_user", error: "still missing required field", prevalOnly: true });
  const ok = item({ id: "i3", toolName: "ask_user", output: "answered" });
  const t = thread([turn("t1", [failed1, failed2]), turn("t2", [ok])]);

  // failed1's NEXT same-tool item is failed2, which is itself still failed -
  // failed1 does not get credit for a success two attempts later.
  expect(supersededBySuccess(failed1, t)).toBe(false);
  // failed2 is the one immediately preceding the success, so it demotes.
  expect(supersededBySuccess(failed2, t)).toBe(true);
});

test("a preval-only failure with no later attempt at all stays open", () => {
  const failed = item({ id: "i1", toolName: "ask_user", error: "missing required field", prevalOnly: true });
  const t = thread([turn("t1", [failed])]);

  expect(supersededBySuccess(failed, t)).toBe(false);
});

test("a later call to a DIFFERENT tool does not count as superseding", () => {
  const failed = item({ id: "i1", toolName: "ask_user", error: "missing required field", prevalOnly: true });
  const other = item({ id: "i2", toolName: "shell", output: "ok" });
  const t = thread([turn("t1", [failed]), turn("t2", [other])]);

  expect(supersededBySuccess(failed, t)).toBe(false);
});

test("returns false with no thread loaded yet", () => {
  const failed = item({ id: "i1", toolName: "ask_user", error: "missing required field", prevalOnly: true });
  expect(supersededBySuccess(failed, undefined)).toBe(false);
});

test("an item with no toolName never supersedes (defensive)", () => {
  const failed = item({ id: "i1", error: "x", prevalOnly: true });
  const ok = item({ id: "i2", toolName: "ask_user", output: "ok" });
  const t = thread([turn("t1", [failed, ok])]);
  expect(supersededBySuccess(failed, t)).toBe(false);
});
