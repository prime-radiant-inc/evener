import { expect, test } from "vitest";
import { unmountAttemptsBeforeGivingUp, unmountEveryTree } from "./testUnmount";

function messages(errors: unknown[]): string[] {
  return errors.map((error) => (error instanceof Error ? error.message : String(error)));
}

test("an unmount that throws is reported, and the unmount runs again until it gets through", () => {
  const reported: unknown[] = [];
  const thrownByEachRun = [new Error("thrown by the first tree"), new Error("thrown by the second tree")];
  let runs = 0;
  unmountEveryTree(
    () => {
      runs += 1;
      const error = thrownByEachRun.shift();
      if (error) throw error;
    },
    (error) => reported.push(error),
  );
  expect(runs).toBe(3);
  expect(messages(reported)).toEqual(["thrown by the first tree", "thrown by the second tree"]);
});

test("an unmount that never gets through stops, and says what it relied on", () => {
  const reported: unknown[] = [];
  let runs = 0;
  unmountEveryTree(
    () => {
      runs += 1;
      throw new Error("thrown by the same tree again");
    },
    (error) => reported.push(error),
  );
  expect(runs).toBe(unmountAttemptsBeforeGivingUp);
  expect(reported).toHaveLength(unmountAttemptsBeforeGivingUp + 1);
  expect(messages(reported).at(-1)).toContain("root.unmount()");
});
