// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import {
  classifyJobStatus,
  removeSubagentRow,
  resetSubagentModuleStoreForTests,
  resolveRowKey,
  type SubagentRow,
  turnScopeKey,
  upsertSubagentRow,
  useSubagentRow,
} from "./subagentModuleStore";

afterEach(() => {
  cleanup();
  resetSubagentModuleStoreForTests();
});

function makeRow(rowKey: string, overrides: Partial<SubagentRow> = {}): SubagentRow {
  return {
    rowKey,
    kind: "running",
    resultPreview: "",
    ...overrides,
  };
}

test("removeSubagentRow removes only the requested row", () => {
  const scopeKey = turnScopeKey("session_a", "turn_1");
  const first = renderHook(() => useSubagentRow(scopeKey, "dlg:first"));
  const second = renderHook(() => useSubagentRow(scopeKey, "dlg:second"));

  act(() => {
    upsertSubagentRow(scopeKey, makeRow("dlg:first", { resultPreview: "first" }));
    upsertSubagentRow(scopeKey, makeRow("dlg:second", { resultPreview: "second" }));
  });
  expect(first.result.current?.resultPreview).toBe("first");
  expect(second.result.current?.resultPreview).toBe("second");

  act(() => removeSubagentRow(scopeKey, "dlg:first"));
  expect(first.result.current).toBeUndefined();
  expect(second.result.current).toEqual(makeRow("dlg:second", { resultPreview: "second" }));
});

test("removing an unknown row preserves the existing row", () => {
  const scopeKey = turnScopeKey("session_a", "turn_1");
  const existing = renderHook(() => useSubagentRow(scopeKey, "dlg:existing"));
  const row = makeRow("dlg:existing", { kind: "done", resultPreview: "complete" });

  act(() => upsertSubagentRow(scopeKey, row));
  act(() => removeSubagentRow(scopeKey, "dlg:missing"));

  expect(existing.result.current).toBe(row);
});

test("classifyJobStatus maps protocol synonyms to presentation states", () => {
  expect(classifyJobStatus(undefined)).toBe("running");
  expect(classifyJobStatus("failed")).toBe("failed");
  expect(classifyJobStatus("errored")).toBe("failed");
  expect(classifyJobStatus("error")).toBe("failed");
  expect(classifyJobStatus("exhausted")).toBe("failed");
  expect(classifyJobStatus("cancelled")).toBe("stopped");
  expect(classifyJobStatus("stopped")).toBe("stopped");
  expect(classifyJobStatus("completed")).toBe("done");
  expect(classifyJobStatus("done")).toBe("done");
  expect(classifyJobStatus("succeeded")).toBe("done");
  expect(classifyJobStatus("unknown")).toBe("unknown");
  expect(classifyJobStatus("running")).toBe("running");
  expect(classifyJobStatus("")).toBe("running");
});

test("resolveRowKey gives durable identities precedence over call identities", () => {
  expect(resolveRowKey("dlg_1", undefined, "fallback")).toBe("dlg:dlg_1");
  expect(resolveRowKey(undefined, "job_1", "fallback")).toBe("job:job_1");
  expect(resolveRowKey(undefined, undefined, "call_1")).toBe("call:call_1");
  expect(resolveRowKey("dlg_1", "job_1", "call_1")).toBe("dlg:dlg_1");
});

test("upsertSubagentRow migrates a call-keyed placeholder to its durable delegate key", () => {
  const scopeKey = turnScopeKey("session_a", "turn_1");
  const placeholder = renderHook(() => useSubagentRow(scopeKey, "call:call_1"));
  const durable = renderHook(() => useSubagentRow(scopeKey, "dlg:dlg_1"));

  act(() => upsertSubagentRow(scopeKey, makeRow("call:call_1", { resultPreview: "starting" })));
  act(() =>
    upsertSubagentRow(scopeKey, makeRow("dlg:dlg_1", { delegateId: "dlg_1", resultPreview: "running" }), "call:call_1"),
  );

  expect(placeholder.result.current).toBeUndefined();
  expect(durable.result.current).toEqual(makeRow("dlg:dlg_1", { delegateId: "dlg_1", resultPreview: "running" }));
});
