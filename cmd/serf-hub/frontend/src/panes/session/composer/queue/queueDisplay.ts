// Pure text-display helpers shared by real queue rows and optimistic pending
// rows (pendingReconcile.ts / QueueStrip.tsx). Ported verbatim from the
// legacy optimistic-pending registry (cmd/serf-hub/assets/pending.js:14-41)
// since these exact strings are also a MATCHING key, not just a display
// choice - see pendingReconcile.ts's own header comment for why fidelity
// here matters beyond cosmetics.

// Collapses any whitespace run to a single space and trims both ends.
export function normalizeText(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

// The synthetic label an image-only entry displays (and the string this
// module trusts the daemon's own queue preview to also produce for an
// image-only queued entry - see pendingReconcile.ts).
export function imagePlaceholder(count: number): string {
  if (count === 1) return "[image]";
  if (count > 1) return `[${count} images]`;
  return "";
}

// queueEntryPreviewText is the one label computation used for BOTH a real
// queue row's fallback text and a pending entry's display/matching text:
// normalized text wins whenever non-blank; only a blank (or whitespace-only)
// text falls back to the image placeholder.
export function queueEntryPreviewText(text: string, imageCount: number): string {
  const normalized = normalizeText(text);
  return normalized || imagePlaceholder(imageCount);
}

const DEFAULT_MAX_DISPLAY_LENGTH = 140;

// The client-side visual cap layered on top of the daemon's own first-line
// truncation (parity-m5-composer.md §B) - independent of and smaller than
// most real messages, so this mostly matters for a single very long line.
export function truncateForDisplay(text: string, max: number = DEFAULT_MAX_DISPLAY_LENGTH): string {
  if (text.length <= max) return text;
  return `${text.slice(0, max)}…`;
}
