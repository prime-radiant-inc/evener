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
import { AsyncLocalStorage } from "node:async_hooks";
import { configure, getConfig } from "@testing-library/react";

interface TestRun {
  timedOut: boolean;
  ended: boolean;
}

const currentTest = new AsyncLocalStorage<TestRun>();
let latestRun: TestRun | undefined;

export function runAsTheCurrentTest<T>(runTest: () => Promise<T>, signal: AbortSignal): Promise<T> {
  if (latestRun) latestRun.ended = true;
  const run: TestRun = { timedOut: false, ended: false };
  latestRun = run;
  signal.addEventListener("abort", () => {
    run.timedOut = true;
  });
  return currentTest.run(run, runTest);
}

const whenStopped = new Set<() => void>();

function stopBody(): Promise<never> {
  for (const notify of whenStopped) notify();
  whenStopped.clear();
  return new Promise<never>(() => {});
}

// Resolves the next time a body is stopped at a wait: what a wiring test awaits
// to know the body has reached the point where it would have acted.
export function whenAnEndedBodyStopsForTests(): Promise<void> {
  return new Promise<void>((resolve) => whenStopped.add(resolve));
}

const environment = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean; jest?: unknown };

// React Testing Library's own test for Jest's fake timers, which its wait
// wrapper advances when they are on.
function jestFakeTimersAreEnabled(): boolean {
  if (environment.jest === undefined || environment.jest === null) return false;
  const timer = setTimeout as unknown as { _isMockFunction?: boolean };
  return timer._isMockFunction === true || Object.hasOwn(setTimeout, "clock");
}

// Replaces React Testing Library's asyncWrapper, which every wait and every
// user-event call runs through, with its own steps in its own order, so a live
// test's waits settle exactly as before: even one more await here reorders the
// updates a wait lets land with React's act environment off, and
// Spawn.test.tsx's fake-timer tests fail on that. The one addition is the check
// after the macrotask the wrapper waits out before it returns (the next test
// can start during it): a stopped wait never reaches the restore of the act
// environment. Events go through Testing Library's own eventWrapper.
export function guardTestingLibraryAgainstEndedBodies(): void {
  const testingLibrary = getConfig();
  configure({
    asyncWrapper: async (callback) => {
      const run = currentTest.getStore();
      if (run?.ended) return stopBody();
      const begunBeforeTimeout = run !== undefined && !run.timedOut;
      const abandoned = () => run !== undefined && (run.ended || (run.timedOut && begunBeforeTimeout));
      const previousActEnvironment = environment.IS_REACT_ACT_ENVIRONMENT;
      environment.IS_REACT_ACT_ENVIRONMENT = false;
      let stopped = false;
      try {
        const result = await callback();
        await new Promise<void>((resolve) => {
          setTimeout(() => resolve(), 0);
          if (jestFakeTimersAreEnabled())
            (environment.jest as { advanceTimersByTime(ms: number): void }).advanceTimersByTime(0);
        });
        if (abandoned()) {
          stopped = true;
          return stopBody();
        }
        return result;
      } catch (error) {
        if (abandoned()) {
          stopped = true;
          return stopBody();
        }
        throw error;
      } finally {
        if (!stopped) environment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment;
      }
    },
    eventWrapper: (callback) => (currentTest.getStore()?.ended ? undefined : testingLibrary.eventWrapper(callback)),
  });
}
