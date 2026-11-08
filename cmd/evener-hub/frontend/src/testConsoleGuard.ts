const GUARDED_METHODS = ["error", "warn", "log", "info", "debug"] as const;

type GuardedMethod = (typeof GUARDED_METHODS)[number];
type GuardedConsole = Record<GuardedMethod, (...args: unknown[]) => void>;

const ACT_ESCAPE_STACK_LABEL = "act() escape caller stack";

// The captured stack travels as this value, not a bare string, so the renderer
// can tell the guard's own diagnostic from a test's multi-line console argument
// and print only the former verbatim.
class CapturedStack {
  constructor(readonly text: string) {}
}

export interface ConsoleGuardOptions {
  // Append the stack at the console.error call to React's act() escape
  // warning. React names only the component, and the origin of the update that
  // escaped is what one rare occurrence could not name (#4612); set
  // EVENER_ACT_STACK_CAPTURE=1 for a run that has to reproduce it. Off in every
  // normal run, so pristine output is unchanged.
  actStackCapture?: boolean;
}

// React's own format string for an update outside act() (react-dom's
// warnIfUpdatesNotWrappedWithActDEV). Matched structurally, not by the final
// rendered sentence.
function isActUpdateWarning(args: unknown[]): boolean {
  const message = args[0];
  return (
    typeof message === "string" &&
    message.startsWith("An update to ") &&
    message.includes("inside a test was not wrapped in act(")
  );
}

// Vitest runs in Node, but this package's tsconfig carries no Node types, so
// read the switch off the global rather than importing `process`.
function actStackCaptureEnabledByEnv(): boolean {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.EVENER_ACT_STACK_CAPTURE === "1";
}

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
export function guardConsoleOutput(target: GuardedConsole, options: ConsoleGuardOptions = {}) {
  const actStackCapture = options.actStackCapture ?? actStackCaptureEnabledByEnv();
  const calls: { method: GuardedMethod; args: unknown[] }[] = [];
  for (const method of GUARDED_METHODS) {
    const print = target[method];
    target[method] = (...args: unknown[]) => {
      const captured =
        actStackCapture && method === "error" && isActUpdateWarning(args)
          ? [
              ...args,
              new CapturedStack(
                `${ACT_ESCAPE_STACK_LABEL}:\n${new Error(ACT_ESCAPE_STACK_LABEL).stack ?? "(unavailable)"}`,
              ),
            ]
          : args;
      calls.push({ method, args: captured });
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
  // A captured stack is multi-line by nature. JSON-quoting it would hide every
  // frame behind \n escapes, which is the opposite of the diagnostic's job.
  if (arg instanceof CapturedStack) return arg.text;
  try {
    return JSON.stringify(arg) ?? String(arg);
  } catch {
    return String(arg);
  }
}
