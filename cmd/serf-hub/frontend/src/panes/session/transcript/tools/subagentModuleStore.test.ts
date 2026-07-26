import { renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { SerfJobInfo } from "../../../../protocol/types.gen";
import {
  applySerfJobFinished,
  applySerfJobStarted,
  claimLeader,
  classifyJobStatus,
  releaseLeader,
  resetSubagentModuleStoreForTests,
  resolveRowKey,
  setWatchedLiveKind,
  turnScopeKey,
  updateSubagentRowIfExists,
  upsertSubagentRow,
  useSubagentRows,
} from "./subagentModuleStore";

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

// hzq9: rows sort worst-first by kind, and only THEN by spawn (first-seen)
// order within a kind - never by plain update recency. Both halves of that
// rule matter and neither may be dropped in favor of the other: an
// incidental re-upsert that doesn't change a row's kind must not reorder it
// (recency alone is never the sort key), but a row whose kind genuinely
// changes MUST move - that's the whole point of worst-first, and exactly
// when a reader should notice it.
test("upsertSubagentRow: same-kind rows keep spawn order on an incidental re-touch that doesn't change kind", () => {
  upsertSubagentRow("turn_c", { rowKey: "job_1", kind: "running", task: "first", resultPreview: "" });
  upsertSubagentRow("turn_c", { rowKey: "job_2", kind: "running", task: "second", resultPreview: "" });
  // Re-touch job_1 with the SAME kind - it must stay first, not jump to the
  // back just because it was touched more recently.
  upsertSubagentRow("turn_c", { rowKey: "job_1", kind: "running", task: "first", resultPreview: "still going" });
  const { result } = renderHook(() => useSubagentRows("turn_c"));
  expect(result.current.map((r) => r.rowKey)).toEqual(["job_1", "job_2"]);
});

test("upsertSubagentRow: a row settling to a lower-priority kind (done) moves behind a still-running row, even though it spawned first", () => {
  upsertSubagentRow("turn_c2", { rowKey: "job_1", kind: "running", task: "first", resultPreview: "" });
  upsertSubagentRow("turn_c2", { rowKey: "job_2", kind: "running", task: "second", resultPreview: "" });
  // job_1 spawned first, but settling to "done" demotes it behind job_2,
  // which is still running - a row's OWN state changing is exactly when a
  // reader should see it move.
  upsertSubagentRow("turn_c2", { rowKey: "job_1", kind: "done", task: "first", resultPreview: "ok" });
  const { result } = renderHook(() => useSubagentRows("turn_c2"));
  expect(result.current.map((r) => r.rowKey)).toEqual(["job_2", "job_1"]);
});

// hzq9: the row LIST (not just the header tally, which already ordered
// failures first) must sort worst-first: failed -> unknown -> running ->
// stopped -> done. "stopped" (3zf8) sits between running and done - a
// deliberate stop is neither a live child nor a defect, but it is not a
// clean success either, so it stays out of done's "nothing to see here"
// territory. Spawned deliberately out of worst-first order (done first) so
// a naive spawn-index sort would leave this test's own arrange step
// accidentally matching the expected order.
test("hzq9: rows sort worst-first by kind (failed > unknown > running > stopped > done), spawn order applying only within a kind", () => {
  upsertSubagentRow("turn_worst", { rowKey: "r_done", kind: "done", task: "d", resultPreview: "" });
  upsertSubagentRow("turn_worst", { rowKey: "r_running", kind: "running", task: "r", resultPreview: "" });
  upsertSubagentRow("turn_worst", { rowKey: "r_failed", kind: "failed", task: "f", resultPreview: "" });
  upsertSubagentRow("turn_worst", { rowKey: "r_unknown", kind: "unknown", task: "u", resultPreview: "" });
  upsertSubagentRow("turn_worst", { rowKey: "r_stopped", kind: "stopped", task: "s", resultPreview: "" });
  const { result } = renderHook(() => useSubagentRows("turn_worst"));
  expect(result.current.map((r) => r.rowKey)).toEqual(["r_failed", "r_unknown", "r_running", "r_stopped", "r_done"]);
});

// The live overlay kind (liveKind) is what the row actually DISPLAYS
// (subagentModule.tsx's SubagentRowView reads liveKind over the frozen
// kind) - sorting must agree with that, or a row could visually read as
// failed while still sorting by its stale frozen "running"/"done".
test("hzq9: sorting reads the LIVE overlay kind over the frozen tool-output kind, same as the display does", () => {
  upsertSubagentRow("turn_live_sort", { rowKey: "r_a", kind: "running", task: "a", resultPreview: "" });
  upsertSubagentRow("turn_live_sort", { rowKey: "r_b", kind: "running", task: "b", resultPreview: "" });
  // r_b spawned SECOND, so a plain spawn-index sort would leave it behind
  // r_a - but its live overlay has already settled to "failed" while its
  // frozen kind is still "running", so it must sort ahead as the failed row
  // it now displays as.
  updateSubagentRowIfExists("turn_live_sort", "r_b", { liveKind: "failed" });
  const { result } = renderHook(() => useSubagentRows("turn_live_sort"));
  expect(result.current.map((r) => r.rowKey)).toEqual(["r_b", "r_a"]);
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

// --- upsertSubagentRow preserves every overlay field, not just liveKind ---

test("upsertSubagentRow preserves liveReason/resumable/exhaustion fields a delegate re-render never carries", () => {
  upsertSubagentRow("turn_ov", { rowKey: "job_1", kind: "running", task: "t", resultPreview: "" });
  updateSubagentRowIfExists("turn_ov", "job_1", {
    liveKind: "failed",
    liveReason: "exhausted budget",
    resumable: true,
    exhaustionBudget: "10m",
    exhaustionLimit: 20,
  });
  // A later incidental DelegateBody re-render re-upserts the SAME frozen data.
  upsertSubagentRow("turn_ov", { rowKey: "job_1", kind: "running", task: "t", resultPreview: "" });
  const { result } = renderHook(() => useSubagentRows("turn_ov"));
  expect(result.current[0]).toMatchObject({
    liveKind: "failed",
    liveReason: "exhausted budget",
    resumable: true,
    exhaustionBudget: "10m",
    exhaustionLimit: 20,
  });
});

// --- applySerfJobStarted / applySerfJobFinished (dr7e) --------------------

function job(overrides: Partial<SerfJobInfo> = {}): SerfJobInfo {
  return { jobId: "job_1", jobType: "delegate", status: "running", outputBytes: 0, ...overrides };
}

// A fixed test sessionRef - every scope-key-building call below routes
// through turnScopeKey exactly as production call sites do (kata 8525),
// so a test that only touched a bare turnId string before this fix would
// silently stop matching the row applySerfJobStarted/Finished actually
// patch (they always scope by sessionRef now).
const SESS = "sess_x";

test("applySerfJobStarted: no-op when the job carries no originTurnId (never fabricates a row)", () => {
  applySerfJobStarted(job(), SESS);
  const { result } = renderHook(() => useSubagentRows(turnScopeKey(SESS, "turn_never_seen")));
  expect(result.current).toEqual([]);
});

test("applySerfJobStarted: no-op when no row exists for the resolved rowKey (only patches an EXISTING row)", () => {
  applySerfJobStarted(job({ originTurnId: "turn_js1", delegateId: "dlg_1" }), SESS);
  const { result } = renderHook(() => useSubagentRows(turnScopeKey(SESS, "turn_js1")));
  expect(result.current).toEqual([]);
});

test("applySerfJobStarted: resets liveKind to running and clears prior terminal detail (delegate_send resume)", () => {
  const scope = turnScopeKey(SESS, "turn_js2");
  upsertSubagentRow(scope, { rowKey: "dlg:dlg_2", kind: "done", task: "t", resultPreview: "" });
  updateSubagentRowIfExists(scope, "dlg:dlg_2", {
    liveKind: "failed",
    liveReason: "exhausted",
    resumable: true,
    exhaustionBudget: "1m",
    exhaustionLimit: 5,
  });

  applySerfJobStarted(job({ originTurnId: "turn_js2", delegateId: "dlg_2", jobId: "job_2" }), SESS);

  const { result } = renderHook(() => useSubagentRows(scope));
  expect(result.current[0]).toMatchObject({
    liveKind: "running",
    liveReason: undefined,
    resumable: undefined,
    exhaustionBudget: undefined,
    exhaustionLimit: undefined,
  });
});

test("applySerfJobFinished: patches liveKind/liveReason/resumable/exhaustion from the notification", () => {
  const scope = turnScopeKey(SESS, "turn_jf1");
  upsertSubagentRow(scope, { rowKey: "dlg:dlg_3", kind: "running", task: "t", resultPreview: "" });

  applySerfJobFinished(
    job({
      originTurnId: "turn_jf1",
      delegateId: "dlg_3",
      status: "exhausted",
      reason: "ran out of turns",
      resumable: true,
      exhaustionBudget: "30m",
      exhaustionLimit: 60,
    }),
    SESS,
  );

  const { result } = renderHook(() => useSubagentRows(scope));
  expect(result.current[0]).toMatchObject({
    liveKind: "failed",
    liveReason: "ran out of turns",
    resumable: true,
    exhaustionBudget: "30m",
    exhaustionLimit: 60,
  });
});

test("applySerfJobFinished: rowKey resolution matches resolveRowKey (delegateId over jobId over originToolCallId)", () => {
  const scope = turnScopeKey(SESS, "turn_jf2");
  upsertSubagentRow(scope, { rowKey: "job:job_4", kind: "running", task: "t", resultPreview: "" });
  applySerfJobFinished(job({ originTurnId: "turn_jf2", jobId: "job_4", status: "completed" }), SESS);
  const { result } = renderHook(() => useSubagentRows(scope));
  expect(result.current[0]?.liveKind).toBe("done");
});

// --- kata 8525: cross-session isolation ------------------------------------

test("turnScopeKey: two different sessionRefs with the SAME turnId never produce the same key", () => {
  expect(turnScopeKey("session_a", "turn_1")).not.toBe(turnScopeKey("session_b", "turn_1"));
});

test("applySerfJobFinished: a job from one session never patches an existing row planted under the same turnId in a different session", () => {
  // Both sessions' first real turn lands on the identical "turn_1" string
  // (turn ids restart at 0 per session - internal/appprojector's own
  // nextTurn counter) - exactly the collision kata 8525 was reproduced with.
  const scopeA = turnScopeKey("session_a", "turn_1");
  const scopeB = turnScopeKey("session_b", "turn_1");
  upsertSubagentRow(scopeA, { rowKey: "dlg:dlg_a", kind: "running", task: "session A's own task", resultPreview: "" });
  upsertSubagentRow(scopeB, { rowKey: "dlg:dlg_b", kind: "running", task: "session B's own task", resultPreview: "" });

  applySerfJobFinished(job({ originTurnId: "turn_1", delegateId: "dlg_b", status: "completed" }), "session_b");

  const { result: rowsA } = renderHook(() => useSubagentRows(scopeA));
  const { result: rowsB } = renderHook(() => useSubagentRows(scopeB));
  expect(rowsA.current).toHaveLength(1);
  expect(rowsA.current[0]?.liveKind).toBeUndefined(); // session A's row untouched
  expect(rowsB.current).toHaveLength(1);
  expect(rowsB.current[0]?.liveKind).toBe("done");
});

// --- setWatchedLiveKind: the guard that keeps a lagging watch from --------
// --- resurrecting a row a serf/job/finished notification already settled -

test("setWatchedLiveKind: a 'running' write is suppressed once the row is already terminal", () => {
  upsertSubagentRow("turn_wg1", { rowKey: "job_1", kind: "running", task: "t", resultPreview: "" });
  updateSubagentRowIfExists("turn_wg1", "job_1", { liveKind: "done" });

  setWatchedLiveKind("turn_wg1", "job_1", "running");

  const { result } = renderHook(() => useSubagentRows("turn_wg1"));
  expect(result.current[0]?.liveKind).toBe("done");
});

test("setWatchedLiveKind: a terminal write still applies normally over a running row", () => {
  upsertSubagentRow("turn_wg2", { rowKey: "job_1", kind: "running", task: "t", resultPreview: "" });
  setWatchedLiveKind("turn_wg2", "job_1", "failed");
  const { result } = renderHook(() => useSubagentRows("turn_wg2"));
  expect(result.current[0]?.liveKind).toBe("failed");
});

test("setWatchedLiveKind: 'running' still applies when the row's kind isn't already terminal", () => {
  upsertSubagentRow("turn_wg3", { rowKey: "job_1", kind: "running", task: "t", resultPreview: "" });
  setWatchedLiveKind("turn_wg3", "job_1", "running");
  const { result } = renderHook(() => useSubagentRows("turn_wg3"));
  expect(result.current[0]?.liveKind).toBe("running");
});

test("setWatchedLiveKind: the frozen tool-output kind alone (no liveKind yet) also guards against a stale running write", () => {
  upsertSubagentRow("turn_wg4", { rowKey: "job_1", kind: "failed", task: "t", resultPreview: "" });
  setWatchedLiveKind("turn_wg4", "job_1", "running");
  const { result } = renderHook(() => useSubagentRows("turn_wg4"));
  expect(result.current[0]?.liveKind).toBeUndefined();
});

// 3zf8: "stopped" (a cancelled/deliberately-killed child) is terminal too -
// a lagging watch racing a job_stop must not resurrect the spinner any more
// than it may over a done/failed/unknown row.
test("setWatchedLiveKind: a stopped row also guards against a stale running write (stopped is terminal too)", () => {
  upsertSubagentRow("turn_wg5", { rowKey: "job_1", kind: "stopped", task: "t", resultPreview: "" });
  setWatchedLiveKind("turn_wg5", "job_1", "running");
  const { result } = renderHook(() => useSubagentRows("turn_wg5"));
  expect(result.current[0]?.liveKind).toBeUndefined();
});

// classifyJobStatus/resolveRowKey moved here from subagentModule.tsx (dr7e) -
// re-verify the re-export chain resolves to the SAME functions, since
// subagentModule.test.tsx already unit-tests their behavior in full via that
// re-export.
test("classifyJobStatus/resolveRowKey are exported directly from this module too", () => {
  expect(classifyJobStatus("completed")).toBe("done");
  expect(resolveRowKey("dlg_1", undefined, "call_1")).toBe("dlg:dlg_1");
});
