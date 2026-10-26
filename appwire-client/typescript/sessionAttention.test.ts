// @vitest-environment node

import { expect, test } from "vitest";
import {
  type AttentionClass,
  type AttentionFacts,
  attentionNeedsYou,
  attentionState,
  normalizeWireState,
} from "./sessionAttention";

test("normalizeWireState maps the raw thread vocabulary onto the navigation one", () => {
  expect(normalizeWireState("closed")).toBe("ended");
  expect(normalizeWireState("systemError")).toBe("errored");
  for (const state of ["active", "awaiting", "warning", "restartRequired", "errored", "ended", "notLoaded", "idle"]) {
    expect(normalizeWireState(state)).toBe(state);
  }
});

test("a raw wire state classifies as its normalized twin", () => {
  expect(attentionState("systemError")).toBe(attentionState("errored"));
  expect(attentionState("closed")).toBe(attentionState("ended"));
});

const approval: AttentionFacts = { approvalPending: true };
const ask: AttentionFacts = { askPending: true };
const children: AttentionFacts = { runningChildren: true };

test.each<[string, AttentionFacts, AttentionClass]>([
  ["errored", {}, "failed"],
  ["errored", { ...ask, ...approval, ...children }, "failed"],
  ["restartRequired", {}, "restartNeeded"],
  // A restart or an end outranks a pending approval and a question.
  ["restartRequired", approval, "restartNeeded"],
  ["restartRequired", ask, "restartNeeded"],
  ["ended", approval, "shutDown"],
  ["notLoaded", approval, "shutDown"],
  ["ended", children, "shutDown"],
  ["warning", {}, "warning"],
  ["warning", approval, "warning"],
  ["warning", ask, "warning"],
  // Only a nonblocking warning yields to live child work.
  ["warning", children, "working"],
  ["warning", { ...approval, ...children }, "warning"],
  ["awaiting", ask, "question"],
  ["awaiting", { ...ask, ...approval }, "question"],
  ["awaiting", approval, "approval"],
  ["active", approval, "approval"],
  ["idle", approval, "approval"],
  // A question needs the session to be awaiting.
  ["active", ask, "working"],
  // Awaiting without a question ended its turn on needs_response.
  ["awaiting", {}, "needsYou"],
  ["awaiting", children, "needsYou"],
  ["active", {}, "working"],
  ["idle", children, "working"],
  ["idle", {}, "idle"],
  ["", {}, "idle"],
  ["someFutureState", {}, "idle"],
  // An unreachable source is never working or needing you.
  ["active", { offline: true }, "shutDown"],
  ["awaiting", { ...ask, ...approval, offline: true }, "shutDown"],
])("%s with %o is %s", (state, facts, expected) => {
  expect(attentionState(state, facts)).toBe(expected);
});

test("the needs-you family is the classes someone has to look at", () => {
  const family: AttentionClass[] = ["question", "needsYou", "approval", "warning", "restartNeeded"];
  const rest: AttentionClass[] = ["failed", "working", "idle", "shutDown"];
  expect(family.filter(attentionNeedsYou)).toEqual(family);
  expect(rest.filter(attentionNeedsYou)).toEqual([]);
});
