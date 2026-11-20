// Positional "[image N]" marker helpers for a composer textarea (parity-
// m5-composer.md §G, contracts §Attachments): markerText renders the
// placeholder a staged image is anchored to, insertMarker splices it in at
// the caller's selection, and stripMarker removes it again when the image is
// dropped. Both apps share these so a draft written on one composer
// round-trips through the other.
//
// Everything here is pure string splicing that returns the new text and
// cursor as plain values; nothing writes a DOM node's `.value`. The web
// composer's textarea is a React-controlled input, and React resets a direct
// value write on a controlled element back to its last-rendered value
// whenever a change-family native event fires anywhere in the tree (the
// hidden file-picker <input>'s own change event is enough). Callers apply
// the returned value and cursor through their own text state, which is the
// one place React will not fight them.
export function markerText(n: number): string {
  return `[image ${n}]`;
}

// markerPattern is the ONE matcher for the literal markerText renders: every
// site that scans or rewrites "[image N]" - attachmentMarkers' send-time
// translation, recoveryDraft's renumbering, TurnFailureEndCap's retry
// round-trip - goes through this instead of spelling the format again, so
// changing the placeholder syntax is a one-place edit. The coupling is pinned
// by textareaMarkers.test.ts, which asserts the pattern matches markerText
// output and captures N.
//
// A fresh RegExp per call (not a shared module-level one) avoids the
// lastIndex state a global regex carries between callers.
export function markerPattern(): RegExp {
  return /\[image (\d+)\]/g;
}

export interface TextEdit {
  value: string;
  cursor: number;
}

// Separate from TextEdit (not an override via intersection: `number &
// (number | undefined)` collapses right back to `number`, which is why
// this needs its own named shape rather than `TextEdit & {cursor: ...}`).
export interface TextEditWithUnknownCursor {
  value: string;
  cursor: number | undefined;
  /** The span this edit removed, in the ORIGINAL value's coordinates, when the
   * edit was a marker removal. Callers that must describe their own edit to
   * reconcileMarkerRanges read it here rather than searching the text again,
   * so the coordinates cannot drift from the deletion they describe. */
  splice?: TextSplice;
}

// TextSplice is one exact edit, in the coordinates of the text it replaced:
// the UTF-16 span [start, end) became `insertedLength` units of new text (zero
// for a deletion). A caller that performed the edit itself knows these; a
// before/after pair only INFERS them, and the inference is wrong whenever the
// two texts share a prefix or suffix that is not the edit's own boundary - an
// insertion immediately before "[image 1]" shares "[image " with the marker it
// did not touch.
export interface TextSplice {
  start: number;
  end: number;
  insertedLength: number;
}

// One exact occurrence of markerText(n) in a text: which number it names and
// the UTF-16 range [start, end) the literal occupies. Two spellings of the
// same number are two distinct ranges, so the occurrence a writer STAGED can
// be named apart from an identical literal beside it - deleting the reference
// removes the one that has no bytes behind it and leaves the prose alone. A
// bare number cannot say that, which is why every provenance record carries
// ranges when it can.
export interface ImageMarkerRange {
  marker: number;
  /** Inclusive UTF-16 offset of the literal's first character. */
  start: number;
  /** Exclusive UTF-16 offset one past its last character. */
  end: number;
}

// insertMarker splices `marker` into `value` at [start,end) (replacing any
// selected range), returning the new value and the cursor position just
// after the inserted text.
export function insertMarker(value: string, start: number, end: number, marker: string): TextEdit {
  const nextValue = value.slice(0, start) + marker + value.slice(end);
  return { value: nextValue, cursor: start + marker.length };
}

// The span stripMarker removes: the FIRST literal occurrence of markerText(n),
// or undefined when the value spells none. Callers that must describe their
// edit to reconcileMarkerRanges use this rather than searching again, so the
// coordinates can never drift from the deletion they describe.
export function markerRemovalSplice(value: string, n: number): TextSplice | undefined {
  const needle = markerText(n);
  const start = value.indexOf(needle);
  return start < 0 ? undefined : { start, end: start + needle.length, insertedLength: 0 };
}

// stripMarker removes the FIRST literal occurrence of markerText(n) from
// `value` (plain string search, not regex - avoids escaping surprises).
// If `cursor` sat past the deletion point, the returned cursor shifts back
// by the marker's length so it stays anchored to the same character; if it
// sat before the deletion point, it comes back unchanged. Returns the
// ORIGINAL value/cursor untouched (a safe no-op result, not a throw) when
// the marker isn't present or `cursor` is unknown (undefined).
export function stripMarker(value: string, cursor: number | undefined, n: number): TextEditWithUnknownCursor {
  const splice = markerRemovalSplice(value, n);
  if (splice === undefined) return { value, cursor };
  const nextValue = value.slice(0, splice.start) + value.slice(splice.end);
  if (typeof cursor !== "number") return { value: nextValue, cursor, splice };
  const removed = splice.end - splice.start;
  const nextCursor = cursor > splice.start ? Math.max(splice.start, cursor - removed) : cursor;
  return { value: nextValue, cursor: nextCursor, splice };
}

// stripMarkerAt removes the exact occurrence `range` names when `value` still
// spells markerText(range.marker) at those offsets, leaving identical literals
// elsewhere in `value` untouched.
export function stripMarkerAt(
  value: string,
  cursor: number | undefined,
  range: ImageMarkerRange,
): TextEditWithUnknownCursor {
  if (value.slice(range.start, range.end) !== markerText(range.marker)) return { value, cursor };
  const splice: TextSplice = { start: range.start, end: range.end, insertedLength: 0 };
  const nextValue = value.slice(0, splice.start) + value.slice(splice.end);
  if (typeof cursor !== "number") return { value: nextValue, cursor, splice };
  const removed = splice.end - splice.start;
  const nextCursor = cursor > splice.start ? Math.max(splice.start, cursor - removed) : cursor;
  return { value: nextValue, cursor: nextCursor, splice };
}

/** Every occurrence of a marker literal in `text`, in document order. */
export function markerRanges(text: string): ImageMarkerRange[] {
  return Array.from(text.matchAll(markerPattern()), (match) => {
    const offset = match.index ?? 0;
    return { marker: Number(match[1]), start: offset, end: offset + match[0].length };
  });
}

/**
 * Keeps only the ranges that are still true of `text`: each must sit inside
 * the text, and the slice it names must be exactly markerText(marker). A
 * stored range can outlive the text it described - a hand-edited record, a
 * record written by an older build, a merge that moved the prose - and a
 * range that no longer matches is worse than none: a repair would delete
 * whatever now occupies those offsets. Sorted and deduplicated by position,
 * because a range's identity IS its position.
 */
export function validMarkerRanges(text: string, ranges: readonly ImageMarkerRange[] | undefined): ImageMarkerRange[] {
  if (ranges === undefined || ranges.length === 0) return [];
  const seen = new Set<string>();
  const kept: ImageMarkerRange[] = [];
  for (const range of ranges) {
    if (typeof range !== "object" || range === null) continue;
    if (!Number.isSafeInteger(range.marker) || range.marker <= 0) continue;
    if (!Number.isSafeInteger(range.start) || !Number.isSafeInteger(range.end)) continue;
    if (range.start < 0 || range.end < range.start || range.end > text.length) continue;
    if (text.slice(range.start, range.end) !== markerText(range.marker)) continue;
    const key = `${range.marker}:${range.start}:${range.end}`;
    if (seen.has(key)) continue;
    seen.add(key);
    kept.push({ marker: range.marker, start: range.start, end: range.end });
  }
  return kept.sort((left, right) => left.start - right.start || left.marker - right.marker);
}

/**
 * The ranges an edit moves or retires, given the same before/after pair and
 * shared prefix/suffix every plain-text splice is described by. A range
 * entirely inside the replaced span is retired; one entirely in the
 * surviving prefix or suffix is kept (shifted when it followed the span); a
 * range the splice cuts through is retired too, because half a literal is
 * not an occurrence and the editor's own mark is gone just the same.
 *
 * `splice`, when the caller knows the edit it performed, replaces the
 * prefix/suffix inference outright. The inferred boundary can fall INSIDE an
 * untouched literal: inserting "[image 2]" at the caret before "[image 1]"
 * yields a shared prefix of "[image " (7), which cuts through the marker, so
 * the inference retires a range the edit never touched. The real splice there
 * is {start: 0, end: 0, insertedLength: 9}, which leaves the marker wholly in
 * the surviving suffix and shifts it past the inserted text.
 */
export function reconcileMarkerRanges(
  before: string,
  after: string,
  ranges: readonly ImageMarkerRange[] | undefined,
  prefix: number,
  suffix: number,
  splice?: TextSplice,
): ImageMarkerRange[] {
  const valid = validMarkerRanges(before, ranges);
  if (valid.length === 0) return [];
  const edit = splice ?? { start: prefix, end: before.length - suffix, insertedLength: after.length - prefix - suffix };
  const shift = edit.insertedLength - (edit.end - edit.start);
  const kept: ImageMarkerRange[] = [];
  for (const range of valid) {
    if (range.end <= edit.start) kept.push(range);
    else if (range.start >= edit.end) kept.push({ ...range, start: range.start + shift, end: range.end + shift });
  }
  return validMarkerRanges(after, kept);
}

/**
 * The ranges of `text` the adoption record names, given either explicit
 * ranges or the legacy bare marker list. A text with two spellings of one
 * number is exactly the case a bare list cannot answer, so a legacy list
 * marks ALL of that number's occurrences - the conservative reading, and the
 * one the pre-range builds implemented. An explicit range list wins over the
 * numbers whenever it carries any, so a record that names one of two
 * identical spellings is respected.
 */
export function adoptedMarkerRanges(
  text: string,
  adopted: readonly number[] | undefined,
  ranges: readonly ImageMarkerRange[] | undefined,
): ImageMarkerRange[] {
  const exact = validMarkerRanges(text, ranges);
  // A marker the ranges do not mention is one this record says was staged
  // without saying where - a tile's own number, or a record written before
  // ranges existed. Those occurrences are ALL adopted, exactly as a pre-range
  // reader took them; dropping them here would let a mixed record send a
  // genuinely missing reference.
  const located = new Set(exact.map((range) => range.marker));
  const unlocated = new Set((adopted ?? []).filter((marker) => !located.has(marker)));
  const legacy = unlocated.size > 0 ? markerRanges(text).filter((range) => unlocated.has(range.marker)) : [];
  if (legacy.length === 0) return exact;
  return [...exact, ...legacy].sort((left, right) => left.start - right.start || left.marker - right.marker);
}

/** The distinct marker numbers a range list names, ascending. */
export function markerNumbersOf(ranges: readonly ImageMarkerRange[]): number[] {
  return [...new Set(ranges.map((range) => range.marker))].sort((left, right) => left - right);
}
