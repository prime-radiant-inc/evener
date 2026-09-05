import { expect, test } from "vitest";
import { cloneAndDeepFreezeJSON, equalJSON } from "./immutable";

test("equalJSON ignores object key order and preserves exact array order", () => {
  expect(
    equalJSON(
      { alpha: 1, nested: { beta: true, gamma: ["first", "second"] } },
      { nested: { gamma: ["first", "second"], beta: true }, alpha: 1 },
    ),
  ).toBe(true);
  expect(equalJSON({ values: ["first", "second"] }, { values: ["second", "first"] })).toBe(false);
  expect(equalJSON({ alpha: 1 }, { alpha: 1, beta: null })).toBe(false);
});

test("cloneAndDeepFreezeJSON recursively detaches and freezes JSON", () => {
  const sourceJob = { id: "job-1", detail: { status: "running" } };
  const source = {
    metadata: { flags: ["installed"] },
    jobs: [sourceJob],
  };

  const cloned = cloneAndDeepFreezeJSON(source);

  expect(cloned).toEqual(source);
  expect(cloned).not.toBe(source);
  expect(cloned.metadata).not.toBe(source.metadata);
  expect(cloned.metadata.flags).not.toBe(source.metadata.flags);
  expect(cloned.jobs).not.toBe(source.jobs);
  expect(cloned.jobs[0]).not.toBe(source.jobs[0]);
  expect(cloned.jobs[0]?.detail).not.toBe(source.jobs[0]?.detail);
  expect(Object.isFrozen(cloned)).toBe(true);
  expect(Object.isFrozen(cloned.metadata)).toBe(true);
  expect(Object.isFrozen(cloned.metadata.flags)).toBe(true);
  expect(Object.isFrozen(cloned.jobs)).toBe(true);
  expect(Object.isFrozen(cloned.jobs[0])).toBe(true);
  expect(Object.isFrozen(cloned.jobs[0]?.detail)).toBe(true);

  source.metadata.flags[0] = "mutated";
  sourceJob.detail.status = "completed";
  expect(cloned.metadata.flags).toEqual(["installed"]);
  expect(cloned.jobs[0]?.detail.status).toBe("running");
});
