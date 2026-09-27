// React keeps an act() scope open until the promise its callback returned
// settles, and it closes the scope only for a caller that awaited it. A test
// that times out inside `await act(async () => ...)` leaves that scope open
// when the next test starts, and the next test then runs inside it: React
// queues that test's renders into the abandoned scope, and logs "The current
// testing environment is not configured to support act(...)" whenever Testing
// Library clears the act flag to wait. The next test fails with a message that
// points at nothing it did.
//
// So after each test the setup waits for any scope the test left open to
// close, one macrotask at a time, and reports it on that test. The wait is
// condition-watching because React offers nothing to await; the bound turns a
// scope that never closes into a named failure. Work that only the test's own
// `finally` or an onTestFinished hook releases cannot settle during the wait,
// so such a scope always takes the whole bound.

export interface ActScopeGuardPorts {
  // Whether a React act() scope is open right now.
  actScopeOpen(): boolean;
  // Resolves after one macrotask, on timers a test cannot have faked.
  nextTurn(): Promise<void>;
  // Milliseconds on a clock a test cannot have faked.
  now(): number;
}

// Waits for an act() scope the test left open to close, and returns the
// failure to report on that test, or undefined when none was open.
export async function waitOutLeakedActScope(ports: ActScopeGuardPorts, boundMs: number): Promise<string | undefined> {
  if (!ports.actScopeOpen()) return undefined;
  const deadline = ports.now() + boundMs;
  while (ports.actScopeOpen()) {
    if (ports.now() >= deadline) {
      return `This test left a React act() scope open, and it was still open ${boundMs}ms later: the test timed out inside act() and the work that act awaited never settled, or it called act(async ...) without awaiting it, which React never closes. Tests after it run inside that scope.`;
    }
    await ports.nextTurn();
  }
  return "This test left a React act() scope open: it timed out inside act(), and the scope closed only once the work that act awaited settled. The next test would have run inside it, so the setup waited for it to close first.";
}

// React's own reading of "a scope is open": its act queue is set. The queue
// also stays set, with no scope open, after an act whose callback threw or
// rejected, until the next act at the outermost level clears it. A no-op act
// does that without touching a scope that is really open, where it only nests.
// React holds back the errors from work that runs inside a scope, and that act
// rethrows them; they go to reportError, because they belong to the test that
// left the scope.
export function reactActScopeOpen(
  internals: { actQueue: unknown },
  act: (callback: () => void) => unknown,
  reportError: (error: unknown) => void,
): boolean {
  if (internals.actQueue === null) return false;
  try {
    act(() => {});
  } catch (error) {
    reportError(error);
  }
  return internals.actQueue !== null;
}
