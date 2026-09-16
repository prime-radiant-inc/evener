import { findEntityIds } from "@evener/appwire-client";

/** One atomic run of a string: a plain text run, or a single detected entity
 * id. Segments appear in source order and every character belongs to exactly
 * one of them. This is THE interleave walk the entity surfaces share - the
 * prose component and the tool row both render from it, so both agree on what
 * an id is. */
export type EntityTextSegment = { kind: "text"; text: string } | { kind: "entity"; id: string };

/** Splits a string into its text and entity segments, in order. An id is ONE
 * segment wherever it lands, never split across two: a clipped id with an
 * ellipsis inside it is not even detectable as an id, so no card could attach
 * to it. This mirrors `findEntityIds`' own contract - the ids it detects are
 * exactly the ones that resolve to a card - and callers place their cuts
 * between segments rather than inside one. `protect` names a span the caller
 * must keep whole: any id overlapping it stays part of the surrounding text
 * run instead of becoming an entity segment. */
export function segmentEntityIds(text: string, protect?: EntitySpan): EntityTextSegment[] {
  const segments: EntityTextSegment[] = [];
  let cursor = 0;
  for (const match of findEntityIds(text)) {
    if (protect !== undefined && match.start < protect.end && match.end > protect.start) continue;
    if (match.start > cursor) segments.push({ kind: "text", text: text.slice(cursor, match.start) });
    segments.push({ kind: "entity", id: match.id });
    cursor = match.end;
  }
  if (cursor < text.length) segments.push({ kind: "text", text: text.slice(cursor) });
  return segments;
}

/** A span of a string, in the same UTF-16 indices `findEntityIds` and
 * `String.indexOf` use, that a caller of segmentEntityIds must keep whole. */
export type EntitySpan = { start: number; end: number };
