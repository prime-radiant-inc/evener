import type { UseTranscriptResult } from "../panes/session/transcript/useTranscript";
import { type ComicDelegateStrip, comicStripForUpdate } from "./delegates";
import { type ComicRow, composeComicRows } from "./layout";
import type { createComicMeasurer } from "./measure";
import type { ComicEntry } from "./projection";
import type { ComicReadingPosition } from "./readerPosition";
import type { useComicActivity } from "./useComicActivity";
import type { ComicChildAvailability, ComicReaderSnapshot, ComicSubchatState } from "./useComicSubchats";
export type ComicFlowRow = { id: string; sessionRef: string; path: readonly string[] } & (
  | { kind: "panels"; row: ComicRow }
  | { kind: "strip"; strip: ComicDelegateStrip; updateCount: number; foldedEntryIds: readonly string[] }
  | { kind: "turn-errors-group" }
  | { kind: "history"; history: UseTranscriptResult }
  | { kind: "activity"; activity: ReturnType<typeof useComicActivity> }
  | { kind: "availability"; availability: ComicChildAvailability }
);
export function composeComicFlow(
  root: ComicReaderSnapshot,
  subchats: ComicSubchatState,
  viewportWidth: number,
  fits: ReturnType<typeof createComicMeasurer>["fits"],
  panelMaxWidth?: number,
): readonly ComicFlowRow[] {
  const rows: ComicFlowRow[] = [];
  const append = (reader: ComicReaderSnapshot, ancestors: readonly string[]) => {
    const base = { sessionRef: reader.ref, path: reader.path };
    const id = (part: string) => JSON.stringify([reader.ref, reader.path, part]);
    const panels = (entries: readonly ComicEntry[]) => {
      for (const row of composeComicRows(entries, viewportWidth, fits, panelMaxWidth))
        rows.push({ ...base, id: id(row.id), kind: "panels", row });
    };
    const stripRow = (strip: ComicDelegateStrip, updateCount: number, foldedEntryIds: readonly string[]) => {
      rows.push({ ...base, id: id(strip.key), kind: "strip", strip, updateCount, foldedEntryIds });
      const path = [...reader.path, strip.key];
      if (!subchats.expandedPaths.some((p) => JSON.stringify(p) === JSON.stringify(path))) return;
      const availability = subchats.availability?.get(JSON.stringify(path));
      if (availability && availability !== "ready")
        rows.push({
          sessionRef: strip.childRef,
          path,
          id: JSON.stringify([strip.childRef, path, "availability"]),
          kind: "availability",
          availability,
        });
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
    // A delegate's mid-work updates fold into its strip instead of rendering as
    // captions, so a strip whose launch is outside the loaded window still
    // anchors inline at the earliest update it kept.
    const strips = reader.activity.strips;
    // Anchors compare by source index, then by projected order: a delegate_send's
    // earlier updates and its final reply can share one source index.
    const folded = new Map<string, { first: number; order: number; entryIds: string[] }>();
    const items: ComicEntry[] = [];
    const itemOrders: number[] = [];
    for (const [order, entry] of reader.entries.entries()) {
      if (entry.sourceIndex === undefined) continue;
      const strip = entry.delegateUpdate ? comicStripForUpdate(strips, entry.delegateUpdate) : undefined;
      if (strip) {
        const record = folded.get(strip.key) ?? { first: entry.sourceIndex, order, entryIds: [] };
        record.entryIds.push(entry.id);
        if (record.first > entry.sourceIndex) {
          record.first = entry.sourceIndex;
          record.order = order;
        }
        folded.set(strip.key, record);
        continue;
      }
      items.push(entry);
      itemOrders.push(order);
    }
    let cursor = 0;
    for (const { strip, updateCount, anchor, entryIds } of strips
      .flatMap((strip) => {
        const record = folded.get(strip.key);
        // A loaded launch keeps the legacy placement: same-index items stay before it.
        const anchor = strip.source
          ? { index: strip.source.sourceIndex, order: Infinity }
          : record
            ? { index: record.first, order: record.order }
            : undefined;
        return anchor === undefined
          ? []
          : {
              strip,
              updateCount: record?.entryIds.length ?? 0,
              anchor,
              entryIds: record?.entryIds ?? [],
            };
      })
      .sort((a, b) => a.anchor.index - b.anchor.index || a.anchor.order - b.anchor.order)) {
      const start = cursor;
      const itemPrecedesAnchor = (index: number) =>
        (items[index]?.sourceIndex ?? Infinity) < anchor.index ||
        ((items[index]?.sourceIndex ?? Infinity) === anchor.index && (itemOrders[index] ?? -1) < anchor.order);
      while (cursor < items.length && itemPrecedesAnchor(cursor)) cursor += 1;
      panels(items.slice(start, cursor));
      stripRow(strip, updateCount, entryIds);
    }
    panels(items.slice(cursor));
    const errors = reader.entries.filter((entry) => entry.sourceTurnId !== undefined);
    if (errors.length) {
      rows.push({ ...base, id: id("turn-errors"), kind: "turn-errors-group" });
      panels(errors);
    }
    rows.push({ ...base, id: id("activity"), kind: "activity", activity: reader.activity });
  };
  append(root, [root.ref]);
  return rows;
}

/** True when the strip's launch or one of its folded updates is in the reader's loaded window. */
export function comicStripAnchored(reader: ComicReaderSnapshot, strip: ComicDelegateStrip): boolean {
  if (strip.source) return true;
  return reader.entries.some(
    (entry) =>
      entry.delegateUpdate !== undefined &&
      comicStripForUpdate(reader.activity.strips, entry.delegateUpdate)?.key === strip.key,
  );
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
    if (
      flow.kind === "strip" &&
      (position.entryId === flow.strip.key || flow.foldedEntryIds.includes(position.entryId))
    )
      return index;
    if (flow.kind !== "panels") continue;
    for (const { slice } of flow.row.panels.flatMap((panel) => panel.balloons)) {
      if (slice.entryId !== position.entryId) continue;
      last = index;
      if (position.offset >= slice.start && (position.offset < slice.end || slice.start === slice.end)) return index;
    }
  }
  return last;
}
