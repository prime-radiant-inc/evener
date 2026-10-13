// @vitest-environment node

import { expect, test } from "vitest";
import { isSuppressedSteeringKind, labelledSteeringKind, STEERING_KIND_LABELS, steeringLabel } from "./steeringLabels";
import { STEERING_KINDS } from "./types.gen";

test("labels every daemon steer the transcript shows, and no other", () => {
  for (const kind of STEERING_KINDS) {
    const shown = !isSuppressedSteeringKind(kind) && kind !== "notification";
    expect(steeringLabel(kind) !== "System steered", kind).toBe(shown);
  }
});

test("leaves out the current task and the task list, which the tasks surfaces own", () => {
  expect(isSuppressedSteeringKind("current-task")).toBe(true);
  expect(isSuppressedSteeringKind("task-list")).toBe(true);
  expect(isSuppressedSteeringKind("tasks-done")).toBe(false);
  expect(isSuppressedSteeringKind(undefined)).toBe(false);
});

test("puts the engineering labels in plain words (spec 5)", () => {
  expect(STEERING_KIND_LABELS).toMatchObject({
    "precompact-hook": "Hook context before compacting",
    "compact-nudge": "Running low on context",
    "no-tool-calls": "Reminded to keep working",
    "transcript-pointer": "Where to find the full transcript",
  });
});

test("names no kind it doesn't know, rather than inventing a label from a slug", () => {
  const bare = steeringLabel(undefined);
  for (const kind of ["some-future-kind", "toString"]) {
    expect(labelledSteeringKind(kind), kind).toBeUndefined();
    expect(steeringLabel(kind), kind).toBe(bare);
  }
});

test("says a steer as the system's, naming its kind, or bare when the kind has no label", () => {
  expect(steeringLabel("compact-nudge")).toBe("System steered: Running low on context");
  expect(steeringLabel("human-note")).toBe("System steered: Human note");
  // A colon promises a value, so a kind with no label gets none.
  expect(steeringLabel("some-future-kind")).toBe("System steered");
  expect(steeringLabel(undefined)).toBe("System steered");
});

test("a steer from the session's parent agent says so, whatever its kind", () => {
  const fromParent = steeringLabel(undefined, "parent");
  expect(steeringLabel("agent-message", "parent")).toBe(fromParent);
  expect(steeringLabel("compact-nudge", "parent")).toBe(fromParent);
  expect(fromParent).not.toBe(steeringLabel(undefined));
  expect(fromParent).not.toBe(steeringLabel("agent-message"));
});
