import type {
  ItemModel,
  ProjectedEntry,
  ProjectedTurn,
  ThreadModel,
  TranscriptProjection,
  TurnModel,
} from "@evener/appwire-client";
import {
  APPROVAL_DECISION_EVENT_KIND,
  attentionWarningNotice,
  configFingerprint,
  entryDisplayKey,
  hasFailedNotification,
  hasItemFailure,
  isActiveItem,
  isErrorEvent,
  isParentSteer,
  isQuietYield,
  projectThread,
  steeringNotificationFragments,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type { ReactNode, RefObject } from "react";
import { createContext, useContext, useMemo, useRef } from "react";
import { useStore } from "zustand";
import { workspaceStore } from "../../../shell/workspace";
import { transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { createTranscriptRenderContext, TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { type CommittedVirtualListLayout, VirtualList, type VirtualListHandle } from "../../../widgets";
import { modelLabel } from "../chrome/statusFormat";
import { exchangeClosersFor } from "./exchangeClosers";
import { exchangeOpenersFor } from "./exchangeOpeners";
import { FlowOverlay } from "./flow/FlowOverlay";
import type { CapturedTranscriptView } from "./flow/transcriptViewRegistry";
import { useTranscriptViewRegistration } from "./flow/useTranscriptViewRegistration";
import { readingEntryPoint } from "./flow/viewAnchor";
import { ProjectedIntentGroup, ProjectedQuietYieldGroup, TurnBlock } from "./TurnBlock";
import { foldTurnEntries, intentGroupId } from "./toolRuns";
import type { TranscriptReadView } from "./transcriptReadView";
import { asTurnError } from "./turnFailure";
import { useEntityView } from "./useEntityView";
import "./messages";
import "./tools";
import styles from "../session.module.css";

const ESTIMATED_TURN_HEIGHT = 96;

// The end inset row: the phone's floating composer overlays the transcript's
// bottom edge (PaneScaffold's footer overlay), so the scroll content carries
// its own trailing reserve - the last turn scrolls out from under the card
// instead of stopping behind it. Rendered on every surface as a row (panes
// never ask which host is showing); session.module.css measures it to the
// reserved height only under the phone's media gate, 0 everywhere else.
const END_INSET_ROW_ID = "transcript-end-inset";
const ESTIMATED_END_INSET_HEIGHT = 150;
type IntentEntry = Extract<ProjectedEntry, { kind: "intent" }>;
type QuietWakeEntry = Extract<ProjectedEntry, { kind: "item" | "critical" | "thinking" }>;

export const TranscriptSourcePaneContext = createContext<string | undefined>(undefined);

export interface TranscriptTurnRow {
  readonly kind: "turn";
  readonly id: string;
  readonly turn: ProjectedTurn;
  readonly sourceTurnIndex: number;
  readonly showTurnSeparator: boolean;
}

export interface TranscriptIntentGroupRow {
  readonly kind: "intentGroup";
  readonly id: string;
  readonly entries: readonly IntentEntry[];
  readonly sourceTurnIndexes: readonly number[];
  readonly sourceTurnIds: readonly string[];
  readonly separatorTurn?: TurnModel;
}

export interface TranscriptQuietYieldGroupRow {
  readonly kind: "quietYieldGroup";
  readonly id: string;
  readonly entries: readonly QuietWakeEntry[];
  readonly turns: readonly ProjectedTurn[];
  readonly quietCount: number;
  readonly sourceTurnIds: readonly string[];
  readonly separatorTurn?: TurnModel;
}

export type TranscriptBodyRow = TranscriptTurnRow | TranscriptIntentGroupRow | TranscriptQuietYieldGroupRow;

function quietYieldGroupId(firstEntryKey: string): string {
  return `quiet-yield-group:${firstEntryKey}`;
}

const QUIET_WAKE_BOUNDARY_EVENT_KINDS: ReadonlySet<string> = new Set([
  "warning",
  "interrupted",
  "loop_detection",
  "turn_limit",
  "tool_repair",
]);

function isRoutineDaemonNotificationSteer(item: ItemModel): boolean {
  if (
    item.type !== "steering" ||
    item.source === "user" ||
    item.steeringKind === "human-note" ||
    isParentSteer(item.source)
  ) {
    return false;
  }
  const fragments = steeringNotificationFragments(item.text ?? "");
  if (hasFailedNotification(fragments)) return false;
  return item.steeringKind === "notification" || fragments !== null;
}

function isQuietWakeSystemBoundary(item: ItemModel, turnStatus: string | undefined): boolean {
  if (isErrorEvent(item) || attentionWarningNotice(item) !== null) return true;
  if (item.eventKind === APPROVAL_DECISION_EVENT_KIND) return true;
  if (item.eventKind !== undefined && QUIET_WAKE_BOUNDARY_EVENT_KINDS.has(item.eventKind)) return true;
  if (item.eventKind === "hook_completed" && typeof item.exitCode === "number" && item.exitCode !== 0) return true;
  return hasItemFailure(item) || isActiveItem(item, turnStatus);
}

function isQuietWakeBoundary(entry: ProjectedEntry, turnStatus: string | undefined): boolean {
  if (entry.kind === "intent") return true;
  if (entry.kind === "critical") return !isRoutineDaemonNotificationSteer(entry.item);
  if (entry.kind === "thinking") return entry.live || hasItemFailure(entry.item);
  const item = entry.item;
  switch (item.type) {
    case "reasoning":
      return isActiveItem(item, turnStatus) || hasItemFailure(item);
    case "steering":
      return !isRoutineDaemonNotificationSteer(item);
    case "systemMessage":
      return isQuietWakeSystemBoundary(item, turnStatus);
    default:
      return true;
  }
}

function isEmptyHiddenTurn(turn: ProjectedTurn): boolean {
  return turn.entries.length === 0 && !asTurnError(turn.source.error);
}

function quietWakeEntries(turn: ProjectedTurn): { entries: readonly QuietWakeEntry[]; quietCount: number } | undefined {
  if (turn.entries.length === 0) return undefined;
  if (asTurnError(turn.source.error) || turn.source.status === "failed" || turn.source.status === "interrupted") {
    return undefined;
  }
  const entries: QuietWakeEntry[] = [];
  let quietCount = 0;
  for (const entry of turn.entries) {
    if (isQuietWakeBoundary(entry, turn.source.status)) return undefined;
    if (entry.kind === "item" && entry.item.type === "systemMessage" && isQuietYield(entry.item)) {
      quietCount += 1;
    }
    entries.push(entry as QuietWakeEntry);
  }
  return quietCount > 0 ? { entries, quietCount } : undefined;
}

export interface TranscriptAnchorEntry {
  readonly id: string;
  readonly sourceIndex: number;
  readonly index: number;
  readonly isMessage: boolean;
  /** For a folded tool run: the entry ids it stands in for (toolRuns.ts). */
  members?: readonly string[];
}

interface FlatEntry {
  readonly entry: ProjectedEntry;
  readonly turnIndex: number;
}

interface CrossTurnIntentRun {
  readonly id: string;
  readonly entries: readonly IntentEntry[];
  readonly sourceTurnIndexes: readonly number[];
  readonly sourceTurnIds: readonly string[];
  readonly separatorTurn?: TurnModel;
}

function entryKey(turnIndex: number, entry: ProjectedEntry): string {
  return `${turnIndex}\0${entry.id}`;
}

function fragmentTurn(turn: ProjectedTurn, entries: readonly ProjectedEntry[]): ProjectedTurn {
  const visibleIds = new Set(entries.map((entry) => (entry.kind === "intent" ? entry.sourceItemId : entry.item.id)));
  return {
    id: turn.id,
    source: turn.source,
    entries,
    visibleItems: turn.visibleItems.filter((item) => visibleIds.has(item.id)),
  };
}

function turnRow(
  turn: ProjectedTurn,
  sourceTurnIndex: number,
  entries: readonly ProjectedEntry[],
  segment: number,
  showTurnSeparator: boolean,
): TranscriptTurnRow {
  const isWholeTurn = entries.length === turn.entries.length;
  return {
    kind: "turn",
    id: isWholeTurn ? turn.id : `turn:${turn.id}:segment:${segment}`,
    turn: isWholeTurn ? turn : fragmentTurn(turn, entries),
    sourceTurnIndex,
    showTurnSeparator,
  };
}

/**
 * Converts projected turns into the stable rows consumed by both the virtual
 * and preview layouts. A contiguous intent run that crosses a turn boundary
 * becomes one row; any visible item/critical/message entry or source turn with
 * a renderable failure end cap breaks that run.
 * Single-turn intent groups remain inside TurnBlock except for a terminal run:
 * that run gets its eventual first-action row identity before a following turn
 * streams in, while separator metadata preserves its ordinary turn chrome.
 */
export function transcriptRowsForProjection(projection: TranscriptProjection): readonly TranscriptBodyRow[] {
  const flat: FlatEntry[] = projection.turns.flatMap((turn, turnIndex) =>
    turn.entries.map((entry) => ({ entry, turnIndex })),
  );
  const crossRunByEntry = new Map<string, CrossTurnIntentRun>();
  const registerIntentRun = (start: number, end: number) => {
    if (start >= end) return;
    const run = flat.slice(start, end);
    const sourceTurnIndexes = [...new Set(run.map((item) => item.turnIndex))];
    let adjacentTurns = sourceTurnIndexes.length > 1;
    for (let index = 1; index < sourceTurnIndexes.length; index += 1) {
      const previous = sourceTurnIndexes[index - 1];
      const current = sourceTurnIndexes[index];
      if (previous === undefined || current === undefined) {
        adjacentTurns = false;
        break;
      }
      for (let mid = previous + 1; mid < current; mid += 1) {
        const midTurn = projection.turns[mid];
        if (midTurn === undefined || !isEmptyHiddenTurn(midTurn)) {
          adjacentTurns = false;
          break;
        }
      }
      if (!adjacentTurns) break;
    }
    const terminalSingleTurn = sourceTurnIndexes.length === 1 && end === flat.length;
    if (!adjacentTurns && !terminalSingleTurn) return;
    const entries = run.map((item) => item.entry).filter((entry): entry is IntentEntry => entry.kind === "intent");
    const sourceTurnIds = sourceTurnIndexes.map((turnIndex) => projection.turns[turnIndex]?.id ?? "");
    const finalSourceTurnIndex = sourceTurnIndexes.at(-1);
    const group: CrossTurnIntentRun = {
      id: intentGroupId(entries[0]?.id),
      entries,
      sourceTurnIndexes,
      sourceTurnIds,
      separatorTurn:
        end === flat.length && finalSourceTurnIndex !== undefined
          ? projection.turns[finalSourceTurnIndex]?.source
          : undefined,
    };
    for (const item of run) crossRunByEntry.set(entryKey(item.turnIndex, item.entry), group);
  };
  let flatIndex = 0;
  while (flatIndex < flat.length) {
    const first = flat[flatIndex];
    if (first?.entry.kind !== "intent") {
      flatIndex += 1;
      continue;
    }
    let end = flatIndex + 1;
    while (end < flat.length && flat[end]?.entry.kind === "intent") end += 1;
    let cleanStart = flatIndex;
    for (let cursor = flatIndex; cursor < end; cursor += 1) {
      const candidate = flat[cursor];
      const sourceTurn = candidate === undefined ? undefined : projection.turns[candidate.turnIndex]?.source;
      if (sourceTurn && asTurnError(sourceTurn.error)) {
        registerIntentRun(cleanStart, cursor);
        cleanStart = cursor + 1;
      }
    }
    registerIntentRun(cleanStart, end);
    flatIndex = end;
  }

  const rows: TranscriptBodyRow[] = [];
  const emittedRuns = new Set<CrossTurnIntentRun>();
  for (const [sourceTurnIndex, projectedTurn] of projection.turns.entries()) {
    let segmentStart = 0;
    let cursor = 0;
    let segment = 0;
    let hadCrossRun = false;
    while (cursor < projectedTurn.entries.length) {
      const entry = projectedTurn.entries[cursor];
      const run = entry === undefined ? undefined : crossRunByEntry.get(entryKey(sourceTurnIndex, entry));
      if (run === undefined) {
        cursor += 1;
        continue;
      }
      hadCrossRun = true;
      if (cursor > segmentStart) {
        const entries = projectedTurn.entries.slice(segmentStart, cursor);
        rows.push(turnRow(projectedTurn, sourceTurnIndex, entries, segment++, false));
      }
      if (!emittedRuns.has(run)) {
        rows.push({
          kind: "intentGroup",
          id: run.id,
          entries: run.entries,
          sourceTurnIndexes: run.sourceTurnIndexes,
          sourceTurnIds: run.sourceTurnIds,
          separatorTurn: run.separatorTurn,
        });
        emittedRuns.add(run);
      }
      while (cursor < projectedTurn.entries.length) {
        const candidate = projectedTurn.entries[cursor];
        if (candidate === undefined || crossRunByEntry.get(entryKey(sourceTurnIndex, candidate)) !== run) break;
        cursor += 1;
      }
      segmentStart = cursor;
    }
    if (!hadCrossRun) {
      rows.push({ kind: "turn", id: projectedTurn.id, turn: projectedTurn, sourceTurnIndex, showTurnSeparator: true });
    } else if (segmentStart < projectedTurn.entries.length) {
      const entries = projectedTurn.entries.slice(segmentStart);
      rows.push(turnRow(projectedTurn, sourceTurnIndex, entries, segment++, true));
    }
  }
  const grouped: TranscriptBodyRow[] = [];
  let rowIndex = 0;
  while (rowIndex < rows.length) {
    const firstRow = rows[rowIndex];
    const firstWake =
      firstRow?.kind === "turn" && firstRow.showTurnSeparator ? quietWakeEntries(firstRow.turn) : undefined;
    if (firstRow?.kind !== "turn" || firstWake === undefined) {
      if (firstRow) grouped.push(firstRow);
      rowIndex += 1;
      continue;
    }
    const run: { row: TranscriptTurnRow; entries: readonly QuietWakeEntry[]; quietCount: number }[] = [
      { row: firstRow, entries: firstWake.entries, quietCount: firstWake.quietCount },
    ];
    const skippedEmptyIds: string[] = [];
    let pendingEmpty: TranscriptTurnRow[] = [];
    let end = rowIndex + 1;
    let lastQuietEnd = rowIndex + 1;
    while (end < rows.length) {
      const candidate = rows[end];
      if (candidate?.kind !== "turn" || !candidate.showTurnSeparator) break;
      if (isEmptyHiddenTurn(candidate.turn)) {
        pendingEmpty.push(candidate);
        end += 1;
        continue;
      }
      const wake = quietWakeEntries(candidate.turn);
      if (wake === undefined) break;
      for (const empty of pendingEmpty) skippedEmptyIds.push(empty.turn.id);
      pendingEmpty = [];
      run.push({ row: candidate, entries: wake.entries, quietCount: wake.quietCount });
      end += 1;
      lastQuietEnd = end;
    }
    const firstEntry = run[0]?.entries[0];
    if (run.length < 2 || firstEntry === undefined) {
      grouped.push(firstRow);
      rowIndex += 1;
    } else {
      const entries = run.flatMap((member) => member.entries);
      const turns = run.flatMap((member) => member.entries.map(() => member.row.turn));
      const quietCount = run.reduce((total, member) => total + member.quietCount, 0);
      const lastRunTurn = run.at(-1)?.row.turn.source;
      grouped.push({
        kind: "quietYieldGroup",
        id: quietYieldGroupId(entryDisplayKey(firstEntry)),
        entries,
        turns,
        quietCount,
        sourceTurnIds: [...run.map((member) => member.row.turn.id), ...skippedEmptyIds],
        separatorTurn: lastQuietEnd === rows.length ? lastRunTurn : undefined,
      });
      rowIndex = lastQuietEnd;
    }
  }
  return grouped;
}

// Anchors are registered from the SAME fold TurnBlock renders (toolRuns.ts's
// foldTurnEntries): a folded run is one anchor under the run's id, carrying
// its first entry's source index, because while the run is closed no element
// carries the individual entry ids and a restore that looked for one would
// find nothing (roborev on PR #947).
export function transcriptAnchorEntriesForRows(rows: readonly TranscriptBodyRow[]): readonly TranscriptAnchorEntry[] {
  return rows.flatMap((row, index) => {
    if (row.kind === "intentGroup" || row.kind === "quietYieldGroup") {
      return row.entries.map((entry) => ({
        id: entryDisplayKey(entry),
        sourceIndex: entry.sourceIndex,
        index,
        isMessage: false,
      }));
    }
    return foldTurnEntries(row.turn).flatMap((entry) => {
      if (entry.kind === "run") {
        const first = entry.entries[0];
        return first
          ? [
              {
                id: entry.id,
                sourceIndex: first.sourceIndex,
                index,
                isMessage: false,
                members: entry.entries.map((member) => member.id),
              },
            ]
          : [];
      }
      return [
        {
          id: entryDisplayKey(entry),
          sourceIndex: entry.sourceIndex,
          index,
          isMessage: entry.kind === "item" && entry.isMessage,
        },
      ];
    });
  });
}

// The disclosure ids ToolRunGroup mints (run:<first entry>) for the runs
// TurnBlock will fold. The projector's eligibleDisclosureIds inventory only
// knows source item ids, so without these the Full-view baseline could not
// clear a run the reader closed before leaving Full and coming back
// (roborev on PR #947): a stale explicit close would keep the run shut in a
// view whose contract is "everything open".
export function transcriptRunDisclosureIdsForRows(rows: readonly TranscriptBodyRow[]): readonly string[] {
  return rows.flatMap((row) =>
    row.kind === "turn" ? foldTurnEntries(row.turn).flatMap((entry) => (entry.kind === "run" ? [entry.id] : [])) : [],
  );
}

function rowSourceTurnIds(row: TranscriptBodyRow): readonly string[] {
  return row.kind === "turn" ? [row.turn.source.id] : row.sourceTurnIds;
}

export function transcriptSourceTurnRowIndexesForRows(rows: readonly TranscriptBodyRow[]): ReadonlyMap<string, number> {
  const indexes = new Map<string, number>();
  for (const [index, row] of rows.entries()) {
    for (const turnId of rowSourceTurnIds(row)) indexes.set(turnId, index);
  }
  return indexes;
}

/** The first row that renders any of a source turn: where the seen divider
 * for that turn goes, and the row a session opening at it scrolls to. */
export function transcriptFirstRowIndexForTurn(rows: readonly TranscriptBodyRow[], turnId: string): number {
  return rows.findIndex((row) => rowSourceTurnIds(row).includes(turnId));
}

export interface TranscriptBodyProps {
  model: ThreadModel;
  config: TranscriptDisplayConfigV1;
  /**
   * A view already derived from this body's own (model, config): the
   * projection, the rows built from it, and the anchor entries folded from
   * those rows. A caller that already derives the trio for its own manifest
   * hands it down so one model revision costs one derivation instead of the
   * caller's and the body's separate copies. Callers that supply no prepared
   * view (the standalone preview surface) derive their own, as they always
   * have. The three members travel together: they are one derivation, and a
   * partially supplied view would let the rendered rows diverge from the
   * projection the rest of the body reads.
   */
  preparedView?: {
    projection: TranscriptProjection;
    rows: readonly TranscriptBodyRow[];
    anchorEntries: readonly TranscriptAnchorEntry[];
  };
  surface: "live" | "readOnly" | "preview";
  disclosureScope: string;
  sessionRef?: string;
  showSeenDividerTurnId?: string;
  loadOlderRow?: ReactNode;
  liveOverlay?: ReactNode;
  attentionOverlay?: ReactNode;
  onOverlayHeightChange?: (height: number) => void;
  listRef?: RefObject<VirtualListHandle | null>;
  onMeasurementsChange?: (layout?: CommittedVirtualListLayout) => void;
  trailingContent?: ReactNode;
  /**
   * An extra row appended AFTER the last transcript row, inside the virtual
   * list itself (unlike trailingContent, which sits beside the list and
   * therefore stays pinned). This is the slot for an interactive surface
   * that must scroll away with the transcript - the session pane's pending-
   * questions dock (composer/askDock): anchored to the composer's footer it
   * kept covering the bottom of the screen while the reader scrolled back
   * for context. As a real row it gets the list's own stable keying (id),
   * dynamic measurement, and end-anchored following: an arriving row follows
   * the tail for a reader at the bottom and does NOT yank a scrolled-back
   * reader. Virtual-list only - the preview surface (no virtual list) never
   * renders it. Interactive state inside the row must live in a store, not
   * component state: scrolling far enough away unmounts the row.
   */
  trailingRow?: { id: string; content: ReactNode };
  /** Stable pane identity for host-remount scroll state; optional for callers. */
  viewId?: string;
  /** Owning interactive pane; omitted by previews and non-owning renders. */
  sourcePaneId?: string;
  initialViewCapture?: CapturedTranscriptView;
  readView?: TranscriptReadView;
  /** The owning reader's following-bottom intent, used by reflow captures. */
  followingBottom?: () => boolean;
  /** The reader is held at a place they chose (the scroll owner's
   * holdsPlace): the list stops following new output until they let go, and
   * still keeps their place across older pages arriving above it. */
  holdsPlace?: boolean;
  onAnnounceViewChange?: (summary: string) => void;
}

// Every rendered row carries the same identity attributes, and every live
// column the same class. One wrapper each keeps that contract in one place
// for all row kinds and both column branches.
function TranscriptRow({ id, children }: { id: string; children: ReactNode }) {
  return (
    <div data-testid="transcript-row" data-row-id={id}>
      {children}
    </div>
  );
}

function TranscriptColumn({ children }: { children: ReactNode }) {
  return <div className={styles.transcriptContent}>{children}</div>;
}

export function TranscriptBody({
  model,
  config,
  preparedView,
  surface,
  disclosureScope,
  sessionRef,
  showSeenDividerTurnId,
  loadOlderRow,
  liveOverlay,
  attentionOverlay,
  onOverlayHeightChange,
  listRef,
  onMeasurementsChange,
  trailingContent,
  trailingRow,
  viewId,
  sourcePaneId: explicitSourcePaneId,
  initialViewCapture,
  readView,
  followingBottom,
  holdsPlace = false,
  onAnnounceViewChange,
}: TranscriptBodyProps) {
  const inheritedSourcePaneId = useContext(TranscriptSourcePaneContext);
  const sourcePaneId = explicitSourcePaneId ?? inheritedSourcePaneId;
  const focusFallbackRef = useRef<HTMLElement>(null);
  const entities = useEntityView(sessionRef ?? model.ref, model);
  const projection = useMemo(
    () => preparedView?.projection ?? projectThread(model, config),
    [preparedView, model, config],
  );
  const rows = useMemo(() => preparedView?.rows ?? transcriptRowsForProjection(projection), [preparedView, projection]);
  // Source item ids from the projector plus the folded-run ids the rows will
  // render, so the Full baseline reaches every disclosure on screen.
  const eligibleDisclosureIds = useMemo(
    () => [...projection.eligibleDisclosureIds, ...transcriptRunDisclosureIdsForRows(rows)],
    [projection, rows],
  );
  const openers = useMemo(() => exchangeOpenersFor(model.turns), [model.turns]);
  const closers = useMemo(() => exchangeClosersFor(model.turns), [model.turns]);
  const agentLabel = modelLabel(model.modelProvider, model.model);
  const itemRenderFingerprint = [
    configFingerprint(config),
    JSON.stringify(projection.metadata),
    eligibleDisclosureIds.join("\0"),
    surface,
    sessionRef,
    disclosureScope,
    sourcePaneId,
  ].join("\0");
  // biome-ignore lint/correctness/useExhaustiveDependencies: itemRenderFingerprint covers projection semantics; retaining its identity avoids settled-row rerenders for unrelated stream deltas
  const itemRenderContext = useMemo(
    () =>
      createTranscriptRenderContext({
        config,
        projection,
        eligibleDisclosureIds,
        surface,
        sessionRef,
        disclosureScope,
        sourcePaneId,
      }),
    [itemRenderFingerprint],
  );
  const displayViewport = useStore(transcriptDisplayStore, (state) => state.viewport);
  // The end inset is the list's last row after the trailing row when one is
  // present (one synthetic row past every transcript row, keyed by its own
  // stable id so the list's append-following and measurement treat it like
  // any other row). An EMPTY transcript keeps zero rows: the reserve is for
  // sliding the last turn clear of the composer, and there is no turn to
  // slide - while the zero-row special cases (the placement fallbacks and the
  // empty-state paths) must keep seeing a rowless list.
  const endInsetIndex =
    rows.length === 0 && trailingRow === undefined ? -1 : rows.length + (trailingRow === undefined ? 0 : 1);
  const isEndInsetIndex = (index: number) => index === endInsetIndex;
  const viewRegistration = useTranscriptViewRegistration({
    enabled: surface !== "preview",
    id: viewId ?? `${surface}:${sessionRef ?? disclosureScope}`,
    logicalRef: sessionRef ?? model.ref,
    layout: displayViewport,
    viewKey: configFingerprint(config),
    initialViewCapture,
    readView,
    followingBottom,
    listRef,
    anchorEntries: preparedView?.anchorEntries ?? transcriptAnchorEntriesForRows(rows),
    // Include the synthetic rows: the trailing row when present, and the end
    // inset row whenever any content exists. Following-bottom view restores
    // target renderedRowCount - 1 - the inset row - which lands the last turn
    // clear of the floating composer on the phone and is a no-op at its
    // desktop height of 0.
    renderedRowCount: endInsetIndex + 1,
    canRestoreFocus: () => sourcePaneId === undefined || workspaceStore.getState().focusedPaneId === sourcePaneId,
    focusFallback: () => focusFallbackRef.current?.focus(),
    announce: onAnnounceViewChange,
  });

  const seenDividerRowIndex = useMemo(
    () => (showSeenDividerTurnId === undefined ? -1 : transcriptFirstRowIndexForTurn(rows, showSeenDividerTurnId)),
    [rows, showSeenDividerTurnId],
  );

  const renderRow = (row: TranscriptBodyRow, index: number) => {
    const showSeenDivider = index === seenDividerRowIndex;
    if (row.kind === "intentGroup") {
      return (
        <TranscriptRow id={row.id}>
          <ProjectedIntentGroup
            entries={row.entries}
            rowId={row.id}
            sourceTurnIds={row.sourceTurnIds}
            separatorTurn={row.separatorTurn}
            viewAnchorIndex={index}
            showSeenDivider={showSeenDivider}
            sessionRef={sessionRef}
            renderContext={itemRenderContext}
            thread={model}
          />
        </TranscriptRow>
      );
    }
    if (row.kind === "quietYieldGroup") {
      return (
        <TranscriptRow id={row.id}>
          <ProjectedQuietYieldGroup
            entries={row.entries}
            turns={row.turns}
            quietCount={row.quietCount}
            rowId={row.id}
            sourceTurnIds={row.sourceTurnIds}
            separatorTurn={row.separatorTurn}
            viewAnchorIndex={index}
            showSeenDivider={showSeenDivider}
            sessionRef={sessionRef}
            renderContext={itemRenderContext}
            thread={model}
          />
        </TranscriptRow>
      );
    }
    return (
      <TranscriptRow id={row.id}>
        <TurnBlock
          turn={row.turn}
          sessionRef={sessionRef}
          showSeenDivider={showSeenDivider}
          exchangeOpeners={openers}
          exchangeClosers={closers}
          agentLabel={agentLabel}
          viewAnchorIndex={index}
          showTurnSeparator={row.showTurnSeparator}
          renderContext={itemRenderContext}
          thread={model}
        />
      </TranscriptRow>
    );
  };

  const readingPoint = () => {
    const port = listRef?.current?.getScrollElement();
    return port ? readingEntryPoint(port) : undefined;
  };

  const rowAt = (index: number) => {
    const row = rows[index];
    if (!row) throw new Error(`Transcript row ${index} out of range for ${rows.length} rows`);
    return row;
  };

  // The trailing row is index rows.length when present: one synthetic row
  // past every transcript row, keyed by its own stable id so the list's
  // append-following and measurement treat it like any other row.
  const isTrailingRowIndex = (index: number) => trailingRow !== undefined && index === rows.length;

  const list = (
    <section
      ref={focusFallbackRef}
      className={styles.transcriptList}
      data-testid="transcript-virtual-list"
      data-reading-scroll-host=""
      aria-label="Transcript"
      tabIndex={-1}
    >
      <VirtualList
        ref={listRef}
        dynamic
        anchorToEnd={!viewRegistration.hasRetainedPlacement()}
        followEnd={!holdsPlace}
        readingPoint={readingPoint}
        count={endInsetIndex + 1}
        estimateSize={(index) => (isEndInsetIndex(index) ? ESTIMATED_END_INSET_HEIGHT : ESTIMATED_TURN_HEIGHT)}
        getItemKey={(index) =>
          isEndInsetIndex(index)
            ? END_INSET_ROW_ID
            : isTrailingRowIndex(index) && trailingRow
              ? trailingRow.id
              : rowAt(index).id
        }
        renderRow={(index) =>
          isEndInsetIndex(index) ? (
            <div
              data-testid="transcript-end-inset"
              data-row-id={END_INSET_ROW_ID}
              aria-hidden="true"
              className={styles.transcriptEndInset}
            />
          ) : isTrailingRowIndex(index) && trailingRow ? (
            <TranscriptRow id={trailingRow.id}>{trailingRow.content}</TranscriptRow>
          ) : (
            renderRow(rowAt(index), index)
          )
        }
        onChange={() => {
          try {
            onMeasurementsChange?.();
          } finally {
            viewRegistration.restoreAfterMeasurement();
          }
        }}
        onLayout={(layout) => {
          viewRegistration.restoreAfterLayout(layout);
          if (onMeasurementsChange && !viewRegistration.isPlacementPending() && layout.isCurrent())
            onMeasurementsChange(layout);
        }}
      />
    </section>
  );

  let content: ReactNode;
  if (surface === "preview") {
    content = (
      <div data-testid="transcript-preview-flow">
        {rows.map((row, index) => (
          <div key={row.id}>{renderRow(row, index)}</div>
        ))}
      </div>
    );
  } else {
    let transcriptContent: ReactNode;
    if (surface === "live") {
      transcriptContent = (
        <FlowOverlay
          top={loadOlderRow}
          pill={liveOverlay}
          attentionOverlay={attentionOverlay}
          onOverlayHeightChange={onOverlayHeightChange}
        >
          <TranscriptColumn>
            {list}
            {trailingContent}
          </TranscriptColumn>
        </FlowOverlay>
      );
    } else {
      transcriptContent = (
        <>
          {loadOlderRow}
          <TranscriptColumn>
            {list}
            {trailingContent}
          </TranscriptColumn>
        </>
      );
    }
    content = <div className={styles.transcriptBody}>{transcriptContent}</div>;
  }

  return (
    <TranscriptRenderProvider
      config={config}
      projection={projection}
      eligibleDisclosureIds={eligibleDisclosureIds}
      surface={surface}
      sessionRef={sessionRef}
      disclosureScope={disclosureScope}
      thread={model}
      sourcePaneId={sourcePaneId}
      entities={entities}
    >
      {content}
    </TranscriptRenderProvider>
  );
}
