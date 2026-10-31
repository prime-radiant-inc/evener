import { type ComicMarkdownDocument, type ComicMarkdownPart, projectComicMarkdown } from "./markdown";
import type { ComicEntry } from "./projection";

export type ComicSlice = { entryId: string; start: number; end: number };
export type ComicComposition = "wide" | "packed" | "spotlight" | "insert";
export type ComicFits = (
  document: ComicMarkdownDocument,
  part: ComicMarkdownPart,
  composition: ComicComposition,
  kind: ComicEntry["kind"],
  slotWidth?: number,
) => boolean;
export type ComicRow = {
  id: string;
  /** Grid columns this row lays out; panels keep their own max width as gutter. */
  columns: number;
  unit: "panel" | "insert";
  panels: readonly {
    slice: ComicSlice;
    composition: ComicComposition;
    document: ComicMarkdownDocument;
    part: ComicMarkdownPart;
  }[];
};

const ROW_GAP = 15;
/** --comic-panel-max-width is 26rem; the px fallback assumes the default root size. */
const PANEL_REM = 26;
const DEFAULT_PANEL_WIDTH = PANEL_REM * 16;

/** How many panel-sized columns fit side by side; one column whenever the pane is narrower. */
export function comicPanelColumns(viewportWidth: number, panelMaxWidth = DEFAULT_PANEL_WIDTH): number {
  if (!(panelMaxWidth > 0)) return 1;
  return Math.max(1, Math.floor((viewportWidth + ROW_GAP) / (panelMaxWidth + ROW_GAP)));
}

type Offset = { start: number; end: number };

function singleProseDialogue(entry: ComicEntry, document: ComicMarkdownDocument): ComicMarkdownPart | undefined {
  const part = document.parts[0];
  return entry.kind === "dialogue" &&
    !entry.streaming &&
    !entry.images?.length &&
    entry.text &&
    document.parts.length === 1 &&
    part?.kind === "prose"
    ? part
    : undefined;
}

/** Preserve each source code unit. Intl boundaries prevent slicing inside a visible character. */
export function splitComicText(text: string, fits: (candidate: string) => boolean): readonly Offset[] {
  if (!text) return [{ start: 0, end: 0 }];
  const parts: Offset[] = [];
  let start = 0;
  let end = 0;
  const levels = ["sentence", "word", "grapheme"] as const;

  function append(token: Offset, level: number): void {
    if (end > start && !fits(text.slice(start, token.end))) {
      parts.push({ start, end });
      start = end;
    }
    if (!fits(text.slice(start, token.end)) && level < levels.length - 1) {
      const segmenter = new Intl.Segmenter(undefined, { granularity: levels[level + 1] });
      for (const segment of segmenter.segment(text.slice(token.start, token.end))) {
        append(
          { start: token.start + segment.index, end: token.start + segment.index + segment.segment.length },
          level + 1,
        );
      }
    } else {
      end = token.end; // A single oversized grapheme is deliberately never split or clipped.
    }
  }

  for (const segment of new Intl.Segmenter(undefined, { granularity: "sentence" }).segment(text)) {
    append({ start: segment.index, end: segment.index + segment.segment.length }, 0);
  }
  parts.push({ start, end });
  return parts;
}

/** A row is the strip boundary: a continuation, source, or thought always starts a new one. */
export function composeComicRows(
  entries: readonly ComicEntry[],
  viewportWidth: number,
  fits: ComicFits,
  panelMaxWidth = DEFAULT_PANEL_WIDTH,
): readonly ComicRow[] {
  const rows: ComicRow[] = [];
  const documents = entries.map((entry) => projectComicMarkdown(entry.text, entry.streaming));
  const columns = comicPanelColumns(viewportWidth, panelMaxWidth);
  const slotWidth = (viewportWidth - (columns - 1) * ROW_GAP) / columns;
  for (let index = 0; index < entries.length; index += 1) {
    const current = entries[index];
    const document = documents[index];
    if (!current || !document) continue;
    const packable = (value: ComicEntry, doc: ComicMarkdownDocument) => {
      const part = singleProseDialogue(value, doc);
      return !!part && fits(doc, part, "packed", value.kind, slotWidth);
    };
    // Packing needs a neighbour; a lone short entry keeps its spotlight chance.
    if (columns > 1 && index + 1 < entries.length) {
      const group: { value: ComicEntry; doc: ComicMarkdownDocument }[] = [];
      let scan = index;
      while (scan < entries.length) {
        const value = entries[scan];
        const doc = documents[scan];
        if (!value || !doc || !packable(value, doc)) break;
        group.push({ value, doc });
        scan += 1;
      }
      if (group.length >= 2) {
        const full = Math.floor(group.length / columns) * columns;
        for (let start = 0; start < full; start += columns) {
          const members = group.slice(start, start + columns);
          rows.push({
            id: members.map((member) => member.value.id).join(":"),
            columns,
            unit: "panel",
            panels: members.flatMap(({ value, doc }) =>
              doc.parts.map((part) => ({
                slice: { entryId: value.id, start: 0, end: value.text.length },
                composition: "packed" as const,
                document: doc,
                part,
              })),
            ),
          });
        }
        const remainder = group.slice(full);
        if (remainder.length >= 2) {
          rows.push({
            id: remainder.map((member) => member.value.id).join(":"),
            columns,
            unit: "panel",
            panels: remainder.flatMap(({ value, doc }) =>
              doc.parts.map((part) => ({
                slice: { entryId: value.id, start: 0, end: value.text.length },
                composition: "packed" as const,
                document: doc,
                part,
              })),
            ),
          });
        }
        index += full;
        // A lone trailing member keeps its spotlight chance through the single path.
        if (remainder.length === 1) {
          index -= 1; // the loop's increment lands on the lone member
          continue;
        }
        index += remainder.length - 1; // the loop's increment steps past the group
        continue;
      }
    }
    const single = singleProseDialogue(current, document);
    const spotlight = single && current.text.length <= 80 && fits(document, single, "spotlight", current.kind);
    const composition: ComicComposition = spotlight ? "spotlight" : "wide";
    for (const part of document.parts) {
      const candidate = (start: number, end: number): ComicMarkdownPart => ({
        ...part,
        start,
        end,
        input: current.text.slice(start, end),
        visible: [
          { text: current.text.slice(start, end), offsets: Array.from({ length: end - start }, (_, i) => start + i) },
        ],
      });
      let parts = part.plain
        ? splitComicText(part.input, (text) => fits(document, { ...part, input: text }, composition, current.kind)).map(
            (offset) => candidate(part.start + offset.start, part.start + offset.end),
          )
        : [part];
      if (!part.plain && part.kind === "prose" && part.blocks?.length) {
        parts = [];
        let pending: ComicMarkdownPart | undefined;
        const withReferences = (input: string) => (document.references ? `${document.references}\n\n${input}` : input);
        let input = "";
        for (const block of part.blocks) {
          const joined = input ? `${input}\n\n${block.input}` : block.input;
          const combined = {
            ...part,
            start: pending?.start ?? block.start,
            end: block.end,
            input: withReferences(joined),
            visible: [...(pending?.visible ?? []), ...block.visible],
            blocks: undefined,
          };
          if (pending && !fits(document, combined, composition, current.kind)) {
            parts.push(pending);
            pending = undefined;
          }
          input = pending ? joined : block.input;
          pending = pending ? combined : { ...part, ...block, input: withReferences(input), blocks: undefined };
        }
        if (pending) parts.push(pending);
      }
      for (const piece of parts) {
        rows.push({
          id: `${current.id}:${piece.start}`,
          columns: 1,
          unit: part.kind !== "prose" ? "insert" : "panel",
          panels: [
            {
              slice: { entryId: current.id, start: piece.start, end: piece.end },
              document,
              part: piece,
              composition: piece.kind !== "prose" ? "insert" : piece.start ? "wide" : composition,
            },
          ],
        });
      }
    }
  }
  return rows;
}
