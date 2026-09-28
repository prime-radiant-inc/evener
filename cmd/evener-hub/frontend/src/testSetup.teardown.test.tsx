// testSetup.ts's teardown of each test, through the hooks it actually runs
// in: the order of these tests is the point, so each one reads what the one
// before it left behind.
import { cleanup, render } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, expect, onTestFailed, test } from "vitest";

function ThrowsOnUnmount() {
  useEffect(
    () => () => {
      throw new Error("thrown by an effect cleanup as its tree unmounts");
    },
    [],
  );
  return <span>throws as it unmounts</span>;
}

// What the setup failed the last failing test with, one message per error.
let reportedOnTheFailingTest: string[] = [];

function recordWhatFailsThisTest() {
  onTestFailed(({ task }) => {
    reportedOnTheFailingTest = (task.result?.errors ?? []).map((error) => error.message);
  });
}

const testWhoseOwnAfterEachThrows =
  "a test whose file's own afterEach throws fails with that error and its own console output";
const testWhoseOwnCleanupMeetsAThrowingUnmount =
  "a test whose file's own cleanup meets a tree that throws as it unmounts fails with that error and its own console output";

// This file's own afterEach, which runs before the setup's teardown, as a test
// file's afterEach always does.
afterEach(({ task }) => {
  if (task.name === testWhoseOwnAfterEachThrows) throw new Error("thrown by the file's own afterEach");
  if (task.name === testWhoseOwnCleanupMeetsAThrowingUnmount) cleanup();
});

// The unmount throws after the test logged. The setup fails the test with both,
// so test.fails counts that expected failure as a pass.
test.fails("a test whose tree throws as it unmounts fails with that error and its own console output", () => {
  recordWhatFailsThisTest();
  render(<ThrowsOnUnmount />);
  console.warn("written by the test whose tree throws as it unmounts");
});

// Nothing of that test's reaches this one: its container was removed and its
// console output was reported on it, so this test's own console check passes.
test("the test after it starts with an empty page and a clean console", () => {
  expect(reportedOnTheFailingTest).toEqual([
    expect.stringContaining("thrown by an effect cleanup as its tree unmounts"),
    expect.stringContaining("written by the test whose tree throws as it unmounts"),
  ]);
  expect(document.body.innerHTML).toBe("");
});

function LogsAsItUnmounts() {
  useEffect(
    () => () => {
      console.warn("written as the tree unmounts");
    },
    [],
  );
  return <span>logs as it unmounts</span>;
}

// The teardown unmounts before it checks the console, so what a tree writes as
// it unmounts is reported on the test that rendered it.
test.fails("a test whose tree logs as it unmounts fails with that output", () => {
  recordWhatFailsThisTest();
  render(<LogsAsItUnmounts />);
});

test("the test after it hears nothing of what the earlier tree wrote as it unmounted", () => {
  expect(reportedOnTheFailingTest).toEqual([expect.stringContaining("written as the tree unmounts")]);
  expect(document.body.innerHTML).toBe("");
});

let laterTreeUnmounted = false;

function RecordsItsUnmount() {
  useEffect(
    () => () => {
      laterTreeUnmounted = true;
    },
    [],
  );
  return <span>records its unmount</span>;
}

test.fails("a test whose first tree throws as it unmounts, with a second tree after it", () => {
  render(<ThrowsOnUnmount />);
  render(<RecordsItsUnmount />);
});

test("the tree after the one that threw is unmounted too", () => {
  expect(laterTreeUnmounted).toBe(true);
  expect(document.body.innerHTML).toBe("");
});

// Vitest runs a file's afterEach hooks in one loop, so this file's throwing
// afterEach skips every afterEach after it. The setup's teardown still runs.
test.fails(testWhoseOwnAfterEachThrows, () => {
  recordWhatFailsThisTest();
  render(<span>rendered by the test whose file's own afterEach throws</span>);
  console.warn("written by the test whose file's own afterEach throws");
});

test("the test after a throwing afterEach starts with an empty page and a clean console", () => {
  expect(reportedOnTheFailingTest).toEqual([
    expect.stringContaining("thrown by the file's own afterEach"),
    expect.stringContaining("written by the test whose file's own afterEach throws"),
  ]);
  expect(document.body.innerHTML).toBe("");
});

// Many test files run Testing Library's cleanup() in an afterEach of their
// own. When a tree throws as it unmounts there, that cleanup throws before the
// setup's teardown runs, and stops part way.
test.fails(testWhoseOwnCleanupMeetsAThrowingUnmount, () => {
  recordWhatFailsThisTest();
  render(<ThrowsOnUnmount />);
  console.warn("written by the test whose file's own cleanup meets a throwing unmount");
});

test("the test after a file's own throwing cleanup starts with an empty page and a clean console", () => {
  expect(reportedOnTheFailingTest).toEqual([
    expect.stringContaining("thrown by an effect cleanup as its tree unmounts"),
    expect.stringContaining("written by the test whose file's own cleanup meets a throwing unmount"),
  ]);
  expect(document.body.innerHTML).toBe("");
});
