const GUARDED_METHODS = ["error", "warn", "log", "info", "debug"] as const;

type GuardedMethod = (typeof GUARDED_METHODS)[number];
type GuardedConsole = Record<GuardedMethod, (...args: unknown[]) => void>;

// Test output must be pristine: anything a test writes to the console is a
// failure unless the test expected it. Each guarded method is replaced with a
// plain wrapper that records the call and still prints it. A test that expects
// output spies on the method itself (vi.spyOn(console, "error")
// .mockImplementation(...)) and asserts the calls; its spy sits in front of the
// wrapper, so expected output never reaches the record. The wrappers are plain
// functions rather than vi spies so vi.restoreAllMocks and a test's own
// mockRestore return the console to the guarded wrapper instead of removing it.
export function guardConsoleOutput(target: GuardedConsole) {
  const calls: { method: GuardedMethod; args: unknown[] }[] = [];
  for (const method of GUARDED_METHODS) {
    const print = target[method];
    target[method] = (...args: unknown[]) => {
      calls.push({ method, args });
      print.apply(target, args);
    };
  }
  return {
    // Returns a failure message for everything recorded since the last call,
    // or undefined when nothing was, and clears the record either way.
    takeUnexpectedOutput(): string | undefined {
      const [first, ...rest] = calls.splice(0);
      if (first === undefined) return undefined;
      const more = rest.length > 0 ? ` and ${rest.length} more call(s)` : "";
      return `Unexpected console output: console.${first.method}(${JSON.stringify(first.args.map(String).join(" "))})${more}. Assert expected output with vi.spyOn(console, ...).`;
    },
  };
}
