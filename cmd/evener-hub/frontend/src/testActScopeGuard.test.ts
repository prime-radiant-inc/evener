import * as React from "react";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, test } from "vitest";
import { reactActScopeOpen, waitOutLeakedActScope } from "./testActScopeGuard";

const reactInternals = (
  React as unknown as { __CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE: { actQueue: unknown } }
).__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;

function rethrow(error: unknown): never {
  throw error;
}

function realPorts() {
  return {
    actScopeOpen: () => reactActScopeOpen(reactInternals, act, rethrow),
    nextTurn: () => new Promise<void>((resolve) => setTimeout(resolve, 0)),
    now: () => performance.now(),
  };
}

test("a test that left no act scope open passes at once", async () => {
  await expect(waitOutLeakedActScope(realPorts(), 1_000)).resolves.toBeUndefined();
});

// The shape a timed-out test leaves behind: its body is still awaiting an
// async act whose work is running when the test's hooks run. React closes the
// scope only once that work settles.
test("an act scope the test left open is waited out, then reported", async () => {
  let released = false;
  const abandonedBody = (async () => {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30));
      released = true;
    });
  })();
  expect(reactInternals.actQueue).not.toBeNull();

  const report = await waitOutLeakedActScope(realPorts(), 5_000);

  expect(released).toBe(true);
  expect(reactInternals.actQueue).toBeNull();
  expect(report).toMatch(/left a React act\(\) scope open/);
  await abandonedBody;
});

test("a scope that never closes is reported when the bound runs out", async () => {
  let clock = 0;
  const report = await waitOutLeakedActScope(
    {
      actScopeOpen: () => true,
      nextTurn: async () => {
        clock += 100;
      },
      now: () => clock,
    },
    1_000,
  );

  expect(report).toMatch(/still open 1000ms later/);
  expect(clock).toBe(1_000);
});

// React leaves its act queue set after an act whose callback rejected, with no
// scope open, until the next act at the outermost level clears it.
test("the queue a rejected act leaves behind reads as no open scope, and reading it clears it", async () => {
  await expect(
    act(async () => {
      throw new Error("rejected on purpose");
    }),
  ).rejects.toThrow("rejected on purpose");
  expect(reactInternals.actQueue).not.toBeNull();

  expect(reactActScopeOpen(reactInternals, act, rethrow)).toBe(false);
  expect(reactInternals.actQueue).toBeNull();
});

test("an act scope that is open reads as open, and reading it leaves it open", async () => {
  let release = () => {};
  const heldBody = (async () => {
    await act(async () => {
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    });
  })();

  expect(reactActScopeOpen(reactInternals, act, rethrow)).toBe(true);
  expect(reactInternals.actQueue).not.toBeNull();

  release();
  await heldBody;
  expect(reactActScopeOpen(reactInternals, act, rethrow)).toBe(false);
});

// React holds back the errors from work that runs inside an act scope, and the
// next act to finish rethrows them.
test("errors from work that runs as the scope is read are handed on, and the reading still answers", async () => {
  function ThrowsOnRender(): never {
    throw new Error("thrown by work the rejected act left queued");
  }
  const root = createRoot(document.createElement("div"));
  await expect(
    act(async () => {
      root.render(createElement(ThrowsOnRender));
      throw new Error("rejected on purpose");
    }),
  ).rejects.toThrow("rejected on purpose");

  const errors: unknown[] = [];
  expect(reactActScopeOpen(reactInternals, act, (error) => errors.push(error))).toBe(false);
  expect(errors.map(String)).toEqual(["Error: thrown by work the rejected act left queued"]);
});
