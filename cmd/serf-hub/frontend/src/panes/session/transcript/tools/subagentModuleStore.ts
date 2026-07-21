// subagentModuleStore is the cross-item aggregation side-channel the
// subagent module needs but the locked interfaces don't provide.
// ToolRenderProps is deliberately {item, live} only (no turn, no sibling
// items - confirmed by reading ToolCallItem.tsx: it receives the full
// ItemRenderProps, turn included, but its own destructuring drops `turn`
// before ever calling a descriptor's body), so a body component genuinely
// cannot see its siblings through props. This store is a same-directory
// workaround, mirroring stores/threads.ts's own createStore/useStore
// idiom: every job_*/delegate row computes its OWN data from its OWN item
// (never reaching outside tools/**) and upserts it here keyed by
// (turnId, rowKey); exactly one row per turnId - the first to mount, via a
// lazy claimLeader() call - renders the aggregated module chrome
// (subagentModule.tsx's SubagentModule), reading every row back out
// reactively through useSubagentRows. Rows/leadership are irrelevant once
// nothing observes a turnId anymore; this intentionally does not evict
// entries (turns.length only grows for a session's lifetime in the
// threads store too - same trade-off, not a new one).

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export type SubagentRowKind = "running" | "done" | "failed" | "unknown";

export interface SubagentRow {
  rowKey: string;
  spawnIndex: number;
  kind: SubagentRowKind;
  task: string;
  jobId?: string;
  transcriptRef?: string;
  startedAt?: string;
  completedAt?: string;
  resultPreview: string;
}

export type SubagentRowInput = Omit<SubagentRow, "spawnIndex">;

interface ModuleStoreState {
  // turnId -> rowKey -> row (source of truth, O(1) upsert/lookup)
  turnRowsByKey: Map<string, Map<string, SubagentRow>>;
  // turnId -> that turn's rows as a spawn-ordered array - maintained
  // alongside turnRowsByKey so useSubagentRows can return a STABLE
  // reference between actual mutations. useStore ultimately rides
  // useSyncExternalStore, which requires a selector to return a
  // referentially-stable snapshot when nothing changed; a selector that
  // freshly Array.from()+sort()s on every call returns a new array every
  // render, which useSyncExternalStore reads as "changed every time" and
  // enters an infinite re-render loop (confirmed live: an earlier version
  // of this store did exactly that, thrown by React itself in this very
  // test file). Precomputing the array here, once per real mutation, is
  // the fix - same tactic threads.ts's own frameTimes already uses
  // (returns the Map's stored array reference directly, never a fresh
  // derivation).
  turnRowsSorted: Map<string, SubagentRow[]>;
  // turnId -> next spawn index to assign a brand-new row
  turnNextSpawnIndex: Map<string, number>;
  // turnId -> the item id currently holding "leader" status for that turn
  turnLeader: Map<string, string>;
}

const moduleStore = createStore<ModuleStoreState>(() => ({
  turnRowsByKey: new Map(),
  turnRowsSorted: new Map(),
  turnNextSpawnIndex: new Map(),
  turnLeader: new Map(),
}));

const EMPTY_ROWS: SubagentRow[] = [];

function sortedRows(rows: Map<string, SubagentRow>): SubagentRow[] {
  return Array.from(rows.values()).sort((a, b) => a.spawnIndex - b.spawnIndex);
}

// upsertSubagentRow creates a row on first sight of `rowKey` (assigning it
// the next spawn index for this turn, so display order is fixed at
// first-seen order per parity §12's "fixed by spawn order" rule) or
// updates it in place on every later call - used by `delegate` itself
// (subagentModule.tsx), the one tool in this family allowed to spawn a
// fresh row.
export function upsertSubagentRow(turnId: string, row: SubagentRowInput): void {
  moduleStore.setState((s) => {
    const existingForTurn = s.turnRowsByKey.get(turnId);
    const rows = new Map(existingForTurn ?? []);
    const existingRow = rows.get(row.rowKey);
    const nextIndexBefore = s.turnNextSpawnIndex.get(turnId) ?? 0;
    const spawnIndex = existingRow?.spawnIndex ?? nextIndexBefore;
    rows.set(row.rowKey, { ...row, spawnIndex });

    const turnRowsByKey = new Map(s.turnRowsByKey);
    turnRowsByKey.set(turnId, rows);
    const turnRowsSorted = new Map(s.turnRowsSorted);
    turnRowsSorted.set(turnId, sortedRows(rows));
    const turnNextSpawnIndex = new Map(s.turnNextSpawnIndex);
    turnNextSpawnIndex.set(turnId, existingRow ? nextIndexBefore : nextIndexBefore + 1);
    return { turnRowsByKey, turnRowsSorted, turnNextSpawnIndex };
  });
}

// updateSubagentRowIfExists patches an EXISTING row only - used by
// job_status/job_stop/delegate_send, which check on/message an already-
// spawned child and must never fabricate a row of their own (mirrors the
// legacy reconcileSubagent's own rule: "only ever updates an existing row
// - it never spawns a new one from a read/list/message call"). A rowKey
// with no existing row (no `delegate` call seen yet this turn, or none at
// all) is a silent no-op.
export function updateSubagentRowIfExists(turnId: string, rowKey: string, patch: Partial<SubagentRowInput>): void {
  moduleStore.setState((s) => {
    const existingForTurn = s.turnRowsByKey.get(turnId);
    const existingRow = existingForTurn?.get(rowKey);
    if (!existingForTurn || !existingRow) return s;
    const rows = new Map(existingForTurn);
    rows.set(rowKey, { ...existingRow, ...patch });
    const turnRowsByKey = new Map(s.turnRowsByKey);
    turnRowsByKey.set(turnId, rows);
    const turnRowsSorted = new Map(s.turnRowsSorted);
    turnRowsSorted.set(turnId, sortedRows(rows));
    return { turnRowsByKey, turnRowsSorted };
  });
}

// useSubagentRows reactively selects every row tracked for `turnId`,
// ordered by spawn index (never by update recency - a row that just
// changed status must not visually jump to a different position). Reads
// the precomputed array directly - see turnRowsSorted's own doc comment
// for why this must not re-derive/re-sort inline.
export function useSubagentRows(turnId: string): SubagentRow[] {
  return useStore(moduleStore, (s) => s.turnRowsSorted.get(turnId) ?? EMPTY_ROWS);
}

// claimLeader is a plain (non-reactive) function, meant to be called from
// a component's lazy useState initializer - it runs once per mount,
// before paint, and the result never changes for that component instance's
// lifetime. Returns true for whichever item id claims (or already holds)
// leadership for `turnId`; false for every other item. Idempotent: the
// current leader re-claiming its own slot stays true.
export function claimLeader(turnId: string, itemId: string): boolean {
  const current = moduleStore.getState().turnLeader.get(turnId);
  if (current === undefined) {
    moduleStore.setState((s) => {
      const turnLeader = new Map(s.turnLeader);
      turnLeader.set(turnId, itemId);
      return { turnLeader };
    });
    return true;
  }
  return current === itemId;
}

// releaseLeader frees the leader slot ONLY when `itemId` is the current
// leader (a stale release from a non-leader - e.g. a follower unmounting -
// must never clear the real leader's claim). Called from the leader's own
// cleanup effect so a remount (VirtualList windowing a turn back into
// view) can re-elect cleanly instead of leaving a permanently-stale claim
// for an item id that no longer exists.
export function releaseLeader(turnId: string, itemId: string): void {
  const state = moduleStore.getState();
  if (state.turnLeader.get(turnId) !== itemId) return;
  moduleStore.setState((s) => {
    const turnLeader = new Map(s.turnLeader);
    turnLeader.delete(turnId);
    return { turnLeader };
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
    turnRowsSorted: new Map(),
    turnNextSpawnIndex: new Map(),
    turnLeader: new Map(),
  });
}
