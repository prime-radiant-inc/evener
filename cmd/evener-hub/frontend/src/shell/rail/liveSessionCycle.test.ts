// Unit tests for liveSessionCycle.ts: the session.liveNext /
// session.livePrevious keybinding actions step through the rail's live
// section in server order, wrapping at both ends. The shape mirrors
// needsYouCycle.test.ts's nextNeedsYouRef cases, bidirectional.

import { expect, test } from "vitest";
import { adjacentLiveSessionRef } from "./liveSessionCycle";

test("next advances in list order and wraps from the last to the first", () => {
  expect(adjacentLiveSessionRef(["a", "b", "c"], "a", "next")).toBe("b");
  expect(adjacentLiveSessionRef(["a", "b", "c"], "b", "next")).toBe("c");
  expect(adjacentLiveSessionRef(["a", "b", "c"], "c", "next")).toBe("a");
});

test("previous steps backward and wraps from the first to the last", () => {
  expect(adjacentLiveSessionRef(["a", "b", "c"], "c", "previous")).toBe("b");
  expect(adjacentLiveSessionRef(["a", "b", "c"], "b", "previous")).toBe("a");
  expect(adjacentLiveSessionRef(["a", "b", "c"], "a", "previous")).toBe("c");
});

test("next lands on the first and previous on the last without a matching current", () => {
  expect(adjacentLiveSessionRef(["a", "b", "c"], null, "next")).toBe("a");
  expect(adjacentLiveSessionRef(["a", "b", "c"], "missing", "next")).toBe("a");
  expect(adjacentLiveSessionRef(["a", "b", "c"], null, "previous")).toBe("c");
  expect(adjacentLiveSessionRef(["a", "b", "c"], "missing", "previous")).toBe("c");
});

test("returns null for an empty live list", () => {
  expect(adjacentLiveSessionRef([], "a", "next")).toBeNull();
  expect(adjacentLiveSessionRef([], null, "previous")).toBeNull();
});

test("a single live session cycles onto itself", () => {
  expect(adjacentLiveSessionRef(["a"], "a", "next")).toBe("a");
  expect(adjacentLiveSessionRef(["a"], "a", "previous")).toBe("a");
});
