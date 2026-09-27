// React keeps an act() scope open until the promise its callback returned
// settles. A test that times out inside `await act(async () => ...)`, or calls
// act(async ...) without awaiting it, leaves that scope open when the next test
// starts, and the next test then runs inside it: React queues that test's
// renders into the abandoned scope instead of committing them, and logs "The
// current testing environment is not configured to support act(...)" whenever
// Testing Library clears the act flag to wait. The next test fails with a
// message that points at nothing it did.
//
// So after each test the setup waits for any scope the test left open to
// close, one macrotask at a time, and then fails that test by name. The wait is
// condition-watching because React offers nothing to await; the bound only
// turns a scope that never closes into a named failure.

export interface ActScopeGuardPorts {
  // Whether a React act() scope is open right now.
  actScopeOpen(): boolean;
  // Resolves after one macrotask, on timers a test cannot have faked.
  nextTurn(): Promise<void>;
  // Milliseconds on a clock a test cannot have faked.
  now(): number;
}

export async function awaitActScopeClosed(ports: ActScopeGuardPorts, boundMs: number): Promise<string | undefined> {
  if (!ports.actScopeOpen()) return undefined;
  const deadline = ports.now() + boundMs;
  while (ports.actScopeOpen()) {
    if (ports.now() >= deadline) {
      return `This test left a React act() scope open, and it was still open ${boundMs}ms later: the test timed out inside act(), or called act(async ...) without awaiting it, and the work it awaited never finished. Tests after it run inside that scope.`;
    }
    await ports.nextTurn();
  }
  return "This test left a React act() scope open: it timed out inside act(), or called act(async ...) without awaiting it. The next test would have run inside that scope, so the setup waited for it to close first.";
}
