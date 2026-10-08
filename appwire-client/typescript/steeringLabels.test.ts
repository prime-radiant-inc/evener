// @vitest-environment node

import { expect, test } from "vitest";
import { isSuppressedSteeringKind, STEERING_KIND_LABELS, steeringLabel } from "./steeringLabels";
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

test("says the task reminders the way the phone always has, in both clients", () => {
  expect(STEERING_KIND_LABELS["tasks-done"]).toBe("Tasks complete");
  expect(STEERING_KIND_LABELS["task-inactive"]).toBe("Task list idle");
});

test("says a detector's and a provider's steers as events", () => {
  expect(STEERING_KIND_LABELS["loop-detected"]).toBe("Loop detected");
  expect(STEERING_KIND_LABELS["provider-failure"]).toBe("Provider failed");
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
  expect(steeringLabel("some-future-kind")).toBe("System steered");
  expect(steeringLabel(undefined)).toBe("System steered");
  expect(steeringLabel("toString")).toBe("System steered");
});

test("says a steer as the system's, naming its kind, or bare when the kind has no label", () => {
  expect(steeringLabel("compact-nudge")).toBe("System steered: Running low on context");
  expect(steeringLabel("human-note")).toBe("System steered: Human note");
  // A colon promises a value, so a kind with no label gets none.
  expect(steeringLabel("some-future-kind")).toBe("System steered");
  expect(steeringLabel(undefined)).toBe("System steered");
});
