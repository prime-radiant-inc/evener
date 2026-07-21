// Positional "[image N]" marker helpers (parity-m5-composer.md §G, contracts
// §Attachments), ported from composer-attachments.js's insertAtCursor/
// stripMarker/nextMarker.
//
// Pure string splicing, NOT direct DOM mutation: the composer's textarea is
// a React-controlled input (value={text}), and React tracks controlled
// form elements to detect/undo native value drift - a "change"-family
// native event firing ANYWHERE in the tree (verified: specifically the
// hidden file-picker <input>'s own change event, not a click or a paste
// event) triggers React's controlled-input restoration pass, which
// silently reset a direct `el.value = ...` mutation on this UNRELATED
// textarea back to its last-rendered value in exactly one of this file's
// own integration tests (Composer.test.tsx's file-picker attach case) -
// caught by that test actually failing, not by inspection. The fix is
// structural, not a workaround: every caller now computes the new text +
// cursor position as plain values and applies them through the composer's
// own `text` state (setText), which is the only place React won't fight
// itself over - see Composer.tsx's writeText/cursor-restore effect.
export function markerText(n: number): string {
  return `[image ${n}]`;
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
}

// insertMarker splices `marker` into `value` at [start,end) (replacing any
// selected range), returning the new value and the cursor position just
// after the inserted text - the caller applies both via its own controlled
// state instead of writing a DOM node's `.value` directly.
export function insertMarker(value: string, start: number, end: number, marker: string): TextEdit {
  const nextValue = value.slice(0, start) + marker + value.slice(end);
  return { value: nextValue, cursor: start + marker.length };
}

// stripMarker removes the FIRST literal occurrence of markerText(n) from
// `value` (plain string search, not regex - avoids escaping surprises).
// If `cursor` sat past the deletion point, the returned cursor shifts back
// by the marker's length so it stays anchored to the same character; if it
// sat before the deletion point, it comes back unchanged. Returns the
// ORIGINAL value/cursor untouched (a safe no-op result, not a throw) when
// the marker isn't present or `cursor` is unknown (undefined).
export function stripMarker(value: string, cursor: number | undefined, n: number): TextEditWithUnknownCursor {
  const needle = markerText(n);
  const idx = value.indexOf(needle);
  if (idx < 0) return { value, cursor };
  const nextValue = value.slice(0, idx) + value.slice(idx + needle.length);
  if (typeof cursor !== "number") return { value: nextValue, cursor };
  const nextCursor = cursor > idx ? Math.max(idx, cursor - needle.length) : cursor;
  return { value: nextValue, cursor: nextCursor };
}
