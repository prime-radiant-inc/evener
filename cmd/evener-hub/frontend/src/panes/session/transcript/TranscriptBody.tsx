import type { ReactNode, RefObject } from "react";
import { useMemo, useRef } from "react";
import { useStore } from "zustand";
import type { ThreadModel, TurnModel } from "../../../protocol/model";
import { transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { configFingerprint, type TranscriptDisplayConfigV1 } from "../../../transcriptDisplay/config";
import {
  type ProjectedEntry,
  type ProjectedTurn,
  projectThread,
  type TranscriptProjection,
} from "../../../transcriptDisplay/projector";
import { createTranscriptRenderContext, TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { VirtualList, type VirtualListHandle } from "../../../widgets";
import { modelLabel } from "../chrome/statusFormat";
import { exchangeOpenersFor } from "./exchangeOpeners";
import { FlowOverlay } from "./flow/FlowOverlay";
import { useTranscriptViewRegistration } from "./flow/useTranscriptScroll";
import { ProjectedIntentGroup, TurnBlock } from "./TurnBlock";
import { foldTurnEntries } from "./toolRuns";
import { asTurnError } from "./turnFailure";
import "./messages";
import "./tools";
import styles from "../session.module.css";

const ESTIMATED_TURN_HEIGHT = 96;
type IntentEntry = Extract<ProjectedEntry, { kind: "intent" }>;

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

export type TranscriptBodyRow = TranscriptTurnRow | TranscriptIntentGroupRow;

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
      if (previous === undefined || current === undefined || current !== previous + 1) {
        adjacentTurns = false;
        break;
      }
    }
    const terminalSingleTurn = sourceTurnIndexes.length === 1 && end === flat.length;
    if (!adjacentTurns && !terminalSingleTurn) return;
    const entries = run.map((item) => item.entry).filter((entry): entry is IntentEntry => entry.kind === "intent");
    const sourceTurnIds = sourceTurnIndexes.map((turnIndex) => projection.turns[turnIndex]?.id ?? "");
    const finalSourceTurnIndex = sourceTurnIndexes.at(-1);
    const group: CrossTurnIntentRun = {
      id: `intent-group:${entries[0]?.id}`,
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
  return rows;
}

// Anchors are registered from the SAME fold TurnBlock renders (toolRuns.ts's
// foldTurnEntries): a folded run is one anchor under the run's id, carrying
// its first entry's source index, because while the run is closed no element
// carries the individual entry ids and a restore that looked for one would
// find nothing (roborev on PR #947).
export function transcriptAnchorEntriesForRows(rows: readonly TranscriptBodyRow[]): readonly TranscriptAnchorEntry[] {
  return rows.flatMap((row, index) => {
    if (row.kind === "intentGroup") {
      return row.entries.map((entry) => ({
        id: entry.id,
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
          id: entry.id,
          sourceIndex: entry.sourceIndex,
          index,
          isMessage: entry.kind === "item" && entry.isMessage,
        },
      ];
    });
  });
}

export function transcriptSourceTurnRowIndexesForRows(rows: readonly TranscriptBodyRow[]): ReadonlyMap<string, number> {
  const indexes = new Map<string, number>();
  for (const [index, row] of rows.entries()) {
    if (row.kind === "intentGroup") {
      for (const turnId of row.sourceTurnIds) indexes.set(turnId, index);
    } else {
      indexes.set(row.turn.source.id, index);
    }
  }
  return indexes;
}

export interface TranscriptBodyProps {
  model: ThreadModel;
  config: TranscriptDisplayConfigV1;
  surface: "live" | "readOnly" | "preview";
  disclosureScope: string;
  sessionRef?: string;
  showSeenDividerTurnId?: string;
  loadOlderRow?: ReactNode;
  liveOverlay?: ReactNode;
  listRef?: RefObject<VirtualListHandle | null>;
  onMeasurementsChange?: () => void;
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
  onAnnounceViewChange?: (summary: string) => void;
}

export function TranscriptBody({
  model,
  config,
  surface,
  disclosureScope,
  sessionRef,
  showSeenDividerTurnId,
  loadOlderRow,
  liveOverlay,
  listRef,
  onMeasurementsChange,
  trailingContent,
  trailingRow,
  viewId,
  onAnnounceViewChange,
}: TranscriptBodyProps) {
  const focusFallbackRef = useRef<HTMLElement>(null);
  const projection = useMemo(() => projectThread(model, config), [model, config]);
  const rows = useMemo(() => transcriptRowsForProjection(projection), [projection]);
  const openers = useMemo(() => exchangeOpenersFor(model.turns), [model.turns]);
  const agentLabel = modelLabel(model.modelProvider, model.model);
  const itemRenderFingerprint = [
    configFingerprint(config),
    JSON.stringify(projection.metadata),
    projection.eligibleDisclosureIds.join("\0"),
    surface,
    sessionRef,
    disclosureScope,
  ].join("\0");
  // biome-ignore lint/correctness/useExhaustiveDependencies: itemRenderFingerprint covers projection semantics; retaining its identity avoids settled-row rerenders for unrelated stream deltas
  const itemRenderContext = useMemo(
    () =>
      createTranscriptRenderContext({
        config,
        projection,
        surface,
        sessionRef,
        disclosureScope,
      }),
    [itemRenderFingerprint],
  );
  const displayViewport = useStore(transcriptDisplayStore, (state) => state.viewport);
  const viewRegistration = useTranscriptViewRegistration({
    enabled: surface !== "preview",
    id: viewId ?? `${surface}:${sessionRef ?? disclosureScope}`,
    layout: displayViewport,
    viewKey: configFingerprint(config),
    listRef,
    anchorEntries: transcriptAnchorEntriesForRows(rows),
    // Include the synthetic trailing row: following-bottom view restores
    // target renderedRowCount - 1, which is the trailing row when present.
    renderedRowCount: rows.length + (trailingRow === undefined ? 0 : 1),
    focusFallback: () => focusFallbackRef.current?.focus(),
    announce: onAnnounceViewChange,
  });

  const renderRow = (row: TranscriptBodyRow, index: number) => {
    const seenTurnId = showSeenDividerTurnId;
    const seenAlreadyRendered =
      seenTurnId !== undefined &&
      rows
        .slice(0, index)
        .some((previous) =>
          previous.kind === "intentGroup"
            ? previous.sourceTurnIds.includes(seenTurnId)
            : previous.turn.source.id === seenTurnId,
        );
    const showSeenDivider = seenTurnId !== undefined && !seenAlreadyRendered;
    if (row.kind === "intentGroup") {
      return (
        <div data-testid="transcript-row" data-row-id={row.id}>
          <ProjectedIntentGroup
            entries={row.entries}
            rowId={row.id}
            sourceTurnIds={row.sourceTurnIds}
            separatorTurn={row.separatorTurn}
            viewAnchorIndex={index}
            showSeenDivider={showSeenDivider && row.sourceTurnIds.includes(seenTurnId ?? "")}
            sessionRef={sessionRef}
            renderContext={itemRenderContext}
            thread={model}
          />
        </div>
      );
    }
    return (
      <div data-testid="transcript-row" data-row-id={row.id}>
        <TurnBlock
          turn={row.turn}
          sessionRef={sessionRef}
          showSeenDivider={showSeenDivider && row.turn.source.id === seenTurnId}
          exchangeOpeners={openers}
          agentLabel={agentLabel}
          viewAnchorIndex={index}
          showTurnSeparator={row.showTurnSeparator}
          renderContext={itemRenderContext}
          thread={model}
        />
      </div>
    );
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
      aria-label="Transcript"
      tabIndex={-1}
    >
      <VirtualList
        ref={listRef}
        dynamic
        anchorToEnd
        count={rows.length + (trailingRow === undefined ? 0 : 1)}
        estimateSize={() => ESTIMATED_TURN_HEIGHT}
        getItemKey={(index) => (isTrailingRowIndex(index) && trailingRow ? trailingRow.id : rowAt(index).id)}
        renderRow={(index) =>
          isTrailingRowIndex(index) && trailingRow ? (
            <div data-testid="transcript-row" data-row-id={trailingRow.id}>
              {trailingRow.content}
            </div>
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
        <FlowOverlay top={loadOlderRow} pill={liveOverlay}>
          <div className={styles.transcriptContent}>
            {list}
            {trailingContent}
          </div>
        </FlowOverlay>
      );
    } else {
      transcriptContent = (
        <>
          {loadOlderRow}
          <div className={styles.transcriptContent}>
            {list}
            {trailingContent}
          </div>
        </>
      );
    }
    content = <div className={styles.transcript}>{transcriptContent}</div>;
  }

  return (
    <TranscriptRenderProvider
      config={config}
      projection={projection}
      surface={surface}
      sessionRef={sessionRef}
      disclosureScope={disclosureScope}
      thread={model}
    >
      {content}
    </TranscriptRenderProvider>
  );
}
