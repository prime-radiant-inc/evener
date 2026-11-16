// The deadline Testing Library's waits use by default, for one test (#5373). A
// wait that passes its own `timeout` keeps that instead.
//
// Testing Library defaults that deadline to a fixed second. That is an
// assertion window sized to nothing in particular, and an async chain - a
// store gate, a serial mutation queue, a wire round-trip - legitimately sums
// past it on a loaded host, so the wait fails while the test still has budget
// to run. Vitest already owns that budget: the test's own timeout bounds the
// whole test. So the wait gets that budget. It is the runner's own ceiling for
// the test, not a second guess about how slow a host may be, and a test that
// hangs still fails at its declared deadline.
//
// The wait gives up a margin before that deadline, so its failure is the one
// reported: the assertion and the DOM it looked at. That holds for a wait begun
// near the top of a test, which is the ordinary case.
//
// It does not hold for a wait begun late in a test, or for a second slow wait in
// the same test. Testing Library takes a duration and starts its timer when the
// wait is called, so a wait begun three seconds into a five-second test gets its
// four seconds from there and the runner's timer fires first - the test reports
// only "Test timed out", naming nothing. internal/testwait recomputes its margin
// from the binary's absolute deadline and so has no such window; Testing
// Library's config cannot express that. Closing it needs a per-test deadline
// the wait can read, which this setup file has no way to give it.
//
// The floor is Testing Library's default. A test that declares less than it
// keeps the window it always had, so no test's wait gets tighter than before.
// For such a test the floor can sit at or above its own timeout; that only
// means the runner's timer may report first, which is the report it had.
import { configure, getConfig } from "@testing-library/react";

// Testing Library's own default (asyncUtilTimeout in @testing-library/dom).
const TESTING_LIBRARY_DEFAULT_WAIT_MS = 1_000;

// How long before the test's deadline the wait gives up.
const WAIT_DEADLINE_MARGIN_MS = 1_000;

// The wait deadline for a test whose vitest timeout is `testTimeoutMs`. Zero
// and Infinity are the runner's "no deadline": no finite budget, so the default
// stands. Undefined carries the same meaning for a caller with no test task.
// The aroundEach below always has one, so that arm is typing rather than a
// shape in use.
export function asyncWaitBudgetFor(testTimeoutMs: number | undefined): number {
  if (testTimeoutMs === undefined || !Number.isFinite(testTimeoutMs) || testTimeoutMs <= 0) {
    return TESTING_LIBRARY_DEFAULT_WAIT_MS;
  }
  return Math.max(TESTING_LIBRARY_DEFAULT_WAIT_MS, testTimeoutMs - WAIT_DEADLINE_MARGIN_MS);
}

// Sets the deadline Testing Library's waits use to this test's budget, and
// returns the function that puts the previous one back. Called from the setup's
// aroundEach, which vitest wraps around each test's beforeEach, body, afterEach
// and fixture cleanup - not around beforeAll/afterAll, which stay on the
// default.
export function useTheTestRunnersWaitBudget(testTimeoutMs: number | undefined): () => void {
  const previous = getConfig().asyncUtilTimeout;
  configure({ asyncUtilTimeout: asyncWaitBudgetFor(testTimeoutMs) });
  return () => configure({ asyncUtilTimeout: previous });
}
