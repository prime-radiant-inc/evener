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
}

// insertMarker splices `marker` into `value` at [start,end) (replacing any
// selected range), returning the new value and the cursor position just
// after the inserted text.
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
