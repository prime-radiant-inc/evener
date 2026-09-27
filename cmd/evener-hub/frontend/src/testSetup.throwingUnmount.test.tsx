// testSetup.ts's unmount of each test's trees, through the hooks it actually
// runs in: the order of these tests is the point, so each one reads what the
// one before it left behind.
import { render } from "@testing-library/react";
import { useEffect } from "react";
import { expect, onTestFailed, test } from "vitest";

function ThrowsOnUnmount() {
  useEffect(
    () => () => {
      throw new Error("thrown by an effect cleanup as its tree unmounts");
    },
    [],
  );
  return <span>throws as it unmounts</span>;
}

let reportedOnTheThrowingTest: string[] = [];

// The unmount throws after the test logged. The setup fails the test with both,
// so test.fails counts that expected failure as a pass.
test.fails("a test whose tree throws as it unmounts fails with that error and its own console output", () => {
  onTestFailed(({ task }) => {
    reportedOnTheThrowingTest = (task.result?.errors ?? []).map((error) => error.message);
  });
  render(<ThrowsOnUnmount />);
  console.warn("written by the test whose tree throws as it unmounts");
});

// Nothing of that test's reaches this one: its container was removed and its
// console output was reported on it, so this test's own console check passes.
test("the test after it starts with an empty page and a clean console", () => {
  expect(reportedOnTheThrowingTest).toEqual([
    expect.stringContaining("thrown by an effect cleanup as its tree unmounts"),
    expect.stringContaining("written by the test whose tree throws as it unmounts"),
  ]);
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
