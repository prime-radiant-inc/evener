// Per-delegate presentation state shared by the delegate tool row, its card,
// and stable delegate notifications. Rows stay keyed by parent session/turn so
// follow-up tools and liveness can update the same delegate without coupling
// sibling ToolCallItems together for rendering.

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
  // Live and stable overlays survive re-renders of the frozen tool item.
  liveKind?: SubagentRowKind;
  liveReason?: string;
  resumable?: boolean;
  exhaustionBudget?: string;
  exhaustionLimit?: number;
  task: string;
  delegateId?: string;
  stable?: EvenerDelegateInfo;
  transcriptRef?: string;
  startedAt?: string;
  completedAt?: string;
  resultPreview: string;
}

interface ModuleStoreState {
  // turn scope -> rowKey -> row
  turnRowsByKey: Map<string, Map<string, SubagentRow>>;
  // sessionRef -> delegateId -> the latest stable projection seen for that
  // delegate (revision-fenced at write). Rows usually mutate through
  // applyEvenerDelegateUpdated's originTurnId fast path; this map is the
  // safety net for projections that arrive with no originTurnId (the stored
  // delegate descriptor records origin_tool_call_id, and older daemons never
  // recorded a turn id at all) or before the card's own row exists (a read
  // hydrates before the user scrolls the delegate turn into view; the row
  // materializes on mount). upsertSubagentRow consumes it at row creation.
  sessionProjections: Map<string, Map<string, EvenerDelegateInfo>>;
}

const moduleStore = createStore<ModuleStoreState>(() => ({
  turnRowsByKey: new Map(),
  sessionProjections: new Map(),
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

// effectiveRowKind is the kind a row actually displays: stable projection,
// then live child watch, then the frozen delegate tool output.
export function effectiveRowKind(row: SubagentRow): SubagentRowKind {
  if (row.stable) return classifyJobStatus(stableDelegateDisplayStatus(row.stable));
  return row.liveKind ?? row.kind;
}

// The delegate call creates a row; later renders and follow-up events update it
// in place. A call-keyed placeholder may migrate to its durable delegate key.
export function upsertSubagentRow(scopeKey: string, row: SubagentRow, migrateFromRowKey?: string): void {
  moduleStore.setState((s) => {
    const existingForTurn = s.turnRowsByKey.get(scopeKey);
    const rows = new Map(existingForTurn ?? []);
    const existingRow = rows.get(row.rowKey) ?? (migrateFromRowKey ? rows.get(migrateFromRowKey) : undefined);
    if (migrateFromRowKey && migrateFromRowKey !== row.rowKey) rows.delete(migrateFromRowKey);
    let next: SubagentRow = {
      liveKind: existingRow?.liveKind,
      liveReason: existingRow?.liveReason,
      resumable: existingRow?.resumable,
      exhaustionBudget: existingRow?.exhaustionBudget,
      exhaustionLimit: existingRow?.exhaustionLimit,
      stable: existingRow?.stable,
      ...row,
    };
    // A row with no stable state yet picks up this session's stashed
    // projection for the same delegate, if one landed first (the read path
    // hydrates before VirtualList ever mounts the delegate's card).
    if (!next.stable && next.delegateId) {
      const pending = s.sessionProjections.get(sessionRefOfScopeKey(scopeKey))?.get(next.delegateId);
      if (pending) {
        next = { ...next, ...inputFromStable(next.rowKey, pending, next) };
      }
    }
    rows.set(row.rowKey, next);

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

// Follow-up tools may patch a delegate but never create one.
export function updateSubagentRowIfExists(scopeKey: string, rowKey: string, patch: Partial<SubagentRow>): void {
  moduleStore.setState((s) => {
    const existingForTurn = s.turnRowsByKey.get(scopeKey);
    const existingRow = existingForTurn?.get(rowKey);
    if (!existingForTurn || !existingRow) return s;
    const rows = new Map(existingForTurn);
    rows.set(rowKey, { ...existingRow, ...patch });
    const turnRowsByKey = new Map(s.turnRowsByKey);
    turnRowsByKey.set(scopeKey, rows);
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

function cloneStableDelegate(delegate: EvenerDelegateInfo): EvenerDelegateInfo {
  const { waitIgnoredReason: _callScoped, ...stable } = delegate as EvenerDelegateInfo & {
    waitIgnoredReason?: unknown;
  };
  return {
    ...stable,
    warnings: stable.warnings ? [...stable.warnings] : undefined,
    diagnostics: stable.diagnostics ? [...stable.diagnostics] : undefined,
    usage: stable.usage ? { ...stable.usage } : undefined,
    worktree: stable.worktree ? { ...stable.worktree } : undefined,
  };
}

function mergeLatestActivity(current: string | undefined, incoming: string | undefined): string | undefined {
  if (!incoming) return current;
  if (!current) return incoming;
  const currentMillis = Date.parse(current);
  const incomingMillis = Date.parse(incoming);
  if (Number.isNaN(incomingMillis)) return current;
  return Number.isNaN(currentMillis) || incomingMillis > currentMillis ? incoming : current;
}

function mergeStableDelegate(current: EvenerDelegateInfo, incoming: EvenerDelegateInfo): EvenerDelegateInfo {
  const stable = incoming.projectionRevision > current.projectionRevision ? cloneStableDelegate(incoming) : current;
  const latestActivityAt = mergeLatestActivity(current.latestActivityAt, incoming.latestActivityAt);
  return latestActivityAt === stable.latestActivityAt ? stable : { ...stable, latestActivityAt };
}

// inputFromStable builds the row payload a stable projection carries. Shared
// by the originTurnId fast path and the no-originTurnId scan/stash path so
// both land the identical fields.
function inputFromStable(rowKey: string, stable: EvenerDelegateInfo, existing?: SubagentRow): SubagentRow {
  const resumable = stable.exhaustionResumable ?? stable.resumable;
  return {
    rowKey,
    kind: classifyJobStatus(stableDelegateDisplayStatus(stable)),
    delegateId: stable.delegateId,
    stable,
    task: stable.task ?? stable.description ?? existing?.task ?? "",
    transcriptRef: stable.transcriptRef || existing?.transcriptRef,
    startedAt: stable.runStartedAt,
    completedAt: stable.runEndedAt,
    resultPreview: stable.reason ?? existing?.resultPreview ?? "",
    liveReason: stable.reason,
    resumable,
    exhaustionBudget: stable.exhaustionBudget,
    exhaustionLimit: stable.exhaustionLimit,
  };
}

// sessionRefOfScopeKey splits a turnScopeKey back to its sessionRef ("" when
// the key was built without one).
function sessionRefOfScopeKey(scopeKey: string): string {
  return scopeKey.split("\0")[0] ?? "";
}

// A snapshot may arrive before virtualization mounts the originating item.
export function applyEvenerDelegateUpdated(delegate: EvenerDelegateInfo, sessionRef: string | undefined): void {
  if (!delegate.delegateId) return;
  const delegateId = delegate.delegateId;

  // Every projection - with or without an origin turn - is stashed per
  // session, revision-fenced, so a row that materializes later (the card
  // mounts on scroll, after the read hydrated) still picks it up.
  moduleStore.setState((s) => {
    const bySession = new Map(s.sessionProjections);
    const sessionKey = sessionRef ?? "";
    const byDelegate = new Map(bySession.get(sessionKey) ?? []);
    const current = byDelegate.get(delegateId);
    byDelegate.set(delegateId, current ? mergeStableDelegate(current, delegate) : cloneStableDelegate(delegate));
    bySession.set(sessionKey, byDelegate);
    return { sessionProjections: bySession };
  });
  const stable = moduleStore
    .getState()
    .sessionProjections.get(sessionRef ?? "")
    ?.get(delegateId);
  if (!stable) return;

  if (delegate.originTurnId) {
    const scopeKey = turnScopeKey(sessionRef, delegate.originTurnId);
    const rowKey = resolveRowKey(delegateId, undefined, delegate.originToolCallId ?? delegate.originItemId ?? "");
    const existing = moduleStore.getState().turnRowsByKey.get(scopeKey)?.get(rowKey);
    upsertSubagentRow(scopeKey, inputFromStable(rowKey, stable, existing));
    return;
  }

  // No origin turn id: patch every row this session already shows for this
  // delegate (a card mounted before the read landed). Rows mounted later
  // consume the stash in upsertSubagentRow instead.
  const prefix = `${sessionRef ?? ""}\0`;
  const rowKey = `dlg:${delegateId}`;
  for (const [scopeKey, rows] of moduleStore.getState().turnRowsByKey) {
    if (!scopeKey.startsWith(prefix)) continue;
    const existing = rows.get(rowKey);
    if (existing) updateSubagentRowIfExists(scopeKey, rowKey, inputFromStable(rowKey, stable, existing));
  }
}

const TERMINAL_KINDS: ReadonlySet<SubagentRowKind> = new Set(["done", "stopped", "failed", "unknown"]);

// A lagging child watch cannot resurrect a terminal stable projection.
export function setWatchedLiveKind(scopeKey: string, rowKey: string, liveKind: SubagentRowKind): void {
  if (liveKind === "running") {
    const existingRow = moduleStore.getState().turnRowsByKey.get(scopeKey)?.get(rowKey);
    if (existingRow && TERMINAL_KINDS.has(effectiveRowKind(existingRow))) return;
  }
  updateSubagentRowIfExists(scopeKey, rowKey, { liveKind });
}

export function useSubagentRow(scopeKey: string, rowKey: string): SubagentRow | undefined {
  return useStore(moduleStore, (s) => s.turnRowsByKey.get(scopeKey)?.get(rowKey));
}

export function useRunningSubagentCount(scopeKey: string | undefined): number {
  return useStore(moduleStore, (s) => {
    if (scopeKey === undefined) return 0;
    let count = 0;
    for (const row of s.turnRowsByKey.get(scopeKey)?.values() ?? []) {
      if (effectiveRowKind(row) === "running") count++;
    }
    return count;
  });
}

// Parent-session rows and pre-mount projections share the parent pane's
// lifetime. Dropping both prevents virtualized historical rows from turning
// the page-lifetime store into an unbounded session cache.
export function releaseSubagentSession(sessionRef: string): void {
  moduleStore.setState((s) => {
    const sessionProjections = new Map(s.sessionProjections);
    sessionProjections.delete(sessionRef);
    const turnRowsByKey = new Map(s.turnRowsByKey);
    const prefix = `${sessionRef}\0`;
    for (const scopeKey of turnRowsByKey.keys()) {
      if (scopeKey.startsWith(prefix)) turnRowsByKey.delete(scopeKey);
    }
    return { sessionProjections, turnRowsByKey };
  });
}

// resetSubagentModuleStoreForTests clears every module-private field this
// singleton store holds. subagentModuleStore.ts is a page-lifetime
// singleton (same precedent as stores/threads.ts), so tests that exercise
// it directly must reset between cases to stay isolated; no production
// code should call this.
export function resetSubagentModuleStoreForTests(): void {
  moduleStore.setState({
    turnRowsByKey: new Map(),
    sessionProjections: new Map(),
  });
}
