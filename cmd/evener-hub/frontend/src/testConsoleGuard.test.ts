import { expect, test, vi } from "vitest";
import { guardConsoleOutput } from "./testConsoleGuard";

function fakeConsole() {
  const printed: unknown[][] = [];
  const print = (...args: unknown[]) => {
    printed.push(args);
  };
  return {
    target: { error: print, warn: print, log: print, info: print, debug: print },
    printed,
  };
}

test("unexpected console output fails the check, naming the method and its first message", () => {
  const { target } = fakeConsole();
  const guard = guardConsoleOutput(target);

  target.warn("first", 1);
  target.error("second");

  expect(guard.takeUnexpectedOutput()).toBe(
    'Unexpected console output: console.warn("first", 1) and 1 more call(s). If the test expects it, spy with vi.spyOn(console, "warn").mockImplementation(() => {}) and assert the calls.',
  );
});

test("the report shows each argument's detail: objects as JSON, errors by message, the rest as text", () => {
  const { target } = fakeConsole();
  const guard = guardConsoleOutput(target);
  const cyclic: Record<string, unknown> = {};
  cyclic.self = cyclic;

  target.error({ id: 1 }, new Error("boom"), cyclic, undefined);

  expect(guard.takeUnexpectedOutput()).toContain('console.error({"id":1}, Error: boom, [object Object], undefined)');
});

test("the check clears what it reported, so one test's output is not blamed on the next", () => {
  const { target } = fakeConsole();
  const guard = guardConsoleOutput(target);

  target.log("once");
  expect(guard.takeUnexpectedOutput()).toContain('console.log("once")');

  expect(guard.takeUnexpectedOutput()).toBeUndefined();
});

test("a test's own console spy takes the output it expects and asserts, so the check passes", () => {
  const { target, printed } = fakeConsole();
  const guard = guardConsoleOutput(target);

  const spy = vi.spyOn(target, "error").mockImplementation(() => {});
  try {
    target.error("expected failure");
    expect(spy).toHaveBeenCalledWith("expected failure");
  } finally {
    spy.mockRestore();
  }

  expect(guard.takeUnexpectedOutput()).toBeUndefined();
  expect(printed).toEqual([]);
});

test("guarded output still prints, so a failing test shows what it wrote", () => {
  const { target, printed } = fakeConsole();
  guardConsoleOutput(target);

  target.info("visible", { id: 1 });
  target.debug("also visible");

  expect(printed).toEqual([["visible", { id: 1 }], ["also visible"]]);
});
