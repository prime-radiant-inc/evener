import { afterEach, test, expect } from "vitest";
import {
  claimLeader,
  releaseLeader,
  upsertSubagentRow,
  updateSubagentRowIfExists,
  useSubagentRows,
  resetSubagentModuleStoreForTests,
} from "./subagentModuleStore";
import { renderHook } from "@testing-library/react";

afterEach(resetSubagentModuleStoreForTests);

// subagentModuleStore.ts is the cross-item aggregation side-channel this
// directory needs but the locked ToolRenderProps/ItemRenderProps interfaces
// don't provide directly (ToolRenderProps is {item, live} only - no turn,
// no sibling items; ToolCallItem.tsx, which IS given the owning turn,
// discards it before reaching a tool descriptor's body). See
// subagentModule.tsx's own header for the full design rationale: every
// job_*/delegate row computes its OWN data from its OWN item and upserts
// it here keyed by (turnId, rowKey); the first such row to mount for a
// turnId claims "leadership" and is the one that actually renders the
// aggregated module chrome (tally/fold), reading every row back out
// reactively via useSubagentRows.

test("upsertSubagentRow: a fresh row appears in the turn's row list", () => {
  upsertSubagentRow("turn_a", { rowKey: "job_1", kind: "running", task: "run tests", resultPreview: "" });
  const { result } = renderHook(() => useSubagentRows("turn_a"));
  expect(result.current).toHaveLength(1);
  expect(result.current[0]).toMatchObject({ rowKey: "job_1", kind: "running", task: "run tests" });
});

test("upsertSubagentRow: a second call with the SAME rowKey updates in place rather than adding a row", () => {
  upsertSubagentRow("turn_b", { rowKey: "job_1", kind: "running", task: "run tests", resultPreview: "" });
  upsertSubagentRow("turn_b", { rowKey: "job_1", kind: "done", task: "run tests", resultPreview: "all green" });
  const { result } = renderHook(() => useSubagentRows("turn_b"));
  expect(result.current).toHaveLength(1);
  expect(result.current[0]).toMatchObject({ kind: "done", resultPreview: "all green" });
});

test("upsertSubagentRow: rows are ordered by spawn (first-seen) order, not update recency", () => {
  upsertSubagentRow("turn_c", { rowKey: "job_1", kind: "running", task: "first", resultPreview: "" });
  upsertSubagentRow("turn_c", { rowKey: "job_2", kind: "running", task: "second", resultPreview: "" });
  // Re-touch job_1 - it must stay first, not jump to the back.
  upsertSubagentRow("turn_c", { rowKey: "job_1", kind: "done", task: "first", resultPreview: "ok" });
  const { result } = renderHook(() => useSubagentRows("turn_c"));
  expect(result.current.map((r) => r.rowKey)).toEqual(["job_1", "job_2"]);
});

test("rows in different turns never mix", () => {
  upsertSubagentRow("turn_d1", { rowKey: "job_1", kind: "running", task: "x", resultPreview: "" });
  upsertSubagentRow("turn_d2", { rowKey: "job_1", kind: "failed", task: "y", resultPreview: "" });
  const { result: r1 } = renderHook(() => useSubagentRows("turn_d1"));
  const { result: r2 } = renderHook(() => useSubagentRows("turn_d2"));
  expect(r1.current[0]?.kind).toBe("running");
  expect(r2.current[0]?.kind).toBe("failed");
});

test("useSubagentRows for a turn with no rows returns an empty array, not undefined", () => {
  const { result } = renderHook(() => useSubagentRows("turn_never_seen"));
  expect(result.current).toEqual([]);
});

// --- updateSubagentRowIfExists (job_status/job_stop/delegate_send's own
// contribution - they check on an EXISTING child, never spawn a fresh row
// on their own, mirroring the legacy reconcileSubagent's own rule) -------

test("updateSubagentRowIfExists: updates an existing row's kind/preview in place", () => {
  upsertSubagentRow("turn_e", { rowKey: "job_1", kind: "running", task: "long build", resultPreview: "" });
  updateSubagentRowIfExists("turn_e", "job_1", { kind: "failed", resultPreview: "build error" });
  const { result } = renderHook(() => useSubagentRows("turn_e"));
  expect(result.current[0]).toMatchObject({ kind: "failed", resultPreview: "build error", task: "long build" });
});

test("updateSubagentRowIfExists: a rowKey with no existing row is a silent no-op, never fabricates one", () => {
  updateSubagentRowIfExists("turn_f", "job_never_spawned", { kind: "done", resultPreview: "x" });
  const { result } = renderHook(() => useSubagentRows("turn_f"));
  expect(result.current).toEqual([]);
});

// --- leader election -----------------------------------------------------

test("claimLeader: the first claim for a turnId succeeds", () => {
  expect(claimLeader("turn_g", "item_1")).toBe(true);
});

test("claimLeader: a second, different item never wins leadership for the same turn", () => {
  claimLeader("turn_h", "item_1");
  expect(claimLeader("turn_h", "item_2")).toBe(false);
});

test("claimLeader: re-claiming with the SAME item id that already holds leadership stays true (idempotent)", () => {
  claimLeader("turn_i", "item_1");
  expect(claimLeader("turn_i", "item_1")).toBe(true);
});

test("releaseLeader: frees the slot only when released by the current leader, letting a later claim win", () => {
  claimLeader("turn_j", "item_1");
  releaseLeader("turn_j", "item_1");
  expect(claimLeader("turn_j", "item_2")).toBe(true);
});

test("releaseLeader: a stale release from a non-leader item does not clear the real leader's slot", () => {
  claimLeader("turn_k", "item_1");
  releaseLeader("turn_k", "item_2"); // item_2 was never leader
  expect(claimLeader("turn_k", "item_3")).toBe(false); // item_1 still holds it
});

test("leadership is independent per turnId", () => {
  claimLeader("turn_l1", "item_1");
  expect(claimLeader("turn_l2", "item_1")).toBe(true);
});
