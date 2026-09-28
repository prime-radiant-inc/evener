// A test body that times out keeps running: vitest fails the test and moves on,
// but it cannot stop the body, which carries on from wherever it was waiting,
// into whichever test runs next. What it does then lands on that test: it
// clicks what that test rendered, opens act() scopes around that test's
// renders, and when a Testing Library wait it began ends, the wait restores the
// act environment it found, over the one the next test's own wait had set.
//
// So each test runs with a marker of its own, carried through its body's awaits
// by AsyncLocalStorage (testSetup.ts wraps each test in runAsTheCurrentTest).
// The marker records when the test times out, and it ends when the next test
// starts. A body stops at the end of any wait it began before its test timed
// out, and at the first wait it begins after its test ended: the wait never
// settles, so the body goes no further, and the wait restores nothing. Events
// it fires after its test ended are not dispatched. A wait the test's own hooks
// begin after it timed out runs as usual. The body's other steps (a direct
// act() or render(), a store call) still run until it reaches one of those.
//
// Two things it cannot see. It takes the tests in a file to run one at a time,
// as this suite's do (none is concurrent): the next test starting is what ends
// the one before. And a callback a body scheduled on a fake clock runs as
// whichever test later advances that clock, so it counts as that test's work;
// it is out of reach only once the clock is put away (vi.useRealTimers).
import { AsyncLocalStorage } from "node:async_hooks";
import { configure } from "@testing-library/react";

interface TestRun {
  // Vitest aborts it when the test times out.
  signal: AbortSignal;
  ended: boolean;
}

const currentTest = new AsyncLocalStorage<TestRun>();
let latestRun: TestRun | undefined;

export function runAsTheCurrentTest(runTest: () => Promise<void>, signal: AbortSignal): Promise<void> {
  if (latestRun) latestRun.ended = true;
  const run: TestRun = { signal, ended: false };
  latestRun = run;
  return currentTest.run(run, runTest);
}

// Before Node 24 (CI runs 22), every AsyncLocalStorage that has run and was
// never disabled writes a property onto each promise its worker creates from
// then on. vmThreads evaluates this module afresh for every test file, so each
// file turns its own storage off once its tests are done; otherwise a worker
// carries one live storage per file it has run. On those versions, turning it
// off also takes the marker from a body still running then, so a wait it
// begins or an event it fires after that is not stopped (a wait already under
// way still is). By then no test in its file is left for it to act on, and the
// next file runs in a VM context of its own.
export function stopMarkingTests(): void {
  currentTest.disable();
}

// The running test's marker, as a wait or an event begun here would read it.
export function currentTestMarkerForTests(): TestRun | undefined {
  return currentTest.getStore();
}

const whenStopped = new Set<() => void>();

function stopBody(): Promise<never> {
  for (const notify of whenStopped) notify();
  whenStopped.clear();
  return new Promise<never>(() => {});
}

// Resolves the next time a body is stopped at a wait: what a wiring test awaits
// to know the body has reached the point where it would have acted. A stop
// reaches only the callers already waiting, so call this before whatever lets
// the body stop.
export function whenAnEndedBodyStopsForTests(): Promise<void> {
  return new Promise<void>((resolve) => whenStopped.add(resolve));
}

const reactEnvironment = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
const testGlobals = globalThis as { jest?: { advanceTimersByTime(milliseconds: number): void } | null };

// React Testing Library's own test for Jest's fake timers, which its wait
// wrapper advances when they are on.
function jestFakeTimersAreEnabled(): boolean {
  if (testGlobals.jest === undefined || testGlobals.jest === null) return false;
  const timer = setTimeout as unknown as { _isMockFunction?: boolean };
  return timer._isMockFunction === true || Object.hasOwn(setTimeout, "clock");
}

// Replaces React Testing Library's asyncWrapper, which every wait and every
// user-event call runs through, with its own steps in its own order, so a live
// test's waits settle exactly as before: even one more await here reorders the
// updates a wait lets land with React's act environment off, and
// Spawn.test.tsx's fake-timer tests fail on that. It adds checks, never awaits:
// on entry, and before it returns or rethrows, which for a return is after the
// macrotask it waits out (the next test can start during it). A stopped wait
// never reaches the restore of the act environment. Events go through Testing
// Library's own eventWrapper.
export function guardTestingLibraryAgainstEndedBodies(): void {
  configure((testingLibrary) => ({
    asyncWrapper: async (callback) => {
      const run = currentTest.getStore();
      if (run?.ended) return stopBody();
      const begunBeforeTimeout = run !== undefined && !run.signal.aborted;
      const abandoned = () => run !== undefined && (run.ended || (run.signal.aborted && begunBeforeTimeout));
      const previousActEnvironment = reactEnvironment.IS_REACT_ACT_ENVIRONMENT;
      reactEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
      try {
        const result = await callback();
        await new Promise<void>((resolve) => {
          setTimeout(() => resolve(), 0);
          if (jestFakeTimersAreEnabled()) testGlobals.jest?.advanceTimersByTime(0);
        });
        if (abandoned()) return stopBody();
        return result;
      } catch (error) {
        if (abandoned()) return stopBody();
        throw error;
      } finally {
        if (!abandoned()) reactEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment;
      }
    },
    eventWrapper: (callback) => (currentTest.getStore()?.ended ? undefined : testingLibrary.eventWrapper(callback)),
  }));
}
