import * as React from "react";
import { act } from "react";
import { expect, test } from "vitest";
import { awaitActScopeClosed } from "./testActScopeGuard";

const reactInternals = (
  React as unknown as { __CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE: { actQueue: unknown } }
).__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;

function realPorts() {
  return {
    actScopeOpen: () => reactInternals.actQueue !== null,
    nextTurn: () => new Promise<void>((resolve) => setTimeout(resolve, 0)),
    now: () => performance.now(),
  };
}

test("a test that left no act scope open passes at once", async () => {
  await expect(awaitActScopeClosed(realPorts(), 1_000)).resolves.toBeUndefined();
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

  const report = await awaitActScopeClosed(realPorts(), 5_000);

  expect(released).toBe(true);
  expect(reactInternals.actQueue).toBeNull();
  expect(report).toMatch(/left a React act\(\) scope open/);
  await abandonedBody;
});

test("a scope that never closes is reported once the bound passes", async () => {
  let clock = 0;
  const report = await awaitActScopeClosed(
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
