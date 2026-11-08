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

test("an act() escape warning records its caller stack when capture is enabled", () => {
  const { target } = fakeConsole();
  const guard = guardConsoleOutput(target, { actStackCapture: true });

  function warningCaller() {
    target.error("An update to %s inside a test was not wrapped in act(...)", "Composer");
  }
  warningCaller();

  const report = guard.takeUnexpectedOutput();
  expect(report).toContain("act() escape caller stack");
  // The stack names the frame that logged the warning, which is what the
  // capture exists to add to the component name React already supplies.
  expect(report).toContain("warningCaller");
});

test("the act() stack capture is off unless enabled, leaving other output unchanged", () => {
  const { target } = fakeConsole();
  const guard = guardConsoleOutput(target, { actStackCapture: false });

  target.error("An update to %s inside a test was not wrapped in act(...)", "Composer");

  const report = guard.takeUnexpectedOutput();
  expect(report).not.toContain("act() escape caller stack");
  expect(report).toContain("console.error");
});

test("EVENER_ACT_STACK_CAPTURE=1 is the switch that turns capture on", () => {
  vi.stubEnv("EVENER_ACT_STACK_CAPTURE", "1");
  try {
    const { target } = fakeConsole();
    const guard = guardConsoleOutput(target);
    target.error("An update to %s inside a test was not wrapped in act(...)", "Composer");
    expect(guard.takeUnexpectedOutput()).toContain("act() escape caller stack");
  } finally {
    vi.unstubAllEnvs();
  }
});
