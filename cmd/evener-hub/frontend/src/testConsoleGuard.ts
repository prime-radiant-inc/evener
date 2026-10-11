const GUARDED_METHODS = ["error", "warn", "log", "info", "debug"] as const;

type GuardedMethod = (typeof GUARDED_METHODS)[number];
type GuardedConsole = Record<GuardedMethod, (...args: unknown[]) => void>;

// Test output must be pristine: anything a test writes through console.error,
// warn, log, info or debug is a failure unless the test expected it. Other
// console methods (table, trace, dir, ...) are not guarded. Each guarded
// method is replaced with a
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
      return `Unexpected console output: console.${first.method}(${first.args.map(describeArgument).join(", ")})${more}. If the test expects it, spy with vi.spyOn(console, "${first.method}").mockImplementation(() => {}) and assert the calls.`;
    },
  };
}

// JSON shows an object's fields where String would print [object Object]. An
// Error's fields are not enumerable, so it reads by its message instead; the
// tag check works across the VM contexts vitest's pool runs files in, where
// instanceof does not. Values JSON cannot encode (cycles, undefined) fall back
// to String.
function describeArgument(arg: unknown): string {
  if (Object.prototype.toString.call(arg) === "[object Error]") return String(arg);
  try {
    return JSON.stringify(arg) ?? String(arg);
  } catch {
    return String(arg);
  }
}
