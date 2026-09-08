// @vitest-environment node
import { afterEach, expect, test, vi } from "vitest";
import { revealSessionInRail, setRailRevealHandler } from "./railController";

afterEach(() => setRailRevealHandler(null));

test("revealSessionInRail queues across the lazy chunk; the next handler gets it", () => {
  const handler = vi.fn();
  revealSessionInRail("local:abc123");
  expect(handler).not.toHaveBeenCalled();
  setRailRevealHandler(handler);
  expect(handler).toHaveBeenCalledWith("local:abc123");
});

test("revealSessionInRail dispatches the ref to the registered handler", () => {
  const handler = vi.fn();
  setRailRevealHandler(handler);
  revealSessionInRail("local:abc123");
  expect(handler).toHaveBeenCalledWith("local:abc123");
});

test("setRailRevealHandler(null) clears the handler; a queued reveal drops with the unmounted host", () => {
  // No rail mounted (the lazy chunk still in flight): the reveal waits.
  revealSessionInRail("local:x");
  // The host goes away before the chunk arrives (its unmount cleanup
  // clears): the wait drops with it, so a later, unrelated mount does not
  // expand a section the user no longer asked about.
  setRailRevealHandler(null);
  const unrelated = vi.fn();
  setRailRevealHandler(unrelated);
  expect(unrelated).not.toHaveBeenCalled();
});

test("the most recently registered handler wins (a remounted RailHost supersedes the old one)", () => {
  const first = vi.fn();
  const second = vi.fn();
  setRailRevealHandler(first);
  setRailRevealHandler(second);
  revealSessionInRail("local:x");
  expect(first).not.toHaveBeenCalled();
  expect(second).toHaveBeenCalledWith("local:x");
});

test("only the latest queued reveal waits (each reveal supersedes the last)", () => {
  revealSessionInRail("local:stale");
  revealSessionInRail("local:fresh");
  const handler = vi.fn();
  setRailRevealHandler(handler);
  expect(handler).toHaveBeenCalledTimes(1);
  expect(handler).toHaveBeenCalledWith("local:fresh");
});

test("a delivered queue does not redeliver to a later handler", () => {
  revealSessionInRail("local:x");
  const first = vi.fn();
  setRailRevealHandler(first);
  expect(first).toHaveBeenCalledWith("local:x");
  const second = vi.fn();
  setRailRevealHandler(second);
  expect(second).not.toHaveBeenCalled();
});
