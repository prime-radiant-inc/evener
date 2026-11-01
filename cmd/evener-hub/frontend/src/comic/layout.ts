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
export type ComicBalloon = {
  slice: ComicSlice;
  composition: ComicComposition;
  document: ComicMarkdownDocument;
  part: ComicMarkdownPart;
};
/** One panel: a party's stacked balloons in source order, or a single balloon. */
export type ComicPanelSpec = { balloons: readonly ComicBalloon[] };
export type ComicRow = {
  id: string;
  /** Grid columns this row lays out; panels keep their own max width as gutter. */
  columns: number;
  unit: "panel" | "insert";
  panels: readonly ComicPanelSpec[];
};

const ROW_GAP = 15;
/** --comic-panel-max-width is 26rem; the px fallback assumes the default root size. */
const PANEL_REM = 26;
const DEFAULT_PANEL_WIDTH = PANEL_REM * 16;
/** Balloons per stacked panel; a longer run continues in the next panel of the same party. */
const STACK_MAX = 8;

/** How many panel-sized columns fit side by side; one column whenever the pane is narrower. */
export function comicPanelColumns(viewportWidth: number, panelMaxWidth = DEFAULT_PANEL_WIDTH): number {
  if (!(panelMaxWidth > 0)) return 1;
  return Math.max(1, Math.floor((viewportWidth + ROW_GAP) / (panelMaxWidth + ROW_GAP)));
}

type Offset = { start: number; end: number };

function prosePart(entry: ComicEntry, document: ComicMarkdownDocument): ComicMarkdownPart | undefined {
  const part = document.parts[0];
  return !entry.streaming &&
    !entry.images?.length &&
    entry.text &&
    document.parts.length === 1 &&
    part?.kind === "prose"
    ? part
    : undefined;
}

/** The party a stacked run belongs to: a persona key, or nothing for unattributed source rows. */
function comicParty(entry: ComicEntry): string | undefined {
  return entry.persona?.key;
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

type Member = { value: ComicEntry; doc: ComicMarkdownDocument };
type Unit =
  | { kind: "stack"; members: readonly Member[] }
  | { kind: "singleton"; member: Member }
  | { kind: "own"; rows: readonly ComicRow[] };

/**
 * A row is the strip boundary: a continuation, source, or thought always starts a new one.
 * Consecutive short same-party messages stack into one panel; different-party singletons
 * pack side by side by width; everything else keeps its own wide, spotlight or insert row.
 */
export function composeComicRows(
  entries: readonly ComicEntry[],
  viewportWidth: number,
  fits: ComicFits,
  panelMaxWidth = DEFAULT_PANEL_WIDTH,
): readonly ComicRow[] {
  const documents = entries.map((entry) => projectComicMarkdown(entry.text, entry.streaming));
  const columns = comicPanelColumns(viewportWidth, panelMaxWidth);
  // The packed grid caps every track at the panel size token, so measurement
  // must use the capped width too or budgets can overshoot what renders.
  const slotWidth = Math.min((viewportWidth - (columns - 1) * ROW_GAP) / columns, panelMaxWidth);
  const stackable = (value: ComicEntry, doc: ComicMarkdownDocument) => {
    const part = prosePart(value, doc);
    return (
      !!part &&
      (value.kind === "dialogue" || value.kind === "caption") &&
      !!comicParty(value) &&
      fits(doc, part, "packed", value.kind, slotWidth)
    );
  };
  const packable = (value: ComicEntry, doc: ComicMarkdownDocument) => {
    const part = prosePart(value, doc);
    return !!part && value.kind === "dialogue" && fits(doc, part, "packed", value.kind, slotWidth);
  };
  // Each entry's compact verdict is asked for at most once, by index.
  const stackMemo = new Map<number, boolean>();
  const packMemo = new Map<number, boolean>();
  const stackableAt = (index: number): boolean => {
    const cached = stackMemo.get(index);
    if (cached !== undefined) return cached;
    const value = entries[index];
    const doc = documents[index];
    const result = !!value && !!doc && stackable(value, doc);
    stackMemo.set(index, result);
    return result;
  };
  const packableAt = (index: number): boolean => {
    const cached = packMemo.get(index);
    if (cached !== undefined) return cached;
    const value = entries[index];
    const doc = documents[index];
    const result = !!value && !!doc && packable(value, doc);
    packMemo.set(index, result);
    return result;
  };

  // Phase 1: classify each entry as stackable run, packed singleton, or its own rows.
  const units: Unit[] = [];
  let previousPack = false;
  let index = 0;
  while (index < entries.length) {
    const current = entries[index];
    const document = documents[index];
    if (!current || !document) {
      index += 1;
      continue;
    }
    const next = entries[index + 1];
    const nextDocument = documents[index + 1];
    const samePartyStack =
      next && nextDocument && stackableAt(index) && stackableAt(index + 1) && comicParty(current) === comicParty(next);
    if (samePartyStack) {
      const members: Member[] = [];
      let scan = index;
      while (scan < entries.length && members.length < STACK_MAX) {
        const value = entries[scan];
        const doc = documents[scan];
        if (!value || !doc || !stackableAt(scan) || comicParty(value) !== comicParty(current)) break;
        members.push({ value, doc });
        scan += 1;
      }
      units.push({ kind: "stack", members });
      previousPack = true;
      index += members.length;
      continue;
    }
    // Packing needs a packable neighbour or predecessor; a lone short entry keeps its
    // spotlight chance through the own path, so no packed fit is even asked of it.
    const neighbourPacks = next && nextDocument ? packableAt(index + 1) : false;
    if (columns > 1 && (previousPack || neighbourPacks) && packableAt(index)) {
      units.push({ kind: "singleton", member: { value: current, doc: document } });
      previousPack = true;
      index += 1;
      continue;
    }
    previousPack = false;
    units.push({ kind: "own", rows: ownRows(current, document, fits) });
    index += 1;
  }

  // Phase 2: lay consecutive pack units into rows of `columns` panels. A unit left
  // alone keeps today's treatment: a stacked run stays one panel, a dialogue singleton
  // falls back to the own path's spotlight-or-wide row.
  const rows: ComicRow[] = [];
  for (let position = 0; position < units.length; position += 1) {
    const unit = units[position];
    if (!unit || unit.kind === "own") {
      if (unit?.kind === "own") rows.push(...unit.rows);
      continue;
    }
    const group: Extract<Unit, { kind: "stack" | "singleton" }>[] = [];
    let scan = position;
    while (scan < units.length && group.length < columns) {
      const candidate = units[scan];
      if (!candidate || candidate.kind === "own") break;
      group.push(candidate);
      scan += 1;
    }
    if (group.length >= 2) {
      rows.push(packRow(group, columns));
      position += group.length - 1;
      continue;
    }
    const solo = group[0];
    if (solo?.kind === "stack") rows.push(packRow([solo], 1));
    else if (solo?.kind === "singleton") rows.push(...ownRows(solo.member.value, solo.member.doc, fits));
  }
  return rows;
}

function packRow(units: readonly Extract<Unit, { kind: "stack" | "singleton" }>[], columns: number): ComicRow {
  const id = units
    .flatMap((unit) => (unit.kind === "stack" ? unit.members : [unit.member]).map((member) => member.value.id))
    .join(":");
  const panels = units.map((unit) => ({
    balloons: (unit.kind === "stack" ? unit.members : [unit.member]).flatMap(({ value, doc }) =>
      doc.parts.map((part) => ({
        slice: { entryId: value.id, start: 0, end: value.text.length },
        composition: "packed" as const,
        document: doc,
        part,
      })),
    ),
  }));
  return { id, columns, unit: "panel", panels };
}

function ownRows(current: ComicEntry, document: ComicMarkdownDocument, fits: ComicFits): readonly ComicRow[] {
  const single = prosePart(current, document);
  const spotlight =
    single &&
    current.kind === "dialogue" &&
    current.text.length <= 80 &&
    fits(document, single, "spotlight", current.kind);
  const composition: ComicComposition = spotlight ? "spotlight" : "wide";
  const rows: ComicRow[] = [];
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
            balloons: [
              {
                slice: { entryId: current.id, start: piece.start, end: piece.end },
                document,
                part: piece,
                composition: piece.kind !== "prose" ? "insert" : piece.start ? "wide" : composition,
              },
            ],
          },
        ],
      });
    }
  }
  return rows;
}
