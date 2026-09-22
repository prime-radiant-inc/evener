// Pure text-display helpers shared by real queue rows and optimistic pending
// rows (pendingReconcile.ts / QueueStrip.tsx) now live in the package's
// mutation core next to inputPreview, which builds the same label for a
// durable or authoritative pending entry - the label is a MATCHING KEY
// between a queued daemon row and its optimistic pending row, not just
// cosmetics.
export {
  imagePlaceholder,
  normalizeText,
  pendingEntryPreview,
  queueEntryPreviewText,
  skillMarkers,
  truncateForDisplay,
} from "@evener/appwire-client/state/mutation";
