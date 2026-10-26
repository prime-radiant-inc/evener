// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { humanizeState } from "./railSessionState";
import { approvalWaiting } from "./sessionAttention";

// --- humanizeState: one lowercase word per wire state ---------------------
// The rail's second line (web RailRow, native screens session list) leads
// with this gloss. Each case below is a state a caller passes today.

test("active reads as working", () => {
  expect(humanizeState("active", false)).toBe("working");
});

test("awaiting splits on askPending: a pending question vs a turn that ended on needs_response", () => {
  expect(humanizeState("awaiting", true)).toBe("question waiting");
  expect(humanizeState("awaiting", false)).toBe("needs you");
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

// --- approvals --------------------------------------------------------------
// A sandbox escalation blocks its turn mid-tool, so the session keeps reporting
// "active" while it waits on a person's allow or deny. The approval flag, not
// the state, is what says so; only a failure outranks it.

test("a pending approval reads as approval waiting, never working", () => {
  expect(humanizeState("active", false, true)).toBe("approval waiting");
  expect(humanizeState("idle", false, true)).toBe("approval waiting");
  expect(humanizeState("awaiting", true, true)).toBe("approval waiting");
});

test("a failure outranks a pending approval", () => {
  expect(humanizeState("errored", false, true)).toBe("failed");
});

// The hub's hubapi.AttentionState test reads this same fixture: a state the hub
// leaves unescalated is one approvalWaiting refuses.
const approvalCases = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../testdata/attention/approval_state.json", import.meta.url).href), "utf8"),
) as { state: string; approval_pending: boolean; want: string }[];

test("approvalWaiting agrees with the hub's AttentionState fixture", () => {
  for (const c of approvalCases) {
    expect({ state: c.state, waiting: approvalWaiting(c.state, c.approval_pending) }).toEqual({
      state: c.state,
      waiting: c.approval_pending && c.want === "awaiting",
    });
  }
});

test("a restart or an end outranks a pending approval", () => {
  expect(humanizeState("restartRequired", false, true)).toBe("restart required");
  expect(humanizeState("ended", false, true)).toBe("ended");
});
