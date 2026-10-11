// testSetup.ts's act-scope guard, through the hooks it actually runs in: the
// order of these tests is the point, so each one reads what the one before it
// left behind.
import { act, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";

// Times out inside act on purpose, after rendering and logging. The guard
// waits for the scope to close and fails this test by name, together with its
// own console output, so test.fails counts that expected failure as a pass.
test.fails("a test that times out inside act fails with its own report", async () => {
  render(<span data-testid="left-by-the-leaking-test">left</span>);
  console.warn("written by the leaking test");
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 300));
  });
}, 100);

// Nothing of the leaking test's reaches this one: its tree was unmounted and
// its console output was reported on it, so this test's own console check
// passes.
test("the test after it starts with an empty page and a clean console", () => {
  expect(screen.queryByTestId("left-by-the-leaking-test")).toBeNull();
  expect(document.body.innerHTML).toBe("");
});

// React leaves its act queue set after an act that rejects, with no scope
// open. That is not a leak: this test passes, and promptly.
test("a test whose act rejects as it expects passes", async () => {
  await expect(
    act(async () => {
      throw new Error("rejected on purpose");
    }),
  ).rejects.toThrow("rejected on purpose");
});

// React keeps the work an act queued before its callback rejected, and the
// next act runs it: here, the guard's own. When that work throws, the error is
// this test's and fails it, which is the only thing that does, and the setup's
// unmount still runs for it.
test.fails("a test whose leftover React work throws fails with that error", async () => {
  function ThrowsOnRender(): never {
    throw new Error("thrown by work the rejected act left queued");
  }
  await expect(
    act(async () => {
      render(<ThrowsOnRender />);
      throw new Error("rejected on purpose");
    }),
  ).rejects.toThrow("rejected on purpose");
});

test("the test after that one also starts with an empty page", () => {
  expect(document.body.innerHTML).toBe("");
});
