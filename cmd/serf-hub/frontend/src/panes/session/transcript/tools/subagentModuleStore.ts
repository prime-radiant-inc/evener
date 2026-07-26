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
import type { SerfJobInfo } from "../../../../protocol/types.gen";

export type SubagentRowKind = "running" | "done" | "failed" | "unknown";

export interface SubagentRow {
  rowKey: string;
  spawnIndex: number;
  kind: SubagentRowKind;
  // Live-status overlay written back by either watchedChild.tsx's watch
  // (yd16) or a serf/job/started|finished notification (dr7e - see
  // applySerfJobStarted/applySerfJobFinished below). The pill prefers this
  // over the frozen tool-output `kind`. OVERLAY-OWNED: the delegate upsert
  // must never clobber it (see upsertSubagentRow), since DelegateBody
  // re-upserts the frozen output on every incidental render.
  liveKind?: SubagentRowKind;
  // liveReason/resumable/exhaustionBudget/exhaustionLimit: detail a
  // serf/job/finished notification carries that the delegate tool call's own
  // frozen output never does (appwire.SerfJobInfo - dr7e). Same
  // OVERLAY-OWNED rule as liveKind: preserved across upsertSubagentRow, only
  // ever written by applySerfJobStarted/applySerfJobFinished.
  liveReason?: string;
  resumable?: boolean;
  exhaustionBudget?: string;
  exhaustionLimit?: number;
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
    // Preserve every overlay field: DelegateBody re-upserts the frozen tool
    // output on every incidental render and must never wipe the live status
    // detail watchedChild.tsx (yd16) or a serf/job/started|finished
    // notification (dr7e) wrote. An explicit value in `row` still wins (the
    // spread lands after), but the delegate input never carries any of these -
    // rowFromDelegateItem only ever derives kind/task/jobId/transcriptRef/
    // startedAt/completedAt/resultPreview from the tool call's own output.
    rows.set(row.rowKey, {
      liveKind: existingRow?.liveKind,
      liveReason: existingRow?.liveReason,
      resumable: existingRow?.resumable,
      exhaustionBudget: existingRow?.exhaustionBudget,
      exhaustionLimit: existingRow?.exhaustionLimit,
      ...row,
      spawnIndex,
    });

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

// classifyJobStatus mirrors renderer.js's classifyJobStatus (parity §12):
// note cancelled/stopped land in "done", not "failed". Any status this
// codebase hasn't seen yet (including an absent one, e.g. the delegate
// call settled but the child hasn't reported a status field at all)
// degrades to "running" rather than a confusing/alarming "unknown" - an
// honest "still don't know anything bad happened" default. Lives here (not
// subagentModule.tsx) so applySerfJobFinished below and stores/threads.ts
// can both reach it without a core store importing a React/UI file -
// subagentModule.tsx re-exports it for its own existing callers.
export function classifyJobStatus(status: string | undefined): SubagentRowKind {
  if (status === undefined) return "running";
  if (["failed", "errored", "error", "exhausted"].includes(status)) return "failed";
  if (["completed", "done", "cancelled", "stopped", "succeeded"].includes(status)) return "done";
  if (status === "unknown") return "unknown";
  return "running";
}

// resolveRowKey prefers delegateId (stable across a delegate's whole
// lifetime, including across several jobs it may run in sequence), then
// jobId (a shell job has no delegateId at all), then a fallback (the
// originating call's own id) so every call still gets SOME row even
// before any id is known. Prefixed per kind so a delegate id can never
// collide with an unrelated job id that happens to share the same raw
// string. Same layering reason as classifyJobStatus above.
export function resolveRowKey(delegateId: string | undefined, jobId: string | undefined, fallback: string): string {
  if (delegateId) return `dlg:${delegateId}`;
  if (jobId) return `job:${jobId}`;
  return `call:${fallback}`;
}

const TERMINAL_KINDS: ReadonlySet<SubagentRowKind> = new Set(["done", "failed", "unknown"]);

// setWatchedLiveKind is WatchedChildIndicator's own guarded writer (dr7e).
// The child's own thread-status subscription can lag the parent's
// authoritative serf/job/finished notification (applySerfJobFinished
// below): once that notification has already settled a row into a terminal
// kind, a slower watch catching up to a stale "running" read must not
// resurrect the spinner. A terminal reading from the watch (the child
// socket itself closing, say) still applies normally - only a "running"
// write is ever suppressed, and only when the row is already terminal.
export function setWatchedLiveKind(turnId: string, rowKey: string, liveKind: SubagentRowKind): void {
  if (liveKind === "running") {
    const existingRow = moduleStore.getState().turnRowsByKey.get(turnId)?.get(rowKey);
    if (existingRow && TERMINAL_KINDS.has(existingRow.liveKind ?? existingRow.kind)) return;
  }
  updateSubagentRowIfExists(turnId, rowKey, { liveKind });
}

// applySerfJobStarted / applySerfJobFinished are the "Signal merging" step
// from docs/superpowers/specs/2026-06-25-subagent-run-rendering-design.md
// (dr7e): serf/job/started|finished notifications carry the same
// (originTurnId, delegateId/jobId) linkage a delegate item's own frozen tool
// output does, so they route through resolveRowKey exactly like
// rowFromDelegateItem (subagentModule.tsx) does, and patch an EXISTING row
// only - never spawn one, the same "only `delegate` spawns a row" rule
// updateSubagentRowIfExists already enforces for job_status/job_stop/
// delegate_send. A job with no originTurnId (a bare shell job, or a job not
// run via `delegate`) has no row to route to and is silently ignored - the
// caller (stores/threads.ts) doesn't need to pre-filter by job type.
//
// Deliberately narrow: this SUPPLEMENTS watchedChild.tsx's per-child watch,
// it does not replace it. The notification is a strictly better source for
// terminal status/reason/resumable/exhaustion detail (authoritative, arrives
// even for an off-screen/unmounted row, carries fields the child's own
// thread status never does) - but it is exactly two discrete events (start,
// finish), never a continuous stream, so it cannot drive the running row's
// live Cadence animation. That still needs watchThread's frameTimes.
export function applySerfJobStarted(job: SerfJobInfo): void {
  if (!job.originTurnId) return;
  const rowKey = resolveRowKey(job.delegateId, job.jobId, job.originToolCallId ?? job.originItemId ?? job.jobId);
  // A fresh start - including a delegate_send resume reusing the SAME
  // delegateId/rowKey - must un-terminal the row and drop the PREVIOUS job's
  // reason/resumable/exhaustion detail; that detail must never linger under
  // a new running job.
  updateSubagentRowIfExists(job.originTurnId, rowKey, {
    liveKind: "running",
    liveReason: undefined,
    resumable: undefined,
    exhaustionBudget: undefined,
    exhaustionLimit: undefined,
  });
}

export function applySerfJobFinished(job: SerfJobInfo): void {
  if (!job.originTurnId) return;
  const rowKey = resolveRowKey(job.delegateId, job.jobId, job.originToolCallId ?? job.originItemId ?? job.jobId);
  updateSubagentRowIfExists(job.originTurnId, rowKey, {
    liveKind: classifyJobStatus(job.status),
    liveReason: job.reason,
    resumable: job.resumable,
    exhaustionBudget: job.exhaustionBudget,
    exhaustionLimit: job.exhaustionLimit,
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
