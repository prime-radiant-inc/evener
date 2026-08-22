// Per-delegate presentation state shared by the tool row and its card.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ItemModel } from "../../../../protocol/model";
import { stableDelegateDisplayStatus } from "../../../../protocol/stableDelegate";
import type { EvenerDelegateInfo } from "../../../../protocol/types.gen";
import { parseArgs, parseJSONObject, str } from "./helpers";

export type SubagentRowKind = "running" | "done" | "stopped" | "failed" | "unknown";

export interface SubagentRow {
  rowKey: string;
  kind: SubagentRowKind;
  resumable?: boolean;
  exhaustionBudget?: string;
  exhaustionLimit?: number;
  delegateId?: string;
  transcriptRef?: string;
  startedAt?: string;
  completedAt?: string;
  resultPreview: string;
}

interface ModuleStoreState {
  turnRowsByKey: Map<string, Map<string, SubagentRow>>;
}

const moduleStore = createStore<ModuleStoreState>(() => ({
  turnRowsByKey: new Map(),
}));

// Turn ids restart per session; NUL keeps the pair collision-free.
export function turnScopeKey(sessionRef: string | undefined, turnId: string): string {
  return `${sessionRef ?? ""}\0${turnId}`;
}

export function itemScopeKey(sessionRef: string | undefined, itemId: string): string {
  return `${sessionRef ?? ""}\0${itemId}`;
}

// Mirrors rowFromDelegateItem so the tool-row status targets its own card.
export function rowKeyForDelegateItem(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  const parsed = parseJSONObject(item.output);
  const delegateId = str(parsed ?? args, "delegate_id");
  return resolveRowKey(delegateId, undefined, item.callId ?? item.id);
}

// The stable projection owns lifecycle after hydration. Frozen tool output is
// the pre-hydration fallback.
export function effectiveRowKind(row: SubagentRow, stable?: EvenerDelegateInfo): SubagentRowKind {
  if (stable) return classifyJobStatus(stableDelegateDisplayStatus(stable));
  return row.kind;
}

// The delegate call creates a row; later renders update it in place. A
// call-keyed placeholder may migrate to its durable delegate key.
export function upsertSubagentRow(scopeKey: string, row: SubagentRow, migrateFromRowKey?: string): void {
  moduleStore.setState((s) => {
    const rows = new Map(s.turnRowsByKey.get(scopeKey) ?? []);
    if (migrateFromRowKey && migrateFromRowKey !== row.rowKey) rows.delete(migrateFromRowKey);
    rows.set(row.rowKey, row);

    const turnRowsByKey = new Map(s.turnRowsByKey);
    turnRowsByKey.set(scopeKey, rows);
    return { turnRowsByKey };
  });
}

export function removeSubagentRow(scopeKey: string, rowKey: string): void {
  moduleStore.setState((state) => {
    const existing = state.turnRowsByKey.get(scopeKey);
    if (!existing?.has(rowKey)) return state;
    const rows = new Map(existing);
    rows.delete(rowKey);
    const turnRowsByKey = new Map(state.turnRowsByKey);
    if (rows.size === 0) {
      turnRowsByKey.delete(scopeKey);
    } else {
      turnRowsByKey.set(scopeKey, rows);
    }
    return { turnRowsByKey };
  });
}

// A deliberate stop is terminal but distinct from success or failure.
export function classifyJobStatus(status: string | undefined): SubagentRowKind {
  if (status === undefined) return "running";
  if (["failed", "errored", "error", "exhausted"].includes(status)) return "failed";
  if (["cancelled", "stopped"].includes(status)) return "stopped";
  if (["completed", "done", "succeeded"].includes(status)) return "done";
  if (status === "unknown") return "unknown";
  return "running";
}

// Prefixes prevent equal raw ids from different identity classes colliding.
export function resolveRowKey(delegateId: string | undefined, jobId: string | undefined, fallback: string): string {
  if (delegateId) return `dlg:${delegateId}`;
  if (jobId) return `job:${jobId}`;
  return `call:${fallback}`;
}

export function useSubagentRow(scopeKey: string, rowKey: string): SubagentRow | undefined {
  return useStore(moduleStore, (s) => s.turnRowsByKey.get(scopeKey)?.get(rowKey));
}

export function useRunningSubagentCount(scopeKey: string | undefined, delegates?: EvenerDelegateInfo[]): number {
  return useStore(moduleStore, (s) => {
    if (scopeKey === undefined) return 0;
    let count = 0;
    for (const row of s.turnRowsByKey.get(scopeKey)?.values() ?? []) {
      const stable = row.delegateId ? delegates?.find((delegate) => delegate.delegateId === row.delegateId) : undefined;
      if (effectiveRowKind(row, stable) === "running") count++;
    }
    return count;
  });
}

export function releaseSubagentRows(sessionRef: string): void {
  moduleStore.setState((state) => {
    const prefix = `${sessionRef}\0`;
    const turnRowsByKey = new Map(state.turnRowsByKey);
    let changed = false;
    for (const scopeKey of turnRowsByKey.keys()) {
      if (!scopeKey.startsWith(prefix)) continue;
      turnRowsByKey.delete(scopeKey);
      changed = true;
    }
    return changed ? { turnRowsByKey } : state;
  });
}

export function resetSubagentModuleStoreForTests(): void {
  moduleStore.setState({
    turnRowsByKey: new Map(),
  });
}
