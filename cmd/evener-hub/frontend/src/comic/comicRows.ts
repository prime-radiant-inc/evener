import type { UseTranscriptResult } from "../panes/session/transcript/useTranscript";
import type { ComicDelegateStrip } from "./delegates";
import { type ComicRow, composeComicRows } from "./layout";
import type { createComicMeasurer } from "./measure";
import type { ComicEntry } from "./projection";
import type { ComicReadingPosition } from "./readerPosition";
import type { useComicActivity } from "./useComicActivity";
import type { ComicReaderSnapshot, ComicSubchatState } from "./useComicSubchats";
export type ComicFlowRow = { id: string; sessionRef: string; path: readonly string[] } & (
  | { kind: "panels"; row: ComicRow }
  | { kind: "strip"; strip: ComicDelegateStrip }
  | { kind: "unplaced-group" }
  | { kind: "turn-errors-group" }
  | { kind: "history"; history: UseTranscriptResult }
  | { kind: "activity"; activity: ReturnType<typeof useComicActivity> }
);
export function composeComicFlow(
  root: ComicReaderSnapshot,
  subchats: ComicSubchatState,
  viewportWidth: number,
  fits: ReturnType<typeof createComicMeasurer>["fits"],
): readonly ComicFlowRow[] {
  const rows: ComicFlowRow[] = [];
  const append = (reader: ComicReaderSnapshot, ancestors: readonly string[]) => {
    const base = { sessionRef: reader.ref, path: reader.path };
    const id = (part: string) => JSON.stringify([reader.ref, reader.path, part]);
    const panels = (entries: readonly ComicEntry[]) => {
      for (const row of composeComicRows(entries, viewportWidth, fits))
        rows.push({ ...base, id: id(row.id), kind: "panels", row });
    };
    const stripRow = (strip: ComicDelegateStrip) => {
      rows.push({ ...base, id: id(strip.key), kind: "strip", strip });
      const path = [...reader.path, strip.key];
      const child = subchats.readers.get(JSON.stringify(path));
      if (
        child &&
        child.ref === strip.childRef &&
        !ancestors.includes(child.ref) &&
        subchats.expandedPaths.some((p) => JSON.stringify(p) === JSON.stringify(path))
      )
        append(child, [...ancestors, child.ref]);
    };
    rows.push({ ...base, id: id("history"), kind: "history", history: reader.history });
    const items = reader.entries.filter((entry) => entry.sourceIndex !== undefined);
    let cursor = 0;
    for (const { strip, source } of reader.activity.strips
      .flatMap((strip) => (strip.source ? [{ strip, source: strip.source }] : []))
      .sort((a, b) => a.source.sourceIndex - b.source.sourceIndex)) {
      const start = cursor;
      while (cursor < items.length && (items[cursor]?.sourceIndex ?? Infinity) <= source.sourceIndex) cursor += 1;
      panels(items.slice(start, cursor));
      stripRow(strip);
    }
    panels(items.slice(cursor));
    const errors = reader.entries.filter((entry) => entry.sourceTurnId !== undefined);
    if (errors.length) {
      rows.push({ ...base, id: id("turn-errors"), kind: "turn-errors-group" });
      panels(errors);
    }
    const unplaced = reader.activity.strips.filter((strip) => !strip.source);
    if (unplaced.length) {
      rows.push({ ...base, id: id("unplaced"), kind: "unplaced-group" });
      for (const strip of unplaced) stripRow(strip);
    }
    rows.push({ ...base, id: id("activity"), kind: "activity", activity: reader.activity });
  };
  append(root, [root.ref]);
  return rows;
}
export function findComicFlowAnchorRow(
  rows: readonly ComicFlowRow[],
  position: ComicReadingPosition,
): number | undefined {
  let last: number | undefined;
  for (const [index, flow] of rows.entries()) {
    if (flow.sessionRef !== position.sessionRef || JSON.stringify(flow.path) !== JSON.stringify(position.path))
      continue;
    if (position.mode === "end") {
      if (flow.kind === "panels" || last === undefined) last = index;
      continue;
    }
    if (flow.kind === "strip" && position.entryId === flow.strip.key) return index;
    if (flow.kind !== "panels") continue;
    for (const { slice } of flow.row.panels) {
      if (slice.entryId !== position.entryId) continue;
      last = index;
      if (position.offset >= slice.start && (position.offset < slice.end || slice.start === slice.end)) return index;
    }
  }
  return last;
}
