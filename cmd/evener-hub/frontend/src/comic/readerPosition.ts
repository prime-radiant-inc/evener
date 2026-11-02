import type { ComicRow } from "./layout";

export type ComicReadingTarget = { sessionRef: string; path: readonly string[] };
export type ComicReadingPosition = ComicReadingTarget &
  ({ mode: "end" } | { mode: "away"; entryId: string; offset: number; viewportOffset: number });
export type ComicPositionRecord = { ownerRef: string; position: ComicReadingPosition };
const storageKey = (ref: string) => `evener:comic:position:${ref}`;

export function readComicPosition(ref: string): ComicReadingPosition | undefined {
  return readComicPositionRecord(ref)?.position;
}

export function readComicPositionRecord(ref: string): ComicPositionRecord | undefined {
  try {
    const stored = JSON.parse(sessionStorage.getItem(storageKey(ref)) ?? "null");
    const value = stored?.position;
    if (
      stored?.version !== 1 ||
      typeof stored.ownerRef !== "string" ||
      !stored.ownerRef ||
      !value ||
      typeof value.sessionRef !== "string" ||
      !value.sessionRef ||
      !Array.isArray(value.path) ||
      !value.path.every((key: unknown) => typeof key === "string")
    )
      return;
    const target = { sessionRef: value.sessionRef, path: value.path };
    const ownerRef: string = stored.ownerRef;
    if (value.mode === "end") return { ownerRef, position: { ...target, mode: "end" } };
    if (
      value.mode === "away" &&
      typeof value.entryId === "string" &&
      Number.isSafeInteger(value.offset) &&
      value.offset >= 0 &&
      Number.isFinite(value.viewportOffset)
    ) {
      return {
        ownerRef,
        position: {
          ...target,
          mode: "away",
          entryId: value.entryId,
          offset: value.offset,
          viewportOffset: value.viewportOffset,
        },
      };
    }
  } catch {
    /* Storage is optional, including in private browsing. */
  }
}

export function writeComicPosition(ref: string, position: ComicReadingPosition, ownerRef = ref): void {
  const target = { sessionRef: position.sessionRef, path: [...position.path] };
  const anchor =
    position.mode === "end"
      ? { ...target, mode: "end" }
      : {
          ...target,
          mode: "away",
          entryId: position.entryId,
          offset: position.offset,
          viewportOffset: position.viewportOffset,
        };
  try {
    sessionStorage.setItem(storageKey(ref), JSON.stringify({ version: 1, ownerRef, position: anchor }));
  } catch {
    /* Reading never depends on storage quota. */
  }
}

export function findComicAnchorRow(rows: readonly ComicRow[], position: ComicReadingPosition): number | undefined {
  if (position.mode === "end") return rows.length ? rows.length - 1 : undefined;
  let last: number | undefined;
  for (let index = 0; index < rows.length; index += 1) {
    for (const { slice } of rows[index]?.panels ?? []) {
      if (slice.entryId !== position.entryId) continue;
      last = index;
      if (position.offset >= slice.start && position.offset < slice.end) return index;
      if (slice.start === slice.end && position.offset === slice.start) return index;
    }
  }
  // A truncated/deleted tail settles at the remaining end of that source.
  return last;
}
