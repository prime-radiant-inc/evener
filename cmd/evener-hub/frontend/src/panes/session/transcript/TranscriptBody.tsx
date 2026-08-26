import type { ReactNode, RefObject } from "react";
import { useMemo } from "react";
import { useStore } from "zustand";
import type { ThreadModel } from "../../../protocol/model";
import { transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { configFingerprint, type TranscriptDisplayConfigV1 } from "../../../transcriptDisplay/config";
import {
  type ProjectedEntry,
  type ProjectedTurn,
  projectThread,
  type TranscriptProjection,
} from "../../../transcriptDisplay/projector";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { VirtualList, type VirtualListHandle } from "../../../widgets";
import { modelLabel } from "../chrome/statusFormat";
import { exchangeOpenersFor } from "./exchangeOpeners";
import { FlowOverlay } from "./flow/FlowOverlay";
import { useTranscriptViewRegistration } from "./flow/useTranscriptScroll";
import { ProjectedIntentGroup, TurnBlock } from "./TurnBlock";
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
}

export type TranscriptBodyRow = TranscriptTurnRow | TranscriptIntentGroupRow;

export interface TranscriptAnchorEntry {
  readonly id: string;
  readonly sourceIndex: number;
  readonly index: number;
  readonly isMessage: boolean;
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
 * becomes one row; any visible item/critical/message entry breaks that run.
 * Single-turn intent groups remain inside TurnBlock so ordinary turn chrome is
 * unchanged.
 */
export function transcriptRowsForProjection(projection: TranscriptProjection): readonly TranscriptBodyRow[] {
  const flat: FlatEntry[] = projection.turns.flatMap((turn, turnIndex) =>
    turn.entries.map((entry) => ({ entry, turnIndex })),
  );
  const crossRunByEntry = new Map<string, CrossTurnIntentRun>();
  let flatIndex = 0;
  while (flatIndex < flat.length) {
    const first = flat[flatIndex];
    if (first?.entry.kind !== "intent") {
      flatIndex += 1;
      continue;
    }
    let end = flatIndex + 1;
    while (end < flat.length && flat[end]?.entry.kind === "intent") end += 1;
    const run = flat.slice(flatIndex, end);
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
    if (adjacentTurns) {
      const entries = run.map((item) => item.entry).filter((entry): entry is IntentEntry => entry.kind === "intent");
      const sourceTurnIds = sourceTurnIndexes.map((turnIndex) => projection.turns[turnIndex]?.id ?? "");
      const group: CrossTurnIntentRun = {
        id: `intent-group:${entries[0]?.id}:${entries.at(-1)?.id}`,
        entries,
        sourceTurnIndexes,
        sourceTurnIds,
      };
      for (const item of run) crossRunByEntry.set(entryKey(item.turnIndex, item.entry), group);
    }
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

export function transcriptAnchorEntriesForRows(rows: readonly TranscriptBodyRow[]): readonly TranscriptAnchorEntry[] {
  return rows.flatMap((row, index) => {
    const entries = row.kind === "intentGroup" ? row.entries : row.turn.entries;
    return entries.map((entry) => ({
      id: entry.id,
      sourceIndex: entry.sourceIndex,
      index,
      isMessage: entry.kind === "item" && entry.isMessage,
    }));
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
  toolbar?: ReactNode;
  /** Stable pane identity for host-remount scroll state; optional for callers. */
  viewId?: string;
  /** Optional focus target supplied by the live Detail control. */
  detailTriggerRef?: RefObject<HTMLElement | null>;
  onFocusDetailTrigger?: () => void;
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
  toolbar,
  viewId,
  detailTriggerRef,
  onFocusDetailTrigger,
  onAnnounceViewChange,
}: TranscriptBodyProps) {
  const projection = useMemo(() => projectThread(model, config), [model, config]);
  const rows = useMemo(() => transcriptRowsForProjection(projection), [projection]);
  const openers = useMemo(() => exchangeOpenersFor(model.turns), [model.turns]);
  const agentLabel = modelLabel(model.modelProvider, model.model);
  const displayViewport = useStore(transcriptDisplayStore, (state) => state.viewport);
  const viewRegistration = useTranscriptViewRegistration({
    enabled: surface !== "preview",
    id: viewId ?? `${surface}:${sessionRef ?? disclosureScope}`,
    layout: displayViewport,
    viewKey: configFingerprint(config),
    listRef,
    anchorEntries: transcriptAnchorEntriesForRows(rows),
    renderedRowCount: rows.length,
    detailTriggerRef,
    focusDetailTrigger: onFocusDetailTrigger,
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
            viewAnchorIndex={index}
            showSeenDivider={showSeenDivider && row.sourceTurnIds.includes(seenTurnId ?? "")}
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
        />
      </div>
    );
  };

  const rowAt = (index: number) => {
    const row = rows[index];
    if (!row) throw new Error(`Transcript row ${index} out of range for ${rows.length} rows`);
    return row;
  };

  const list = (
    <div className={styles.transcriptList} data-testid="transcript-virtual-list">
      <VirtualList
        ref={listRef}
        dynamic
        anchorToEnd
        count={rows.length}
        estimateSize={() => ESTIMATED_TURN_HEIGHT}
        getItemKey={(index) => rowAt(index).id}
        renderRow={(index) => renderRow(rowAt(index), index)}
        onChange={() => {
          try {
            onMeasurementsChange?.();
          } finally {
            viewRegistration.restoreAfterMeasurement();
          }
        }}
      />
    </div>
  );

  const content =
    surface === "preview" ? (
      <div data-testid="transcript-preview-flow">
        {rows.map((row, index) => (
          <div key={row.id}>{renderRow(row, index)}</div>
        ))}
      </div>
    ) : (
      <div className={styles.transcript}>
        {surface === "live" ? (
          <FlowOverlay top={loadOlderRow} pill={liveOverlay}>
            <div className={styles.transcriptContent}>
              {toolbar}
              {list}
              {trailingContent}
            </div>
          </FlowOverlay>
        ) : (
          <>
            {loadOlderRow}
            <div className={styles.transcriptContent}>
              {toolbar}
              {list}
              {trailingContent}
            </div>
          </>
        )}
      </div>
    );

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
