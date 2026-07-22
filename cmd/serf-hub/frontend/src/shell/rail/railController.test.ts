// @vitest-environment node
import { afterEach, expect, test, vi } from "vitest";
import { revealSessionInRail, setRailRevealHandler } from "./railController";

afterEach(() => setRailRevealHandler(null));

test("revealSessionInRail is a no-op-safe call when no rail is mounted", () => {
  expect(() => revealSessionInRail("local:abc123")).not.toThrow();
});

test("revealSessionInRail dispatches the ref to the registered handler", () => {
  const handler = vi.fn();
  setRailRevealHandler(handler);
  revealSessionInRail("local:abc123");
  expect(handler).toHaveBeenCalledWith("local:abc123");
});

test("setRailRevealHandler(null) clears the handler; later reveals are no-ops", () => {
  const handler = vi.fn();
  setRailRevealHandler(handler);
  setRailRevealHandler(null);
  revealSessionInRail("local:x");
  expect(handler).not.toHaveBeenCalled();
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
