import type { ComicEntry } from "./projection";

export type ComicSlice = { entryId: string; start: number; end: number };
export type ComicComposition = "wide" | "paired" | "spotlight";
export type ComicRow = { id: string; panels: readonly { slice: ComicSlice; composition: ComicComposition }[] };

type Offset = { start: number; end: number };

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
  fits: (text: string, composition: ComicComposition, kind: ComicEntry["kind"]) => boolean,
): readonly ComicRow[] {
  const rows: ComicRow[] = [];
  for (let index = 0; index < entries.length; index += 1) {
    const current = entries[index];
    if (!current) continue;
    const next = entries[index + 1];
    const pairable = (value: ComicEntry) =>
      value.kind === "dialogue" && !value.streaming && !!value.text && fits(value.text, "paired", value.kind);
    if (viewportWidth > 760 && next && pairable(current) && pairable(next)) {
      rows.push({
        id: `${current.id}:${next.id}`,
        panels: [current, next].map((value) => ({
          slice: { entryId: value.id, start: 0, end: value.text.length },
          composition: "paired" as const,
        })),
      });
      index += 1;
      continue;
    }
    const spotlight =
      current.kind === "dialogue" &&
      !current.streaming &&
      current.text.length > 0 &&
      current.text.length <= 80 &&
      fits(current.text, "spotlight", current.kind);
    const composition: ComicComposition = spotlight ? "spotlight" : "wide";
    const slices = splitComicText(current.text, (text) => fits(text, composition, current.kind));
    for (const slice of slices) {
      rows.push({
        id: `${current.id}:${slice.start}`,
        panels: [{ slice: { entryId: current.id, ...slice }, composition: slice.start ? "wide" : composition }],
      });
    }
  }
  return rows;
}
