// @vitest-environment node

import { expect, test } from "vitest";
import { humanizeState } from "./railSessionState";

// --- humanizeState: one lowercase word per wire state ---------------------
// The rail's second line (web RailRow, native screens session list) leads
// with this gloss. Each case below is a state a caller passes today.

test("active reads as working", () => {
  expect(humanizeState("active", false)).toBe("working");
});

test("awaiting splits on askPending: a blocked question vs a turn that simply ended", () => {
  expect(humanizeState("awaiting", true)).toBe("question waiting");
  expect(humanizeState("awaiting", false)).toBe("your move");
});

test("restartRequired, warning, errored and ended each get their own word", () => {
  expect(humanizeState("restartRequired", false)).toBe("restart required");
  expect(humanizeState("warning", false)).toBe("warning");
  expect(humanizeState("errored", false)).toBe("failed");
  expect(humanizeState("ended", false)).toBe("ended");
});

test("askPending only matters for awaiting; every other state ignores it", () => {
  expect(humanizeState("active", true)).toBe("working");
  expect(humanizeState("restartRequired", true)).toBe("restart required");
  expect(humanizeState("warning", true)).toBe("warning");
  expect(humanizeState("errored", true)).toBe("failed");
  expect(humanizeState("ended", true)).toBe("ended");
});

test("idle, notLoaded, the empty state and any unknown value all read as idle", () => {
  expect(humanizeState("idle", false)).toBe("idle");
  expect(humanizeState("notLoaded", false)).toBe("idle");
  expect(humanizeState("", false)).toBe("idle");
  expect(humanizeState("someFutureState", true)).toBe("idle");
});
