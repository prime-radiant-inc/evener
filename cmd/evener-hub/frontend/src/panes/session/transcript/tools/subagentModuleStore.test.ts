import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel } from "../../../../protocol/model";
import type { EvenerDelegateInfo } from "../../../../protocol/types.gen";
import {
  applyEvenerDelegateUpdated,
  itemScopeKey,
  resetSubagentModuleStoreForTests,
  rowKeyForDelegateItem,
  setWatchedLiveKind,
  turnScopeKey,
  updateSubagentRowIfExists,
  upsertSubagentRow,
  useSubagentRow,
} from "./subagentModuleStore";

afterEach(resetSubagentModuleStoreForTests);

function stableDelegate(overrides: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo {
  return {
    delegateId: "dlg_1",
    ownerSessionId: "sess_owner",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    transcriptRef: "local:sess_child",
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    resumable: true,
    projectionRevision: 1,
    originTurnId: "turn_1",
    task: "inspect the repository",
    latestActivityAt: "2026-08-15T10:00:00Z",
    ...overrides,
  };
}

test("delegate rows use delegate_id and never promote an activation job_id", () => {
  const item = (output: Record<string, unknown>, id: string): ItemModel => ({
    id,
    callId: `call_${id}`,
    turnId: "turn_1",
    type: "commandExecution",
    toolName: "delegate",
    text: "",
    output: JSON.stringify(output),
  });

  expect(rowKeyForDelegateItem(item({ delegate_id: "dlg_stable", status: "running" }, "stable"))).toBe(
    "dlg:dlg_stable",
  );
  expect(rowKeyForDelegateItem(item({ job_id: "job_activation", status: "running" }, "activation"))).toBe(
    "call:call_activation",
  );
});

test("a row updates in place and follow-up tools cannot fabricate a missing row", () => {
  const scope = turnScopeKey("ref_parent", "turn_1");
  upsertSubagentRow(scope, { rowKey: "dlg:1", kind: "running", task: "run tests", resultPreview: "" });
  const { result } = renderHook(() => useSubagentRow(scope, "dlg:1"));

  act(() => updateSubagentRowIfExists(scope, "dlg:1", { kind: "failed", resultPreview: "build error" }));
  expect(result.current).toMatchObject({ kind: "failed", resultPreview: "build error", task: "run tests" });

  act(() => updateSubagentRowIfExists(scope, "dlg:missing", { kind: "done" }));
  const { result: missing } = renderHook(() => useSubagentRow(scope, "dlg:missing"));
  expect(missing.current).toBeUndefined();
});

test("a delegate re-render preserves live and stable overlay fields", () => {
  const scope = turnScopeKey("ref_parent", "turn_1");
  const frozen = { rowKey: "dlg:1", kind: "running" as const, task: "task", resultPreview: "" };
  upsertSubagentRow(scope, frozen);
  updateSubagentRowIfExists(scope, "dlg:1", {
    liveKind: "failed",
    liveReason: "exhausted budget",
    resumable: true,
    exhaustionBudget: "10m",
    exhaustionLimit: 20,
  });
  upsertSubagentRow(scope, frozen);

  const { result } = renderHook(() => useSubagentRow(scope, "dlg:1"));
  expect(result.current).toMatchObject({
    liveKind: "failed",
    liveReason: "exhausted budget",
    resumable: true,
    exhaustionBudget: "10m",
    exhaustionLimit: 20,
  });
});

test("stable delegate updates create and revision-fence their exact row", () => {
  const scope = turnScopeKey("ref_root", "turn_1");
  applyEvenerDelegateUpdated(
    stableDelegate({
      projectionRevision: 3,
      lifecycle: "idle",
      phase: "idle",
      status: "idle",
      outcome: "completed",
      terminal: true,
    }),
    "ref_root",
  );
  applyEvenerDelegateUpdated(
    stableDelegate({ projectionRevision: 2, latestActivityAt: "2026-08-15T10:01:00Z" }),
    "ref_root",
  );

  const { result } = renderHook(() => useSubagentRow(scope, "dlg:dlg_1"));
  expect(result.current?.kind).toBe("done");
  expect(result.current?.stable).toMatchObject({
    projectionRevision: 3,
    outcome: "completed",
    latestActivityAt: "2026-08-15T10:01:00Z",
  });
});

test("a projection without an origin turn hydrates its row when virtualization mounts it later", () => {
  applyEvenerDelegateUpdated(stableDelegate({ originTurnId: undefined, projectionRevision: 7 }), "ref_root");
  const scope = turnScopeKey("ref_root", "turn_late");
  const { result } = renderHook(() => useSubagentRow(scope, "dlg:dlg_1"));
  expect(result.current).toBeUndefined();

  act(() =>
    upsertSubagentRow(scope, {
      rowKey: "dlg:dlg_1",
      kind: "running",
      task: "mounted late",
      resultPreview: "",
      delegateId: "dlg_1",
    }),
  );
  expect(result.current?.stable?.projectionRevision).toBe(7);
});

test("a lagging running watch cannot resurrect a terminal row", () => {
  const scope = turnScopeKey("ref_root", "turn_1");
  upsertSubagentRow(scope, { rowKey: "dlg:1", kind: "done", task: "task", resultPreview: "" });
  setWatchedLiveKind(scope, "dlg:1", "running");

  const { result } = renderHook(() => useSubagentRow(scope, "dlg:1"));
  expect(result.current?.liveKind).toBeUndefined();
});

test("session-scoped disclosure and turn keys do not collide", () => {
  expect(turnScopeKey("session_a", "turn_1")).not.toBe(turnScopeKey("session_b", "turn_1"));
  expect(itemScopeKey("session_a", "item_1")).not.toBe(itemScopeKey("session_b", "item_1"));
});
