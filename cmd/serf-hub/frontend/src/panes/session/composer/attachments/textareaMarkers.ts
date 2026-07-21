// Positional "[image N]" marker helpers (parity-m5-composer.md §G, contracts
// §Attachments), ported from composer-attachments.js's insertAtCursor/
// stripMarker/nextMarker. Pure DOM manipulation - no React, no store - so
// the ingestion hook can call these directly against the real textarea
// node it holds a ref to.

export function markerText(n: number): string {
  return `[image ${n}]`;
}

// insertAtCursor splices `text` into el.value at the current selection
// (replacing any selected range), then moves the cursor to just after the
// inserted text.
export function insertAtCursor(el: HTMLTextAreaElement, text: string): void {
  const value = el.value;
  const start = typeof el.selectionStart === "number" ? el.selectionStart : value.length;
  const end = typeof el.selectionEnd === "number" ? el.selectionEnd : start;
  el.value = value.slice(0, start) + text + value.slice(end);
  const pos = start + text.length;
  el.selectionStart = pos;
  el.selectionEnd = pos;
}

// stripMarker removes the FIRST literal occurrence of markerText(n) from
// el.value (plain string search, not regex - avoids escaping surprises).
// If the cursor sat past the deletion point, shifts it back by the
// marker's length so it stays anchored to the same character; if it sat
// before the deletion point, it is explicitly restored unchanged. A null
// element (no textarea currently wired) is a safe no-op, never a throw -
// the caller (useAttachments) can always call this unconditionally.
//
// Reads selectionStart BEFORE reassigning el.value, not after: setting a
// text control's `.value` moves its cursor to the end of the new value
// (HTMLInputElement/HTMLTextAreaElement's own value-setter behavior,
// reproduced by jsdom) - reading selectionStart afterward would see that
// reset position, not the cursor's actual position at the time of the
// strip. The legacy composer-attachments.js's own stripMarker reads it
// AFTER the reassignment (the same bug), but its test suite never asserts
// a post-strip cursor position, so this was never caught - fixed here
// rather than ported, per this codebase's "fix it when you find it" rule.
export function stripMarker(el: HTMLTextAreaElement | null, n: number): void {
  if (!el) return;
  const needle = markerText(n);
  const value = el.value;
  const idx = value.indexOf(needle);
  if (idx < 0) return;
  const cursor = el.selectionStart;
  el.value = value.slice(0, idx) + value.slice(idx + needle.length);
  if (typeof cursor === "number") {
    const restored = cursor > idx ? Math.max(idx, cursor - needle.length) : cursor;
    el.selectionStart = restored;
    el.selectionEnd = restored;
  }
}
