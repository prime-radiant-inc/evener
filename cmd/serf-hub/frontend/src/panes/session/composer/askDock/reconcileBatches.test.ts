import { expect, test, vi } from "vitest";
import { type AskBatch, reconcileBatches } from "./reconcileBatches";

function batch(overrides: Partial<AskBatch> = {}): AskBatch {
  return { id: "b1", keys: [], sending: false, ...overrides };
}

test("no batches and no live keys stays empty", () => {
  const mintId = vi.fn();
  expect(reconcileBatches([], [], mintId)).toEqual([]);
  expect(mintId).not.toHaveBeenCalled();
});

test("first-ever live keys with no prior batches mint one new open batch", () => {
  const mintId = vi.fn(() => "new-1");
  const result = reconcileBatches([], ["a:0", "a:1"], mintId);
  expect(result).toEqual([{ id: "new-1", keys: ["a:0", "a:1"], sending: false }]);
  expect(mintId).toHaveBeenCalledTimes(1);
});

test("a new live key merges into the existing OPEN (non-sending) batch rather than minting a new one", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"] })];
  const mintId = vi.fn(() => "should-not-be-used");
  const result = reconcileBatches(prev, ["a:0", "a:1"], mintId);
  expect(result).toEqual([{ id: "b1", keys: ["a:0", "a:1"], sending: false }]);
  expect(mintId).not.toHaveBeenCalled();
});

test("a new live key while the only batch is SENDING mints a separate, independent batch", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"], sending: true })];
  const mintId = vi.fn(() => "b2");
  const result = reconcileBatches(prev, ["a:0", "a:1"], mintId);
  expect(result).toEqual([
    { id: "b1", keys: ["a:0"], sending: true },
    { id: "b2", keys: ["a:1"], sending: false },
  ]);
});

test("a key that falls out of the live set is pruned from a non-sending batch", () => {
  const prev = [batch({ id: "b1", keys: ["a:0", "a:1"] })];
  const result = reconcileBatches(prev, ["a:0"], vi.fn());
  expect(result).toEqual([{ id: "b1", keys: ["a:0"], sending: false }]);
});

test("a non-sending batch that loses every key is dropped entirely", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"] })];
  const result = reconcileBatches(prev, [], vi.fn());
  expect(result).toEqual([]);
});

test("a SENDING batch's keys are protected - never pruned by the live-set signal, even if none remain live", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"], sending: true })];
  const result = reconcileBatches(prev, [], vi.fn());
  expect(result).toEqual([{ id: "b1", keys: ["a:0"], sending: true }]);
});

test("a sendError carries through untouched when nothing about that batch changes", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"], sendError: "transient failure" })];
  const result = reconcileBatches(prev, ["a:0"], vi.fn());
  expect(result).toEqual([{ id: "b1", keys: ["a:0"], sending: false, sendError: "transient failure" }]);
});

test("returns the SAME array reference when nothing changed (no new keys, no pruning)", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"] })];
  const result = reconcileBatches(prev, ["a:0"], vi.fn());
  expect(result).toBe(prev);
});

test("with one sending and one open batch, new keys join the open batch, not a third", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"], sending: true }), batch({ id: "b2", keys: ["a:1"] })];
  const mintId = vi.fn();
  const result = reconcileBatches(prev, ["a:0", "a:1", "a:2"], mintId);
  expect(result).toEqual([
    { id: "b1", keys: ["a:0"], sending: true },
    { id: "b2", keys: ["a:1", "a:2"], sending: false },
  ]);
  expect(mintId).not.toHaveBeenCalled();
});

test("when every existing batch is sending, new keys mint a fresh batch", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"], sending: true }), batch({ id: "b2", keys: ["a:1"], sending: true })];
  const mintId = vi.fn(() => "b3");
  const result = reconcileBatches(prev, ["a:0", "a:1", "a:2"], mintId);
  expect(result).toEqual([
    { id: "b1", keys: ["a:0"], sending: true },
    { id: "b2", keys: ["a:1"], sending: true },
    { id: "b3", keys: ["a:2"], sending: false },
  ]);
});

test("new keys preserve the live set's own posting order when merged into an open batch", () => {
  const prev = [batch({ id: "b1", keys: ["a:0"] })];
  const result = reconcileBatches(prev, ["a:0", "a:1", "a:2"], vi.fn());
  expect(result[0]?.keys).toEqual(["a:0", "a:1", "a:2"]);
});

test("a late-arriving key while a sibling batch is sending later merges into the (still open) second batch", () => {
  // Simulates: batch1 sent (sending=true, keys=[a:0]); ask2 arrives -> batch2
  // minted (keys=[a:1]); ask3 arrives before batch1 settles -> ask3 must
  // join batch2 (the open one), not spawn a third.
  const afterAsk2 = reconcileBatches(
    [batch({ id: "b1", keys: ["a:0"], sending: true })],
    ["a:0", "a:1"],
    vi.fn(() => "b2"),
  );
  const afterAsk3 = reconcileBatches(
    afterAsk2,
    ["a:0", "a:1", "a:2"],
    vi.fn(() => "should-not-be-used"),
  );
  expect(afterAsk3).toEqual([
    { id: "b1", keys: ["a:0"], sending: true },
    { id: "b2", keys: ["a:1", "a:2"], sending: false },
  ]);
});
