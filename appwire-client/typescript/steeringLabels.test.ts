// @vitest-environment node

import { expect, test } from "vitest";
import { isSuppressedSteeringKind, STEERING_KIND_LABELS, steeringKindLabel } from "./steeringLabels";
import { STEERING_KINDS } from "./types.gen";

test("labels every daemon steer the transcript shows, and no other", () => {
  for (const kind of STEERING_KINDS) {
    const shown = !isSuppressedSteeringKind(kind) && kind !== "notification";
    expect(steeringKindLabel(kind) !== undefined, kind).toBe(shown);
  }
});

test("leaves out the current task and the task list, which the tasks surfaces own", () => {
  expect(isSuppressedSteeringKind("current-task")).toBe(true);
  expect(isSuppressedSteeringKind("task-list")).toBe(true);
  expect(isSuppressedSteeringKind("tasks-done")).toBe(false);
  expect(isSuppressedSteeringKind(undefined)).toBe(false);
});

test("says the task reminders the way the phone always has, in both clients", () => {
  expect(steeringKindLabel("tasks-done")).toBe("Tasks complete");
  expect(steeringKindLabel("task-nudge")).toBe("Task reminder");
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
  expect(steeringKindLabel("some-future-kind")).toBeUndefined();
  expect(steeringKindLabel(undefined)).toBeUndefined();
  expect(steeringKindLabel("toString")).toBeUndefined();
});
