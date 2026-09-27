// testSetup.ts's handling of a test body that keeps running after its test
// ended (vitest cannot stop a body that timed out), through the hooks it
// actually runs in. Each pair is a test that times out on purpose, which
// test.fails counts as a pass, and the test after it, which must not feel its
// body.
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useEffect, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { whenAnEndedBodyStopsForTests } from "./testEndedBodyGuard";

// Renders its first stage, then its second from a timer: an update Testing
// Library lets land outside act while a wait is running, because the wait
// turns React's act environment off.
function Staged() {
  const [stage, setStage] = useState("first stage");
  useEffect(() => {
    const timer = setTimeout(() => setStage("second stage"), 50);
    return () => clearTimeout(timer);
  }, []);
  return <p>{stage}</p>;
}

// First in the file, so its wait begins with the act environment on, as a
// test's does. The wait ends during the next test, once "first stage"
// renders, and Testing Library then restores the environment it found when
// the wait began.
test.fails("a test that times out while it waits for text", async () => {
  await screen.findByText("first stage", {}, { timeout: 2_000 });
}, 100);

test("the act environment the next test's own wait turned off stays off when that wait ends", async () => {
  render(<Staged />);
  expect(await screen.findByText("second stage")).toBeTruthy();
});

function ClickCounter({ onClick }: { onClick: () => void }) {
  const [clicks, setClicks] = useState(0);
  return (
    <button
      type="button"
      onClick={() => {
        setClicks((count) => count + 1);
        onClick();
      }}
    >
      clicked {clicks} times
    </button>
  );
}

const clickedByAnEndedBody = deferred<void>();

// Its wait ends only once the next test renders the button.
test.fails("a test that times out while it waits for a button", async () => {
  const button = await screen.findByRole("button", {}, { timeout: 2_000 });
  fireEvent.click(button);
}, 100);

test("its body stops when that wait ends, so the button the next test rendered stays unclicked", async () => {
  render(<ClickCounter onClick={() => clickedByAnEndedBody.resolve()} />);
  await act(async () => {
    await Promise.race([clickedByAnEndedBody.promise, whenAnEndedBodyStopsForTests()]);
  });
  expect(screen.getByRole("button").textContent).toBe("clicked 0 times");
});

const resumeTheEndedBody = deferred<void>();
const endedBodyFinished = deferred<void>();

// This one's wait is no Testing Library call, so it resumes as it would have.
test.fails("a test that times out while it awaits something else", async () => {
  await resumeTheEndedBody.promise;
  fireEvent.click(screen.getByRole("button"));
  endedBodyFinished.resolve();
}, 100);

test("an event its body fires once it resumes does nothing", async () => {
  render(<ClickCounter onClick={() => {}} />);
  await act(async () => {
    resumeTheEndedBody.resolve();
    await endedBodyFinished.promise;
  });
  expect(screen.getByRole("button").textContent).toBe("clicked 0 times");
});

const cleanupRanAfterItsTest = deferred<void>();

// Its wait fails on its own, after its test has ended; the body stops there
// rather than unwinding into its finally.
test.fails("a test that times out while it waits for text that never comes", async () => {
  try {
    await screen.findByText("never rendered", {}, { timeout: 300 });
  } finally {
    cleanupRanAfterItsTest.resolve();
  }
}, 100);

test("its body stops when that wait fails too, before its finally runs", async () => {
  const outcome = await Promise.race([
    cleanupRanAfterItsTest.promise.then(() => "its finally ran"),
    whenAnEndedBodyStopsForTests().then(() => "it stopped"),
  ]);
  expect(outcome).toBe("it stopped");
});

// Every wait turns the act environment off while it runs, and this one never
// gets to turn it back on.
test.fails("a test that times out while it waits for more text that never comes", async () => {
  await screen.findByText("never rendered either", {}, { timeout: 2_000 });
}, 100);

test("the test after it still hears about an update outside act", async () => {
  const errors = vi.spyOn(console, "error").mockImplementation(() => {});
  const updated = deferred<void>();
  function Late() {
    const [text, setText] = useState("before");
    useEffect(() => {
      const timer = setTimeout(() => {
        setText("after");
        updated.resolve();
      }, 0);
      return () => clearTimeout(timer);
    }, []);
    return <p>{text}</p>;
  }
  render(<Late />);
  await updated.promise;
  expect(errors.mock.calls.some(([message]) => String(message).includes("not wrapped in act"))).toBe(true);
  errors.mockRestore();
});

const resumeBeforeItsNextWait = deferred<void>();

// Its body resumes during the next test and begins a new wait there.
test.fails("a test that times out while it awaits something before its next wait", async () => {
  await resumeBeforeItsNextWait.promise;
  await screen.findByText("never rendered here either", {}, { timeout: 2_000 });
}, 100);

test("a wait its body begins once it resumes leaves this test's act environment alone", async () => {
  const errors = vi.spyOn(console, "error").mockImplementation(() => {});
  const updated = deferred<void>();
  function Late() {
    const [text, setText] = useState("before");
    useEffect(() => {
      const timer = setTimeout(() => {
        setText("after");
        updated.resolve();
      }, 0);
      return () => clearTimeout(timer);
    }, []);
    return <p>{text}</p>;
  }
  const stopped = whenAnEndedBodyStopsForTests();
  resumeBeforeItsNextWait.resolve();
  await stopped;
  render(<Late />);
  await updated.promise;
  expect(errors.mock.calls.some(([message]) => String(message).includes("not wrapped in act"))).toBe(true);
  errors.mockRestore();
});

const whatItsBodyDid = deferred<string>();
const itsBodyWentOn = deferred<void>();

// Renders what the timed-out test below waits for, from that test's own
// afterEach, which runs before the next test starts, then waits to learn what
// its body did once that wait ended.
afterEach(async (context) => {
  if (context.task.name !== "a test that times out while its own hooks will end its wait") return;
  const stopped = whenAnEndedBodyStopsForTests();
  render(<span>rendered by its own afterEach</span>);
  whatItsBodyDid.resolve(
    await Promise.race([stopped.then(() => "it stopped"), itsBodyWentOn.promise.then(() => "it went on")]),
  );
});

// Its wait ends during its own hooks, before the next test starts: the body
// stops there too, because its test timed out.
test.fails("a test that times out while its own hooks will end its wait", async () => {
  await screen.findByText("rendered by its own afterEach", {}, { timeout: 2_000 });
  itsBodyWentOn.resolve();
}, 100);

test("its body stops when a wait it began before it timed out ends during its hooks", async () => {
  expect(await whatItsBodyDid.promise).toBe("it stopped");
});
