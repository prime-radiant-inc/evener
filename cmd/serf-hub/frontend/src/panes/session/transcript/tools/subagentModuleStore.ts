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
import type { ItemModel } from "../../../../protocol/model";
import type { SerfJobInfo } from "../../../../protocol/types.gen";
import { parseArgs, parseJSONObject, str } from "./helpers";

export type SubagentRowKind = "running" | "done" | "stopped" | "failed" | "unknown";

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

// turnScopeKey composes the string every function below actually keys its
// Maps on. Turn ids are minted per-session (internal/appprojector's own
// nextTurn counter restarts at 0 for every fresh thread), so a bare turnId
// is NOT globally unique - two unrelated sessions routinely land their first
// real turn on the identical "turn_N" string (same fixed preamble length).
// This store is a page-lifetime singleton (see the file header), so without
// sessionRef in the key, two sessions sharing a turnId collide into the SAME
// Map entry and each renders the other's rows (kata 8525 - reproduced live:
// a brand-new session's delegate block showed two orphaned rows verbatim
// from an unrelated, already-abandoned earlier session). sessionRef is
// omitted (never sent as undefined) from ToolRenderProps only for a
// hypothetical future caller (see its own doc comment) - the "" fallback
// keeps that path working, merely un-isolated, exactly like before this fix.
// The NUL separator can't appear in either a sessionRef or a turn id, so two
// distinct (sessionRef, turnId) pairs can never concatenate to the same key.
export function turnScopeKey(sessionRef: string | undefined, turnId: string): string {
  return `${sessionRef ?? ""}\0${turnId}`;
}

// rowKeyForDelegateItem mirrors subagentModule.tsx's rowFromDelegateItem keying
// logic so the top-level tool-row status can always target the same row that
// powers the module.
export function rowKeyForDelegateItem(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  const parsed = parseJSONObject(item.output);
  const delegateId = str(parsed ?? args, "delegate_id");
  const jobId = str(parsed ?? args, "job_id");
  return resolveRowKey(delegateId, jobId, item.callId ?? item.id);
}

// effectiveRowKind is the kind a row actually DISPLAYS: subagentModule.tsx's
// SubagentRowView prefers the live-watch/notification overlay (liveKind) over
// the frozen tool-output kind (see its own comment), and setWatchedLiveKind's
// terminal guard below already needed this exact "liveKind ?? kind" rule.
// sortedRows (hzq9) needs the same rule for a third reason: sorting and
// rendering must agree on what "worst" means, or a row could visually read as
// failed while still sorting by its stale, possibly-terminal-superseded,
// frozen kind.
export function effectiveRowKind(row: SubagentRow): SubagentRowKind {
  return row.liveKind ?? row.kind;
}

// KIND_SORT_PRIORITY is the worst-first row order (9f16d9d35's "honest-clock
// demotion + worst-first overflow" - shipped once, into the legacy renderer,
// lost when it was deleted - kata hzq9). "stopped" (3zf8) sits between
// running and done: a deliberate stop is neither a live child nor a defect,
// but it is not a clean success either, so it stays out of done's "nothing
// to see here" territory.
const KIND_SORT_PRIORITY: Record<SubagentRowKind, number> = {
  failed: 0,
  unknown: 1,
  running: 2,
  stopped: 3,
  done: 4,
};

// sortedRows orders worst-first by kind, falling back to spawn (first-seen)
// index only as a tiebreaker WITHIN a kind (see upsertSubagentRow's own
// comment on why spawn order matters there, and useSubagentRows' for why
// this must stay a stable, precomputed array rather than a fresh sort per
// render). A row only changes position when its own effective kind changes -
// an incidental re-upsert/patch that leaves the kind alone never reorders it,
// so rows don't jump around as children merely report in, but a row that
// actually settles (or regresses) DOES move, which is exactly when a reader
// should notice it.
function sortedRows(rows: Map<string, SubagentRow>): SubagentRow[] {
  return Array.from(rows.values()).sort((a, b) => {
    const kindDelta = KIND_SORT_PRIORITY[effectiveRowKind(a)] - KIND_SORT_PRIORITY[effectiveRowKind(b)];
    return kindDelta !== 0 ? kindDelta : a.spawnIndex - b.spawnIndex;
  });
}

// upsertSubagentRow creates a row on first sight of `rowKey` (assigning it
// the next spawn index for this turn, so its position within its own kind's
// group is fixed at first-seen order per parity §12's "fixed by spawn order"
// rule - see sortedRows above for the worst-first grouping this now sits
// inside, kata hzq9) or
// updates it in place on every later call - used by `delegate` itself
// (subagentModule.tsx), the one tool in this family allowed to spawn a
// fresh row. `scopeKey` is a turnScopeKey(sessionRef, turnId) - see that
// function's own doc comment for why a bare turnId is not enough.
export function upsertSubagentRow(scopeKey: string, row: SubagentRowInput, migrateFromRowKey?: string): void {
  moduleStore.setState((s) => {
    const existingForTurn = s.turnRowsByKey.get(scopeKey);
    const rows = new Map(existingForTurn ?? []);
    const existingRow = rows.get(row.rowKey) ?? (migrateFromRowKey ? rows.get(migrateFromRowKey) : undefined);
    if (migrateFromRowKey && migrateFromRowKey !== row.rowKey) rows.delete(migrateFromRowKey);
    const nextIndexBefore = s.turnNextSpawnIndex.get(scopeKey) ?? 0;
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
    turnRowsByKey.set(scopeKey, rows);
    const turnRowsSorted = new Map(s.turnRowsSorted);
    turnRowsSorted.set(scopeKey, sortedRows(rows));
    const turnNextSpawnIndex = new Map(s.turnNextSpawnIndex);
    turnNextSpawnIndex.set(scopeKey, existingRow ? nextIndexBefore : nextIndexBefore + 1);
    return { turnRowsByKey, turnRowsSorted, turnNextSpawnIndex };
  });
}

// updateSubagentRowIfExists patches an EXISTING row only - used by
// job_status/job_stop/delegate_send, which check on/message an already-
// spawned child and must never fabricate a row of their own (mirrors the
// legacy reconcileSubagent's own rule: "only ever updates an existing row
// - it never spawns a new one from a read/list/message call"). A rowKey
// with no existing row (no `delegate` call seen yet this turn, or none at
// all) is a silent no-op. `scopeKey` - see upsertSubagentRow's own comment.
export function updateSubagentRowIfExists(scopeKey: string, rowKey: string, patch: Partial<SubagentRowInput>): void {
  moduleStore.setState((s) => {
    const existingForTurn = s.turnRowsByKey.get(scopeKey);
    const existingRow = existingForTurn?.get(rowKey);
    if (!existingForTurn || !existingRow) return s;
    const rows = new Map(existingForTurn);
    rows.set(rowKey, { ...existingRow, ...patch });
    const turnRowsByKey = new Map(s.turnRowsByKey);
    turnRowsByKey.set(scopeKey, rows);
    const turnRowsSorted = new Map(s.turnRowsSorted);
    turnRowsSorted.set(scopeKey, sortedRows(rows));
    return { turnRowsByKey, turnRowsSorted };
  });
}

// classifyJobStatus mirrors renderer.js's classifyJobStatus (parity §12),
// with one deliberate departure (3zf8): cancelled/stopped land in their OWN
// "stopped" kind, not "done" - a child killed on purpose (job_stop) or
// reconciled to stopped/runtime_lost after a hub restart (agent/internal/
// jobstore/reconcile.go) is not a failure (nothing broke), but it is not a
// clean completion either, and rendering it byte-identical to one (same
// glyph, tone, and label) was the exact defect. Any status this codebase
// hasn't seen yet (including an absent one, e.g. the delegate call settled
// but the child hasn't reported a status field at all) degrades to
// "running" rather than a confusing/alarming "unknown" - an honest "still
// don't know anything bad happened" default. Lives here (not
// subagentModule.tsx) so applySerfJobFinished below and stores/threads.ts
// can both reach it without a core store importing a React/UI file -
// subagentModule.tsx re-exports it for its own existing callers.
export function classifyJobStatus(status: string | undefined): SubagentRowKind {
  if (status === undefined) return "running";
  if (["failed", "errored", "error", "exhausted"].includes(status)) return "failed";
  if (["cancelled", "stopped"].includes(status)) return "stopped";
  if (["completed", "done", "succeeded"].includes(status)) return "done";
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

const TERMINAL_KINDS: ReadonlySet<SubagentRowKind> = new Set(["done", "stopped", "failed", "unknown"]);

// setWatchedLiveKind is WatchedChildIndicator's own guarded writer (dr7e).
// The child's own thread-status subscription can lag the parent's
// authoritative serf/job/finished notification (applySerfJobFinished
// below): once that notification has already settled a row into a terminal
// kind, a slower watch catching up to a stale "running" read must not
// resurrect the spinner. A terminal reading from the watch (the child
// socket itself closing, say) still applies normally - only a "running"
// write is ever suppressed, and only when the row is already terminal.
export function setWatchedLiveKind(scopeKey: string, rowKey: string, liveKind: SubagentRowKind): void {
  if (liveKind === "running") {
    const existingRow = moduleStore.getState().turnRowsByKey.get(scopeKey)?.get(rowKey);
    if (existingRow && TERMINAL_KINDS.has(effectiveRowKind(existingRow))) return;
  }
  updateSubagentRowIfExists(scopeKey, rowKey, { liveKind });
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
//
// sessionRef (kata 8525) is the notification's own `ref` field (SerfJobParams -
// the hub always sets it, appwire_projection.go's own EventJobStarted/
// EventJobFinished cases), threaded through turnScopeKey exactly like every
// other route into this store: originTurnId alone is per-session, not
// globally unique, and this store is a page-lifetime singleton.
export function applySerfJobStarted(job: SerfJobInfo, sessionRef: string | undefined): void {
  if (!job.originTurnId) return;
  const rowKey = resolveRowKey(job.delegateId, job.jobId, job.originToolCallId ?? job.originItemId ?? job.jobId);
  // A fresh start - including a delegate_send resume reusing the SAME
  // delegateId/rowKey - must un-terminal the row and drop the PREVIOUS job's
  // reason/resumable/exhaustion detail; that detail must never linger under
  // a new running job.
  updateSubagentRowIfExists(turnScopeKey(sessionRef, job.originTurnId), rowKey, {
    liveKind: "running",
    liveReason: undefined,
    resumable: undefined,
    exhaustionBudget: undefined,
    exhaustionLimit: undefined,
  });
}

export function applySerfJobFinished(job: SerfJobInfo, sessionRef: string | undefined): void {
  if (!job.originTurnId) return;
  const rowKey = resolveRowKey(job.delegateId, job.jobId, job.originToolCallId ?? job.originItemId ?? job.jobId);
  updateSubagentRowIfExists(turnScopeKey(sessionRef, job.originTurnId), rowKey, {
    liveKind: classifyJobStatus(job.status),
    liveReason: job.reason,
    resumable: job.resumable,
    exhaustionBudget: job.exhaustionBudget,
    exhaustionLimit: job.exhaustionLimit,
  });
}

// useSubagentRows reactively selects every row tracked for `scopeKey`,
// worst-first by kind and by spawn index within a kind (kata hzq9 - see
// sortedRows' own comment; never by plain update recency - an incidental
// re-upsert that leaves a row's kind unchanged must not visually jump it to a
// different position). Reads the precomputed array directly - see
// turnRowsSorted's own doc comment for why this must not re-derive/re-sort
// inline.
export function useSubagentRows(scopeKey: string): SubagentRow[] {
  return useStore(moduleStore, (s) => s.turnRowsSorted.get(scopeKey) ?? EMPTY_ROWS);
}

export function useLeader(scopeKey: string): string | undefined {
  return useStore(moduleStore, (s) => s.turnLeader.get(scopeKey));
}

// claimLeader is a plain (non-reactive) function called from a component
// effect when the reactive leader slot is empty. Returns true for whichever
// item id claims (or already holds) leadership for `scopeKey`; false for every
// other item. Idempotent: the current leader re-claiming its own slot stays
// true.
export function claimLeader(scopeKey: string, itemId: string): boolean {
  const current = moduleStore.getState().turnLeader.get(scopeKey);
  if (current === undefined) {
    moduleStore.setState((s) => {
      const turnLeader = new Map(s.turnLeader);
      turnLeader.set(scopeKey, itemId);
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
export function releaseLeader(scopeKey: string, itemId: string): void {
  const state = moduleStore.getState();
  if (state.turnLeader.get(scopeKey) !== itemId) return;
  moduleStore.setState((s) => {
    const turnLeader = new Map(s.turnLeader);
    turnLeader.delete(scopeKey);
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
